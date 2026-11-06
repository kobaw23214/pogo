package webgateway

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/webservice"
	"nhooyr.io/websocket"
)

type routerFixture struct {
	router  *Router
	public  string
	client  *http.Client
	runtime *httptest.Server
	close   func()
}

func testRouter(t *testing.T, app, main http.Handler, change func(*RouterOptions), secure bool, entryOptions ...Config) *routerFixture {
	t.Helper()
	if app == nil {
		app = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, r.URL.RequestURI()) })
	}
	if main == nil {
		main = http.NotFoundHandler()
	}
	runtime := httptest.NewServer(app)
	t.Cleanup(runtime.Close)
	primary := httptest.NewServer(main)
	t.Cleanup(primary.Close)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	scheme := "http"
	if secure {
		scheme = "https"
	}
	public := scheme + "://pogo.example.test:" + strconv.Itoa(port)
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
	}}
	var tlsConfig *tls.Config
	if secure {
		certificate, pool := routerCertificate(t)
		tlsConfig = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	opts := RouterOptions{MainUpstream: primary.URL, MainHosts: []string{"pogo.example.test:" + strconv.Itoa(port)}, PublicOrigin: public, PreviewDomain: "preview.example.test", AllowInsecureHTTP: !secure, AllowLoopbackUpstream: true, ProbeClient: client, ControlTimeout: time.Hour}
	if change != nil {
		change(&opts)
	}
	router, err := NewRouter(opts)
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	entryConfig := Config{}
	if len(entryOptions) > 0 {
		entryConfig = entryOptions[0]
	}
	entryConfig.TURNAddress = strings.TrimPrefix(primary.URL, "http://")
	entryConfig.TLSConfig = tlsConfig
	entry, err := New(listener, entryConfig)
	if err != nil {
		listener.Close()
		router.Close()
		t.Fatal(err)
	}
	server := &http.Server{Handler: entry.HTTPHandler(router.Handler()), ConnContext: entry.ConnContext}
	done := make(chan error, 1)
	go func() { done <- server.Serve(entry.HTTPListener()) }()
	closed := sync.Once{}
	closeFn := func() {
		closed.Do(func() {
			entry.Close()
			server.Close()
			router.Close()
			transport.CloseIdleConnections()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("router HTTP server did not exit")
			}
		})
	}
	t.Cleanup(closeFn)
	return &routerFixture{router: router, public: public, client: client, runtime: runtime, close: closeFn}
}

func routerCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(123), Subject: pkix.Name{CommonName: "gateway fixture"}, DNSNames: []string{"pogo.example.test", "*.preview.example.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool.AddCert(leaf)
	return certificate, pool
}

func (f *routerFixture) record() webservice.Record {
	return webservice.Record{SchemaVersion: 1, ServiceID: "ws-one", Owner: "owner-one", Session: "session-one", Installation: "install-one", Spec: webservice.Spec{Name: "example", ProjectRoot: "project", Profile: "python-web", StartArgv: []string{"python", "app.py"}, HealthPath: "/health"}, SpecDigest: "digest-one", Generation: 1, Revision: 1, State: "ready", LeaseExpiresAt: time.Now().Add(time.Minute), Instance: webservice.Instance{ID: "container-one", NetworkID: "network-one", Upstream: strings.TrimPrefix(f.runtime.URL, "http://")}, OperationID: "open-one"}
}

func applyOpen(t *testing.T, f *routerFixture, rec webservice.Record) webservice.Grant {
	t.Helper()
	if err := f.router.Apply(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	grant, err := f.router.Open(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	return grant
}
func response(t *testing.T, c *http.Client, method, address string, body io.Reader, headers map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, address, body)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(data)
}
func errorCode(err error) string {
	var e *webservice.Error
	if errors.As(err, &e) {
		return e.Code
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func TestRouterMainAndApplicationPoliciesAreSeparate(t *testing.T) {
	mainSeen := make(chan http.Header, 1)
	appSeen := make(chan http.Header, 1)
	f := testRouter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appSeen <- r.Header.Clone()
		w.Header().Set("Set-Cookie", "attack=1; Domain=example.test")
		w.Header().Set("Content-Security-Policy", "default-src *")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Cache-Control", "public, max-age=31536000")
		w.Header().Set("X-Frame-Options", "DENY")
		_, _ = io.WriteString(w, "app")
	}), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Clone()
		h.Set("Observed-Host", r.Host)
		mainSeen <- h
		w.Header().Set("Set-Cookie", "pogo=keep")
		w.Header().Set("Content-Security-Policy", "main-policy")
		_, _ = io.WriteString(w, "main")
	}), nil, false)
	headers := map[string]string{"Authorization": "Bearer private", "Cookie": "pogo=private", "Origin": f.public, "X-Forwarded-Host": "attacker.invalid", "Forwarded": "host=attacker.invalid"}
	resp, body := response(t, f.client, "GET", f.public+"/api/example", nil, headers)
	if resp.StatusCode != 200 || body != "main" || resp.Header.Get("Set-Cookie") != "pogo=keep" || resp.Header.Get("Content-Security-Policy") != "main-policy" {
		t.Fatalf("main response changed: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	seen := <-mainSeen
	u, _ := url.Parse(f.public)
	if seen.Get("Authorization") != "Bearer private" || seen.Get("Cookie") != "pogo=private" || seen.Get("Origin") != f.public || seen.Get("Observed-Host") != u.Host || seen.Get("X-Forwarded-Host") != u.Host || seen.Get("Forwarded") != "" {
		t.Fatalf("main auth/authority changed: %v", seen)
	}
	grant := applyOpen(t, f, f.record())
	resp, body = response(t, f.client, "POST", grant.PreviewURL+"api", strings.NewReader("{}"), headers)
	seen = <-appSeen
	if resp.StatusCode != 200 || body != "app" || seen.Get("Authorization") != "" || seen.Get("Cookie") != "" || seen.Get("Forwarded") != "" {
		t.Fatalf("dynamic credentials leaked: %d %q %v", resp.StatusCode, body, seen)
	}
	if resp.Header.Get("Set-Cookie") != "" || resp.Header.Get("Access-Control-Allow-Credentials") != "" || resp.Header.Get("Access-Control-Allow-Origin") != "*" || resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("X-Frame-Options") != "" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox allow-scripts allow-forms") {
		t.Fatalf("dynamic policy was overridden: %v", resp.Header)
	}
	if grant.BrowserURL != nil {
		t.Fatal("unverified native browser URL was advertised")
	}
}

func TestRouterDynamicHeadersHaveSingleWireValuesForOpaqueOrigin(t *testing.T) {
	f := testRouter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// An upstream may itself emit several policy headers. The gateway must
		// replace all of them and emit exactly one value on the real HTTP wire.
		w.Header().Add("Access-Control-Allow-Origin", "https://first.invalid")
		w.Header().Add("Access-Control-Allow-Origin", "https://second.invalid")
		w.Header().Add("Content-Security-Policy", "default-src *")
		w.Header().Add("Cache-Control", "public, max-age=3600")
		if r.URL.Path == "/redirect" {
			w.Header().Set("Location", "https://outside.invalid/")
			w.WriteHeader(http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}), nil, nil, false)
	rec := f.record()
	grant := applyOpen(t, f, rec)
	assertResponse := func(method, path string, want int) {
		t.Helper()
		resp, _ := response(t, f.client, method, grant.PreviewURL+path, strings.NewReader(`{"counter":1}`), map[string]string{"Origin": "null", "Content-Type": "application/json"})
		if resp.StatusCode != want {
			t.Fatalf("%s %s status=%d want=%d", method, path, resp.StatusCode, want)
		}
		for _, header := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Methods", "Access-Control-Allow-Headers", "Access-Control-Expose-Headers", "Content-Security-Policy", "Cache-Control", "Referrer-Policy", "X-Content-Type-Options", "Cross-Origin-Resource-Policy"} {
			if values := resp.Header.Values(header); len(values) != 1 {
				t.Fatalf("%s %s emitted %d %s values: %q", method, path, len(values), header, values)
			}
		}
		if values := resp.Header.Values("Access-Control-Allow-Origin"); values[0] != "*" {
			t.Fatalf("opaque origin ACAO=%q", values)
		}
	}
	assertResponse(http.MethodGet, "api", http.StatusOK)
	assertResponse(http.MethodPost, "api", http.StatusOK)
	assertResponse(http.MethodOptions, "api", http.StatusNoContent)
	assertResponse(http.MethodGet, "redirect", http.StatusBadGateway)
	f.runtime.Close()
	assertResponse(http.MethodGet, "api", http.StatusBadGateway)
	rec.State = "degraded"
	rec.Revision++
	if err := f.router.Apply(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	assertResponse(http.MethodGet, "api", http.StatusServiceUnavailable)
	if err := f.router.CloseGrant(context.Background(), rec, grant.GrantID); err != nil {
		t.Fatal(err)
	}
	assertResponse(http.MethodGet, "api", http.StatusNotFound)
}

func TestRouterHostRegistryAndScopeCannotBeBypassed(t *testing.T) {
	f := testRouter(t, nil, nil, nil, false)
	rec := f.record()
	grant := applyOpen(t, f, rec)
	for _, host := range []string{"unknown.example.test", "garbage.preview.example.test", strings.Repeat("a", 48) + ".preview.example.test"} {
		u, _ := url.Parse(f.public)
		u.Host = net.JoinHostPort(host, f.router.port)
		resp, _ := response(t, f.client, "GET", u.String(), nil, nil)
		if resp.StatusCode != 404 {
			t.Fatalf("unknown host accepted: %q %d", host, resp.StatusCode)
		}
	}
	other := rec
	other.Owner = "other"
	if code := errorCode(f.router.Apply(context.Background(), other)); code != "PERMISSION_DENIED" {
		t.Fatalf("owner overwrite accepted: %s", code)
	}
	if code := errorCode(f.router.CloseGrant(context.Background(), other, grant.GrantID)); code != "PERMISSION_DENIED" {
		t.Fatalf("cross-owner revoke accepted: %s", code)
	}
	resp, _ := response(t, f.client, "GET", grant.PreviewURL, nil, nil)
	if resp.StatusCode != 200 {
		t.Fatal("valid grant was damaged by denied operations")
	}
	bad := rec
	bad.ServiceID = "bad"
	bad.Instance.Upstream = "8.8.8.8:8080"
	if errorCode(f.router.Apply(context.Background(), bad)) != "INVALID_ROUTE" {
		t.Fatal("public upstream accepted")
	}
	bad.Instance.Upstream = "http://127.0.0.1:8080"
	if errorCode(f.router.Apply(context.Background(), bad)) != "INVALID_ROUTE" {
		t.Fatal("arbitrary URL accepted")
	}
}

func TestRouterGrantOpenIsIdempotentAndGenerationFenced(t *testing.T) {
	f := testRouter(t, nil, nil, nil, false)
	rec := f.record()
	grant := applyOpen(t, f, rec)
	repeated, err := f.router.Open(context.Background(), rec)
	if err != nil || grant.GrantID != repeated.GrantID || grant.PreviewURL != repeated.PreviewURL {
		t.Fatal("open repeated its effect")
	}
	if err := f.router.CloseGrant(context.Background(), rec, grant.GrantID); err != nil {
		t.Fatal(err)
	}
	if code := errorCode(func() error { _, err := f.router.Open(context.Background(), rec); return err }()); code != "GRANT_EXPIRED" {
		t.Fatalf("revoked operation reissued: %s", code)
	}
	rec.OperationID = "open-two"
	grant, err = f.router.Open(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	next := rec
	next.Generation++
	next.Revision++
	next.Instance.ID = "container-two"
	if err := f.router.Apply(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	resp, _ := response(t, f.client, "GET", grant.PreviewURL, nil, nil)
	if resp.StatusCode != 404 {
		t.Fatal("old generation remained reachable")
	}
	if errorCode(f.router.Apply(context.Background(), rec)) != "STALE_ROUTE" {
		t.Fatal("old generation overwrote newer route")
	}
	next.OperationID = "open-three"
	if _, err := f.router.Open(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	next.Revision++
	if err := f.router.Revoke(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	next.Revision++
	if errorCode(f.router.Apply(context.Background(), next)) != "STALE_ROUTE" {
		t.Fatal("revoked generation revived")
	}
}

func TestRouterDegradedRecoversWithoutChangingGrant(t *testing.T) {
	f := testRouter(t, nil, nil, nil, false)
	rec := f.record()
	grant := applyOpen(t, f, rec)
	rec.State = "degraded"
	rec.Revision++
	if err := f.router.Apply(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	resp, _ := response(t, f.client, "GET", grant.PreviewURL, nil, nil)
	if resp.StatusCode != 503 {
		t.Fatalf("degraded response=%d", resp.StatusCode)
	}
	rec.State = "ready"
	rec.Revision++
	if err := f.router.Apply(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	resp, _ = response(t, f.client, "GET", grant.PreviewURL, nil, nil)
	if resp.StatusCode != 200 {
		t.Fatal("grant did not recover")
	}
}

func TestRouterProbeTraversesVerifiedTLSAndBypassesReadinessGate(t *testing.T) {
	f := testRouter(t, nil, nil, nil, true)
	rec := f.record()
	rec.State = "starting"
	if err := f.router.Apply(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if _, err := f.router.Open(context.Background(), rec); errorCode(err) != "SERVICE_NOT_READY" {
		t.Fatal("public open admitted starting service")
	}
	h, err := f.router.Probe(context.Background(), rec)
	if err != nil || h.UpstreamStatus != 200 || h.GatewayStatus != 200 || h.Result != "passed" {
		t.Fatalf("probe=%+v %v", h, err)
	}
	f.router.mu.Lock()
	grants := len(f.router.grants)
	f.router.mu.Unlock()
	if grants != 0 {
		t.Fatal("internal probe grant leaked")
	}
	rec.Spec.HealthPath = "https://attacker.invalid/"
	if _, err := f.router.Probe(context.Background(), rec); errorCode(err) != "INVALID_HEALTH_PATH" {
		t.Fatal("external health target accepted")
	}
}

func TestRouterRepeatedProbesDoNotExhaustSharedEntry(t *testing.T) {
	for _, secure := range []bool{false, true} {
		name := "http"
		if secure {
			name = "https"
		}
		t.Run(name, func(t *testing.T) {
			// This deliberately injects the ordinary pooling fixture client.
			// Router.Probe must close each fresh hostname connection itself,
			// independently of the command's defensive transport settings.
			f := testRouter(t, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "main-remains-available") }), nil, secure, Config{MaxConnections: 4})
			rec := f.record()
			rec.State = "starting"
			if err := f.router.Apply(context.Background(), rec); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 24; i++ {
				health, err := f.router.Probe(context.Background(), rec)
				if err != nil || health.GatewayStatus != http.StatusOK {
					t.Fatalf("probe %d exhausted the entry: %+v %v", i, health, err)
				}
				resp, body := response(t, f.client, http.MethodGet, f.public, nil, nil)
				if resp.StatusCode != http.StatusOK || body != "main-remains-available" {
					t.Fatalf("main failed after probe %d: %d %q", i, resp.StatusCode, body)
				}
			}
		})
	}
}

func TestRouterHealthRedirectOrNon2xxNeverPasses(t *testing.T) {
	for _, code := range []int{302, 404, 503} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			f := testRouter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://outside.invalid/")
				w.WriteHeader(code)
			}), nil, nil, false)
			rec := f.record()
			rec.State = "starting"
			if err := f.router.Apply(context.Background(), rec); err != nil {
				t.Fatal(err)
			}
			if _, err := f.router.Probe(context.Background(), rec); errorCode(err) != "HEALTH_CHECK_FAILED" {
				t.Fatal("non-2xx HTTP was accepted")
			}
		})
	}
}

func TestRouterQuotaAndLeaseExpiry(t *testing.T) {
	f := testRouter(t, nil, nil, nil, false)
	rec := f.record()
	rec.LeaseExpiresAt = time.Now().Add(350 * time.Millisecond)
	if err := f.router.Apply(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	var first webservice.Grant
	for i := 0; i < 4; i++ {
		rec.OperationID = "open-" + strconv.Itoa(i)
		g, err := f.router.Open(context.Background(), rec)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = g
		}
		if !g.ExpiresAt.Equal(rec.LeaseExpiresAt) {
			t.Fatal("grant outlived lease")
		}
	}
	rec.OperationID = "over-quota"
	if _, err := f.router.Open(context.Background(), rec); errorCode(err) != "GRANT_QUOTA_EXCEEDED" {
		t.Fatal("service grant quota ignored")
	}
	time.Sleep(400 * time.Millisecond)
	resp, _ := response(t, f.client, "GET", first.PreviewURL, nil, nil)
	if resp.StatusCode != 404 {
		t.Fatal("expired lease remained accessible")
	}
}

func TestRouterRevocationClosesWebSocketAndControlLossKeepsMain(t *testing.T) {
	f := testRouter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		for {
			kind, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if err := conn.Write(ctx, kind, data); err != nil {
				return
			}
		}
	}), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "main-ok") }), nil, false)
	rec := f.record()
	grant := applyOpen(t, f, rec)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	wsURL := strings.Replace(grant.PreviewURL, "http://", "ws://", 1)
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: f.client})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := conn.Write(ctx, websocket.MessageText, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	_, data, err := conn.Read(ctx)
	if err != nil || !bytes.Equal(data, []byte("hello")) {
		t.Fatal("websocket relay failed")
	}
	if err := f.router.CloseGrant(ctx, rec, grant.GrantID); err != nil {
		t.Fatal(err)
	}
	closedCtx, closedCancel := context.WithTimeout(context.Background(), time.Second)
	defer closedCancel()
	if _, _, err := conn.Read(closedCtx); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("revoke did not close WS: %v", err)
	}
	rec.OperationID = "open-after-close"
	grant, err = f.router.Open(ctx, rec)
	if err != nil {
		t.Fatal(err)
	}
	f.router.suspendControl()
	resp, _ := response(t, f.client, "GET", grant.PreviewURL, nil, nil)
	if resp.StatusCode != 503 {
		t.Fatal("disconnected management remained ready")
	}
	resp, body := response(t, f.client, "GET", f.public, nil, nil)
	if resp.StatusCode != 200 || body != "main-ok" {
		t.Fatal("management loss disabled main site")
	}
}

func TestRouterAdminUnixClientAndPrivatePathProtection(t *testing.T) {
	if err := socketManagementSupported(); err != nil {
		t.Skip("private Unix management is unsupported on this platform")
	}
	f := testRouter(t, nil, nil, func(o *RouterOptions) { o.ControlTimeout = 2 * time.Second }, false)
	parent := filepath.Join(t.TempDir(), "admin")
	socket := filepath.Join(parent, "control.sock")
	server, err := ListenAdmin(socket, f.router)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	info, err := os.Stat(socket)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("management socket is not private")
	}
	client := NewAdminClient(socket)
	defer client.Close()
	ctx := context.Background()
	if err := client.Preflight(ctx); err != nil {
		t.Fatal(err)
	}
	rec := f.record()
	if err := client.Apply(ctx, rec); err != nil {
		t.Fatal(err)
	}
	grant, err := client.Open(ctx, rec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Probe(ctx, rec); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2100 * time.Millisecond)
	resp, _ := response(t, f.client, "GET", grant.PreviewURL, nil, nil)
	if resp.StatusCode != 200 {
		t.Fatal("healthy admin client did not heartbeat")
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	resp, _ = response(t, f.client, "GET", grant.PreviewURL, nil, nil)
	if resp.StatusCode != 503 {
		t.Fatal("closed management session remained available")
	}
	if _, err := ListenAdmin(socket, f.router); err == nil {
		t.Fatal("replaced an active socket")
	}
	bad := filepath.Join(t.TempDir(), "ordinary-file")
	if err := os.WriteFile(bad, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ListenAdmin(bad, f.router); err == nil {
		t.Fatal("ordinary file replaced")
	}
	data, _ := os.ReadFile(bad)
	if string(data) != "preserve" {
		t.Fatal("ordinary file damaged")
	}
}

func TestRouterNewControllerRevokesOldGrantsUntilFreshApply(t *testing.T) {
	if err := socketManagementSupported(); err != nil {
		t.Skip("private Unix management is unsupported on this platform")
	}
	f := testRouter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws" {
			_, _ = io.WriteString(w, "app-ok")
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			kind, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			if err := conn.Write(r.Context(), kind, data); err != nil {
				return
			}
		}
	}), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "main-ok") }), nil, false)
	socket := filepath.Join(t.TempDir(), "admin", "control.sock")
	server, err := ListenAdmin(socket, f.router)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	old := NewAdminClient(socket)
	defer old.Close()
	ctx := context.Background()
	if err := old.Preflight(ctx); err != nil {
		t.Fatal(err)
	}
	rec := f.record()
	if err := old.Apply(ctx, rec); err != nil {
		t.Fatal(err)
	}
	grant, err := old.Open(ctx, rec)
	if err != nil {
		t.Fatal(err)
	}
	wsCtx, wsCancel := context.WithTimeout(ctx, 3*time.Second)
	defer wsCancel()
	conn, _, err := websocket.Dial(wsCtx, strings.Replace(grant.PreviewURL, "http://", "ws://", 1)+"ws", &websocket.DialOptions{HTTPClient: f.client})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := conn.Write(wsCtx, websocket.MessageText, []byte("before-takeover")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.Read(wsCtx); err != nil {
		t.Fatal(err)
	}
	fresh := NewAdminClient(socket)
	defer fresh.Close()
	if err := fresh.Preflight(ctx); err != nil {
		t.Fatal(err)
	}
	closedCtx, closedCancel := context.WithTimeout(ctx, time.Second)
	defer closedCancel()
	if _, _, err := conn.Read(closedCtx); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("controller takeover did not close old WS: %v", err)
	}
	resp, _ := response(t, f.client, http.MethodGet, grant.PreviewURL, nil, nil)
	if resp.StatusCode != 404 {
		t.Fatal("old bearer URL recovered after takeover")
	}
	rec.OperationID = "open-after-takeover"
	if _, err := fresh.Open(ctx, rec); errorCode(err) != "SERVICE_NOT_READY" {
		t.Fatalf("old ready route was trusted without fresh Apply: %v", err)
	}
	if err := fresh.Apply(ctx, rec); err != nil {
		t.Fatal(err)
	}
	replayed := rec
	replayed.OperationID = "open-one"
	if _, err := fresh.Open(ctx, replayed); errorCode(err) != "GRANT_EXPIRED" {
		t.Fatalf("takeover reissued the old open operation: %v", err)
	}
	newGrant, err := fresh.Open(ctx, rec)
	if err != nil {
		t.Fatal(err)
	}
	if newGrant.PreviewURL == grant.PreviewURL {
		t.Fatal("old bearer was reused")
	}
	if err := fresh.Preflight(ctx); err != nil {
		t.Fatal(err)
	}
	if err := fresh.raw(ctx, "heartbeat", webservice.Record{}, "", nil); err != nil {
		t.Fatal(err)
	}
	resp, _ = response(t, f.client, http.MethodGet, newGrant.PreviewURL, nil, nil)
	if resp.StatusCode != 200 {
		t.Fatal("same-controller retry or heartbeat revoked current grant")
	}
	if err := old.Apply(ctx, rec); errorCode(err) != "CONTROL_SUPERSEDED" {
		t.Fatalf("superseded controller applied a route: %v", err)
	}
	if _, err := old.Open(ctx, rec); errorCode(err) != "CONTROL_SUPERSEDED" {
		t.Fatalf("superseded controller signed a grant: %v", err)
	}
	_ = old.Close()
	resp, _ = response(t, f.client, http.MethodGet, newGrant.PreviewURL, nil, nil)
	if resp.StatusCode != 200 {
		t.Fatal("old controller close disabled new controller")
	}
	resp, body := response(t, f.client, http.MethodGet, f.public, nil, nil)
	if resp.StatusCode != 200 || body != "main-ok" {
		t.Fatal("takeover disabled the main site")
	}
	// A takeover keeps the generation/revision fence, including stopped slots.
	rec.Revision++
	if err := fresh.Revoke(ctx, rec); err != nil {
		t.Fatal(err)
	}
	third := NewAdminClient(socket)
	defer third.Close()
	if err := third.Preflight(ctx); err != nil {
		t.Fatal(err)
	}
	rec.Revision++
	if err := third.Apply(ctx, rec); errorCode(err) != "STALE_ROUTE" {
		t.Fatalf("takeover discarded a revoked-generation tombstone: %v", err)
	}
}

type blockedAdminContext struct {
	context.Context
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *blockedAdminContext) Err() error {
	c.once.Do(func() { close(c.entered) })
	<-c.release
	return nil
}

func TestRouterControllerTakeoverFencesInFlightMutations(t *testing.T) {
	if err := socketManagementSupported(); err != nil {
		t.Skip("private Unix management is unsupported on this platform")
	}
	for _, action := range []string{"apply", "open"} {
		t.Run(action, func(t *testing.T) {
			f := testRouter(t, nil, nil, nil, false)
			socket := filepath.Join(t.TempDir(), "admin", "control.sock")
			server, err := ListenAdmin(socket, f.router)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			old := NewAdminClient(socket)
			defer old.Close()
			if err := old.Preflight(context.Background()); err != nil {
				t.Fatal(err)
			}
			rec := f.record()
			if err := old.Apply(context.Background(), rec); err != nil {
				t.Fatal(err)
			}
			pending := rec
			pending.OperationID = "pending-old-open"
			if action == "apply" {
				pending.Revision++
			}
			blocked := &blockedAdminContext{Context: context.Background(), entered: make(chan struct{}), release: make(chan struct{})}
			var unblock sync.Once
			release := func() { unblock.Do(func() { close(blocked.release) }) }
			defer release()
			oldReply := httptest.NewRecorder()
			oldDone := make(chan struct{})
			oldBody, _ := json.Marshal(adminRequest{Action: action, ClientID: old.id, Record: pending})
			go func() {
				defer close(oldDone)
				request := httptest.NewRequest(http.MethodPost, "http://unix/control", bytes.NewReader(oldBody)).WithContext(blocked)
				server.serveHTTP(oldReply, request)
			}()
			select {
			case <-blocked.entered:
			case <-time.After(time.Second):
				t.Fatal("old mutation did not reach the execution barrier")
			}
			fresh := NewAdminClient(socket)
			defer fresh.Close()
			newDone := make(chan error, 1)
			go func() { newDone <- fresh.Preflight(context.Background()) }()
			select {
			case err := <-newDone:
				release()
				<-oldDone
				t.Fatalf("controller changed while an authorized %s was still paused: %v", action, err)
			case <-time.After(50 * time.Millisecond):
			}
			release()
			select {
			case <-oldDone:
			case <-time.After(time.Second):
				t.Fatal("old mutation did not finish")
			}
			if err := <-newDone; err != nil {
				t.Fatal(err)
			}
			var result adminResponse
			if err := json.Unmarshal(oldReply.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Error != nil {
				t.Fatalf("previously authorized mutation failed before takeover: %v", result.Error)
			}
			if action == "open" {
				var grant webservice.Grant
				if err := json.Unmarshal(result.Result, &grant); err != nil {
					t.Fatal(err)
				}
				resp, _ := response(t, f.client, http.MethodGet, grant.PreviewURL, nil, nil)
				if resp.StatusCode != 404 {
					t.Fatal("in-flight old open survived controller takeover")
				}
			}
			pending.OperationID = "fresh-controller-open"
			if _, err := fresh.Open(context.Background(), pending); errorCode(err) != "SERVICE_NOT_READY" {
				t.Fatal("new controller could open before republishing")
			}
			if err := fresh.Apply(context.Background(), pending); err != nil {
				t.Fatal(err)
			}
			if _, err := fresh.Open(context.Background(), pending); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRouterConfigurationFailsClosed(t *testing.T) {
	for _, change := range []func(*RouterOptions){func(o *RouterOptions) { o.PublicOrigin = "" }, func(o *RouterOptions) { o.AllowInsecureHTTP = false }, func(o *RouterOptions) { o.MainHosts = nil }, func(o *RouterOptions) { o.PreviewDomain = "*.example.test" }, func(o *RouterOptions) { o.MainHosts = []string{"other.example.test:19770"} }} {
		opts := RouterOptions{MainUpstream: "http://pogo:7777", MainHosts: []string{"pogo.example.test:19770"}, PublicOrigin: "http://pogo.example.test:19770", PreviewDomain: "preview.example.test", AllowInsecureHTTP: true}
		change(&opts)
		router, err := NewRouter(opts)
		if err == nil {
			router.Close()
			t.Fatal("invalid configuration accepted")
		}
	}
}
