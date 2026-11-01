package taskqueue

import (
	"sync"
	"testing"

	af "github.com/yosukeno/pogo/agentworkflow"
)

func awGraphFixture(t *testing.T, s *Store, key string) af.Definition {
	t.Helper()
	d, _, err := s.AWEdit("alice", "research", af.EditRequest{Action: "create", RequestID: key, Name: "并行确认", Steps: []af.Step{{ID: "fork", Kind: "parallel"}, {ID: "left", Kind: "confirm", Prompt: "左侧确认"}, {ID: "right", Kind: "confirm", Prompt: "右侧确认"}, {ID: "work", Kind: "agent", Task: "整理材料"}, {ID: "join", Kind: "join"}}, Graph: &af.Graph{Version: 1, Edges: []af.Edge{{Source: "__input", Target: "fork"}, {Source: "fork", Target: "left"}, {Source: "fork", Target: "right"}, {Source: "fork", Target: "work"}, {Source: "left", Target: "join"}, {Source: "right", Target: "join"}, {Source: "work", Target: "join"}, {Source: "join", Target: "__output"}}}})
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func awGraphAdmission(t *testing.T, s *Store, d af.Definition, id string) (af.Run, Run) {
	t.Helper()
	r := awRunFixture(d, id)
	r.Inputs = map[string]string{}
	r, q, _, err := s.AWAdmit(r, id)
	if err != nil {
		t.Fatal(err)
	}
	q, err = s.Claim(q.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.AWGraphSettle(r.OwnerID, r.AgentID, r.WorkflowID, r.ID, q.ID, q.Revision, r.Revision, af.Running, "")
	if err != nil {
		t.Fatal(err)
	}
	return r, q
}
func awGraphNode(t *testing.T, s *Store, r af.Run, q Run, id, status string) af.Run {
	t.Helper()
	i := awGraphStepIndex(r, id)
	before := r.Steps[i]
	after := before
	after.Status = status
	if after.StartedAt == 0 {
		after.StartedAt = now()
	}
	if status == af.Completed || status == af.Cancelled {
		after.FinishedAt = now()
	}
	if r.Definition.Steps[i].Kind == "agent" {
		after.SessionID = "session-" + id
	}
	next, err := s.AWGraphSaveStep(r.OwnerID, r.AgentID, r.WorkflowID, r.ID, q.ID, q.Revision, before, after)
	if err != nil {
		t.Fatalf("node %s -> %s: %v", id, status, err)
	}
	return next
}
func TestAgentWorkflowGraphConcurrentConfirmationsMergeWithWorkerReceipt(t *testing.T) {
	s, _ := testStore(t)
	d := awGraphFixture(t, s, "graph-create")
	r, q := awGraphAdmission(t, s, d, "parallel-run")
	r = awGraphNode(t, s, r, q, "fork", af.Completed)
	r = awGraphNode(t, s, r, q, "left", af.WaitingConfirmation)
	r = awGraphNode(t, s, r, q, "right", af.WaitingConfirmation)
	r = awGraphNode(t, s, r, q, "work", af.Running)
	before := r.Steps[awGraphStepIndex(r, "work")]
	after := before
	after.Status = af.Completed
	after.FinishedAt = now()
	after.Summary = "真实结果"
	errors := make(chan error, 3)
	var wg sync.WaitGroup
	for _, id := range []string{"left", "right"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _, e := s.AWConfirm("alice", "research", d.ID, r.ID, "confirm-"+id, "checked-"+id, id)
			errors <- e
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, e := s.AWGraphSaveStep(r.OwnerID, r.AgentID, d.ID, r.ID, q.ID, q.Revision, before, after)
		errors <- e
	}()
	wg.Wait()
	close(errors)
	for e := range errors {
		if e != nil {
			t.Fatal(e)
		}
	}
	latest, err := s.AWRun("alice", "research", d.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Steps[1].Feedback != "checked-left" || latest.Steps[2].Feedback != "checked-right" || latest.Steps[3].Summary != "真实结果" {
		t.Fatal("parallel update lost a receipt", latest.Steps)
	}
	if _, _, reused, e := s.AWConfirm("alice", "research", d.ID, r.ID, "confirm-left", "checked-left", "left"); e != nil || !reused {
		t.Fatal("confirmation replay", e, reused)
	}
	_, _, _, err = s.AWConfirm("alice", "research", d.ID, r.ID, "stale-key", "stale", "left")
	awExpect(t, err, ErrConflict)
	_, err = s.AWGraphSaveStep(r.OwnerID, r.AgentID, d.ID, r.ID, q.ID, q.Revision, before, after)
	awExpect(t, err, ErrConflict)
	latest = awGraphNode(t, s, latest, q, "join", af.Completed)
	latest, err = s.AWGraphSettle(latest.OwnerID, latest.AgentID, d.ID, latest.ID, q.ID, q.Revision, latest.Revision, af.Completed, "")
	if err != nil {
		t.Fatal(err)
	}
	if latest.Status != af.Completed || latest.CurrentStep != 5 {
		t.Fatal(latest)
	}
}
func TestAgentWorkflowGraphRestartFencesQueuedConfirmationWake(t *testing.T) {
	s, _ := testStore(t)
	d := awGraphFixture(t, s, "recover-create")
	r, q := awGraphAdmission(t, s, d, "unknown-run")
	r = awGraphNode(t, s, r, q, "fork", af.Completed)
	r = awGraphNode(t, s, r, q, "left", af.WaitingConfirmation)
	r = awGraphNode(t, s, r, q, "right", af.WaitingConfirmation)
	r = awGraphNode(t, s, r, q, "work", af.Running)
	if _, _, _, e := s.AWConfirm("alice", "research", d.ID, r.ID, "confirm-left", "checked", "left"); e != nil {
		t.Fatal(e)
	}
	if e := s.Recover(); e != nil {
		t.Fatal(e)
	}
	if e := s.AWRecover(); e != nil {
		t.Fatal(e)
	}
	latest, err := s.AWRun("alice", "research", d.ID, r.ID)
	if err != nil || latest.Status != af.Interrupted || latest.Steps[3].Status != af.Interrupted || latest.Steps[1].Status != af.Completed {
		t.Fatal("unknown work not fenced", latest, err)
	}
	segments, e := s.AWExecutions("alice", "research", d.ID, r.ID)
	if e != nil || len(segments) != 2 {
		t.Fatal(e, len(segments))
	}
	for _, seg := range segments {
		if seg.Status == Queued || seg.Status == Running {
			t.Fatal("queued wake survived unknown work", seg)
		}
	}
	_, err = s.AWGraphSettle(r.OwnerID, r.AgentID, d.ID, r.ID, q.ID, q.Revision, latest.Revision, af.Running, "")
	awExpect(t, err, ErrConflict)
}
func TestAgentWorkflowGraphWaitingSurvivesRestartAndCancellationBlocksConfirm(t *testing.T) {
	s, _ := testStore(t)
	d := awGraphFixture(t, s, "wait-create")
	r, q := awGraphAdmission(t, s, d, "waiting-run")
	r = awGraphNode(t, s, r, q, "fork", af.Completed)
	r = awGraphNode(t, s, r, q, "left", af.WaitingConfirmation)
	r = awGraphNode(t, s, r, q, "right", af.WaitingConfirmation)
	r = awGraphNode(t, s, r, q, "work", af.Running)
	r = awGraphNode(t, s, r, q, "work", af.Completed)
	r, err := s.AWGraphSettle(r.OwnerID, r.AgentID, d.ID, r.ID, q.ID, q.Revision, r.Revision, af.WaitingConfirmation, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Finish(q.ID, q.Revision, Outcome{Status: Completed, SessionID: r.ExecutionSessionID, EndReason: "waiting_confirmation"}); err != nil {
		t.Fatal(err)
	}
	if err = s.Recover(); err != nil {
		t.Fatal(err)
	}
	if err = s.AWRecover(); err != nil {
		t.Fatal(err)
	}
	same, err := s.AWRun("alice", "research", d.ID, r.ID)
	if err != nil || same.Status != af.WaitingConfirmation || same.Steps[3].Status != af.Completed {
		t.Fatal(same, err)
	}
	stopped, err := s.AWCancel("alice", "research", d.ID, r.ID)
	if err != nil || stopped.Status != af.Cancelled {
		t.Fatal(stopped, err)
	}
	_, _, _, err = s.AWConfirm("alice", "research", d.ID, r.ID, "late", "no", "right")
	awExpect(t, err, ErrConflict)
}
