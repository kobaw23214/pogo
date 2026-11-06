package webgateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"net/url"
	"nhooyr.io/websocket"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testPathRouter(t *testing.T, app, main http.Handler, secure bool) *routerFixture {
	t.Helper()
	f := testRouter(t, app, main, func(opts *RouterOptions) {
		opts.PreviewMode, opts.PreviewDomain = "path", ""
		u, _ := url.Parse(opts.PublicOrigin)
		if !secure {
			u.Host = strings.Replace(u.Host, "pogo.example.test", "127.0.0.1", 1)
		}
		opts.PublicOrigin, opts.MainHosts = u.String(), []string{u.Host}
	}, secure)
	f.public = f.router.origin.String()
	return f
}

func TestPathRouterConfiguration(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "192.168.1.50", "10.1.2.3", "169.254.1.2", "[::1]", "[fd00::1]", "localhost", "0.0.0.0", "[::]", "8.8.8.8", "pogo.example.test", "[fe80::1%25en0]"} {
		t.Run(host, func(t *testing.T) {
			opts := RouterOptions{MainUpstream: "http://127.0.0.1:7777", MainHosts: []string{host + ":19770"}, PublicOrigin: "http://" + host + ":19770", PreviewMode: "path", AllowInsecureHTTP: true}
			r, err := NewRouter(opts)
			allowed := host != "0.0.0.0" && host != "[::]" && host != "8.8.8.8" && host != "pogo.example.test" && host != "[fe80::1%25en0]"
			if (err == nil) != allowed {
				t.Fatalf("allowed=%t err=%v", allowed, err)
			}
			if r != nil {
				r.Close()
			}
		})
	}
	base := RouterOptions{MainUpstream: "http://127.0.0.1:7777", MainHosts: []string{"127.0.0.1:19770"}, PublicOrigin: "http://127.0.0.1:19770", PreviewMode: "path"}
	if r, err := NewRouter(base); err == nil {
		r.Close()
		t.Fatal("HTTP accepted without permission")
	}
	base.AllowInsecureHTTP = true
	for _, mode := range []string{"typo", ""} {
		base.PreviewMode = mode
		if r, err := NewRouter(base); err == nil {
			r.Close()
			t.Fatal("invalid mode or missing preview DNS accepted")
		}
	}
	base.PreviewMode = "path"
	base.MainHosts = []string{"localhost:19770"}
	if r, err := NewRouter(base); err == nil {
		r.Close()
		t.Fatal("public origin outside allowlist accepted")
	}
}

func TestPathRouterRequestsAndPolicies(t *testing.T) {
	type observed struct {
		method, uri, body string
		headers           http.Header
	}
	seen := make(chan observed, 1)
	f := testPathRouter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		seen <- observed{r.Method, r.URL.RequestURI(), string(data), r.Header.Clone()}
		for k, v := range map[string]string{"Set-Cookie": "pogo=attack", "Content-Location": "/api/private", "Link": "</api/private>;rel=preload", "Refresh": "0;url=/api/private", "Alt-Svc": "h2=outside.invalid", "Report-To": "attack", "NEL": "attack", "Access-Control-Allow-Credentials": "true", "Strict-Transport-Security": "max-age=0", "Service-Worker-Allowed": "/", "Accept-CH": "Sec-CH-UA-Full-Version-List"} {
			w.Header().Set(k, v)
		}
		_, _ = io.WriteString(w, "app-ok")
	}), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "pogo=keep")
		_, _ = io.WriteString(w, "main-ok")
	}), false)
	grant := applyOpen(t, f, f.record())
	if !strings.HasPrefix(grant.PreviewURL, f.public+previewPathPrefix) || grant.BrowserURL != nil || !strings.Contains(strings.Join(grant.Limitations, " "), "relative resource and API URLs") {
		t.Fatalf("grant=%+v", grant)
	}
	uri := "api/%61%20b/%E4%B8%AD?x=%2F&x=second+value&redirect=..%2Fapi"
	resp, body := response(t, f.client, http.MethodPatch, grant.PreviewURL+uri, strings.NewReader("payload"), map[string]string{"Cookie": "pogo=secret", "Authorization": "Bearer secret", "Proxy-Authorization": "proxy-secret", "Forwarded": "host=private", "X-Forwarded-For": "private", "X-Real-IP": "private"})
	got := <-seen
	if resp.StatusCode != 200 || body != "app-ok" || got.uri != "/"+uri || got.method != http.MethodPatch || got.body != "payload" {
		t.Fatalf("status=%d body=%q request=%+v", resp.StatusCode, body, got)
	}
	for _, h := range []string{"Cookie", "Authorization", "Proxy-Authorization", "Forwarded", "X-Forwarded-For", "X-Real-IP"} {
		if got.headers.Get(h) != "" {
			t.Fatalf("app received %s", h)
		}
	}
	for _, h := range []string{"Set-Cookie", "Link", "Content-Location", "Refresh", "Alt-Svc", "Report-To", "NEL", "Access-Control-Allow-Credentials", "Strict-Transport-Security", "Service-Worker-Allowed", "Accept-CH"} {
		if resp.Header.Get(h) != "" {
			t.Fatalf("unsafe header: %s", h)
		}
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "sandbox allow-scripts allow-forms;") || strings.Contains(csp, "allow-same-origin") {
		t.Fatalf("CSP=%s", csp)
	}
	wsURL := strings.Replace(grant.PreviewURL, "http://", "ws://", 1)
	for _, directive := range strings.Split(csp, ";") {
		fields := strings.Fields(directive)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "script-src", "style-src", "img-src", "font-src", "media-src", "connect-src", "form-action":
			for _, v := range fields[1:] {
				if v != "'unsafe-inline'" && v != "'unsafe-eval'" && v != "blob:" && v != "data:" && v != grant.PreviewURL && v != wsURL {
					t.Fatalf("unscoped source: %s", directive)
				}
			}
		}
	}
	resp, body = response(t, f.client, http.MethodGet, f.public+"/api/main", nil, nil)
	if resp.StatusCode != 200 || body != "main-ok" || resp.Header.Get("Set-Cookie") != "pogo=keep" {
		t.Fatal("main policy changed")
	}
}

func TestPathRouterMalformedAndRevokedGrantsNeverFallBack(t *testing.T) {
	var mainCalls, appCalls atomic.Int32
	f := testPathRouter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { appCalls.Add(1) }), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mainCalls.Add(1) }), false)
	rec := f.record()
	grant := applyOpen(t, f, rec)
	u, _ := url.Parse(grant.PreviewURL)
	prefix := u.Path
	for _, candidate := range []string{"/_pogo/web", "/_pogo/web/", "/_pogo/webs", strings.TrimSuffix(prefix, "/"), previewPathPrefix + strings.Repeat("f", 48) + "/", previewPathPrefix + strings.Repeat("z", 48) + "/", prefix + "../api", prefix + ".", prefix + "%2e%2e/api", prefix + "%252e%252e/api", prefix + "%2fapi", prefix + "%5capi", prefix + "%255capi", prefix + "a//b", prefix + "%01", "//_pogo/web/" + strings.TrimPrefix(prefix, previewPathPrefix), "/_pogo/%77eb/" + strings.TrimPrefix(prefix, previewPathPrefix), "/%5fpogo/web/" + strings.TrimPrefix(prefix, previewPathPrefix)} {
		t.Run(candidate, func(t *testing.T) {
			w := httptest.NewRecorder()
			f.router.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, f.public+candidate, nil))
			if w.Code != 404 {
				t.Fatalf("status=%d", w.Code)
			}
		})
	}
	if mainCalls.Load() != 0 || appCalls.Load() != 0 {
		t.Fatal("malformed path reached upstream")
	}
	if err := f.router.CloseGrant(context.Background(), rec, grant.GrantID); err != nil {
		t.Fatal(err)
	}
	resp, _ := response(t, f.client, http.MethodGet, grant.PreviewURL, nil, nil)
	if resp.StatusCode != 404 || mainCalls.Load() != 0 || appCalls.Load() != 0 {
		t.Fatal("revoked grant reached upstream")
	}
}

func TestPathRouterProbe(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(map[bool]string{false: "HTTP", true: "HTTPS"}[secure], func(t *testing.T) {
			var checks atomic.Int32
			f := testPathRouter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.RequestURI() != "/health/%61%20b?x=%2F" {
					http.Error(w, "wrong path", 400)
					return
				}
				checks.Add(1)
			}), nil, secure)
			rec := f.record()
			rec.State = "starting"
			rec.Spec.HealthPath = "/health/%61%20b?x=%2F"
			if err := f.router.Apply(context.Background(), rec); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 8; i++ {
				h, err := f.router.Probe(context.Background(), rec)
				if err != nil || h.Result != "passed" || h.GatewayStatus != 200 || h.UpstreamStatus != 200 {
					t.Fatalf("probe=%+v err=%v", h, err)
				}
			}
			if checks.Load() != 16 {
				t.Fatalf("checks=%d", checks.Load())
			}
			f.router.mu.Lock()
			leaked := len(f.router.grants)
			f.router.mu.Unlock()
			if leaked != 0 {
				t.Fatal("probe grant leaked")
			}
			rec.Spec.HealthPath = "/%252e%252e/private"
			if _, err := f.router.Probe(context.Background(), rec); errorCode(err) != "INVALID_HEALTH_PATH" {
				t.Fatalf("unsafe path: %v", err)
			}
		})
	}
}

func TestPathRouterRedirects(t *testing.T) {
	location := ""
	f := testPathRouter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", location)
		w.WriteHeader(http.StatusFound)
	}), nil, false)
	grant := applyOpen(t, f, f.record())
	u, _ := url.Parse(grant.PreviewURL)
	for _, tc := range []struct {
		location, path string
		allowed        bool
	}{
		{"/next?x=%2F#part", "next?x=%2F#part", true}, {"../next", "next", true}, {"?q=one+two", "folder/page?q=one+two", true}, {f.runtime.URL + "/next/%61%20b", "next/%61%20b", true}, {grant.PreviewURL + "next", "next", true}, {u.Path + "next", "next", true},
		{"https://outside.invalid/", "", false}, {"//outside.invalid/", "", false}, {f.public + "/api/private", "", false}, {previewPathPrefix + strings.Repeat("a", 48) + "/", "", false}, {f.public + previewPathPrefix + strings.Repeat("b", 48) + "/", "", false}, {"/next/%2e%2e/api", "", false}, {"/%252e%252e/api", "", false}, {"/%2fapi", "", false}, {"http:/api/private", "", false},
	} {
		t.Run(tc.location, func(t *testing.T) {
			location = tc.location
			resp, _ := response(t, f.client, http.MethodGet, grant.PreviewURL+"folder/page", nil, nil)
			want := http.StatusBadGateway
			if tc.allowed {
				want = http.StatusFound
			}
			if resp.StatusCode != want {
				t.Fatalf("status=%d want=%d", resp.StatusCode, want)
			}
			if tc.allowed && resp.Header.Get("Location") != u.Path+tc.path {
				t.Fatalf("Location=%q want=%q", resp.Header.Get("Location"), u.Path+tc.path)
			}
		})
	}
}

func TestPathRouterFiltersEarlyHints(t *testing.T) {
	f := testPathRouter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", "</api/private>;rel=preload;as=script")
		w.Header().Set("Set-Cookie", "pogo=attack")
		w.WriteHeader(http.StatusEarlyHints)
		w.WriteHeader(http.StatusOK)
	}), nil, false)
	grant := applyOpen(t, f, f.record())
	var hints atomic.Int32
	req, _ := http.NewRequest(http.MethodGet, grant.PreviewURL, nil)
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{Got1xxResponse: func(code int, h textproto.MIMEHeader) error {
		if code == http.StatusEarlyHints {
			hints.Add(1)
			if h.Get("Link") != "" || h.Get("Set-Cookie") != "" {
				t.Error("unsafe early hint headers")
			}
		}
		return nil
	}}))
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if hints.Load() != 1 || resp.StatusCode != 200 || len(resp.Header.Values("Content-Security-Policy")) != 1 {
		t.Fatalf("hints=%d response=%+v", hints.Load(), resp)
	}
}

func TestPathRouterWebSocketAndGeneration(t *testing.T) {
	f := testPathRouter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/ws?x=%2F" {
			http.Error(w, "wrong websocket path", 400)
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
			if conn.Write(r.Context(), kind, data) != nil {
				return
			}
		}
	}), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "main-ok") }), false)
	rec := f.record()
	grant := applyOpen(t, f, rec)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, strings.Replace(grant.PreviewURL, "http://", "ws://", 1)+"ws?x=%2F", &websocket.DialOptions{HTTPClient: f.client})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := conn.Write(ctx, websocket.MessageText, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	_, data, err := conn.Read(ctx)
	if err != nil || string(data) != "hello" {
		t.Fatalf("echo=%q err=%v", data, err)
	}
	rec.Generation++
	rec.Revision++
	if err := f.router.Apply(ctx, rec); err != nil {
		t.Fatal(err)
	}
	closed, cancelClosed := context.WithTimeout(ctx, time.Second)
	defer cancelClosed()
	if _, _, err := conn.Read(closed); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("generation kept websocket: %v", err)
	}
	resp, _ := response(t, f.client, http.MethodGet, grant.PreviewURL, nil, nil)
	if resp.StatusCode != 404 {
		t.Fatal("old generation accessible")
	}
	rec.OperationID = "new-generation"
	grant, err = f.router.Open(ctx, rec)
	if err != nil {
		t.Fatal(err)
	}
	f.router.suspendControl()
	resp, _ = response(t, f.client, http.MethodGet, grant.PreviewURL, nil, nil)
	if resp.StatusCode != 503 {
		t.Fatal("control loss kept preview")
	}
	resp, body := response(t, f.client, http.MethodGet, f.public, nil, nil)
	if resp.StatusCode != 200 || body != "main-ok" {
		t.Fatal("control loss changed main")
	}
}

func TestPathRouterPreservesMainPathHandling(t *testing.T) {
	f := testPathRouter(t, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.URL.RequestURI())
	}), false)
	for _, uri := range []string{"/assets/a%25b", "/assets/id%2Fpart", "/main/../other", "/main//nested", "/assets/%252e%252e/name"} {
		resp, body := response(t, f.client, http.MethodGet, f.public+uri, nil, nil)
		if resp.StatusCode != http.StatusOK || body != uri {
			t.Fatalf("main handling changed for %q: %d %q", uri, resp.StatusCode, body)
		}
	}
}
