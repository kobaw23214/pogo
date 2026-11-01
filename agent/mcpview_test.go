package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

// viewCaller records tools/call invocations and answers with a text result.
type viewCaller struct {
	mu    sync.Mutex
	calls []string
}

func (c *viewCaller) call(_ context.Context, toolName string, _ json.RawMessage) (*mcp.CallToolResult, error) {
	c.mu.Lock()
	c.calls = append(c.calls, toolName)
	c.mu.Unlock()
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "called " + toolName}}}, nil
}

func (c *viewCaller) called(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, n := range c.calls {
		if n == name {
			return true
		}
	}
	return false
}

// synthCatalog builds n neutral synthetic MCP tools on one synthetic server.
// Every fortieth tool is a directory-role navigation hint; the rest are plain
// operations with no catalog metadata at all.
func synthCatalog(t *testing.T, caller *viewCaller, n int) []tools.Tool {
	t.Helper()
	out := make([]tools.Tool, 0, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("op-%03d", i)
		spec := &mcp.Tool{
			Name:        name,
			Description: fmt.Sprintf("synthetic operation %d for layered discovery tests", i),
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"query": map[string]any{"type": "string"},
			}},
		}
		if i%40 == 0 {
			spec.Meta = map[string]any{tools.MCPCatalogMetaKey: map[string]any{
				"group": fmt.Sprintf("block-%02d", i/40), "role": "directory",
			}}
		}
		mt, err := tools.NewMCPTool("synthetic", spec, 0, caller.call)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, mt)
	}
	return out
}

// viewRegistry builds a session registry over the synthetic catalog, with the
// genuine helper installed by tools.New.
func viewRegistry(t *testing.T, catalog []tools.Tool) *tools.Registry {
	t.Helper()
	return tools.New(tools.Options{Cwd: t.TempDir(), MCP: catalog, SpillDir: t.TempDir()})
}

// viewClient replays scripted responses and records the schema list of every
// request — the actual model-visible tool view.
type viewClient struct {
	mu        sync.Mutex
	responses []llm.Response
	schemas   [][]llm.ToolSchema
}

func (c *viewClient) Model() string { return "view-fixture" }

func (c *viewClient) Stream(_ context.Context, _ string, _ []llm.Message, schemas []llm.ToolSchema, _ func(llm.Delta)) (llm.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.schemas = append(c.schemas, append([]llm.ToolSchema(nil), schemas...))
	if len(c.responses) == 0 {
		return llm.Response{
			Message:    llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "done"}}},
			StopReason: llm.StopEndTurn,
		}, nil
	}
	r := c.responses[0]
	c.responses = c.responses[1:]
	return r, nil
}

func (c *viewClient) requestSchemas(n int) []llm.ToolSchema {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n >= len(c.schemas) {
		return nil
	}
	return c.schemas[n]
}

func schemaNames(schemas []llm.ToolSchema) map[string]bool {
	out := make(map[string]bool, len(schemas))
	for _, s := range schemas {
		out[s.Name] = true
	}
	return out
}

func toolUseResp(id, name, args string) llm.Response {
	return llm.Response{
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
			Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(args),
		}}},
		StopReason: llm.StopToolUse,
	}
}

func runAndDrain(t *testing.T, a *Agent, prompt string) {
	t.Helper()
	for ev := range a.Run(context.Background(), prompt) {
		if ev.Kind == EventAgentEnd && ev.Err != nil {
			t.Fatalf("run failed: %v", ev.Err)
		}
	}
}

func TestMCPViewEagerBelowThreshold(t *testing.T) {
	caller := &viewCaller{}
	reg := viewRegistry(t, synthCatalog(t, caller, 5))
	a := New(Config{Client: &viewClient{}, Registry: reg, SystemPrompt: "s"})
	if a.mcpDeferred {
		t.Fatal("small catalog deferred")
	}
	names := schemaNames(a.schemas)
	for i := 0; i < 5; i++ {
		if !names[fmt.Sprintf("mcp_synthetic_op-%03d", i)] {
			t.Fatalf("eager view lost mcp_synthetic_op-%03d", i)
		}
	}
	if !names[tools.MCPDescribeName] || !names["read"] || !names["bash"] {
		t.Fatal("eager view lost local tools or helper")
	}
}

func TestMCPViewDeferredInitialBounds(t *testing.T) {
	caller := &viewCaller{}
	reg := viewRegistry(t, synthCatalog(t, caller, 80))
	a := New(Config{Client: &viewClient{}, Registry: reg, SystemPrompt: "s"})
	if !a.mcpDeferred {
		t.Fatal("large catalog stayed eager")
	}
	names := schemaNames(a.schemas)
	mcpShown, dirs := 0, 0
	for _, s := range a.schemas {
		if strings.HasPrefix(s.Name, "mcp_synthetic_") {
			mcpShown++
			tool, _ := reg.Get(s.Name)
			if tool.(*tools.MCPTool).CatalogRole() == tools.MCPCatalogRoleDirectory {
				dirs++
			}
		}
	}
	if mcpShown == 0 || mcpShown > mcpViewMaxDirectories || dirs != mcpShown {
		t.Fatalf("initial deferred view shows %d MCP tools (%d dirs), want only <=%d directories",
			mcpShown, dirs, mcpViewMaxDirectories)
	}
	if names["mcp_synthetic_op-001"] {
		t.Fatal("plain operation visible in initial deferred view")
	}
	if !names[tools.MCPDescribeName] || !names["read"] {
		t.Fatal("helper or local tool missing from deferred view")
	}
	// Non-MCP instances and order are preserved at the head of the view.
	if a.schemas[0].Name != "read" {
		t.Fatalf("non-MCP order changed: first schema is %s", a.schemas[0].Name)
	}
	// Everything hidden stays indexed for the helper.
	helper, _ := reg.Get(tools.MCPDescribeName)
	res, err := helper.Execute(context.Background(), json.RawMessage(`{"filter":"op-079","limit":1}`))
	if err != nil || !strings.Contains(res.Text, "mcp_synthetic_op-079") {
		t.Fatalf("hidden tool not discoverable: %v\n%s", err, res.Text)
	}
}

func TestMCPViewByteThresholdDefers(t *testing.T) {
	caller := &viewCaller{}
	props := map[string]any{}
	for i := 0; i < 4000; i++ {
		props[fmt.Sprintf("field-%04d", i)] = map[string]any{"type": "string", "description": "synthetic field"}
	}
	spec := &mcp.Tool{Name: "wide", Description: "wide",
		InputSchema: map[string]any{"type": "object", "properties": props}}
	mt, err := tools.NewMCPTool("synthetic", spec, 0, caller.call)
	if err != nil {
		t.Fatal(err)
	}
	reg := viewRegistry(t, []tools.Tool{mt})
	a := New(Config{Client: &viewClient{}, Registry: reg, SystemPrompt: "s"})
	if !a.mcpDeferred {
		t.Fatal("single oversized schema stayed eager")
	}
	if schemaNames(a.schemas)["mcp_synthetic_wide"] {
		t.Fatal("oversized operation visible before selection")
	}
}

func TestMCPViewHelperCollisionFailsOpen(t *testing.T) {
	caller := &viewCaller{}
	catalog := synthCatalog(t, caller, 40)
	// A caller-owned tool named mcp_describe shadows the helper.
	own := &fakeTool{name: tools.MCPDescribeName, mode: tools.Parallel}
	all := append([]tools.Tool{own}, catalog...)
	reg := tools.NewRegistry(all...)
	a := New(Config{Client: &viewClient{}, Registry: reg, SystemPrompt: "s"})
	if a.mcpDeferred {
		t.Fatal("shadowed helper did not fail open to eager visibility")
	}
	if !schemaNames(a.schemas)["mcp_synthetic_op-039"] {
		t.Fatal("fail-open view lost a tool")
	}
	// NewRegistry alone never installs the helper: same fail-open.
	a2 := New(Config{Client: &viewClient{}, Registry: tools.NewRegistry(catalog...), SystemPrompt: "s"})
	if a2.mcpDeferred {
		t.Fatal("missing helper did not fail open to eager visibility")
	}
}

// TestMCPViewDetailSelectsForNextRequest is the core loop test: the initial
// request hides operations, a successful detail call exposes exactly that
// tool's native schema on the next request, and the tool then runs through
// the normal path (validation, bound caller) unchanged.
func TestMCPViewDetailSelectsForNextRequest(t *testing.T) {
	caller := &viewCaller{}
	reg := viewRegistry(t, synthCatalog(t, caller, 60))
	client := &viewClient{responses: []llm.Response{
		toolUseResp("c1", tools.MCPDescribeName, `{"tool":"mcp_synthetic_op-059"}`),
		toolUseResp("c2", "mcp_synthetic_op-059", `{"query":"late item"}`),
	}}
	a := New(Config{Client: client, Registry: reg, SystemPrompt: "s"})
	runAndDrain(t, a, "go")

	if schemaNames(client.requestSchemas(0))["mcp_synthetic_op-059"] {
		t.Fatal("hidden tool visible before selection")
	}
	if !schemaNames(client.requestSchemas(1))["mcp_synthetic_op-059"] {
		t.Fatal("successful detail did not expose the native schema on the next request")
	}
	if !schemaNames(client.requestSchemas(2))["mcp_synthetic_op-059"] {
		t.Fatal("selection did not persist into the following request")
	}
	if !caller.called("op-059") {
		t.Fatal("selected tool did not reach its bound caller")
	}
	// Nothing else was activated by the batch.
	if len(a.mcpSelected) != 1 {
		t.Fatalf("unexpected selections: %v", a.mcpSelected)
	}
}

type gateFunc func(GateRequest) GateDecision

func (g gateFunc) Review(_ context.Context, req GateRequest) GateDecision { return g(req) }

func TestMCPViewNonActivatingDescribeCalls(t *testing.T) {
	caller := &viewCaller{}
	reg := viewRegistry(t, synthCatalog(t, caller, 40))
	client := &viewClient{responses: []llm.Response{
		// Index call: no provenance, selects nothing.
		toolUseResp("c1", tools.MCPDescribeName, `{}`),
		// Failed detail: unknown tool, selects nothing.
		toolUseResp("c2", tools.MCPDescribeName, `{"tool":"mcp_synthetic_op-999"}`),
	}}
	a := New(Config{Client: client, Registry: reg, SystemPrompt: "s"})
	runAndDrain(t, a, "go")
	if len(a.mcpSelected) != 0 {
		t.Fatalf("failed/index calls selected: %v", a.mcpSelected)
	}
	if schemaNames(client.requestSchemas(2))["mcp_synthetic_op-010"] {
		t.Fatal("non-detail calls changed the view")
	}

	// Gate denial does not activate either.
	client2 := &viewClient{responses: []llm.Response{
		toolUseResp("c1", tools.MCPDescribeName, `{"tool":"mcp_synthetic_op-010"}`),
	}}
	a2 := New(Config{Client: client2, Registry: reg, SystemPrompt: "s", Gate: gateFunc(func(GateRequest) GateDecision {
		return Deny("no")
	})})
	runAndDrain(t, a2, "go")
	if len(a2.mcpSelected) != 0 {
		t.Fatal("denied describe call activated a tool")
	}
}

// TestMCPViewGateRewriteSelectsActualResult: the gate rewrites the describe
// arguments to name a different tool; the selection must follow the actual
// (rewritten) call and its real result, not the model's original arguments.
func TestMCPViewGateRewriteSelectsActualResult(t *testing.T) {
	caller := &viewCaller{}
	reg := viewRegistry(t, synthCatalog(t, caller, 40))
	client := &viewClient{responses: []llm.Response{
		toolUseResp("c1", tools.MCPDescribeName, `{"tool":"mcp_synthetic_op-001"}`),
	}}
	gate := gateFunc(func(req GateRequest) GateDecision {
		if req.ToolName == tools.MCPDescribeName {
			return GateDecision{Allow: true, Args: json.RawMessage(`{"tool":"mcp_synthetic_op-002"}`)}
		}
		return Allow
	})
	a := New(Config{Client: client, Registry: reg, SystemPrompt: "s", Gate: gate})
	runAndDrain(t, a, "go")
	if len(a.mcpSelected) != 1 || a.mcpSelected[0].name != "mcp_synthetic_op-002" {
		t.Fatalf("rewrite not honored: %v", a.mcpSelected)
	}
	if schemaNames(client.requestSchemas(1))["mcp_synthetic_op-001"] {
		t.Fatal("model-requested (rewritten-away) tool was activated")
	}
}
