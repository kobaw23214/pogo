package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/taskqueue"
	"github.com/yosukeno/pogo/workflow"
)

func trialHash(t *testing.T, manager *Manager, wf Workflow) string {
	t.Helper()
	_, _, hash, err := manager.workflows.canonicalDraft(wf)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func trialStart(t *testing.T, url, token string, wf Workflow, hash, key string, inputs map[string]string, want int) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"expected_revision": wf.Revision, "expected_hash": hash, "request_id": key, "inputs": inputs})
	code, raw := workflowServiceRequest(t, url, token, http.MethodPost, "/api/manage/workflows/"+wf.ID+"/test-run", string(body), "")
	if code != want {
		t.Fatalf("draft trial: %d %s, want %d", code, raw, want)
	}
	if code != http.StatusCreated {
		return ""
	}
	var response struct {
		ID             string `json:"id"`
		TestRun        bool   `json:"test_run"`
		DraftRevision  int    `json:"draft_revision"`
		DefinitionHash string `json:"definition_hash"`
		RealExecution  bool   `json:"real_execution"`
	}
	if err := json.Unmarshal(raw, &response); err != nil || response.ID == "" || !response.TestRun || response.DraftRevision != wf.Revision || response.DefinitionHash != hash || !response.RealExecution {
		t.Fatalf("invalid trial receipt: %s %v", raw, err)
	}
	return response.ID
}

func TestWorkflowManualParameterizedRunPreservesInputsAndIdempotency(t *testing.T) {
	var got atomic.Value
	h := newWorkflowHarness(t, func(_ context.Context, _, user string) (llm.Response, error) {
		got.Store(user)
		return textTurn("done"), nil
	})
	a := createStudioAgent(t, h, "参数执行", "reply only", "ready")
	wf := createWorkflow(t, h, map[string]any{"name": "参数工作流", "nodes": []map[string]any{
		{"id": "finish", "agent": a.ID, "task": "Process {{ inputs.order_id }}", "tools": []string{}},
	}})
	publishWorkflow(t, h, wf.ID)
	if code, _ := workflowServiceRequest(t, h.srv.URL, h.token, http.MethodPost, "/api/workflows/"+wf.ID+"/runs", `{"inputs":{"order_id":"A"},"version_id":"000000000000"}`, "wrong-version"); code != http.StatusConflict {
		t.Fatalf("explicit published version was not bound: %d", code)
	}
	for _, body := range []string{`{}`, `{"inputs":{"unknown":"x"}}`, `{"inputs":{"order_id":"A"},"owner_id":"forged"}`, `{"inputs":{"order_id":42}}`, `{"inputs":{"order_id":"A"}} {}`} {
		code, _ := workflowServiceRequest(t, h.srv.URL, h.token, http.MethodPost, "/api/workflows/"+wf.ID+"/runs", body, "invalid")
		if code != http.StatusBadRequest {
			t.Fatalf("accepted invalid body %s: %d", body, code)
		}
	}
	body := `{"inputs":{"order_id":"采购单-A"}}`
	code, raw := workflowServiceRequest(t, h.srv.URL, h.token, http.MethodPost, "/api/workflows/"+wf.ID+"/runs", body, "parameter-intent")
	if code != http.StatusCreated {
		t.Fatalf("parameter start: %d %s", code, raw)
	}
	var response struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &response)
	code, repeated := workflowServiceRequest(t, h.srv.URL, h.token, http.MethodPost, "/api/workflows/"+wf.ID+"/runs", body, "parameter-intent")
	if code != http.StatusCreated || string(repeated) != string(raw) {
		t.Fatalf("identical input intent duplicated: %d %s / %s", code, repeated, raw)
	}
	code, _ = workflowServiceRequest(t, h.srv.URL, h.token, http.MethodPost, "/api/workflows/"+wf.ID+"/runs", `{"inputs":{"order_id":"B"}}`, "parameter-intent")
	if code != http.StatusConflict {
		t.Fatalf("input change should conflict: %d", code)
	}
	rec := awaitWorkflowService(t, h.mgr, response.ID, func(r workflow.RunRecord) bool { return workflow.IsTerminal(r.Status) })
	if rec.Status != workflow.RunCompleted || rec.Inputs["order_id"] != "采购单-A" || rec.TestRun || !strings.Contains(got.Load().(string), "采购单-A") {
		t.Fatalf("parameter runtime lost binding: %+v user=%v", rec, got.Load())
	}
	meta, err := taskRunsDB(t, h.mgr).GetWorkflowRun(rec.ID)
	if err != nil || meta.Inputs["order_id"] != "采购单-A" || meta.TestRun {
		t.Fatalf("parameter input not durable: %+v %v", meta, err)
	}
}

func TestWorkflowDraftTrialFrozenApprovalRestartAndUnchangedPublication(t *testing.T) {
	dir, cwd := t.TempDir(), t.TempDir()
	var calls atomic.Int32
	stream := func(_ context.Context, _, user string) (llm.Response, error) {
		calls.Add(1)
		if !strings.Contains(user, "SAMPLE-A") {
			t.Errorf("trial input was not applied: %q", user)
		}
		return textTurn("draft sample done"), nil
	}
	config := func(c *Config) { c.SessionDir, c.Cwd = dir, cwd }
	h := newWorkflowHarness(t, stream, config)
	a := createStudioAgent(t, h, "试跑执行", "reply", "ready")
	wf := createWorkflow(t, h, map[string]any{"name": "原发布定义", "nodes": []map[string]any{
		{"id": "review", "kind": "human", "prompt": "review {{ inputs.case_id }}"},
		{"id": "finish", "agent": a.ID, "task": "finish {{ inputs.case_id }}", "tools": []string{}, "after": []string{"review"}},
	}})
	pointer := publishWorkflow(t, h, wf.ID)
	input := wf.workflowInput
	input.Name = "尚未发布的草稿"
	draft, err := h.mgr.workflows.put(wf.ID, input, wf.Revision)
	if err != nil {
		t.Fatal(err)
	}
	hash := trialHash(t, h.mgr, draft)
	rid := trialStart(t, h.srv.URL, h.token, draft, hash, "sample-once", map[string]string{"case_id": "SAMPLE-A"}, http.StatusCreated)
	if again := trialStart(t, h.srv.URL, h.token, draft, hash, "sample-once", map[string]string{"case_id": "SAMPLE-A"}, http.StatusCreated); again != rid {
		t.Fatalf("trial was duplicated: %s / %s", rid, again)
	}
	trialStart(t, h.srv.URL, h.token, draft, hash, "sample-once", map[string]string{"case_id": "SAMPLE-B"}, http.StatusConflict)
	rec := awaitWorkflowService(t, h.mgr, rid, func(r workflow.RunRecord) bool { return r.Nodes["review"].Status == workflow.NodeWaitingHuman })
	if !rec.TestRun || rec.DraftRevision != draft.Revision || rec.DefinitionHash != hash || rec.Definition.Name != draft.Name || rec.Inputs["case_id"] != "SAMPLE-A" || rec.Origin != nil || calls.Load() != 0 {
		t.Fatalf("trial record is not frozen and attributed: %+v", rec)
	}
	meta, err := taskRunsDB(t, h.mgr).GetWorkflowRun(rid)
	if err != nil || !meta.TestRun || meta.Source != "draft_test" || meta.PublicationID != "" || meta.Callback != nil {
		t.Fatalf("trial inherited a business publication/callback: %+v %v", meta, err)
	}
	input = draft.workflowInput
	input.Name = "随后继续编辑的草稿"
	newer, err := h.mgr.workflows.put(wf.ID, input, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	trialStart(t, h.srv.URL, h.token, draft, hash, "stale-review", map[string]string{"case_id": "SAMPLE-A"}, http.StatusConflict)
	if p, ok := h.mgr.workflows.pointer(wf.ID); !ok || p.VersionID != pointer.VersionID {
		t.Fatalf("trial moved the real publication pointer: %+v", p)
	}
	// A clean restart retains the marker, frozen draft, input and approval wait.
	deadline := time.Now().Add(8 * time.Second)
	for {
		segments, err := taskRunsDB(t, h.mgr).ListWorkflowExecutions(rid)
		if err != nil {
			t.Fatal(err)
		}
		if len(segments) == 1 && segments[0].Status == taskqueue.Completed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("trial did not release its first execution slot")
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.srv.Close()
	h.mgr.Close()
	h2 := newWorkflowHarness(t, stream, config)
	rec = getWorkflowRun(t, h2, wf.ID, rid)
	if !rec.TestRun || rec.DraftRevision != draft.Revision || rec.DefinitionHash != hash || rec.Definition.Name != draft.Name {
		t.Fatalf("restart changed trial snapshot: %+v", rec)
	}
	code, raw := workflowServiceRequest(t, h2.srv.URL, h2.token, http.MethodPost, "/api/workflows/"+wf.ID+"/runs/"+rid+"/nodes/review/resume", `{"action":"approve"}`, "")
	if code != http.StatusOK {
		t.Fatalf("trial resume: %d %s", code, raw)
	}
	rec = awaitWorkflowService(t, h2.mgr, rid, func(r workflow.RunRecord) bool { return workflow.IsTerminal(r.Status) })
	if rec.Status != workflow.RunCompleted || !rec.TestRun || rec.ID != rid || calls.Load() != 1 || rec.Definition.Name != draft.Name {
		t.Fatalf("trial approval did not resume exact Run: %+v calls=%d", rec, calls.Load())
	}
	if p, ok := h2.mgr.workflows.pointer(wf.ID); !ok || p.VersionID != pointer.VersionID {
		t.Fatalf("trial completion moved publication: %+v", p)
	}
	retained, _ := h2.mgr.workflows.get(wf.ID)
	if retained.Name != newer.Name || retained.Revision != newer.Revision {
		t.Fatal("trial overwrote later draft edits")
	}
	segments, err := taskRunsDB(t, h2.mgr).ListWorkflowExecutions(rid)
	if err != nil || len(segments) != 2 {
		t.Fatalf("trial should have two execution segments: %+v %v", segments, err)
	}
}

func TestWorkflowDraftTrialPermissionsApprovalAndCancelRetry(t *testing.T) {
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { return textTurn("unused"), nil })
	alice := identity.LocalPrincipal(identity.LocalScope("alice"), "alice", false)
	if err := taskRunsDB(t, h.mgr).SetAccessRole(alice.ID, "builder"); err != nil {
		t.Fatal(err)
	}
	wf, err := h.mgr.workflows.put("", workflowInput{Name: "受控独立审批试跑", Nodes: []workflow.Node{{ID: "review", Kind: workflow.KindHuman, Prompt: "review {{ inputs.case_id }}", ApprovalPolicy: &workflow.ApprovalPolicy{PreventSelfApproval: true, Assignment: "fixed", Approvers: []string{"alice", "bob"}}}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	hash := trialHash(t, h.mgr, wf)
	trialStart(t, h.srv.URL, bobToken, wf, hash, "reader-may-not-test", map[string]string{"case_id": "ONE"}, http.StatusForbidden)
	rid := trialStart(t, h.srv.URL, aliceToken, wf, hash, "builder-sample", map[string]string{"case_id": "ONE"}, http.StatusCreated)
	awaitWorkflowService(t, h.mgr, rid, func(r workflow.RunRecord) bool { return r.Nodes["review"].Status == workflow.NodeWaitingHuman })
	code, centerRaw := workflowServiceRequest(t, h.srv.URL, aliceToken, http.MethodGet, "/api/tasks", "", "")
	if code != http.StatusOK {
		t.Fatalf("task center trial projection: %d %s", code, centerRaw)
	}
	var center struct {
		Runs []taskCenterRun `json:"runs"`
	}
	_ = json.Unmarshal(centerRaw, &center)
	found := false
	for _, item := range center.Runs {
		if item.ID == rid {
			found = item.TestRun && item.DraftRevision == wf.Revision && item.DefinitionHash == hash
		}
	}
	if !found {
		t.Fatalf("task center lost draft-test provenance: %s", centerRaw)
	}
	path := "/api/workflows/" + wf.ID + "/runs/" + rid
	code, _ = workflowServiceRequest(t, h.srv.URL, aliceToken, http.MethodPost, path+"/nodes/review/resume", `{"action":"approve"}`, "")
	if code != http.StatusNotFound {
		t.Fatalf("trial bypassed self-approval rule: %d", code)
	}
	code, raw := workflowServiceRequest(t, h.srv.URL, bobToken, http.MethodPost, path+"/nodes/review/resume", `{"action":"approve"}`, "")
	if code != http.StatusOK {
		t.Fatalf("automatic trial reviewer: %d %s", code, raw)
	}
	awaitWorkflowService(t, h.mgr, rid, func(r workflow.RunRecord) bool { return r.Status == workflow.RunCompleted })
	if _, ok := h.mgr.workflows.pointer(wf.ID); ok {
		t.Fatal("unpublished trial created a publication")
	}
	// Explicit cancellation/retry is still the ordinary API and preserves the
	// trial label plus inputs, rather than silently turning into a business Run.
	release := taskRunsHoldSlots(t, h.mgr)
	cancelID := trialStart(t, h.srv.URL, aliceToken, wf, hash, "cancel-sample", map[string]string{"case_id": "CANCEL"}, http.StatusCreated)
	code, raw = workflowServiceRequest(t, h.srv.URL, aliceToken, http.MethodPost, "/api/workflows/"+wf.ID+"/runs/"+cancelID+"/cancel", "", "")
	if code != http.StatusOK {
		t.Fatalf("trial cancel: %d %s", code, raw)
	}
	release()
	awaitWorkflowService(t, h.mgr, cancelID, func(r workflow.RunRecord) bool { return r.Status == workflow.RunCancelled })
	code, raw = workflowServiceRequest(t, h.srv.URL, aliceToken, http.MethodPost, "/api/workflows/"+wf.ID+"/runs/"+cancelID+"/retry", "", "retry-sample")
	if code != http.StatusCreated {
		t.Fatalf("trial retry: %d %s", code, raw)
	}
	var retry struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &retry)
	if retry.ID == "" || retry.ID == cancelID {
		t.Fatalf("trial retry did not create a distinct Run: %s", raw)
	}
	rec := awaitWorkflowService(t, h.mgr, retry.ID, func(r workflow.RunRecord) bool { return r.Nodes["review"].Status == workflow.NodeWaitingHuman })
	if !rec.TestRun || rec.DefinitionHash != hash || rec.DraftRevision != wf.Revision || rec.Inputs["case_id"] != "CANCEL" {
		t.Fatalf("retry lost draft provenance: %+v", rec)
	}
	code, raw = workflowServiceRequest(t, h.srv.URL, bobToken, http.MethodPost, "/api/workflows/"+wf.ID+"/runs/"+retry.ID+"/nodes/review/resume", `{"action":"approve"}`, "")
	if code != http.StatusOK {
		t.Fatalf("retry review: %d %s", code, raw)
	}
	awaitWorkflowService(t, h.mgr, retry.ID, func(r workflow.RunRecord) bool { return r.Status == workflow.RunCompleted })
}

func TestWorkflowDraftTrialRejectsUnsupportedExecutionWithoutAdmission(t *testing.T) {
	var calls atomic.Int32
	h := newWorkflowHarness(t, func(context.Context, string, string) (llm.Response, error) {
		calls.Add(1)
		return textTurn("unused"), nil
	})
	a := createStudioAgent(t, h, "需要命令审批", "work", "ready")
	wf := createWorkflow(t, h, map[string]any{"name": "不支持的试跑能力", "nodes": []map[string]any{{"id": "work", "agent": a.ID, "task": "run command", "tools": []string{"bash"}}}})
	trialStart(t, h.srv.URL, h.token, wf, trialHash(t, h.mgr, wf), "unsupported", nil, http.StatusBadRequest)
	rows, err := taskRunsDB(t, h.mgr).ListWorkflowRuns(taskqueue.WorkflowRunFilter{WorkflowID: wf.ID})
	if err != nil || len(rows) != 0 || calls.Load() != 0 {
		t.Fatalf("unsupported trial was admitted or ran: %+v calls=%d %v", rows, calls.Load(), err)
	}
}

func TestWorkflowDraftTrialQueuedExecutionRechecksEditPermission(t *testing.T) {
	var calls atomic.Int32
	h := newMultiuserHarness(t, func(int) (llm.Response, error) {
		calls.Add(1)
		return textTurn("must not execute"), nil
	})
	alice := identity.LocalPrincipal(identity.LocalScope("alice"), "alice", false)
	if err := taskRunsDB(t, h.mgr).SetAccessRole(alice.ID, "builder"); err != nil {
		t.Fatal(err)
	}
	empty := []string{}
	a, err := h.mgr.studio.put("", studioInput{Name: "撤权后不能试跑", Instructions: "no tools", Model: "k3", Status: "ready", Tools: &empty}, 0)
	if err != nil {
		t.Fatal(err)
	}
	wf, err := h.mgr.workflows.put("", workflowInput{Name: "排队后撤编辑权限", Nodes: []workflow.Node{{ID: "work", Agent: a.ID, Task: "reply once", Tools: &empty}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	release := taskRunsHoldSlots(t, h.mgr)
	defer release()
	rid := trialStart(t, h.srv.URL, aliceToken, wf, trialHash(t, h.mgr, wf), "revoke-queued-test", nil, http.StatusCreated)
	if err := taskRunsDB(t, h.mgr).SetAccessRole(alice.ID, "user"); err != nil {
		t.Fatal(err)
	}
	release()
	rec := awaitWorkflowService(t, h.mgr, rid, func(r workflow.RunRecord) bool { return workflow.IsTerminal(r.Status) })
	if rec.Status != workflow.RunFailed || !rec.TestRun || calls.Load() != 0 {
		t.Fatalf("queued test bypassed revoked edit permission: %+v calls=%d", rec, calls.Load())
	}
}

func TestWorkflowDraftTrialLaterNodeRechecksCapturedAgentCapabilities(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var first sync.Once
	var lateCalls atomic.Int32
	h := newWorkflowHarness(t, func(ctx context.Context, system, _ string) (llm.Response, error) {
		if strings.Contains(system, "等待能力变更") {
			first.Do(func() { close(entered) })
			select {
			case <-release:
			case <-ctx.Done():
				return llm.Response{}, ctx.Err()
			}
		} else {
			lateCalls.Add(1)
		}
		return textTurn("done"), nil
	})
	empty := []string{}
	a, err := h.mgr.studio.put("", studioInput{Name: "等待能力变更", Instructions: "reply", Model: "k3", Status: "ready", Tools: &empty}, 0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := h.mgr.studio.put("", studioInput{Name: "后续能力节点", Instructions: "reply", Model: "k3", Status: "ready", Tools: &empty}, 0)
	if err != nil {
		t.Fatal(err)
	}
	wf := createWorkflow(t, h, map[string]any{"name": "执行中能力变化", "nodes": []map[string]any{
		{"id": "first", "agent": a.ID, "task": "wait", "tools": []string{}},
		{"id": "later", "agent": b.ID, "task": "reply", "after": []string{"first"}},
	}})
	rid := trialStart(t, h.srv.URL, h.token, wf, trialHash(t, h.mgr, wf), "change-later-agent", nil, http.StatusCreated)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first test node did not enter its model")
	}
	changed := b.studioInput
	bash := []string{"bash"}
	changed.Tools = &bash
	if _, err := h.mgr.studio.put(b.ID, changed, b.Revision); err != nil {
		t.Fatal(err)
	}
	close(release)
	rec := awaitWorkflowService(t, h.mgr, rid, func(r workflow.RunRecord) bool { return workflow.IsTerminal(r.Status) })
	if rec.Status != workflow.RunFailed || rec.Nodes["later"].Status != workflow.NodeFailed || lateCalls.Load() != 0 {
		t.Fatalf("later node used broadened capabilities: %+v late_calls=%d", rec, lateCalls.Load())
	}
}
