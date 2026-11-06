package web

// The MCP *client* tests: the pool projecting a real pogo MCP server's tools
// (loopback — the same fixture philosophy as mcp_test.go, the wire behavior is
// the thing under test), the /api/mcp editor surface, and what a session does
// with the projection (registration snapshot + trust pre-approval).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/mcpclient"
	"github.com/yosukeno/pogo/tools"
)

// mcpLoopTokenEnv names the environment variable the loopback bearer token
// rides in — bearer_token_env holds a name, never the token itself.
const mcpLoopTokenEnv = "MCPLOOP_TOKEN"

// mcpClientAPI is a harness plus a second server wired for the /api/mcp
// routes — serveWeb's production arrangement, where the Manager's pool and
// the HTTP editor share one store path.
type mcpClientAPI struct {
	h     *harness
	srv   *httptest.Server
	store string
}

func newMCPClientAPI(t *testing.T, tweaks ...func(*Config)) *mcpClientAPI {
	t.Helper()
	store := filepath.Join(t.TempDir(), "mcp.json")
	base := []func(*Config){func(c *Config) { c.MCPClientStore = store }}
	h := newHarness(t, func(int) (llm.Response, error) { return textTurn("ok"), nil },
		append(base, tweaks...)...)
	srv, err := NewServer(h.mgr, ServerOptions{Token: h.token, MCPClientStore: store})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &mcpClientAPI{h: h, srv: ts, store: store}
}

func (a *mcpClientAPI) do(method, path, body string) *http.Response {
	a.h.t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, a.srv.URL+path, r)
	if err != nil {
		a.h.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+a.h.token)
	resp, err := a.srv.Client().Do(req)
	if err != nil {
		a.h.t.Fatal(err)
	}
	return resp
}

// getMCP decodes GET /api/mcp's view.
func (a *mcpClientAPI) getMCP(t *testing.T) mcpView {
	t.Helper()
	resp := a.do(http.MethodGet, "/api/mcp", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/mcp: %d", resp.StatusCode)
	}
	var v mcpView
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// waitMCPConnected polls /api/mcp/status until the named server reports
// connected. Connects are the pool's background business, so a PUT answers
// before they settle — the poll is the test's edge of that asynchrony.
func (a *mcpClientAPI) waitMCPConnected(t *testing.T, name string) mcpclient.Status {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp := a.do(http.MethodGet, "/api/mcp/status", "")
		var out struct {
			Status []mcpclient.Status `json:"status"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		for _, st := range out.Status {
			if st.Name == name && st.State == mcpclient.StateConnected {
				return st
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server %q never reported connected", name)
	return mcpclient.Status{}
}

// TestMCPClientPoolLoopback is the round trip that justifies the package: a
// pool configured against another pogo's /mcp endpoint projects its tools
// under the mcp_<server>_ prefix, and one of them executes for real.
func TestMCPClientPoolLoopback(t *testing.T) {
	ts := newMCPServer(t, func(int) (llm.Response, error) { return textTurn("ok"), nil }, "")
	t.Setenv(mcpLoopTokenEnv, "test-token")

	pool := mcpclient.NewPool(nil)
	// Registered after the fixture's ts.Close, and cleanups run LIFO: the
	// pool drains its keepers (and their open GET streams) before the server
	// starts waiting on them.
	t.Cleanup(pool.Close)
	pool.Reconcile(context.Background(), mcpclient.Config{Servers: map[string]*mcpclient.ServerConfig{
		"remote": {URL: ts.URL + "/mcp", BearerTokenEnv: mcpLoopTokenEnv, Trust: true},
	}})
	if err := pool.WaitReady(context.Background()); err != nil {
		t.Fatal(err)
	}

	proj, trusted, status := pool.Snapshot()
	names := map[string]bool{}
	for _, tool := range proj {
		names[tool.Name()] = true
	}
	for _, want := range []string{
		"mcp_remote_agent_submit_task", "mcp_remote_agent_task_status",
		"mcp_remote_agent_task_result", "mcp_remote_agent_task_list",
	} {
		if !names[want] {
			t.Errorf("projection missing %s (has %v)", want, names)
		}
	}
	// A trusted server pre-approves every projected wire name.
	if len(trusted) != len(proj) {
		t.Fatalf("trusted = %d names, want one per projected tool (%d)", len(trusted), len(proj))
	}
	if len(status) != 1 || status[0].State != mcpclient.StateConnected {
		t.Fatalf("status = %#v, want one connected server", status)
	}
	st := status[0]
	if st.ProtocolVersion == "" || st.ServerName != "pogo" {
		t.Fatalf("status handshake fields = %#v", st)
	}

	// The lightest real call: agent_task_list is read-only and needs no task.
	var listTool tools.Tool
	for _, tool := range proj {
		if tool.Name() == "mcp_remote_agent_task_list" {
			listTool = tool
		}
	}
	if listTool == nil {
		t.Fatal("mcp_remote_agent_task_list not projected")
	}
	res, err := listTool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(res.Text, `"tasks"`) {
		t.Fatalf("result text = %q, want the structured list folded in", res.Text)
	}
	d, ok := res.Details.(tools.MCPDetails)
	if !ok || d.Server != "remote" || d.Tool != "agent_task_list" {
		t.Fatalf("details = %#v", res.Details)
	}
}

// TestMCPClientAPI exercises the editor surface: GET's failure tolerance,
// PUT's strictness and reconcile, probe's both outcomes.
func TestMCPClientAPI(t *testing.T) {
	loopback := newMCPServer(t, func(int) (llm.Response, error) { return textTurn("ok"), nil }, "")
	t.Setenv(mcpLoopTokenEnv, "test-token")
	a := newMCPClientAPI(t)

	// GET over a file that does not exist yet: an empty config with no error —
	// no servers is the normal state of a fresh machine.
	v := a.getMCP(t)
	if v.LoadError != "" || len(v.Config.Servers) != 0 || len(v.Status) != 0 {
		t.Fatalf("empty GET = %#v", v)
	}
	if v.Path != a.store {
		t.Fatalf("path = %q, want %q", v.Path, a.store)
	}

	// A valid PUT lands on disk and reconciles into the pool.
	good := `{"servers":{"remote":{"url":"` + loopback.URL + `/mcp","bearer_token_env":"` + mcpLoopTokenEnv + `","trust":true}}}`
	resp := a.do(http.MethodPut, "/api/mcp", good)
	var putView mcpView
	if err := json.NewDecoder(resp.Body).Decode(&putView); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /api/mcp: %d", resp.StatusCode)
	}
	saved, err := mcpclient.Load(a.store)
	if err != nil || saved.Servers["remote"] == nil {
		t.Fatalf("saved file = %#v, %v", saved.Servers, err)
	}
	st := a.waitMCPConnected(t, "remote")
	if st.ProtocolVersion == "" || st.Tools == 0 {
		t.Fatalf("connected status = %#v", st)
	}

	// A session created now projects the pool: registry material aside (no
	// accessor — the wiring is one assignment in newSession), the policy grant
	// is observable, and it is the half that must not lie.
	sess, err := a.h.mgr.Create("", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, auto := sess.policy.Decide(agent.GateRequest{ToolName: "mcp_remote_agent_submit_task"}); !auto {
		t.Error("trusted MCP tool was not pre-approved for a new session")
	}
	if _, auto := sess.policy.Decide(agent.GateRequest{ToolName: "mcp_remote_not_a_tool"}); auto {
		t.Error("a name the pool never projected must not be auto-approved")
	}
	if _, auto := sess.policy.Decide(agent.GateRequest{ToolName: "bash", Args: json.RawMessage(`{"command":"ls"}`)}); auto {
		// The trust grant must not widen anything beyond its own wire names.
		t.Error("bash auto-approved — the trust grant leaked")
	}

	// The strict-decode and validation failures are 400s, never a saved file.
	for name, body := range map[string]string{
		"broken json":   `{"servers":`,
		"unknown field": `{"servers":{},"nope":1}`,
		"both kinds":    `{"servers":{"x":{"command":"c","url":"http://127.0.0.1:1/mcp"}}}`,
	} {
		resp := a.do(http.MethodPut, "/api/mcp", body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("PUT %s: %d, want 400", name, resp.StatusCode)
		}
	}

	// Probe rehearses the real connection: bearer resolution included.
	probe := func(body string) (int, mcpclient.ProbeResult) {
		t.Helper()
		resp := a.do(http.MethodPost, "/api/mcp/probe", body)
		defer resp.Body.Close()
		var pr mcpclient.ProbeResult
		if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, pr
	}
	code, pr := probe(`{"url":"` + loopback.URL + `/mcp","bearer_token_env":"` + mcpLoopTokenEnv + `"}`)
	if code != http.StatusOK || !pr.OK {
		t.Fatalf("probe = %d %#v", code, pr)
	}
	found := false
	for _, name := range pr.Tools {
		if name == "agent_submit_task" {
			found = true
		}
	}
	if !found || pr.ProtocolVersion == "" || pr.ServerName != "pogo" {
		t.Fatalf("probe result = %#v", pr)
	}
	// The filter applies to the probe exactly as it would to the pool.
	_, pr = probe(`{"url":"` + loopback.URL + `/mcp","bearer_token_env":"` + mcpLoopTokenEnv + `","exclude_tools":["agent_*"]}`)
	if !pr.OK || len(pr.Tools) != 0 {
		t.Fatalf("filtered probe = %#v, want ok with no tools", pr)
	}
	// A dead host is a 200 with ok:false — the diagnostic, not an accident.
	_, pr = probe(`{"url":"http://127.0.0.1:1/mcp"}`)
	if pr.OK || pr.Error == "" {
		t.Fatalf("dead-host probe = %#v, want ok:false with a reason", pr)
	}
	// And a malformed entry is a 400, same bar as PUT.
	if code, _ := probe(`{}`); code != http.StatusBadRequest {
		t.Fatalf("empty probe entry = %d, want 400", code)
	}
	if code, _ := probe(`{"url":"http://127.0.0.1:1/mcp","bogus":1}`); code != http.StatusBadRequest {
		t.Fatalf("unknown-field probe = %d, want 400", code)
	}
}

// TestMCPClientBootProjection covers the file-at-boot path: NewManager loads
// mcp.json itself, a session created once the pool is up carries the
// projection, and trust stays per-server — an untrusted entry to the *same*
// server gates its tools.
func TestMCPClientBootProjection(t *testing.T) {
	loopback := newMCPServer(t, func(int) (llm.Response, error) { return textTurn("ok"), nil }, "")
	t.Setenv(mcpLoopTokenEnv, "test-token")

	store := filepath.Join(t.TempDir(), "mcp.json")
	cfg := `{"servers":{
		"remote":{"url":"` + loopback.URL + `/mcp","bearer_token_env":"` + mcpLoopTokenEnv + `","trust":true},
		"plain":{"url":"` + loopback.URL + `/mcp","bearer_token_env":"` + mcpLoopTokenEnv + `"}
	}}`
	if err := os.WriteFile(store, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, func(int) (llm.Response, error) { return textTurn("ok"), nil },
		func(c *Config) { c.MCPClientStore = store })

	// The pool connects in the background from NewManager on; wait for the
	// projection rather than for a fixed sleep.
	deadline := time.Now().Add(10 * time.Second)
	var proj []tools.Tool
	var trusted []string
	for time.Now().Before(deadline) {
		proj, trusted = h.mgr.mcpProjection(nil, nil)
		ready := map[string]bool{}
		for _, tool := range proj {
			ready[tool.Name()] = true
		}
		// The servers connect independently: the trusted remote can appear
		// before plain, so a nonempty projection/grant list is not enough.
		if ready["mcp_remote_agent_submit_task"] && ready["mcp_plain_agent_submit_task"] && len(trusted) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(proj) == 0 {
		t.Fatal("projection stayed empty")
	}
	seen := map[string]bool{}
	for _, tool := range proj {
		seen[tool.Name()] = true
	}
	if !seen["mcp_remote_agent_submit_task"] || !seen["mcp_plain_agent_submit_task"] {
		t.Fatalf("projection = %v", seen)
	}
	trustedSet := map[string]bool{}
	for _, name := range trusted {
		trustedSet[name] = true
	}
	if !trustedSet["mcp_remote_agent_submit_task"] {
		t.Fatal("trusted server's wire name missing from the grant list")
	}
	if trustedSet["mcp_plain_agent_submit_task"] {
		t.Fatal("untrusted server's wire name must not be granted")
	}

	sess, err := h.mgr.Create("", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, auto := sess.policy.Decide(agent.GateRequest{ToolName: "mcp_remote_agent_submit_task"}); !auto {
		t.Error("trusted server's tool gated")
	}
	if _, auto := sess.policy.Decide(agent.GateRequest{ToolName: "mcp_plain_agent_submit_task"}); auto {
		t.Error("untrusted server's tool auto-approved")
	}
}

// --- dedupeMCP ---------------------------------------------------------------

// stubSynth is the minimal Tool a manifest entry is for the dedupe: a name.
type stubSynth struct{ name string }

func (s stubSynth) Name() string                       { return s.name }
func (s stubSynth) Description() string                { return "stub" }
func (s stubSynth) InputSchema() map[string]any        { return map[string]any{"type": "object"} }
func (s stubSynth) ExecutionMode() tools.ExecutionMode { return tools.Sequential }
func (s stubSynth) Execute(context.Context, json.RawMessage) (tools.Result, error) {
	return tools.Result{}, errors.New("unused")
}

func mcpToolForTest(t *testing.T, server, tool string) *tools.MCPTool {
	t.Helper()
	mt, err := tools.NewMCPTool(server, &mcp.Tool{
		Name: tool, Description: "test tool",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}, 0, func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
		return nil, errors.New("unused")
	})
	if err != nil {
		t.Fatal(err)
	}
	return mt
}

// TestDedupeMCP: a wire-name collision with a manifest tool skips the MCP
// entry *and* its trust grant — the grant under a colliding name would
// pre-approve the manifest tool standing under it.
func TestDedupeMCP(t *testing.T) {
	echo := mcpToolForTest(t, "s", "echo")
	search := mcpToolForTest(t, "s", "search")
	colliding := stubSynth{name: echo.Name()}

	proj, trusted := dedupeMCP(
		[]tools.Tool{echo, search},
		[]string{echo.Name(), search.Name()},
		[]tools.Tool{colliding},
	)
	if len(proj) != 1 || proj[0].Name() != search.Name() {
		t.Fatalf("proj = %v, want only the non-colliding tool", proj)
	}
	if len(trusted) != 1 || trusted[0] != search.Name() {
		t.Fatalf("trusted = %v, want the colliding grant dropped with its tool", trusted)
	}

	// No collision passes both halves through untouched.
	proj, trusted = dedupeMCP([]tools.Tool{echo}, []string{echo.Name()}, nil)
	if len(proj) != 1 || len(trusted) != 1 {
		t.Fatalf("no-collision = %v %v", proj, trusted)
	}
}
