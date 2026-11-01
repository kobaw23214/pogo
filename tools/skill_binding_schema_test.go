package tools

import (
	"testing"

	"github.com/yosukeno/pogo/llm"
)

// Each host binding group's declarations have their own ceiling, for the same
// reason agent.TestToolSchemasStaySmall bounds the core set: a Skill-bound
// group is sent on every request once its Skill is loaded (or always, where it
// cannot be deferred), so its size is a per-turn cost that should move by
// decision rather than by drift. The agent-workflows group is the case that
// made this necessary — its four declarations once weighed 89 KB because one
// step schema was repeated in each. The core ceiling does not see these
// groups at all except browser, so each group is measured on its own here,
// through llm.ToolsWireJSON like the request path.
//
// Measured on 2026-11-01: agent-workflows 26119 bytes (agent_workflow_edit
// alone 17988), background-tasks 7512, browser-use 7192 (native backend),
// skill-creator 1853. Like the core ceiling, each number sits a couple of
// hundred bytes above that, so a growth has to argue past a failing test.
// Raise one deliberately and say what the new text buys.
func TestSkillBindingGroupSchemasStaySmall(t *testing.T) {
	ceilings := map[string]int{
		"agent-workflows":  26400,
		"background-tasks": 7700,
		"browser-use":      7400,
		"skill-creator":    2000,
	}
	r := New(Options{Cwd: t.TempDir(), Browser: &stubLink{}, SkillAuthor: &skillAuthorFixture{},
		BackgroundTasks: &fakeBackgroundTaskBackend{}, AgentWorkflows: &agentWorkflowFixture{}})
	for _, name := range SkillBindingNames() {
		ceiling, ok := ceilings[name]
		if !ok {
			t.Errorf("binding group %s has no declaration ceiling; measure it and add one", name)
			continue
		}
		var schemas []llm.ToolSchema
		for _, tool := range SkillBindingTools(name) {
			registered, ok := r.Get(tool)
			if !ok {
				t.Fatalf("binding group %s: %s is not registered in the measured shape", name, tool)
			}
			schemas = append(schemas, llm.ToolSchema{Name: registered.Name(), Description: registered.Description(), InputSchema: registered.InputSchema()})
		}
		wire, err := llm.ToolsWireJSON(schemas)
		if err != nil {
			t.Fatalf("binding group %s does not marshal: %v", name, err)
		}
		t.Logf("%-16s %6d bytes across %d tools (ceiling %d)", name, len(wire), len(schemas), ceiling)
		for _, s := range schemas {
			one, _ := llm.ToolsWireJSON([]llm.ToolSchema{s})
			t.Logf("  %-22s %6d bytes (description %d)", s.Name, len(one), len(s.Description))
		}
		if len(wire) > ceiling {
			t.Errorf("binding group %s declarations are %d bytes, over the %d ceiling. Raise it deliberately "+
				"and say what the new text buys", name, len(wire), ceiling)
		}
	}
}
