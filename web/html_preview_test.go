package web

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/tools"
)

func writeHTMLPreviewFixture(t *testing.T, file, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func htmlPreviewHarness(t *testing.T) *muHarness {
	t.Helper()
	t.Setenv(tools.SpillDirEnv, t.TempDir())
	h := newMultiuserHarness(t, scriptedTurns(textTurn("done")))
	t.Cleanup(h.psrv.Close)
	return h
}

func createHTMLPreviewTest(t *testing.T, h *muHarness, sid, file string) htmlPreviewView {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"path": file})
	response := h.doAs(aliceToken, http.MethodPost, "/api/sessions/"+sid+"/html-previews", string(body))
	defer response.Body.Close()
	var view htmlPreviewView
	data, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusCreated || json.Unmarshal(data, &view) != nil || view.ID == "" || view.URL == "" {
		t.Fatalf("could not create preview: %d %s", response.StatusCode, data)
	}
	return view
}

func htmlPreviewGetTest(t *testing.T, h *muHarness, method, resource string) (*http.Response, string) {
	t.Helper()
	request, err := http.NewRequest(method, h.srv.URL+resource, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", "null")
	response, err := h.srv.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, string(data)
}

func TestHTMLPreviewServesSandboxedArtifactAndRelativeAssets(t *testing.T) {
	h := htmlPreviewHarness(t)
	sid := h.createAs(aliceToken)
	sess := h.session(sid)
	html := `<script type="module" src="main.mjs"></script><link rel="stylesheet" href="style.css"><h1>成品</h1>`
	for name, data := range map[string]string{
		"index.html": html, "main.mjs": `const x = await fetch('./data.json').then(r=>r.json()); document.body.dataset.ok=x.ok`,
		"style.css": "body { color: red }", "data.json": `{"ok":true}`, "image.svg": `<svg xmlns="http://www.w3.org/2000/svg"/>`,
	} {
		writeHTMLPreviewFixture(t, filepath.Join(sess.cwd, "demo", name), data)
	}
	view := createHTMLPreviewTest(t, h, sid, "demo/index.html")
	if !view.ReadOnly || view.Path != "demo/index.html" || view.ProjectRoot != "demo" || !strings.HasPrefix(view.BrowserURL, h.srv.URL+htmlPreviewPrefix) || strings.Contains(view.URL, aliceToken) || strings.Contains(view.BrowserURL, h.token) {
		t.Fatalf("invalid preview identity/URLs: %+v", view)
	}
	expires, err := time.Parse(time.RFC3339Nano, view.ExpiresAt)
	if err != nil || time.Until(expires) > htmlPreviewTTL || time.Until(expires) < htmlPreviewTTL-time.Minute {
		t.Fatal("capability did not receive a fixed 30-minute expiry")
	}
	rootURL := strings.TrimSuffix(view.URL, "index.html")
	parsedServer, _ := url.Parse(h.srv.URL)
	for _, resource := range []string{"index.html", "main.mjs", "style.css", "data.json", "image.svg"} {
		response, body := htmlPreviewGetTest(t, h, http.MethodGet, rootURL+resource)
		if response.StatusCode != http.StatusOK || body == "" {
			t.Fatalf("asset %s was unavailable without Pogo token: %d %s", resource, response.StatusCode, body)
		}
		policy := response.Header.Get("Content-Security-Policy")
		for _, clause := range []string{"sandbox allow-scripts;", "default-src 'none'", "connect-src " + parsedServer.Host + rootURL, "form-action 'none'", "base-uri 'none'", "frame-src 'none'", "worker-src 'none'"} {
			if !strings.Contains(policy, clause) {
				t.Errorf("asset %s omitted policy %q: %s", resource, clause, policy)
			}
		}
		if strings.Contains(policy, "allow-same-origin") || response.Header.Get("Access-Control-Allow-Credentials") != "" || response.Header.Get("Access-Control-Allow-Origin") != "*" || response.Header.Get("Set-Cookie") != "" || response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("Referrer-Policy") != "no-referrer" || response.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("artifact origin/credentials were not isolated: %v", response.Header)
		}
	}
	response, body := htmlPreviewGetTest(t, h, http.MethodHead, view.URL)
	if response.StatusCode != http.StatusOK || body != "" {
		t.Fatal("HEAD did not preserve read-only response semantics")
	}
	response, _ = htmlPreviewGetTest(t, h, http.MethodOptions, rootURL+"main.mjs")
	if response.StatusCode != http.StatusNoContent || response.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("opaque-origin module preflight was not narrowly allowed")
	}
	raw := h.doAs(aliceToken, http.MethodGet, "/api/files/content?raw=1&path=demo/index.html", "")
	raw.Body.Close()
	if raw.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("existing raw HTML restriction changed: %d", raw.StatusCode)
	}
}

func TestHTMLPreviewUsesFilePanelPathsAndConversationBoundary(t *testing.T) {
	h := htmlPreviewHarness(t)
	owner := identity.LocalScope("alice")
	userRoot := h.mgr.UserRoot(owner)
	writeHTMLPreviewFixture(t, filepath.Join(userRoot, "nested", "site", "index.html"), "<h1>NESTED</h1>")
	writeHTMLPreviewFixture(t, filepath.Join(userRoot, "outside.html"), "<h1>OUTSIDE</h1>")
	sess, err := h.mgr.CreateAs("k3", "nested", &owner)
	if err != nil {
		t.Fatal(err)
	}
	servicePath, err := filepath.Rel(h.mgr.Cwd(), filepath.Join(sess.cwd, "site", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	view := createHTMLPreviewTest(t, h, sess.ID, filepath.ToSlash(servicePath))
	if view.Path != "nested/site/index.html" || view.ProjectRoot != "nested/site" {
		t.Fatalf("file-panel path was double-prefixed by cwd: %+v", view)
	}
	response, body := htmlPreviewGetTest(t, h, http.MethodGet, view.URL)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "NESTED") {
		t.Fatal("nested workspace artifact was not rendered")
	}
	for _, body := range []string{`{"path":"outside.html"}`, `{"path":"../outside.html"}`, `{"path":"nested/site/index.html","project_root":"."}`, `{"path":"nested/site/index.html","root":"/tmp"}`} {
		response := h.doAs(aliceToken, http.MethodPost, "/api/sessions/"+sess.ID+"/html-previews", body)
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Errorf("conversation boundary accepted %s: %d", body, response.StatusCode)
		}
	}
}

func TestHTMLPreviewOwnerExpiryRevocationAndSessionDeletion(t *testing.T) {
	h := htmlPreviewHarness(t)
	aliceSID, bobSID := h.createAs(aliceToken), h.createAs(bobToken)
	writeHTMLPreviewFixture(t, filepath.Join(h.session(aliceSID).cwd, "index.html"), "<h1>PRIVATE</h1>")
	for _, token := range []string{"", bobToken} {
		response := h.doAs(token, http.MethodPost, "/api/sessions/"+aliceSID+"/html-previews", `{"path":"index.html"}`)
		response.Body.Close()
		want := http.StatusNotFound
		if token == "" {
			want = http.StatusUnauthorized
		}
		if response.StatusCode != want {
			t.Fatalf("preview creation bypassed owner: %d", response.StatusCode)
		}
	}
	view := createHTMLPreviewTest(t, h, aliceSID, "index.html")
	response := h.doAs(bobToken, http.MethodDelete, "/api/sessions/"+bobSID+"/html-previews/"+view.ID, "")
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatal("another user revoked an owner's capability")
	}
	response = h.doAs(aliceToken, http.MethodDelete, "/api/sessions/"+aliceSID+"/html-previews/"+view.ID, "")
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatal("owner could not revoke capability")
	}
	response, _ = htmlPreviewGetTest(t, h, http.MethodGet, view.URL)
	if response.StatusCode != http.StatusNotFound {
		t.Fatal("revoked capability stayed usable")
	}
	view = createHTMLPreviewTest(t, h, aliceSID, "index.html")
	capability := strings.Split(strings.TrimPrefix(view.URL, htmlPreviewPrefix), "/")[0]
	h.psrv.htmlPreviewMu.Lock()
	h.psrv.htmlPreviews[capability].expires = time.Now().Add(-time.Second)
	h.psrv.htmlPreviewMu.Unlock()
	response, _ = htmlPreviewGetTest(t, h, http.MethodGet, view.URL)
	if response.StatusCode != http.StatusNotFound {
		t.Fatal("expired capability was renewed by access")
	}
	view = createHTMLPreviewTest(t, h, aliceSID, "index.html")
	if err := h.mgr.Delete(aliceSID); err != nil {
		t.Fatal(err)
	}
	response, _ = htmlPreviewGetTest(t, h, http.MethodGet, view.URL)
	if response.StatusCode != http.StatusNotFound {
		t.Fatal("deleted session's artifact capability stayed usable")
	}
}

func TestHTMLPreviewRejectsTraversalPrivateFilesAndSymlinks(t *testing.T) {
	h := htmlPreviewHarness(t)
	sid := h.createAs(aliceToken)
	sess := h.session(sid)
	writeHTMLPreviewFixture(t, filepath.Join(sess.cwd, "site/index.html"), "<h1>SITE</h1>")
	for _, name := range []string{".env", ".git/config", "node_modules/private.js", "users.json", "providers.json", "src/private.go", "data.json"} {
		writeHTMLPreviewFixture(t, filepath.Join(sess.cwd, "site", filepath.FromSlash(name)), "PRIVATE")
	}
	outside := filepath.Join(t.TempDir(), "secret.html")
	writeHTMLPreviewFixture(t, outside, "OUTSIDE_SECRET")
	if err := os.Symlink(outside, filepath.Join(sess.cwd, "site/link.html")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(sess.cwd, "site/linked")); err != nil {
		t.Fatal(err)
	}
	view := createHTMLPreviewTest(t, h, sid, "site/index.html")
	root := strings.TrimSuffix(view.URL, "index.html")
	for _, name := range []string{".env", ".git/config", "node_modules/private.js", "users.json", "providers.json", "src/private.go", "link.html", "linked/secret.html", "../index.html", "%2e%2e/index.html", "data.json/../../index.html"} {
		request, err := http.NewRequest(http.MethodGet, h.srv.URL+root+name, nil)
		if err != nil {
			t.Fatal(err)
		}
		client := *h.srv.Client()
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode == http.StatusOK || strings.Contains(string(body), "OUTSIDE_SECRET") || strings.Contains(string(body), "PRIVATE") {
			t.Errorf("private/escaping resource %q was disclosed: %d %s", name, response.StatusCode, body)
		}
	}
	response := h.doAs(aliceToken, http.MethodPost, "/api/sessions/"+sid+"/html-previews", `{"path":"site/link.html"}`)
	response.Body.Close()
	if response.StatusCode == http.StatusCreated {
		t.Fatal("symbolic entry HTML acquired a capability")
	}
	response, _ = htmlPreviewGetTest(t, h, http.MethodGet, htmlPreviewPrefix+strings.Repeat("0", 64)+"/index.html")
	if response.StatusCode != http.StatusNotFound {
		t.Fatal("invalid capability fell through to authenticated file access")
	}
}

func TestHTMLPreviewCannotAuthorizeMainAPIOrWrites(t *testing.T) {
	h := htmlPreviewHarness(t)
	sid := h.createAs(aliceToken)
	writeHTMLPreviewFixture(t, filepath.Join(h.session(sid).cwd, "index.html"), "<h1>ARTIFACT</h1>")
	view := createHTMLPreviewTest(t, h, sid, "index.html")
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		response, _ := htmlPreviewGetTest(t, h, method, view.URL)
		if response.StatusCode == http.StatusOK || response.StatusCode == http.StatusNoContent || response.Header.Get("Access-Control-Allow-Origin") == "*" {
			t.Fatalf("capability enabled a write or broad CORS: %s %d", method, response.StatusCode)
		}
	}
	for _, resource := range []string{"/api/config", "/api/files?path=.", "/api/sessions/" + sid} {
		response, _ := htmlPreviewGetTest(t, h, http.MethodGet, resource+"?capability="+url.QueryEscape(view.ID))
		if response.StatusCode != http.StatusForbidden || response.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("artifact opaque origin reached the application API: %s %d", resource, response.StatusCode)
		}
	}
}

func TestHTMLPreviewBrowserOriginUsesListenerAndIgnoresForwarded(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "http://public.example/api/sessions/test/html-previews", nil)
	request.Header.Set("X-Forwarded-Host", "attacker.example")
	request.Header.Set("X-Forwarded-Proto", "https")
	request = request.WithContext(context.WithValue(request.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("172.18.0.5"), Port: 7777}))
	origin, err := htmlPreviewBrowserOrigin(request)
	if err != nil || origin != "http://172.18.0.5:7777" {
		t.Fatalf("browser URL did not use the actual listener: %s %v", origin, err)
	}
	for _, host := range []string{"attacker.example'; default-src *", "user@attacker.example", "example.test/a", "example.test\r\nX: y", "*.example.test", "example.test,attacker.test"} {
		request.Host = host
		if _, err := htmlPreviewRequestOrigin(request); err == nil {
			t.Fatalf("unsafe CSP authority accepted: %q", host)
		}
	}
}

func TestHTMLPreviewQuotaSizeAndServerClose(t *testing.T) {
	h := htmlPreviewHarness(t)
	sid := h.createAs(aliceToken)
	cwd := h.session(sid).cwd
	writeHTMLPreviewFixture(t, filepath.Join(cwd, "index.html"), "<h1>ARTIFACT</h1>")
	large, err := os.Create(filepath.Join(cwd, "large.html"))
	if err != nil {
		t.Fatal(err)
	}
	if err := large.Truncate(htmlPreviewHTMLLimit + 1); err != nil {
		large.Close()
		t.Fatal(err)
	}
	large.Close()
	response := h.doAs(aliceToken, http.MethodPost, "/api/sessions/"+sid+"/html-previews", `{"path":"large.html"}`)
	response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatal("oversized entry HTML acquired a preview capability")
	}
	var first htmlPreviewView
	for count := 0; count < htmlPreviewOwnerLimit; count++ {
		view := createHTMLPreviewTest(t, h, sid, "index.html")
		if count == 0 {
			first = view
		}
	}
	response = h.doAs(aliceToken, http.MethodPost, "/api/sessions/"+sid+"/html-previews", `{"path":"index.html"}`)
	response.Body.Close()
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("owner exceeded %d active preview capabilities: %d", htmlPreviewOwnerLimit, response.StatusCode)
	}
	h.psrv.Close()
	response, _ = htmlPreviewGetTest(t, h, http.MethodGet, first.URL)
	if response.StatusCode != http.StatusNotFound {
		t.Fatal("server close left preview capabilities usable")
	}
	h.psrv.htmlPreviewMu.Lock()
	remaining := len(h.psrv.htmlPreviews)
	h.psrv.htmlPreviewMu.Unlock()
	if remaining != 0 {
		t.Fatal("server close leaked preview roots/timers")
	}
}
