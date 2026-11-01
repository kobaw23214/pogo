package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"nhooyr.io/websocket"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func liveTicketRequest(t *testing.T, h *muHarness, token, sid, origin, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", h.srv.URL+"/api/sessions/"+sid+"/browser/live-ticket", strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	res, err := h.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var data struct {
		Ticket  string `json:"ticket"`
		Expires int    `json:"expires_in"`
	}
	_ = json.NewDecoder(res.Body).Decode(&data)
	if data.Ticket != "" && (data.Expires != 30 || res.Header.Get("Cache-Control") != "no-store") {
		t.Fatal("ticket expiry/cache contract")
	}
	return res.StatusCode, data.Ticket
}
func liveTicket(t *testing.T, h *muHarness, sid string) string {
	t.Helper()
	status, ticket := liveTicketRequest(t, h, aliceToken, sid, h.srv.URL, `{"context_ref":"`+nativeSheetsContext+`","tab_id":"t1"}`)
	if status != 200 || len(ticket) != 64 {
		t.Fatalf("ticket issuance=%d", status)
	}
	return ticket
}
func liveURL(h *muHarness, sid, ticket string) string {
	return h.srv.URL + "/api/sessions/" + sid + "/browser/live?context_ref=" + nativeSheetsContext + "&tab_id=t1&ticket=" + ticket
}
func liveUpgrade(t *testing.T, h *muHarness, endpoint, origin string) (int, http.Header, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", endpoint, nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("Authorization", "Bearer "+aliceToken)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	res, err := h.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, string(data)
}
func liveState(w http.ResponseWriter) {
	writeJSON(w, 200, map[string]any{"context_ref": nativeSheetsContext, "viewed_tab": "t1"})
}

func TestNativeBrowserLiveTicketProxy(t *testing.T) {
	var connections atomic.Int32
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/state") {
			liveState(w)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/live") {
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		connections.Add(1)
		if len(r.URL.Query()) != 2 || r.URL.Query().Get("tab_id") != "t1" || r.URL.Query().Get("context_ref") != nativeSheetsContext || r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" || r.Header.Get("X-User-Secret") != "" {
			t.Error("client secret or unbound query reached worker")
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		kind, data, err := c.Read(ctx)
		if err == nil {
			_ = c.Write(ctx, kind, data)
		}
	})
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	h.mgr.cfg.browser = cfg
	sid := h.createAs(aliceToken)
	h.session(sid).nativeBrowser.created = true
	ticket := liveTicket(t, h, sid)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, liveURL(h, sid, ticket), &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {h.srv.URL}, "Cookie": {"user=secret"}, "X-User-Secret": {"secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	payload := `{"type":"input","text":"中文拖动"}`
	if err = c.Write(ctx, websocket.MessageText, []byte(payload)); err != nil {
		t.Fatal(err)
	}
	_, data, err := c.Read(ctx)
	if err != nil || string(data) != payload {
		t.Fatalf("opaque bidirectional message=%q: %v", data, err)
	}
	if status, _, _ := liveUpgrade(t, h, liveURL(h, sid, ticket), h.srv.URL); status != 401 {
		t.Fatal("ticket replay accepted")
	}
	if status, _, _ := liveUpgrade(t, h, strings.Replace(liveURL(h, sid, ticket), "ticket="+ticket, "token="+aliceToken, 1), h.srv.URL); status != 401 {
		t.Fatal("durable token fallback accepted")
	}
	if connections.Load() != 1 {
		t.Fatal("rejected requests reached worker")
	}
}
func TestNativeBrowserLiveTicketBindingAndLifetime(t *testing.T) {
	var state, other atomic.Int32
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/state") {
			state.Add(1)
			liveState(w)
			return
		}
		other.Add(1)
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	h.mgr.cfg.browser = cfg
	sid := h.createAs(aliceToken)
	bob := h.createAs(bobToken)
	body := `{"context_ref":"` + nativeSheetsContext + `","tab_id":"t1"}`
	for _, tc := range []struct {
		token, origin, body string
		want                int
	}{{"", h.srv.URL, body, 401}, {bobToken, h.srv.URL, body, 404}, {aliceToken, "https://evil.test", body, 403}, {aliceToken, "", body, 403}, {aliceToken, h.srv.URL, `{"context_ref":"wrong","tab_id":"t1"}`, 400}, {aliceToken, h.srv.URL, body, 409}} {
		if status, _ := liveTicketRequest(t, h, tc.token, sid, tc.origin, tc.body); status != tc.want {
			t.Fatalf("issuance=%d want=%d", status, tc.want)
		}
	}
	if state.Load() != 0 || other.Load() != 0 {
		t.Fatal("rejected issuance touched/created worker")
	}
	h.session(sid).nativeBrowser.created = true
	for _, tc := range []struct {
		name   string
		mutate func(string) string
		origin string
	}{{"session", func(v string) string { return strings.Replace(v, sid, bob, 1) }, h.srv.URL}, {"target", func(v string) string { return strings.Replace(v, "tab_id=t1", "tab_id=t2", 1) }, h.srv.URL}, {"origin", func(v string) string { return v }, ""}, {"extra credential", func(v string) string { return v + "&token=" + aliceToken }, h.srv.URL}} {
		t.Run(tc.name, func(t *testing.T) {
			ticket := liveTicket(t, h, sid)
			if status, _, _ := liveUpgrade(t, h, tc.mutate(liveURL(h, sid, ticket)), tc.origin); status != 401 {
				t.Fatalf("upgrade=%d", status)
			}
		})
	}
	ticket := liveTicket(t, h, sid)
	h.psrv.browserLiveMu.Lock()
	entry := h.psrv.browserLiveTickets[ticket]
	entry.expires = time.Now().Add(-time.Second)
	h.psrv.browserLiveTickets[ticket] = entry
	h.psrv.browserLiveMu.Unlock()
	if status, _, _ := liveUpgrade(t, h, liveURL(h, sid, ticket), h.srv.URL); status != 401 {
		t.Fatal("expired ticket accepted")
	}
	ticket = liveTicket(t, h, sid)
	if status, _, _ := liveUpgrade(t, h, liveURL(h, sid, ticket), "https://evil.test"); status != 403 {
		t.Fatal("cross-origin accepted")
	}
	ticket = liveTicket(t, h, sid)
	h.psrv.auth.mu.Lock()
	delete(h.psrv.auth.users, "alice")
	h.psrv.auth.mu.Unlock()
	if status, _, _ := liveUpgrade(t, h, liveURL(h, sid, ticket), h.srv.URL); status != 401 {
		t.Fatal("deleted owner accepted")
	}
	if other.Load() != 0 {
		t.Fatal("invalid upgrade reached worker")
	}
}
func TestNativeBrowserLiveTicketLimitAndRedaction(t *testing.T) {
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/state") {
			liveState(w)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/live") {
			w.Header().Set("Location", "http://private/worker-id")
			w.Header().Set("Set-Cookie", "token=worker-secret")
			w.WriteHeader(409)
			fmt.Fprint(w, "worker-id worker-secret")
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	h.mgr.cfg.browser = cfg
	sid := h.createAs(aliceToken)
	h.session(sid).nativeBrowser.created = true
	ticket := liveTicket(t, h, sid)
	status, headers, body := liveUpgrade(t, h, liveURL(h, sid, ticket), h.srv.URL)
	if status != 409 || strings.Contains(body, "worker-") || headers.Get("Location") != "" || headers.Get("Set-Cookie") != "" {
		t.Fatal("worker failure leaked private service details")
	}
	h.psrv.browserLiveMu.Lock()
	for i := 0; i < nativeBrowserLiveTicketLimit; i++ {
		h.psrv.browserLiveTickets[fmt.Sprint(i)] = nativeBrowserLiveTicket{expires: time.Now().Add(time.Hour)}
	}
	h.psrv.browserLiveMu.Unlock()
	if status, _ := liveTicketRequest(t, h, aliceToken, sid, h.srv.URL, `{"context_ref":"`+nativeSheetsContext+`","tab_id":"t1"}`); status != 429 {
		t.Fatalf("ticket cap=%d", status)
	}
}
func TestNativeBrowserLiveRejectsMissingIdentityInAuthenticatedDeployment(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	h.psrv.opts.Token = ""
	if status, _, _ := liveUpgrade(t, h, liveURL(h, "unknown", strings.Repeat("a", 64)), h.srv.URL); status != 401 {
		t.Fatal("unauthenticated websocket accepted")
	}
	if status, _ := liveTicketRequest(t, h, aliceToken, "unknown", h.srv.URL, `{}`); status != 401 {
		t.Fatal("unauthenticated deployment issued ticket")
	}
}

func TestNativeBrowserLiveIntentionalTokenlessSingleUser(t *testing.T) {
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/state") {
			liveState(w)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/live") {
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.CloseNow()
		kind, data, err := c.Read(r.Context())
		if err == nil {
			_ = c.Write(r.Context(), kind, data)
		}
	})
	h := newHarness(t, scriptedTurns(textTurn("unused")))
	h.mgr.cfg.browser = cfg
	sid := h.createSession()
	h.session(sid).nativeBrowser.created = true
	server, err := NewServer(h.mgr, ServerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	front := httptest.NewServer(server.Handler())
	t.Cleanup(front.Close)
	plain := &muHarness{t: t, mgr: h.mgr, psrv: server, srv: front}
	body := `{"context_ref":"` + nativeSheetsContext + `","tab_id":"t1"}`
	for _, origin := range []string{"", "https://other.test"} {
		if status, _ := liveTicketRequest(t, plain, "", sid, origin, body); status != 403 {
			t.Fatal("tokenless ticket bypassed same-origin check")
		}
	}
	status, ticket := liveTicketRequest(t, plain, "", sid, front.URL, body)
	if status != 200 || len(ticket) != 64 {
		t.Fatalf("intentional tokenless issuance=%d", status)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, liveURL(plain, sid, ticket), &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {front.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if err := c.Write(ctx, websocket.MessageText, []byte("tokenless single-user")); err != nil {
		t.Fatal(err)
	}
	_, data, err := c.Read(ctx)
	if err != nil || string(data) != "tokenless single-user" {
		t.Fatal("tokenless single-user live exchange failed")
	}
	if status, _, _ := liveUpgrade(t, plain, liveURL(plain, sid, ticket), front.URL); status != 401 {
		t.Fatal("tokenless single-user ticket replay accepted")
	}
	// Configuring a credential immediately removes the exception, even without
	// an account store. Other account-enabled coverage exercises the other gate.
	server.opts.Token = "configured-required-token"
	if status, _ := liveTicketRequest(t, plain, "", sid, front.URL, body); status != 401 {
		t.Fatal("configured authentication bypassed")
	}
}

func TestNativeBrowserLiveMaintenanceAndLifecycleClose(t *testing.T) {
	if nativeBrowserLiveWatchInterval > 250*time.Millisecond {
		t.Fatal("maintenance poll exceeds its bound")
	}
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/state") {
			liveState(w)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/live") {
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			kind, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if c.Write(r.Context(), kind, data) != nil {
				return
			}
		}
	})
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	h.mgr.cfg.browser = cfg
	sid := h.createAs(aliceToken)
	h.session(sid).nativeBrowser.created = true
	connect := func(marker string) *websocket.Conn {
		t.Helper()
		req, _ := http.NewRequest("POST", h.srv.URL+"/api/sessions/"+sid+"/browser/live-ticket", strings.NewReader(`{"context_ref":"`+nativeSheetsContext+`","tab_id":"t1"}`))
		req.Header.Set("Authorization", "Bearer "+aliceToken)
		req.Header.Set("Origin", h.srv.URL)
		if marker != "" {
			req.Header.Set("X-Pogo-Release", marker)
		}
		res, err := h.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var issued struct {
			Ticket string `json:"ticket"`
		}
		_ = json.NewDecoder(res.Body).Decode(&issued)
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("maintenance ticket %d", res.StatusCode)
		}
		headers := http.Header{"Origin": {h.srv.URL}}
		if marker != "" {
			headers.Set("X-Pogo-Release", marker)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		c, _, err := websocket.Dial(ctx, liveURL(h, sid, issued.Ticket), &websocket.DialOptions{HTTPHeader: headers})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.CloseNow() })
		return c
	}
	marker := func(value string) {
		t.Helper()
		if value == "" {
			if err := os.Remove(h.mgr.releaseMaintenancePath()); err != nil {
				t.Fatal(err)
			}
			return
		}
		raw, _ := json.Marshal(map[string]string{"token": value})
		if err := os.WriteFile(h.mgr.releaseMaintenancePath(), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	disconnected := func(c *websocket.Conn) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _, err := c.Read(ctx)
		if err == nil || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatalf("connection was not closed by maintenance/lifecycle: %v", err)
		}
	}
	c := connect("")
	marker(strings.Repeat("a", 64))
	disconnected(c)
	deadline := time.Now().Add(time.Second)
	for {
		h.psrv.browserLiveMu.Lock()
		count := len(h.psrv.browserLiveConnections)
		h.psrv.browserLiveMu.Unlock()
		if count == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("closed ordinary socket remained registered")
		}
		time.Sleep(time.Millisecond)
	}
	// A correctly authenticated maintenance acceptance stream survives polls.
	c = connect(strings.Repeat("a", 64))
	time.Sleep(3 * nativeBrowserLiveWatchInterval)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	if err := c.Write(ctx, websocket.MessageText, []byte("same marker")); err != nil {
		t.Fatal(err)
	}
	_, payload, err := c.Read(ctx)
	cancel()
	if err != nil || string(payload) != "same marker" {
		t.Fatal("valid maintenance acceptance connection was cut")
	}
	marker(strings.Repeat("b", 64))
	disconnected(c)
	c = connect(strings.Repeat("b", 64))
	marker("")
	disconnected(c)
	c = connect("")
	h.session(sid).nativeBrowser.Close()
	disconnected(c)
	h.session(sid).nativeBrowser.mu.Lock()
	h.session(sid).nativeBrowser.closed = false
	h.session(sid).nativeBrowser.mu.Unlock()
	c = connect("")
	h.psrv.Close()
	disconnected(c)
}
