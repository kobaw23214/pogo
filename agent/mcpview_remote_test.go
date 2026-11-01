package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

// serverCatalog builds n neutral synthetic MCP tools on one named server; the
// first is always named sharedOpName so two catalogs collide on a remote
// tool name while their canonical wire names stay distinct.
const sharedOpName = "shared_op"

func serverCatalog(t *testing.T, caller *viewCaller, server string, n int) []tools.Tool {
	t.Helper()
	out := make([]tools.Tool, 0, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("%s-op-%03d", server, i)
		if i == 0 {
			name = sharedOpName
		}
		mt, err := tools.NewMCPTool(server, &mcp.Tool{
			Name:        name,
			Description: fmt.Sprintf("synthetic %s operation %d for remote-name tests", server, i),
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"query": map[string]any{"type": "string"},
			}},
		}, 0, caller.call)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, mt)
	}
	return out
}

// TestMCPViewRemoteNameSelectsCanonicalWireName: a detail request naming the
// server's exact tool name (not the local wire name) resolves to the
// canonical entry, and the deferred view exposes that exact tool's native
// declaration on the next model request.
func TestMCPViewRemoteNameSelectsCanonicalWireName(t *testing.T) {
	caller := &viewCaller{}
	reg := viewRegistry(t, synthCatalog(t, caller, 60))
	client := &viewClient{responses: []llm.Response{
		toolUseResp("c1", tools.MCPDescribeName, `{"tool":"op-059"}`),
		toolUseResp("c2", "mcp_synthetic_op-059", `{"query":"via remote name"}`),
	}}
	a := New(Config{Client: client, Registry: reg, SystemPrompt: "s"})
	if !a.mcpDeferred {
		t.Fatal("fixture catalog stayed eager")
	}
	runAndDrain(t, a, "go")
	if schemaNames(client.requestSchemas(0))["mcp_synthetic_op-059"] {
		t.Fatal("hidden tool visible before selection")
	}
	if !schemaNames(client.requestSchemas(1))["mcp_synthetic_op-059"] {
		t.Fatal("remote-name detail did not expose the canonical wire schema on the next request")
	}
	if len(a.mcpSelected) != 1 || a.mcpSelected[0].name != "mcp_synthetic_op-059" {
		t.Fatalf("selection is not the canonical wire name: %v", a.mcpSelected)
	}
	if !caller.called("op-059") {
		t.Fatal("selected tool did not reach its bound caller")
	}
}

// TestMCPViewAmbiguousRemoteNameSelectsNothingUntilScoped: the same remote
// tool name on two servers fails explicitly and activates nothing; scoping by
// server then selects exactly that server's canonical tool.
func TestMCPViewAmbiguousRemoteNameSelectsNothingUntilScoped(t *testing.T) {
	caller := &viewCaller{}
	catalog := append(serverCatalog(t, caller, "alpha", 40), serverCatalog(t, caller, "bravo", 5)...)
	reg := viewRegistry(t, catalog)
	client := &viewClient{responses: []llm.Response{
		toolUseResp("c1", tools.MCPDescribeName, `{"tool":"shared_op"}`),
		toolUseResp("c2", tools.MCPDescribeName, `{"tool":"shared_op","server":"bravo"}`),
	}}
	a := New(Config{Client: client, Registry: reg, SystemPrompt: "s"})
	if !a.mcpDeferred {
		t.Fatal("two-server fixture stayed eager")
	}
	runAndDrain(t, a, "go")
	if schemaNames(client.requestSchemas(1))["mcp_alpha_shared_op"] ||
		schemaNames(client.requestSchemas(1))["mcp_bravo_shared_op"] {
		t.Fatal("ambiguous remote name activated a tool")
	}
	if len(a.mcpSelected) != 1 || a.mcpSelected[0].name != "mcp_bravo_shared_op" {
		t.Fatalf("server-scoped resolution selected %v", a.mcpSelected)
	}
	if schemaNames(client.requestSchemas(2))["mcp_alpha_shared_op"] {
		t.Fatal("the unscoped server's tool was activated")
	}
	if !schemaNames(client.requestSchemas(2))["mcp_bravo_shared_op"] {
		t.Fatal("scoped selection missing from the next request")
	}
}

// TestMCPViewServerMismatchSelectsNothing: a canonical wire name paired with
// a mismatched server argument fails in the helper and never activates the
// tool — the mismatch must not silently redirect to another server.
func TestMCPViewServerMismatchSelectsNothing(t *testing.T) {
	caller := &viewCaller{}
	catalog := append(serverCatalog(t, caller, "alpha", 40), serverCatalog(t, caller, "bravo", 5)...)
	reg := viewRegistry(t, catalog)
	client := &viewClient{responses: []llm.Response{
		toolUseResp("c1", tools.MCPDescribeName, `{"tool":"mcp_alpha_shared_op","server":"bravo"}`),
	}}
	a := New(Config{Client: client, Registry: reg, SystemPrompt: "s"})
	runAndDrain(t, a, "go")
	if len(a.mcpSelected) != 0 {
		t.Fatalf("mismatched server selected: %v", a.mcpSelected)
	}
	if schemaNames(client.requestSchemas(1))["mcp_alpha_shared_op"] {
		t.Fatal("mismatch changed the view")
	}
}

// TestMCPViewRepeatedDetailKeepsSingleSelection: re-inspecting the same
// unchanged tool is guidance-level (no hard block) but must not duplicate or
// otherwise corrupt the selection set or the byte budget.
func TestMCPViewRepeatedDetailKeepsSingleSelection(t *testing.T) {
	caller := &viewCaller{}
	reg := viewRegistry(t, synthCatalog(t, caller, 60))
	client := &viewClient{responses: []llm.Response{
		describeResponse(7), describeResponse(7),
	}}
	a := New(Config{Client: client, Registry: reg, SystemPrompt: "s"})
	runAndDrain(t, a, "inspect twice")
	if len(a.mcpSelected) != 1 || a.mcpSelected[0].name != "mcp_synthetic_op-007" {
		t.Fatalf("repeat inspection duplicated a selection: %v", a.mcpSelected)
	}
	if !schemaNames(client.requestSchemas(2))["mcp_synthetic_op-007"] {
		t.Fatal("repeated selection lost from the view")
	}
}

// TestMCPViewRemoteNameResolutionKeepsCanonicalDetails guards the
// detail result contract used for selection: structured provenance still
// carries the canonical wire name and the frozen definition fingerprint.
func TestMCPViewRemoteNameResolutionKeepsCanonicalDetails(t *testing.T) {
	caller := &viewCaller{}
	reg := viewRegistry(t, synthCatalog(t, caller, 40))
	helper, ok := reg.Get(tools.MCPDescribeName)
	if !ok {
		t.Fatal("helper missing")
	}
	res, err := helper.Execute(context.Background(), json.RawMessage(`{"tool":"op-039"}`))
	if err != nil {
		t.Fatal(err)
	}
	details, ok := res.Details.(tools.MCPDescribeDetails)
	if !ok || details.WireName != "mcp_synthetic_op-039" || details.Fingerprint == "" {
		t.Fatalf("remote-name detail lost canonical provenance: %#v", res.Details)
	}
	mt, _ := reg.Get("mcp_synthetic_op-039")
	if details.Fingerprint != mt.(*tools.MCPTool).DefinitionFingerprint() {
		t.Fatal("fingerprint no longer pins the frozen definition")
	}
}
