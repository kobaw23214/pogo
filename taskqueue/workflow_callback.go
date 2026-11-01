package taskqueue

// The callback outbox lives beside workflow admissions. A subscription and its
// first event are inserted in the admission transaction. State reconciliation
// can safely repeat inserts after an interrupted file/outbox transition.
import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type WorkflowCallbackTarget struct {
	PublicationID string `json:"publication_id"`
	URL           string `json:"url"`
	SigningSecret string `json:"signing_secret"`
}

type WorkflowCallbackSubscription struct {
	RunID         string
	PublicationID string
	Target        WorkflowCallbackTarget
	LastStatus    string
	LastRevision  int64
}

type WorkflowCallbackEvent struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	CreatedAt       string `json:"created_at"`
	PublicationID   string `json:"publication_id"`
	WorkflowID      string `json:"workflow_id"`
	VersionID       string `json:"version_id,omitempty"`
	BusinessID      string `json:"business_id,omitempty"`
	ExternalSubject string `json:"external_subject,omitempty"`
	RunID           string `json:"run_id"`
	RetryOf         string `json:"retry_of,omitempty"`
	Status          string `json:"status"`
	StateRevision   int64  `json:"state_revision"`
	Sequence        int64  `json:"sequence"`
}

type WorkflowCallbackDelivery struct {
	ID            string          `json:"id"`
	EventType     string          `json:"event_type"`
	RunID         string          `json:"run_id"`
	PublicationID string          `json:"publication_id"`
	BusinessID    string          `json:"business_id,omitempty"`
	StateRevision int64           `json:"state_revision"`
	Sequence      int64           `json:"sequence"`
	Status        string          `json:"status"`
	Attempts      int             `json:"attempts"`
	Failures      int             `json:"-"`
	NextAttemptAt int64           `json:"next_attempt_at"`
	LastStatus    int             `json:"last_status,omitempty"`
	LastError     string          `json:"last_error,omitempty"`
	CreatedAt     int64           `json:"created_at"`
	DeliveredAt   int64           `json:"delivered_at,omitempty"`
	TargetURL     string          `json:"target_url"`
	BlockedReason string          `json:"blocked_reason,omitempty"`
	Payload       json.RawMessage `json:"-"`
	SigningSecret string          `json:"-"`
	LeaseUntil    int64           `json:"-"`
	ClaimToken    string          `json:"-"`
}

func initWorkflowCallbackSchema(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS workflow_callback_subscriptions(
 run_id TEXT PRIMARY KEY REFERENCES workflow_run_catalog(id), publication_id TEXT NOT NULL,
 target BLOB NOT NULL, last_status TEXT NOT NULL DEFAULT '', last_revision INTEGER NOT NULL DEFAULT -1);
CREATE TABLE IF NOT EXISTS workflow_callback_outbox(
 id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES workflow_callback_subscriptions(run_id),
 publication_id TEXT NOT NULL, event_type TEXT NOT NULL, state_revision INTEGER NOT NULL, sequence INTEGER NOT NULL,
 business_id TEXT NOT NULL, payload BLOB NOT NULL, target_url TEXT NOT NULL, signing_secret TEXT NOT NULL,
 status TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, failures INTEGER NOT NULL DEFAULT 0,
 next_attempt_at INTEGER NOT NULL, last_status INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL, delivered_at INTEGER NOT NULL DEFAULT 0, blocked_reason TEXT NOT NULL DEFAULT '',
 lease_until INTEGER NOT NULL DEFAULT 0, claim_token TEXT NOT NULL DEFAULT '', UNIQUE(run_id,event_type,state_revision));
CREATE INDEX IF NOT EXISTS workflow_callback_dispatch ON workflow_callback_outbox(status,next_attempt_at,created_at,id);
CREATE INDEX IF NOT EXISTS workflow_callback_publication ON workflow_callback_outbox(publication_id,created_at DESC,id);`)
	return err
}

func validateWorkflowCallbackTarget(t WorkflowCallbackTarget) error {
	if !validText(t.PublicationID, 128, true) || !validText(t.URL, 4096, true) || !validText(t.SigningSecret, 256, true) {
		return fmt.Errorf("%w: invalid callback target", ErrConflict)
	}
	return nil
}
func callbackEventID(runID, status string, revision int64) string {
	b := sha256.Sum256([]byte(fmt.Sprintf("workflow-callback-v1\x00%s\x00%s\x00%d", runID, status, revision)))
	return "evt_" + hex.EncodeToString(b[:])
}
func callbackEventType(status string) string {
	switch status {
	case "queued", "waiting_human", "completed", "failed", "cancelled", "interrupted":
		return "run." + status
	}
	return ""
}
func insertWorkflowCallbackSubscription(tx *sql.Tx, meta WorkflowRunMeta) error {
	if meta.Callback == nil {
		return nil
	}
	if err := validateWorkflowCallbackTarget(*meta.Callback); err != nil {
		return err
	}
	if meta.Callback.PublicationID != meta.PublicationID || meta.PublicationID == "" {
		return fmt.Errorf("%w: callback publication mismatch", ErrConflict)
	}
	raw, err := json.Marshal(meta.Callback)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO workflow_callback_subscriptions(run_id,publication_id,target) VALUES(?,?,?)`, meta.ID, meta.PublicationID, raw); err != nil {
		return err
	}
	return recordWorkflowCallbackState(tx, meta, "queued", 0, meta.CreatedAt)
}
func recordWorkflowCallbackState(tx *sql.Tx, meta WorkflowRunMeta, status string, revision, at int64) error {
	var raw []byte
	var lastStatus string
	var lastRevision int64
	err := tx.QueryRow(`SELECT target,last_status,last_revision FROM workflow_callback_subscriptions WHERE run_id=?`, meta.ID).Scan(&raw, &lastStatus, &lastRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if revision < lastRevision {
		return nil
	}
	if status == lastStatus {
		if revision > lastRevision {
			_, err = tx.Exec(`UPDATE workflow_callback_subscriptions SET last_revision=? WHERE run_id=?`, revision, meta.ID)
		}
		return err
	}
	// A terminal virtual queued cancellation has revision zero. Once settled,
	// reconciliation must not regress it to a stale queued admission snapshot.
	if callbackTerminal(lastStatus) {
		return nil
	}
	typ := callbackEventType(status)
	if typ != "" {
		var target WorkflowCallbackTarget
		if err = json.Unmarshal(raw, &target); err != nil {
			return err
		}
		if at <= 0 {
			at = now()
		}
		var sequence int64
		if err := tx.QueryRow(`SELECT COALESCE(MAX(sequence),0)+1 FROM workflow_callback_outbox WHERE run_id=?`, meta.ID).Scan(&sequence); err != nil {
			return err
		}
		event := WorkflowCallbackEvent{Sequence: sequence, ID: callbackEventID(meta.ID, status, revision), Type: typ, CreatedAt: time.UnixMilli(at).UTC().Format(time.RFC3339Nano), PublicationID: meta.PublicationID, WorkflowID: meta.WorkflowID, VersionID: meta.VersionID, BusinessID: meta.BusinessID, ExternalSubject: meta.ExternalSubject, RunID: meta.ID, RetryOf: meta.RetryOf, Status: status, StateRevision: revision}
		payload, e := json.Marshal(event)
		if e != nil {
			return e
		}
		_, err = tx.Exec(`INSERT OR IGNORE INTO workflow_callback_outbox(id,run_id,publication_id,event_type,state_revision,sequence,business_id,payload,target_url,signing_secret,status,next_attempt_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,'pending',?,?)`, event.ID, meta.ID, meta.PublicationID, typ, revision, sequence, meta.BusinessID, payload, target.URL, target.SigningSecret, at, at)
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec(`UPDATE workflow_callback_subscriptions SET last_status=?,last_revision=? WHERE run_id=?`, status, revision, meta.ID)
	return err
}
func callbackTerminal(s string) bool {
	return s == "completed" || s == "failed" || s == "cancelled" || s == "interrupted"
}
func (s *Store) RecordWorkflowCallbackState(runID, status string, revision, at int64) error {
	if !validText(runID, 128, true) || !validText(status, 64, true) || revision < 0 {
		return ErrConflict
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	meta, err := workflowMeta(tx, runID)
	if err != nil {
		return err
	}
	if err = recordWorkflowCallbackState(tx, meta, status, revision, at); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) ListWorkflowCallbackSubscriptions() ([]WorkflowCallbackSubscription, error) {
	rows, err := s.db.Query(`SELECT run_id,publication_id,target,last_status,last_revision FROM workflow_callback_subscriptions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WorkflowCallbackSubscription{}
	for rows.Next() {
		var v WorkflowCallbackSubscription
		var raw []byte
		if err = rows.Scan(&v.RunID, &v.PublicationID, &raw, &v.LastStatus, &v.LastRevision); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &v.Target); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

const callbackDeliveryColumns = `id,event_type,run_id,publication_id,business_id,state_revision,sequence,status,attempts,failures,next_attempt_at,last_status,last_error,created_at,delivered_at,target_url,blocked_reason,payload,signing_secret,lease_until,claim_token`

func scanWorkflowCallbackDelivery(row rowScanner) (WorkflowCallbackDelivery, error) {
	var d WorkflowCallbackDelivery
	err := row.Scan(&d.ID, &d.EventType, &d.RunID, &d.PublicationID, &d.BusinessID, &d.StateRevision, &d.Sequence, &d.Status, &d.Attempts, &d.Failures, &d.NextAttemptAt, &d.LastStatus, &d.LastError, &d.CreatedAt, &d.DeliveredAt, &d.TargetURL, &d.BlockedReason, &d.Payload, &d.SigningSecret, &d.LeaseUntil, &d.ClaimToken)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}
func (s *Store) WorkflowCallbackDelivery(id string) (WorkflowCallbackDelivery, error) {
	return scanWorkflowCallbackDelivery(s.db.QueryRow(`SELECT `+callbackDeliveryColumns+` FROM workflow_callback_outbox WHERE id=?`, id))
}
func (s *Store) ListWorkflowCallbackDeliveries(publicationID string, limit int) ([]WorkflowCallbackDelivery, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT `+callbackDeliveryColumns+` FROM workflow_callback_outbox WHERE publication_id=? ORDER BY created_at DESC,sequence DESC,id DESC LIMIT ?`, publicationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WorkflowCallbackDelivery{}
	for rows.Next() {
		v, e := scanWorkflowCallbackDelivery(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Due callbacks are only leased immediately before their HTTP attempt. A
// stopped/crashed process leaves a bounded lease; the next controller retries.
func (s *Store) DueWorkflowCallbacks(at int64, limit int) ([]WorkflowCallbackDelivery, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT `+callbackDeliveryColumns+` FROM workflow_callback_outbox WHERE NOT EXISTS(SELECT 1 FROM workflow_callback_outbox older WHERE older.run_id=workflow_callback_outbox.run_id AND older.sequence<workflow_callback_outbox.sequence AND older.status!='delivered') AND ((status IN ('pending','paused') AND next_attempt_at<=?) OR (status='delivering' AND lease_until<=?)) ORDER BY created_at,sequence,id LIMIT ?`, at, at, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WorkflowCallbackDelivery{}
	for rows.Next() {
		d, e := scanWorkflowCallbackDelivery(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *Store) HoldWorkflowCallback(id, status, reason string, at int64) error {
	if status != "paused" && status != "blocked" {
		return ErrConflict
	}
	_, err := s.db.Exec(`UPDATE workflow_callback_outbox SET status=?,blocked_reason=?,next_attempt_at=?,lease_until=0,claim_token='' WHERE id=? AND (status IN ('pending','paused') OR (status='delivering' AND lease_until<=?))`, status, reason, at+1000, id, at)
	return err
}
func (s *Store) ClaimWorkflowCallback(id, claim string, at, leaseUntil int64) (WorkflowCallbackDelivery, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return WorkflowCallbackDelivery{}, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE workflow_callback_outbox SET status='delivering',attempts=attempts+1,lease_until=?,claim_token=?,blocked_reason='' WHERE id=? AND NOT EXISTS(SELECT 1 FROM workflow_callback_outbox older WHERE older.run_id=workflow_callback_outbox.run_id AND older.sequence<workflow_callback_outbox.sequence AND older.status!='delivered') AND ((status IN ('pending','paused') AND next_attempt_at<=?) OR (status='delivering' AND lease_until<=?))`, leaseUntil, claim, id, at, at)
	if err != nil {
		return WorkflowCallbackDelivery{}, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return WorkflowCallbackDelivery{}, ErrConflict
	}
	d, err := scanWorkflowCallbackDelivery(tx.QueryRow(`SELECT `+callbackDeliveryColumns+` FROM workflow_callback_outbox WHERE id=?`, id))
	if err != nil {
		return d, err
	}
	return d, tx.Commit()
}
func (s *Store) FinishWorkflowCallback(id, claim string, httpStatus int, reason string, at, next int64, maxFailures int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	d, err := scanWorkflowCallbackDelivery(tx.QueryRow(`SELECT `+callbackDeliveryColumns+` FROM workflow_callback_outbox WHERE id=?`, id))
	if err != nil {
		return err
	}
	if d.Status != "delivering" || d.ClaimToken != claim {
		return ErrConflict
	}
	status, delivered, failures := "delivered", at, d.Failures
	if httpStatus < 200 || httpStatus >= 300 {
		failures++
		status = "pending"
		delivered = 0
		if failures >= maxFailures {
			status = "failed"
		}
	}
	if len(reason) > 512 {
		reason = reason[:512]
	}
	_, err = tx.Exec(`UPDATE workflow_callback_outbox SET status=?,failures=?,next_attempt_at=?,last_status=?,last_error=?,delivered_at=?,lease_until=0,claim_token='' WHERE id=?`, status, failures, next, httpStatus, reason, delivered, id)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) RetryWorkflowCallback(id, publicationID string, target WorkflowCallbackTarget, useCurrent bool, at int64) error {
	if err := validateWorkflowCallbackTarget(target); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	d, err := scanWorkflowCallbackDelivery(tx.QueryRow(`SELECT `+callbackDeliveryColumns+` FROM workflow_callback_outbox WHERE id=? AND publication_id=?`, id, publicationID))
	if err != nil {
		return err
	}
	if d.Status == "delivering" || d.Status == "delivered" {
		return ErrConflict
	}
	if d.TargetURL != target.URL || d.SigningSecret != target.SigningSecret {
		if !useCurrent {
			return fmt.Errorf("%w: callback target changed; confirm use_current_target", ErrConflict)
		}
	}
	_, err = tx.Exec(`UPDATE workflow_callback_outbox SET status='pending',failures=0,next_attempt_at=?,blocked_reason='',target_url=?,signing_secret=?,lease_until=0,claim_token='' WHERE id=?`, at, target.URL, target.SigningSecret, id)
	if err != nil {
		return err
	}
	return tx.Commit()
}
