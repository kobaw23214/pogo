package web

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/yosukeno/pogo/schedule"
	"github.com/yosukeno/pogo/taskqueue"
	"github.com/yosukeno/pogo/workflow"
)

func workflowLineageFixture(t *testing.T, wrapperStatus string) (*Manager, *taskqueue.Store, schedule.Job, taskqueue.Run, taskqueue.WorkflowRunMeta) {
	t.Helper()
	scheduleEnv(t)
	m := sweepManager(t, 0)
	s, err := NewServer(m, ServerOptions{Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	db := taskCenterTestQueue(t, m)
	job := taskCenterAddSchedule(t, s, "lineage-schedule", "", "wf_lineage", nil)
	row := taskCenterEnqueue(t, db, job, "original")
	row, err = db.Claim(row.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	rec := workflow.RunRecord{ID: "run_lineage_original", WorkflowID: job.WorkflowID,
		Definition: taskCenterWorkflowDefinition(job.WorkflowID), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Status: workflow.RunFailed, Error: "process restarted", ExecutionID: row.ID,
		Nodes: map[string]workflow.NodeState{"review": {Status: workflow.NodeFailed, Error: "process restarted"}}}
	meta, _, err := db.BindWorkflowExecution(workflowMeta(rec, "schedule", job.ID), "schedule:"+row.ID, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeWFRecord(m.wfruns.recordPath(rec.ID), rec); err != nil {
		t.Fatal(err)
	}
	row, err = db.Finish(row.ID, row.Revision, taskqueue.Outcome{Status: wrapperStatus, SessionID: rec.ID, EndReason: "controller_restart"})
	if err != nil {
		t.Fatal(err)
	}
	return m, db, job, row, meta
}

func workflowLineageRetry(t *testing.T, db *taskqueue.Store, parent taskqueue.WorkflowRunMeta, id string) (taskqueue.WorkflowRunMeta, taskqueue.Run) {
	t.Helper()
	meta := parent
	meta.ID, meta.Source, meta.SourceID, meta.RetryOf = id, "retry", parent.ID, parent.ID
	meta.CreatedAt = time.Now().UnixMilli()
	meta, execution, _, err := db.AdmitWorkflow(meta, "intent:"+id, taskqueue.Request{Source: "workflow", SourceID: meta.WorkflowID, Trigger: "retry", TriggerID: "start", DefinitionHash: "v1", Title: "Explicit retry", Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	return meta, execution
}

func workflowLineageTerminal(t *testing.T, m *Manager, db *taskqueue.Store, meta taskqueue.WorkflowRunMeta, execution taskqueue.Run, status string) {
	t.Helper()
	active, err := db.Claim(execution.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Finish(active.ID, active.Revision, taskqueue.Outcome{Status: status, SessionID: meta.ID})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := workflowMetaRecord(meta)
	if err != nil {
		t.Fatal(err)
	}
	rec.ExecutionID = execution.ID
	rec.Status, rec.EndedAt = workflow.RunCompleted, time.Now().UTC().Format(time.RFC3339Nano)
	if status == taskqueue.Interrupted {
		rec.Status, rec.Error = workflow.RunFailed, "process restarted"
	}
	if err := writeWFRecord(m.wfruns.recordPath(meta.ID), rec); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowScheduleLineageExplicitRetryUnblocksAfterKnownTerminal(t *testing.T) {
	m, db, job, old, original := workflowLineageFixture(t, taskqueue.Interrupted)
	if _, created, err := m.taskRuns.enqueueSchedule(job, schedule.TriggerManual, time.Now()); created || !errors.Is(err, schedule.ErrRunning) {
		t.Fatalf("unknown original admitted: %v %v", created, err)
	}
	retry, segment := workflowLineageRetry(t, db, original, "run_lineage_retry")
	if _, created, err := m.taskRuns.enqueueSchedule(job, schedule.TriggerScheduled, time.Now()); created || !errors.Is(err, schedule.ErrRunning) {
		t.Fatalf("queued retry admitted another clock tick: %v %v", created, err)
	}
	workflowLineageTerminal(t, m, db, retry, segment, taskqueue.Completed)
	_, created, err := m.taskRuns.enqueueSchedule(job, schedule.TriggerScheduled, time.Now())
	if err != nil || !created {
		t.Fatalf("known-terminal logical retry did not clear old guard: %v %v", created, err)
	}
	preserved, err := db.Get(old.ID)
	if err != nil || !reflect.DeepEqual(preserved, old) {
		t.Fatalf("old interruption receipt changed: %+v %v", preserved, err)
	}
}

func TestWorkflowScheduleLineageContinuationInterruptionRequiresRetryChain(t *testing.T) {
	m, db, job, old, original := workflowLineageFixture(t, taskqueue.Completed)
	continuation, _, err := db.EnqueueWorkflow(original.ID, taskqueue.Request{Source: "workflow", SourceID: original.WorkflowID, Trigger: "resume", TriggerID: "human:review:1", DefinitionHash: "v1", Title: "Human continuation", Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	workflowLineageTerminal(t, m, db, original, continuation, taskqueue.Interrupted)
	if _, created, err := m.taskRuns.enqueueSchedule(job, schedule.TriggerManual, time.Now()); created || !errors.Is(err, schedule.ErrRunning) {
		t.Fatalf("completed wrapper hid interrupted continuation: %v %v", created, err)
	}
	first, segment := workflowLineageRetry(t, db, original, "run_lineage_retry_1")
	workflowLineageTerminal(t, m, db, first, segment, taskqueue.Interrupted)
	if _, err := db.CancelWorkflow(first.ID); err != nil {
		t.Fatal(err)
	}
	if _, created, err := m.taskRuns.enqueueSchedule(job, schedule.TriggerManual, time.Now()); created || !errors.Is(err, schedule.ErrRunning) {
		t.Fatalf("cancel intent hid unknown child result: %v %v", created, err)
	}
	second, segment := workflowLineageRetry(t, db, first, "run_lineage_retry_2")
	workflowLineageTerminal(t, m, db, second, segment, taskqueue.Completed)
	_, created, err := m.taskRuns.enqueueSchedule(job, schedule.TriggerManual, time.Now())
	if err != nil || !created {
		t.Fatalf("reviewed retry chain stayed blocked: %v %v", created, err)
	}
	preserved, err := db.Get(old.ID)
	if err != nil || !reflect.DeepEqual(preserved, old) {
		t.Fatalf("wrapper receipt changed: %+v %v", preserved, err)
	}
}

func TestWorkflowScheduleLineageLeavesUnboundLegacyGuardUnchanged(t *testing.T) {
	scheduleEnv(t)
	m := sweepManager(t, 0)
	s, err := NewServer(m, ServerOptions{Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	db := taskCenterTestQueue(t, m)
	job := taskCenterAddSchedule(t, s, "legacy-lineage", "", "wf_legacy", nil)
	row := taskCenterEnqueue(t, db, job, "legacy")
	row, err = db.Claim(row.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	row, err = db.Finish(row.ID, row.Revision, taskqueue.Outcome{Status: taskqueue.Interrupted})
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := m.taskRuns.enqueueSchedule(job, schedule.TriggerManual, time.Now()); created || !errors.Is(err, schedule.ErrRunning) {
		t.Fatalf("unbound interruption bypassed: %v %v", created, err)
	}
	retry, err := db.Retry(row.ID, row.Revision)
	if err != nil {
		t.Fatal(err)
	}
	retry, err = db.Claim(retry.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Finish(retry.ID, retry.Revision, taskqueue.Outcome{Status: taskqueue.Completed}); err != nil {
		t.Fatal(err)
	}
	_, created, err := m.taskRuns.enqueueSchedule(job, schedule.TriggerManual, time.Now())
	if err != nil || !created {
		t.Fatalf("legacy explicit queue retry semantics changed: %v %v", created, err)
	}
}

func TestWorkflowScheduleLineageImportedLegacyWrapperCanBeReviewedByLogicalRetry(t *testing.T) {
	for _, wrapperStatus := range []string{taskqueue.Interrupted, taskqueue.Completed} {
		t.Run(wrapperStatus, func(t *testing.T) {
			scheduleEnv(t)
			m := sweepManager(t, 0)
			s, err := NewServer(m, ServerOptions{Token: "test-token"})
			if err != nil {
				t.Fatal(err)
			}
			db := taskCenterTestQueue(t, m)
			job := taskCenterAddSchedule(t, s, "imported-lineage", "", "wf_imported", nil)
			row := taskCenterEnqueue(t, db, job, "historical")
			row, err = db.Claim(row.ID, 2)
			if err != nil {
				t.Fatal(err)
			}
			rec := workflow.RunRecord{ID: "run_imported_history", WorkflowID: job.WorkflowID, Definition: taskCenterWorkflowDefinition(job.WorkflowID),
				CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Status: workflow.RunFailed, Error: "process restarted", Nodes: map[string]workflow.NodeState{}}
			if err := writeWFRecord(m.wfruns.recordPath(rec.ID), rec); err != nil {
				t.Fatal(err)
			}
			meta := workflowMeta(rec, "legacy", "")
			if err := db.ImportWorkflowRun(meta); err != nil {
				t.Fatal(err)
			}
			row, err = db.Finish(row.ID, row.Revision, taskqueue.Outcome{Status: wrapperStatus, SessionID: rec.ID})
			if err != nil {
				t.Fatal(err)
			}
			if _, handled, err := newWorkflowScheduleGuard(m.taskRuns, db).blocked(row); err != nil || handled {
				t.Fatalf("unmanaged imported wrapper changed legacy policy: %v %v", handled, err)
			}
			if wrapperStatus == taskqueue.Completed {
				continuation, _, err := db.EnqueueWorkflow(meta.ID, taskqueue.Request{Source: "workflow", SourceID: meta.WorkflowID, Trigger: "resume", TriggerID: "legacy-human", DefinitionHash: "v1", Title: "New continuation", Payload: json.RawMessage(`{}`)})
				if err != nil {
					t.Fatal(err)
				}
				workflowLineageTerminal(t, m, db, meta, continuation, taskqueue.Interrupted)
				if _, created, err := m.taskRuns.enqueueSchedule(job, schedule.TriggerManual, time.Now()); created || !errors.Is(err, schedule.ErrRunning) {
					t.Fatalf("legacy completed wrapper hid new continuation interruption: %v %v", created, err)
				}
			}
			retry, segment := workflowLineageRetry(t, db, meta, "run_imported_retry")
			if _, created, err := m.taskRuns.enqueueSchedule(job, schedule.TriggerManual, time.Now()); created || !errors.Is(err, schedule.ErrRunning) {
				t.Fatalf("historical wrapper ignored queued logical retry: %v %v", created, err)
			}
			workflowLineageTerminal(t, m, db, retry, segment, taskqueue.Completed)
			_, created, err := m.taskRuns.enqueueSchedule(job, schedule.TriggerManual, time.Now())
			if err != nil || !created {
				t.Fatalf("reviewed imported history stayed latched: %v %v", created, err)
			}
			if _, err := db.WorkflowRunForExecution(row.ID); !errors.Is(err, taskqueue.ErrNotFound) {
				t.Fatalf("historical wrapper was rebound: %v", err)
			}
			old, err := db.Get(row.ID)
			if err != nil || !reflect.DeepEqual(old, row) {
				t.Fatalf("historical receipt rewritten: %+v %v", old, err)
			}
		})
	}
}
