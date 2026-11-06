package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/schedule"
	"github.com/yosukeno/pogo/sentinel"
	"github.com/yosukeno/pogo/sentinelexec"
	"github.com/yosukeno/pogo/taskqueue"
	"github.com/yosukeno/pogo/tools"
)

func taskRunsDB(t *testing.T, m *Manager) *taskqueue.Store {
	t.Helper()
	db, err := m.taskRuns.database(true)
	if err != nil || db == nil {
		t.Fatalf("queue database: %v", err)
	}
	return db
}

func taskRunsJob(t *testing.T, id string) schedule.Job {
	t.Helper()
	j := schedule.Job{ID: id, Title: id, Prompt: "original instruction", Kind: schedule.KindEvery, EverySec: 3600, Enabled: true, CreatedAt: time.Now(), Workdir: t.TempDir()}
	st := schedule.NewStore(schedule.DefaultDir())
	if err := st.Add(j); err != nil {
		t.Fatal(err)
	}
	j, err := st.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func taskRunsAwait(t *testing.T, db *taskqueue.Store, id, status string) taskqueue.Run {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var last taskqueue.Run
	for time.Now().Before(deadline) {
		var err error
		last, err = db.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if last.Status == status {
			return last
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("run did not become %s: %+v", status, last)
	return last
}

// Reserve actual database slots, so both a wakeup and the periodic pump must
// leave admissions queued. Releasing them exercises the same capacity gate as
// a real worker finishing; no runtime switches or test-only product hooks.
func taskRunsHoldSlots(t *testing.T, m *Manager) func() {
	t.Helper()
	db := taskRunsDB(t, m)
	// Prevent the live pump from observing a reservation between Enqueue and
	// Claim. Capacity itself is still enforced in the real database below.
	m.taskRuns.mu.Lock()
	defer m.taskRuns.mu.Unlock()
	reserved := []taskqueue.Run{}
	for i := 0; i < taskRunConcurrency; i++ {
		r, _, err := db.Enqueue(taskqueue.Request{TaskID: fmt.Sprintf("test-reservation:%d", i), Source: "test-reservation", SourceID: "host-test", Trigger: "test", TriggerID: "first", DefinitionHash: "fixture", OwnerID: "single-user", Title: "Reserved execution slot"})
		if err != nil {
			t.Fatal(err)
		}
		r, err = db.Claim(r.ID, taskRunConcurrency)
		if err != nil {
			t.Fatal(err)
		}
		reserved = append(reserved, r)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for _, r := range reserved {
				if _, err := db.Finish(r.ID, r.Revision, taskqueue.Outcome{Status: taskqueue.Completed}); err != nil {
					t.Error(err)
				}
			}
			m.taskRuns.wake()
		})
	}
}

func TestTaskRunsScheduledTriggerIsDurablyDeduplicated(t *testing.T) {
	scheduleEnv(t)
	var calls atomic.Int32
	m := schedulerManager(t, time.Hour, func(context.Context, schedule.Job, schedule.LogPaths, io.Writer) schedule.RunOutcome {
		calls.Add(1)
		return schedule.RunOutcome{SessionID: "offline-result", EndReason: "completed"}
	})
	release := taskRunsHoldSlots(t, m)
	defer release()
	j := taskRunsJob(t, "same-clock-fire")
	fire := time.Now().Add(-time.Second)
	first, created, err := m.taskRuns.enqueueSchedule(j, schedule.TriggerScheduled, fire)
	if err != nil || !created {
		t.Fatalf("first admission: %+v %v %v", first, created, err)
	}
	for i := 0; i < 5; i++ {
		again, created, err := m.taskRuns.enqueueSchedule(j, schedule.TriggerScheduled, fire)
		if err != nil || created || again.ID != first.ID {
			t.Fatalf("same clock fire duplicated: %+v %v %v", again, created, err)
		}
	}
	db := taskRunsDB(t, m)
	m.taskRuns.pump()
	if calls.Load() != 0 || taskRunsAwait(t, db, first.ID, taskqueue.Queued).Source != "schedule" {
		t.Fatal("scheduler bypassed the persisted queue or its capacity limit")
	}
	release()
	done := taskRunsAwait(t, db, first.ID, taskqueue.Completed)
	rows, err := db.List(taskqueue.Filter{TaskID: first.TaskID})
	if err != nil || len(rows) != 1 || calls.Load() != 1 || done.SessionID != "offline-result" {
		t.Fatalf("expected one admission/execution: rows=%d calls=%d done=%+v err=%v", len(rows), calls.Load(), done, err)
	}
	// Replaying the same clock tick after completion still cannot rerun it.
	again, created, err := m.taskRuns.enqueueSchedule(j, schedule.TriggerScheduled, fire)
	if err != nil || created || again.ID != done.ID || calls.Load() != 1 {
		t.Fatal("completed clock trigger was resurrected", err)
	}
}

func TestTaskRunsManualAPIAdmitsBeforeLegacyExecution(t *testing.T) {
	scheduleEnv(t)
	var calls atomic.Int32
	m := schedulerManager(t, time.Hour, func(context.Context, schedule.Job, schedule.LogPaths, io.Writer) schedule.RunOutcome {
		calls.Add(1)
		return schedule.RunOutcome{EndReason: "completed"}
	})
	server, err := NewServer(m, ServerOptions{Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(server.Handler())
	defer front.Close()
	release := taskRunsHoldSlots(t, m)
	defer release()
	j := taskRunsJob(t, "manual-through-queue")
	code, out := scheduleRequest(t, front, http.MethodPost, "/api/schedule/jobs/"+j.ID+"/run-now", "")
	id, _ := out["run_id"].(string)
	if code != http.StatusAccepted || out["queued"] != true || id == "" {
		t.Fatalf("manual admission response: %d %v", code, out)
	}
	db := taskRunsDB(t, m)
	r := taskRunsAwait(t, db, id, taskqueue.Queued)
	if calls.Load() != 0 || r.Trigger != schedule.TriggerManual || !strings.HasPrefix(r.TriggerID, "manual:") {
		t.Fatalf("manual path ran before durable admission: calls=%d row=%+v", calls.Load(), r)
	}
	if code, _ := scheduleRequest(t, front, http.MethodPost, "/api/schedule/jobs/"+j.ID+"/run-now", ""); code != http.StatusConflict {
		t.Fatalf("second manual request bypassed active admission: %d", code)
	}
	release()
	taskRunsAwait(t, db, id, taskqueue.Completed)
	if calls.Load() != 1 {
		t.Fatalf("manual execution count=%d", calls.Load())
	}
}

func TestTaskRunsQueuedDefinitionCannotExecuteChangedInstructionsOrOwner(t *testing.T) {
	for _, change := range []string{"pause", "instruction", "owner"} {
		t.Run(change, func(t *testing.T) {
			scheduleEnv(t)
			var calls atomic.Int32
			m := schedulerManager(t, time.Hour, func(context.Context, schedule.Job, schedule.LogPaths, io.Writer) schedule.RunOutcome {
				calls.Add(1)
				return schedule.RunOutcome{}
			})
			release := taskRunsHoldSlots(t, m)
			defer release()
			j := taskRunsJob(t, "sealed-"+change)
			r, _, err := m.taskRuns.enqueueSchedule(j, schedule.TriggerScheduled, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if err := schedule.NewStore(schedule.DefaultDir()).Update(j.ID, func(current *schedule.Job) {
				switch change {
				case "pause":
					current.Enabled = false
				case "instruction":
					current.Prompt = "a new instruction must require a new admission"
					current.DefHash = schedule.DefinitionHash(*current)
				case "owner":
					owner := identity.LocalScope("another-owner")
					current.Owner = &owner
					current.DefHash = schedule.DefinitionHash(*current)
				}
			}); err != nil {
				t.Fatal(err)
			}
			release()
			taskRunsAwait(t, taskRunsDB(t, m), r.ID, taskqueue.Cancelled)
			if calls.Load() != 0 {
				t.Fatal("queued instance executed a paused or changed definition")
			}
		})
	}
}

func TestTaskRunsCancelledClockAdmissionPreservesFuturePeriodicFire(t *testing.T) {
	dir := scheduleEnv(t)
	m := schedulerManager(t, time.Hour, func(context.Context, schedule.Job, schedule.LogPaths, io.Writer) schedule.RunOutcome {
		t.Error("cancelled admission invoked runner")
		return schedule.RunOutcome{}
	})
	release := taskRunsHoldSlots(t, m)
	defer release()
	j := taskRunsJob(t, "period-after-cancel")
	fire := time.Now().Add(-time.Second)
	r, _, err := m.taskRuns.enqueueSchedule(j, schedule.TriggerScheduled, fire)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := schedule.NewStore(dir).Find(j.ID)
	if err != nil || saved.LastQueuedAt.IsZero() || !saved.LastRunAt.IsZero() || schedule.DefinitionHash(saved) != r.DefinitionHash {
		t.Fatalf("clock admission did not commit a runtime-only watermark: %+v %v", saved, err)
	}
	if _, err := m.taskRuns.cancel(r.ID, r.Revision); err != nil {
		t.Fatal(err)
	}
	current, err := schedule.NewStore(dir).Find(j.ID)
	if err != nil || !current.Enabled || !current.LastRunAt.IsZero() || !current.LastQueuedAt.Equal(saved.LastQueuedAt) {
		t.Fatalf("cancellation changed the periodic definition or admission clock: %+v %v", current, err)
	}
	if got := nextFire(current, current.LastQueuedAt); got == nil || !got.Equal(current.LastQueuedAt.Add(time.Hour)) {
		t.Fatalf("cancelling one admission swallowed periodic future fire: %v", got)
	}
	// The old fire remains cancelled on replay; a future period has a new key.
	again, created, err := m.taskRuns.enqueueSchedule(j, schedule.TriggerScheduled, fire)
	if err != nil || created || again.ID != r.ID || again.Status != taskqueue.Cancelled {
		t.Fatalf("old clock replay after cancellation: %+v %v %v", again, created, err)
	}
	futureFire := saved.LastQueuedAt.Add(time.Hour)
	next, created, err := m.taskRuns.enqueueSchedule(current, schedule.TriggerScheduled, futureFire)
	if err != nil || !created || next.ID == r.ID || next.TriggerID == r.TriggerID {
		t.Fatalf("future cycle was not independently admitted: %+v %v %v", next, created, err)
	}
	// Keep the synthetic future request from running during test cleanup.
	if _, err := m.taskRuns.cancel(next.ID, next.Revision); err != nil {
		t.Fatal(err)
	}
}

func TestTaskRunsRestartDoesNotRerunUnknownWorkAndRetryPreservesOldReceipt(t *testing.T) {
	dir := scheduleEnv(t)
	j := taskRunsJob(t, "restart-receipt")
	sessions := t.TempDir()
	path := filepath.Join(sessions, "task-runs", "runs.db")
	db, err := taskqueue.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(scheduleTaskPayload{Directory: dir})
	r, _, err := db.Enqueue(taskqueue.Request{TaskID: "schedule:" + j.ID, Source: "schedule", SourceID: j.ID, Trigger: schedule.TriggerManual, TriggerID: "manual:before-crash", DefinitionHash: schedule.DefinitionHash(j), OwnerID: "single-user", Title: j.Title, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	r, err = db.Claim(r.ID, taskRunConcurrency)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.AttachSession(r.ID, r.Revision, "original-ambiguous-session"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	restore := schedRunner
	schedRunner = func(context.Context, schedule.Job, schedule.LogPaths, io.Writer) schedule.RunOutcome {
		calls.Add(1)
		return schedule.RunOutcome{SessionID: "explicit-retry-session", EndReason: "completed", Summary: "confirmed retry"}
	}
	t.Cleanup(func() { schedRunner = restore })
	h := newHarness(t, func(int) (llm.Response, error) {
		t.Error("legacy fixture unexpectedly called model")
		return textTurn("unexpected"), nil
	}, func(c *Config) { c.SessionDir = sessions; c.ScheduleTickInterval = time.Hour })
	db = taskRunsDB(t, h.mgr)
	old := taskRunsAwait(t, db, r.ID, taskqueue.Interrupted)
	h.mgr.taskRuns.pump()
	if calls.Load() != 0 || old.SessionID != "original-ambiguous-session" {
		t.Fatal("restart retried unknown execution or discarded its session")
	}
	_, _, err = h.mgr.taskRuns.enqueueSchedule(j, schedule.TriggerManual, time.Now())
	if !errors.Is(err, schedule.ErrRunning) {
		t.Fatalf("run-now bypassed unresolved interrupted receipt: %v", err)
	}
	next, err := h.mgr.taskRuns.retry(old.ID, old.Revision)
	if err != nil || next.ID == old.ID || next.RetryOf != old.ID {
		t.Fatalf("explicit retry: %+v %v", next, err)
	}
	taskRunsAwait(t, db, next.ID, taskqueue.Completed)
	preserved, err := db.Get(old.ID)
	if err != nil || !reflect.DeepEqual(preserved, old) || calls.Load() != 1 {
		t.Fatalf("retry overwrote ambiguous history or ran repeatedly: %+v %v calls=%d", preserved, err, calls.Load())
	}
}

func TestTaskRunsScheduleAndSentinelShareExecutionSlotsWhileCollectorStaysLive(t *testing.T) {
	scheduleEnv(t)
	modelEntered, modelRelease := make(chan struct{}), make(chan struct{})
	scheduleEntered, scheduleRelease := make(chan string, 4), make(chan struct{})
	collectorEntered, collectorRelease := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(modelRelease); close(scheduleRelease); close(collectorRelease) })
	}
	defer release()
	restore := schedRunner
	schedRunner = func(ctx context.Context, job schedule.Job, _ schedule.LogPaths, _ io.Writer) schedule.RunOutcome {
		scheduleEntered <- job.ID
		if job.ID == "slot-schedule-first" {
			select {
			case <-scheduleRelease:
			case <-ctx.Done():
			}
		}
		return schedule.RunOutcome{EndReason: "completed"}
	}
	t.Cleanup(func() { schedRunner = restore })
	h := newHarness(t, func(n int) (llm.Response, error) {
		if n == 1 {
			close(modelEntered)
			<-modelRelease
			return toolTurn(tools.WatchDecideName, `{"decision":"report","summary":"shared slot report"}`), nil
		}
		return textTurn("sentinel completed"), nil
	}, func(c *Config) {
		var err error
		c.Cwd, err = filepath.EvalSymlinks(c.Cwd)
		if err != nil {
			t.Fatal(err)
		}
		c.SessionDir, err = filepath.EvalSymlinks(c.SessionDir)
		if err != nil {
			t.Fatal(err)
		}
		c.ScheduleTickInterval = time.Hour
	})
	session := sentinelTestSession(t, h)
	watch := sentinelTestCreate(t, h, session)
	native, err := h.mgr.sentinels.database(true)
	if err != nil {
		t.Fatal(err)
	}
	watch, err = native.SetEnabled(watch.Spec.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	defer native.SetEnabled(watch.Spec.ID, false)
	if err := native.SetRuntimeGeneration(watch.Spec.ID, watch.Spec.Generation, "waiting", "", time.Now().Add(time.Hour).UnixMilli(), 0); err != nil {
		t.Fatal(err)
	}
	collector := newSentinelRuntime(h.mgr)
	defer collector.cancel()
	collector.runner = func(ctx context.Context, _ sentinelexec.Config, _ func(sentinel.Frame) error) error {
		close(collectorEntered)
		select {
		case <-collectorRelease:
		case <-ctx.Done():
		}
		return nil
	}
	collectorDone := make(chan struct{})
	go func() { defer close(collectorDone); collector.collect(context.Background(), native, watch) }()
	defer func() { release(); <-collectorDone }()
	select {
	case <-collectorEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("collector did not start")
	}
	j1 := taskRunsJob(t, "slot-schedule-first")
	first, _, err := h.mgr.taskRuns.enqueueSchedule(j1, schedule.TriggerManual, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-scheduleEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("schedule did not start while collector was alive")
	}
	if err := native.Accept(watch.Spec.ID, watch.Spec.Generation, sentinel.Frame{V: 1, ID: "shared-slots-frame", Events: []sentinel.InputEvent{{Key: "shared:1", Summary: "offline shared capacity event"}}}); err != nil {
		t.Fatal(err)
	}
	watch, err = native.Get(watch.Spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.mgr.sentinels.dispatch(native, watch)
	select {
	case <-modelEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("sentinel did not share second execution slot with schedule")
	}
	j2 := taskRunsJob(t, "slot-schedule-second")
	second, _, err := h.mgr.taskRuns.enqueueSchedule(j2, schedule.TriggerManual, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	db := taskRunsDB(t, h.mgr)
	h.mgr.taskRuns.pump()
	rows, err := db.List(taskqueue.Filter{ActiveOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	running, sources := 0, map[string]bool{}
	for _, row := range rows {
		if row.Status == taskqueue.Running {
			running++
			sources[row.Source] = true
			if row.Source == "sentinel" {
				if row.SessionID == "" || h.session(row.SessionID).Policy().State().Mode != string(ModeStrict) {
					t.Fatal("queued sentinel weakened strict policy or lost durable session binding")
				}
			}
		}
	}
	if running != 2 || !sources["schedule"] || !sources["sentinel"] || taskRunsAwait(t, db, second.ID, taskqueue.Queued).Status != taskqueue.Queued {
		t.Fatalf("execution capacity not shared: running=%d sources=%v", running, sources)
	}
	select {
	case id := <-scheduleEntered:
		t.Fatalf("third execution bypassed shared limit: %s", id)
	default:
	}
	// One execution ending admits the third; the collector's lifetime is not
	// counted as bounded model execution and remains independent throughout.
	release()
	taskRunsAwait(t, db, first.ID, taskqueue.Completed)
	taskRunsAwait(t, db, second.ID, taskqueue.Completed)
}

func TestTaskRunsAgentQueueRetainsExplicitApprovalAndRejectsRevokedOwner(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(fmt.Sprintf("owner-revoked=%v", revoke), func(t *testing.T) {
			dir := scheduleEnv(t)
			h, capture := newSentinelHarness(t, []llm.Response{textTurn("offline strict schedule completed")})
			a := createStudioAgent(t, h, "Strict queued agent", "only do the requested work", "ready")
			body := map[string]any{"name": "strict-queued", "agent_id": a.ID, "prompt": "offline task", "kind": "every", "every_sec": 3600, "timezone": "UTC", "approval_mode": "strict"}
			agentScheduleResponse(t, h.do(http.MethodPost, "/api/schedule/jobs", studioBody(body)), http.StatusCreated)
			release := taskRunsHoldSlots(t, h.mgr)
			defer release()
			response := agentScheduleResponse(t, h.do(http.MethodPost, "/api/schedule/jobs/strict-queued/run-now", ""), http.StatusAccepted)
			id, _ := response["run_id"].(string)
			if id == "" {
				t.Fatal("agent manual run missing durable queue identity")
			}
			if revoke {
				h.mgr.mu.Lock()
				h.mgr.scheduleOwnerValid = func(*identity.Scope) bool { return false }
				h.mgr.mu.Unlock()
			}
			release()
			if revoke {
				taskRunsAwait(t, taskRunsDB(t, h.mgr), id, taskqueue.Failed)
				if sentinelModelCalls(capture) != 0 {
					t.Fatal("revoked owner still reached model execution")
				}
				return
			}
			run := taskRunsAwait(t, taskRunsDB(t, h.mgr), id, taskqueue.Completed)
			job, err := schedule.NewStore(dir).Find("strict-queued")
			if err != nil || run.OwnerID != sessionResultOwnerKey(job.Owner) || run.SessionID != job.SessionID || h.session(run.SessionID).Policy().State().Mode != string(ModeStrict) || sentinelModelCalls(capture) != 1 {
				t.Fatalf("queued agent changed permission/session/policy: run=%+v job=%+v err=%v", run, job, err)
			}
		})
	}
}

type taskRunsRevocationClient struct {
	entered   chan struct{}
	cancelled chan struct{}
	calls     atomic.Int32
}

func (*taskRunsRevocationClient) Model() string { return "k3" }
func (c *taskRunsRevocationClient) Stream(ctx context.Context, _ string, _ []llm.Message, _ []llm.ToolSchema, _ func(llm.Delta)) (llm.Response, error) {
	if c.calls.Add(1) != 1 {
		return llm.Response{}, errors.New("revoked execution made an unexpected subsequent model call")
	}
	close(c.entered)
	<-ctx.Done()
	close(c.cancelled)
	return llm.Response{StopReason: llm.StopAborted}, nil
}

func TestTaskRunsSentinelPreparationCannotEraseActiveRevocationHandle(t *testing.T) {
	scheduleEnv(t)
	client := &taskRunsRevocationClient{entered: make(chan struct{}), cancelled: make(chan struct{})}
	h := newHarness(t, nil, func(c *Config) {
		var err error
		c.Cwd, err = filepath.EvalSymlinks(c.Cwd)
		if err != nil {
			t.Fatal(err)
		}
		c.SessionDir, err = filepath.EvalSymlinks(c.SessionDir)
		if err != nil {
			t.Fatal(err)
		}
		c.ScheduleTickInterval = time.Hour
		c.NewClient = func(config.Resolved, func(llm.RetryInfo)) llm.Client { return client }
	})
	var revoked atomic.Bool
	h.mgr.mu.Lock()
	h.mgr.scheduleOwnerValid = func(*identity.Scope) bool { return !revoked.Load() }
	h.mgr.mu.Unlock()
	sess := sentinelTestSession(t, h)
	watch := sentinelTestCreate(t, h, sess)
	native, err := h.mgr.sentinels.database(true)
	if err != nil {
		t.Fatal(err)
	}
	watch, err = native.SetEnabled(watch.Spec.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	defer native.SetEnabled(watch.Spec.ID, false)
	if err := native.SetRuntimeGeneration(watch.Spec.ID, watch.Spec.Generation, "waiting", "", time.Now().Add(time.Hour).UnixMilli(), 0); err != nil {
		t.Fatal(err)
	}
	if err := native.Accept(watch.Spec.ID, watch.Spec.Generation, sentinel.Frame{V: 1, ID: "revocation-handle", Events: []sentinel.InputEvent{{Key: "revocation:1", Summary: "hold the first offline model call"}}}); err != nil {
		t.Fatal(err)
	}
	watch, err = native.Get(watch.Spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.mgr.sentinels.dispatch(native, watch)
	select {
	case <-client.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("sentinel execution never reached the blocked model")
	}
	// Events remain pending during execution. A later tick therefore performs
	// another (empty) admission preparation for the same still-running batch.
	h.mgr.sentinels.tick()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mgr.sentinels.mu.Lock()
		preparing := h.mgr.sentinels.runs[watch.Spec.ID]
		activeCancel := h.mgr.sentinels.runCancels[watch.Spec.ID]
		h.mgr.sentinels.mu.Unlock()
		if !preparing {
			if activeCancel == nil {
				t.Fatal("empty preparation erased the active worker's revocation handle")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("additional admission preparation did not drain")
		}
		time.Sleep(time.Millisecond)
	}
	revoked.Store(true)
	h.mgr.sentinels.tick()
	select {
	case <-client.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("owner revocation failed to cancel the existing model context")
	}
	db := taskRunsDB(t, h.mgr)
	rows, err := db.List(taskqueue.Filter{TaskID: "sentinel:" + watch.Spec.ID})
	if err != nil || len(rows) != 1 {
		t.Fatalf("revoked execution admissions: %+v %v", rows, err)
	}
	taskRunsAwait(t, db, rows[0].ID, taskqueue.Interrupted)
	if client.calls.Load() != 1 {
		t.Fatalf("revoked execution continued model work: %d calls", client.calls.Load())
	}
}

func TestTaskRunsRunNowCannotBypassInterruptedWorkAfterThousandReceipts(t *testing.T) {
	scheduleEnv(t)
	var calls atomic.Int32
	m := schedulerManager(t, time.Hour, func(context.Context, schedule.Job, schedule.LogPaths, io.Writer) schedule.RunOutcome {
		calls.Add(1)
		return schedule.RunOutcome{EndReason: "completed"}
	})
	release := taskRunsHoldSlots(t, m)
	defer release()
	j := taskRunsJob(t, "long-lived-periodic-task")
	db := taskRunsDB(t, m)
	payload, _ := json.Marshal(scheduleTaskPayload{Directory: schedule.DefaultDir()})
	var ambiguous taskqueue.Run
	// Seed real durable receipts, with the unresolved record beyond the old
	// 1000-row ascending history window. Prevent the pump from claiming these
	// fixture admissions before their synthetic receipts have been committed.
	func() {
		m.taskRuns.mu.Lock()
		defer m.taskRuns.mu.Unlock()
		for i := 0; i <= 1000; i++ {
			r, created, err := db.Enqueue(taskqueue.Request{TaskID: "schedule:" + j.ID, Source: "schedule", SourceID: j.ID, Trigger: schedule.TriggerScheduled, TriggerID: fmt.Sprintf("previous-period:%04d", i), DefinitionHash: schedule.DefinitionHash(j), OwnerID: "single-user", Title: j.Title, Payload: payload})
			if err != nil || !created {
				t.Fatalf("seed receipt %d: created=%v err=%v", i, created, err)
			}
			r, err = db.Claim(r.ID, 10000)
			if err != nil {
				t.Fatal(err)
			}
			out := taskqueue.Outcome{Status: taskqueue.Completed}
			if i == 1000 {
				out = taskqueue.Outcome{Status: taskqueue.Interrupted, Error: "previous side effects need reconciliation"}
			}
			r, err = db.Finish(r.ID, r.Revision, out)
			if err != nil {
				t.Fatal(err)
			}
			if i == 1000 {
				ambiguous = r
			}
		}
	}()
	server, err := NewServer(m, ServerOptions{Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(server.Handler())
	defer front.Close()
	code, out := scheduleRequest(t, front, http.MethodPost, "/api/schedule/jobs/"+j.ID+"/run-now", "")
	if code != http.StatusConflict {
		t.Fatalf("long history let run-now bypass unresolved execution: %d %v", code, out)
	}
	rows, err := db.List(taskqueue.Filter{TaskID: "schedule:" + j.ID})
	preserved, getErr := db.Get(ambiguous.ID)
	if err != nil || getErr != nil || len(rows) != 1001 || !reflect.DeepEqual(preserved, ambiguous) || calls.Load() != 0 {
		t.Fatalf("run-now changed unresolved historic work: rows=%d calls=%d list=%v get=%v", len(rows), calls.Load(), err, getErr)
	}
}

func TestTaskRunsCancelledPublicationRepairsNativeBatchAndAllowsFutureEvents(t *testing.T) {
	scheduleEnv(t)
	h, capture := newSentinelHarness(t, []llm.Response{
		toolTurn(tools.WatchDecideName, `{"decision":"report","summary":"handled only the new event"}`),
		textTurn("new event completed"),
	})
	release := taskRunsHoldSlots(t, h.mgr)
	defer release()
	sess := sentinelTestSession(t, h)
	watch := sentinelTestCreate(t, h, sess)
	native, err := h.mgr.sentinels.database(true)
	if err != nil {
		t.Fatal(err)
	}
	watch, err = native.SetEnabled(watch.Spec.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	defer native.SetEnabled(watch.Spec.ID, false)
	if err := native.SetRuntimeGeneration(watch.Spec.ID, watch.Spec.Generation, "waiting", "", time.Now().Add(time.Hour).UnixMilli(), 0); err != nil {
		t.Fatal(err)
	}
	const oldMarker = "CANCELLED_EVENT_SHOULD_NOT_REACH_MODEL"
	const newMarker = "NEW_EVENT_MUST_REACH_MODEL"
	if err := native.Accept(watch.Spec.ID, watch.Spec.Generation, sentinel.Frame{V: 1, ID: "before-cancel-half-commit", Events: []sentinel.InputEvent{{Key: "cancel-publication:old", Summary: oldMarker}}}); err != nil {
		t.Fatal(err)
	}
	batch, err := native.ClaimBatch(watch.Spec.ID, sentinel.MaxBatchEvents)
	if err != nil || batch == nil {
		t.Fatalf("freeze cancelled observations: %+v %v", batch, err)
	}
	if err := native.QueueBatch(batch.ID); err != nil {
		t.Fatal(err)
	}
	old, created, err := h.mgr.taskRuns.enqueueSentinel(watch, *batch, "")
	if err != nil || !created {
		t.Fatalf("publish original batch: %+v %v %v", old, created, err)
	}
	db := taskRunsDB(t, h.mgr)
	// Simulate process death after the generic cancellation committed, before
	// cancelSentinelQueued could synchronize the separate native database.
	cancelled, err := db.Cancel(old.ID, old.Revision)
	if err != nil {
		t.Fatal(err)
	}
	beforeRepair, err := native.GetBatch(batch.ID)
	if err != nil || beforeRepair.Status != "queued" || beforeRepair.RunID != "" {
		t.Fatalf("did not establish the real cross-database half-commit: %+v %v", beforeRepair, err)
	}
	watch, err = native.Get(watch.Spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The supervisor republishes this persisted queued batch on its next pass.
	// Reusing its cancelled generic identity must repair, never execute it.
	h.mgr.sentinels.dispatch(native, watch)
	repaired, err := native.GetBatch(batch.ID)
	if err != nil || repaired.Status != "completed" || repaired.Decision != "ignore" || repaired.RunID != "" || !strings.Contains(repaired.Summary, "取消") {
		t.Fatalf("cancelled publication left the native batch stranded: %+v %v", repaired, err)
	}
	watch, err = native.Get(watch.Spec.ID)
	if err != nil || watch.Pending != 0 || sentinelModelCalls(capture) != 0 {
		t.Fatalf("repair executed cancelled observations or left them pending: %+v %v calls=%d", watch, err, sentinelModelCalls(capture))
	}
	again, created, err := h.mgr.taskRuns.enqueueSentinel(watch, *batch, "")
	if err != nil || created || !reflect.DeepEqual(again, cancelled) {
		t.Fatalf("cancelled publication replay was not idempotent: %+v %v %v", again, created, err)
	}
	if repeated, err := native.GetBatch(batch.ID); err != nil || !reflect.DeepEqual(repeated, repaired) {
		t.Fatal("repeated repair changed the durable cancellation receipt", err)
	}
	release()
	h.mgr.taskRuns.pump()
	if sentinelModelCalls(capture) != 0 {
		t.Fatal("cancelled old batch reached model after execution capacity freed")
	}
	// A later, distinct event belongs to a new batch and still triggers the
	// enabled watch. The dismissed observations remain retained for audit.
	if err := native.Accept(watch.Spec.ID, watch.Spec.Generation, sentinel.Frame{V: 1, ID: "after-cancel-new-event", Events: []sentinel.InputEvent{{Key: "cancel-publication:new", Summary: newMarker}}}); err != nil {
		t.Fatal(err)
	}
	watch, err = native.Get(watch.Spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.mgr.sentinels.dispatch(native, watch)
	rows, err := db.List(taskqueue.Filter{TaskID: old.TaskID})
	if err != nil || len(rows) != 2 {
		t.Fatalf("new event was blocked by the cancelled batch: %+v %v", rows, err)
	}
	var next taskqueue.Run
	for _, row := range rows {
		if row.ID != old.ID {
			next = row
		}
	}
	next = taskRunsAwait(t, db, next.ID, taskqueue.Completed)
	var nextPayload sentinelTaskPayload
	if err := json.Unmarshal(next.Payload, &nextPayload); err != nil || nextPayload.BatchID == batch.ID {
		t.Fatalf("new event reused cancelled frozen membership: %+v %v", nextPayload, err)
	}
	newBatch, err := native.GetBatch(nextPayload.BatchID)
	if err != nil || newBatch.Status != "completed" || len(newBatch.Events) != 1 || newBatch.Events[0].Key != "cancel-publication:new" {
		t.Fatalf("new event did not independently complete: %+v %v", newBatch, err)
	}
	_, _, histories, _ := capture.snapshot()
	combined := ""
	for _, history := range histories {
		for _, message := range history {
			combined += studioMessageText(message)
		}
	}
	if strings.Contains(combined, oldMarker) || !strings.Contains(combined, newMarker) || len(histories) != 2 {
		t.Fatalf("cancelled observation leaked into later model work: calls=%d old=%v new=%v", len(histories), strings.Contains(combined, oldMarker), strings.Contains(combined, newMarker))
	}
	if preserved, err := db.Get(old.ID); err != nil || !reflect.DeepEqual(preserved, cancelled) {
		t.Fatal("future work overwrote the original cancellation record", err)
	}
}
