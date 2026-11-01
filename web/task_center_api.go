package web

// The task center is a public projection of existing definitions and durable
// admissions, not a second source of truth or a source of execution authority.
import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/schedule"
	"github.com/yosukeno/pogo/sentinel"
	"github.com/yosukeno/pogo/taskqueue"
	"github.com/yosukeno/pogo/workflow"
)

// Explicit fields keep private execution payloads, account bindings, cursors,
// collector state and argv out of the management API.
type taskCenterTask struct {
	ID              string `json:"id"`
	Source          string `json:"source"`
	SourceID        string `json:"source_id"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	AgentID         string `json:"agent_id"`
	AgentName       string `json:"agent_name,omitempty"`
	WorkflowID      string `json:"workflow_id,omitempty"`
	Trigger         string `json:"trigger"`
	Executor        string `json:"executor"`
	Enabled         bool   `json:"enabled"`
	State           string `json:"state"`
	NextRun         string `json:"next_run,omitempty"`
	ExpiresAt       int64  `json:"expires_at,omitempty"`
	QueuedCount     int    `json:"queued_count"`
	RunningCount    int    `json:"running_count"`
	PendingEvents   int    `json:"pending_events"`
	Attention       bool   `json:"attention"`
	ResultURL       string `json:"result_url,omitempty"`
	SentinelID      string `json:"sentinel_id,omitempty"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	Revision        int64  `json:"revision,omitempty"`
	CanManage       bool   `json:"can_manage"`
	// Live legacy/Agent approval state protects an attention signal when a
	// separately cancelled run merely left an old scheduler error receipt.
	waitingHuman bool
}

type taskCenterRun struct {
	ID              string `json:"id"`
	TaskID          string `json:"task_id"`
	Source          string `json:"source"`
	SourceID        string `json:"source_id"`
	Title           string `json:"title"`
	AgentID         string `json:"agent_id"`
	WorkflowID      string `json:"workflow_id,omitempty"`
	WorkflowRunID   string `json:"workflow_run_id,omitempty"`
	TestRun         bool   `json:"test_run,omitempty"`
	DraftRevision   int    `json:"draft_revision,omitempty"`
	DefinitionHash  string `json:"definition_hash,omitempty"`
	Trigger         string `json:"trigger"`
	Status          string `json:"status"`
	Revision        int64  `json:"revision"`
	Attempt         int    `json:"attempt"`
	CancelRequested bool   `json:"cancel_requested"`
	CreatedAt       int64  `json:"created_at"`
	StartedAt       int64  `json:"started_at,omitempty"`
	FinishedAt      int64  `json:"finished_at,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	SentinelID      string `json:"sentinel_id,omitempty"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
	ResultURL       string `json:"result_url,omitempty"`
	Summary         string `json:"summary,omitempty"`
	Error           string `json:"error,omitempty"`
	EndReason       string `json:"end_reason,omitempty"`
	CanCancel       bool   `json:"can_cancel"`
	CanRetry        bool   `json:"can_retry"`
	WaitingHuman    bool   `json:"waiting_human"`
}

type taskCenterBinding struct {
	view  taskCenterTask
	owner string
}

func (s *Server) registerTaskCenterRoutes() {
	s.mux.HandleFunc("GET /api/tasks", s.handleTaskCenter)
	s.mux.HandleFunc("POST /api/tasks/{source}/{id}/pause", func(w http.ResponseWriter, r *http.Request) { s.handleTaskCenterEnabled(w, r, false) })
	s.mux.HandleFunc("POST /api/tasks/{source}/{id}/resume", func(w http.ResponseWriter, r *http.Request) { s.handleTaskCenterEnabled(w, r, true) })
	s.mux.HandleFunc("POST /api/task-runs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) { s.handleTaskCenterRunControl(w, r, false) })
	s.mux.HandleFunc("POST /api/task-runs/{id}/retry", func(w http.ResponseWriter, r *http.Request) { s.handleTaskCenterRunControl(w, r, true) })
}

func (s *Server) taskCenterOwner(r *http.Request) (*identity.Scope, bool) {
	if owner, ok := s.ownerScope(r); ok {
		return &owner, owner.Valid()
	}
	return nil, !s.multiuser()
}

func (s *Server) taskCenterSentinelVisible(r *http.Request, spec sentinel.Spec) bool {
	owner, valid := s.taskCenterOwner(r)
	if !valid || spec.AgentID == "" || spec.OwnerID != sessionResultOwnerKey(owner) {
		return false
	}
	// Reuse the schedule ownership rule, and reject mismatched serialized
	// bindings rather than treating a claimed owner key as authorization.
	var saved *identity.Scope
	if len(spec.Owner) != 0 && string(spec.Owner) != "null" {
		saved = &identity.Scope{}
		if json.Unmarshal(spec.Owner, saved) != nil || !saved.Valid() {
			return false
		}
	}
	return sessionResultOwnerKey(saved) == spec.OwnerID && s.scheduleVisible(r, schedule.Job{AgentID: spec.AgentID, Owner: saved})
}

func (s *Server) taskCenterScheduleView(j schedule.Job) taskCenterTask {
	v := s.scheduleJobViews([]schedule.Job{j})[0]
	result := taskCenterTask{ID: "schedule:" + j.ID, Source: "schedule", SourceID: j.ID, Title: j.Title, Description: j.Prompt, AgentID: j.AgentID, AgentName: v.AgentName, WorkflowID: j.WorkflowID, Trigger: "time", Executor: v.ExecutionMode, Enabled: j.Enabled, State: "scheduled", ResultURL: v.ResultURL, Revision: j.Revision, CanManage: true}
	if result.Title == "" {
		result.Title = j.ID
	}
	if j.SessionID != "" {
		result.ResultURL = s.taskCenterRunView(taskqueue.Run{Request: taskqueue.Request{AgentID: j.AgentID, WorkflowID: j.WorkflowID, OwnerID: sessionResultOwnerKey(j.Owner)}, SessionID: j.SessionID}).ResultURL
	}
	if v.NextRun != nil {
		result.NextRun = v.NextRun.Format(time.RFC3339)
	}
	if at, err := time.Parse(time.RFC3339Nano, j.ExpiresAt); err == nil {
		result.ExpiresAt = at.UnixMilli()
	}
	switch {
	case j.ExpiresAt != "" && taskCenterExpired(j.ExpiresAt):
		result.State = "expired"
		result.NextRun = ""
	case j.Kind == schedule.KindAt && j.LastRunStatus == "ok" && v.NextRun == nil:
		// Successful one-shot jobs are disabled by RunOnce. That is completion,
		// not a user pause of an unfinished responsibility.
		result.State = "completed"
	case !j.Enabled:
		result.State = "paused"
		if j.ConsecutiveFailures >= 10 {
			result.State, result.Attention = "autoPaused", true
		}
	case v.AvailabilityError != "":
		result.State, result.Attention = "unavailable", true
	case j.LastRunStatus == "error":
		result.State, result.Attention = "error", true
	}
	result.Attention = result.Attention || v.WaitingHuman
	result.waitingHuman = v.WaitingHuman
	return result
}

func taskCenterExpired(at string) bool {
	t, err := time.Parse(time.RFC3339, at)
	return err == nil && !t.After(time.Now())
}

func (s *Server) taskCenterSentinelView(w sentinel.Watch) taskCenterTask {
	result := taskCenterTask{ID: "sentinel:" + w.Spec.ID, Source: "sentinel", SourceID: w.Spec.ID, Title: w.Spec.Title, Description: w.Spec.Task, AgentID: w.Spec.AgentID, Trigger: "condition", Executor: "agent", Enabled: w.Spec.Enabled, State: "listening", PendingEvents: w.Pending, Revision: w.Spec.Revision, CanManage: true, ExpiresAt: w.Spec.ExpiresAt}
	result.SentinelID, result.ParentSessionID, result.SessionID = w.Spec.ID, s.mgr.sentinelParentSession(w), w.Spec.SessionID
	if a, ok := s.mgr.studio.get(w.Spec.AgentID); ok {
		result.AgentName = a.Name
	}
	if w.Spec.SessionID != "" {
		result.ResultURL = s.taskCenterRunView(taskqueue.Run{Request: taskqueue.Request{AgentID: w.Spec.AgentID, OwnerID: w.Spec.OwnerID}, SessionID: w.Spec.SessionID}).ResultURL
	}
	if w.Spec.Expired(time.Now().UnixMilli()) {
		result.State = "expired"
		result.Attention = w.Pending > 0
	} else if !w.Spec.Enabled {
		result.State = "paused"
	} else if _, err := s.mgr.sentinels.authorized(w.Spec); err != nil {
		result.State, result.Attention = "blocked", true
	} else if w.Status == "error" || w.Status == "blocked" {
		result.State, result.Attention = w.Status, true
	} else if w.Status == "waiting_capacity" {
		result.State = "waiting_capacity"
	}
	if w.NextAt > 0 && w.Spec.Enabled && !w.Spec.Expired(time.Now().UnixMilli()) {
		result.NextRun = time.UnixMilli(w.NextAt).UTC().Format(time.RFC3339)
	}
	return result
}

func (s *Server) taskCenterDefinitions(r *http.Request) (map[string]taskCenterBinding, error) {
	jobs, err := s.scheduleStore().Load()
	if err != nil {
		return nil, fmt.Errorf("读取定时任务失败: %w", err)
	}
	result := map[string]taskCenterBinding{}
	aid := r.URL.Query().Get("agent_id")
	for _, j := range jobs {
		if !s.scheduleVisible(r, j) || aid != "" && j.AgentID != aid {
			continue
		}
		v := s.taskCenterScheduleView(j)
		result[v.ID] = taskCenterBinding{v, sessionResultOwnerKey(j.Owner)}
	}
	if s.mgr.sentinels == nil {
		return result, nil
	}
	db, err := s.mgr.sentinels.database(false)
	if err != nil {
		return nil, fmt.Errorf("读取条件监听失败: %w", err)
	}
	if db == nil {
		return result, nil
	}
	owner, valid := s.taskCenterOwner(r)
	if !valid {
		return result, nil
	}
	watches, err := db.List(sessionResultOwnerKey(owner), aid)
	if err != nil {
		return nil, fmt.Errorf("读取条件监听失败: %w", err)
	}
	for _, w := range watches {
		if !s.taskCenterSentinelVisible(r, w.Spec) {
			continue
		}
		v := s.taskCenterSentinelView(w)
		result[v.ID] = taskCenterBinding{v, w.Spec.OwnerID}
	}
	return result, nil
}

func taskCenterRunVisible(run taskqueue.Run, defs map[string]taskCenterBinding) bool {
	b, ok := defs[run.TaskID]
	return ok && run.TaskID == run.Source+":"+run.SourceID && run.Source == b.view.Source && run.SourceID == b.view.SourceID && run.OwnerID == b.owner && run.AgentID == b.view.AgentID && run.WorkflowID == b.view.WorkflowID
}

// The immutable admission owner survives deletion or rebinding of the source.
// Only controls need the current definition; history never borrows a new
// definition's title, owner or execution authority.
func (s *Server) taskCenterRunReadable(r *http.Request, run taskqueue.Run) bool {
	if aid := r.URL.Query().Get("agent_id"); aid != "" && aid != run.AgentID {
		return false
	}
	if _, rec, ok := s.taskCenterWorkflowBound(run); ok {
		allowed, err := s.workflowRunAllowed(r, rec, wfRunRead, "")
		return err == nil && allowed
	}
	if run.Source == "workflow" {
		// A claimed WorkflowRunID in a raw queue request is not an ownership
		// binding. Only the transactional catalog association is authoritative.
		return false
	}
	if run.Source != "schedule" && run.Source != "sentinel" || run.TaskID != run.Source+":"+run.SourceID {
		return false
	}
	if aid := r.URL.Query().Get("agent_id"); aid != "" && aid != run.AgentID {
		return false
	}
	if run.AgentID == "" {
		return run.Source == "schedule" && s.adminRequest(r) && run.OwnerID == sessionResultOwnerKey(nil)
	}
	owner, valid := s.taskCenterOwner(r)
	return valid && run.OwnerID == sessionResultOwnerKey(owner)
}

func (s *Server) taskCenterWorkflowBound(run taskqueue.Run) (taskqueue.WorkflowRunMeta, workflow.RunRecord, bool) {
	if run.WorkflowID == "" || s.mgr.taskRuns == nil || s.mgr.wfruns == nil {
		return taskqueue.WorkflowRunMeta{}, workflow.RunRecord{}, false
	}
	db, err := s.mgr.taskRuns.database(false)
	if err != nil || db == nil {
		return taskqueue.WorkflowRunMeta{}, workflow.RunRecord{}, false
	}
	meta, err := db.WorkflowRunForExecution(run.ID)
	if errors.Is(err, taskqueue.ErrNotFound) && run.Source == "schedule" && run.TaskID == "schedule:"+run.SourceID && validID(run.SessionID) {
		meta, err = db.GetWorkflowRun(run.SessionID)
		if err == nil {
			segments, lookupErr := db.ListWorkflowExecutions(meta.ID)
			if lookupErr != nil {
				return taskqueue.WorkflowRunMeta{}, workflow.RunRecord{}, false
			}
			managed := false
			for _, segment := range segments {
				managed = managed || segment.Source == "workflow"
			}
			if !managed {
				return taskqueue.WorkflowRunMeta{}, workflow.RunRecord{}, false
			}
		}
	}
	if err != nil || meta.WorkflowID != run.WorkflowID || meta.OwnerID != run.OwnerID {
		return taskqueue.WorkflowRunMeta{}, workflow.RunRecord{}, false
	}
	rec, ok := s.mgr.wfruns.snapshot(meta.ID)
	if !ok || rec.WorkflowID != meta.WorkflowID {
		return taskqueue.WorkflowRunMeta{}, workflow.RunRecord{}, false
	}
	return meta, rec, true
}

func (s *Server) taskCenterWorkflowProjection(run taskqueue.Run, meta taskqueue.WorkflowRunMeta, rec workflow.RunRecord) taskCenterRun {
	v := taskCenterRun{ID: meta.ID, WorkflowRunID: meta.ID, TaskID: "workflow-run:" + meta.ID,
		Source: "workflow", SourceID: meta.WorkflowID, Title: rec.Definition.Name, WorkflowID: meta.WorkflowID,
		Trigger: meta.Source, Status: rec.Status, Revision: rec.StateRevision + 1, Attempt: 1,
		CancelRequested: meta.CancelRequested, CreatedAt: meta.CreatedAt, SessionID: meta.ID,
		TestRun: meta.TestRun, DraftRevision: meta.DraftRevision, DefinitionHash: meta.DefinitionHash,
		ResultURL: "/manage/workflows/" + url.PathEscape(meta.WorkflowID) + "?run=" + url.QueryEscape(meta.ID),
		Summary:   run.Summary, Error: rec.Error, EndReason: run.EndReason}
	if meta.Source == "schedule" && meta.SourceID != "" {
		v.Source, v.SourceID, v.TaskID = "schedule", meta.SourceID, "schedule:"+meta.SourceID
	}
	if v.Title == "" {
		v.Title = run.Title
	}
	for _, node := range rec.Nodes {
		v.WaitingHuman = v.WaitingHuman || node.Status == workflow.NodeWaitingHuman
		if at, err := time.Parse(time.RFC3339Nano, node.StartedAt); err == nil && (v.StartedAt == 0 || at.UnixMilli() < v.StartedAt) {
			v.StartedAt = at.UnixMilli()
		}
	}
	if rec.Status == workflow.RunWorking {
		v.Status = taskqueue.Running
	}
	if workflow.IsTerminal(rec.Status) {
		v.WaitingHuman = false
		if ended, err := time.Parse(time.RFC3339Nano, rec.EndedAt); err == nil {
			v.FinishedAt = ended.UnixMilli()
		}
	}
	if rec.PersistenceError != "" {
		v.Error = rec.PersistenceError
	}
	if !v.WaitingHuman && v.EndReason == "waiting_human" {
		v.EndReason = ""
	}
	v.CanCancel = (rec.Status == "queued" || rec.Status == workflow.RunWorking) && !meta.CancelRequested
	v.CanRetry = rec.Status == workflow.RunFailed || rec.Status == workflow.RunCancelled || rec.Status == taskqueue.Interrupted
	return v
}

func (s *Server) taskCenterRunViewFor(r *http.Request, run taskqueue.Run) taskCenterRun {
	if meta, rec, ok := s.taskCenterWorkflowBound(run); ok {
		v := s.taskCenterWorkflowProjection(run, meta, rec)
		if nodes := s.workflowReadableNodes(r, rec); nodes != nil {
			v.Summary = ""
			if v.Error != "" {
				v.Error = "工作流执行异常，请由运行负责人核查"
			}
		}
		cancel, cerr := s.workflowRunAllowed(r, rec, wfRunCancel, "")
		retry, rerr := s.workflowRunAllowed(r, rec, wfRunRetry, "")
		v.CanCancel = v.CanCancel && cerr == nil && cancel
		v.CanRetry = v.CanRetry && rerr == nil && retry
		if _, available := s.mgr.workflows.get(rec.WorkflowID); !available {
			v.CanRetry = false
		}
		return v
	}
	return s.taskCenterRunView(run)
}

func (s *Server) taskCenterRunSession(run taskqueue.Run) *Session {
	if !validID(run.SessionID) || isBrowserAuthoringID(run.SessionID) {
		return nil
	}
	s.mgr.mu.Lock()
	sess := s.mgr.sessions[run.SessionID]
	s.mgr.mu.Unlock()
	if sess != nil && sess.studioID == run.AgentID && sessionResultOwnerKey(sess.owner) == run.OwnerID {
		return sess
	}
	return nil
}

func (s *Server) taskCenterColdSessionOwned(run taskqueue.Run) bool {
	if !validID(run.SessionID) || isBrowserAuthoringID(run.SessionID) {
		return false
	}
	binding, ok := s.mgr.studio.binding(run.SessionID)
	if !ok || binding.Agent.ID != run.AgentID || sessionResultOwnerKey(binding.Owner) != run.OwnerID {
		return false
	}
	if !s.multiuser() {
		return run.OwnerID == sessionResultOwnerKey(nil)
	}
	if s.mgr.cfg.Catalog == nil {
		return false
	}
	record, err := s.mgr.cfg.Catalog.OwnerOf(context.Background(), run.SessionID)
	if err != nil || !record.Scope.Valid() || sessionResultOwnerKey(&record.Scope) != run.OwnerID {
		return false
	}
	_, err = s.mgr.cfg.Catalog.Get(context.Background(), record.Scope, run.SessionID)
	return err == nil
}

func (s *Server) taskCenterSentinelCancelledNeedsReview(run taskqueue.Run) bool {
	if run.Source != "sentinel" || run.Status != taskqueue.Cancelled || run.TaskID != "sentinel:"+run.SourceID || s.mgr.sentinels == nil {
		return false
	}
	var payload sentinelTaskPayload
	if json.Unmarshal(run.Payload, &payload) != nil || payload.BatchID == "" || payload.Attempt <= 0 || run.TriggerID != fmt.Sprintf("batch:%s:%d", payload.BatchID, payload.Attempt) {
		return false
	}
	db, err := s.mgr.sentinels.database(false)
	if err != nil || db == nil {
		return false
	}
	w, err := db.Get(run.SourceID)
	if err != nil || w.Spec.OwnerID != run.OwnerID || w.Spec.AgentID != run.AgentID || sentinelTaskHash(w.Spec) != run.DefinitionHash {
		return false
	}
	batch, err := db.GetBatch(payload.BatchID)
	return err == nil && batch.WatchID == run.SourceID && batch.Attempt == payload.Attempt && batch.Status == "interrupted" && (run.SessionID == "" || run.SessionID == batch.SessionID)
}

func (s *Server) taskCenterRunView(run taskqueue.Run) taskCenterRun {
	if meta, rec, ok := s.taskCenterWorkflowBound(run); ok {
		return s.taskCenterWorkflowProjection(run, meta, rec)
	}
	v := taskCenterRun{ID: run.ID, TaskID: run.TaskID, Source: run.Source, SourceID: run.SourceID, Title: run.Title, AgentID: run.AgentID, WorkflowID: run.WorkflowID, Trigger: run.Trigger, Status: run.Status, Revision: run.Revision, Attempt: run.Attempt, CancelRequested: run.CancelRequested, CreatedAt: run.CreatedAt, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, SessionID: run.SessionID, Summary: run.Summary, Error: run.Error, EndReason: run.EndReason}
	if run.Source == "sentinel" {
		v.SentinelID = run.SourceID
		if records, _, err := s.mgr.sentinelSessionRecords(); err == nil {
			for _, record := range records {
				if record.Spec.ID == run.SourceID && record.Spec.OwnerID == run.OwnerID && record.Spec.AgentID == run.AgentID {
					v.ParentSessionID = s.mgr.sentinelParentSession(record)
					break
				}
			}
		}
	}
	if run.SessionID != "" && run.WorkflowID != "" {
		v.SessionID = ""
		if s.mgr.wfruns != nil {
			if rec, ok := s.mgr.wfruns.snapshot(run.SessionID); ok && rec.WorkflowID == run.WorkflowID && sessionResultOwnerKey(rec.Owner) == run.OwnerID {
				v.SessionID = run.SessionID
				v.ResultURL = "/manage/workflows/" + url.PathEscape(run.WorkflowID) + "?run=" + url.QueryEscape(run.SessionID)
				for _, node := range rec.Nodes {
					v.WaitingHuman = v.WaitingHuman || node.Status == workflow.NodeWaitingHuman
				}
				// The schedule wrapper may have returned while a human node was
				// suspended. The native workflow remains the live display truth.
				switch rec.Status {
				case workflow.RunWorking:
					v.Status, v.FinishedAt = taskqueue.Running, 0
				case workflow.RunCompleted:
					v.Status = taskqueue.Completed
				case workflow.RunFailed:
					v.Status, v.Error = taskqueue.Failed, rec.Error
				case workflow.RunCancelled:
					v.Status = taskqueue.Cancelled
				}
				if rec.Status != workflow.RunWorking {
					v.WaitingHuman = false
					if ended, err := time.Parse(time.RFC3339Nano, rec.EndedAt); err == nil {
						v.FinishedAt = ended.UnixMilli()
					}
				}
			}
		}
	} else if run.SessionID != "" && run.AgentID != "" {
		sess := s.taskCenterRunSession(run)
		if sess == nil && !s.taskCenterColdSessionOwned(run) {
			v.SessionID = ""
		} else {
			if run.Source == "sentinel" {
				// Sentinel results retain their own native identity/viewer. The
				// schedule-only task_session route must never receive this role.
				v.ResultURL = "/manage/agents/" + url.PathEscape(run.AgentID) + "/chat?c=" + url.QueryEscape(run.SessionID)
			} else {
				v.ResultURL = scheduleResultURL(run.AgentID, run.SessionID)
			}
			if sess != nil && run.Status == taskqueue.Running {
				v.WaitingHuman = len(sess.Hub().Snapshot().Live.PendingGates) > 0
			}
		}
	} else {
		v.SessionID = ""
	}
	if !v.WaitingHuman && v.EndReason == "waiting_human" {
		v.EndReason = ""
	}
	v.CanCancel = (run.Status == taskqueue.Queued && v.Status == taskqueue.Queued || run.Status == taskqueue.Running && v.Status == taskqueue.Running) && !run.CancelRequested
	v.CanRetry = (run.Status == taskqueue.Failed || run.Status == taskqueue.Interrupted) && v.Status == run.Status && !v.WaitingHuman
	if !v.WaitingHuman && s.taskCenterSentinelCancelledNeedsReview(run) {
		v.CanRetry = true
	}
	return v
}

func (s *Server) handleTaskCenter(w http.ResponseWriter, r *http.Request) {
	if !taskCenterValidAgentFilter(r.URL.Query().Get("agent_id")) {
		writeErr(w, http.StatusBadRequest, errors.New("agent_id 无效"))
		return
	}
	defs, err := s.taskCenterDefinitions(r)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	runs := []taskCenterRun{}
	queueError := ""
	if s.mgr.taskRuns == nil {
		queueError = "后台执行队列尚未就绪"
	} else if db, err := s.mgr.taskRuns.database(false); err != nil {
		queueError = "读取执行队列失败: " + err.Error()
	} else if db != nil {
		rows, err := db.List(taskqueue.Filter{AgentID: r.URL.Query().Get("agent_id")})
		if err != nil {
			queueError = "读取执行队列失败: " + err.Error()
		} else {
			latest := map[string]taskCenterRun{}
			waitingHuman := map[string]bool{}
			seenWorkflows := map[string]bool{}
			for _, row := range rows {
				if !s.taskCenterRunReadable(r, row) {
					continue
				}
				v := s.taskCenterRunViewFor(r, row)
				if v.WorkflowRunID != "" {
					if seenWorkflows[v.WorkflowRunID] {
						continue
					}
					seenWorkflows[v.WorkflowRunID] = true
					if v.Source != "schedule" {
						runs = append(runs, v)
						continue
					}
				}
				visible := taskCenterRunVisible(row, defs)
				if v.WorkflowRunID != "" && v.Source == "schedule" {
					binding, found := defs[v.TaskID]
					visible = found && binding.view.Source == "schedule" && binding.view.WorkflowID == v.WorkflowID && binding.owner == row.OwnerID
				}
				if !visible {
					if v.WorkflowRunID == "" {
						v.CanCancel, v.CanRetry = false, false
					}
					runs = append(runs, v)
					continue
				}
				runs = append(runs, v)
				b := defs[v.TaskID]
				if v.Status == taskqueue.Queued {
					b.view.QueuedCount++
				}
				if v.Status == taskqueue.Running && !v.WaitingHuman {
					b.view.RunningCount++
				}
				b.view.Attention = b.view.Attention || v.WaitingHuman
				waitingHuman[v.TaskID] = waitingHuman[v.TaskID] || v.WaitingHuman
				// Active sentinel cancellation can leave a native interrupted
				// batch whose execution outcome still requires explicit review.
				if v.Source == "sentinel" && v.Status == taskqueue.Cancelled && v.CanRetry {
					b.view.Attention = true
				}
				defs[v.TaskID] = b
				if previous, ok := latest[v.TaskID]; !ok || v.CreatedAt > previous.CreatedAt || v.CreatedAt == previous.CreatedAt && v.ID > previous.ID {
					latest[v.TaskID] = v
				}
			}
			for taskID, run := range latest {
				b := defs[taskID]
				if b.view.Source == "schedule" && b.view.State == "error" && run.Status == taskqueue.Cancelled && run.CancelRequested {
					b.view.State = "scheduled"
					if !b.view.Enabled {
						b.view.State = "paused"
					}
					b.view.Attention = b.view.waitingHuman || waitingHuman[taskID]
				}
				b.view.Attention = b.view.Attention || run.Status == taskqueue.Failed || run.Status == taskqueue.Interrupted
				defs[taskID] = b
			}
		}
	}
	tasks := make([]taskCenterTask, 0, len(defs))
	for _, b := range defs {
		tasks = append(tasks, b.view)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	response := map[string]any{"tasks": tasks, "runs": runs}
	if queueError != "" {
		response["queue_error"] = queueError
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleTaskCenterEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	id, source := r.PathValue("id"), r.PathValue("source")
	switch source {
	case "schedule":
		j, err := s.scheduleStore().Find(id)
		if err != nil || !s.scheduleVisible(r, j) || r.URL.Query().Get("agent_id") != "" && r.URL.Query().Get("agent_id") != j.AgentID {
			writeErr(w, http.StatusNotFound, ErrNotFound)
			return
		}
		if err := schedule.SetEnabled(s.scheduleStore(), id, enabled); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		j, err = s.scheduleStore().Find(id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if s.mgr.taskRuns != nil {
			s.mgr.taskRuns.wake()
		}
		writeJSON(w, http.StatusOK, map[string]any{"task": s.taskCenterScheduleView(j)})
	case "sentinel":
		if s.mgr.sentinels == nil {
			writeErr(w, http.StatusNotFound, ErrNotFound)
			return
		}
		db, err := s.mgr.sentinels.database(false)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if db == nil {
			writeErr(w, http.StatusNotFound, ErrNotFound)
			return
		}
		watch, err := db.Get(id)
		if err != nil || !s.taskCenterSentinelVisible(r, watch.Spec) || r.URL.Query().Get("agent_id") != "" && r.URL.Query().Get("agent_id") != watch.Spec.AgentID {
			writeErr(w, http.StatusNotFound, ErrNotFound)
			return
		}
		if enabled {
			if _, err = s.mgr.sentinels.authorized(watch.Spec); err == nil {
				err = verifySentinelBundle(watch.Spec.ScriptDir)
			}
			if err == nil && runtime.GOOS != "linux" {
				err = errors.New("条件监听仅可在受支持的 Linux 沙箱中恢复")
			}
			if err != nil {
				writeErr(w, http.StatusConflict, err)
				return
			}
		}
		s.mgr.sentinels.mu.Lock()
		watch, err = db.SetEnabled(id, enabled)
		s.mgr.sentinels.mu.Unlock()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if !enabled {
			s.mgr.sentinels.stopWatch(id)
		}
		if s.mgr.taskRuns != nil {
			s.mgr.taskRuns.wake()
		}
		writeJSON(w, http.StatusOK, map[string]any{"task": s.taskCenterSentinelView(watch)})
	default:
		writeErr(w, http.StatusNotFound, ErrNotFound)
	}
}

func (s *Server) handleTaskCenterRunControl(w http.ResponseWriter, r *http.Request, retry bool) {
	if s.mgr.taskRuns == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("后台执行队列尚未就绪"))
		return
	}
	db, err := s.mgr.taskRuns.database(false)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if db == nil {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	if meta, err := db.GetWorkflowRun(r.PathValue("id")); err == nil {
		s.handleTaskCenterWorkflowControl(w, r, db, meta, retry)
		return
	} else if errors.Is(err, taskqueue.ErrConflict) {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	} else if !errors.Is(err, taskqueue.ErrNotFound) {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	run, err := db.Get(r.PathValue("id"))
	if errors.Is(err, taskqueue.ErrNotFound) || errors.Is(err, taskqueue.ErrConflict) {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if meta, _, ok := s.taskCenterWorkflowBound(run); ok {
		s.handleTaskCenterWorkflowControl(w, r, db, meta, retry)
		return
	}
	if !s.taskCenterRunReadable(r, run) {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	defs, err := s.taskCenterDefinitions(r)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if !taskCenterRunVisible(run, defs) {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	var body struct {
		Revision int64 `json:"revision"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("请提供执行记录的 revision"))
		return
	}
	if d.Decode(new(any)) != io.EOF || body.Revision <= 0 {
		writeErr(w, http.StatusBadRequest, errors.New("revision 必须是正整数，且请求不能含多余内容"))
		return
	}
	if run.Revision != body.Revision {
		writeErr(w, http.StatusConflict, taskqueue.ErrConflict)
		return
	}
	if retry {
		run, err = s.mgr.taskRuns.retry(run.ID, body.Revision)
	} else {
		run, err = s.mgr.taskRuns.cancel(run.ID, body.Revision)
	}
	if err != nil {
		switch {
		case errors.Is(err, taskqueue.ErrNotFound):
			writeErr(w, http.StatusNotFound, ErrNotFound)
		case errors.Is(err, taskqueue.ErrConflict), errors.Is(err, taskqueue.ErrBusy):
			writeErr(w, http.StatusConflict, err)
		default:
			writeErr(w, http.StatusInternalServerError, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": s.taskCenterRunView(run)})
}

func (s *Server) handleTaskCenterWorkflowControl(w http.ResponseWriter, r *http.Request, db *taskqueue.Store, meta taskqueue.WorkflowRunMeta, retry bool) {
	if r.URL.Query().Get("agent_id") != "" {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	rec, ok := s.mgr.wfruns.snapshot(meta.ID)
	if !ok || rec.WorkflowID != meta.WorkflowID {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	action := wfRunCancel
	if retry {
		action = wfRunRetry
	}
	read, rerr := s.workflowRunAllowed(r, rec, wfRunRead, "")
	allowed, err := s.workflowRunAllowed(r, rec, action, "")
	if rerr != nil || err != nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("暂时无法确认工作流运行权限"))
		return
	}
	if !read || !allowed {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	var body struct {
		Revision int64 `json:"revision"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil || d.Decode(new(any)) != io.EOF || body.Revision <= 0 {
		writeErr(w, http.StatusBadRequest, errors.New("请提供执行记录的有效 revision"))
		return
	}
	if body.Revision != rec.StateRevision+1 {
		writeErr(w, http.StatusConflict, taskqueue.ErrConflict)
		return
	}
	if retry {
		if rec.Status != workflow.RunFailed && rec.Status != workflow.RunCancelled && rec.Status != "interrupted" {
			writeErr(w, http.StatusConflict, taskqueue.ErrConflict)
			return
		}
		initiator := s.workflowPrincipal(r).ID
		actorDigest := sha256.Sum256([]byte(initiator))
		key := fmt.Sprintf("task-center-retry:%s:%d:%x", rec.ID, body.Revision, actorDigest)
		next, err := s.mgr.admitWorkflow(rec.Definition, rec.Owner, "retry", rec.ID, key, rec.ID, initiator)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, errWorkflowApprovalAssignment) {
				status = http.StatusBadRequest
			}
			if errors.Is(err, taskqueue.ErrConflict) {
				status = http.StatusConflict
			}
			writeErr(w, status, err)
			return
		}
		segments, err := db.ListWorkflowExecutions(next.ID)
		if err != nil || len(segments) == 0 {
			writeErr(w, http.StatusInternalServerError, errors.New("新运行已受理，执行段读取失败"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"run": s.taskCenterRunViewFor(r, segments[0])})
		return
	}
	if rec.Status != "queued" && rec.Status != workflow.RunWorking {
		writeErr(w, http.StatusConflict, taskqueue.ErrConflict)
		return
	}
	if _, err := s.mgr.cancelWorkflowRun(meta.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	segments, err := db.ListWorkflowExecutions(meta.ID)
	if err != nil || len(segments) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"cancelled": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": s.taskCenterRunViewFor(r, segments[0])})
}

// Keep a bounded query shape; empty means the current owner's visible tasks.
func taskCenterValidAgentFilter(value string) bool {
	return len(value) <= 256 && !strings.ContainsRune(value, 0)
}
