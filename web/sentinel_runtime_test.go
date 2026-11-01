package web

import (
	"context"
	"encoding/json"
	"github.com/yosukeno/pogo/sentinel"
	"github.com/yosukeno/pogo/sentinelexec"
	"strings"
	"testing"
	"time"
)

// Bind a host-created wake without contacting a model. This isolates decision
// durability from network/model behavior and exercises the real scoped backend.
func sentinelDecisionHarness(t *testing.T) (*sentinelToolBackend, *sentinelWakeBinding) {
	t.Helper()
	h, _ := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	run := &activeRun{id: "decision-run"}
	wake := &sentinelWakeBinding{s: s, input: sentinelWakeInput{ID: "decision-wake", WatchID: "watch", BatchID: "batch"}, hash: "saved-request-hash", run: run}
	run.wake = wake
	s.mu.Lock()
	s.active = run
	s.mu.Unlock()
	t.Cleanup(func() { s.mu.Lock(); s.active = nil; s.mu.Unlock() })
	return h.mgr.sentinelBackend(s).(*sentinelToolBackend), wake
}

func TestSentinelDeferRetryUsesSavedDeadline(t *testing.T) {
	b, wake := sentinelDecisionHarness(t)
	raw := json.RawMessage(`{"decision":"defer","summary":"wait for a meaningful change","defer_seconds":60}`)
	if _, err := b.Decide(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	before, err := b.s.readWakeReceipt(wake.input.ID)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err = b.Decide(context.Background(), raw); err != nil {
		t.Fatalf("identical retry rejected: %v", err)
	}
	after, err := b.s.readWakeReceipt(wake.input.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.DeferUntil != before.DeferUntil || after.DeferSeconds != 60 || after.Status != "decision_pending" {
		t.Fatalf("retry changed saved decision: before=%+v after=%+v", before, after)
	}
}
func TestSentinelDeferRetryRejectsDifferentInterval(t *testing.T) {
	b, wake := sentinelDecisionHarness(t)
	if _, err := b.Decide(context.Background(), json.RawMessage(`{"decision":"defer","summary":"later","defer_seconds":60}`)); err != nil {
		t.Fatal(err)
	}
	_, err := b.Decide(context.Background(), json.RawMessage(`{"decision":"defer","summary":"later","defer_seconds":120}`))
	if err == nil || !strings.Contains(err.Error(), "不同") {
		t.Fatalf("conflicting defer accepted: %v", err)
	}
	after, err := b.s.readWakeReceipt(wake.input.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.DeferSeconds != 60 {
		t.Fatal("conflict replaced original decision")
	}
}

func TestSentinelPauseDuringDebounceRetainsExistingSessionBatch(t *testing.T) {
	h, cap := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	paused := sentinelTestCreate(t, h, s)
	db, err := h.mgr.sentinels.database(true)
	if err != nil {
		t.Fatal(err)
	}
	spec := paused.Spec
	spec.ID = ""
	spec.Enabled = true
	spec.DebounceMS = 100
	spec.SessionID = s.ID
	w, err := db.Create(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Accept(w.Spec.ID, w.Spec.Generation, sentinel.Frame{V: 1, ID: "before-pause", Events: []sentinel.InputEvent{{Key: "e1", Summary: "change"}}}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); h.mgr.sentinels.dispatch(db, w) }()
	var batch sentinel.Batch
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		history, e := db.ListBatches(w.Spec.ID, 1)
		if e != nil {
			t.Fatal(e)
		}
		if len(history) > 0 {
			batch = history[0]
			break
		}
		time.Sleep(time.Millisecond)
	}
	if batch.ID == "" {
		t.Fatal("dispatch did not claim a batch")
	}
	if _, err = db.SetEnabled(w.Spec.ID, false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("paused dispatch did not return")
	}
	got, err := db.GetBatch(batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if (got.Status != "queued" && got.Status != "retry_pending") || got.SessionID != s.ID || got.Attempt != 1 {
		t.Fatalf("paused batch stuck or changed identity: %+v", got)
	}
	if sentinelModelCalls(cap) != 0 {
		t.Fatal("paused watch called a model")
	}
}

func TestSentinelRetiredSchedulerSnapshotNeverStartsCollector(t *testing.T) {
	h, _ := newSentinelHarness(t, nil)
	session := sentinelTestSession(t, h)
	watch := sentinelTestCreate(t, h, session)
	db, err := h.mgr.sentinels.database(true)
	if err != nil {
		t.Fatal(err)
	}
	watch, err = db.SetEnabled(watch.Spec.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.SetEnabled(watch.Spec.ID, false); err != nil {
		t.Fatal(err)
	}
	called := false
	runtime := newSentinelRuntime(h.mgr)
	defer runtime.cancel()
	runtime.runner = func(context.Context, sentinelexec.Config, func(sentinel.Frame) error) error {
		called = true
		return nil
	}
	runtime.collect(context.Background(), db, watch)
	if called {
		t.Fatal("retired scheduler snapshot launched paused script")
	}
}
