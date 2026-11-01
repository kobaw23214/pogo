package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
)

func officeReadinessTestBody() map[string]any {
	return map[string]any{"ready": true, "adapter": "pogo-office-history", "engineVersion": "9.4.0 (build:129)", "verifiedAssets": 9, "extensionSha256": strings.Repeat("a", 64)}
}

func officeReadinessTestService(engine *httptest.Server) *officeService {
	return &officeService{cfg: &officeConfig{serviceURL: engine.URL, client: engine.Client()}}
}

func officeReadinessTestExpire(o *officeService) {
	o.readinessMu.Lock()
	o.readinessUntil = time.Time{}
	o.readinessMu.Unlock()
}

func TestOfficeReadinessConfigurationErrorsDoNotFailCoreStartup(t *testing.T) {
	for _, key := range []string{"POGO_OFFICE_BROWSER_URL", "POGO_OFFICE_SERVICE_URL", "POGO_OFFICE_PUBLIC_URL", "POGO_OFFICE_FETCH_URL", "POGO_OFFICE_JWT_SECRET_FILE", "POGO_OFFICE_UNAVAILABLE_REASON"} {
		t.Setenv(key, "")
	}
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { return llm.Response{}, errors.New("no model requests allowed") })
	badSecret := filepath.Join(t.TempDir(), "invalid-jwt")
	if err := os.WriteFile(badSecret, []byte("short"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{filepath.Join(t.TempDir(), "missing-jwt"), badSecret} {
		s, err := NewServer(h.mgr, ServerOptions{Token: "core-token", Office: &OfficeOptions{ServiceURL: "http://editor.invalid", PublicURL: "http://pogo.invalid", JWTSecretFile: secret}})
		if err != nil {
			t.Fatalf("optional Office error stopped core: %v", err)
		}
		defer s.Close()
		request := httptest.NewRequest("GET", "/api/office/config", nil)
		request.Header.Set("Authorization", "Bearer core-token")
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, request)
		var status officeAvailability
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &status) != nil || status.State != "config_error" || !status.Configured || status.Ready || status.Enabled {
			t.Fatalf("configuration failure not isolated: %d %s", response.Code, response.Body)
		}
		if strings.Contains(response.Body.String(), secret) || strings.Contains(response.Body.String(), "editor.invalid") {
			t.Fatal("operator configuration escaped diagnostics")
		}
		request = httptest.NewRequest("POST", "/api/sessions", strings.NewReader(`{}`))
		request.Header.Set("Authorization", "Bearer core-token")
		response = httptest.NewRecorder()
		s.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusCreated {
			t.Fatalf("core conversation creation failed: %d %s", response.Code, response.Body)
		}
	}
	badUsers := filepath.Join(t.TempDir(), "invalid-users")
	if err := os.WriteFile(badUsers, []byte(`{`), 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := NewServer(h.mgr, ServerOptions{UsersFile: badUsers, Office: &OfficeOptions{JWTSecretFile: badSecret}}); err == nil {
		s.Close()
		t.Fatal("core authentication error was downgraded")
	}
}

func TestOfficeReadinessExplicitDeploymentErrorMarker(t *testing.T) {
	for _, key := range []string{"POGO_OFFICE_BROWSER_URL", "POGO_OFFICE_SERVICE_URL", "POGO_OFFICE_PUBLIC_URL", "POGO_OFFICE_FETCH_URL", "POGO_OFFICE_JWT_SECRET_FILE"} {
		t.Setenv(key, "")
	}
	t.Setenv("POGO_OFFICE_UNAVAILABLE_REASON", "config_error")
	s := &Server{}
	if err := s.initOffice(); err != nil {
		t.Fatal(err)
	}
	status := s.officeAvailability(context.Background())
	if status.State != "config_error" || !status.Configured || status.Ready || s.office != nil {
		t.Fatalf("marker was treated as no configuration: %+v", status)
	}
	response := httptest.NewRecorder()
	if s.officeAvailable(response) || response.Code != 503 || !strings.Contains(response.Body.String(), "OFFICE_UNAVAILABLE") {
		t.Fatal("unavailable Office API did not provide a clear error")
	}
}

func TestOfficeReadinessStartupDoesNotWaitForEngine(t *testing.T) {
	var calls atomic.Int32
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); <-r.Context().Done() }))
	defer engine.Close()
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { return llm.Response{}, nil })
	secret := filepath.Join(t.TempDir(), "jwt")
	if err := os.WriteFile(secret, []byte(strings.Repeat("s", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(h.mgr, ServerOptions{Office: &OfficeOptions{ServiceURL: engine.URL, PublicURL: "http://pogo.invalid", JWTSecretFile: secret}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if calls.Load() != 0 {
		t.Fatal("core startup probed/waited for Office")
	}
	start := time.Now()
	status := s.officeAvailability(context.Background())
	if status.Ready || !status.Enabled || !status.Configured || status.State != "unavailable" || time.Since(start) > 2*time.Second {
		t.Fatalf("upstream readiness was not bounded: %+v", status)
	}
}

func TestOfficeReadinessRejectsIncompleteHealthAndRedirects(t *testing.T) {
	for _, field := range []string{"ready", "adapter", "engineVersion", "verifiedAssets", "extensionSha256"} {
		t.Run(field, func(t *testing.T) {
			body := officeReadinessTestBody()
			delete(body, field)
			engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, body) }))
			defer engine.Close()
			if officeReadinessTestService(engine).editorReady(context.Background()) {
				t.Fatal("incomplete runtime identity accepted")
			}
		})
	}
	for _, body := range []string{`{"ready":true}`, `{"ready":true}junk`, strings.Repeat(" ", 5000)} {
		engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		if officeReadinessTestService(engine).editorReady(context.Background()) {
			t.Fatal("malformed/oversized readiness accepted")
		}
		engine.Close()
	}
	var redirected atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
		writeJSON(w, 200, officeReadinessTestBody())
	}))
	defer other.Close()
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 302) }))
	defer engine.Close()
	if officeReadinessTestService(engine).editorReady(context.Background()) || redirected.Load() != 0 {
		t.Fatal("readiness followed an unapproved redirect")
	}
}

func TestOfficeReadinessCacheAndConcurrentCallers(t *testing.T) {
	var calls atomic.Int32
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		writeJSON(w, 200, officeReadinessTestBody())
	}))
	defer engine.Close()
	o := officeReadinessTestService(engine)
	var group sync.WaitGroup
	for i := 0; i < 24; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if !o.editorReady(context.Background()) {
				t.Error("healthy runtime was not ready")
			}
		}()
	}
	group.Wait()
	if calls.Load() != 1 || !o.editorReady(context.Background()) || calls.Load() != 1 {
		t.Fatal("callers flooded the health endpoint instead of sharing cache")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if o.editorReady(canceled) {
		t.Fatal("a canceled open used a cached ready result")
	}
}

func TestOfficeReadinessRecoveryPreservesDirtyAndUnknownOperations(t *testing.T) {
	var healthy atomic.Bool
	var writes atomic.Int32
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/pogo-office/healthz" {
			writes.Add(1)
			http.Error(w, "no mutations", 500)
			return
		}
		body := officeReadinessTestBody()
		body["ready"] = healthy.Load()
		writeJSON(w, 200, body)
	}))
	defer engine.Close()
	o, document, _ := officeTestService(t)
	o.cfg.serviceURL, o.cfg.client = engine.URL, engine.Client()
	document.dirtyEpoch, document.persistedEpoch = 7, 3
	unknown := &officeOperation{id: "unknown_write", dispatched: true, done: make(chan struct{})}
	document.queue = []*officeOperation{unknown}
	s := &Server{office: o}
	if status := s.officeAvailability(context.Background()); status.Ready || !status.Enabled {
		t.Fatal("configuration was confused with runtime readiness")
	}
	healthy.Store(true)
	officeReadinessTestExpire(o)
	if status := s.officeAvailability(context.Background()); !status.Ready || status.State != "ready" {
		t.Fatal("service recovery required core restart")
	}
	if document.dirtyEpoch != 7 || document.persistedEpoch != 3 || len(document.queue) != 1 || document.queue[0] != unknown || !unknown.dispatched || writes.Load() != 0 {
		t.Fatal("availability retry changed dirty state or replayed an unknown write")
	}
}

func TestOfficeReadinessUnavailableOpenLeavesExistingStateReadable(t *testing.T) {
	var healthy atomic.Bool
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := officeReadinessTestBody()
		body["ready"] = healthy.Load()
		writeJSON(w, 200, body)
	}))
	defer engine.Close()
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { return llm.Response{}, errors.New("no model requests allowed") })
	secret := filepath.Join(t.TempDir(), "jwt")
	if err := os.WriteFile(secret, []byte(strings.Repeat("s", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	h.psrv.opts.Office = &OfficeOptions{ServiceURL: engine.URL, PublicURL: h.srv.URL, JWTSecretFile: secret}
	if err := h.psrv.initOffice(); err != nil {
		t.Fatal(err)
	}
	defer h.psrv.closeOffice()
	conversation := h.createAs(aliceToken)
	session := h.session(conversation)
	if err := os.WriteFile(filepath.Join(session.cwd, "probe.docx"), officeTestPackage(t, "keep my document"), 0600); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, h.srv.URL+path, strings.NewReader(body))
		if method == "POST" && path == "/api/office/sessions" && !healthy.Load() {
			ctx, cancel := context.WithTimeout(r.Context(), 100*time.Millisecond)
			defer cancel()
			r = r.WithContext(ctx)
		}
		r.Header.Set("Authorization", "Bearer "+aliceToken)
		w := httptest.NewRecorder()
		h.psrv.Handler().ServeHTTP(w, r)
		return w
	}
	body, _ := json.Marshal(map[string]any{"path": "probe.docx", "conversation_id": conversation, "instanceId": "owned_editor_readiness_test"})
	response := request("POST", "/api/office/sessions", string(body))
	if response.Code != http.StatusGatewayTimeout || !strings.Contains(response.Body.String(), "OFFICE_OPEN_TIMEOUT") || len(h.psrv.office.sessions) != 0 {
		t.Fatalf("unavailable engine created an editor: %d %s", response.Code, response.Body)
	}
	link := h.psrv.office.link(conversation, session.cwd, session.owner, session.hub)
	openContext, cancelOpen := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelOpen()
	_, err := link.Execute(openContext, json.RawMessage(`{"action":"open","path":"probe.docx"}`))
	officeExpectError(t, err, "OFFICE_OPEN_TIMEOUT")
	healthy.Store(true)
	officeReadinessTestExpire(h.psrv.office)
	response = request("POST", "/api/office/sessions", string(body))
	if response.Code != 200 {
		t.Fatalf("recovered engine did not open: %d %s", response.Code, response.Body)
	}
	var opened struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &opened); err != nil {
		t.Fatal(err)
	}
	document := h.psrv.office.sessions[opened.Session.ID]
	document.mu.Lock()
	document.dirtyEpoch, document.persistedEpoch = 1, 0
	document.mu.Unlock()
	healthy.Store(false)
	officeReadinessTestExpire(h.psrv.office)
	if status := h.psrv.officeAvailability(context.Background()); status.Ready {
		t.Fatal("stopped engine remained ready")
	}
	response = request("GET", "/api/office/sessions/"+opened.Session.ID+"/status", "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"dirty":true`) {
		t.Fatalf("existing dirty document status was blocked: %d %s", response.Code, response.Body)
	}
	response = request("POST", "/api/office/sessions/"+opened.Session.ID+"/close", `{}`)
	if response.Code != 409 || strings.Contains(response.Body.String(), "OFFICE_UNAVAILABLE") || document.closed {
		t.Fatalf("availability bypassed the dirty close fence: %d %s", response.Code, response.Body)
	}
}
