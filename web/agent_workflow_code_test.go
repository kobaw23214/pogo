package web

import (
	"encoding/json"
	"testing"
	"time"

	af "github.com/yosukeno/pogo/agentworkflow"
	"github.com/yosukeno/pogo/workflowcode"
)

func awRequireCodeRuntimes(t *testing.T) {
	t.Helper()
	for _, status := range workflowcode.Capabilities() {
		if !status.Available {
			t.Skipf("isolated %s runtime unavailable: %s", status.Language, status.Reason)
		}
	}
}

func awCodeStep(id, language, source string, inputs map[string]af.CodeBinding, outputs map[string]af.ValueSchema) af.Step {
	return af.Step{ID: id, Kind: "code", Title: id, Code: &af.CodeSpec{Language: language, Source: source, Inputs: inputs, Outputs: outputs}}
}

func awCreateCodeDefinition(t *testing.T, h *muHarness, a studioAgent, d af.Definition, key string) af.Definition {
	t.Helper()
	return awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "edit", map[string]any{"action": "create", "request_id": key, "name": d.Name, "inputs": d.Inputs, "steps": d.Steps, "graph": d.Graph}, 200), "workflow")
}

func awStartCodeTrial(t *testing.T, h *muHarness, a studioAgent, d af.Definition, source string, inputs any, key string) af.Run {
	t.Helper()
	return awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "trial", "request_id": key, "workflow_id": d.ID, "source_session_id": source, "expected_revision": d.Revision, "expected_hash": d.DefinitionHash, "inputs": inputs}, 200), "run")
}

func TestAgentWorkflowCodeSequenceTypedDataConfirmationAndNodeTrial(t *testing.T) {
	awRequireCodeRuntimes(t)
	h, model := awGraphModelHarness(t, "", false)
	a := awTestAgent(t, h, "独立脚本验收") // deliberately no bash or file grants
	source := h.studioConversationAs(aliceToken, a.ID)
	d := awCreateCodeDefinition(t, h, a, af.Definition{Name: "订单计算与确认", Inputs: []af.Input{{Key: "orders", Type: "array", Required: true}, {Key: "factor", Type: "number", Default: "2"}}, Steps: []af.Step{
		awCodeStep("sum", "javascript", "async function main(inputs) { console.log('sum executed'); return { amount: inputs.orders.reduce((n, row) => n + row.amount, 0), count: inputs.orders.length }; }", map[string]af.CodeBinding{"orders": {Type: "array", Source: "inputs.orders"}}, map[string]af.ValueSchema{"amount": {Type: "number"}, "count": {Type: "integer"}}),
		awCodeStep("calculate", "python", "def main(inputs):\n    print('calculate executed')\n    return {'total': inputs['amount'] * inputs['factor'], 'review': True}\n", map[string]af.CodeBinding{"amount": {Type: "number", Source: "steps.sum.output.amount"}, "factor": {Type: "number", Source: "inputs.factor"}}, map[string]af.ValueSchema{"total": {Type: "number"}, "review": {Type: "boolean"}}),
		{ID: "review", Kind: "confirm", Prompt: "请确认计算金额 {{ steps.calculate.output.total }}"},
		awCodeStep("finish", "javascript", "function main(inputs) { return { total: inputs.total, confirmed: true }; }", map[string]af.CodeBinding{"total": {Type: "number", Source: "steps.calculate.output.total"}}, map[string]af.ValueSchema{"total": {Type: "number"}, "confirmed": {Type: "boolean"}}),
	}}, "code-sequence-create")
	accepted := awStartCodeTrial(t, h, a, d, source, map[string]any{"orders": []map[string]int{{"amount": 15}, {"amount": 25}}}, "code-sequence-trial")
	wait := awTestRun(t, h, aliceToken, a.ID, d.ID, accepted.ID, af.WaitingConfirmation)
	if wait.ExecutionSessionID != "" || string(wait.Steps[1].Output) != `{"review":true,"total":80}` && string(wait.Steps[1].Output) != `{"total":80,"review":true}` || wait.Steps[1].CodeExecution == nil {
		t.Fatalf("typed execution receipt missing: %+v", wait)
	}
	confirmed := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "confirm", map[string]any{"workflow_id": d.ID, "run_id": accepted.ID, "step_id": "review", "request_id": "code-confirm", "feedback": "独立计算已核对"}, 200), "run")
	if confirmed.ID != accepted.ID {
		t.Fatal("confirmation changed logical run")
	}
	done := awTestRun(t, h, aliceToken, a.ID, d.ID, accepted.ID, af.Completed)
	var output map[string]any
	if json.Unmarshal(done.Steps[3].Output, &output) != nil || output["total"] != float64(80) || output["confirmed"] != true {
		t.Fatal("structured data lost", string(done.Steps[3].Output))
	}
	model.mu.Lock()
	calls := len(model.began)
	model.mu.Unlock()
	if calls != 0 {
		t.Fatal("code workflow unexpectedly invoked model")
	}
	request := map[string]any{"action": "test_node", "request_id": "test-code-node", "workflow_id": d.ID, "step_id": "calculate", "source_session_id": source, "expected_revision": d.Revision, "expected_hash": d.DefinitionHash, "node_inputs": map[string]any{"amount": 12, "factor": 3}}
	test := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "run", request, 200), "run")
	test = awTestRun(t, h, aliceToken, a.ID, d.ID, test.ID, af.Completed)
	if test.NodeTest == nil || !test.Trial || test.Steps[0].Status != af.Skipped || test.Steps[2].Status != af.Skipped || test.Steps[1].Status != af.Completed {
		t.Fatal("node trial ran unrelated steps", test)
	}
	if json.Unmarshal(test.Steps[1].Output, &output) != nil || output["total"] != float64(36) {
		t.Fatal("node sample arguments were not used")
	}
	again := awTestCall(t, h, aliceToken, a.ID, "run", request, 200)
	if !awTestValue[bool](t, again, "reused") || awTestValue[af.Run](t, again, "run").ID != test.ID {
		t.Fatal("node trial was duplicated")
	}
	request["node_inputs"] = map[string]any{"amount": 13, "factor": 3}
	awTestCall(t, h, aliceToken, a.ID, "run", request, 409)
	awTestCall(t, h, bobToken, a.ID, "read", map[string]string{"action": "run", "workflow_id": d.ID, "run_id": test.ID}, 404)
}

func TestAgentWorkflowCodeParallelJoinAndCondition(t *testing.T) {
	awRequireCodeRuntimes(t)
	h, _ := awGraphModelHarness(t, "", false)
	a := awTestAgent(t, h, "独立并行脚本")
	source := h.studioConversationAs(aliceToken, a.ID)
	value := func(n string) map[string]af.CodeBinding {
		return map[string]af.CodeBinding{"value": {Type: "number", Value: json.RawMessage(n)}}
	}
	outputs := map[string]af.ValueSchema{"value": {Type: "number"}}
	d := af.Definition{Name: "并行计算汇聚", Steps: []af.Step{
		{ID: "fork", Kind: "parallel"},
		awCodeStep("left", "python", "def main(inputs):\n    return {'value': inputs['value'] * 2}\n", value("20"), outputs),
		awCodeStep("right", "javascript", "function main(inputs) { return {value: inputs.value * 3}; }", value("10"), outputs),
		{ID: "join", Kind: "join"},
		awCodeStep("combine", "javascript", "function main(inputs) { return {value: inputs.branches.left.value + inputs.branches.right.value}; }", map[string]af.CodeBinding{"branches": {Type: "object", Source: "steps.join.output"}}, outputs),
		{ID: "condition", Kind: "condition", Condition: &af.Condition{Source: "steps.combine.output.value", Operator: "gte", Value: "70"}},
		awCodeStep("yes", "python", "def main(inputs):\n    return {'value': inputs['value']}\n", value("1"), outputs),
		awCodeStep("no", "python", "def main(inputs):\n    return {'value': inputs['value']}\n", value("0"), outputs),
		{ID: "finish", Kind: "join"},
	}, Graph: &af.Graph{Version: 1, Edges: []af.Edge{{Source: "__input", Target: "fork"}, {Source: "fork", Target: "left"}, {Source: "fork", Target: "right"}, {Source: "left", Target: "join"}, {Source: "right", Target: "join"}, {Source: "join", Target: "combine"}, {Source: "combine", Target: "condition"}, {Source: "condition", Target: "yes", Branch: "true"}, {Source: "condition", Target: "no", Branch: "false"}, {Source: "yes", Target: "finish"}, {Source: "no", Target: "finish"}, {Source: "finish", Target: "__output"}}}}
	d = awCreateCodeDefinition(t, h, a, d, "code-parallel-create")
	run := awStartCodeTrial(t, h, a, d, source, map[string]string{}, "code-parallel-start")
	done := awTestRun(t, h, aliceToken, a.ID, d.ID, run.ID, af.Completed)
	if done.Steps[5].Branch != "true" || done.Steps[7].Status != af.Skipped || len(done.Steps[3].Output) == 0 {
		t.Fatal("typed join/condition failed", done)
	}
	var result map[string]int
	if json.Unmarshal(done.Steps[4].Output, &result) != nil || result["value"] != 70 {
		t.Fatal("parallel outputs collided", string(done.Steps[4].Output))
	}
}

func TestAgentWorkflowCodeFailureRetryAndCancellation(t *testing.T) {
	awRequireCodeRuntimes(t)
	h, _ := awGraphModelHarness(t, "", false)
	a := awTestAgent(t, h, "独立代码错误")
	source := h.studioConversationAs(aliceToken, a.ID)
	d := awCreateCodeDefinition(t, h, a, af.Definition{Name: "类型错误", Steps: []af.Step{awCodeStep("bad", "python", "def main(inputs):\n    print('before type error')\n    return {'value': 'wrong type'}\n", map[string]af.CodeBinding{}, map[string]af.ValueSchema{"value": {Type: "number"}})}}, "code-error-create")
	run := awStartCodeTrial(t, h, a, d, source, map[string]string{}, "code-error-start")
	failed := awGraphWaitTerminal(t, h, a, d, run.ID, af.Failed)
	if failed.Steps[0].CodeExecution == nil || failed.Steps[0].CodeExecution.ErrorCode != "output_schema_invalid" || failed.Steps[0].CodeExecution.Stdout == "" {
		t.Fatal("error/log receipt lost", failed)
	}
	oldHash := d.DefinitionHash
	d = awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "edit", map[string]any{"action": "patch", "request_id": "code-error-fix", "workflow_id": d.ID, "expected_revision": d.Revision, "operations": []map[string]any{{"op": "update_step", "step_id": "bad", "patch": map[string]any{"code": &af.CodeSpec{Language: "python", Source: "def main(inputs):\n    return {'value': 3}\n", Inputs: map[string]af.CodeBinding{}, Outputs: map[string]af.ValueSchema{"value": {Type: "number"}}}}}}}, 200), "workflow")
	retry := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "retry", "request_id": "code-error-retry", "workflow_id": d.ID, "run_id": run.ID, "source_session_id": source}, 200), "run")
	retry = awGraphWaitTerminal(t, h, a, d, retry.ID, af.Failed)
	if retry.VersionID != oldHash || retry.RetryOf != run.ID {
		t.Fatal("retry silently switched code version")
	}
	updated := awStartCodeTrial(t, h, a, d, source, map[string]string{}, "code-fixed-start")
	awTestRun(t, h, aliceToken, a.ID, d.ID, updated.ID, af.Completed)
	long := awCreateCodeDefinition(t, h, a, af.Definition{Name: "取消计算", Steps: []af.Step{awCodeStep("wait", "javascript", "async function main(inputs) { await new Promise(r => setTimeout(r, 15000)); return {value:1}; }", map[string]af.CodeBinding{}, map[string]af.ValueSchema{"value": {Type: "number"}})}}, "code-cancel-create")
	running := awStartCodeTrial(t, h, a, long, source, map[string]string{}, "code-cancel-start")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		v := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]string{"action": "run", "workflow_id": long.ID, "run_id": running.ID}, 200), "run")
		if v.Steps[0].Status == af.Running {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "cancel", "request_id": "code-cancel", "workflow_id": long.ID, "run_id": running.ID}, 200)
	awGraphWaitTerminal(t, h, a, long, running.ID, af.Cancelled)
}
