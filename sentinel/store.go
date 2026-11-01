package sentinel

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db    *sql.DB
	clock func() int64 // optional deterministic test clock; never script supplied
}

func (s *Store) currentTime() int64 {
	if s.clock != nil {
		return s.clock()
	}
	return now()
}

type queryer interface {
	QueryRow(string, ...any) *sql.Row
	Query(string, ...any) (*sql.Rows, error)
}

func now() int64 { return time.Now().UnixMilli() }
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// Open requires a real disk-backed WAL database. Admission must stop on
// persistence failure; there is deliberately no in-memory fallback.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" || path == ":memory:" {
		return nil, errors.New("sentinel: a persistent database path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
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
	fail := func(e error) (*Store, error) { db.Close(); return nil, e }
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
		return fail(errors.New("sentinel: required durability settings unavailable"))
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS sentinel_schema(version INTEGER NOT NULL);INSERT INTO sentinel_schema SELECT 1 WHERE NOT EXISTS(SELECT 1 FROM sentinel_schema);
 CREATE TABLE IF NOT EXISTS sentinel_watches(id TEXT PRIMARY KEY,owner_id TEXT NOT NULL,agent_id TEXT NOT NULL,spec BLOB NOT NULL,state BLOB,cursor BLOB,status TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',next_at INTEGER NOT NULL DEFAULT 0,failures INTEGER NOT NULL DEFAULT 0,removed INTEGER NOT NULL DEFAULT 0);
 CREATE INDEX IF NOT EXISTS sentinel_owner ON sentinel_watches(owner_id,agent_id,removed);
 CREATE TABLE IF NOT EXISTS sentinel_frames(watch_id TEXT NOT NULL,frame_id TEXT NOT NULL,digest TEXT NOT NULL,created_at INTEGER NOT NULL,PRIMARY KEY(watch_id,frame_id));
 CREATE TABLE IF NOT EXISTS sentinel_events(id TEXT PRIMARY KEY,watch_id TEXT NOT NULL,event_key TEXT NOT NULL,summary TEXT NOT NULL,data BLOB,batch_id TEXT NOT NULL DEFAULT '',settled INTEGER NOT NULL DEFAULT 0,created_at INTEGER NOT NULL,UNIQUE(watch_id,event_key));
 CREATE INDEX IF NOT EXISTS sentinel_pending ON sentinel_events(watch_id,settled,batch_id,created_at);
 CREATE TABLE IF NOT EXISTS sentinel_batches(id TEXT PRIMARY KEY,watch_id TEXT NOT NULL,session_id TEXT NOT NULL DEFAULT '',run_id TEXT NOT NULL DEFAULT '',status TEXT NOT NULL,decision TEXT NOT NULL DEFAULT '',summary TEXT NOT NULL DEFAULT '',error TEXT NOT NULL DEFAULT '',created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL,not_before INTEGER NOT NULL DEFAULT 0,attempt INTEGER NOT NULL DEFAULT 1);
 CREATE INDEX IF NOT EXISTS sentinel_batch_watch ON sentinel_batches(watch_id,status,created_at);`)
	if err != nil {
		return fail(err)
	}
	var v int
	if err = db.QueryRow("SELECT version FROM sentinel_schema").Scan(&v); err != nil {
		return fail(err)
	}
	if v != 1 {
		return fail(fmt.Errorf("sentinel: unsupported schema version %d", v))
	}
	return &Store{db: db}, nil
}
func (s *Store) Close() error { return s.db.Close() }

func validateSpec(p Spec) error {
	if len(p.ID) > 128 || len(p.OwnerID) > 512 || len(p.AgentID) > 128 || len(p.Title) > 512 || len(p.Task) > 64<<10 || len(p.Owner) > 16<<10 {
		return errors.New("sentinel: specification field is too large")
	}
	if p.AgentID == "" || strings.TrimSpace(p.Title) == "" || strings.TrimSpace(p.Task) == "" {
		return errors.New("sentinel: Agent, title and task are required")
	}
	if !filepath.IsAbs(p.Workspace) || !filepath.IsAbs(p.ScriptDir) {
		return errors.New("sentinel: host-bound workspace and script directory must be absolute")
	}
	if p.Mode != "poll" && p.Mode != "stream" {
		return errors.New("sentinel: mode must be poll or stream")
	}
	if len(p.Command) == 0 || len(p.Command) > 64 {
		return errors.New("sentinel: command must be a nonempty argv")
	}
	for _, arg := range p.Command {
		if strings.ContainsRune(arg, 0) || len(arg) > 8192 {
			return errors.New("sentinel: invalid command argument")
		}
	}
	if strings.TrimSpace(p.Command[0]) == "" {
		return errors.New("sentinel: empty executable")
	}
	if p.TimeoutSeconds <= 0 || p.TimeoutSeconds > 86400 || p.DebounceMS < 0 || p.DebounceMS > 60000 || p.MaxTurns <= 0 || p.MaxTurns > 300 {
		return errors.New("sentinel: invalid execution limits")
	}
	if p.Mode == "poll" && (p.IntervalSeconds < 1 || p.IntervalSeconds > 30*86400) {
		return errors.New("sentinel: invalid polling interval")
	}
	if len(p.Owner) > 0 && !json.Valid(p.Owner) {
		return errors.New("sentinel: invalid owner record")
	}
	if p.ExpiresAt < 0 {
		return errors.New("sentinel: invalid host deadline")
	}
	return nil
}

func (s *Store) Create(p Spec) (Watch, error) {
	if p.ID == "" {
		p.ID = newID()
	}
	if p.Revision <= 0 {
		p.Revision = 1
	}
	if p.Generation <= 0 {
		p.Generation = 1
	}
	p.CreatedAt = s.currentTime()
	p.UpdatedAt = p.CreatedAt
	if err := validateSpec(p); err != nil {
		return Watch{}, err
	}
	if p.Expired(p.CreatedAt) {
		return Watch{}, ErrExpired
	}
	b, err := json.Marshal(p)
	if err != nil {
		return Watch{}, err
	}
	status := "paused"
	if p.Enabled {
		status = "starting"
	}
	_, err = s.db.Exec("INSERT INTO sentinel_watches(id,owner_id,agent_id,spec,status) VALUES(?,?,?,?,?)", p.ID, p.OwnerID, p.AgentID, b, status)
	if err != nil {
		return Watch{}, err
	}
	return s.Get(p.ID)
}

func get(q queryer, id string) (Watch, error) {
	var w Watch
	var spec, state, cursor []byte
	err := q.QueryRow(`SELECT spec,state,cursor,status,error,next_at,failures,(SELECT count(*) FROM sentinel_events WHERE watch_id=sentinel_watches.id AND settled=0) FROM sentinel_watches WHERE id=? AND removed=0`, id).Scan(&spec, &state, &cursor, &w.Status, &w.Error, &w.NextAt, &w.Failures, &w.Pending)
	if errors.Is(err, sql.ErrNoRows) {
		return w, ErrNotFound
	}
	if err != nil {
		return w, err
	}
	if err = json.Unmarshal(spec, &w.Spec); err != nil {
		return w, err
	}
	w.State = state
	w.Cursor = cursor
	return w, nil
}
func (s *Store) Get(id string) (Watch, error) { return get(s.db, id) }
func (s *Store) List(ownerID, agentID string) ([]Watch, error) {
	query := "SELECT id FROM sentinel_watches WHERE removed=0 AND owner_id=?"
	args := []any{ownerID}
	if agentID != "" {
		query += " AND agent_id=?"
		args = append(args, agentID)
	}
	query += " ORDER BY rowid"
	return s.listWatches(query, args)
}

// ListAll is only for the trusted supervisor. Model-facing lists use List
// with the exact captured owner key.
func (s *Store) ListAll() ([]Watch, error) {
	return s.listWatches("SELECT id FROM sentinel_watches WHERE removed=0 ORDER BY rowid", nil)
}
func (s *Store) listWatches(query string, args []any) ([]Watch, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := make([]Watch, 0, len(ids))
	for _, id := range ids {
		w, err := s.Get(id)
		if err == ErrNotFound {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, nil
}
func writeSpec(tx *sql.Tx, p Spec) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = tx.Exec("UPDATE sentinel_watches SET spec=? WHERE id=? AND removed=0", b, p.ID)
	return err
}
func (s *Store) SetEnabled(id string, enabled bool) (Watch, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Watch{}, err
	}
	defer tx.Rollback()
	w, err := get(tx, id)
	if err != nil {
		return Watch{}, err
	}
	if w.Spec.Expired(s.currentTime()) {
		if _, err = expireWatch(tx, &w, s.currentTime()); err != nil {
			return Watch{}, err
		}
		if err = tx.Commit(); err != nil {
			return Watch{}, err
		}
		if enabled {
			return w, ErrExpired
		}
		return w, nil
	}
	w.Spec.Enabled = enabled
	w.Spec.Generation++
	w.Spec.Revision++
	w.Spec.UpdatedAt = now()
	if err = writeSpec(tx, w.Spec); err != nil {
		return Watch{}, err
	}
	status := "paused"
	if enabled {
		status = "starting"
	}
	_, err = tx.Exec("UPDATE sentinel_watches SET status=?,error='',failures=0,next_at=0 WHERE id=?", status, id)
	if err != nil {
		return Watch{}, err
	}
	if err = tx.Commit(); err != nil {
		return Watch{}, err
	}
	return s.Get(id)
}

// expireWatch is one atomic control transition. It retires old collectors and
// marks unfinished admissions for review without settling or deleting events.
func expireWatch(tx *sql.Tx, w *Watch, at int64) (bool, error) {
	if !w.Spec.Expired(at) || (!w.Spec.Enabled && w.Status == "expired") {
		return false, nil
	}
	w.Spec.Enabled = false
	w.Spec.Generation++
	w.Spec.Revision++
	w.Spec.UpdatedAt = at
	if err := writeSpec(tx, w.Spec); err != nil {
		return false, err
	}
	if _, err := tx.Exec("UPDATE sentinel_watches SET status='expired',error='',next_at=0 WHERE id=?", w.Spec.ID); err != nil {
		return false, err
	}
	if _, err := tx.Exec("UPDATE sentinel_batches SET status='interrupted',error=?,updated_at=? WHERE watch_id=? AND status IN ('admitting','running','queued','retry_pending')", ErrExpired.Error(), at, w.Spec.ID); err != nil {
		return false, err
	}
	w.Status, w.Error, w.NextAt = "expired", "", 0
	return true, nil
}

// Expire enforces a saved deadline even for paused watches or after restart.
// A stale generation cannot retire a newly changed registration.
func (s *Store) Expire(id string, generation int64) (Watch, bool, error) {
	return s.ExpireAt(id, generation, s.currentTime())
}

// ExpireAt takes trusted host time, also allowing deterministic supervisor
// tests. It is not part of the script protocol or a model-facing argument.
func (s *Store) ExpireAt(id string, generation int64, at int64) (Watch, bool, error) {
	if at < 0 {
		return Watch{}, false, errors.New("sentinel: invalid host time")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Watch{}, false, err
	}
	defer tx.Rollback()
	w, err := get(tx, id)
	if err != nil {
		return w, false, err
	}
	if w.Spec.Generation != generation {
		return w, false, ErrStaleGeneration
	}
	changed, err := expireWatch(tx, &w, at)
	if err != nil {
		return w, false, err
	}
	if err = tx.Commit(); err != nil {
		return w, false, err
	}
	return w, changed, nil
}

// A transaction can span the cutoff while validating/inserting a batch. Never
// commit its observations/admission after that boundary: roll it back first,
// then persist only the deadline control transition in a separate transaction.
func (s *Store) commitBeforeDeadline(tx *sql.Tx, w Watch) error {
	at := s.currentTime()
	if w.Spec.Expired(at) {
		if err := tx.Rollback(); err != nil {
			return err
		}
		if _, _, err := s.ExpireAt(w.Spec.ID, w.Spec.Generation, at); err != nil {
			return err
		}
		return ErrExpired
	}
	return tx.Commit()
}
func (s *Store) UpdateSession(id, sessionID string) error {
	if len(sessionID) > 256 {
		return errors.New("sentinel: session ID too large")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	w, err := get(tx, id)
	if err != nil {
		return err
	}
	w.Spec.SessionID = sessionID
	w.Spec.UpdatedAt = now()
	if err = writeSpec(tx, w.Spec); err != nil {
		return err
	}
	return tx.Commit()
}

func ValidateFrame(frame Frame) error {
	if frame.V != 1 || strings.TrimSpace(frame.ID) == "" || len(frame.ID) > 256 || len(frame.Events) > 64 {
		return errors.New("sentinel: invalid frame envelope")
	}
	if len(frame.State) > MaxStateBytes || len(frame.Cursor) > MaxStateBytes {
		return errors.New("sentinel: state or cursor exceeds limit")
	}
	for _, e := range frame.Events {
		if strings.TrimSpace(e.Key) == "" || len(e.Key) > 256 || len(e.Summary) > 2048 || len(e.Data) > 32<<10 {
			return errors.New("sentinel: invalid event")
		}
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	if len(raw) > MaxFrameBytes {
		return errors.New("sentinel: frame exceeds limit")
	}
	return nil
}

// Accept is the only collector cursor commit path. An ACK may be sent only
// after it returns nil. Queue overflow rolls back every proposed state change.
func (s *Store) Accept(id string, generation int64, frame Frame) error {
	if err := ValidateFrame(frame); err != nil {
		return err
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	h := sha256.Sum256(raw)
	digest := hex.EncodeToString(h[:])
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	w, err := get(tx, id)
	if err != nil {
		return err
	}
	if w.Spec.Generation != generation {
		return ErrStaleGeneration
	}
	if w.Spec.Expired(s.currentTime()) {
		if _, err = expireWatch(tx, &w, s.currentTime()); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return ErrExpired
	}
	if !w.Spec.Enabled {
		return ErrDisabled
	}
	var previous string
	err = tx.QueryRow("SELECT digest FROM sentinel_frames WHERE watch_id=? AND frame_id=?", id, frame.ID).Scan(&previous)
	if err == nil {
		if previous != digest {
			return fmt.Errorf("%w: frame ID %s was reused with different events, state or cursor; use a new frame ID when frame contents change, keeping event keys stable for unchanged source events", ErrConflict, conflictIdentity(frame.ID))
		}
		return s.commitBeforeDeadline(tx, w)
	}
	if err != sql.ErrNoRows {
		return err
	}
	for _, event := range frame.Events {
		var summary string
		var data []byte
		err = tx.QueryRow("SELECT summary,data FROM sentinel_events WHERE watch_id=? AND event_key=?", id, event.Key).Scan(&summary, &data)
		if err == nil {
			if summary != event.Summary || !jsonEqual(data, event.Data) {
				return fmt.Errorf("%w: event key %s was reused with different summary or data; keep an event key's content unchanged and use a new key for a new source event/version", ErrConflict, conflictIdentity(event.Key))
			}
			continue
		}
		if err != sql.ErrNoRows {
			return err
		}
		if w.Pending >= MaxPendingEvents {
			return ErrQueueFull
		}
		_, err = tx.Exec("INSERT INTO sentinel_events(id,watch_id,event_key,summary,data,created_at) VALUES(?,?,?,?,?,?)", newID(), id, event.Key, event.Summary, nullable(event.Data), now())
		if err != nil {
			return err
		}
		w.Pending++
	}
	state, cursor := w.State, w.Cursor
	if len(frame.State) > 0 {
		state = frame.State
	}
	if len(frame.Cursor) > 0 {
		cursor = frame.Cursor
	}
	_, err = tx.Exec("INSERT INTO sentinel_frames(watch_id,frame_id,digest,created_at) VALUES(?,?,?,?)", id, frame.ID, digest, now())
	if err != nil {
		return err
	}
	_, err = tx.Exec("UPDATE sentinel_watches SET state=?,cursor=? WHERE id=?", nullable(state), nullable(cursor), id)
	if err != nil {
		return err
	}
	return s.commitBeforeDeadline(tx, w)
}

// Collector identities are untrusted and may contain credentials. Use a stable
// short fingerprint for correlation; never include raw identities or payloads.
func conflictIdentity(id string) string {
	digest := sha256.Sum256([]byte(id))
	return "sha256:" + hex.EncodeToString(digest[:8])
}

func nullable(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return []byte(b)
}
func jsonEqual(a, b []byte) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == 0 && len(b) == 0
	}
	var x, y bytes.Buffer
	if json.Compact(&x, a) != nil || json.Compact(&y, b) != nil {
		return false
	}
	return bytes.Equal(x.Bytes(), y.Bytes())
}

func (s *Store) SetRuntime(id, status, message string, nextAt int64, failures int) error {
	if len(status) > 64 || nextAt < 0 || failures < 0 {
		return errors.New("sentinel: invalid runtime status")
	}
	message = boundedError(message)
	result, err := s.db.Exec("UPDATE sentinel_watches SET status=?,error=?,next_at=?,failures=? WHERE id=? AND removed=0", status, message, nextAt, failures, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

func loadBatch(q queryer, id string) (Batch, error) {
	var b Batch
	err := q.QueryRow("SELECT id,watch_id,session_id,run_id,status,decision,summary,error,created_at,updated_at,not_before,attempt FROM sentinel_batches WHERE id=?", id).Scan(&b.ID, &b.WatchID, &b.SessionID, &b.RunID, &b.Status, &b.Decision, &b.Summary, &b.Error, &b.CreatedAt, &b.UpdatedAt, &b.NotBefore, &b.Attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	if err != nil {
		return b, err
	}
	rows, err := q.Query("SELECT id,watch_id,event_key,summary,data,batch_id FROM sentinel_events WHERE batch_id=? ORDER BY rowid", id)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	b.Events = []Event{}
	for rows.Next() {
		var e Event
		var data []byte
		if err = rows.Scan(&e.ID, &e.WatchID, &e.Key, &e.Summary, &data, &e.BatchID); err != nil {
			return b, err
		}
		e.Data = data
		b.Events = append(b.Events, e)
	}
	return b, rows.Err()
}
func (s *Store) GetBatch(id string) (Batch, error) { return loadBatch(s.db, id) }

// ClaimBatch never replays an interrupted/running admission. The host must
// first reconcile its durable execution receipt. A due deferred batch retains
// its identity and frozen event membership.
func (s *Store) ClaimBatch(id string, maxEvents int) (*Batch, error) {
	if maxEvents <= 0 || maxEvents > MaxBatchEvents {
		maxEvents = MaxBatchEvents
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	w, err := get(tx, id)
	if err != nil {
		return nil, err
	}
	if w.Spec.Expired(s.currentTime()) {
		if _, err = expireWatch(tx, &w, s.currentTime()); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return nil, ErrExpired
	}
	if !w.Spec.Enabled {
		return nil, nil
	}
	var existing, status string
	var notBefore int64
	err = tx.QueryRow("SELECT id,status,not_before FROM sentinel_batches WHERE watch_id=? AND status!='completed' ORDER BY rowid LIMIT 1", id).Scan(&existing, &status, &notBefore)
	if err == nil {
		if (status != "deferred" && status != "retry_pending") || notBefore > now() {
			return nil, nil
		}
		if status == "deferred" {
			_, err = tx.Exec("UPDATE sentinel_batches SET attempt=attempt+1,run_id='' WHERE id=?", existing)
			if err != nil {
				return nil, err
			}
		}
		_, err = tx.Exec("UPDATE sentinel_batches SET status='admitting',decision='',updated_at=? WHERE id=?", now(), existing)
		if err != nil {
			return nil, err
		}
		b, err := loadBatch(tx, existing)
		if err != nil {
			return nil, err
		}
		if err = s.commitBeforeDeadline(tx, w); err != nil {
			return nil, err
		}
		return &b, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	rows, err := tx.Query("SELECT id, length(CAST(summary AS BLOB))*6+length(CAST(event_key AS BLOB))*6+coalesce(length(data),0)+512 FROM sentinel_events WHERE watch_id=? AND settled=0 AND batch_id='' ORDER BY rowid LIMIT ?", id, maxEvents)
	if err != nil {
		return nil, err
	}
	var eventIDs []string
	batchBytes := 0
	for rows.Next() {
		var eventID string
		var size int
		if err = rows.Scan(&eventID, &size); err != nil {
			rows.Close()
			return nil, err
		}
		if len(eventIDs) > 0 && batchBytes+size > MaxBatchBytes {
			break
		}
		batchBytes += size
		eventIDs = append(eventIDs, eventID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(eventIDs) == 0 {
		return nil, nil
	}
	b := Batch{Attempt: 1, ID: newID(), WatchID: id, Status: "admitting", SessionID: w.Spec.SessionID, CreatedAt: now(), UpdatedAt: now()}
	_, err = tx.Exec("INSERT INTO sentinel_batches(id,watch_id,session_id,status,created_at,updated_at) VALUES(?,?,?,?,?,?)", b.ID, id, b.SessionID, b.Status, b.CreatedAt, b.UpdatedAt)
	if err != nil {
		return nil, err
	}
	for _, eventID := range eventIDs {
		_, err = tx.Exec("UPDATE sentinel_events SET batch_id=? WHERE id=?", b.ID, eventID)
		if err != nil {
			return nil, err
		}
	}
	b, err = loadBatch(tx, b.ID)
	if err != nil {
		return nil, err
	}
	if err = s.commitBeforeDeadline(tx, w); err != nil {
		return nil, err
	}
	return &b, nil
}

func (s *Store) ListUnsettled() ([]Batch, error) {
	return s.listBatches("SELECT id FROM sentinel_batches WHERE status!='completed' ORDER BY rowid", nil)
}

// QueueBatch records shared-dispatcher admission before a model is started.
// A repeated publication can repair the cross-store admission window.
func (s *Store) QueueBatch(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	b, err := loadBatch(tx, id)
	if err != nil {
		return err
	}
	w, err := get(tx, b.WatchID)
	if err != nil {
		return err
	}
	if w.Spec.Expired(s.currentTime()) {
		if _, err = expireWatch(tx, &w, s.currentTime()); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return ErrExpired
	}
	result, err := tx.Exec("UPDATE sentinel_batches SET status='queued',updated_at=? WHERE id=? AND status IN ('admitting','retry_pending','queued') AND run_id=''", now(), id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	return s.commitBeforeDeadline(tx, w)
}
func (s *Store) ListBatches(watchID string, limit int) ([]Batch, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	return s.listBatches("SELECT id FROM sentinel_batches WHERE watch_id=? ORDER BY rowid DESC LIMIT ?", []any{watchID, limit})
}
func (s *Store) listBatches(query string, args []any) ([]Batch, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := make([]Batch, 0, len(ids))
	for _, id := range ids {
		b, err := s.GetBatch(id)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func (s *Store) SetBatchRun(id, sessionID, runID, status string) error {
	if status != "admitting" && status != "running" && status != "interrupted" && status != "retry_pending" {
		return errors.New("sentinel: invalid execution status")
	}
	if len(sessionID) > 256 || len(runID) > 256 {
		return errors.New("sentinel: invalid execution identity")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	b, err := loadBatch(tx, id)
	if err != nil {
		return err
	}
	w, e := get(tx, b.WatchID)
	if e != nil {
		return e
	}
	if status != "interrupted" {
		if w.Spec.Expired(s.currentTime()) {
			if _, err = expireWatch(tx, &w, s.currentTime()); err != nil {
				return err
			}
			if err = tx.Commit(); err != nil {
				return err
			}
			return ErrExpired
		}
	}
	if b.Status == "completed" {
		return ErrConflict
	}
	if status == "retry_pending" && (b.RunID != "" || runID != "") {
		return ErrConflict
	}
	if b.SessionID != "" && b.SessionID != sessionID {
		return ErrConflict
	}
	if b.RunID != "" && runID != "" && b.RunID != runID {
		return ErrConflict
	}
	if runID == "" {
		runID = b.RunID
	}
	_, err = tx.Exec("UPDATE sentinel_batches SET session_id=?,run_id=?,status=?,updated_at=? WHERE id=?", sessionID, runID, status, now(), id)
	if err != nil {
		return err
	}
	if status == "interrupted" {
		return tx.Commit()
	}
	return s.commitBeforeDeadline(tx, w)
}
func (s *Store) InterruptBatch(id, reason string) error {
	reason = boundedError(reason)
	result, err := s.db.Exec("UPDATE sentinel_batches SET status='interrupted',error=?,updated_at=? WHERE id=? AND status!='completed'", reason, now(), id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrConflict
	}
	return err
}

// SettleBatch is idempotent for an identical decision; a conflicting repeated
// decision never overwrites the first durable outcome.
func (s *Store) SettleBatch(id, decision, summary string, deferUntil int64) error {
	if decision != "ignore" && decision != "report" && decision != "defer" {
		return errors.New("sentinel: decision must be ignore, report or defer")
	}
	if len(summary) > 32<<10 {
		return errors.New("sentinel: decision summary too large")
	}
	if decision == "defer" && (deferUntil <= 0 || deferUntil > now()+30*86400*1000) {
		return errors.New("sentinel: invalid defer deadline")
	}
	if decision != "defer" && deferUntil != 0 {
		return errors.New("sentinel: only defer accepts a deadline")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	b, err := loadBatch(tx, id)
	if err != nil {
		return err
	}
	if b.Status == "completed" || b.Status == "deferred" {
		if b.Decision == decision && b.Summary == summary && b.NotBefore == deferUntil {
			return nil
		}
		return ErrConflict
	}
	status := "completed"
	if decision == "defer" {
		status = "deferred"
	}
	_, err = tx.Exec("UPDATE sentinel_batches SET status=?,decision=?,summary=?,error='',not_before=?,updated_at=? WHERE id=?", status, decision, summary, deferUntil, now(), id)
	if err != nil {
		return err
	}
	if status == "completed" {
		_, err = tx.Exec("UPDATE sentinel_events SET settled=1 WHERE batch_id=?", id)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Recover is called by the single supervisor after acquiring ownership, never
// merely by opening another database connection. Running work becomes visible
// as interrupted, and collector generations are fenced before any restart.
func (s *Store) Recover() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query("SELECT id FROM sentinel_watches WHERE removed=0")
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		w, err := get(tx, id)
		if err != nil {
			return err
		}
		w.Spec.Generation++
		w.Spec.UpdatedAt = now()
		if err = writeSpec(tx, w.Spec); err != nil {
			return err
		}
		status := "paused"
		if w.Spec.Enabled {
			status = "starting"
		} else if w.Status == "expired" {
			// Expiration is already a durable terminal control transition.
			// Demoting it to paused would make the next deadline reconciliation
			// retire the same watch again and increment its business revision.
			status = "expired"
		}
		_, err = tx.Exec("UPDATE sentinel_watches SET status=? WHERE id=?", status, id)
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec("UPDATE sentinel_batches SET status='interrupted',error='supervisor restarted; reconcile execution receipt before retry',updated_at=? WHERE status IN ('admitting','running')", now())
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Remove preserves receipts and frozen batches for audit. It does not claim
// that a live execution stopped; the supervisor must cancel and drain it first.
func (s *Store) Remove(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	w, err := get(tx, id)
	if err != nil {
		return err
	}
	var count int
	if err = tx.QueryRow("SELECT count(*) FROM sentinel_batches WHERE watch_id=? AND status IN ('admitting','running','queued')", id).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return ErrBatchActive
	}
	w.Spec.Enabled = false
	w.Spec.Generation++
	w.Spec.Revision++
	w.Spec.UpdatedAt = now()
	if err = writeSpec(tx, w.Spec); err != nil {
		return err
	}
	_, err = tx.Exec("UPDATE sentinel_watches SET removed=1,status='removed' WHERE id=?", id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// RetryBatch starts a new, explicit attempt; it never silently replays work
// whose prior outcome was unknown. Stable event and batch identities remain.
func (s *Store) RetryBatch(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	b, err := loadBatch(tx, id)
	if err != nil {
		return err
	}
	if b.Status != "interrupted" {
		return ErrConflict
	}
	w, err := get(tx, b.WatchID)
	if err != nil {
		return err
	}
	if w.Spec.Expired(s.currentTime()) {
		if _, err = expireWatch(tx, &w, s.currentTime()); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return ErrExpired
	}
	_, err = tx.Exec("UPDATE sentinel_batches SET status='retry_pending',attempt=attempt+1,run_id='',decision='',summary='',error='',not_before=0,updated_at=? WHERE id=?", now(), id)
	if err != nil {
		return err
	}
	return s.commitBeforeDeadline(tx, w)
}

// SetRuntimeGeneration prevents a retired collector's cleanup/error callback
// from replacing the state of a newly resumed collector.
func (s *Store) SetRuntimeGeneration(id string, generation int64, status, message string, nextAt int64, failures int) error {
	if len(status) > 64 || nextAt < 0 || failures < 0 {
		return errors.New("sentinel: invalid runtime status")
	}
	message = boundedError(message)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	w, err := get(tx, id)
	if err != nil {
		return err
	}
	if w.Spec.Generation != generation {
		return ErrStaleGeneration
	}
	if w.Spec.Expired(s.currentTime()) {
		if _, err = expireWatch(tx, &w, s.currentTime()); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return ErrExpired
	}
	_, err = tx.Exec("UPDATE sentinel_watches SET status=?,error=?,next_at=?,failures=? WHERE id=? AND removed=0", status, message, nextAt, failures, id)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func boundedError(message string) string {
	if len(message) <= 4096 {
		return message
	}
	return strings.ToValidUTF8(message[:4096], "")
}
