package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/mcpclient"
	"github.com/yosukeno/pogo/tools"
)

type catalogModelCall struct {
	schemas  []llm.ToolSchema
	messages []llm.Message
	reply    chan llm.Response
}

// Each model call waits for the test, allowing catalog changes between model
// turns and while a run is executing without timing-dependent model stubs.
type catalogModel struct{ requests chan catalogModelCall }

func (c *catalogModel) Model() string { return "k3" }
func (c *catalogModel) Stream(ctx context.Context, _ string, messages []llm.Message, schemas []llm.ToolSchema, _ func(llm.Delta)) (llm.Response, error) {
	call := catalogModelCall{schemas: append([]llm.ToolSchema(nil), schemas...), messages: append([]llm.Message(nil), messages...), reply: make(chan llm.Response, 1)}
	select {
	case c.requests <- call:
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
	select {
	case resp := <-call.reply:
		return resp, nil
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
}
func (c *catalogModel) next(t *testing.T) catalogModelCall {
	t.Helper()
	select {
	case call := <-c.requests:
		return call
	case <-time.After(10 * time.Second):
		t.Fatal("model never received the run's catalog")
	}
	return catalogModelCall{}
}

type webCatalogFixture struct {
	srv    *mcp.Server
	online atomic.Bool
	calls  atomic.Int64
	lists  atomic.Int64
	url    string
	hold   atomic.Pointer[catalogHold]
}

type catalogHold struct {
	entered chan struct{}
	release chan struct{}
}

func newWebCatalogFixture(t *testing.T, notify, online bool) *webCatalogFixture {
	t.Helper()
	f := &webCatalogFixture{srv: mcp.NewServer(&mcp.Implementation{Name: "catalog-fixture"}, &mcp.ServerOptions{
		PageSize: 1, Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: notify}},
	})}
	f.online.Store(online)
	f.srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/list" {
				f.lists.Add(1)
				if hold := f.hold.Load(); hold != nil {
					select {
					case hold.entered <- struct{}{}:
					default:
					}
					select {
					case <-hold.release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
			}
			return next(ctx, method, req)
		}
	})
	f.put("echo", "original", "text", false)
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return f.srv }, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !f.online.Load() {
			http.Error(w, "offline", http.StatusServiceUnavailable)
			return
		}
		// Exercise HTTP servers that cannot maintain a notification GET stream.
		if !notify && r.Method == http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	f.url = ts.URL
	t.Cleanup(ts.Close)
	return f
}

func (f *webCatalogFixture) put(name, desc, arg string, readOnly bool) {
	f.srv.AddTool(&mcp.Tool{
		Name: name, Description: desc,
		InputSchema:  map[string]any{"type": "object", "properties": map[string]any{arg: map[string]any{"type": "string"}}, "required": []string{arg}, "additionalProperties": false},
		OutputSchema: map[string]any{"type": "object", "properties": map[string]any{"version": map[string]any{"type": "string", "const": desc}}},
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: readOnly, IdempotentHint: readOnly, Title: desc},
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		f.calls.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: desc + ":" + string(req.Params.Arguments)}}, StructuredContent: map[string]any{"version": desc}}, nil
	})
}

func newCatalogHarness(t *testing.T, url string) (*harness, *catalogModel) {
	t.Helper()
	store := filepath.Join(t.TempDir(), "mcp.json")
	if err := mcpclient.Save(store, mcpclient.Config{Servers: map[string]*mcpclient.ServerConfig{"remote": {URL: url, Trust: true, StartupTimeoutMs: 5000}}}); err != nil {
		t.Fatal(err)
	}
	model := &catalogModel{requests: make(chan catalogModelCall, 16)}
	h := newHarness(t, nil, func(c *Config) {
		c.MCPClientStore = store
		c.NewClient = func(config.Resolved, func(llm.RetryInfo)) llm.Client { return model }
	})
	return h, model
}

func waitCatalog(t *testing.T, pool *mcpclient.Pool, ready func([]tools.Tool, []mcpclient.Status) bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		list, _, status := pool.Snapshot()
		if ready(list, status) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	list, _, status := pool.Snapshot()
	t.Fatalf("catalog did not settle: tools=%v status=%+v", list, status)
}

func catalogHas(name, description string) func([]tools.Tool, []mcpclient.Status) bool {
	return func(list []tools.Tool, _ []mcpclient.Status) bool {
		for _, tool := range list {
			if tool.Name() == name && tool.Description() == description {
				return true
			}
		}
		return false
	}
}

func modelSchema(t *testing.T, call catalogModelCall, name string) llm.ToolSchema {
	t.Helper()
	for _, schema := range call.schemas {
		if schema.Name == name {
			return schema
		}
	}
	t.Fatalf("model was not offered %s", name)
	return llm.ToolSchema{}
}

func withoutModelTool(t *testing.T, call catalogModelCall, name string) {
	t.Helper()
	for _, schema := range call.schemas {
		if schema.Name == name {
			t.Fatalf("model was still offered %s", name)
		}
	}
}

func startCatalogRun(t *testing.T, s *Session, model *catalogModel) catalogModelCall {
	t.Helper()
	if _, err := s.Start("continue", nil, ""); err != nil {
		t.Fatal(err)
	}
	return model.next(t)
}

func finishCatalogRun(t *testing.T, s *Session, call catalogModelCall) {
	t.Helper()
	call.reply <- textTurn("done")
	deadline := time.Now().Add(5 * time.Second)
	for s.Active() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s.Active() {
		t.Fatal("run did not finish")
	}
}

func TestMCPSessionNextRunRefreshesCatalog(t *testing.T) {
	f := newWebCatalogFixture(t, true, true)
	h, model := newCatalogHarness(t, f.url)
	waitCatalog(t, h.mgr.mcpPool, catalogHas("mcp_remote_echo", "original"))
	s, err := h.mgr.Create("", "")
	if err != nil {
		t.Fatal(err)
	}
	first := startCatalogRun(t, s, model)
	if modelSchema(t, first, "mcp_remote_echo").Description != "original" {
		t.Fatal("bad initial definition")
	}
	finishCatalogRun(t, s, first)
	agentBefore, pathBefore, cwdBefore := s.agent, s.Path(), s.cwd
	f.put("added", "new capability", "query", true)
	waitCatalog(t, h.mgr.mcpPool, catalogHas("mcp_remote_added", "new capability"))
	added := startCatalogRun(t, s, model)
	modelSchema(t, added, "mcp_remote_added")
	if len(added.messages) <= len(first.messages) {
		t.Fatal("refresh discarded history")
	}
	finishCatalogRun(t, s, added)
	f.put("echo", "updated description", "value", true)
	waitCatalog(t, h.mgr.mcpPool, catalogHas("mcp_remote_echo", "updated description"))
	updated := startCatalogRun(t, s, model)
	schema := modelSchema(t, updated, "mcp_remote_echo")
	if schema.Description != "updated description" || schema.InputSchema["properties"].(map[string]any)["value"] == nil {
		t.Fatalf("stale schema: %+v", schema)
	}
	updated.reply <- toolTurn("mcp_remote_echo", `{"value":"new arguments"}`)
	result := model.next(t)
	if !strings.Contains(result.messages[len(result.messages)-1].Text(), "updated description") {
		// Tool-result blocks are not included by Message.Text(). Inspect their text.
		data, _ := json.Marshal(result.messages[len(result.messages)-1])
		if !strings.Contains(string(data), "updated description") {
			t.Fatalf("execution used stale binding: %s", data)
		}
	}
	finishCatalogRun(t, s, result)
	proj, _ := h.mgr.mcpProjection(nil, nil)
	for _, tool := range proj {
		if tool.Name() == "mcp_remote_echo" {
			mt := tool.(*tools.MCPTool)
			if mt.ExecutionMode() != tools.Parallel || mt.Annotations().Title != "updated description" || mt.OutputSchema()["properties"].(map[string]any)["version"].(map[string]any)["const"] != "updated description" {
				t.Fatal("output schema or annotations stayed stale")
			}
		}
	}
	f.srv.RemoveTools("added")
	waitCatalog(t, h.mgr.mcpPool, func(list []tools.Tool, _ []mcpclient.Status) bool { return len(list) == 1 })
	deleted := startCatalogRun(t, s, model)
	withoutModelTool(t, deleted, "mcp_remote_added")
	modelSchema(t, deleted, "read")
	modelSchema(t, deleted, "todo")
	finishCatalogRun(t, s, deleted)
	if s.agent != agentBefore || s.Path() != pathBefore || s.cwd != cwdBefore {
		t.Fatal("refresh recreated the conversation or workspace")
	}
}

func TestMCPActiveRunKeepsSnapshotAndRejectsChangedBinding(t *testing.T) {
	f := newWebCatalogFixture(t, true, true)
	h, model := newCatalogHarness(t, f.url)
	waitCatalog(t, h.mgr.mcpPool, catalogHas("mcp_remote_echo", "original"))
	s, err := h.mgr.Create("", "")
	if err != nil {
		t.Fatal(err)
	}
	first := startCatalogRun(t, s, model)
	f.put("added", "next run only", "text", false)
	waitCatalog(t, h.mgr.mcpPool, catalogHas("mcp_remote_added", "next run only"))
	first.reply <- toolTurn("mcp_remote_echo", `{"text":"still valid"}`)
	second := model.next(t)
	withoutModelTool(t, second, "mcp_remote_added")
	if f.calls.Load() != 1 {
		t.Fatal("unrelated catalog change broke an unchanged call")
	}
	f.put("echo", "new version", "value", false)
	waitCatalog(t, h.mgr.mcpPool, catalogHas("mcp_remote_echo", "new version"))
	second.reply <- toolTurn("mcp_remote_echo", `{"text":"old plan"}`)
	third := model.next(t)
	if modelSchema(t, third, "mcp_remote_echo").Description != "original" {
		t.Fatal("active model loop switched definitions")
	}
	last, _ := json.Marshal(third.messages[len(third.messages)-1])
	if !strings.Contains(string(last), "tool call was not sent") || !strings.Contains(string(last), "changed") {
		t.Fatalf("missing explicit stale-binding error: %s", last)
	}
	if f.calls.Load() != 1 {
		t.Fatal("stale call reached server or was replayed")
	}
	finishCatalogRun(t, s, third)
	next := startCatalogRun(t, s, model)
	if modelSchema(t, next, "mcp_remote_echo").Description != "new version" {
		t.Fatal("next run did not switch definitions")
	}
	modelSchema(t, next, "mcp_remote_added")
	finishCatalogRun(t, s, next)
}

func TestMCPSessionLateStartupAndRecovery(t *testing.T) {
	f := newWebCatalogFixture(t, true, false)
	h, model := newCatalogHarness(t, f.url)
	_ = h.mgr.mcpPool.WaitReady(context.Background())
	s, err := h.mgr.Create("", "")
	if err != nil {
		t.Fatal(err)
	}
	first := startCatalogRun(t, s, model)
	withoutModelTool(t, first, "mcp_remote_echo")
	finishCatalogRun(t, s, first)
	f.online.Store(true)
	waitCatalog(t, h.mgr.mcpPool, catalogHas("mcp_remote_echo", "original"))
	recovered := startCatalogRun(t, s, model)
	modelSchema(t, recovered, "mcp_remote_echo")
	finishCatalogRun(t, s, recovered)
	f.online.Store(false)
	for session := range f.srv.Sessions() {
		_ = session.Close()
	}
	waitCatalog(t, h.mgr.mcpPool, func(list []tools.Tool, status []mcpclient.Status) bool {
		return len(list) == 0 && len(status) == 1 && status[0].State == mcpclient.StateDisconnected && status[0].Tools == 1 && status[0].CatalogStale && status[0].LastError != ""
	})
	down := startCatalogRun(t, s, model)
	withoutModelTool(t, down, "mcp_remote_echo")
	finishCatalogRun(t, s, down)
	f.srv.RemoveTools("echo")
	f.put("after_restart", "recovered catalog", "text", false)
	f.online.Store(true)
	waitCatalog(t, h.mgr.mcpPool, catalogHas("mcp_remote_after_restart", "recovered catalog"))
	last := startCatalogRun(t, s, model)
	withoutModelTool(t, last, "mcp_remote_echo")
	modelSchema(t, last, "mcp_remote_after_restart")
	finishCatalogRun(t, s, last)
}

func TestMCPSessionPollsWithoutNotifications(t *testing.T) {
	f := newWebCatalogFixture(t, false, true)
	h, model := newCatalogHarness(t, f.url)
	waitCatalog(t, h.mgr.mcpPool, catalogHas("mcp_remote_echo", "original"))
	s, err := h.mgr.Create("", "")
	if err != nil {
		t.Fatal(err)
	}
	first := startCatalogRun(t, s, model)
	finishCatalogRun(t, s, first)
	f.put("silent", "no notification", "text", false)
	withinTTL := startCatalogRun(t, s, model)
	withoutModelTool(t, withinTTL, "mcp_remote_silent")
	finishCatalogRun(t, s, withinTTL)
	if f.lists.Load() != 1 {
		t.Fatalf("TTL did not suppress polling: %d", f.lists.Load())
	}
	_, _, statuses := h.mgr.mcpPool.Snapshot()
	if delay := time.Until(statuses[0].CatalogUpdatedAt.Add(mcpclient.CatalogRefreshTTL + 20*time.Millisecond)); delay > 0 {
		time.Sleep(delay)
	}
	next := startCatalogRun(t, s, model)
	modelSchema(t, next, "mcp_remote_silent")
	if f.lists.Load() != 3 {
		t.Fatalf("expected complete two-page polling refresh: %d requests", f.lists.Load())
	}
	finishCatalogRun(t, s, next)
}

func TestMCPSessionAccountCatalogAndExecutionIsolation(t *testing.T) {
	a := newWebCatalogFixture(t, true, true)
	b := newWebCatalogFixture(t, true, true)
	a.put("echo", "alice identity", "text", false)
	b.put("echo", "bob identity", "text", false)
	aHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return a.srv }, nil)
	bHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return b.srv }, nil)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer alice-fixture-token":
			aHandler.ServeHTTP(w, r)
		case "Bearer bob-fixture-token":
			bHandler.ServeHTTP(w, r)
		default:
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}
	}))
	t.Cleanup(endpoint.Close)
	h, model := newCatalogHarness(t, endpoint.URL)
	alice, bob := identity.LocalScope("alice"), identity.LocalScope("bob")
	h.mgr.SetMCPUserTokens(func() map[identity.Scope]map[string]string {
		return map[identity.Scope]map[string]string{alice: {"remote": "alice-fixture-token"}, bob: {"remote": "bob-fixture-token"}}
	})
	for _, scope := range []identity.Scope{alice, bob} {
		if err := os.MkdirAll(h.mgr.UserRoot(scope), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sa, err := h.mgr.CreateAs("", "", &alice)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := h.mgr.CreateAs("", "", &bob)
	if err != nil {
		t.Fatal(err)
	}
	waitCatalog(t, h.mgr.poolFor(&alice), catalogHas("mcp_remote_echo", "alice identity"))
	waitCatalog(t, h.mgr.poolFor(&bob), catalogHas("mcp_remote_echo", "bob identity"))
	// Sessions exist before one account's capabilities change.
	a.put("alice_only", "private capability", "text", false)
	waitCatalog(t, h.mgr.poolFor(&alice), catalogHas("mcp_remote_alice_only", "private capability"))
	for _, item := range []struct {
		s        *Session
		identity string
		private  bool
	}{{sa, "alice identity", true}, {sb, "bob identity", false}} {
		call := startCatalogRun(t, item.s, model)
		if modelSchema(t, call, "mcp_remote_echo").Description != item.identity {
			t.Fatal("account received another account's schema")
		}
		if item.private {
			modelSchema(t, call, "mcp_remote_alice_only")
		} else {
			withoutModelTool(t, call, "mcp_remote_alice_only")
		}
		call.reply <- toolTurn("mcp_remote_echo", `{"text":"who am I"}`)
		result := model.next(t)
		data, _ := json.Marshal(result.messages[len(result.messages)-1])
		if !strings.Contains(string(data), item.identity) {
			t.Fatalf("execution used another account's credential: %s", data)
		}
		finishCatalogRun(t, item.s, result)
	}
	if a.calls.Load() != 1 || b.calls.Load() != 1 {
		t.Fatalf("calls crossed accounts: alice=%d bob=%d", a.calls.Load(), b.calls.Load())
	}
}

func TestMCPPolicyRefreshRevokesOnlyChangedGrants(t *testing.T) {
	p := NewPolicy()
	old, stable := mcpToolForTest(t, "remote", "echo"), mcpToolForTest(t, "remote", "stable")
	p.ReplaceMCP([]tools.Tool{old, stable}, nil)
	p.AllowTool(old.Name())
	p.AllowTool(stable.Name())
	p.AllowTool("bash")
	p.AllowToolCommand(old.Name(), "do-work")
	updated := mcpToolForTest(t, "remote", "echo")
	p.ReplaceMCP([]tools.Tool{updated, stable}, nil)
	if _, auto := p.Decide(agent.GateRequest{ToolName: updated.Name(), Args: json.RawMessage(`{"command":"do-work"}`)}); auto {
		t.Fatal("changed tool inherited old tool or command approval")
	}
	for _, name := range []string{stable.Name(), "bash"} {
		if _, auto := p.Decide(agent.GateRequest{ToolName: name}); !auto {
			t.Fatalf("unrelated approval revoked: %s", name)
		}
	}
	p.AllowCommand("shell-grant")
	if _, auto := p.Decide(agent.GateRequest{ToolName: updated.Name(), Args: json.RawMessage(`{"command":"shell-grant"}`)}); auto {
		t.Fatal("MCP inherited a shell approval")
	}
	p.ReplaceMCP([]tools.Tool{updated}, []string{updated.Name()})
	if _, auto := p.Decide(agent.GateRequest{ToolName: updated.Name()}); !auto {
		t.Fatal("explicit current server trust not applied")
	}
	p.ReplaceMCP(nil, nil)
	if _, auto := p.Decide(agent.GateRequest{ToolName: updated.Name()}); auto {
		t.Fatal("removed tool retained server trust")
	}
}

func TestMCPMetadataChangesRevokeRememberedApproval(t *testing.T) {
	f := newWebCatalogFixture(t, true, true)
	h, model := newCatalogHarness(t, f.url)
	h.mgr.ReconcileMCP(context.Background(), mcpclient.Config{Servers: map[string]*mcpclient.ServerConfig{
		"remote": {URL: f.url, Trust: false},
	}})
	waitCatalog(t, h.mgr.mcpPool, catalogHas("mcp_remote_echo", "original"))
	s, err := h.mgr.Create("", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"outputSchema", "annotations"} {
		t.Run(field, func(t *testing.T) {
			proj, _ := h.mgr.mcpProjection(nil, nil)
			before := proj[0].(*tools.MCPTool)
			s.policy.AllowTool(before.Name())
			if _, auto := s.policy.Decide(agent.GateRequest{ToolName: before.Name()}); !auto {
				t.Fatal("grant did not apply")
			}
			spec := &mcp.Tool{Name: "echo", Description: before.Description(), InputSchema: before.InputSchema(), OutputSchema: before.OutputSchema(), Annotations: before.Annotations()}
			if field == "outputSchema" {
				spec.OutputSchema.(map[string]any)["properties"].(map[string]any)["extra"] = map[string]any{"type": "boolean"}
			} else {
				spec.Annotations.Title = "changed safety/display metadata"
				unsafe := true
				spec.Annotations.DestructiveHint = &unsafe
			}
			f.srv.AddTool(spec, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{}, nil
			})
			waitCatalog(t, h.mgr.mcpPool, func(list []tools.Tool, _ []mcpclient.Status) bool {
				return len(list) == 1 && list[0].(*tools.MCPTool).DefinitionFingerprint() != before.DefinitionFingerprint()
			})
			call := startCatalogRun(t, s, model)
			if _, auto := s.policy.Decide(agent.GateRequest{ToolName: before.Name()}); auto {
				t.Fatal("metadata-only change inherited prior approval")
			}
			if modelSchema(t, call, before.Name()).Description != before.Description() {
				t.Fatal("test changed the description instead of metadata alone")
			}
			finishCatalogRun(t, s, call)
		})
	}
}

func TestMCPSteeringDuringCatalogPreparationIsPreserved(t *testing.T) {
	f := newWebCatalogFixture(t, true, true)
	hold := &catalogHold{entered: make(chan struct{}, 1), release: make(chan struct{})}
	f.hold.Store(hold)
	h, model := newCatalogHarness(t, f.url)
	select {
	case <-hold.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("initial catalog pull did not start")
	}
	s, err := h.mgr.Create("", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start("original prompt", nil, ""); err != nil {
		t.Fatal(err)
	}
	// No model loop exists yet, and the catalog is deliberately blocked.
	if !s.Steer("additional instruction") {
		t.Fatal("run rejected input while refreshing its catalog")
	}
	if s.Steer("   ") {
		t.Fatal("empty steering message accepted")
	}
	close(hold.release)
	call := model.next(t)
	modelSchema(t, call, "mcp_remote_echo")
	if len(call.messages) != 2 || call.messages[0].Text() != "original prompt" || call.messages[1].Text() != "additional instruction" {
		t.Fatalf("prepared run lost or reordered input: %+v", call.messages)
	}
	finishCatalogRun(t, s, call)
	var users []string
	for _, message := range s.Hub().Snapshot().Messages {
		if message.Role == "user" {
			for _, block := range message.Content {
				if block.Text != "" {
					users = append(users, block.Text)
				}
			}
		}
	}
	if len(users) != 2 || users[1] != "additional instruction" {
		t.Fatalf("steering missing from timeline: %v", users)
	}
}
