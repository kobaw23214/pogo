package web

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yosukeno/pogo/skillobserve"
)

type skillRecorderHolder struct{ recorder skillobserve.Recorder }

func (m *Manager) SetSkillRecorder(r skillobserve.Recorder) {
	if r == nil {
		m.skillRecorder.Store(nil)
		return
	}
	m.skillRecorder.Store(&skillRecorderHolder{r})
}

func (m *Manager) SkillRecorder() skillobserve.Recorder {
	if h := m.skillRecorder.Load(); h != nil {
		return h.recorder
	}
	return nil
}

func parseSkillEventFilter(r *http.Request) (skillobserve.Filter, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	now := time.Now().UnixMilli()
	f := skillobserve.Filter{From: now - int64(24*time.Hour/time.Millisecond), To: now, Limit: 50}
	if err != nil {
		return f, errors.New("invalid query encoding")
	}
	fields := map[string]*string{"stage": &f.Stage, "caller": &f.Caller, "entrypoint": &f.Entrypoint, "client_id": &f.ClientID, "skill_id": &f.SkillID, "skill_name": &f.SkillName, "skill_source": &f.SkillSource, "mechanism": &f.Mechanism, "tool": &f.Tool, "status": &f.Status, "session_id": &f.SessionID, "run_id": &f.RunID, "task_id": &f.TaskID, "trace_id": &f.TraceID, "owner_id": &f.OwnerID, "q": &f.Query, "before": &f.Before}
	for key, values := range q {
		if len(values) != 1 {
			return f, errors.New("duplicate filter")
		}
		if dst, ok := fields[key]; ok {
			v := strings.TrimSpace(values[0])
			if !utf8.ValidString(v) || len(v) > 1024 {
				return f, errors.New("invalid filter")
			}
			*dst = v
		} else if key != "from" && key != "to" && key != "limit" {
			return f, errors.New("unknown filter")
		}
	}
	for key, allowed := range map[string][]string{
		"stage": {"", "discovery", "load", "execute"}, "caller": {"", "internal", "external", "unknown"}, "entrypoint": {"", "pogo", "mcp", "cli", "unknown"},
		"status": {"", "running", "ok", "error", "cancelled", "timeout", "interrupted"}, "mechanism": {"", "catalog", "file_read", "explicit", "manifest", "runtime"},
		"skill_source": {"", "user", "project", "path", "embedded", "bundle", "unknown"},
	} {
		valid := false
		for _, v := range allowed {
			if *fields[key] == v {
				valid = true
				break
			}
		}
		if !valid {
			return f, fmt.Errorf("invalid %s", key)
		}
	}
	for key, dst := range map[string]*int64{"from": &f.From, "to": &f.To} {
		if values, ok := q[key]; ok {
			*dst, err = strconv.ParseInt(values[0], 10, 64)
			if err != nil || *dst < 0 || *dst > 253402300799999 {
				return f, errors.New("invalid time")
			}
		}
	}
	if f.From > f.To {
		return f, errors.New("from must not exceed to")
	}
	if values, ok := q["limit"]; ok {
		f.Limit, err = strconv.Atoi(values[0])
		if err != nil || f.Limit < 1 || f.Limit > 200 {
			return f, errors.New("limit must be between 1 and 200")
		}
	}
	return f, nil
}

func (s *Server) skillMonitorContext(w http.ResponseWriter, r *http.Request) (context.Context, context.CancelFunc, bool) {
	w.Header().Set("Cache-Control", "no-store")
	if s.skillCalls == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("skill monitoring is unavailable"))
		return nil, nil, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	return ctx, cancel, true
}

func writeSkillMonitorError(w http.ResponseWriter, err error) {
	if errors.Is(err, skillobserve.ErrInvalidFilter) || errors.Is(err, skillobserve.ErrInvalidCursor) {
		writeErr(w, http.StatusBadRequest, errors.New("invalid skill filters or cursor"))
		return
	}
	writeErr(w, http.StatusServiceUnavailable, errors.New("skill history query failed; retry later"))
}

func (s *Server) handleSkillEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	f, err := parseSkillEventFilter(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel, ok := s.skillMonitorContext(w, r)
	if !ok {
		return
	}
	defer cancel()
	page, err := s.skillCalls.Query(ctx, f)
	if err != nil {
		writeSkillMonitorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleSkillEventStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	f, err := parseSkillEventFilter(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel, ok := s.skillMonitorContext(w, r)
	if !ok {
		return
	}
	defer cancel()
	f.Before = ""
	stats, err := s.skillCalls.Stats(ctx, f)
	if err != nil {
		writeSkillMonitorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleSkillEventDetail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := r.PathValue("id")
	if !observationID.MatchString(id) {
		writeErr(w, http.StatusBadRequest, errors.New("invalid event id"))
		return
	}
	ctx, cancel, ok := s.skillMonitorContext(w, r)
	if !ok {
		return
	}
	defer cancel()
	event, err := s.skillCalls.Detail(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, errors.New("event not found"))
		return
	}
	if err != nil {
		writeSkillMonitorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"event": event})
}
