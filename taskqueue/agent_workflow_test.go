package taskqueue

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	af "github.com/yosukeno/pogo/agentworkflow"
)

func awFixture(t *testing.T, s *Store, owner, agent string) af.Definition {
	t.Helper()
	d, reused, e := s.AWEdit(owner, agent, af.EditRequest{Action: "create", RequestID: "create", Name: "资料报告", Inputs: []af.Input{{Key: "topic", Required: true}}, Steps: []af.Step{{ID: "collect", Kind: "agent", Task: "整理 {{ inputs.topic }}", OutputFile: "notes.md"}, {ID: "review", Kind: "confirm", Prompt: "确认 {{ steps.collect.summary }}"}, {ID: "report", Kind: "agent", Task: "按 {{ steps.review.feedback }} 写报告", OutputFile: "report.md"}}})
	if e != nil || reused {
		t.Fatalf("create %v %v", reused, e)
	}
	return d
}
func awRunFixture(d af.Definition, id string) af.Run {
	return af.Run{ID: id, OwnerID: d.OwnerID, AgentID: d.AgentID, WorkflowID: d.ID, VersionID: d.DefinitionHash, Definition: d, AgentFingerprint: "capability-fingerprint", AgentSnapshot: json.RawMessage(`{"revision":1,"tools":["read","write"]}`), Inputs: map[string]string{"topic": "独立测试资料"}, Trial: true, SourceSessionID: "source-chat", Workspace: "agents/agent/workflow-runs/" + id}
}
func awWaiting(t *testing.T, s *Store, r af.Run, q Run) (af.Run, Run) {
	t.Helper()
	q, e := s.Claim(q.ID, 2)
	if e != nil {
		t.Fatal(e)
	}
	r.Status = af.Running
	r.ExecutionSessionID = "execution-session-" + r.ID
	r.Steps[0].Status = af.Running
	r.Steps[0].StartedAt = now()
	r, e = s.AWUpdate(r, r.Revision, q.ID, q.Revision)
	if e != nil {
		t.Fatal(e)
	}
	r.Steps[0].Status = af.Completed
	r.Steps[0].Summary = "资料已整理"
	r.Steps[0].Artifact = "notes.md"
	r.Steps[0].ArtifactSHA256 = strings.Repeat("a", 64)
	r.Steps[0].FinishedAt = now()
	r.CurrentStep = 1
	r.Steps[1].Status = af.WaitingConfirmation
	r.Steps[1].StartedAt = now()
	r.Status = af.WaitingConfirmation
	r, e = s.AWUpdate(r, r.Revision, q.ID, q.Revision)
	if e != nil {
		t.Fatal(e)
	}
	return r, q
}
func awExpect(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("expected %v, got %v", target, err)
	}
}

func TestAgentWorkflowDraftScopePreciseVersionAndAtomicEdits(t *testing.T) {
	s, _ := testStore(t)
	d := awFixture(t, s, "alice", "research")
	request := af.EditRequest{Action: "create", RequestID: "create", Name: d.Name, Inputs: d.Inputs, Steps: d.Steps}
	replay, reused, e := s.AWEdit("alice", "research", request)
	if e != nil || !reused || replay.ID != d.ID {
		t.Fatalf("create replay %+v %v %v", replay, reused, e)
	}
	request.Name = "different"
	_, _, e = s.AWEdit("alice", "research", request)
	awExpect(t, e, ErrConflict)
	_, e = s.AWGet("bob", "research", d.ID)
	awExpect(t, e, ErrNotFound)
	_, e = s.AWGet("alice", "writing", d.ID)
	awExpect(t, e, ErrNotFound)
	if got, e := s.AWList("bob", "research"); e != nil || len(got) != 0 {
		t.Fatalf("leaked %v %v", got, e)
	}
	_, _, e = s.AWEdit("alice", "research", af.EditRequest{Action: "patch", WorkflowID: d.ID, ExpectedRevision: 1, RequestID: "bad", Operations: []af.Operation{{Op: "update_step", StepID: "collect", Patch: json.RawMessage(`{"task":"changed"}`)}, {Op: "remove_step", StepID: "review"}}})
	awExpect(t, e, af.ErrInvalid)
	same, e := s.AWGet("alice", "research", d.ID)
	if e != nil || same.DefinitionHash != d.DefinitionHash {
		t.Fatal("partial edit committed")
	}
	d2, reused, e := s.AWEdit("alice", "research", af.EditRequest{Action: "patch", WorkflowID: d.ID, ExpectedRevision: 1, RequestID: "patch", Operations: []af.Operation{{Op: "update_step", StepID: "collect", Patch: json.RawMessage(`{"task":"检查来源并整理 {{ inputs.topic }}"}`)}}})
	if e != nil || reused || d2.Revision != 2 {
		t.Fatalf("patch %v %v", reused, e)
	}
	_, e = s.AWEnable("alice", "research", d.ID, 1, d.DefinitionHash, true)
	awExpect(t, e, ErrConflict)
	enabled, e := s.AWEnable("alice", "research", d.ID, 2, d2.DefinitionHash, true)
	if e != nil || enabled.EnabledVersion != d2.DefinitionHash {
		t.Fatal(e)
	}
	again, e := s.AWEnable("alice", "research", d.ID, 2, d2.DefinitionHash, true)
	if e != nil || again.EnabledVersion != enabled.EnabledVersion {
		t.Fatal("enable not idempotent", e)
	}
	name := "新名称"
	newDraft, _, e := s.AWEdit("alice", "research", af.EditRequest{Action: "patch", WorkflowID: d.ID, ExpectedRevision: 2, RequestID: "new-draft", Operations: []af.Operation{{Op: "set_metadata", Name: &name}}})
	if e != nil || newDraft.EnabledVersion != enabled.EnabledVersion {
		t.Fatal(e)
	}
	old, e := s.AWVersion("alice", "research", d.ID, enabled.EnabledVersion)
	if e != nil || old.Name != d2.Name || old.Revision != 2 {
		t.Fatal("version changed", e)
	}
}

func TestAgentWorkflowAdmissionAtomicAndConcurrentReplay(t *testing.T) {
	s, _ := testStore(t)
	d := awFixture(t, s, "alice", "research")
	var wg sync.WaitGroup
	var fresh atomic.Int32
	errs := make(chan error, 6)
	ids := make(chan string, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, _, reused, e := s.AWAdmit(awRunFixture(d, fmt.Sprintf("run-%d", i)), "one-intent")
			if e != nil {
				errs <- e
				return
			}
			if !reused {
				fresh.Add(1)
			}
			ids <- r.ID
		}(i)
	}
	wg.Wait()
	close(errs)
	close(ids)
	for e := range errs {
		t.Error(e)
	}
	if fresh.Load() != 1 {
		t.Fatalf("minted %d Runs", fresh.Load())
	}
	metadataOnly := awRunFixture(d, "metadata-only-replay")
	metadataOnly.AgentSnapshot = json.RawMessage(`{"revision":1,"tools":["read","write"],"conversation_count":10}`)
	_, _, reused, e := s.AWAdmit(metadataOnly, "one-intent")
	if e != nil || !reused {
		t.Fatal("presentation-only snapshot change broke replay", e)
	}
	id := ""
	for rid := range ids {
		if id != "" && id != rid {
			t.Fatal("replay changed identity")
		}
		id = rid
	}
	runs, e := s.AWRuns("alice", "research", d.ID)
	if e != nil || len(runs) != 1 {
		t.Fatalf("%v %v", runs, e)
	}
	_, _, _, e = s.AWAdmit(func() af.Run { r := awRunFixture(d, "another"); r.Inputs["topic"] = "different"; return r }(), "one-intent")
	awExpect(t, e, ErrConflict)
	if _, e = s.db.Exec(`CREATE TRIGGER aw_admit_failure BEFORE INSERT ON taskqueue_runs WHEN NEW.trigger_id='start' BEGIN SELECT RAISE(ABORT,'injected failure'); END`); e != nil {
		t.Fatal(e)
	}
	_, _, _, e = s.AWAdmit(awRunFixture(d, "rollback"), "fail")
	if e == nil {
		t.Fatal("expected failure")
	}
	_, e = s.AWRun("alice", "research", d.ID, "rollback")
	awExpect(t, e, ErrNotFound)
	if _, e = s.db.Exec(`DROP TRIGGER aw_admit_failure`); e != nil {
		t.Fatal(e)
	}
	_, _, reused, e = s.AWAdmit(awRunFixture(d, "after-rollback"), "fail")
	if e != nil || reused {
		t.Fatal("failed transaction kept intent", e)
	}
}

func TestAgentWorkflowConfirmationSameRunAndPreviousSlotRace(t *testing.T) {
	s, _ := testStore(t)
	d := awFixture(t, s, "alice", "research")
	r, q, _, e := s.AWAdmit(awRunFixture(d, "run-confirm"), "start")
	if e != nil {
		t.Fatal(e)
	}
	r, q = awWaiting(t, s, r, q)
	_, _, _, e = s.AWConfirm("bob", "research", d.ID, r.ID, "confirm", "continue")
	awExpect(t, e, ErrNotFound)
	_, _, _, e = s.AWConfirm("alice", "research", d.ID, r.ID, "wrong-step", "增加出处", "collect")
	awExpect(t, e, ErrConflict)
	continued, next, reused, e := s.AWConfirm("alice", "research", d.ID, r.ID, "confirm", "增加出处", "review")
	if e != nil || reused || continued.ID != r.ID || continued.CurrentStep != 2 || continued.Status != af.Queued || continued.Steps[1].Feedback != "增加出处" {
		t.Fatalf("confirm %+v %v %v", continued, reused, e)
	}
	_, e = s.Claim(next.ID, 2)
	awExpect(t, e, ErrBusy)
	old, oldQ, reused, e := s.AWConfirm("alice", "research", d.ID, r.ID, "confirm", "增加出处", "review")
	if e != nil || !reused || old.ID != r.ID || oldQ.ID != next.ID {
		t.Fatal("confirmation replay", e)
	}
	_, _, _, e = s.AWConfirm("alice", "research", d.ID, r.ID, "other-command", "增加出处")
	awExpect(t, e, ErrConflict)
	_, _, _, e = s.AWConfirm("alice", "research", d.ID, r.ID, "confirm", "changed")
	awExpect(t, e, ErrConflict)
	if _, e = s.Finish(q.ID, q.Revision, Outcome{Status: Completed, SessionID: r.ExecutionSessionID}); e != nil {
		t.Fatal(e)
	}
	next, e = s.Claim(next.ID, 2)
	if e != nil {
		t.Fatal(e)
	}
	continued.Status = af.Running
	continued.Steps[2].Status = af.Running
	continued, e = s.AWUpdate(continued, continued.Revision, next.ID, next.Revision)
	if e != nil {
		t.Fatal(e)
	}
	changed := continued
	changed.Steps = append([]af.StepRun(nil), continued.Steps...)
	changed.Steps[0].Summary = "rewritten"
	_, e = s.AWUpdate(changed, changed.Revision, next.ID, next.Revision)
	awExpect(t, e, ErrConflict)
	invalidArtifact := continued
	invalidArtifact.Steps = append([]af.StepRun(nil), continued.Steps...)
	invalidArtifact.Steps[2].Artifact = "report.md"
	invalidArtifact.Steps[2].ArtifactSHA256 = strings.Repeat("z", 64)
	_, e = s.AWUpdate(invalidArtifact, invalidArtifact.Revision, next.ID, next.Revision)
	awExpect(t, e, ErrConflict)
	continued.Steps[2].Status = af.Completed
	continued.Steps[2].Artifact = "report.md"
	continued.Steps[2].ArtifactSHA256 = strings.Repeat("b", 64)
	continued.CurrentStep = 3
	continued.Status = af.Completed
	continued, e = s.AWUpdate(continued, continued.Revision, next.ID, next.Revision)
	if e != nil {
		t.Fatal(e)
	}
	executions, e := s.AWExecutions("alice", "research", d.ID, r.ID)
	if e != nil || len(executions) != 2 {
		t.Fatalf("%d segments %v", len(executions), e)
	}
}

func TestAgentWorkflowCancellationRetryAndRestartFence(t *testing.T) {
	s, _ := testStore(t)
	d := awFixture(t, s, "alice", "research")
	r, q, _, e := s.AWAdmit(awRunFixture(d, "cancel"), "cancel")
	if e != nil {
		t.Fatal(e)
	}
	q, e = s.Claim(q.ID, 2)
	if e != nil {
		t.Fatal(e)
	}
	r.Status = af.Running
	r.Steps[0].Status = af.Running
	r, e = s.AWUpdate(r, r.Revision, q.ID, q.Revision)
	if e != nil {
		t.Fatal(e)
	}
	cancelled, e := s.AWCancel("alice", "research", d.ID, r.ID)
	if e != nil || cancelled.Status != af.Running || !cancelled.CancelRequested || cancelled.Revision != r.Revision {
		t.Fatal("premature cancellation", e)
	}
	r.Status = af.Cancelled
	r, e = s.AWUpdate(r, r.Revision, q.ID, q.Revision)
	if e != nil || !r.CancelRequested {
		t.Fatal("worker could not settle", e)
	}
	name := "新版草稿"
	_, _, e = s.AWEdit("alice", "research", af.EditRequest{Action: "patch", WorkflowID: d.ID, ExpectedRevision: 1, RequestID: "new", Operations: []af.Operation{{Op: "set_metadata", Name: &name}}})
	if e != nil {
		t.Fatal(e)
	}
	retry := awRunFixture(d, "retry")
	retry.RetryOf = r.ID
	child, _, reused, e := s.AWAdmit(retry, "retry")
	if e != nil || reused || child.VersionID != r.VersionID || child.ID == r.ID {
		t.Fatal("retry did not freeze old version", e)
	}
	r2, q2, _, e := s.AWAdmit(awRunFixture(func() af.Definition { n, _ := s.AWGet("alice", "research", d.ID); return n }(), "unknown"), "unknown")
	if e != nil {
		t.Fatal(e)
	}
	q2, e = s.Claim(q2.ID, 3)
	if e != nil {
		t.Fatal(e)
	}
	r2.Status = af.Running
	r2.Steps[0].Status = af.Running
	r2, e = s.AWUpdate(r2, r2.Revision, q2.ID, q2.Revision)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Recover(); e != nil {
		t.Fatal(e)
	}
	if e = s.AWRecover(); e != nil {
		t.Fatal(e)
	}
	unknown, e := s.AWRun("alice", "research", d.ID, r2.ID)
	if e != nil || unknown.Status != af.Interrupted {
		t.Fatal("unknown execution requeued", e)
	}
	r2.Status = af.Completed
	r2.CurrentStep = 3
	_, e = s.AWUpdate(r2, r2.Revision, q2.ID, q2.Revision)
	awExpect(t, e, ErrConflict)
}

func TestAgentWorkflowWaitSurvivesReopen(t *testing.T) {
	s, path := testStore(t)
	d := awFixture(t, s, "alice", "research")
	r, q, _, e := s.AWAdmit(awRunFixture(d, "waiting-restart"), "start")
	if e != nil {
		t.Fatal(e)
	}
	r, q = awWaiting(t, s, r, q)
	if _, e = s.Finish(q.ID, q.Revision, Outcome{Status: Completed}); e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.Recover(); e != nil {
		t.Fatal(e)
	}
	if e = s.AWRecover(); e != nil {
		t.Fatal(e)
	}
	loaded, e := s.AWRun("alice", "research", d.ID, r.ID)
	if e != nil || loaded.Status != af.WaitingConfirmation || loaded.Steps[0].Summary != "资料已整理" {
		t.Fatal("wait lost", e)
	}
	continued, _, _, e := s.AWConfirm("alice", "research", d.ID, r.ID, "after-restart", "继续")
	if e != nil || continued.ID != r.ID || continued.CurrentStep != 2 {
		t.Fatal("same Run resume lost", e)
	}
}

func TestAgentWorkflowCancelPersistedWaitBeforeSegmentFinish(t *testing.T) {
	s, _ := testStore(t)
	d := awFixture(t, s, "alice", "research")
	r, q, _, e := s.AWAdmit(awRunFixture(d, "cancel-wait-race"), "start")
	if e != nil {
		t.Fatal(e)
	}
	r, q = awWaiting(t, s, r, q)
	stopped, e := s.AWCancel("alice", "research", d.ID, r.ID)
	if e != nil || stopped.Status != af.Cancelled || !stopped.CancelRequested {
		t.Fatalf("wait stuck cancelling: %+v %v", stopped, e)
	}
	if _, e = s.Finish(q.ID, q.Revision, Outcome{Status: Completed}); e != nil {
		t.Fatal(e)
	}
	_, _, _, e = s.AWConfirm("alice", "research", d.ID, r.ID, "late", "continue")
	awExpect(t, e, ErrConflict)
}

func TestAgentWorkflowDelayedConfirmationCannotApproveLaterWait(t *testing.T) {
	s, _ := testStore(t)
	d := awFixture(t, s, "alice", "research")
	d, _, e := s.AWEdit("alice", "research", af.EditRequest{Action: "patch", WorkflowID: d.ID, ExpectedRevision: d.Revision, RequestID: "second-wait", Operations: []af.Operation{{Op: "add_step", Step: &af.Step{ID: "final_review", Kind: "confirm", Prompt: "确认最终报告 {{ steps.report.summary }}"}}}})
	if e != nil {
		t.Fatal(e)
	}
	r, q, _, e := s.AWAdmit(awRunFixture(d, "two-waits"), "start")
	if e != nil {
		t.Fatal(e)
	}
	r, q = awWaiting(t, s, r, q)
	r, next, _, e := s.AWConfirm("alice", "research", d.ID, r.ID, "first-decision", "继续", "review")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Finish(q.ID, q.Revision, Outcome{Status: Completed}); e != nil {
		t.Fatal(e)
	}
	next, e = s.Claim(next.ID, 2)
	if e != nil {
		t.Fatal(e)
	}
	r.Steps[2].Status = af.Completed
	r.Steps[2].Summary = "报告完成"
	r.CurrentStep = 3
	r.Steps[3].Status = af.WaitingConfirmation
	r.Status = af.WaitingConfirmation
	r, e = s.AWUpdate(r, r.Revision, next.ID, next.Revision)
	if e != nil {
		t.Fatal(e)
	}
	_, _, _, e = s.AWConfirm("alice", "research", d.ID, r.ID, "delayed-old-page", "继续", "review")
	awExpect(t, e, ErrConflict)
	replayed, original, reused, e := s.AWConfirm("alice", "research", d.ID, r.ID, "first-decision", "继续", "review")
	if e != nil || !reused || replayed.CurrentStep != 3 || original.ID != next.ID {
		t.Fatal("old intent did not safely replay", e)
	}
	_, _, _, e = s.AWConfirm("alice", "research", d.ID, r.ID, "first-decision", "继续", "final_review")
	awExpect(t, e, ErrConflict)
	continued, _, reused, e := s.AWConfirm("alice", "research", d.ID, r.ID, "last-decision", "继续", "final_review")
	if e != nil || reused || continued.CurrentStep != 4 {
		t.Fatal("correct current wait rejected", e)
	}
}
