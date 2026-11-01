package browserrelay

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

type childListener struct {
	parent  *Service
	queue   chan *clientConn
	done    chan struct{}
	once    sync.Once
	queueMu sync.Mutex
}

func (l *childListener) Accept() (net.Conn, error) {
	select {
	case <-l.done:
		return nil, net.ErrClosed
	default:
	}
	select {
	case c := <-l.queue:
		select {
		case <-l.done:
			_ = c.Close()
			return nil, net.ErrClosed
		default:
			return c, nil
		}
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *childListener) Close() error {
	l.once.Do(func() {
		l.queueMu.Lock()
		defer l.queueMu.Unlock()
		close(l.done)
		for {
			select {
			case c := <-l.queue:
				_ = c.Close()
			default:
				return
			}
		}
	})
	return nil
}

func (l *childListener) enqueue(c *clientConn) {
	l.queueMu.Lock()
	defer l.queueMu.Unlock()
	select {
	case <-l.done:
		_ = c.Close()
	default:
		select {
		case l.queue <- c:
		default:
			_ = c.Close()
		}
	}
}

func (l *childListener) Addr() net.Addr { return l.parent.base.Addr() }

type clientConn struct {
	net.Conn
	service *Service
	reader  *bufio.Reader
	once    sync.Once
	writeMu sync.Mutex
	created time.Time
	turn    bool
	grant   *grant
	pending []byte
}

func (c *clientConn) Close() error {
	var err error
	c.once.Do(func() {
		err = c.Conn.Close()
		c.service.connectionClosed(c)
	})
	return err
}

func (c *clientConn) Read(p []byte) (int, error) {
	if !c.turn {
		return c.reader.Read(p)
	}
	if len(p) == 0 {
		return 0, nil
	}
	if len(c.pending) == 0 {
		deadline := time.Now().Add(c.service.cfg.IdleTimeout)
		c.service.mu.Lock()
		g := c.grant
		if g == nil {
			deadline = c.created.Add(c.service.cfg.CredentialTTL)
		} else if g.leaseUntil.Before(deadline) {
			deadline = g.leaseUntil
		}
		c.service.mu.Unlock()
		if err := c.Conn.SetReadDeadline(deadline); err != nil {
			return 0, err
		}
		frame, err := readTURNFrame(c.reader, c.service.cfg.MaxFrameBytes)
		if err != nil {
			return 0, err
		}
		c.pending = frame
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

func (c *clientConn) Write(p []byte) (int, error) {
	if !c.turn {
		return c.Conn.Write(p)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.Conn.SetWriteDeadline(time.Now().Add(c.service.cfg.WriteTimeout)); err != nil {
		return 0, err
	}
	return c.Conn.Write(p)
}

func readTURNFrame(r io.Reader, maximum int) ([]byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, err
	}
	length := int(binary.BigEndian.Uint16(header[2:4]))
	stun := header[0]&0xc0 == 0
	if stun {
		if length%4 != 0 || length+20 >= maximum {
			return nil, errInvalidFrame
		}
		header = append(header, make([]byte, 16)...)
		if _, err := io.ReadFull(r, header[4:]); err != nil {
			return nil, err
		}
		if binary.BigEndian.Uint32(header[4:8]) != 0x2112a442 {
			return nil, errInvalidFrame
		}
	} else if header[0]&0xc0 == 0x40 {
		length = (length + 3) &^ 3
		if length+4 >= maximum {
			return nil, errInvalidFrame
		}
	} else {
		return nil, errInvalidFrame
	}
	frame := append(header, make([]byte, length)...)
	_, err := io.ReadFull(r, frame[len(header):])
	return frame, err
}

var errInvalidFrame = errors.New("invalid or oversized TURN frame")

func firstTURNRequest(header []byte, max int) bool {
	if len(header) != 20 {
		return false
	}
	kind := binary.BigEndian.Uint16(header[:2])
	size := int(binary.BigEndian.Uint16(header[2:4]))
	return (kind == 0x0001 || kind == 0x0003) && size%4 == 0 && size+20 < max && binary.BigEndian.Uint32(header[4:8]) == 0x2112a442
}

func (s *Service) acceptLoop() {
	defer close(s.acceptDone)
	for {
		conn, err := s.base.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed || s.draining || len(s.connections) >= s.cfg.MaxConnections {
			s.mu.Unlock()
			_ = conn.Close()
			continue
		}
		c := &clientConn{Conn: conn, service: s, reader: bufio.NewReaderSize(conn, s.cfg.MaxFrameBytes), created: time.Now()}
		s.connections[c] = struct{}{}
		s.mu.Unlock()
		select {
		case s.probes <- struct{}{}:
			s.probeWG.Add(1)
			go s.classify(c)
		default:
			_ = c.Close()
		}
	}
}

func (s *Service) classify(c *clientConn) {
	defer s.probeWG.Done()
	defer func() { <-s.probes }()
	if err := c.SetReadDeadline(time.Now().Add(s.cfg.ProbeTimeout)); err != nil {
		_ = c.Close()
		return
	}
	first, err := c.reader.Peek(1)
	if err != nil {
		_ = c.Close()
		return
	}
	isTURN := false
	if first[0] == 0 {
		header, err := c.reader.Peek(20)
		if err != nil || !firstTURNRequest(header, s.cfg.MaxFrameBytes) {
			_ = c.Close()
			return
		}
		isTURN = true
	}
	if err := c.SetReadDeadline(time.Time{}); err != nil {
		_ = c.Close()
		return
	}
	listener := s.http
	if isTURN {
		listener = s.turnListener
		s.mu.Lock()
		if len(s.turnConnections) >= s.cfg.MaxTURNConnections || s.closed || s.draining {
			s.mu.Unlock()
			_ = c.Close()
			return
		}
		c.turn = true
		s.turnConnections[remoteKey(c.RemoteAddr())] = c
		s.mu.Unlock()
	}
	listener.enqueue(c)
}

type tlsContextKey struct{}

// ConnContext and HTTPHandler preserve Request.TLS when the supplied base
// listener is already TLS-terminated. Use both with http.Server.Serve.
func (s *Service) ConnContext(ctx context.Context, conn net.Conn) context.Context {
	if c, ok := conn.(*clientConn); ok {
		if tlsConn, ok := c.Conn.(*tls.Conn); ok {
			state := tlsConn.ConnectionState()
			return context.WithValue(ctx, tlsContextKey{}, &state)
		}
	}
	return ctx
}

func (s *Service) HTTPHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if state, ok := r.Context().Value(tlsContextKey{}).(*tls.ConnectionState); ok {
			r.TLS = state
		}
		next.ServeHTTP(w, r)
	})
}
