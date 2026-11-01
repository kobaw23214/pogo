package web

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	af "github.com/yosukeno/pogo/agentworkflow"
	"github.com/yosukeno/pogo/tools"
)

func TestAgentWorkflowCodePolicyRetriesFallbackAndFrozenHistory(t *testing.T) {
	awRequireCodeRuntimes(t)
	h, model := awGraphModelHarness(t, "", false)
	a := awTestAgent(t, h, "独立失败处理")
	source := h.studioConversationAs(aliceToken, a.ID)
	first := awCodeStep("parse", "python", "def main(inputs):\n    print('attempt executed')\n    return {'value': 'invalid type'}\n", map[string]af.CodeBinding{}, map[string]af.ValueSchema{"value": {Type: "number"}})
	first.Policy = &af.CodePolicy{MaxRetries: 2, RetryDelayMS: 1, OnFailure: "fallback", Fallback: json.RawMessage(`{"value":42}`)}
	d := awCreateCodeDefinition(t, h, a, af.Definition{Name: "重试与备用输出", Steps: []af.Step{first, awCodeStep("consume", "javascript", "function main(inputs){ return {value:inputs.value*2}; }", map[string]af.CodeBinding{"value": {Type: "number", Source: "steps.parse.output.value"}}, map[string]af.ValueSchema{"value": {Type: "number"}})}}, "policy-create")
	run := awStartCodeTrial(t, h, a, d, source, map[string]string{}, "policy-start")
	done := awTestRun(t, h, aliceToken, a.ID, d.ID, run.ID, af.Completed)
	if !done.Steps[0].FallbackUsed || len(done.Steps[0].Attempts) != 3 || string(done.Steps[0].Output) != `{"value":42}` || string(done.Steps[1].Output) != `{"value":84}` {
		t.Fatal("fallback or actual attempts missing", done)
	}
	for i, attempt := range done.Steps[0].Attempts {
		if attempt.Number != i+1 || attempt.Error == "" || attempt.Execution == nil || attempt.Execution.ErrorCode != "output_schema_invalid" || attempt.FinishedAt < attempt.StartedAt || len(attempt.Output) == 0 {
			t.Fatal("failed attempt disappeared", attempt)
		}
	}
	node := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "test_node", "request_id": "policy-node", "workflow_id": d.ID, "step_id": "parse", "source_session_id": source, "expected_revision": d.Revision, "expected_hash": d.DefinitionHash, "node_inputs": map[string]any{}}, 200), "run")
	node = awTestRun(t, h, aliceToken, a.ID, d.ID, node.ID, af.Completed)
	if !node.Steps[0].FallbackUsed || len(node.Steps[0].Attempts) != 3 || node.Steps[1].Status != af.Skipped {
		t.Fatal("single node did not preserve policy audit", node)
	}
	model.mu.Lock()
	calls := len(model.began)
	model.mu.Unlock()
	if calls != 0 {
		t.Fatal("policy invoked model")
	}
	// Changing the draft policy does not change the already completed run.
	awTestCall(t, h, aliceToken, a.ID, "edit", map[string]any{"action": "patch", "request_id": "policy-draft-change", "workflow_id": d.ID, "expected_revision": d.Revision, "operations": []map[string]any{{"op": "update_step", "step_id": "parse", "patch": map[string]any{"policy": map[string]any{"on_failure": "fallback", "fallback": map[string]int{"value": 99}}}}}}, 200)
	old := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "run", "workflow_id": d.ID, "run_id": run.ID}, 200), "run")
	if old.VersionID != done.VersionID || string(old.Steps[0].Output) != `{"value":42}` || len(old.Steps[0].Attempts) != 3 {
		t.Fatal("old policy history changed")
	}
}

func TestAgentWorkflowCodePolicyCancelDuringRetryDelay(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "revoke"}[revoke], func(t *testing.T) {
			awRequireCodeRuntimes(t)
			h, _ := awGraphModelHarness(t, "", false)
			a := awTestAgent(t, h, "独立重试取消")
			source := h.studioConversationAs(aliceToken, a.ID)
			step := awCodeStep("fail", "javascript", "function main(inputs){throw new Error('synthetic failure');}", map[string]af.CodeBinding{}, map[string]af.ValueSchema{"value": {Type: "number"}})
			step.Policy = &af.CodePolicy{MaxRetries: 2, RetryDelayMS: 1500, OnFailure: "fallback", Fallback: json.RawMessage(`{"value":99}`)}
			d := awCreateCodeDefinition(t, h, a, af.Definition{Name: "取消重试", Steps: []af.Step{step}}, "policy-cancel-create")
			run := awStartCodeTrial(t, h, a, d, source, map[string]string{}, "policy-cancel-start")
			found := false
			for until := time.Now().Add(5 * time.Second); time.Now().Before(until); {
				live := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "run", "workflow_id": d.ID, "run_id": run.ID}, 200), "run")
				if len(live.Steps[0].Attempts) == 1 && live.Steps[0].Attempts[0].FinishedAt > 0 {
					found = true
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !found {
				t.Fatal("retry failure was not persisted before waiting")
			}
			want := af.Cancelled
			if revoke {
				body := studioAgentBody(a.Name, a.Instructions, "ready")
				body["revision"], body["tools"], body["skills"], body["mcp_servers"] = a.Revision, []string{tools.AgentWorkflowReadName}, []string{}, []string{}
				response := h.doAs(h.token, http.MethodPatch, "/api/manage/agents/"+a.ID, studioBody(body))
				defer response.Body.Close()
				if response.StatusCode != 200 {
					t.Fatal("revoke failed", response.StatusCode)
				}
				want = af.Interrupted
			} else {
				awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "cancel", "request_id": "policy-cancel", "workflow_id": d.ID, "run_id": run.ID}, 200)
			}
			stopped := awGraphWaitTerminal(t, h, a, d, run.ID, want)
			if len(stopped.Steps[0].Attempts) != 1 || stopped.Steps[0].FallbackUsed {
				t.Fatal("cancelled code retried or generated fallback", stopped)
			}
		})
	}
}
