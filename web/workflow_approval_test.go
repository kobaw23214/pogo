package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/schedule"
	"github.com/yosukeno/pogo/taskqueue"
	"github.com/yosukeno/pogo/workflow"
)

func approvalFixture(t *testing.T, approvers []string, assignment string) (*muHarness, Workflow) {
	t.Helper()
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { return textTurn("approved result"), nil })
	wf := approvalFixtureWorkflow(t, h, approvers, assignment)
	return h, wf
}

func approvalFixtureWorkflow(t *testing.T, h *muHarness, approvers []string, assignment string) Workflow {
	t.Helper()
	wf, err := h.mgr.workflows.put("", workflowInput{Name: "独立审批分配", Nodes: []workflow.Node{{ID: "review", Kind: workflow.KindHuman, Prompt: "Review", ApprovalPolicy: &workflow.ApprovalPolicy{PreventSelfApproval: true, Assignment: assignment, Approvers: approvers}}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.mgr.workflows.publish(wf.ID, "v1", ""); err != nil {
		t.Fatal(err)
	}
	return wf
}

func approvalFixtureRecord(t *testing.T, h *muHarness, wid, rid, token string) workflow.RunRecord {
	t.Helper()
	code, raw := workflowServiceRequest(t, h.srv.URL, token, "GET", "/api/workflows/"+wid+"/runs/"+rid, "", "")
	if code != 200 {
		t.Fatalf("get assigned record: %d %s", code, raw)
	}
	var rec workflow.RunRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestWorkflowApprovalFixedAssignmentNoSelfAndExplicitGrants(t *testing.T) {
	h, wf := approvalFixture(t, []string{"alice", "bob"}, "fixed")
	rid := workflowServiceStart(t, h.srv.URL, aliceToken, wf.ID, "assign-once", 201)
	rec := awaitWorkflowService(t, h.mgr, rid, func(r workflow.RunRecord) bool { return r.Nodes["review"].Status == workflow.NodeWaitingHuman })
	db := taskRunsDB(t, h.mgr)
	aliceID := identity.LocalPrincipal(identity.LocalScope("alice"), "alice", false).ID
	bobID := identity.LocalPrincipal(identity.LocalScope("bob"), "bob", false).ID
	meta, err := db.GetWorkflowRun(rid)
	if err != nil || !slices.Equal(meta.Approvals["review"].Assignees, []string{bobID}) {
		t.Fatalf("automatic assignment=%+v %v", meta.Approvals, err)
	}
	alice := approvalFixtureRecord(t, h, wf.ID, rid, aliceToken)
	bob := approvalFixtureRecord(t, h, wf.ID, rid, bobToken)
	if alice.Capabilities.CanApprove || alice.Approvals["review"].BlockedReason != "initiator_cannot_approve" || !bob.Capabilities.CanApprove || bob.Capabilities.CanCancel || bob.Capabilities.CanRetry || bob.Capabilities.CanManageParticipants {
		t.Fatalf("wrong approval capability: alice=%+v bob=%+v", alice.Capabilities, bob.Capabilities)
	}
	if len(bob.Approvals["review"].Assignees) != 1 || bob.Approvals["review"].Assignees[0].DisplayName != "bob" {
		t.Fatalf("assignment projection=%+v", bob.Approvals)
	}
	path := "/api/workflows/" + wf.ID + "/runs/" + rid
	for _, token := range []string{aliceToken, h.token} {
		code, _ := workflowServiceRequest(t, h.srv.URL, token, "POST", path+"/nodes/review/resume", `{"action":"approve"}`, "")
		if code != 404 {
			t.Fatalf("owner or unassigned operator approved: %d", code)
		}
	}
	if err := h.mgr.enqueueWorkflowDecision(rec, workflow.HumanDecision{Node: "review", Action: "approve", Actor: aliceID}); err == nil {
		t.Fatal("internal decision path bypassed separation policy")
	}
	redacted := rec
	redacted.Definition.Nodes = slices.Clone(rec.Definition.Nodes)
	redacted.Definition.Nodes[0].ApprovalPolicy = nil
	if err := h.mgr.enqueueWorkflowDecision(redacted, workflow.HumanDecision{Node: "review", Action: "approve", Actor: aliceID}); err == nil {
		t.Fatal("redacted projection bypassed catalog approval policy")
	}
	if code, _ := workflowServiceRequest(t, h.srv.URL, aliceToken, "PUT", path+"/participants", `{"principal_id":`+jsonQuoteApproval(aliceID)+`,"node_id":"review","actions":["run.read","approval.decide"]}`, ""); code != 400 {
		t.Fatalf("self participant approval grant accepted: %d", code)
	}
	// Even a privileged raw grant and administrator role cannot turn a
	// configured initiator into the approver of their own request.
	if err := db.SetRunGrant(rid, aliceID, "review", []string{wfRunRead, wfRunApprove}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetAccessRole(aliceID, "space_admin"); err != nil {
		t.Fatal(err)
	}
	if code, _ := workflowServiceRequest(t, h.srv.URL, aliceToken, "POST", path+"/nodes/review/resume", `{"action":"approve"}`, ""); code != 404 {
		t.Fatal("space_admin bypassed self-approval prohibition")
	}
	if err := db.SetRunGrant(rid, bobID, "review", nil); err != nil {
		t.Fatal(err)
	}
	if code, _ := workflowServiceRequest(t, h.srv.URL, bobToken, "POST", path+"/nodes/review/resume", `{"action":"approve"}`, ""); code != 404 {
		t.Fatal("revoked automatic assignment remained usable")
	}
	if err := db.SetRunGrant(rid, bobID, "review", []string{wfRunRead, wfRunApprove, wfArtifactRead}); err != nil {
		t.Fatal(err)
	}
	if code, _ := workflowServiceRequest(t, h.srv.URL, bobToken, "POST", path+"/nodes/review/resume", `{"action":"approve"}`, ""); code != 200 {
		t.Fatalf("assigned reviewer failed: %d", code)
	}
	final := awaitWorkflowService(t, h.mgr, rid, func(r workflow.RunRecord) bool { return r.Status == workflow.RunCompleted })
	segments, err := db.ListWorkflowExecutions(rid)
	if err != nil || len(segments) != 2 || final.ID != rid || final.Nodes["review"].Human.DecidedBy != bobID {
		t.Fatalf("continuation did not retain one audited Run: %+v %+v %v", final, segments, err)
	}
}

func jsonQuoteApproval(value string) string { raw, _ := json.Marshal(value); return string(raw) }

func TestWorkflowApprovalNoEligibleAdmissionAndIdempotentReceipt(t *testing.T) {
	h, wf := approvalFixture(t, []string{"alice", "bob"}, "fixed")
	rid := workflowServiceStart(t, h.srv.URL, aliceToken, wf.ID, "accepted", 201)
	db := taskRunsDB(t, h.mgr)
	bobID := identity.LocalPrincipal(identity.LocalScope("bob"), "bob", false).ID
	if err := db.SetAccessRole(bobID, "disabled"); err != nil {
		t.Fatal(err)
	}
	if again := workflowServiceStart(t, h.srv.URL, aliceToken, wf.ID, "accepted", 201); again != rid {
		t.Fatal("lost receipt created a second Run after reviewer disable")
	}
	workflowServiceStart(t, h.srv.URL, aliceToken, wf.ID, "new-intent", 400)
	rows, _ := db.ListWorkflowRuns(taskqueue.WorkflowRunFilter{WorkflowID: wf.ID})
	if len(rows) != 1 {
		t.Fatal("invalid routing left a queued Run")
	}
	policyOnlySelf := approvalFixtureWorkflow(t, h, []string{"alice"}, "fixed")
	workflowServiceStart(t, h.srv.URL, aliceToken, policyOnlySelf.ID, "self-only", 400)
	business := approvalFixtureWorkflow(t, h, nil, "business")
	workflowServiceStart(t, h.srv.URL, aliceToken, business.ID, "no-trusted-routing", 400)
	// Caller-controlled JSON cannot inject approvers into the ordinary API.
	code, _ := workflowServiceRequest(t, h.srv.URL, aliceToken, "POST", "/api/workflows/"+business.ID+"/runs", `{"approvers":{"review":["bob"]},"business_approvers":{"review":["bob"]}}`, "browser-routing")
	if code != 400 {
		t.Fatal("browser-controlled approval routing was accepted")
	}
}

func TestWorkflowApprovalRestartAndRetryExcludeActualInitiator(t *testing.T) {
	h, wf := approvalFixture(t, []string{"alice", "bob", "carol"}, "fixed")
	h.psrv.auth.mu.Lock()
	h.psrv.auth.users["carol"] = userRecord{Username: "carol", Token: "approval-carol-test-token"}
	h.psrv.auth.mu.Unlock()
	rid := workflowServiceStart(t, h.srv.URL, aliceToken, wf.ID, "before-restart", 201)
	awaitWorkflowService(t, h.mgr, rid, func(r workflow.RunRecord) bool { return r.Nodes["review"].Status == workflow.NodeWaitingHuman })
	// Reopen the same on-disk Manager, preserving account identities and grants.
	cfg := h.mgr.cfg
	h.srv.Close()
	h.psrv.Close()
	h.mgr.Close()
	m, err := NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	s, err := NewServer(m, ServerOptions{Token: h.token})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	s.auth.mu.Lock()
	s.auth.path = "approval-test-users.json"
	s.auth.users = map[string]userRecord{"alice": {Username: "alice", Token: aliceToken}, "bob": {Username: "bob", Token: bobToken}, "carol": {Username: "carol", Token: "approval-carol-test-token"}}
	s.auth.mu.Unlock()
	server := httptest.NewServer(s.Handler())
	t.Cleanup(server.Close)
	h.mgr, h.psrv, h.srv = m, s, server
	if rec := approvalFixtureRecord(t, h, wf.ID, rid, bobToken); !rec.Capabilities.CanApprove || rec.Nodes["review"].Status != workflow.NodeWaitingHuman {
		t.Fatal("approval routing did not survive process restart")
	}
	if code, _ := workflowServiceRequest(t, server.URL, aliceToken, "POST", "/api/workflows/"+wf.ID+"/runs/"+rid+"/cancel", "", ""); code != 200 {
		t.Fatalf("cancel: %d", code)
	}
	old := awaitWorkflowService(t, m, rid, func(r workflow.RunRecord) bool { return r.Status == workflow.RunCancelled })
	bobID := identity.LocalPrincipal(identity.LocalScope("bob"), "bob", false).ID
	carolID := identity.LocalPrincipal(identity.LocalScope("carol"), "carol", false).ID
	db := taskRunsDB(t, m)
	if err := db.SetRunGrant(rid, bobID, "", []string{wfRunRead, wfRunRetry}); err != nil {
		t.Fatal(err)
	}
	front := taskCenterTestFront(t, s)
	code, _, raw := taskCenterRequest(t, front, bobToken, "POST", "/api/task-runs/"+rid+"/retry", `{"revision":`+fmtApprovalRevision(old.StateRevision+1)+`}`)
	if code != 200 {
		t.Fatalf("task-center retry: %d %s", code, raw)
	}
	rows, _ := db.ListWorkflowRuns(taskqueue.WorkflowRunFilter{WorkflowID: wf.ID, Source: "retry"})
	if len(rows) != 1 || rows[0].InitiatorID != bobID || !slices.Equal(rows[0].Approvals["review"].Assignees, []string{carolID}) {
		t.Fatalf("retry did not exclude actual initiator: %+v", rows)
	}
	retry := rows[0]
	awaitWorkflowService(t, m, retry.ID, func(r workflow.RunRecord) bool { return r.Nodes["review"].Status == workflow.NodeWaitingHuman })
	if code, _ := workflowServiceRequest(t, server.URL, bobToken, "POST", "/api/workflows/"+wf.ID+"/runs/"+retry.ID+"/nodes/review/resume", `{"action":"approve"}`, ""); code != 404 {
		t.Fatal("retry initiator acquired self approval")
	}
	if code, _ := workflowServiceRequest(t, server.URL, "approval-carol-test-token", "POST", "/api/workflows/"+wf.ID+"/runs/"+retry.ID+"/nodes/review/resume", `{"action":"approve"}`, ""); code != 200 {
		t.Fatalf("retry assignee could not approve: %d", code)
	}
}

func fmtApprovalRevision(value int64) string { raw, _ := json.Marshal(value); return string(raw) }

func TestWorkflowApprovalScheduledBindingAndAgentApproval(t *testing.T) {
	scheduleEnv(t)
	h, wf := approvalFixture(t, []string{"bob"}, "fixed")
	db := taskRunsDB(t, h.mgr)
	job := taskCenterAddSchedule(t, h.psrv, "approval-schedule", "", wf.ID, nil)
	segment := taskCenterEnqueue(t, db, job, "approval-initial")
	segment, err := db.Claim(segment.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	def, _ := h.mgr.workflows.publishedDefinition(wf.ID)
	ctx := context.WithValue(context.Background(), taskRunContextKey{}, taskRunBinding{runtime: h.mgr.taskRuns, id: segment.ID, revision: segment.Revision})
	out := h.mgr.runWorkflowJobQueued(ctx, schedule.Job{ID: job.ID, WorkflowID: wf.ID}, def)
	if out.Err != "" || out.SessionID == "" {
		t.Fatalf("scheduled approval: %+v", out)
	}
	meta, err := db.GetWorkflowRun(out.SessionID)
	if err != nil || len(meta.Approvals["review"].Assignees) != 1 {
		t.Fatalf("scheduled initial segment bypassed routing: %+v %v", meta, err)
	}
	// Missing business routing on a scheduled first segment is rejected before
	// executing, instead of falling back to a service/owner approval grant.
	business := approvalFixtureWorkflow(t, h, nil, "business")
	def, _ = h.mgr.workflows.publishedDefinition(business.ID)
	if _, err := h.mgr.admitWorkflow(def, nil, "schedule", "business-schedule", "schedule-no-route", ""); !errors.Is(err, errWorkflowApprovalAssignment) {
		t.Fatalf("scheduled business route bypass: %v", err)
	}
	agent, err := h.mgr.studio.put("", studioInput{Name: "审核草稿", Model: "k3", Instructions: "reply", Status: "ready"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	agentWF, err := h.mgr.workflows.put("", workflowInput{Name: "Agent结果审批", Nodes: []workflow.Node{{ID: "draft", Agent: agent.ID, Task: "produce draft", Approval: true, ApprovalPolicy: &workflow.ApprovalPolicy{PreventSelfApproval: true, Assignment: "fixed", Approvers: []string{"bob"}}}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.mgr.workflows.publish(agentWF.ID, "v1", ""); err != nil {
		t.Fatal(err)
	}
	rid := workflowServiceStart(t, h.srv.URL, aliceToken, agentWF.ID, "agent-result", 201)
	awaitWorkflowService(t, h.mgr, rid, func(r workflow.RunRecord) bool { return r.Nodes["draft"].Status == workflow.NodeWaitingHuman })
	if rec := approvalFixtureRecord(t, h, agentWF.ID, rid, aliceToken); rec.Capabilities.CanApprove {
		t.Fatal("Agent result owner retained automatic self approval")
	}
	if code, _ := workflowServiceRequest(t, h.srv.URL, bobToken, "POST", "/api/workflows/"+agentWF.ID+"/runs/"+rid+"/nodes/draft/resume", `{"action":"approve"}`, ""); code != 200 {
		t.Fatalf("Agent result assignee: %d", code)
	}
	awaitWorkflowService(t, h.mgr, rid, func(r workflow.RunRecord) bool { return r.Status == workflow.RunCompleted })
}
