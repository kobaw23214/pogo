package web

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skills"
)

// The MCP tests are full round trips through the SDK's own client transports —
// the only honest way to test a protocol layer, since the failure modes that
// matter (a query token dropped by the legacy handshake, a 401 on the POST-back
// endpoint, a schema that does not round-trip) live in the wire behavior, not
// in the handlers in isolation.

// bearerRT stamps the credential on every request — what a remote agent's
// HTTP client does once, at the transport.
type bearerRT struct{ token string }

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// newMCPServer builds a second server around the standard harness's manager,
// with the MCP endpoints on. The harness's own server stays MCP-less, so the
// rest of the suite cannot see this surface at all.
func newMCPServer(t *testing.T, respond func(int) (llm.Response, error), mcpToken string) *httptest.Server {
	t.Helper()
	return newMCPServerOpts(t, respond, &MCPOptions{Token: mcpToken})
}

// newMCPServerOpts is newMCPServer with the full options surface, for tests
// that exercise the demand caps or the audit log.
func newMCPServerOpts(t *testing.T, respond func(int) (llm.Response, error), mcpOpts *MCPOptions) *httptest.Server {
	t.Helper()
	h := newHarness(t, respond)
	srv, err := NewServer(h.mgr, ServerOptions{Token: h.token, MCP: mcpOpts})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func connectStreamable(t *testing.T, url, token string) *mcp.ClientSession {
	t.Helper()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "mcp-test"}, nil).Connect(context.Background(),
		&mcp.StreamableClientTransport{
			Endpoint:   url + "/mcp",
			HTTPClient: &http.Client{Transport: bearerRT{token}},
		}, nil)
	if err != nil {
		t.Fatalf("connect streamable: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

func structured(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool error: %s", resultText(res))
	}
	m, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("no structured content: %#v", res.StructuredContent)
	}
	return m
}

func resultText(res *mcp.CallToolResult) string {
	if len(res.Content) == 0 {
		return ""
	}
	if tc, ok := res.Content[0].(*mcp.TextContent); ok {
		return tc.Text
	}
	return ""
}

// pollStatus calls agent_task_status until the task leaves "working". The cap
// is generous because the scripted model answers instantly — any wait at all
// is scheduling, not work.
func pollStatus(t *testing.T, cs *mcp.ClientSession, taskID string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		sc := structured(t, callTool(t, cs, "agent_task_status", map[string]any{"task_id": taskID}))
		if sc["status"] != "working" {
			return sc
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("task did not finish within the deadline")
	return nil
}

func submitTask(t *testing.T, cs *mcp.ClientSession, args map[string]any) string {
	t.Helper()
	sc := structured(t, callTool(t, cs, "agent_submit_task", args))
	id, _ := sc["task_id"].(string)
	if id == "" {
		t.Fatalf("submit returned no task_id: %#v", sc)
	}
	return id
}

func TestMCPSubmitPollResult(t *testing.T) {
	ts := newMCPServer(t, func(int) (llm.Response, error) { return textTurn("all done"), nil }, "")
	cs := connectStreamable(t, ts.URL, "test-token")

	// The agent-level tools are on the list; the harness has no skills, so the
	// synth passthrough contributes nothing — its coverage is the manifest
	// tests' job.
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range res.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"agent_submit_task", "agent_task_status", "agent_task_result", "agent_task_cancel"} {
		if !names[want] {
			t.Errorf("tools/list missing %s (has %v)", want, names)
		}
	}
	// Every agent-level tool declares an outputSchema — a programmatic caller
	// should not have to reverse-engineer the structured content.
	for _, tool := range res.Tools {
		if strings.HasPrefix(tool.Name, "agent_") && tool.OutputSchema == nil {
			t.Errorf("%s has no outputSchema", tool.Name)
		}
	}

	id := submitTask(t, cs, map[string]any{"prompt": "do the thing"})
	sc := pollStatus(t, cs, id)
	if sc["status"] != "completed" || sc["end_reason"] != "completed" || sc["disposition"] != "done" {
		t.Fatalf("status = %#v, want completed/done", sc)
	}

	out := callTool(t, cs, "agent_task_result", map[string]any{"task_id": id})
	if out.IsError {
		t.Fatalf("result: %s", resultText(out))
	}
	if text := resultText(out); text != "all done" {
		t.Fatalf("result text = %q, want the scripted answer", text)
	}
	rsc := structured(t, out)
	if rsc["status"] != "completed" {
		t.Fatalf("result structured = %#v", rsc)
	}
}

func TestMCPFollowUpContinuesSession(t *testing.T) {
	turn := 0
	ts := newMCPServer(t, func(int) (llm.Response, error) {
		turn++
		return textTurn("answer " + string(rune('0'+turn))), nil
	}, "")
	cs := connectStreamable(t, ts.URL, "test-token")

	first := submitTask(t, cs, map[string]any{"prompt": "first"})
	pollStatus(t, cs, first)

	second := submitTask(t, cs, map[string]any{"prompt": "second", "session_id": first})
	if second != first {
		t.Fatalf("follow-up created a new session %s instead of continuing %s", second, first)
	}
	pollStatus(t, cs, second)
	out := callTool(t, cs, "agent_task_result", map[string]any{"task_id": second})
	if text := resultText(out); text != "answer 2" {
		t.Fatalf("follow-up text = %q, want answer 2 (session context kept)", text)
	}
}

func TestMCPUnknownTaskAndIdleCancel(t *testing.T) {
	ts := newMCPServer(t, func(int) (llm.Response, error) { return textTurn("done"), nil }, "")
	cs := connectStreamable(t, ts.URL, "test-token")

	res := callTool(t, cs, "agent_task_status", map[string]any{"task_id": "doesnotexist"})
	if !res.IsError {
		t.Fatal("unknown task should be an execution error, not a success")
	}

	id := submitTask(t, cs, map[string]any{"prompt": "quick"})
	pollStatus(t, cs, id)
	// Idempotent by contract: no run in flight, so false, not an error.
	sc := structured(t, callTool(t, cs, "agent_task_cancel", map[string]any{"task_id": id}))
	if sc["cancelled"] != false {
		t.Fatalf("idle cancel = %#v, want cancelled:false", sc)
	}
}

func TestMCPSeparateToken(t *testing.T) {
	ts := newMCPServer(t, func(int) (llm.Response, error) { return textTurn("ok"), nil }, "mcp-secret")

	// The web token must not open the MCP door — that is the point of a
	// separate credential.
	if _, err := mcp.NewClient(&mcp.Implementation{Name: "mcp-test"}, nil).Connect(context.Background(),
		&mcp.StreamableClientTransport{
			Endpoint:   ts.URL + "/mcp",
			HTTPClient: &http.Client{Transport: bearerRT{"test-token"}},
		}, nil); err == nil {
		t.Fatal("web token admitted to /mcp")
	}
	cs := connectStreamable(t, ts.URL, "mcp-secret")
	submitTask(t, cs, map[string]any{"prompt": "hi"})
}

func TestMCPNoTokenRejected(t *testing.T) {
	ts := newMCPServer(t, func(int) (llm.Response, error) { return textTurn("ok"), nil }, "")
	if _, err := mcp.NewClient(&mcp.Implementation{Name: "mcp-test"}, nil).Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil); err == nil {
		t.Fatal("unauthenticated connect admitted to /mcp")
	}
}

// TestMCPQueryTokenStreamable pins the interop detail that makes ?token= work
// on /mcp at all: the streamable client POSTs to the endpoint URL it was
// given, query included. The legacy /sse transport cannot do this (its
// POST-back endpoint is rebuilt without the query) — that is why /sse's
// contract is header-only and documented as such in mcp.go.
func TestMCPQueryTokenStreamable(t *testing.T) {
	ts := newMCPServer(t, func(int) (llm.Response, error) { return textTurn("ok"), nil }, "")
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "mcp-test"}, nil).Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp?token=test-token"}, nil)
	if err != nil {
		t.Fatalf("connect with query token: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	submitTask(t, cs, map[string]any{"prompt": "hi"})
}

// TestMCPLegacySSE exercises the deprecated 2024-11-05 transport end to end —
// the mode "the other side speaks SSE" may well mean. Bearer header only: the
// handshake's POST-back URL drops the query (see mcp.go's header comment).
func TestMCPLegacySSE(t *testing.T) {
	ts := newMCPServer(t, func(int) (llm.Response, error) { return textTurn("legacy ok"), nil }, "")
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "mcp-test"}, nil).Connect(context.Background(),
		&mcp.SSEClientTransport{
			Endpoint:   ts.URL + "/sse",
			HTTPClient: &http.Client{Transport: bearerRT{"test-token"}},
		}, nil)
	if err != nil {
		t.Fatalf("connect legacy sse: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	if _, err := cs.ListTools(context.Background(), nil); err != nil {
		t.Fatalf("legacy tools/list: %v", err)
	}
	id := submitTask(t, cs, map[string]any{"prompt": "over the old wire"})
	sc := pollStatus(t, cs, id)
	if sc["status"] != "completed" {
		t.Fatalf("legacy status = %#v", sc)
	}
	out := callTool(t, cs, "agent_task_result", map[string]any{"task_id": id})
	if text := resultText(out); text != "legacy ok" {
		t.Fatalf("legacy result = %q", text)
	}
}

// TestMCPSubmitWhileRunActive covers the one-conversation-one-run rule as the
// MCP caller meets it: a second submit to a busy session is a readable
// execution error, not a dropped task or a 409 they cannot see.
func TestMCPSubmitWhileRunActive(t *testing.T) {
	unblock := make(chan struct{})
	ts := newMCPServer(t, func(int) (llm.Response, error) {
		<-unblock
		return textTurn("slowly done"), nil
	}, "")
	cs := connectStreamable(t, ts.URL, "test-token")

	id := submitTask(t, cs, map[string]any{"prompt": "slow one"})
	res := callTool(t, cs, "agent_submit_task", map[string]any{"prompt": "impatient", "session_id": id})
	if !res.IsError {
		t.Fatal("second submit to a busy session should be an execution error")
	}
	close(unblock)
	pollStatus(t, cs, id)
}

// --- Phase 2: wait, steer, recovery, reload ---------------------------------

func TestMCPWaitReturnsResult(t *testing.T) {
	ts := newMCPServer(t, func(int) (llm.Response, error) { return textTurn("waited for this"), nil }, "")
	cs := connectStreamable(t, ts.URL, "test-token")
	id := submitTask(t, cs, map[string]any{"prompt": "anything"})

	res := callTool(t, cs, "agent_task_wait", map[string]any{"task_id": id, "timeout_seconds": 30})
	if res.IsError {
		t.Fatalf("wait: %s", resultText(res))
	}
	if text := resultText(res); text != "waited for this" {
		t.Fatalf("wait text = %q", text)
	}
	sc := structured(t, res)
	if sc["status"] != "completed" {
		t.Fatalf("wait status = %#v", sc)
	}
}

func TestMCPWaitStreamsProgress(t *testing.T) {
	// The model holds its reply until the wait is on the wire: a task that
	// finishes first takes the wait's early-terminal path, where there is
	// nothing to stream — the test would pass vacuously. The harness is built
	// by hand (not newMCPServer) so the hub's subscriber count can say exactly
	// when the wait has subscribed — a sleep here is a flake, not a pause.
	unblock := make(chan struct{})
	h := newHarness(t, func(int) (llm.Response, error) {
		<-unblock
		return textTurn("the answer arrives after some typing"), nil
	})
	srv, err := NewServer(h.mgr, ServerOptions{Token: h.token, MCP: &MCPOptions{}})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	var mu sync.Mutex
	var progress []string
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "mcp-test"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, r *mcp.ProgressNotificationClientRequest) {
			mu.Lock()
			progress = append(progress, r.Params.Message)
			mu.Unlock()
		},
	}).Connect(context.Background(),
		&mcp.StreamableClientTransport{
			Endpoint:   ts.URL + "/mcp",
			HTTPClient: &http.Client{Transport: bearerRT{"test-token"}},
		}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	id := submitTask(t, cs, map[string]any{"prompt": "take your time"})
	// The token is the caller's to set — nothing auto-generates one — so this
	// call carries it explicitly and the wait's notifications have somewhere
	// to go.
	params := &mcp.CallToolParams{Name: "agent_task_wait", Arguments: map[string]any{"task_id": id, "timeout_seconds": 30}}
	params.SetProgressToken("wait-1")
	waitErr := make(chan error, 1)
	go func() {
		_, err := cs.CallTool(context.Background(), params)
		waitErr <- err
	}()

	// The run's own watcher holds one hub subscription for the run's lifetime,
	// so a second subscriber is the wait's progress stream, settled and ready.
	sess, err := h.mgr.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for sess.Hub().Subscribers() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the wait never subscribed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(unblock)
	select {
	case err := <-waitErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("wait did not return after the run ended")
	}

	// The server's send is ordered before the result, but the SDK client
	// dispatches notification handlers off its read loop — give the frames that
	// arrived with the result a moment to land in the handler.
	deadline2 := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(progress)
		mu.Unlock()
		if n > 0 {
			return
		}
		if time.Now().After(deadline2) {
			t.Fatal("no progress notifications received for a wait that carried a progressToken")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestMCPWaitTimesOutWithoutKillingTask(t *testing.T) {
	unblock := make(chan struct{})
	ts := newMCPServer(t, func(int) (llm.Response, error) {
		<-unblock
		return textTurn("late"), nil
	}, "")
	cs := connectStreamable(t, ts.URL, "test-token")

	id := submitTask(t, cs, map[string]any{"prompt": "slow"})
	sc := structured(t, callTool(t, cs, "agent_task_wait", map[string]any{"task_id": id, "timeout_seconds": 1}))
	if sc["timed_out"] != true || sc["status"] != "working" {
		t.Fatalf("timed-out wait = %#v, want timed_out+working", sc)
	}

	// The wait ending changed nothing about the run: it finishes on its own.
	close(unblock)
	sc = pollStatus(t, cs, id)
	if sc["status"] != "completed" {
		t.Fatalf("status after unblock = %#v", sc)
	}
}

func TestMCPSteerRunningTask(t *testing.T) {
	// Turn one asks for bash, which standard policy gates — the run stays in
	// flight, which is the state steering exists for.
	ts := newMCPServer(t, func(int) (llm.Response, error) {
		return toolTurn("bash", `{"command":"echo hi"}`), nil
	}, "")

	cs := connectStreamable(t, ts.URL, "test-token")
	id := submitTask(t, cs, map[string]any{"prompt": "run something"})

	// Wait until the run is actually in flight; submitting and the first model
	// call race otherwise, and a steer that lands before the run starts is
	// correctly refused. The gated bash call also pins the input_required
	// state: a human's pending approval is its own status, not silent working.
	deadline := time.Now().Add(10 * time.Second)
	busy := false
	for time.Now().Before(deadline) {
		res := callTool(t, cs, "agent_task_status", map[string]any{"task_id": id})
		if res.IsError {
			t.Fatalf("status: %s", resultText(res))
		}
		if sc := structured(t, res); sc["status"] == "input_required" {
			if sc["awaiting_approval"] != float64(1) {
				t.Fatalf("input_required without the pending gate: %#v", sc)
			}
			busy = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !busy {
		t.Fatal("run never reached the gated (input_required) state")
	}

	sc := structured(t, callTool(t, cs, "agent_task_steer", map[string]any{"task_id": id, "text": "actually, just stop after this"}))
	if sc["steered"] != true {
		t.Fatalf("steer = %#v", sc)
	}
	callTool(t, cs, "agent_task_cancel", map[string]any{"task_id": id})
}

func TestMCPRestartRecoversTasks(t *testing.T) {
	t.Setenv("KIMI_API_KEY", "test-key")
	installTestCatalog(t)
	cwd, sdir := t.TempDir(), t.TempDir()
	newMgr := func() *Manager {
		mgr, err := NewManager(Config{
			Cwd: cwd, SessionDir: sdir, Model: "k3", MaxTurns: 5,
			GateTimeout: 5 * time.Second, RunTimeout: time.Minute, IdleTimeout: time.Hour,
			NewClient: func(c config.Resolved, _ func(llm.RetryInfo)) llm.Client {
				return &scriptedClient{model: c.Model, respond: func() (llm.Response, error) {
					return textTurn("pre-restart answer"), nil
				}}
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return mgr
	}

	mgr1 := newMgr()
	t.Cleanup(mgr1.Close)
	srv1, err := NewServer(mgr1, ServerOptions{Token: "test-token", MCP: &MCPOptions{}})
	if err != nil {
		t.Fatal(err)
	}
	ts1 := httptest.NewServer(srv1.Handler())
	cs1 := connectStreamable(t, ts1.URL, "test-token")
	id := submitTask(t, cs1, map[string]any{"prompt": "before restart"})
	pollStatus(t, cs1, id)
	// Client before server: the streamable transport holds a standalone GET
	// stream open, and httptest.Server.Close waits on outstanding requests —
	// closing it first would wait on a stream only the client can end.
	cs1.Close()
	ts1.Close()
	mgr1.Close()

	// A fresh process over the same session dir: no registry, all transcript.
	mgr2 := newMgr()
	t.Cleanup(mgr2.Close)
	srv2, err := NewServer(mgr2, ServerOptions{Token: "test-token", MCP: &MCPOptions{}})
	if err != nil {
		t.Fatal(err)
	}
	ts2 := httptest.NewServer(srv2.Handler())
	t.Cleanup(ts2.Close)
	cs2 := connectStreamable(t, ts2.URL, "test-token")

	sc := structured(t, callTool(t, cs2, "agent_task_status", map[string]any{"task_id": id}))
	if sc["recovered"] != true || sc["status"] != "completed" {
		t.Fatalf("recovered status = %#v", sc)
	}
	res := callTool(t, cs2, "agent_task_result", map[string]any{"task_id": id})
	if text := resultText(res); text != "pre-restart answer" {
		t.Fatalf("recovered result = %q", text)
	}
}

func TestMCPResultTruncatesHugeAnswers(t *testing.T) {
	big := strings.Repeat("x", 200*1024)
	ts := newMCPServer(t, func(int) (llm.Response, error) { return textTurn(big), nil }, "")
	cs := connectStreamable(t, ts.URL, "test-token")
	id := submitTask(t, cs, map[string]any{"prompt": "write a lot"})
	pollStatus(t, cs, id)

	res := callTool(t, cs, "agent_task_result", map[string]any{"task_id": id})
	sc := structured(t, res)
	if sc["truncated"] != true {
		t.Fatalf("expected truncated:true, got %#v", sc)
	}
	if text := resultText(res); len(text) >= len(big) {
		t.Fatalf("truncated text is %d bytes, not smaller than the %d-byte original", len(text), len(big))
	}
	if note, _ := sc["note"].(string); !strings.Contains(note, "transcript") {
		t.Fatalf("truncation note should point at the transcript: %q", note)
	}
}

func TestMCPSynthToolsHotReload(t *testing.T) {
	skillDir := t.TempDir()
	toolsJSON := `{"tools":[{"name":"t.echo","title":"Echo","description":"test passthrough",` +
		`"inputSchema":{"type":"object","properties":{},"additionalProperties":false},` +
		`"annotations":{"readOnlyHint":true},"x-pogo-exec":{"argv":["echo","hi"]}}]}`
	if err := os.WriteFile(filepath.Join(skillDir, "tools.json"), []byte(toolsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	sk := skills.Skill{Name: "t", Description: "test", Path: filepath.Join(skillDir, "SKILL.md"), Dir: skillDir}

	h := newHarness(t, func(int) (llm.Response, error) { return textTurn("ok"), nil },
		func(c *Config) { c.Skills = []skills.Skill{sk} })
	srv, err := NewServer(h.mgr, ServerOptions{Token: h.token, MCP: &MCPOptions{}})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	cs := connectStreamable(t, ts.URL, "test-token")

	hasEcho := func() bool {
		res, err := cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, tool := range res.Tools {
			if tool.Name == "t.echo" {
				return true
			}
		}
		return false
	}
	if !hasEcho() {
		t.Fatal("t.echo missing from the initial tools/list")
	}

	// The upload endpoint's reload path, exercised directly: the skill set
	// changing must reach tools/list without a restart.
	h.mgr.SetSkills(nil)
	if hasEcho() {
		t.Fatal("t.echo still listed after the skill was removed")
	}
}

// --- Phase 3: demand caps, idempotency ---------------------------------------

// TestMCPSubmitClientRefIdempotent pins the retry contract: a resubmitted
// client_ref returns the original task and never starts a second run.
func TestMCPSubmitClientRefIdempotent(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	ts := newMCPServer(t, func(int) (llm.Response, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return textTurn("ref answer"), nil
	}, "")
	cs := connectStreamable(t, ts.URL, "test-token")

	first := submitTask(t, cs, map[string]any{"prompt": "do it", "client_ref": "order-7"})
	pollStatus(t, cs, first)

	res := callTool(t, cs, "agent_submit_task", map[string]any{"prompt": "do it", "client_ref": "order-7"})
	sc := structured(t, res)
	if sc["deduplicated"] != true || sc["task_id"] != first {
		t.Fatalf("resubmission = %#v, want deduplicated task %s", sc, first)
	}
	if sc["status"] != "completed" {
		t.Fatalf("deduplicated status = %#v, want the original's completed", sc)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("model called %d times for one logical task", calls)
	}
}

// TestMCPConcurrencyLimit: the working-task ceiling refuses new submissions
// with a readable error, and the refusal leaves nothing behind — the next
// submit once a slot frees goes through.
func TestMCPConcurrencyLimit(t *testing.T) {
	unblock := make(chan struct{})
	ts := newMCPServerOpts(t, func(int) (llm.Response, error) {
		<-unblock
		return textTurn("slow"), nil
	}, &MCPOptions{MaxConcurrent: 1})
	cs := connectStreamable(t, ts.URL, "test-token")

	id := submitTask(t, cs, map[string]any{"prompt": "occupies the one slot"})
	// Let the first run reach the model before submitting against the cap.
	time.Sleep(200 * time.Millisecond)

	res := callTool(t, cs, "agent_submit_task", map[string]any{"prompt": "one too many"})
	if !res.IsError || !strings.Contains(resultText(res), "concurrency limit") {
		t.Fatalf("over-cap submit = %v %q, want a concurrency refusal", res.IsError, resultText(res))
	}

	close(unblock)
	pollStatus(t, cs, id)
	// The freed slot admits the previously refused shape of request.
	submitTask(t, cs, map[string]any{"prompt": "now it fits"})
}

// TestMCPSubmitRateLimit: submissions past the per-minute budget are refused
// with a retry hint, and the refusal is a tool result, not a protocol error.
func TestMCPSubmitRateLimit(t *testing.T) {
	ts := newMCPServerOpts(t, func(int) (llm.Response, error) { return textTurn("ok"), nil },
		&MCPOptions{SubmitRPM: 2})
	cs := connectStreamable(t, ts.URL, "test-token")

	submitTask(t, cs, map[string]any{"prompt": "one"})
	submitTask(t, cs, map[string]any{"prompt": "two"})
	res := callTool(t, cs, "agent_submit_task", map[string]any{"prompt": "three"})
	if !res.IsError || !strings.Contains(resultText(res), "rate limit") {
		t.Fatalf("third submit = %v %q, want a rate-limit refusal", res.IsError, resultText(res))
	}
}

// --- Phase 4: adversarial-review hardening tests ------------------------------

// TestMCPConcurrentSameRef pins the atomic reservation: two concurrent
// first-submits of one client_ref produce exactly one run and exactly one
// deduplicated answer — a retried submit is never a double spend, not even
// when the retries race.
func TestMCPConcurrentSameRef(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	ts := newMCPServer(t, func(int) (llm.Response, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return textTurn("one of one"), nil
	}, "")
	cs := connectStreamable(t, ts.URL, "test-token")

	type outcome struct {
		sc map[string]any
	}
	results := make(chan outcome, 2)
	for i := 0; i < 2; i++ {
		go func() {
			// Inline CallTool: callTool's Fatalf must not run off the test's
			// own goroutine.
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
				Name:      "agent_submit_task",
				Arguments: map[string]any{"prompt": "same", "client_ref": "race-1"},
			})
			if err != nil || res.IsError {
				results <- outcome{}
				return
			}
			m, _ := res.StructuredContent.(map[string]any)
			results <- outcome{sc: m}
		}()
	}
	var ids []string
	dedups := 0
	for i := 0; i < 2; i++ {
		o := <-results
		id, _ := o.sc["task_id"].(string)
		if id == "" {
			t.Fatalf("concurrent submit failed: %#v", o.sc)
		}
		ids = append(ids, id)
		if o.sc["deduplicated"] == true {
			dedups++
		}
	}
	if ids[0] != ids[1] {
		t.Fatalf("one ref produced two tasks: %v", ids)
	}
	if dedups != 1 {
		t.Fatalf("deduplicated count = %d, want exactly one", dedups)
	}
	// The winning submission returns before its run reaches the model; wait for
	// the run to finish before counting calls.
	pollStatus(t, cs, ids[0])
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("model called %d times for one ref", calls)
	}
}

// TestMCPSynthToolGateBypassIsClosed: a declared tool the agent loop would
// send to a human gets refused on the sessionless MCP channel — no gate to
// park in, no execution. (A non-read-only tool is gated on every host, sandbox
// or not, which is what keeps this test environment-independent.)
func TestMCPSynthToolGateBypassIsClosed(t *testing.T) {
	skillDir := t.TempDir()
	toolsJSON := `{"tools":[{"name":"t.write","title":"Write","description":"not read-only",` +
		`"inputSchema":{"type":"object","properties":{},"additionalProperties":false},` +
		`"x-pogo-exec":{"argv":["echo","hi"]}}]}`
	if err := os.WriteFile(filepath.Join(skillDir, "tools.json"), []byte(toolsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	sk := skills.Skill{Name: "t", Description: "test", Path: filepath.Join(skillDir, "SKILL.md"), Dir: skillDir}

	h := newHarness(t, func(int) (llm.Response, error) { return textTurn("ok"), nil },
		func(c *Config) { c.Skills = []skills.Skill{sk} })
	srv, err := NewServer(h.mgr, ServerOptions{Token: h.token, MCP: &MCPOptions{}})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	cs := connectStreamable(t, ts.URL, "test-token")

	res := callTool(t, cs, "t.write", map[string]any{})
	if !res.IsError || !strings.Contains(resultText(res), "no approval gate") {
		t.Fatalf("direct MCP call of a reviewable tool = %v %q, want a refusal pointing at the gate", res.IsError, resultText(res))
	}
}

// TestMCPWaitCap: over the concurrent-wait cap a wait is refused with a
// pointer back to polling — the wait never becomes an unbounded grant of
// goroutines, subscriptions and hanging requests.
func TestMCPWaitCap(t *testing.T) {
	t.Setenv("POGO_MCP_MAX_WAITS", "")
	unblock := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(unblock) }) }
	// The scripted model ignores cancellation while blocked. Release it even
	// when an assertion fails, before Manager.Close waits for the run to drain.
	defer release()
	h := newHarness(t, func(int) (llm.Response, error) {
		<-unblock
		return textTurn("late"), nil
	})
	srv, err := NewServer(h.mgr, ServerOptions{Token: h.token, MCP: &MCPOptions{MaxWaits: 1}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	cs := connectStreamable(t, ts.URL, "test-token")
	id := submitTask(t, cs, map[string]any{"prompt": "slow"})

	type waitResult struct {
		result *mcp.CallToolResult
		err    error
	}
	first := make(chan waitResult, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() {
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name:      "agent_task_wait",
			Arguments: map[string]any{"task_id": id, "timeout_seconds": 30},
		})
		first <- waitResult{result, err}
	}()

	// Observe the first wait acquiring the real server slot before sending a
	// competing request. Probing for rejection before this point could itself
	// win the slot, reject the intended first waiter, and make every later probe
	// succeed serially until the old test failed and hung in cleanup.
	waitFor(t, 5*time.Second, func() bool { return len(srv.mcp.waitSlots) == 1 })
	cs2 := connectStreamable(t, ts.URL, "test-token")
	res := callTool(t, cs2, "agent_task_wait", map[string]any{"task_id": id, "timeout_seconds": 1})
	if !res.IsError || !strings.Contains(resultText(res), "too many concurrent waits") {
		t.Fatalf("second wait was not refused at the cap: %+v", res)
	}
	release()
	select {
	case outcome := <-first:
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		if status := structured(t, outcome.result)["status"]; status != "completed" {
			t.Fatalf("first wait did not complete normally: %v", status)
		}
	case <-ctx.Done():
		t.Fatal("first wait did not return after releasing the model")
	}
}

// TestMCPContinueReappliesPolicy: a session raised to auto out of band (a
// browser's /auto) does not carry that grant into an MCP-continued run — the
// channel's policy is the ceiling its runs get, or "continue the human's
// session" is a privilege escalation.
func TestMCPContinueReappliesPolicy(t *testing.T) {
	turn := 0
	h := newHarness(t, func(int) (llm.Response, error) {
		turn++
		if turn == 1 {
			return textTurn("first done"), nil
		}
		return toolTurn("bash", `{"command":"echo hi"}`), nil
	})
	srv, err := NewServer(h.mgr, ServerOptions{Token: h.token, MCP: &MCPOptions{}})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	cs := connectStreamable(t, ts.URL, "test-token")

	id := submitTask(t, cs, map[string]any{"prompt": "first"})
	pollStatus(t, cs, id)
	sess, err := h.mgr.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	sess.SetPolicy(ModeAuto, 0) // the out-of-band grant a browser /auto sets

	submitTask(t, cs, map[string]any{"prompt": "now bash", "session_id": id})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		sc := structured(t, callTool(t, cs, "agent_task_status", map[string]any{"task_id": id}))
		if sc["status"] == "input_required" {
			callTool(t, cs, "agent_task_cancel", map[string]any{"task_id": id})
			return
		}
		if sc["status"] != "working" {
			t.Fatalf("continued run became %v without a gate — the auto grant leaked", sc)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("continued run never surfaced input_required")
}

// --- Phase 5: approval closure, artifacts, list, webhook ----------------------

// awaitGate polls until the task's bash call is gated, and returns the
// pending_gates entry the remote approver would read.
func awaitGate(t *testing.T, cs *mcp.ClientSession, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		sc := structured(t, callTool(t, cs, "agent_task_status", map[string]any{"task_id": id}))
		if sc["status"] == "input_required" {
			gates, _ := sc["pending_gates"].([]any)
			if len(gates) != 1 {
				t.Fatalf("pending_gates = %#v, want exactly one", sc["pending_gates"])
			}
			gate, _ := gates[0].(map[string]any)
			if gate == nil {
				t.Fatalf("pending_gates entry is %#v", gates[0])
			}
			return gate
		}
		if sc["status"] != "working" {
			t.Fatalf("task became %v without showing the gate", sc)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("task never surfaced input_required")
	return nil
}

// A verdict is queued before Review publishes gate_resolved. Wait only for
// that answered gate to disappear; a new gate still requires a fresh decision.
func awaitGateResolution(t *testing.T, cs *mcp.ClientSession, id, gateID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ticker := time.NewTicker(30 * time.Millisecond)
	defer ticker.Stop()
	for {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "agent_task_status", Arguments: map[string]any{"task_id": id}})
		if err != nil {
			t.Fatalf("wait for gate %s resolution: %v", gateID, err)
		}
		sc := structured(t, res)
		if sc["status"] != "input_required" {
			return
		}
		gates, _ := sc["pending_gates"].([]any)
		if len(gates) != 1 {
			t.Fatalf("unexpected pending gates after answering %s: %#v", gateID, sc)
		}
		gate, _ := gates[0].(map[string]any)
		if gate["gate_id"] != gateID {
			t.Fatalf("new approval required after answering %s: %#v", gateID, sc)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("gate %s did not resolve: %v", gateID, ctx.Err())
		case <-ticker.C:
		}
	}
}

// TestMCPApproveGateRemote: the approval ticket carries the facts (gate_id,
// tool, args), and a remote approve through agent_task_approve lets the run
// proceed to completion — the web UI is an answering channel, not the only one.
func TestMCPApproveGateRemote(t *testing.T) {
	turn := 0
	ts := newMCPServer(t, func(int) (llm.Response, error) {
		turn++
		if turn == 1 {
			return toolTurn("bash", `{"command":"echo hi"}`), nil
		}
		return textTurn("approved and done"), nil
	}, "")
	cs := connectStreamable(t, ts.URL, "test-token")
	id := submitTask(t, cs, map[string]any{"prompt": "run something"})

	gate := awaitGate(t, cs, id)
	if gate["tool"] != "bash" || gate["gate_id"] == "" {
		t.Fatalf("pending gate = %#v", gate)
	}
	if args, _ := gate["args"].(string); !strings.Contains(args, "echo hi") {
		t.Fatalf("gate args = %q, want the command the approver is judging", args)
	}

	// gate_id omitted: exactly one pending gate makes it unambiguous.
	sc := structured(t, callTool(t, cs, "agent_task_approve", map[string]any{"task_id": id, "decision": "approve"}))
	if sc["approved"] != true {
		t.Fatalf("approve = %#v", sc)
	}
	awaitGateResolution(t, cs, id, gate["gate_id"].(string))
	sc = pollStatus(t, cs, id)
	if sc["status"] != "completed" {
		t.Fatalf("status after approval = %#v", sc)
	}
	if out := callTool(t, cs, "agent_task_result", map[string]any{"task_id": id}); resultText(out) != "approved and done" {
		t.Fatalf("result = %q", resultText(out))
	}
}

// TestMCPDenyGateWithFeedback: a remote deny refuses the call and the feedback
// goes back to the agent — the run finishes on its own words.
func TestMCPDenyGateWithFeedback(t *testing.T) {
	turn := 0
	ts := newMCPServer(t, func(int) (llm.Response, error) {
		turn++
		if turn == 1 {
			return toolTurn("bash", `{"command":"rm -rf /tmp/x"}`), nil
		}
		return textTurn("understood, explaining instead"), nil
	}, "")
	cs := connectStreamable(t, ts.URL, "test-token")
	id := submitTask(t, cs, map[string]any{"prompt": "clean up"})

	gate := awaitGate(t, cs, id)
	sc := structured(t, callTool(t, cs, "agent_task_approve", map[string]any{
		"task_id": id, "gate_id": gate["gate_id"],
		"decision": "deny", "feedback": "not allowed here; explain the steps instead",
	}))
	if sc["approved"] != false {
		t.Fatalf("deny = %#v", sc)
	}
	awaitGateResolution(t, cs, id, gate["gate_id"].(string))
	sc = pollStatus(t, cs, id)
	if sc["status"] != "completed" {
		t.Fatalf("status after denial = %#v", sc)
	}
}

// TestMCPTaskList: the registry answers "what has this channel submitted",
// newest first, with the state filter applied.
func TestMCPTaskList(t *testing.T) {
	ts := newMCPServer(t, func(int) (llm.Response, error) { return textTurn("listed"), nil }, "")
	cs := connectStreamable(t, ts.URL, "test-token")
	first := submitTask(t, cs, map[string]any{"prompt": "one"})
	second := submitTask(t, cs, map[string]any{"prompt": "two"})
	pollStatus(t, cs, first)
	pollStatus(t, cs, second)

	sc := structured(t, callTool(t, cs, "agent_task_list", map[string]any{}))
	tasks, _ := sc["tasks"].([]any)
	if len(tasks) < 2 {
		t.Fatalf("list = %#v", sc)
	}
	gotFirst, _ := tasks[0].(map[string]any)["task_id"].(string)
	gotSecond, _ := tasks[1].(map[string]any)["task_id"].(string)
	if gotFirst != second || gotSecond != first {
		t.Fatalf("order = %s, %s; want newest first (%s, %s)", gotFirst, gotSecond, second, first)
	}

	sc = structured(t, callTool(t, cs, "agent_task_list", map[string]any{"status": "working"}))
	if tasks, _ := sc["tasks"].([]any); len(tasks) != 0 {
		t.Fatalf("working filter = %#v, want empty", tasks)
	}
	sc = structured(t, callTool(t, cs, "agent_task_list", map[string]any{"status": "terminal"}))
	if tasks, _ := sc["tasks"].([]any); len(tasks) != 2 {
		t.Fatalf("terminal filter = %#v, want the two finished", tasks)
	}
}

// TestMCPTaskFile: the artifact channel reads a file the run wrote, and a
// path that escapes the workspace is refused.
func TestMCPTaskFile(t *testing.T) {
	turn := 0
	ts := newMCPServer(t, func(int) (llm.Response, error) {
		turn++
		if turn == 1 {
			return toolTurn("write", `{"path":"report.txt","content":"the full long answer lives here"}`), nil
		}
		return textTurn("wrote report.txt"), nil
	}, "")
	cs := connectStreamable(t, ts.URL, "test-token")
	id := submitTask(t, cs, map[string]any{"prompt": "write a report"})
	pollStatus(t, cs, id)

	sc := structured(t, callTool(t, cs, "agent_task_file", map[string]any{"task_id": id, "path": "report.txt"}))
	if content, _ := sc["content"].(string); content != "the full long answer lives here" {
		t.Fatalf("file content = %q", content)
	}
	res := callTool(t, cs, "agent_task_file", map[string]any{"task_id": id, "path": "../../etc/passwd"})
	if !res.IsError {
		t.Fatal("an escaping path was served")
	}
}

// TestMCPWebhookDelivery: the terminal push lands with the Standard Webhooks
// signature a receiver can verify, carrying ids and outcome — not the answer.
func TestMCPWebhookDelivery(t *testing.T) {
	type delivery struct {
		body      []byte
		signature string
		id        string
	}
	got := make(chan delivery, 4)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- delivery{body: body, signature: r.Header.Get("X-Pogo-Signature"), id: r.Header.Get("X-Pogo-Delivery-Id")}
	}))
	t.Cleanup(receiver.Close)

	ts := newMCPServerOpts(t, func(int) (llm.Response, error) { return textTurn("pushed"), nil },
		&MCPOptions{WebhookAllowInsecure: true})
	cs := connectStreamable(t, ts.URL, "test-token")
	id := submitTask(t, cs, map[string]any{
		"prompt": "notify me", "webhook_url": receiver.URL, "webhook_secret": "whsec",
	})

	select {
	case d := <-got:
		if d.id == "" {
			t.Fatal("no delivery id header")
		}
		var payload map[string]any
		if err := json.Unmarshal(d.body, &payload); err != nil {
			t.Fatalf("body: %v", err)
		}
		if payload["event"] != "task.finished" || payload["task_id"] != id || payload["status"] != "completed" {
			t.Fatalf("payload = %s", d.body)
		}
		// Verify the signature exactly the way a receiver would.
		parts := strings.SplitN(d.signature, ",v1=", 2)
		if len(parts) != 2 || !strings.HasPrefix(parts[0], "t=") {
			t.Fatalf("malformed signature header %q", d.signature)
		}
		tsPart := strings.TrimPrefix(parts[0], "t=")
		mac := hmac.New(sha256.New, []byte("whsec"))
		fmt.Fprintf(mac, "%s.", tsPart)
		mac.Write(d.body)
		want := fmt.Sprintf("t=%s,v1=%x", tsPart, mac.Sum(nil))
		if subtle.ConstantTimeCompare([]byte(d.signature), []byte(want)) != 1 {
			t.Fatalf("signature %q does not verify (want %q)", d.signature, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no webhook delivered")
	}
}

// TestMCPWebhookRetries: a failing endpoint is retried on the backoff
// schedule, not dropped after one attempt.
func TestMCPWebhookRetries(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(receiver.Close)

	ts := newMCPServerOpts(t, func(int) (llm.Response, error) { return textTurn("done"), nil },
		&MCPOptions{WebhookAllowInsecure: true, WebhookBackoff: []time.Duration{50 * time.Millisecond}})
	cs := connectStreamable(t, ts.URL, "test-token")
	submitTask(t, cs, map[string]any{"prompt": "flaky receiver", "webhook_url": receiver.URL})

	// One initial attempt plus one retry on the configured 50ms backoff.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := attempts
		mu.Unlock()
		if n >= 2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("attempts = %d, want the initial attempt plus a retry", attempts)
}

// TestMCPWebhookURLRejected: push endpoints are vetted at submit time — plain
// http and privately resolving hosts never reach the delivery path.
func TestMCPWebhookURLRejected(t *testing.T) {
	ts := newMCPServer(t, func(int) (llm.Response, error) { return textTurn("never runs"), nil }, "")
	cs := connectStreamable(t, ts.URL, "test-token")

	for _, u := range []string{"http://example.com/hook", "https://127.0.0.1/hook", "not-a-url"} {
		res := callTool(t, cs, "agent_submit_task", map[string]any{"prompt": "x", "webhook_url": u})
		if !res.IsError || !strings.Contains(resultText(res), "webhook_url") {
			t.Fatalf("webhook_url %q: got %v %q, want a submit-time refusal", u, res.IsError, resultText(res))
		}
	}
}
