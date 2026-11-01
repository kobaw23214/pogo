package skillobserve

import (
	"context"
	"testing"
)

// A table created by the previous schema (no span/client/instance columns)
// is upgraded additively: old rows keep their data and their outcome, new
// writes round-trip the new fields.
func TestMigrationFromPreviousSchema(t *testing.T) {
	db := testDB(t)
	_, err := db.Exec(`CREATE TABLE ` + storeTable + ` (
		id TEXT PRIMARY KEY,
		trace_id TEXT NOT NULL DEFAULT '',
		parent_id TEXT NOT NULL DEFAULT '',
		skill_id TEXT NOT NULL DEFAULT '',
		skill_name TEXT NOT NULL DEFAULT '',
		skill_source TEXT NOT NULL DEFAULT '',
		stage TEXT NOT NULL DEFAULT '',
		caller TEXT NOT NULL DEFAULT '',
		entrypoint TEXT NOT NULL DEFAULT '',
		mechanism TEXT NOT NULL DEFAULT '',
		tool TEXT NOT NULL DEFAULT '',
		wire_tool TEXT NOT NULL DEFAULT '',
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
		(id, trace_id, skill_id, skill_name, stage, caller, started_at, finished_at, duration_ms, status)
		VALUES (?, 'legacy-trace', 'sk-1', 'demo', 'execute', 'internal', 1000, 1005, 5, 'ok')`, legacyID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO ` + storeTable + `
		(id, trace_id, skill_id, skill_name, stage, caller, started_at, status)
		VALUES ('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 'legacy-trace', 'sk-1', 'demo', 'execute', 'internal', 2000, 'running')`); err != nil {
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
	if legacy.SkillName != "demo" || legacy.Status != "ok" || legacy.DurationMs != 5 {
		t.Errorf("legacy row changed by migration: %+v", legacy)
	}
	if legacy.SpanID != "" || legacy.ClientID != "" {
		t.Errorf("legacy row gained phantom fields: %+v", legacy)
	}
	stuck, err := s.Detail(context.Background(), "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if err != nil {
		t.Fatal(err)
	}
	if stuck.Status != "interrupted" {
		t.Errorf("default-options store left stale running row %q", stuck.Status)
	}

	e := mkEvent(9000, func(e *Event) {
		e.SpanID = "1111111111111111"
		e.ParentSpanID = "2222222222222222"
		e.TraceFlags = "01"
		e.ClientID = "caller-9"
	})
	recordAll(t, s, []Event{e})
	got, err := s.Detail(context.Background(), e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SpanID != e.SpanID || got.ParentSpanID != e.ParentSpanID ||
		got.TraceFlags != "01" || got.ClientID != "caller-9" {
		t.Errorf("round-trip = %+v, want new fields of %+v", got, e)
	}

	s.Close()
	s2, err := NewStore(StoreOptions{DB: db}) // reopening is a no-op migration
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
	s1.Record(mkEvent(1, func(e *Event) { e.Status = "running" }))
	flush(t, s1)

	s2, err := NewStore(StoreOptions{DB: db, InstanceID: "inst-2", SkipRecovery: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err := s2.Detail(context.Background(), mkEvent(1, nil).ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "running" {
		t.Errorf("SkipRecovery store flipped a foreign running row to %q", got.Status)
	}
	var inst string
	if err := db.QueryRow(`SELECT recorder_instance_id FROM `+storeTable+
		` WHERE id = ?`, got.ID).Scan(&inst); err != nil {
		t.Fatal(err)
	}
	if inst != "inst-1" {
		t.Errorf("recorder_instance_id = %q, want inst-1", inst)
	}
}

// ClientID filters identically on both backends.
func TestClientIDFilterParity(t *testing.T) {
	events := []Event{
		mkEvent(1, func(e *Event) { e.ClientID = "alice" }),
		mkEvent(2, func(e *Event) { e.ClientID = "bob" }),
		mkEvent(3, func(e *Event) { e.ClientID = "alice" }),
	}
	sqlStore, _ := newSQLStore(t, StoreOptions{})
	recordAll(t, sqlStore, events)
	memStore, err := NewStore(StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer memStore.Close()
	recordAll(t, memStore, events)

	f := Filter{ClientID: "alice"}
	sqlIDs := []string{}
	for _, e := range collectAll(t, sqlStore, f) {
		sqlIDs = append(sqlIDs, e.ID)
	}
	memIDs := []string{}
	for _, e := range collectAll(t, memStore, f) {
		memIDs = append(memIDs, e.ID)
	}
	if len(sqlIDs) != 2 || len(memIDs) != 2 {
		t.Fatalf("ClientID=alice: sql %v vs memory %v, want 2 each", sqlIDs, memIDs)
	}
	for i := range sqlIDs {
		if sqlIDs[i] != memIDs[i] {
			t.Fatalf("ClientID=alice: sql %v vs memory %v", sqlIDs, memIDs)
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
	if sqlStats.Total != memStats.Total || sqlStats.Total != 2 {
		t.Errorf("stats total: sql %d vs memory %d, want 2", sqlStats.Total, memStats.Total)
	}
}
