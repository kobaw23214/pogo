package web

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yosukeno/pogo/schedule"
	"github.com/yosukeno/pogo/sentinel"
	"github.com/yosukeno/pogo/sentinelexec"
	"github.com/yosukeno/pogo/taskqueue"
)

func taskRunsMaintenanceMarker(t *testing.T, m *Manager, malformed bool) {
	t.Helper()
	path := m.releaseMaintenancePath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"token":"` + strings.Repeat("d", 64) + `"}`)
	if malformed {
		data = []byte(`{"token":`)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

// The supervisor's explicit wait evaluates Done before blocking. This context
// preserves cancellation while making arrival of the operator marker at that
// boundary deterministic, without sleeping and hoping a goroutine reached it.
type taskRunsMaintenanceWaitContext struct {
	context.Context
	once   sync.Once
	onWait func()
}

func (c *taskRunsMaintenanceWaitContext) Done() <-chan struct{} {
	c.once.Do(c.onWait)
	return c.Context.Done()
}

func TestTaskRunsMaintenanceHoldsQueuedWithoutCancellingCurrentExecution(t *testing.T) {
	scheduleEnv(t)
	entered, finish := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	var finishOnce sync.Once
	release := func() { finishOnce.Do(func() { close(finish) }) }
	defer release()
	m := schedulerManager(t, time.Hour, func(ctx context.Context, _ schedule.Job, _ schedule.LogPaths, _ io.Writer) schedule.RunOutcome {
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-finish:
			case <-ctx.Done():
				return schedule.RunOutcome{Err: "unexpected execution cancellation"}
			}
		}
		return schedule.RunOutcome{EndReason: "completed"}
	})
	first, _, err := m.taskRuns.enqueueSchedule(taskRunsJob(t, "already-running"), schedule.TriggerManual, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first execution did not start")
	}
	taskRunsMaintenanceMarker(t, m, false)
	second, _, err := m.taskRuns.enqueueSchedule(taskRunsJob(t, "wait-for-maintenance"), schedule.TriggerManual, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	m.taskRuns.pump()
	db := taskRunsDB(t, m)
	current, err := db.Get(first.ID)
	if err != nil || current.Status != taskqueue.Running || current.CancelRequested || calls.Load() != 1 {
		t.Fatalf("maintenance changed an executing task: %+v calls=%d err=%v", current, calls.Load(), err)
	}
	queued := taskRunsAwait(t, db, second.ID, taskqueue.Queued)
	if queued.StartedAt != 0 {
		t.Fatal("maintenance allowed a queue claim")
	}
	release()
	taskRunsAwait(t, db, first.ID, taskqueue.Completed)
	// A completed in-flight execution must not wake another task into the
	// maintenance window, even when that wakeup bypasses the one-second tick.
	m.taskRuns.pump()
	if calls.Load() != 1 || taskRunsAwait(t, db, second.ID, taskqueue.Queued).StartedAt != 0 {
		t.Fatal("worker completion bypassed maintenance")
	}
	if err := os.Remove(m.releaseMaintenancePath()); err != nil {
		t.Fatal(err)
	}
	m.taskRuns.pump()
	taskRunsAwait(t, db, second.ID, taskqueue.Completed)
	if calls.Load() != 2 {
		t.Fatal("queued request did not resume exactly once")
	}
}

func TestTaskRunsMaintenanceRequeuesClaimBeforeSourceExecution(t *testing.T) {
	scheduleEnv(t)
	var calls atomic.Int32
	m := schedulerManager(t, time.Hour, func(context.Context, schedule.Job, schedule.LogPaths, io.Writer) schedule.RunOutcome {
		calls.Add(1)
		return schedule.RunOutcome{EndReason: "completed"}
	})
	// Even an unreadable/malformed marker must remain fail-closed.
	taskRunsMaintenanceMarker(t, m, true)
	run, _, err := m.taskRuns.enqueueSchedule(taskRunsJob(t, "claim-race"), schedule.TriggerManual, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	db := taskRunsDB(t, m)
	claimed, err := db.Claim(run.ID, taskRunConcurrency)
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce Claim completing just before the operator creates the marker.
	// The executor must defer without calling either source implementation.
	ctx, cancel := context.WithCancel(m.taskRuns.ctx)
	m.taskRuns.mu.Lock()
	m.taskRuns.active[claimed.ID] = taskExecution{run: claimed, cancel: cancel}
	m.taskRuns.wg.Add(1)
	m.taskRuns.mu.Unlock()
	m.taskRuns.execute(ctx, db, claimed)
	got, err := db.Get(run.ID)
	if err != nil || got.Status != taskqueue.Queued || got.StartedAt != 0 || got.SessionID != "" || calls.Load() != 0 || got.Revision <= claimed.Revision {
		t.Fatalf("pre-start maintenance was not deferred: %+v calls=%d err=%v", got, calls.Load(), err)
	}
	if err := os.Remove(m.releaseMaintenancePath()); err != nil {
		t.Fatal(err)
	}
	taskRunsAwait(t, db, run.ID, taskqueue.Completed)
	if calls.Load() != 1 {
		t.Fatal("deferred claim did not resume once")
	}
}

func TestSentinelMaintenanceStopsCollectorsWithoutLosingAcknowledgedCursor(t *testing.T) {
	for _, incomingFrame := range []bool{false, true} {
		t.Run(map[bool]string{false: "idle-stream", true: "frame-before-supervisor-tick"}[incomingFrame], func(t *testing.T) {
			h, captured := newSentinelHarness(t, nil)
			s := sentinelTestSession(t, h)
			paused := sentinelTestCreate(t, h, s)
			db, err := h.mgr.sentinels.database(true)
			if err != nil {
				t.Fatal(err)
			}
			spec := paused.Spec
			spec.ID, spec.Mode = "", "stream"
			paused, err = db.Create(spec)
			if err != nil {
				t.Fatal(err)
			}
			db, watch := sentinelResilienceEnabled(t, h, paused)
			// The real Manager is reserved above; this second supervisor drives
			// the same durable store without racing the host's one-second tick.
			runtime := newSentinelRuntime(h.mgr)
			runtime.store = db
			t.Cleanup(func() {
				runtime.cancel()
				runtime.mu.Lock()
				for _, w := range runtime.workers {
					w.cancel()
				}
				runtime.mu.Unlock()
				runtime.wg.Wait()
			})
			accepted, rejected, resumed := make(chan struct{}), make(chan error, 1), make(chan sentinelexec.Config, 1)
			tryFrame := make(chan struct{})
			var calls atomic.Int32
			checkpoint := sentinel.Frame{V: 1, ID: "committed-before-maintenance", State: json.RawMessage(`{"offset":1}`), Cursor: json.RawMessage(`{"after":"first"}`)}
			runtime.runner = func(ctx context.Context, cfg sentinelexec.Config, accept func(sentinel.Frame) error) error {
				if calls.Add(1) > 1 {
					resumed <- cfg
					<-ctx.Done()
					return ctx.Err()
				}
				if err := accept(checkpoint); err != nil {
					rejected <- err
					return err
				}
				close(accepted)
				if incomingFrame {
					select {
					case <-tryFrame:
					case <-ctx.Done():
						return ctx.Err()
					}
					err := accept(sentinel.Frame{V: 1, ID: "unacknowledged-during-maintenance", State: json.RawMessage(`{"offset":2}`), Cursor: json.RawMessage(`{"after":"second"}`)})
					rejected <- err
					return err
				}
				<-ctx.Done()
				return ctx.Err()
			}
			runtime.tick()
			select {
			case <-accepted:
			case err := <-rejected:
				t.Fatal(err)
			case <-time.After(3 * time.Second):
				t.Fatal("collector did not persist its initial checkpoint")
			}
			before, err := db.Get(watch.Spec.ID)
			if err != nil {
				t.Fatal(err)
			}
			taskRunsMaintenanceMarker(t, h.mgr, incomingFrame)
			if incomingFrame {
				close(tryFrame)
				select {
				case err := <-rejected:
					if err == nil {
						t.Fatal("maintenance acknowledged a new cursor")
					}
				case <-time.After(3 * time.Second):
					t.Fatal("maintenance did not reject the in-flight frame")
				}
			}
			sentinelResilienceAwait(t, "collector quiescence", func() bool {
				runtime.tick()
				runtime.mu.Lock()
				defer runtime.mu.Unlock()
				return len(runtime.workers) == 0
			})
			for i := 0; i < 3; i++ {
				runtime.tick()
			}
			after, err := db.Get(watch.Spec.ID)
			if err != nil || !after.Spec.Enabled || after.Spec.Generation != before.Spec.Generation || !reflect.DeepEqual(after.State, before.State) || !reflect.DeepEqual(after.Cursor, before.Cursor) || after.Failures != before.Failures || after.NextAt != before.NextAt || after.Error != before.Error || calls.Load() != 1 || sentinelModelCalls(captured) != 0 {
				t.Fatalf("maintenance changed responsibility/checkpoint/failure: before=%+v after=%+v calls=%d err=%v", before, after, calls.Load(), err)
			}
			if err := os.Remove(h.mgr.releaseMaintenancePath()); err != nil {
				t.Fatal(err)
			}
			runtime.tick()
			select {
			case cfg := <-resumed:
				if !reflect.DeepEqual(cfg.State, checkpoint.State) || !reflect.DeepEqual(cfg.Cursor, checkpoint.Cursor) {
					t.Fatalf("collector did not resume committed checkpoint: %+v", cfg)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("enabled collector did not resume after maintenance")
			}
		})
	}
}

func TestSentinelMaintenanceBlocksNewBatchAdmission(t *testing.T) {
	h, captured := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	paused := sentinelTestCreate(t, h, s)
	db, watch := sentinelResilienceEnabled(t, h, paused)
	release := taskRunsHoldSlots(t, h.mgr)
	defer release()
	if err := db.Accept(watch.Spec.ID, watch.Spec.Generation, sentinel.Frame{V: 1, ID: "observed-before-maintenance", Events: []sentinel.InputEvent{{Key: "unchanged-event", Summary: "retained observation"}}, Cursor: json.RawMessage(`{"after":1}`)}); err != nil {
		t.Fatal(err)
	}
	taskRunsMaintenanceMarker(t, h.mgr, false)
	h.mgr.sentinels.dispatch(db, watch)
	history, err := db.ListBatches(watch.Spec.ID, 10)
	if err != nil || len(history) != 0 || sentinelModelCalls(captured) != 0 {
		t.Fatalf("maintenance admitted a new batch: %+v err=%v", history, err)
	}
	retained, err := db.Get(watch.Spec.ID)
	if err != nil || retained.Pending != 1 || !retained.Spec.Enabled {
		t.Fatalf("maintenance dropped pending observation: %+v %v", retained, err)
	}
	if err := os.Remove(h.mgr.releaseMaintenancePath()); err != nil {
		t.Fatal(err)
	}
	h.mgr.sentinels.dispatch(db, retained)
	history, err = db.ListBatches(watch.Spec.ID, 10)
	if err != nil || len(history) != 1 || history[0].Status != "queued" || sentinelModelCalls(captured) != 0 {
		t.Fatalf("retained batch did not enter shared queue: %+v err=%v", history, err)
	}
	// This test checks admission only. Keep the queued source paused while
	// releasing the test capacity reservations during cleanup.
	if _, err := db.SetEnabled(watch.Spec.ID, false); err != nil {
		t.Fatal(err)
	}
}

func TestSentinelMaintenanceDuringDebounceRetainsUnstartedExecution(t *testing.T) {
	h, captured := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	paused := sentinelTestCreate(t, h, s)
	db, err := h.mgr.sentinels.database(true)
	if err != nil {
		t.Fatal(err)
	}
	spec := paused.Spec
	spec.ID, spec.DebounceMS = "", 50
	paused, err = db.Create(spec)
	if err != nil {
		t.Fatal(err)
	}
	db, watch := sentinelResilienceEnabled(t, h, paused)
	if err := db.Accept(watch.Spec.ID, watch.Spec.Generation, sentinel.Frame{V: 1, ID: "debounced-observation", Events: []sentinel.InputEvent{{Key: "debounced-event", Summary: "not yet investigated"}}}); err != nil {
		t.Fatal(err)
	}
	batch, err := db.ClaimBatch(watch.Spec.ID, sentinel.MaxBatchEvents)
	if err != nil || batch == nil {
		t.Fatalf("claim native batch: %+v %v", batch, err)
	}
	if err := db.QueueBatch(batch.ID); err != nil {
		t.Fatal(err)
	}
	// Hold automatic pumping while constructing the boundary case. The
	// request below is then claimed from the real queue explicitly.
	taskRunsMaintenanceMarker(t, h.mgr, false)
	run, _, err := h.mgr.taskRuns.enqueueSentinel(watch, *batch, "")
	if err != nil {
		t.Fatal(err)
	}
	queue := taskRunsDB(t, h.mgr)
	claimed, err := queue.Claim(run.ID, taskRunConcurrency)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(h.mgr.releaseMaintenancePath()); err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithCancel(h.mgr.taskRuns.ctx)
	ctx := &taskRunsMaintenanceWaitContext{Context: parent, onWait: func() { taskRunsMaintenanceMarker(t, h.mgr, false) }}
	h.mgr.taskRuns.mu.Lock()
	h.mgr.taskRuns.active[claimed.ID] = taskExecution{run: claimed, cancel: cancel}
	h.mgr.taskRuns.wg.Add(1)
	h.mgr.taskRuns.mu.Unlock()
	// The generic slot has started, but the source's explicit debounce is
	// still preparation. Maintenance arriving here must not call a model.
	h.mgr.taskRuns.execute(ctx, queue, claimed)
	returned := taskRunsAwait(t, queue, run.ID, taskqueue.Queued)
	native, err := db.GetBatch(batch.ID)
	if err != nil || native.Status != "retry_pending" || native.RunID != "" || native.Attempt != batch.Attempt || native.SessionID != "" || returned.Attempt != claimed.Attempt || returned.StartedAt != 0 || returned.SessionID != "" || returned.Revision <= claimed.Revision || sentinelModelCalls(captured) != 0 {
		t.Fatalf("maintenance consumed unstarted debounce: native=%+v queue=%+v modelCalls=%d err=%v", native, returned, sentinelModelCalls(captured), err)
	}
	current, err := db.Get(watch.Spec.ID)
	if err != nil || !current.Spec.Enabled || current.Spec.Generation != watch.Spec.Generation || current.Spec.SessionID != "" {
		t.Fatalf("debounce maintenance mutated responsibility/session: %+v err=%v", current, err)
	}
	rows, err := queue.List(taskqueue.Filter{TaskID: run.TaskID})
	if err != nil || len(rows) != 1 || rows[0].ID != run.ID {
		t.Fatalf("maintenance created a replacement execution/attempt: %+v err=%v", rows, err)
	}
}
