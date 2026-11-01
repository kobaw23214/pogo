package mcpclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// credClient builds the same client httpClient() builds, without standing up
// a whole pool conn: bearer + one custom header scoped to urlStr.
func credClient(t *testing.T, urlStr string) *http.Client {
	t.Helper()
	c := &conn{cfg: &ServerConfig{
		URL:         urlStr,
		BearerToken: "test-token",
		Headers:     map[string]string{"X-Api-Key": "test-key"},
	}}
	hc, err := c.httpClient()
	if err != nil {
		t.Fatal(err)
	}
	return hc
}

func authAndKey(r *http.Request) (string, string) {
	return r.Header.Get("Authorization"), r.Header.Get("X-Api-Key")
}

// Direct (non-redirect) requests keep the old behaviour: bearer and custom
// headers on every request to the configured origin.
func TestHeaderTransportDirectRequest(t *testing.T) {
	var gotAuth, gotKey string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotKey = authAndKey(r)
		w.WriteHeader(200)
	}))
	t.Cleanup(ts.Close)

	resp, err := credClient(t, ts.URL).Get(ts.URL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if gotAuth != "Bearer test-token" || gotKey != "test-key" {
		t.Fatalf("direct request lost credentials: auth=%q key=%q", gotAuth, gotKey)
	}
}

// A cross-origin redirect (here: different port on the same loopback host)
// must not be followed with credentials — the client stops on the 3xx and
// the second server sees nothing sensitive at all.
func TestHeaderTransportCrossOriginRedirect(t *testing.T) {
	var leakAuth, leakKey string
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		leakAuth, leakKey = authAndKey(r)
		w.WriteHeader(200)
	}))
	t.Cleanup(target.Close)

	jump := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/loot", http.StatusFound)
	}))
	t.Cleanup(jump.Close)

	resp, err := credClient(t, jump.URL).Get(jump.URL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want the unfollowed 302", resp.StatusCode)
	}
	if hits.Load() != 0 {
		t.Fatal("cross-origin redirect was followed")
	}
	if leakAuth != "" || leakKey != "" {
		t.Fatalf("credentials leaked cross-origin: auth=%q key=%q", leakAuth, leakKey)
	}
}

// Same-origin redirects keep working: followed (default 10-hop behaviour)
// with the credential still attached.
func TestHeaderTransportSameOriginRedirect(t *testing.T) {
	var gotAuth, gotKey string
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/landing", http.StatusFound)
			return
		}
		gotAuth, gotKey = authAndKey(r)
		w.WriteHeader(200)
	}))
	t.Cleanup(ts.Close)

	resp, err := credClient(t, ts.URL).Get(ts.URL + "/start")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want followed redirect 200", resp.StatusCode)
	}
	if gotAuth != "Bearer test-token" || gotKey != "test-key" {
		t.Fatalf("same-origin redirect lost credentials: auth=%q key=%q", gotAuth, gotKey)
	}
}

// The redirect ceiling stays at Go's default: more than 10 hops errors out.
func TestHeaderTransportRedirectCeiling(t *testing.T) {
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, ts.URL+"/loop", http.StatusFound)
	}))
	t.Cleanup(ts.Close)

	_, err := credClient(t, ts.URL).Get(ts.URL + "/loop")
	if err == nil || !strings.Contains(err.Error(), "stopped after 10 redirects") {
		t.Fatalf("err = %v, want the 10-redirect ceiling", err)
	}
}

// The origin policy itself: cross-host, cross-port, scheme downgrade and the
// default-port equivalence, pinned without needing a TLS fixture.
func TestHeaderTransportOriginPolicy(t *testing.T) {
	mustParse := func(s string) *url.URL {
		u, err := url.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	cases := []struct {
		origin, target string
		want           bool
	}{
		{"https://api.example.com/mcp", "https://api.example.com/other", true},
		{"https://api.example.com/mcp", "https://api.example.com:443/x", true}, // default port equivalence
		{"http://127.0.0.1:8080/mcp", "http://127.0.0.1:8080/sse", true},
		{"https://api.example.com/mcp", "https://evil.example.com/", false}, // cross host
		{"http://127.0.0.1:8080/mcp", "http://127.0.0.1:9090/x", false},     // cross port
		{"https://api.example.com/mcp", "http://api.example.com/x", false},  // downgrade
		{"https://api.example.com/mcp", "https://api.example.com:444/x", false},
		{"http://example.com/mcp", "https://example.com/x", false}, // upgrade is still a scope change
	}
	for _, tc := range cases {
		ht := &headerTransport{origin: mustParse(tc.origin)}
		if got := ht.sameOrigin(mustParse(tc.target)); got != tc.want {
			t.Errorf("sameOrigin(%q -> %q) = %v, want %v", tc.origin, tc.target, got, tc.want)
		}
	}
	// No parsed origin means no stamping scope: everything is out of scope.
	ht := &headerTransport{}
	if ht.sameOrigin(mustParse("http://127.0.0.1/x")) {
		t.Error("nil origin must scope nothing")
	}
}

// A cancelled request must fail as cancelled, not hang or retry.
func TestHeaderTransportCancel(t *testing.T) {
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	t.Cleanup(func() { close(release); ts.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", ts.URL+"/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		resp, err := credClient(t, ts.URL).Do(req)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		done <- err
	}()
	time.Sleep(50 * time.Millisecond) // let the request reach the handler
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled request succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled request did not return")
	}
}
