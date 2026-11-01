package web

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
)

// panelHarness builds a server with panels registered, fronted by a stub
// backend that echoes what it received. No run is ever started, so the
// scripted client only has to exist. The optional mutators let a test adjust
// ServerOptions (the panel-store tests use one).
func panelHarness(t *testing.T, panels []Panel, opt ...func(*ServerOptions)) (*httptest.Server, *httptest.Server) {
	t.Helper()
	t.Setenv("KIMI_API_KEY", "test-key")
	installTestCatalog(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "backend:"+r.URL.Path+"?"+r.URL.RawQuery)
	}))
	t.Cleanup(backend.Close)

	for i := range panels {
		panels[i].URL = strings.ReplaceAll(panels[i].URL, "BACKEND", backend.URL)
	}
	mgr, err := NewManager(Config{
		Cwd:         t.TempDir(),
		SessionDir:  t.TempDir(),
		Model:       "k3",
		MaxTurns:    5,
		GateTimeout: 5 * time.Second,
		RunTimeout:  time.Minute,
		IdleTimeout: time.Hour,
		NewClient: func(c config.Resolved, _ func(llm.RetryInfo)) llm.Client {
			return &scriptedClient{model: c.Model}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)

	opts := ServerOptions{Token: "test-token", Panels: panels}
	for _, f := range opt {
		f(&opts)
	}
	srv, err := NewServer(mgr, opts)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(srv.Handler())
	t.Cleanup(front.Close)
	return front, backend
}

// postPanel fires the runtime-registration endpoint. The body is raw JSON so
// each test states exactly what it sends; a BACKEND placeholder is swapped for
// the stub server's URL like panelHarness does for flag panels.
func postPanel(t *testing.T, front, backend *httptest.Server, body string) (*http.Response, string) {
	t.Helper()
	body = strings.ReplaceAll(body, "BACKEND", backend.URL)
	req, _ := http.NewRequest(http.MethodPost, front.URL+"/api/panels", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, string(out)
}

func TestPanelsListed(t *testing.T) {
	front, _ := panelHarness(t, []Panel{{Name: "样本库", URL: "BACKEND"}})
	resp, err := http.Get(front.URL + "/api/panels")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/api/panels without token: got %d, want 401 (it is an /api route)", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, front.URL+"/api/panels", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "样本库") || !strings.Contains(string(body), "/panels/样本库/") {
		t.Fatalf("panel listing wrong: %s", body)
	}
}

func TestPanelProxyStripsPrefix(t *testing.T) {
	front, _ := panelHarness(t, []Panel{{Name: "app", URL: "BACKEND"}})

	// Content like the page itself: no token required.
	resp, err := http.Get(front.URL + "/panels/app/deep/page?x=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "backend:/deep/page?x=1" {
		t.Fatalf("proxy did not strip prefix/preserve query: %s", body)
	}

	// Bare name redirects to the trailing slash so relative links resolve.
	req, _ := http.NewRequest(http.MethodGet, front.URL+"/panels/app", nil)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Location") != "/panels/app/" {
		t.Fatalf("redirect: got %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	// Unknown panel is a plain 404.
	resp, err = http.Get(front.URL + "/panels/nope/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown panel: got %d, want 404", resp.StatusCode)
	}
}

func TestPanelValidation(t *testing.T) {
	for _, p := range []Panel{
		{Name: "has/slash", URL: "http://x"},
		{Name: "has=equals", URL: "http://x"},
		{Name: "", URL: "http://x"},
		{Name: "ok", URL: "not-a-url"},
		{Name: "ok", URL: "ftp://x"},
	} {
		if err := validatePanel(p); err == nil {
			t.Errorf("%+v: want invalid", p)
		}
	}
	if err := validatePanel(Panel{Name: "样本库", URL: "http://127.0.0.1:8080"}); err != nil {
		t.Errorf("valid panel rejected: %v", err)
	}
}

// ---- runtime registration (POST /api/panels) ----

func TestPanelRegisterRequiresToken(t *testing.T) {
	front, _ := panelHarness(t, nil)
	resp, err := http.Post(front.URL+"/api/panels", "application/json",
		strings.NewReader(`{"url":"http://127.0.0.1:1","name":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("register without token: got %d, want 401", resp.StatusCode)
	}
}

func TestPanelRegisterProxiesAndLists(t *testing.T) {
	front, backend := panelHarness(t, nil)
	resp, body := postPanel(t, front, backend, `{"url":"BACKEND","name":"Malware"}`)
	if resp.StatusCode != http.StatusOK ||
		!strings.Contains(body, `"name":"Malware"`) ||
		!strings.Contains(body, `"path":"/panels/Malware/"`) {
		t.Fatalf("register response wrong: %d %s", resp.StatusCode, body)
	}

	// The registered panel serves like a flag one: content without token.
	presp, err := http.Get(front.URL + "/panels/Malware/deep?x=1")
	if err != nil {
		t.Fatal(err)
	}
	defer presp.Body.Close()
	pbody, _ := io.ReadAll(presp.Body)
	if string(pbody) != "backend:/deep?x=1" {
		t.Fatalf("proxied body wrong: %s (backend %s)", pbody, backend.URL)
	}

	// And it is listed for the UI alongside any flag panels.
	req, _ := http.NewRequest(http.MethodGet, front.URL+"/api/panels", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	lresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer lresp.Body.Close()
	lbody, _ := io.ReadAll(lresp.Body)
	if !strings.Contains(string(lbody), "/panels/Malware/") {
		t.Fatalf("registered panel not listed: %s", lbody)
	}
}

func TestPanelRegisterIdempotentPerURL(t *testing.T) {
	// A URL a flag already serves maps to that flag panel — re-adding the
	// shortcut must not grow a duplicate.
	front, backend := panelHarness(t, []Panel{{Name: "样本库", URL: "BACKEND"}})
	resp, body := postPanel(t, front, backend, `{"url":"BACKEND","name":"whatever"}`)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"name":"样本库"`) {
		t.Fatalf("flag URL should map to the flag panel: %d %s", resp.StatusCode, body)
	}

	// Same runtime URL twice: same panel both times.
	resp, body = postPanel(t, front, backend, fmt.Sprintf(`{"url":%q,"name":"Two"}`, backend.URL+"/x"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first register failed: %d %s", resp.StatusCode, body)
	}
	resp, body = postPanel(t, front, backend, fmt.Sprintf(`{"url":%q,"name":"Two again"}`, backend.URL+"/x"))
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"name":"Two"`) {
		t.Fatalf("re-register not idempotent: %d %s", resp.StatusCode, body)
	}
}

func TestPanelRegisterDerivesNames(t *testing.T) {
	front, backend := panelHarness(t, nil)
	// A hint with URL-hostile characters sanitizes into a legal segment.
	_, body := postPanel(t, front, backend, `{"url":"BACKEND","name":"My / Panel="}`)
	if !strings.Contains(body, `"name":"My---Panel"`) {
		t.Fatalf("name not sanitized: %s", body)
	}
	// Same hint, different URL: derived name gets a numeric suffix.
	_, body = postPanel(t, front, backend, fmt.Sprintf(`{"url":%q,"name":"My / Panel="}`, backend.URL+"/x"))
	if !strings.Contains(body, `"name":"My---Panel-2"`) {
		t.Fatalf("collision not suffixed: %s", body)
	}
	// No hint at all: the host stands in (port colon sanitized, dots kept).
	_, body = postPanel(t, front, backend, fmt.Sprintf(`{"url":%q}`, backend.URL+"/y"))
	if !strings.Contains(body, `"name":"127.0.0.1-`) || strings.Contains(body, `"name":"127.0.0.1:`) {
		t.Fatalf("host fallback wrong: %s", body)
	}
}

func TestPanelRegisterRejectsBadURL(t *testing.T) {
	front, backend := panelHarness(t, nil)
	for _, u := range []string{"not a url", "ftp://x", "", "http://"} {
		resp, _ := postPanel(t, front, backend, fmt.Sprintf(`{"url":%q,"name":"x"}`, u))
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("url %q: got %d, want 400", u, resp.StatusCode)
		}
	}
}

func TestMapLoopbackHost(t *testing.T) {
	const mapTo = "host.docker.internal"
	for _, c := range []struct{ in, want string }{
		{"http://localhost:18090/", "http://" + mapTo + ":18090/"},
		{"http://127.0.0.1:8080/x", "http://" + mapTo + ":8080/x"},
		{"http://localhost/", "http://" + mapTo + "/"},
		{"https://grafana.local:3000/", "https://grafana.local:3000/"}, // not loopback: untouched
		{"http://[::1]:9000/", "http://" + mapTo + ":9000/"},
	} {
		if got := mapLoopbackHost(c.in, mapTo); got != c.want {
			t.Errorf("mapLoopbackHost(%q): got %q, want %q", c.in, got, c.want)
		}
	}
	// No mapping: identity, the native-deployment case.
	if got := mapLoopbackHost("http://localhost:8080/", ""); got != "http://localhost:8080/" {
		t.Errorf("empty mapping rewrote: %q", got)
	}
}

func TestPanelRegisterMapsLoopback(t *testing.T) {
	// Registering a loopback URL with a mapping set must proxy through the
	// mapped host — and be idempotent against the raw spelling.
	front, backend := panelHarness(t, nil, func(o *ServerOptions) {
		o.PanelLoopbackMap = "127.0.0.1" // map localhost → the stub's own host
	})
	// The mapping rewrites only the host, so the port must be the stub's.
	bu, _ := url.Parse(backend.URL)
	resp, body := postPanel(t, front, backend,
		fmt.Sprintf(`{"url":"http://localhost:%s/","name":"Loop"}`, bu.Port()))
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"name":"Loop"`) {
		t.Fatalf("register failed: %d %s", resp.StatusCode, body)
	}
	presp, err := http.Get(front.URL + "/panels/Loop/probe")
	if err != nil {
		t.Fatal(err)
	}
	defer presp.Body.Close()
	pbody, _ := io.ReadAll(presp.Body)
	if string(pbody) != "backend:/probe?" {
		t.Fatalf("mapped loopback does not proxy: %s", pbody)
	}
	// The raw spelling still resolves to the same panel (mapped before the
	// idempotency check).
	resp, body = postPanel(t, front, backend, fmt.Sprintf(`{"url":%q,"name":"Other"}`, backend.URL))
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"name":"Loop"`) {
		t.Fatalf("raw vs mapped not idempotent: %d %s", resp.StatusCode, body)
	}
}

func TestPanelRegisterPersistsAcrossRestart(t *testing.T) {
	store := filepath.Join(t.TempDir(), "panels.json")
	front, backend := panelHarness(t, nil, func(o *ServerOptions) { o.PanelStore = store })
	resp, body := postPanel(t, front, backend, `{"url":"BACKEND","name":"Kept"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register failed: %d %s", resp.StatusCode, body)
	}

	// A fresh server on the same store serves the same panel; a flag panel
	// with the same name would win (loadPanelStore drops stored collisions).
	front2, _ := panelHarness(t, nil, func(o *ServerOptions) { o.PanelStore = store })
	presp, err := http.Get(front2.URL + "/panels/Kept/")
	if err != nil {
		t.Fatal(err)
	}
	defer presp.Body.Close()
	pbody, _ := io.ReadAll(presp.Body)
	if string(pbody) != "backend:/?" {
		t.Fatalf("persisted panel does not proxy: %s", pbody)
	}
}

// ---- runtime revocation (DELETE /api/panels/{name}) ----

// deletePanel fires the revoke endpoint; want controls the expected status.
func deletePanel(t *testing.T, front *httptest.Server, name string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, front.URL+"/api/panels/"+url.PathEscape(name), nil)
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, string(out)
}

func listed(t *testing.T, front *httptest.Server) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, front.URL+"/api/panels", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

func TestPanelDeleteRequiresToken(t *testing.T) {
	front, _ := panelHarness(t, nil)
	req, _ := http.NewRequest(http.MethodDelete, front.URL+"/api/panels/x", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("delete without token: got %d, want 401", resp.StatusCode)
	}
}

func TestPanelDeleteRevokes(t *testing.T) {
	store := filepath.Join(t.TempDir(), "panels.json")
	front, backend := panelHarness(t, []Panel{{Name: "flagged", URL: "BACKEND"}}, func(o *ServerOptions) { o.PanelStore = store })
	postPanel(t, front, backend, fmt.Sprintf(`{"url":%q,"name":"Gone"}`, backend.URL+"/gone"))

	resp, body := deletePanel(t, front, "Gone")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"deleted":true`) {
		t.Fatalf("delete response wrong: %d %s", resp.StatusCode, body)
	}
	// Revoked immediately: the proxy route and the listing both forget it…
	presp, err := http.Get(front.URL + "/panels/Gone/")
	if err != nil {
		t.Fatal(err)
	}
	presp.Body.Close()
	if presp.StatusCode != http.StatusNotFound {
		t.Fatalf("revoked panel still serves: %d", presp.StatusCode)
	}
	if l := listed(t, front); strings.Contains(l, "Gone") {
		t.Fatalf("revoked panel still listed: %s", l)
	}
	if l := listed(t, front); !strings.Contains(l, "flagged") {
		t.Fatalf("sibling flag panel vanished: %s", l)
	}
	// …and the store says so too: a fresh server on the same file stays clean.
	front2, _ := panelHarness(t, nil, func(o *ServerOptions) { o.PanelStore = store })
	if l := listed(t, front2); strings.Contains(l, "Gone") {
		t.Fatalf("revoked panel resurrected from store: %s", l)
	}
	// Deleting again is a plain 404, not an error-shaped surprise.
	resp, body = deletePanel(t, front, "Gone")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("double delete: got %d %s, want 404", resp.StatusCode, body)
	}
}

func TestPanelDeleteFlagPanelRefused(t *testing.T) {
	front, _ := panelHarness(t, []Panel{{Name: "样本库", URL: "BACKEND"}})
	resp, body := deletePanel(t, front, "样本库")
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, "-web-panel") {
		t.Fatalf("flag panel delete: got %d %s, want 409 naming the flag", resp.StatusCode, body)
	}
	if l := listed(t, front); !strings.Contains(l, "样本库") {
		t.Fatalf("flag panel vanished after refused delete: %s", l)
	}
}

func TestPanelDeleteAllowsReRegister(t *testing.T) {
	front, backend := panelHarness(t, nil)
	postPanel(t, front, backend, fmt.Sprintf(`{"url":%q,"name":"Cycle"}`, backend.URL+"/c"))
	deletePanel(t, front, "Cycle")
	// The URL is free again: a re-register recreates the same name.
	resp, body := postPanel(t, front, backend, fmt.Sprintf(`{"url":%q,"name":"Cycle"}`, backend.URL+"/c"))
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"name":"Cycle"`) {
		t.Fatalf("re-register after delete failed: %d %s", resp.StatusCode, body)
	}
}
