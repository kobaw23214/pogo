package web

// The workflow service admits logical runs and human commands through the
// persistent dispatcher. A queue row is an execution segment; run.json remains
// the authority for node state. Legacy files are indexed without rewriting them.
import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/schedule"
	"github.com/yosukeno/pogo/taskqueue"
	"github.com/yosukeno/pogo/workflow"
)

type workflowSegment struct {
	Decision *workflow.HumanDecision `json:"decision,omitempty"`
}

func workflowMeta(rec workflow.RunRecord, source, sourceID string) taskqueue.WorkflowRunMeta {
	definition, _ := json.Marshal(rec.Definition)
	owner, _ := json.Marshal(rec.Owner)
	created, _ := time.Parse(time.RFC3339Nano, rec.CreatedAt)
	createdAt := int64(0)
	if !created.IsZero() && created.UnixMilli() >= 0 {
		createdAt = created.UnixMilli()
	}
	principal := sessionResultOwnerKey(rec.Owner)
	meta := taskqueue.WorkflowRunMeta{ID: rec.ID, SpaceID: taskqueue.DefaultSpaceID,
		WorkflowID: rec.WorkflowID, OwnerID: principal, Source: source, SourceID: sourceID,
		InitiatorID: principal, ExecutionPrincipalID: principal,
		Definition: definition, Owner: owner, CreatedAt: createdAt, Inputs: rec.Inputs}
	meta.TestRun, meta.DraftRevision, meta.DefinitionHash = rec.TestRun, rec.DraftRevision, rec.DefinitionHash
	if rec.Owner == nil && source == "legacy" {
		meta.InitiatorID, meta.ExecutionPrincipalID = "legacy/system", "legacy/system"
	}
	return meta
}

func workflowOrigin(meta taskqueue.WorkflowRunMeta) *workflow.RunOrigin {
	if meta.PublicationID == "" {
		return nil
	}
	return &workflow.RunOrigin{Source: meta.Source, SourceID: meta.SourceID, PublicationID: meta.PublicationID, IntegrationID: meta.IntegrationID, ExternalSubject: meta.ExternalSubject, BusinessID: meta.BusinessID, VersionID: meta.VersionID, RetryOf: meta.RetryOf}
}

func workflowMetaRecord(meta taskqueue.WorkflowRunMeta) (workflow.RunRecord, error) {
	var def workflow.Definition
	if err := json.Unmarshal(meta.Definition, &def); err != nil {
		return workflow.RunRecord{}, err
	}
	var owner *identity.Scope
	if err := json.Unmarshal(meta.Owner, &owner); err != nil {
		return workflow.RunRecord{}, err
	}
	status := "queued"
	if meta.CancelRequested {
		status = workflow.RunCancelled
	}
	return workflow.RunRecord{ID: meta.ID, WorkflowID: meta.WorkflowID, Revision: def.Revision,
		Definition: def, Inputs: meta.Inputs, Origin: workflowOrigin(meta), Status: status, Owner: owner, CreatedAt: time.UnixMilli(meta.CreatedAt).UTC().Format(time.RFC3339Nano),
		Nodes: map[string]workflow.NodeState{}, TestRun: meta.TestRun, DraftRevision: meta.DraftRevision, DefinitionHash: meta.DefinitionHash}, nil
}

func (m *Manager) importWorkflowHistory(db *taskqueue.Store) error {
	entries, err := os.ReadDir(m.wfruns.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validID(entry.Name()) {
			continue
		}
		rec, err := readWFRecord(m.wfruns.recordPath(entry.Name()))
		if err != nil {
			continue
		} // Damaged files are preserved for operator review.
		if rec.ID != entry.Name() || !validID(rec.ID) || !validID(rec.WorkflowID) {
			continue
		}
		if _, err := db.GetWorkflowRun(rec.ID); err == nil {
			continue
		} else if !errors.Is(err, taskqueue.ErrNotFound) {
			return err
		}
		meta := workflowMeta(rec, "legacy", "")
		if rec.Owner == nil {
			meta.InitiatorID, meta.ExecutionPrincipalID = "legacy/system", "legacy/system"
		}
		if err := db.ImportWorkflowRun(meta); errors.Is(err, taskqueue.ErrConflict) {
			continue // Unsupported legacy metadata never destroys or blocks its files.
		} else if err != nil {
			return err
		}
	}
	return nil
}

type workflowAdmissionOptions struct {
	InitiatorID       string
	VersionID         string
	Inputs            map[string]string
	PublicationID     string
	IntegrationID     string
	ExternalSubject   string
	BusinessID        string
	Callback          *taskqueue.WorkflowCallbackTarget
	BusinessApprovers map[string][]string
	TestRun           bool
	DraftRevision     int
	DefinitionHash    string
}

func (m *Manager) admitWorkflow(def workflow.Definition, owner *identity.Scope, source, sourceID, key, retryOf string, initiator ...string) (taskqueue.WorkflowRunMeta, error) {
	options := workflowAdmissionOptions{}
	if len(initiator) > 0 {
		options.InitiatorID = initiator[0]
	}
	return m.admitWorkflowWithOptions(def, owner, source, sourceID, key, retryOf, options)
}

func (m *Manager) admitWorkflowWithOptions(def workflow.Definition, owner *identity.Scope, source, sourceID, key, retryOf string, options workflowAdmissionOptions) (taskqueue.WorkflowRunMeta, error) {
	if _, ok := m.workflows.get(def.ID); !ok {
		return taskqueue.WorkflowRunMeta{}, ErrNotFound
	}
	def.Normalize()
	if err := def.Validate(); err != nil {
		return taskqueue.WorkflowRunMeta{}, err
	}
	if key == "" {
		key = rand.Text()
	}
	if len(key) > 256 {
		return taskqueue.WorkflowRunMeta{}, errors.New("运行请求标识过长")
	}
	rec := workflow.RunRecord{ID: "run_" + rand.Text(), WorkflowID: def.ID, Revision: def.Revision,
		Definition: def, Owner: owner, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	meta := workflowMeta(rec, source, sourceID)
	meta.RetryOf = retryOf
	if options.InitiatorID != "" {
		meta.InitiatorID = options.InitiatorID
	}
	meta.Inputs = options.Inputs
	meta.TestRun, meta.DraftRevision, meta.DefinitionHash = options.TestRun, options.DraftRevision, options.DefinitionHash
	meta.Callback = options.Callback
	meta.PublicationID, meta.IntegrationID, meta.ExternalSubject, meta.BusinessID = options.PublicationID, options.IntegrationID, options.ExternalSubject, options.BusinessID
	if source == "schedule" {
		meta.InitiatorID, meta.ExecutionPrincipalID = "service:workflow-scheduler", "service:workflow-scheduler"
	}
	meta.VersionID = options.VersionID
	if meta.VersionID == "" {
		meta.VersionID = m.workflowVersionForDefinition(def)
	}
	db, err := m.taskRuns.database(true)
	if err != nil {
		return meta, err
	}
	var previousApprovals map[string]taskqueue.WorkflowApprovalAssignment
	if retryOf != "" {
		if previous, err := db.GetWorkflowRun(retryOf); err == nil {
			meta.VersionID = previous.VersionID
			meta.ExecutionPrincipalID = previous.ExecutionPrincipalID
			meta.PublicationID, meta.IntegrationID, meta.ExternalSubject, meta.BusinessID = previous.PublicationID, previous.IntegrationID, previous.ExternalSubject, previous.BusinessID
			meta.Inputs = previous.Inputs
			meta.TestRun, meta.DraftRevision, meta.DefinitionHash = previous.TestRun, previous.DraftRevision, previous.DefinitionHash
			meta.Callback = previous.Callback
			previousApprovals = previous.Approvals
		}
	}
	if err := m.prepareWorkflowApprovalAssignments(def, &meta, key, options.BusinessApprovers, previousApprovals); err != nil {
		return meta, err
	}
	payload, _ := json.Marshal(workflowSegment{})
	req := taskqueue.Request{Source: "workflow", SourceID: def.ID, Trigger: source,
		TriggerID: "start", DefinitionHash: meta.VersionID, Title: def.Name, Payload: payload}
	if req.DefinitionHash == "" {
		req.DefinitionHash = fmt.Sprintf("revision:%d", def.Revision)
	}
	meta, _, _, err = db.AdmitWorkflow(meta, key, req)
	if err == nil {
		m.taskRuns.wake()
	}
	return meta, err
}

// enqueueWorkflowDecision atomically accepts one outcome for a particular wait.
// The deadline identifies the wait generation, so multiple people and the
// timeout sweep cannot accept conflicting decisions for the same node.
func (m *Manager) enqueueWorkflowDecision(rec workflow.RunRecord, decision workflow.HumanDecision) error {
	// Callers may hold a redacted or stale HTTP projection. Re-read authority
	// before evaluating the form or the actor that will enter the command log.
	if current, ok := m.wfruns.snapshot(rec.ID); ok && current.WorkflowID == rec.WorkflowID {
		rec = current
	} else {
		return ErrNotFound
	}
	if rec.Status != workflow.RunWorking {
		return errWFRunNotSuspended
	}
	st, ok := rec.Nodes[decision.Node]
	if !ok || st.Status != workflow.NodeWaitingHuman || st.Human == nil {
		return errWFNodeNotWaiting
	}
	if !decision.Timeout {
		if err := validateHumanDecision(rec.Definition.Node(decision.Node), decision); err != nil {
			return err
		}
	}
	db, err := m.taskRuns.database(true)
	if err != nil {
		return err
	}
	meta, err := db.GetWorkflowRun(rec.ID)
	if errors.Is(err, taskqueue.ErrNotFound) {
		if err := db.ImportWorkflowRun(workflowMeta(rec, "legacy", "")); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	_, protected := meta.Approvals[decision.Node]
	if !decision.Timeout && (protected || workflowApprovalPolicy(rec, decision.Node) != nil) {
		m.taskRuns.mu.Lock()
		authorize := m.taskRuns.workflowApprovalDecisionAuthorize
		m.taskRuns.mu.Unlock()
		if authorize == nil {
			return errors.New("审批授权尚未就绪")
		}
		if err := authorize(rec, decision); err != nil {
			return err
		}
	}
	decision.CommandID = "human:" + decision.Node + ":" + st.Human.Deadline
	// Actor and timestamp are audit metadata; the command's outcome is stable
	// across HTTP retries, but a different decision conflicts in the store.
	decision.DecidedAt = ""
	payload, err := json.Marshal(workflowSegment{Decision: &decision})
	if err != nil {
		return err
	}
	req := taskqueue.Request{Source: "workflow", SourceID: rec.WorkflowID, Trigger: "resume",
		TriggerID: decision.CommandID, DefinitionHash: fmt.Sprintf("revision:%d", rec.Revision), Title: rec.Definition.Name, Payload: payload}
	_, _, _, err = db.WorkflowCommand(rec.ID, decision.CommandID, payload, req)
	if err == nil {
		m.taskRuns.wake()
	}
	return err
}

func (m *Manager) enqueueWorkflowRecovery(rec workflow.RunRecord) error {
	db, err := m.taskRuns.database(true)
	if err != nil {
		return err
	}
	if _, err := db.GetWorkflowRun(rec.ID); errors.Is(err, taskqueue.ErrNotFound) {
		if err := db.ImportWorkflowRun(workflowMeta(rec, "legacy", "")); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if segments, err := db.ListWorkflowExecutions(rec.ID); err != nil {
		return err
	} else {
		for _, segment := range segments {
			if segment.Status == taskqueue.Queued || segment.Status == taskqueue.Running {
				return nil
			}
		}
	}
	payload, _ := json.Marshal(workflowSegment{})
	_, _, err = db.EnqueueWorkflow(rec.ID, taskqueue.Request{Source: "workflow", SourceID: rec.WorkflowID,
		Trigger: "recovery", TriggerID: fmt.Sprintf("recovery:%d", rec.StateRevision),
		DefinitionHash: fmt.Sprintf("revision:%d", rec.Revision), Title: rec.Definition.Name, Payload: payload})
	if err == nil {
		m.taskRuns.wake()
	}
	return err
}

func (r *taskRunRuntime) executeWorkflow(ctx context.Context, execution taskqueue.Run) taskqueue.Outcome {
	db, err := r.database(false)
	if err != nil || db == nil {
		return taskqueue.Outcome{Status: taskqueue.Failed, Error: "运行目录不可用"}
	}
	meta, err := db.GetWorkflowRun(execution.WorkflowRunID)
	if err != nil {
		return taskqueue.Outcome{Status: taskqueue.Failed, Error: "运行受理记录不存在"}
	}
	var segment workflowSegment
	if err := json.Unmarshal(execution.Payload, &segment); err != nil {
		return taskqueue.Outcome{Status: taskqueue.Failed, Error: "执行段格式无效"}
	}
	return r.m.executeWorkflowSegment(ctx, meta, execution.ID, segment.Decision)
}

func (m *Manager) executeWorkflowSegment(ctx context.Context, meta taskqueue.WorkflowRunMeta, executionID string, decision *workflow.HumanDecision) taskqueue.Outcome {
	out := taskqueue.Outcome{SessionID: meta.ID}
	m.wfruns.controlMu.Lock()
	locked := true
	defer func() {
		if locked {
			m.wfruns.controlMu.Unlock()
		}
	}()
	m.wfruns.waitUnmapped(meta.ID, 2*time.Second)
	m.wfruns.mu.Lock()
	_, stillActive := m.wfruns.runs[meta.ID]
	m.wfruns.mu.Unlock()
	if stillActive {
		out.EndReason = "not_started"
		return out
	}
	db, err := m.taskRuns.database(false)
	if err != nil || db == nil {
		out.Status, out.Error = taskqueue.Failed, "运行目录不可用"
		return out
	}
	fresh, err := db.GetWorkflowRun(meta.ID)
	if err != nil {
		out.Status, out.Error = taskqueue.Failed, "运行受理记录不存在"
		return out
	}
	rec, err := readWFRecord(m.wfruns.recordPath(meta.ID))
	if errors.Is(err, os.ErrNotExist) {
		rec, err = workflowMetaRecord(meta)
	}
	if err != nil {
		out.Status, out.Error = taskqueue.Failed, "运行状态不可读，未自动重跑"
		return out
	}
	if workflow.IsTerminal(rec.Status) {
		return workflowSegmentOutcome(rec)
	}
	if fresh.CancelRequested {
		if _, err := os.Stat(m.wfruns.recordPath(meta.ID)); errors.Is(err, os.ErrNotExist) {
			rec.Status, rec.EndedAt = workflow.RunCancelled, time.Now().UTC().Format(time.RFC3339Nano)
			if err := os.MkdirAll(m.wfruns.runDir(meta.ID), 0700); err != nil {
				out.Status, out.Error = taskqueue.Failed, err.Error()
				return out
			}
			if err := m.wfruns.writeRecord(m.wfruns.recordPath(meta.ID), rec); err != nil {
				out.Status, out.Error = taskqueue.Failed, wfPersistenceFailure
				return out
			}
		} else {
			m.wfruns.cancel(meta.ID)
		}
		out.Status, out.EndReason = taskqueue.Cancelled, workflow.RunCancelled
		return out
	}
	if ctx.Err() != nil {
		out.Status, out.Error = taskqueue.Cancelled, ctx.Err().Error()
		return out
	}
	m.taskRuns.mu.Lock()
	authorize := m.taskRuns.workflowAuthorize
	m.taskRuns.mu.Unlock()
	if authorize == nil {
		out.EndReason = "not_started"
		return out
	}
	if authorize != nil && !authorize(meta) {
		rec.Status, rec.Error, rec.EndedAt = workflow.RunFailed, "执行身份或工作流权限已失效", time.Now().UTC().Format(time.RFC3339Nano)
		if err := os.MkdirAll(m.wfruns.runDir(meta.ID), 0700); err != nil {
			out.Status, out.Error = taskqueue.Failed, err.Error()
			return out
		}
		if err := m.wfruns.writeRecord(m.wfruns.recordPath(meta.ID), rec); err != nil {
			out.Status, out.Error = taskqueue.Interrupted, wfPersistenceFailure
			return out
		}
		return workflowSegmentOutcome(rec)
	}
	if rec.AppliedCommandID != "" && decision != nil && rec.AppliedCommandID == decision.CommandID {
		decision = nil // A durable decision is never applied twice after recovery.
	}
	if decision != nil {
		st := rec.Nodes[decision.Node]
		if st.Status != workflow.NodeWaitingHuman {
			out.Status, out.Error = taskqueue.Failed, "人工节点状态已改变"
			return out
		}
		decision.DecidedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	// A suspended segment may still be draining its final publish. Do not run
	// two executors for the same logical run in that short window.
	if current, err := readWFRecord(m.wfruns.recordPath(meta.ID)); err == nil {
		rec = current
		if workflow.IsTerminal(rec.Status) {
			return workflowSegmentOutcome(rec)
		}
	}
	rec.ExecutionID, rec.Capabilities = executionID, nil
	rec.Inputs = meta.Inputs
	rec.Origin = workflowOrigin(meta)
	rec.TestRun, rec.DraftRevision, rec.DefinitionHash = meta.TestRun, meta.DraftRevision, meta.DefinitionHash
	workspace := filepath.Join(m.wfruns.runDir(meta.ID), "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		out.Status, out.Error = taskqueue.Failed, err.Error()
		return out
	}
	if err := m.wfruns.writeRecord(m.wfruns.recordPath(meta.ID), rec); err != nil {
		out.Status, out.Error = taskqueue.Failed, wfPersistenceFailure
		if rec.Status == workflow.RunWorking {
			rec.PersistenceError = wfPersistenceFailure
			m.wfruns.mu.Lock()
			m.wfruns.blocked[rec.ID] = rec
			m.wfruns.mu.Unlock()
			out.Status = taskqueue.Interrupted
		}
		return out
	}
	runCtx, cancel := context.WithCancel(ctx)
	run := &wfRun{id: rec.ID, wid: rec.WorkflowID, owner: rec.Owner, hub: NewHub(), ctx: runCtx, cancel: cancel,
		done: make(chan struct{}), record: rec}
	err = m.wfruns.drive(run, &workflow.Runner{Record: rec, Executor: m.wfruns.executor(run),
		Sink: &wfRunSink{rm: m.wfruns, run: run}, Workspace: workspace, Resume: decision})
	if err != nil {
		cancel()
		out.Status, out.Error = taskqueue.Failed, err.Error()
		return out
	}
	m.wfruns.controlMu.Unlock()
	locked = false
	defer cancel()
	<-run.done
	last, ok := m.wfruns.snapshot(meta.ID)
	if !ok {
		out.Status, out.Error = taskqueue.Failed, "执行结束但状态不可读"
		return out
	}
	return workflowSegmentOutcome(last)
}

func workflowSegmentOutcome(rec workflow.RunRecord) taskqueue.Outcome {
	out := taskqueue.Outcome{SessionID: rec.ID, Status: taskqueue.Completed, EndReason: rec.Status, Error: rec.Error}
	if rec.PersistenceError != "" {
		out.Status, out.Error = taskqueue.Interrupted, rec.PersistenceError
		return out
	}
	switch rec.Status {
	case workflow.RunWorking:
		out.EndReason = "waiting_human"
	case workflow.RunFailed:
		out.Status = taskqueue.Failed
	case workflow.RunCancelled:
		out.Status = taskqueue.Cancelled
	}
	return out
}

func (m *Manager) cancelWorkflowRun(id string) (bool, error) {
	m.wfruns.controlMu.Lock()
	defer m.wfruns.controlMu.Unlock()
	if rec, ok := m.wfruns.snapshot(id); ok && (workflow.IsTerminal(rec.Status) || rec.Status == taskqueue.Interrupted) {
		return rec.Status == workflow.RunCancelled, nil
	}
	db, err := m.taskRuns.database(true)
	if err != nil {
		return false, err
	}
	if _, err := db.GetWorkflowRun(id); errors.Is(err, taskqueue.ErrNotFound) {
		if rec, ok := m.wfruns.snapshot(id); ok {
			if err := db.ImportWorkflowRun(workflowMeta(rec, "legacy", "")); err != nil {
				return false, err
			}
		} else {
			return false, taskqueue.ErrNotFound
		}
	} else if err != nil {
		return false, err
	}
	if _, err := db.CancelWorkflow(id); err != nil {
		return false, err
	}
	if executions, err := db.ListWorkflowExecutions(id); err == nil {
		m.taskRuns.mu.Lock()
		for _, execution := range executions {
			if active, ok := m.taskRuns.active[execution.ID]; ok && active.cancel != nil {
				active.cancel()
			}
		}
		m.taskRuns.mu.Unlock()
	}
	_, sent := m.wfruns.cancel(id)
	if !sent {
		if rec, ok := m.wfruns.snapshot(id); ok && rec.Status == workflow.RunCancelled {
			sent = true
		}
	}
	m.taskRuns.wake()
	return sent, nil
}

func (m *Manager) runWorkflowJobQueued(ctx context.Context, job schedule.Job, def workflow.Definition) schedule.RunOutcome {
	// The normal scheduler already owns a worker. Attach its logical run in
	// place; a second admission plus a wait would deadlock a full dispatcher.
	if binding, ok := ctx.Value(taskRunContextKey{}).(taskRunBinding); ok {
		db, err := binding.runtime.database(false)
		if err != nil || db == nil {
			return schedule.RunOutcome{Err: "工作流运行目录不可用", ExitCode: 1}
		}
		rec := workflow.RunRecord{ID: "run_" + rand.Text(), WorkflowID: def.ID, Revision: def.Revision,
			Definition: def, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		meta := workflowMeta(rec, "schedule", job.ID)
		meta.InitiatorID, meta.ExecutionPrincipalID = "service:workflow-scheduler", "service:workflow-scheduler"
		meta.VersionID = m.workflowVersionForDefinition(def)
		key := "schedule:" + binding.id
		if err := m.prepareWorkflowApprovalAssignments(def, &meta, key, nil, nil); err != nil {
			return schedule.RunOutcome{Err: err.Error(), ExitCode: 1}
		}
		meta, _, err = db.BindWorkflowExecution(meta, key, binding.id)
		if err != nil {
			return schedule.RunOutcome{Err: err.Error(), ExitCode: 1}
		}
		if err := bindTaskSession(ctx, meta.ID); err != nil {
			return schedule.RunOutcome{Err: err.Error(), ExitCode: 1}
		}
		return legacyWorkflowJobOutcome(m.executeWorkflowSegment(ctx, meta, binding.id, nil))
	}
	// Explicit callers without a dispatcher slot use the same durable queue.
	meta, err := m.admitWorkflow(def, nil, "schedule", job.ID, "", "")
	if err != nil {
		return schedule.RunOutcome{Err: err.Error(), ExitCode: 1}
	}
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if rec, ok := m.wfruns.snapshot(meta.ID); ok {
			if workflow.IsTerminal(rec.Status) {
				return legacyWorkflowJobOutcome(workflowSegmentOutcome(rec))
			}
			if rec.Status == workflow.RunWorking {
				waiting, running := false, false
				for _, st := range rec.Nodes {
					waiting = waiting || st.Status == workflow.NodeWaitingHuman
					running = running || st.Status == workflow.NodeRunning
				}
				if waiting && !running {
					return legacyWorkflowJobOutcome(workflowSegmentOutcome(rec))
				}
			}
		}
		select {
		case <-ctx.Done():
			_, _ = m.cancelWorkflowRun(meta.ID)
			return schedule.RunOutcome{SessionID: meta.ID, Err: ctx.Err().Error(), ExitCode: 1}
		case <-tick.C:
		}
	}
}

func (s *Server) handleWorkflowRunRetry(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.workflowRunFor(w, r, wfRunRetry)
	if !ok {
		return
	}
	if rec.Status != workflow.RunFailed && rec.Status != workflow.RunCancelled && rec.Status != "interrupted" {
		writeErr(w, http.StatusConflict, errors.New("仅失败或取消的运行可以重新发起"))
		return
	}
	meta, err := s.mgr.admitWorkflow(rec.Definition, rec.Owner, "retry", rec.ID, r.Header.Get("Idempotency-Key"), rec.ID, s.workflowPrincipal(r).ID)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, taskqueue.ErrConflict) {
			status = http.StatusConflict
		}
		writeErr(w, status, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": meta.ID})
}

func (s *Server) bindWorkflowExecutionAuthorization() {
	s.mgr.taskRuns.mu.Lock()
	s.mgr.taskRuns.workflowApprovalResolver = s.resolveWorkflowApprovals
	s.mgr.taskRuns.workflowApprovalDecisionAuthorize = s.authorizeWorkflowApprovalDecision
	s.mgr.taskRuns.workflowAuthorize = func(meta taskqueue.WorkflowRunMeta) bool {
		if _, ok := s.mgr.workflows.get(meta.WorkflowID); !ok {
			return false
		}
		var owner *identity.Scope
		if json.Unmarshal(meta.Owner, &owner) != nil {
			return false
		}
		if meta.OwnerID != sessionResultOwnerKey(owner) {
			return false
		}
		if owner == nil {
			// Only host-created scheduled/legacy service admissions use the
			// global execution profile. This grants no browser operator access.
			allowed := meta.Source == "schedule" && meta.ExecutionPrincipalID == "service:workflow-scheduler" ||
				meta.Source == "legacy" && (meta.ExecutionPrincipalID == "legacy/system" || meta.ExecutionPrincipalID == "single-user") ||
				meta.Source == "retry" && meta.RetryOf != "" && (meta.ExecutionPrincipalID == "service:workflow-scheduler" || meta.ExecutionPrincipalID == "legacy/system") ||
				!s.multiuser() && meta.OwnerID == "single-user" && meta.ExecutionPrincipalID == "single-user"
			return allowed && s.workflowTrialExecutionAllowed(meta, identity.SingleUserPrincipal())
		}
		if !s.multiuser() {
			return false
		}
		if meta.ExecutionPrincipalID != meta.OwnerID {
			return false
		}
		if *owner == identity.LocalScope(bootstrapUser) {
			return s.workflowTrialExecutionAllowed(meta, identity.LocalPrincipal(*owner, bootstrapUser, true))
		}
		s.auth.mu.RLock()
		var principal identity.Principal
		for _, account := range s.auth.users {
			if scope := identity.LocalScope(account.Username); scope == *owner {
				principal = identity.LocalPrincipal(scope, account.Username, account.Admin || account.Username == bootstrapUser)
				break
			}
		}
		s.auth.mu.RUnlock()
		if !principal.Valid() {
			return false
		}
		req, _ := http.NewRequest(http.MethodPost, "/", nil)
		allowed, err := s.workflowAllowed(withWorkflowPrincipal(req, principal), meta.WorkflowID, wfInvoke)
		return err == nil && allowed && s.workflowTrialExecutionAllowed(meta, principal)
	}
	s.mgr.taskRuns.mu.Unlock()
	s.mgr.taskRuns.wake()
}

// Reconcile an interrupted segment only when no external work was in flight.
// The native restart recovery has already failed any durable running node.
func (m *Manager) reconcileWorkflowSegments(db *taskqueue.Store) error {
	metas, err := db.ListWorkflowRuns(taskqueue.WorkflowRunFilter{})
	if err != nil {
		return err
	}
	for _, meta := range metas {
		if meta.CancelRequested {
			continue
		}
		rec, err := readWFRecord(m.wfruns.recordPath(meta.ID))
		hasRecord := err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err == nil && workflow.IsTerminal(rec.Status) {
			continue
		}
		executions, err := db.ListWorkflowExecutions(meta.ID)
		if err != nil {
			return err
		}
		if len(executions) == 0 {
			continue
		}
		// Newest first is not an API assumption: select it explicitly.
		last := executions[0]
		for _, execution := range executions {
			if execution.CreatedAt > last.CreatedAt || execution.CreatedAt == last.CreatedAt && execution.ID > last.ID {
				last = execution
			}
		}
		if last.Status != taskqueue.Interrupted {
			continue
		}
		var payload workflowSegment
		if last.Source == "workflow" {
			if json.Unmarshal(last.Payload, &payload) != nil {
				continue
			}
		} else if !hasRecord {
			continue
		} // Legacy schedules without a state file are not replayed.
		if payload.Decision == nil && hasRecord {
			waiting := false
			for _, state := range rec.Nodes {
				waiting = waiting || state.Status == workflow.NodeWaitingHuman
			}
			if waiting {
				continue
			}
		}
		raw, _ := json.Marshal(payload)
		_, _, enqueueErr := db.EnqueueWorkflow(meta.ID, taskqueue.Request{Source: "workflow", SourceID: meta.WorkflowID,
			Trigger: "recovery", TriggerID: "recover-execution:" + last.ID, DefinitionHash: last.DefinitionHash, Title: last.Title, Payload: raw})
		if enqueueErr != nil && !errors.Is(enqueueErr, taskqueue.ErrConflict) {
			return enqueueErr
		}
	}
	return nil
}

func (m *Manager) workflowVersionForDefinition(def workflow.Definition) string {
	want, err := json.Marshal(canonicalWorkflow{Name: def.Name, Description: def.Description, Revision: def.Revision, Nodes: def.Nodes})
	if err != nil {
		return ""
	}
	if pointer, ok := m.workflows.pointer(def.ID); ok {
		if stored, err := m.workflows.readVersion(def.ID, pointer.VersionID); err == nil {
			got, _ := json.Marshal(stored.Workflow)
			if bytes.Equal(want, got) {
				return pointer.VersionID
			}
		}
	}
	versions, err := m.workflows.versions(def.ID)
	if err != nil {
		return ""
	}
	for _, version := range versions {
		stored, err := m.workflows.readVersion(def.ID, version.VersionID)
		if err != nil {
			continue
		}
		got, _ := json.Marshal(stored.Workflow)
		if bytes.Equal(want, got) {
			return version.VersionID
		}
	}
	return ""
}

// A node-only reviewer sees the graph and its own review material. Opening a
// review must not disclose other nodes' inputs, summaries or tool frames.
// nil means full-run visibility; a nonnil set is the permitted node scope.
func (s *Server) workflowReadableNodes(r *http.Request, rec workflow.RunRecord) map[string]bool {
	principal := s.workflowPrincipal(r)
	role, err := s.workflowRole(principal)
	if err != nil {
		return map[string]bool{}
	}
	if role == "space_admin" {
		return nil
	}
	db, err := s.workflowAccessStore(false)
	if err != nil || db == nil {
		return map[string]bool{}
	}
	if meta, err := db.GetWorkflowRun(rec.ID); err == nil && meta.OwnerID == principal.ID {
		return nil
	}
	if grants, err := db.RunGrant(rec.ID, principal.ID, ""); err == nil && slices.Contains(grants, wfRunRead) {
		return nil
	}
	if _, err := db.GetWorkflowRun(rec.ID); errors.Is(err, taskqueue.ErrNotFound) && rec.Owner != nil && principal.Scope != nil && *rec.Owner == *principal.Scope {
		return nil
	}
	nodes := map[string]bool{}
	rows, err := db.ListRunGrants(rec.ID)
	if err != nil {
		return nodes
	}
	for _, row := range rows {
		if row.PrincipalID == principal.ID && row.NodeID != "" && (slices.Contains(row.Actions, wfRunRead) || slices.Contains(row.Actions, wfRunApprove)) {
			nodes[row.NodeID] = true
		}
	}
	return nodes
}

func (s *Server) projectWorkflowRun(r *http.Request, rec workflow.RunRecord) workflow.RunRecord {
	caps := s.workflowRunCapabilities(r, rec)
	rec.Capabilities = &caps
	rec.Approvals = s.workflowApprovalView(r, rec)
	nodes := s.workflowReadableNodes(r, rec)
	if nodes == nil {
		return rec
	}
	if rec.Error != "" {
		rec.Error = "工作流执行异常，请由运行负责人核查"
	}
	rec.Inputs = nil // Run-wide business input is not node-scoped review material.
	rec.Origin = nil
	if rec.Approvals != nil {
		visible := map[string]workflow.RunApprovalAssignment{}
		for node, assignment := range rec.Approvals {
			if nodes[node] {
				visible[node] = assignment
			}
		}
		rec.Approvals = visible
	}
	states := make(map[string]workflow.NodeState, len(rec.Nodes))
	for id, state := range rec.Nodes {
		if nodes[id] {
			states[id] = state
		} else {
			states[id] = workflow.NodeState{Status: state.Status, StartedAt: state.StartedAt, EndedAt: state.EndedAt}
		}
	}
	rec.Nodes = states
	definitions := make([]workflow.Node, len(rec.Definition.Nodes))
	for i, node := range rec.Definition.Nodes {
		definitions[i] = workflow.Node{ID: node.ID, Kind: node.Kind, Agent: node.Agent, After: node.After}
	}
	rec.Definition.Nodes = definitions
	return rec
}
