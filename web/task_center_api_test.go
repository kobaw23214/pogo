package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/schedule"
	"github.com/yosukeno/pogo/sentinel"
	"github.com/yosukeno/pogo/taskqueue"
)

// Do not start a model or a second dispatcher: API tests control admission
// transitions directly, using the same durable store as the real runtime.
func taskCenterTestQueue(t *testing.T, m *Manager) *taskqueue.Store {
	t.Helper()
	if m.taskRuns != nil {
		m.taskRuns.stop()
		<-m.taskRuns.done
	}
	r := newTaskRunRuntime(m)
	db, err := taskqueue.Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	r.store = db
	m.taskRuns = r
	t.Cleanup(func() { r.stop(); _ = db.Close() })
	return db
}

func taskCenterTestFront(t *testing.T, s *Server) *httptest.Server {
	t.Helper()
	s.mux = http.NewServeMux()
	s.registerTaskCenterRoutes()
	front := httptest.NewServer(s.Handler())
	t.Cleanup(front.Close)
	return front
}

func taskCenterRequest(t *testing.T, front *httptest.Server, token, method, path, body string) (int, map[string]any, string) {
	t.Helper()
	req, err := http.NewRequest(method, front.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := front.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return response.StatusCode, out, string(raw)
}

func taskCenterAddSchedule(t *testing.T, s *Server, id, agent, wid string, owner *identity.Scope) schedule.Job {
	t.Helper()
	j := schedule.Job{ID: id, Title: id + " title", Prompt: "Test background responsibility", AgentID: agent, WorkflowID: wid, Owner: owner, Workdir: t.TempDir(), Kind: schedule.KindEvery, EverySec: 3600, CreatedAt: time.Now(), Timezone: "UTC", Enabled: true}
	if err := s.scheduleStore().Add(j); err != nil {
		t.Fatal(err)
	}
	j, err := s.scheduleStore().Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func taskCenterEnqueue(t *testing.T, db *taskqueue.Store, j schedule.Job, trigger string) taskqueue.Run {
	t.Helper()
	run, _, err := db.Enqueue(taskqueue.Request{TaskID: "schedule:" + j.ID, Source: "schedule", SourceID: j.ID, Trigger: "manual", TriggerID: trigger, DefinitionHash: j.DefHash, OwnerID: sessionResultOwnerKey(j.Owner), AgentID: j.AgentID, WorkflowID: j.WorkflowID, Title: j.Title, DueAt: 0, Payload: json.RawMessage(`{"private_payload":"SYNTHETIC_QUEUE_SECRET"}`)})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func taskCenterIDs(t *testing.T, out map[string]any, field string) map[string]map[string]any {
	t.Helper()
	values, ok := out[field].([]any)
	if !ok {
		t.Fatalf("%s is not an array: %v", field, out)
	}
	rows := map[string]map[string]any{}
	for _, value := range values {
		row := value.(map[string]any)
		rows[row["id"].(string)] = row
	}
	return rows
}

func TestTaskCenterOwnerAgentAndLegacyScopes(t *testing.T) {
	scheduleEnv(t)
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { t.Error("task center contacted a model"); return llm.Response{}, nil })
	db := taskCenterTestQueue(t, h.mgr)
	front := taskCenterTestFront(t, h.psrv)
	alice, bob := identity.LocalScope("alice"), identity.LocalScope("bob")
	a := taskCenterAddSchedule(t, h.psrv, "alice-a", "agent-a", "", &alice)
	taskCenterAddSchedule(t, h.psrv, "alice-b", "agent-b", "", &alice)
	b := taskCenterAddSchedule(t, h.psrv, "bob-a", "agent-a", "", &bob)
	taskCenterAddSchedule(t, h.psrv, "legacy", "", "", nil)
	ar, br := taskCenterEnqueue(t, db, a, "a"), taskCenterEnqueue(t, db, b, "b")
	for _, owner := range []*identity.Scope{&alice, &bob} {
		watchDB, err := h.mgr.sentinels.database(true)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(owner)
		_, err = watchDB.Create(sentinel.Spec{ID: "watch-" + owner.SubjectID, OwnerID: sessionResultOwnerKey(owner), Owner: encoded, AgentID: "agent-a", Workspace: t.TempDir(), ScriptDir: t.TempDir(), Command: []string{"/bin/false"}, Title: "Owned condition", Task: "Observe changes", Mode: "poll", IntervalSeconds: 60, TimeoutSeconds: 10, MaxTurns: 5})
		if err != nil {
			t.Fatal(err)
		}
	}
	forged := br.Request
	forged.TaskID, forged.SourceID, forged.TriggerID = "schedule:"+a.ID, a.ID, "wrong-owner-binding"
	forgedRun, _, err := db.Enqueue(forged)
	if err != nil {
		t.Fatal(err)
	}
	code, out, raw := taskCenterRequest(t, front, aliceToken, "GET", "/api/tasks?agent_id=agent-a", "")
	tasks, runs := taskCenterIDs(t, out, "tasks"), taskCenterIDs(t, out, "runs")
	if code != 200 || len(tasks) != 2 || tasks["schedule:alice-a"] == nil || tasks["sentinel:watch-"+alice.SubjectID] == nil || len(runs) != 1 || runs[ar.ID] == nil || runs[br.ID] != nil {
		t.Fatalf("owner/agent filter failed: %d %s", code, raw)
	}
	for _, secret := range []string{"SYNTHETIC_QUEUE_SECRET", "owner_id", "subject_id", "private_payload", "definition_hash", "payload"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("private execution envelope exposed: %s", secret)
		}
	}
	if code, _, raw = taskCenterRequest(t, front, aliceToken, "GET", "/api/tasks?agent_id=agent-c", ""); code != 200 || !strings.Contains(raw, `"tasks":[]`) || !strings.Contains(raw, `"runs":[]`) {
		t.Fatalf("empty Agent scope leaked other tasks: %d %s", code, raw)
	}
	for _, path := range []string{"/api/tasks/schedule/bob-a/pause", "/api/tasks/schedule/legacy/pause", "/api/tasks/sentinel/watch-" + bob.SubjectID + "/pause", "/api/task-runs/" + br.ID + "/cancel", "/api/task-runs/" + forgedRun.ID + "/cancel", "/api/task-runs/" + ar.ID + "/cancel?agent_id=agent-b"} {
		if code, _, raw = taskCenterRequest(t, front, aliceToken, "POST", path, `{"revision":1}`); code != 404 {
			t.Fatalf("cross-scope control %s: %d %s", path, code, raw)
		}
	}
	// Admin access keeps legacy definitions visible, without gaining
	// another account's owner-bound Agent tasks.
	h.psrv.auth.mu.Lock()
	u := h.psrv.auth.users["alice"]
	u.Admin = true
	h.psrv.auth.users["alice"] = u
	h.psrv.auth.mu.Unlock()
	code, out, raw = taskCenterRequest(t, front, aliceToken, "GET", "/api/tasks", "")
	tasks = taskCenterIDs(t, out, "tasks")
	if code != 200 || len(tasks) != 4 || tasks["schedule:legacy"]["executor"] != "legacy" || tasks["schedule:bob-a"] != nil {
		t.Fatalf("admin definition boundary failed: %d %s", code, raw)
	}
}

func TestTaskCenterSentinelProjectionAndResumePermissions(t *testing.T) {
	scheduleEnv(t)
	m := sweepManager(t, 0)
	s, err := NewServer(m, ServerOptions{Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	taskCenterTestQueue(t, m)
	front := taskCenterTestFront(t, s)
	db, err := m.sentinels.database(true)
	if err != nil {
		t.Fatal(err)
	}
	w, err := db.Create(sentinel.Spec{ID: "probe", OwnerID: sessionResultOwnerKey(nil), AgentID: "agent-a", Workspace: t.TempDir(), ScriptDir: t.TempDir(), Command: []string{"/bin/false", "SYNTHETIC_COLLECTOR_SECRET"}, Title: "Condition", Task: "Observe meaningful changes", Mode: "poll", IntervalSeconds: 60, TimeoutSeconds: 10, MaxTurns: 5, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Accept(w.Spec.ID, w.Spec.Generation, sentinel.Frame{V: 1, ID: "first", Events: []sentinel.InputEvent{{Key: "one", Summary: "changed"}}, State: json.RawMessage(`{"private_state":"SYNTHETIC_COLLECTOR_SECRET"}`), Cursor: json.RawMessage(`"SYNTHETIC_COLLECTOR_SECRET"`)}); err != nil {
		t.Fatal(err)
	}
	code, out, raw := taskCenterRequest(t, front, "test-token", "GET", "/api/tasks?agent_id=agent-a", "")
	row := taskCenterIDs(t, out, "tasks")["sentinel:probe"]
	if code != 200 || row == nil || row["trigger"] != "condition" || row["running_count"] != float64(0) || row["pending_events"] != float64(1) || strings.Contains(raw, "SYNTHETIC_COLLECTOR_SECRET") || strings.Contains(raw, `"cursor"`) || strings.Contains(raw, `"command"`) {
		t.Fatalf("unsafe or misleading sentinel projection: %d %s", code, raw)
	}
	if code, _, raw = taskCenterRequest(t, front, "test-token", "POST", "/api/tasks/sentinel/probe/pause", ""); code != 200 {
		t.Fatalf("pause: %d %s", code, raw)
	}
	w, err = db.Get(w.Spec.ID)
	if err != nil || w.Spec.Enabled || w.Pending != 1 {
		t.Fatalf("pause lost pending observations: %+v %v", w, err)
	}
	if code, _, raw = taskCenterRequest(t, front, "test-token", "POST", "/api/tasks/sentinel/probe/resume", ""); code != 409 {
		t.Fatalf("resume bypassed missing Agent/tool authority: %d %s", code, raw)
	}
	if w, err = db.Get(w.Spec.ID); err != nil || w.Spec.Enabled {
		t.Fatal("failed resume changed enabled state")
	}
}

func TestTaskCenterRunControlRevisionAndRetry(t *testing.T) {
	scheduleEnv(t)
	m := sweepManager(t, 0)
	s, err := NewServer(m, ServerOptions{Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	db := taskCenterTestQueue(t, m)
	front := taskCenterTestFront(t, s)
	j := taskCenterAddSchedule(t, s, "cancel-probe", "agent-a", "", nil)
	run := taskCenterEnqueue(t, db, j, "cancel")
	base := "/api/task-runs/" + run.ID
	for _, body := range []string{"", `{}`, `{"revision":0}`, `{"revision":1,"owner_id":"forged"}`, `{"revision":1} {}`} {
		if code, _, raw := taskCenterRequest(t, front, "test-token", "POST", base+"/cancel", body); code != 400 {
			t.Fatalf("invalid control %q: %d %s", body, code, raw)
		}
	}
	if code, _, raw := taskCenterRequest(t, front, "test-token", "POST", base+"/cancel", `{"revision":999}`); code != 409 {
		t.Fatalf("stale cancel: %d %s", code, raw)
	}
	if code, _, raw := taskCenterRequest(t, front, "test-token", "POST", base+"/cancel", `{"revision":1}`); code != 200 {
		t.Fatalf("cancel: %d %s", code, raw)
	}
	if got, err := db.Get(run.ID); err != nil || got.Status != taskqueue.Cancelled {
		t.Fatalf("cancel not persisted: %+v %v", got, err)
	}
	run = taskCenterEnqueue(t, db, j, "failed")
	run, err = db.Claim(run.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	run, err = db.Finish(run.ID, run.Revision, taskqueue.Outcome{Status: taskqueue.Failed, Error: "controlled failure"})
	if err != nil {
		t.Fatal(err)
	}
	base = "/api/task-runs/" + run.ID + "/retry"
	body := studioBody(map[string]any{"revision": run.Revision})
	if code, _, raw := taskCenterRequest(t, front, "test-token", "POST", base, `{"revision":999}`); code != 409 {
		t.Fatalf("stale retry: %d %s", code, raw)
	}
	code, out, raw := taskCenterRequest(t, front, "test-token", "POST", base, body)
	if code != 200 {
		t.Fatalf("retry: %d %s", code, raw)
	}
	next := out["run"].(map[string]any)
	code, out, raw = taskCenterRequest(t, front, "test-token", "POST", base, body)
	if code != 200 || out["run"].(map[string]any)["id"] != next["id"] || strings.Contains(raw, "SYNTHETIC_QUEUE_SECRET") {
		t.Fatalf("retry was not idempotent/private: %d %s", code, raw)
	}
	for _, path := range []string{"/api/task-runs/unknown/cancel", "/api/tasks/unknown/id/pause", "/api/tasks/schedule/unknown/resume", "/api/tasks/sentinel/unknown/pause"} {
		if code, _, raw := taskCenterRequest(t, front, "test-token", "POST", path, `{"revision":1}`); code != 404 {
			t.Fatalf("unknown control %s: %d %s", path, code, raw)
		}
	}
}

func TestTaskCenterPauseDoesNotCancelCurrentRun(t *testing.T) {
	scheduleEnv(t)
	m := sweepManager(t, 0)
	s, err := NewServer(m, ServerOptions{Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	db := taskCenterTestQueue(t, m)
	front := taskCenterTestFront(t, s)
	j := taskCenterAddSchedule(t, s, "pause-probe", "agent-a", "", nil)
	run := taskCenterEnqueue(t, db, j, "active")
	run, err = db.Claim(run.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if code, _, raw := taskCenterRequest(t, front, "test-token", "POST", "/api/tasks/schedule/pause-probe/pause", ""); code != 200 {
		t.Fatalf("pause: %d %s", code, raw)
	}
	got, err := db.Get(run.ID)
	if err != nil || got.Status != taskqueue.Running || got.CancelRequested {
		t.Fatalf("definition pause cancelled current execution: %+v %v", got, err)
	}
}

func TestTaskCenterListErrorsStayVisible(t *testing.T) {
	dir := scheduleEnv(t)
	m := sweepManager(t, 0)
	s, err := NewServer(m, ServerOptions{Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	taskCenterTestQueue(t, m)
	front := taskCenterTestFront(t, s)
	taskCenterAddSchedule(t, s, "visible", "agent-a", "", nil)
	m.taskRuns.mu.Lock()
	m.taskRuns.err = errors.New("controlled queue failure")
	m.taskRuns.mu.Unlock()
	code, out, raw := taskCenterRequest(t, front, "test-token", "GET", "/api/tasks", "")
	if code != 200 || len(taskCenterIDs(t, out, "tasks")) != 1 || out["queue_error"] == nil {
		t.Fatalf("queue failure pretended to be empty success: %d %s", code, raw)
	}
	if err := os.WriteFile(filepath.Join(dir, "jobs.json"), []byte("broken JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	if code, _, raw := taskCenterRequest(t, front, "test-token", "GET", "/api/tasks", ""); code != 500 {
		t.Fatalf("definition load failure pretended to be empty success: %d %s", code, raw)
	}
}

func TestTaskCenterCompletedOneShotIsNotAnUnfinishedPause(t *testing.T) {
	m := sweepManager(t, 0)
	s, err := NewServer(m, ServerOptions{Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	job := schedule.Job{ID: "finished-once", Kind: schedule.KindAt, At: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), LastRunStatus: "ok", Enabled: false}
	if view := s.taskCenterScheduleView(job); view.State != "completed" || view.Enabled || view.Attention {
		t.Fatalf("successful disabled one-shot looked paused: %+v", view)
	}
	job.LastRunStatus = ""
	if view := s.taskCenterScheduleView(job); view.State != "paused" {
		t.Fatalf("unfinished disabled one-shot looked complete: %+v", view)
	}
}

func TestTaskCenterDeletedAndReboundDefinitionsKeepOnlyOwnersReadOnlyHistory(t *testing.T) {
	scheduleEnv(t)
	h := newMultiuserHarness(t, func(int) (llm.Response, error) {
		t.Error("history listing contacted a model")
		return llm.Response{}, nil
	})
	db := taskCenterTestQueue(t, h.mgr)
	front := taskCenterTestFront(t, h.psrv)
	alice, bob := identity.LocalScope("alice"), identity.LocalScope("bob")
	j := taskCenterAddSchedule(t, h.psrv, "retained", "agent-a", "", &alice)
	run := taskCenterEnqueue(t, db, j, "old-owned-run")
	if err := h.psrv.scheduleStore().Remove(j.ID); err != nil {
		t.Fatal(err)
	}
	code, out, raw := taskCenterRequest(t, front, aliceToken, "GET", "/api/tasks?agent_id=agent-a", "")
	runs := taskCenterIDs(t, out, "runs")
	if code != 200 || len(taskCenterIDs(t, out, "tasks")) != 0 || len(runs) != 1 || runs[run.ID]["can_cancel"] != false || runs[run.ID]["can_retry"] != false {
		t.Fatalf("deleted definition hid history or retained controls: %d %s", code, raw)
	}
	if code, _, raw = taskCenterRequest(t, front, aliceToken, "POST", "/api/task-runs/"+run.ID+"/cancel", `{"revision":1}`); code != 404 {
		t.Fatalf("deleted source retained execution authority: %d %s", code, raw)
	}
	// An operator replacing jobs.json cannot transfer an old admission's owner.
	// Normal schedule creation also reserves deleted IDs; this models a store
	// restored from another definition version using the same source identity.
	j.Owner, j.Title, j.Prompt = &bob, "NEW_OWNER_PRIVATE_TITLE", "NEW_OWNER_PRIVATE_DESCRIPTION"
	if err := os.WriteFile(filepath.Join(schedule.DefaultDir(), "jobs.json"), []byte(studioBody([]schedule.Job{j})), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, raw = taskCenterRequest(t, front, aliceToken, "GET", "/api/tasks?agent_id=agent-a", "")
	runs = taskCenterIDs(t, out, "runs")
	if code != 200 || len(taskCenterIDs(t, out, "tasks")) != 0 || runs[run.ID] == nil || runs[run.ID]["can_cancel"] != false || strings.Contains(raw, "NEW_OWNER_PRIVATE") {
		t.Fatalf("old history borrowed the new source owner's fields: %d %s", code, raw)
	}
	code, out, raw = taskCenterRequest(t, front, bobToken, "GET", "/api/tasks?agent_id=agent-a", "")
	if code != 200 || len(taskCenterIDs(t, out, "tasks")) != 1 || len(taskCenterIDs(t, out, "runs")) != 0 || strings.Contains(raw, run.ID) {
		t.Fatalf("new source owner inherited the old run: %d %s", code, raw)
	}
	if code, _, raw = taskCenterRequest(t, front, bobToken, "POST", "/api/task-runs/"+run.ID+"/cancel", `{"revision":1}`); code != 404 {
		t.Fatalf("new source owner controlled the old run: %d %s", code, raw)
	}
}

func TestTaskCenterRunLinksRequireOriginalSessionBinding(t *testing.T) {
	scheduleEnv(t)
	h, _ := newStudioHarness(t, nil)
	a := createStudioAgent(t, h, "Bound Agent", "No execution expected", "ready")
	sess := h.session(startStudioConversation(t, h, a.ID))
	s, err := NewServer(h.mgr, ServerOptions{Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	run := taskqueue.Run{Request: taskqueue.Request{AgentID: a.ID, OwnerID: sessionResultOwnerKey(sess.owner)}, SessionID: sess.ID, Status: taskqueue.Completed}
	if v := s.taskCenterRunView(run); v.SessionID != sess.ID || v.ResultURL == "" {
		t.Fatalf("original retained session link was lost: %+v", v)
	}
	run.OwnerID = "another-owner"
	if v := s.taskCenterRunView(run); v.SessionID != "" || v.ResultURL != "" {
		t.Fatalf("foreign session link was exposed: %+v", v)
	}
	run.OwnerID, run.AgentID = sessionResultOwnerKey(sess.owner), "another-agent"
	if v := s.taskCenterRunView(run); v.SessionID != "" || v.ResultURL != "" {
		t.Fatalf("another Agent's session link was exposed: %+v", v)
	}
}

func TestTaskCenterCancelledScheduleClearsOnlyCancellationErrorAttention(t *testing.T) {
	scheduleEnv(t)
	h, _ := newStudioHarness(t, nil)
	a := createStudioAgent(t, h, "Background Agent", "No model execution expected", "ready")
	db := taskCenterTestQueue(t, h.mgr)
	s, err := NewServer(h.mgr, ServerOptions{Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	front := taskCenterTestFront(t, s)
	for _, kind := range []string{"ordinary", "unavailable", "auto-paused", "expired"} {
		aid, wid := a.ID, ""
		if kind == "unavailable" {
			aid = "missing-agent"
		}
		j := taskCenterAddSchedule(t, s, "cancel-"+kind, aid, wid, nil)
		if err := s.scheduleStore().Update(j.ID, func(saved *schedule.Job) {
			saved.LastRunStatus, saved.LastError = "error", "context canceled"
			if kind == "auto-paused" {
				saved.Enabled, saved.ConsecutiveFailures = false, 10
			}
			if kind == "expired" {
				saved.ExpiresAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
			}
		}); err != nil {
			t.Fatal(err)
		}

		run := taskCenterEnqueue(t, db, j, "user-cancel")
		run, err = db.Claim(run.ID, 2)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Cancel(run.ID, run.Revision); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Finish(run.ID, run.Revision, taskqueue.Outcome{Status: taskqueue.Cancelled, EndReason: "cancelled"}); err != nil {
			t.Fatal(err)
		}
	}
	code, out, raw := taskCenterRequest(t, front, "test-token", "GET", "/api/tasks", "")
	if code != 200 {
		t.Fatalf("list cancellation projections: %d %s", code, raw)
	}
	tasks := taskCenterIDs(t, out, "tasks")
	if got := tasks["schedule:cancel-ordinary"]; got["attention"] != false || got["state"] != "scheduled" {
		t.Fatalf("ordinary user cancellation raised an error: %v", got)
	}
	for _, kind := range []string{"unavailable", "auto-paused"} {
		if got := tasks["schedule:cancel-"+kind]; got["attention"] != true {
			t.Fatalf("cancellation hid %s attention: %v", kind, got)
		}
	}
	for kind, expected := range map[string]string{"unavailable": "unavailable", "auto-paused": "autoPaused", "expired": "expired"} {
		if got := tasks["schedule:cancel-"+kind]; got["state"] != expected {
			t.Fatalf("cancellation changed %s definition state: %v", kind, got)
		}
	}
	j, err := s.scheduleStore().Find("cancel-ordinary")
	if err != nil || j.LastRunStatus != "error" || j.LastError != "context canceled" {
		t.Fatalf("UI projection changed original scheduler history: %+v %v", j, err)
	}
}

func TestTaskCenterCancelledSentinelRetriesOnlyItsInterruptedNativeBatch(t *testing.T) {
	scheduleEnv(t)
	h, capture := newSentinelHarness(t, nil)
	sess := sentinelTestSession(t, h)
	w := sentinelTestCreate(t, h, sess)
	queue := taskCenterTestQueue(t, h.mgr)
	s, err := NewServer(h.mgr, ServerOptions{Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	front := taskCenterTestFront(t, s)
	native, err := h.mgr.sentinels.database(true)
	if err != nil {
		t.Fatal(err)
	}
	prepareBatch := func(n int) sentinel.Batch {
		t.Helper()
		// Freeze collection while this fixture creates an admission; no source
		// process or model is started by this management projection test.
		h.mgr.sentinels.mu.Lock()
		defer h.mgr.sentinels.mu.Unlock()
		var e error
		w, e = native.SetEnabled(w.Spec.ID, true)
		if e != nil {
			t.Fatal(e)
		}
		if e = native.Accept(w.Spec.ID, w.Spec.Generation, sentinel.Frame{V: 1, ID: fmt.Sprintf("frame-%d", n), Events: []sentinel.InputEvent{{Key: fmt.Sprintf("event-%d", n), Summary: "Changed"}}}); e != nil {
			t.Fatal(e)
		}
		batch, e := native.ClaimBatch(w.Spec.ID, 1)
		if e != nil || batch == nil {
			t.Fatalf("create native batch: %+v %v", batch, e)
		}
		w, e = native.SetEnabled(w.Spec.ID, false)
		if e != nil {
			t.Fatal(e)
		}
		return *batch
	}
	first := prepareBatch(1)
	if err := native.SetBatchRun(first.ID, sess.ID, "native-active-run", "running"); err != nil {
		t.Fatal(err)
	}
	if err := native.InterruptBatch(first.ID, "User cancelled active handling; outcome requires review"); err != nil {
		t.Fatal(err)
	}
	run, _, err := h.mgr.taskRuns.enqueueSentinel(w, first, "")
	if err != nil {
		t.Fatal(err)
	}
	run, err = queue.Claim(run.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Cancel(run.ID, run.Revision); err != nil {
		t.Fatal(err)
	}
	run, err = queue.Finish(run.ID, run.Revision, taskqueue.Outcome{Status: taskqueue.Cancelled, SessionID: sess.ID, EndReason: "cancelled"})
	if err != nil {
		t.Fatal(err)
	}
	code, out, raw := taskCenterRequest(t, front, "test-token", "GET", "/api/tasks?agent_id="+sess.studioID, "")
	if code != 200 || taskCenterIDs(t, out, "runs")[run.ID]["can_retry"] != true || taskCenterIDs(t, out, "tasks")[run.TaskID]["attention"] != true {
		t.Fatalf("active sentinel cancellation lost review/retry signal: %d %s", code, raw)
	}
	wrong := run
	wrong.Payload = json.RawMessage(studioBody(sentinelTaskPayload{BatchID: first.ID, Attempt: first.Attempt + 1}))
	if view := s.taskCenterRunView(wrong); view.CanRetry {
		t.Fatal("another batch attempt granted retry to the cancelled run")
	}
	wrong = run
	wrong.TriggerID = "another-trigger"
	if view := s.taskCenterRunView(wrong); view.CanRetry {
		t.Fatal("unrelated admission trigger granted retry to the cancelled run")
	}
	if err := native.SettleBatch(first.ID, "ignore", "Prior interrupted outcome explicitly checked", 0); err != nil {
		t.Fatal(err)
	}
	second := prepareBatch(2)
	queued, _, err := h.mgr.taskRuns.enqueueSentinel(w, second, "")
	if err != nil {
		t.Fatal(err)
	}
	queued, err = h.mgr.taskRuns.cancel(queued.ID, queued.Revision)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := native.GetBatch(second.ID)
	if err != nil || batch.Status != "completed" || batch.Decision != "ignore" {
		t.Fatalf("unstarted cancellation was not settled: %+v %v", batch, err)
	}
	code, out, raw = taskCenterRequest(t, front, "test-token", "GET", "/api/tasks?agent_id="+sess.studioID, "")
	if code != 200 || taskCenterIDs(t, out, "runs")[queued.ID]["can_retry"] != false || taskCenterIDs(t, out, "runs")[run.ID]["can_retry"] != false || taskCenterIDs(t, out, "tasks")[run.TaskID]["attention"] != false {
		t.Fatalf("settled cancellation retained interruption/retry signal: %d %s", code, raw)
	}
	if sentinelModelCalls(capture) != 0 {
		t.Fatal("management projection started a model")
	}
}
