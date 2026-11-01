package web

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/yosukeno/pogo/mcpobserve"
)

// Agent history is an account-private projection of the observation store, not
// a list of the current conversation. Unknown old calls are deliberately never
// inferred from a supplied session id, current Agent, or prompt text.
func (s *Server) agentMCPFilter(w http.ResponseWriter, r *http.Request) (mcpobserve.Filter, bool) {
	a, ok := s.studioApp(w, r)
	if !ok {
		return mcpobserve.Filter{}, false
	}
	f, err := parseMCPCallFilter(r)
	if err != nil {
		writeErr(w, 400, err)
		return f, false
	}
	owner := ""
	if scope, present := s.ownerScope(r); present && scope.Valid() {
		owner = scope.SubjectID
	} else if s.multiuser() {
		writeErr(w, 401, errors.New("请先登录"))
		return f, false
	}
	if (f.AgentID != "" && f.AgentID != a.ID) || (f.OwnerID != "" && f.OwnerID != owner) {
		writeErr(w, 400, errors.New("call filters cannot change the current account or Agent"))
		return f, false
	}
	f.AgentID, f.OwnerID, f.ExactOwner = a.ID, owner, true
	return f, true
}
func (s *Server) handleAgentMCPCalls(w http.ResponseWriter, r *http.Request) {
	f, ok := s.agentMCPFilter(w, r)
	if !ok {
		return
	}
	ctx, cancel, ok := s.monitorContext(w, r)
	if !ok {
		return
	}
	defer cancel()
	page, err := s.mcpCalls.Query(ctx, f)
	if err != nil {
		writeMonitorError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, page)
}
func (s *Server) handleAgentMCPStats(w http.ResponseWriter, r *http.Request) {
	f, ok := s.agentMCPFilter(w, r)
	if !ok {
		return
	}
	ctx, cancel, ok := s.monitorContext(w, r)
	if !ok {
		return
	}
	defer cancel()
	f.Before = ""
	stats, err := s.mcpCalls.Stats(ctx, f)
	if err != nil {
		writeMonitorError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, stats)
}
func (s *Server) handleAgentMCPDetail(w http.ResponseWriter, r *http.Request) {
	f, ok := s.agentMCPFilter(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !observationID.MatchString(id) {
		writeErr(w, 400, errors.New("invalid call id"))
		return
	}
	ctx, cancel, ok := s.monitorContext(w, r)
	if !ok {
		return
	}
	defer cancel()
	// A detail opened from an older time range remains addressable; only trusted
	// account+Agent constrain identity. Never let default 24h hide that selection.
	f = mcpobserve.Filter{AgentID: f.AgentID, OwnerID: f.OwnerID, ExactOwner: true}
	call, err := s.mcpCalls.DetailScoped(ctx, id, f)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, 404, errors.New("call not found"))
		return
	}
	if err != nil {
		writeMonitorError(w, err)
		return
	}
	call = s.historicalMCPDiagnostic(ctx, call)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"call": call})
}
