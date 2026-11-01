package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/pion/ice/v4"
	"github.com/pion/logging"
	"github.com/pion/webrtc/v4"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

type server struct {
	cfg       config
	mu        sync.Mutex
	sessions  map[string]*session
	peers     map[string]*peer
	creating  map[viewKey]*client
	listeners []*net.UnixListener
	clients   map[*client]bool
	done      chan struct{}
	once      sync.Once
	received  atomic.Uint64
	rejected  atomic.Uint64
	udpMux    ice.UDPMux
	udp       net.PacketConn
}
type session struct {
	token string
	owner *client
	host  *frameHost
	pages map[string]*source
}
type source struct {
	display    uint32
	generation uint64
	lastSeq    uint64
	lastIDR    time.Time
	pendingIDR bool
	lastBudget int64
}
type viewKey struct{ session, page, view string }
type client struct {
	conn   *net.UnixConn
	server *server
	queue  chan any
	done   chan struct{}
	once   sync.Once
}
type frameHost struct {
	conn    *net.UnixConn
	session string
	server  *server
	queue   chan any
	done    chan struct{}
	once    sync.Once
}

func newServer(c config) (*server, error) {
	if c.validate() != nil {
		return nil, errInvalid
	}
	return &server{cfg: c, sessions: map[string]*session{}, peers: map[string]*peer{}, creating: map[viewKey]*client{}, clients: map[*client]bool{}, done: make(chan struct{})}, nil
}
func (s *server) Listen() error {
	if err := os.MkdirAll(s.cfg.Dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(s.cfg.Dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 || !ownedFile(info) {
		return errInvalid
	}
	for port := int(s.cfg.PortMin); port <= int(s.cfg.PortMax); port++ {
		conn, e := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
		if e == nil {
			s.udp = conn
			break
		}
	}
	if s.udp == nil {
		return errMedia
	}
	factory := logging.NewDefaultLoggerFactory()
	factory.Writer = io.Discard
	s.udpMux = webrtc.NewICEUDPMux(factory.NewLogger("ice"), s.udp)
	for _, name := range []string{"ctl.sock", "frames.sock"} {
		path := filepath.Join(s.cfg.Dir, name)
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return errInvalid
		}
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		if err != nil {
			return err
		}
		if err = os.Chmod(path, 0600); err != nil {
			_ = listener.Close()
			return err
		}
		s.listeners = append(s.listeners, listener)
		go s.accept(listener, name == "frames.sock")
	}
	go s.diagnosticsLoop()
	return nil
}
func (s *server) accept(listener *net.UnixListener, frames bool) {
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			return
		}
		if !sameUID(conn) {
			_ = conn.Close()
			continue
		}
		if frames {
			go s.frameConnection(conn)
		} else {
			go s.controlConnection(conn)
		}
	}
}
func (s *server) Close() {
	s.once.Do(func() {
		close(s.done)
		for _, l := range s.listeners {
			_ = l.Close()
		}
		s.mu.Lock()
		clients := make([]*client, 0, len(s.clients))
		for c := range s.clients {
			clients = append(clients, c)
		}
		s.mu.Unlock()
		for _, c := range clients {
			c.close()
		}
		if s.udpMux != nil {
			_ = s.udpMux.Close()
		}
		if s.udp != nil {
			_ = s.udp.Close()
		}
	})
}
func send(queue chan any, done <-chan struct{}, value any) bool {
	select {
	case <-done:
		return false
	default:
	}
	select {
	case queue <- value:
		return true
	case <-done:
		return false
	default:
		return false
	}
}
func writeMessages(c *net.UnixConn, queue <-chan any, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case value := <-queue:
			_ = c.SetWriteDeadline(time.Now().Add(2 * time.Second))
			if json.NewEncoder(c).Encode(value) != nil {
				_ = c.Close()
				return
			}
		}
	}
}
func (c *client) emit(value any) {
	if !send(c.queue, c.done, value) {
		go c.close()
	}
}
func (c *client) close() {
	c.once.Do(func() {
		close(c.done)
		_ = c.conn.Close()
		s := c.server
		s.mu.Lock()
		delete(s.clients, c)
		var peers []*peer
		var hosts []*frameHost
		for id, p := range s.peers {
			if p.owner == c {
				peers = append(peers, p)
				delete(s.peers, id)
			}
		}
		for id, state := range s.sessions {
			if state.owner == c {
				if state.host != nil {
					hosts = append(hosts, state.host)
				}
				delete(s.sessions, id)
			}
		}
		s.mu.Unlock()
		for _, p := range peers {
			p.close()
		}
		for _, h := range hosts {
			h.close()
		}
	})
}
func (s *server) controlConnection(conn *net.UnixConn) {
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReaderSize(conn, maxJSON+1)
	b, err := readLine(reader)
	var h hello
	if err != nil || strictJSON(b, &h) != nil || h.Type != "hello" || h.Version != 1 || h.Session != "" || !secretEqual(h.Token, s.cfg.Token) {
		_ = conn.Close()
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	c := &client{conn: conn, server: s, queue: make(chan any, 32), done: make(chan struct{})}
	s.mu.Lock()
	if len(s.clients) >= 4 {
		s.mu.Unlock()
		_ = conn.Close()
		return
	}
	s.clients[c] = true
	s.mu.Unlock()
	defer c.close()
	go writeMessages(conn, c.queue, c.done)
	c.emit(map[string]any{"type": "ready", "version": 1})
	for {
		b, err = readLine(reader)
		if err != nil {
			return
		}
		var r request
		if strictJSON(b, &r) != nil || !identifier.MatchString(r.ID) {
			return
		}
		result, e := s.rpc(c, r)
		if e != nil {
			code := e.Error()
			if !errors.Is(e, errScope) && !errors.Is(e, errInvalid) && !errors.Is(e, errCapacity) {
				code = errMedia.Error()
			}
			c.emit(map[string]any{"id": r.ID, "ok": false, "error": code})
		} else {
			c.emit(map[string]any{"id": r.ID, "ok": true, "result": result})
		}
	}
}

type peerArgs struct {
	scope
	PeerID     string          `json:"peer_id"`
	Codec      string          `json:"codec,omitempty"`
	Width      uint16          `json:"width,omitempty"`
	Height     uint16          `json:"height,omitempty"`
	MinContent uint64          `json:"content_seq_min,omitempty"`
	Profile    string          `json:"profile_level_id,omitempty"`
	Answer     json.RawMessage `json:"answer,omitempty"`
	Candidate  json.RawMessage `json:"candidate,omitempty"`
}

func (s *server) rpc(c *client, r request) (any, error) {
	switch r.Op {
	case "register_session":
		var a struct {
			Session string `json:"session"`
			Token   string `json:"token"`
		}
		if strictJSON(r.Args, &a) != nil || !sessionIdentifier.MatchString(a.Session) || len(a.Token) < 32 || len(a.Token) > 256 {
			return nil, errInvalid
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if old := s.sessions[a.Session]; old != nil {
			if old.owner != c || !secretEqual(a.Token, old.token) {
				return nil, errScope
			}
			return map[string]bool{"registered": true}, nil
		}
		if len(s.sessions) >= 32 {
			return nil, errCapacity
		}
		s.sessions[a.Session] = &session{token: a.Token, owner: c, pages: map[string]*source{}}
		return map[string]bool{"registered": true}, nil
	case "register_page":
		var a struct {
			Session    string `json:"session"`
			Page       string `json:"page"`
			Display    uint32 `json:"display_id"`
			Generation uint64 `json:"generation"`
		}
		if strictJSON(r.Args, &a) != nil || !identifier.MatchString(a.Page) || a.Display < 2 || a.Generation == 0 {
			return nil, errInvalid
		}
		s.mu.Lock()
		state := s.sessions[a.Session]
		if state == nil || state.owner != c {
			s.mu.Unlock()
			return nil, errScope
		}
		old := state.pages[a.Page]
		if old != nil && (a.Generation < old.generation || (a.Generation == old.generation && a.Display != old.display)) {
			s.mu.Unlock()
			return nil, errScope
		}
		for page, value := range state.pages {
			if page != a.Page && value.display == a.Display {
				s.mu.Unlock()
				return nil, errScope
			}
		}
		if old == nil && len(state.pages) >= 64 {
			s.mu.Unlock()
			return nil, errCapacity
		}
		var closed []*peer
		if old == nil || old.generation != a.Generation {
			state.pages[a.Page] = &source{display: a.Display, generation: a.Generation}
			for id, p := range s.peers {
				if p.scope.Session == a.Session && p.scope.Page == a.Page {
					delete(s.peers, id)
					closed = append(closed, p)
				}
			}
		}
		s.mu.Unlock()
		for _, p := range closed {
			p.close()
		}
		return map[string]bool{"registered": true}, nil
	case "unregister_session":
		var a struct {
			Session string `json:"session"`
		}
		if strictJSON(r.Args, &a) != nil {
			return nil, errInvalid
		}
		s.mu.Lock()
		state := s.sessions[a.Session]
		if state == nil || state.owner != c {
			s.mu.Unlock()
			return nil, errScope
		}
		delete(s.sessions, a.Session)
		var peers []*peer
		for id, p := range s.peers {
			if p.scope.Session == a.Session {
				delete(s.peers, id)
				peers = append(peers, p)
			}
		}
		s.mu.Unlock()
		for _, p := range peers {
			p.close()
		}
		if state.host != nil {
			state.host.close()
		}
		return map[string]bool{"closed": true}, nil
	case "unregister_page":
		var a struct {
			Session string `json:"session"`
			Page    string `json:"page"`
		}
		if strictJSON(r.Args, &a) != nil || !identifier.MatchString(a.Page) {
			return nil, errInvalid
		}
		s.mu.Lock()
		state := s.sessions[a.Session]
		if state == nil || state.owner != c {
			s.mu.Unlock()
			return nil, errScope
		}
		delete(state.pages, a.Page)
		var peers []*peer
		for id, p := range s.peers {
			if p.scope.Session == a.Session && p.scope.Page == a.Page {
				delete(s.peers, id)
				peers = append(peers, p)
			}
		}
		s.mu.Unlock()
		for _, p := range peers {
			p.close()
		}
		return map[string]bool{"closed": true}, nil
	case "create":
		var a peerArgs
		if strictJSON(r.Args, &a) != nil || !a.scope.valid() || a.Width < 2 || a.Height < 2 || a.Width > 3840 || a.Height > 2400 || a.Width%2 != 0 || a.Height%2 != 0 || uint64(a.Width)*uint64(a.Height) > 7464960 || (a.Codec != "vp8" && a.Codec != "h264") || (a.Codec == "h264" && !profilePattern.MatchString(a.Profile)) {
			return nil, errInvalid
		}
		return s.createPeer(c, a)
	case "answer", "candidate", "close", "stats", "keyframe":
		var a peerArgs
		if strictJSON(r.Args, &a) != nil || !a.scope.valid() {
			return nil, errInvalid
		}
		s.mu.Lock()
		p := s.peers[a.PeerID]
		if p == nil || p.owner != c || p.scope != a.scope {
			s.mu.Unlock()
			return nil, errScope
		}
		if r.Op == "close" {
			delete(s.peers, a.PeerID)
		}
		s.mu.Unlock()
		switch r.Op {
		case "close":
			p.close()
			s.updateEncoderBudget(p.scope)
			return map[string]bool{"closed": true}, nil
		case "stats":
			return p.stats(), nil
		case "keyframe":
			s.requestIDR(p.scope, "viewer")
			return map[string]bool{"requested": true}, nil
		case "answer":
			return nil, p.answer(a.Answer)
		case "candidate":
			return nil, p.candidate(a.Candidate)
		}
	}
	return nil, errInvalid
}
func (s *server) frameConnection(conn *net.UnixConn) {
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReaderSize(conn, maxJSON+1)
	b, e := readLine(reader)
	var h hello
	if e != nil || strictJSON(b, &h) != nil || h.Version != 1 || (h.Type != "" && h.Type != "hello") {
		_ = conn.Close()
		return
	}
	fh := &frameHost{conn: conn, session: h.Session, server: s, queue: make(chan any, 16), done: make(chan struct{})}
	s.mu.Lock()
	state := s.sessions[h.Session]
	if state == nil || !secretEqual(h.Token, state.token) || state.host != nil {
		s.mu.Unlock()
		_ = conn.Close()
		return
	}
	state.host = fh
	s.mu.Unlock()
	defer fh.close()
	_ = conn.SetReadDeadline(time.Time{})
	go writeMessages(conn, fh.queue, fh.done)
	send(fh.queue, fh.done, map[string]any{"type": "ready", "version": 1, "session": h.Session})
	s.mu.Lock()
	var scopes []scope
	for _, p := range s.peers {
		if p.scope.Session == h.Session {
			scopes = append(scopes, p.scope)
		}
	}
	s.mu.Unlock()
	for _, sc := range scopes {
		s.updateEncoderBudget(sc)
		s.requestIDR(sc, "host-ready")
	}
	for {
		f, err := readFrame(reader)
		if err != nil {
			return
		}
		if s.publish(fh, f) != nil {
			s.rejected.Add(1)
			return
		}
	}
}
func (h *frameHost) close() {
	h.once.Do(func() {
		close(h.done)
		_ = h.conn.Close()
		s := h.server
		s.mu.Lock()
		state := s.sessions[h.session]
		var peers []*peer
		if state != nil && state.host == h {
			state.host = nil
			for _, source := range state.pages {
				source.lastSeq = 0
				source.lastBudget = 0
			}
			for id, p := range s.peers {
				if p.scope.Session == h.session {
					delete(s.peers, id)
					peers = append(peers, p)
				}
			}
		}
		s.mu.Unlock()
		for _, p := range peers {
			p.event("closed")
			p.close()
		}
	})
}
func (s *server) publish(h *frameHost, f *frame) error {
	s.mu.Lock()
	state := s.sessions[h.session]
	if state == nil || state.host != h {
		s.mu.Unlock()
		return errScope
	}
	source := state.pages[f.Page]
	if source == nil || source.display != f.Display || source.generation != f.Generation {
		s.rejected.Add(1)
		s.mu.Unlock()
		return nil
	}
	if f.Seq <= source.lastSeq {
		s.mu.Unlock()
		return errInvalid
	}
	source.lastSeq = f.Seq
	s.received.Add(1)
	var peers []*peer
	for _, p := range s.peers {
		if p.scope.Session == h.session && p.scope.Page == f.Page && p.scope.Generation == f.Generation {
			peers = append(peers, p)
		}
	}
	s.mu.Unlock()
	for _, p := range peers {
		p.enqueue(f)
	}
	return nil
}
func (s *server) requestIDR(sc scope, reason string) {
	s.mu.Lock()
	state := s.sessions[sc.Session]
	if state == nil || state.host == nil {
		s.mu.Unlock()
		return
	}
	source := state.pages[sc.Page]
	if source == nil || source.generation != sc.Generation {
		s.mu.Unlock()
		return
	}
	remaining := 500*time.Millisecond - time.Since(source.lastIDR)
	if remaining > 0 {
		if !source.pendingIDR {
			source.pendingIDR = true
			time.AfterFunc(remaining, func() { s.mu.Lock(); source.pendingIDR = false; s.mu.Unlock(); s.requestIDR(sc, reason) })
		}
		s.mu.Unlock()
		return
	}
	source.lastIDR = time.Now()
	host := state.host
	display := source.display
	s.mu.Unlock()
	if !send(host.queue, host.done, map[string]any{"type": "request_idr", "session": sc.Session, "page": sc.Page, "display_id": display, "generation": sc.Generation, "reason": reason}) {
		go host.close()
	}
}
func (s *server) removePeer(id string) {
	s.mu.Lock()
	p := s.peers[id]
	delete(s.peers, id)
	s.mu.Unlock()
	if p != nil {
		p.close()
		s.updateEncoderBudget(p.scope)
	}
}

func (s *server) updateEncoderBudget(sc scope) {
	if s.cfg.Network != "wan" {
		return
	}
	s.mu.Lock()
	state := s.sessions[sc.Session]
	if state == nil || state.host == nil {
		s.mu.Unlock()
		return
	}
	source := state.pages[sc.Page]
	if source == nil || source.generation != sc.Generation {
		s.mu.Unlock()
		return
	}
	budget := int64(12_000_000)
	found := false
	for _, p := range s.peers {
		if p.scope.Session == sc.Session && p.scope.Page == sc.Page && p.scope.Generation == sc.Generation {
			found = true
			if p.targetBitrate.Load() < budget {
				budget = p.targetBitrate.Load()
			}
		}
	}
	if budget < 250_000 {
		budget = 250_000
	}
	if !found || budget == source.lastBudget {
		s.mu.Unlock()
		return
	}
	source.lastBudget = budget
	host := state.host
	display := source.display
	s.mu.Unlock()
	if !send(host.queue, host.done, map[string]any{"type": "encoder_config", "session": sc.Session, "page": sc.Page, "display_id": display, "generation": sc.Generation, "bitrate_kbps": budget / 1000, "fps": 60, "cbr": true}) {
		go host.close()
	}
}
func randomID() string { var b [24]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }
func (s *server) diagnosticsLoop() {
	timer := time.NewTicker(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-timer.C:
			s.mu.Lock()
			rows := make([]any, 0, len(s.peers))
			for _, p := range s.peers {
				hash := sha256.Sum256([]byte(p.scope.Session + "\x00" + p.scope.Page + "\x00" + p.scope.View))
				rows = append(rows, map[string]any{"scope_hash": hex.EncodeToString(hash[:6]), "stats": p.stats()})
			}
			count := len(s.sessions)
			s.mu.Unlock()
			value := map[string]any{"active_sessions": count, "peers": rows, "frames_received": s.received.Load(), "frames_rejected": s.rejected.Load()}
			b, _ := json.Marshal(value)
			path := filepath.Join(s.cfg.Dir, "diagnostics.json.tmp")
			if os.WriteFile(path, b, 0600) == nil {
				_ = os.Rename(path, filepath.Join(s.cfg.Dir, "diagnostics.json"))
			}
		}
	}
}
