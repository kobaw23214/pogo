package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

const nativeBrowserLiveTicketTTL = 30 * time.Second
const nativeBrowserLiveTicketLimit = 1024
const nativeBrowserLiveWatchInterval = 100 * time.Millisecond

type nativeBrowserLiveConnection struct {
	mu               sync.Mutex
	body             io.ReadCloser
	closed           bool
	cancel           context.CancelFunc
	maintenanceGrant [32]byte
	identity         requestIdentity
	sid              string
	target           nativeBrowserTarget
	relayScope       string
	onClose          func()
}

func (c *nativeBrowserLiveConnection) close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	body := c.body
	onClose := c.onClose
	c.mu.Unlock()
	c.cancel()
	if onClose != nil {
		onClose()
	}
	if body != nil {
		_ = body.Close()
	}
}

func (c *nativeBrowserLiveConnection) setBody(body io.ReadCloser) {
	c.mu.Lock()
	closed := c.closed
	if !closed {
		c.body = body
	}
	c.mu.Unlock()
	if closed {
		_ = body.Close()
	}
}

func (s *Server) closeBrowserLive() {
	s.closeBrowserAuthoring()
	s.browserLiveMu.Lock()
	s.browserLiveClosed = true
	s.browserLiveTickets = nil
	connections := make([]*nativeBrowserLiveConnection, 0, len(s.browserLiveConnections))
	for connection := range s.browserLiveConnections {
		connections = append(connections, connection)
	}
	s.browserLiveMu.Unlock()
	for _, connection := range connections {
		connection.close()
	}
}

func (s *Server) handleReleaseBrowserLive(w http.ResponseWriter, r *http.Request) {
	s.browserLiveMu.Lock()
	total, ordinary, blocked := len(s.browserLiveConnections), 0, 0
	grant := releaseMaintenanceGrant(r)
	for connection := range s.browserLiveConnections {
		if connection.maintenanceGrant == ([32]byte{}) {
			ordinary++
		}
		if connection.maintenanceGrant != grant {
			blocked++
		}
	}
	s.browserLiveMu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]int{"connections": total, "ordinary_connections": ordinary, "blocked_connections": blocked})
}

func (s *Server) watchBrowserLive(ctx context.Context, b *nativeBrowser, connection *nativeBrowserLiveConnection) {
	ticker := time.NewTicker(nativeBrowserLiveWatchInterval)
	defer ticker.Stop()
	for {
		active, token := s.mgr.releaseMaintenance()
		var current [32]byte
		if active && token != "" {
			current = sha256.Sum256([]byte(token))
		}
		if (active && token == "") || current != connection.maintenanceGrant || b.liveReady() != nil {
			connection.close()
			return
		}
		select {
		case <-ctx.Done():
			connection.close()
			return
		case <-ticker.C:
		}
	}
}

var nativeBrowserLiveTransport = func() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// This is a configured private worker, not a user-provided web URL. Do not
	// send its service credential through an ambient HTTP proxy.
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = 10 * time.Second
	transport.ForceAttemptHTTP2 = false
	return transport
}()

type nativeBrowserLiveTicket struct {
	identity    requestIdentity
	sid, origin string
	target      nativeBrowserTarget
	expires     time.Time
}

func isNativeBrowserLivePath(r *http.Request) bool {
	parts := strings.Split(r.URL.Path, "/")
	return len(parts) == 6 && parts[1] == "api" && parts[2] == "sessions" && parts[3] != "" && parts[4] == "browser" && parts[5] == "live"
}

func nativeBrowserLiveOrigin(r *http.Request) (string, bool) {
	origin := r.Header.Get("Origin")
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Host != r.Host || u.User != nil || origin != u.Scheme+"://"+u.Host {
		return "", false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return "", false
	}
	return origin, true
}

func nativeBrowserWebSocketRequest(r *http.Request) bool {
	connection := false
	for _, part := range strings.Split(r.Header.Get("Connection"), ",") {
		connection = connection || strings.EqualFold(strings.TrimSpace(part), "upgrade")
	}
	return r.Method == http.MethodGet && connection && strings.EqualFold(r.Header.Get("Upgrade"), "websocket") && r.Header.Get("Sec-WebSocket-Version") == "13"
}

// liveReady only observes existing execution ownership. Connecting a viewer
// cannot create a context, clean one up or replay an interrupted operation.
func (b *nativeBrowser) liveReady() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.cleanupRequired {
		return &nativeBrowserHTTPError{status: http.StatusGone, message: "browser context is closed"}
	}
	if !b.created {
		return &nativeBrowserHTTPError{status: http.StatusConflict, message: "browser context has not been opened"}
	}
	return nil
}

func (s *Server) handleBrowserLiveTicket(w http.ResponseWriter, r *http.Request) {
	identity, authenticated := identityFrom(r)
	// Preserve the server's intentional unauthenticated single-user mode. An
	// enabled account store or configured token never enters this exception.
	// The viewer still needs a same-origin, bound, single-use ticket.
	if !authenticated && s.opts.Token == "" && !s.multiuser() {
		identity, authenticated = &requestIdentity{}, true
	}
	if !authenticated {
		writeErr(w, http.StatusUnauthorized, errors.New("browser live access requires authentication"))
		return
	}
	origin, ok := nativeBrowserLiveOrigin(r)
	if !ok {
		writeErr(w, http.StatusForbidden, errors.New("browser live access requires the same origin"))
		return
	}
	sess, ok := s.nativeBrowserSession(w, r)
	if !ok {
		return
	}
	var input struct {
		ContextRef string `json:"context_ref"`
		TabID      string `json:"tab_id"`
	}
	if err := decodeNativeBrowserBody(r, &input); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	target := nativeBrowserTarget{ContextRef: input.ContextRef, TabID: input.TabID}
	if !target.valid() {
		writeNativeBrowserError(w, invalidNativeTarget())
		return
	}
	if err := sess.nativeBrowser.liveReady(); err != nil {
		writeNativeBrowserError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), DefaultBrowserOpTimeout)
	defer cancel()
	// The bound State form explicitly never calls ensure or recovers a context.
	if _, err := sess.nativeBrowser.State(ctx, target); err != nil {
		status := http.StatusBadGateway
		var remote *nativeBrowserHTTPError
		if errors.As(err, &remote) {
			switch remote.status {
			case 400, 403, 404, 409, 410, 429, 503:
				status = remote.status
			}
		}
		writeErr(w, status, errors.New("browser page binding is unavailable; refresh the browser state before reconnecting"))
		return
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("browser live ticket unavailable"))
		return
	}
	ticket, now := hex.EncodeToString(nonce[:]), time.Now()
	s.browserLiveMu.Lock()
	if s.browserLiveClosed {
		s.browserLiveMu.Unlock()
		writeErr(w, http.StatusServiceUnavailable, errors.New("browser live service is shutting down"))
		return
	}
	if s.browserLiveTickets == nil {
		s.browserLiveTickets = make(map[string]nativeBrowserLiveTicket)
	}
	for key, issued := range s.browserLiveTickets {
		if !issued.expires.After(now) {
			delete(s.browserLiveTickets, key)
		}
	}
	if len(s.browserLiveTickets) >= nativeBrowserLiveTicketLimit {
		s.browserLiveMu.Unlock()
		writeErr(w, http.StatusTooManyRequests, errors.New("too many pending browser live connections"))
		return
	}
	s.browserLiveTickets[ticket] = nativeBrowserLiveTicket{identity: *identity, sid: r.PathValue("sid"), origin: origin, target: target, expires: now.Add(nativeBrowserLiveTicketTTL)}
	s.browserLiveMu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"ticket": ticket, "expires_in": 30})
}

func (s *Server) authenticateNativeBrowserLive(r *http.Request) (requestIdentity, bool) {
	denied := requestIdentity{}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) != 3 || len(query["ticket"]) != 1 || len(query["context_ref"]) != 1 || len(query["tab_id"]) != 1 || len(query.Get("ticket")) != 64 {
		return denied, false
	}
	// Consume even a failed binding attempt so the nonce cannot be probed or
	// accidentally reused. A new connection always obtains a fresh ticket.
	s.browserLiveMu.Lock()
	issued, exists := s.browserLiveTickets[query.Get("ticket")]
	delete(s.browserLiveTickets, query.Get("ticket"))
	s.browserLiveMu.Unlock()
	origin, ok := nativeBrowserLiveOrigin(r)
	target := nativeBrowserTarget{ContextRef: query.Get("context_ref"), TabID: query.Get("tab_id")}
	parts := strings.Split(r.URL.Path, "/")
	if !exists || !issued.expires.After(time.Now()) || !ok || origin != issued.origin || !nativeBrowserWebSocketRequest(r) || !target.valid() || target != issued.target || parts[3] != issued.sid {
		return denied, false
	}
	if s.multiuser() {
		s.auth.mu.RLock()
		_, known := s.auth.users[issued.identity.Username]
		s.auth.mu.RUnlock()
		if !known || s.localIdentity(issued.identity.Username) != issued.identity {
			return denied, false
		}
	}
	return issued.identity, true
}

func (s *Server) handleBrowserLive(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.nativeBrowserSession(w, r)
	if !ok {
		return
	}
	if !nativeBrowserWebSocketRequest(r) {
		writeErr(w, http.StatusBadRequest, errors.New("browser live access requires a WebSocket upgrade"))
		return
	}
	target, err := parseNativeBrowserTarget(r)
	if err != nil || !target.valid() {
		writeNativeBrowserError(w, invalidNativeTarget())
		return
	}
	if err := sess.nativeBrowser.liveReady(); err != nil {
		writeNativeBrowserError(w, err)
		return
	}
	b := sess.nativeBrowser
	ctx, cancel := context.WithCancel(r.Context())
	identity, _ := identityFrom(r)
	scope := nativeBrowserRelayViewScope(r.URL.Query().Get("ticket"))
	connection := &nativeBrowserLiveConnection{cancel: cancel, maintenanceGrant: releaseMaintenanceGrant(r),
		sid: r.PathValue("sid"), target: target, relayScope: scope,
		onClose: func() { s.revokeBrowserRelayScope(scope) }}
	if identity != nil {
		connection.identity = *identity
	}
	s.browserLiveMu.Lock()
	if s.browserLiveClosed {
		s.browserLiveMu.Unlock()
		cancel()
		writeErr(w, http.StatusServiceUnavailable, errors.New("browser live service is shutting down"))
		return
	}
	if s.browserLiveConnections == nil {
		s.browserLiveConnections = make(map[*nativeBrowserLiveConnection]struct{})
	}
	s.browserLiveConnections[connection] = struct{}{}
	s.browserLiveMu.Unlock()
	defer func() {
		connection.close()
		s.browserLiveMu.Lock()
		delete(s.browserLiveConnections, connection)
		s.browserLiveMu.Unlock()
	}()
	r = r.WithContext(ctx)
	go s.watchBrowserLive(ctx, b, connection)
	upstream, err := url.Parse(b.config.url + "/sessions/" + url.PathEscape(b.id) + target.suffix("/live"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, errors.New("browser live service is unavailable"))
		return
	}
	proxy := &httputil.ReverseProxy{
		Transport: nativeBrowserLiveTransport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = upstream
			pr.Out.Host = upstream.Host
			// The worker receives only its own credential and the WebSocket
			// handshake. Never forward cookies, tickets, origins or user tokens.
			pr.Out.Header = make(http.Header)
			pr.Out.Header.Set("Authorization", "Bearer "+b.config.token)
			pr.Out.Header.Set("Connection", "Upgrade")
			pr.Out.Header.Set("Upgrade", "websocket")
			pr.Out.Header.Set("Sec-WebSocket-Key", pr.In.Header.Get("Sec-WebSocket-Key"))
			pr.Out.Header.Set("Sec-WebSocket-Version", "13")
		},
		ModifyResponse: func(res *http.Response) error {
			if res.StatusCode == http.StatusSwitchingProtocols {
				connection.setBody(res.Body)
				res.Header = http.Header{"Connection": {"Upgrade"}, "Upgrade": {"websocket"}, "Sec-Websocket-Accept": {res.Header.Get("Sec-WebSocket-Accept")}, "Cache-Control": {"no-store"}}
				return nil
			}
			_ = res.Body.Close()
			status := res.StatusCode
			switch status {
			case 400, 403, 404, 409, 410, 429, 503:
			default:
				status = http.StatusBadGateway
			}
			body := "{\"error\":\"browser live connection was rejected; refresh the browser state before reconnecting\"}\n"
			res.StatusCode, res.Status = status, http.StatusText(status)
			res.Header = http.Header{"Content-Type": {"application/json"}, "Cache-Control": {"no-store"}}
			res.Body, res.ContentLength = io.NopCloser(strings.NewReader(body)), int64(len(body))
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			writeErr(w, http.StatusBadGateway, errors.New("browser live service is unreachable; no operation was retried"))
		},
		ErrorLog: log.New(io.Discard, "", 0),
	}
	sess.touch()
	proxy.ServeHTTP(w, r)
}
