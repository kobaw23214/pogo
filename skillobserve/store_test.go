package skillobserve

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	// Match the WAL/busy-timeout shared DB supplied by web in production.
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "observe.db")+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func newSQLStore(t *testing.T, opts StoreOptions) (*Store, *sql.DB) {
	t.Helper()
	if opts.DB == nil {
		opts.DB = testDB(t)
	}
	s, err := NewStore(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, opts.DB
}

func newMemStore(t *testing.T, opts StoreOptions) *Store {
	t.Helper()
	opts.DB = nil
	s, err := NewStore(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func flush(t *testing.T, s *Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}
}

var testBase = time.Now().UnixMilli()

var (
	testStages      = []string{"discovery", "load", "execute"}
	testCallers     = []string{"internal", "external", "unknown"}
	testEntrypoints = []string{"pogo", "mcp", "unknown"}
	testSources     = []string{"user", "project", "embedded"}
	testMechanisms  = []string{"catalog", "file_read", "explicit", "manifest"}
	testStatuses    = []string{"running", "ok", "error", "timeout", "cancelled", "interrupted"}
)

func mkEvent(i int, mutate func(*Event)) Event {
	e := Event{
		ID:          fmt.Sprintf("%032x", i+1),
		TraceID:     fmt.Sprintf("trace-%d", i%4),
		SkillID:     fmt.Sprintf("skill-%d", i%3),
		SkillName:   fmt.Sprintf("skill-name-%d", i%3),
		SkillSource: testSources[i%len(testSources)],
		Stage:       testStages[i%len(testStages)],
		Caller:      testCallers[i%len(testCallers)],
		Entrypoint:  testEntrypoints[i%len(testEntrypoints)],
		Mechanism:   testMechanisms[i%len(testMechanisms)],
		Tool:        fmt.Sprintf("tool-%d", i%2),
		SessionID:   fmt.Sprintf("sess-%d", i%2),
		RunID:       fmt.Sprintf("run-%d", i%3),
		TaskID:      fmt.Sprintf("task-%d", i%2),
		OwnerID:     fmt.Sprintf("owner-%d", i%2),
		StartedAt:   testBase + int64(i)*10,
		FinishedAt:  testBase + int64(i)*10 + 5,
		DurationMs:  float64(i%50) + 1,
		Status:      testStatuses[i%len(testStatuses)],
	}
	if mutate != nil {
		mutate(&e)
	}
	return e
}

func recordAll(t *testing.T, s *Store, events []Event) {
	t.Helper()
	for _, e := range events {
		s.Record(e)
	}
	flush(t, s)
}

func collectAll(t *testing.T, s *Store, f Filter) []Event {
	t.Helper()
	var out []Event
	for {
		p, err := s.Query(context.Background(), f)
		if err != nil {
			t.Fatal(err)
		}
		if p.Events == nil {
			t.Fatal("events array must be non-nil")
		}
		out = append(out, p.Events...)
		if p.NextCursor == "" {
			return out
		}
		f.Before = p.NextCursor
	}
}

// More than one page of persistent history, with tied started_at values:
// the keyset cursor must neither skip nor repeat rows.
func TestKeysetPaginationCoversAll(t *testing.T) {
	s, _ := newSQLStore(t, StoreOptions{})
	events := make([]Event, 75)
	for i := range events {
		events[i] = mkEvent(i, func(e *Event) { e.StartedAt = testBase + int64(i/5) })
	}
	recordAll(t, s, events)

	seen := map[string]int{}
	var prev *Event
	pages := 0
	f := Filter{Limit: 30}
	for {
		p, err := s.Query(context.Background(), f)
		if err != nil {
			t.Fatal(err)
		}
		if p.Storage != "sqlite" {
			t.Fatalf("storage = %q", p.Storage)
		}
		pages++
		for i := range p.Events {
			e := &p.Events[i]
			seen[e.ID]++
			if prev != nil {
				if e.StartedAt > prev.StartedAt ||
					(e.StartedAt == prev.StartedAt && e.ID > prev.ID) {
					t.Fatalf("order broke at %+v after %+v", e, prev)
				}
			}
			prev = e
		}
		if p.NextCursor == "" {
			break
		}
		f.Before = p.NextCursor
	}
	if len(seen) != 75 || pages < 3 {
		t.Fatalf("pagination lost or repeated rows: %d unique over %d pages", len(seen), pages)
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("event %s seen %d times", id, n)
		}
	}
}

// Persistent history must survive past the memory ring size: SQL mode pages
// through every retained row regardless of the Ring option.
func TestPaginationBeyondRingStaysComplete(t *testing.T) {
	s, _ := newSQLStore(t, StoreOptions{Ring: 5})
	events := make([]Event, 60)
	for i := range events {
		events[i] = mkEvent(i, nil)
	}
	recordAll(t, s, events)
	got := collectAll(t, s, Filter{Limit: 7})
	if len(got) != 60 {
		t.Fatalf("expected 60 events beyond ring size, got %d", len(got))
	}
}

// Memory mode is explicitly bounded: the ring keeps only the newest events
// and reports storage:"memory".
func TestMemoryRingEvictsOldest(t *testing.T) {
	s := newMemStore(t, StoreOptions{Ring: 5})
	for i := 0; i < 10; i++ {
		s.Record(mkEvent(i, nil))
	}
	p, err := s.Query(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Storage != "memory" || len(p.Events) != 5 {
		t.Fatalf("memory ring not bounded: %+v", p)
	}
	if p.Events[len(p.Events)-1].ID != mkEvent(5, nil).ID {
		t.Fatalf("evicted the wrong end: %+v", p.Events)
	}
}

// Start's running snapshot and End's completed snapshot share one id: the
// upsert must leave exactly one row holding the final state.
func TestStartEndSameIDUpserts(t *testing.T) {
	for _, mode := range []string{"sqlite", "memory"} {
		t.Run(mode, func(t *testing.T) {
			var s *Store
			if mode == "sqlite" {
				s, _ = newSQLStore(t, StoreOptions{})
			} else {
				s = newMemStore(t, StoreOptions{})
			}
			e := mkEvent(0, func(e *Event) {
				e.Stage = "execute"
				e.Status = "running"
				e.FinishedAt = 0
				e.DurationMs = 0
			})
			s.Record(e)
			done := e
			done.Status = "ok"
			done.FinishedAt = e.StartedAt + 42
			done.DurationMs = 42
			s.Record(done)
			flush(t, s)
			st, err := s.Stats(context.Background(), Filter{})
			if err != nil {
				t.Fatal(err)
			}
			if st.Total != 1 || st.Executions != 1 || st.ExecutionOK != 1 || st.ExecutionRunning != 0 {
				t.Fatalf("upsert double counted: %+v", st)
			}
			d, err := s.Detail(context.Background(), e.ID)
			if err != nil {
				t.Fatal(err)
			}
			if d.Status != "ok" || d.DurationMs != 42 {
				t.Fatalf("detail did not keep final snapshot: %+v", d)
			}
		})
	}
}

func TestMalformedCursorRejected(t *testing.T) {
	bad := []string{
		"not-base64!!",
		encodeCursor(0, "abc"),                   // nonpositive timestamp
		encodeCursor(10, ""),                     // empty id
		encodeCursor(10, "has-dash-not-allowed"), // invalid id char
		encodeCursor(10, string(make([]byte, 65))),
	}
	for _, mode := range []string{"sqlite", "memory"} {
		var s *Store
		if mode == "sqlite" {
			s, _ = newSQLStore(t, StoreOptions{})
		} else {
			s = newMemStore(t, StoreOptions{})
		}
		for _, c := range bad {
			if _, err := s.Query(context.Background(), Filter{Before: c}); !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("%s cursor %q: got %v, want ErrInvalidCursor", mode, c, err)
			}
		}
	}
}

// Discovery and load events must never leak into execution outcomes, error
// rate, or latency percentiles.
func TestExecutionMetricsExcludeDiscoveryAndLoad(t *testing.T) {
	events := []Event{
		// discovery / load noise that must not move execution numbers
		mkEvent(0, func(e *Event) { e.Stage = "discovery"; e.Status = "ok"; e.DurationMs = 999 }),
		mkEvent(1, func(e *Event) { e.Stage = "load"; e.Status = "error"; e.DurationMs = 888 }),
		// executions: ok(10), error(20), timeout(30), cancelled(40), running, interrupted
		mkEvent(2, func(e *Event) { e.Stage = "execute"; e.Status = "ok"; e.DurationMs = 10 }),
		mkEvent(3, func(e *Event) { e.Stage = "execute"; e.Status = "error"; e.DurationMs = 20 }),
		mkEvent(4, func(e *Event) { e.Stage = "execute"; e.Status = "timeout"; e.DurationMs = 30 }),
		mkEvent(5, func(e *Event) { e.Stage = "execute"; e.Status = "cancelled"; e.DurationMs = 40 }),
		mkEvent(6, func(e *Event) { e.Stage = "execute"; e.Status = "running"; e.DurationMs = 0; e.FinishedAt = 0 }),
		mkEvent(7, func(e *Event) { e.Stage = "execute"; e.Status = "interrupted"; e.DurationMs = 50 }),
	}
	for _, mode := range []string{"sqlite", "memory"} {
		t.Run(mode, func(t *testing.T) {
			var s *Store
			if mode == "sqlite" {
				s, _ = newSQLStore(t, StoreOptions{})
			} else {
				s = newMemStore(t, StoreOptions{})
			}
			recordAll(t, s, events)
			st, err := s.Stats(context.Background(), Filter{})
			if err != nil {
				t.Fatal(err)
			}
			if st.Total != 8 || st.Discoveries != 1 || st.Loads != 1 || st.Executions != 6 {
				t.Fatalf("stage counts: %+v", st)
			}
			if st.ExecutionOK != 1 || st.ExecutionErrors != 2 || st.ExecutionTimeouts != 1 ||
				st.ExecutionCancelled != 1 || st.ExecutionRunning != 1 || st.ExecutionInterrupted != 1 {
				t.Fatalf("execution outcomes: %+v", st)
			}
			// errors / (ok + errors) = 2/3; cancelled is a latency sample
			// but never a success denominator member.
			if st.ErrorRate == nil || *st.ErrorRate != 2.0/3.0 {
				t.Fatalf("error rate: %v", st.ErrorRate)
			}
			// latency samples are ok/error/timeout/cancelled = 10,20,30,40
			if st.P50Ms == nil || *st.P50Ms != 20 {
				t.Fatalf("p50: %v", st.P50Ms)
			}
			if st.P95Ms == nil || *st.P95Ms != 40 {
				t.Fatalf("p95: %v", st.P95Ms)
			}
			if st.MaxMs == nil || *st.MaxMs != 40 {
				t.Fatalf("max: %v", st.MaxMs)
			}
		})
	}
}

// Without completed executions the rate and latency fields stay null, and a
// stage=load filter intentionally reports zero execution metrics.
func TestNoExecutionSamplesAreNull(t *testing.T) {
	events := []Event{
		mkEvent(0, func(e *Event) { e.Stage = "discovery"; e.Status = "ok" }),
		mkEvent(1, func(e *Event) { e.Stage = "load"; e.Status = "ok" }),
		mkEvent(2, func(e *Event) { e.Stage = "execute"; e.Status = "running"; e.FinishedAt = 0 }),
	}
	s, _ := newSQLStore(t, StoreOptions{})
	recordAll(t, s, events)
	for name, f := range map[string]Filter{"all": {}, "loads_only": {Stage: "load"}} {
		st, err := s.Stats(context.Background(), f)
		if err != nil {
			t.Fatal(err)
		}
		if st.ErrorRate != nil || st.P50Ms != nil || st.P95Ms != nil || st.MaxMs != nil {
			t.Fatalf("%s: execution metrics must be null without samples: %+v", name, st)
		}
		if st.ExecutionErrors != 0 || st.ExecutionOK != 0 {
			t.Fatalf("%s: execution counts must be zero: %+v", name, st)
		}
	}
	st, err := s.Stats(context.Background(), Filter{Stage: "load"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Loads != 1 || st.Executions != 0 || st.ExecutionRunning != 0 {
		t.Fatalf("stage filter leaked other stages: %+v", st)
	}
}

// Rows left running by a dead process are unknowable: reopening the store
// flips them to interrupted with a stable error kind.
func TestStaleRunningRecoveredAsInterrupted(t *testing.T) {
	db := testDB(t)
	s1, err := NewStore(StoreOptions{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	s1.Record(mkEvent(0, func(e *Event) { e.Stage = "execute"; e.Status = "running"; e.FinishedAt = 0 }))
	flush(t, s1)
	s1.Close()

	s2, err := NewStore(StoreOptions{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s2.Close)
	d, err := s2.Detail(context.Background(), mkEvent(0, nil).ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != "interrupted" || d.ErrorKind != "unknown_outcome" {
		t.Fatalf("stale running row not recovered: %+v", d)
	}
}

func TestRetentionSweepByAgeAndRowCap(t *testing.T) {
	old := time.Now().Add(-48 * time.Hour).UnixMilli()
	s, _ := newSQLStore(t, StoreOptions{RetainDays: 1, MaxRows: 3})
	for i := 0; i < 5; i++ {
		s.Record(mkEvent(i, nil))
	}
	s.Record(mkEvent(99, func(e *Event) { e.StartedAt = old }))
	flush(t, s)
	s.sweepOnce()
	st, err := s.Stats(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 3 {
		t.Fatalf("retention kept %d rows, want 3 (age sweep + row cap)", st.Total)
	}
	// Row cap keeps the newest by keyset order.
	got := collectAll(t, s, Filter{})
	for _, e := range got {
		if e.StartedAt == old {
			t.Fatal("expired event survived sweep")
		}
	}
}

func TestRetentionZeroDisablesTimeExpiry(t *testing.T) {
	for _, mode := range []string{"sqlite", "memory"} {
		t.Run(mode, func(t *testing.T) {
			var s *Store
			if mode == "sqlite" {
				s, _ = newSQLStore(t, StoreOptions{RetainDays: 0})
			} else {
				s = newMemStore(t, StoreOptions{RetainDays: 0})
			}
			e := mkEvent(0, nil)
			e.StartedAt = time.Now().Add(-90 * 24 * time.Hour).UnixMilli()
			s.Record(e)
			flush(t, s)
			s.sweepOnce()
			p, err := s.Query(context.Background(), Filter{})
			if err != nil || len(p.Events) != 1 {
				t.Fatalf("zero retention must disable time expiry: %+v %v", p, err)
			}
		})
	}
}

// Queue loss after Close is counted and reported, never swallowed.
func TestRecordAfterCloseCountedAsDropped(t *testing.T) {
	s, _ := newSQLStore(t, StoreOptions{})
	s.Close()
	s.Record(mkEvent(0, nil))
	p, err := s.Query(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Dropped != 1 || len(p.Events) != 0 {
		t.Fatalf("closed-store record not surfaced: %+v", p)
	}
	st, err := s.Stats(context.Background(), Filter{})
	if err != nil || st.Dropped != 1 {
		t.Fatalf("stats must surface drops too: %+v %v", st, err)
	}
}

// Database write failures are counted and visible on Page and Stats.
func TestDBWriteFailuresVisible(t *testing.T) {
	s, db := newSQLStore(t, StoreOptions{})
	// Break writes only: reads keep working so the failure counters stay
	// observable on Page and Stats answers.
	if _, err := db.Exec(`CREATE TRIGGER block_skill_writes BEFORE INSERT ON ` + storeTable + `
		BEGIN SELECT RAISE(ABORT, 'simulated write failure'); END;`); err != nil {
		t.Fatal(err)
	}
	s.Record(mkEvent(0, nil))
	flush(t, s)
	p, err := s.Query(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if p.WriteErrors == 0 {
		t.Fatalf("write failure not surfaced on page: %+v", p)
	}
	st, err := s.Stats(context.Background(), Filter{})
	if err != nil || st.WriteErrors == 0 {
		t.Fatalf("write failure not surfaced on stats: %+v %v", st, err)
	}
}

// Close is idempotent, drains pending writes, and Flush after Close is a
// no-op instead of a hang.
func TestCloseIdempotentAndDrains(t *testing.T) {
	s, _ := newSQLStore(t, StoreOptions{})
	s.Record(mkEvent(0, nil))
	s.Close()
	s.Close() // must not panic or hang
	got := collectAll(t, s, Filter{})
	if len(got) != 1 {
		t.Fatalf("close lost a queued write: %d events", len(got))
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Flush(ctx); err != nil {
		t.Fatalf("flush after close: %v", err)
	}
}

func TestDetailMissingReturnsNoRows(t *testing.T) {
	s, _ := newSQLStore(t, StoreOptions{})
	if _, err := s.Detail(context.Background(), "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("got %v, want sql.ErrNoRows", err)
	}
	m := newMemStore(t, StoreOptions{})
	if _, err := m.Detail(context.Background(), "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("memory: got %v, want sql.ErrNoRows", err)
	}
}

// The skill table is fully isolated: MCP rows in the same database are
// neither read nor disturbed, and MCP stores never see skill rows.
func TestSkillTableIsolatedFromMCP(t *testing.T) {
	db := testDB(t)
	s, err := NewStore(StoreOptions{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	recordAll(t, s, []Event{mkEvent(0, nil), mkEvent(1, nil)})

	// Foreign rows in an MCP-shaped table must not leak into skill answers.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS mcpobserve_calls (
		id TEXT PRIMARY KEY, trace_id TEXT NOT NULL DEFAULT '',
		started_at INTEGER NOT NULL, status TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO mcpobserve_calls (id, trace_id, started_at, status)
		VALUES ('mcp-row-1', 'trace-x', ?, 'ok')`, testBase); err != nil {
		t.Fatal(err)
	}
	st, err := s.Stats(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 2 {
		t.Fatalf("skill stats polluted by MCP rows: %+v", st)
	}
	if _, err := s.Detail(context.Background(), "mcp-row-1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("skill detail reached into MCP table: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mcpobserve_calls`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("skill store touched MCP table: n=%d err=%v", n, err)
	}
}

// SQL and memory backends must agree exactly on every filter combination:
// same pages, same cursors, same stats.
func TestSQLMemoryParity(t *testing.T) {
	events := make([]Event, 48)
	for i := range events {
		events[i] = mkEvent(i, nil)
	}
	sqlStore, _ := newSQLStore(t, StoreOptions{})
	recordAll(t, sqlStore, events)
	memStore := newMemStore(t, StoreOptions{})
	recordAll(t, memStore, events)

	filters := []Filter{
		{},
		{Stage: "execute"},
		{Stage: "load", Caller: "external"},
		{Caller: "internal", Entrypoint: "pogo"},
		{Entrypoint: "unknown"},
		{SkillID: "skill-1"},
		{SkillName: "skill-name-2"},
		{SkillSource: "project"},
		{Mechanism: "manifest", Tool: "tool-1"},
		{Status: "timeout"},
		{Status: "cancelled", Stage: "execute"},
		{SessionID: "sess-1", RunID: "run-1", TaskID: "task-1"},
		{TraceID: "trace-2", OwnerID: "owner-1"},
		{Query: "tool-0"},
		{Query: "skill-name-1"},
		{Query: "%_\\"}, // LIKE metacharacters must stay literal
		{From: testBase + 100, To: testBase + 300},
		{Stage: "discovery", From: testBase, To: testBase + 10000},
	}
	for _, f := range filters {
		f.Limit = 7
		sqlEvents := collectAll(t, sqlStore, f)
		memEvents := collectAll(t, memStore, f)
		if !reflect.DeepEqual(sqlEvents, memEvents) {
			t.Fatalf("query parity broke for %+v:\nsql=%v\nmem=%v", f, sqlEvents, memEvents)
		}
		sqlStats, err := sqlStore.Stats(context.Background(), f)
		if err != nil {
			t.Fatal(err)
		}
		memStats, err := memStore.Stats(context.Background(), f)
		if err != nil {
			t.Fatal(err)
		}
		sqlStats.Storage, memStats.Storage = "", ""
		sqlStats.Dropped, memStats.Dropped = 0, 0
		sqlStats.WriteErrors, memStats.WriteErrors = 0, 0
		if !reflect.DeepEqual(sqlStats, memStats) {
			t.Fatalf("stats parity broke for %+v:\nsql=%+v\nmem=%+v", f, sqlStats, memStats)
		}
	}
}

// by_skill groups per-skill stage counts with execution-only p95, ordered by
// executions then volume, capped at 20.
func TestBySkillGrouping(t *testing.T) {
	var events []Event
	// skill-a: 2 executions (ok 10ms, error 90ms), 1 discovery
	events = append(events,
		mkEvent(0, func(e *Event) {
			e.SkillID, e.SkillName, e.SkillSource = "skill-a", "alpha", "user"
			e.Stage = "execute"
			e.Status = "ok"
			e.DurationMs = 10
		}),
		mkEvent(1, func(e *Event) {
			e.SkillID, e.SkillName, e.SkillSource = "skill-a", "alpha", "user"
			e.Stage = "execute"
			e.Status = "error"
			e.DurationMs = 90
		}),
		mkEvent(2, func(e *Event) {
			e.SkillID, e.SkillName, e.SkillSource = "skill-a", "alpha", "user"
			e.Stage = "discovery"
			e.Status = "ok"
		}),
		// skill-b: 1 execution (ok 50ms), 3 loads
		mkEvent(3, func(e *Event) {
			e.SkillID, e.SkillName, e.SkillSource = "skill-b", "beta", "project"
			e.Stage = "execute"
			e.Status = "ok"
			e.DurationMs = 50
		}),
	)
	for i := 0; i < 3; i++ {
		i := i
		events = append(events, mkEvent(10+i, func(e *Event) {
			e.SkillID, e.SkillName, e.SkillSource = "skill-b", "beta", "project"
			e.Stage = "load"
			e.Status = "ok"
			e.DurationMs = 777 // load duration must not reach p95
		}))
	}
	s, _ := newSQLStore(t, StoreOptions{})
	recordAll(t, s, events)
	st, err := s.Stats(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.BySkill) != 2 {
		t.Fatalf("by_skill: %+v", st.BySkill)
	}
	a, b := st.BySkill[0], st.BySkill[1]
	if a.SkillID != "skill-a" || a.Executions != 2 || a.Discoveries != 1 || a.Loads != 0 ||
		a.ExecutionErrors != 1 || a.P95Ms == nil || *a.P95Ms != 90 {
		t.Fatalf("skill-a bucket: %+v", a)
	}
	if b.SkillID != "skill-b" || b.Executions != 1 || b.Loads != 3 ||
		b.ExecutionErrors != 0 || b.P95Ms == nil || *b.P95Ms != 50 {
		t.Fatalf("skill-b bucket: %+v", b)
	}
}

func TestByCallerCounts(t *testing.T) {
	events := []Event{
		mkEvent(0, func(e *Event) { e.Caller = "internal" }),
		mkEvent(1, func(e *Event) { e.Caller = "external" }),
		mkEvent(2, func(e *Event) { e.Caller = "unknown" }),
		mkEvent(3, func(e *Event) { e.Caller = "something-unexpected" }),
	}
	s, _ := newSQLStore(t, StoreOptions{})
	recordAll(t, s, events)
	st, err := s.Stats(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if st.ByCaller.Internal != 1 || st.ByCaller.External != 1 || st.ByCaller.Unknown != 2 {
		t.Fatalf("by_caller: %+v", st.ByCaller)
	}
}
