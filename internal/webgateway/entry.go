// Package webgateway provides the shared transport entry for the future managed
// website gateway. It does not register application routes or grant authority.
package webgateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Config is administrator-owned. TURNAddress must be the fixed private Pogo
// plaintext shared listener, never an address supplied by an application/model.
type Config struct {
	TURNAddress    string
	TLSConfig      *tls.Config
	ProbeTimeout   time.Duration
	DialTimeout    time.Duration
	MaxConnections int
	MaxProbes      int
	QueueSize      int
}

func (c Config) normalized() (Config, error) {
	host, port, err := net.SplitHostPort(c.TURNAddress)
	if err != nil || host == "" {
		return c, errors.New("gateway: fixed private TURN address is required")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return c, errors.New("gateway: invalid TURN port")
	}
	if c.ProbeTimeout < 0 || c.DialTimeout < 0 || c.MaxConnections < 0 || c.MaxProbes < 0 || c.QueueSize < 0 {
		return c, errors.New("gateway: limits must be positive")
	}
	if c.ProbeTimeout == 0 {
		c.ProbeTimeout = 5 * time.Second
	}
	if c.DialTimeout == 0 {
		c.DialTimeout = 3 * time.Second
	}
	if c.MaxConnections == 0 {
		c.MaxConnections = 256
	}
	if c.MaxProbes == 0 {
		c.MaxProbes = 32
	}
	if c.QueueSize == 0 {
		c.QueueSize = 64
	}
	return c, nil
}

// Entry splits decrypted HTTP and initial TURN/STUN requests on one listener.
// Pogo retains TURN credentials, peer restrictions, allocation and revocation.
// Each TURN client receives a separate upstream TCP connection, without a PROXY
// header. The entry is an opt-in transport foundation, not a deployed gateway.
type Entry struct {
	base      net.Listener
	cfg       Config
	http      *httpListener
	queue     chan *bufferedConn
	probes    chan struct{}
	done      chan struct{}
	ctx       context.Context
	cancel    context.CancelFunc
	once      sync.Once
	wg        sync.WaitGroup
	mu        sync.Mutex
	closed    bool
	clients   map[*bufferedConn]struct{}
	upstreams map[net.Conn]struct{}
}

// New takes listener ownership only on success. TLS terminates before protocol
// probing, including for TURN clients which do not advertise ALPN.
func New(listener net.Listener, cfg Config) (*Entry, error) {
	if listener == nil {
		return nil, errors.New("gateway: listener is required")
	}
	cfg, err := cfg.normalized()
	if err != nil {
		return nil, err
	}
	if cfg.TLSConfig != nil {
		config := cfg.TLSConfig.Clone()
		config.NextProtos = []string{"http/1.1", "stun.turn"}
		listener = tls.NewListener(listener, config)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Entry{
		base: listener, cfg: cfg, queue: make(chan *bufferedConn, cfg.QueueSize),
		probes: make(chan struct{}, cfg.MaxProbes), done: make(chan struct{}),
		ctx: ctx, cancel: cancel, clients: make(map[*bufferedConn]struct{}),
		upstreams: make(map[net.Conn]struct{}),
	}
	e.http = &httpListener{entry: e}
	e.wg.Add(1)
	go e.accept()
	return e, nil
}

func (e *Entry) HTTPListener() net.Listener { return e.http }

func (e *Entry) accept() {
	defer e.wg.Done()
	var retryDelay time.Duration
	for {
		conn, err := e.base.Accept()
		if err != nil {
			var temporary net.Error
			if errors.As(err, &temporary) && temporary.Temporary() {
				if retryDelay == 0 {
					retryDelay = 5 * time.Millisecond
				} else {
					retryDelay = min(retryDelay*2, time.Second)
				}
				timer := time.NewTimer(retryDelay)
				select {
				case <-timer.C:
					continue
				case <-e.done:
					timer.Stop()
					return
				}
			}
			e.stop()
			return
		}
		retryDelay = 0
		c := &bufferedConn{Conn: conn, reader: bufio.NewReaderSize(conn, 4096), entry: e}
		e.mu.Lock()
		if e.closed || len(e.clients) >= e.cfg.MaxConnections {
			e.mu.Unlock()
			_ = conn.Close()
			continue
		}
		e.clients[c] = struct{}{}
		e.mu.Unlock()
		select {
		case e.probes <- struct{}{}:
			e.wg.Add(1)
			go e.classify(c)
		default:
			_ = c.Close()
		}
	}
}

func (e *Entry) classify(c *bufferedConn) {
	defer e.wg.Done()
	// Release a probe slot once classified; long-lived TURN/HTTP connections
	// remain charged against MaxConnections rather than the slow-probe budget.
	probeHeld := true
	release := func() {
		if probeHeld {
			<-e.probes
			probeHeld = false
		}
	}
	defer release()
	_ = c.SetDeadline(time.Now().Add(e.cfg.ProbeTimeout))
	prefix, err := c.reader.Peek(8)
	if err != nil {
		_ = c.Close()
		return
	}
	isHTTP := httpPrefix(prefix)
	if !isHTTP {
		header, err := c.reader.Peek(20)
		if err != nil || !initialTURN(header) {
			_ = c.Close()
			return
		}
	}
	if tc, ok := c.Conn.(*tls.Conn); ok {
		state := tc.ConnectionState()
		c.tlsState = &state
		if (isHTTP && state.NegotiatedProtocol == "stun.turn") || (!isHTTP && state.NegotiatedProtocol == "http/1.1") {
			_ = c.Close()
			return
		}
	}
	_ = c.SetDeadline(time.Time{})
	if isHTTP {
		release()
		select {
		case e.queue <- c:
		default:
			_ = c.Close()
		}
		return
	}
	e.tunnel(c, release)
}

func httpPrefix(prefix []byte) bool {
	for _, method := range []string{"GET ", "HEAD ", "POST ", "PUT ", "PATCH ", "DELETE ", "OPTIONS ", "CONNECT ", "TRACE "} {
		if bytes.HasPrefix(prefix, []byte(method)) {
			return true
		}
	}
	return false
}

func initialTURN(header []byte) bool {
	if len(header) < 20 {
		return false
	}
	kind := binary.BigEndian.Uint16(header[:2])
	length := int(binary.BigEndian.Uint16(header[2:4]))
	return (kind == 0x0001 || kind == 0x0003) && length%4 == 0 && length+20 <= 16384 &&
		binary.BigEndian.Uint32(header[4:8]) == 0x2112a442
}

func (e *Entry) tunnel(client *bufferedConn, releaseProbe func()) {
	dialer := net.Dialer{Timeout: e.cfg.DialTimeout}
	upstream, err := dialer.DialContext(e.ctx, "tcp", e.cfg.TURNAddress)
	if err != nil {
		_ = client.Close()
		return
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		_ = upstream.Close()
		_ = client.Close()
		return
	}
	e.upstreams[upstream] = struct{}{}
	e.mu.Unlock()
	// Include pending upstream dials in the preparation budget. A failed Pogo
	// upstream must not let a burst of valid prefixes fill all client slots
	// with simultaneous dials after bypassing MaxProbes.
	releaseProbe()
	defer func() {
		_ = upstream.Close()
		_ = client.Close()
		e.mu.Lock()
		delete(e.upstreams, upstream)
		e.mu.Unlock()
	}()
	copied := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, client); copied <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstream); copied <- struct{}{} }()
	<-copied
	// An upstream revoke/EOF must terminate the external connection promptly;
	// waiting for its next write would leave a browser media socket behind.
	_ = upstream.Close()
	_ = client.Close()
	<-copied
}

func (e *Entry) stop() {
	e.once.Do(func() {
		e.mu.Lock()
		e.closed = true
		clients := make([]net.Conn, 0, len(e.clients)+len(e.upstreams))
		for c := range e.clients {
			clients = append(clients, c)
		}
		for c := range e.upstreams {
			clients = append(clients, c)
		}
		e.mu.Unlock()
		close(e.done)
		e.cancel()
		_ = e.base.Close()
		for _, c := range clients {
			_ = c.Close()
		}
	})
}

// Close closes queued, probing, HTTP and TURN connections and joins workers.
func (e *Entry) Close() error {
	e.stop()
	e.wg.Wait()
	return nil
}

type bufferedConn struct {
	net.Conn
	reader   *bufio.Reader
	entry    *Entry
	tlsState *tls.ConnectionState
	once     sync.Once
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
func (c *bufferedConn) Close() error {
	_ = c.Conn.SetDeadline(time.Now())
	err := c.Conn.Close()
	c.once.Do(func() {
		c.entry.mu.Lock()
		delete(c.entry.clients, c)
		c.entry.mu.Unlock()
	})
	return err
}

type httpListener struct{ entry *Entry }

func (l *httpListener) Accept() (net.Conn, error) {
	select {
	case <-l.entry.done:
		return nil, net.ErrClosed
	case c := <-l.entry.queue:
		select {
		case <-l.entry.done:
			_ = c.Close()
			return nil, net.ErrClosed
		default:
			return c, nil
		}
	}
}
func (l *httpListener) Close() error   { return l.entry.Close() }
func (l *httpListener) Addr() net.Addr { return l.entry.base.Addr() }

type tlsStateKey struct{}

// ConnContext and HTTPHandler must be installed together on the HTTP server:
// buffering hides *tls.Conn from net/http's direct type assertion.
func (e *Entry) ConnContext(ctx context.Context, conn net.Conn) context.Context {
	if c, ok := conn.(*bufferedConn); ok && c.tlsState != nil {
		return context.WithValue(ctx, tlsStateKey{}, c.tlsState)
	}
	return ctx
}

func (e *Entry) HTTPHandler(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if state, ok := r.Context().Value(tlsStateKey{}).(*tls.ConnectionState); ok {
			r.TLS = state
		}
		next.ServeHTTP(&entryWriter{ResponseWriter: w}, r)
	})
}

// A proxy may read the hijacked connection rather than the bufio.ReadWriter
// returned by net/http. Keep bytes already read ahead with that connection,
// otherwise an Upgrade payload coalesced with its headers is stranded.
type entryWriter struct{ http.ResponseWriter }

func (w *entryWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *entryWriter) Flush() {
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *entryWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, buffer, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err != nil {
		return nil, nil, err
	}
	// Detach the read-ahead bytes. WebSocket libraries may Reset the returned
	// reader onto this connection; sharing that reader would then recurse.
	pending := make([]byte, buffer.Reader.Buffered())
	if _, err := io.ReadFull(buffer.Reader, pending); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	wrapped := &hijackedConn{Conn: conn, reader: io.MultiReader(bytes.NewReader(pending), conn)}
	buffer.Reader.Reset(wrapped)
	return wrapped, buffer, nil
}

type hijackedConn struct {
	net.Conn
	reader io.Reader
}

func (c *hijackedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
