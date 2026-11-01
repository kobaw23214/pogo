package web

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestMCPLogCloseDrainsAndStopsDatabaseWorkers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observe.db")
	st, err := newMCPLogStore(MCPLogOptions{DBPath: path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.close)
	for i := 0; i < 12; i++ {
		st.record(&MCPCallLog{Verdict: mcpVerdictOK, Transport: "streamable", HTTPMethod: "POST", ReqID: "fixture", Status: 200})
	}
	st.close()
	for _, stopped := range []chan struct{}{st.writerDone, st.sweepDone} {
		select {
		case <-stopped:
		default:
			t.Fatal("database worker outlived close")
		}
	}
	st.record(&MCPCallLog{Verdict: mcpVerdictOK})
	if st.dropped.Load() != 1 {
		t.Fatal("late write was not rejected")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mcp_calls`).Scan(&count); err != nil || count != 12 {
		t.Fatalf("queued history did not drain: %d %v", count, err)
	}
}

func TestMCPLogCloseCancelsDatabaseConnectionWait(t *testing.T) {
	st, err := newMCPLogStore(MCPLogOptions{DBPath: filepath.Join(t.TempDir(), "observe.db")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.close)
	st.db.SetMaxOpenConns(1)
	conn, err := st.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	st.record(&MCPCallLog{Verdict: mcpVerdictOK})
	done := make(chan struct{})
	go func() { st.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("close did not cancel a blocked connection acquisition")
	}
}
