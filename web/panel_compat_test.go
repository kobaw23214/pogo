package web

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func panelTestMapper(t *testing.T) (panelURLMapper, *url.URL) {
	t.Helper()
	m := newPanelURLMapper(Panel{Name: "系统一", URL: "http://backend.test/app/?entry=1#/dashboard", ProxyMode: "compat"})
	u, _ := url.Parse("http://backend.test/app/page")
	return m, u
}

func TestPanelCompatResourceRewriting(t *testing.T) {
	m, base := panelTestMapper(t)
	prefix := "/panels/" + url.PathEscape("系统一")
	body := `<!doctype html><html><head><base href="/app/"><link rel="stylesheet" href="/assets/main.css"><script type="module" src="/assets/main.js"></script></head><body><a href="next?q=1&amp;x=2#view">next</a><img srcset="/a.png 1x, /b.png 2x"><img src="https://cdn.test/logo.png"><style>@import '/theme.css'; .x{background:url(../image.png)}</style><script type="module">import { x } from '/lib.js'; import('/lazy.js'); const s = '/unchanged';</script><script type="importmap">{"imports":{"lib":"/lib.js"},"scopes":{"/app/":{"other":"./other.js"}}}</script></body></html>`
	out, err := rewritePanelHTML([]byte(body), m, base, "test-nonce")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{`href="` + prefix + `/app/"`, `href="` + prefix + `/assets/main.css"`, `src="` + prefix + `/assets/main.js"`, prefix + `/app/next?q=1&amp;x=2#view`, prefix + `/a.png 1x`, prefix + `/b.png 2x`, `https://cdn.test/logo.png`, prefix + `/theme.css`, prefix + `/image.png`, prefix + `/lib.js`, prefix + `/lazy.js`, `'/unchanged'`, prefix + `/app/other.js`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Index(s, "const config =") > strings.Index(s, `src="`+prefix+`/assets/main.js"`) {
		t.Fatal("bootstrap must run before upstream scripts")
	}
	if strings.Count(s, "const config =") != 1 {
		t.Fatal("bootstrap injected more than once")
	}
	if _, err := rewritePanelHTML([]byte(`<script src="/a.js" integrity="sha256-x"></script>`), m, base, "n"); err == nil {
		t.Fatal("must not silently invalidate SRI")
	}
}

func TestPanelCompatCSSAndModules(t *testing.T) {
	m := newPanelURLMapper(Panel{Name: "app", URL: "http://backend.test"})
	base, _ := url.Parse("http://backend.test/assets/main.css")
	css, err := rewritePanelCSS([]byte(`/* url(/leave) */ @import "/theme.css"; a{background:URL(\2f img.png);content:"/leave";mask:url(data:image/svg+xml,svg);font: url("https://cdn.test/font.woff")}`), m, base)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"/panels/app/theme.css"`, `url("/panels/app/img.png")`, `/* url(/leave) */`, `content:"/leave"`, `url(data:image/svg+xml,svg)`, `https://cdn.test/font.woff`} {
		if !strings.Contains(string(css), want) {
			t.Errorf("missing %s in %s", want, css)
		}
	}
	js, err := rewritePanelJS([]byte(`import '/a.js'; export {x} from './b.js'; import('/c.js'); import(name); import('bare'); const unrelated = '/a.js';`), m, base)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"/panels/app/a.js"`, `"/panels/app/assets/b.js"`, `"/panels/app/c.js"`, `import(name)`, `import('bare')`, `'/a.js'`} {
		if !strings.Contains(string(js), want) {
			t.Errorf("missing %s in %s", want, js)
		}
	}
	if _, err := rewritePanelJS([]byte(`not valid !!!`), m, base); err == nil {
		t.Fatal("unparseable JS must not be mutated")
	}
}

func TestPanelCompatProxySemantics(t *testing.T) {
	var received *http.Request
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Clone(r.Context())
		w.Header().Set("Content-Type", "application/json")
		w.Header().Add("Set-Cookie", "app_session=abc; Path=/; HttpOnly; SameSite=Lax")
		w.Header().Add("Set-Cookie", "deep=1")
		w.Header().Set("Location", "/login?next=1#signin")
		io.WriteString(w, `{"ok":true}`)
	}))
	defer backend.Close()
	p := Panel{Name: "中文", URL: backend.URL + "/app/?entry=1#/home", ProxyMode: "compat"}
	front := httptest.NewServer(newPanelProxy(p))
	defer front.Close()
	prefix := "/panels/" + url.PathEscape(p.Name)
	req, _ := http.NewRequest("POST", front.URL+prefix+"/api/%E4%B8%AD%E6%96%87?q=2", strings.NewReader("payload"))
	req.Header.Set("Origin", front.URL)
	req.Header.Set("Referer", front.URL+prefix+"/app/?nav=1")
	req.Header.Set("Cookie", sessionCookie+"=private; app_session=user")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if received.URL.EscapedPath() != "/api/%E4%B8%AD%E6%96%87" || received.URL.RawQuery != "q=2" {
		t.Fatalf("wrong target: %s", received.URL)
	}
	if received.Header.Get("Origin") != backend.URL || received.Header.Get("Referer") != backend.URL+"/app/?nav=1" {
		t.Fatal("origin/referrer not translated")
	}
	if received.Header.Get("Cookie") != "app_session=user" {
		t.Fatal("Pogo cookie leaked")
	}
	if res.Header.Get("Location") != prefix+"/login?next=1#signin" {
		t.Fatal(res.Header.Get("Location"))
	}
	if res.Cookies()[0].Path != prefix+"/" || !res.Cookies()[0].HttpOnly {
		t.Fatal(res.Cookies())
	}
	if res.Cookies()[1].Path != prefix+"/api" {
		t.Fatal(res.Cookies())
	}
	if panelEntryPath(p) != prefix+"/app/?entry=1#/home" {
		t.Fatal(panelEntryPath(p))
	}
}

func TestPanelCompatResponseGuards(t *testing.T) {
	cases := []struct {
		name, typ, encoding, cookie, dest, cache, body string
		status, want                                   int
		code                                           string
	}{
		{name: "html-as-module", typ: "text/html", dest: "script", body: "<html></html>", want: 502, code: "PANEL_RESOURCE_TYPE_MISMATCH"},
		{name: "html-as-css", typ: "text/html", dest: "style", body: "<html></html>", want: 502, code: "PANEL_RESOURCE_TYPE_MISMATCH"},
		{name: "gzip", typ: "text/css", encoding: "gzip", body: "a{background:url(/a.png)}", want: 200},
		{name: "oversized", typ: "text/css", body: strings.Repeat(" ", panelRewriteLimit+1), want: 502, code: "PANEL_COMPAT_UNSUPPORTED"},
		{name: "gzip-oversized", typ: "text/css", encoding: "gzip", body: strings.Repeat(" ", panelRewriteLimit+1), want: 502, code: "PANEL_COMPAT_UNSUPPORTED"},
		{name: "brotli", typ: "text/css", encoding: "br", body: "compressed", want: 502, code: "PANEL_COMPAT_UNSUPPORTED"},
		{name: "host-cookie", typ: "application/json", cookie: "__Host-auth=secret; Secure; Path=/", body: "{}", want: 502, code: "PANEL_COMPAT_UNSUPPORTED"},
		{name: "pogo-cookie", typ: "application/json", cookie: sessionCookie + "=secret; Path=/", body: "{}", want: 502, code: "PANEL_COMPAT_UNSUPPORTED"},
		{name: "charset", typ: "text/html; charset=gbk", body: "html", want: 502, code: "PANEL_COMPAT_UNSUPPORTED"},
		{name: "no-transform", typ: "text/css", cache: "no-transform", body: "css", want: 502, code: "PANEL_COMPAT_UNSUPPORTED"},
		{name: "partial-css", typ: "text/css", status: 206, body: "css", want: 502, code: "PANEL_COMPAT_UNSUPPORTED"},
		{name: "binary", typ: "application/octet-stream", status: 206, body: "binary", want: 206},
		{name: "sse", typ: "text/event-stream", body: "data: hello\n\n", want: 200},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tt.typ)
				w.Header().Set("Content-Encoding", tt.encoding)
				w.Header().Set("Cache-Control", tt.cache)
				if tt.cookie != "" {
					w.Header().Add("Set-Cookie", tt.cookie)
				}
				if tt.status != 0 {
					w.WriteHeader(tt.status)
				}
				if tt.encoding == "gzip" {
					gz := gzip.NewWriter(w)
					io.WriteString(gz, tt.body)
					gz.Close()
				} else {
					io.WriteString(w, tt.body)
				}
			}))
			defer backend.Close()
			front := httptest.NewServer(newPanelProxy(Panel{Name: "app", URL: backend.URL, ProxyMode: "compat"}))
			defer front.Close()
			req, _ := http.NewRequest("GET", front.URL+"/panels/app/file", nil)
			req.Header.Set("Sec-Fetch-Dest", tt.dest)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			body, _ := io.ReadAll(res.Body)
			if res.StatusCode != tt.want || res.Header.Get("X-Pogo-Panel-Error") != tt.code {
				t.Fatalf("got %d %s %s", res.StatusCode, res.Header.Get("X-Pogo-Panel-Error"), body)
			}
			if tt.name == "gzip" && (!bytes.Contains(body, []byte("/panels/app/a.png")) || res.Header.Get("Content-Encoding") != "") {
				t.Fatal("gzip rewrite failed")
			}
			if tt.name == "binary" || tt.name == "sse" {
				if string(body) != tt.body {
					t.Fatal("non-text modified")
				}
			}
		})
	}
}

func TestPanelCompatModePersistenceAndRollback(t *testing.T) {
	store := filepath.Join(t.TempDir(), "panels.json")
	front, backend := panelHarness(t, nil, func(o *ServerOptions) { o.PanelStore = store })
	res, body := postPanel(t, front, backend, `{"name":"app","url":"BACKEND/app/?entry=1#/home"}`)
	if res.StatusCode != 200 {
		t.Fatal(body)
	}
	for _, mode := range []string{"compat", "passthrough"} {
		res, body = postPanel(t, front, backend, fmt.Sprintf(`{"name":"app","url":"BACKEND/app/?entry=1#/home","proxy_mode":%q}`, mode))
		if res.StatusCode != 200 || !strings.Contains(body, `"proxy_mode":"`+mode+`"`) {
			t.Fatal(body)
		}
		var saved struct{ Panels []Panel }
		data, _ := os.ReadFile(store)
		if json.Unmarshal(data, &saved) != nil || len(saved.Panels) != 1 || saved.Panels[0].ProxyMode != mode {
			t.Fatal(string(data))
		}
		check, err := http.Get(front.URL + "/panels/app/assets/a.js?q=2")
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(check.Body)
		check.Body.Close()
		want := "backend:/assets/a.js?q=2"
		if mode == "passthrough" {
			want = "backend:/app/assets/a.js?entry=1&q=2"
		}
		if string(got) != want {
			t.Fatalf("%s got %s want %s", mode, got, want)
		}
	}
	res, _ = postPanel(t, front, backend, `{"url":"BACKEND","proxy_mode":"unknown"}`)
	if res.StatusCode != 400 {
		t.Fatal("invalid mode accepted")
	}
}

func TestPanelCompatCSP(t *testing.T) {
	for _, policy := range []string{"script-src 'self' 'unsafe-inline'", "default-src 'self' 'unsafe-inline'"} {
		got := panelCSPNonce(policy, "x")
		if strings.Contains(got, "'nonce-") {
			t.Fatal("nonce unexpectedly disables upstream unsafe-inline", got)
		}
	}
	got := panelCSPNonce("default-src 'self'; script-src 'self' 'nonce-original'; object-src 'none'", "new")
	for _, part := range []string{"'nonce-original'", "'nonce-new'", "object-src 'none'"} {
		if !strings.Contains(got, part) {
			t.Fatal(got)
		}
	}
}

func TestPanelCompatMappedOrigin(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "App.Example:80" {
			t.Errorf("lost virtual host: %s", r.Host)
		}
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<script src="http://app.example/assets/app.js"></script>`)
	}))
	defer backend.Close()
	p := Panel{Name: "中文", URL: backend.URL, SourceURL: "http://App.Example:80/app/", ProxyMode: "compat"}
	front := httptest.NewServer(newPanelProxy(p))
	defer front.Close()
	res, err := http.Get(front.URL + "/panels/%e4%b8%ad%e6%96%87/app/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if !bytes.Contains(body, []byte(`/panels/%E4%B8%AD%E6%96%87/assets/app.js`)) {
		t.Fatal("source host normalization failed")
	}
}

func TestPanelCompatReusePreservesExistingMode(t *testing.T) {
	front, backend := panelHarness(t, []Panel{{Name: "flag", URL: "BACKEND"}})
	res, body := postPanel(t, front, backend, `{"url":"BACKEND","proxy_mode":"compat","preserve_existing":true}`)
	if res.StatusCode != 200 || !strings.Contains(body, `"proxy_mode":"passthrough"`) {
		t.Fatal(body)
	}
	res, _ = postPanel(t, front, backend, `{"url":"BACKEND","proxy_mode":"compat"}`)
	if res.StatusCode != 400 {
		t.Fatal("startup policy should not change through runtime API")
	}
}
