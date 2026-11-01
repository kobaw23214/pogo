package web

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yosukeno/pogo/mcpobserve"
)

// Close releases observability resources. Call after stopping HTTP admission
// and draining the Manager; Serve does this automatically. The shared SQLite
// database stays open until the metadata writer has flushed its last events.
func (s *Server) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.unbindWorkflowAuthoringTools()
		if s.mgr != nil {
			s.mgr.mu.Lock()
			if s.mgr.agentWorkflowHost == s {
				s.mgr.agentWorkflowHost = nil
			}
			s.mgr.mu.Unlock()
		}
		s.stopWorkflowCallbacks()
		s.closeHTMLPreviews()
		s.closeOffice()
		s.closeBrowserLive()
		if s.observation != nil {
			s.observation.Close()
		} else {
			// Covers callers constructing a Server directly in focused tests.
			if s.skillCalls != nil {
				s.skillCalls.Close()
			}
			if s.mcpCalls != nil {
				s.mcpCalls.Close()
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.otlp.Shutdown(ctx)
		s.mcplog.close()
	})
}

var observationID = regexp.MustCompile(`^[a-f0-9]{32}$`)

const mcpObservationRequestHeader = "X-Pogo-Mcp-Observation-Request"

func parseMCPCallFilter(r *http.Request) (mcpobserve.Filter, error) {
	q := r.URL.Query()
	now := time.Now().UnixMilli()
	f := mcpobserve.Filter{From: now - int64(24*time.Hour/time.Millisecond), To: now, Limit: 50}
	allowed := map[string]bool{"from": true, "to": true, "limit": true, "before": true, "direction": true, "entrypoint": true, "client_id": true, "server": true, "tool": true, "status": true, "session_id": true, "run_id": true, "task_id": true, "trace_id": true, "owner_id": true, "agent_id": true, "client": true, "q": true}
	for key, values := range q {
		if !allowed[key] || len(values) != 1 {
			return f, fmt.Errorf("invalid filter: %s", key)
		}
	}
	for key, dst := range map[string]*string{"direction": &f.Direction, "server": &f.Server, "tool": &f.Tool, "status": &f.Status,
		"entrypoint": &f.Entrypoint, "client_id": &f.ClientID, "session_id": &f.SessionID, "run_id": &f.RunID, "task_id": &f.TaskID, "trace_id": &f.TraceID, "owner_id": &f.OwnerID, "agent_id": &f.AgentID, "client": &f.Client, "q": &f.Query, "before": &f.Before} {
		v := strings.TrimSpace(q.Get(key))
		if !utf8.ValidString(v) || len(v) > 1024 {
			return f, fmt.Errorf("invalid filter: %s", key)
		}
		*dst = v
	}
	if f.Direction != "" && f.Direction != "inbound" && f.Direction != "outbound" {
		return f, errors.New("invalid direction")
	}
	switch f.Entrypoint {
	case "", "pogo", "mcp", "cli", "unknown":
	default:
		return f, errors.New("invalid entrypoint")
	}
	switch f.Status {
	case "", "running", "ok", "error", "cancelled", "timeout", "interrupted":
	default:
		return f, errors.New("invalid status")
	}
	for key, dst := range map[string]*int64{"from": &f.From, "to": &f.To} {
		if v, ok := q[key]; ok {
			n, err := strconv.ParseInt(v[0], 10, 64)
			if err != nil || n < 0 || n > 253402300799999 {
				return f, fmt.Errorf("invalid time: %s", key)
			}
			*dst = n
		}
	}
	if f.From > f.To {
		return f, errors.New("from must not exceed to")
	}
	if v, ok := q["limit"]; ok {
		n, err := strconv.Atoi(v[0])
		if err != nil || n < 1 || n > 200 {
			return f, errors.New("limit must be between 1 and 200")
		}
		f.Limit = n
	}
	return f, nil
}

func (s *Server) monitorContext(w http.ResponseWriter, r *http.Request) (context.Context, context.CancelFunc, bool) {
	if s.mcpCalls == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("MCP call monitoring is unavailable"))
		return nil, nil, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	return ctx, cancel, true
}

func writeMonitorError(w http.ResponseWriter, err error) {
	if errors.Is(err, mcpobserve.ErrInvalidFilter) || errors.Is(err, mcpobserve.ErrInvalidCursor) {
		writeErr(w, http.StatusBadRequest, errors.New("invalid call filters or cursor"))
		return
	}
	writeErr(w, http.StatusServiceUnavailable, errors.New("MCP call history query failed; retry later"))
}

func (s *Server) handleMCPCalls(w http.ResponseWriter, r *http.Request) {
	f, err := parseMCPCallFilter(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
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
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleMCPCallStats(w http.ResponseWriter, r *http.Request) {
	f, err := parseMCPCallFilter(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
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
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleMCPCallDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !observationID.MatchString(id) {
		writeErr(w, http.StatusBadRequest, errors.New("invalid call id"))
		return
	}
	ctx, cancel, ok := s.monitorContext(w, r)
	if !ok {
		return
	}
	defer cancel()
	call, err := s.mcpCalls.Detail(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, errors.New("call not found"))
		return
	}
	if err != nil {
		writeMonitorError(w, err)
		return
	}
	call = s.historicalMCPDiagnostic(ctx, call)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"call": call})
}
