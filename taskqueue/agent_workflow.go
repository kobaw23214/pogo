package taskqueue

// Agent-private workflow state uses separate tables. Admission, confirmation
// and queue continuation share one transaction; no platform workflow record is
// imported, reinterpreted, or rewritten.
import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	af "github.com/yosukeno/pogo/agentworkflow"
)

func initAgentWorkflowSchema(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS agent_workflow_definitions(
 id TEXT PRIMARY KEY,owner_id TEXT NOT NULL,agent_id TEXT NOT NULL,definition BLOB NOT NULL);
CREATE INDEX IF NOT EXISTS agent_workflow_scope ON agent_workflow_definitions(owner_id,agent_id);
CREATE TABLE IF NOT EXISTS agent_workflow_versions(
 workflow_id TEXT NOT NULL,version_id TEXT NOT NULL,definition BLOB NOT NULL,PRIMARY KEY(workflow_id,version_id));
CREATE TABLE IF NOT EXISTS agent_workflow_edits(
 owner_id TEXT NOT NULL,agent_id TEXT NOT NULL,request_key TEXT NOT NULL,fingerprint TEXT NOT NULL,response BLOB NOT NULL,
 PRIMARY KEY(owner_id,agent_id,request_key));
CREATE TABLE IF NOT EXISTS agent_workflow_runs(
 id TEXT PRIMARY KEY,owner_id TEXT NOT NULL,agent_id TEXT NOT NULL,workflow_id TEXT NOT NULL,created_at INTEGER NOT NULL,record BLOB NOT NULL);
CREATE INDEX IF NOT EXISTS agent_workflow_run_scope ON agent_workflow_runs(owner_id,agent_id,workflow_id,created_at DESC);
CREATE TABLE IF NOT EXISTS agent_workflow_admissions(
 owner_id TEXT NOT NULL,agent_id TEXT NOT NULL,workflow_id TEXT NOT NULL,request_key TEXT NOT NULL,fingerprint TEXT NOT NULL,
 run_id TEXT NOT NULL REFERENCES agent_workflow_runs(id),execution_id TEXT NOT NULL REFERENCES taskqueue_runs(id),
 PRIMARY KEY(owner_id,agent_id,workflow_id,request_key));
CREATE TABLE IF NOT EXISTS agent_workflow_executions(
 run_id TEXT NOT NULL REFERENCES agent_workflow_runs(id),execution_id TEXT NOT NULL UNIQUE REFERENCES taskqueue_runs(id),PRIMARY KEY(run_id,execution_id));
CREATE TABLE IF NOT EXISTS agent_workflow_commands(
 run_id TEXT NOT NULL REFERENCES agent_workflow_runs(id),step_id TEXT NOT NULL,command_key TEXT NOT NULL,fingerprint TEXT NOT NULL,
 execution_id TEXT REFERENCES taskqueue_runs(id),PRIMARY KEY(run_id,step_id),UNIQUE(run_id,command_key));`)
	return err
}

func awScope(owner, agent string) error {
	if !validText(owner, 512, true) || !validText(agent, 256, true) {
		return ErrConflict
	}
	return nil
}
func awDecodeDefinition(row rowScanner) (af.Definition, error) {
	var b []byte
	var d af.Definition
	if err := row.Scan(&b); err != nil {
		if err == sql.ErrNoRows {
			return d, ErrNotFound
		}
		return d, err
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return d, fmt.Errorf("%w: invalid internal workflow record", ErrConflict)
	}
	if d.DefinitionHash != af.Hash(d) {
		return d, fmt.Errorf("%w: internal workflow integrity check", ErrConflict)
	}
	return d, nil
}
func awGet(q queryer, owner, agent, id string) (af.Definition, error) {
	return awDecodeDefinition(q.QueryRow(`SELECT definition FROM agent_workflow_definitions WHERE id=? AND owner_id=? AND agent_id=?`, id, owner, agent))
}
func awSaveDefinition(tx *sql.Tx, d af.Definition) error {
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO agent_workflow_definitions(id,owner_id,agent_id,definition) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET definition=excluded.definition`, d.ID, d.OwnerID, d.AgentID, b)
	return err
}
func (s *Store) AWList(owner, agent string, includeDeleted ...bool) ([]af.Definition, error) {
	if len(includeDeleted) > 1 {
		return nil, ErrConflict
	}
	withDeleted := len(includeDeleted) == 1 && includeDeleted[0]
	if err := awScope(owner, agent); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT definition FROM agent_workflow_definitions WHERE owner_id=? AND agent_id=? ORDER BY id`, owner, agent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []af.Definition{}
	for rows.Next() {
		d, e := awDecodeDefinition(rows)
		if e != nil {
			return nil, e
		}
		if (!d.Archived && !d.Deleted) || withDeleted {
			out = append(out, d)
		}
	}
	return out, rows.Err()
}

// awWorkflowActive is evaluated inside the same immediate write transaction as
// the deletion marker. Admission cannot insert a Run between this check and the
// tombstone. A cancelling execution also has to exit before deletion succeeds.
func awWorkflowActive(q queryer, owner, agent, workflow string) (bool, error) {
	var active bool
	err := q.QueryRow(`SELECT EXISTS(
 SELECT 1 FROM agent_workflow_runs WHERE owner_id=? AND agent_id=? AND workflow_id=?
 AND COALESCE(json_extract(CAST(record AS TEXT),'$.status'),'') NOT IN ('completed','failed','cancelled','interrupted')
 ) OR EXISTS(
 SELECT 1 FROM agent_workflow_runs r JOIN agent_workflow_executions e ON e.run_id=r.id
 JOIN taskqueue_runs q ON q.id=e.execution_id
 WHERE r.owner_id=? AND r.agent_id=? AND r.workflow_id=? AND q.status IN ('queued','running')
 )`, owner, agent, workflow, owner, agent, workflow).Scan(&active)
	return active, err
}

func (s *Store) AWGet(owner, agent, id string) (af.Definition, error) {
	if err := awScope(owner, agent); err != nil {
		return af.Definition{}, err
	}
	return awGet(s.db, owner, agent, id)
}

func (s *Store) AWEdit(owner, agent string, req af.EditRequest) (af.Definition, bool, error) {
	if err := awScope(owner, agent); err != nil {
		return af.Definition{}, false, err
	}
	if !validText(req.RequestID, 128, true) || strings.TrimSpace(req.RequestID) != req.RequestID {
		return af.Definition{}, false, ErrConflict
	}
	b, err := json.Marshal(req)
	if err != nil || len(b) > af.MaxRequestBytes {
		return af.Definition{}, false, ErrConflict
	}
	fingerprint := digest(b, nil)
	tx, err := s.db.Begin()
	if err != nil {
		return af.Definition{}, false, err
	}
	defer tx.Rollback()
	var oldFingerprint string
	var response []byte
	err = tx.QueryRow(`SELECT fingerprint,response FROM agent_workflow_edits WHERE owner_id=? AND agent_id=? AND request_key=?`, owner, agent, req.RequestID).Scan(&oldFingerprint, &response)
	if err == nil {
		if oldFingerprint != fingerprint {
			return af.Definition{}, false, ErrConflict
		}
		var old af.Definition
		if json.Unmarshal(response, &old) != nil {
			return old, false, ErrConflict
		}
		if current, e := awGet(tx, owner, agent, old.ID); e != nil {
			return af.Definition{}, false, e
		} else if current.Deleted && req.Action != "delete" {
			return af.Definition{}, false, fmt.Errorf("%w: 流程已删除，只能查看历史", ErrConflict)
		}
		return old, true, nil
	}
	if err != sql.ErrNoRows {
		return af.Definition{}, false, err
	}
	var d af.Definition
	switch req.Action {
	case "create":
		if req.WorkflowID != "" || req.ExpectedRevision != 0 || len(req.Operations) > 0 || req.VersionID != "" || req.TargetRevision != 0 {
			return d, false, ErrConflict
		}
		id, e := randomID()
		if e != nil {
			return d, false, e
		}
		d = af.Definition{ID: "aw_" + id, OwnerID: owner, AgentID: agent, Name: req.Name, Description: req.Description, Revision: 1, Inputs: req.Inputs, Steps: req.Steps, Graph: req.Graph, ToolApprovalMode: req.ToolApprovalMode, CreatedAt: now()}
	case "patch", "archive", "delete":
		if req.WorkflowID == "" || req.ExpectedRevision < 1 || req.Name != "" || req.Description != "" || req.Steps != nil || req.Inputs != nil || req.Graph != nil || req.ToolApprovalMode != "" || req.VersionID != "" || req.TargetRevision != 0 {
			return d, false, ErrConflict
		}
		d, err = awGet(tx, owner, agent, req.WorkflowID)
		if err != nil {
			return d, false, err
		}
		if d.Deleted || (d.Archived && req.Action != "delete") || d.Revision != req.ExpectedRevision || d.Revision == math.MaxInt {
			return d, false, ErrConflict
		}
		if req.Action == "archive" || req.Action == "delete" {
			if len(req.Operations) > 0 {
				return d, false, ErrConflict
			}
			if req.Action == "delete" {
				busy, e := awWorkflowActive(tx, owner, agent, d.ID)
				if e != nil {
					return d, false, e
				}
				if busy {
					return d, false, fmt.Errorf("%w: 流程仍有运行或等待确认，请先结束运行后再删除", ErrConflict)
				}
				d.Deleted = true
			}
			d.Archived = true
			d.EnabledVersion = ""
		} else {
			d, err = af.Patch(d, req.Operations)
			if err != nil {
				return d, false, fmt.Errorf("%w: %w", ErrConflict, err)
			}
		}
		d.Revision++
	case "restore":
		if req.WorkflowID == "" || req.ExpectedRevision < 1 || !af.ValidHash(req.VersionID) || req.TargetRevision < 1 || req.Name != "" || req.Description != "" || req.Steps != nil || req.Inputs != nil || req.Graph != nil || req.ToolApprovalMode != "" || len(req.Operations) != 0 {
			return d, false, ErrConflict
		}
		current, e := awVersionCurrent(tx, owner, agent, req.WorkflowID)
		if e != nil {
			return d, false, e
		}
		if current.Archived || current.Deleted || current.Revision != req.ExpectedRevision || current.Revision == math.MaxInt {
			return d, false, ErrConflict
		}
		d, _, err = awExactDefinitionVersion(tx, current, req.VersionID, req.TargetRevision)
		if err != nil {
			return d, false, err
		}
		// Restoring creates a new draft. The active version and every previous
		// definition/Run remain immutable until an explicit enable operation.
		d.ID, d.OwnerID, d.AgentID = current.ID, current.OwnerID, current.AgentID
		d.CreatedAt, d.EnabledVersion, d.Archived = current.CreatedAt, current.EnabledVersion, false
		d.Revision = current.Revision + 1
	default:
		return d, false, ErrConflict
	}
	if d.Inputs == nil {
		d.Inputs = []af.Input{}
	}
	if d.Steps == nil {
		d.Steps = []af.Step{}
	}
	if req.Action != "delete" {
		if ds := af.Validate(d); len(ds) > 0 {
			return d, false, fmt.Errorf("%w: %w: %s", ErrConflict, af.ErrInvalid, ds[0].Message)
		}
	}
	d.UpdatedAt = now()
	d.DefinitionHash = af.Hash(d)
	if err = awSaveDefinition(tx, d); err != nil {
		return d, false, err
	}
	response, _ = json.Marshal(d)
	_, err = tx.Exec(`INSERT INTO agent_workflow_edits(owner_id,agent_id,request_key,fingerprint,response) VALUES(?,?,?,?,?)`, owner, agent, req.RequestID, fingerprint, response)
	if err != nil {
		return d, false, err
	}
	if err = tx.Commit(); err != nil {
		return d, false, err
	}
	return d, false, nil
}

func (s *Store) AWEnable(owner, agent, id string, revision int, hash string, enabled bool) (af.Definition, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return af.Definition{}, err
	}
	defer tx.Rollback()
	d, err := awGet(tx, owner, agent, id)
	if err != nil {
		return d, err
	}
	if d.Archived || d.Deleted || d.Revision != revision || d.DefinitionHash != hash {
		return d, ErrConflict
	}
	if enabled {
		if ds := af.Validate(d); len(ds) > 0 {
			return d, fmt.Errorf("%w: %s", ErrConflict, ds[0].Message)
		}
		snapshot := d
		snapshot.EnabledVersion = ""
		raw, _ := json.Marshal(snapshot)
		_, err = tx.Exec(`INSERT INTO agent_workflow_versions(workflow_id,version_id,definition) VALUES(?,?,?) ON CONFLICT(workflow_id,version_id) DO NOTHING`, id, hash, raw)
		if err != nil {
			return d, err
		}
		d.EnabledVersion = hash
	} else {
		d.EnabledVersion = ""
	}
	d.UpdatedAt = now()
	if err = awSaveDefinition(tx, d); err != nil {
		return d, err
	}
	if err = tx.Commit(); err != nil {
		return d, err
	}
	return d, nil
}
func (s *Store) AWVersion(owner, agent, id, version string) (af.Definition, error) {
	if _, err := awGet(s.db, owner, agent, id); err != nil {
		return af.Definition{}, err
	}
	return awDecodeDefinition(s.db.QueryRow(`SELECT definition FROM agent_workflow_versions WHERE workflow_id=? AND version_id=?`, id, version))
}

func awDecodeRun(row rowScanner) (af.Run, error) {
	var b []byte
	var r af.Run
	if err := row.Scan(&b); err != nil {
		if err == sql.ErrNoRows {
			return r, ErrNotFound
		}
		return r, err
	}
	if json.Unmarshal(b, &r) != nil {
		return r, ErrConflict
	}
	return r, nil
}
func awRun(q queryer, owner, agent, wid, rid string) (af.Run, error) {
	return awDecodeRun(q.QueryRow(`SELECT record FROM agent_workflow_runs WHERE id=? AND owner_id=? AND agent_id=? AND workflow_id=?`, rid, owner, agent, wid))
}
func awSaveRun(tx *sql.Tx, r af.Run) error {
	b, e := json.Marshal(r)
	if e != nil || len(b) > 8<<20 {
		return ErrConflict
	}
	_, e = tx.Exec(`UPDATE agent_workflow_runs SET record=? WHERE id=? AND owner_id=? AND agent_id=? AND workflow_id=?`, b, r.ID, r.OwnerID, r.AgentID, r.WorkflowID)
	return e
}
func awRequest(r af.Run, trigger, key string) Request {
	payload, _ := json.Marshal(map[string]string{"run_id": r.ID})
	return Request{TaskID: "agent-workflow-run:" + r.ID, Source: "agent_workflow", SourceID: r.WorkflowID, Trigger: trigger, TriggerID: key, DefinitionHash: r.VersionID, OwnerID: r.OwnerID, AgentID: r.AgentID, WorkflowID: r.WorkflowID, WorkflowRunID: r.ID, Title: r.Definition.Name, Payload: payload}
}
func awLink(tx *sql.Tx, rid, eid string) error {
	_, err := tx.Exec(`INSERT INTO agent_workflow_executions(run_id,execution_id) VALUES(?,?)`, rid, eid)
	return err
}

func normalizeAWRun(r af.Run) (af.Run, error) {
	if awScope(r.OwnerID, r.AgentID) != nil || !validText(r.ID, 128, true) || !validText(r.WorkflowID, 256, true) || !validText(r.VersionID, 256, true) || r.Definition.ID != r.WorkflowID || r.Definition.OwnerID != r.OwnerID || r.Definition.AgentID != r.AgentID || r.Definition.DefinitionHash != af.Hash(r.Definition) || r.VersionID != r.Definition.DefinitionHash || len(af.Validate(r.Definition)) > 0 {
		return r, ErrConflict
	}
	if !validText(r.AgentFingerprint, 256, true) || len(r.AgentSnapshot) > 1<<20 || !json.Valid(r.AgentSnapshot) || !validText(r.SourceSessionID, 256, false) || !validText(r.RetryOf, 128, false) || len(r.Files) > 32 || len(r.Inputs) > 32 {
		return r, ErrConflict
	}
	if !af.ValidRunResources(r.Definition, r.Resources) {
		return r, ErrConflict
	}
	if r.NodeTest != nil && (!r.Trial || r.NodeTest.StepID == "" || len(r.Files) > 0 || !awValidNodeTest(r)) {
		return r, ErrConflict
	}
	normalizedInputs, inputErr := af.NormalizeInputs(r.Definition, r.Inputs)
	if r.NodeTest != nil {
		normalizedInputs, inputErr = map[string]string{}, nil
	}
	if inputErr != nil {
		return r, ErrConflict
	}
	r.Inputs = normalizedInputs
	declared := map[string]bool{}
	for _, i := range r.Definition.Inputs {
		declared[i.Key] = true
	}
	for k, v := range r.Inputs {
		if !declared[k] || !validText(v, 64<<10, false) {
			return r, ErrConflict
		}
	}
	copyInputs := map[string]string{}
	for _, i := range r.Definition.Inputs {
		v := r.Inputs[i.Key]
		if r.NodeTest == nil && i.Required && strings.TrimSpace(v) == "" {
			return r, ErrConflict
		}
		copyInputs[i.Key] = v
	}
	r.Inputs = copyInputs
	for _, f := range r.Files {
		if !validText(f.Source, 4096, true) || !af.SafePath(f.Path) || !af.ValidHash(f.SHA256) {
			return r, ErrConflict
		}
	}
	r.Status = af.Queued
	r.Error = ""
	r.Summary = ""
	r.CurrentStep = 0
	r.ExecutionSessionID = ""
	r.CancelRequested = false
	r.Revision = 1
	r.CreatedAt = now()
	r.UpdatedAt = r.CreatedAt
	r.Steps = []af.StepRun{}
	for _, s := range r.Definition.Steps {
		status := af.Queued
		if r.NodeTest != nil && s.ID != r.NodeTest.StepID {
			status = af.Skipped
		}
		r.Steps = append(r.Steps, af.StepRun{StepID: s.ID, Status: status})
	}
	return r, nil
}

func awAdmissionFingerprint(r af.Run) string {
	r.ID = ""
	r.CreatedAt = 0
	r.UpdatedAt = 0
	r.Workspace = ""
	// The host's stable capability fingerprint covers all executable Agent
	// fields. Its full snapshot also contains harmless presentation metadata
	// such as conversation counts that may change after the first admission.
	r.AgentSnapshot = nil
	raw, _ := json.Marshal(r)
	return digest(raw, nil)
}

func (s *Store) AWAdmit(r af.Run, key string) (af.Run, Run, bool, error) {
	var err error
	r, err = normalizeAWRun(r)
	if err != nil || !validText(key, 128, true) {
		return r, Run{}, false, ErrConflict
	}
	fingerprint := awAdmissionFingerprint(r)
	tx, err := s.db.Begin()
	if err != nil {
		return r, Run{}, false, err
	}
	defer tx.Rollback()
	var rid, eid, oldFingerprint string
	err = tx.QueryRow(`SELECT run_id,execution_id,fingerprint FROM agent_workflow_admissions WHERE owner_id=? AND agent_id=? AND workflow_id=? AND request_key=?`, r.OwnerID, r.AgentID, r.WorkflowID, key).Scan(&rid, &eid, &oldFingerprint)
	if err == nil {
		if oldFingerprint != fingerprint {
			return r, Run{}, false, ErrConflict
		}
		old, e := awRun(tx, r.OwnerID, r.AgentID, r.WorkflowID, rid)
		if e != nil {
			return r, Run{}, false, e
		}
		q, e := get(tx, eid)
		return old, q.Run, true, e
	}
	if err != sql.ErrNoRows {
		return r, Run{}, false, err
	}
	d, err := awGet(tx, r.OwnerID, r.AgentID, r.WorkflowID)
	if err != nil {
		return r, Run{}, false, err
	}
	if d.Archived || d.Deleted {
		return r, Run{}, false, ErrConflict
	}
	if r.RetryOf != "" {
		old, e := awRun(tx, r.OwnerID, r.AgentID, r.WorkflowID, r.RetryOf)
		if e != nil {
			return r, Run{}, false, e
		}
		oldInputs, _ := json.Marshal(old.Inputs)
		newInputs, _ := json.Marshal(r.Inputs)
		oldFiles, _ := json.Marshal(old.Files)
		newFiles, _ := json.Marshal(r.Files)
		oldResources, _ := json.Marshal(old.Resources)
		newResources, _ := json.Marshal(r.Resources)
		oldTest, _ := json.Marshal(old.NodeTest)
		newTest, _ := json.Marshal(r.NodeTest)
		if (old.Status != af.Failed && old.Status != af.Cancelled && old.Status != af.Interrupted) ||
			r.VersionID != old.VersionID || af.Hash(r.Definition) != af.Hash(old.Definition) || r.Trial != old.Trial ||
			r.AgentFingerprint != old.AgentFingerprint || !bytes.Equal(old.AgentSnapshot, r.AgentSnapshot) ||
			!bytes.Equal(oldInputs, newInputs) || !bytes.Equal(oldFiles, newFiles) || !bytes.Equal(oldResources, newResources) || !bytes.Equal(oldTest, newTest) {
			return r, Run{}, false, ErrConflict
		}
	} else if r.Trial {
		if d.Revision != r.Definition.Revision || d.DefinitionHash != r.VersionID {
			return r, Run{}, false, ErrConflict
		}
	} else {
		if d.EnabledVersion != r.VersionID {
			return r, Run{}, false, ErrConflict
		}
		version, e := awDecodeDefinition(tx.QueryRow(`SELECT definition FROM agent_workflow_versions WHERE workflow_id=? AND version_id=?`, r.WorkflowID, r.VersionID))
		if e != nil || version.DefinitionHash != r.VersionID {
			return r, Run{}, false, ErrConflict
		}
	}
	raw, _ := json.Marshal(r)
	_, err = tx.Exec(`INSERT INTO agent_workflow_runs(id,owner_id,agent_id,workflow_id,created_at,record) VALUES(?,?,?,?,?,?)`, r.ID, r.OwnerID, r.AgentID, r.WorkflowID, r.CreatedAt, raw)
	if err != nil {
		return r, Run{}, false, err
	}
	execution, _, err := enqueueTx(tx, awRequest(r, "manual", "start"))
	if err != nil {
		return r, Run{}, false, err
	}
	if err = awLink(tx, r.ID, execution.ID); err != nil {
		return r, Run{}, false, err
	}
	_, err = tx.Exec(`INSERT INTO agent_workflow_admissions(owner_id,agent_id,workflow_id,request_key,fingerprint,run_id,execution_id) VALUES(?,?,?,?,?,?,?)`, r.OwnerID, r.AgentID, r.WorkflowID, key, fingerprint, r.ID, execution.ID)
	if err != nil {
		return r, Run{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return r, Run{}, false, err
	}
	return r, execution, false, nil
}

func (s *Store) AWRun(owner, agent, wid, rid string) (af.Run, error) {
	if err := awScope(owner, agent); err != nil {
		return af.Run{}, err
	}
	return awRun(s.db, owner, agent, wid, rid)
}
func (s *Store) AWRuns(owner, agent, wid string) ([]af.Run, error) {
	if err := awScope(owner, agent); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT record FROM agent_workflow_runs WHERE owner_id=? AND agent_id=? AND workflow_id=? ORDER BY created_at DESC,id DESC`, owner, agent, wid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []af.Run{}
	for rows.Next() {
		r, e := awDecodeRun(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) AWExecutions(owner, agent, wid, rid string) ([]Run, error) {
	if _, err := s.AWRun(owner, agent, wid, rid); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT `+prefixedColumns("q")+` FROM taskqueue_runs q JOIN agent_workflow_executions e ON e.execution_id=q.id WHERE e.run_id=? ORDER BY q.created_at,q.id`, rid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, e := decodeRow(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, r.Run)
	}
	return out, rows.Err()
}

func awImmutable(r af.Run) []byte {
	r.ExecutionSessionID = ""
	r.Workspace = ""
	r.Status = ""
	r.Error = ""
	r.Summary = ""
	r.Steps = nil
	r.CurrentStep = 0
	r.CancelRequested = false
	r.Revision = 0
	r.UpdatedAt = 0
	raw, _ := json.Marshal(r)
	return raw
}
func awTerminal(status string) bool {
	return status == af.Completed || status == af.Failed || status == af.Cancelled || status == af.Interrupted
}

// AWUpdate is fenced by both the logical record revision and the currently
// running dispatcher claim. A pre-restart or superseded worker cannot commit.
func (s *Store) AWUpdate(r af.Run, expectedRevision int64, executionID string, claimRevision int64) (af.Run, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	old, err := awRun(tx, r.OwnerID, r.AgentID, r.WorkflowID, r.ID)
	if err != nil {
		return r, err
	}
	if old.Definition.Graph != nil || old.NodeTest != nil {
		return r, ErrConflict
	}
	execution, err := get(tx, executionID)
	if err != nil {
		return r, err
	}
	if execution.Status != Running || execution.Revision != claimRevision || execution.WorkflowRunID != r.ID || execution.OwnerID != r.OwnerID || execution.AgentID != r.AgentID || execution.Source != "agent_workflow" || old.Revision != expectedRevision || old.Revision == math.MaxInt64 || !bytes.Equal(awImmutable(old), awImmutable(r)) {
		return r, ErrConflict
	}
	if awTerminal(old.Status) {
		return r, ErrConflict
	}
	if old.ExecutionSessionID != "" && old.ExecutionSessionID != r.ExecutionSessionID {
		return r, ErrConflict
	}
	if old.Workspace != "" && old.Workspace != r.Workspace {
		return r, ErrConflict
	}
	if r.CurrentStep < old.CurrentStep || r.CurrentStep > len(r.Definition.Steps) || len(r.Steps) != len(r.Definition.Steps) || !validText(r.Error, 64<<10, false) || !validText(r.Summary, 128<<10, false) {
		return r, ErrConflict
	}
	switch r.Status {
	case af.Running, af.WaitingConfirmation, af.Completed, af.Failed, af.Cancelled, af.Interrupted:
	default:
		return r, ErrConflict
	}
	if r.Status == af.WaitingConfirmation && (r.CurrentStep >= len(r.Definition.Steps) || r.Definition.Steps[r.CurrentStep].Kind != "confirm") {
		return r, ErrConflict
	}
	if r.Status == af.Completed && r.CurrentStep != len(r.Definition.Steps) {
		return r, ErrConflict
	}
	for i, sr := range r.Steps {
		if !awValidCodeReceipt(r.Definition.Steps[i], sr) || !awValidReceiptTransition(r, i, old.Steps[i], sr) {
			return r, ErrConflict
		}
		if sr.StepID != r.Definition.Steps[i].ID || !validText(sr.Summary, 128<<10, false) || !validText(sr.Feedback, 64<<10, false) ||
			(sr.Artifact != "" && (!af.SafePath(sr.Artifact) || !af.ValidHash(sr.ArtifactSHA256))) || (sr.Artifact == "" && sr.ArtifactSHA256 != "") {
			return r, ErrConflict
		}
		switch sr.Status {
		case af.Queued, af.Running, af.WaitingConfirmation, af.Completed, af.Failed, af.Cancelled, af.Interrupted:
		default:
			return r, ErrConflict
		}
		if (i < r.CurrentStep && sr.Status != af.Completed) || (i > r.CurrentStep && sr.Status != af.Queued) ||
			(sr.Status == af.WaitingConfirmation && (r.Definition.Steps[i].Kind != "confirm" || i != r.CurrentStep)) {
			return r, ErrConflict
		}
		if old.Steps[i].Status == af.Completed {
			a, _ := json.Marshal(old.Steps[i])
			b, _ := json.Marshal(sr)
			if !bytes.Equal(a, b) {
				return r, ErrConflict
			}
		}
	}
	r.CancelRequested = old.CancelRequested || execution.CancelRequested
	if r.ExecutionSessionID == "" {
		for _, step := range r.Steps {
			if step.Repeat != nil {
				for _, item := range step.Repeat.Items {
					if item.SessionID != "" {
						r.ExecutionSessionID = item.SessionID
						break
					}
				}
			}
			if r.ExecutionSessionID != "" {
				break
			}
		}
	}
	if r.CancelRequested && r.Status == af.Completed {
		r.Status, r.Error = af.Cancelled, "已取消；已完成的节点结果保留"
	}
	r.Revision = old.Revision + 1
	r.UpdatedAt = now()
	if err = awSaveRun(tx, r); err != nil {
		return r, err
	}
	if err = tx.Commit(); err != nil {
		return r, err
	}
	return r, nil
}

// AWConfirm accepts a decision once for the current wait, persists feedback,
// advances the step and enqueues its continuation in one durable transaction.
func (s *Store) AWConfirm(owner, agent, wid, rid, key, feedback string, expectedStepID ...string) (af.Run, Run, bool, error) {
	if !validText(key, 128, true) || !validText(feedback, 64<<10, false) || len(expectedStepID) > 1 || len(expectedStepID) == 1 && !af.ValidID(expectedStepID[0]) {
		return af.Run{}, Run{}, false, ErrConflict
	}
	tx, err := s.db.Begin()
	if err != nil {
		return af.Run{}, Run{}, false, err
	}
	defer tx.Rollback()
	r, err := awRun(tx, owner, agent, wid, rid)
	if err != nil {
		return r, Run{}, false, err
	}
	if r.Definition.Graph != nil {
		return s.awGraphConfirmTx(tx, r, key, feedback, expectedStepID)
	}
	fingerprint := digest([]byte(feedback), nil)
	var priorFingerprint, eid, priorStep string
	err = tx.QueryRow(`SELECT fingerprint,execution_id,step_id FROM agent_workflow_commands WHERE run_id=? AND command_key=?`, rid, key).Scan(&priorFingerprint, &eid, &priorStep)
	if err == nil {
		if priorFingerprint != fingerprint || len(expectedStepID) == 1 && expectedStepID[0] != priorStep {
			return r, Run{}, false, ErrConflict
		}
		q, e := get(tx, eid)
		return r, q.Run, true, e
	}
	if err != sql.ErrNoRows {
		return r, Run{}, false, err
	}
	if r.Status != af.WaitingConfirmation || r.CancelRequested || r.CurrentStep < 0 || r.CurrentStep >= len(r.Steps) || r.Definition.Steps[r.CurrentStep].Kind != "confirm" || r.Revision == math.MaxInt64 {
		return r, Run{}, false, ErrConflict
	}
	stepID := r.Steps[r.CurrentStep].StepID
	if len(expectedStepID) == 1 && expectedStepID[0] != stepID {
		return r, Run{}, false, ErrConflict
	}
	var count int
	if err = tx.QueryRow(`SELECT count(*) FROM agent_workflow_commands WHERE run_id=? AND step_id=?`, rid, stepID).Scan(&count); err != nil {
		return r, Run{}, false, err
	}
	if count != 0 {
		return r, Run{}, false, ErrConflict
	}
	// The previous worker may still be finishing its completed segment. The
	// queue's per-task running constraint keeps this continuation unclaimable
	// until that slot is released; no synchronous wait occurs here.
	r.Steps[r.CurrentStep].Status = af.Completed
	r.Steps[r.CurrentStep].Feedback = feedback
	r.Steps[r.CurrentStep].FinishedAt = now()
	r.CurrentStep++
	r.Status = af.Queued
	r.Error = ""
	r.Revision++
	r.UpdatedAt = now()
	q, _, err := enqueueTx(tx, awRequest(r, "confirmation", "confirm:"+stepID))
	if err != nil {
		return r, Run{}, false, err
	}
	if err = awLink(tx, rid, q.ID); err != nil {
		return r, Run{}, false, err
	}
	_, err = tx.Exec(`INSERT INTO agent_workflow_commands(run_id,step_id,command_key,fingerprint,execution_id) VALUES(?,?,?,?,?)`, rid, stepID, key, fingerprint, q.ID)
	if err != nil {
		return r, Run{}, false, err
	}
	if err = awSaveRun(tx, r); err != nil {
		return r, Run{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return r, Run{}, false, err
	}
	return r, q, false, nil
}

func (s *Store) AWCancel(owner, agent, wid, rid string) (af.Run, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return af.Run{}, err
	}
	defer tx.Rollback()
	r, err := awRun(tx, owner, agent, wid, rid)
	if err != nil {
		return r, err
	}
	if awTerminal(r.Status) {
		if r.Status == af.Cancelled {
			return r, nil
		}
		return r, ErrConflict
	}
	if r.Revision == math.MaxInt64 {
		return r, ErrConflict
	}
	rows, err := tx.Query(`SELECT `+prefixedColumns("q")+` FROM taskqueue_runs q JOIN agent_workflow_executions e ON e.execution_id=q.id WHERE e.run_id=?`, rid)
	if err != nil {
		return r, err
	}
	executions := []storedRun{}
	for rows.Next() {
		q, e := decodeRow(rows)
		if e != nil {
			rows.Close()
			return r, e
		}
		executions = append(executions, q)
	}
	if err = rows.Close(); err != nil {
		return r, err
	}
	active := false
	for _, q := range executions {
		if q.Status == Running {
			active = true
			_, err = tx.Exec(`UPDATE taskqueue_runs SET cancel_requested=1 WHERE id=? AND status='running'`, q.ID)
		} else if q.Status == Queued {
			_, err = tx.Exec(`UPDATE taskqueue_runs SET status='cancelled',cancel_requested=1,revision=revision+1,finished_at=?,end_reason='cancelled_before_start' WHERE id=? AND status='queued'`, now(), q.ID)
		}
		if err != nil {
			return r, err
		}
	}
	r.CancelRequested = true
	// Like the queue cancellation intent, an active logical Run keeps its
	// execution revision so the actual worker can still settle its receipt.
	// A persisted confirmation is already a safe boundary, even if its old
	// dispatcher segment has not yet returned from its short finish epilogue.
	stopNow := !active || r.Status == af.WaitingConfirmation || r.Status == af.Queued
	if stopNow {
		r.Revision++
	}
	r.UpdatedAt = now()
	if stopNow {
		r.Status = af.Cancelled
	}
	if err = awSaveRun(tx, r); err != nil {
		return r, err
	}
	if err = tx.Commit(); err != nil {
		return r, err
	}
	return r, nil
}

// AWRecover follows Store.Recover under the exclusive controller lease. Only
// unknown executing work is interrupted; persisted human waits are untouched.
func (s *Store) AWRecover() error {
	if err := s.awGraphRecover(); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT r.record FROM agent_workflow_runs r WHERE EXISTS(SELECT 1 FROM agent_workflow_executions e JOIN taskqueue_runs q ON q.id=e.execution_id WHERE e.run_id=r.id AND q.status='interrupted') AND NOT EXISTS(SELECT 1 FROM agent_workflow_executions e JOIN taskqueue_runs q ON q.id=e.execution_id WHERE e.run_id=r.id AND q.status IN ('queued','running'))`)
	if err != nil {
		return err
	}
	runs := []af.Run{}
	for rows.Next() {
		r, e := awDecodeRun(rows)
		if e != nil {
			rows.Close()
			return e
		}
		runs = append(runs, r)
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, r := range runs {
		if r.Status != af.Running && r.Status != af.Queued {
			continue
		}
		r.Status = af.Interrupted
		r.Error = "执行期间服务中断，外部操作结果待核查；未自动重跑"
		if r.CurrentStep < len(r.Steps) && r.Steps[r.CurrentStep].Status == af.Running {
			r.Steps[r.CurrentStep].Status = af.Interrupted
			r.Steps[r.CurrentStep].FinishedAt = now()
			awStopRepeatItems(&r.Steps[r.CurrentStep], af.Interrupted, r.Error)
		}
		r.Revision++
		r.UpdatedAt = now()
		if err = awSaveRun(tx, r); err != nil {
			return err
		}
	}
	return tx.Commit()
}
