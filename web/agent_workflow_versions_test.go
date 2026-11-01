package web

import (
	"encoding/json"
	"net/http"
	"testing"

	af "github.com/yosukeno/pogo/agentworkflow"
	"github.com/yosukeno/pogo/tools"
)

func TestAgentWorkflowVersionsHTTPPrecisePreviewRestoreAndPermissions(t *testing.T) {
	h := newMultiuserHarness(t, nil)
	a := awTestAgent(t, h, "版本管理测试助手")
	d1 := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "edit", map[string]any{"action": "create", "request_id": "version-create", "name": "最初的流程", "steps": []af.Step{{ID: "review", Kind: "confirm", Title: "本人确认", Prompt: "请检查独立测试资料"}}}, 200), "workflow")
	d2 := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "edit", map[string]any{"action": "patch", "workflow_id": d1.ID, "expected_revision": 1, "request_id": "version-save-two", "operations": []any{map[string]any{"op": "set_metadata", "name": "已启用的版本"}}}, 200), "workflow")
	awTestCall(t, h, aliceToken, a.ID, "enable", map[string]any{"action": "enable", "workflow_id": d1.ID, "expected_revision": 2, "expected_hash": d2.DefinitionHash}, 200)
	d3 := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "edit", map[string]any{"action": "patch", "workflow_id": d1.ID, "expected_revision": 2, "request_id": "version-save-three", "operations": []any{map[string]any{"op": "set_metadata", "name": "最新未启用草稿"}}}, 200), "workflow")
	page := awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "versions", "workflow_id": d1.ID}, 200)
	versions := awTestValue[[]af.VersionSummary](t, page, "versions")
	enabled := awTestValue[af.VersionSummary](t, page, "enabled_version")
	if len(versions) != 3 || versions[0].Revision != 3 || !versions[0].IsCurrentDraft || versions[0].IsEnabled || enabled.Revision != 2 || enabled.VersionID != d2.DefinitionHash || !versions[1].IsEnabled {
		t.Fatal("wrong visible version identities", versions, enabled)
	}
	preview := awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "version", "workflow_id": d1.ID, "version_id": d1.DefinitionHash, "target_revision": 1}, 200)
	snapshot := awTestValue[af.Definition](t, preview, "workflow")
	if snapshot.OwnerID != "" || snapshot.AgentID != a.ID || snapshot.Name != d1.Name || snapshot.Revision != 1 || snapshot.DefinitionHash != d1.DefinitionHash {
		t.Fatal("preview was not the exact public historical snapshot", snapshot)
	}
	restore := map[string]any{"action": "restore", "workflow_id": d1.ID, "expected_revision": d3.Revision, "version_id": d1.DefinitionHash, "target_revision": 1, "request_id": "restore-first"}
	result := awTestCall(t, h, aliceToken, a.ID, "edit", restore, 200)
	d4 := awTestValue[af.Definition](t, result, "workflow")
	if d4.Revision != 4 || d4.Name != d1.Name || d4.EnabledVersion != d2.DefinitionHash || d4.OwnerID != "" {
		t.Fatal("restore changed the live version or exposed private scope", d4)
	}
	if !awTestValue[bool](t, awTestCall(t, h, aliceToken, a.ID, "edit", restore, 200), "reused") {
		t.Fatal("restore retry was not idempotent")
	}
	stale := map[string]any{"action": "restore", "workflow_id": d1.ID, "expected_revision": 3, "version_id": d1.DefinitionHash, "target_revision": 1, "request_id": "stale-restore"}
	awTestCall(t, h, aliceToken, a.ID, "edit", stale, 409)
	awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "version", "workflow_id": d1.ID, "version_id": d1.DefinitionHash, "target_revision": 2}, 404)
	awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "versions", "workflow_id": d1.ID, "before_revision": -1}, 409)
	for _, action := range []string{"versions", "version"} {
		awTestCall(t, h, bobToken, a.ID, "read", map[string]any{"action": action, "workflow_id": d1.ID, "version_id": d1.DefinitionHash, "target_revision": 1}, 404)
	}
	restore["expected_revision"], restore["request_id"] = 4, "foreign-restore"
	awTestCall(t, h, bobToken, a.ID, "edit", restore, 404)
	other := awTestAgent(t, h, "另一个Agent")
	awTestCall(t, h, aliceToken, other.ID, "read", map[string]any{"action": "versions", "workflow_id": d1.ID}, 404)
	awTestCall(t, h, aliceToken, other.ID, "edit", restore, 404)
	// Existing edit permission is still the complete restore authority. Read
	// access remains available after revocation, with no hidden activation.
	body := studioAgentBody(a.Name, a.Instructions, "ready")
	body["revision"], body["tools"], body["skills"], body["mcp_servers"] = a.Revision, []string{tools.AgentWorkflowReadName}, []string{}, []string{}
	response := h.doAs(h.token, http.MethodPatch, "/api/manage/agents/"+a.ID, studioBody(body))
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("could not revoke edit permission", response.StatusCode)
	}
	restore["request_id"] = "revoked-restore"
	awTestCall(t, h, aliceToken, a.ID, "edit", restore, 403)
	page = awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "versions", "workflow_id": d1.ID}, 200)
	var caps agentWorkflowCaps
	if err := json.Unmarshal(page["capabilities"], &caps); err != nil || caps.Edit {
		t.Fatal("history widened edit authority", err)
	}
	retained := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "get", "workflow_id": d1.ID}, 200), "workflow")
	if retained.Revision != 4 || retained.EnabledVersion != d2.DefinitionHash {
		t.Fatal("rejected restore modified workflow state", retained)
	}
}
