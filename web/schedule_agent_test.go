package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/inputref"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/schedule"
	"github.com/yosukeno/pogo/skills"
)

func agentScheduleResponse(t *testing.T, response *http.Response, want int) map[string]any {
	t.Helper()
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != want {
		t.Fatalf("status %d want%d: %s", response.StatusCode, want, raw)
	}
	out := map[string]any{}
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func waitAgentSchedule(t *testing.T, id, dir string) schedule.Job {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		j, err := schedule.NewStore(dir).Find(id)
		if err == nil && !j.LastRunAt.IsZero() {
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("scheduled run did not finish")
	return schedule.Job{}
}
func TestAgentScheduleRunsLiveAgentAndDurableOrderedRefsAfterSourceDeletion(t *testing.T) {
	dir := scheduleEnv(t)
	h, cap := newStudioHarness(t, []llm.Response{textTurn("scheduled output")})
	a := createStudioAgent(t, h, "Task Agent", "OLD-CONTEXT", "ready")
	source := startStudioConversation(t, h, a.ID)
	cwd := h.session(source).cwd
	if err := os.MkdirAll(filepath.Join(cwd, "uploads"), 0700); err != nil {
		t.Fatal(err)
	}
	oldPath := "uploads/input.txt"
	if err := os.WriteFile(filepath.Join(cwd, oldPath), []byte("immutable scheduled input"), 0600); err != nil {
		t.Fatal(err)
	}
	noteSessionUpload(cwd, source, oldPath)
	parts := []inputref.Part{{Type: "text", Text: "first "}, {Type: "shortcut", ShortcutID: "local-card", Title: "nested", Parts: []inputref.Part{{Type: "file", Path: oldPath}, {Type: "text", Text: " after file"}}}, {Type: "text", Text: " last"}}
	body := map[string]any{"agent_id": a.ID, "source_session_id": source, "title": "日报", "prompt": "first last", "parts": parts, "input_layout": "inline", "timezone": "Asia/Shanghai", "kind": "every", "every_sec": 3600, "name": "agent-durable"}
	created := agentScheduleResponse(t, h.do("POST", "/api/schedule/jobs", studioBody(body)), 201)
	view := created["job"].(map[string]any)
	if view["revision"] != float64(1) || view["agent_id"] != a.ID {
		t.Fatalf("created %v", view)
	}
	stored, err := schedule.NewStore(dir).Find("agent-durable")
	if err != nil {
		t.Fatal(err)
	}
	if handle := stored.Parts[1].Parts[0].Path; !strings.HasPrefix(handle, "information-assets/") {
		t.Fatal("attachment was not persisted as immutable asset")
	}
	response := h.do("DELETE", "/api/sessions/"+source, "")
	response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatal(response.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(cwd, oldPath)); !os.IsNotExist(err) {
		t.Fatalf("source upload unexpectedly survives: %v", err)
	}
	addStudioTestModel(t, "other-model")
	a = patchStudioPolicy(t, h, a, map[string]any{"instructions": "NEW-LIVE-CONTEXT", "model": "other-model", "models": []string{"other-model"}, "tools": []string{"read"}, "skills": []string{}, "mcp_servers": []string{}})
	agentScheduleResponse(t, h.do("POST", "/api/schedule/jobs/agent-durable/run-now", ""), 202)
	done := waitAgentSchedule(t, "agent-durable", dir)
	if done.LastRunStatus != "ok" || done.LastError != "" || done.LastSummary != "scheduled output" || done.SessionID == "" {
		t.Fatalf("run result %+v", done)
	}
	binding, ok := h.mgr.studio.binding(done.SessionID)
	if !ok || binding.Agent.ID != a.ID || binding.Agent.Revision != a.Revision {
		t.Fatal("result not bound to latest Agent")
	}
	systems, _, histories, _ := cap.snapshot()
	if len(systems) != 1 || !strings.Contains(systems[0], "NEW-LIVE-CONTEXT") || strings.Contains(systems[0], "OLD-CONTEXT") {
		t.Fatalf("wrong instructions %v", systems)
	}
	if models := cap.clientModels(); models[len(models)-1] != "other-model" {
		t.Fatalf("wrong current Agent model %v", models)
	}
	var blocks []llm.Block
	for _, m := range histories[0] {
		if m.Role == llm.RoleUser {
			blocks = m.Content
		}
	}
	positions := map[string]int{}
	filePath := ""
	for i, b := range blocks {
		if b.Type == llm.BlockFile {
			positions["file"] = i
			filePath = b.FilePath
		}
		if strings.Contains(b.Text, "first ") {
			positions["first"] = i
		}
		if b.Text == " after file" {
			positions["after"] = i
		}
		if b.Text == " last" {
			positions["last"] = i
		}
	}
	if !(positions["first"] < positions["file"] && positions["file"] < positions["after"] && positions["after"] < positions["last"]) {
		t.Fatalf("ordered refs lost %+v", blocks)
	}
	data, err := os.ReadFile(filepath.Join(h.session(done.SessionID).cwd, filePath))
	if err != nil || string(data) != "immutable scheduled input" {
		t.Fatalf("durable asset lost %q %v", data, err)
	}
	history := agentScheduleResponse(t, h.do("GET", "/api/schedule/jobs/agent-durable/runs", ""), 200)
	row := history["runs"].([]any)[0].(map[string]any)
	if row["model"] != "other-model" || row["agent_revision"] != float64(a.Revision) || !strings.Contains(row["result_url"].(string), done.SessionID) {
		t.Fatalf("history missing provenance %v", row)
	}
}
func TestAgentScheduleEditCASAndInternalCronDeadline(t *testing.T) {
	dir := scheduleEnv(t)
	h := newHarness(t, scriptedTurns(textTurn("ok")))
	a := createStudioAgent(t, h, "Plan", "context", "ready")
	body := map[string]any{"agent_id": a.ID, "prompt": "old task", "timezone": "Asia/Shanghai", "kind": "cron", "cron": "0 9 * * *", "name": "editable"}
	agentScheduleResponse(t, h.do("POST", "/api/schedule/jobs", studioBody(body)), 201)
	body["prompt"] = "new task"
	body["revision"] = 1
	body["cron"] = "0 10 * * *"
	edited := agentScheduleResponse(t, h.do("PATCH", "/api/schedule/jobs/editable", studioBody(body)), 200)
	if edited["job"].(map[string]any)["revision"] != float64(2) {
		t.Fatal("revision not advanced")
	}
	agentScheduleResponse(t, h.do("PATCH", "/api/schedule/jobs/editable", studioBody(body)), 409)
	before, _ := schedule.NewStore(dir).Find("editable")
	if before.Prompt != "new task" || before.DefHash != schedule.DefinitionHash(before) {
		t.Fatal("edit not sealed")
	}
	now := time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC)
	anchor := now.Add(-time.Minute)
	j := schedule.Job{ID: "due-cron", Enabled: true, Kind: schedule.KindCron, Cron: "0 9 * * *", Timezone: "Asia/Shanghai", CreatedAt: anchor}
	if fire := nextFire(j, now.Add(2*time.Second)); fire == nil || !fire.Equal(now) {
		t.Fatalf("cron deadline moved after crossing tick: %v", fire)
	}
	j.LastRunAt = now
	if fire := nextFire(j, now.Add(2*time.Second)); fire == nil || !fire.After(now) {
		t.Fatalf("cron refires completed tick %v", fire)
	}
	j.Enabled = false
	if fire := nextFire(j, now); fire != nil {
		t.Fatal("disabled task has next fire")
	}
}

func TestAgentSchedulePinnedCompletionDoesNotReadOrCancelLaterUserRun(t *testing.T) {
	scheduleEnv(t)
	entered, release := make(chan struct{}), make(chan struct{})
	h := newHarness(t, func(n int) (llm.Response, error) {
		if n == 1 {
			return textTurn("original scheduled answer"), nil
		}
		close(entered)
		<-release
		return textTurn("later user answer"), nil
	})
	a := createStudioAgent(t, h, "Runs", "context", "ready")
	sid := startStudioConversation(t, h, a.ID)
	sess := h.session(sid)
	firstReceipt := &scheduleRunReceipt{}
	var first *activeRun
	firstID, err := sess.startOrderedInput("scheduled", nil, nil, "", "", mcpobserve.Metadata{}, "", func(r *activeRun) { first = r; r.observe = firstReceipt.observe })
	if err != nil {
		t.Fatal(err)
	}
	<-first.done
	laterReceipt := &scheduleRunReceipt{}
	var later *activeRun
	_, err = sess.startOrderedInput("later user", nil, nil, "", "", mcpobserve.Metadata{}, "", func(r *activeRun) { later = r; r.observe = laterReceipt.observe })
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	first.cancel()
	close(release)
	<-later.done
	if first.persistErr != nil || firstReceipt.summary != "original scheduled answer" || firstReceipt.endReason != "completed" {
		t.Fatalf("bad first receipt %+v", firstReceipt)
	}
	if laterReceipt.summary != "later user answer" || laterReceipt.endReason != "completed" {
		t.Fatalf("old cancellation affected new run: %+v", laterReceipt)
	}
	sess.hub.mu.Lock()
	events := append([]Event(nil), sess.hub.log...)
	sess.hub.mu.Unlock()
	replay := &scheduleRunReceipt{}
	observeScheduleWindow(replay, events, firstID)
	if replay.summary != "original scheduled answer" || replay.endReason != "completed" {
		t.Fatalf("window borrowed later result %+v", replay)
	}
	replay.observe(Event{Type: EvError, Error: "other run failed"})
	if replay.err != "" {
		t.Fatal("unrelated error replaced completed result")
	}
}

func TestAgentScheduleInternalCronActuallyFiresOnceAndResynchronizes(t *testing.T) {
	dir := scheduleEnv(t)
	h := newHarness(t, scriptedTurns(textTurn("not used")))
	recorder := newRecordingRunner()
	old := schedRunner
	schedRunner = recorder.run
	t.Cleanup(func() { schedRunner = old })
	now := time.Now().UTC()
	deadline := now.Truncate(time.Minute)
	// A server was absent for many cycles. Only one missed fire is run, then
	// LastRunAt re-synchronizes the deadline instead of replaying the backlog.
	job := schedule.Job{ID: "missed-cron", Prompt: "offline", Workdir: h.mgr.Cwd(), Kind: schedule.KindCron, Cron: fmt.Sprintf("%d %d * * *", deadline.Minute(), deadline.Hour()), Timezone: "UTC", CreatedAt: deadline.Add(-30 * 24 * time.Hour), Enabled: true}
	if err := schedule.NewStore(dir).Add(job); err != nil {
		t.Fatal(err)
	}
	h.mgr.fireDuePass(now, true)
	select {
	case <-recorder.saw:
	case <-time.After(4 * time.Second):
		t.Fatal("internal cron never fired")
	}
	finished := waitAgentSchedule(t, job.ID, dir)
	if fire := nextFire(finished, time.Now()); fire == nil || !fire.After(now) {
		t.Fatalf("cron did not resynchronize after catch-up: %v", fire)
	}
	h.mgr.fireDuePass(time.Now(), true)
	if recorder.count() != 1 {
		t.Fatalf("replayed historical cron cycles: %d", recorder.count())
	}
}

func TestAgentScheduleWorkflowTimezoneRoundTripAndOldClientPreservation(t *testing.T) {
	scheduleEnv(t)
	h := newHarness(t, scriptedTurns(textTurn("done")))
	a := createStudioAgent(t, h, "Workflow", "context", "ready")
	wf := createWorkflow(t, h, soloBody(a.ID, "scheduled"))
	endpoint := "/api/manage/workflows/" + wf.ID + "/schedule"
	initial := agentScheduleResponse(t, h.do("POST", endpoint, `{"kind":"cron","cron":"0 9 * * *","timezone":"Asia/Shanghai"}`), 200)
	if initial["timezone"] != "Asia/Shanghai" || initial["effective_timezone"] != "Asia/Shanghai" {
		t.Fatalf("zone summary lost: %v", initial)
	}
	older := agentScheduleResponse(t, h.do("POST", endpoint, `{"kind":"cron","cron":"0 10 * * *"}`), 200)
	if older["timezone"] != "Asia/Shanghai" {
		t.Fatalf("old client reset server-side zone: %v", older)
	}
	agentScheduleResponse(t, h.do("POST", endpoint, `{"kind":"cron","cron":"0 9 * * *","timezone":"Invalid/Zone"}`), 400)
}

func TestAgentScheduleCapabilityRemovalAndPausedAgentNeverFallback(t *testing.T) {
	dir := scheduleEnv(t)
	sk := writeTestSkill(t, t.TempDir(), "plan-skill", "scheduled skill", "# Selected only")
	h, cap := newStudioHarness(t, []llm.Response{textTurn("must not execute")}, func(c *Config) { c.Skills = []skills.Skill{sk} })
	a := createStudioAgent(t, h, "Capabilities", "context", "ready")
	a = patchStudioPolicy(t, h, a, map[string]any{"skills": []string{skillIDOf(sk)}})
	body := map[string]any{"agent_id": a.ID, "parts": []inputref.Part{{Type: "skill", Name: sk.Name}, {Type: "text", Text: "do task"}}, "input_layout": "inline", "timezone": "UTC", "kind": "every", "every_sec": 3600, "name": "removed-skill"}
	agentScheduleResponse(t, h.do("POST", "/api/schedule/jobs", studioBody(body)), 201)
	a = patchStudioPolicy(t, h, a, map[string]any{"skills": []string{}})
	agentScheduleResponse(t, h.do("POST", "/api/schedule/jobs/removed-skill/run-now", ""), 202)
	done := waitAgentSchedule(t, "removed-skill", dir)
	if done.LastRunStatus != "error" || !strings.Contains(done.LastError, "skill") {
		t.Fatalf("removed skill was not refused: %+v", done)
	}
	systems, _, _, _ := cap.snapshot()
	if len(systems) != 0 {
		t.Fatal("removed capability ran a model or bare runtime")
	}
	a = patchStudioPolicy(t, h, a, map[string]any{"status": "draft"})
	job, _ := schedule.NewStore(dir).Find("removed-skill")
	result := h.mgr.runAgentSchedule(context.Background(), job, schedule.LogPaths{}, nil)
	if result.ExitCode == 0 || !strings.Contains(result.Err, "暂停") {
		t.Fatalf("draft Agent fallback %+v", result)
	}
}

func TestAgentScheduleMissingDefaultWorkspaceCannotFollowSymlinkAncestor(t *testing.T) {
	scheduleEnv(t)
	h := newHarness(t, scriptedTurns(textTurn("unused")))
	a := createStudioAgent(t, h, "Directories", "context", "ready")
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(h.mgr.Cwd(), "agents")); err != nil {
		t.Skip(err)
	}
	body := map[string]any{"agent_id": a.ID, "prompt": "task", "timezone": "UTC", "kind": "every", "every_sec": 3600}
	agentScheduleResponse(t, h.do("POST", "/api/schedule/jobs", studioBody(body)), 400)
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("default workspace followed alias outside owner root")
	}
}

func TestAgentScheduleWorkflowEditAndConcurrentRecreateKeepHistoryNamespaces(t *testing.T) {
	dir := scheduleEnv(t)
	h := newHarness(t, scriptedTurns(textTurn("unused")))
	a := createStudioAgent(t, h, "Workflow history", "context", "ready")
	wf := createWorkflow(t, h, soloBody(a.ID, "task"))
	endpoint := "/api/manage/workflows/" + wf.ID + "/schedule"
	first := agentScheduleResponse(t, h.do("POST", endpoint, `{"kind":"every","every_sec":3600,"timezone":"UTC"}`), 200)
	originalID := first["job_id"].(string)
	if originalID != wf.ID {
		t.Fatal("initial legacy Workflow ID changed")
	}
	st := schedule.NewStore(dir)
	if err := st.AppendRun(originalID, schedule.RunRecord{Status: "ok", Summary: "prior workflow result"}); err != nil {
		t.Fatal(err)
	}
	edited := agentScheduleResponse(t, h.do("POST", endpoint, `{"kind":"every","every_sec":7200,"timezone":"UTC"}`), 200)
	if edited["job_id"] != originalID {
		t.Fatal("edit changed existing history namespace")
	}
	if runs, _ := st.Runs(originalID, 0); len(runs) != 1 {
		t.Fatal("edit lost retained history")
	}
	response := h.do("DELETE", endpoint, "")
	response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatal(response.StatusCode)
	}
	var wg sync.WaitGroup
	results := make(chan *http.Response, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- h.do("POST", endpoint, `{"kind":"every","every_sec":3600,"timezone":"UTC"}`)
		}()
	}
	wg.Wait()
	close(results)
	newID := ""
	for response := range results {
		row := agentScheduleResponse(t, response, 200)
		id := row["job_id"].(string)
		if id == originalID {
			t.Fatal("recreated Workflow borrowed deleted history")
		}
		if newID != "" && id != newID {
			t.Fatal("concurrent updates created duplicate jobs")
		}
		newID = id
	}
	jobs, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, j := range jobs {
		if j.WorkflowID == wf.ID {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("workflow has %d active schedule definitions", count)
	}
	if runs, _ := st.Runs(newID, 0); len(runs) != 0 {
		t.Fatal("new job exposed predecessor history")
	}
	agentScheduleResponse(t, h.do("GET", "/api/schedule/jobs/"+originalID+"/runs", ""), 404)
}

func TestAgentSchedulePausedManualRunDoesNotEnableAndExpiredRequestIsSynchronous(t *testing.T) {
	dir := scheduleEnv(t)
	h := newHarness(t, scriptedTurns(textTurn("manual once")))
	a := createStudioAgent(t, h, "Manual once", "context", "ready")
	body := map[string]any{"agent_id": a.ID, "prompt": "manual", "timezone": "UTC", "kind": "every", "every_sec": 3600, "name": "paused-manual"}
	agentScheduleResponse(t, h.do("POST", "/api/schedule/jobs", studioBody(body)), 201)
	agentScheduleResponse(t, h.do("POST", "/api/schedule/jobs/paused-manual/disable", ""), 200)
	agentScheduleResponse(t, h.do("POST", "/api/schedule/jobs/paused-manual/run-now", ""), 202)
	done := waitAgentSchedule(t, "paused-manual", dir)
	if done.Enabled || done.LastRunStatus != "ok" || done.LastSummary != "manual once" {
		t.Fatalf("manual run enabled paused task or failed: %+v", done)
	}
	deadline := time.Now().Add(4 * time.Second)
	for h.mgr.SchedRunning()[done.ID] && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	st := schedule.NewStore(dir)
	called := false
	if _, err := st.RunOnce(context.Background(), done.ID, schedule.TriggerScheduled, nil, func(context.Context, schedule.Job, schedule.LogPaths, io.Writer) schedule.RunOutcome {
		called = true
		return schedule.RunOutcome{}
	}); err == nil || called {
		t.Fatal("scheduled trigger ran a paused Agent job")
	}
	// A trusted historical expired task is seeded directly; a public create
	// correctly refuses past expiry. The HTTP action must refuse before 202.
	expired := schedule.Job{ID: "expired-manual", AgentID: a.ID, Prompt: "expired", Workdir: done.Workdir, Timezone: "UTC", Kind: schedule.KindEvery, EverySec: 3600, Enabled: false, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(-time.Hour).Format(time.RFC3339)}
	if err := st.Add(expired); err != nil {
		t.Fatal(err)
	}
	response := agentScheduleResponse(t, h.do("POST", "/api/schedule/jobs/expired-manual/run-now", ""), 409)
	if !strings.Contains(response["error"].(string), "过期") {
		t.Fatalf("unclear expiry response %v", response)
	}
	if runs, _ := st.Runs(expired.ID, 0); len(runs) != 0 {
		t.Fatal("rejected HTTP action pretended to start an expired task")
	}
	if len(h.mgr.SchedRunning()) != 0 {
		t.Fatal("rejected expired action reserved a runtime")
	}
	if h.calls() != 1 {
		t.Fatal("paused scheduled or expired request called model")
	}
}

func TestAgentScheduleTimerWaitsForFullyInitializedAccountRuntime(t *testing.T) {
	dir := scheduleEnv(t)
	h := newHarness(t, scriptedTurns(textTurn("must not start early")))
	a := createStudioAgent(t, h, "Readiness", "context", "ready")
	now := time.Now()
	job := schedule.Job{ID: "runtime-not-ready", AgentID: a.ID, Prompt: "scheduled", Workdir: filepath.Join(h.mgr.Cwd(), "agents", a.ID), Timezone: "UTC", Kind: schedule.KindEvery, EverySec: 60, CreatedAt: now.Add(-time.Hour), Enabled: true}
	if err := schedule.NewStore(dir).Add(job); err != nil {
		t.Fatal(err)
	}
	h.mgr.mu.Lock()
	ready := h.mgr.scheduleOwnerValid
	h.mgr.scheduleOwnerValid = nil
	h.mgr.mu.Unlock()
	defer func() { h.mgr.mu.Lock(); h.mgr.scheduleOwnerValid = ready; h.mgr.mu.Unlock() }()
	h.mgr.fireDuePass(now, true)
	if len(h.mgr.SchedRunning()) != 0 || h.calls() != 0 {
		t.Fatal("timer started Agent before account/Office/MCP initialization")
	}
	outcome := h.mgr.runAgentSchedule(context.Background(), job, schedule.LogPaths{}, nil)
	if outcome.ExitCode == 0 || !strings.Contains(outcome.Err, "尚未就绪") {
		t.Fatalf("uninitialized runtime accepted direct dispatch: %+v", outcome)
	}
}
