package mcpobserve

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

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
			c := Call{ID: fmt.Sprintf("%032x", i+1), Direction: "outbound", Server: "fixture", Tool: "echo", StartedAt: time.Now().UnixMilli(), Status: "running"}
			s.Record(c)
			c.Status = "ok"
			c.FinishedAt = c.StartedAt + 2
			c.DurationMs = 2
			s.Record(c)
			time.Sleep(time.Millisecond)
		}
	}()
	defer func() { <-done }()
	for i := 0; i < 40; i++ {
		st, err := s.Stats(ctx, Filter{})
		if err != nil {
			t.Fatal(err)
		}
		var grouped int64
		for _, b := range st.ByTool {
			grouped += b.Total
		}
		if grouped != st.Total || st.ByDirection.Outbound != st.Total || st.Running+st.OK != st.Total {
			t.Fatalf("inconsistent snapshot: %+v", st)
		}
		if st.OK == 0 && st.P95Ms != nil {
			t.Fatalf("latency without a completed sample: %+v", st)
		}
	}
}

func TestStoreRetentionZeroKeepsOlderCalls(t *testing.T) {
	s, _ := newSQLStore(t, StoreOptions{RetainDays: 0})
	c := mkCall(0, nil)
	c.StartedAt = time.Now().Add(-90 * 24 * time.Hour).UnixMilli()
	s.Record(c)
	flush(t, s)
	p, err := s.Query(context.Background(), Filter{})
	if err != nil || len(p.Calls) != 1 {
		t.Fatalf("zero retention should disable time expiry: %+v %v", p, err)
	}
}

func TestStoreCloseCancelsBlockedDatabaseWriter(t *testing.T) {
	s, db := newSQLStore(t, StoreOptions{})
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	s.Record(mkCall(1, nil))
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
