package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/yosukeno/pogo/sentinel"
	"github.com/yosukeno/pogo/sentinelexec"
)

// These tests drive the real controller and durable Store with controlled
// runner callbacks. They deliberately do not contact an external model/source.
func sentinelResilienceAwait(t *testing.T, description string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for " + description)
}

// Hold only the scheduler slots, not Store admission. Tests can exercise the
// actual collection callback without allowing pending events to invoke a model.
func sentinelResilienceHold(r *sentinelRuntime, w sentinel.Watch) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.workers[w.Spec.ID] = &sentinelWorker{generation: w.Spec.Generation, mode: w.Spec.Mode, cancel: func() {}, done: make(chan struct{})}
	r.runs[w.Spec.ID] = true
}

func sentinelResilienceEnabled(t *testing.T, h *harness, w sentinel.Watch) (*sentinel.Store, sentinel.Watch) {
	t.Helper()
	db, err := h.mgr.sentinels.database(true)
	if err != nil {
		t.Fatal(err)
	}
	// Install the reservation before enabling, so no real source command or
	// fake model can race a one-second background tick.
	w.Spec.Generation++
	sentinelResilienceHold(h.mgr.sentinels, w)
	w, err = db.SetEnabled(w.Spec.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	return db, w
}

// This is an actual graceful controller reconstruction against the same disk
// state, not a SIGKILL/power-loss simulation. NewManager starts a fresh runtime;
// its normal database open owns Recover and generation fencing.
func sentinelResilienceRestart(t *testing.T, h *harness, id string) (*sentinelRuntime, *sentinel.Store, sentinel.Watch) {
	t.Helper()
	cfg := h.mgr.cfg
	h.mgr.mu.Lock()
	valid := h.mgr.scheduleOwnerValid
	h.mgr.mu.Unlock()
	h.mgr.Close()
	mgr, err := NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	r := mgr.sentinels
	db, err := r.database(false)
	if err != nil || db == nil {
		t.Fatalf("restarted controller did not reopen durable observations: db=%v err=%v", db, err)
	}
	w, err := db.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	sentinelResilienceHold(r, w)
	// Arm the same test owner only after scheduler slots are reserved.
	mgr.mu.Lock()
	mgr.scheduleOwnerValid = valid
	mgr.mu.Unlock()
	return r, db, w
}

func sentinelResilienceCollectFrame(t *testing.T, r *sentinelRuntime, db *sentinel.Store, w sentinel.Watch, frame sentinel.Frame) error {
	t.Helper()
	var admissionErr error
	r.mu.Lock()
	r.runner = func(_ context.Context, _ sentinelexec.Config, accept func(sentinel.Frame) error) error {
		admissionErr = accept(frame)
		return admissionErr
	}
	r.mu.Unlock()
	r.collect(context.Background(), db, w)
	return admissionErr
}

func TestSentinelResilienceDueTransientFailureRetriesAfterBackoff(t *testing.T) {
	h, capture := newSentinelHarness(t, nil)
	session := sentinelTestSession(t, h)
	watch := sentinelTestCreate(t, h, session)
	db, watch := sentinelResilienceEnabled(t, h, watch)
	var err error
	runtime := h.mgr.sentinels
	// Reserve this source's collector slot while explicitly advancing five
	// attempts. This avoids depending on wall-clock exponential delays, without
	// replacing the actual failure-accounting path in collect.
	runtime.mu.Lock()
	runtime.workers[watch.Spec.ID] = &sentinelWorker{generation: watch.Spec.Generation, mode: watch.Spec.Mode, cancel: func() {}, done: make(chan struct{})}
	runtime.runner = func(context.Context, sentinelexec.Config, func(sentinel.Frame) error) error {
		return errors.New("temporary upstream connection loss")
	}
	runtime.mu.Unlock()
	for attempt := 1; attempt <= 5; attempt++ {
		watch, err = db.Get(watch.Spec.ID)
		if err != nil {
			t.Fatal(err)
		}
		runtime.collect(context.Background(), db, watch)
		current, err := db.Get(watch.Spec.ID)
		if err != nil || current.Failures != attempt || current.Status != "error" || !current.Spec.Enabled {
			t.Fatalf("failure attempt was not recorded: attempt=%d watch=%+v err=%v", attempt, current, err)
		}
	}
	watch, err = db.Get(watch.Spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Minute).UnixMilli()
	if err := db.SetRuntimeGeneration(watch.Spec.ID, watch.Spec.Generation, "error", watch.Error, future, watch.Failures); err != nil {
		t.Fatal(err)
	}
	runtime, db, watch = sentinelResilienceRestart(t, h, watch.Spec.ID)
	if watch.Failures != 5 || watch.NextAt != future {
		t.Fatalf("controller reconstruction lost the scheduled retry: %+v", watch)
	}
	premature := make(chan struct{}, 1)
	runtime.mu.Lock()
	delete(runtime.workers, watch.Spec.ID)
	runtime.runner = func(context.Context, sentinelexec.Config, func(sentinel.Frame) error) error {
		premature <- struct{}{}
		return errors.New("scheduled retry was early")
	}
	runtime.mu.Unlock()
	runtime.tick()
	select {
	case <-premature:
		t.Fatal("collector retried before its durable backoff deadline")
	case <-time.After(100 * time.Millisecond):
	}
	if current, err := db.Get(watch.Spec.ID); err != nil || current.NextAt != future || current.Failures != 5 {
		t.Fatalf("future retry changed without collection: %+v err=%v", current, err)
	}
	recovered := make(chan struct{}, 1)
	runtime.mu.Lock()
	delete(runtime.workers, watch.Spec.ID)
	runtime.runner = func(_ context.Context, _ sentinelexec.Config, accept func(sentinel.Frame) error) error {
		if err := accept(sentinel.Frame{V: 1, ID: "healthy-after-disconnect", Events: []sentinel.InputEvent{}, State: json.RawMessage(`{"healthy":true}`), Cursor: json.RawMessage(`{"sequence":1}`)}); err != nil {
			return err
		}
		select {
		case recovered <- struct{}{}:
		default:
		}
		return nil
	}
	runtime.mu.Unlock()
	// Install the recovered source before advancing its due time, so even a
	// concurrent background tick must execute the intended healthy attempt.
	// Source identity, cursor and failure streak remain unchanged.
	if err := db.SetRuntimeGeneration(watch.Spec.ID, watch.Spec.Generation, "error", watch.Error, time.Now().Add(-time.Second).UnixMilli(), watch.Failures); err != nil {
		t.Fatal(err)
	}
	runtime.tick()
	select {
	case <-recovered:
	case <-time.After(1500 * time.Millisecond):
		current, _ := db.Get(watch.Spec.ID)
		t.Fatalf("enabled transient source never retried after its backoff expired: %+v", current)
	}
	sentinelResilienceAwait(t, "healthy status after retry", func() bool {
		current, err := db.Get(watch.Spec.ID)
		return err == nil && current.Failures == 0 && current.Status == "waiting" && current.Error == ""
	})
	if sentinelModelCalls(capture) != 0 {
		t.Fatal("empty recovery checkpoint woke a model")
	}
}

func TestSentinelResilienceControllerRestartPreservesCursorQueueAndDedup(t *testing.T) {
	h, capture := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	db, before := sentinelResilienceEnabled(t, h, sentinelTestCreate(t, h, s))
	initial := sentinel.Frame{V: 1, ID: "committed-before-restart", Events: []sentinel.InputEvent{
		{Key: "source:1", Summary: "first committed observation", Data: json.RawMessage(`{"version":1}`)},
		{Key: "source:2", Summary: "second committed observation", Data: json.RawMessage(`{"version":2}`)},
	}, State: json.RawMessage(`{"baseline":[1,2]}`), Cursor: json.RawMessage(`{"sequence":2}`)}
	if err := sentinelResilienceCollectFrame(t, h.mgr.sentinels, db, before, initial); err != nil {
		t.Fatal(err)
	}
	frozen, err := db.ClaimBatch(before.Spec.ID, 1)
	if err != nil || frozen == nil || len(frozen.Events) != 1 {
		t.Fatalf("could not prepare an admitted wake for restart: %+v err=%v", frozen, err)
	}
	if err = db.SetBatchRun(frozen.ID, "retained-sentinel-session", "unknown-prior-run", "running"); err != nil {
		t.Fatal(err)
	}
	r, reopened, after := sentinelResilienceRestart(t, h, before.Spec.ID)
	if !after.Spec.Enabled || after.Spec.Generation <= before.Spec.Generation || after.Pending != 2 || string(after.State) != string(initial.State) || string(after.Cursor) != string(initial.Cursor) {
		t.Fatalf("restart lost committed source position or queue: before=%+v after=%+v", before, after)
	}
	if err = reopened.Accept(after.Spec.ID, before.Spec.Generation, sentinel.Frame{V: 1, ID: "late-old-collector", Events: []sentinel.InputEvent{{Key: "source:late", Summary: "must not commit"}}}); !errors.Is(err, sentinel.ErrStaleGeneration) {
		t.Fatalf("retired controller could still commit: %v", err)
	}
	interrupted, err := reopened.GetBatch(frozen.ID)
	if err != nil || interrupted.Status != "interrupted" || interrupted.RunID != "unknown-prior-run" || interrupted.SessionID != "retained-sentinel-session" || !reflect.DeepEqual(interrupted.Events, frozen.Events) {
		t.Fatalf("restart did not preserve the ambiguous wake identity/events: %+v err=%v", interrupted, err)
	}
	if claimed, err := reopened.ClaimBatch(after.Spec.ID, 32); err != nil || claimed != nil {
		t.Fatalf("an ambiguous prior run was automatically replayed: %+v err=%v", claimed, err)
	}
	// Stable frame receipts and source event identities survive a controller
	// restart. Replayed observations are deduplicated, while the first truly
	// new observation after recovery must not be mistaken for a fresh baseline.
	if err = reopened.Accept(after.Spec.ID, after.Spec.Generation, initial); err != nil {
		t.Fatal(err)
	}
	resumedFrame := sentinel.Frame{V: 1, ID: "new-after-restart", Events: []sentinel.InputEvent{
		initial.Events[1], {Key: "source:3", Summary: "first new observation after recovery", Data: json.RawMessage(`{"version":3}`)},
	}, State: json.RawMessage(`{"baseline":[1,2,3]}`), Cursor: json.RawMessage(`{"sequence":3}`)}
	var restoredState, restoredCursor json.RawMessage
	r.mu.Lock()
	r.runner = func(_ context.Context, cfg sentinelexec.Config, accept func(sentinel.Frame) error) error {
		restoredState, restoredCursor = cfg.State, cfg.Cursor
		return accept(resumedFrame)
	}
	r.mu.Unlock()
	r.collect(context.Background(), reopened, after)
	if string(restoredState) != string(initial.State) || string(restoredCursor) != string(initial.Cursor) {
		t.Fatalf("restarted collector did not receive its persisted position: state=%s cursor=%s", restoredState, restoredCursor)
	}
	if err = reopened.Accept(after.Spec.ID, after.Spec.Generation, resumedFrame); err != nil {
		t.Fatal(err)
	}
	current, err := reopened.Get(after.Spec.ID)
	if err != nil || current.Pending != 3 || string(current.Cursor) != string(resumedFrame.Cursor) || string(current.State) != string(resumedFrame.State) {
		t.Fatalf("replay duplicated events or new recovery observation was lost: %+v err=%v", current, err)
	}
	if err = reopened.RetryBatch(frozen.ID); err != nil {
		t.Fatal(err)
	}
	retried, err := reopened.ClaimBatch(after.Spec.ID, 32)
	if err != nil || retried == nil || retried.ID != frozen.ID || retried.Attempt != 2 || retried.RunID != "" || !reflect.DeepEqual(retried.Events, frozen.Events) {
		t.Fatalf("explicit retry lost frozen membership or logical wake identity: %+v err=%v", retried, err)
	}
	if err = reopened.SettleBatch(retried.ID, "ignore", "controlled recovery was verified", 0); err != nil {
		t.Fatal(err)
	}
	next, err := reopened.ClaimBatch(after.Spec.ID, 32)
	if err != nil || next == nil || len(next.Events) != 2 || next.Events[0].Key != "source:2" || next.Events[1].Key != "source:3" {
		t.Fatalf("recovery observations did not remain in the next wake: %+v err=%v", next, err)
	}
	if sentinelModelCalls(capture) != 0 {
		t.Fatal("restart/dedup validation invoked a model")
	}
}

func TestSentinelResilienceFullQueueRejectsCursorAndCanReplayAfterRestart(t *testing.T) {
	h, capture := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	db, w := sentinelResilienceEnabled(t, h, sentinelTestCreate(t, h, s))
	r := h.mgr.sentinels
	for start := 0; start < sentinel.MaxPendingEvents-1; start += 64 {
		end := min(start+64, sentinel.MaxPendingEvents-1)
		frame := sentinel.Frame{V: 1, ID: fmt.Sprintf("seed:%d", start), State: json.RawMessage(fmt.Sprintf(`{"accepted":%d}`, end)), Cursor: json.RawMessage(fmt.Sprintf(`{"sequence":%d}`, end))}
		for n := start; n < end; n++ {
			frame.Events = append(frame.Events, sentinel.InputEvent{Key: fmt.Sprintf("seed-event:%d", n), Summary: "bounded controlled observation"})
		}
		if err := sentinelResilienceCollectFrame(t, r, db, w, frame); err != nil {
			t.Fatal(err)
		}
	}
	w, err := db.Get(w.Spec.ID)
	if err != nil || w.Pending != sentinel.MaxPendingEvents-1 {
		t.Fatalf("controlled queue fill failed: %+v err=%v", w, err)
	}
	// The first event would fit, the second would not. Admission must reject
	// the whole frame, without an ACK/receipt or an advanced durable cursor.
	rejected := sentinel.Frame{V: 1, ID: "unacknowledged-overflow", Events: []sentinel.InputEvent{
		{Key: "overflow:1", Summary: "first event would fit"},
		{Key: "overflow:2", Summary: "second event exceeds pending capacity"},
	}, State: json.RawMessage(`{"accepted":1025}`), Cursor: json.RawMessage(`{"sequence":1025}`)}
	if err = sentinelResilienceCollectFrame(t, r, db, w, rejected); !errors.Is(err, sentinel.ErrQueueFull) {
		t.Fatalf("collector received an ACK or wrong rejection at full queue: %v", err)
	}
	requireUnadvanced := func(db *sentinel.Store, id string) sentinel.Watch {
		t.Helper()
		current, err := db.Get(id)
		if err != nil || current.Pending != sentinel.MaxPendingEvents-1 || string(current.State) != string(w.State) || string(current.Cursor) != string(w.Cursor) {
			t.Fatalf("rejected frame partially committed or advanced source position: %+v err=%v", current, err)
		}
		return current
	}
	requireUnadvanced(db, w.Spec.ID)
	r, db, recovered := sentinelResilienceRestart(t, h, w.Spec.ID)
	requireUnadvanced(db, recovered.Spec.ID)
	// Host-confirmed settlement creates capacity. A source may now resend the
	// exact unacknowledged proposal; restart must not leave a false receipt.
	settled, err := db.ClaimBatch(recovered.Spec.ID, 2)
	if err != nil || settled == nil || len(settled.Events) != 2 {
		t.Fatalf("could not free controlled queue capacity: %+v err=%v", settled, err)
	}
	if err = db.SettleBatch(settled.ID, "ignore", "controlled capacity recovery", 0); err != nil {
		t.Fatal(err)
	}
	if err = sentinelResilienceCollectFrame(t, r, db, recovered, rejected); err != nil {
		t.Fatalf("unacknowledged source proposal could not be replayed after capacity recovery: %v", err)
	}
	if err = db.Accept(recovered.Spec.ID, recovered.Spec.Generation, rejected); err != nil {
		t.Fatal(err)
	}
	current, err := db.Get(recovered.Spec.ID)
	if err != nil || current.Pending != sentinel.MaxPendingEvents-1 || string(current.State) != string(rejected.State) || string(current.Cursor) != string(rejected.Cursor) || current.Failures != 0 {
		t.Fatalf("replayed frame was not committed exactly once after recovery: %+v err=%v", current, err)
	}
	// Stable event identities deduplicate across a different frame too, even
	// after the first two seed events were settled before the restart replay.
	if err = db.Accept(recovered.Spec.ID, recovered.Spec.Generation, sentinel.Frame{V: 1, ID: "new-frame-same-source-events", Events: append(rejected.Events, sentinel.InputEvent{Key: "seed-event:0", Summary: "bounded controlled observation"})}); err != nil {
		t.Fatal(err)
	}
	if final, err := db.Get(recovered.Spec.ID); err != nil || final.Pending != current.Pending {
		t.Fatalf("same source events returned to the queue after settlement/replay: %+v err=%v", final, err)
	}
	if sentinelModelCalls(capture) != 0 {
		t.Fatal("queue backpressure/recovery invoked a model")
	}
}

func TestSentinelResilienceCorruptSealedBundleIsNotAutomaticallyRetried(t *testing.T) {
	h, capture := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	db, w := sentinelResilienceEnabled(t, h, sentinelTestCreate(t, h, s))
	r := h.mgr.sentinels
	entry := filepath.Join(w.Spec.ScriptDir, "main.py")
	originalCode, err := os.ReadFile(entry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(entry, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry, []byte("# corrupted immutable source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	executed := make(chan struct{}, 1)
	r.mu.Lock()
	r.runner = func(context.Context, sentinelexec.Config, func(sentinel.Frame) error) error {
		select {
		case executed <- struct{}{}:
		default:
		}
		return nil
	}
	r.mu.Unlock()
	r.collect(context.Background(), db, w)
	failed, err := db.Get(w.Spec.ID)
	if err != nil || failed.Failures < 5 || failed.NextAt != 0 || failed.Status != "error" || failed.Error == "" {
		t.Fatalf("corrupt sealed code was not durably stopped: %+v err=%v", failed, err)
	}
	// Restore the exact trusted bytes out of band. Otherwise a broken tick
	// could keep scheduling collect, fail seal verification again, and still
	// appear safe merely because it never reached the runner. Fatal admission
	// must remain stopped until an explicit control action even after repair.
	if err = os.WriteFile(entry, originalCode, 0400); err != nil {
		t.Fatal(err)
	}
	if err = verifySentinelBundle(w.Spec.ScriptDir); err != nil {
		t.Fatal(err)
	}
	r, db, w = sentinelResilienceRestart(t, h, w.Spec.ID)
	r.mu.Lock()
	delete(r.workers, w.Spec.ID)
	r.runner = func(context.Context, sentinelexec.Config, func(sentinel.Frame) error) error {
		select {
		case executed <- struct{}{}:
		default:
		}
		return nil
	}
	r.mu.Unlock()
	r.tick()
	select {
	case <-executed:
		t.Fatal("a corrupt sealed source reached the runner or was auto retried after restart")
	case <-time.After(100 * time.Millisecond):
	}
	if current, err := db.Get(w.Spec.ID); err != nil || current.Failures < 5 || current.NextAt != 0 || current.Error != failed.Error {
		t.Fatalf("fatal seal failure was reset by scheduler/reconstruction: %+v err=%v", current, err)
	}
	if sentinelModelCalls(capture) != 0 {
		t.Fatal("fatal source validation invoked a model")
	}
}

func TestSentinelResiliencePauseAndGenerationFenceInFlightCallbacks(t *testing.T) {
	h, capture := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	db, original := sentinelResilienceEnabled(t, h, sentinelTestCreate(t, h, s))
	r := h.mgr.sentinels
	callback := make(chan func(sentinel.Frame) error, 1)
	release := make(chan struct{})
	finished := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	r.runner = func(ctx context.Context, _ sentinelexec.Config, accept func(sentinel.Frame) error) error {
		callback <- accept
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	r.mu.Unlock()
	go func() {
		defer close(finished)
		r.collect(ctx, db, original)
	}()
	defer func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("controlled retired collector did not drain")
		}
	}()
	var accept func(sentinel.Frame) error
	select {
	case accept = <-callback:
	case <-time.After(time.Second):
		t.Fatal("controlled collector did not start")
	}
	paused, err := db.SetEnabled(original.Spec.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	late := sentinel.Frame{V: 1, ID: "late-after-control", Events: []sentinel.InputEvent{{Key: "late:1", Summary: "must not reach a retired collector"}}, State: json.RawMessage(`{"late":true}`), Cursor: json.RawMessage(`{"sequence":99}`)}
	if err = accept(late); !errors.Is(err, sentinel.ErrStaleGeneration) {
		t.Fatalf("paused collector callback committed: %v", err)
	}
	if err = db.Accept(paused.Spec.ID, paused.Spec.Generation, late); !errors.Is(err, sentinel.ErrDisabled) {
		t.Fatalf("paused source could admit a fresh-generation frame: %v", err)
	}
	resumed, err := db.SetEnabled(original.Spec.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	sentinelResilienceHold(r, resumed)
	if err = accept(late); !errors.Is(err, sentinel.ErrStaleGeneration) {
		t.Fatalf("old collector callback committed after resume: %v", err)
	}
	if err = db.SetRuntimeGeneration(original.Spec.ID, original.Spec.Generation, "error", "stale cleanup", 0, 5); !errors.Is(err, sentinel.ErrStaleGeneration) {
		t.Fatalf("retired collector replaced new controller runtime state: %v", err)
	}
	// An old scheduler snapshot must not even invoke the runner after resume.
	r.collect(context.Background(), db, original)
	select {
	case <-callback:
		t.Fatal("retired scheduler snapshot reached the runner")
	default:
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("old collector did not exit after controlled release")
	}
	current, err := db.Get(resumed.Spec.ID)
	if err != nil || current.Pending != 0 || len(current.Cursor) != 0 || len(current.State) != 0 || current.Spec.Generation != resumed.Spec.Generation || current.Status != "starting" || current.Failures != 0 {
		t.Fatalf("retired callback/cleanup changed resumed source state: %+v err=%v", current, err)
	}
	if sentinelModelCalls(capture) != 0 {
		t.Fatal("pause/generation fencing invoked a model")
	}
}
