package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/yosukeno/pogo/internal/browserrelay"
)

var browserRelayEpoch = regexp.MustCompile(`^[A-Za-z0-9_-]{1,160}$`)

func nativeBrowserRelayViewScope(ticket string) string {
	if len(ticket) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(ticket); err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(ticket))
	return hex.EncodeToString(sum[:])
}

// The HTTP server owns the only public TCP listener. TURN allocations bind
// private UDP sockets solely to communicate with the configured W2 worker.
func (s *Server) startBrowserMediaRelay(listener net.Listener) (net.Listener, error) {
	if mode := os.Getenv("POGO_BROWSER_MEDIA_RELAY"); mode != "" && mode != "web-port" {
		return nil, errors.New("POGO_BROWSER_MEDIA_RELAY must be web-port")
	}
	if s.mgr.cfg.browser == nil || s.mgr.cfg.browserDesktopOwner != nil {
		return listener, nil
	}
	worker, err := url.Parse(s.mgr.cfg.browser.url)
	if err != nil {
		return nil, errors.New("browser relay worker configuration is invalid")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", worker.Hostname())
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("browser relay worker address is unavailable")
	}
	allowed := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if address.Is4() && !address.IsUnspecified() && !address.IsMulticast() {
			allowed = append(allowed, address)
		}
	}
	if len(allowed) == 0 {
		return nil, errors.New("browser relay requires a configured IPv4 worker")
	}
	// A UDP connect selects the Docker route and source address without sending
	// packets, consulting external IP services or publishing a UDP listener.
	route, err := net.DialUDP("udp4", nil, net.UDPAddrFromAddrPort(netip.AddrPortFrom(allowed[0], 19780)))
	if err != nil {
		return nil, errors.New("browser relay private worker route is unavailable")
	}
	local := route.LocalAddr().(*net.UDPAddr).AddrPort().Addr().Unmap()
	_ = route.Close()
	minimum, maximum, err := browserRelayPortRange(os.Getenv("POGO_BROWSER_RELAY_UDP_PORT_RANGE"))
	if err != nil {
		return nil, err
	}
	relay, err := browserrelay.New(listener, browserrelay.Config{
		Realm: "pogo-browser", RelayAddr: local, BindAddr: local,
		AllowedPeerIPs: allowed, PeerPortMin: minimum, PeerPortMax: maximum,
		CredentialTTL: time.Minute, ScopeTTL: 6 * time.Hour,
	})
	if err != nil {
		return nil, fmt.Errorf("browser media relay startup: %w", err)
	}
	s.browserLiveMu.Lock()
	s.browserMediaRelay = relay
	s.browserRelayWorkerHost = worker.Hostname()
	s.browserRelayPortMin, s.browserRelayPortMax = minimum, maximum
	for _, address := range allowed {
		for port := uint32(minimum); port <= uint32(maximum); port++ {
			s.browserRelayPeers = append(s.browserRelayPeers, netip.AddrPortFrom(address, uint16(port)))
		}
	}
	s.browserLiveMu.Unlock()
	return relay.HTTPListener(), nil
}

func browserRelayPortRange(value string) (uint16, uint16, error) {
	if value == "" {
		value = "19780-19811"
	}
	parts := strings.Split(value, "-")
	if len(parts) != 2 {
		return 0, 0, errors.New("browser relay requires a bounded internal UDP port range")
	}
	lo, e1 := strconv.Atoi(parts[0])
	hi, e2 := strconv.Atoi(parts[1])
	if e1 != nil || e2 != nil || lo < 1024 || hi > 65535 || hi < lo || hi-lo > 31 {
		return 0, 0, errors.New("browser relay requires a bounded internal UDP port range")
	}
	return uint16(lo), uint16(hi), nil
}

func (s *Server) closeBrowserMediaRelay() {
	s.browserLiveMu.Lock()
	relay := s.browserMediaRelay
	s.browserMediaRelay = nil
	s.browserRelayPeers = nil
	s.browserRelayWorkerHost = ""
	s.browserLiveMu.Unlock()
	if relay != nil {
		_ = relay.Close()
	}
}

func (s *Server) revokeBrowserRelayScope(scope string) {
	if scope == "" {
		return
	}
	s.browserLiveMu.Lock()
	relay := s.browserMediaRelay
	s.browserLiveMu.Unlock()
	if relay != nil {
		relay.RevokeScope(scope)
	}
}

// Refresh exact DNS addresses on a media epoch, not on each input event. A
// recreated worker must not inherit the old container's address permissions.
func (s *Server) refreshBrowserRelayPeers(ctx context.Context, relay *browserrelay.Service) ([]netip.AddrPort, error) {
	s.browserRelayResolveMu.Lock()
	defer s.browserRelayResolveMu.Unlock()
	s.browserLiveMu.Lock()
	host, lo, hi := s.browserRelayWorkerHost, s.browserRelayPortMin, s.browserRelayPortMax
	peers := append([]netip.AddrPort(nil), s.browserRelayPeers...)
	current := s.browserMediaRelay == relay && !s.browserLiveClosed
	s.browserLiveMu.Unlock()
	if !current {
		return nil, browserrelay.ErrClosed
	}
	// A directly injected service is used by focused server tests; hosted
	// production services always set their configured worker hostname at startup.
	if host == "" {
		return peers, nil
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("browser relay worker address is unavailable")
	}
	// LookupNetIP may represent even an IPv4 literal as ::ffff:a.b.c.d.
	// Apply the same canonicalization as startup before updating permissions.
	for i, address := range addresses {
		addresses[i] = address.Unmap()
	}
	if err := relay.UpdateAllowedPeerIPs(addresses); err != nil {
		return nil, err
	}
	peers = nil
	for _, address := range addresses {
		for port := uint32(lo); port <= uint32(hi); port++ {
			peers = append(peers, netip.AddrPortFrom(address.Unmap(), uint16(port)))
		}
	}
	s.browserLiveMu.Lock()
	if s.browserMediaRelay != relay || s.browserLiveClosed {
		s.browserLiveMu.Unlock()
		return nil, browserrelay.ErrClosed
	}
	s.browserRelayPeers = peers
	s.browserLiveMu.Unlock()
	return peers, nil
}

func browserRelayURL(origin string) (string, error) {
	u, err := url.Parse(origin)
	if err != nil || u.User != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("browser relay origin is invalid")
	}
	port, scheme := u.Port(), "turn"
	if u.Scheme == "https" {
		scheme = "turns"
		if port == "" {
			port = "443"
		}
	} else if port == "" {
		port = "80"
	}
	return scheme + ":" + net.JoinHostPort(u.Hostname(), port) + "?transport=tcp", nil
}

func (s *Server) handleBrowserMediaRelayTicket(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	identity, authenticated := identityFrom(r)
	if !authenticated && s.opts.Token == "" && !s.multiuser() {
		identity, authenticated = &requestIdentity{}, true
	}
	if !authenticated {
		writeErr(w, http.StatusUnauthorized, errors.New("browser media relay requires authentication"))
		return
	}
	origin, sameOrigin := nativeBrowserLiveOrigin(r)
	if !sameOrigin {
		writeErr(w, http.StatusForbidden, errors.New("browser media relay requires the same origin"))
		return
	}
	sess, ok := s.nativeBrowserSession(w, r)
	if !ok {
		return
	}
	var input struct {
		ContextRef string `json:"context_ref"`
		TabID      string `json:"tab_id"`
		Generation int64  `json:"generation"`
		MediaEpoch string `json:"media_epoch"`
		LiveTicket string `json:"live_ticket"`
	}
	if err := decodeNativeBrowserBody(r, &input); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("browser media relay binding is invalid"))
		return
	}
	target := nativeBrowserTarget{ContextRef: input.ContextRef, TabID: input.TabID}
	scope := nativeBrowserRelayViewScope(input.LiveTicket)
	if !target.valid() || scope == "" || input.Generation < 1 || input.Generation > 9007199254740991 || !browserRelayEpoch.MatchString(input.MediaEpoch) {
		writeErr(w, http.StatusBadRequest, errors.New("browser media relay binding is invalid"))
		return
	}
	if err := sess.nativeBrowser.liveReady(); err != nil {
		writeNativeBrowserError(w, err)
		return
	}
	s.browserLiveMu.Lock()
	relay := s.browserMediaRelay
	var view *nativeBrowserLiveConnection
	if !s.browserLiveClosed && relay != nil {
		for connection := range s.browserLiveConnections {
			if connection.relayScope == scope && connection.sid == r.PathValue("sid") && connection.target == target && connection.identity == *identity {
				view = connection
				break
			}
		}
	}
	s.browserLiveMu.Unlock()
	if relay == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("browser media relay is unavailable"))
		return
	}
	if view == nil {
		writeErr(w, http.StatusForbidden, errors.New("browser media relay requires its active page view"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	state, err := sess.nativeBrowser.State(ctx, target)
	var generation int64
	if err != nil || json.Unmarshal(state["generation"], &generation) != nil || generation != input.Generation {
		writeErr(w, http.StatusConflict, errors.New("browser media relay page generation changed"))
		return
	}
	endpoint, err := browserRelayURL(origin)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	peers, err := s.refreshBrowserRelayPeers(ctx, relay)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("browser relay private worker route is unavailable"))
		return
	}
	// Issue against the service's configured worker peers. The request cannot
	// nominate a relay target, address, port, another context or another viewer.
	view.mu.Lock()
	if view.closed || r.Context().Err() != nil {
		view.mu.Unlock()
		writeErr(w, http.StatusGone, errors.New("browser media relay view is closed"))
		return
	}
	credentials, err := relay.IssuePeers(scope+":"+input.MediaEpoch, peers)
	view.mu.Unlock()
	if err != nil {
		writeErr(w, http.StatusTooManyRequests, errors.New("browser media relay capacity is unavailable"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"context_ref": target.ContextRef, "tab_id": target.TabID, "generation": input.Generation,
		"media_epoch": input.MediaEpoch, "ice_transport_policy": "relay", "expires_in": credentials.ExpiresIn,
		"ice_servers": []map[string]string{{"urls": endpoint, "username": credentials.Username, "credential": credentials.Password}},
	})
}
