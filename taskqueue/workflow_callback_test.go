package taskqueue

import (
	"bytes"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func callbackMetaFixture(id string) WorkflowRunMeta {
	meta := workflowMetaFixture(id, "alice")
	meta.PublicationID = "pub_1"
	meta.BusinessID = "ORDER-A"
	meta.ExternalSubject = "employee-a"
	meta.Inputs = map[string]string{"private": "must not leave"}
	meta.Callback = &WorkflowCallbackTarget{PublicationID: "pub_1", URL: "https://business.example/callback", SigningSecret: "test-signing-secret"}
	return meta
}
func admitCallbackFixture(t *testing.T, s *Store, id string) WorkflowCallbackDelivery {
	t.Helper()
	_, _, _, err := s.AdmitWorkflow(callbackMetaFixture(id), id, workflowRequestFixture())
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListWorkflowCallbackDeliveries("pub_1", 100)
	if err != nil || len(rows) != 1 {
		t.Fatalf("deliveries=%+v err=%v", rows, err)
	}
	return rows[0]
}
func TestWorkflowCallbackAtomicAdmissionAndStableSequence(t *testing.T) {
	s, _ := testStore(t)
	first := admitCallbackFixture(t, s, "run_callback")
	if first.Sequence != 1 || first.EventType != "run.queued" || first.Status != "pending" {
		t.Fatalf("first=%+v", first)
	}
	if bytes.Contains(first.Payload, []byte("private")) || bytes.Contains(first.Payload, []byte("must not leave")) {
		t.Fatal("business inputs in callback payload")
	}
	repeated := callbackMetaFixture("different-id")
	repeated.CreatedAt = 999
	if meta, _, created, err := s.AdmitWorkflow(repeated, "run_callback", workflowRequestFixture()); err != nil || created || meta.ID != "run_callback" {
		t.Fatalf("repeat=%+v %v %v", meta, created, err)
	}
	if err := s.RecordWorkflowCallbackState("run_callback", "queued", 0, 999); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		status   string
		revision int64
	}{{"working", 1}, {"waiting_human", 3}, {"waiting_human", 4}, {"working", 5}, {"completed", 8}, {"queued", 0}} {
		if err := s.RecordWorkflowCallbackState("run_callback", step.status, step.revision, 1000); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.ListWorkflowCallbackDeliveries("pub_1", 100)
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	for _, d := range rows {
		var event WorkflowCallbackEvent
		if err := json.Unmarshal(d.Payload, &event); err != nil {
			t.Fatal(err)
		}
		if event.Sequence != d.Sequence || event.ID != d.ID || event.BusinessID != "ORDER-A" {
			t.Fatalf("event=%+v delivery=%+v", event, d)
		}
	}
	if got, err := s.WorkflowCallbackDelivery(first.ID); err != nil || !bytes.Equal(got.Payload, first.Payload) {
		t.Fatal("same event payload changed")
	}
	// Queue/outbox failure must roll back the whole new logical admission.
	if _, err := s.db.Exec(`CREATE TRIGGER callback_fail BEFORE INSERT ON workflow_callback_outbox WHEN NEW.run_id='run_bad' BEGIN SELECT RAISE(ABORT,'callback unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.AdmitWorkflow(callbackMetaFixture("run_bad"), "bad", workflowRequestFixture()); err == nil {
		t.Fatal("admission acknowledged without outbox")
	}
	if _, err := s.GetWorkflowRun("run_bad"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("admission not rolled back: %v", err)
	}
	// Old metadata never acquires a subscription retroactively.
	if _, _, _, err := s.AdmitWorkflow(workflowMetaFixture("legacy", "alice"), "legacy", workflowRequestFixture()); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordWorkflowCallbackState("legacy", "completed", 5, 1000); err != nil {
		t.Fatal(err)
	}
	subscriptions, err := s.ListWorkflowCallbackSubscriptions()
	if err != nil || len(subscriptions) != 1 {
		t.Fatalf("legacy subscribed: %+v %v", subscriptions, err)
	}
}
func TestWorkflowCallbackRetriesLeaseRecoveryOrderingAndManualTarget(t *testing.T) {
	s, path := testStore(t)
	first := admitCallbackFixture(t, s, "run_replay")
	if err := s.RecordWorkflowCallbackState("run_replay", "cancelled", 0, 100); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.ListWorkflowCallbackDeliveries("pub_1", 100)
	if len(rows) != 2 {
		t.Fatalf("rows=%+v", rows)
	}
	var second WorkflowCallbackDelivery
	for _, d := range rows {
		if d.Sequence == 2 {
			second = d
		}
	}
	if second.EventType != "run.cancelled" || second.StateRevision != 0 {
		t.Fatalf("virtual cancel=%+v", second)
	}
	if _, err := s.ClaimWorkflowCallback(second.ID, "out-of-order", 100, 300); !errors.Is(err, ErrConflict) {
		t.Fatalf("claimed out-of-order event: %v", err)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.ClaimWorkflowCallback(first.ID, "same-claim", 100, 300); err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("duplicate workers=%d", successes.Load())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	s = reopened
	due, err := s.DueWorkflowCallbacks(299, 20)
	if err != nil || len(due) != 0 {
		t.Fatalf("live lease reused %+v %v", due, err)
	}
	due, err = s.DueWorkflowCallbacks(300, 20)
	if err != nil || len(due) != 1 || due[0].ID != first.ID {
		t.Fatalf("lease not recovered %+v %v", due, err)
	}
	replay, err := s.ClaimWorkflowCallback(first.ID, "new-claim", 300, 500)
	if err != nil || replay.Attempts != 2 || !bytes.Equal(replay.Payload, first.Payload) {
		t.Fatalf("replay=%+v %v", replay, err)
	}
	if err = s.FinishWorkflowCallback(first.ID, "same-claim", 204, "", 300, 300, 2); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale worker ack accepted: %v", err)
	}
	if err = s.FinishWorkflowCallback(first.ID, "new-claim", 503, "HTTP 503", 301, 600, 2); err != nil {
		t.Fatal(err)
	}
	due, _ = s.DueWorkflowCallbacks(599, 20)
	if len(due) != 0 {
		t.Fatal("backoff ignored")
	}
	if _, err = s.ClaimWorkflowCallback(first.ID, "last", 600, 800); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishWorkflowCallback(first.ID, "last", 0, "connection failed", 601, 900, 2); err != nil {
		t.Fatal(err)
	}
	failed, _ := s.WorkflowCallbackDelivery(first.ID)
	if failed.Status != "failed" || failed.Attempts != 3 {
		t.Fatalf("failed=%+v", failed)
	}
	due, _ = s.DueWorkflowCallbacks(1000, 20)
	if len(due) != 0 {
		t.Fatal("failed earlier event did not block later state")
	}
	target := WorkflowCallbackTarget{PublicationID: "pub_1", URL: "https://new.example/callback", SigningSecret: "rotated-secret"}
	if err = s.RetryWorkflowCallback(first.ID, "pub_1", target, false, 1000); !errors.Is(err, ErrConflict) {
		t.Fatalf("retarget without confirmation: %v", err)
	}
	if err = s.RetryWorkflowCallback(first.ID, "other_pub", target, true, 1000); !errors.Is(err, ErrNotFound) {
		t.Fatalf("publication boundary: %v", err)
	}
	if err = s.RetryWorkflowCallback(first.ID, "pub_1", target, true, 1000); err != nil {
		t.Fatal(err)
	}
	replay, err = s.ClaimWorkflowCallback(first.ID, "manual", 1000, 1200)
	if err != nil || replay.TargetURL != target.URL || !bytes.Equal(replay.Payload, first.Payload) || replay.Failures != 0 {
		t.Fatalf("manual replay=%+v %v", replay, err)
	}
	if err = s.FinishWorkflowCallback(first.ID, "manual", 204, "", 1001, 1001, 2); err != nil {
		t.Fatal(err)
	}
	due, err = s.DueWorkflowCallbacks(1001, 20)
	if err != nil || len(due) != 1 || due[0].ID != second.ID || due[0].Sequence != 2 {
		t.Fatalf("later event did not release=%+v %v", due, err)
	}
}
