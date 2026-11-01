package web

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/sentinel"
	"github.com/yosukeno/pogo/sentinelexec"
	"github.com/yosukeno/pogo/tools"
)

func TestSentinelHostPersistsRelativeDeadlineAndRegistrationReceipt(t *testing.T) {
	h, _ := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	script := sentinelTestBundle(t, s)
	before := time.Now().Add(10 * time.Minute).UnixMilli()
	result, err := (&tools.Watch{Backend: h.mgr.sentinelBackend(s)}).Execute(context.Background(), json.RawMessage(studioBody(map[string]any{"action": "create", "title": "bounded", "task": "observe custom source", "mode": "poll", "script_path": script, "stop_after_seconds": 600})))
	if err != nil {
		t.Fatal(err)
	}
	var w sentinel.Watch
	if err = json.Unmarshal([]byte(result.Text), &w); err != nil {
		t.Fatal(err)
	}
	after := time.Now().Add(10 * time.Minute).UnixMilli()
	if w.Spec.ExpiresAt < before || w.Spec.ExpiresAt > after || w.Spec.Enabled {
		t.Fatalf("host lifetime not persisted: %+v", w.Spec)
	}
	db, err := h.mgr.sentinels.database(false)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := db.Get(w.Spec.ID)
	if err != nil || saved.Spec.ExpiresAt != w.Spec.ExpiresAt {
		t.Fatalf("deadline not durable: %+v %v", saved, err)
	}
	if !strings.HasPrefix(result.Text, `{"notice":`) || result.Details.(map[string]any)["background_handoff"] == nil {
		t.Fatal("handoff missing")
	}
}

func TestSentinelDeadlineTickRetiresWorkDuringMaintenance(t *testing.T) {
	for _, mode := range []string{"poll", "stream"} {
		t.Run(mode, func(t *testing.T) {
			h, cap := newSentinelHarness(t, nil)
			s := sentinelTestSession(t, h)
			paused := sentinelTestCreate(t, h, s)
			r := h.mgr.sentinels
			db, err := r.database(false)
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(time.Hour).UnixMilli()
			r.clock.Store(deadline)
			p := paused.Spec
			p.ID = ""
			p.Enabled = true
			p.ExpiresAt = deadline
			p.Mode = mode
			if mode == "stream" {
				p.IntervalSeconds = 0
			}
			w, err := db.Create(p)
			if err != nil {
				t.Fatal(err)
			}
			if err = db.Accept(w.Spec.ID, w.Spec.Generation, sentinel.Frame{V: 1, ID: "confirmed", Events: []sentinel.InputEvent{{Key: "first", Summary: "known"}}, State: json.RawMessage(`{"position":7}`), Cursor: json.RawMessage(`{"position":7}`)}); err != nil {
				t.Fatal(err)
			}
			workerCtx, workerCancel := context.WithCancel(context.Background())
			defer workerCancel()
			runCtx, runCancel := context.WithCancel(context.Background())
			defer runCancel()
			r.mu.Lock()
			r.workers[w.Spec.ID] = &sentinelWorker{generation: w.Spec.Generation, mode: mode, cancel: workerCancel, done: make(chan struct{})}
			r.runCancels[w.Spec.ID] = runCancel
			r.mu.Unlock()
			if err = os.WriteFile(h.mgr.releaseMaintenancePath(), []byte(`{"token":"`+strings.Repeat("d", 64)+`"}`), 0600); err != nil {
				t.Fatal(err)
			}
			r.tick()
			got, err := db.Get(w.Spec.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Spec.Enabled || got.Status != "expired" || got.Pending != 1 || string(got.State) != `{"position":7}` || workerCtx.Err() == nil || runCtx.Err() == nil {
				t.Fatalf("deadline did not retire all work: %+v worker=%v run=%v", got, workerCtx.Err(), runCtx.Err())
			}
			if sentinelModelCalls(cap) != 0 {
				t.Fatal("expiry started a model")
			}
		})
	}
}

func TestSentinelDeadlineAdmissionRejectsBeforeSavingWake(t *testing.T) {
	h, _ := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	paused := sentinelTestCreate(t, h, s)
	r := h.mgr.sentinels
	db, err := r.database(false)
	if err != nil {
		t.Fatal(err)
	}
	p := paused.Spec
	p.ID = ""
	p.Enabled = true
	p.ExpiresAt = time.Now().Add(time.Hour).UnixMilli()
	w, err := db.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	r.clock.Store(w.Spec.ExpiresAt)
	b := &sentinelWakeBinding{s: s, input: sentinelWakeInput{ID: "past-deadline", WatchID: w.Spec.ID, Generation: w.Spec.Generation}}
	if err = b.admission("preparing"); !errors.Is(err, sentinel.ErrExpired) {
		t.Fatalf("late wake admitted: %v", err)
	}
	if _, err = s.readWakeReceipt(b.input.ID); !os.IsNotExist(err) {
		t.Fatalf("late admission saved a receipt: %v", err)
	}
	got, _ := db.Get(w.Spec.ID)
	if got.Spec.Enabled || got.Status != "expired" {
		t.Fatalf("late admission left watch enabled: %+v", got)
	}
}

func TestSentinelDeadlineCancelsCollectorWithoutAnotherTick(t *testing.T) {
	h, _ := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	paused := sentinelTestCreate(t, h, s)
	db, err := h.mgr.sentinels.database(false)
	if err != nil {
		t.Fatal(err)
	}
	p := paused.Spec
	p.ID = ""
	p.Enabled = true
	p.ExpiresAt = time.Now().Add(80 * time.Millisecond).UnixMilli()
	w, err := db.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	r := newSentinelRuntime(h.mgr)
	defer r.cancel()
	called := false
	r.runner = func(ctx context.Context, _ sentinelexec.Config, _ func(sentinel.Frame) error) error {
		called = true
		<-ctx.Done()
		return ctx.Err()
	}
	done := make(chan struct{})
	go func() { defer close(done); r.collect(context.Background(), db, w) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("collector did not stop at saved deadline")
	}
	if !called {
		t.Fatal("test did not exercise a running collector")
	}
	limit := time.Now().Add(time.Second)
	for {
		got, e := db.Get(w.Spec.ID)
		if e == nil && !got.Spec.Enabled && got.Status == "expired" {
			break
		}
		if time.Now().After(limit) {
			t.Fatalf("deadline state not saved: %+v %v", got, e)
		}
		time.Sleep(time.Millisecond)
	}
}
