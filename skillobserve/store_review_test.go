package skillobserve

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// Concurrent writers must never produce a contradictory Stats snapshot:
// stage counts and caller counts always sum back to total, and latency
// fields never appear without a completed execution sample.
func TestStoreStatsConsistentDuringWrites(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "concurrent.db")+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := NewStore(StoreOptions{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 160; i++ {
			e := Event{
				ID:        fmt.Sprintf("%032x", i+1),
				TraceID:   "trace-c",
				SkillID:   "skill-c",
				SkillName: "concurrent",
				Stage:     "execute",
				Caller:    "internal",
				StartedAt: time.Now().UnixMilli(),
				Status:    "running",
			}
			s.Record(e)
			e.Status = "ok"
			e.FinishedAt = e.StartedAt + 2
			e.DurationMs = 2
			s.Record(e)
			time.Sleep(time.Millisecond)
		}
	}()
	defer func() { <-done }()
	for i := 0; i < 40; i++ {
		st, err := s.Stats(ctx, Filter{})
		if err != nil {
			t.Fatal(err)
		}
		if st.Discoveries+st.Loads+st.Executions != st.Total {
			t.Fatalf("stage counts diverged from total: %+v", st)
		}
		if st.ByCaller.Internal+st.ByCaller.External+st.ByCaller.Unknown != st.Total {
			t.Fatalf("caller counts diverged from total: %+v", st)
		}
		if st.ExecutionRunning+st.ExecutionOK != st.Executions {
			t.Fatalf("execution outcomes diverged: %+v", st)
		}
		var grouped int64
		for _, b := range st.BySkill {
			grouped += b.Discoveries + b.Loads + b.Executions
		}
		if grouped != st.Total {
			t.Fatalf("by_skill buckets diverged from total: %+v", st)
		}
		if st.ExecutionOK == 0 && st.P95Ms != nil {
			t.Fatalf("latency without a completed sample: %+v", st)
		}
	}
}

// A writer blocked on a busy shared connection must be released by Close:
// the bounded drain expires, the store cancels its DB context, and the
// abandoned write shows up in write_errors.
func TestStoreCloseCancelsBlockedDatabaseWriter(t *testing.T) {
	s, db := newSQLStore(t, StoreOptions{})
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	s.Record(mkEvent(1, nil))
	deadline := time.Now().Add(time.Second)
	for db.Stats().WaitCount == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if db.Stats().WaitCount == 0 {
		t.Fatal("writer did not start waiting for the occupied connection")
	}
	s.Close()
	select {
	case <-s.writerDone:
	case <-time.After(time.Second):
		t.Fatal("writer kept waiting after bounded Close")
	}
	select {
	case <-s.sweepDone:
	case <-time.After(time.Second):
		t.Fatal("retention worker survived Close")
	}
	if s.writeErrors.Load() == 0 {
		t.Fatal("abandoned write was not reported")
	}
}

// Memory mode applies the same retention window on reads, keeping parity
// with SQL sweeps.
func TestMemoryRetentionExpiry(t *testing.T) {
	s := newMemStore(t, StoreOptions{RetainDays: 1})
	old := mkEvent(0, nil)
	old.StartedAt = time.Now().Add(-48 * time.Hour).UnixMilli()
	s.Record(old)
	s.Record(mkEvent(1, nil))
	p, err := s.Query(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Events) != 1 || p.Events[0].ID == old.ID {
		t.Fatalf("memory retention did not expire old event: %+v", p)
	}
}
