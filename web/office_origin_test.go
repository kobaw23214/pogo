package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOfficeRequestOriginUsesAuthenticatedClientEntry(t *testing.T) {
	for _, test := range []struct {
		name, address, origin, hint, referrer, site, want string
	}{
		{"ssh tunnel", "http://localhost:8080/api/office/sessions", "http://localhost:8080", "http://localhost:8080", "", "same-origin", "http://localhost:8080"},
		{"https proxy initial get", "http://pogo.example/api/office/sessions/id/config", "", "https://pogo.example", "", "same-origin", "https://pogo.example"},
		{"https proxy referrer", "http://pogo.example/api/office/sessions/id/config", "", "", "https://pogo.example/chat", "same-origin", "https://pogo.example"},
		{"direct tls", "https://pogo.example/api/office/sessions", "", "", "", "", "https://pogo.example"},
		{"local non browser", "http://127.0.0.1:19931/api/office/sessions", "", "", "", "", "http://127.0.0.1:19931"},
		{"different host", "http://localhost:8080/api/office/sessions", "http://attacker.example", "", "", "same-origin", ""},
		{"different port", "http://localhost:8080/api/office/sessions", "http://localhost:8081", "", "", "same-origin", ""},
		{"null origin", "http://localhost:8080/api/office/sessions", "null", "", "", "same-origin", ""},
		{"credentialed origin", "http://localhost:8080/api/office/sessions", "http://user@localhost:8080", "", "", "same-origin", ""},
		{"origin path", "http://localhost:8080/api/office/sessions", "http://localhost:8080/", "", "", "same-origin", ""},
		{"conflicting hint", "http://localhost:8080/api/office/sessions", "http://localhost:8080", "https://localhost:8080", "", "same-origin", ""},
		{"cross site", "http://localhost:8080/api/office/sessions", "http://localhost:8080", "", "", "cross-site", ""},
		{"same site different origin", "http://localhost:8080/api/office/sessions", "http://localhost:8080", "", "", "same-site", ""},
		{"tls downgrade", "https://pogo.example/api/office/sessions", "http://pogo.example", "", "", "same-origin", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", test.address, nil)
			r.Header.Set("Origin", test.origin)
			r.Header.Set("X-Pogo-Office-Origin", test.hint)
			r.Header.Set("Referer", test.referrer)
			r.Header.Set("Sec-Fetch-Site", test.site)
			r.Header.Set("X-Forwarded-Host", "attacker.example")
			r.Header.Set("X-Forwarded-Proto", "https")
			origin, allowed := officeRequestOrigin(r)
			if origin != test.want || allowed != (test.want != "") {
				t.Fatalf("origin=%q allowed=%v; want=%q", origin, allowed, test.want)
			}
		})
	}
}

func TestOfficeTunnelConfigRelativePathsAndInternalDocumentFetch(t *testing.T) {
	s, document, _ := officeGatewayTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("asset")) }))
	s.office.cfg.publicURL = "https://192.0.2.88:19770"
	s.office.cfg.fetchURL = "http://pogo:7777"
	document.mu.Lock()
	document.browserOrigin = "" // Unmounted fixture; the actual config must bind it.
	document.mu.Unlock()
	r := httptest.NewRequest("POST", "http://localhost:8080/api/office/sessions", nil)
	r.Header.Set("Origin", "http://localhost:8080")
	r.Header.Set("X-Pogo-Office-Origin", "http://localhost:8080")
	r.Header.Set("Authorization", "Bearer "+s.opts.Token)
	w := httptest.NewRecorder()
	s.writeOfficeConfigRequest(w, r, document, "synthetic-tunnel-instance", [32]byte{}, document.panelPath)
	if w.Code != 200 {
		t.Fatalf("forwarded editor bootstrap failed: %s", w.Body.String())
	}
	var opened struct {
		BridgeURL, BrowserURL string
		Config                map[string]any
	}
	if err := json.Unmarshal(w.Body.Bytes(), &opened); err != nil {
		t.Fatal(err)
	}
	bridge := "/office/bridge/" + document.id + "/" + document.bridgeToken + "/"
	if opened.BridgeURL != bridge || opened.BrowserURL != officeGatewayPrefix || strings.Contains(w.Body.String(), "192.0.2.88") {
		t.Fatal("configured server address escaped into client configuration")
	}
	claims, err := s.office.verifyJWT(opened.Config["token"].(string))
	if err != nil || len(claims) == 0 {
		t.Fatal("same-origin configuration lost its native engine signature")
	}
	plugins := opened.Config["editorConfig"].(map[string]any)["plugins"].(map[string]any)["pluginsData"].([]any)
	if plugins[0] != bridge+"config.json" || !strings.HasPrefix(opened.Config["document"].(map[string]any)["url"].(string), "http://pogo:7777/office/files/") {
		t.Fatal("client origin changed internal engine download or plugin path")
	}
	if document.browserOrigin != "http://localhost:8080" || document.instanceID != "synthetic-tunnel-instance" {
		t.Fatal("bootstrap did not bind the current browser origin and instance")
	}
	for _, cookie := range w.Result().Cookies() {
		if cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
			t.Fatal("HTTP tunnel cookie followed the configured HTTPS origin instead of the browser")
		}
	}
	manifest := httptest.NewRequest("GET", "http://localhost:8080"+bridge+"config.json", nil)
	manifest.SetPathValue("oid", document.id)
	manifest.SetPathValue("cap", document.bridgeToken)
	manifest.SetPathValue("resource", "config.json")
	mw := httptest.NewRecorder()
	s.handleOfficeBridge(mw, manifest)
	var config map[string]any
	if mw.Code != 200 || json.Unmarshal(mw.Body.Bytes(), &config) != nil || config["baseUrl"] != bridge || mw.Header().Get("Content-Security-Policy") != "frame-ancestors 'self'" || mw.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("plugin manifest did not remain same-origin and root-relative")
	}
	r = httptest.NewRequest("GET", "http://localhost:8081/api/office/sessions/id/config", nil)
	r.Header.Set("Authorization", "Bearer "+s.opts.Token)
	w = httptest.NewRecorder()
	s.writeOfficeConfigRequest(w, r, document, "synthetic-tunnel-instance", [32]byte{}, document.panelPath)
	if w.Code != 403 || document.browserOrigin != "http://localhost:8080" {
		t.Fatal("another tunnel rebound an existing editor")
	}
}

func TestOfficeGatewayMultipleTunnelsKeepIndependentGrants(t *testing.T) {
	var forwarded []string
	s, document, _ := officeGatewayTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded = append(forwarded, r.Header.Get("X-Forwarded-Proto")+"://"+r.Header.Get("X-Forwarded-Host"))
		if r.Header.Get("X-Pogo-Office-Origin") != "" {
			t.Error("bootstrap origin hint reached the private engine")
		}
		w.Write([]byte("asset"))
	}))
	file := filepath.Join(document.cwd, "second.docx")
	if err := os.WriteFile(file, document.snapshot, 0600); err != nil {
		t.Fatal(err)
	}
	second, err := s.office.open("conversation-1", document.cwd, nil, document.hub, file, "second.docx", nil)
	if err != nil {
		t.Fatal(err)
	}
	one := officeGatewayTestRebind(t, s, document, "http://localhost:8080")
	two := officeGatewayTestRebind(t, s, second, "http://localhost:8081")
	if one.Name == two.Name || s.office.gateway.grant(one.Value) == nil || s.office.gateway.grant(two.Value) == nil {
		t.Fatal("a second local port replaced the first editor grant")
	}
	for _, origin := range []string{"http://localhost:8080", "http://localhost:8081"} {
		r := httptest.NewRequest("GET", origin+officeGatewayPrefix+"/web-apps/apps/api/documents/api.js", nil)
		r.AddCookie(one)
		r.AddCookie(two)
		w := httptest.NewRecorder()
		s.handleOfficeGateway(w, r)
		if w.Code != 200 || forwarded[len(forwarded)-1] != origin+officeGatewayPrefix {
			t.Fatal("shared-host cookies chose the wrong port or forwarded a fixed server address")
		}
	}
	r := httptest.NewRequest("GET", "http://localhost:8081"+officeGatewayPrefix+"/doc/"+document.key+"/c/info", nil)
	r.AddCookie(one)
	r.AddCookie(two)
	w := httptest.NewRecorder()
	s.handleOfficeGateway(w, r)
	if w.Code != 404 {
		t.Fatal("one tunnel inherited another origin's document grant")
	}
	// A TLS terminator can share Host with an HTTP entry. A same-origin referrer
	// selects the HTTPS grant; absent scheme evidence the choice is rejected.
	secure := officeGatewayTestRebind(t, s, second, "https://localhost:8080")
	for _, referrer := range []string{"", "http://localhost:8080/chat", "https://localhost:8080/chat"} {
		r := httptest.NewRequest("GET", "http://localhost:8080"+officeGatewayPrefix+"/web-apps/apps/api/documents/api.js", nil)
		r.AddCookie(one)
		r.AddCookie(secure)
		r.Header.Set("Referer", referrer)
		w := httptest.NewRecorder()
		s.handleOfficeGateway(w, r)
		if referrer == "" {
			if w.Code != 401 {
				t.Fatal("ambiguous HTTP/HTTPS grants were selected by cookie order")
			}
		} else if w.Code != 200 || forwarded[len(forwarded)-1] != strings.TrimSuffix(referrer, "/chat")+officeGatewayPrefix {
			t.Fatal("proxy origin did not follow the bound scheme")
		}
	}
}
