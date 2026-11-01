package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/yosukeno/pogo/skillobserve"
)

func TestSkillMonitorHTTPStageStatsAndIsolation(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("ok")))
	s, err := NewServer(h.mgr, ServerOptions{Token: h.token, MCP: &MCPOptions{Token: h.token}, MCPLog: &MCPLogOptions{DBPath: filepath.Join(t.TempDir(), "observe.db"), Ring: 2, PayloadOff: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.mgr.Close(); s.Close() })
	base := time.Now().Add(-time.Minute).UnixMilli()
	for i := 0; i < 10; i++ {
		stage, status := "discovery", "ok"
		if i >= 4 {
			stage = "load"
		}
		if i >= 6 {
			stage = "execute"
			status = []string{"ok", "error", "cancelled", "timeout"}[i-6]
		}
		s.skillCalls.Record(skillobserve.Event{ID: fmt.Sprintf("%032x", i+1), TraceID: "fixture-trace", SkillID: "fixture-id", SkillName: "fixture", SkillSource: "path", Stage: stage, Caller: "internal", Entrypoint: "pogo", Mechanism: "catalog", StartedAt: base + int64(i), FinishedAt: base + int64(i) + 10, DurationMs: 10, Status: status})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.skillCalls.Flush(ctx); err != nil {
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
	decode := func(w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		if w.Code != 200 {
			t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
		}
		var v map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	page := decode(get("/api/skills/events?limit=2", h.token))
	if len(page["events"].([]any)) != 2 || page["storage"] != "sqlite" {
		t.Fatalf("bad page: %+v", page)
	}
	cursor := page["next_cursor"].(string)
	if cursor == "" {
		t.Fatal("SQLite history lost rows beyond ring capacity")
	}
	second := decode(get("/api/skills/events?limit=2&before="+cursor, h.token))
	if page["events"].([]any)[1].(map[string]any)["id"] == second["events"].([]any)[0].(map[string]any)["id"] {
		t.Fatal("cursor repeated row")
	}
	st := decode(get("/api/skills/events/stats?limit=1", h.token))
	if st["total"] != float64(10) || st["discoveries"] != float64(4) || st["loads"] != float64(2) || st["executions"] != float64(4) || st["execution_errors"] != float64(2) {
		t.Fatalf("stage counts contaminated: %+v", st)
	}
	if rate := st["error_rate"].(float64); rate < 0.666 || rate > 0.667 {
		t.Fatalf("non-execution or cancelled event entered rate: %+v", st)
	}
	loads := decode(get("/api/skills/events/stats?stage=load", h.token))
	if loads["executions"] != float64(0) || loads["error_rate"] != nil || loads["p95_ms"] != nil {
		t.Fatalf("load represented as execution success: %+v", loads)
	}
	if mcp := decode(get("/api/mcp/calls/stats", h.token)); mcp["total"] != float64(0) {
		t.Fatalf("skill event entered MCP totals: %+v", mcp)
	}
	if w := get("/api/skills/events/00000000000000000000000000000001", h.token); w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("persistent detail: %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{"/api/skills/events?caller=forged", "/api/skills/events?stage=success", "/api/skills/events?limit=999", "/api/skills/events?from=2&to=1", "/api/skills/events?before=broken", "/api/skills/events?stage=load&stage=execute", "/api/skills/events?payload=secret"} {
		if w := get(path, h.token); w.Code != 400 {
			t.Errorf("%s returned %d", path, w.Code)
		}
	}
	if w := get("/api/skills/events/ffffffffffffffffffffffffffffffff", h.token); w.Code != 404 {
		t.Errorf("missing detail=%d", w.Code)
	}
	if w := get("/api/skills/events", ""); w.Code != 401 {
		t.Errorf("unauthenticated=%d", w.Code)
	}
}

func TestSkillMonitorWorksWithoutMCPAndRespectsAdminAndDisable(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("ok")))
	s, err := NewServer(h.mgr, ServerOptions{Token: h.token})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.skillCalls == nil || s.mcp != nil {
		t.Fatal("skill monitor incorrectly depends on MCP serving")
	}
	// Use the same local/delegated admin policy as the settings configuration.
	s.auth.path = "enabled-test-only"
	for _, id := range []*requestIdentity{{Local: true, Username: "local-user"}, {Username: "token-bound"}} {
		r := httptest.NewRequest(http.MethodGet, "/api/skills/events", nil)
		r = r.WithContext(context.WithValue(r.Context(), requestIdentityKey, id))
		w := httptest.NewRecorder()
		s.admin(s.handleSkillEvents)(w, r)
		if w.Code != 403 {
			t.Fatalf("non-admin monitoring access=%d", w.Code)
		}
	}
	disabled, err := NewServer(h.mgr, ServerOptions{MCPLog: &MCPLogOptions{SkillCallsOff: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer disabled.Close()
	w := httptest.NewRecorder()
	disabled.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/skills/events", nil))
	if w.Code != 503 {
		t.Fatalf("disabled=%d", w.Code)
	}
}
