package taskqueue

// The workflow catalog owns admission, attribution, grants and durable human
// commands. Node execution state stays in the workflow runner's run.json.
import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

const DefaultSpaceID = "default"

type WorkflowRunMeta struct {
	ID                   string            `json:"id"`
	SpaceID              string            `json:"space_id"`
	WorkflowID           string            `json:"workflow_id"`
	VersionID            string            `json:"version_id,omitempty"`
	OwnerID              string            `json:"owner_id"`
	InitiatorID          string            `json:"initiator_id,omitempty"`
	ExecutionPrincipalID string            `json:"execution_principal_id,omitempty"`
	Source               string            `json:"source"`
	SourceID             string            `json:"source_id,omitempty"`
	Definition           json.RawMessage   `json:"definition"`
	Owner                json.RawMessage   `json:"owner,omitempty"`
	CreatedAt            int64             `json:"created_at"`
	RetryOf              string            `json:"retry_of,omitempty"`
	PublicationID        string            `json:"publication_id,omitempty"`
	IntegrationID        string            `json:"integration_id,omitempty"`
	ExternalSubject      string            `json:"external_subject,omitempty"`
	BusinessID           string            `json:"business_id,omitempty"`
	Inputs               map[string]string `json:"inputs,omitempty"`
	// TestRun distinguishes a frozen draft execution from a published business
	// invocation. These additive fields leave historical metadata unchanged.
	TestRun         bool                                  `json:"test_run,omitempty"`
	DraftRevision   int                                   `json:"draft_revision,omitempty"`
	DefinitionHash  string                                `json:"definition_hash,omitempty"`
	Callback        *WorkflowCallbackTarget               `json:"callback,omitempty"`
	Approvals       map[string]WorkflowApprovalAssignment `json:"approvals,omitempty"`
	CancelRequested bool                                  `json:"cancel_requested,omitempty"`
}

type WorkflowRunFilter struct {
	SpaceID    string
	WorkflowID string
	OwnerID    string
	Source     string
	Limit      int // zero returns all matching records to the trusted host
	Offset     int
}

func initWorkflowSchema(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS workflow_control_schema(version INTEGER NOT NULL);
INSERT INTO workflow_control_schema SELECT 1 WHERE NOT EXISTS(SELECT 1 FROM workflow_control_schema);
CREATE TABLE IF NOT EXISTS workflow_run_catalog(
 id TEXT PRIMARY KEY, space_id TEXT NOT NULL, workflow_id TEXT NOT NULL, owner_id TEXT NOT NULL,
 source TEXT NOT NULL, source_id TEXT NOT NULL, created_at INTEGER NOT NULL,
 metadata BLOB NOT NULL, cancel_requested INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS workflow_run_scope ON workflow_run_catalog(space_id,workflow_id,owner_id,created_at DESC,id);
CREATE TABLE IF NOT EXISTS workflow_admissions(
 owner_id TEXT NOT NULL, workflow_id TEXT NOT NULL, request_key TEXT NOT NULL,
 fingerprint TEXT NOT NULL, run_id TEXT NOT NULL REFERENCES workflow_run_catalog(id),
 execution_id TEXT NOT NULL REFERENCES taskqueue_runs(id), PRIMARY KEY(owner_id,workflow_id,request_key));
CREATE TABLE IF NOT EXISTS workflow_executions(
 run_id TEXT NOT NULL REFERENCES workflow_run_catalog(id),
 execution_id TEXT NOT NULL UNIQUE REFERENCES taskqueue_runs(id), PRIMARY KEY(run_id,execution_id));
CREATE TABLE IF NOT EXISTS workflow_commands(
 run_id TEXT NOT NULL REFERENCES workflow_run_catalog(id), command_key TEXT NOT NULL,
 payload BLOB NOT NULL, fingerprint TEXT NOT NULL,
 execution_id TEXT NOT NULL REFERENCES taskqueue_runs(id), PRIMARY KEY(run_id,command_key));
CREATE TABLE IF NOT EXISTS workflow_space_roles(
 space_id TEXT NOT NULL, principal_id TEXT NOT NULL, role TEXT NOT NULL, PRIMARY KEY(space_id,principal_id));
CREATE TABLE IF NOT EXISTS workflow_resource_grants(
 workflow_id TEXT NOT NULL, principal_id TEXT NOT NULL, actions BLOB NOT NULL, PRIMARY KEY(workflow_id,principal_id));
CREATE TABLE IF NOT EXISTS workflow_run_grants(
 run_id TEXT NOT NULL REFERENCES workflow_run_catalog(id), principal_id TEXT NOT NULL,
 node_id TEXT NOT NULL, actions BLOB NOT NULL, PRIMARY KEY(run_id,principal_id,node_id));`)
	if err != nil {
		return err
	}
	var version int
	if err := tx.QueryRow("SELECT version FROM workflow_control_schema").Scan(&version); err != nil {
		return err
	}
	if version != 1 {
		return fmt.Errorf("%w: unsupported workflow control schema version", ErrConflict)
	}
	return tx.Commit()
}

func normalizeWorkflowMeta(meta WorkflowRunMeta) (WorkflowRunMeta, error) {
	if meta.SpaceID == "" {
		meta.SpaceID = DefaultSpaceID
	}
	for _, value := range []struct {
		text string
		max  int
		req  bool
	}{{meta.ID, 128, true}, {meta.SpaceID, 128, true}, {meta.WorkflowID, 256, true}, {meta.VersionID, 256, false},
		{meta.OwnerID, 512, true}, {meta.InitiatorID, 512, false}, {meta.ExecutionPrincipalID, 512, false}, {meta.Source, 64, true}, {meta.SourceID, 512, false}, {meta.RetryOf, 128, false}, {meta.PublicationID, 128, false}, {meta.IntegrationID, 128, false}, {meta.ExternalSubject, 256, false}, {meta.BusinessID, 256, false}} {
		if !validText(value.text, value.max, value.req) {
			return WorkflowRunMeta{}, fmt.Errorf("%w: invalid workflow metadata", ErrConflict)
		}
	}
	if meta.CreatedAt < 0 || len(meta.Definition) == 0 || len(meta.Definition) > 4<<20 || !json.Valid(meta.Definition) ||
		len(meta.Owner) > 16<<10 || len(meta.Owner) > 0 && !json.Valid(meta.Owner) {
		return WorkflowRunMeta{}, fmt.Errorf("%w: invalid workflow snapshot", ErrConflict)
	}
	if meta.TestRun {
		if meta.DraftRevision < 1 || !validText(meta.DefinitionHash, 256, true) || meta.PublicationID != "" || meta.Callback != nil {
			return WorkflowRunMeta{}, fmt.Errorf("%w: invalid workflow draft test", ErrConflict)
		}
	} else if meta.DraftRevision != 0 || meta.DefinitionHash != "" {
		return WorkflowRunMeta{}, fmt.Errorf("%w: test metadata without draft test", ErrConflict)
	}
	if len(meta.Inputs) > 32 {
		return WorkflowRunMeta{}, fmt.Errorf("%w: too many workflow inputs", ErrConflict)
	}
	inputCopy := map[string]string{}
	total := 0
	for k, v := range meta.Inputs {
		if !validText(k, 64, true) || len(v) > 16<<10 {
			return WorkflowRunMeta{}, fmt.Errorf("%w: invalid workflow input", ErrConflict)
		}
		total += len(v)
		inputCopy[k] = v
	}
	if total > 64<<10 {
		return WorkflowRunMeta{}, fmt.Errorf("%w: workflow inputs too large", ErrConflict)
	}
	approvals, err := normalizeWorkflowApprovals(meta)
	if err != nil {
		return WorkflowRunMeta{}, err
	}
	meta.Approvals = approvals
	if meta.Callback != nil {
		target := *meta.Callback
		if meta.PublicationID == "" || target.PublicationID != meta.PublicationID || !validText(target.URL, 4096, true) || !validText(target.SigningSecret, 256, true) {
			return WorkflowRunMeta{}, fmt.Errorf("%w: invalid workflow callback subscription", ErrConflict)
		}
		meta.Callback = &target
	}
	if len(inputCopy) > 0 {
		meta.Inputs = inputCopy
	}
	meta.Definition = bytes.Clone(meta.Definition)
	meta.Owner = bytes.Clone(meta.Owner)
	return meta, nil
}

const workflowMetaColumns = `metadata,cancel_requested`

func decodeWorkflowMeta(row rowScanner) (WorkflowRunMeta, error) {
	var raw []byte
	var cancel bool
	if err := row.Scan(&raw, &cancel); err != nil {
		if err == sql.ErrNoRows {
			return WorkflowRunMeta{}, ErrNotFound
		}
		return WorkflowRunMeta{}, err
	}
	var meta WorkflowRunMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return WorkflowRunMeta{}, fmt.Errorf("%w: invalid stored workflow metadata", ErrConflict)
	}
	meta.CancelRequested = cancel
	return normalizeWorkflowMeta(meta)
}

func workflowMeta(q queryer, id string) (WorkflowRunMeta, error) {
	return decodeWorkflowMeta(q.QueryRow("SELECT "+workflowMetaColumns+" FROM workflow_run_catalog WHERE id=?", id))
}

func insertWorkflowMeta(tx *sql.Tx, meta WorkflowRunMeta) error {
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO workflow_run_catalog(id,space_id,workflow_id,owner_id,source,source_id,created_at,metadata,cancel_requested)
 VALUES(?,?,?,?,?,?,?,?,?)`, meta.ID, meta.SpaceID, meta.WorkflowID, meta.OwnerID, meta.Source, meta.SourceID, meta.CreatedAt, raw, meta.CancelRequested)
	if err != nil {
		return err
	}
	if err := grantWorkflowApprovers(tx, meta); err != nil {
		return err
	}
	return insertWorkflowCallbackSubscription(tx, meta)
}

func bindWorkflowExecution(meta WorkflowRunMeta, req Request) Request {
	req.TaskID = "workflow-run:" + meta.ID
	req.WorkflowRunID = meta.ID
	req.WorkflowID = meta.WorkflowID
	req.OwnerID = meta.OwnerID
	return req
}

// workflowAdmissionFingerprint omits only generated identities and wall-clock
// admission times. Reusing an intent for another definition or payload fails.
func workflowAdmissionFingerprint(meta WorkflowRunMeta, req Request) (string, error) {
	meta.ID, meta.CreatedAt, meta.CancelRequested = "", 0, false
	req.TaskID, req.WorkflowRunID, req.DueAt = "workflow-run", "", 0
	raw, err := json.Marshal(struct {
		Meta    WorkflowRunMeta `json:"meta"`
		Request Request         `json:"request"`
	}{meta, req})
	if err != nil {
		return "", err
	}
	return digest(raw, nil), nil
}

// AdmitWorkflow atomically records a logical run and its first execution.
// requestKey is isolated by owner and workflow. A repeated identical intent
// returns the original run, even if the caller generated a new ID and time.
func (s *Store) AdmitWorkflow(meta WorkflowRunMeta, requestKey string, req Request) (WorkflowRunMeta, Run, bool, error) {
	meta, err := normalizeWorkflowMeta(meta)
	if err != nil || !validText(requestKey, 512, true) || meta.CancelRequested {
		if err == nil {
			err = ErrConflict
		}
		return WorkflowRunMeta{}, Run{}, false, err
	}
	if meta.CreatedAt == 0 {
		meta.CreatedAt = now()
	}
	req = bindWorkflowExecution(meta, req)
	if err := validateRequest(req); err != nil {
		return WorkflowRunMeta{}, Run{}, false, err
	}
	fingerprint, err := workflowAdmissionFingerprint(meta, req)
	if err != nil {
		return WorkflowRunMeta{}, Run{}, false, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return WorkflowRunMeta{}, Run{}, false, err
	}
	defer tx.Rollback()
	var oldID, executionID, oldFingerprint string
	err = tx.QueryRow(`SELECT run_id,execution_id,fingerprint FROM workflow_admissions WHERE owner_id=? AND workflow_id=? AND request_key=?`, meta.OwnerID, meta.WorkflowID, requestKey).Scan(&oldID, &executionID, &oldFingerprint)
	if err == nil {
		if oldFingerprint != fingerprint {
			return WorkflowRunMeta{}, Run{}, false, fmt.Errorf("%w: workflow request key reused with different content", ErrConflict)
		}
		old, err := workflowMeta(tx, oldID)
		if err != nil {
			return WorkflowRunMeta{}, Run{}, false, err
		}
		execution, err := get(tx, executionID)
		return old, execution.Run, false, err
	}
	if err != sql.ErrNoRows {
		return WorkflowRunMeta{}, Run{}, false, err
	}
	if err := insertWorkflowMeta(tx, meta); err != nil {
		return WorkflowRunMeta{}, Run{}, false, err
	}
	if err := grantWorkflowInitiator(tx, meta); err != nil {
		return WorkflowRunMeta{}, Run{}, false, err
	}
	execution, _, err := enqueueTx(tx, req)
	if err != nil {
		return WorkflowRunMeta{}, Run{}, false, err
	}
	if _, err := tx.Exec(`INSERT INTO workflow_admissions(owner_id,workflow_id,request_key,fingerprint,run_id,execution_id) VALUES(?,?,?,?,?,?)`, meta.OwnerID, meta.WorkflowID, requestKey, fingerprint, meta.ID, execution.ID); err != nil {
		return WorkflowRunMeta{}, Run{}, false, err
	}
	if err := linkWorkflowExecution(tx, meta.ID, execution.ID); err != nil {
		return WorkflowRunMeta{}, Run{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return WorkflowRunMeta{}, Run{}, false, err
	}
	return meta, execution, true, nil
}

func linkWorkflowExecution(tx *sql.Tx, runID, executionID string) error {
	var boundID string
	err := tx.QueryRow("SELECT run_id FROM workflow_executions WHERE execution_id=?", executionID).Scan(&boundID)
	if err == nil {
		if boundID != runID {
			return ErrConflict
		}
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	_, err = tx.Exec("INSERT INTO workflow_executions(run_id,execution_id) VALUES(?,?)", runID, executionID)
	return err
}

// A delegated retry keeps its approved execution owner, while the actual
// initiator can follow and control its new run. Approval remains separately
// assigned and is never implied by initiating a retry.
func grantWorkflowInitiator(tx *sql.Tx, meta WorkflowRunMeta) error {
	if meta.InitiatorID == "" || meta.InitiatorID == meta.OwnerID {
		return nil
	}
	_, err := tx.Exec(`INSERT INTO workflow_run_grants(run_id,principal_id,node_id,actions) VALUES(?,?,'',?)`, meta.ID, meta.InitiatorID, []byte(`["artifact.read","run.cancel","run.read","run.retry"]`))
	return err
}

func (s *Store) GetWorkflowRun(id string) (WorkflowRunMeta, error) {
	if err := validID(id); err != nil {
		return WorkflowRunMeta{}, err
	}
	return workflowMeta(s.db, id)
}

// BindWorkflowExecution registers an already-admitted schedule segment without
// changing its immutable request or reserving another worker slot.
func (s *Store) BindWorkflowExecution(meta WorkflowRunMeta, requestKey, executionID string) (WorkflowRunMeta, bool, error) {
	meta, err := normalizeWorkflowMeta(meta)
	if err != nil {
		return WorkflowRunMeta{}, false, err
	}
	if !validText(requestKey, 512, true) || meta.CancelRequested {
		return WorkflowRunMeta{}, false, ErrConflict
	}
	if err := validID(executionID); err != nil {
		return WorkflowRunMeta{}, false, err
	}
	if meta.CreatedAt == 0 {
		meta.CreatedAt = now()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return WorkflowRunMeta{}, false, err
	}
	defer tx.Rollback()
	execution, err := get(tx, executionID)
	if err != nil {
		return WorkflowRunMeta{}, false, err
	}
	if execution.WorkflowID != meta.WorkflowID || execution.OwnerID != meta.OwnerID || (execution.Status != Queued && execution.Status != Running) {
		return WorkflowRunMeta{}, false, ErrConflict
	}
	fingerprint, err := workflowAdmissionFingerprint(meta, execution.Request)
	if err != nil {
		return WorkflowRunMeta{}, false, err
	}
	var oldID, oldExecution, oldFingerprint string
	err = tx.QueryRow(`SELECT run_id,execution_id,fingerprint FROM workflow_admissions WHERE owner_id=? AND workflow_id=? AND request_key=?`, meta.OwnerID, meta.WorkflowID, requestKey).Scan(&oldID, &oldExecution, &oldFingerprint)
	if err == nil {
		if oldExecution != executionID || oldFingerprint != fingerprint {
			return WorkflowRunMeta{}, false, ErrConflict
		}
		old, err := workflowMeta(tx, oldID)
		return old, false, err
	}
	if err != sql.ErrNoRows {
		return WorkflowRunMeta{}, false, err
	}
	if _, err := workflowMeta(tx, meta.ID); err == nil {
		return WorkflowRunMeta{}, false, ErrConflict
	} else if err != ErrNotFound {
		return WorkflowRunMeta{}, false, err
	}
	var bound string
	err = tx.QueryRow("SELECT run_id FROM workflow_executions WHERE execution_id=?", executionID).Scan(&bound)
	if err == nil {
		return WorkflowRunMeta{}, false, ErrConflict
	}
	if err != sql.ErrNoRows {
		return WorkflowRunMeta{}, false, err
	}
	if err := insertWorkflowMeta(tx, meta); err != nil {
		return WorkflowRunMeta{}, false, err
	}
	if err := grantWorkflowInitiator(tx, meta); err != nil {
		return WorkflowRunMeta{}, false, err
	}
	if _, err := tx.Exec(`INSERT INTO workflow_admissions(owner_id,workflow_id,request_key,fingerprint,run_id,execution_id) VALUES(?,?,?,?,?,?)`, meta.OwnerID, meta.WorkflowID, requestKey, fingerprint, meta.ID, executionID); err != nil {
		return WorkflowRunMeta{}, false, err
	}
	if err := linkWorkflowExecution(tx, meta.ID, executionID); err != nil {
		return WorkflowRunMeta{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return WorkflowRunMeta{}, false, err
	}
	return meta, true, nil
}

func (s *Store) WorkflowRunForExecution(executionID string) (WorkflowRunMeta, error) {
	if err := validID(executionID); err != nil {
		return WorkflowRunMeta{}, err
	}
	return decodeWorkflowMeta(s.db.QueryRow(`SELECT c.metadata,c.cancel_requested FROM workflow_run_catalog c JOIN workflow_executions e ON e.run_id=c.id WHERE e.execution_id=?`, executionID))
}

// ImportWorkflowRun registers old files without rewriting or reassigning them.
// Existing admissions retain their original metadata and cancellation intent.
func (s *Store) ImportWorkflowRun(meta WorkflowRunMeta) error {
	meta, err := normalizeWorkflowMeta(meta)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	old, err := workflowMeta(tx, meta.ID)
	if err == nil {
		if old.WorkflowID != meta.WorkflowID || old.OwnerID != meta.OwnerID || old.SpaceID != meta.SpaceID {
			return fmt.Errorf("%w: imported workflow cannot replace its identity or owner", ErrConflict)
		}
		return nil
	}
	if err != ErrNotFound {
		return err
	}
	if err := insertWorkflowMeta(tx, meta); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListWorkflowRuns(filter WorkflowRunFilter) ([]WorkflowRunMeta, error) {
	if filter.Limit < 0 || filter.Limit > 10000 || filter.Offset < 0 || filter.Offset > 100000000 {
		return nil, ErrConflict
	}
	where, args := []string{"1=1"}, []any{}
	for _, f := range []struct {
		column, value string
		max           int
	}{
		{"space_id", filter.SpaceID, 128}, {"workflow_id", filter.WorkflowID, 256}, {"owner_id", filter.OwnerID, 512}, {"source", filter.Source, 64},
	} {
		if !validText(f.value, f.max, false) {
			return nil, ErrConflict
		}
		if f.value != "" {
			where = append(where, f.column+"=?")
			args = append(args, f.value)
		}
	}
	query := "SELECT " + workflowMetaColumns + " FROM workflow_run_catalog WHERE " + strings.Join(where, " AND ") + " ORDER BY created_at DESC,id DESC"
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	} else if filter.Offset > 0 {
		query += " LIMIT -1"
	}
	if filter.Offset > 0 {
		query += " OFFSET ?"
		args = append(args, filter.Offset)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WorkflowRunMeta{}
	for rows.Next() {
		meta, err := decodeWorkflowMeta(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, meta)
	}
	return out, rows.Err()
}

// EnqueueWorkflow is a non-human continuation. TriggerID is its idempotency
// identity; duplicate content returns the original execution segment.
func (s *Store) EnqueueWorkflow(runID string, req Request) (Run, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Run{}, false, err
	}
	defer tx.Rollback()
	meta, err := workflowMeta(tx, runID)
	if err != nil {
		return Run{}, false, err
	}
	if meta.CancelRequested {
		return Run{}, false, ErrConflict
	}
	req = bindWorkflowExecution(meta, req)
	// Retrying an identical trigger keeps its original eligibility time.
	if old, e := decodeRow(tx.QueryRow("SELECT "+columns+" FROM taskqueue_runs WHERE task_id=? AND trigger_id=?", req.TaskID, req.TriggerID)); e == nil {
		req.DueAt = old.DueAt
	} else if e != ErrNotFound {
		return Run{}, false, e
	}
	execution, created, err := enqueueTx(tx, req)
	if err != nil {
		return Run{}, false, err
	}
	if err := linkWorkflowExecution(tx, runID, execution.ID); err != nil {
		return Run{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, false, err
	}
	return execution, created, nil
}

// WorkflowCommand admits exactly one decision for a human wait generation.
// Same-key/same-payload retries return its original segment; competing actions
// conflict. The command and the continuation are committed together.
func (s *Store) WorkflowCommand(runID, commandKey string, payload json.RawMessage, req Request) (WorkflowRunMeta, Run, bool, error) {
	if err := validID(runID); err != nil {
		return WorkflowRunMeta{}, Run{}, false, err
	}
	if !validText(commandKey, 512, true) || len(payload) == 0 || len(payload) > MaxPayloadBytes || !json.Valid(payload) {
		return WorkflowRunMeta{}, Run{}, false, ErrConflict
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, payload); err != nil {
		return WorkflowRunMeta{}, Run{}, false, err
	}
	payload = compact.Bytes()
	fingerprint := digest(payload, nil)
	tx, err := s.db.Begin()
	if err != nil {
		return WorkflowRunMeta{}, Run{}, false, err
	}
	defer tx.Rollback()
	meta, err := workflowMeta(tx, runID)
	if err != nil {
		return WorkflowRunMeta{}, Run{}, false, err
	}
	var previous, executionID string
	err = tx.QueryRow("SELECT fingerprint,execution_id FROM workflow_commands WHERE run_id=? AND command_key=?", runID, commandKey).Scan(&previous, &executionID)
	if err == nil {
		if previous != fingerprint {
			return WorkflowRunMeta{}, Run{}, false, ErrConflict
		}
		execution, err := get(tx, executionID)
		return meta, execution.Run, false, err
	}
	if err != sql.ErrNoRows {
		return WorkflowRunMeta{}, Run{}, false, err
	}
	if meta.CancelRequested {
		return WorkflowRunMeta{}, Run{}, false, ErrConflict
	}
	req = bindWorkflowExecution(meta, req)
	req.TriggerID = "command:" + digest([]byte(commandKey), nil)
	if !bytes.Equal(bytes.TrimSpace(req.Payload), bytes.TrimSpace(payload)) {
		req.Payload = bytes.Clone(payload)
	}
	execution, _, err := enqueueTx(tx, req)
	if err != nil {
		return WorkflowRunMeta{}, Run{}, false, err
	}
	if _, err := tx.Exec("INSERT INTO workflow_commands(run_id,command_key,payload,fingerprint,execution_id) VALUES(?,?,?,?,?)", runID, commandKey, payload, fingerprint, execution.ID); err != nil {
		return WorkflowRunMeta{}, Run{}, false, err
	}
	if err := linkWorkflowExecution(tx, runID, execution.ID); err != nil {
		return WorkflowRunMeta{}, Run{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return WorkflowRunMeta{}, Run{}, false, err
	}
	return meta, execution, true, nil
}

func (s *Store) ListWorkflowExecutions(runID string) ([]Run, error) {
	if err := validID(runID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query("SELECT "+prefixedColumns("q")+" FROM taskqueue_runs q JOIN workflow_executions e ON e.execution_id=q.id WHERE e.run_id=? ORDER BY q.created_at,q.id", runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		item, err := decodeRow(rows)
		if err != nil {
			return nil, err
		}
		item.WorkflowRunID = runID
		out = append(out, item.Run)
	}
	return out, rows.Err()
}

func prefixedColumns(prefix string) string {
	fields := strings.Split(columns, ",")
	for i := range fields {
		fields[i] = prefix + "." + fields[i]
	}
	return strings.Join(fields, ",")
}

// CancelWorkflow records durable logical intent before cancelling unstarted
// segments. Running segments retain their revision for their real receipt.
func (s *Store) CancelWorkflow(runID string) (WorkflowRunMeta, error) {
	if err := validID(runID); err != nil {
		return WorkflowRunMeta{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return WorkflowRunMeta{}, err
	}
	defer tx.Rollback()
	meta, err := workflowMeta(tx, runID)
	if err != nil {
		return WorkflowRunMeta{}, err
	}
	var exhausted int
	if err := tx.QueryRow(`SELECT count(*) FROM taskqueue_runs WHERE id IN (SELECT execution_id FROM workflow_executions WHERE run_id=?) AND status='queued' AND revision=?`, runID, int64(math.MaxInt64)).Scan(&exhausted); err != nil {
		return WorkflowRunMeta{}, err
	}
	if exhausted > 0 {
		return WorkflowRunMeta{}, ErrConflict
	}
	if _, err := tx.Exec("UPDATE workflow_run_catalog SET cancel_requested=1 WHERE id=?", runID); err != nil {
		return WorkflowRunMeta{}, err
	}
	_, err = tx.Exec(`UPDATE taskqueue_runs SET cancel_requested=1,
 status=CASE WHEN status='queued' THEN 'cancelled' ELSE status END,
 revision=CASE WHEN status='queued' THEN revision+1 ELSE revision END,
 finished_at=CASE WHEN status='queued' THEN max(?,created_at) ELSE finished_at END,
 end_reason=CASE WHEN status='queued' THEN 'cancelled_before_start' ELSE end_reason END
 WHERE id IN (SELECT execution_id FROM workflow_executions WHERE run_id=?) AND status IN ('queued','running')`, now(), runID)
	if err != nil {
		return WorkflowRunMeta{}, err
	}
	if err := tx.Commit(); err != nil {
		return WorkflowRunMeta{}, err
	}
	meta.CancelRequested = true
	return meta, nil
}
