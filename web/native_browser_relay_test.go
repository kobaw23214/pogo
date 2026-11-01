package web

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/internal/browserrelay"
	"nhooyr.io/websocket"
)

func TestBrowserRelayURLAndRange(t *testing.T) {
	for origin, want := range map[string]string{
		"http://localhost:19777": "turn:localhost:19777?transport=tcp",
		"https://pogo.example":   "turns:pogo.example:443?transport=tcp",
		"https://[::1]:19777":    "turns:[::1]:19777?transport=tcp",
		"http://pogo.example":    "turn:pogo.example:80?transport=tcp",
	} {
		got, err := browserRelayURL(origin)
		if err != nil || got != want {
			t.Fatalf("origin %s: %q %v", origin, got, err)
		}
	}
	for _, origin := range []string{"file:///tmp/a", "http://user:pass@localhost:19777", "http://", "ws://localhost"} {
		if _, err := browserRelayURL(origin); err == nil {
			t.Fatal("invalid media origin accepted")
		}
	}
	for _, ports := range []string{"1-32", "19780-19812", "2", "19811-19780", "65535-65536"} {
		if _, _, err := browserRelayPortRange(ports); err == nil {
			t.Fatal("unbounded media peer range accepted")
		}
	}
	if lo, hi, err := browserRelayPortRange(""); err != nil || lo != 19780 || hi != 19811 {
		t.Fatal("internal range default")
	}
}

func TestBrowserRelayRefreshIPv4Literal(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := netip.MustParseAddr("192.0.2.20")
	relay, err := browserrelay.New(listener, browserrelay.Config{RelayAddr: netip.MustParseAddr("127.0.0.1"), BindAddr: netip.MustParseAddr("127.0.0.1"), AllowedPeerIPs: []netip.Addr{address}})
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	defer relay.Close()
	server := &Server{browserMediaRelay: relay, browserRelayWorkerHost: "192.0.2.20", browserRelayPortMin: 19780, browserRelayPortMax: 19781}
	peers, err := server.refreshBrowserRelayPeers(context.Background(), relay)
	if err != nil || len(peers) != 2 || peers[0] != netip.AddrPortFrom(address, 19780) || peers[1] != netip.AddrPortFrom(address, 19781) {
		t.Fatalf("IPv4 literal must remain a valid, exact private relay peer: %v %v", peers, err)
	}
}

func TestBrowserRelayTicketRequiresActiveOwnedView(t *testing.T) {
	worker := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/state") {
			writeJSON(w, 200, map[string]any{"context_ref": nativeSheetsContext, "viewed_tab": "t1", "generation": 1})
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
			kind, body, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if c.Write(r.Context(), kind, body) != nil {
				return
			}
		}
	})
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	h.mgr.cfg.browser = worker
	sid := h.createAs(aliceToken)
	h.session(sid).nativeBrowser.created = true
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	peer := netip.MustParseAddrPort("192.0.2.20:19780")
	relay, err := browserrelay.New(listener, browserrelay.Config{RelayAddr: netip.MustParseAddr("127.0.0.1"), BindAddr: netip.MustParseAddr("127.0.0.1"), AllowedPeerIPs: []netip.Addr{peer.Addr()}})
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { relay.Close() })
	h.psrv.browserMediaRelay, h.psrv.browserRelayPeers = relay, []netip.AddrPort{peer}
	ticket := liveTicket(t, h, sid)
	body := map[string]any{"context_ref": nativeSheetsContext, "tab_id": "t1", "generation": 1, "media_epoch": "m1", "live_ticket": ticket}
	request := func(token, owner, origin string, value map[string]any) (int, map[string]json.RawMessage) {
		raw, _ := json.Marshal(value)
		r, _ := http.NewRequest("POST", h.srv.URL+"/api/sessions/"+owner+"/browser/media-relay-ticket", strings.NewReader(string(raw)))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Origin", origin)
		response, err := h.srv.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode == http.StatusOK && response.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("media credential response was cacheable")
		}
		var out map[string]json.RawMessage
		_ = json.NewDecoder(response.Body).Decode(&out)
		return response.StatusCode, out
	}
	if code, _ := request(aliceToken, sid, h.srv.URL, body); code != 403 {
		t.Fatal("credential issued before an active WebSocket view")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, liveURL(h, sid, ticket), &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {h.srv.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"type":"ping"}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Read(ctx); err != nil {
		t.Fatal(err)
	}
	code, out := request(aliceToken, sid, h.srv.URL, body)
	if code != 200 || string(out["ice_transport_policy"]) != `"relay"` {
		t.Fatalf("valid page view was rejected: %d", code)
	}
	var servers []struct{ URLs, Username, Credential string }
	_ = json.Unmarshal(out["ice_servers"], &servers)
	want, _ := browserRelayURL(h.srv.URL)
	if len(servers) != 1 || servers[0].URLs != want || servers[0].Username == "" || servers[0].Credential == "" {
		t.Fatal("same-port relay credential contract")
	}
	for _, change := range []map[string]any{{"generation": 2}, {"tab_id": "t2"}, {"context_ref": "ctx_111111111111111111111111"}, {"live_ticket": strings.Repeat("a", 64)}, {"peer": "127.0.0.1:22"}} {
		bad := map[string]any{}
		for k, v := range body {
			bad[k] = v
		}
		for k, v := range change {
			bad[k] = v
		}
		if code, _ := request(aliceToken, sid, h.srv.URL, bad); code == 200 {
			t.Fatal("unbound relay credential issued")
		}
	}
	if code, _ := request(bobToken, sid, h.srv.URL, body); code == 200 {
		t.Fatal("another account obtained relay credentials")
	}
	if code, _ := request(aliceToken, sid, "https://evil.example", body); code == 200 {
		t.Fatal("cross-origin relay credential issued")
	}
	_ = c.CloseNow()
	deadline := time.Now().Add(time.Second)
	for {
		code, _ := request(aliceToken, sid, h.srv.URL, body)
		if code == 403 || code == 410 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("closed view retained relay issuance")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
