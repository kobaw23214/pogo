package web

// Hot-reload tests for the MCP *server* side's file configuration
// (web/mcpserve.go): the enable/token/policy gates must follow PUT /api/mcp
// and the POGO_MCP* environment without a restart, while the demand caps stay
// construction-fixed and merely report restart_required. The wire side of the
// gates is exercised for real — raw POSTs for the refusal shapes, the SDK's
// own client for the admissions, the same fixture philosophy as mcp_test.go.

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/mcpclient"
	"github.com/yosukeno/pogo/skills"
)

// serveFixture neutralizes the ambient POGO_MCP* environment and builds the
// standard harness with a store-backed, initially fileless MCP server side.
func serveFixture(t *testing.T, tweaks ...func(*Config)) *mcpClientAPI {
	t.Helper()
	for _, name := range []string{
		"POGO_MCP", "POGO_MCP_TOKEN", "POGO_MCP_POLICY",
		"POGO_MCP_MAX_CONCURRENT", "POGO_MCP_SUBMIT_RPM", "POGO_MCP_MAX_WAITS",
	} {
		t.Setenv(name, "")
	}
	return newMCPClientAPI(t, tweaks...)
}

// postMCP is a raw POST against /mcp, for the assertions where the gate's own
// answer (404/401/503) is the whole story — it runs before the protocol
// handler, so the body only has to be plausible.
func postMCP(t *testing.T, endpoint string, headers ...string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"probe","version":"0"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// tryConnect answers whether an SDK handshake against /mcp succeeds — the
// positive half of the gate, where "200" is the whole protocol round trip.
func tryConnect(t *testing.T, endpoint, token string) error {
	t.Helper()
	hc := &http.Client{}
	if token != "" {
		hc = &http.Client{Transport: bearerRT{token}}
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "mcp-test"}, nil).Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: hc}, nil)
	if err != nil {
		return err
	}
	_ = cs.Close()
	return nil
}

func (a *mcpClientAPI) putMCP(t *testing.T, body string) mcpView {
	t.Helper()
	resp := a.do(http.MethodPut, "/api/mcp", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /api/mcp: %d", resp.StatusCode)
	}
	var v mcpView
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func (a *mcpClientAPI) serveState(t *testing.T) ServeState {
	t.Helper()
	v := a.getMCP(t)
	if v.ServerState == nil {
		t.Fatal("GET /api/mcp carries no server_state")
	}
	return *v.ServerState
}

// TestMCPServeGateHotReload walks the enable/token state machine end to end:
// off by default, on without auth, on with a direct token, back off — every
// transition a PUT, none a restart.
func TestMCPServeGateHotReload(t *testing.T) {
	a := serveFixture(t)
	mcpURL := a.srv.URL + "/mcp"

	// Nothing configured anywhere: the endpoints answer an ordinary 404.
	if resp := postMCP(t, mcpURL); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("default /mcp = %d, want 404", resp.StatusCode)
	}
	st := a.serveState(t)
	if st.Enabled || st.TokenRequired || st.Source != "default" || st.RestartRequired {
		t.Fatalf("default state = %#v", st)
	}
	if len(st.Endpoints) != 2 || st.Endpoints[0] != "/mcp" || st.Endpoints[1] != "/sse" {
		t.Fatalf("endpoints = %v", st.Endpoints)
	}

	// Enabled without a token: no Bearer required, none refused either.
	v := a.putMCP(t, `{"servers":{},"server":{"enabled":true}}`)
	if v.ServerState == nil || !v.ServerState.Enabled || v.ServerState.TokenRequired {
		t.Fatalf("PUT response state = %#v", v.ServerState)
	}
	if err := tryConnect(t, mcpURL, ""); err != nil {
		t.Fatalf("unauthenticated connect while open: %v", err)
	}
	if err := tryConnect(t, mcpURL, "anything"); err != nil {
		// "Clients get in whether or not they send a Bearer" is the contract.
		t.Fatalf("connect with a pointless Bearer while open: %v", err)
	}
	if st := a.serveState(t); st.Source != "file" || st.Policy != "standard" {
		t.Fatalf("file-enabled state = %#v", st)
	}
	// include_tools rides the same hot path as the gate fields.
	v = a.putMCP(t, `{"servers":{},"server":{"enabled":true,"include_tools":["agent_*"]}}`)
	if st := a.serveState(t); len(st.IncludeTools) != 1 || st.IncludeTools[0] != "agent_*" {
		t.Fatalf("state include_tools = %v", st.IncludeTools)
	}

	// A direct token takes effect on the next request: no/wrong Bearer 401
	// with the authenticate header, right Bearer and ?token= connect.
	a.putMCP(t, `{"servers":{},"server":{"enabled":true,"token":"secret"}}`)
	if resp := postMCP(t, mcpURL); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-Bearer /mcp = %d, want 401", resp.StatusCode)
	} else if wa := resp.Header.Get("WWW-Authenticate"); !strings.Contains(wa, "Bearer") {
		t.Fatalf("WWW-Authenticate = %q, want a Bearer challenge", wa)
	}
	if resp := postMCP(t, mcpURL, "Authorization", "Bearer wrong"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong-Bearer /mcp = %d, want 401", resp.StatusCode)
	}
	if err := tryConnect(t, mcpURL, "secret"); err != nil {
		t.Fatalf("connect with the file token: %v", err)
	}
	if err := tryConnect(t, mcpURL+"?token=secret", ""); err != nil {
		t.Fatalf("connect with the query token: %v", err)
	}
	if st := a.serveState(t); !st.TokenRequired {
		t.Fatalf("token-enabled state = %#v", st)
	}

	// Back off again: the 404 returns, Bearer or not.
	a.putMCP(t, `{"servers":{},"server":{"enabled":false}}`)
	if resp := postMCP(t, mcpURL, "Authorization", "Bearer secret"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled /mcp = %d, want 404", resp.StatusCode)
	}
}

// TestMCPServeTokenEnvAndCaps covers the two remaining file shapes: a
// token_env that cannot resolve fails closed, and a cap edit reports
// restart_required instead of silently half-applying.
func TestMCPServeTokenEnvAndCaps(t *testing.T) {
	a := serveFixture(t)
	mcpURL := a.srv.URL + "/mcp"

	// token_env naming a missing variable: the operator believes auth is on,
	// so the gate refuses the service (503) rather than silently opening.
	a.putMCP(t, `{"servers":{},"server":{"enabled":true,"token_env":"POGO_TEST_DEFINITELY_MISSING_MCP_TOKEN"}}`)
	if resp := postMCP(t, mcpURL, "Authorization", "Bearer anything"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unresolvable token_env /mcp = %d, want 503", resp.StatusCode)
	}
	if st := a.serveState(t); !st.TokenRequired {
		t.Fatalf("misconfigured state = %#v, want token_required", st)
	}

	// token_env that resolves: the variable's value is the credential.
	t.Setenv("POGO_TEST_LIVE_MCP_TOKEN", "live-secret")
	a.putMCP(t, `{"servers":{},"server":{"enabled":true,"token_env":"POGO_TEST_LIVE_MCP_TOKEN"}}`)
	if resp := postMCP(t, mcpURL); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-Bearer /mcp = %d, want 401", resp.StatusCode)
	}
	if err := tryConnect(t, mcpURL, "live-secret"); err != nil {
		t.Fatalf("connect with the env-resolved token: %v", err)
	}

	// A cap edit is not hot: the state says so, and enabled keeps working.
	v := a.putMCP(t, `{"servers":{},"server":{"enabled":true,"max_concurrent":16}}`)
	if v.ServerState == nil || !v.ServerState.RestartRequired {
		t.Fatalf("cap-edited state = %#v, want restart_required", v.ServerState)
	}
	if err := tryConnect(t, mcpURL, ""); err != nil {
		t.Fatalf("connect after a cap edit: %v", err)
	}
	v = a.putMCP(t, `{"servers":{},"server":{"enabled":true}}`)
	if v.ServerState == nil || v.ServerState.RestartRequired {
		t.Fatalf("cap-reverted state = %#v, want restart_required cleared", v.ServerState)
	}
}

// TestMCPServeEnvOverrides: the POGO_MCP* environment sits above the file —
// for the token, for the enable, and for the policy — and server_state says
// so with source:"env".
func TestMCPServeEnvOverrides(t *testing.T) {
	a := serveFixture(t)
	mcpURL := a.srv.URL + "/mcp"

	a.putMCP(t, `{"servers":{},"server":{"enabled":true,"token":"file-secret","policy":"strict"}}`)
	t.Setenv("POGO_MCP_TOKEN", "env-secret")

	// The file's token no longer admits; the environment's does.
	if resp := postMCP(t, mcpURL, "Authorization", "Bearer file-secret"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("file token under env override = %d, want 401", resp.StatusCode)
	}
	if err := tryConnect(t, mcpURL, "env-secret"); err != nil {
		t.Fatalf("connect with the env token: %v", err)
	}
	if st := a.serveState(t); st.Source != "env" {
		t.Fatalf("state source = %q, want env", st.Source)
	}
	// Policy likewise: env auto over file strict.
	if st := a.serveState(t); st.Policy != "strict" {
		t.Fatalf("policy before env override = %q", st.Policy)
	}
	t.Setenv("POGO_MCP_POLICY", "auto")
	if st := a.serveState(t); st.Policy != "auto" {
		t.Fatalf("policy under env override = %q, want auto", st.Policy)
	}

	// POGO_MCP=1 forces the endpoints on over an explicit file disable.
	a.putMCP(t, `{"servers":{},"server":{"enabled":false,"token":"file-secret"}}`)
	t.Setenv("POGO_MCP", "1")
	if err := tryConnect(t, mcpURL, "env-secret"); err != nil {
		t.Fatalf("connect under POGO_MCP=1 with the env token: %v", err)
	}
}

// TestMCPServeEffectiveTiers pins the precedence resolution itself, at unit
// granularity: env > file > static for all three hot fields, including the
// file section's power to mask the static baseline with an explicit zero.
func TestMCPServeEffectiveTiers(t *testing.T) {
	t.Setenv("POGO_MCP", "")
	t.Setenv("POGO_MCP_TOKEN", "")
	t.Setenv("POGO_MCP_POLICY", "")

	l := newMCPLayer(nil, MCPOptions{Token: "static-tok", Policy: "auto"}, true, nil)
	if !l.effectiveEnabled() {
		t.Error("static baseline presence should enable")
	}
	if tok, _ := l.effectiveToken(); tok != "static-tok" {
		t.Errorf("static token = %q", tok)
	}
	if got := l.effectivePolicy(); got != ModeAuto {
		t.Errorf("static policy = %q", got)
	}

	// A file section outranks the static baseline — including its zero
	// values, which are statements, not absences.
	l.setFileConfig(&mcpclient.ServeConfig{Enabled: false, Token: "file-tok", PolicyRaw: "strict"})
	if l.effectiveEnabled() {
		t.Error("file enabled:false must mask the static baseline's enable")
	}
	if tok, _ := l.effectiveToken(); tok != "file-tok" {
		t.Errorf("file token over static = %q", tok)
	}
	if got := l.effectivePolicy(); got != ModeStrict {
		t.Errorf("file policy over static = %q", got)
	}

	// And the environment outranks the file.
	t.Setenv("POGO_MCP", "1")
	t.Setenv("POGO_MCP_TOKEN", "env-tok")
	t.Setenv("POGO_MCP_POLICY", "standard")
	if !l.effectiveEnabled() {
		t.Error("POGO_MCP=1 must force-enable over file disabled")
	}
	if tok, _ := l.effectiveToken(); tok != "env-tok" {
		t.Errorf("env token over file = %q", tok)
	}
	if got := l.effectivePolicy(); got != ModeStandard {
		t.Errorf("env policy over file = %q", got)
	}
}

// --- include_tools whitelist -------------------------------------------------

// listMCPNames answers the set of tool names tools/list currently returns —
// the surface the whitelist edits.
func listMCPNames(t *testing.T, cs *mcp.ClientSession) map[string]bool {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range res.Tools {
		names[tool.Name] = true
	}
	return names
}

// TestMCPServeIncludeTools: the file's whitelist filters tools/list, refuses
// tools/call of unlisted names, follows PUT /api/mcp without a restart, and
// shows up in server_state.
func TestMCPServeIncludeTools(t *testing.T) {
	// A synth passthrough gives the list a non-agent_* name to filter on.
	skillDir := t.TempDir()
	toolsJSON := `{"tools":[{"name":"mal.echo","title":"Echo","description":"test passthrough",` +
		`"inputSchema":{"type":"object","properties":{},"additionalProperties":false},` +
		`"annotations":{"readOnlyHint":true},"x-pogo-exec":{"argv":["echo","hi"]}}]}`
	if err := os.WriteFile(filepath.Join(skillDir, "tools.json"), []byte(toolsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	sk := skills.Skill{Name: "t", Description: "test", Path: filepath.Join(skillDir, "SKILL.md"), Dir: skillDir}

	a := serveFixture(t, func(c *Config) { c.Skills = []skills.Skill{sk} })

	// Enabled without a whitelist: the full surface is listed.
	a.putMCP(t, `{"servers":{},"server":{"enabled":true}}`)
	cs := connectStreamable(t, a.srv.URL, "")
	names := listMCPNames(t, cs)
	if !names["mal.echo"] || !names["agent_submit_task"] || !names["agent_task_status"] {
		t.Fatalf("unfiltered list = %v", names)
	}
	if st := a.serveState(t); len(st.IncludeTools) != 0 {
		t.Fatalf("unfiltered state include_tools = %v", st.IncludeTools)
	}

	// The whitelist takes effect immediately — same client session, no
	// restart: only the matching entries survive tools/list.
	v := a.putMCP(t, `{"servers":{},"server":{"enabled":true,"include_tools":["mal.*","agent_task_status"]}}`)
	if v.ServerState == nil || len(v.ServerState.IncludeTools) != 2 ||
		v.ServerState.IncludeTools[0] != "mal.*" || v.ServerState.IncludeTools[1] != "agent_task_status" {
		t.Fatalf("PUT response include_tools = %#v", v.ServerState)
	}
	names = listMCPNames(t, cs)
	if len(names) != 2 || !names["mal.echo"] || !names["agent_task_status"] {
		t.Fatalf("filtered list = %v, want exactly mal.echo + agent_task_status", names)
	}

	// A call to an unlisted tool is refused outright; a call to a listed one
	// reaches its handler (an unknown-task *result* error proves the trip).
	_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "agent_submit_task", Arguments: map[string]any{"prompt": "x"},
	})
	if err == nil || !strings.Contains(err.Error(), "include_tools") {
		t.Fatalf("call of filtered tool = %v, want an include_tools refusal", err)
	}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "agent_task_status", Arguments: map[string]any{"task_id": "nope"},
	})
	if err != nil {
		t.Fatalf("call of whitelisted tool: %v", err)
	}
	if !res.IsError {
		t.Fatal("unknown task should be a result-level error — the point is the call went through")
	}

	// A second edit moves the surface again, still without a restart.
	a.putMCP(t, `{"servers":{},"server":{"enabled":true,"include_tools":["agent_*"]}}`)
	names = listMCPNames(t, cs)
	if names["mal.echo"] || !names["agent_submit_task"] {
		t.Fatalf("re-filtered list = %v, want agent_* only", names)
	}
	// And clearing the whitelist restores the full surface.
	a.putMCP(t, `{"servers":{},"server":{"enabled":true}}`)
	if names = listMCPNames(t, cs); !names["mal.echo"] {
		t.Fatalf("cleared whitelist list = %v, want mal.echo back", names)
	}
}
