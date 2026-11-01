package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	af "github.com/yosukeno/pogo/agentworkflow"
)

func awGeneralReportDefinition() af.Definition {
	d := awGraphReportDefinition()
	d.Name = "普通节点直接分流汇合"
	steps := []af.Step{}
	for _, step := range d.Steps {
		if step.ID == "fork" || step.ID == "parallel_join" || step.ID == "merge" {
			continue
		}
		if step.ID == "report" {
			step.Task = "根据实际完成的前序分支与本人确认意见生成报告"
		}
		steps = append(steps, step)
	}
	d.Steps = steps
	d.Graph = &af.Graph{Version: 1, Edges: []af.Edge{
		{Source: af.GraphInput, Target: "choose"},
		{Source: "choose", Target: "left", Branch: "true"}, {Source: "choose", Target: "right", Branch: "true"},
		{Source: "choose", Target: "brief", Branch: "false"},
		{Source: "left", Target: "check_left"}, {Source: "right", Target: "check_right"},
		{Source: "check_left", Target: "report"}, {Source: "check_right", Target: "report"}, {Source: "brief", Target: "report"},
		{Source: "report", Target: af.GraphOutput},
	}}
	return d
}

func TestAgentWorkflowGeneralGraphAgentFanInWaitsForBothConfirmations(t *testing.T) {
	for _, mode := range []string{"full", "brief"} {
		t.Run(mode, func(t *testing.T) {
			h, model := awGraphModelHarness(t, "", false)
			a := awTestAgent(t, h, "通用图隔离助手", "write")
			source := h.studioConversationAs(aliceToken, a.ID)
			if err := os.WriteFile(filepath.Join(h.session(source).cwd, "source.md"), []byte("GRAPH-SOURCE-731 synthetic input"), 0600); err != nil {
				t.Fatal(err)
			}
			definition := awGeneralReportDefinition()
			d := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "edit", map[string]any{"action": "create", "request_id": "general-create", "name": definition.Name, "inputs": definition.Inputs, "steps": definition.Steps, "graph": definition.Graph}, 200), "workflow")
			accepted := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "trial", "request_id": "general-trial", "workflow_id": d.ID, "expected_revision": d.Revision, "expected_hash": d.DefinitionHash, "source_session_id": source, "inputs": map[string]string{"mode": mode}, "files": []string{"source.md"}}, 200), "run")
			if mode == "full" {
				wait := awTestRun(t, h, aliceToken, a.ID, d.ID, accepted.ID, af.WaitingConfirmation)
				for _, id := range []string{"left", "right"} {
					if wait.Steps[awGraphIndex(wait, id)].Status != af.Completed {
						t.Fatal("ordinary condition fan-out did not finish independent Agent nodes", wait)
					}
				}
				if wait.Steps[awGraphIndex(wait, "brief")].Status != af.Skipped || wait.Steps[awGraphIndex(wait, "report")].Status != af.Queued {
					t.Fatal("report must wait for both selected confirmations, not the skipped alternative", wait)
				}
				for _, id := range []string{"check_left", "check_right"} {
					confirmed := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "confirm", map[string]any{"workflow_id": d.ID, "run_id": accepted.ID, "step_id": id, "request_id": id, "feedback": map[string]string{"check_left": "checked-left", "check_right": "checked-right"}[id]}, 200), "run")
					if confirmed.ID != accepted.ID {
						t.Fatal("confirmation replaced the logical Run")
					}
					if id == "check_left" && confirmed.Steps[awGraphIndex(confirmed, "report")].Status != af.Queued {
						t.Fatal("first confirmation incorrectly released the multi-input node", confirmed)
					}
				}
			}
			done := awTestRun(t, h, aliceToken, a.ID, d.ID, accepted.ID, af.Completed)
			if !reflect.DeepEqual(done.Definition.Graph, d.Graph) || done.VersionID != d.DefinitionHash {
				t.Fatal("general graph execution rewrote its frozen version")
			}
			model.mu.Lock()
			defer model.mu.Unlock()
			if mode == "full" {
				if model.began["left"] != 1 || model.began["right"] != 1 || model.began["report"] != 1 || model.began["brief"] != 0 || model.peak < 2 {
					t.Fatal("fan-in duplicated, serialized, or executed an unselected node", model.began, model.peak)
				}
			} else if model.began["brief"] != 1 || model.began["report"] != 1 || model.began["left"] != 0 || model.began["right"] != 0 {
				t.Fatal("skipped paths blocked or executed during fallback fan-in", model.began)
			}
		})
	}
}

func TestAgentWorkflowGeneralGraphTypedFanInAndFanOut(t *testing.T) {
	awRequireCodeRuntimes(t)
	h, model := awGraphModelHarness(t, "", false)
	a := awTestAgent(t, h, "通用代码图隔离助手")
	source := h.studioConversationAs(aliceToken, a.ID)
	outputs := map[string]af.ValueSchema{"value": {Type: "number"}}
	d := af.Definition{Name: "双输入计算与并行后续", Steps: []af.Step{
		awCodeStep("left", "python", "def main(inputs):\n    return {'value': 20}\n", map[string]af.CodeBinding{}, outputs),
		awCodeStep("right", "javascript", "function main(inputs) { return {value:30}; }", map[string]af.CodeBinding{}, outputs),
		awCodeStep("combine", "javascript", "function main(inputs) { return {value:inputs.left+inputs.right}; }", map[string]af.CodeBinding{"left": {Type: "number", Source: "steps.left.output.value"}, "right": {Type: "number", Source: "steps.right.output.value"}}, outputs),
		{ID: "review", Kind: "confirm", Prompt: "核对实际合计 {{ steps.combine.output.value }}"},
		awCodeStep("audit", "python", "def main(inputs):\n    return {'value': inputs['value'] + 1}\n", map[string]af.CodeBinding{"value": {Type: "number", Source: "steps.combine.output.value"}}, outputs),
		awCodeStep("finish", "javascript", "function main(inputs) { return {value:inputs.audit+inputs.total}; }", map[string]af.CodeBinding{"total": {Type: "number", Source: "steps.combine.output.value"}, "audit": {Type: "number", Source: "steps.audit.output.value"}}, outputs),
	}, Graph: &af.Graph{Version: 1, Edges: []af.Edge{
		{Source: af.GraphInput, Target: "left"}, {Source: af.GraphInput, Target: "right"},
		{Source: "left", Target: "combine"}, {Source: "right", Target: "combine"},
		{Source: "combine", Target: "review"}, {Source: "combine", Target: "audit"},
		{Source: "review", Target: "finish"}, {Source: "audit", Target: "finish"}, {Source: "finish", Target: af.GraphOutput},
	}}}
	d = awCreateCodeDefinition(t, h, a, d, "general-code-create")
	accepted := awStartCodeTrial(t, h, a, d, source, map[string]string{}, "general-code-trial")
	wait := awTestRun(t, h, aliceToken, a.ID, d.ID, accepted.ID, af.WaitingConfirmation)
	if wait.Steps[2].Status != af.Completed || wait.Steps[4].Status != af.Completed || string(wait.Steps[2].Output) != `{"value":50}` || wait.Steps[5].Status != af.Queued {
		t.Fatal("typed fan-in read a missing result or failed to wait for confirmation", wait)
	}
	awTestCall(t, h, aliceToken, a.ID, "confirm", map[string]any{"workflow_id": d.ID, "run_id": accepted.ID, "step_id": "review", "request_id": "general-code-confirm", "feedback": "核对合计50"}, 200)
	done := awTestRun(t, h, aliceToken, a.ID, d.ID, accepted.ID, af.Completed)
	var result map[string]int
	if json.Unmarshal(done.Steps[5].Output, &result) != nil || result["value"] != 101 {
		t.Fatal("independent typed outputs were lost at fan-in", string(done.Steps[5].Output))
	}
	for _, step := range done.Steps {
		if step.StepID != "review" && len(step.Attempts) != 1 {
			t.Fatal("confirmation replayed an already completed code branch", step.StepID, len(step.Attempts))
		}
	}
	model.mu.Lock()
	defer model.mu.Unlock()
	if len(model.began) != 0 {
		t.Fatal("fixed calculations invoked a model", model.began)
	}
}
