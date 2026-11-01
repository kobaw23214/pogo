package taskqueue

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func workflowMetaFixture(id, owner string) WorkflowRunMeta {
	return WorkflowRunMeta{ID: id, WorkflowID: "wf_1", OwnerID: owner, InitiatorID: owner, ExecutionPrincipalID: owner,
		Source: "manual", SourceID: "wf_1", VersionID: "version_1", Definition: json.RawMessage(`{"id":"wf_1","nodes":[]}`),
		Owner: json.RawMessage(`{"subject_id":"alice"}`), CreatedAt: 100}
}

func workflowRequestFixture() Request {
	return Request{Source: "workflow", SourceID: "wf_1", Trigger: "manual", TriggerID: "start", DefinitionHash: "version_1", Title: "Workflow"}
}

func TestWorkflowAdmissionAtomicIdempotentAndScoped(t *testing.T) {
	s, _ := testStore(t)
	meta, segment, created, err := s.AdmitWorkflow(workflowMetaFixture("run_a", "alice"), "request_1", workflowRequestFixture())
	if err != nil || !created || meta.SpaceID != DefaultSpaceID || segment.WorkflowRunID != meta.ID || segment.TaskID != "workflow-run:run_a" || segment.OwnerID != "alice" {
		t.Fatalf("admit: %+v %+v %v %v", meta, segment, created, err)
	}
	repeated := workflowMetaFixture("different_random_id", "alice")
	repeated.CreatedAt = 999
	req := workflowRequestFixture()
	req.DueAt = 999
	old, oldSegment, created, err := s.AdmitWorkflow(repeated, "request_1", req)
	if err != nil || created || old.ID != "run_a" || old.CreatedAt != 100 || oldSegment.ID != segment.ID {
		t.Fatalf("idempotent intent changed: %+v %+v %v %v", old, oldSegment, created, err)
	}
	repeated.Definition = json.RawMessage(`{"id":"wf_1","nodes":[{"id":"changed"}]}`)
	_, _, _, err = s.AdmitWorkflow(repeated, "request_1", req)
	expectError(t, err, ErrConflict)
	other, _, created, err := s.AdmitWorkflow(workflowMetaFixture("run_b", "bob"), "request_1", workflowRequestFixture())
	if err != nil || !created || other.OwnerID != "bob" {
		t.Fatalf("owner intent scopes mixed: %+v %v %v", other, created, err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER workflow_admit_failure BEFORE INSERT ON taskqueue_runs WHEN NEW.title='reject' BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	req = workflowRequestFixture()
	req.Title = "reject"
	_, _, _, err = s.AdmitWorkflow(workflowMetaFixture("run_rollback", "alice"), "rejected", req)
	if err == nil {
		t.Fatal("expected injected enqueue failure")
	}
	_, err = s.GetWorkflowRun("run_rollback")
	expectError(t, err, ErrNotFound)
	if _, err := s.db.Exec("DROP TRIGGER workflow_admit_failure"); err != nil {
		t.Fatal(err)
	}
	_, _, created, err = s.AdmitWorkflow(workflowMetaFixture("run_after_rollback", "alice"), "rejected", workflowRequestFixture())
	if err != nil || !created {
		t.Fatalf("failed transaction kept intent: %v %v", created, err)
	}
}

func TestWorkflowConcurrentIntentAndHumanDecision(t *testing.T) {
	s, _ := testStore(t)
	var wg sync.WaitGroup
	var createdCount atomic.Int32
	errorsFound := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			meta, _, created, err := s.AdmitWorkflow(workflowMetaFixture(fmt.Sprintf("run_%d", i), "alice"), "same", workflowRequestFixture())
			if err != nil {
				errorsFound <- err
				return
			}
			if created {
				createdCount.Add(1)
			}
			if meta.OwnerID != "alice" {
				errorsFound <- fmt.Errorf("bad owner")
			}
		}(i)
	}
	wg.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
	if createdCount.Load() != 1 {
		t.Fatalf("created %d logical runs", createdCount.Load())
	}
	metas, err := s.ListWorkflowRuns(WorkflowRunFilter{OwnerID: "alice"})
	if err != nil || len(metas) != 1 {
		t.Fatalf("catalog: %+v %v", metas, err)
	}
	meta := metas[0]
	var accepted atomic.Int32
	var conflicts atomic.Int32
	for _, action := range []string{"approve", "reject"} {
		wg.Add(1)
		go func(action string) {
			defer wg.Done()
			payload, _ := json.Marshal(map[string]string{"node": "review", "action": action})
			_, _, _, err := s.WorkflowCommand(meta.ID, "human:review:wait-generation", payload, workflowRequestFixture())
			switch {
			case err == nil:
				accepted.Add(1)
			case errors.Is(err, ErrConflict):
				conflicts.Add(1)
			default:
				t.Error(err)
			}
		}(action)
	}
	wg.Wait()
	if accepted.Load() != 1 || conflicts.Load() != 1 {
		t.Fatalf("competing decisions: accepted=%d conflict=%d", accepted.Load(), conflicts.Load())
	}
	executions, err := s.ListWorkflowExecutions(meta.ID)
	if err != nil || len(executions) != 2 {
		t.Fatalf("segments: %+v %v", executions, err)
	}
	for _, execution := range executions {
		if execution.WorkflowRunID != meta.ID {
			t.Fatalf("lost logical ID: %+v", execution)
		}
	}
}

func TestWorkflowCommandReplayCancellationAndSegments(t *testing.T) {
	s, _ := testStore(t)
	meta, first, _, err := s.AdmitWorkflow(workflowMetaFixture("run_a", "alice"), "start", workflowRequestFixture())
	if err != nil {
		t.Fatal(err)
	}
	active, err := s.Claim(first.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"node":"review","action":"approve"}`)
	_, command, created, err := s.WorkflowCommand(meta.ID, "human:review:1", payload, workflowRequestFixture())
	if err != nil || !created {
		t.Fatalf("decision: %v %v", created, err)
	}
	_, again, created, err := s.WorkflowCommand(meta.ID, "human:review:1", json.RawMessage(" {\n \"node\":\"review\",\"action\":\"approve\" } "), workflowRequestFixture())
	if err != nil || created || again.ID != command.ID {
		t.Fatalf("decision replay: %+v %v %v", again, created, err)
	}
	_, _, _, err = s.WorkflowCommand(meta.ID, "human:review:1", json.RawMessage(`{"node":"review","action":"reject"}`), workflowRequestFixture())
	expectError(t, err, ErrConflict)
	cancelled, err := s.CancelWorkflow(meta.ID)
	if err != nil || !cancelled.CancelRequested {
		t.Fatalf("cancel intent: %+v %v", cancelled, err)
	}
	queued, err := s.Get(command.ID)
	if err != nil || queued.Status != Cancelled || !queued.CancelRequested {
		t.Fatalf("queued segment survived: %+v %v", queued, err)
	}
	running, err := s.Get(active.ID)
	if err != nil || running.Status != Running || running.Revision != active.Revision || !running.CancelRequested {
		t.Fatalf("cancel falsified receipt: %+v %v", running, err)
	}
	_, err = s.Finish(active.ID, active.Revision, Outcome{Status: Completed})
	if err != nil {
		t.Fatalf("actual result blocked by cancellation: %v", err)
	}
	_, _, _, err = s.WorkflowCommand(meta.ID, "human:review:2", payload, workflowRequestFixture())
	expectError(t, err, ErrConflict)
	_, _, err = s.EnqueueWorkflow(meta.ID, workflowRequestFixture())
	expectError(t, err, ErrConflict)
	_, again, created, err = s.WorkflowCommand(meta.ID, "human:review:1", payload, workflowRequestFixture())
	if err != nil || created || again.ID != command.ID {
		t.Fatalf("original decision receipt lost after cancellation: %v %v", created, err)
	}
}

func TestWorkflowBindLegacyExecutionPreservesEnvelope(t *testing.T) {
	s, path := testStore(t)
	req := request("legacy-schedule")
	req.AgentID = ""
	req.WorkflowID = "wf_1"
	req.Source = "schedule"
	req.OwnerID = "single-user"
	segment := admit(t, s, req)
	before, err := get(s.db, segment.ID)
	if err != nil {
		t.Fatal(err)
	}
	meta := workflowMetaFixture("run_bound", "single-user")
	meta.Source = "schedule"
	meta.Owner = nil
	bound, created, err := s.BindWorkflowExecution(meta, "schedule:"+segment.ID, segment.ID)
	if err != nil || !created {
		t.Fatalf("bind: %+v %v %v", bound, created, err)
	}
	after, err := get(s.db, segment.ID)
	if err != nil || before.digest != after.digest || after.WorkflowRunID != "" || after.TaskID != req.TaskID || !bytes.Equal(before.Payload, after.Payload) {
		t.Fatalf("binding rewrote old envelope: %+v %v", after, err)
	}
	meta.ID = "new_random_id"
	bound, created, err = s.BindWorkflowExecution(meta, "schedule:"+segment.ID, segment.ID)
	if err != nil || created || bound.ID != "run_bound" {
		t.Fatalf("binding replay: %+v %v %v", bound, created, err)
	}
	meta.ID = "illegal_rebind"
	meta.OwnerID = "bob"
	_, _, err = s.BindWorkflowExecution(meta, "other", segment.ID)
	expectError(t, err, ErrConflict)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	bound, err = reopened.WorkflowRunForExecution(segment.ID)
	if err != nil || bound.ID != "run_bound" {
		t.Fatalf("binding lost on restart: %+v %v", bound, err)
	}
	if err := reopened.ImportWorkflowRun(workflowMetaFixture("run_history", "alice")); err != nil {
		t.Fatal(err)
	}
	history := workflowMetaFixture("run_history", "bob")
	expectError(t, reopened.ImportWorkflowRun(history), ErrConflict)
	old, err := reopened.GetWorkflowRun("run_history")
	if err != nil || old.OwnerID != "alice" {
		t.Fatalf("history reassigned: %+v %v", old, err)
	}
}

func TestWorkflowAccessOverridesNodeScopesAndDurability(t *testing.T) {
	s, path := testStore(t)
	if err := s.ImportWorkflowRun(workflowMetaFixture("run_a", "alice")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccessRole("bob", "builder"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccessRole("denied", "disabled"); err != nil {
		t.Fatal(err)
	}
	grant, err := s.WorkflowGrant("wf_1", "bob")
	if err != nil || grant != nil {
		t.Fatalf("missing override: %+v %v", grant, err)
	}
	if err := s.SetWorkflowGrant("wf_1", "bob", []string{}); err != nil {
		t.Fatal(err)
	}
	grant, err = s.WorkflowGrant("wf_1", "bob")
	if err != nil || grant == nil || len(grant) != 0 {
		t.Fatalf("empty override lost: %+v %v", grant, err)
	}
	if err := s.SetRunGrant("run_a", "bob", "review", []string{"approval.decide", "run.read", "run.read"}); err != nil {
		t.Fatal(err)
	}
	other, err := s.RunGrant("run_a", "bob", "other")
	if err != nil || other != nil {
		t.Fatalf("node grant leaked: %+v %v", other, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	role, err := reopened.AccessRole("bob")
	if err != nil || role != "builder" {
		t.Fatalf("role lost: %s %v", role, err)
	}
	grants, err := reopened.ListRunGrants("run_a")
	if err != nil || len(grants) != 1 || grants[0].NodeID != "review" || len(grants[0].Actions) != 2 {
		t.Fatalf("grant list: %+v %v", grants, err)
	}
	if err := reopened.SetRunGrant("run_a", "bob", "review", nil); err != nil {
		t.Fatal(err)
	}
	grant, err = reopened.RunGrant("run_a", "bob", "review")
	if err != nil || grant != nil {
		t.Fatalf("revocation failed: %+v %v", grant, err)
	}
	if err := reopened.DeleteWorkflowGrant("wf_1", "bob"); err != nil {
		t.Fatal(err)
	}
	grant, err = reopened.WorkflowGrant("wf_1", "bob")
	if err != nil || grant != nil {
		t.Fatalf("default restoration failed: %+v %v", grant, err)
	}
}
