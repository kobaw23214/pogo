// Shared observability database lifecycle.
//
// One SQLite file (mcplog.db) is shared between the long-running web server
// and any number of short-lived CLI processes. That sharing only works with
// the right pragmas: WAL so a reader never blocks the writer and vice versa,
// a busy timeout so a CLI starting while the web server writes waits instead
// of erroring, and an immediate txlock so write transactions fail fast under
// contention instead of upgrading mid-transaction.
package observability

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// OpenDatabase opens the shared observability database with the modernc
// sqlite driver. Explicit pragmas are verified on the real connection.
// Transaction mode is a driver option (_txlock), not a SQLite pragma.
//
// The caller owns the returned handle: Runtime.Close never closes it.
func OpenDatabase(path string) (*sql.DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("observability: empty database path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	dsn := "file:" + strings.Join(parts, "/") + "?_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)" +
		"&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Verify the pragmas actually took effect instead of trusting the DSN
	// string: an ignored pragma is exactly the failure mode this function
	// exists to prevent, and it fails silently without this check.
	var journal string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		db.Close()
		return nil, fmt.Errorf("observability: journal_mode pragma: %w", err)
	}
	if journal != "wal" {
		db.Close()
		return nil, fmt.Errorf("observability: journal_mode = %q, want wal", journal)
	}
	var busy int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil {
		db.Close()
		return nil, fmt.Errorf("observability: busy_timeout pragma: %w", err)
	}
	if busy < 5000 {
		db.Close()
		return nil, fmt.Errorf("observability: busy_timeout = %d, want >= 5000", busy)
	}
	// Keep readers available while a bounded number of connections queue for
	// SQLite's single writer. Every connection receives the same pragmas.
	db.SetMaxOpenConns(4)
	return db, nil
}
