package mcpobserve

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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

func flush(t *testing.T, s *Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}
}

var testBase = time.Now().UnixMilli()

func mkCall(i int, mutate func(*Call)) Call {
	c := Call{
		ID:        fmt.Sprintf("%032x", i+1),
		TraceID:   fmt.Sprintf("trace-%d", i%4),
		Direction: "outbound",
		Server:    "srv",
		Tool:      fmt.Sprintf("tool-%d", i%3),
		SessionID: "sess-1",
		StartedAt: testBase + int64(i)*10,
		Status:    "ok",
	}
	if mutate != nil {
		mutate(&c)
	}
	return c
}

func recordAll(t *testing.T, s *Store, calls []Call) {
	t.Helper()
	for _, c := range calls {
		s.Record(c)
	}
	flush(t, s)
}

func collectAll(t *testing.T, s *Store, f Filter) []Call {
	t.Helper()
	var out []Call
	for {
		p, err := s.Query(context.Background(), f)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p.Calls...)
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
	calls := make([]Call, 75)
	for i := range calls {
		calls[i] = mkCall(i, func(c *Call) { c.StartedAt = testBase + int64(i/5) })
	}
	recordAll(t, s, calls)

	seen := map[string]int{}
	var prev *Call
	pages := 0
	f := Filter{Limit: 30}
	for {
		p, err := s.Query(context.Background(), f)
		if err != nil {
			t.Fatal(err)
		}
		if p.Calls == nil {
			t.Fatal("calls array must be non-nil")
		}
		if p.Storage != "sqlite" {
			t.Fatalf("storage = %q", p.Storage)
		}
		pages++
		for i := range p.Calls {
			c := &p.Calls[i]
			seen[c.ID]++
			if prev != nil {
				if c.StartedAt > prev.StartedAt ||
					(c.StartedAt == prev.StartedAt && c.ID >= prev.ID) {
					t.Fatalf("order broken at %s after %s", c.ID, prev.ID)
				}
			}
			prev = c
		}
		if p.NextCursor == "" {
			break
		}
		f.Before = p.NextCursor
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
	}
	if pages < 3 {
		t.Fatalf("expected >1 page, got %d", pages)
	}
	if len(seen) != 75 {
		t.Fatalf("covered %d unique calls, want 75", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("call %s seen %d times", id, n)
		}
	}
}

// Start and End snapshots share one id and must count as a single call.
func TestStartEndDedup(t *testing.T) {
	s, _ := newSQLStore(t, StoreOptions{})
	start := mkCall(0, func(c *Call) { c.Status = "running" })
	end := start
	end.Status, end.FinishedAt, end.DurationMs, end.ErrorKind = "error", start.StartedAt+40, 40, "tool_error"
	s.Record(start)
	s.Record(end)
	flush(t, s)

	st, err := s.Stats(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 1 || st.Running != 0 || st.Errors != 1 {
		t.Fatalf("stats = %+v", st)
	}
	c, err := s.Detail(context.Background(), start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != "error" || c.ErrorKind != "tool_error" || c.DurationMs != 40 {
		t.Fatalf("detail = %+v", c)
	}
}

// Direction, status, time window, substring query, and correlation filters.
func TestFilters(t *testing.T) {
	s, _ := newSQLStore(t, StoreOptions{})
	calls := []Call{
		mkCall(0, func(c *Call) { c.Direction = "inbound"; c.Tool = "alpha"; c.Status = "ok" }),
		mkCall(1, func(c *Call) {
			c.Direction = "outbound"
			c.Tool = "beta"
			c.Status = "error"
			c.ErrorKind = "tool_error"
		}),
		mkCall(2, func(c *Call) { c.Direction = "inbound"; c.Tool = "alpha"; c.Status = "timeout" }),
		mkCall(3, func(c *Call) {
			c.Direction = "outbound"
			c.Tool = "beta"
			c.Status = "ok"
			c.TraceID = "special-trace"
			c.RunID = "run-9"
			c.OwnerID = "owner-7"
		}),
	}
	recordAll(t, s, calls)
	q := func(f Filter) int64 {
		t.Helper()
		st, err := s.Stats(context.Background(), f)
		if err != nil {
			t.Fatal(err)
		}
		return st.Total
	}
	if n := q(Filter{Direction: "inbound"}); n != 2 {
		t.Fatalf("direction filter = %d", n)
	}
	if n := q(Filter{Status: "error"}); n != 1 {
		t.Fatalf("status filter = %d", n)
	}
	if n := q(Filter{From: testBase + 15, To: testBase + 25}); n != 1 {
		t.Fatalf("time filter = %d", n)
	}
	if n := q(Filter{Query: "alph"}); n != 2 {
		t.Fatalf("substring tool = %d", n)
	}
	if n := q(Filter{Query: "SPECIAL-trace"}); n != 1 { // case-insensitive ASCII
		t.Fatalf("substring trace = %d", n)
	}
	if n := q(Filter{Query: "tool_error"}); n != 1 {
		t.Fatalf("substring error_kind = %d", n)
	}
	if n := q(Filter{TraceID: "special-trace"}); n != 1 {
		t.Fatalf("trace filter = %d", n)
	}
	if n := q(Filter{RunID: "run-9", OwnerID: "owner-7"}); n != 1 {
		t.Fatalf("correlation filter = %d", n)
	}
	if n := q(Filter{SessionID: "sess-1"}); n != 4 {
		t.Fatalf("session filter = %d", n)
	}
	// LIKE metacharacters in the query string must be literal, not wildcards.
	if n := q(Filter{Query: "100%"}); n != 0 {
		t.Fatalf("like escape = %d", n)
	}
	// List and stats agree under the same filter.
	p, err := s.Query(context.Background(), Filter{Direction: "inbound", Status: "timeout"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Calls) != 1 || p.Calls[0].Tool != "alpha" {
		t.Fatalf("list = %+v", p.Calls)
	}
}

// Stats must cover every matching retained row, not the UI page.
func TestStatsIgnoreLimitAndCursor(t *testing.T) {
	s, _ := newSQLStore(t, StoreOptions{})
	calls := make([]Call, 40)
	for i := range calls {
		calls[i] = mkCall(i, nil)
	}
	recordAll(t, s, calls)
	st, err := s.Stats(context.Background(), Filter{Limit: 1, Before: encodeCursor(testBase+100, "zzz")})
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 40 || st.OK != 40 {
		t.Fatalf("stats = %+v", st)
	}
}

// SQL and memory backends must answer identically for the same data.
func TestMemorySQLParity(t *testing.T) {
	sqlStore, _ := newSQLStore(t, StoreOptions{})
	memStore, err := NewStore(StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(memStore.Close)

	calls := make([]Call, 0, 45)
	statuses := []string{"ok", "error", "timeout", "cancelled", "interrupted", "running"}
	for i := 0; i < 45; i++ {
		calls = append(calls, mkCall(i, func(c *Call) {
			c.Status = statuses[i%len(statuses)]
			c.Direction = []string{"inbound", "outbound"}[i%2]
			c.StartedAt = testBase + int64(i/3) // ties exercise the id tiebreak
			if c.Status != "running" {
				c.DurationMs = float64(i%17) + 0.5
				c.FinishedAt = c.StartedAt + int64(c.DurationMs)
			}
		}))
	}
	recordAll(t, sqlStore, calls)
	for _, c := range calls {
		memStore.Record(c)
	}

	for _, f := range []Filter{
		{}, {Direction: "inbound"}, {Status: "error"}, {Tool: "tool-1"},
		{Query: "trace-2"}, {From: testBase + 5, To: testBase + 10},
		{Direction: "outbound", Status: "timeout"},
	} {
		f.Limit = 7
		gotSQL := collectAll(t, sqlStore, f)
		gotMem := collectAll(t, memStore, f)
		if !reflect.DeepEqual(gotSQL, gotMem) {
			t.Fatalf("filter %+v:\nsql=%v\nmem=%v", f, gotSQL, gotMem)
		}
		stSQL, err := sqlStore.Stats(context.Background(), f)
		if err != nil {
			t.Fatal(err)
		}
		stMem, err := memStore.Stats(context.Background(), f)
		if err != nil {
			t.Fatal(err)
		}
		if stMem.Storage != "memory" || stSQL.Storage != "sqlite" {
			t.Fatalf("storage tags = %q/%q", stSQL.Storage, stMem.Storage)
		}
		stSQL.Storage, stMem.Storage = "", ""
		if !reflect.DeepEqual(stSQL, stMem) {
			t.Fatalf("stats filter %+v:\nsql=%+v\nmem=%+v", f, stSQL, stMem)
		}
	}
}

// Empty history: rate and latency are null, arrays are non-nil.
func TestEmptyStatsNull(t *testing.T) {
	for _, opts := range []StoreOptions{{}, {DB: testDB(t)}} {
		s, err := NewStore(opts)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		st, err := s.Stats(context.Background(), Filter{})
		if err != nil {
			t.Fatal(err)
		}
		if st.ErrorRate != nil || st.P50Ms != nil || st.P95Ms != nil || st.MaxMs != nil {
			t.Fatalf("null fields = %+v", st)
		}
		if st.ByTool == nil || len(st.ByTool) != 0 {
			t.Fatalf("by_tool = %+v", st.ByTool)
		}
		p, err := s.Query(context.Background(), Filter{})
		if err != nil {
			t.Fatal(err)
		}
		if p.Calls == nil || len(p.Calls) != 0 || p.NextCursor != "" {
			t.Fatalf("page = %+v", p)
		}
	}
}

// Unknown, interrupted, cancelled, and running outcomes never count as
// successes: error_rate divides errors by (ok+error+timeout) only, and
// latency samples exclude running/interrupted.
func TestErrorRateExcludesUnknown(t *testing.T) {
	s, _ := newSQLStore(t, StoreOptions{})
	calls := []Call{
		mkCall(0, func(c *Call) { c.Status = "ok"; c.DurationMs = 10 }),
		mkCall(1, func(c *Call) { c.Status = "error"; c.DurationMs = 20 }),
		mkCall(2, func(c *Call) { c.Status = "timeout"; c.DurationMs = 30 }),
		mkCall(3, func(c *Call) { c.Status = "cancelled"; c.DurationMs = 40 }),
		mkCall(4, func(c *Call) { c.Status = "running" }),
		mkCall(5, func(c *Call) { c.Status = "interrupted" }),
	}
	recordAll(t, s, calls)
	st, err := s.Stats(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 6 || st.Errors != 2 || st.Timeouts != 1 || st.OK != 1 ||
		st.Cancelled != 1 || st.Running != 1 || st.Interrupted != 1 {
		t.Fatalf("stats = %+v", st)
	}
	if st.ErrorRate == nil || *st.ErrorRate != 2.0/3.0 {
		t.Fatalf("error_rate = %v", st.ErrorRate)
	}
	if st.P50Ms == nil || *st.P50Ms != 20 {
		t.Fatalf("p50 = %v", st.P50Ms)
	}
	if st.MaxMs == nil || *st.MaxMs != 40 {
		t.Fatalf("max = %v", st.MaxMs)
	}
}

func TestPercentilesNearestRank(t *testing.T) {
	s, _ := newSQLStore(t, StoreOptions{})
	for i := 1; i <= 100; i++ {
		s.Record(mkCall(i, func(c *Call) {
			c.Status = "ok"
			c.DurationMs = float64(i)
		}))
	}
	flush(t, s)
	st, err := s.Stats(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if st.P50Ms == nil || *st.P50Ms != 50 || st.P95Ms == nil || *st.P95Ms != 95 ||
		st.MaxMs == nil || *st.MaxMs != 100 {
		t.Fatalf("p50=%v p95=%v max=%v", st.P50Ms, st.P95Ms, st.MaxMs)
	}
	if st.ErrorRate == nil || *st.ErrorRate != 0 {
		t.Fatalf("error_rate = %v", st.ErrorRate)
	}
	if len(st.ByTool) != 3 {
		t.Fatalf("by_tool buckets = %+v", st.ByTool)
	}
	for _, b := range st.ByTool {
		if b.P95Ms == nil {
			t.Fatalf("bucket %+v missing p95", b)
		}
	}
}

// History survives a simulated process restart on the same database file.
func TestPersistenceAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")
	db1, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	s1, err := NewStore(StoreOptions{DB: db1})
	if err != nil {
		t.Fatal(err)
	}
	recordAll(t, s1, []Call{mkCall(0, nil), mkCall(1, nil)})
	s1.Close()
	db1.Close()

	db2, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	s2, err := NewStore(StoreOptions{DB: db2})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	st, err := s2.Stats(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 2 || st.OK != 2 {
		t.Fatalf("stats after restart = %+v", st)
	}
}

// Rows a dead process left running become interrupted at startup,
// conservatively excluding them from successes and latency.
func TestStartupMarksRunningInterrupted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crash.db")
	db1, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	s1, err := NewStore(StoreOptions{DB: db1})
	if err != nil {
		t.Fatal(err)
	}
	s1.Record(mkCall(0, func(c *Call) { c.Status = "running" }))
	flush(t, s1)
	s1.Close()
	db1.Close()

	db2, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	s2, err := NewStore(StoreOptions{DB: db2})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	c, err := s2.Detail(context.Background(), fmt.Sprintf("%032x", 1))
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != "interrupted" || c.ErrorKind != "unknown_outcome" {
		t.Fatalf("call after restart = %+v", c)
	}
	st, err := s2.Stats(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Interrupted != 1 || st.ErrorRate != nil || st.P50Ms != nil {
		t.Fatalf("stats = %+v", st)
	}
}

func TestRetention(t *testing.T) {
	s, db := newSQLStore(t, StoreOptions{RetainDays: 1})
	old := testBase - 48*time.Hour.Milliseconds()
	s.Record(mkCall(0, func(c *Call) { c.StartedAt = old }))
	s.Record(mkCall(1, nil))
	flush(t, s)
	// The retention cutoff applies to reads even between sweeps.
	st, err := s.Stats(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 1 {
		t.Fatalf("stats with retention = %+v", st)
	}
	s.sweepOnce()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + storeTable).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rows after sweep = %d", n)
	}
}

func TestMaxRows(t *testing.T) {
	s, db := newSQLStore(t, StoreOptions{MaxRows: 5})
	for i := 0; i < 10; i++ {
		s.Record(mkCall(i, nil))
	}
	flush(t, s)
	s.sweepOnce()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + storeTable).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("rows = %d, want 5", n)
	}
	// Newest rows survive.
	st, err := s.Stats(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 5 {
		t.Fatalf("stats = %+v", st)
	}
	all := collectAll(t, s, Filter{})
	if all[0].StartedAt != testBase+90 || all[4].StartedAt != testBase+50 {
		t.Fatalf("kept rows = %+v", all)
	}
}

func TestMemoryBounded(t *testing.T) {
	s, err := NewStore(StoreOptions{Ring: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 5; i++ {
		s.Record(mkCall(i, nil))
	}
	p, err := s.Query(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Calls) != 3 || p.Storage != "memory" {
		t.Fatalf("page = %+v", p)
	}
}

func TestInvalidCursor(t *testing.T) {
	s, _ := newSQLStore(t, StoreOptions{})
	for _, bad := range []string{"garbage", "!!!", encodeCursor(-1, "x"), encodeCursor(5, "bad id!")} {
		if _, err := s.Query(context.Background(), Filter{Before: bad}); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("cursor %q: err = %v", bad, err)
		}
	}
}

func TestDetailNotFound(t *testing.T) {
	s, _ := newSQLStore(t, StoreOptions{})
	if _, err := s.Detail(context.Background(), "nope"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("err = %v", err)
	}
	mem, err := NewStore(StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer mem.Close()
	if _, err := mem.Detail(context.Background(), "nope"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("memory err = %v", err)
	}
}

// A broken database must surface as errors and counters, never as a quiet
// empty success.
func TestWriteErrorsSurface(t *testing.T) {
	s, db := newSQLStore(t, StoreOptions{})
	recordAll(t, s, []Call{mkCall(0, nil)})
	db.Close() // the test owns this handle; simulates a lost database
	s.Record(mkCall(1, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.Flush(ctx)
	if s.writeErrors.Load() == 0 {
		t.Fatal("write_errors not counted")
	}
	if _, err := s.Query(context.Background(), Filter{}); err == nil {
		t.Fatal("query on broken DB must fail, not return empty success")
	}
	st, err := s.Stats(context.Background(), Filter{})
	if err == nil {
		t.Fatal("stats on broken DB must fail")
	}
	if st.WriteErrors == 0 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestQueueOverflowCounted(t *testing.T) {
	old := storeQueueSize
	storeQueueSize = 2
	defer func() { storeQueueSize = old }()
	s, _ := newSQLStore(t, StoreOptions{})
	for i := 0; i < 20000 && s.dropped.Load() == 0; i++ {
		s.Record(mkCall(i, nil))
	}
	if s.dropped.Load() == 0 {
		t.Fatal("expected drops with a full queue")
	}
	p, err := s.Query(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Dropped == 0 {
		t.Fatal("page must surface the drop counter")
	}
	flush(t, s)
}

// Record, Query, Stats, and Close racing must stay sound; Close is
// idempotent and never closes the shared DB.
func TestConcurrentCloseRecordQuery(t *testing.T) {
	s, db := newSQLStore(t, StoreOptions{})
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				s.Record(mkCall(g*1000+i%1000, nil))
			}
		}(g)
	}
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				s.Query(context.Background(), Filter{Limit: 5})
				s.Stats(context.Background(), Filter{})
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	s.Close()
	s.Close() // idempotent
	close(stop)
	wg.Wait()
	s.Record(mkCall(999999, nil)) // after close: counted, no panic
	if err := db.Ping(); err != nil {
		t.Fatal("store closed the shared DB")
	}
}

// The event.go Start/End flow lands as one deduplicated call in the store.
func TestSpanIntegration(t *testing.T) {
	s, _ := newSQLStore(t, StoreOptions{})
	ctx := WithRecorder(context.Background(), s)
	ctx = WithMetadata(ctx, Metadata{SessionID: "sess-x", OwnerID: "owner-x"})
	_, span := Start(ctx, Call{Direction: "outbound", Server: "srv", Tool: "t"})
	span.End("ok", "")
	flush(t, s)
	st, err := s.Stats(context.Background(), Filter{SessionID: "sess-x"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 1 || st.OK != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

// JSON contract: nulls stay null, arrays never become null.
func TestJSONShape(t *testing.T) {
	s, _ := newSQLStore(t, StoreOptions{})
	p, err := s.Query(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"calls":[]`, `"next_cursor":""`, `"storage":"sqlite"`, `"dropped":0`, `"write_errors":0`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("page json %s missing %s", b, want)
		}
	}
	st, err := s.Stats(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	b, err = json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"error_rate":null`, `"p50_ms":null`, `"p95_ms":null`,
		`"max_ms":null`, `"by_tool":[]`, `"by_direction":{"inbound":0,"outbound":0}`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("stats json %s missing %s", b, want)
		}
	}
}
