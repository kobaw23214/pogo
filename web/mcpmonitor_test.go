package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/mcpobserve"
)

func TestMCPMonitorHTTPHistoryAndFilters(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("done")))
	s, err := NewServer(h.mgr, ServerOptions{Token: h.token, MCP: &MCPOptions{}, MCPLog: &MCPLogOptions{DBPath: filepath.Join(t.TempDir(), "calls.db"), Ring: 2}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	var ids []string
	now := time.Now().Add(-time.Minute).UnixMilli()
	for i := 0; i < 7; i++ {
		id := mcpobserve.NewID()
		ids = append(ids, id)
		status := "ok"
		if i%2 == 0 {
			status = "error"
		}
		s.mcpCalls.Record(mcpobserve.Call{ID: id, TraceID: mcpobserve.NewID(), Direction: "outbound", Server: "fixture", Tool: "echo", StartedAt: now + int64(i), FinishedAt: now + int64(i) + 10, DurationMs: 10, Status: status, SessionID: "local-session"})
	}
	if err := s.mcpCalls.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	get := func(path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	first := get("/api/mcp/calls?limit=2&direction=outbound&server=fixture", h.token)
	if first.Code != 200 {
		t.Fatalf("list: %d %s", first.Code, first.Body)
	}
	var page mcpobserve.Page
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	// History must not shrink to the two-row memory ring.
	if len(page.Calls) != 2 || page.NextCursor == "" || page.Storage != "sqlite" {
		t.Fatalf("bad page: %s", first.Body)
	}
	stats := get("/api/mcp/calls/stats?limit=1&direction=outbound&server=fixture", h.token)
	var summary map[string]any
	json.Unmarshal(stats.Body.Bytes(), &summary)
	if stats.Code != 200 || summary["total"] != float64(7) || summary["errors"] != float64(4) {
		t.Fatalf("stats did not cover all filtered rows: %s", stats.Body)
	}
	next := get("/api/mcp/calls?limit=2&direction=outbound&server=fixture&before="+page.NextCursor, h.token)
	var second mcpobserve.Page
	json.Unmarshal(next.Body.Bytes(), &second)
	if next.Code != 200 || len(second.Calls) != 2 || second.Calls[0].ID == page.Calls[0].ID {
		t.Fatalf("bad pagination: %s", next.Body)
	}
	if detail := get("/api/mcp/calls/"+ids[0], h.token); detail.Code != 200 {
		t.Fatalf("durable detail: %s", detail.Body)
	}
	for _, path := range []string{"/api/mcp/calls?direction=unknown", "/api/mcp/calls?limit=201", "/api/mcp/calls?from=10&to=9", "/api/mcp/calls?before=invalid!", "/api/mcp/calls?status=ok&status=error", "/api/mcp/calls?typo=x", "/api/mcp/calls/not-an-id"} {
		if got := get(path, h.token); got.Code != 400 {
			t.Errorf("%s returned %d: %s", path, got.Code, got.Body)
		}
	}
	for _, path := range []string{"/api/mcp/calls", "/api/mcp/calls/stats", "/api/mcp/calls/" + ids[0]} {
		if got := get(path, ""); got.Code != 401 {
			t.Errorf("unauthenticated %s: %d", path, got.Code)
		}
	}
	if first.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("history must not be cached")
	}
}

func TestMCPMonitorAdminBoundaryAndDisable(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("done")))
	s, err := NewServer(h.mgr, ServerOptions{Token: h.token, MCP: &MCPOptions{}, MCPLog: &MCPLogOptions{CallsOff: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if s.mcpCalls != nil {
		t.Fatal("CallsOff still initialized monitor")
	}
	r := httptest.NewRequest("GET", "/api/mcp/calls", nil)
	r.Header.Set("Authorization", "Bearer "+h.token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled: %d %s", w.Code, w.Body)
	}
	// Isolate the existing admin middleware after authentication. Neither a
	// local non-admin nor a fixed-token caller gets monitor access.
	s.auth.path = "test-users-enabled"
	for _, id := range []*requestIdentity{{Local: true, Username: "local-user"}, {Username: "token-bound"}} {
		for _, handler := range []http.HandlerFunc{s.handleMCPCalls, s.handleMCPCallStats, s.handleMCPCallDetail} {
			req := httptest.NewRequest("GET", "/api/mcp/calls", nil)
			req = req.WithContext(context.WithValue(req.Context(), requestIdentityKey, id))
			out := httptest.NewRecorder()
			s.admin(handler)(out, req)
			if out.Code != 403 || !strings.Contains(out.Body.String(), "admin") {
				t.Fatalf("non-admin admitted: %d", out.Code)
			}
		}
	}
}
