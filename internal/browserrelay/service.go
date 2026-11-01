// Package browserrelay shares the web TCP listener with authenticated TURN/TCP.
// It relays opaque WebRTC datagrams without decoding or changing the W2 stream.
package browserrelay

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/logging"
	"github.com/pion/stun/v4"
	"github.com/pion/turn/v5"
)

type Config struct {
	Realm              string
	RelayAddr          netip.Addr
	BindAddr           netip.Addr
	AllowedPeerIPs     []netip.Addr
	PeerPortMin        uint16
	PeerPortMax        uint16
	CredentialTTL      time.Duration
	ScopeTTL           time.Duration
	ProbeTimeout       time.Duration
	IdleTimeout        time.Duration
	WriteTimeout       time.Duration
	MaxConnections     int
	MaxTURNConnections int
	MaxProbes          int
	HTTPQueue          int
	TURNQueue          int
	MaxCredentials     int
	MaxFrameBytes      int
	allowLoopback      bool
}

type Credentials struct {
	Username  string    `json:"username"`
	Password  string    `json:"password"`
	ExpiresIn int       `json:"expires_in"`
	ExpiresAt time.Time `json:"expires_at"`
}

type grant struct {
	username, scope string
	key             []byte
	initialUntil    time.Time
	leaseUntil      time.Time
	peers           map[netip.AddrPort]struct{}
	revoked         atomic.Bool
	pinned          *clientConn
	packets         map[*relayPacketConn]struct{}
}

type Service struct {
	cfg              Config
	base             net.Listener
	http             *childListener
	turnListener     *childListener
	turn             *turn.Server
	mu               sync.Mutex
	connections      map[*clientConn]struct{}
	turnConnections  map[string]*clientConn
	grants           map[string]*grant
	scopeStarts      map[string]time.Time
	probes           chan struct{}
	closed, draining bool
	acceptDone       chan struct{}
	probeWG          sync.WaitGroup
	expireDone       chan struct{}
	done             chan struct{}
	closeOnce        sync.Once
}

var (
	ErrClosed     = errors.New("browser media relay is closed or draining")
	ErrPeerDenied = errors.New("browser media peer is not authorized")
	ErrCapacity   = errors.New("browser media relay capacity reached")
)

func normalizeConfig(c Config) (Config, error) {
	if c.Realm == "" {
		c.Realm = "pogo-browser"
	}
	if len(c.Realm) > 128 || strings.ContainsAny(c.Realm, "\r\n\x00") {
		return c, errors.New("invalid relay realm")
	}
	if !c.RelayAddr.Is4() || c.RelayAddr.IsUnspecified() || c.RelayAddr.IsMulticast() {
		return c, errors.New("IPv4 relay address required")
	}
	if !c.BindAddr.IsValid() {
		c.BindAddr = netip.IPv4Unspecified()
	}
	if !c.BindAddr.Is4() {
		return c, errors.New("IPv4 relay bind address required")
	}
	if len(c.AllowedPeerIPs) == 0 || len(c.AllowedPeerIPs) > 32 {
		return c, ErrPeerDenied
	}
	for _, ip := range c.AllowedPeerIPs {
		if !ip.Is4() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLoopback() && !c.allowLoopback {
			return c, ErrPeerDenied
		}
	}
	if c.PeerPortMin == 0 {
		c.PeerPortMin = 19780
	}
	if c.PeerPortMax == 0 {
		c.PeerPortMax = 19811
	}
	if c.PeerPortMax < c.PeerPortMin || int(c.PeerPortMax)-int(c.PeerPortMin) > 31 {
		return c, ErrPeerDenied
	}
	if c.CredentialTTL == 0 {
		c.CredentialTTL = time.Minute
	}
	if c.ScopeTTL == 0 {
		c.ScopeTTL = 6 * time.Hour
	}
	if c.ProbeTimeout == 0 {
		c.ProbeTimeout = 2 * time.Second
	}
	if c.IdleTimeout == 0 {
		c.IdleTimeout = time.Minute
	}
	if c.WriteTimeout == 0 {
		c.WriteTimeout = 5 * time.Second
	}
	if c.CredentialTTL <= 0 || c.CredentialTTL > time.Minute || c.ScopeTTL < c.CredentialTTL || c.ScopeTTL > 6*time.Hour || c.ProbeTimeout <= 0 || c.ProbeTimeout > 10*time.Second || c.IdleTimeout <= 0 || c.WriteTimeout <= 0 {
		return c, errors.New("invalid relay time bounds")
	}
	if c.MaxConnections == 0 {
		c.MaxConnections = 1024
	}
	if c.MaxTURNConnections == 0 {
		c.MaxTURNConnections = 128
	}
	if c.MaxProbes == 0 {
		c.MaxProbes = 64
	}
	if c.HTTPQueue == 0 {
		c.HTTPQueue = 128
	}
	if c.TURNQueue == 0 {
		c.TURNQueue = 64
	}
	if c.MaxCredentials == 0 {
		c.MaxCredentials = 1024
	}
	if c.MaxFrameBytes == 0 {
		c.MaxFrameBytes = 4096
	}
	if c.MaxConnections < 1 || c.MaxConnections > 16384 || c.MaxTURNConnections < 1 || c.MaxTURNConnections > c.MaxConnections || c.MaxProbes < 1 || c.MaxProbes > c.MaxConnections || c.HTTPQueue < 1 || c.HTTPQueue > c.MaxConnections || c.TURNQueue < 1 || c.TURNQueue > c.MaxConnections || c.MaxCredentials < 1 || c.MaxCredentials > 16384 || c.MaxFrameBytes < 1600 || c.MaxFrameBytes > 8192 {
		return c, errors.New("invalid relay resource bounds")
	}
	c.AllowedPeerIPs = append([]netip.Addr(nil), c.AllowedPeerIPs...)
	return c, nil
}

// New takes ownership of an already bound listener on success. No second public port or
// UDP listener is opened. An already TLS-wrapped listener is supported.
func New(listener net.Listener, cfg Config) (*Service, error) {
	if listener == nil {
		return nil, errors.New("web listener required")
	}
	cfg, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	s := &Service{cfg: cfg, base: listener, connections: make(map[*clientConn]struct{}), turnConnections: make(map[string]*clientConn), grants: make(map[string]*grant), scopeStarts: make(map[string]time.Time), probes: make(chan struct{}, cfg.MaxProbes), acceptDone: make(chan struct{}), expireDone: make(chan struct{}), done: make(chan struct{})}
	s.http = &childListener{parent: s, queue: make(chan *clientConn, cfg.HTTPQueue), done: make(chan struct{})}
	s.turnListener = &childListener{parent: s, queue: make(chan *clientConn, cfg.TURNQueue), done: make(chan struct{})}
	logger := logging.NewDefaultLoggerFactory()
	logger.Writer = io.Discard
	s.turn, err = turn.NewServer(turn.ServerConfig{
		Realm: cfg.Realm, LoggerFactory: logger, AuthHandler: s.authenticate, QuotaHandler: s.quota,
		StrictAddressFamily: true, InboundMTU: cfg.MaxFrameBytes,
		ListenerConfigs: []turn.ListenerConfig{{Listener: s.turnListener, RelayAddressGenerator: &relayGenerator{s: s}, PermissionHandler: s.permission}},
		EventHandler:    turn.EventHandler{OnAuth: s.authenticated},
	})
	if err != nil {
		return nil, err
	}
	go s.acceptLoop()
	go s.expireLoop()
	return s, nil
}

func (s *Service) HTTPListener() net.Listener { return s.http }
func (s *Service) Addr() net.Addr             { return s.base.Addr() }

func remoteKey(addr net.Addr) string { return addr.Network() + ":" + addr.String() }

// IssuePeers must only be called after the owning live view has been authorized.
// Scope is a view hash optionally followed by a colon-delimited epoch.
func (s *Service) IssuePeers(scope string, peers []netip.AddrPort) (Credentials, error) {
	if len(scope) < 1 || len(scope) > 256 || strings.ContainsAny(scope, "\r\n\x00") || len(peers) < 1 || len(peers) > 1024 {
		return Credentials{}, ErrPeerDenied
	}
	var random [48]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Credentials{}, err
	}
	username := base64.RawURLEncoding.EncodeToString(random[:24])
	password := base64.RawURLEncoding.EncodeToString(random[24:])
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.draining {
		return Credentials{}, ErrClosed
	}
	if len(peers) > 32*len(s.cfg.AllowedPeerIPs) {
		return Credentials{}, ErrPeerDenied
	}
	allowed := make(map[netip.AddrPort]struct{}, len(peers))
	for _, peer := range peers {
		if !s.allowedPeer(peer) {
			return Credentials{}, ErrPeerDenied
		}
		allowed[netip.AddrPortFrom(peer.Addr().Unmap(), peer.Port())] = struct{}{}
	}
	if len(s.grants) >= s.cfg.MaxCredentials {
		return Credentials{}, ErrCapacity
	}
	view := strings.SplitN(scope, ":", 2)[0]
	if view == "" {
		return Credentials{}, ErrPeerDenied
	}
	start, ok := s.scopeStarts[view]
	if !ok {
		if len(s.scopeStarts) >= s.cfg.MaxCredentials {
			return Credentials{}, ErrCapacity
		}
		start = now
		s.scopeStarts[view] = start
	}
	until := start.Add(s.cfg.ScopeTTL)
	if !now.Before(until) {
		return Credentials{}, ErrClosed
	}
	expires := now.Add(s.cfg.CredentialTTL)
	if until.Before(expires) {
		expires = until
	}
	s.grants[username] = &grant{username: username, scope: scope, key: turn.GenerateAuthKey(username, s.cfg.Realm, password), initialUntil: expires, leaseUntil: until, peers: allowed, packets: make(map[*relayPacketConn]struct{})}
	return Credentials{Username: username, Password: password, ExpiresIn: int((expires.Sub(now) + time.Second - 1) / time.Second), ExpiresAt: expires}, nil
}

// UpdateAllowedPeerIPs accepts only the exact addresses freshly resolved for
// the configured worker. Old grants are fenced and their sockets are closed
// when any of their peers is no longer in that address set. No CIDR is accepted.
func (s *Service) UpdateAllowedPeerIPs(ips []netip.Addr) error {
	if len(ips) == 0 || len(ips) > 32 {
		return ErrPeerDenied
	}
	set := make(map[netip.Addr]struct{}, len(ips))
	for _, ip := range ips {
		if !ip.Is4() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLoopback() && !s.cfg.allowLoopback {
			return ErrPeerDenied
		}
		set[ip] = struct{}{}
	}
	s.mu.Lock()
	if s.closed || s.draining {
		s.mu.Unlock()
		return ErrClosed
	}
	s.cfg.AllowedPeerIPs = append([]netip.Addr(nil), ips...)
	var conns []*clientConn
	var packets []*relayPacketConn
	for key, g := range s.grants {
		remove := false
		for peer := range g.peers {
			if _, ok := set[peer.Addr().Unmap()]; !ok {
				remove = true
				break
			}
		}
		if remove {
			g.revoked.Store(true)
			delete(s.grants, key)
			if g.pinned != nil {
				conns = append(conns, g.pinned)
			}
			for p := range g.packets {
				packets = append(packets, p)
			}
		}
	}
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
	for _, p := range packets {
		_ = p.Close()
	}
	return nil
}

func (s *Service) allowedPeer(peer netip.AddrPort) bool {
	if !peer.IsValid() || peer.Port() < s.cfg.PeerPortMin || peer.Port() > s.cfg.PeerPortMax {
		return false
	}
	for _, ip := range s.cfg.AllowedPeerIPs {
		if peer.Addr().Unmap() == ip.Unmap() {
			return true
		}
	}
	return false
}

func (s *Service) authenticate(request *turn.RequestAttributes) (string, []byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.grants[request.Username]
	if g == nil || g.revoked.Load() || request.Realm != s.cfg.Realm || !time.Now().Before(g.leaseUntil) || s.closed {
		return "", nil, false
	}
	c := s.turnConnections[remoteKey(request.SrcAddr)]
	if c == nil {
		return "", nil, false
	}
	if request.Method == stun.MethodAllocate {
		if s.draining || !time.Now().Before(g.initialUntil) || g.pinned != nil && g.pinned != c {
			return "", nil, false
		}
	} else if g.pinned != c {
		return "", nil, false
	}
	return g.username, g.key, true
}

// Pion calls OnAuth(true) only after MESSAGE-INTEGRITY has been verified.
func (s *Service) authenticated(src, _ net.Addr, _, username, _ string, method string, valid bool) {
	if !valid || method != "Allocate" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.grants[username]
	c := s.turnConnections[remoteKey(src)]
	if g != nil && c != nil && g.pinned == nil && c.grant == nil && !g.revoked.Load() && time.Now().Before(g.initialUntil) && !s.closed && !s.draining {
		g.pinned = c
		c.grant = g
	}
}

func (s *Service) quota(username, realm string, src net.Addr) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.grants[username]
	return !s.closed && !s.draining && realm == s.cfg.Realm && g != nil && !g.revoked.Load() && g.pinned != nil && g.pinned == s.turnConnections[remoteKey(src)] && len(g.packets) == 0 && time.Now().Before(g.leaseUntil)
}

func (s *Service) permission(src net.Addr, peer net.IP) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.turnConnections[remoteKey(src)]
	if c == nil || c.grant == nil || c.grant.revoked.Load() || !time.Now().Before(c.grant.leaseUntil) {
		return false
	}
	ip, ok := netip.AddrFromSlice(peer)
	if !ok {
		return false
	}
	for allowed := range c.grant.peers {
		if allowed.Addr().Unmap() == ip.Unmap() {
			return true
		}
	}
	return false
}

func (s *Service) connectionClosed(c *clientConn) {
	s.mu.Lock()
	delete(s.connections, c)
	key := remoteKey(c.RemoteAddr())
	if s.turnConnections[key] == c {
		delete(s.turnConnections, key)
	}
	g := c.grant
	var packets []*relayPacketConn
	if g != nil && g.pinned == c {
		g.revoked.Store(true)
		delete(s.grants, g.username)
		for p := range g.packets {
			packets = append(packets, p)
		}
	}
	s.mu.Unlock()
	for _, p := range packets {
		_ = p.Close()
	}
}

// Called with mu held, before releasing that lock to close the collected sockets.
func (s *Service) revokeLocked(predicate func(*grant) bool) ([]*clientConn, []*relayPacketConn) {
	var conns []*clientConn
	var packets []*relayPacketConn
	for key, g := range s.grants {
		if predicate(g) {
			g.revoked.Store(true)
			delete(s.grants, key)
			if g.pinned != nil {
				conns = append(conns, g.pinned)
			}
			for p := range g.packets {
				packets = append(packets, p)
			}
		}
	}
	return conns, packets
}

func closeRevoked(conns []*clientConn, packets []*relayPacketConn) {
	for _, c := range conns {
		_ = c.Close()
	}
	for _, p := range packets {
		_ = p.Close()
	}
}

func (s *Service) revoke(predicate func(*grant) bool) {
	s.mu.Lock()
	conns, packets := s.revokeLocked(predicate)
	s.mu.Unlock()
	closeRevoked(conns, packets)
}

// RevokeScope also matches colon-delimited epochs of the exact view key.
func (s *Service) RevokeScope(scope string) {
	if scope == "" {
		return
	}
	s.mu.Lock()
	conns, packets := s.revokeLocked(func(g *grant) bool { return g.scope == scope || strings.HasPrefix(g.scope, scope+":") })
	for key := range s.scopeStarts {
		if key == scope || strings.HasPrefix(key, scope+":") {
			delete(s.scopeStarts, key)
		}
	}
	s.mu.Unlock()
	closeRevoked(conns, packets)
}

func (s *Service) expireLoop() {
	defer close(s.expireDone)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			s.revoke(func(g *grant) bool {
				return !now.Before(g.leaseUntil) || g.pinned == nil && !now.Before(g.initialUntil)
			})
			// Keep the view start until its owner calls RevokeScope. Deleting it
			// here would let an ACTIVE view reset its hard lifetime with a new
			// epoch after expiry. This registry shares the credential bound.
		case <-s.done:
			return
		}
	}
}

func (s *Service) ActiveAllocations() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for _, g := range s.grants {
		total += len(g.packets)
	}
	return total
}

// Drain stops new handshakes, permits current allocations to finish until the
// caller's deadline, and then closes TURN and HTTP connections. Coordinate this
// with http.Server.Shutdown for the application's HTTP request drain.
func (s *Service) Drain(ctx context.Context) error {
	s.mu.Lock()
	s.draining = true
	s.mu.Unlock()
	_ = s.base.Close()
	_ = s.http.Close()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if s.ActiveAllocations() == 0 {
			return s.Close()
		}
		select {
		case <-ctx.Done():
			_ = s.Close()
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.draining = true
		s.mu.Unlock()
		_ = s.base.Close()
		_ = s.http.Close()
		_ = s.turnListener.Close()
		s.revoke(func(*grant) bool { return true })
		s.mu.Lock()
		var connections []*clientConn
		for c := range s.connections {
			connections = append(connections, c)
		}
		s.mu.Unlock()
		for _, c := range connections {
			_ = c.Close()
		}
		_ = s.turn.Close()
		close(s.done)
		<-s.acceptDone
		s.probeWG.Wait()
		<-s.expireDone
	})
	return nil
}
