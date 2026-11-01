package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/observability"
	"github.com/yosukeno/pogo/skillobserve"
)

func TestWebObservabilityWithoutMCPServing(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("done")))
	s, err := NewServer(h.mgr, ServerOptions{Token: h.token})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if s.mcp != nil || s.mcpCalls == nil || s.skillCalls == nil {
		t.Fatal("outbound observation must work without the incoming MCP server")
	}
	ctx := mcpobserve.WithRecorder(context.Background(), h.mgr.MCPRecorder())
	ctx = skillobserve.WithRecorder(ctx, h.mgr.SkillRecorder())
	ctx = mcpobserve.WithMetadata(ctx, mcpobserve.Metadata{Entrypoint: "pogo", ClientID: "trusted-client"})
	ctx, call := mcpobserve.Start(ctx, mcpobserve.Call{Direction: "outbound", Server: "fixture", Tool: "echo"})
	call.End("ok", "")
	page, err := s.mcpCalls.Query(context.Background(), mcpobserve.Filter{ClientID: "trusted-client", Entrypoint: "pogo"})
	if err != nil || len(page.Calls) != 1 || page.Calls[0].Status != "ok" {
		t.Fatalf("observation: %+v %v", page, err)
	}
	if page.Calls[0].SpanID == "" || mcpobserve.TraceMeta(ctx)["traceparent"] == nil {
		t.Fatal("standard trace missing")
	}
	get := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/observability/status", nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	if got := get(""); got.Code != 401 {
		t.Fatalf("unauthenticated health: %d", got.Code)
	}
	w := get(h.token)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatalf("health: %d %s", w.Code, w.Body)
	}
}

func TestWebObservabilitySharesLiveRuntimeAndExports(t *testing.T) {
	collected := make(chan []byte, 1)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" || r.Header.Get("Authorization") != "Bearer collector-fixture" {
			t.Error("invalid exporter request")
		}
		body, _ := io.ReadAll(r.Body)
		collected <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer collector.Close()
	h := newHarness(t, scriptedTurns(textTurn("done")))
	path := filepath.Join(t.TempDir(), "shared.db")
	s, err := NewServer(h.mgr, ServerOptions{Token: h.token,
		MCPLog: &MCPLogOptions{DBPath: path}, OTLP: &observability.OTLPOptions{
			Endpoint: collector.URL + "/v1/traces", Headers: map[string]string{"Authorization": "Bearer collector-fixture"}, FlushInterval: time.Minute,
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	ctx := mcpobserve.WithRecorder(context.Background(), h.mgr.MCPRecorder())
	ctx = mcpobserve.WithMetadata(ctx, mcpobserve.Metadata{Entrypoint: "pogo", OwnerID: "private-owner"})
	_, call := mcpobserve.Start(ctx, mcpobserve.Call{Direction: "outbound", Server: "fixture", Tool: "echo"})
	if err := s.mcpCalls.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	db, err := observability.OpenDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cli, err := observability.Open(observability.Options{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	page, err := cli.MCP.Query(context.Background(), mcpobserve.Filter{})
	if err != nil || len(page.Calls) != 1 || page.Calls[0].Status != "running" {
		t.Fatalf("CLI interrupted live Web call: %+v %v", page, err)
	}
	call.End("ok", "")
	s.Close() // terminal event reaches OTLP before shutdown completes
	var body []byte
	select {
	case body = <-collected:
	case <-time.After(time.Second):
		t.Fatal("collector received no export")
	}
	if !json.Valid(body) || !strings.Contains(string(body), `"resourceSpans"`) {
		t.Fatalf("no real OTLP envelope: %s", body)
	}
	for _, secret := range []string{"private-owner", "collector-fixture", collector.URL} {
		if strings.Contains(string(body), secret) {
			t.Fatal("export included private data")
		}
	}
	if s.otlp.Stats().Exported != 1 {
		t.Fatalf("export stats: %+v", s.otlp.Stats())
	}
}

func TestObservabilityFiltersAcceptCLIAndClientIdentity(t *testing.T) {
	f, err := parseMCPCallFilter(httptest.NewRequest("GET", "/?entrypoint=cli&client_id=client-a", nil))
	if err != nil || f.Entrypoint != "cli" || f.ClientID != "client-a" {
		t.Fatalf("MCP filter: %+v %v", f, err)
	}
	g, err := parseSkillEventFilter(httptest.NewRequest("GET", "/?entrypoint=cli&client_id=client-a", nil))
	if err != nil || g.Entrypoint != "cli" || g.ClientID != "client-a" {
		t.Fatalf("skill filter: %+v %v", g, err)
	}
	for _, query := range []string{"entrypoint=other", "client_id=a&client_id=b", "entrypoint=cli&entrypoint=pogo"} {
		if _, err := parseMCPCallFilter(httptest.NewRequest("GET", "/?"+query, nil)); err == nil {
			t.Errorf("MCP accepted %s", query)
		}
		if _, err := parseSkillEventFilter(httptest.NewRequest("GET", "/?"+query, nil)); err == nil {
			t.Errorf("skill accepted %s", query)
		}
	}
}

func TestWebInvalidOTLPStillRecordsLocally(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("done")))
	s, err := NewServer(h.mgr, ServerOptions{Token: h.token, OTLP: &observability.OTLPOptions{Endpoint: "bad-url"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.otlpConfigError || s.mcpCalls == nil || s.skillCalls == nil || s.otlp != nil {
		t.Fatal("invalid exporter disabled local recording")
	}
}

// The runtime mechanism and bundle source are first-class filter values:
// packaged runtime Skill executions must be selectable on the monitor API.
func TestSkillEventFilterAcceptsRuntimeAndBundle(t *testing.T) {
	f, err := parseSkillEventFilter(httptest.NewRequest("GET", "/?mechanism=runtime&skill_source=bundle", nil))
	if err != nil || f.Mechanism != "runtime" || f.SkillSource != "bundle" {
		t.Fatalf("filter: %+v %v", f, err)
	}
	for _, query := range []string{"mechanism=bogus", "skill_source=bogus"} {
		if _, err := parseSkillEventFilter(httptest.NewRequest("GET", "/?"+query, nil)); err == nil {
			t.Errorf("accepted %s", query)
		}
	}
}
