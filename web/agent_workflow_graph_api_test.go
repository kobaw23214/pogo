package web

import (
	"encoding/json"
	"reflect"
	"testing"

	af "github.com/yosukeno/pogo/agentworkflow"
	"github.com/yosukeno/pogo/identity"
)

// Shared by isolated API and browser fixtures. The two full-report branches
// each require the owning user's confirmation before the final report.
func awGraphExampleDefinition() af.Definition {
	return af.Definition{Name: "条件与并行资料报告", Description: "隔离测试资料：full模式并行整理两组资料，分别本人确认后汇聚；其他模式生成简报。", Inputs: []af.Input{{Key: "mode", Label: "报告模式（full 或 brief）", Required: true}, {Key: "topic", Label: "报告主题", Required: true}}, Steps: []af.Step{
		{ID: "route", Kind: "condition", Title: "选择报告模式", Condition: &af.Condition{Source: "inputs.mode", Operator: "equals", Value: "full"}},
		{ID: "fork", Kind: "parallel", Title: "并行整理"},
		{ID: "research", Kind: "agent", Title: "整理要点", Task: "阅读所选资料，整理 {{ inputs.topic }} 的要点。", OutputFile: "research.md"},
		{ID: "confirm_research", Kind: "confirm", Title: "确认资料要点", Prompt: "请核对整理要点：{{ steps.research.summary }}"},
		{ID: "sources", Kind: "agent", Title: "核对来源", Task: "阅读所选资料，独立核对 {{ inputs.topic }} 的来源。", OutputFile: "sources.md"},
		{ID: "confirm_sources", Kind: "confirm", Title: "确认资料来源", Prompt: "请核对来源：{{ steps.sources.summary }}"},
		{ID: "parallel_join", Kind: "join", Title: "汇聚两组资料"},
		{ID: "brief", Kind: "agent", Title: "整理简报资料", Task: "阅读所选资料，整理 {{ inputs.topic }} 的简要资料。", OutputFile: "brief.md"},
		{ID: "result_join", Kind: "join", Title: "汇聚选中路径"},
		{ID: "report", Kind: "agent", Title: "生成报告", Task: "根据实际完成的资料与本人意见生成报告，保留来源：{{ steps.result_join.summary }}", OutputFile: "report.md"},
	}, Graph: &af.Graph{Version: 1, Edges: []af.Edge{
		{Source: af.GraphInput, Target: "route"},
		{Source: "route", Target: "fork", Branch: "true"}, {Source: "route", Target: "brief", Branch: "false"},
		{Source: "fork", Target: "research"}, {Source: "fork", Target: "sources"},
		{Source: "research", Target: "confirm_research"}, {Source: "sources", Target: "confirm_sources"},
		{Source: "confirm_research", Target: "parallel_join"}, {Source: "confirm_sources", Target: "parallel_join"},
		{Source: "parallel_join", Target: "result_join"}, {Source: "brief", Target: "result_join"},
		{Source: "result_join", Target: "report"}, {Source: "report", Target: af.GraphOutput},
	}}}
}

func TestAgentWorkflowGraphAPIUpgradeIsolationAndExactVersion(t *testing.T) {
	h := newMultiuserHarness(t, nil)
	a := awTestAgent(t, h, "graph-api-owner")
	other := awTestAgent(t, h, "graph-api-other")
	original := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "edit", map[string]any{"action": "create", "request_id": "legacy-graph-upgrade", "name": "保留旧版", "inputs": []af.Input{{Key: "mode", Required: true}}, "steps": []af.Step{{ID: "collect", Kind: "agent", Task: "整理资料"}}}, 200), "workflow")
	awTestCall(t, h, aliceToken, a.ID, "enable", map[string]any{"action": "enable", "workflow_id": original.ID, "expected_revision": original.Revision, "expected_hash": original.DefinitionHash}, 200)
	owner := identity.LocalScope("alice")
	oldVersion, err := taskRunsDB(t, h.mgr).AWVersion(sessionResultOwnerKey(&owner), a.ID, original.ID, original.DefinitionHash)
	if err != nil {
		t.Fatal(err)
	}
	oldBytes, _ := json.Marshal(oldVersion)
	graph := &af.Graph{Version: 1, Edges: []af.Edge{{Source: af.GraphInput, Target: "route"}, {Source: "route", Target: "full", Branch: "true"}, {Source: "route", Target: "brief", Branch: "false"}, {Source: "full", Target: "merge"}, {Source: "brief", Target: "merge"}, {Source: "merge", Target: "collect"}, {Source: "collect", Target: af.GraphOutput}}}
	operations := []any{
		map[string]any{"op": "add_step", "step": af.Step{ID: "route", Kind: "condition", Condition: &af.Condition{Source: "inputs.mode", Operator: "equals", Value: "full"}}},
		map[string]any{"op": "add_step", "step": af.Step{ID: "full", Kind: "agent", Task: "完整资料"}},
		map[string]any{"op": "add_step", "step": af.Step{ID: "brief", Kind: "agent", Task: "简要资料"}},
		map[string]any{"op": "add_step", "step": af.Step{ID: "merge", Kind: "join"}},
		map[string]any{"op": "update_step", "step_id": "collect", "patch": map[string]string{"task": "汇总 {{ steps.merge.summary }}"}},
		map[string]any{"op": "set_graph", "graph": graph},
	}
	upgraded := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "edit", map[string]any{"action": "patch", "workflow_id": original.ID, "request_id": "upgrade-v2", "expected_revision": original.Revision, "operations": operations}, 200), "workflow")
	if upgraded.Graph == nil || upgraded.Revision != 2 || upgraded.DefinitionHash == original.DefinitionHash || upgraded.EnabledVersion != original.DefinitionHash {
		t.Fatalf("upgrade altered activation or lost graph: %+v", upgraded)
	}
	again, err := taskRunsDB(t, h.mgr).AWVersion(sessionResultOwnerKey(&owner), a.ID, original.ID, original.DefinitionHash)
	if err != nil {
		t.Fatal(err)
	}
	afterBytes, _ := json.Marshal(again)
	if string(afterBytes) != string(oldBytes) || again.Graph != nil {
		t.Fatal("graph upgrade rewrote legacy version")
	}
	awTestCall(t, h, aliceToken, a.ID, "enable", map[string]any{"action": "enable", "workflow_id": original.ID, "expected_revision": original.Revision, "expected_hash": original.DefinitionHash}, 409)
	enabled := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "enable", map[string]any{"action": "enable", "workflow_id": original.ID, "expected_revision": upgraded.Revision, "expected_hash": upgraded.DefinitionHash}, 200), "workflow")
	if enabled.EnabledVersion != upgraded.DefinitionHash {
		t.Fatal("did not enable exact graph version")
	}
	awTestCall(t, h, bobToken, a.ID, "read", map[string]any{"action": "get", "workflow_id": original.ID}, 404)
	awTestCall(t, h, aliceToken, other.ID, "read", map[string]any{"action": "get", "workflow_id": original.ID}, 404)
	awTestCall(t, h, bobToken, a.ID, "edit", map[string]any{"action": "patch", "workflow_id": original.ID, "request_id": "other-owner", "expected_revision": upgraded.Revision, "operations": operations}, 404)
	broken := *graph
	broken.Edges = append([]af.Edge(nil), graph.Edges...)
	broken.Edges[len(broken.Edges)-1].Target = "route"
	awTestCall(t, h, aliceToken, a.ID, "edit", map[string]any{"action": "patch", "workflow_id": original.ID, "request_id": "cycle", "expected_revision": upgraded.Revision, "operations": []any{map[string]any{"op": "set_graph", "graph": broken}}}, 409)
	retained := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "get", "workflow_id": original.ID}, 200), "workflow")
	if !reflect.DeepEqual(retained, enabled) {
		t.Fatal("invalid graph edit was not atomic")
	}
}

func TestAgentWorkflowGraphAPICreateAndValidate(t *testing.T) {
	h := newMultiuserHarness(t, nil)
	a := awTestAgent(t, h, "graph-create")
	definition := awGraphExampleDefinition()
	valid := awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "validate", "definition": definition}, 200)
	if !awTestValue[bool](t, valid, "valid") {
		t.Fatalf("example validation: %s", valid["diagnostics"])
	}
	if awTestValue[int](t, valid, "diagnostic_count") != 0 || len(awTestValue[map[string]int](t, valid, "diagnostic_counts")) != 0 {
		t.Fatal("valid definition did not return empty diagnostic counts")
	}
	saved := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "edit", map[string]any{"action": "create", "request_id": "graph-create", "name": definition.Name, "inputs": definition.Inputs, "steps": definition.Steps, "graph": definition.Graph}, 200), "workflow")
	if !reflect.DeepEqual(saved.Graph, definition.Graph) || saved.Steps[0].Condition == nil {
		t.Fatal("graph/condition missing from saved definition")
	}
	invalid := definition
	invalid.Graph = &af.Graph{Version: 1, Edges: append([]af.Edge(nil), definition.Graph.Edges...)}
	invalid.Graph.Edges[1].Branch = "other"
	result := awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "validate", "definition": invalid}, 200)
	if awTestValue[bool](t, result, "valid") || len(awTestValue[[]af.Diagnostic](t, result, "diagnostics")) == 0 {
		t.Fatal("invalid branch lacked structured diagnostics")
	}
}

func TestAgentWorkflowGraphAPIInvalidStructureDiagnosticCounts(t *testing.T) {
	h := newMultiuserHarness(t, nil)
	a := awTestAgent(t, h, "graph-diagnostic-counts")
	definition := awGraphExampleDefinition()
	definition.Graph.Edges = definition.Graph.Edges[:len(definition.Graph.Edges)-1]
	definition.Steps[4].OutputFile = definition.Steps[2].OutputFile
	definition.Steps[9].Task += " 请读取 {{ steps.missing_node.summary }}。"
	result := awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "validate", "definition": definition}, 200)
	diagnostics := awTestValue[[]af.Diagnostic](t, result, "diagnostics")
	counts := awTestValue[map[string]int](t, result, "diagnostic_counts")
	want := map[string]int{"invalid_node_degree": 2, "unreachable_node": 12, "invalid_output_file": 1, "unknown_step": 1}
	if awTestValue[bool](t, result, "valid") || awTestValue[int](t, result, "diagnostic_count") != 16 || len(diagnostics) != 16 || !reflect.DeepEqual(counts, want) {
		t.Fatalf("misleading structure/reference diagnostics: count=%s counts=%v diagnostics=%+v", result["diagnostic_count"], counts, diagnostics)
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == "unknown_step" && diagnostic.StepID != "report" {
			t.Fatal("a declared upstream reference was reported as unknown", diagnostic)
		}
	}
}
