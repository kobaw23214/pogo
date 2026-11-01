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

// promptClient is viewClient plus a record of the system prompt actually sent
// with every model request, so guidance tests can assert on real wire input.
type promptClient struct {
	viewClient
	mu      sync.Mutex
	systems []string
}

func (c *promptClient) Stream(ctx context.Context, system string, prompt []llm.Message, schemas []llm.ToolSchema, obs func(llm.Delta)) (llm.Response, error) {
	c.mu.Lock()
	c.systems = append(c.systems, system)
	c.mu.Unlock()
	return c.viewClient.Stream(ctx, system, prompt, schemas, obs)
}

func (c *promptClient) systemPrompts() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.systems...)
}

// stampingCaller answers each call with different bytes (a fresh stamp), so a
// result-sensitive repeat detector would be fooled into missing real repeats.
type stampingCaller struct {
	mu     sync.Mutex
	counts map[string]int
}

func (c *stampingCaller) call(_ context.Context, toolName string, _ json.RawMessage) (*mcp.CallToolResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.counts == nil {
		c.counts = make(map[string]int)
	}
	c.counts[toolName]++
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{
		Text: fmt.Sprintf("called %s stamp=%d", toolName, c.counts[toolName]),
	}}}, nil
}

func (c *stampingCaller) count(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[name]
}

// annotatedSpec builds one synthetic server tool with explicit annotations.
func annotatedSpec(name string, readonly, directory bool) *mcp.Tool {
	spec := &mcp.Tool{
		Name:        name,
		Description: "synthetic annotated operation for guidance tests",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"query": map[string]any{"type": "string"},
			"opt":   map[string]any{"type": "integer"},
		}},
	}
	if readonly {
		spec.Annotations = &mcp.ToolAnnotations{ReadOnlyHint: true}
	}
	if directory {
		spec.Meta = map[string]any{tools.MCPCatalogMetaKey: map[string]any{"role": "directory"}}
	}
	return spec
}

// guideCatalog is a deferred-size catalog with every advisory case installed
// alongside plain un-annotated synthetic operations.
func guideCatalog(t *testing.T, caller *stampingCaller) (*tools.Registry, *stampingCaller) {
	t.Helper()
	readonly, err := tools.NewMCPTool("synthetic", annotatedSpec("readonly", true, false), 0, caller.call)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := tools.NewMCPTool("synthetic", annotatedSpec("writer", false, false), 0, caller.call)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := tools.NewMCPTool("synthetic", annotatedSpec("nav", true, true), 0, caller.call)
	if err != nil {
		t.Fatal(err)
	}
	return viewRegistry(t, append([]tools.Tool{readonly, writer, dir}, synthCatalog(t, &viewCaller{}, 60)...)), caller
}

func drainEvents(t *testing.T, a *Agent, prompt string) []Event {
	t.Helper()
	var events []Event
	for ev := range a.Run(context.Background(), prompt) {
		if ev.Kind == EventAgentEnd && ev.Err != nil {
			t.Fatalf("run failed: %v", ev.Err)
		}
		events = append(events, ev)
	}
	return events
}

func guideSteerTexts(events []Event) []string {
	var out []string
	for _, ev := range events {
		if ev.Kind == EventSteer {
			out = append(out, ev.Text)
		}
	}
	return out
}

func resultsSettledBefore(events []Event, idx int, wantID string) bool {
	for i := 0; i < idx; i++ {
		if events[i].Kind != EventToolResults {
			continue
		}
		for _, b := range events[i].Message.Content {
			if b.Type == llm.BlockToolResult && b.ToolUseID == wantID {
				return true
			}
		}
	}
	return false
}

// TestMCPGuideSystemPromptFollowsRealView: only the deferred session's actual
// wire requests carry the local discovery guidance; eager and no-MCP sessions
// send the caller's prompt untouched, and the fixed-cost system-token estimate
// always describes the prompt that was really sent.
func TestMCPGuideSystemPromptFollowsRealView(t *testing.T) {
	deferredClient := &promptClient{}
	reg, _ := guideCatalog(t, &stampingCaller{})
	deferredClient.responses = []llm.Response{toolUseResp("c1", "mcp_synthetic_readonly", `{"query":"x"}`)}
	a := New(Config{Client: deferredClient, Registry: reg, SystemPrompt: "base prompt"})
	if !a.mcpDeferred {
		t.Fatal("fixture catalog stayed eager")
	}
	events := drainEvents(t, a, "go")
	if len(deferredClient.systemPrompts()) == 0 {
		t.Fatal("no model request recorded")
	}
	for i, system := range deferredClient.systemPrompts() {
		if !strings.HasPrefix(system, "base prompt") || !strings.Contains(system, tools.MCPDescribeName) {
			t.Fatalf("deferred request %d did not carry deferred-discovery guidance: %q", i, system)
		}
	}
	want := int64(len(a.modelSystemPrompt()) / llm.BytesPerToken)
	if a.systemPromptTokens != want {
		t.Fatalf("deferred systemPromptTokens=%d, want %d", a.systemPromptTokens, want)
	}
	if a.fixedCostTokens < a.systemPromptTokens {
		t.Fatalf("fixedCostTokens=%d below systemPromptTokens=%d", a.fixedCostTokens, a.systemPromptTokens)
	}
	if len(guideSteerTexts(events)) != 0 {
		t.Fatal("single call should not advise")
	}

	eagerClient := &promptClient{}
	eagerReg := viewRegistry(t, synthCatalog(t, &viewCaller{}, 5))
	eager := New(Config{Client: eagerClient, Registry: eagerReg, SystemPrompt: "base prompt"})
	if eager.mcpDeferred {
		t.Fatal("small catalog deferred")
	}
	drainEvents(t, eager, "go")
	for i, system := range eagerClient.systemPrompts() {
		if system != "base prompt" {
			t.Fatalf("eager request %d changed the system prompt: %q", i, system)
		}
	}
	if want := int64(len("base prompt") / llm.BytesPerToken); eager.systemPromptTokens != want {
		t.Fatalf("eager systemPromptTokens=%d, want %d", eager.systemPromptTokens, want)
	}

	noneClient := &promptClient{}
	noneReg := viewRegistry(t, nil)
	none := New(Config{Client: noneClient, Registry: noneReg, SystemPrompt: "base prompt"})
	if none.mcpDeferred {
		t.Fatal("no-MCP session deferred")
	}
	drainEvents(t, none, "go")
	for i, system := range noneClient.systemPrompts() {
		if system != "base prompt" {
			t.Fatalf("no-MCP request %d changed the system prompt: %q", i, system)
		}
	}
}

// TestMCPRepeatAdvisoryNoticesRepeatedReads: two successful read-only calls
// with the same canonical arguments but different IDs, key order, whitespace
// and differing result bytes produce exactly one run-local advisory, after
// complete tool results — and both calls still reach the bound caller and the
// gate.
func TestMCPRepeatAdvisoryNoticesRepeatedReads(t *testing.T) {
	reg, caller := guideCatalog(t, &stampingCaller{})
	gateCalls := 0
	client := &promptClient{}
	client.responses = []llm.Response{
		toolUseResp("c1", "mcp_synthetic_readonly", `{"opt":1,"query":"same"}`),
		toolUseResp("c2", "mcp_synthetic_readonly", `{"query":"same",   "opt":1}`),
		toolUseResp("c3", "mcp_synthetic_readonly", `{"opt":1,"query":"same"}`),
	}
	a := New(Config{Client: client, Registry: reg, SystemPrompt: "s", Gate: gateFunc(func(GateRequest) GateDecision {
		gateCalls++
		return Allow
	})})
	events := drainEvents(t, a, "go")
	steers := guideSteerTexts(events)
	if len(steers) != 1 || !strings.Contains(steers[0], "mcp_synthetic_readonly") {
		t.Fatalf("want exactly one read-repeat advisory, got %d: %q", len(steers), steers)
	}
	if caller.count("readonly") != 3 {
		t.Fatalf("advisory interfered with execution: caller saw %d calls", caller.count("readonly"))
	}
	if gateCalls != 3 {
		t.Fatalf("advisory bypassed or duplicated the gate: %d reviews", gateCalls)
	}
	idx := indexOfSteer(events)
	if !resultsSettledBefore(events, idx, "c2") {
		t.Fatal("advisory emitted before the repeated call's complete tool results")
	}
	if resultsSettledBefore(events, idx, "c3") {
		t.Fatal("third call settled before the advisory fired")
	}

	// A new run starts from a fresh counter even on the same Agent.
	client.responses = []llm.Response{
		toolUseResp("d1", "mcp_synthetic_readonly", `{"opt":1,"query":"same"}`),
		toolUseResp("d2", "mcp_synthetic_readonly", `{"opt":1,"query":"same"}`),
	}
	events2 := drainEvents(t, a, "go again")
	if got := len(guideSteerTexts(events2)); got != 1 {
		t.Fatalf("run-local advisory did not reset for the next run: %d steers", got)
	}
	if caller.count("readonly") != 5 {
		t.Fatalf("second run did not execute its repeats: %d total calls", caller.count("readonly"))
	}
}

func indexOfSteer(events []Event) int {
	for i, ev := range events {
		if ev.Kind == EventSteer {
			return i
		}
	}
	return -1
}

// TestMCPRepeatAdvisoryIgnoresNonRepeats: changed arguments, failed or denied
// calls, un-annotated and directory-role operations, the helper and local
// tools never trigger the advisory.
func TestMCPRepeatAdvisoryIgnoresNonRepeats(t *testing.T) {
	reg, _ := guideCatalog(t, &stampingCaller{})
	client := &promptClient{}
	client.responses = []llm.Response{
		// Same read-only tool, changed arguments.
		toolUseResp("a1", "mcp_synthetic_readonly", `{"query":"one"}`),
		toolUseResp("a2", "mcp_synthetic_readonly", `{"query":"two"}`),
		// Successful-but-unrelated repeats: write-effect tool, directory role,
		// the helper, a local tool, and an unknown tool (error result).
		toolUseResp("b1", "mcp_synthetic_writer", `{"query":"w"}`),
		toolUseResp("b2", "mcp_synthetic_writer", `{"query":"w"}`),
		toolUseResp("n1", "mcp_synthetic_nav", `{"query":"n"}`),
		toolUseResp("n2", "mcp_synthetic_nav", `{"query":"n"}`),
		toolUseResp("h1", tools.MCPDescribeName, `{"filter":"op-0","limit":1}`),
		toolUseResp("h2", tools.MCPDescribeName, `{"filter":"op-0","limit":1}`),
		toolUseResp("r1", "ls", `{"path":"."}`),
		toolUseResp("r2", "ls", `{"path":"."}`),
		toolUseResp("u1", "mcp_synthetic_missing", `{"query":"x"}`),
		toolUseResp("u2", "mcp_synthetic_missing", `{"query":"x"}`),
	}
	a := New(Config{Client: client, Registry: reg, SystemPrompt: "s"})
	events := drainEvents(t, a, "go")
	if got := guideSteerTexts(events); len(got) != 0 {
		t.Fatalf("spurious advisory: %q", got)
	}

	// Denied repeats of a write-effect tool settle as errors and must not
	// advise either.
	deniedClient := &promptClient{}
	deniedClient.responses = []llm.Response{
		toolUseResp("w1", "mcp_synthetic_writer", `{"query":"w"}`),
		toolUseResp("w2", "mcp_synthetic_writer", `{"query":"w"}`),
	}
	denied := New(Config{Client: deniedClient, Registry: reg, SystemPrompt: "s",
		Gate: gateFunc(func(GateRequest) GateDecision { return Deny("no") })})
	if got := guideSteerTexts(drainEvents(t, denied, "go")); len(got) != 0 {
		t.Fatalf("denied calls advised: %q", got)
	}
}

// TestMCPRepeatAdvisoryOnlyWhenDeferred: the advisory is guidance for the
// deferred-discovery harness, so an eager session never sees it even for a
// successful identical read-only repeat.
func TestMCPRepeatAdvisoryOnlyWhenDeferred(t *testing.T) {
	caller := &stampingCaller{}
	ro, err := tools.NewMCPTool("synthetic", annotatedSpec("readonly", true, false), 0, caller.call)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := tools.NewMCPTool("synthetic", annotatedSpec("plain", false, false), 0, caller.call)
	if err != nil {
		t.Fatal(err)
	}
	reg := viewRegistry(t, []tools.Tool{ro, plain})
	client := &promptClient{}
	client.responses = []llm.Response{
		toolUseResp("c1", "mcp_synthetic_readonly", `{"query":"same"}`),
		toolUseResp("c2", "mcp_synthetic_readonly", `{"query":"same"}`),
	}
	a := New(Config{Client: client, Registry: reg, SystemPrompt: "s"})
	if a.mcpDeferred {
		t.Fatal("small annotated catalog deferred")
	}
	events := drainEvents(t, a, "go")
	if got := guideSteerTexts(events); len(got) != 0 {
		t.Fatalf("eager session advised: %q", got)
	}
	if caller.count("readonly") != 2 {
		t.Fatalf("eager repeats did not execute: %d", caller.count("readonly"))
	}
}
