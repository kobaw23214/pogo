package web

// Workflow management API: the CRUD and publish half of the workflow surface.
// Runs live in workflowrun.go; both halves register here so the route table
// reads as one feature. The publish model's reads (list and get) answer with
// workflowView: the draft plus its pointer, its dirty flag and its schedule.
import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yosukeno/pogo/schedule"
)

func (s *Server) workflowRoutes() {
	s.mux.HandleFunc("GET /api/manage/workflows", s.handleWorkflowList)
	s.mux.HandleFunc("POST /api/manage/workflows", s.requireWorkflowAction(wfEdit, s.handleWorkflowCreate))
	s.mux.HandleFunc("GET /api/manage/workflows/{wid}", s.handleWorkflowGet)
	s.mux.HandleFunc("GET /api/manage/workflows/{wid}/capabilities", s.requireWorkflowAction(wfEdit, s.handleWorkflowEditorCapabilities))
	s.mux.HandleFunc("PATCH /api/manage/workflows/{wid}", s.requireWorkflowAction(wfEdit, s.handleWorkflowUpdate))
	s.mux.HandleFunc("DELETE /api/manage/workflows/{wid}", s.requireWorkflowAction(wfDelete, s.handleWorkflowDelete))
	s.mux.HandleFunc("POST /api/manage/workflows/{wid}/publish", s.requireWorkflowAction(wfPublish, s.handleWorkflowPublish))
	s.mux.HandleFunc("GET /api/manage/workflows/{wid}/versions", s.requireWorkflowAction(wfRead, s.handleWorkflowVersions))
	s.mux.HandleFunc("POST /api/manage/workflows/{wid}/rollback", s.requireWorkflowAction(wfPublish, s.handleWorkflowRollback))
	s.mux.HandleFunc("POST /api/manage/workflows/{wid}/schedule", s.requireWorkflowAction(wfSchedule, s.handleWorkflowSchedulePut))
	s.mux.HandleFunc("DELETE /api/manage/workflows/{wid}/schedule", s.requireWorkflowAction(wfSchedule, s.handleWorkflowScheduleDelete))
	// The agent md import rides this table rather than touching studio.go's.
	s.mux.HandleFunc("POST /api/manage/agents/import", s.admin(s.handleStudioImport))
	s.mux.HandleFunc("POST /api/workflows/{wid}/runs", s.handleWorkflowRunStart)
	s.mux.HandleFunc("GET /api/workflows/{wid}/runs", s.handleWorkflowRunList)
	s.mux.HandleFunc("GET /api/workflows/{wid}/runs/{rid}", s.handleWorkflowRunGet)
	s.mux.HandleFunc("GET /api/workflows/{wid}/runs/{rid}/stream", s.handleWorkflowRunStream)
	s.mux.HandleFunc("GET /api/workflows/{wid}/runs/{rid}/nodes/{nid}/artifact", s.handleWorkflowRunArtifact)
	s.mux.HandleFunc("POST /api/workflows/{wid}/runs/{rid}/nodes/{nid}/resume", s.handleWorkflowRunResume)
	s.mux.HandleFunc("POST /api/workflows/{wid}/runs/{rid}/cancel", s.handleWorkflowRunCancel)
	s.mux.HandleFunc("POST /api/workflows/{wid}/runs/{rid}/retry", s.handleWorkflowRunRetry)
	s.registerWorkflowAccessRoutes()
	s.registerWorkflowAuthoringRoutes()
	s.registerWorkflowTrialRoutes()
}

// workflowView is one draft as the reads answer it: registry row plus the
// publish pointer (null when never published), the dirty flag saying the
// draft moved past the pointer, and the schedule summary when one exists.
type workflowView struct {
	Workflow
	SpaceID        string                `json:"space_id"`
	Capabilities   *WorkflowCapabilities `json:"capabilities,omitempty"`
	Published      *publishPointer       `json:"published"`
	Dirty          bool                  `json:"dirty"`
	DefinitionHash string                `json:"definition_hash,omitempty"`
	Schedule       *wfScheduleView       `json:"schedule,omitempty"`
}

// wfScheduleView is the workflow-facing summary of its one schedule job.
type wfScheduleView struct {
	Timezone               string     `json:"timezone,omitempty"`
	Revision               int64      `json:"revision"`
	EffectiveTimezone      string     `json:"effective_timezone"`
	EffectiveTimezoneLabel string     `json:"effective_timezone_label"`
	JobID                  string     `json:"job_id"`
	Kind                   string     `json:"kind"`
	Cron                   string     `json:"cron,omitempty"`
	EverySec               int64      `json:"every_sec,omitempty"`
	At                     string     `json:"at,omitempty"`
	Enabled                bool       `json:"enabled"`
	NextRun                *time.Time `json:"next_run"`
	LastRunStatus          string     `json:"last_run_status,omitempty"`
}

// workflowSchedules indexes schedule jobs by the workflow they fire, one
// jobs.json read per request.
func (s *Server) workflowSchedules() map[string]schedule.Job {
	jobs, err := s.scheduleStore().Load()
	out := map[string]schedule.Job{}
	if err != nil {
		return out
	}
	for _, j := range jobs {
		if j.WorkflowID != "" {
			out[j.WorkflowID] = j
		}
	}
	return out
}

func wfScheduleToView(j schedule.Job) *wfScheduleView {
	zone, label := schedule.EffectiveTimezone(j.Timezone)
	return &wfScheduleView{
		EffectiveTimezone: zone, EffectiveTimezoneLabel: label,
		Revision: j.Revision, JobID: j.ID, Kind: j.Kind, Cron: j.Cron, EverySec: j.EverySec, At: j.At, Timezone: j.Timezone,
		Enabled: j.Enabled, NextRun: nextFire(j, time.Now()), LastRunStatus: j.LastRunStatus,
	}
}

func (s *Server) workflowViewFor(w Workflow, schedules map[string]schedule.Job) workflowView {
	v := workflowView{Workflow: w, SpaceID: "default", Dirty: s.mgr.workflows.dirty(w)}
	_, _, v.DefinitionHash, _ = s.mgr.workflows.canonicalDraft(w)
	if ptr, ok := s.mgr.workflows.pointer(w.ID); ok {
		v.Published = &ptr
	}
	if j, ok := schedules[w.ID]; ok {
		v.Schedule = wfScheduleToView(j)
	}
	return v
}

func scrubWorkflowDraft(w *Workflow) {
	// Store list/get return value copies, but Nodes is a slice. Never scrub
	// instructions through that shared backing array in the live registry.
	w.Nodes = append(w.Nodes[:0:0], w.Nodes...)
	for i := range w.Nodes {
		w.Nodes[i].Task = ""
		w.Nodes[i].Prompt = ""
		w.Nodes[i].ApprovalPolicy = nil
	}
}

func (s *Server) handleWorkflowList(w http.ResponseWriter, r *http.Request) {
	canCreate, err := s.workflowAllowed(r, "", wfEdit)
	if err != nil {
		workflowAccessError(w, err)
		return
	}
	schedules := s.workflowSchedules()
	views := []workflowView{}
	for _, wf := range s.mgr.workflows.list(true) {
		allowed, err := s.workflowAllowed(r, wf.ID, wfRead)
		if err != nil {
			workflowAccessError(w, err)
			return
		}
		if !allowed {
			continue
		}
		caps := s.workflowCapabilities(r, wf.ID)
		// Publication metadata describes the complete saved draft. Redaction
		// changes its content and must happen only after hashing that draft.
		view := s.workflowViewFor(wf, schedules)
		if !caps.CanEdit && !caps.CanPublish && !caps.CanManageAccess {
			scrubWorkflowDraft(&view.Workflow)
		}
		view.Capabilities = &caps
		views = append(views, view)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"workflows": views, "can_manage": canCreate})
}

func (s *Server) handleWorkflowGet(w http.ResponseWriter, r *http.Request) {
	wf, ok := s.mgr.workflows.get(r.PathValue("wid"))
	if !ok {
		writeErr(w, 404, ErrNotFound)
		return
	}
	allowed, err := s.workflowAllowed(r, wf.ID, wfRead)
	if err != nil {
		workflowAccessError(w, err)
		return
	}
	if !allowed {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	caps := s.workflowCapabilities(r, wf.ID)
	view := s.workflowViewFor(wf, s.workflowSchedules())
	if !caps.CanEdit && !caps.CanPublish && !caps.CanManageAccess {
		scrubWorkflowDraft(&view.Workflow)
	}
	w.Header().Set("Cache-Control", "no-store")
	view.Capabilities = &caps
	writeJSON(w, 200, view)
}

func (s *Server) handleWorkflowCreate(w http.ResponseWriter, r *http.Request) {
	var input workflowInput
	if err := decodeBody(r, &input); err != nil {
		writeErr(w, 400, err)
		return
	}
	wf, err := s.mgr.workflows.put("", input, 0)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, http.StatusCreated, wf)
}

func (s *Server) handleWorkflowUpdate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		workflowInput
		Revision int `json:"revision"`
	}
	if err := decodeBody(r, &input); err != nil {
		writeErr(w, 400, err)
		return
	}
	wf, err := s.mgr.workflows.put(r.PathValue("wid"), input.workflowInput, input.Revision)
	if err != nil {
		status := 400
		if errors.Is(err, ErrNotFound) {
			status = 404
		} else if strings.Contains(err.Error(), "已被更新") {
			status = 409
		}
		writeErr(w, status, err)
		return
	}
	// Draft edits never touch runs: they start from published snapshots.
	view := s.workflowViewFor(wf, s.workflowSchedules())
	caps := s.workflowCapabilities(r, wf.ID)
	view.Capabilities = &caps
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, view)
}

func (s *Server) handleWorkflowDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("wid")
	if err := s.mgr.workflows.remove(id); err != nil {
		status := 500
		if errors.Is(err, ErrNotFound) {
			status = 404
		}
		writeErr(w, status, err)
		return
	}
	s.mgr.wfruns.cancelWorkflow(id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleWorkflowPublish(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name             string  `json:"name"`
		Desc             string  `json:"desc"`
		ExpectedRevision *int    `json:"expected_revision,omitempty"`
		ExpectedHash     *string `json:"expected_hash,omitempty"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	if (body.ExpectedRevision == nil) != (body.ExpectedHash == nil) || body.ExpectedRevision != nil && (*body.ExpectedRevision < 1 || *body.ExpectedHash == "") {
		writeErr(w, 400, errors.New("精确发布需要有效 expected_revision 和 expected_hash"))
		return
	}
	expected, hash := 0, ""
	if body.ExpectedRevision != nil {
		expected, hash = *body.ExpectedRevision, *body.ExpectedHash
	}
	ptr, err := s.mgr.workflows.publishExact(r.PathValue("wid"), body.Name, body.Desc, expected, hash)
	if err != nil {
		status := 400
		if errors.Is(err, ErrNotFound) {
			status = 404
		} else if errors.Is(err, errWorkflowDraftChanged) {
			status = 409
		}
		writeErr(w, status, err)
		return
	}
	writeJSON(w, http.StatusCreated, ptr)
}

func (s *Server) handleWorkflowVersions(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.mgr.workflows.get(r.PathValue("wid")); !ok {
		writeErr(w, 404, ErrNotFound)
		return
	}
	versions, err := s.mgr.workflows.versions(r.PathValue("wid"))
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"versions": versions})
}

func (s *Server) handleWorkflowRollback(w http.ResponseWriter, r *http.Request) {
	var body struct {
		VersionID string `json:"version_id"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	ptr, err := s.mgr.workflows.rollback(r.PathValue("wid"), strings.TrimSpace(body.VersionID))
	if err != nil {
		status := 400
		if errors.Is(err, ErrNotFound) {
			status = 404
		}
		writeErr(w, status, err)
		return
	}
	writeJSON(w, 200, ptr)
}

// wfScheduleBody is POST schedule's payload: the trigger subset of a Job.
// One workflow has at most one schedule; re-posting replaces it.
var workflowScheduleMutationMu sync.Mutex

type wfScheduleBody struct {
	Revision *int64  `json:"revision"`
	Timezone *string `json:"timezone"`
	Kind     string  `json:"kind"`
	Cron     string  `json:"cron"`
	EverySec int64   `json:"every_sec"`
	At       string  `json:"at"`
	Enabled  *bool   `json:"enabled"`
}

func (s *Server) handleWorkflowSchedulePut(w http.ResponseWriter, r *http.Request) {
	workflowScheduleMutationMu.Lock()
	defer workflowScheduleMutationMu.Unlock()
	wf, ok := s.mgr.workflows.get(r.PathValue("wid"))
	if !ok {
		writeErr(w, 404, ErrNotFound)
		return
	}
	var body wfScheduleBody
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	now := time.Now()
	job := schedule.Job{ID: wf.ID, WorkflowID: wf.ID, Prompt: "workflow:" + wf.ID, Enabled: true, CreatedAt: now, Timezone: "", ScheduleFrom: &now}
	if body.Timezone != nil {
		job.Timezone = strings.TrimSpace(*body.Timezone)
	} else if old, err := s.scheduleStore().Find(wf.ID); err == nil {
		job.Timezone = old.Timezone
	}
	if _, err := schedule.Location(job.Timezone); err != nil {
		writeErr(w, 400, err)
		return
	}
	if body.Enabled != nil {
		job.Enabled = *body.Enabled
	}
	switch body.Kind {
	case schedule.KindCron:
		if strings.TrimSpace(body.Cron) == "" || body.EverySec != 0 || body.At != "" {
			writeErr(w, 400, errors.New(`kind "cron" wants cron set, and every_sec/at empty`))
			return
		}
		if _, err := schedule.Parse(body.Cron); err != nil {
			writeErr(w, 400, err)
			return
		}
		job.Kind, job.Cron = schedule.KindCron, body.Cron
	case schedule.KindEvery:
		if !schedule.ValidEverySeconds(body.EverySec) || body.Cron != "" || body.At != "" {
			writeErr(w, 400, errors.New(`kind "every" wants every_sec >= 1, and cron/at empty`))
			return
		}
		job.Kind, job.EverySec = schedule.KindEvery, body.EverySec
	case schedule.KindAt:
		if strings.TrimSpace(body.At) == "" || body.Cron != "" || body.EverySec != 0 {
			writeErr(w, 400, errors.New(`kind "at" wants at set, and cron/every_sec empty`))
			return
		}
		at, err := schedule.ParseAtZone(body.At, job.Timezone)
		if err != nil {
			writeErr(w, 400, err)
			return
		}
		if !at.After(now) {
			writeErr(w, 400, errors.New("at is in the past"))
			return
		}
		job.Kind, job.At = schedule.KindAt, at.Format(time.RFC3339)
	default:
		writeErr(w, 400, errors.New(`kind: want "cron", "every" or "at"`))
		return
	}
	store := s.scheduleStore()
	if previous, exists := s.workflowSchedules()[wf.ID]; exists {
		if s.mgr.SchedRunning()[previous.ID] {
			writeErr(w, 409, schedule.ErrRunning)
			return
		}
		revision := previous.Revision
		if body.Revision != nil {
			revision = *body.Revision
		}
		updated, err := store.ReplaceDefinition(previous.ID, revision, job, schedule.DefinitionOptions{Enabled: body.Enabled})
		if err != nil {
			scheduleAPIError(w, err)
			return
		}
		job = updated
	} else {
		// Deleted IDs retain private history. A recreated Workflow gets a fresh
		// job namespace; it never borrows the predecessor's logs or another owner.
		err := schedule.AddJob(store, schedule.Internal(), job)
		if err != nil && strings.Contains(err.Error(), "already exists") {
			job.ID = schedule.NewID("workflow", wf.ID, now.String())
			err = schedule.AddJob(store, schedule.Internal(), job)
		}
		if err != nil {
			writeErr(w, 500, err)
			return
		}
	}

	writeJSON(w, 200, wfScheduleToView(job))
}

func (s *Server) handleWorkflowScheduleDelete(w http.ResponseWriter, r *http.Request) {
	workflowScheduleMutationMu.Lock()
	defer workflowScheduleMutationMu.Unlock()
	wid := r.PathValue("wid")
	if _, ok := s.mgr.workflows.get(wid); !ok {
		writeErr(w, 404, ErrNotFound)
		return
	}
	job, ok := s.workflowSchedules()[wid]
	if !ok {
		writeErr(w, 404, ErrNotFound)
		return
	}
	if s.mgr.SchedRunning()[job.ID] {
		writeErr(w, 409, schedule.ErrRunning)
		return
	}
	if _, err := schedule.RemoveJob(s.scheduleStore(), schedule.Internal(), job.ID); err != nil {
		writeErr(w, 404, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
