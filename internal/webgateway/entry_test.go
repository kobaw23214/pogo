package webgateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

const testTimeout = 3 * time.Second

func newTestEntry(t *testing.T, cfg Config, handler http.Handler) (*Entry, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return serveTestEntry(t, ln, cfg, handler)
}

func serveTestEntry(t *testing.T, ln net.Listener, cfg Config, handler http.Handler) (*Entry, string) {
	t.Helper()
	if cfg.TURNAddress == "" {
		cfg.TURNAddress, _ = newTestUpstream(t, func(c net.Conn) { io.Copy(io.Discard, c) })
	}
	entry, err := New(ln, cfg)
	if err != nil {
		ln.Close()
		t.Fatal(err)
	}
	server := &http.Server{Handler: entry.HTTPHandler(handler), ConnContext: entry.ConnContext}
	done := make(chan error, 1)
	go func() { done <- server.Serve(entry.HTTPListener()) }()
	t.Cleanup(func() {
		entry.Close()
		server.Close()
		select {
		case <-done:
		case <-time.After(testTimeout):
			t.Error("HTTP listener did not stop after entry close")
		}
	})
	return entry, ln.Addr().String()
}

func dialTest(t *testing.T, address string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", address, testTimeout)
	if err != nil {
		t.Fatal(err)
	}
	c.SetDeadline(time.Now().Add(testTimeout))
	t.Cleanup(func() { c.Close() })
	return c
}

func readExact(t *testing.T, c net.Conn, want []byte) {
	t.Helper()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("read transparent bytes: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("received %x, want %x", got, want)
	}
}

func writeExact(t *testing.T, c net.Conn, data []byte) {
	t.Helper()
	if _, err := c.Write(data); err != nil {
		t.Fatal(err)
	}
}

// This fixture accepts real TCP connections. All accepted sockets are tracked
// so a failing assertion cannot leave an upstream goroutine behind.
func newTestUpstream(t *testing.T, handle func(net.Conn)) (string, <-chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan struct{}, 32)
	var mu sync.Mutex
	connections := make(map[net.Conn]struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			connections[c] = struct{}{}
			mu.Unlock()
			accepted <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer c.Close()
				defer func() {
					mu.Lock()
					delete(connections, c)
					mu.Unlock()
				}()
				c.SetDeadline(time.Now().Add(testTimeout))
				handle(c)
			}()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		for c := range connections {
			c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return ln.Addr().String(), accepted
}

func waitAccepted(t *testing.T, accepted <-chan struct{}) {
	t.Helper()
	select {
	case <-accepted:
	case <-time.After(testTimeout):
		t.Fatal("TURN upstream did not receive a connection")
	}
}

func stunFrame(messageType uint16, marker byte) []byte {
	frame := make([]byte, 28)
	binary.BigEndian.PutUint16(frame[0:2], messageType)
	binary.BigEndian.PutUint16(frame[2:4], 8)
	binary.BigEndian.PutUint32(frame[4:8], 0x2112a442)
	for i := 8; i < 20; i++ {
		frame[i] = marker + byte(i)
	}
	// REQUESTED-TRANSPORT: UDP. The mux must forward even the transaction ID
	// and attribute padding unchanged; authentication remains upstream's job.
	copy(frame[20:], []byte{0, 0x19, 0, 4, 17, 0, 0, 0})
	return frame
}

func testCertificate() tls.Certificate {
	s := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	certificate := s.TLS.Certificates[0]
	s.Close()
	return certificate
}

func TestEntryHTTPPreservesBytesHostOriginAndKeepAlive(t *testing.T) {
	_, address := newTestEntry(t, Config{}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "%s|%s|%s|%s|%s", r.Method, r.URL.RequestURI(), r.Host, r.Header.Get("Origin"), body)
	}))
	c := dialTest(t, address)
	reader := bufio.NewReader(c)
	for i := 0; i < 2; i++ {
		body := fmt.Sprintf("payload-%d", i)
		request := fmt.Sprintf("POST /echo?q=%d HTTP/1.1\r\nHost: pogo.example.test:19770\r\nOrigin: https://pogo.example.test:19770\r\nContent-Length: %d\r\n\r\n%s", i, len(body), body)
		writeExact(t, c, []byte(request))
		response, err := http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("POST|/echo?q=%d|pogo.example.test:19770|https://pogo.example.test:19770|%s", i, body)
		if response.StatusCode != http.StatusOK || string(got) != want {
			t.Fatalf("response = %d %q, want 200 %q", response.StatusCode, got, want)
		}
	}
}

func TestEntryTLSHTTPPreservesTLSStateWithoutMutatingConfiguration(t *testing.T) {
	protocols := []string{"existing-protocol", "unused-a", "unused-b"}
	before := append([]string(nil), protocols...)
	cfg := &tls.Config{Certificates: []tls.Certificate{testCertificate()}, MinVersion: tls.VersionTLS12, NextProtos: protocols[:1]}
	states := make(chan tls.ConnectionState, 1)
	_, address := newTestEntry(t, Config{TLSConfig: cfg}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			http.Error(w, "missing request TLS state", http.StatusInternalServerError)
			return
		}
		states <- *r.TLS
		io.WriteString(w, "tls-http")
	}))
	c, err := tls.DialWithDialer(&net.Dialer{Timeout: testTimeout}, "tcp", address, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}}) // Local httptest certificate only.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.SetDeadline(time.Now().Add(testTimeout))
	writeExact(t, c, []byte("GET / HTTP/1.1\r\nHost: pogo.example.test\r\nConnection: close\r\n\r\n"))
	response, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || string(body) != "tls-http" {
		t.Fatalf("TLS response = %d %q", response.StatusCode, body)
	}
	select {
	case state := <-states:
		if !state.HandshakeComplete || state.Version < tls.VersionTLS12 || state.NegotiatedProtocol != "http/1.1" {
			t.Fatalf("invalid request TLS state: %+v", state)
		}
	case <-time.After(testTimeout):
		t.Fatal("no request TLS state observed")
	}
	if cfg.MinVersion != tls.VersionTLS12 || !reflect.DeepEqual(cfg.NextProtos, []string{"existing-protocol"}) || !reflect.DeepEqual(protocols, before) {
		t.Fatalf("caller TLS configuration was modified: config protocols=%q backing=%q", cfg.NextProtos, protocols)
	}
}

func TestEntryTURNForwardsExactBytesBidirectionally(t *testing.T) {
	first := append(stunFrame(0x0003, 1), []byte("coalesced-client-tail")...)
	second := []byte("client-after-upstream-push")
	push := []byte("upstream-push-before-next-request")
	observed := make(chan []byte, 2)
	upstream, _ := newTestUpstream(t, func(c net.Conn) {
		got := make([]byte, len(first))
		if _, err := io.ReadFull(c, got); err != nil {
			return
		}
		observed <- got
		if _, err := c.Write(push); err != nil {
			return
		}
		got = make([]byte, len(second))
		if _, err := io.ReadFull(c, got); err != nil {
			return
		}
		observed <- got
		c.Write([]byte("ack"))
	})
	_, address := newTestEntry(t, Config{TURNAddress: upstream}, http.NotFoundHandler())
	c := dialTest(t, address)
	writeExact(t, c, first)
	readExact(t, c, push)
	writeExact(t, c, second)
	readExact(t, c, []byte("ack"))
	for _, want := range [][]byte{first, second} {
		select {
		case got := <-observed:
			if !bytes.Equal(got, want) {
				t.Fatalf("upstream received %x, want %x", got, want)
			}
		case <-time.After(testTimeout):
			t.Fatal("missing upstream bytes")
		}
	}
}

func TestEntryTLSTURNSupportsAbsentAndExplicitALPN(t *testing.T) {
	for _, protocol := range []string{"", "stun.turn"} {
		t.Run(fmt.Sprintf("alpn=%q", protocol), func(t *testing.T) {
			upstream, _ := newTestUpstream(t, func(c net.Conn) { io.Copy(c, c) })
			_, address := newTestEntry(t, Config{TURNAddress: upstream, TLSConfig: &tls.Config{Certificates: []tls.Certificate{testCertificate()}}}, http.NotFoundHandler())
			clientCfg := &tls.Config{InsecureSkipVerify: true} // Local httptest certificate only.
			if protocol != "" {
				clientCfg.NextProtos = []string{protocol}
			}
			c, err := tls.DialWithDialer(&net.Dialer{Timeout: testTimeout}, "tcp", address, clientCfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { c.Close() })
			c.SetDeadline(time.Now().Add(testTimeout))
			if c.ConnectionState().NegotiatedProtocol != protocol {
				t.Fatalf("negotiated %q, want %q", c.ConnectionState().NegotiatedProtocol, protocol)
			}
			frame := stunFrame(0x0001, 17)
			writeExact(t, c, frame)
			readExact(t, c, frame)
		})
	}
}

func TestEntryTURNConnectionsRemainIndependent(t *testing.T) {
	upstream, accepted := newTestUpstream(t, func(c net.Conn) { io.Copy(c, c) })
	_, address := newTestEntry(t, Config{TURNAddress: upstream}, http.NotFoundHandler())
	first, second := dialTest(t, address), dialTest(t, address)
	for i, c := range []net.Conn{first, second} {
		frame := stunFrame(0x0003, byte(30+i))
		writeExact(t, c, frame)
		readExact(t, c, frame)
		waitAccepted(t, accepted)
	}
	first.Close()
	writeExact(t, second, []byte("still-second"))
	readExact(t, second, []byte("still-second"))
}

func TestEntryUpstreamClosurePropagates(t *testing.T) {
	upstream, _ := newTestUpstream(t, func(c net.Conn) {
		header := make([]byte, 28)
		io.ReadFull(c, header)
	})
	_, address := newTestEntry(t, Config{TURNAddress: upstream}, http.NotFoundHandler())
	c := dialTest(t, address)
	writeExact(t, c, stunFrame(0x0003, 0))
	var b [1]byte
	if _, err := c.Read(b[:]); err == nil {
		t.Fatal("client stayed open after upstream closed")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("upstream closure did not propagate before deadline")
	}
}

func TestEntryMalformedTURNNeverDialsUpstream(t *testing.T) {
	upstream, accepted := newTestUpstream(t, func(c net.Conn) { io.Copy(io.Discard, c) })
	_, address := newTestEntry(t, Config{TURNAddress: upstream, ProbeTimeout: 80 * time.Millisecond}, http.NotFoundHandler())
	badType := stunFrame(0x0002, 0)
	badCookie := stunFrame(0x0003, 0)
	badCookie[4] ^= 0xff
	unaligned := stunFrame(0x0003, 0)
	binary.BigEndian.PutUint16(unaligned[2:4], 3)
	oversized := stunFrame(0x0003, 0)
	binary.BigEndian.PutUint16(oversized[2:4], 16388)
	for name, input := range map[string][]byte{"type": badType, "cookie": badCookie, "length_alignment": unaligned, "oversized": oversized, "truncated": stunFrame(0x0003, 0)[:11]} {
		t.Run(name, func(t *testing.T) {
			c := dialTest(t, address)
			writeExact(t, c, input)
			if tcp, ok := c.(*net.TCPConn); ok {
				tcp.CloseWrite()
			}
			c.SetReadDeadline(time.Now().Add(time.Second))
			io.Copy(io.Discard, c)
			c.Close()
			select {
			case <-accepted:
				t.Fatal("invalid first TURN frame caused an upstream dial")
			default:
			}
		})
	}
}

func TestEntrySlowProbeTimesOut(t *testing.T) {
	_, address := newTestEntry(t, Config{ProbeTimeout: 50 * time.Millisecond}, http.NotFoundHandler())
	c := dialTest(t, address)
	c.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	if _, err := c.Read(b[:]); err == nil {
		t.Fatal("silent probe was not closed")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("silent probe remained open beyond its configured timeout")
	}
}

func TestEntryCloseReleasesPendingQueuedAndTURNConnections(t *testing.T) {
	upstream, accepted := newTestUpstream(t, func(c net.Conn) { io.Copy(io.Discard, c) })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := New(ln, Config{TURNAddress: upstream, ProbeTimeout: 2 * time.Second, MaxConnections: 8, MaxProbes: 4, QueueSize: 2})
	if err != nil {
		ln.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { entry.Close() })
	address := ln.Addr().String()
	turn := dialTest(t, address)
	writeExact(t, turn, stunFrame(0x0003, 0))
	waitAccepted(t, accepted)
	queued := dialTest(t, address)
	writeExact(t, queued, []byte("GET /queued HTTP/1.1\r\nHost: pogo.test\r\n\r\n"))
	pending := dialTest(t, address)
	done := make(chan error, 1)
	go func() { done <- entry.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close blocked on a pending or queued connection")
	}
	for name, c := range map[string]net.Conn{"turn": turn, "queued_http": queued, "pending_probe": pending} {
		c.SetReadDeadline(time.Now().Add(time.Second))
		var b [1]byte
		if _, err := c.Read(b[:]); err == nil {
			t.Errorf("%s connection remained open", name)
		} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
			t.Errorf("%s connection was not released before deadline", name)
		}
	}
	if c, err := entry.HTTPListener().Accept(); err == nil {
		c.Close()
		t.Fatal("HTTP listener accepted a queued connection after Close")
	}
	if c, err := net.DialTimeout("tcp", address, time.Second); err == nil {
		c.Close()
		t.Fatal("entry close did not close its owned listener")
	}
}

func TestEntryCloseReleasesServedHTTPConnection(t *testing.T) {
	entry, address := newTestEntry(t, Config{}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "keep-alive")
	}))
	c := dialTest(t, address)
	writeExact(t, c, []byte("GET / HTTP/1.1\r\nHost: pogo.test\r\n\r\n"))
	response, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if err := entry.Close(); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	if _, err := c.Read(b[:]); err == nil {
		t.Fatal("served keep-alive connection remained open after entry close")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("entry did not release an HTTPServer-owned connection")
	}
}

func TestEntryConnectionLimitRejectsExcessAndRecovers(t *testing.T) {
	upstream, accepted := newTestUpstream(t, func(c net.Conn) {
		frame := make([]byte, 28)
		if _, err := io.ReadFull(c, frame); err != nil {
			return
		}
		c.Write([]byte("ready"))
		io.Copy(io.Discard, c)
	})
	_, address := newTestEntry(t, Config{TURNAddress: upstream, MaxConnections: 2, MaxProbes: 2, QueueSize: 2}, http.NotFoundHandler())
	open := func(marker byte) net.Conn {
		c := dialTest(t, address)
		writeExact(t, c, stunFrame(0x0003, marker))
		readExact(t, c, []byte("ready"))
		waitAccepted(t, accepted)
		return c
	}
	first := open(1)
	_ = open(2)
	excess := dialTest(t, address)
	excess.Write(stunFrame(0x0003, 3))
	excess.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	if _, err := excess.Read(b[:]); err == nil {
		t.Fatal("excess connection was admitted")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("excess connection was left waiting without a bounded rejection")
	}
	select {
	case <-accepted:
		t.Fatal("excess connection reached TURN upstream")
	default:
	}
	first.Close()
	// Wait for the released slot by making an HTTP request. It cannot reach the
	// TURN fixture, and confirms capacity recovery without inspecting internals.
	deadline := time.Now().Add(time.Second)
	for {
		c, err := net.DialTimeout("tcp", address, time.Second)
		if err == nil {
			c.SetDeadline(time.Now().Add(100 * time.Millisecond))
			fmt.Fprint(c, "GET / HTTP/1.1\r\nHost: pogo.test\r\nConnection: close\r\n\r\n")
			response, readErr := http.ReadResponse(bufio.NewReader(c), nil)
			if readErr == nil {
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
				c.Close()
				break
			}
			c.Close()
		}
		if time.Now().After(deadline) {
			t.Fatal("connection capacity did not recover after a client closed")
		}
		time.Sleep(time.Millisecond)
	}
}

type temporaryAcceptError struct{}

func (temporaryAcceptError) Error() string   { return "temporary accept fixture" }
func (temporaryAcceptError) Timeout() bool   { return false }
func (temporaryAcceptError) Temporary() bool { return true }

type temporaryAcceptListener struct {
	net.Listener
	once sync.Once
}

func (ln *temporaryAcceptListener) Accept() (net.Conn, error) {
	first := false
	ln.once.Do(func() { first = true })
	if first {
		return nil, temporaryAcceptError{}
	}
	return ln.Listener.Accept()
}

func TestEntryTemporaryAcceptFailureDoesNotDisableHTTP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, address := serveTestEntry(t, &temporaryAcceptListener{Listener: ln}, Config{}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "after-temporary-error")
	}))
	c := dialTest(t, address)
	writeExact(t, c, []byte("GET / HTTP/1.1\r\nHost: pogo.test\r\nConnection: close\r\n\r\n"))
	response, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || string(body) != "after-temporary-error" {
		t.Fatalf("HTTP after temporary Accept failure = %d %q", response.StatusCode, body)
	}
}

func TestEntryInvalidConfigLeavesListenerWithCaller(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	if entry, err := New(ln, Config{}); err == nil {
		entry.Close()
		t.Fatal("missing fixed TURN upstream was accepted")
	}
	accepted := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			defer c.Close()
			c.SetDeadline(time.Now().Add(testTimeout))
			got := make([]byte, 4)
			_, err = io.ReadFull(c, got)
			if err == nil && string(got) != "ping" {
				err = fmt.Errorf("listener received %q", got)
			}
		}
		accepted <- err
	}()
	c := dialTest(t, ln.Addr().String())
	writeExact(t, c, []byte("ping"))
	select {
	case err := <-accepted:
		if err != nil {
			t.Fatalf("failed construction consumed caller listener: %v", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("caller could not accept after failed construction")
	}
}

func TestEntryWebSocketLibraryHijackSupportsBidirectionalFrames(t *testing.T) {
	messages := []struct {
		kind websocket.MessageType
		data []byte
	}{
		{websocket.MessageText, []byte("first client message")},
		{websocket.MessageBinary, bytes.Repeat([]byte{0, 1, 127, 128, 254, 255}, 16000)},
		{websocket.MessageText, []byte("最后一帧：网页通道正常")},
	}
	serverDone := make(chan error, 1)
	_, address := newTestEntry(t, Config{}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), testTimeout)
		defer cancel()
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			serverDone <- fmt.Errorf("accept websocket: %w", err)
			return
		}
		defer c.CloseNow()
		c.SetReadLimit(512 << 10)
		if err := c.Write(ctx, websocket.MessageText, []byte("server-first")); err != nil {
			serverDone <- fmt.Errorf("server greeting: %w", err)
			return
		}
		for i, want := range messages {
			kind, got, err := c.Read(ctx)
			if err != nil {
				serverDone <- fmt.Errorf("server read frame %d: %w", i, err)
				return
			}
			if kind != want.kind || !bytes.Equal(got, want.data) {
				serverDone <- fmt.Errorf("server frame %d mismatch: kind=%v bytes=%d", i, kind, len(got))
				return
			}
			if err := c.Write(ctx, kind, got); err != nil {
				serverDone <- fmt.Errorf("server echo frame %d: %w", i, err)
				return
			}
		}
		serverDone <- nil
	}))
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws://"+address+"/websocket", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	c.SetReadLimit(512 << 10)
	kind, greeting, err := c.Read(ctx)
	if err != nil || kind != websocket.MessageText || string(greeting) != "server-first" {
		t.Fatalf("server-first frame: kind=%v data=%q error=%v", kind, greeting, err)
	}
	for i, message := range messages {
		if err := c.Write(ctx, message.kind, message.data); err != nil {
			t.Fatalf("client write frame %d: %v", i, err)
		}
		kind, got, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("client read frame %d: %v", i, err)
		}
		if kind != message.kind || !bytes.Equal(got, message.data) {
			t.Fatalf("client frame %d mismatch: kind=%v bytes=%d", i, kind, len(got))
		}
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
