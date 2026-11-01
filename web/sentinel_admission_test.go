package web

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/mcpobserve"
)

// Seed a real persisted history, then arm the actual pre-run compaction check.
// Admission regressions must not be hidden by a fresh/empty conversation.
func sentinelArmCompaction(t *testing.T, s *Session) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	ms := []llm.Message{llm.UserText("Previously assigned standing instruction."), {Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: strings.Repeat("retained context ", 2000)}}}}
	s.agent.SetMessages(ms)
	s.agent.SetCompact(agent.CompactConfig{Threshold: 1})
	if err := s.persistMessagesLocked(ms); err != nil {
		t.Fatal(err)
	}
	if !s.agent.ShouldCompact() {
		t.Fatal("test history did not arm pre-run compaction")
	}
}

type sentinelAdmissionClient struct {
	stream func(context.Context) (llm.Response, error)
}

func (*sentinelAdmissionClient) Model() string { return "k3" }
func (c *sentinelAdmissionClient) Stream(ctx context.Context, _ string, _ []llm.Message, _ []llm.ToolSchema, _ func(llm.Delta)) (llm.Response, error) {
	return c.stream(ctx)
}

func TestSentinelPreparingReceiptFailureBlocksAutoCompaction(t *testing.T) {
	h, cap := newSentinelHarness(t, []llm.Response{textTurn("must never summarize")})
	s := sentinelTestSession(t, h)
	sentinelArmCompaction(t, s)
	input := sentinelTestWake("wake_preparing_write_failure")
	b := &sentinelWakeBinding{s: s, input: input, hash: "fixture-hash"}
	// A regular file occupying the parent path fails independently of chmod
	// behavior or root privileges, at the real receipt writer inside prepare.
	badParent := filepath.Join(h.mgr.cfg.SessionDir, "sentinels", "receipts", s.ID)
	if err := os.MkdirAll(filepath.Dir(badParent), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badParent, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := s.startPreparedInput("new sentinel observation", nil, nil, "", "", mcpobserve.Metadata{}, "", context.Background(), func() error { return b.admission("preparing") })
	if err == nil {
		t.Fatal("failed preparing receipt did not reject admission")
	}
	if n := sentinelModelCalls(cap); n != 0 {
		t.Fatalf("failed receipt allowed %d provider calls before admission", n)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil || s.compacting || !s.agent.ShouldCompact() {
		t.Fatal("failed admission started work or replaced the retained history")
	}
}

func TestSentinelParentDeadlineBoundsPreRunCompaction(t *testing.T) {
	h, _ := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	sentinelArmCompaction(t, s)
	input := sentinelTestWake("wake_compaction_deadline")
	var calls atomic.Int32
	var sawPreparing atomic.Bool
	var sawDeadline atomic.Bool
	client := &sentinelAdmissionClient{stream: func(ctx context.Context) (llm.Response, error) {
		if calls.Add(1) != 1 {
			return llm.Response{}, errors.New("substantive model call followed cancelled preparation")
		}
		if receipt, err := s.readWakeReceipt(input.ID); err == nil && receipt.Status == "preparing" && receipt.RunID == "" {
			sawPreparing.Store(true)
		}
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < time.Second {
			sawDeadline.Store(true)
		}
		<-ctx.Done()
		return llm.Response{}, ctx.Err()
	}}
	s.mu.Lock()
	s.agent.SetClient(client)
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	b, err := s.startSentinelWake(input, ctx)
	if b != nil && b.run != nil {
		sentinelWaitRun(t, b)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled preparation should reject starting the run: %v", err)
	}
	if time.Since(started) > 2*time.Second || calls.Load() != 1 || !sawPreparing.Load() || !sawDeadline.Load() {
		t.Fatalf("preparation boundary: calls=%d preparing=%v deadline=%v elapsed=%s", calls.Load(), sawPreparing.Load(), sawDeadline.Load(), time.Since(started))
	}
	// A preparing receipt is an interrupted attempt requiring reconciliation;
	// retrying the same wake ID may not silently repeat the compaction spend.
	if _, err := s.startSentinelWake(input); err == nil {
		t.Fatal("cancelled preparation was automatically replayed")
	}
}

func TestSentinelPausedOrRetiredGenerationBlocksPreCompaction(t *testing.T) {
	for _, mode := range []string{"paused", "retired", "owner_revoked"} {
		t.Run(mode, func(t *testing.T) {
			h, cap := newSentinelHarness(t, []llm.Response{textTurn("must not run")})
			s := sentinelTestSession(t, h)
			w := sentinelTestCreate(t, h, s)
			db, err := h.mgr.sentinels.database(true)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "paused" {
				w, err = db.SetEnabled(w.Spec.ID, true)
				if err != nil {
					t.Fatal(err)
				}
			}
			input := sentinelTestWake("wake_blocked_" + mode)
			input.WatchID, input.Generation = w.Spec.ID, w.Spec.Generation
			if mode == "retired" {
				if _, err := db.SetEnabled(w.Spec.ID, false); err != nil {
					t.Fatal(err)
				}
				if _, err := db.SetEnabled(w.Spec.ID, true); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "owner_revoked" {
				h.mgr.mu.Lock()
				h.mgr.scheduleOwnerValid = func(*identity.Scope) bool { return false }
				h.mgr.mu.Unlock()
			}
			sentinelArmCompaction(t, s)
			if _, err := s.startSentinelWake(input); err == nil {
				t.Fatal("paused/retired/revoked source admitted")
			}
			if n := sentinelModelCalls(cap); n != 0 {
				t.Fatalf("blocked source made %d pre-admission provider calls", n)
			}
		})
	}
}

func TestSentinelPauseDuringCompactionBlocksRunningAdmission(t *testing.T) {
	h, _ := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	w := sentinelTestCreate(t, h, s)
	db, err := h.mgr.sentinels.database(true)
	if err != nil {
		t.Fatal(err)
	}
	w, err = db.SetEnabled(w.Spec.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	input := sentinelTestWake("wake_paused_during_compact")
	input.WatchID, input.Generation = w.Spec.ID, w.Spec.Generation
	sentinelArmCompaction(t, s)
	var calls atomic.Int32
	client := &sentinelAdmissionClient{stream: func(context.Context) (llm.Response, error) {
		if calls.Add(1) != 1 {
			return llm.Response{}, errors.New("substantive model call followed a pause")
		}
		if _, err := db.SetEnabled(w.Spec.ID, false); err != nil {
			return llm.Response{}, err
		}
		return textTurn("Short retained summary."), nil
	}}
	s.mu.Lock()
	s.agent.SetClient(client)
	s.mu.Unlock()
	b, err := s.startSentinelWake(input)
	if err != nil {
		t.Fatal(err)
	}
	sentinelWaitRun(t, b)
	if calls.Load() != 1 {
		t.Fatalf("paused source made %d provider calls; wanted compaction only", calls.Load())
	}
	receipt, err := s.readWakeReceipt(input.ID)
	if err != nil || receipt.Status != "interrupted" || b.run.persistErr == nil {
		t.Fatalf("paused admission was not retained as interrupted: %+v %v runerr=%v", receipt, err, b.run.persistErr)
	}
}
