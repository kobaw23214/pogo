package web

import (
	"io"
	"net/http"
	"testing"

	af "github.com/yosukeno/pogo/agentworkflow"
	"github.com/yosukeno/pogo/tools"
)

func TestAgentWorkflowDeleteAPIKeepsHistoryAndExistingAuthorization(t *testing.T) {
	h := newMultiuserHarness(t, nil)
	a := awTestAgent(t, h, "删除测试助手")
	other := awTestAgent(t, h, "另一个助手")
	platformBefore := studioBody(h.mgr.workflows.list(true))
	create := map[string]any{"action": "create", "request_id": "delete-fixture-create", "name": "可删除草稿", "steps": []af.Step{{ID: "calculate", Kind: "code", Code: &af.CodeSpec{Language: "python", Source: "def main(inputs):\n    return {\"total\": inputs[\"value\"] + 1}", Inputs: map[string]af.CodeBinding{"value": {Type: "number", Value: []byte("4")}}, Outputs: map[string]af.ValueSchema{"total": {Type: "number"}}}}, {ID: "review", Kind: "confirm", Prompt: "请本人确认"}}}
	d := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "edit", create, 200), "workflow")
	request := map[string]any{"action": "delete", "workflow_id": d.ID, "expected_revision": d.Revision, "request_id": "delete-exact"}
	awTestCall(t, h, bobToken, a.ID, "edit", request, 404)
	awTestCall(t, h, aliceToken, other.ID, "edit", request, 404)
	deleted := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "edit", request, 200), "workflow")
	if !deleted.Deleted || !deleted.Archived || deleted.EnabledVersion != "" {
		t.Fatal("missing tombstone", deleted)
	}
	replay := awTestValue[bool](t, awTestCall(t, h, aliceToken, a.ID, "edit", request, 200), "reused")
	if !replay {
		t.Fatal("delete replay not idempotent")
	}
	list := awTestValue[[]af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "list"}, 200), "workflows")
	if len(list) != 0 {
		t.Fatal("deleted workflow leaked into list", list)
	}
	history := awTestValue[[]af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "list", "include_deleted": true}, 200), "workflows")
	if len(history) != 1 || !history[0].Deleted {
		t.Fatal("deleted history missing", history)
	}
	foreign := awTestValue[[]af.Definition](t, awTestCall(t, h, bobToken, a.ID, "read", map[string]any{"action": "list", "include_deleted": true}, 200), "workflows")
	if len(foreign) != 0 {
		t.Fatal("deleted scope leak", foreign)
	}
	saved := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "get", "workflow_id": d.ID}, 200), "workflow")
	if !saved.Deleted {
		t.Fatal("get lost tombstone")
	}
	version := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "version", "workflow_id": d.ID, "version_id": d.DefinitionHash, "target_revision": d.Revision}, 200), "workflow")
	if version.Deleted || version.DefinitionHash != d.DefinitionHash {
		t.Fatal("old saved version changed", version)
	}
	awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "trial", "workflow_id": d.ID, "request_id": "deleted-trial", "expected_revision": deleted.Revision, "expected_hash": deleted.DefinitionHash}, 409)
	awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "test_node", "workflow_id": d.ID, "step_id": "calculate", "node_inputs": map[string]any{"value": 4}, "request_id": "deleted-node-test", "expected_revision": deleted.Revision, "expected_hash": deleted.DefinitionHash}, 409)
	retainedRuns := awTestValue[[]af.Run](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "runs", "workflow_id": d.ID}, 200), "runs")
	if len(retainedRuns) != 0 {
		t.Fatal("deleted execution attempt created Run", retainedRuns)
	}
	awTestCall(t, h, aliceToken, a.ID, "enable", map[string]any{"action": "enable", "workflow_id": d.ID, "expected_revision": deleted.Revision, "expected_hash": deleted.DefinitionHash}, 409)
	awTestCall(t, h, aliceToken, a.ID, "edit", map[string]any{"action": "restore", "workflow_id": d.ID, "request_id": "deleted-restore", "expected_revision": deleted.Revision, "version_id": d.DefinitionHash, "target_revision": d.Revision}, 409)
	// Revoking the pre-existing edit capability also denies deletion; no new grant exists.
	body := studioAgentBody(other.Name, other.Instructions, "ready")
	body["revision"], body["tools"], body["skills"], body["mcp_servers"] = other.Revision, []string{tools.AgentWorkflowReadName}, []string{}, []string{}
	response := h.doAs(h.token, http.MethodPatch, "/api/manage/agents/"+other.ID, studioBody(body))
	defer response.Body.Close()
	if response.StatusCode != 200 {
		b, _ := io.ReadAll(response.Body)
		t.Fatalf("revoke edit %d %s", response.StatusCode, b)
	}
	awTestCall(t, h, aliceToken, other.ID, "edit", request, 403)
	if platformBefore != studioBody(h.mgr.workflows.list(true)) {
		t.Fatal("platform workflows changed")
	}
}
