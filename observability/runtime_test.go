package observability

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/skillobserve"
)

func openDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := OpenDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func openRuntime(t *testing.T, db *sql.DB) *Runtime {
	t.Helper()
	rt, err := Open(Options{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	return rt
}

func flushAll(t *testing.T, rt *Runtime) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if rt.MCP != nil {
		if err := rt.MCP.Flush(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if rt.Skills != nil {
		if err := rt.Skills.Flush(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

func mcpStatus(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var status string
	if err := db.QueryRow(`SELECT status FROM mcpobserve_calls WHERE id = ?`, id).
		Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func skillStatus(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var status string
	if err := db.QueryRow(`SELECT status FROM skillobserve_events WHERE id = ?`, id).
		Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func recordRunning(t *testing.T, rt *Runtime, id string) {
	t.Helper()
	rt.MCP.Record(mcpobserve.Call{ID: id, TraceID: "tr", Direction: "outbound",
		Server: "srv", Tool: "tool", StartedAt: time.Now().UnixMilli(), Status: "running"})
	rt.Skills.Record(skillobserve.Event{ID: id, TraceID: "tr", SkillID: "sk",
		Stage: "execute", Caller: "internal", Entrypoint: "cli",
		StartedAt: time.Now().UnixMilli(), Status: "running"})
	flushAll(t, rt)
}

// The hard boundary: a second process opening the shared database must not
// interrupt the first (still live) process's running spans.
func TestTwoRuntimesDoNotInterruptEachOther(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "obs.db"))
	rt1 := openRuntime(t, db)
	recordRunning(t, rt1, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	rt2 := openRuntime(t, db) // simulates a CLI starting while web runs
	flushAll(t, rt2)

	if got := mcpStatus(t, db, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); got != "running" {
		t.Errorf("mcp row status = %q after second runtime opened, want running", got)
	}
	if got := skillStatus(t, db, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); got != "running" {
		t.Errorf("skill row status = %q after second runtime opened, want running", got)
	}
	// Rows written by rt1 carry its instance stamp.
	var inst string
	if err := db.QueryRow(`SELECT recorder_instance_id FROM mcpobserve_calls
		WHERE id = ?`, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa").Scan(&inst); err != nil {
		t.Fatal(err)
	}
	if inst != rt1.InstanceID() {
		t.Errorf("recorder_instance_id = %q, want %q", inst, rt1.InstanceID())
	}
	if inst == rt2.InstanceID() {
		t.Error("the two runtimes share an instance id")
	}
}

// A killed process leaves a stale lease and running rows; the next opener
// recovers them. Legacy rows (no recorder stamp) are recovered too.
func TestStaleAndLegacyRecovery(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "obs.db"))
	rt1 := openRuntime(t, db) // establishes schema and lease table

	stale := time.Now().Add(-2 * staleAfter).UnixMilli()
	if _, err := db.Exec(`INSERT INTO `+leaseTable+`
		(id, pid, started_at, last_heartbeat, closed_at) VALUES ('dead', 0, ?, ?, 0)`,
		stale, stale); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	for _, q := range []string{
		`INSERT INTO mcpobserve_calls (id, trace_id, started_at, status, recorder_instance_id)
			VALUES ('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 'tr', ?, 'running', 'dead')`,
		`INSERT INTO mcpobserve_calls (id, trace_id, started_at, status, recorder_instance_id)
			VALUES ('cccccccccccccccccccccccccccccccc', 'tr', ?, 'running', '')`,
		`INSERT INTO skillobserve_events (id, trace_id, started_at, status, recorder_instance_id)
			VALUES ('dddddddddddddddddddddddddddddddd', 'tr', ?, 'running', 'dead')`,
	} {
		if _, err := db.Exec(q, now); err != nil {
			t.Fatal(err)
		}
	}

	rt2 := openRuntime(t, db)
	flushAll(t, rt2)
	for id, table := range map[string]func(*testing.T, *sql.DB, string) string{
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb": mcpStatus,
		"cccccccccccccccccccccccccccccccc": mcpStatus,
		"dddddddddddddddddddddddddddddddd": skillStatus,
	} {
		if got := table(t, db, id); got != "interrupted" {
			t.Errorf("row %s status = %q, want interrupted (stale/legacy recorder)", id, got)
		}
	}

	// rt1 is live; a running row stamped with its instance survives rt2.
	rt1.MCP.Record(mcpobserve.Call{ID: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		TraceID: "tr", Direction: "outbound", Server: "srv", Tool: "tool",
		StartedAt: now, Status: "running"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rt1.MCP.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	rt3 := openRuntime(t, db)
	flushAll(t, rt3)
	if got := mcpStatus(t, db, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"); got != "running" {
		t.Errorf("live instance row status = %q, want running", got)
	}
}

// Close joins the store workers, marks only this instance's remaining
// running rows interrupted, closes the lease — and never the caller's DB.
func TestCloseMarksOwnInterruptedOnly(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "obs.db"))
	rt1 := openRuntime(t, db)
	rt2, err := Open(Options{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	recordRunning(t, rt1, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	recordRunning(t, rt2, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	rt2.Close()
	if got := mcpStatus(t, db, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"); got != "interrupted" {
		t.Errorf("own row after Close = %q, want interrupted", got)
	}
	if got := skillStatus(t, db, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"); got != "interrupted" {
		t.Errorf("own skill row after Close = %q, want interrupted", got)
	}
	if got := mcpStatus(t, db, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); got != "running" {
		t.Errorf("other runtime's row after Close = %q, want running", got)
	}
	var closedAt int64
	if err := db.QueryRow(`SELECT closed_at FROM `+leaseTable+` WHERE id = ?`,
		rt2.InstanceID()).Scan(&closedAt); err != nil {
		t.Fatal(err)
	}
	if closedAt == 0 {
		t.Error("lease still open after Close")
	}
	// Caller-owned DB survives Runtime.Close, by contract.
	if err := db.Ping(); err != nil {
		t.Errorf("caller-owned DB closed by Runtime.Close: %v", err)
	}
	rt2.Close() // idempotent
}

// Memory mode (DB nil) works without any lease bookkeeping.
func TestMemoryRuntimeHasNoLease(t *testing.T) {
	rt, err := Open(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	rt.MCP.Record(mcpobserve.Call{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		TraceID: "tr", Direction: "outbound", Server: "srv", Tool: "tool",
		StartedAt: time.Now().UnixMilli(), Status: "ok"})
	page, err := rt.MCP.Query(context.Background(), mcpobserve.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Storage != "memory" || len(page.Calls) != 1 {
		t.Errorf("storage = %q, calls = %d; want memory/1", page.Storage, len(page.Calls))
	}
}

type fakeSink struct {
	mcp    []mcpobserve.Call
	skills []skillobserve.Event
}

func (f *fakeSink) RecordMCP(c mcpobserve.Call)      { f.mcp = append(f.mcp, c) }
func (f *fakeSink) RecordSkill(e skillobserve.Event) { f.skills = append(f.skills, e) }

// The sink fan-out records locally even with no exporter attached, and fans
// out when one is; disabling a half records nothing for it.
func TestRecorderFanoutAndGates(t *testing.T) {
	sink := &fakeSink{}
	rt, err := Open(Options{Sink: sink})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if rec := rt.MCPRecorder(); rec != nil {
		rec.Record(mcpobserve.Call{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			TraceID: "tr", StartedAt: time.Now().UnixMilli(), Status: "ok"})
	}
	if rec := rt.SkillRecorder(); rec != nil {
		rec.Record(skillobserve.Event{ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			TraceID: "tr", SkillID: "sk", StartedAt: time.Now().UnixMilli(), Status: "ok"})
	}
	if len(sink.mcp) != 1 || len(sink.skills) != 1 {
		t.Errorf("sink got %d mcp / %d skill, want 1/1", len(sink.mcp), len(sink.skills))
	}
	if n, _ := rt.MCP.Stats(context.Background(), mcpobserve.Filter{}); n.Total != 1 {
		t.Errorf("local mcp total = %d, want 1 (local recording with a sink)", n.Total)
	}

	gated, err := Open(Options{DisableMCP: true, DisableSkill: true})
	if err != nil {
		t.Fatal(err)
	}
	defer gated.Close()
	if gated.MCPRecorder() != nil || gated.SkillRecorder() != nil {
		t.Error("disabled halves must not hand out recorders")
	}
}

// Context installs metadata and both recorders in the shape the web runner
// builds by hand.
func TestRuntimeContext(t *testing.T) {
	rt, err := Open(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	ctx := rt.Context(context.Background(), mcpobserve.Metadata{
		TraceID: "trace-1", SessionID: "sess-1", RunID: "run-1", Entrypoint: "cli",
	})
	md := mcpobserve.MetadataFrom(ctx)
	if md.TraceID != "trace-1" || md.RunID != "run-1" || md.Entrypoint != "cli" {
		t.Errorf("metadata = %+v", md)
	}
	// A started MCP span flows into the local store through the context alone.
	_, span := mcpobserve.Start(ctx, mcpobserve.Call{Direction: "outbound", Server: "s", Tool: "t"})
	span.End("ok", "")
	st, _ := rt.MCP.Stats(context.Background(), mcpobserve.Filter{})
	if st.Total != 1 {
		t.Errorf("mcp total = %d, want 1", st.Total)
	}
}
