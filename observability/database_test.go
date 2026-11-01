package observability

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenDatabasePragmas(t *testing.T) {
	db, err := OpenDatabase(filepath.Join(t.TempDir(), "obs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var journal string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if journal != "wal" {
		t.Errorf("journal_mode = %q, want wal", journal)
	}
	var busy int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil {
		t.Fatal(err)
	}
	if busy < 5000 {
		t.Errorf("busy_timeout = %d, want >= 5000", busy)
	}
}

func TestOpenDatabaseBadPath(t *testing.T) {
	// sql.Open is lazy; OpenDatabase must surface the failure via its pragma
	// verification rather than handing back a handle that errors later.
	parent := filepath.Join(t.TempDir(), "file-not-directory")
	if err := os.WriteFile(parent, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := OpenDatabase(filepath.Join(parent, "obs.db"))
	if err == nil {
		db.Close()
		t.Fatal("expected an error for a database below a regular file")
	}
}

func TestOpenDatabaseEscapedPathAndImmediateTransactions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new #目录", "history #1.db")
	db, err := OpenDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatal("database was not created at the exact path", err)
	}
	if _, err := db.Exec("CREATE TABLE fixture (id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	other, err := OpenDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	conn, err := other.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "PRAGMA busy_timeout=1"); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// BEGIN IMMEDIATE must already hold the write lock before its first
	// write. A fictitious txlock PRAGMA would leave this transaction deferred.
	if _, err := conn.ExecContext(context.Background(), "INSERT INTO fixture VALUES(1)"); err == nil {
		t.Fatal("transaction did not acquire the immediate write lock")
	}
}
