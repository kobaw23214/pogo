package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

type fakeClient struct {
	calls      int
	run        func(context.Context, int, []llm.Message) (llm.Response, error)
	t          *testing.T
	wantSystem string
}

func (f *fakeClient) Model() string { return "fixture-model" }
func (f *fakeClient) Stream(ctx context.Context, system string, history []llm.Message, schemas []llm.ToolSchema, delta func(llm.Delta)) (llm.Response, error) {
	f.calls++
	if len(schemas) != 1 || schemas[0].Name != "browser" {
		f.t.Errorf("unexpected tools: %+v", schemas)
	}
	wantSystem := f.wantSystem
	if wantSystem == "" {
		wantSystem = systemPrompt
	}
	if system != wantSystem {
		f.t.Error("not using the declared browser-only system prompt")
	}
	if delta != nil {
		delta(llm.Delta{Kind: llm.DeltaThinking, Text: "HIDDEN_REASONING"})
	}
	return f.run(ctx, f.calls, history)
}
func toolResponse(action string, u llm.Usage) llm.Response {
	return llm.Response{StopReason: llm.StopToolUse, Usage: u, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, Name: "browser", ID: "call-1", Input: json.RawMessage(`{"action":"` + action + `"}`)}}}}
}
func answerResponse(text string, u llm.Usage) llm.Response {
	return llm.Response{StopReason: llm.StopEndTurn, Usage: u, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockThinking, Text: "HIDDEN_REASONING"}, {Type: llm.BlockText, Text: text}}}}
}

type workerFixture struct {
	server           *httptest.Server
	mu               sync.Mutex
	creates, cancels int
	actions          []map[string]any
	execute          func(http.ResponseWriter, *http.Request)
	cancelStatus     int
	t                *testing.T
}

func newWorkerFixture(t *testing.T) *workerFixture {
	f := &workerFixture{t: t}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer worker-secret" {
			t.Error("worker credential was not passed only in Authorization")
		}
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/sessions/eval-case":
			f.mu.Lock()
			f.creates++
			f.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		case r.URL.Path == "/sessions/eval-case/cancel":
			f.mu.Lock()
			f.cancels++
			status := f.cancelStatus
			f.mu.Unlock()
			if status != 0 {
				w.WriteHeader(status)
			}
			json.NewEncoder(w).Encode(map[string]bool{"ok": status == 0})
		case r.URL.Path == "/sessions/eval-case/execute":
			var op map[string]any
			if err := json.NewDecoder(r.Body).Decode(&op); err != nil {
				t.Error(err)
			}
			f.mu.Lock()
			f.actions = append(f.actions, op)
			f.mu.Unlock()
			if f.execute != nil {
				f.execute(w, r)
			} else {
				json.NewEncoder(w).Encode(tools.BrowserReply{OK: true, Backend: "native", Text: "[e1] button Continue", URL: "https://fixture.invalid/start"})
			}
		default:
			t.Errorf("unexpected worker request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}
func (f *workerFixture) counts() (int, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.creates, len(f.actions), f.cancels
}
func defaultInput(worker string) input {
	return input{Prompt: "Read the fixture and answer.", StartURL: "https://fixture.invalid/start", WorkerURL: worker, SessionID: "eval-case", Model: "fixture-model"}
}
func runFixture(t *testing.T, request input, client llm.Client, cfg config.Resolved) (int, []map[string]any, string) {
	t.Helper()
	t.Setenv("POGO_BROWSER_EVAL_TOKEN", "worker-secret")
	raw, _ := json.Marshal(request)
	var stdout, stderr bytes.Buffer
	code := runCLI(context.Background(), nil, bytes.NewReader(raw), &stdout, &stderr, func(model, providers string) (llm.Client, config.Resolved, error) { return client, cfg, nil })
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
	events := decodeEvents(t, stdout.String())
	return code, events, stdout.String()
}
func decodeEvents(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("stdout is not JSONL: %v", err)
		}
		out = append(out, e)
	}
	return out
}
func assertEnd(t *testing.T, events []map[string]any, reason string) map[string]any {
	t.Helper()
	end := events[len(events)-1]
	if end["type"] != "end" || end["end_reason"] != reason {
		t.Fatalf("end=%+v, want %s", end, reason)
	}
	return end
}

func TestRealAgentBrowserOnlyProtocolAndUsage(t *testing.T) {
	worker := newWorkerFixture(t)
	client := &fakeClient{t: t, run: func(_ context.Context, n int, _ []llm.Message) (llm.Response, error) {
		if n == 1 {
			return toolResponse("snapshot", llm.Usage{Input: 20, Output: 5, CacheRead: 4, Reasoning: 2}), nil
		}
		return answerResponse("Done api-secret worker-secret https://private-provider.invalid", llm.Usage{Input: 30, Output: 3}), nil
	}}
	code, events, raw := runFixture(t, defaultInput(worker.server.URL), client, config.Resolved{Model: "fixture-model", Provider: "fixture-provider", APIKey: "api-secret", BaseURL: "https://private-provider.invalid"})
	if code != 0 {
		t.Fatal(raw)
	}
	end := assertEnd(t, events, "completed")
	if end["usage_measured"] != true || end["cost"] != nil || end["profile"] != profile || end["provider"] != "fixture-provider" || len(end["system_prompt_sha256"].(string)) != 64 {
		t.Fatalf("metadata: %+v", end)
	}
	u := end["usage"].(map[string]any)
	if u["input"] != float64(50) || u["output"] != float64(8) || u["cache_read"] != float64(4) || u["reasoning"] != float64(2) {
		t.Fatalf("usage: %+v", u)
	}
	for _, secret := range []string{"HIDDEN_REASONING", "api-secret", "worker-secret", "https://private-provider.invalid"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("sensitive value leaked: %s", secret)
		}
	}
	creates, actions, cancels := worker.counts()
	if creates != 1 || actions != 1 || cancels != 0 {
		t.Fatalf("worker requests: %d %d %d", creates, actions, cancels)
	}
	if worker.actions[0]["action"] != "snapshot" || len(worker.actions[0]["op_id"].(string)) != 32 {
		t.Fatal("start URL was navigated again or unique op_id missing")
	}
	var start, endTool bool
	for _, event := range events {
		switch event["type"] {
		case "tool_start":
			start = true
			if event["args"].(map[string]any)["action"] != "snapshot" {
				t.Fatal(event)
			}
		case "tool_end":
			endTool = true
			if event["ok"] != true || event["duration_ms"] == nil {
				t.Fatal(event)
			}
		}
	}
	if !start || !endTool {
		t.Fatal("missing tool lifecycle")
	}
}

func TestScreenshotResolverFeedsVisionOnlyAndNeverLogsBytes(t *testing.T) {
	var jpegBytes bytes.Buffer
	if err := jpeg.Encode(&jpegBytes, image.NewRGBA(image.Rect(0, 0, 32, 16)), nil); err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(jpegBytes.Bytes())
	for _, vision := range []bool{true, false} {
		t.Run(map[bool]string{true: "vision", false: "text"}[vision], func(t *testing.T) {
			worker := newWorkerFixture(t)
			worker.execute = func(w http.ResponseWriter, _ *http.Request) {
				json.NewEncoder(w).Encode(tools.BrowserReply{OK: true, Backend: "native", Version: "v1", Screenshot: &tools.BrowserScreenshot{Data: encoded, Mime: "image/jpeg", Width: 32, Height: 16}})
			}
			observed := false
			client := &fakeClient{t: t, run: func(_ context.Context, n int, history []llm.Message) (llm.Response, error) {
				if n == 1 {
					return toolResponse("screenshot", llm.Usage{Input: 10, Output: 5}), nil
				}
				for _, message := range history {
					for _, block := range message.Content {
						for _, im := range block.Images {
							observed = true
							if im.ImageRef == "" || im.ImageMime != "image/jpeg" {
								t.Error("image reference metadata lost")
							}
							if vision && im.DataURL != "data:image/jpeg;base64,"+encoded {
								t.Error("vision model did not receive real JPEG")
							}
							if !vision && im.DataURL != "" {
								t.Error("text model received image data")
							}
						}
					}
				}
				return answerResponse("Observed", llm.Usage{Input: 10, Output: 2}), nil
			}}
			code, events, raw := runFixture(t, defaultInput(worker.server.URL), client, config.Resolved{Model: "fixture-model", SeesImages: vision})
			if code != 0 || observed != vision || strings.Contains(raw, encoded) || strings.Contains(raw, "data:image/") {
				t.Fatal("image path failed or bytes leaked")
			}
			_, actions, _ := worker.counts()
			if vision && actions != 1 || !vision && actions != 0 {
				t.Fatal("screenshot capability was not enforced before worker execution")
			}
			if !vision {
				found := false
				for _, event := range events {
					if event["type"] == "tool_end" {
						found = true
						if event["ok"] != false {
							t.Fatal("text-only screenshot did not fail")
						}
					}
				}
				if !found {
					t.Fatal("missing rejected screenshot result")
				}
			}
			if assertEnd(t, events, "completed")["sees_images"] != vision {
				t.Fatal("vision metadata incorrect")
			}
		})
	}
}

func TestUnknownBrowserOutcomeIsNotReplayedAndCancellationIsAcknowledged(t *testing.T) {
	worker := newWorkerFixture(t)
	worker.execute = func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	}
	client := &fakeClient{t: t, run: func(context.Context, int, []llm.Message) (llm.Response, error) {
		return toolResponse("snapshot", llm.Usage{Input: 10, Output: 5}), nil
	}}
	code, events, raw := runFixture(t, defaultInput(worker.server.URL), client, config.Resolved{Model: "fixture-model"})
	if code != 1 {
		t.Fatal(raw)
	}
	assertEnd(t, events, "browser_error")
	_, actions, cancels := worker.counts()
	if actions != 1 || cancels != 1 || client.calls != 1 {
		t.Fatalf("replayed uncertain work: actions=%d cancels=%d model=%d", actions, cancels, client.calls)
	}
}

func TestKnownPageFailureRemainsVisibleWithoutTransportRetry(t *testing.T) {
	worker := newWorkerFixture(t)
	worker.execute = func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(tools.BrowserReply{OK: false, Error: "target missing"})
	}
	client := &fakeClient{t: t, run: func(_ context.Context, n int, _ []llm.Message) (llm.Response, error) {
		if n == 1 {
			return toolResponse("snapshot", llm.Usage{Input: 10, Output: 5}), nil
		}
		return answerResponse("Unable to locate target", llm.Usage{Input: 10, Output: 2}), nil
	}}
	_, events, _ := runFixture(t, defaultInput(worker.server.URL), client, config.Resolved{Model: "fixture-model"})
	assertEnd(t, events, "completed")
	found := false
	for _, e := range events {
		if e["type"] == "tool_end" {
			found = true
			if e["ok"] != false || e["error"] == nil {
				t.Fatal(e)
			}
		}
	}
	if !found {
		t.Fatal("page failure event missing")
	}
}

func TestBudgetsAndModelFailure(t *testing.T) {
	for _, kind := range []string{"token_budget", "cost_budget", "turn_limit", "time_budget", "transport_error"} {
		t.Run(kind, func(t *testing.T) {
			worker := newWorkerFixture(t)
			request := defaultInput(worker.server.URL)
			cfg := config.Resolved{Model: "fixture-model"}
			switch kind {
			case "token_budget":
				request.TokenBudget = 15
			case "cost_budget":
				request.CostBudget = 1
				cfg.Price = &llm.Price{Input: 1000000}
			case "turn_limit":
				request.MaxTurns = 1
			case "time_budget":
				request.TimeBudgetMS = 40
			}
			client := &fakeClient{t: t, run: func(ctx context.Context, _ int, _ []llm.Message) (llm.Response, error) {
				if kind == "time_budget" {
					<-ctx.Done()
					return llm.Response{}, ctx.Err()
				}
				if kind == "transport_error" {
					return llm.Response{}, errors.New("api-secret at https://secret-endpoint.invalid")
				}
				return toolResponse("snapshot", llm.Usage{Input: 10, Output: 5, CacheRead: 1, Reasoning: 2}), nil
			}}
			code, events, raw := runFixture(t, request, client, cfg)
			if code != 1 {
				t.Fatal(raw)
			}
			end := assertEnd(t, events, kind)
			if strings.Contains(raw, "secret-endpoint") || strings.Contains(raw, "api-secret") {
				t.Fatal("raw provider error leaked")
			}
			_, actions, cancels := worker.counts()
			expectedActions := 0
			if kind == "turn_limit" {
				expectedActions = 1
			}
			if actions != expectedActions || cancels != 1 || client.calls != 1 {
				t.Fatalf("bad budget stop: actions=%d cancel=%d model=%d", actions, cancels, client.calls)
			}
			if end["usage_measured"] != (kind != "time_budget" && kind != "transport_error") {
				t.Fatalf("usage presence incorrect: %+v", end)
			}
			if kind == "cost_budget" && (end["cost"] != float64(9) || end["cost_unit"] != "configured") {
				t.Fatal("cache accounting or cost unit incorrect", end)
			}
		})
	}
}

func TestInputConfigAndUnknownPriceFailBeforeBrowser(t *testing.T) {
	worker := newWorkerFixture(t)
	for _, kind := range []string{"model", "turns", "tokens", "time", "worker_credentials", "unknown_field", "cost_without_price", "missing_token", "load_error"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("POGO_BROWSER_EVAL_TOKEN", "worker-secret")
			request := defaultInput(worker.server.URL)
			switch kind {
			case "model":
				request.Model = ""
			case "turns":
				request.MaxTurns = 201
			case "tokens":
				request.TokenBudget = -1
			case "time":
				request.TimeBudgetMS = 600001
			case "worker_credentials":
				request.WorkerURL = "http://user:secret@localhost"
			case "cost_without_price":
				request.CostBudget = 1
			case "missing_token":
				t.Setenv("POGO_BROWSER_EVAL_TOKEN", "")
			}
			raw, _ := json.Marshal(request)
			if kind == "unknown_field" {
				raw = []byte(`{"unknown":true}`)
			}
			var out, diag bytes.Buffer
			code := runCLI(context.Background(), nil, bytes.NewReader(raw), &out, &diag, func(string, string) (llm.Client, config.Resolved, error) {
				if kind == "load_error" {
					return nil, config.Resolved{}, errors.New("SECRET PROVIDER DETAILS")
				}
				return nil, config.Resolved{Model: "fixture-model"}, nil
			})
			if code != 2 {
				t.Fatalf("accepted invalid request: %d %s", code, out.String())
			}
			events := decodeEvents(t, out.String())
			end := events[len(events)-1]
			if end["usage_measured"] != false || end["cost"] != nil || strings.Contains(out.String(), "SECRET") || diag.Len() != 0 {
				t.Fatal("invalid failure accounting or diagnostic disclosure")
			}
		})
	}
	creates, actions, cancels := worker.counts()
	if creates+actions+cancels != 0 {
		t.Fatal("invalid config contacted browser")
	}
}

func TestExplicitFlagModelAndProviderPath(t *testing.T) {
	worker := newWorkerFixture(t)
	t.Setenv("POGO_BROWSER_EVAL_TOKEN", "worker-secret")
	raw, _ := json.Marshal(defaultInput(worker.server.URL))
	var out bytes.Buffer
	client := &fakeClient{t: t, run: func(context.Context, int, []llm.Message) (llm.Response, error) {
		return answerResponse("done", llm.Usage{}), nil
	}}
	code := runCLI(context.Background(), []string{"--model", "chosen-model", "--providers", "/fixture/providers.json"}, bytes.NewReader(raw), &out, &bytes.Buffer{}, func(model, providers string) (llm.Client, config.Resolved, error) {
		if model != "chosen-model" || providers != "/fixture/providers.json" {
			t.Fatal("explicit CLI selection lost")
		}
		return client, config.Resolved{Model: model, Price: &llm.Price{}}, nil
	})
	if code != 0 {
		t.Fatal(out.String())
	}
	end := assertEnd(t, decodeEvents(t, out.String()), "completed")
	if end["usage_measured"] != false || end["cost"] != nil {
		t.Fatal("missing provider usage was invented")
	}
}
