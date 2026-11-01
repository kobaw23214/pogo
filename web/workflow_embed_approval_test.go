package web

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/taskqueue"
	"github.com/yosukeno/pogo/workflow"
)

func dynamicApprovalEmbedFixture(t *testing.T) (*muHarness, Workflow, workflowPublication, string) {
	t.Helper()
	h, wf, p, secret := workflowEmbedFixture(t)
	wf.Nodes[0].ApprovalPolicy = &workflow.ApprovalPolicy{PreventSelfApproval: true, Assignment: "business"}
	updated, err := h.mgr.workflows.put(wf.ID, wf.workflowInput, wf.Revision)
	if err != nil {
		t.Fatal(err)
	}
	ptr, err := h.mgr.workflows.publish(wf.ID, "业务审批规则", "")
	if err != nil {
		t.Fatal(err)
	}
	p.VersionID = ptr.VersionID
	p.Mappings = append(p.Mappings, workflowPublicationMapping{Subject: "employee-a-alias", Username: "alice"})
	response := embedJSON(t, h, h.token, "PUT", "/api/manage/workflows/"+wf.ID+"/publications/"+p.ID, p, 200)
	if err := json.Unmarshal(mustMarshalCallback(t, response), &p); err != nil {
		t.Fatal(err)
	}
	return h, updated, p, secret
}

func TestWorkflowApprovalEditorSaveRetainsPublishedMetadataAndCapabilities(t *testing.T) {
	h, wf, p, _ := dynamicApprovalEmbedFixture(t)
	alice := identity.LocalPrincipal(identity.LocalScope("alice"), "alice", false)
	if err := taskRunsDB(t, h.mgr).SetAccessRole(alice.ID, "builder"); err != nil {
		t.Fatal(err)
	}
	response := embedJSON(t, h, aliceToken, "PATCH", "/api/manage/workflows/"+wf.ID, map[string]any{
		"revision": wf.Revision, "name": wf.Name, "description": "保存新的审批配置草稿", "nodes": wf.Nodes,
	}, 200)
	var saved workflowView
	if err := json.Unmarshal(mustMarshalCallback(t, response), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Published == nil || saved.Published.VersionID != p.VersionID || !saved.Dirty || saved.Capabilities == nil || !saved.Capabilities.CanEdit || saved.Capabilities.CanPublish || saved.Capabilities.CanManagePublications {
		t.Fatalf("saving draft lost publication or changed builder permissions: %+v", saved)
	}
	if saved.Nodes[0].ApprovalPolicy == nil || saved.Nodes[0].ApprovalPolicy.Assignment != "business" {
		t.Fatal("approval configuration disappeared from the save acknowledgement")
	}
}

func dynamicApprovalSession(t *testing.T, h *muHarness, p workflowPublication, secret string, approvers map[string][]string, want int) string {
	t.Helper()
	launch := embedJSON(t, h, secret, "POST", embedPath(p, "launch"), map[string]any{
		"subject": "employee-a", "origin": "https://business.example", "business_id": "ORDER-A",
		"approver_subjects": approvers, "inputs": map[string]string{"case_id": "ORDER-A"},
	}, want)
	if want != http.StatusCreated {
		return ""
	}
	return embedString(t, embedJSON(t, h, "", "POST", embedPath(p, "session"), map[string]string{
		"code": embedString(t, launch, "launch_code"), "parent_origin": "https://business.example",
	}, 201), "session_token")
}

func TestWorkflowEmbedApprovalOnlyTrustedMappedCandidatesCanAssign(t *testing.T) {
	h, wf, p, secret := dynamicApprovalEmbedFixture(t)
	for _, subjects := range []map[string][]string{
		{"unknown-node": {"employee-b"}}, {"review": {"unmapped-subject"}}, {"review": {}},
	} {
		dynamicApprovalSession(t, h, p, secret, subjects, 400)
	}
	// An ordinary browser cannot place an approver directly in POST /runs.
	without := dynamicApprovalSession(t, h, p, secret, nil, 201)
	embedJSON(t, h, without, "POST", embedPath(p, "runs"), map[string]any{
		"inputs": map[string]string{"case_id": "ORDER-A"}, "request_key": "untrusted-browser",
		"approver_subjects": map[string][]string{"review": {"employee-b"}},
	}, 400)
	// Different external subjects resolving to the same account remain self.
	alias := dynamicApprovalSession(t, h, p, secret, map[string][]string{"review": {"employee-a-alias"}}, 201)
	embedJSON(t, h, alias, "POST", embedPath(p, "runs"), map[string]any{
		"inputs": map[string]string{"case_id": "ORDER-A"}, "request_key": "alias-self",
	}, 400)
	if rows, err := taskRunsDB(t, h.mgr).ListWorkflowRuns(taskqueue.WorkflowRunFilter{WorkflowID: wf.ID}); err != nil || len(rows) != 0 {
		t.Fatalf("invalid assignments admitted work: %d %v", len(rows), err)
	}
	// Only the integration-authenticated exchange can attach the valid mapping.
	alice := dynamicApprovalSession(t, h, p, secret, map[string][]string{"review": {"employee-b"}}, 201)
	rid := embedString(t, embedJSON(t, h, alice, "POST", embedPath(p, "runs"), map[string]any{
		"inputs": map[string]string{"case_id": "ORDER-A"}, "request_key": "trusted-assignment",
		"approver_subjects": map[string][]string{"review": {"employee-a"}},
	}, 201), "id")
	awaitWorkflowService(t, h.mgr, rid, func(rec workflow.RunRecord) bool { return rec.Nodes["review"].Status == workflow.NodeWaitingHuman })
	self := embedJSON(t, h, alice, "GET", embedPath(p, "runs/"+rid), nil, 200)
	var rec workflow.RunRecord
	_ = json.Unmarshal(mustMarshalCallback(t, self), &rec)
	if rec.Capabilities == nil || rec.Capabilities.CanApprove || rec.Approvals["review"].BlockedReason != "initiator_cannot_approve" {
		t.Fatalf("initiator acquired approval through browser input: %+v", rec)
	}
	embedJSON(t, h, alice, "POST", embedPath(p, "runs/"+rid+"/nodes/review/resume"), map[string]any{"action": "approve", "fields": map[string]string{"note": "self"}}, 404)
	bob := businessEmbedSession(t, h, p, secret, "employee-b", "ORDER-A", rid, 201)
	view := embedJSON(t, h, bob, "GET", embedPath(p, "runs/"+rid), nil, 200)
	_ = json.Unmarshal(mustMarshalCallback(t, view), &rec)
	if rec.Capabilities == nil || !rec.Capabilities.CanApprove || rec.Capabilities.CanCancel || len(rec.Approvals["review"].Assignees) != 1 || rec.Approvals["review"].Assignees[0].DisplayName != "bob" {
		t.Fatalf("mapped reviewer did not get the precise node assignment: %+v", rec)
	}
	embedJSON(t, h, bob, "POST", embedPath(p, "runs/"+rid+"/nodes/review/resume"), map[string]any{"action": "approve", "fields": map[string]string{"note": "trusted business reviewer"}}, 200)
	awaitWorkflowService(t, h.mgr, rid, func(rec workflow.RunRecord) bool { return rec.Status == workflow.RunCompleted })
}
