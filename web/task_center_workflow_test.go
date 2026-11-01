package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/taskqueue"
	"github.com/yosukeno/pogo/workflow"
)

func taskCenterWorkflowDefinition(id string) workflow.Definition {
	return workflow.Definition{ID: id, Name: "Shared workflow", Revision: 1,
		Nodes: []workflow.Node{{ID: "review", Kind: workflow.KindHuman, Prompt: "Review the result"}}}
}

func taskCenterRegisterWorkflowDefinition(m *Manager, def workflow.Definition) {
	m.workflows.mu.Lock()
	defer m.workflows.mu.Unlock()
	m.workflows.data.Workflows[def.ID] = Workflow{ID: def.ID, Revision: def.Revision, workflowInput: workflowInput{Name: def.Name, Description: def.Description, Nodes: def.Nodes}}
}

func TestTaskCenterWorkflowManualAdmissionAndActionPermissions(t *testing.T) {
	scheduleEnv(t)
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { t.Error("task center started a model"); return llm.Response{}, nil })
	db := taskCenterTestQueue(t, h.mgr)
	front := taskCenterTestFront(t, h.psrv)
	alice, bob := identity.LocalScope("alice"), identity.LocalScope("bob")
	taskCenterRegisterWorkflowDefinition(h.mgr, taskCenterWorkflowDefinition("wf_manual"))
	meta, err := h.mgr.admitWorkflow(taskCenterWorkflowDefinition("wf_manual"), &alice, "manual", "wf_manual", "intent_1", "")
	if err != nil {
		t.Fatal(err)
	}
	code, out, raw := taskCenterRequest(t, front, aliceToken, http.MethodGet, "/api/tasks", "")
	runs := taskCenterIDs(t, out, "runs")
	row := runs[meta.ID]
	if code != http.StatusOK || len(runs) != 1 || row == nil || row["source"] != "workflow" || row["workflow_run_id"] != meta.ID || row["status"] != "queued" || row["can_cancel"] != true || row["can_retry"] != false {
		t.Fatalf("manual logical run missing: %d %s", code, raw)
	}
	if len(taskCenterIDs(t, out, "tasks")) != 0 {
		t.Fatalf("manual run manufactured a schedule: %s", raw)
	}
	if !strings.Contains(row["result_url"].(string), "?run="+meta.ID) {
		t.Fatalf("wrong logical result: %s", raw)
	}
	code, out, raw = taskCenterRequest(t, front, bobToken, http.MethodGet, "/api/tasks", "")
	if code != http.StatusOK || len(taskCenterIDs(t, out, "runs")) != 0 {
		t.Fatalf("other owner saw run: %d %s", code, raw)
	}
	pid := identity.LocalPrincipal(bob, "bob", false).ID
	if err := db.SetRunGrant(meta.ID, pid, "", []string{wfRunRead}); err != nil {
		t.Fatal(err)
	}
	code, out, raw = taskCenterRequest(t, front, bobToken, http.MethodGet, "/api/tasks", "")
	row = taskCenterIDs(t, out, "runs")[meta.ID]
	if code != http.StatusOK || row == nil || row["can_cancel"] != false || row["can_retry"] != false {
		t.Fatalf("observer controls leaked: %d %s", code, raw)
	}
	segments, err := db.ListWorkflowExecutions(meta.ID)
	if err != nil || len(segments) != 1 {
		t.Fatalf("segments: %+v %v", segments, err)
	}
	for _, id := range []string{meta.ID, segments[0].ID} {
		code, _, raw = taskCenterRequest(t, front, bobToken, http.MethodPost, "/api/task-runs/"+id+"/cancel", `{"revision":1}`)
		if code != http.StatusNotFound {
			t.Fatalf("observer cancelled via %s: %d %s", id, code, raw)
		}
	}
	code, out, raw = taskCenterRequest(t, front, aliceToken, http.MethodGet, "/api/tasks?agent_id=agent-a", "")
	if code != http.StatusOK || len(taskCenterIDs(t, out, "runs")) != 0 {
		t.Fatalf("Agent-specific center acquired unbound workflow: %d %s", code, raw)
	}
	code, out, raw = taskCenterRequest(t, front, aliceToken, http.MethodPost, "/api/task-runs/"+meta.ID+"/cancel", `{"revision":1}`)
	if code != http.StatusOK {
		t.Fatalf("owner cancel: %d %s", code, raw)
	}
	segment, err := db.Get(segments[0].ID)
	if err != nil || segment.Status != taskqueue.Cancelled {
		t.Fatalf("cancel did not reach execution: %+v %v", segment, err)
	}
	if meta, err := db.GetWorkflowRun(meta.ID); err != nil || !meta.CancelRequested {
		t.Fatalf("cancel intent not durable: %+v %v", meta, err)
	}
}

func TestTaskCenterWorkflowDeduplicatesScheduleAndHumanContinuation(t *testing.T) {
	for _, imported := range []bool{false, true} {
		t.Run(fmt.Sprintf("imported_%v", imported), func(t *testing.T) {
			scheduleEnv(t)
			m := sweepManager(t, 0)
			s, err := NewServer(m, ServerOptions{Token: "test-token"})
			if err != nil {
				t.Fatal(err)
			}
			db := taskCenterTestQueue(t, m)
			front := taskCenterTestFront(t, s)
			j := taskCenterAddSchedule(t, s, "wf_schedule", "", "wf_native", nil)
			first := taskCenterEnqueue(t, db, j, "first")
			first, err = db.Claim(first.ID, 2)
			if err != nil {
				t.Fatal(err)
			}
			rec := workflow.RunRecord{ID: "run_task_center", WorkflowID: j.WorkflowID, Definition: taskCenterWorkflowDefinition(j.WorkflowID),
				Status: workflow.RunWorking, StateRevision: 7, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
				Nodes: map[string]workflow.NodeState{"review": {Status: workflow.NodeWaitingHuman}}}
			meta := workflowMeta(rec, "schedule", j.ID)
			if imported {
				meta = workflowMeta(rec, "legacy", "")
				if err := db.ImportWorkflowRun(meta); err != nil {
					t.Fatal(err)
				}
			} else {
				meta, _, err = db.BindWorkflowExecution(meta, "schedule:"+first.ID, first.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := writeWFRecord(m.wfruns.recordPath(rec.ID), rec); err != nil {
				t.Fatal(err)
			}
			_, err = db.Finish(first.ID, first.Revision, taskqueue.Outcome{Status: taskqueue.Completed, SessionID: rec.ID, EndReason: "waiting_human"})
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = db.EnqueueWorkflow(meta.ID, taskqueue.Request{Source: "workflow", SourceID: rec.WorkflowID, Trigger: "resume", TriggerID: "resume_review", DefinitionHash: "v1", Title: "Workflow continuation", Payload: json.RawMessage(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			code, out, raw := taskCenterRequest(t, front, "test-token", http.MethodGet, "/api/tasks", "")
			runs := taskCenterIDs(t, out, "runs")
			row := runs[meta.ID]
			wantSource := "schedule"
			if imported {
				wantSource = "workflow"
			}
			if code != http.StatusOK || len(runs) != 1 || row == nil || row["source"] != wantSource || row["status"] != "running" || row["waiting_human"] != true || row["can_cancel"] != true || row["revision"] != float64(8) {
				t.Fatalf("logical human run duplicated or lost: %d %s", code, raw)
			}
			if !imported && (row["source_id"] != j.ID || row["task_id"] != "schedule:"+j.ID) {
				t.Fatalf("schedule attribution lost: %s", raw)
			}
			rec.Status, rec.StateRevision, rec.EndedAt = workflow.RunCompleted, 8, time.Now().UTC().Format(time.RFC3339Nano)
			rec.Nodes["review"] = workflow.NodeState{Status: workflow.NodeSuccess}
			if err := writeWFRecord(m.wfruns.recordPath(rec.ID), rec); err != nil {
				t.Fatal(err)
			}
			code, out, raw = taskCenterRequest(t, front, "test-token", http.MethodGet, "/api/tasks", "")
			row = taskCenterIDs(t, out, "runs")[meta.ID]
			if code != http.StatusOK || row["status"] != "completed" || row["waiting_human"] != false || row["can_cancel"] != false || row["end_reason"] == "waiting_human" {
				t.Fatalf("completed run kept stale human state: %d %s", code, raw)
			}
		})
	}
}

func TestTaskCenterWorkflowRetryUsesLogicalStateAndPreservesOldRun(t *testing.T) {
	scheduleEnv(t)
	h := newMultiuserHarness(t, func(int) (llm.Response, error) {
		t.Error("retry contacted model before dispatch")
		return llm.Response{}, nil
	})
	db := taskCenterTestQueue(t, h.mgr)
	front := taskCenterTestFront(t, h.psrv)
	alice := identity.LocalScope("alice")
	taskCenterRegisterWorkflowDefinition(h.mgr, taskCenterWorkflowDefinition("wf_retry"))
	meta, err := h.mgr.admitWorkflow(taskCenterWorkflowDefinition("wf_retry"), &alice, "manual", "wf_retry", "original", "")
	if err != nil {
		t.Fatal(err)
	}
	segments, err := db.ListWorkflowExecutions(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	segment, err := db.Claim(segments[0].ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Finish(segment.ID, segment.Revision, taskqueue.Outcome{Status: taskqueue.Failed, Error: "test failure"})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := workflowMetaRecord(meta)
	if err != nil {
		t.Fatal(err)
	}
	rec.Status, rec.StateRevision, rec.Error, rec.EndedAt = workflow.RunFailed, 4, "test failure", time.Now().UTC().Format(time.RFC3339Nano)
	if err := writeWFRecord(h.mgr.wfruns.recordPath(meta.ID), rec); err != nil {
		t.Fatal(err)
	}
	code, _, raw := taskCenterRequest(t, front, aliceToken, http.MethodPost, "/api/task-runs/"+meta.ID+"/retry", `{"revision":4}`)
	if code != http.StatusConflict {
		t.Fatalf("stale logical revision accepted: %d %s", code, raw)
	}
	code, out, raw := taskCenterRequest(t, front, aliceToken, http.MethodPost, "/api/task-runs/"+meta.ID+"/retry", `{"revision":5}`)
	if code != http.StatusOK {
		t.Fatalf("owner retry: %d %s", code, raw)
	}
	next := out["run"].(map[string]any)["id"].(string)
	if next == meta.ID {
		t.Fatal("retry reused terminal logical run")
	}
	child, err := db.GetWorkflowRun(next)
	if err != nil || child.RetryOf != meta.ID || child.OwnerID != meta.OwnerID {
		t.Fatalf("retry linkage: %+v %v", child, err)
	}
	code, out, raw = taskCenterRequest(t, front, aliceToken, http.MethodPost, "/api/task-runs/"+meta.ID+"/retry", fmt.Sprintf(`{"revision":%d}`, 5))
	if code != http.StatusOK || out["run"].(map[string]any)["id"] != next {
		t.Fatalf("retry request created duplicate: %d %s", code, raw)
	}
	old, ok := h.mgr.wfruns.snapshot(meta.ID)
	if !ok || old.Status != workflow.RunFailed || old.Error != "test failure" {
		t.Fatalf("old outcome overwritten: %+v", old)
	}
	bob := identity.LocalPrincipal(identity.LocalScope("bob"), "bob", false)
	if err := db.SetRunGrant(meta.ID, bob.ID, "", []string{wfRunRead, wfRunRetry}); err != nil {
		t.Fatal(err)
	}
	code, out, raw = taskCenterRequest(t, front, bobToken, http.MethodPost, "/api/task-runs/"+meta.ID+"/retry", `{"revision":5}`)
	if code != http.StatusOK {
		t.Fatalf("authorized participant retry: %d %s", code, raw)
	}
	delegatedID := out["run"].(map[string]any)["id"].(string)
	delegated, err := db.GetWorkflowRun(delegatedID)
	if err != nil || delegated.InitiatorID != bob.ID || delegated.OwnerID != meta.OwnerID || delegated.ExecutionPrincipalID != meta.ExecutionPrincipalID {
		t.Fatalf("retry lost actor or expanded execution identity: %+v %v", delegated, err)
	}
	grant, err := db.RunGrant(delegatedID, bob.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range grant {
		if action == wfRunApprove {
			t.Fatal("retry initiator received implicit approval")
		}
	}
	code, out, raw = taskCenterRequest(t, front, bobToken, http.MethodGet, "/api/tasks", "")
	if code != http.StatusOK || taskCenterIDs(t, out, "runs")[delegatedID] == nil {
		t.Fatalf("retry initiator cannot follow new Run: %d %s", code, raw)
	}
}
