package web

import (
	"errors"
	"io"
	"net/http"

	af "github.com/yosukeno/pogo/agentworkflow"
	"github.com/yosukeno/pogo/taskqueue"
)

// Presentation edits have a separate route and storage revision. They never
// enter the semantic workflow edit service or the model-facing native tools.
func (s *Server) registerAgentWorkflowLayoutRoutes() {
	s.mux.HandleFunc("GET /api/apps/{aid}/agent-workflows/{wid}/layout", s.handleAgentWorkflowLayout)
	s.mux.HandleFunc("PUT /api/apps/{aid}/agent-workflows/{wid}/layout", s.handleAgentWorkflowLayout)
}

func (s *Server) handleAgentWorkflowLayout(w http.ResponseWriter, r *http.Request) {
	value, err := s.agentWorkflowLayout(r)
	if err != nil {
		status := http.StatusBadRequest
		var denied *awHTTPError
		if errors.As(err, &denied) {
			status = denied.status
		} else if errors.Is(err, taskqueue.ErrNotFound) || errors.Is(err, ErrNotFound) {
			status = http.StatusNotFound
		} else if errors.Is(err, taskqueue.ErrConflict) || errors.Is(err, af.ErrConflict) {
			status = http.StatusConflict
		}
		writeErr(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) agentWorkflowLayout(r *http.Request) (map[string]any, error) {
	owner, ok := s.taskCenterOwner(r)
	aid, wid := r.PathValue("aid"), r.PathValue("wid")
	if !ok || !validID(aid) || !validID(wid) {
		return nil, ErrNotFound
	}
	write := r.Method == http.MethodPut
	if active, _ := s.mgr.releaseMaintenance(); write && active {
		return nil, &awHTTPError{http.StatusServiceUnavailable, "服务维护中，请稍后保存布局"}
	}
	s.mgr.mu.Lock()
	valid := s.mgr.scheduleOwnerValid
	s.mgr.mu.Unlock()
	if valid == nil || !valid(owner) {
		return nil, awDenied("当前账号不可用")
	}
	a, available := s.mgr.studio.get(aid)
	available = available && a.Status == "ready"
	a.Tools = s.mgr.studioNativeSelection(a)
	caps := awCaps(a, available)
	if write && !caps.Edit {
		return nil, awDenied("当前 Agent 未授权编辑内部工作流布局")
	}
	db, err := s.mgr.taskRuns.database(true)
	if err != nil {
		return nil, err
	}
	key := sessionResultOwnerKey(owner)
	result := map[string]any{"ok": true, "agent_id": aid, "capabilities": caps}
	if !write {
		layout, err := db.AWLayout(key, aid, wid)
		if err != nil {
			return nil, err
		}
		result["layout"] = layout
		return result, nil
	}
	d, err := db.AWGet(key, aid, wid)
	if err != nil {
		return nil, err
	}
	if d.Archived || d.Deleted {
		return nil, awDenied("已归档的内部工作流不能修改布局")
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return nil, &awHTTPError{http.StatusBadRequest, "布局请求过大或无法读取"}
	}
	var req af.LayoutSave
	if err = awDecode(raw, &req); err != nil {
		return nil, err
	}
	layout, reused, err := db.AWSaveLayout(key, aid, wid, req)
	if err != nil {
		return nil, err
	}
	result["layout"], result["reused"] = layout, reused
	return result, nil
}
