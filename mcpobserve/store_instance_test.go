package mcpobserve

import (
	"context"
	"testing"
)

// A table created by the previous schema (no span/client/entrypoint/instance
// columns) is upgraded additively: old rows keep their data and their
// outcome, new writes round-trip the new fields.
func TestMigrationFromPreviousSchema(t *testing.T) {
	db := testDB(t)
	_, err := db.Exec(`CREATE TABLE ` + storeTable + ` (
		id TEXT PRIMARY KEY,
		trace_id TEXT NOT NULL DEFAULT '',
		parent_id TEXT NOT NULL DEFAULT '',
		direction TEXT NOT NULL DEFAULT '',
		server TEXT NOT NULL DEFAULT '',
		tool TEXT NOT NULL DEFAULT '',
		wire_tool TEXT NOT NULL DEFAULT '',
		transport TEXT NOT NULL DEFAULT '',
		client TEXT NOT NULL DEFAULT '',
		mcp_session_id TEXT NOT NULL DEFAULT '',
		request_id TEXT NOT NULL DEFAULT '',
		session_id TEXT NOT NULL DEFAULT '',
		run_id TEXT NOT NULL DEFAULT '',
		task_id TEXT NOT NULL DEFAULT '',
		owner_id TEXT NOT NULL DEFAULT '',
		tool_call_id TEXT NOT NULL DEFAULT '',
		started_at INTEGER NOT NULL,
		finished_at INTEGER NOT NULL DEFAULT 0,
		duration_ms REAL NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT '',
		error_kind TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		t.Fatal(err)
	}
	legacyID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := db.Exec(`INSERT INTO `+storeTable+`
		(id, trace_id, direction, server, tool, started_at, finished_at, duration_ms, status)
		VALUES (?, 'legacy-trace', 'outbound', 'srv', 'old-tool', 1000, 1005, 5, 'ok')`, legacyID); err != nil {
		t.Fatal(err)
	}
	// A running row from the dead previous process: legacy single-process
	// recovery (no InstanceID/SkipRecovery) flips it, exactly as before.
	if _, err := db.Exec(`INSERT INTO ` + storeTable + `
		(id, trace_id, direction, server, tool, started_at, status)
		VALUES ('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 'legacy-trace', 'outbound', 'srv', 'stuck', 2000, 'running')`); err != nil {
		t.Fatal(err)
	}

	s, err := NewStore(StoreOptions{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	legacy, err := s.Detail(context.Background(), legacyID)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Tool != "old-tool" || legacy.Status != "ok" || legacy.DurationMs != 5 {
		t.Errorf("legacy row changed by migration: %+v", legacy)
	}
	if legacy.SpanID != "" || legacy.ClientID != "" || legacy.Entrypoint != "" {
		t.Errorf("legacy row gained phantom fields: %+v", legacy)
	}
	stuck, err := s.Detail(context.Background(), "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if err != nil {
		t.Fatal(err)
	}
	if stuck.Status != "interrupted" {
		t.Errorf("default-options store left stale running row %q", stuck.Status)
	}

	// New writes round-trip every new column.
	c := mkCall(9000, func(c *Call) {
		c.SpanID = "1111111111111111"
		c.ParentSpanID = "2222222222222222"
		c.TraceFlags = "01"
		c.ClientID = "caller-9"
		c.Entrypoint = "cli"
	})
	recordAll(t, s, []Call{c})
	got, err := s.Detail(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SpanID != c.SpanID || got.ParentSpanID != c.ParentSpanID ||
		got.TraceFlags != "01" || got.ClientID != "caller-9" || got.Entrypoint != "cli" {
		t.Errorf("round-trip = %+v, want new fields of %+v", got, c)
	}

	// Reopening the migrated table is a no-op migration.
	s.Close()
	s2, err := NewStore(StoreOptions{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	s2.Close()
}

// SkipRecovery suppresses the startup flip; the observability runtime uses
// it so a CLI start never interrupts the web server's live spans.
func TestSkipRecoveryLeavesRunningRows(t *testing.T) {
	db := testDB(t)
	s1, err := NewStore(StoreOptions{DB: db, InstanceID: "inst-1", SkipRecovery: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s1.Close()
	s1.Record(mkCall(1, func(c *Call) { c.Status = "running" }))
	flush(t, s1)

	s2, err := NewStore(StoreOptions{DB: db, InstanceID: "inst-2", SkipRecovery: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err := s2.Detail(context.Background(), mkCall(1, nil).ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "running" {
		t.Errorf("SkipRecovery store flipped a foreign running row to %q", got.Status)
	}
	// The row carries its writer's stamp.
	var inst string
	if err := db.QueryRow(`SELECT recorder_instance_id FROM `+storeTable+
		` WHERE id = ?`, got.ID).Scan(&inst); err != nil {
		t.Fatal(err)
	}
	if inst != "inst-1" {
		t.Errorf("recorder_instance_id = %q, want inst-1", inst)
	}
}

// ClientID and Entrypoint filter identically on both backends.
func TestClientIDEntrypointFilterParity(t *testing.T) {
	calls := []Call{
		mkCall(1, func(c *Call) { c.ClientID, c.Entrypoint = "alice", "cli" }),
		mkCall(2, func(c *Call) { c.ClientID, c.Entrypoint = "bob", "mcp" }),
		mkCall(3, func(c *Call) { c.ClientID, c.Entrypoint = "alice", "pogo" }),
	}
	sqlStore, _ := newSQLStore(t, StoreOptions{})
	recordAll(t, sqlStore, calls)
	memStore, err := NewStore(StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer memStore.Close()
	recordAll(t, memStore, calls)

	for _, f := range []Filter{
		{ClientID: "alice"},
		{Entrypoint: "cli"},
		{ClientID: "alice", Entrypoint: "pogo"},
		{Query: "caller", ClientID: "nobody"},
	} {
		sqlIDs := []string{}
		for _, c := range collectAll(t, sqlStore, f) {
			sqlIDs = append(sqlIDs, c.ID)
		}
		memIDs := []string{}
		for _, c := range collectAll(t, memStore, f) {
			memIDs = append(memIDs, c.ID)
		}
		if len(sqlIDs) != len(memIDs) {
			t.Fatalf("filter %+v: sql %v vs memory %v", f, sqlIDs, memIDs)
		}
		for i := range sqlIDs {
			if sqlIDs[i] != memIDs[i] {
				t.Fatalf("filter %+v: sql %v vs memory %v", f, sqlIDs, memIDs)
			}
		}
		sqlStats, err := sqlStore.Stats(context.Background(), f)
		if err != nil {
			t.Fatal(err)
		}
		memStats, err := memStore.Stats(context.Background(), f)
		if err != nil {
			t.Fatal(err)
		}
		if sqlStats.Total != memStats.Total {
			t.Errorf("filter %+v: sql total %d vs memory %d", f, sqlStats.Total, memStats.Total)
		}
	}

	// Sanity: the filters actually select.
	if got := collectAll(t, sqlStore, Filter{ClientID: "alice"}); len(got) != 2 {
		t.Errorf("ClientID=alice matched %d, want 2", len(got))
	}
	if got := collectAll(t, sqlStore, Filter{Entrypoint: "mcp"}); len(got) != 1 {
		t.Errorf("Entrypoint=mcp matched %d, want 1", len(got))
	}
}
