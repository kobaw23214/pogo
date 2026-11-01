package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/taskqueue"
	"github.com/yosukeno/pogo/workflow"
)

func workflowServiceRequest(t *testing.T, baseURL, token, method, path, body, key string) (int, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, baseURL+path, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, raw
}

func workflowServiceStart(t *testing.T, baseURL, token, wid, key string, want int) string {
	t.Helper()
	code, raw := workflowServiceRequest(t, baseURL, token, http.MethodPost, "/api/workflows/"+wid+"/runs", "", key)
	if code != want {
		t.Fatalf("start workflow: %d %s, want %d", code, raw, want)
	}
	if code != http.StatusCreated {
		return ""
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || result.ID == "" {
		t.Fatalf("invalid start receipt: %s %v", raw, err)
	}
	return result.ID
}

func awaitWorkflowService(t *testing.T, manager *Manager, rid string, ready func(workflow.RunRecord) bool) workflow.RunRecord {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var last workflow.RunRecord
	for time.Now().Before(deadline) {
		if rec, ok := manager.wfruns.snapshot(rid); ok {
			last = rec
			if ready(rec) {
				return rec
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("workflow %s did not reach expected state: %+v", rid, last)
	return last
}

func TestWorkflowServiceStartIntentAndPublishedConflict(t *testing.T) {
	h := newWorkflowHarness(t, func(context.Context, string, string) (llm.Response, error) { return textTurn("unused"), nil })
	wf := createWorkflow(t, h, map[string]any{"name": "固定运行意图", "nodes": []map[string]any{{"id": "review", "kind": "human", "prompt": "review"}}})
	publishWorkflow(t, h, wf.ID)
	release := taskRunsHoldSlots(t, h.mgr)
	defer release()
	first := workflowServiceStart(t, h.srv.URL, h.token, wf.ID, "request-once", http.StatusCreated)
	again := workflowServiceStart(t, h.srv.URL, h.token, wf.ID, "request-once", http.StatusCreated)
	if first != again {
		t.Fatalf("one start intent created two runs: %s / %s", first, again)
	}
	input := wf.workflowInput
	input.Name = "修改后的发布定义"
	if _, err := h.mgr.workflows.put(wf.ID, input, wf.Revision); err != nil {
		t.Fatal(err)
	}
	publishWorkflow(t, h, wf.ID)
	workflowServiceStart(t, h.srv.URL, h.token, wf.ID, "request-once", http.StatusConflict)
	rows, err := taskRunsDB(t, h.mgr).ListWorkflowRuns(taskqueue.WorkflowRunFilter{WorkflowID: wf.ID})
	if err != nil || len(rows) != 1 || rows[0].ID != first {
		t.Fatalf("idempotency catalog changed: %+v %v", rows, err)
	}
}

func TestWorkflowServiceQueuedCancellationDoesNotInvokeModel(t *testing.T) {
	var calls atomic.Int32
	h := newWorkflowHarness(t, func(context.Context, string, string) (llm.Response, error) {
		calls.Add(1)
		return textTurn("finished"), nil
	})
	a := createStudioAgent(t, h, "受队列控制", "one step", "ready")
	wf := createWorkflow(t, h, map[string]any{"name": "排队取消", "nodes": []map[string]any{{"id": "work", "agent": a.ID, "task": "do work"}}})
	publishWorkflow(t, h, wf.ID)
	release := taskRunsHoldSlots(t, h.mgr)
	defer release()
	rid := workflowServiceStart(t, h.srv.URL, h.token, wf.ID, "queued-cancel", http.StatusCreated)
	rec := getWorkflowRun(t, h, wf.ID, rid)
	if rec.Status != "queued" || rec.Capabilities == nil || !rec.Capabilities.CanCancel {
		t.Fatalf("queued run not visible/cancellable: %+v", rec)
	}
	code, raw := workflowServiceRequest(t, h.srv.URL, h.token, http.MethodPost, "/api/workflows/"+wf.ID+"/runs/"+rid+"/cancel", "", "")
	if code != http.StatusOK {
		t.Fatalf("cancel queued run: %d %s", code, raw)
	}
	release()
	rec = awaitWorkflowService(t, h.mgr, rid, func(rec workflow.RunRecord) bool { return rec.Status == workflow.RunCancelled })
	if calls.Load() != 0 || rec.ID != rid {
		t.Fatalf("cancelled admission invoked a model: calls=%d %+v", calls.Load(), rec)
	}
	executions, err := taskRunsDB(t, h.mgr).ListWorkflowExecutions(rid)
	if err != nil || len(executions) != 1 || executions[0].Status != taskqueue.Cancelled {
		t.Fatalf("cancelled segment receipt: %+v %v", executions, err)
	}
}

func TestWorkflowServiceHumanDecisionRaceRetainsOneLogicalRun(t *testing.T) {
	var calls atomic.Int32
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { calls.Add(1); return textTurn("finished"), nil })
	a, err := h.mgr.studio.put("", studioInput{Name: "审批后执行", Instructions: "finish", Model: "k3", Status: "ready"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	wf, err := h.mgr.workflows.put("", workflowInput{Name: "多人竞争决定", Nodes: []workflow.Node{
		{ID: "review", Kind: workflow.KindHuman, Prompt: "review"},
		{ID: "finish", Agent: a.ID, Task: "finish once", After: []workflow.Dep{{Node: "review"}}},
	}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.mgr.workflows.publish(wf.ID, "v1", ""); err != nil {
		t.Fatal(err)
	}
	rid := workflowServiceStart(t, h.srv.URL, aliceToken, wf.ID, "human-start", http.StatusCreated)
	awaitWorkflowService(t, h.mgr, rid, func(rec workflow.RunRecord) bool { return rec.Nodes["review"].Status == workflow.NodeWaitingHuman })
	db := taskRunsDB(t, h.mgr)
	deadline := time.Now().Add(8 * time.Second)
	for {
		executions, err := db.ListWorkflowExecutions(rid)
		if err != nil {
			t.Fatal(err)
		}
		if len(executions) == 1 && executions[0].Status == taskqueue.Completed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiting workflow retained execution slot: %+v", executions)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Both real database worker slots must now be available. Keep them held
	// while conflicting decisions arrive, so the test observes durable command
	// arbitration rather than a race against an already completed model call.
	release := taskRunsHoldSlots(t, h.mgr)
	defer release()
	bobID := identity.LocalPrincipal(identity.LocalScope("bob"), "bob", false).ID
	if err := db.SetRunGrant(rid, bobID, "review", []string{wfRunRead, wfRunApprove}); err != nil {
		t.Fatal(err)
	}
	type result struct {
		code          int
		actor, action string
		raw           []byte
	}
	results := make(chan result, 2)
	var workers sync.WaitGroup
	for _, candidate := range []struct{ token, actor, action string }{{aliceToken, sessionResultOwnerKey(&identity.Scope{AppID: "pogo", TenantID: "local", SubjectID: identity.LocalSubject("alice"), ProjectID: "default"}), "approve"}, {bobToken, bobID, "reject"}} {
		workers.Add(1)
		go func(token, actor, action string) {
			defer workers.Done()
			body, _ := json.Marshal(map[string]any{"action": action})
			code, raw := workflowServiceRequest(t, h.srv.URL, token, http.MethodPost, "/api/workflows/"+wf.ID+"/runs/"+rid+"/nodes/review/resume", string(body), "")
			results <- result{code, actor, action, raw}
		}(candidate.token, candidate.actor, candidate.action)
	}
	workers.Wait()
	close(results)
	var accepted result
	conflicts := 0
	for result := range results {
		switch result.code {
		case http.StatusOK:
			accepted = result
		case http.StatusConflict:
			conflicts++
		default:
			t.Fatalf("unexpected human decision response: %d %s", result.code, result.raw)
		}
	}
	if accepted.actor == "" || conflicts != 1 {
		t.Fatalf("decisions not arbitrated: accepted=%+v conflicts=%d", accepted, conflicts)
	}
	release()
	final := awaitWorkflowService(t, h.mgr, rid, func(rec workflow.RunRecord) bool { return workflow.IsTerminal(rec.Status) })
	if final.ID != rid || final.Nodes["review"].Human.DecidedBy != accepted.actor || final.Nodes["review"].Human.Action != accepted.action {
		t.Fatalf("decision audit/run identity changed: %+v", final)
	}
	wantCalls := int32(0)
	if accepted.action == "approve" {
		wantCalls = 1
	}
	if calls.Load() != wantCalls {
		t.Fatalf("downstream ran unexpectedly: calls=%d want=%d", calls.Load(), wantCalls)
	}
	executions, err := db.ListWorkflowExecutions(rid)
	if err != nil || len(executions) != 2 {
		t.Fatalf("logical run lost segment relation: %+v %v", executions, err)
	}
}

func TestWorkflowServiceInitializationFailureIsNotPermanentlyQueued(t *testing.T) {
	var calls atomic.Int32
	h := newWorkflowHarness(t, func(context.Context, string, string) (llm.Response, error) {
		calls.Add(1)
		return textTurn("unused"), nil
	})
	a := createStudioAgent(t, h, "写入失败前不执行", "finish", "ready")
	wf := createWorkflow(t, h, map[string]any{"name": "初始化失败", "nodes": []map[string]any{{"id": "work", "agent": a.ID, "task": "finish"}}})
	publishWorkflow(t, h, wf.ID)
	h.mgr.wfruns.persist = func(string, workflow.RunRecord) error { return errors.New("injected initial state write failure") }
	rid := workflowServiceStart(t, h.srv.URL, h.token, wf.ID, "initial-failure", http.StatusCreated)
	db := taskRunsDB(t, h.mgr)
	deadline := time.Now().Add(8 * time.Second)
	for {
		executions, err := db.ListWorkflowExecutions(rid)
		if err != nil {
			t.Fatal(err)
		}
		if len(executions) == 1 && executions[0].Status == taskqueue.Failed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("initial failure not recorded: %+v", executions)
		}
		time.Sleep(5 * time.Millisecond)
	}
	rec := getWorkflowRun(t, h, wf.ID, rid)
	if rec.Status != workflow.RunFailed || rec.Error == "" || calls.Load() != 0 {
		t.Fatalf("initial failure shown as queued/success: %+v calls=%d", rec, calls.Load())
	}
	if _, err := os.Stat(h.mgr.wfruns.recordPath(rid)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("test must cover absent state file, got %v", err)
	}
}

func TestWorkflowServiceRestartPreservesLegacyAndPendingDecision(t *testing.T) {
	sessionDir, cwd := t.TempDir(), t.TempDir()
	legacyOwner := identity.LocalScope("retained-user")
	legacy := workflow.RunRecord{ID: "run_retained_history", WorkflowID: "retained-workflow", Status: workflow.RunCompleted, Owner: &legacyOwner,
		CreatedAt: "2026-10-01T00:00:00Z", EndedAt: "2026-10-01T00:00:01Z", Definition: workflow.Definition{ID: "retained-workflow", Name: "旧记录"}, Nodes: map[string]workflow.NodeState{}}
	raw, _ := json.MarshalIndent(legacy, "", "  ")
	raw = append(raw, '\n')
	legacyPath := filepath.Join(sessionDir, "wf-runs", legacy.ID, wfRunRecordFile)
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	// Old optional timestamps and malformed files must not prevent startup,
	// and importing them must not rewrite the original history.
	undated := legacy
	undated.ID, undated.CreatedAt = "run_undated_history", ""
	undatedRaw, _ := json.Marshal(undated)
	undatedPath := filepath.Join(sessionDir, "wf-runs", undated.ID, wfRunRecordFile)
	if err := writeWFRecord(undatedPath, undated); err != nil {
		t.Fatal(err)
	}
	undatedRaw, _ = os.ReadFile(undatedPath)
	brokenPath := filepath.Join(sessionDir, "wf-runs", "run_incomplete_history", wfRunRecordFile)
	if err := os.MkdirAll(filepath.Dir(brokenPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(brokenPath, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	stream := func(context.Context, string, string) (llm.Response, error) {
		calls.Add(1)
		return textTurn("continued once"), nil
	}
	configure := func(c *Config) { c.SessionDir, c.Cwd = sessionDir, cwd }
	h := newWorkflowHarness(t, stream, configure)
	a := createStudioAgent(t, h, "重启后执行", "finish", "ready")
	wf := createWorkflow(t, h, map[string]any{"name": "恢复原运行", "nodes": []map[string]any{
		{"id": "review", "kind": "human", "prompt": "review"},
		{"id": "finish", "agent": a.ID, "task": "finish once", "after": []string{"review"}},
	}})
	publishWorkflow(t, h, wf.ID)
	rid := workflowServiceStart(t, h.srv.URL, h.token, wf.ID, "restart-pending", http.StatusCreated)
	awaitWorkflowService(t, h.mgr, rid, func(rec workflow.RunRecord) bool { return rec.Nodes["review"].Status == workflow.NodeWaitingHuman })
	db := taskRunsDB(t, h.mgr)
	deadline := time.Now().Add(8 * time.Second)
	for {
		executions, err := db.ListWorkflowExecutions(rid)
		if err != nil {
			t.Fatal(err)
		}
		if len(executions) == 1 && executions[0].Status == taskqueue.Completed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first segment did not suspend")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Durable queued commands must survive shutdown. Reservations belong only
	// to this disposable database and intentionally recover as interrupted.
	_ = taskRunsHoldSlots(t, h.mgr)
	code, response := workflowServiceRequest(t, h.srv.URL, h.token, http.MethodPost, "/api/workflows/"+wf.ID+"/runs/"+rid+"/nodes/review/resume", `{"action":"approve"}`, "")
	if code != http.StatusOK || calls.Load() != 0 {
		t.Fatalf("pending decision did not remain queued: %d %s calls=%d", code, response, calls.Load())
	}
	h.srv.Close()
	h.mgr.Close()
	h2 := newWorkflowHarness(t, stream, configure)
	final := awaitWorkflowService(t, h2.mgr, rid, func(rec workflow.RunRecord) bool { return workflow.IsTerminal(rec.Status) })
	if final.Status != workflow.RunCompleted || final.ID != rid || calls.Load() != 1 || final.AppliedCommandID == "" {
		t.Fatalf("pending command not recovered exactly once: %+v calls=%d", final, calls.Load())
	}
	retained, err := os.ReadFile(legacyPath)
	if err != nil || !bytes.Equal(retained, raw) {
		t.Fatalf("legacy JSON was rewritten: %v", err)
	}
	meta, err := taskRunsDB(t, h2.mgr).GetWorkflowRun(legacy.ID)
	if err != nil || meta.OwnerID != sessionResultOwnerKey(&legacyOwner) {
		t.Fatalf("legacy owner changed: %+v %v", meta, err)
	}
	if retained, err := os.ReadFile(undatedPath); err != nil || !bytes.Equal(retained, undatedRaw) {
		t.Fatalf("undated history changed: %v", err)
	}
	if meta, err := taskRunsDB(t, h2.mgr).GetWorkflowRun(undated.ID); err != nil || meta.CreatedAt != 0 {
		t.Fatalf("undated history not preserved: %+v %v", meta, err)
	}
	if retained, err := os.ReadFile(brokenPath); err != nil || string(retained) != "{}" {
		t.Fatalf("incomplete history changed: %v", err)
	}
}
