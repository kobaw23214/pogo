package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

// Exercise the real agent registry and history conversion, rather than a second
// evaluator-only formatter. Full recorded headers must still be retrievable.
func TestBrowserUseV2ModelHistoryUsesCompactListAndFullRead(t *testing.T) {
	selected, err := selectProfile("browser-use-v2")
	if err != nil {
		t.Fatal(err)
	}
	worker := newWorkerFixture(t)
	t.Setenv("POGO_BROWSER_EVAL_TOKEN", "worker-secret")
	const requestID = "recorded-request-19"
	const headerValue = "retained-full-header-detail"
	row := json.RawMessage(`{"request_id":"recorded-request-19","cdp_request_id":"9.1","target_ref":"dt_example_2","target_type":"worker","url":"https://fixture.invalid/data","method":"GET","status":503,"headers":{"x-context":"retained-full-header-detail"},"response":{"status":503,"headers":{"x-debug":"retained-full-header-detail"}},"body":{"state":"unavailable","reason":"loading_failed"},"future_field":9007199254740993}`)
	worker.execute = func(w http.ResponseWriter, _ *http.Request) {
		worker.mu.Lock()
		method := worker.actions[len(worker.actions)-1]["method"]
		worker.mu.Unlock()
		var data json.RawMessage
		switch method {
		case "network.list":
			data = append(json.RawMessage(`{"paused":false,"cursor":19,"next_cursor":null,"cursor_lost":true,"evicted_before":8,"items":[`), row...)
			data = append(data, []byte(`]}`)...)
		case "network.read":
			data = append(json.RawMessage(`{"paused":false,"request":`), row...)
			data = append(data, '}')
		default:
			t.Errorf("unexpected method: %v", method)
		}
		_ = json.NewEncoder(w).Encode(tools.BrowserReply{OK: true, Backend: "native", URL: "https://fixture.invalid/start", Data: data})
	}
	client := &fakeClient{t: t, wantSystem: selected.prompt, run: func(_ context.Context, turn int, history []llm.Message) (llm.Response, error) {
		latest := ""
		for _, message := range history {
			for _, block := range message.Content {
				if block.Type == llm.BlockToolResult {
					latest = block.Text
				}
			}
		}
		if turn > 1 {
			for _, fact := range []string{requestID, "503", "loading_failed", "9007199254740993"} {
				if !strings.Contains(latest, fact) {
					t.Errorf("model lost observed fact %q: %s", fact, latest)
				}
			}
		}
		if turn == 2 {
			if strings.Contains(latest, headerValue) || !strings.Contains(latest, "network.read") || !strings.Contains(latest, `"cursor_lost":true`) {
				t.Errorf("list view lost detail retrieval or completeness, or leaked raw details into history: %s", latest)
			}
		}
		if turn == 3 {
			if !strings.Contains(latest, headerValue) {
				t.Error("network.read did not retain original recorded header detail")
			}
			return answerResponse("Observed 503; the response body is unavailable and earlier records were evicted.", llm.Usage{Input: 10, Output: 5}), nil
		}
		method := "network.list"
		params := map[string]any{}
		if turn == 2 {
			method = "network.read"
			params["request_id"] = requestID
		}
		raw, _ := json.Marshal(map[string]any{"action": "devtools", "method": method, "params": params})
		return llm.Response{StopReason: llm.StopToolUse, Usage: llm.Usage{Input: 10, Output: 5}, Message: llm.Message{
			Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, Name: "browser", ID: method, Input: raw}},
		}}, nil
	}}
	raw, _ := json.Marshal(defaultInput(worker.server.URL))
	var out, diag bytes.Buffer
	code := runCLI(context.Background(), []string{"--profile", "browser-use-v2"}, bytes.NewReader(raw), &out, &diag,
		func(string, string) (llm.Client, config.Resolved, error) {
			return client, config.Resolved{Model: "fixture-model"}, nil
		})
	if code != 0 || diag.Len() != 0 {
		t.Fatalf("compact browser run failed: %d %s %s", code, out.String(), diag.String())
	}
	assertEnd(t, decodeEvents(t, out.String()), "completed")
	_, calls, cancels := worker.counts()
	if calls != 2 || cancels != 0 {
		t.Fatalf("projection unexpectedly dispatched browser work: calls=%d cancels=%d", calls, cancels)
	}
}
