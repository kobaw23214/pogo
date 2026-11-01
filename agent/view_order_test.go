package agent

import (
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

// What a conversation reveals — a selected MCP tool, a loaded Skill's group —
// extends the declarations instead of rewriting them (mcpview.go,
// skillview.go). A provider that caches by longest common prefix then keeps
// every earlier declaration, and a new run sends what the last request sent.

func wireOf(t *testing.T, schemas []llm.ToolSchema) string {
	t.Helper()
	b, err := llm.ToolsWireJSON(schemas)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// extends reports whether next is prev with declarations appended.
func extends(prev, next string) bool {
	return len(next) > len(prev) && strings.HasPrefix(next, strings.TrimSuffix(prev, "]"))
}

func TestMCPSelectionsExtendTheToolsArrayAndKeepTheirPlace(t *testing.T) {
	catalog := synthCatalog(t, &viewCaller{}, 60)
	client := &viewClient{responses: []llm.Response{describeResponse(33), describeResponse(7), describeResponse(33)}}
	a := New(Config{Client: client, Registry: viewRegistry(t, catalog)})
	runAndDrain(t, a, "choose tools")
	w0, w1, w2, w3 := wireOf(t, client.requestSchemas(0)), wireOf(t, client.requestSchemas(1)), wireOf(t, client.requestSchemas(2)), wireOf(t, client.requestSchemas(3))
	if !extends(w0, w1) || !extends(w1, w2) {
		t.Fatal("a new selection did not append to the tools array")
	}
	// Describing a visible tool again refreshes its recency, not its place.
	if w2 != w3 || a.mcpSelected[len(a.mcpSelected)-1].name != "mcp_synthetic_op-033" {
		t.Fatal("re-describing a visible tool moved it or kept stale recency")
	}
	runAndDrain(t, a, "next run")
	if wireOf(t, client.requestSchemas(4)) != w3 {
		t.Fatal("the next run sent different declarations than the last request")
	}
}

func TestMCPSelectionsFollowAReloadedHistory(t *testing.T) {
	catalog := synthCatalog(t, &viewCaller{}, 60)
	client := &viewClient{responses: []llm.Response{describeResponse(33), describeResponse(7)}}
	a := New(Config{Client: client, Registry: viewRegistry(t, catalog)})
	runAndDrain(t, a, "choose tools")

	reopened := New(Config{Client: &viewClient{}, Registry: viewRegistry(t, catalog)})
	initial := reopened.OverheadTokens()
	reopened.SetMessages(append([]llm.Message(nil), a.Messages()...))
	// The view, and so the fixed cost an interface shows before the next run,
	// is the one the reloaded history implies.
	if wireOf(t, reopened.schemas) != wireOf(t, a.schemas) || reopened.OverheadTokens() <= initial {
		t.Fatalf("reloaded view differs: overhead %d -> %d", initial, reopened.OverheadTokens())
	}
	reopened.SetMessages(append([]llm.Message(nil), a.Messages()[:1]...))
	if len(reopened.mcpSelected) != 0 || reopened.OverheadTokens() != initial {
		t.Fatal("a history without the describe results kept their selections")
	}
}

func TestLoadingASkillAppendsItsToolsAndKeepsThePrompt(t *testing.T) {
	reg, sk, _ := embeddedSkillRegistry(t)
	a := New(Config{Client: &viewClient{}, Registry: reg, SystemPrompt: "s"})
	before, prompt := wireOf(t, a.schemas), a.modelSystemPrompt()
	if !strings.Contains(prompt, "\n- agent-workflows: "+tools.AgentWorkflowReadName) {
		t.Fatalf("loading guidance missing:\n%s", prompt)
	}
	a.SetMessages(append([]llm.Message{llm.UserText("go")}, readSkillTurn("r1", sk.Path, "body")...))
	if !extends(before, wireOf(t, a.schemas)) {
		t.Fatal("loading the Skill rewrote the tools array instead of appending")
	}
	if a.modelSystemPrompt() != prompt {
		t.Fatal("loading the Skill changed the system prompt")
	}
	n, group := len(a.schemas), tools.SkillBindingTools("agent-workflows")
	for i, name := range group {
		if got := a.schemas[n-len(group)+i].Name; got != name {
			t.Fatalf("tail declaration %d is %s, want %s", i, got, name)
		}
	}
}
