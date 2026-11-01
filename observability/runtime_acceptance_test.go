package observability

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/yosukeno/pogo/mcpobserve"
)

func TestRuntimeIndependentDisableWithFreshDatabase(t *testing.T) {
	for _, disabled := range []struct{ mcp, skill bool }{{true, false}, {false, true}, {true, true}} {
		db, err := OpenDatabase(filepath.Join(t.TempDir(), "calls.db"))
		if err != nil {
			t.Fatal(err)
		}
		r, err := Open(Options{DB: db, DisableMCP: disabled.mcp, DisableSkill: disabled.skill})
		if err != nil {
			db.Close()
			t.Fatalf("disabled %+v: %v", disabled, err)
		}
		if (r.MCP == nil) != disabled.mcp || (r.Skills == nil) != disabled.skill {
			t.Fatal("independent gate changed")
		}
		r.Close()
		if r.HeartbeatErrors() != 0 {
			t.Fatal("disabled table was accessed during close")
		}
		db.Close()
	}
}

func TestRuntimeHeartbeatRecoversCrashedRecorderWithoutRestart(t *testing.T) {
	old := heartbeatInterval
	heartbeatInterval = 20 * time.Millisecond
	defer func() { heartbeatInterval = old }()
	db, err := OpenDatabase(filepath.Join(t.TempDir(), "calls.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r, err := Open(Options{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	id := mcpobserve.NewID()
	r.MCP.Record(mcpobserve.Call{ID: id, TraceID: mcpobserve.NewID(), Status: "running", StartedAt: time.Now().UnixMilli()})
	if err := r.MCP.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO observability_instances(id,started_at,last_heartbeat) VALUES('crashed',1,1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE mcpobserve_calls SET recorder_instance_id='crashed' WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		if err := db.QueryRow("SELECT status FROM mcpobserve_calls WHERE id=?", id).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == "interrupted" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("crashed recorder stayed running until another process started")
}

func TestRuntimeCloseBoundsLeaseWorkBehindOccupiedConnection(t *testing.T) {
	db, err := OpenDatabase(filepath.Join(t.TempDir(), "calls.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r, err := Open(Options{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { r.Close(); close(closed) }()
	select {
	case <-closed:
		conn.Close()
	case <-time.After(3500 * time.Millisecond):
		conn.Close()
		<-closed
		t.Fatal("lease shutdown waited indefinitely for a database connection")
	}
}
