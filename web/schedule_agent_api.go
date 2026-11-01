package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/inputref"
	"github.com/yosukeno/pogo/schedule"
	"github.com/yosukeno/pogo/skills"
)

func (s *Server) registerScheduleRoutes() {
	s.mux.HandleFunc("GET /api/schedule/jobs", s.handleScheduleJobs)
	s.mux.HandleFunc("POST /api/schedule/jobs", s.handleScheduleCreateJob)
	s.mux.HandleFunc("POST /api/schedule/preview", s.handleSchedulePreview)
	s.mux.HandleFunc("GET /api/schedule/jobs/{id}", s.handleScheduleGetJob)
	s.mux.HandleFunc("PATCH /api/schedule/jobs/{id}", s.handleScheduleUpdateJob)
	s.mux.HandleFunc("POST /api/schedule/jobs/{id}/enable", s.handleScheduleEnable)
	s.mux.HandleFunc("POST /api/schedule/jobs/{id}/disable", s.handleScheduleDisable)
	s.mux.HandleFunc("DELETE /api/schedule/jobs/{id}", s.handleScheduleDeleteJob)
	s.mux.HandleFunc("POST /api/schedule/jobs/{id}/run-now", s.handleScheduleRunNow)
	s.mux.HandleFunc("GET /api/schedule/jobs/{id}/runs", s.handleScheduleRuns)
}
func decodeScheduleBody(r *http.Request, b *scheduleJobBody) error {
	d := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(b); err != nil {
		return fmt.Errorf("invalid schedule: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("invalid schedule: trailing data")
	}
	return nil
}
func (s *Server) scheduleVisible(r *http.Request, j schedule.Job) bool {
	if j.AgentID == "" {
		return s.adminRequest(r)
	}
	sc, scoped := s.ownerScope(r)
	if scoped {
		return j.Owner != nil && j.Owner.Valid() && *j.Owner == sc
	}
	return !s.multiuser() && j.Owner == nil
}
func (s *Server) visibleScheduleJobs(r *http.Request, jobs []schedule.Job) []schedule.Job {
	out := []schedule.Job{}
	aid, scope := r.URL.Query().Get("agent_id"), r.URL.Query().Get("scope")
	for _, j := range jobs {
		if !s.scheduleVisible(r, j) || (aid != "" && j.AgentID != aid) {
			continue
		}
		if scope == "agent" && j.AgentID == "" || scope == "legacy" && j.AgentID != "" {
			continue
		}
		out = append(out, j)
	}
	return out
}

type scheduleHTTPError struct {
	status int
	err    error
}

func (e scheduleHTTPError) Error() string { return e.err.Error() }
func scheduleAPIError(w http.ResponseWriter, err error) {
	var e scheduleHTTPError
	if errors.As(err, &e) {
		writeErr(w, e.status, e.err)
		return
	}
	if errors.Is(err, schedule.ErrConflict) || errors.Is(err, schedule.ErrRunning) {
		writeErr(w, 409, err)
		return
	}
	writeErr(w, 400, err)
}
func scheduleResultURL(aid, sid string) string {
	return "/manage/agents/" + url.PathEscape(aid) + "/chat?task_session=" + url.QueryEscape(sid)
}
func scheduleRunViews(runs []schedule.RunRecord, aid string, wid ...string) []map[string]any {
	out := make([]map[string]any, 0, len(runs))
	for _, run := range runs {
		raw, _ := json.Marshal(run)
		row := map[string]any{}
		_ = json.Unmarshal(raw, &row)
		if aid != "" && run.SessionID != "" {
			row["result_url"] = scheduleResultURL(aid, run.SessionID)
		}
		if aid == "" && len(wid) > 0 && wid[0] != "" && run.SessionID != "" {
			row["result_url"] = "/manage/workflows/" + url.PathEscape(wid[0]) + "?run=" + url.QueryEscape(run.SessionID)
		}
		out = append(out, row)
	}
	return out
}

// prepareScheduleBody resolves identities and files from trusted host state.
// Snapshot text is data, never a way to grant the Agent another capability.
func (s *Server) prepareScheduleBody(r *http.Request, b *scheduleJobBody, old *schedule.Job) (*identity.Scope, error) {
	if old != nil && (old.AgentID != b.AgentID || old.WorkflowID != "") {
		return nil, errors.New("任务绑定不可更换；Workflow 计划请从工作流编辑")
	}
	if err := b.normalizeAgentRunOptions(old); err != nil {
		return nil, err
	}
	if b.AgentID == "" {
		if !s.adminRequest(r) {
			return nil, scheduleHTTPError{403, errors.New("请选择当前 Agent，旧全局任务仅管理员可管理")}
		}
		if b.Parts != nil {
			return nil, errors.New("混排引用需要选择 Agent")
		}
		return nil, nil
	}
	a, ok := s.mgr.studio.get(b.AgentID)
	if !ok || a.Status != "ready" {
		return nil, errors.New("Agent 不存在或尚未就绪")
	}
	var owner *identity.Scope
	if sc, ok := s.ownerScope(r); ok {
		owner = &sc
	} else if s.multiuser() {
		return nil, scheduleHTTPError{401, errors.New("请先登录")}
	}
	if b.BudgetUSD != 0 {
		return nil, errors.New("Agent 计划暂不支持独立金额预算，请使用轮次上限")
	}
	if b.Timezone == "" {
		return nil, errors.New("请选择明确的 IANA 时区")
	}
	base, _, err := s.mgr.scheduleWorkspace(owner, "", true)
	if err != nil {
		return nil, err
	}
	target := filepath.Join(base, "agents", a.ID)
	if !validAgentDirName(a.ID) {
		return nil, errors.New("invalid Agent workspace")
	}
	if old != nil {
		target = old.Workdir
	}
	if b.SourceSessionID != "" {
		binding, found := s.mgr.studio.binding(b.SourceSessionID)
		if !found || binding.Agent.ID != a.ID || !s.ownsSession(r, b.SourceSessionID) {
			return nil, scheduleHTTPError{404, ErrNotFound}
		}
		if err := s.mgr.rejectBackgroundConversation(b.SourceSessionID); err != nil {
			return nil, scheduleHTTPError{http.StatusConflict, err}
		}
		sess, err := s.mgr.Get(b.SourceSessionID)
		if err != nil {
			return nil, err
		}
		target = sess.cwd
	}
	_, target, err = s.mgr.scheduleWorkspace(owner, target, true)
	if err != nil {
		return nil, err
	}
	if b.Workdir != "" && filepath.Clean(b.Workdir) != filepath.Clean(target) {
		return nil, errors.New("Agent 工作区必须来自当前 Agent 的可信会话")
	}
	b.Workdir = target
	if err = s.mgr.prepareBoundScheduleInput(owner, target, a, b); err != nil {
		return nil, err
	}
	return owner, nil
}

// Legacy explicit /skill input is represented by the same typed atom used by
// the composer, so it passes the normal pinned selection check on every run.
func scheduleInvocationParts(prompt string, parts []inputref.Part) []inputref.Part {
	if parts == nil {
		if name, extra, ok := skills.ParseCommand(strings.TrimSpace(prompt)); ok {
			return []inputref.Part{{Type: "skill", Name: name}, {Type: "text", Text: extra}}
		}
		return nil
	}
	out := inputref.Clone(parts)
	for _, p := range inputref.Flatten(parts) {
		if p.Type == "skill" {
			return out
		}
	}
	for i, p := range out {
		if p.Type == "text" && strings.TrimSpace(p.Text) != "" {
			if name, extra, ok := skills.ParseCommand(strings.TrimSpace(p.Text)); ok {
				replacement := []inputref.Part{{Type: "skill", Name: name}, {Type: "text", Text: extra}}
				out = append(append(out[:i:i], replacement...), out[i+1:]...)
			}
			break
		}
	}
	return out
}
func (s *Server) handleScheduleGetJob(w http.ResponseWriter, r *http.Request) {
	j, ok := s.scheduleFindJob(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	writeJSON(w, 200, map[string]any{"job": s.scheduleJobViews([]schedule.Job{j})[0]})
}
func (s *Server) handleScheduleUpdateJob(w http.ResponseWriter, r *http.Request) {
	old, ok := s.scheduleFindJob(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if s.mgr.SchedRunning()[old.ID] {
		writeErr(w, 409, schedule.ErrRunning)
		return
	}
	var body scheduleJobBody
	if err := decodeScheduleBody(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	if body.Revision == nil {
		writeErr(w, 400, errors.New("revision is required"))
		return
	}
	owner, err := s.prepareScheduleBody(r, &body, &old)
	if err != nil {
		scheduleAPIError(w, err)
		return
	}
	next, _, err := body.build(time.Now())
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	next.Owner = owner
	var installer func(schedule.Job, schedule.Job) error
	if next.AgentID == "" && (old.Timezone == "" || next.Timezone == "") {
		be, e := s.scheduleBackend(schedule.DefaultDir())
		if e != nil {
			writeErr(w, 503, e)
			return
		}
		exe, e := os.Executable()
		if e != nil {
			writeErr(w, 500, e)
			return
		}
		installer = func(previous, replacement schedule.Job) error {
			if previous.Timezone == "" {
				if e := be.Unregister(previous); e != nil {
					return e
				}
			}
			if replacement.Timezone == "" {
				if e := be.Register(replacement, exe); e != nil {
					if previous.Timezone == "" {
						if restore := be.Register(previous, exe); restore != nil {
							return errors.Join(e, fmt.Errorf("restore original trigger: %w", restore))
						}
					}
					return e
				}
			}
			return nil
		}
	}
	// Trigger installation and CAS share the execution lease; failed registration
	// retains the original definition and explicitly restores its old trigger.
	next, err = s.scheduleStore().ReplaceDefinition(old.ID, *body.Revision, next, schedule.DefinitionOptions{Install: installer})
	if err != nil {
		scheduleAPIError(w, err)
		return
	}

	writeJSON(w, 200, map[string]any{"job": s.scheduleJobViews([]schedule.Job{next})[0]})
}
