package taskqueue

import (
	"errors"
	"slices"
	"testing"
)

func TestWorkflowApprovalAdmissionAtomicAndPersistent(t *testing.T) {
	s, path := testStore(t)
	meta := workflowMetaFixture("run_approval", "alice")
	meta.Approvals = map[string]WorkflowApprovalAssignment{"review": {PreventSelfApproval: true, Assignment: "fixed", Candidates: []string{"alice", "bob"}, Assignees: []string{"bob"}}}
	if _, err := s.db.Exec(`CREATE TRIGGER approval_fail BEFORE INSERT ON workflow_run_grants WHEN NEW.node_id='review' BEGIN SELECT RAISE(ABORT,'approval unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.AdmitWorkflow(meta, "approval-request", workflowRequestFixture()); err == nil {
		t.Fatal("admission acknowledged before assigning approvers")
	}
	if _, err := s.GetWorkflowRun(meta.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("assignment failure left a Run: %v", err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER approval_fail`); err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := s.AdmitWorkflow(meta, "approval-request", workflowRequestFixture()); err != nil || !created {
		t.Fatalf("admission failed: %v %v", created, err)
	}
	meta.Approvals["review"].Assignees[0] = "mutated"
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.GetWorkflowRun(meta.ID)
	if err != nil || !slices.Equal(persisted.Approvals["review"].Assignees, []string{"bob"}) {
		t.Fatalf("assignment did not survive reopening: %+v %v", persisted, err)
	}
	grant, err := reopened.RunGrant(meta.ID, "bob", "review")
	if err != nil || !slices.Equal(grant, []string{"approval.decide", "artifact.read", "run.read"}) {
		t.Fatalf("assignment grant=%v %v", grant, err)
	}
	if grant, err := reopened.RunGrant(meta.ID, "bob", ""); err != nil || len(grant) != 0 {
		t.Fatalf("assignment widened whole-Run capabilities: %v %v", grant, err)
	}
	persisted.ID = "run_illegal_self"
	persisted.Approvals["review"] = WorkflowApprovalAssignment{PreventSelfApproval: true, Assignment: "fixed", Candidates: []string{"alice"}, Assignees: []string{"alice"}}
	if _, _, _, err := reopened.AdmitWorkflow(persisted, "bad", workflowRequestFixture()); !errors.Is(err, ErrConflict) {
		t.Fatalf("store accepted a self assignment: %v", err)
	}
}
