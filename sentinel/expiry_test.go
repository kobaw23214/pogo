package sentinel

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func expiryStore(t *testing.T, enabled bool) (*Store, *int64, Watch) {
	t.Helper()
	s, _ := testStore(t)
	clock := int64(1000)
	s.clock = func() int64 { return clock }
	p := testSpec(t)
	p.Enabled = enabled
	p.ExpiresAt = 2000
	w, err := s.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	return s, &clock, w
}

func TestExpiryRejectsLateFramesAndRetainsAcceptedProgress(t *testing.T) {
	s, clock, w := expiryStore(t, true)
	*clock = 1999
	first := oneFrame("before", "one", 7)
	if err := s.Accept(w.Spec.ID, w.Spec.Generation, first); err != nil {
		t.Fatal(err)
	}
	batch, err := s.ClaimBatch(w.Spec.ID, 32)
	if err != nil || batch == nil {
		t.Fatalf("claim: %+v %v", batch, err)
	}
	if err = s.QueueBatch(batch.ID); err != nil {
		t.Fatal(err)
	}
	*clock = 2000 // the boundary itself is already expired
	if err = s.Accept(w.Spec.ID, w.Spec.Generation, oneFrame("late", "two", 8)); !errors.Is(err, ErrExpired) {
		t.Fatalf("late accept=%v", err)
	}
	got, err := s.Get(w.Spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.Enabled || got.Status != "expired" || got.Spec.Generation != w.Spec.Generation+1 || got.Pending != 1 || string(got.State) != `{"n":7}` || string(got.Cursor) != `{"n":7}` {
		t.Fatalf("expiry changed confirmed work: %+v", got)
	}
	closed, err := s.GetBatch(batch.ID)
	if err != nil || closed.Status != "interrupted" || closed.Error != ErrExpired.Error() {
		t.Fatalf("unfinished batch: %+v %v", closed, err)
	}
	var frames int
	if err = s.db.QueryRow("SELECT count(*) FROM sentinel_frames WHERE watch_id=?", w.Spec.ID).Scan(&frames); err != nil || frames != 1 {
		t.Fatalf("late frame persisted: count=%d err=%v", frames, err)
	}
	if err = s.SetRuntimeGeneration(w.Spec.ID, w.Spec.Generation, "waiting", "", 99, 0); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("retired collector replaced expiry: %v", err)
	}
	idempotent, changed, err := s.Expire(got.Spec.ID, got.Spec.Generation)
	if err != nil || changed || idempotent.Spec.Generation != got.Spec.Generation {
		t.Fatalf("expiry not idempotent: %+v %t %v", idempotent, changed, err)
	}
	if _, err = s.SetEnabled(w.Spec.ID, true); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired watch resumed: %v", err)
	}
	if err = s.RetryBatch(batch.ID); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired pending work retried: %v", err)
	}
}

func TestExpiryGuardsQueuedAndAdmittedWork(t *testing.T) {
	for _, action := range []string{"claim", "queue", "run", "retry"} {
		t.Run(action, func(t *testing.T) {
			s, clock, w := expiryStore(t, true)
			if err := s.Accept(w.Spec.ID, w.Spec.Generation, oneFrame("accepted", "one", 1)); err != nil {
				t.Fatal(err)
			}
			var b *Batch
			var err error
			if action != "claim" {
				b, err = s.ClaimBatch(w.Spec.ID, 32)
				if err != nil || b == nil {
					t.Fatalf("claim=%+v %v", b, err)
				}
			}
			if action == "retry" {
				if err = s.InterruptBatch(b.ID, "prior attempt"); err != nil {
					t.Fatal(err)
				}
			}
			*clock = 2000
			switch action {
			case "claim":
				_, err = s.ClaimBatch(w.Spec.ID, 32)
			case "queue":
				err = s.QueueBatch(b.ID)
			case "run":
				err = s.SetBatchRun(b.ID, "session", "run", "running")
			case "retry":
				err = s.RetryBatch(b.ID)
			}
			if !errors.Is(err, ErrExpired) {
				t.Fatalf("%s beyond deadline: %v", action, err)
			}
			got, _ := s.Get(w.Spec.ID)
			if got.Spec.Enabled || got.Status != "expired" || got.Pending != 1 {
				t.Fatalf("pending observation lost: %+v", got)
			}
		})
	}
}

func TestExpiryPersistsAcrossReopenAndIncludesPausedWatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.clock = func() int64 { return 1000 }
	p := testSpec(t)
	p.Enabled = false
	p.ExpiresAt = 2000
	w, err := s.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.clock = func() int64 { return 2000 }
	got, changed, err := s.Expire(w.Spec.ID, w.Spec.Generation)
	if err != nil || !changed || got.Spec.Enabled || got.Status != "expired" || got.Spec.ExpiresAt != 2000 {
		t.Fatalf("reopened deadline lost: %+v %t %v", got, changed, err)
	}
	if _, err = s.SetEnabled(w.Spec.ID, true); !errors.Is(err, ErrExpired) {
		t.Fatalf("past paused deadline extended by resume: %v", err)
	}
}

func TestExpiryLeavesLegacyLifetimeAndNewerGenerationsAlone(t *testing.T) {
	s, clock, w := expiryStore(t, false)
	current, err := s.SetEnabled(w.Spec.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	*clock = 2000
	if _, _, err = s.Expire(w.Spec.ID, w.Spec.Generation); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale expiry ignored generation: %v", err)
	}
	if _, _, err = s.Expire(current.Spec.ID, current.Spec.Generation); err != nil {
		t.Fatal(err)
	}
	p := testSpec(t)
	p.Enabled = true
	legacy, err := s.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	*clock = 1 << 60
	unchanged, changed, err := s.Expire(legacy.Spec.ID, legacy.Spec.Generation)
	if err != nil || changed || !unchanged.Spec.Enabled {
		t.Fatalf("legacy lifetime changed: %+v %v", unchanged, err)
	}
	b, _ := json.Marshal(unchanged.Spec)
	if strings.Contains(string(b), "expires_at") {
		t.Fatal("zero deadline changed legacy wire shape")
	}
}

func TestExpiryCreationRejectsPastOrNegativeDeadline(t *testing.T) {
	s, _ := testStore(t)
	s.clock = func() int64 { return 1000 }
	for _, deadline := range []int64{-1, 999, 1000} {
		p := testSpec(t)
		p.ExpiresAt = deadline
		if _, err := s.Create(p); err == nil {
			t.Fatalf("accepted deadline=%d", deadline)
		}
	}
}

func TestExpiryCrossingDuringCommitRollsBackWholeProposal(t *testing.T) {
	s, _, w := expiryStore(t, true)
	calls := 0
	s.clock = func() int64 {
		calls++
		if calls == 1 {
			return 1999
		}
		return 2000
	}
	if err := s.Accept(w.Spec.ID, w.Spec.Generation, oneFrame("crossing", "must-not-commit", 9)); !errors.Is(err, ErrExpired) {
		t.Fatalf("crossing proposal=%v", err)
	}
	got, err := s.Get(w.Spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "expired" || got.Pending != 0 || len(got.State) != 0 || len(got.Cursor) != 0 {
		t.Fatalf("partial transaction committed: %+v", got)
	}
	var frames int
	if err = s.db.QueryRow("SELECT count(*) FROM sentinel_frames WHERE watch_id=?", w.Spec.ID).Scan(&frames); err != nil || frames != 0 {
		t.Fatalf("late frame receipt persisted: %d %v", frames, err)
	}
}
