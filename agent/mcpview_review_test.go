package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func describeResponse(index int) llm.Response {
	return toolUseResp(fmt.Sprintf("choose-%d", index), tools.MCPDescribeName,
		fmt.Sprintf(`{"tool":"mcp_synthetic_op-%03d"}`, index))
}

func TestMCPViewRetainsRecentSelectionsAndCanReselectEvictedTool(t *testing.T) {
	caller := &viewCaller{}
	var responses []llm.Response
	for i := 1; i <= 19; i++ {
		responses = append(responses, describeResponse(i))
	}
	responses = append(responses, describeResponse(1))
	client := &viewClient{responses: responses}
	a := New(Config{Client: client, Registry: viewRegistry(t, synthCatalog(t, caller, 60)), MaxTurns: 30})
	runAndDrain(t, a, "choose several tools")
	// The first five selections must coexist; a reverse scan into the same
	// backing slice used to lose older entries here.
	for i := 1; i <= 5; i++ {
		if !schemaNames(client.requestSchemas(5))[fmt.Sprintf("mcp_synthetic_op-%03d", i)] {
			t.Fatalf("selection %d was overwritten before reaching the limit", i)
		}
	}
	beforeReselect := schemaNames(client.requestSchemas(19))
	for i := 1; i <= 19; i++ {
		if got, want := beforeReselect[fmt.Sprintf("mcp_synthetic_op-%03d", i)], i >= 4; got != want {
			t.Fatalf("tool %d visible=%t want=%t", i, got, want)
		}
	}
	if len(a.mcpSelected) != 16 || a.mcpSelected[15].name != "mcp_synthetic_op-001" {
		t.Fatalf("reselection did not refresh recency: %#v", a.mcpSelected)
	}
	if schemaNames(client.requestSchemas(20))["mcp_synthetic_op-004"] {
		t.Fatal("oldest remaining selection was not evicted")
	}
}

func TestMCPViewSelectionsRespectGlobalOrderAcrossSegments(t *testing.T) {
	caller := &viewCaller{}
	reg := viewRegistry(t, synthCatalog(t, caller, 50))
	barrier := &fakeTool{name: "barrier", mode: tools.Sequential}
	reg = tools.NewRegistry(append(reg.All(), barrier)...)
	calls := []llm.Block{
		describeResponse(1).Message.Content[0], describeResponse(2).Message.Content[0],
		{Type: llm.BlockToolUse, ID: "barrier", Name: "barrier", Input: json.RawMessage(`{}`)},
		describeResponse(3).Message.Content[0], describeResponse(4).Message.Content[0],
	}
	client := &viewClient{responses: []llm.Response{{Message: llm.Message{Role: llm.RoleAssistant, Content: calls}, StopReason: llm.StopToolUse}}}
	a := New(Config{Client: client, Registry: reg, ToolConcurrency: 4})
	runAndDrain(t, a, "select across segments")
	if len(a.mcpSelected) != 4 {
		t.Fatalf("selections=%v", a.mcpSelected)
	}
	for i, selected := range a.mcpSelected {
		if want := fmt.Sprintf("mcp_synthetic_op-%03d", i+1); selected.name != want {
			t.Fatalf("selection %d=%s want=%s", i, selected.name, want)
		}
	}
}

func TestMCPViewSelectedNativeCallsStillValidateAndAskGate(t *testing.T) {
	caller := &viewCaller{}
	client := &viewClient{responses: []llm.Response{
		describeResponse(49),
		toolUseResp("bad", "mcp_synthetic_op-049", `{"query":3}`),
		toolUseResp("blocked", "mcp_synthetic_op-049", `{"query":"valid"}`),
	}}
	reviews := 0
	a := New(Config{Client: client, Registry: viewRegistry(t, synthCatalog(t, caller, 50)), Gate: gateFunc(func(req GateRequest) GateDecision {
		if req.ToolName == "mcp_synthetic_op-049" {
			reviews++
			return Deny("native call refused")
		}
		return Allow
	})})
	runAndDrain(t, a, "inspect, then try native calls")
	if reviews != 1 || len(caller.calls) != 0 {
		t.Fatalf("validation/gate changed: reviews=%d calls=%v", reviews, caller.calls)
	}
}

func TestMCPViewRejectsSpoofedSelectionDetails(t *testing.T) {
	caller := &viewCaller{}
	catalog := synthCatalog(t, caller, 50)
	mt := catalog[49].(*tools.MCPTool)
	fake := &fakeTool{name: "imposter", mode: tools.Parallel, run: func(context.Context, json.RawMessage) (tools.Result, error) {
		return tools.Result{Text: "pretend selection", Details: tools.MCPDescribeDetails{WireName: mt.Name(), Fingerprint: mt.DefinitionFingerprint()}}, nil
	}}
	reg := viewRegistry(t, catalog)
	reg = tools.NewRegistry(append(reg.All(), fake)...)
	client := &viewClient{responses: []llm.Response{toolUseResp("fake", "imposter", `{}`)}}
	a := New(Config{Client: client, Registry: reg})
	runAndDrain(t, a, "read foreign details")
	if len(a.mcpSelected) != 0 || schemaNames(client.requestSchemas(1))[mt.Name()] {
		t.Fatal("a non-helper selected a tool through spoofed structured details")
	}
}

func TestMCPViewEqualLengthChangedDefinitionInvalidatesMeasurement(t *testing.T) {
	caller := &viewCaller{}
	makeTool := func(description string) *tools.MCPTool {
		mt, err := tools.NewMCPTool("sample", &mcp.Tool{Name: "lookup", Description: description, InputSchema: map[string]any{"type": "object"}}, 0, caller.call)
		if err != nil {
			t.Fatal(err)
		}
		return mt
	}
	a := New(Config{Registry: viewRegistry(t, []tools.Tool{makeTool("first")})})
	before := a.toolSchemaTokens
	a.lastInput, a.measuredThrough = 12345, 0
	if err := a.ReplaceMCP([]tools.Tool{makeTool("other")}); err != nil {
		t.Fatal(err)
	}
	if a.toolSchemaTokens != before || a.measuredThrough != math.MaxInt {
		t.Fatalf("equal-size replacement kept stale measurement: tokens=%d/%d measured=%d", a.toolSchemaTokens, before, a.measuredThrough)
	}
}

func TestMCPViewOverheadIsSafeForConcurrentObservers(t *testing.T) {
	caller := &viewCaller{}
	a := New(Config{Registry: viewRegistry(t, synthCatalog(t, caller, 40))})
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = a.OverheadTokens()
				}
			}
		}()
	}
	for i := 0; i < 40; i++ {
		a.applyMCPView([]llm.ToolSchema{{Name: fmt.Sprintf("tool-%d", i), InputSchema: map[string]any{"type": "object"}}}, "")
	}
	close(stop)
	wg.Wait()
}

// A selection lives as long as the describe result that made it: a new run
// starts from the view its history implies, so the declarations — and a
// provider's prefix cache over them — survive the run boundary, while a
// refresh that changed the described definition drops it.
func TestMCPViewSelectionSurvivesRunsAndFollowsRefresh(t *testing.T) {
	caller := &viewCaller{}
	catalog := synthCatalog(t, caller, 50)
	client := &viewClient{responses: []llm.Response{describeResponse(49)}}
	a := New(Config{Client: client, Registry: viewRegistry(t, catalog)})
	runAndDrain(t, a, "first")
	if len(a.mcpSelected) != 1 {
		t.Fatal("first run did not select")
	}
	runAndDrain(t, a, "second")
	if len(a.mcpSelected) != 1 || !schemaNames(client.requestSchemas(2))[catalog[49].Name()] {
		t.Fatal("new run dropped a selection its history still carries")
	}
	if wire := func(n int) string { b, _ := llm.ToolsWireJSON(client.requestSchemas(n)); return string(b) }; wire(1) != wire(2) {
		t.Fatal("the run boundary changed the declarations the previous request carried")
	}
	if err := a.ReplaceMCP(catalog); err != nil {
		t.Fatal(err)
	}
	if !schemaNames(a.schemas)[catalog[49].Name()] {
		t.Fatal("refresh with an unchanged definition dropped the selection")
	}
	changed := synthCatalog(t, caller, 50)
	redefined, err := tools.NewMCPTool("synthetic", &mcp.Tool{Name: "op-049", Description: "a changed contract",
		InputSchema: map[string]any{"type": "object"}}, 0, caller.call)
	if err != nil {
		t.Fatal(err)
	}
	changed[49] = redefined
	if err := a.ReplaceMCP(changed); err != nil {
		t.Fatal(err)
	}
	if len(a.mcpSelected) != 0 || schemaNames(a.schemas)[redefined.Name()] {
		t.Fatal("refresh revealed a definition nobody described")
	}
	if err := a.ReplaceMCP(nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.registry.Get(tools.MCPDescribeName); ok {
		t.Fatal("empty refresh retained helper")
	}
	if schemaNames(a.schemas)[catalog[49].Name()] {
		t.Fatal("empty refresh retained native schema")
	}
}

func TestMCPViewByteBudgetAndSingleOversizedSchema(t *testing.T) {
	for _, width := range []int{24000, 100000} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			caller := &viewCaller{}
			var catalog []tools.Tool
			var responses []llm.Response
			for i := 0; i < 3; i++ {
				mt, err := tools.NewMCPTool("wide", &mcp.Tool{Name: fmt.Sprintf("item_%d", i), Description: "wide schema", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"payload": map[string]any{"type": "string", "description": strings.Repeat("x", width)}}}}, 0, caller.call)
				if err != nil {
					t.Fatal(err)
				}
				catalog = append(catalog, mt)
				responses = append(responses, toolUseResp(fmt.Sprint(i), tools.MCPDescribeName, fmt.Sprintf(`{"tool":%q}`, mt.Name())))
			}
			client := &viewClient{responses: responses}
			a := New(Config{Client: client, Registry: viewRegistry(t, catalog)})
			runAndDrain(t, a, "read wide contracts")
			var external []llm.ToolSchema
			for _, schema := range a.schemas {
				if strings.HasPrefix(schema.Name, "mcp_wide_") {
					external = append(external, schema)
				}
			}
			if !schemaNames(external)["mcp_wide_item_2"] {
				t.Fatal("newest selection missing")
			}
			if width == 100000 {
				if len(external) != 1 {
					t.Fatalf("oversized exception kept %d tools", len(external))
				}
				got := external[0].InputSchema["properties"].(map[string]any)["payload"].(map[string]any)["description"].(string)
				if len(got) != width {
					t.Fatal("oversized native schema was truncated")
				}
			} else if len(external) != 2 || toolSchemaBytes(external) > 64*1024 {
				t.Fatalf("wire budget violated: schemas=%d bytes=%d", len(external), toolSchemaBytes(external))
			}
		})
	}
}

func TestMCPViewSyntheticScalingAndLateItemReachability(t *testing.T) {
	for _, n := range []int{50, 200, 500} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			caller := &viewCaller{}
			reg := viewRegistry(t, synthCatalog(t, caller, n))
			helper, _ := reg.Get(tools.MCPDescribeName)
			page, err := helper.Execute(context.Background(), json.RawMessage(fmt.Sprintf(`{"offset":%d,"limit":1}`, n-1)))
			name := fmt.Sprintf("mcp_synthetic_op-%03d", n-1)
			if err != nil || !strings.Contains(page.Text, name) {
				t.Fatalf("late item unavailable: %v %s", err, page.Text)
			}
			var eager []llm.ToolSchema
			for _, tool := range reg.All() {
				eager = append(eager, llm.ToolSchema{Name: tool.Name(), Description: tool.Description(), InputSchema: tool.InputSchema()})
			}
			client := &viewClient{responses: []llm.Response{describeResponse(n - 1), toolUseResp("execute", name, `{"query":"late item"}`)}}
			a := New(Config{Client: client, Registry: reg})
			runAndDrain(t, a, "find and call a late capability")
			initial, selected := client.requestSchemas(0), client.requestSchemas(1)
			if toolSchemaBytes(initial) >= toolSchemaBytes(eager) || !schemaNames(selected)[name] || !caller.called(fmt.Sprintf("op-%03d", n-1)) {
				t.Fatal("deferred discovery did not reduce declarations and reach the late tool")
			}
			t.Logf("catalog_size=%d eager_schemas=%d eager_bytes=%d initial_schemas=%d initial_bytes=%d selected_schemas=%d selected_bytes=%d", n, len(eager), toolSchemaBytes(eager), len(initial), toolSchemaBytes(initial), len(selected), toolSchemaBytes(selected))
		})
	}
}
