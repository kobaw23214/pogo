package web

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/webservice"
)

const websiteSettingsAdminToken = "website-settings-admin-fixed-test-token"

type websiteSettingsRunner struct {
	*webServiceHostRunner
	captureMu sync.Mutex
	starts    []webservice.Record
	data      string
	deleted   int
}

func (r *websiteSettingsRunner) Start(ctx context.Context, launch webservice.Launch) (webservice.Instance, error) {
	instance, err := r.webServiceHostRunner.Start(ctx, launch)
	r.captureMu.Lock()
	r.starts = append(r.starts, launch.Record)
	r.captureMu.Unlock()
	instance.ID = fmt.Sprintf("%s-g%d", launch.Record.ServiceID, launch.Record.Generation)
	return instance, err
}

func (r *websiteSettingsRunner) DeleteData(context.Context, webservice.Record) error {
	r.captureMu.Lock()
	defer r.captureMu.Unlock()
	r.deleted++
	r.data = ""
	return nil
}

type websiteSettingsGateway struct {
	webServiceHostGateway
	mu     sync.Mutex
	routes map[string]int64
}

func (*websiteSettingsGateway) LifecycleURLs() bool { return true }
func (g *websiteSettingsGateway) Apply(_ context.Context, record webservice.Record) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.routes[record.ServiceID] = record.Revision
	return nil
}
func (g *websiteSettingsGateway) Open(_ context.Context, record webservice.Record) (webservice.Grant, error) {
	g.mu.Lock()
	revision := g.routes[record.ServiceID]
	g.mu.Unlock()
	if revision != record.Revision {
		return webservice.Grant{}, webservice.Fail("STALE_ROUTE", "route changed")
	}
	hash := sha256.Sum256([]byte(record.LeaseID))
	return webservice.Grant{ServiceID: record.ServiceID, Generation: record.Generation, GrantID: "lifecycle-grant", PreviewURL: fmt.Sprintf("https://%x.preview.test:19770/", hash[:24]), ExpiresAt: record.LeaseExpiresAt, LeaseExpiresAt: record.LeaseExpiresAt}, nil
}

func websiteSettingsHarness(t *testing.T) (*muHarness, *webservice.Manager, *websiteSettingsRunner) {
	t.Helper()
	runner := &websiteSettingsRunner{webServiceHostRunner: &webServiceHostRunner{running: map[string]bool{}}, data: "retained website data"}
	// Maintenance must not accidentally repair a missing metadata Apply before
	// the owner immediately opens the website after renaming it.
	managed, err := webservice.NewManager(webservice.Options{Root: filepath.Join(t.TempDir(), "private-websites"), Installation: "settings-test", Runner: runner, Gateway: &websiteSettingsGateway{routes: map[string]int64{}}, PollInterval: time.Hour, HealthInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = managed.Close() })
	h := newMultiuserHarness(t, func(int) (llm.Response, error) {
		t.Error("website settings contacted the model")
		return llm.Response{}, nil
	}, func(c *Config) { c.WebServices = managed })
	h.psrv.auth.mu.Lock()
	h.psrv.auth.users["settings-admin"] = userRecord{Username: "settings-admin", Admin: true, Token: websiteSettingsAdminToken}
	h.psrv.auth.mu.Unlock()
	return h, managed, runner
}

func websiteSettingsHTTP(t *testing.T, h *muHarness, token, method, route string, value any, want int) []byte {
	t.Helper()
	body := ""
	if value != nil {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		body = string(data)
	}
	response := h.doAs(token, method, route, body)
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != want {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, route, response.StatusCode, want, data)
	}
	return data
}

func websiteSettingsProject(t *testing.T, h *muHarness, sid string) {
	t.Helper()
	project := filepath.Join(h.session(sid).cwd, "site")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "app.py"), []byte("print('owned source')\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

type websiteSettingsView struct {
	ID         string    `json:"service_id"`
	Name       string    `json:"name"`
	Slot       string    `json:"slot_name"`
	Generation int       `json:"generation"`
	State      string    `json:"state"`
	Expires    time.Time `json:"lease_expires_at"`
}

func websiteSettingsStart(t *testing.T, h *muHarness, token, sid, slot string) websiteSettingsView {
	t.Helper()
	data := websiteSettingsHTTP(t, h, token, http.MethodPost, "/api/sessions/"+sid+"/web-services", map[string]any{"name": slot, "project_root": "site", "profile": "python-web", "start_argv": []string{"python3", "app.py"}}, http.StatusOK)
	var accepted websiteSettingsView
	if err := json.Unmarshal(data, &accepted); err != nil || accepted.ID == "" {
		t.Fatalf("invalid accepted website %s: %v", data, err)
	}
	return websiteSettingsReady(t, h, token, sid, accepted.ID, accepted.Generation)
}

func websiteSettingsReady(t *testing.T, h *muHarness, token, sid, id string, generation int) websiteSettingsView {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		data := websiteSettingsHTTP(t, h, token, http.MethodGet, "/api/sessions/"+sid+"/web-services/"+id, nil, http.StatusOK)
		var state websiteSettingsView
		if err := json.Unmarshal(data, &state); err != nil {
			t.Fatal(err)
		}
		if state.State == "ready" && state.Generation == generation {
			return state
		}
		if time.Now().After(deadline) {
			t.Fatalf("website not ready in generation %d: %s", generation, data)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWebServiceSettingsHTTPAdminValidationAndPersistence(t *testing.T) {
	h, managed, runner := websiteSettingsHarness(t)
	const route = "/api/web-services/settings"
	policy := webservice.DefaultPolicy()
	policy.DefaultLeaseSeconds, policy.MaxPerSession, policy.MaxPerUser = 86400, 3, 7
	websiteSettingsHTTP(t, h, aliceToken, http.MethodGet, route, nil, http.StatusForbidden)
	websiteSettingsHTTP(t, h, aliceToken, http.MethodPut, route, policy, http.StatusForbidden)
	websiteSettingsHTTP(t, h, websiteSettingsAdminToken, http.MethodPut, route, policy, http.StatusOK)
	data := websiteSettingsHTTP(t, h, websiteSettingsAdminToken, http.MethodGet, route, nil, http.StatusOK)
	var response struct {
		State webservice.SettingsState `json:"state"`
	}
	if err := json.Unmarshal(data, &response); err != nil || response.State.Settings != policy {
		t.Fatalf("saved settings missing from GET: %s %v", data, err)
	}
	for _, value := range []any{
		map[string]any{"unknown": true},
		map[string]any{"default_lease_seconds": 0},
		func() webservice.Policy { p := policy; p.CPUs = 65; return p }(),
		func() webservice.Policy { p := policy; p.InternalPort = 80; return p }(),
	} {
		websiteSettingsHTTP(t, h, websiteSettingsAdminToken, http.MethodPut, route, value, http.StatusBadRequest)
		if managed.Settings().Settings != policy {
			t.Fatal("rejected settings changed the active policy")
		}
	}
	if err := managed.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := webservice.NewManager(webservice.Options{Root: managed.Root(), Installation: "settings-test", Runner: runner, Gateway: &websiteSettingsGateway{routes: map[string]int64{}}})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Settings().Settings != policy {
		t.Fatal("HTTP settings did not survive manager restart")
	}
}

func TestWebServiceSettingsHTTPDefaultLifetimeAndFriendlyQuotaDetails(t *testing.T) {
	h, _, _ := websiteSettingsHarness(t)
	policy := webservice.DefaultPolicy()
	policy.DefaultLeaseSeconds, policy.MaxPerSession, policy.MaxPerUser = 86400, 1, 1
	websiteSettingsHTTP(t, h, websiteSettingsAdminToken, http.MethodPut, "/api/web-services/settings", policy, http.StatusOK)
	sid := h.createAs(aliceToken)
	websiteSettingsProject(t, h, sid)
	started := time.Now()
	first := websiteSettingsStart(t, h, aliceToken, sid, "first")
	if remaining := first.Expires.Sub(started); remaining < 24*time.Hour || remaining > 24*time.Hour+3*time.Second {
		t.Fatal("Agent/HTTP omitted lifetime did not use platform default", remaining)
	}
	for _, test := range []struct{ sid, scope string }{{sid, "session"}, {h.createAs(aliceToken), "user"}} {
		websiteSettingsProject(t, h, test.sid)
		data := websiteSettingsHTTP(t, h, aliceToken, http.MethodPost, "/api/sessions/"+test.sid+"/web-services", map[string]any{"name": "second", "project_root": "site", "profile": "python-web", "start_argv": []string{"python3", "app.py"}}, http.StatusBadRequest)
		var problem struct {
			Code    string         `json:"code"`
			Message string         `json:"error"`
			Details map[string]any `json:"details"`
		}
		if err := json.Unmarshal(data, &problem); err != nil || problem.Code != "QUOTA_EXCEEDED" || problem.Details["scope"] != test.scope || problem.Details["limit"] != float64(1) || problem.Details["current"] != float64(1) || !strings.Contains(strings.ToLower(problem.Message), "stop") {
			t.Fatalf("quota lacks actionable scoped details: %s %v", data, err)
		}
	}
	other := h.createAs(bobToken)
	websiteSettingsProject(t, h, other)
	websiteSettingsStart(t, h, bobToken, other, "bob-site")
}

func TestWebServiceSettingsHTTPOwnerRenameAndAutomaticRuntimeRestart(t *testing.T) {
	h, managed, runner := websiteSettingsHarness(t)
	sid := h.createAs(aliceToken)
	websiteSettingsProject(t, h, sid)
	first := websiteSettingsStart(t, h, aliceToken, sid, "stable-slot")
	base := "/api/sessions/" + sid + "/web-services/" + first.ID
	for _, test := range []struct{ token, sid string }{{bobToken, sid}, {aliceToken, h.createAs(aliceToken)}} {
		websiteSettingsHTTP(t, h, test.token, http.MethodPost, "/api/sessions/"+test.sid+"/web-services/"+first.ID+"/rename", map[string]any{"display_name": "不允许的修改"}, http.StatusNotFound)
	}
	data := websiteSettingsHTTP(t, h, aliceToken, http.MethodPost, base+"/rename", map[string]any{"display_name": "客户演示网站"}, http.StatusOK)
	var renamed websiteSettingsView
	if err := json.Unmarshal(data, &renamed); err != nil || renamed.Name != "客户演示网站" || renamed.Slot != "stable-slot" || renamed.Generation != first.Generation || !renamed.Expires.Equal(first.Expires) {
		t.Fatalf("rename changed website runtime identity: %s %v", data, err)
	}
	websiteSettingsHTTP(t, h, aliceToken, http.MethodPost, base+"/rename", map[string]any{"display_name": "invalid\nname"}, http.StatusBadRequest)
	runner.captureMu.Lock()
	starts := append([]webservice.Record(nil), runner.starts...)
	runner.captureMu.Unlock()
	if len(starts) != 1 {
		t.Fatal("rename restarted the website")
	}
	original := starts[0]
	beforeURL := websiteSettingsHTTP(t, h, aliceToken, http.MethodPost, base+"/previews", map[string]any{}, http.StatusOK)
	var beforeGrant webservice.Grant
	if err := json.Unmarshal(beforeURL, &beforeGrant); err != nil {
		t.Fatal(err)
	}
	policy := managed.Settings().Settings
	policy.InternalPort, policy.CPUs, policy.MemoryMiB = 9234, 1.5, 768
	updateBody := websiteSettingsHTTP(t, h, websiteSettingsAdminToken, http.MethodPut, "/api/web-services/settings", policy, http.StatusOK)
	var update webservice.SettingsUpdate
	if err := json.Unmarshal(updateBody, &update); err != nil || len(update.Restarted) != 1 || !reflect.DeepEqual(update.Effects.RestartRequired, []string{"internal_port", "cpus", "memory_mib"}) {
		t.Fatalf("runtime change did not enqueue its automatic restart: %s %v", updateBody, err)
	}
	current := websiteSettingsReady(t, h, aliceToken, sid, first.ID, first.Generation+1)
	if current.Name != renamed.Name || !current.Expires.Equal(first.Expires) {
		t.Fatal("automatic restart lost title or extended website lifetime", current)
	}
	runner.captureMu.Lock()
	starts = append([]webservice.Record(nil), runner.starts...)
	deleted, retainedData := runner.deleted, runner.data
	runner.captureMu.Unlock()
	if len(starts) != 2 || starts[1].LeaseID != original.LeaseID || starts[1].LeaseID == "" || starts[1].Runtime != policy.Runtime() || deleted != 0 || retainedData != "retained website data" {
		t.Fatalf("automatic restart changed lease/data or missed runtime configuration: %+v", starts)
	}
	afterURL := websiteSettingsHTTP(t, h, aliceToken, http.MethodPost, base+"/previews", map[string]any{}, http.StatusOK)
	var afterGrant webservice.Grant
	if err := json.Unmarshal(afterURL, &afterGrant); err != nil || beforeGrant.PreviewURL != afterGrant.PreviewURL || !afterGrant.ExpiresAt.Equal(first.Expires) {
		t.Fatalf("same-lifetime URL changed after settings restart: %s %v", afterURL, err)
	}
	for _, forbidden := range []string{`"workspace":`, `"owner":`, `"instance":`, "runtime:8080"} {
		if strings.Contains(string(updateBody), forbidden) || strings.Contains(string(data), forbidden) {
			t.Fatalf("HTTP settings/rename leaked private runtime metadata: %s", forbidden)
		}
	}
}
