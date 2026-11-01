// Package catalog is the authoritative registry of which identity.Scope owns
// which session ID.
//
// The underlying session storage — JSONL transcripts and the session Manager
// — indexes sessions by global session ID, so ownership here keys on the ID
// alone: one ID names one session and one owner, ever. The scope columns
// record that owner, every query filters on them, and a tombstoned row keeps
// its ID, so ownership can be ended but never reassigned.
//
// This is the ownership counterpart to session's index: that index is a
// rebuildable cache derived from the transcripts and safe to delete, while
// these rows are the source of truth for ownership itself. The two stores
// share nothing; ownership is only ever what Create was explicitly told —
// never inferred from transcripts, environment, or file paths.
//
// Cross-scope access reports ErrNotFound, indistinguishable from a missing
// row: "exists but is not yours" is itself information no caller should get.
package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/yosukeno/pogo/identity"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Session states. creating marks a session being assembled before its first
// turn; active is the only state any read path returns; deleted is a
// tombstone — the row stays so the ID stays taken, but no operation treats
// it as existing.
const (
	stateCreating = "creating"
	stateActive   = "active"
	stateDeleted  = "deleted"
)

// Session is one catalog row. Times are Unix milliseconds; Create fills in
// any the caller leaves at zero.
type Session struct {
	ID        string         `json:"id"`
	Scope     identity.Scope `json:"scope"`
	Title     string         `json:"title"`
	State     string         `json:"state"`
	CreatedAt int64          `json:"created_at"`
	UpdatedAt int64          `json:"updated_at"`
}

var (
	// ErrNotFound covers missing IDs and, deliberately indistinguishably,
	// IDs that exist under another scope, in a non-active state, or
	// tombstoned.
	ErrNotFound = errors.New("catalog: session not found")
	// ErrConflict reports that the ID is already taken — by another scope,
	// by a live row, or by a tombstone. IDs are global, so once taken an ID
	// is never reassigned.
	ErrConflict = errors.New("catalog: session already exists")
)

// idPattern bounds session IDs to characters that are safe in file names,
// URLs, and SQL alike, so an ID never needs escaping to be handled. The same
// rule is a CHECK on the table, defense in depth against a caller that
// bypasses Create's validation.
var idPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

// List limits: the default when the caller passes limit <= 0, and the
// ceiling so no single query can pull the whole table.
const (
	defaultListLimit = 50
	maxListLimit     = 200
)

// schemaVersion is stamped into PRAGMA user_version. It gates opens, not
// migrations: a file carrying any other version was written by a build this
// one cannot reason about, and an authoritative store is the last place to
// guess.
const schemaVersion = 1

// applicationID is stamped into PRAGMA application_id (ASCII "PCAT": pogo
// catalog). user_version alone says which shape of catalog; application_id
// says which kind of database at all, so Open never mistakes some other
// SQLite file for an empty catalog.
const applicationID = 0x50434154

// schemaDDL declares the sessions table. STRICT keeps TEXT columns text and
// INTEGER columns integral; the CHECKs make the invariants the package
// relies on (safe IDs, complete ownership, known states) hold at the storage
// layer too. The primary key is the ID alone — the transcripts and the
// Manager index a session by its global ID, so one ID can name only one
// session — while the scope columns record its owner, and the composite
// index keeps scope-filtered reads from scanning the table.
const schemaDDL = `
CREATE TABLE sessions (
	id         TEXT    NOT NULL,
	app_id     TEXT    NOT NULL,
	tenant_id  TEXT    NOT NULL,
	subject_id TEXT    NOT NULL,
	project_id TEXT    NOT NULL,
	title      TEXT    NOT NULL,
	state      TEXT    NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (id),
	CHECK (id NOT GLOB '*[^a-zA-Z0-9_-]*'),
	CHECK (length(id) BETWEEN 1 AND 128),
	CHECK (app_id <> '' AND tenant_id <> '' AND subject_id <> '' AND project_id <> ''),
	CHECK (state IN ('creating', 'active', 'deleted')),
	CHECK (created_at >= 0 AND updated_at >= 0)
) STRICT;

CREATE INDEX sessions_scope_state ON sessions (
	app_id, tenant_id, subject_id, project_id, state, updated_at DESC, id
);
`

// Store is an opened catalog database.
type Store struct{ db *sql.DB }

// Open opens the catalog at path, creating it if absent. The parent
// directory is created 0700 and the database file 0600 (POSIX semantics;
// Windows ignores mode bits), so a catalog holding ownership records is
// private to the service user by default.
func Open(path string) (*Store, error) {
	return open(path, true)
}

// OpenExisting refuses to initialize lost or damaged authority. A deployment
// with an existing data marker must use it, even if catalog.db still exists.
func OpenExisting(path string) (*Store, error) {
	return open(path, false)
}

func open(path string, allowCreate bool) (*Store, error) {
	if path == "" {
		return nil, errors.New("catalog: database path is empty")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("catalog: create %s: %w", dir, err)
	}
	// Pre-create the file 0600 rather than letting SQLite pick the mode: the
	// WAL and shm sidecars inherit the main file's permissions, so this one
	// OpenFile sets the whole family. Chmod afterwards narrows a pre-existing
	// file that arrived with looser bits; best effort only.
	flags := os.O_RDWR
	if allowCreate {
		flags |= os.O_CREATE
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, fmt.Errorf("catalog: create %s: %w", path, err)
	}
	f.Close()
	_ = os.Chmod(path, 0o600)

	// Every pragma rides the DSN so it holds on each pooled connection: WAL
	// for concurrent readers, synchronous FULL because this store is
	// authoritative (durability outranks throughput here), a bounded busy
	// timeout so lock contention errors instead of hanging, enforced foreign
	// keys, and immediate transactions so two processes' write transactions
	// cannot deadlock upgrading a deferred lock.
	dsn := fileURI(path) +
		"?_journal_mode=WAL&_synchronous=FULL&_busy_timeout=5000&_foreign_keys=1&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("catalog: open %s: %w", path, err)
	}
	// One connection, always: SQLite pragmas are per-connection, and catalog
	// traffic is short statements, so serializing on a single connection is
	// cheaper and safer than keeping pragma state coherent across a pool.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.verifyPragmas(); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.initSchema(allowCreate); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the store. Everything is already durable in the file; there
// is no cache to flush and nothing to replay on the next Open.
func (s *Store) Close() error { return s.db.Close() }

// fileURI renders a native path as a file: URI. Each segment is
// percent-encoded so '#', '?', spaces, and non-ASCII bytes stay part of the
// path instead of starting a fragment or query string — the driver splits
// the DSN at the first unencoded '?' — while SQLite decodes the percent
// escapes back to the exact bytes of the original path. The drive colon on
// Windows and the volume root on Unix pass through untouched.
func fileURI(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return "file:" + strings.Join(parts, "/")
}

// verifyPragmas reads the connection's effective PRAGMA values and rejects
// any drift from what Open asked for. The DSN settings are load-bearing
// (WAL, durability, lock behavior), and a driver or DSN regression would
// otherwise fail silently while the database still opened.
func (s *Store) verifyPragmas() error {
	var journal string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
		return fmt.Errorf("catalog: read journal_mode: %w", err)
	}
	if !strings.EqualFold(journal, "wal") {
		return fmt.Errorf("catalog: journal_mode is %q, want WAL", journal)
	}
	var synchronous int
	if err := s.db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		return fmt.Errorf("catalog: read synchronous: %w", err)
	}
	if synchronous != 2 { // FULL
		return fmt.Errorf("catalog: synchronous is %d, want 2 (FULL)", synchronous)
	}
	var foreignKeys int
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		return fmt.Errorf("catalog: read foreign_keys: %w", err)
	}
	if foreignKeys != 1 {
		return fmt.Errorf("catalog: foreign_keys is %d, want 1", foreignKeys)
	}
	var busyTimeout int
	if err := s.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		return fmt.Errorf("catalog: read busy_timeout: %w", err)
	}
	if busyTimeout != 5000 {
		return fmt.Errorf("catalog: busy_timeout is %d, want 5000", busyTimeout)
	}
	return nil
}

// initSchema installs the schema on a completely empty database and refuses
// everything else. A file passes only when application_id and user_version
// carry exactly this build's marks; an unmarked file qualifies as new only
// while it holds no user tables or views at all. Any other combination —
// another application's marks, an unknown or future schema version, or
// unmarked rows someone left behind — is a database this package did not
// make, and re-initializing it would overwrite someone else's data. The
// whole check runs inside one immediate transaction, so two processes
// opening a fresh catalog serialize instead of racing the stamps.
func (s *Store) initSchema(allowCreate bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("catalog: begin: %w", err)
	}
	defer tx.Rollback()
	var appID, version int
	if err := tx.QueryRow(`PRAGMA application_id`).Scan(&appID); err != nil {
		return fmt.Errorf("catalog: read application_id: %w", err)
	}
	if err := tx.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("catalog: read schema version: %w", err)
	}
	if !allowCreate && (appID != applicationID || version != schemaVersion) {
		return errors.New("catalog: existing authority is missing or invalid; restore it instead of initializing a new catalog")
	}
	if appID != applicationID || version != schemaVersion {
		if appID != 0 || version != 0 {
			return fmt.Errorf("catalog: not a catalog database (application_id %#x, schema version %d; want %#x and %d)",
				appID, version, applicationID, schemaVersion)
		}
		var objects int
		if err := tx.QueryRow(`
			SELECT COUNT(*) FROM sqlite_master
			WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite\_%' ESCAPE '\'`,
		).Scan(&objects); err != nil {
			return fmt.Errorf("catalog: inspect existing objects: %w", err)
		}
		if objects != 0 {
			return errors.New("catalog: database holds tables or views but no catalog marks; refusing to open")
		}
		if _, err := tx.Exec(schemaDDL); err != nil {
			return fmt.Errorf("catalog: create schema: %w", err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA application_id = %d`, applicationID)); err != nil {
			return fmt.Errorf("catalog: stamp application_id: %w", err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion)); err != nil {
			return fmt.Errorf("catalog: stamp schema version: %w", err)
		}
	}
	return tx.Commit()
}

// Create registers rec under its own scope, the only path by which ownership
// is ever written. An empty State means creating; a zero timestamp means
// now. An ID already held — live or tombstoned, under this or any other
// scope — is ErrConflict, never an overwrite.
func (s *Store) Create(ctx context.Context, rec Session) error {
	if !rec.Scope.Valid() {
		return errors.New("catalog: invalid scope: an unattributable session cannot be owned")
	}
	if !idPattern.MatchString(rec.ID) {
		return fmt.Errorf("catalog: id %q does not match [a-zA-Z0-9_-]{1,128}", rec.ID)
	}
	state := rec.State
	switch state {
	case "":
		state = stateCreating
	case stateCreating, stateActive:
	default:
		return fmt.Errorf("catalog: state %q must be %q or %q", rec.State, stateCreating, stateActive)
	}
	now := time.Now().UnixMilli()
	created, updated := rec.CreatedAt, rec.UpdatedAt
	if created == 0 {
		created = now
	}
	if updated == 0 {
		updated = now
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (id, app_id, tenant_id, subject_id, project_id, title, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.Scope.AppID, rec.Scope.TenantID, rec.Scope.SubjectID, rec.Scope.ProjectID,
		rec.Title, state, created, updated)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrConflict
		}
		return fmt.Errorf("catalog: create %s: %w", rec.ID, err)
	}
	return nil
}

// Activate moves a creating session to active. Activating an already-active
// session is idempotent; a tombstoned one is gone for good — no call can
// resurrect it — and reports ErrNotFound like any invisible row.
func (s *Store) Activate(ctx context.Context, scope identity.Scope, id string) error {
	if !scope.Valid() {
		return errInvalidScope()
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE sessions SET state = ?, updated_at = ?
		WHERE app_id = ? AND tenant_id = ? AND subject_id = ? AND project_id = ? AND id = ? AND state = ?`,
		stateActive, time.Now().UnixMilli(),
		scope.AppID, scope.TenantID, scope.SubjectID, scope.ProjectID, id, stateCreating)
	if err != nil {
		return fmt.Errorf("catalog: activate %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	// No transition happened: either idempotent re-activation or invisible.
	return s.requireState(ctx, scope, id, stateActive)
}

// Get returns the active session id under scope. Creating, deleted, and
// other-scope rows are indistinguishable from absent ones.
func (s *Store) Get(ctx context.Context, scope identity.Scope, id string) (Session, error) {
	if !scope.Valid() {
		return Session{}, errInvalidScope()
	}
	var rec Session
	err := s.db.QueryRowContext(ctx, `
		SELECT id, app_id, tenant_id, subject_id, project_id, title, state, created_at, updated_at
		FROM sessions
		WHERE app_id = ? AND tenant_id = ? AND subject_id = ? AND project_id = ? AND id = ? AND state = ?`,
		scope.AppID, scope.TenantID, scope.SubjectID, scope.ProjectID, id, stateActive,
	).Scan(&rec.ID, &rec.Scope.AppID, &rec.Scope.TenantID, &rec.Scope.SubjectID, &rec.Scope.ProjectID,
		&rec.Title, &rec.State, &rec.CreatedAt, &rec.UpdatedAt)
	if err == sql.ErrNoRows {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("catalog: get %s: %w", id, err)
	}
	return rec, nil
}

// OwnerOf resolves which scope owns an id, without knowing the scope up
// front. This is an authority question, not an access check: it exists so an
// internal caller (the Manager loading a transcript it was already told to
// load) can pick that owner's memory and workspace roots. Every HTTP-facing
// authorization still goes through the scoped Get, where another scope's row
// is indistinguishable from a missing one.
//
// Tombstoned rows still answer: ownership outlives visibility, and the caller
// that needs "usable" re-checks state itself.
func (s *Store) OwnerOf(ctx context.Context, id string) (Session, error) {
	var rec Session
	err := s.db.QueryRowContext(ctx, `
		SELECT id, app_id, tenant_id, subject_id, project_id, title, state, created_at, updated_at
		FROM sessions
		WHERE id = ?`, id,
	).Scan(&rec.ID, &rec.Scope.AppID, &rec.Scope.TenantID, &rec.Scope.SubjectID, &rec.Scope.ProjectID,
		&rec.Title, &rec.State, &rec.CreatedAt, &rec.UpdatedAt)
	if err == sql.ErrNoRows {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("catalog: owner of %s: %w", id, err)
	}
	return rec, nil
}

// List returns scope's active sessions whose title contains query, newest
// update first with the ID as tiebreak. limit defaults to 50 and caps at 200.
// The scope filter, the state filter, and the limit all apply inside one
// query, so other scopes' rows can neither appear nor eat into the limit,
// and LIKE metacharacters in query match themselves, not everything.
func (s *Store) List(ctx context.Context, scope identity.Scope, query string, limit int) ([]Session, error) {
	if !scope.Valid() {
		return nil, errInvalidScope()
	}
	if limit <= 0 {
		limit = defaultListLimit
	} else if limit > maxListLimit {
		limit = maxListLimit
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, app_id, tenant_id, subject_id, project_id, title, state, created_at, updated_at
		FROM sessions
		WHERE app_id = ? AND tenant_id = ? AND subject_id = ? AND project_id = ? AND state = ?
		  AND title LIKE ? ESCAPE '\'
		ORDER BY updated_at DESC, id
		LIMIT ?`,
		scope.AppID, scope.TenantID, scope.SubjectID, scope.ProjectID, stateActive,
		"%"+escapeLike(query)+"%", limit)
	if err != nil {
		return nil, fmt.Errorf("catalog: list: %w", err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var rec Session
		if err := rows.Scan(&rec.ID, &rec.Scope.AppID, &rec.Scope.TenantID, &rec.Scope.SubjectID, &rec.Scope.ProjectID,
			&rec.Title, &rec.State, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
			return nil, fmt.Errorf("catalog: list: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("catalog: list: %w", err)
	}
	return out, nil
}

// Rename retitles an active session. Anything else — missing, creating,
// tombstoned, or another scope's — is ErrNotFound, and the row is untouched.
func (s *Store) Rename(ctx context.Context, scope identity.Scope, id, title string) error {
	if !scope.Valid() {
		return errInvalidScope()
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE sessions SET title = ?, updated_at = ?
		WHERE app_id = ? AND tenant_id = ? AND subject_id = ? AND project_id = ? AND id = ? AND state = ?`,
		title, time.Now().UnixMilli(),
		scope.AppID, scope.TenantID, scope.SubjectID, scope.ProjectID, id, stateActive)
	if err != nil {
		return fmt.Errorf("catalog: rename %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Tombstone marks a session deleted without deleting the row: the ID stays
// taken, so ownership can be ended but never reassigned. Active and creating
// sessions can be tombstoned; tombstoning again is idempotent for the same
// owner; another scope's row is not found and not touched.
func (s *Store) Tombstone(ctx context.Context, scope identity.Scope, id string) error {
	if !scope.Valid() {
		return errInvalidScope()
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE sessions SET state = ?, updated_at = ?
		WHERE app_id = ? AND tenant_id = ? AND subject_id = ? AND project_id = ? AND id = ? AND state IN (?, ?)`,
		stateDeleted, time.Now().UnixMilli(),
		scope.AppID, scope.TenantID, scope.SubjectID, scope.ProjectID, id, stateActive, stateCreating)
	if err != nil {
		return fmt.Errorf("catalog: tombstone %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	// No transition happened: either idempotent re-tombstone or invisible.
	return s.requireState(ctx, scope, id, stateDeleted)
}

// requireState re-reads a row an update skipped and maps the outcome: state
// want is the idempotent no-op the caller's second call deserves, and
// everything else — missing row, wrong state, another scope's row — is
// ErrNotFound.
func (s *Store) requireState(ctx context.Context, scope identity.Scope, id, want string) error {
	var state string
	err := s.db.QueryRowContext(ctx, `
		SELECT state FROM sessions
		WHERE app_id = ? AND tenant_id = ? AND subject_id = ? AND project_id = ? AND id = ?`,
		scope.AppID, scope.TenantID, scope.SubjectID, scope.ProjectID, id).Scan(&state)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("catalog: read %s: %w", id, err)
	}
	if state == want {
		return nil
	}
	return ErrNotFound
}

// escapeLike escapes LIKE's metacharacters — and the backslash that escapes
// them — so %, _, and \ in a query match those characters literally.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func errInvalidScope() error {
	return errors.New("catalog: invalid scope")
}

// isUniqueViolation reports whether err is SQLite rejecting a duplicate key,
// which is how Create says the ID is taken.
func isUniqueViolation(err error) bool {
	var serr *sqlite.Error
	if !errors.As(err, &serr) {
		return false
	}
	return serr.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY ||
		serr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
}
