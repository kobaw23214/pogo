package web

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/schedule"
	"github.com/yosukeno/pogo/sentinel"
	"github.com/yosukeno/pogo/tools"
)

func backgroundTestRequest(t *testing.T, backend tools.BackgroundTaskBackend, body map[string]any) tools.BackgroundTaskReceipt {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r, err := backend.Task(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func backgroundScheduleBody() map[string]any {
	return map[string]any{"action": "create", "source": "schedule", "title": "目录统计", "task": "统计当前测试目录文件数并把每次结果保存到 Pogo；即使相同也保存这次采样。", "kind": "every", "every_seconds": 120, "timezone": "Asia/Shanghai", "stop_after_seconds": 600}
}

func TestBackgroundTaskScheduleUsesDurableBoundedHostDefinitionAndHandoff(t *testing.T) {
	dir := scheduleEnv(t)
	h, cap := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	s.SetPolicy(ModeAuto, 1)
	body := backgroundScheduleBody()
	raw := json.RawMessage(studioBody(body))
	tool := &tools.BackgroundTask{Backend: h.mgr.backgroundTaskBackend(s)}
	result, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	handoff := result.Details.(map[string]any)["background_handoff"].(map[string]any)
	id := strings.TrimPrefix(handoff["task_id"].(string), "schedule:")
	job, err := schedule.NewStore(dir).Find(id)
	if err != nil {
		t.Fatal(err)
	}
	end, err := time.Parse(time.RFC3339, job.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if job.AgentID != s.studioID || job.Workdir != s.cwd || sessionResultOwnerKey(job.Owner) != sessionResultOwnerKey(s.owner) || job.Enabled || job.Model != "" || job.ApprovalMode != "standard" || job.TurnLimitMode != schedule.TurnLimitLimited || job.MaxTurns <= 0 || job.MaxTurns > 32 || job.EverySec != 120 || end.Sub(job.CreatedAt) != 600*time.Second {
		t.Fatalf("unsafe or non-durable schedule: %+v", job)
	}
	if job.ScheduleFrom == nil || !job.ScheduleFrom.Equal(job.CreatedAt) || job.DefHash != schedule.DefinitionHash(job) {
		t.Fatal("definition timing/hash was not sealed")
	}
	if handoff["registered"] != true || handoff["enabled"] != false || handoff["verification"] != "registration_only" || !strings.HasPrefix(result.Text, "Background delegation registered and paused") {
		t.Fatalf("misleading handoff: %v %s", handoff, result.Text)
	}
	if sentinelModelCalls(cap) != 0 || len(h.mgr.SchedRunning()) != 0 {
		t.Fatal("registration started a foreground or model run")
	}
	// Loading through a fresh Store proves the deadline is saved, not a chat timer.
	again, err := schedule.NewStore(dir).Find(id)
	if err != nil || again.ExpiresAt != job.ExpiresAt {
		t.Fatal("deadline lost across store reload", err)
	}
	backend := h.mgr.backgroundTaskBackend(s)
	resumed := backgroundTestRequest(t, backend, map[string]any{"action": "resume", "source": "schedule", "task_id": id})
	if !resumed.Enabled || resumed.ExpiresAt != job.ExpiresAt || resumed.NextRun == "" {
		t.Fatalf("resume altered deadline or lost timing: %+v", resumed)
	}
	if got := resumed.Result.(map[string]any)["remaining_planned_triggers_before_deadline"]; got != int64(4) {
		t.Fatalf("exclusive cutoff must expose four planned triggers, got %v", got)
	}
	job, _ = schedule.NewStore(dir).Find(id)
	listed := backgroundTestRequest(t, backend, map[string]any{"action": "list", "source": "schedule"}).Result.([]tools.BackgroundTaskReceipt)
	if len(listed) != 1 || listed[0].TaskID != "schedule:"+id || listed[0].SourceID != id {
		t.Fatal("list IDs cannot round trip", listed)
	}
	status := backgroundTestRequest(t, backend, map[string]any{"action": "status", "source": "schedule", "task_id": listed[0].TaskID})
	if status.TaskID != listed[0].TaskID {
		t.Fatal("prefixed status changed ID", status)
	}
	for _, seconds := range []int{120, 240, 360, 480} {
		next := nextFire(job, job.CreatedAt.Add(time.Duration(seconds-1)*time.Second))
		want := job.CreatedAt.Add(time.Duration(seconds) * time.Second)
		if next == nil || !next.Equal(want) {
			t.Fatalf("next at%d=%v want%v", seconds, next, want)
		}
		job.LastQueuedAt = want
	}
	if got := nextFire(job, end); got != nil {
		t.Fatalf("deadline equality admitted new execution: %v", got)
	}
}

func TestBackgroundTaskScheduleLifecycleKeepsHistoryAndRejectsExpiredResume(t *testing.T) {
	dir := scheduleEnv(t)
	h, _ := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	s.SetPolicy(ModeStrict, 0)
	backend := h.mgr.backgroundTaskBackend(s).(*backgroundTaskBackend)
	body := backgroundScheduleBody()
	body["max_turns"] = 3
	created := backgroundTestRequest(t, backend, body)
	st := schedule.NewStore(dir)
	job, _ := st.Find(created.SourceID)
	if job.ApprovalMode != "strict" || job.MaxTurns != 3 {
		t.Fatal("host policy was not retained", job)
	}
	if _, err := backend.scheduleTask(context.Background(), tools.BackgroundTaskRequest{Action: "resume", Source: "schedule", TaskID: job.ID}, job.CreatedAt.Add(600*time.Second)); err == nil {
		t.Fatal("resume renewed an expired lifetime")
	}
	backgroundTestRequest(t, backend, map[string]any{"action": "resume", "source": "schedule", "task_id": job.ID})
	paused := backgroundTestRequest(t, backend, map[string]any{"action": "pause", "source": "schedule", "task_id": job.ID})
	if paused.Enabled {
		t.Fatal("pause retained enabled")
	}
	if err := st.AppendRun(job.ID, schedule.RunRecord{TS: time.Now(), Status: schedule.StatusOK, Summary: "saved sample"}); err != nil {
		t.Fatal(err)
	}
	status := backgroundTestRequest(t, backend, map[string]any{"action": "status", "source": "schedule", "task_id": job.ID})
	encoded, _ := json.Marshal(status)
	if !strings.Contains(string(encoded), "saved sample") {
		t.Fatal("status lost saved run")
	}
	removed := backgroundTestRequest(t, backend, map[string]any{"action": "remove", "source": "schedule", "task_id": job.ID})
	if removed.Registered || removed.Enabled || removed.Status != "removed" {
		t.Fatal("remove receipt claims registered", removed)
	}
	if _, err := st.Find(job.ID); err == nil {
		t.Fatal("definition still present")
	}
	runs, err := st.Runs(job.ID, 10)
	if err != nil || len(runs) != 1 || runs[0].Summary != "saved sample" {
		t.Fatal("remove erased history", runs, err)
	}
}

func TestBackgroundTaskScheduleIsolationAndRevocation(t *testing.T) {
	dir := scheduleEnv(t)
	h, _ := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	backend := h.mgr.backgroundTaskBackend(s)
	created := backgroundTestRequest(t, backend, backgroundScheduleBody())
	other := sentinelTestSession(t, h)
	foreignBackend := h.mgr.backgroundTaskBackend(other)
	for _, action := range []string{"status", "pause", "resume", "remove"} {
		_, err := foreignBackend.Task(context.Background(), json.RawMessage(studioBody(map[string]any{"action": action, "source": "schedule", "task_id": created.TaskID})))
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("other Agent %s exposed task: %v", action, err)
		}
	}
	listed := backgroundTestRequest(t, foreignBackend, map[string]any{"action": "list", "source": "schedule"})
	if len(listed.Result.([]tools.BackgroundTaskReceipt)) != 0 {
		t.Fatal("other Agent list exposed task")
	}
	st := schedule.NewStore(dir)
	foreign, _ := st.Find(created.SourceID)
	foreign.ID += "-foreign"
	owner := identity.LocalScope("another")
	foreign.Owner = &owner
	if err := schedule.AddJob(st, schedule.Internal(), foreign); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Task(context.Background(), json.RawMessage(studioBody(map[string]any{"action": "status", "source": "schedule", "task_id": foreign.ID}))); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign owner task exposed", err)
	}
	a, _ := h.mgr.studio.get(s.studioID)
	patchStudioPolicy(t, h, a, map[string]any{"skills": []string{}, "tools": []string{}})
	if _, err := backend.Task(context.Background(), json.RawMessage(`{"action":"list","source":"schedule"}`)); err == nil {
		t.Fatal("captured backend survived grant revocation")
	}
	if _, err := os.Stat(filepath.Join(dir, "jobs.json")); err != nil {
		t.Fatal("rejected access deleted definitions", err)
	}
}

func TestBackgroundTaskScheduleAtCronAndInvalidInputShareExistingValidation(t *testing.T) {
	dir := scheduleEnv(t)
	h, _ := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	backend := h.mgr.backgroundTaskBackend(s)
	for _, kind := range []string{"at", "cron"} {
		body := backgroundScheduleBody()
		delete(body, "every_seconds")
		delete(body, "stop_after_seconds")
		body["kind"] = kind
		if kind == "at" {
			body["at"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		} else {
			body["cron"] = "*/5 * * * *"
		}
		r := backgroundTestRequest(t, backend, body)
		job, err := schedule.NewStore(dir).Find(r.SourceID)
		if err != nil || job.Kind != kind {
			t.Fatal("kind not persisted", job, err)
		}
	}
	st := schedule.NewStore(dir)
	before, _ := st.Load()
	for _, change := range []map[string]any{{"timezone": "not-a-zone"}, {"kind": "cron", "every_seconds": 0, "cron": "invalid"}, {"task": slashCommands[0]}, {"every_seconds": 600}, {"expires_at": "2099-01-01T00:00:00Z"}, {"approval_mode": "auto"}, {"workdir": "/tmp"}} {
		body := backgroundScheduleBody()
		for k, v := range change {
			body[k] = v
		}
		if _, err := backend.Task(context.Background(), json.RawMessage(studioBody(body))); err == nil {
			t.Errorf("accepted invalid/unsafe input %v", change)
		}
	}
	after, _ := st.Load()
	if len(after) != len(before) {
		t.Fatal("invalid inputs persisted jobs")
	}
}

func TestBackgroundTaskSentinelReusesExistingStoreAndPermissionBoundary(t *testing.T) {
	scheduleEnv(t)
	h, cap := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	a, _ := h.mgr.studio.get(s.studioID)
	patchStudioPolicy(t, h, a, map[string]any{"tools": []string{tools.BackgroundTaskName}})
	script := sentinelTestBundle(t, s)
	backend := h.mgr.backgroundTaskBackend(s)
	r := backgroundTestRequest(t, backend, map[string]any{"action": "create", "source": "sentinel", "title": "脚本变化", "task": "观察变化并报告", "script_path": script, "mode": "poll", "interval_seconds": 120, "stop_after_seconds": 600})
	db, err := h.mgr.sentinels.database(false)
	if err != nil || db == nil {
		t.Fatal(err)
	}
	w, err := db.Get(r.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	if w.Spec.Enabled || w.Spec.Workspace != s.cwd || w.Spec.AgentID != s.studioID || w.Spec.IntervalSeconds != 120 || w.Spec.ExpiresAt-w.Spec.CreatedAt < 599000 || w.Spec.ExpiresAt-w.Spec.CreatedAt > 601000 {
		t.Fatal("sentinel bypassed existing lifecycle", w.Spec)
	}
	if !r.Registered || r.Enabled || r.Verification != "registration_only" {
		t.Fatal("sentinel receipt incorrect", r)
	}
	status := backgroundTestRequest(t, backend, map[string]any{"action": "status", "source": "sentinel", "task_id": r.TaskID})
	if status.TaskID != "sentinel:"+w.Spec.ID {
		t.Fatal("status did not use original watch")
	}
	listed := backgroundTestRequest(t, backend, map[string]any{"action": "list", "source": "sentinel"}).Result.([]tools.BackgroundTaskReceipt)
	if len(listed) != 1 || listed[0].TaskID != r.TaskID {
		t.Fatal("sentinel list ID differs", listed)
	}
	paused := backgroundTestRequest(t, backend, map[string]any{"action": "pause", "source": "sentinel", "task_id": listed[0].TaskID})
	if paused.TaskID != r.TaskID || paused.Enabled {
		t.Fatal("prefixed sentinel pause failed", paused)
	}
	if _, err := db.Get(w.Spec.ID); errors.Is(err, sentinel.ErrNotFound) {
		t.Fatal("status retired registration")
	}
	// An explicitly retried interrupted batch uses the same watch and keeps
	// a paused collector paused; the receipt must not claim execution finished.
	enabled, err := db.SetEnabled(w.Spec.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Accept(w.Spec.ID, enabled.Spec.Generation, sentinel.Frame{V: 1, ID: "binding-retry-frame", Events: []sentinel.InputEvent{{Key: "retry-key", Summary: "synthetic retry"}}}); err != nil {
		t.Fatal(err)
	}
	batch, err := db.ClaimBatch(w.Spec.ID, 10)
	if err != nil || batch == nil {
		t.Fatalf("prepare interrupted batch: %+v %v", batch, err)
	}
	if err = db.InterruptBatch(batch.ID, "synthetic interrupted operation"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.SetEnabled(w.Spec.ID, false); err != nil {
		t.Fatal(err)
	}
	retried := backgroundTestRequest(t, backend, map[string]any{"action": "retry", "source": "sentinel", "task_id": r.TaskID})
	if retried.TaskID != r.TaskID || retried.SourceID != w.Spec.ID || !retried.Registered || retried.Enabled || retried.Status != "retry_queued" || retried.Verification != "queued" {
		t.Fatalf("retry receipt invented registration, activation or completion: %+v", retried)
	}
	queued, err := db.GetBatch(batch.ID)
	if err != nil || queued.Status != "retry_pending" || queued.Attempt != batch.Attempt+1 {
		t.Fatalf("retry did not preserve original batch: %+v %v", queued, err)
	}
	if sentinelModelCalls(cap) != 0 {
		t.Fatal("paused creation or retry woke model")
	}
}
