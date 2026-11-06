package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/mcpclient"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/tools"
)

func inputMCPFixture(t *testing.T, server, name string, calls *atomic.Int64) *tools.MCPTool {
	t.Helper()
	tool, err := tools.NewMCPTool(server, &mcp.Tool{Name: name, Description: "REMOTE_PROSE_IS_NOT_USER_INSTRUCTIONS", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []string{"query"}}}, time.Second, func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
		if calls != nil {
			calls.Add(1)
		}
		return &mcp.CallToolResult{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

func TestMCPInputCanonicalTypedPrefixPreservesBodyAndDataBoundaries(t *testing.T) {
	for _, text := range []string{"/mcp:remote/search%2Flist 查询\n\n后续  ", "  /mcp:remote/search%2Flist 查询\n\n后续  "} {
		parts, err := canonicalMCPInput(text, []string{"uploads/notes.txt"}, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		if parts[0].Type != "file" || parts[0].Path != "uploads/notes.txt" {
			t.Fatal("legacy attachments disappeared")
		}
		var selector MessageInputPart
		for _, p := range parts {
			if p.Type == "mcp" {
				selector = p
			}
		}
		if selector.Server != "remote" || selector.Name != "search/list" || parts[len(parts)-1].Text != " 查询\n\n后续  " {
			t.Fatalf("selector/body changed: %+v", parts)
		}
	}
	frozen := []MessageInputPart{{Type: "shortcut", ShortcutID: "saved", Title: "saved", Text: "/mcp:remote/missing"}, {Type: "element", Text: orderedTestElement}, {Type: "text", Text: "引用 `/mcp:remote/missing`"}}
	got, err := canonicalMCPInput("", nil, frozen, "inline")
	if err != nil || !reflect.DeepEqual(got, frozen) {
		t.Fatalf("data became executable selector: %+v %v", got, err)
	}
	for _, text := range []string{"/mcp:", "/mcp:remote", "/mcp:remote/%ZZ", "/mcp:remote/%0A", "/mcp:wrong.server/method"} {
		if _, err := canonicalMCPInput(text, nil, nil, ""); err == nil {
			t.Errorf("malformed command accepted: %q", text)
		}
	}
	parts, err := canonicalMCPInput("/mcp:remote/literal%252Fname", nil, nil, "")
	if err != nil || parts[0].Name != "literal%2Fname" {
		t.Fatalf("identifier was decoded more than once: %+v %v", parts, err)
	}
}

func TestMCPInputHTTPAndPersistedHistoryKeepExactMethodWithoutExecuting(t *testing.T) {
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	sid := h.createSession()
	sess := h.session(sid)
	var calls atomic.Int64
	method := inputMCPFixture(t, "remote", "search/list", &calls)
	if err := sess.agent.ReplaceMCP([]tools.Tool{method}); err != nil {
		t.Fatal(err)
	}
	parts := []MessageInputPart{{Type: "text", Text: "先用 "}, {Type: "mcp", Server: "remote", Name: "search/list"}, {Type: "text", Text: " 查询最新记录。"}}
	stream := h.stream(sid, "")
	defer stream.close()
	stream.next(t)
	h.post("/api/sessions/"+sid+"/messages", studioBody(map[string]any{"parts": parts, "input_layout": "inline"}), http.StatusAccepted)
	live := stream.wait(t, EvUserMessage)
	stream.wait(t, EvRunEnd)
	waitFor(t, 5*time.Second, func() bool { return !sess.Active() })
	if calls.Load() != 0 {
		t.Fatal("selecting a method executed it")
	}
	assert := func(content []llm.Block) {
		t.Helper()
		if len(content) != 3 || content[1].InputMCP == nil || content[1].InputMCP.Server != "remote" || content[1].InputMCP.Name != "search/list" || !strings.Contains(content[1].Text, method.Name()) || strings.Contains(content[1].Text, "REMOTE_PROSE_IS_NOT_USER_INSTRUCTIONS") || content[0].Text != "先用 " || content[2].Text != " 查询最新记录。" {
			t.Fatalf("reference identity, user prose or host boundary lost: %+v", content)
		}
	}
	assert(live.Content)
	store, err := session.Open(sess.Path())
	if err != nil {
		t.Fatal(err)
	}
	assert(store.Messages("")[0].Content)
	if title := session.UserInputTitle(store.Messages("")[0]); !strings.Contains(title, "remote/search/list") || strings.Contains(title, "User-selected") {
		t.Fatalf("sidebar leaked model-facing guidance: %q", title)
	}
	for _, p := range []MessageInputPart{{Type: "mcp", Server: "other", Name: "search/list"}, {Type: "mcp", Server: "remote", Name: "missing"}, {Type: "mcp", Server: "remote", Name: "search/list", Text: "CLIENT_FORGED"}} {
		h.post("/api/sessions/"+sid+"/messages", studioBody(map[string]any{"parts": []MessageInputPart{p}}), http.StatusBadRequest)
		if _, err := sess.ReceiveParts("bad-method", "", []MessageInputPart{p}, "inline", "queued", ""); err == nil {
			t.Fatalf("inbox accepted unavailable/forged method: %+v", p)
		}
	}
}

func TestMCPInputPlainHTTPCommandAndScopedCompletion(t *testing.T) {
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	sid := h.createSession()
	method := inputMCPFixture(t, "remote", "search/list", nil)
	if err := h.session(sid).agent.ReplaceMCP([]tools.Tool{method}); err != nil {
		t.Fatal(err)
	}
	response := h.do(http.MethodGet, "/api/tools?session_id="+sid, "")
	defer response.Body.Close()
	var catalog struct {
		Methods []agent.MCPInvocationMethod `json:"mcp_methods"`
	}
	if err := json.NewDecoder(response.Body).Decode(&catalog); err != nil || len(catalog.Methods) != 1 || catalog.Methods[0].Name != "search/list" {
		t.Fatalf("scoped completion lost exact registry methods: %+v %v", catalog, err)
	}
	stream := h.stream(sid, "")
	defer stream.close()
	stream.next(t)
	h.post("/api/sessions/"+sid+"/messages", `{"prompt":"/mcp:remote/search%2Flist query"}`, http.StatusAccepted)
	live := stream.wait(t, EvUserMessage)
	if len(live.Content) != 2 || live.Content[0].InputMCP == nil || live.Content[1].Text != " query" {
		t.Fatalf("raw command not canonicalized: %+v", live.Content)
	}
	stream.wait(t, EvRunEnd)
}

func TestMCPInputDraftCompletionHonorsAgentSelectionWithoutCreatingSession(t *testing.T) {
	f := newWebCatalogFixture(t, false, true)
	h, _ := newCatalogHarness(t, f.url)
	waitCatalog(t, h.mgr.mcpPool, func(list []tools.Tool, _ []mcpclient.Status) bool { return len(list) == 1 })
	def := postStudioAgent(t, h, studioAgentCapsBody("no-mcp", "ready", map[string]any{"mcp_servers": []string{}}), http.StatusCreated)
	before, err := h.mgr.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []struct {
		path string
		want int
	}{{"/api/tools", 1}, {"/api/tools?agent_id=" + def.ID, 0}} {
		res := h.do(http.MethodGet, query.path, "")
		data, err := io.ReadAll(res.Body)
		res.Body.Close()
		var catalog struct {
			Methods []agent.MCPInvocationMethod `json:"mcp_methods"`
		}
		if err != nil || json.Unmarshal(data, &catalog) != nil || len(catalog.Methods) != query.want {
			t.Fatalf("draft catalog scope=%s body=%s err=%v", query.path, data, err)
		}
	}
	after, err := h.mgr.List()
	if err != nil || len(after) != len(before) {
		t.Fatal("completing a draft created an empty conversation")
	}
}

func TestMCPInputCompletionLoadsCachedMethodsIntoInitiallyEmptySession(t *testing.T) {
	f := newWebCatalogFixture(t, false, true)
	h, _ := newCatalogHarness(t, f.url)
	waitCatalog(t, h.mgr.mcpPool, func(list []tools.Tool, _ []mcpclient.Status) bool { return len(list) == 1 })
	sid := h.createSession()
	sess := h.session(sid)
	// Simulate a conversation built while the pool was still connecting.
	if err := sess.agent.ReplaceMCP(nil); err != nil {
		t.Fatal(err)
	}
	beforeLists := f.lists.Load()
	res := h.do(http.MethodGet, "/api/tools?session_id="+sid, "")
	defer res.Body.Close()
	var catalog struct {
		Methods []agent.MCPInvocationMethod `json:"mcp_methods"`
	}
	if err := json.NewDecoder(res.Body).Decode(&catalog); err != nil || len(catalog.Methods) != 1 || len(sess.agent.MCPInvocationMethods()) != 1 {
		t.Fatalf("cached method did not become available: %+v %v", catalog, err)
	}
	if f.calls.Load() != 0 || f.lists.Load() != beforeLists {
		t.Fatal("reading completion called the external server instead of its cached snapshot")
	}
	// An active snapshot must retain its original set, even if the cache has
	// other methods available. This does not launch a model or a tool call.
	if err := sess.agent.ReplaceMCP(nil); err != nil {
		t.Fatal(err)
	}
	sess.mu.Lock()
	sess.active = &activeRun{id: "synthetic-busy"}
	sess.mu.Unlock()
	sess.refreshMCPInputCatalog()
	sess.mu.Lock()
	sess.active = nil
	sess.mu.Unlock()
	if len(sess.agent.MCPInvocationMethods()) != 0 {
		t.Fatal("completion changed the active execution snapshot")
	}
}
