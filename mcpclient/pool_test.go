package mcpclient

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	pogotools "github.com/yosukeno/pogo/tools"
)

// The pool tests run against a real go-sdk server over httptest — the only
// honest fixture for a protocol client, the same call web/mcp_test.go makes:
// the failure modes that matter (handshake, tools/list pagination, content
// round trip) live on the wire, not behind a mock.

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newFixture starts an MCP server with three tools spanning the annotation
// space, behind a streamable-HTTP endpoint. The returned cleanup closes the
// pool first: the streamable transport holds a standalone GET stream open,
// and httptest's Close waits on outstanding requests — closing the server
// first would wait on a stream only the client can end (web/mcp_test.go's
// hard-won ordering, applied here preemptively).
func newFixture(t *testing.T) (url string) {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "v1.2.3"}, nil)
	echo := func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "echo:" + string(req.Params.Arguments)}},
		}, nil
	}
	srv.AddTool(&mcp.Tool{
		Name: "echo", Description: "echoes its arguments",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"text": map[string]any{"type": "string"},
		}},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}, echo)
	srv.AddTool(&mcp.Tool{
		Name: "mal.search", Description: "searches the library",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: "3 hits"}},
			StructuredContent: map[string]any{"hits": 3},
		}, nil
	})
	srv.AddTool(&mcp.Tool{
		Name: "mal.exec", Description: "runs a thing",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ran"}}}, nil
	})
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(ts.Close)
	return ts.URL
}

func newPool(t *testing.T) *Pool {
	t.Helper()
	p := NewPool(testLogger())
	// Registered after the fixture's ts.Close wherever both exist, and
	// cleanups run LIFO — the pool drains its keepers before the server
	// starts waiting on the open streams.
	t.Cleanup(p.Close)
	return p
}

func toolNames(ts []pogotools.Tool) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Name())
	}
	return out
}

func TestPoolConnectSnapshotExecute(t *testing.T) {
	url := newFixture(t)
	p := newPool(t)
	p.Reconcile(context.Background(), Config{Servers: map[string]*ServerConfig{
		"fx": {URL: url, Trust: true},
	}})
	if err := p.WaitReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	tools, trusted, status := p.Snapshot()

	// Set comparison: the SDK's tools/list order is the server's business.
	got := map[string]bool{}
	for _, n := range toolNames(tools) {
		got[n] = true
	}
	for _, w := range []string{"mcp_fx_echo", "mcp_fx_mal_search", "mcp_fx_mal_exec"} {
		if !got[w] {
			t.Fatalf("tools = %v, missing %s", toolNames(tools), w)
		}
	}
	if len(got) != 3 {
		t.Fatalf("tools = %v, want exactly the fixture's three", toolNames(tools))
	}
	if len(trusted) != 3 {
		t.Fatalf("trusted = %v, want all three (trust: true)", trusted)
	}
	if len(status) != 1 || status[0].State != StateConnected {
		t.Fatalf("status = %#v", status)
	}
	st := status[0]
	if st.ServerName != "fixture" || st.ServerVersion != "v1.2.3" || st.ProtocolVersion == "" || st.Tools != 3 {
		t.Fatalf("status = %#v, want the handshake's serverInfo and 3 tools", st)
	}

	// A full round trip: the model's arguments cross the real SDK transport
	// and the answer comes back mapped.
	var echo pogotools.Tool
	for _, tl := range tools {
		if tl.Name() == "mcp_fx_echo" {
			echo = tl
		}
	}
	if echo == nil {
		t.Fatal("echo tool missing")
	}
	if echo.ExecutionMode() != pogotools.Parallel {
		t.Error("echo is read-only idempotent on the server; it should be Parallel here")
	}
	res, err := echo.Execute(context.Background(), json.RawMessage(`{"text":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, `"text":"hello"`) {
		t.Fatalf("echo result = %q, want the arguments echoed", res.Text)
	}
	d, ok := res.Details.(pogotools.MCPDetails)
	if !ok || d.Server != "fx" || d.Tool != "echo" {
		t.Fatalf("Details = %#v", res.Details)
	}

	// The structured-content fold: mal.search's structured result must reach
	// the model as text, not only Details (kimi-code#2554).
	for _, tl := range tools {
		if tl.Name() == "mcp_fx_mal_search" {
			res, err := tl.Execute(context.Background(), json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(res.Text, "3 hits") || !strings.Contains(res.Text, `"hits":3`) {
				t.Fatalf("search result = %q, want text + structured fold", res.Text)
			}
		}
	}
}

func TestPoolIncludeExclude(t *testing.T) {
	url := newFixture(t)
	p := newPool(t)
	p.Reconcile(context.Background(), Config{Servers: map[string]*ServerConfig{
		"fx": {URL: url, IncludeTools: []string{"mal.*"}, ExcludeTools: []string{"mal.exec"}},
	}})
	if err := p.WaitReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	tools, trusted, _ := p.Snapshot()
	got := toolNames(tools)
	if strings.Join(got, ",") != "mcp_fx_mal_search" {
		t.Fatalf("tools = %v, want only mcp_fx_mal_search", got)
	}
	if len(trusted) != 0 {
		t.Fatalf("no trust flag, so no trust list: %v", trusted)
	}
}

func TestPoolReconcileRemovesAndReconnects(t *testing.T) {
	url := newFixture(t)
	p := newPool(t)
	bg := context.Background()

	p.Reconcile(bg, Config{Servers: map[string]*ServerConfig{"fx": {URL: url}}})
	if err := p.WaitReady(bg); err != nil {
		t.Fatal(err)
	}
	if tools, _, _ := p.Snapshot(); len(tools) != 3 {
		t.Fatalf("tools = %d, want 3", len(tools))
	}

	// A configuration change (the filter narrows) has a different
	// fingerprint: the conn is torn down and re-dialed.
	p.Reconcile(bg, Config{Servers: map[string]*ServerConfig{
		"fx": {URL: url, IncludeTools: []string{"echo"}},
	}})
	if err := p.WaitReady(bg); err != nil {
		t.Fatal(err)
	}
	if tools, _, _ := p.Snapshot(); strings.Join(toolNames(tools), ",") != "mcp_fx_echo" {
		t.Fatalf("after reconfigure, tools = %v", toolNames(tools))
	}

	// Removing the entry closes the conn; the server vanishes from the
	// projection and the status list alike.
	p.Reconcile(bg, Config{})
	if tools, _, status := p.Snapshot(); len(tools) != 0 || len(status) != 0 {
		t.Fatalf("after removal: tools = %v, status = %v", toolNames(tools), status)
	}
}

func TestPoolRequiredFailure(t *testing.T) {
	p := newPool(t)
	p.Reconcile(context.Background(), Config{Servers: map[string]*ServerConfig{
		// A port that answers nothing: the first attempt fails fast, and
		// required turns that into a startup error naming the server.
		"needed": {URL: "http://127.0.0.1:1/mcp", Required: true, StartupTimeoutMs: 1000},
		"extra":  {URL: "http://127.0.0.1:1/mcp", StartupTimeoutMs: 1000},
	}})
	err := p.WaitReady(context.Background())
	if err == nil || !strings.Contains(err.Error(), "needed") {
		t.Fatalf("WaitReady = %v, want a required-server error naming it", err)
	}
	if strings.Contains(err.Error(), "extra") {
		t.Fatalf("optional servers must not fail startup: %v", err)
	}
	_, _, status := p.Snapshot()
	for _, s := range status {
		if s.State != StateDisconnected || s.LastError == "" {
			t.Fatalf("status = %#v, want disconnected with the cause", s)
		}
	}
}

func TestPoolReconnectingCallFailsFast(t *testing.T) {
	p := newPool(t)
	p.Reconcile(context.Background(), Config{Servers: map[string]*ServerConfig{
		"down": {URL: "http://127.0.0.1:1/mcp", StartupTimeoutMs: 1000},
	}})
	_ = p.WaitReady(context.Background())
	// Never connected: no tools at all — the fail-fast path is exercised by
	// the caller closure directly, which is the object a session holds.
	c := p.conns["down"]
	if _, err := c.call(context.Background(), "anything", json.RawMessage(`{}`)); err == nil ||
		!strings.Contains(err.Error(), "reconnecting") {
		t.Fatalf("call on a down server = %v, want the fail-fast error", err)
	}
}

// TestPoolInlineBearerToken: the inlined token must ride every request as
// the Authorization header. The fixture records what the server actually
// received — the only honest check of a credential path.
func TestPoolInlineBearerToken(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "v1"}, nil)
	srv.AddTool(&mcp.Tool{
		Name: "echo", Description: "d",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	var mu sync.Mutex
	var auths []string
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	p := newPool(t)
	p.Reconcile(context.Background(), Config{Servers: map[string]*ServerConfig{
		"fx": {URL: ts.URL, BearerToken: "inline-secret"},
	}})
	if err := p.WaitReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(auths) == 0 {
		t.Fatal("the fixture saw no requests")
	}
	for _, a := range auths {
		if a != "Bearer inline-secret" {
			t.Fatalf("Authorization = %q, want the inlined token on every request", a)
		}
	}
}

func TestBuildCommand(t *testing.T) {
	// The sanitizer must strip anything credential-shaped (case-insensitively)
	// and the harness's own POGO_ variables, while leaving the rest.
	t.Setenv("KIMI_API_KEY", "sk-should-not-leak")
	t.Setenv("lowercase_token", "also-secret")
	t.Setenv("POGO_CONFIG", "/etc/pogo/providers.json")
	t.Setenv("EDITOR", "vim")
	t.Setenv("EXPAND_ME", "expanded-value")

	cfg := &ServerConfig{
		Command: "srv", Args: []string{"--stdio"}, Cwd: "/tmp",
		Env: map[string]string{
			"CHILD_TOKEN": "$EXPAND_ME", // explicit: informed consent, expanded from the unsanitized env
			"EDITOR":      "nano",       // overrides the inherited value
			"BRACED":      "${EXPAND_ME}/suffix",
		},
	}
	cmd := buildCommand(cfg, os.Getenv)
	if cmd.Path != "srv" || strings.Join(cmd.Args[1:], " ") != "--stdio" {
		t.Fatalf("argv = %v", cmd.Args)
	}
	if cmd.Dir != "/tmp" {
		t.Fatalf("Dir = %q", cmd.Dir)
	}
	env := map[string]string{}
	for _, kv := range cmd.Env {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	for _, gone := range []string{"KIMI_API_KEY", "lowercase_token", "POGO_CONFIG"} {
		if _, ok := env[gone]; ok {
			t.Errorf("%s must not reach the child", gone)
		}
	}
	if env["EDITOR"] != "nano" {
		t.Errorf("explicit env must override the inherited value, got %q", env["EDITOR"])
	}
	if env["CHILD_TOKEN"] != "expanded-value" || env["BRACED"] != "expanded-value/suffix" {
		t.Errorf("expansion from the unsanitized environment failed: %v / %v", env["CHILD_TOKEN"], env["BRACED"])
	}
}

func TestSensitiveEnvName(t *testing.T) {
	for name, want := range map[string]bool{
		"ANTHROPIC_API_KEY": true, "npm_password": true, "AWS_SECRET_ACCESS_KEY": true,
		"OAUTH_CLIENT": true, "MY_CREDENTIALS": true, "POGO_SPILL_DIR": true, "pogo_web": true,
		"PATH": false, "HOME": false, "EDITOR": false,
		// The documented collateral: *KEY* matches MONKEY. Cheap direction —
		// an explicit env entry hands it back.
		"MONKEY": true,
	} {
		if got := sensitiveEnvName(name); got != want {
			t.Errorf("sensitiveEnvName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestFingerprint(t *testing.T) {
	a := fingerprint(&ServerConfig{URL: "https://e.com", IncludeTools: []string{"a", "b"}})
	if a != fingerprint(&ServerConfig{URL: "https://e.com", IncludeTools: []string{"a", "b"}}) {
		t.Fatal("fingerprint must be deterministic")
	}
	if a == fingerprint(&ServerConfig{URL: "https://e.com", IncludeTools: []string{"a"}}) {
		t.Fatal("fingerprint must change with the configuration")
	}
	if a == fingerprint(&ServerConfig{URL: "https://e.com"}) {
		t.Fatal("an omitted field must change the fingerprint")
	}
}

func TestBackoffShape(t *testing.T) {
	// Full jitter makes exact values untestable; bounds and monotony of the
	// ceiling are the contract.
	for attempt, wantCap := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 6: backoffCap, 100: backoffCap} {
		for i := 0; i < 50; i++ {
			if d := backoff(attempt); d < 0 || d > wantCap {
				t.Fatalf("backoff(%d) = %v, outside [0, %v]", attempt, d, wantCap)
			}
		}
	}
}

// TestPoolStatusFields pins the /api/mcp/status projection shape so the web
// layer can rely on it.
func TestPoolStatusFields(t *testing.T) {
	url := newFixture(t)
	p := newPool(t)
	p.Reconcile(context.Background(), Config{Servers: map[string]*ServerConfig{"fx": {URL: url}}})
	if err := p.WaitReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _, status := p.Snapshot()
	if len(status) != 1 {
		t.Fatalf("status = %#v", status)
	}
	s := status[0]
	if s.Name != "fx" || s.Since.IsZero() {
		t.Fatalf("status = %#v", s)
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"name"`, `"state"`, `"tools"`, `"protocol_version"`, `"server_name"`, `"since"`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("status json missing %s: %s", key, data)
		}
	}
}

func TestPoolCatalogs(t *testing.T) {
	url := newFixture(t)
	p := newPool(t)
	p.Reconcile(context.Background(), Config{Servers: map[string]*ServerConfig{"fx": {URL: url}}})
	if err := p.WaitReady(context.Background()); err != nil {
		t.Fatal(err)
	}

	catalogs := p.Catalogs(nil)
	if len(catalogs) != 1 {
		t.Fatalf("catalogs = %#v, want one server", catalogs)
	}
	cat := catalogs[0]
	if cat.Status.Name != "fx" || cat.Status.State != StateConnected {
		t.Fatalf("catalog status = %#v", cat.Status)
	}
	if len(cat.Tools) != 3 {
		t.Fatalf("catalog tools = %d, want the fixture's three", len(cat.Tools))
	}
	got := map[string]bool{}
	for _, tl := range cat.Tools {
		got[tl.Name()] = true
	}
	if !got["mcp_fx_echo"] || !got["mcp_fx_mal_search"] || !got["mcp_fx_mal_exec"] {
		t.Fatalf("catalog wire names = %v", got)
	}

	// The selection narrows like SnapshotServers: a miss is empty, not nil-ness games.
	if narrowed := p.Catalogs(map[string]bool{"other": true}); len(narrowed) != 0 {
		t.Fatalf("narrowed catalogs = %#v, want empty", narrowed)
	}
	if narrowed := p.Catalogs(map[string]bool{"fx": true}); len(narrowed) != 1 {
		t.Fatalf("narrowed catalogs = %#v, want fx only", narrowed)
	}
}
