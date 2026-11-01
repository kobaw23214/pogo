package web

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yosukeno/pogo/identity"
	"nhooyr.io/websocket"
)

func officeGatewayTestServer(t *testing.T, upstream http.Handler) (*Server, *officeDocumentSession, *http.Cookie) {
	t.Helper()
	engine := httptest.NewServer(upstream)
	t.Cleanup(engine.Close)
	service, document, _ := officeTestService(t)
	service.cfg.serviceURL = engine.URL
	service.cfg.serviceOrigin = engine.URL
	service.cfg.publicURL = "https://pogo.example"
	service.cfg.browserOrigin = service.cfg.publicURL
	service.cfg.browserURL = service.cfg.publicURL + officeGatewayPrefix
	s := &Server{office: service, opts: ServerOptions{Token: "private-test-account-token"}, auth: &authStore{}, mgr: &Manager{}}
	service.gateway = newOfficeGateway(s)
	t.Cleanup(service.gateway.close)
	request := httptest.NewRequest(http.MethodPost, "https://pogo.example/api/office/sessions", nil)
	request.Header.Set("Authorization", "Bearer "+s.opts.Token)
	response := httptest.NewRecorder()
	document.mu.Lock()
	err := service.gateway.issue(response, request, document)
	document.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range response.Result().Cookies() {
		if strings.HasPrefix(cookie.Name, officeGatewayCookie+"_") && cookie.Path == officeGatewayPrefix+"/" {
			return s, document, cookie
		}
	}
	t.Fatal("document open did not issue a scoped gateway cookie")
	return nil, nil, nil
}

func officeGatewayTestRequest(s *Server, cookie *http.Cookie, method, resource string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "https://pogo.example"+officeGatewayPrefix+resource, nil)
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	s.handleOfficeGateway(response, request)
	return response
}

func officeGatewayTestRebind(t *testing.T, s *Server, document *officeDocumentSession, origin string) *http.Cookie {
	t.Helper()
	request := httptest.NewRequest("POST", origin+"/api/office/sessions", nil)
	request.Header.Set("Origin", origin)
	request.Header.Set("Authorization", "Bearer "+s.opts.Token)
	response := httptest.NewRecorder()
	document.mu.Lock()
	document.browserOrigin = "" // Only an unmounted synthetic fixture is rebound.
	err := s.office.gateway.issue(response, request, document)
	document.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range response.Result().Cookies() {
		if strings.HasPrefix(cookie.Name, officeGatewayCookie+"_") && cookie.Path == officeGatewayPrefix+"/" {
			return cookie
		}
	}
	t.Fatal("missing scoped editor cookie")
	return nil
}

func TestOfficeGatewayPublicConfigurationSingleOrigin(t *testing.T) {
	service, document, _ := officeTestService(t)
	s := &Server{office: service}
	response := httptest.NewRecorder()
	s.handleOfficeConfig(response, httptest.NewRequest("GET", "/api/office/config", nil))
	if !strings.Contains(response.Body.String(), `"browserURL":"/office/editor"`) || strings.Contains(response.Body.String(), "office.invalid") {
		t.Fatal("container URL escaped the browser config")
	}
	response = httptest.NewRecorder()
	s.writeOfficeConfig(response, document, "test-instance-unique", [32]byte{})
	if !strings.Contains(response.Body.String(), `"browserURL":"/office/editor"`) {
		t.Fatal("document editor did not use the sole Pogo resource path")
	}
}

func TestOfficeGatewayBootstrapFailureDoesNotBindEditor(t *testing.T) {
	s, document, _ := officeGatewayTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "assets") }))
	s.office.gateway.mu.Lock()
	s.office.gateway.closed = true
	s.office.gateway.mu.Unlock()
	request := httptest.NewRequest("POST", "https://pogo.example/api/office/sessions", nil)
	request.Header.Set("Authorization", "Bearer "+s.opts.Token)
	response := httptest.NewRecorder()
	s.writeOfficeConfigRequest(response, request, document, "new-instance-owned", [32]byte{}, document.panelPath)
	document.mu.Lock()
	instance, bootstrap := document.instanceID, document.bootstrapToken
	document.mu.Unlock()
	if response.Code != 503 || instance != "" || bootstrap != "" {
		t.Fatal("gateway rejection left an undisclosed editor instance bound to the document")
	}
}

func TestOfficeGatewayExistingStatusRenewsWithoutExtraPolling(t *testing.T) {
	s, document, cookie := officeGatewayTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "assets") }))
	s.office.gateway.mu.Lock()
	s.office.gateway.grants[cookie.Value].expires = time.Now().Add(time.Hour)
	s.office.gateway.mu.Unlock()
	request := httptest.NewRequest("GET", "https://pogo.example/api/office/sessions/"+document.id+"/status", nil)
	request.Header.Set("Authorization", "Bearer "+s.opts.Token)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	s.office.gateway.renew(response, request, document)
	if len(response.Result().Cookies()) != 3 || time.Until(s.office.gateway.grant(cookie.Value).expires) < officeGatewayTTL-time.Minute {
		t.Fatal("ordinary authenticated status did not renew its existing editor grant")
	}
	response = httptest.NewRecorder()
	s.office.gateway.renew(response, request, document)
	if len(response.Result().Cookies()) != 0 {
		t.Fatal("ordinary status kept reissuing cookies without a renewal need")
	}
}

func TestOfficeGatewayClosedDocumentsDoNotExhaustQuota(t *testing.T) {
	s, document, cookie := officeGatewayTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "assets") }))
	cwd, file, hub := document.cwd, document.absolute, document.hub
	for index := 0; index < officeGatewayMaxDocuments+8; index++ {
		if err := s.office.closeDocument(document); err != nil {
			t.Fatal(err)
		}
		if grant := s.office.gateway.grant(cookie.Value); grant == nil || len(grant.documents) != 0 {
			t.Fatal("closed editor kept consuming the browser's live-document quota")
		}
		next, err := s.office.open("conversation-1", cwd, nil, hub, file, "sample.docx", nil)
		if err != nil {
			t.Fatal(err)
		}
		document = next
		request := httptest.NewRequest("POST", "https://pogo.example/api/office/sessions", nil)
		request.Header.Set("Authorization", "Bearer "+s.opts.Token)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		document.mu.Lock()
		err = s.office.gateway.issue(response, request, document)
		document.mu.Unlock()
		if err != nil {
			t.Fatalf("open/close cycle %d was incorrectly quota-blocked: %v", index, err)
		}
		if grant := s.office.gateway.grant(cookie.Value); grant == nil || len(grant.documents) != 1 {
			t.Fatal("reopened editor did not reuse its existing bounded browser grant")
		}
	}
}

func TestOfficeGatewayAuthorizedStreamingAndHeaderBoundary(t *testing.T) {
	var saw atomic.Bool
	s, _, cookie := officeGatewayTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/web-apps/apps/api/documents/api.js" {
			t.Error("gateway did not strip its virtual path")
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Forwarded") != "" || r.Header.Get("X-Forwarded-For") != "" {
			t.Error("Pogo credentials or client forwarding metadata reached the engine")
		}
		if r.Header.Get("X-Forwarded-Host") != "pogo.example/office/editor" || r.Header.Get("X-Forwarded-Proto") != "https" || r.Header.Get("X-Forwarded-Prefix") != officeGatewayPrefix {
			t.Error("trusted virtual path or HTTPS was lost")
		}
		mac := hmac.New(sha256.New, []byte(strings.Repeat("x", 64)))
		mac.Write([]byte("pogo-office-gateway/v1\nhttps\npogo.example/office/editor\n/office/editor"))
		if r.Header.Get("X-Pogo-Office-Gateway") != base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) {
			t.Error("runtime gateway signature did not match its dedicated contract")
		}
		if r.URL.Query().Get("token") != "" {
			t.Error("durable account query credential reached the engine")
		}
		w.Header().Set("Set-Cookie", "pi_session=upstream-injection; Path=/")
		w.Header().Set("Service-Worker-Allowed", "/")
		w.WriteHeader(200)
		io.WriteString(w, "first chunk\n")
		w.(http.Flusher).Flush()
		io.WriteString(w, "second chunk\n")
		saw.Store(true)
	}))
	request := httptest.NewRequest("GET", "https://pogo.example/office/editor/web-apps/apps/api/documents/api.js?token="+s.opts.Token, nil)
	request.AddCookie(cookie)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "pogo-cookie"})
	request.Header.Set("Authorization", "Bearer "+s.opts.Token)
	for _, name := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-Prefix", "X-Pogo-Office-Gateway"} {
		request.Header.Set(name, "untrusted")
	}
	response := httptest.NewRecorder()
	s.handleOfficeGateway(response, request)
	if response.Code != 200 || !saw.Load() || response.Body.String() != "first chunk\nsecond chunk\n" {
		t.Fatal("authorized resource was not streamed")
	}
	if response.Header().Get("Set-Cookie") != "" || response.Header().Get("Service-Worker-Allowed") != "" {
		t.Fatal("upstream cookies or worker scope escaped its mount")
	}
}

func TestOfficeGatewayServiceWorkerRetainsRuntimeScope(t *testing.T) {
	s, _, cookie := officeGatewayTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Service-Worker-Allowed", "/")
		io.WriteString(w, "worker")
	}))
	version := "9.4.0-739a98bdfa295b1044f778b58b61df76-pogo-" + strings.Repeat("a", 64)
	response := officeGatewayTestRequest(s, cookie, "GET", "/"+version+"/document_editor_service_worker.js")
	if response.Code != 200 || response.Header().Get("Service-Worker-Allowed") != officeGatewayPrefix+"/"+version+"/" {
		t.Fatal("engine worker escaped its verified version mount")
	}
}

func TestOfficeGatewayNativeExportBoundToDocumentCommand(t *testing.T) {
	var requests atomic.Int64
	s, document, cookie := officeGatewayTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); io.WriteString(w, "export") }))
	cmd := url.QueryEscape(`{"id":"` + document.key + `","c":"save","format":"xlsx"}`)
	if response := officeGatewayTestRequest(s, cookie, "POST", "/downloadas/"+document.key+"?cmd="+cmd); response.Code != 200 {
		t.Fatal("current canvas export did not enter its scoped native endpoint")
	}
	for _, resource := range []string{"/downloadas/" + document.key, "/downloadas/another-owner?cmd=" + cmd, "/downloadas/" + document.key + "?cmd=" + url.QueryEscape(`{"id":"another-owner"}`)} {
		if response := officeGatewayTestRequest(s, cookie, "POST", resource); response.Code != 404 {
			t.Fatal("native export accepted an unbound command identity")
		}
	}
	if response := officeGatewayTestRequest(s, cookie, "GET", "/cache/files/data/conv_"+document.key+"_xlsx/output.xlsx"); response.Code != 200 {
		t.Fatal("current document converter result was not scoped to its editor")
	}
	if response := officeGatewayTestRequest(s, cookie, "GET", "/cache/files/data/conv_another-owner_xlsx/output.xlsx"); response.Code != 404 {
		t.Fatal("converter namespace bypassed document ownership")
	}
	if requests.Load() != 2 {
		t.Fatal("rejected export command reached the engine")
	}
}

func TestOfficeGatewayDocumentScopeAndServiceRouteDeny(t *testing.T) {
	var requests atomic.Int64
	s, document, cookie := officeGatewayTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); io.WriteString(w, "resource") }))
	for _, resource := range []string{"/healthcheck", "/info", "/internal/status", "/CommandService.ashx", "/converter", "/ai-proxy", "/example/", "/doc/another-owner/ws", "/cache/files/another-owner/output.xlsx", "/web-apps/../internal/status", "/web-apps%2finternal/status"} {
		if response := officeGatewayTestRequest(s, cookie, "GET", resource); response.Code != 404 {
			t.Fatalf("unsupported/scoped path %s returned %d", resource, response.Code)
		}
	}
	if response := officeGatewayTestRequest(s, nil, "GET", "/web-apps/apps/api/documents/api.js"); response.Code != 401 {
		t.Fatal("editor resources were exposed without an admitted document")
	}
	for _, resource := range []string{"/doc/" + document.key + "/c/", "/cache/files/data/" + document.key + "_123/output.xlsx?token=engine-jwt", "/9.4.0-739a98bdfa295b1044f778b58b61df76-pogo-" + strings.Repeat("a", 64) + "/sdkjs/cell/sdk-all.js"} {
		if response := officeGatewayTestRequest(s, cookie, "GET", resource); response.Code != 200 {
			t.Fatalf("owned editor resource %s returned %d", resource, response.Code)
		}
	}
	if requests.Load() != 3 {
		t.Fatal("denied editor request reached the engine")
	}
	for _, resource := range []string{"/plugins.json", "/9.4.0-739a98bdfa295b1044f778b58b61df76/themes.json"} {
		if response := officeGatewayTestRequest(s, cookie, "GET", resource); response.Code != 200 {
			t.Fatal("fixed native editor catalog was blocked")
		}
		if response := officeGatewayTestRequest(s, cookie, "POST", resource); response.Code != 404 {
			t.Fatal("native editor catalog admitted a write")
		}
	}
	document.mu.Lock()
	document.closed = true
	document.mu.Unlock()
	if response := officeGatewayTestRequest(s, cookie, "GET", "/doc/"+document.key+"/c/info"); response.Code != 404 {
		t.Fatal("closed document kept a dynamic engine capability")
	}
}

func TestOfficeGatewayUploadKeepsOnlyBoundEngineJWT(t *testing.T) {
	var authorization string
	s, document, cookie := officeGatewayTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		io.WriteString(w, "upload")
	}))
	token, err := s.office.signJWT(map[string]any{"document": map[string]any{"key": document.key}, "exp": time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "https://pogo.example/office/editor/upload/"+document.key, nil)
	request.AddCookie(cookie)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	s.handleOfficeGateway(response, request)
	if response.Code != 200 || authorization != "Bearer "+token {
		t.Fatal("verified, document-bound engine upload token was removed")
	}
	other, err := s.office.signJWT(map[string]any{"key": "another-owner"})
	if err != nil {
		t.Fatal(err)
	}
	for _, credential := range []string{other, s.opts.Token, "forged-engine-token"} {
		request = httptest.NewRequest("POST", "https://pogo.example/office/editor/upload/"+document.key, nil)
		request.AddCookie(cookie)
		request.Header.Set("Authorization", "Bearer "+credential)
		response = httptest.NewRecorder()
		s.handleOfficeGateway(response, request)
		if authorization != "" {
			t.Fatal("unverified or cross-document credentials crossed the engine boundary")
		}
	}
}

func TestOfficeGatewayAccountChangeLogoutAndExpiry(t *testing.T) {
	s, document, cookie := officeGatewayTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	request := httptest.NewRequest("POST", "https://pogo.example/api/logout", nil)
	request.AddCookie(&http.Cookie{Name: officeGatewayCookieName(officeGatewayLogoutCookie, "https://pogo.example"), Value: cookie.Value})
	response := httptest.NewRecorder()
	s.handleLogout(response, request)
	if response := officeGatewayTestRequest(s, cookie, "GET", "/web-apps/apps/api/documents/api.js"); response.Code != 401 {
		t.Fatal("logout did not revoke editor capabilities")
	}
	if len(response.Result().Cookies()) != 4 {
		t.Fatal("logout did not expire both editor cookies and its revocation companion")
	}
	request = httptest.NewRequest("POST", "https://pogo.example/api/office/sessions", nil)
	request.Header.Set("Authorization", "Bearer "+s.opts.Token)
	response = httptest.NewRecorder()
	document.mu.Lock()
	err := s.office.gateway.issue(response, request, document)
	document.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, next := range response.Result().Cookies() {
		if strings.HasPrefix(next.Name, officeGatewayCookie+"_") && next.Path == officeGatewayPrefix+"/" {
			cookie = next
		}
	}
	s.office.gateway.mu.Lock()
	s.office.gateway.grants[cookie.Value].expires = time.Now().Add(-time.Second)
	s.office.gateway.mu.Unlock()
	if response := officeGatewayTestRequest(s, cookie, "GET", "/web-apps/apps/api/documents/api.js"); response.Code != 401 {
		t.Fatal("expired editor capability remained usable")
	}
	// A normal authenticated account cannot inherit another owner's grant.
	s.auth = &authStore{path: "test-users", users: map[string]userRecord{"alice": {Username: "alice", Token: "alice-test-token"}, "bob": {Username: "bob", Token: "bob-test-token"}}}
	owner := identity.LocalScope("alice")
	document.owner = &owner
	request = httptest.NewRequest("POST", "https://pogo.example/api/office/sessions", nil)
	request.Header.Set("Authorization", "Bearer alice-test-token")
	response = httptest.NewRecorder()
	document.mu.Lock()
	err = s.office.gateway.issue(response, request, document)
	document.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, next := range response.Result().Cookies() {
		if strings.HasPrefix(next.Name, officeGatewayCookie+"_") && next.Path == officeGatewayPrefix+"/" {
			cookie = next
		}
	}
	request = httptest.NewRequest("GET", "https://pogo.example/office/editor/web-apps/apps/api/documents/api.js", nil)
	request.AddCookie(cookie)
	request.Header.Set("Authorization", "Bearer bob-test-token")
	response = httptest.NewRecorder()
	s.handleOfficeGateway(response, request)
	if response.Code != 403 {
		t.Fatal("cross-account browser grant was accepted")
	}
	s.auth.mu.Lock()
	delete(s.auth.users, "alice")
	s.auth.mu.Unlock()
	if response := officeGatewayTestRequest(s, cookie, "GET", "/doc/"+document.key+"/c/info"); response.Code != 401 {
		t.Fatal("deleted user retained an editor grant")
	}
}

func TestOfficeGatewayRedirectAndEditorJWTPreserved(t *testing.T) {
	var upstreamHost string
	s, _, cookie := officeGatewayTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != "signed-engine-token" {
			t.Error("editor's own JWT query was stripped")
		}
		if r.URL.Query().Get("external") == "yes" {
			w.Header().Set("Location", "https://other-tool.invalid/private")
		} else {
			w.Header().Set("Location", "http://"+upstreamHost+"/9.4.0-129/web-apps/apps/documenteditor/main/index.html?next=1")
		}
		w.WriteHeader(302)
	}))
	u, _ := url.Parse(s.office.cfg.serviceURL)
	upstreamHost = u.Host
	response := officeGatewayTestRequest(s, cookie, "GET", "/web-apps/apps/api/documents/api.js?token=signed-engine-token")
	if response.Code != 302 || response.Header().Get("Location") != "/office/editor/9.4.0-129/web-apps/apps/documenteditor/main/index.html?next=1" {
		t.Fatal("private editor redirect escaped the sole Pogo path")
	}
	if response := officeGatewayTestRequest(s, cookie, "GET", "/web-apps/apps/api/documents/api.js?token=signed-engine-token&external=yes"); response.Code != 502 {
		t.Fatal("external tool redirect escaped the gateway")
	}
}

func TestOfficeGatewayWebSocketStreamsAndRevokes(t *testing.T) {
	s, document, cookie := officeGatewayTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer connection.CloseNow()
		for {
			kind, data, err := connection.Read(r.Context())
			if err != nil {
				return
			}
			if err = connection.Write(r.Context(), kind, data); err != nil {
				return
			}
		}
	}))
	public := httptest.NewServer(http.HandlerFunc(s.handleOfficeGateway))
	defer public.Close()
	cookie = officeGatewayTestRebind(t, s, document, public.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	header := http.Header{}
	header.Set("Cookie", fmt.Sprintf("%s=%s", cookie.Name, cookie.Value))
	connection, _, err := websocket.Dial(ctx, strings.Replace(public.URL, "http://", "ws://", 1)+officeGatewayPrefix+"/doc/"+document.key+"/c/websocket", &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	if err = connection.Write(ctx, websocket.MessageText, []byte("same-origin editor")); err != nil {
		t.Fatal(err)
	}
	_, echo, err := connection.Read(ctx)
	if err != nil || string(echo) != "same-origin editor" {
		t.Fatal("gateway did not stream editor WebSocket frames")
	}
	s.office.gateway.mu.Lock()
	s.office.gateway.revokeLocked(cookie.Value)
	s.office.gateway.mu.Unlock()
	if _, _, err = connection.Read(ctx); err == nil || ctx.Err() != nil {
		t.Fatal("logout/revocation left the editor WebSocket open")
	}
}

func TestOfficeGatewayDocumentCloseDisconnectsItsWebSocket(t *testing.T) {
	s, document, cookie := officeGatewayTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer connection.CloseNow()
		for {
			kind, data, err := connection.Read(r.Context())
			if err != nil {
				return
			}
			if err = connection.Write(r.Context(), kind, data); err != nil {
				return
			}
		}
	}))
	public := httptest.NewServer(http.HandlerFunc(s.handleOfficeGateway))
	defer public.Close()
	cookie = officeGatewayTestRebind(t, s, document, public.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	header := http.Header{}
	header.Set("Cookie", fmt.Sprintf("%s=%s", cookie.Name, cookie.Value))
	connection, _, err := websocket.Dial(ctx, strings.Replace(public.URL, "http://", "ws://", 1)+officeGatewayPrefix+"/doc/"+document.key+"/c/websocket", &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	if err = s.office.closeDocument(document); err != nil {
		t.Fatal(err)
	}
	if _, _, err = connection.Read(ctx); err == nil || ctx.Err() != nil {
		t.Fatal("closing a clean document did not immediately disconnect its engine stream")
	}
	if grant := s.office.gateway.grant(cookie.Value); grant == nil || len(grant.documents) != 0 {
		t.Fatal("document close did not revoke its dynamic resource keys")
	}
}
