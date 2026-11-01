package taskqueue

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

type rowScanner interface{ Scan(...any) error }

type queryer interface {
	QueryRow(string, ...any) *sql.Row
	Query(string, ...any) (*sql.Rows, error)
}

type storedRun struct {
	Run
	digest       string
	finishedFrom int64 // Only Finish writes this; Recover must not admit a late worker.
	readyAt      int64 // Scheduling time may be deferred without changing Request.
}

const columns = `id,request,payload,digest,status,revision,attempt,cancel_requested,created_at,started_at,finished_at,session_id,summary,error,end_reason,finished_from_revision,due_at`

func now() int64 { return time.Now().UnixMilli() }

// Open requires a regular, disk-backed database. No error falls back to memory.
// Opening does not recover active jobs: the host must acquire its controller
// lease before explicitly calling Recover.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" || strings.EqualFold(path, ":memory:") || strings.HasPrefix(strings.ToLower(path), "file:") || len(path) > 4096 || !utf8.ValidString(path) || strings.ContainsRune(path, 0) {
		return nil, fmt.Errorf("%w: a persistent filesystem database path is required", ErrConflict)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(abs); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: database must be a regular file", ErrConflict)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	db, err := sql.Open("sqlite", u.String()+"?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Store, error) { db.Close(); return nil, err }
	db.SetMaxOpenConns(4)
	var journal string
	var synchronous, busy int
	if err = db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		return fail(err)
	}
	if err = db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		return fail(err)
	}
	if err = db.QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil {
		return fail(err)
	}
	if journal != "wal" || synchronous != 2 || busy < 5000 {
		return fail(fmt.Errorf("%w: durable WAL/FULL settings unavailable", ErrConflict))
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS taskqueue_schema(version INTEGER NOT NULL);
INSERT INTO taskqueue_schema SELECT 1 WHERE NOT EXISTS(SELECT 1 FROM taskqueue_schema);
CREATE TABLE IF NOT EXISTS taskqueue_runs(
 id TEXT PRIMARY KEY, task_id TEXT NOT NULL, trigger_id TEXT NOT NULL,
 owner_id TEXT NOT NULL, agent_id TEXT NOT NULL, due_at INTEGER NOT NULL,
 request BLOB NOT NULL, payload BLOB, digest TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('queued','running','completed','failed','cancelled','interrupted')),
 revision INTEGER NOT NULL, attempt INTEGER NOT NULL, cancel_requested INTEGER NOT NULL DEFAULT 0,
 created_at INTEGER NOT NULL, started_at INTEGER NOT NULL DEFAULT 0, finished_at INTEGER NOT NULL DEFAULT 0,
 session_id TEXT NOT NULL DEFAULT '', summary TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', end_reason TEXT NOT NULL DEFAULT '',
 finished_from_revision INTEGER NOT NULL DEFAULT 0,
 UNIQUE(task_id,trigger_id));
CREATE INDEX IF NOT EXISTS taskqueue_dispatch ON taskqueue_runs(status,due_at,created_at,id);
CREATE INDEX IF NOT EXISTS taskqueue_scope ON taskqueue_runs(owner_id,agent_id,task_id,created_at);
CREATE UNIQUE INDEX IF NOT EXISTS taskqueue_one_running_per_task ON taskqueue_runs(task_id) WHERE status='running';`)
	if err != nil {
		return fail(err)
	}
	var version int
	if err = db.QueryRow("SELECT version FROM taskqueue_schema").Scan(&version); err != nil {
		return fail(err)
	}
	if version != 1 {
		return fail(fmt.Errorf("%w: unsupported queue schema version", ErrConflict))
	}
	if err := initWorkflowSchema(db); err != nil {
		return fail(err)
	}
	if err := initWorkflowCallbackSchema(db); err != nil {
		return fail(err)
	}
	if err := initAgentWorkflowSchema(db); err != nil {
		return fail(err)
	}
	if err := initAgentWorkflowLayoutSchema(db); err != nil {
		return fail(err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func validText(value string, limit int, required bool) bool {
	return len(value) <= limit && utf8.ValidString(value) && !strings.ContainsRune(value, 0) && (!required || strings.TrimSpace(value) != "")
}

func validateRequest(r Request) error {
	checks := []struct {
		value    string
		limit    int
		required bool
	}{
		{r.TaskID, 256, true}, {r.Source, 64, true}, {r.SourceID, 512, true},
		{r.Trigger, 64, true}, {r.TriggerID, 512, true}, {r.DefinitionHash, 256, true},
		{r.OwnerID, 512, true}, {r.AgentID, 256, false}, {r.WorkflowID, 256, false}, {r.WorkflowRunID, 128, false},
		{r.Title, 1024, true}, {r.RetryOf, 128, false},
	}
	for _, check := range checks {
		if !validText(check.value, check.limit, check.required) {
			return fmt.Errorf("%w: invalid admission metadata", ErrConflict)
		}
	}
	if r.DueAt < 0 || len(r.Payload) > MaxPayloadBytes || (len(r.Payload) > 0 && (!utf8.Valid(r.Payload) || !json.Valid(r.Payload))) {
		return fmt.Errorf("%w: invalid due time or opaque JSON payload", ErrConflict)
	}
	// Legacy administrator jobs may have neither AgentID nor WorkflowID. Only
	// the trusted host determines whether that source definition is authorized.
	return nil
}

func encodeRequest(r Request) ([]byte, []byte, string, error) {
	if err := validateRequest(r); err != nil {
		return nil, nil, "", err
	}
	payload := bytes.Clone(r.Payload)
	r.Payload = nil
	meta, err := json.Marshal(r)
	if err != nil {
		return nil, nil, "", fmt.Errorf("%w: admission metadata cannot be encoded", ErrConflict)
	}
	return meta, payload, digest(meta, payload), nil
}

func digest(meta, payload []byte) string {
	h := sha256.New()
	h.Write([]byte("taskqueue-request-v1\x00"))
	h.Write(meta)
	h.Write([]byte{0})
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}

func decodeRow(scanner rowScanner) (storedRun, error) {
	var r storedRun
	var meta, payload []byte
	err := scanner.Scan(&r.ID, &meta, &payload, &r.digest, &r.Status, &r.Revision, &r.Attempt, &r.CancelRequested,
		&r.CreatedAt, &r.StartedAt, &r.FinishedAt, &r.SessionID, &r.Summary, &r.Error, &r.EndReason, &r.finishedFrom, &r.readyAt)
	if err == sql.ErrNoRows {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if digest(meta, payload) != r.digest || json.Unmarshal(meta, &r.Request) != nil {
		return storedRun{}, fmt.Errorf("%w: stored admission failed its integrity check", ErrConflict)
	}
	r.Payload = bytes.Clone(payload)
	return r, nil
}

func get(q queryer, id string) (storedRun, error) {
	return decodeRow(q.QueryRow("SELECT "+columns+" FROM taskqueue_runs WHERE id=?", id))
}

func validID(id string) error {
	if !validText(id, 128, true) {
		return fmt.Errorf("%w: invalid run identity", ErrConflict)
	}
	return nil
}

func randomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func insert(tx *sql.Tx, req Request, attempt int) (storedRun, error) {
	meta, payload, sum, err := encodeRequest(req)
	if err != nil {
		return storedRun{}, err
	}
	id, err := randomID()
	if err != nil {
		return storedRun{}, err
	}
	_, err = tx.Exec(`INSERT INTO taskqueue_runs(id,task_id,trigger_id,owner_id,agent_id,due_at,request,payload,digest,status,revision,attempt,created_at)
 VALUES(?,?,?,?,?,?,?,?,?,'queued',1,?,?)`, id, req.TaskID, req.TriggerID, req.OwnerID, req.AgentID, req.DueAt, meta, nullableBytes(payload), sum, attempt, now())
	if err != nil {
		return storedRun{}, err
	}
	return get(tx, id)
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

// Enqueue durably admits one immutable (TaskID,TriggerID). A repeated request
// returns the existing run without resetting its status or changing its data.
func (s *Store) Enqueue(req Request) (Run, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Run{}, false, err
	}
	defer tx.Rollback()
	run, created, err := enqueueTx(tx, req)
	if err != nil {
		return Run{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, false, err
	}
	return run, created, nil
}

// enqueueTx keeps atomic workflow admission and commands on the same durable
// transaction as their execution segment. It does not expose the database.
func enqueueTx(tx *sql.Tx, req Request) (Run, bool, error) {
	_, _, sum, err := encodeRequest(req)
	if err != nil {
		return Run{}, false, err
	}
	old, err := decodeRow(tx.QueryRow("SELECT "+columns+" FROM taskqueue_runs WHERE task_id=? AND trigger_id=?", req.TaskID, req.TriggerID))
	if err == nil {
		if old.digest != sum {
			return Run{}, false, fmt.Errorf("%w: trigger identity was reused with different immutable content", ErrConflict)
		}
		return old.Run, false, nil
	}
	if err != ErrNotFound {
		return Run{}, false, err
	}
	run, err := insert(tx, req, 1)
	if err != nil {
		return Run{}, false, err
	}
	return run.Run, true, nil
}

func (s *Store) Get(id string) (Run, error) {
	if err := validID(id); err != nil {
		return Run{}, err
	}
	r, err := get(s.db, id)
	return r.Run, err
}

// List is ordered by due time, admission time and ID for deterministic oldest
// eligible admission first. An empty filter returns all rows to trusted code.
func (s *Store) List(filter Filter) ([]Run, error) {
	if filter.Limit < 0 || filter.Limit > 10000 || !validText(filter.OwnerID, 512, false) || !validText(filter.AgentID, 256, false) || !validText(filter.TaskID, 256, false) {
		return nil, fmt.Errorf("%w: invalid queue filter", ErrConflict)
	}
	where := []string{"1=1"}
	args := []any{}
	for _, field := range []struct{ column, value string }{{"owner_id", filter.OwnerID}, {"agent_id", filter.AgentID}, {"task_id", filter.TaskID}} {
		if field.value != "" {
			where = append(where, field.column+"=?")
			args = append(args, field.value)
		}
	}
	if filter.ActiveOnly {
		where = append(where, "status IN ('queued','running')")
	}
	query := "SELECT " + columns + " FROM taskqueue_runs WHERE " + strings.Join(where, " AND ") + " ORDER BY due_at,created_at,id"
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Run{}
	for rows.Next() {
		r, err := decodeRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, r.Run)
	}
	return result, rows.Err()
}

// Claim reserves execution before the host starts any worker or side effect.
// Cancel-requested running work still occupies both limits until it ends.
func (s *Store) Claim(id string, maxActive int) (Run, error) {
	if err := validID(id); err != nil {
		return Run{}, err
	}
	if maxActive < 1 || maxActive > 10000 {
		return Run{}, fmt.Errorf("%w: invalid active-run limit", ErrConflict)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	r, err := get(tx, id)
	if err != nil {
		return Run{}, err
	}
	if r.Status == Running {
		return Run{}, ErrBusy
	}
	if r.Status != Queued || r.CancelRequested || r.Revision == math.MaxInt64 {
		return Run{}, fmt.Errorf("%w: run is not a claimable queued admission", ErrConflict)
	}
	if r.readyAt > now() {
		return Run{}, ErrBusy
	}
	var active, taskActive int
	if err := tx.QueryRow("SELECT count(*) FROM taskqueue_runs WHERE status='running'").Scan(&active); err != nil {
		return Run{}, err
	}
	if err := tx.QueryRow("SELECT count(*) FROM taskqueue_runs WHERE status='running' AND task_id=?", r.TaskID).Scan(&taskActive); err != nil {
		return Run{}, err
	}
	if active >= maxActive || taskActive > 0 {
		return Run{}, ErrBusy
	}
	_, err = tx.Exec("UPDATE taskqueue_runs SET status='running',revision=revision+1,started_at=? WHERE id=? AND status='queued' AND revision=?", max(now(), r.CreatedAt), id, r.Revision)
	if err != nil {
		return Run{}, err
	}
	claimed, err := get(tx, id)
	if err != nil {
		return Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return claimed.Run, nil
}

// Release defers a claimed run only when the trusted host has established that
// no execution or side effect started. Unknown outcomes must use Recover or
// Finish, never Release. The original immutable admission remains unchanged.
func (s *Store) Release(id string, revision, dueAt int64) (Run, error) {
	if err := validID(id); err != nil {
		return Run{}, err
	}
	if revision <= 0 || dueAt < 0 {
		return Run{}, ErrConflict
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	r, err := get(tx, id)
	if err != nil {
		return Run{}, err
	}
	if r.Status != Running || r.Revision != revision || r.Revision == math.MaxInt64 || r.CancelRequested {
		return Run{}, ErrConflict
	}
	// A bound but unstarted conversation is no longer this execution's receipt.
	// The next claim must explicitly bind its own conversation again.
	_, err = tx.Exec("UPDATE taskqueue_runs SET status='queued',revision=revision+1,due_at=?,started_at=0,session_id='' WHERE id=? AND status='running' AND revision=?", dueAt, id, revision)
	if err != nil {
		return Run{}, err
	}
	released, err := get(tx, id)
	if err != nil {
		return Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return released.Run, nil
}

func validateOutcome(out Outcome) error {
	if out.Status != Completed && out.Status != Failed && out.Status != Cancelled && out.Status != Interrupted {
		return fmt.Errorf("%w: outcome must be terminal", ErrConflict)
	}
	if !validText(out.SessionID, 256, false) || !validText(out.Summary, 64<<10, false) || !validText(out.Error, 16<<10, false) || !validText(out.EndReason, 256, false) {
		return fmt.Errorf("%w: invalid terminal outcome metadata", ErrConflict)
	}
	return nil
}

func outcomeEqual(r Run, out Outcome) bool {
	return r.Status == out.Status && r.SessionID == out.SessionID && r.Summary == out.Summary && r.Error == out.Error && r.EndReason == out.EndReason
}

// AttachSession binds the claimed execution to its durable conversation before
// the host starts that conversation. It does not change the execution revision.
// A binding is immutable, and must survive cancellation and terminal receipts.
func (s *Store) AttachSession(id string, revision int64, sessionID string) (Run, error) {
	if err := validID(id); err != nil {
		return Run{}, err
	}
	if revision <= 0 || !validText(sessionID, 256, true) {
		return Run{}, ErrConflict
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	r, err := get(tx, id)
	if err != nil {
		return Run{}, err
	}
	if r.Status != Running || r.Revision != revision || (r.SessionID != "" && r.SessionID != sessionID) {
		return Run{}, ErrConflict
	}
	if r.SessionID == sessionID {
		return r.Run, nil
	}
	if _, err := tx.Exec("UPDATE taskqueue_runs SET session_id=? WHERE id=? AND status='running' AND revision=?", sessionID, id, revision); err != nil {
		return Run{}, err
	}
	r.SessionID = sessionID
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return r.Run, nil
}

// Finish records the worker's actual receipt. CancelRequested is intent only:
// a worker that really completed may still finish completed. Identical repeated
// receipts for this claim are idempotent; Recover never grants that exception.
func (s *Store) Finish(id string, revision int64, out Outcome) (Run, error) {
	if err := validID(id); err != nil {
		return Run{}, err
	}
	if err := validateOutcome(out); err != nil {
		return Run{}, err
	}
	if revision <= 0 {
		return Run{}, ErrConflict
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	r, err := get(tx, id)
	if err != nil {
		return Run{}, err
	}
	if r.SessionID != "" {
		if out.SessionID == "" {
			out.SessionID = r.SessionID
		} else if out.SessionID != r.SessionID {
			return Run{}, fmt.Errorf("%w: terminal receipt cannot replace a bound session", ErrConflict)
		}
	}
	if r.Status != Running {
		if r.finishedFrom == revision && outcomeEqual(r.Run, out) {
			return r.Run, nil
		}
		return Run{}, ErrConflict
	}
	if r.Revision != revision || r.Revision == math.MaxInt64 {
		return Run{}, ErrConflict
	}
	_, err = tx.Exec(`UPDATE taskqueue_runs SET status=?,revision=revision+1,finished_at=?,session_id=?,summary=?,error=?,end_reason=?,finished_from_revision=? WHERE id=? AND status='running' AND revision=?`,
		out.Status, max(now(), r.StartedAt), out.SessionID, out.Summary, out.Error, out.EndReason, revision, id, revision)
	if err != nil {
		return Run{}, err
	}
	finished, err := get(tx, id)
	if err != nil {
		return Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return finished.Run, nil
}

// Recover requires the host's exclusive controller lease. Unknown active work
// becomes interrupted, never queued. Neither this method nor Open retries it.
func (s *Store) Recover() error {
	_, err := s.db.Exec(`UPDATE taskqueue_runs SET status='interrupted',revision=revision+1,finished_at=max(?,started_at),
 error='controller restarted before a terminal outcome was recorded',end_reason='controller_restart',finished_from_revision=0 WHERE status='running' AND revision<?`, now(), int64(math.MaxInt64))
	return err
}

// Cancel never claims an active worker has stopped. Its execution revision is
// unchanged so that the real worker can still record its actual terminal state.
func (s *Store) Cancel(id string, revision int64) (Run, error) {
	if err := validID(id); err != nil {
		return Run{}, err
	}
	if revision <= 0 {
		return Run{}, ErrConflict
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	r, err := get(tx, id)
	if err != nil {
		return Run{}, err
	}
	if r.Revision != revision {
		return Run{}, ErrConflict
	}
	switch r.Status {
	case Running:
		_, err = tx.Exec("UPDATE taskqueue_runs SET cancel_requested=1 WHERE id=? AND revision=? AND status='running'", id, revision)
	case Queued:
		if r.Revision == math.MaxInt64 {
			return Run{}, ErrConflict
		}
		_, err = tx.Exec("UPDATE taskqueue_runs SET status='cancelled',cancel_requested=1,revision=revision+1,finished_at=?,end_reason='cancelled_before_start' WHERE id=? AND revision=? AND status='queued'", max(now(), r.CreatedAt), id, revision)
	case Cancelled:
		return r.Run, nil
	default:
		return Run{}, fmt.Errorf("%w: terminal work cannot be stopped retroactively", ErrConflict)
	}
	if err != nil {
		return Run{}, err
	}
	updated, err := get(tx, id)
	if err != nil {
		return Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return updated.Run, nil
}

// Retry is explicit and preserves the old outcome. Its child trigger is fixed
// by the old run and revision, so repeated requests cannot mint duplicate work.
func (s *Store) Retry(id string, revision int64) (Run, error) {
	if err := validID(id); err != nil {
		return Run{}, err
	}
	if revision <= 0 {
		return Run{}, ErrConflict
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	r, err := get(tx, id)
	if err != nil {
		return Run{}, err
	}
	if r.Revision != revision || (r.Status != Failed && r.Status != Interrupted) || r.Attempt >= math.MaxInt32 {
		return Run{}, ErrConflict
	}
	req := r.Request
	req.Trigger, req.TriggerID, req.RetryOf = "retry", fmt.Sprintf("retry:%s:%d", r.ID, revision), r.ID
	old, err := decodeRow(tx.QueryRow("SELECT "+columns+" FROM taskqueue_runs WHERE task_id=? AND trigger_id=?", req.TaskID, req.TriggerID))
	if err == nil {
		req.DueAt = old.DueAt
		_, _, expected, e := encodeRequest(req)
		if e != nil || old.digest != expected || old.Attempt != r.Attempt+1 {
			return Run{}, fmt.Errorf("%w: retry trigger identity has conflicting content", ErrConflict)
		}
		return old.Run, nil
	}
	if err != ErrNotFound {
		return Run{}, err
	}
	req.DueAt = now()
	child, err := insert(tx, req, r.Attempt+1)
	if err != nil {
		return Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return child.Run, nil
}
