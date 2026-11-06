package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/tools"
	"github.com/yosukeno/pogo/webservice"
)

type webServiceHostRunner struct {
	mu      sync.Mutex
	running map[string]bool
}

func (*webServiceHostRunner) Preflight(context.Context) error { return nil }
func (r *webServiceHostRunner) Start(_ context.Context, l webservice.Launch) (webservice.Instance, error) {
	r.mu.Lock()
	r.running[l.Record.ServiceID] = true
	r.mu.Unlock()
	return webservice.Instance{ID: l.Record.ServiceID, Upstream: "runtime:8080"}, nil
}
func (r *webServiceHostRunner) Inspect(_ context.Context, v webservice.Record) (webservice.RuntimeStatus, error) {
	r.mu.Lock()
	on := r.running[v.ServiceID]
	r.mu.Unlock()
	return webservice.RuntimeStatus{Exists: on, Running: on, Instance: v.Instance}, nil
}
func (r *webServiceHostRunner) Stop(_ context.Context, v webservice.Record) error {
	r.mu.Lock()
	delete(r.running, v.ServiceID)
	r.mu.Unlock()
	return nil
}
func (*webServiceHostRunner) Renew(context.Context, webservice.Record) error { return nil }
func (*webServiceHostRunner) Logs(_ context.Context, v webservice.Record, _ string, _ int) (webservice.LogPage, error) {
	return webservice.LogPage{Generation: v.Generation, Text: "owned-service-output"}, nil
}
func (*webServiceHostRunner) DeleteData(context.Context, webservice.Record) error { return nil }

type webServiceHostGateway struct{}

func (webServiceHostGateway) Preflight(context.Context) error                 { return nil }
func (webServiceHostGateway) Apply(context.Context, webservice.Record) error  { return nil }
func (webServiceHostGateway) Revoke(context.Context, webservice.Record) error { return nil }
func (webServiceHostGateway) Probe(_ context.Context, v webservice.Record) (webservice.Health, error) {
	return webservice.Health{Generation: v.Generation, UpstreamStatus: 200, GatewayStatus: 200, Result: "passed"}, nil
}
func (webServiceHostGateway) Open(_ context.Context, v webservice.Record) (webservice.Grant, error) {
	return webservice.Grant{ServiceID: v.ServiceID, Generation: v.Generation, GrantID: "grant-test", PreviewURL: "https://temporary.preview.test:19770/", ExpiresAt: time.Now().Add(30 * time.Minute), LeaseExpiresAt: v.LeaseExpiresAt}, nil
}
func (webServiceHostGateway) CloseGrant(context.Context, webservice.Record, string) error { return nil }

func newWebServiceHostManager(t *testing.T) *webservice.Manager {
	t.Helper()
	manager, err := webservice.NewManager(webservice.Options{Root: filepath.Join(t.TempDir(), "private-services"), Installation: "host-test", Runner: &webServiceHostRunner{running: map[string]bool{}}, Gateway: webServiceHostGateway{}, PollInterval: 10 * time.Millisecond, HealthInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func TestWebServiceOwnerAPIUsesAuthenticatedConversationScope(t *testing.T) {
	managed := newWebServiceHostManager(t)
	h := newMultiuserHarness(t, func(int) (llm.Response, error) {
		t.Error("service management contacted model")
		return llm.Response{}, nil
	}, func(c *Config) { c.WebServices = managed })
	sid, other := h.createAs(aliceToken), h.createAs(aliceToken)
	sess := h.session(sid)
	if err := os.MkdirAll(filepath.Join(sess.cwd, "site"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sess.cwd, "site", "app.py"), []byte("print('fixture')\n"), 0600); err != nil {
		t.Fatal(err)
	}
	start := h.doAs(aliceToken, http.MethodPost, "/api/sessions/"+sid+"/web-services", `{"operation_id":"owner-start","name":"api","project_root":"site","profile":"python-web","start_argv":["python","app.py"]}`)
	data, _ := io.ReadAll(start.Body)
	start.Body.Close()
	if start.StatusCode != 200 {
		t.Fatalf("start: %d %s", start.StatusCode, data)
	}
	var receipt struct {
		ServiceID string `json:"service_id"`
	}
	if err := json.Unmarshal(data, &receipt); err != nil || receipt.ServiceID == "" {
		t.Fatalf("invalid service receipt: %s %v", data, err)
	}
	for _, forbidden := range []string{`"owner":`, `"session":`, `"installation":`, `"instance":`, "runtime:8080"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("private data leaked: %s", data)
		}
	}
	for _, tc := range []struct {
		token, sid, suffix string
		status             int
	}{
		{aliceToken, sid, "/" + receipt.ServiceID, 200},
		{bobToken, sid, "/" + receipt.ServiceID, 404},
		{aliceToken, other, "/" + receipt.ServiceID, 404},
		{aliceToken, sid, "/" + receipt.ServiceID + "/logs", 200},
		{bobToken, sid, "/" + receipt.ServiceID + "/logs", 404},
	} {
		response := h.doAs(tc.token, http.MethodGet, "/api/sessions/"+tc.sid+"/web-services"+tc.suffix, "")
		b, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != tc.status {
			t.Errorf("%+v: %d %s", tc, response.StatusCode, b)
		}
	}
	// Removing the Agent registry tool does not remove the human owner's cleanup
	// API. This call still targets the same host-injected owner and session.
	empty := []string{}
	if err := sess.agent.ReplaceNativeSelection(&empty, nil, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	response := h.doAs(aliceToken, http.MethodPost, "/api/sessions/"+sid+"/web-services/"+receipt.ServiceID+"/stop", `{"operation_id":"owner-stop"}`)
	b, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("owner stop: %d %s", response.StatusCode, b)
	}
}

func TestWebServiceIsUnavailableWithoutBackendAndInHostedExecution(t *testing.T) {
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { return llm.Response{}, nil })
	sid := h.createAs(aliceToken)
	response := h.doAs(aliceToken, http.MethodGet, "/api/sessions/"+sid+"/web-services", "")
	var unavailable struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.NewDecoder(response.Body).Decode(&unavailable); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable || unavailable.Code != "FEATURE_DISABLED" || !strings.Contains(unavailable.Error, "FEATURE_DISABLED") {
		t.Fatalf("unconfigured response: status=%d error=%+v", response.StatusCode, unavailable)
	}
	if slices.Contains(h.session(sid).agent.NativeToolNames(), tools.WebServiceName) {
		t.Fatal("unconfigured host registered a callable schema")
	}
	managed := newWebServiceHostManager(t)
	for _, kind := range []string{"workflow", "schedule", "sentinel"} {
		dir := t.TempDir()
		var store *session.Store
		var err error
		switch kind {
		case "workflow":
			store, err = session.CreateAgentWorkflowWebConversation(dir, dir, "k3", session.AgentWorkflowIdentity{RunID: "test-run"})
		case "schedule":
			store, err = session.CreateBackgroundTaskWebConversation(dir, dir, "k3", session.BackgroundTaskIdentity{Source: "schedule"})
		case "sentinel":
			store, err = session.CreateSentinelWebConversation(dir, dir, "k3", session.SentinelIdentity{WatchID: "test-watch"})
		}
		if err != nil {
			t.Fatal(err)
		}
		m := &Manager{cfg: Config{WebServices: managed}, webServicesReady: true}
		if m.webServiceBackend(&Session{ID: "hosted", store: store, mgr: m, cwd: dir}) != nil {
			t.Errorf("%s received detached service control", kind)
		}
	}
}

func TestWebServiceActionPolicyDoesNotTrustInvalidReadClaims(t *testing.T) {
	p := NewPolicy()
	for _, tc := range []struct {
		args   string
		review bool
	}{
		{`{"action":"list"}`, false},
		{`{"action":"status","service_id":"ws_test"}`, false},
		{`{"action":"logs","service_id":"ws_test"}`, false},
		{`{"action":"open","service_id":"ws_test","operation_id":"op1"}`, true},
		{`{"action":"status","service_id":"ws_test","owner":"forged"}`, true},
	} {
		if got := p.reviewsLocked(agent.GateRequest{ToolName: tools.WebServiceName, Args: json.RawMessage(tc.args)}); got != tc.review {
			t.Errorf("policy %s=%v", tc.args, got)
		}
	}
}

func TestWebServiceStudioGrantRefreshReachesExistingAndNewConversations(t *testing.T) {
	managed := newWebServiceHostManager(t)
	h, capture := newStudioHarness(t, []llm.Response{toolTurn(tools.WebServiceName, `{"action":"list"}`), textTurn("listed"), textTurn("revoked")}, func(c *Config) { c.WebServices = managed })
	definition := postStudioAgent(t, h, studioAgentCapsBody("Web operator", "ready", map[string]any{"tools": []string{}}), 201)
	oldSID := startStudioConversation(t, h, definition.ID)
	oldSession := h.session(oldSID)
	if slices.Contains(nativeCatalogHTTP(t, h, oldSID), tools.WebServiceName) {
		t.Fatal("ungranted managed tool advertised")
	}
	definition = patchStudioPolicy(t, h, definition, map[string]any{"tools": []string{tools.WebServiceName}})
	newSID := startStudioConversation(t, h, definition.ID)
	for _, sid := range []string{oldSID, newSID} {
		if !slices.Contains(nativeCatalogHTTP(t, h, sid), tools.WebServiceName) {
			t.Fatalf("grant not effective in %s", sid)
		}
	}
	if h.session(oldSID) != oldSession {
		t.Fatal("grant refresh recreated old conversation")
	}
	studioAsk(t, h, oldSID, "list this conversation's managed services")
	if got := capture.toolSchemas(); len(got) == 0 || !slices.Contains(got[0], tools.WebServiceName) {
		t.Fatal("actual model schema did not include granted tool")
	}
	definition = patchStudioPolicy(t, h, definition, map[string]any{"tools": []string{}})
	for _, sid := range []string{oldSID, newSID} {
		if slices.Contains(nativeCatalogHTTP(t, h, sid), tools.WebServiceName) {
			t.Fatalf("revoked tool still callable in %s", sid)
		}
	}
	studioAsk(t, h, oldSID, "show configuration after revocation")
	got := capture.toolSchemas()
	if slices.Contains(got[len(got)-1], tools.WebServiceName) {
		t.Fatal("revoked tool leaked to next actual model request")
	}
	if len(oldSession.agent.Messages()) == 0 {
		t.Fatal("grant refresh lost transcript history")
	}
}

func TestWebServiceHostRejectsControlStoreInsideFileToolRoots(t *testing.T) {
	workspace := t.TempDir()
	managed, err := webservice.NewManager(webservice.Options{Root: filepath.Join(workspace, "private"), Installation: "private-boundary", Runner: &webServiceHostRunner{running: map[string]bool{}}, Gateway: webServiceHostGateway{}})
	if err != nil {
		t.Fatal(err)
	}
	defer managed.Close()
	host := &Manager{cfg: Config{Cwd: workspace, WebServices: managed}}
	if err := host.prepareWebServicesHost(); err == nil {
		if host.webServicePrivateRelease != nil {
			host.webServicePrivateRelease()
		}
		t.Fatal("private service registry was placed inside file-tool workspace")
	}
	separate := newWebServiceHostManager(t)
	host = &Manager{cfg: Config{Cwd: workspace, WebServices: separate}, readRoots: []string{filepath.Dir(separate.Root())}}
	if err := host.prepareWebServicesHost(); err == nil {
		if host.webServicePrivateRelease != nil {
			host.webServicePrivateRelease()
		}
		t.Fatal("private registry reachable through an extra read root")
	}
}
