package web

// Scheduled-job API: owner-bound Agent tasks and legacy -schedule jobs.
// Agent tasks always use the in-process bound Studio runtime. Legacy global
// jobs retain their operator-only compatibility routes.
//
// The schedule package owns the state (jobs.json, runs, logs) and the rules;
// this file only adapts it to HTTP, the same split prefsapi.go keeps. The
// state directory is schedule.DefaultDir() — POGO_SCHEDULE_DIR or
// ~/.pogo/schedule — so the panel and the CLI manage one set of jobs. The OS
// backend is Detect(dir) per request: nothing scheduler-shaped is fixed at
// startup, and POGO_SCHEDULE_BACKEND=noop lets the whole API run end to end
// without touching launchd/systemd/crontab (development and tests).
//
// run-now is asynchronous because a run is minutes long: the handler records
// the job in the Manager's in-flight map, runs it in a goroutine, and answers
// 202; GET jobs reports the map as each row's "running" flag.

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yosukeno/pogo/inputref"
	"github.com/yosukeno/pogo/schedule"
)

// schedRunner is the seam scheduleapi_test replaces: production spawns the
// real pogo child, the test drives the bookkeeping with a fake.
var schedRunner = schedule.ChildRunner

// scheduleStore resolves this server's schedule directory. Per call rather
// than once at startup: POGO_SCHEDULE_DIR is the whole configuration, and a
// per-request DefaultDir is what makes the test's t.Setenv work.
func (s *Server) scheduleStore() *schedule.Store {
	return schedule.NewStore(schedule.DefaultDir())
}

// schedDetect is the seam tests use to simulate a machine's scheduler set.
var schedDetect = schedule.Detect

// scheduleBackend picks the backend for one request. A machine with no OS
// scheduler — a container without systemd or cron, or windows — gets the
// in-process backend rather than an error: the Manager's timer fires those
// jobs (see scheduler.go), so the panel keeps working everywhere. What still
// errors is a forced but unknown POGO_SCHEDULE_BACKEND value, because that is
// a typo and a typo should be loud.
func (s *Server) scheduleBackend(dir string) (schedule.Backend, error) {
	be, err := schedDetect(dir)
	if err != nil {
		if errors.Is(err, schedule.ErrNoScheduler) {
			return schedule.Internal(), nil
		}
		return nil, err
	}
	return be, nil
}

// scheduleJobView is one job as the panel renders it: the stored record plus
// the two facts only this process knows — when it fires next, and whether a
// run-now from this server is in flight right now.
type scheduleJobView struct {
	schedule.Job
	NextRun                *time.Time `json:"next_run"`
	Running                bool       `json:"running,omitempty"`
	AgentName              string     `json:"agent_name,omitempty"`
	AgentModel             string     `json:"agent_model,omitempty"`
	AvailabilityError      string     `json:"availability_error,omitempty"`
	ResultURL              string     `json:"result_url,omitempty"`
	Scope                  string     `json:"scope"`
	RunningSessionID       string     `json:"running_session_id,omitempty"`
	RunningResultURL       string     `json:"running_result_url,omitempty"`
	WaitingHuman           bool       `json:"waiting_human,omitempty"`
	EffectiveTimezone      string     `json:"effective_timezone"`
	EffectiveTimezoneLabel string     `json:"effective_timezone_label"`
	ExecutionMode          string     `json:"execution_mode"`
}

func (s *Server) scheduleJobViews(jobs []schedule.Job) []scheduleJobView {
	running := s.mgr.SchedRunning()
	now := time.Now()
	out := make([]scheduleJobView, 0, len(jobs))
	for _, j := range jobs {
		if j.WorkflowID != "" {
			continue
		}
		out = append(out, scheduleJobView{
			// nextFire rather than schedule.NextRun: when this process owns
			// the clock, an every-job that has not run yet has a first fire
			// (counted from creation), and the panel should say so.
			Job:     j,
			NextRun: nextFire(j, now),
			Running: running[j.ID],
		})
	}
	for i := range out {
		j := &out[i]
		j.Scope = "legacy"
		j.ExecutionMode = "legacy"
		j.EffectiveTimezone, j.EffectiveTimezoneLabel = schedule.EffectiveTimezone(j.Timezone)
		j.Owner = nil // the owner is an authorization fact, not a client-controlled field
		if j.AgentID != "" {
			j.Scope = "agent"
			j.ExecutionMode = "agent"
			s.mgr.schedMu.Lock()
			j.RunningSessionID = s.mgr.schedSessions[j.ID]
			s.mgr.schedMu.Unlock()
			if j.RunningSessionID != "" {
				j.RunningResultURL = scheduleResultURL(j.AgentID, j.RunningSessionID)
				s.mgr.mu.Lock()
				sess := s.mgr.sessions[j.RunningSessionID]
				s.mgr.mu.Unlock()
				if sess != nil {
					j.WaitingHuman = len(sess.Hub().Snapshot().Live.PendingGates) > 0
				}
			}
			if a, ok := s.mgr.studio.get(j.AgentID); ok {
				j.AgentName, j.AgentModel = a.Name, a.Model
				if a.Status != "ready" {
					j.AvailabilityError = "Agent 已暂停，执行时将拒绝启动"
				}
			} else {
				j.AvailabilityError = "Agent 已删除，执行时将拒绝启动"
			}
			if j.SessionID != "" {
				j.ResultURL = scheduleResultURL(j.AgentID, j.SessionID)
			}
		}
	}
	return out
}

// SchedStart marks a schedule job in flight; false means it already is, which
// the run-now handler turns into a 409.
func (m *Manager) SchedStart(id string) bool {
	m.schedMu.Lock()
	defer m.schedMu.Unlock()
	if _, ok := m.schedRunning[id]; ok {
		return false
	}
	m.schedRunning[id] = time.Now()
	return true
}

// SchedDone clears the in-flight mark when a run-now goroutine ends.
func (m *Manager) SchedDone(id string) {
	m.schedMu.Lock()
	defer m.schedMu.Unlock()
	delete(m.schedRunning, id)
	delete(m.schedSessions, id)
}

// SchedRunning reports which jobs have a run-now in flight, as a set. A copy:
// the map itself never escapes the lock.
func (m *Manager) SchedRunning() map[string]bool {
	m.schedMu.Lock()
	defer m.schedMu.Unlock()
	out := make(map[string]bool, len(m.schedRunning))
	for id := range m.schedRunning {
		out[id] = true
	}
	return out
}

func (s *Server) handleScheduleJobs(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")
	if scope != "" && scope != "all" && scope != "agent" && scope != "legacy" {
		writeErr(w, 400, errors.New("scope: want agent, legacy or all"))
		return
	}
	dir := schedule.DefaultDir()
	backend, err := s.scheduleBackend(dir)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	jobs, err := s.scheduleStore().Load()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	jobs = s.visibleScheduleJobs(r, jobs)
	serverZone, serverLabel := schedule.ServerTimezone()
	writeJSON(w, http.StatusOK, map[string]any{
		"dir": dir, "server_timezone": serverZone, "server_timezone_label": serverLabel, "current_time": time.Now(),
		"backend": backend.Name(),
		"jobs":    s.scheduleJobViews(jobs),
	})
}

// scheduleJobBody is POST /api/schedule/jobs's payload. Kind picks the
// trigger — "cron", "every" or "at" — and only that kind's field is read;
// the others must stay empty, so a body cannot say two schedules at once.
type scheduleJobBody struct {
	AgentID         string          `json:"agent_id"`
	Title           string          `json:"title"`
	Parts           []inputref.Part `json:"parts"`
	InputLayout     string          `json:"input_layout"`
	Timezone        string          `json:"timezone"`
	Revision        *int64          `json:"revision"`
	SourceSessionID string          `json:"source_session_id"`

	Prompt        string  `json:"prompt"`
	Workdir       string  `json:"workdir"`
	Name          string  `json:"name"`
	Kind          string  `json:"kind"`
	Cron          string  `json:"cron"`
	EverySec      int64   `json:"every_sec"`
	At            string  `json:"at"`
	MaxTurns      int     `json:"max_turns"`
	TurnLimitMode *string `json:"turn_limit_mode"`
	ApprovalMode  *string `json:"approval_mode"`
	BudgetUSD     float64 `json:"budget_usd"`
	// ExpiresAt is an RFC3339 deadline after which the job refuses to fire.
	// Optional, but an unattended self-refiring agent with no sunset is a
	// persistence mechanism, so the field exists and the dialog defaults it.
	ExpiresAt string `json:"expires_at"`
}

// build validates the body into a job ready for schedule.AddJob. now is the
// reference time for "in the future" and the cron's next firing.
func (b *scheduleJobBody) build(now time.Time) (schedule.Job, *time.Time, error) {
	if strings.TrimSpace(b.Prompt) == "" && len(b.Parts) == 0 {
		return schedule.Job{}, nil, errors.New("prompt is required")
	}
	job := schedule.Job{
		Prompt: b.Prompt, Title: strings.TrimSpace(b.Title), AgentID: b.AgentID, Parts: inputref.Clone(b.Parts), InputLayout: b.InputLayout, Timezone: strings.TrimSpace(b.Timezone),
		MaxTurns: b.MaxTurns, TurnLimitMode: scheduleOptionValue(b.TurnLimitMode), ApprovalMode: scheduleOptionValue(b.ApprovalMode),
		BudgetUSD: b.BudgetUSD,
		Enabled:   true, ScheduleFrom: &now,
		CreatedAt: now,
	}
	if b.MaxTurns < 0 || b.BudgetUSD < 0 {
		return schedule.Job{}, nil, errors.New("max_turns and budget_usd must not be negative")
	}
	if err := schedule.ValidateAgentRunOptions(job); err != nil {
		return schedule.Job{}, nil, err
	}
	if _, err := schedule.Location(job.Timezone); err != nil {
		return schedule.Job{}, nil, err
	}
	if utf8.RuneCountInString(job.Title) > 120 {
		return schedule.Job{}, nil, errors.New("title is too long")
	}
	if b.ExpiresAt != "" {
		exp, err := time.Parse(time.RFC3339, strings.TrimSpace(b.ExpiresAt))
		if err != nil {
			return schedule.Job{}, nil, fmt.Errorf("expires_at: want RFC3339: %w", err)
		}
		if !exp.After(now) {
			return schedule.Job{}, nil, errors.New("expires_at is in the past")
		}
		job.ExpiresAt = exp.Format(time.RFC3339Nano)
	}

	workdir := b.Workdir
	if workdir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return schedule.Job{}, nil, err
		}
		workdir = cwd
	}
	abs, err := filepath.Abs(workdir)
	if err != nil {
		return schedule.Job{}, nil, err
	}
	if st, err := os.Stat(abs); b.AgentID == "" && (err != nil || !st.IsDir()) {
		return schedule.Job{}, nil, fmt.Errorf("workdir %q is not a directory", workdir)
	}
	job.Workdir = abs

	var next *time.Time
	switch b.Kind {
	case schedule.KindCron:
		if strings.TrimSpace(b.Cron) == "" || b.EverySec != 0 || b.At != "" {
			return schedule.Job{}, nil, errors.New(`kind "cron" wants cron set, and every_sec/at empty`)
		}
		sc, err := schedule.Parse(b.Cron)
		if err != nil {
			return schedule.Job{}, nil, fmt.Errorf("cron: %w", err)
		}
		loc, _ := schedule.Location(job.Timezone)
		if n := sc.Next(now.In(loc)); !n.IsZero() {
			next = &n
		} else {
			return schedule.Job{}, nil, fmt.Errorf("cron %q never fires (nothing in the next five years)", b.Cron)
		}
		job.Kind, job.Cron = schedule.KindCron, b.Cron
	case schedule.KindEvery:
		if !schedule.ValidEverySeconds(b.EverySec) || b.Cron != "" || b.At != "" {
			return schedule.Job{}, nil, errors.New(`kind "every" wants every_sec >= 1, and cron/at empty`)
		}
		job.Kind, job.EverySec = schedule.KindEvery, b.EverySec
		n := now.Add(time.Duration(b.EverySec) * time.Second)
		next = &n
	case schedule.KindAt:
		if strings.TrimSpace(b.At) == "" || b.Cron != "" || b.EverySec != 0 {
			return schedule.Job{}, nil, errors.New(`kind "at" wants at set, and cron/every_sec empty`)
		}
		at, err := schedule.ParseAtZone(b.At, job.Timezone)
		if err != nil {
			return schedule.Job{}, nil, fmt.Errorf("at: %w", err)
		}
		if !at.After(now) {
			return schedule.Job{}, nil, fmt.Errorf("at %s is in the past", at.Format("2006-01-02 15:04"))
		}
		job.Kind, job.At = schedule.KindAt, at.Format(time.RFC3339)
		next = &at
	default:
		return schedule.Job{}, nil, fmt.Errorf("kind %q: want cron, every or at", b.Kind)
	}

	if b.Name != "" {
		if !schedule.JobNameShape.MatchString(b.Name) {
			return schedule.Job{}, nil, fmt.Errorf("name %q: use 1-40 of [A-Za-z0-9._-], starting with a letter or digit", b.Name)
		}
		job.ID = b.Name
	} else {
		// Same derivation the CLI uses: content plus the clock, so two
		// identical submissions still get distinct ids.
		job.ID = schedule.NewID(job.Prompt, job.Kind, job.Cron, job.At,
			strconv.FormatInt(job.EverySec, 10), now.String())
	}
	return job, next, nil
}

func (s *Server) handleScheduleCreateJob(w http.ResponseWriter, r *http.Request) {
	var body scheduleJobBody
	if err := decodeScheduleBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	owner, err := s.prepareScheduleBody(r, &body, nil)
	if err != nil {
		scheduleAPIError(w, err)
		return
	}
	job, _, err := body.build(time.Now())
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	dir := schedule.DefaultDir()
	backend, err := s.scheduleBackend(dir)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	job.Owner = owner
	if job.AgentID != "" || job.Timezone != "" {
		backend = schedule.Internal()
	}
	if err := schedule.AddJob(s.scheduleStore(), backend, job); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			writeErr(w, http.StatusConflict, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	job, _ = s.scheduleStore().Find(job.ID)
	writeJSON(w, http.StatusCreated, map[string]any{
		"job":     s.scheduleJobViews([]schedule.Job{job})[0],
		"backend": backend.Name(),
	})
}

// scheduleFindJob is the shared 404 gate of the per-job endpoints.
func (s *Server) scheduleFindJob(w http.ResponseWriter, r *http.Request, id string) (schedule.Job, bool) {
	job, err := s.scheduleStore().Find(id)
	if err != nil || !s.scheduleVisible(r, job) {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return schedule.Job{}, false
	}
	return job, true
}

func (s *Server) handleScheduleSetEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	id := r.PathValue("id")
	if _, ok := s.scheduleFindJob(w, r, id); !ok {
		return
	}
	if err := schedule.SetEnabled(s.scheduleStore(), id, enabled); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	job, _ := s.scheduleStore().Find(id)
	views := s.scheduleJobViews([]schedule.Job{job})
	writeJSON(w, http.StatusOK, map[string]any{"job": views[0]})
}

func (s *Server) handleScheduleEnable(w http.ResponseWriter, r *http.Request) {
	s.handleScheduleSetEnabled(w, r, true)
}

func (s *Server) handleScheduleDisable(w http.ResponseWriter, r *http.Request) {
	s.handleScheduleSetEnabled(w, r, false)
}

func (s *Server) handleScheduleDeleteJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.scheduleFindJob(w, r, id); !ok {
		return
	}
	if s.mgr.SchedRunning()[id] {
		writeErr(w, 409, schedule.ErrRunning)
		return
	}
	dir := schedule.DefaultDir()
	// Like the CLI's remove: an unreachable OS scheduler does not stop the
	// delete — the stale trigger fires a process that finds no job and exits.
	backend, err := s.scheduleBackend(dir)
	if err != nil {
		backend = nil
	}
	warn, err := schedule.RemoveJob(s.scheduleStore(), backend, id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": id, "warning": warn})
}

func (s *Server) handleScheduleRunNow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, ok := s.scheduleFindJob(w, r, id)
	if !ok {
		return
	}
	if !job.Enabled && job.AgentID == "" {
		writeErr(w, http.StatusConflict, fmt.Errorf("scheduled job %q is disabled", id))
		return
	}
	if job.ExpiresAt != "" {
		expiry, err := time.Parse(time.RFC3339, job.ExpiresAt)
		if err != nil {
			writeErr(w, 409, errors.New("任务到期时间无效，请先编辑修正"))
			return
		}
		if !expiry.After(time.Now()) {
			writeErr(w, 409, errors.New("任务已过期，请先编辑到期时间再执行"))
			return
		}
	}
	run, _, err := s.mgr.taskRuns.enqueueSchedule(job, schedule.TriggerManual, time.Now())
	if err != nil {
		scheduleAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "run_id": run.ID, "status": run.Status, "queued": true})
}

// scheduleRunsLimit is how much history the details view asks for — the
// newest records, newest first, plus the out.log tail the CLI's logs shows.
const scheduleRunsLimit = 50

func (s *Server) handleScheduleRuns(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, ok := s.scheduleFindJob(w, r, id)
	if !ok {
		return
	}
	store := s.scheduleStore()
	runs, err := store.Runs(id, scheduleRunsLimit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// Runs comes back oldest first; the panel reads newest first.
	for i, j := 0, len(runs)-1; i < j; i, j = i+1, j-1 {
		runs[i], runs[j] = runs[j], runs[i]
	}
	tail, terr := schedule.TailLines(store.OutLogPath(id), scheduleRunsLimit)
	if terr != nil && !os.IsNotExist(terr) {
		writeErr(w, http.StatusInternalServerError, terr)
		return
	}
	if runs == nil {
		runs = []schedule.RunRecord{}
	}
	if tail == nil {
		tail = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"runs":     scheduleRunViews(runs, job.AgentID),
		"log_tail": tail,
		"log_path": store.OutLogPath(id),
	})
}
