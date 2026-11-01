package browserrelay

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/turn/v5"
)

func newTestService(t *testing.T, worker *net.UDPConn, change func(*Config)) *Service {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(19780)
	if worker != nil {
		port = worker.LocalAddr().(*net.UDPAddr).AddrPort().Port()
	}
	cfg := Config{RelayAddr: netip.MustParseAddr("127.0.0.1"), BindAddr: netip.MustParseAddr("127.0.0.1"), AllowedPeerIPs: []netip.Addr{netip.MustParseAddr("127.0.0.1")}, PeerPortMin: port, PeerPortMax: port, allowLoopback: true}
	if change != nil {
		change(&cfg)
	}
	s, err := New(listener, cfg)
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func udpWorker(t *testing.T) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func turnClient(t *testing.T, s *Service, credentials Credentials) (*turn.Client, net.Conn) {
	t.Helper()
	c, err := net.DialTimeout("tcp4", s.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	factory := logging.NewDefaultLoggerFactory()
	factory.Writer = io.Discard
	client, err := turn.NewClient(&turn.ClientConfig{Conn: turn.NewSTUNConn(c), STUNServerAddr: s.Addr().String(), TURNServerAddr: s.Addr().String(), Username: credentials.Username, Password: credentials.Password, Realm: s.cfg.Realm, RTO: 30 * time.Millisecond, LoggerFactory: factory})
	if err != nil {
		c.Close()
		t.Fatal(err)
	}
	if err := client.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close(); c.Close() })
	return client, c
}

func issueFor(t *testing.T, s *Service, worker *net.UDPConn, scope string) Credentials {
	t.Helper()
	c, err := s.IssuePeers(scope, []netip.AddrPort{worker.LocalAddr().(*net.UDPAddr).AddrPort()})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not complete")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestHTTPBytesKeepAliveAndWebSocketHijackPreserved(t *testing.T) {
	s := newTestService(t, nil, nil)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/socket" {
			c, b, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			defer c.Close()
			b.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
			b.Flush()
			payload := make([]byte, 7)
			if _, err := io.ReadFull(b, payload); err != nil {
				t.Error(err)
				return
			}
			c.Write(payload)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		fmt.Fprintf(w, "%s:%s", r.URL.Path, body)
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	go server.Serve(s.HTTPListener())
	t.Cleanup(func() { server.Close() })
	c, err := net.Dial("tcp4", s.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	b := bufio.NewReader(c)
	for _, path := range []string{"/first", "/second"} {
		request := "POST " + path + " HTTP/1.1\r\nHost: pogo.test\r\nContent-Length: 4\r\n\r\nbody"
		for _, ch := range []byte(request) {
			if _, err := c.Write([]byte{ch}); err != nil {
				t.Fatal(err)
			}
		}
		r, err := http.ReadResponse(b, nil)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if string(data) != path+":body" {
			t.Fatalf("HTTP body changed: %q", data)
		}
	}
	c.Write([]byte("GET /socket HTTP/1.1\r\nHost: pogo.test\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\nWSbytes"))
	r, err := http.ReadResponse(b, nil)
	if err != nil || r.StatusCode != 101 {
		t.Fatalf("upgrade: %v", err)
	}
	got := make([]byte, 7)
	if _, err := io.ReadFull(b, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "WSbytes" {
		t.Fatalf("hijack lost buffered bytes: %q", got)
	}
}

func TestTURNTCPBidirectionalAndExactPeerPort(t *testing.T) {
	worker := udpWorker(t)
	denied := udpWorker(t)
	s := newTestService(t, worker, nil)
	credentials := issueFor(t, s, worker, "view:1")
	client, _ := turnClient(t, s, credentials)
	relay, err := client.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	if _, err = relay.WriteTo([]byte("viewer-to-worker"), worker.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1600)
	worker.SetReadDeadline(time.Now().Add(time.Second))
	n, _, err := worker.ReadFromUDP(buf)
	if err != nil || string(buf[:n]) != "viewer-to-worker" {
		t.Fatalf("outgoing: %d %v", n, err)
	}
	if _, err = worker.WriteToUDP([]byte("worker-to-viewer"), relay.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	relay.SetReadDeadline(time.Now().Add(time.Second))
	n, peer, err := relay.ReadFrom(buf)
	if err != nil || string(buf[:n]) != "worker-to-viewer" || peer.String() != worker.LocalAddr().String() {
		t.Fatalf("incoming: %d %v", n, err)
	}
	// Same IP permissions do not authorize another UDP port in either direction.
	relay.WriteTo([]byte("denied-outbound"), denied.LocalAddr())
	denied.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, _, err := denied.ReadFromUDP(buf); err == nil {
		t.Fatal("another UDP port received relay traffic")
	}
	denied.WriteToUDP([]byte("spoofed-inbound"), relay.LocalAddr().(*net.UDPAddr))
	worker.WriteToUDP([]byte("valid-after-spoof"), relay.LocalAddr().(*net.UDPAddr))
	relay.SetReadDeadline(time.Now().Add(time.Second))
	n, _, err = relay.ReadFrom(buf)
	if err != nil || string(buf[:n]) != "valid-after-spoof" {
		t.Fatalf("source port filtering failed: %q %v", buf[:n], err)
	}
	if s.ActiveAllocations() != 1 {
		t.Fatal("expected one allocation")
	}
	s.RevokeScope("view")
	eventually(t, func() bool { return s.ActiveAllocations() == 0 })
	s.mu.Lock()
	pinned := s.grants[credentials.Username]
	s.mu.Unlock()
	if pinned != nil {
		t.Fatal("revoked credential remained active")
	}
}

func TestBadPasswordCannotPinAndCredentialCannotMoveTCPConnection(t *testing.T) {
	worker := udpWorker(t)
	s := newTestService(t, worker, nil)
	credentials := issueFor(t, s, worker, "view:1")
	bad := credentials
	bad.Password = "wrong"
	client, _ := turnClient(t, s, bad)
	if _, err := client.Allocate(); err == nil {
		t.Fatal("bad authentication allocated")
	}
	s.mu.Lock()
	pin := s.grants[credentials.Username].pinned
	s.mu.Unlock()
	if pin != nil {
		t.Fatal("unverified MESSAGE-INTEGRITY pinned the credential")
	}
	valid, _ := turnClient(t, s, credentials)
	relay, err := valid.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	replay, _ := turnClient(t, s, credentials)
	if _, err := replay.Allocate(); err == nil {
		t.Fatal("same credential allocated on another TCP socket")
	}
	if s.ActiveAllocations() != 1 {
		t.Fatal("replay changed existing allocation")
	}
}

func TestConcurrentCredentialReplayHasExactlyOneTCPWinner(t *testing.T) {
	worker := udpWorker(t)
	s := newTestService(t, worker, nil)
	credentials := issueFor(t, s, worker, "view:1")
	first, _ := turnClient(t, s, credentials)
	second, _ := turnClient(t, s, credentials)
	start := make(chan struct{})
	type allocationResult struct {
		conn net.PacketConn
		err  error
	}
	results := make(chan allocationResult, 2)
	for _, client := range []*turn.Client{first, second} {
		go func() {
			<-start
			conn, err := client.Allocate()
			results <- allocationResult{conn, err}
		}()
	}
	close(start)
	winners := 0
	for range 2 {
		select {
		case result := <-results:
			if result.err == nil {
				winners++
				defer result.conn.Close()
			}
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent allocation did not complete")
		}
	}
	if winners != 1 || s.ActiveAllocations() != 1 {
		t.Fatalf("credential replay allocated %d winning sockets / %d allocations", winners, s.ActiveAllocations())
	}
	s.RevokeScope("view")
	if s.ActiveAllocations() != 0 {
		t.Fatal("replay winner survived view revocation")
	}
}

func TestInitialExpiryDoesNotInterruptActivePermissionRefresh(t *testing.T) {
	worker := udpWorker(t)
	s := newTestService(t, worker, func(c *Config) { c.CredentialTTL = 100 * time.Millisecond; c.ScopeTTL = 2 * time.Second })
	credentials := issueFor(t, s, worker, "view:1")
	client, _ := turnClient(t, s, credentials)
	relay, err := client.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	time.Sleep(150 * time.Millisecond)
	if err := client.CreatePermission(worker.LocalAddr()); err != nil {
		t.Fatalf("active authenticated socket stopped after initial credential expiry: %v", err)
	}
	if _, err := relay.WriteTo([]byte("after-initial-expiry"), worker.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	worker.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 100)
	if _, _, err := worker.ReadFromUDP(buf); err != nil {
		t.Fatal(err)
	}
	unused := issueFor(t, s, worker, "other:1")
	time.Sleep(150 * time.Millisecond)
	expired, _ := turnClient(t, s, unused)
	if _, err := expired.Allocate(); err == nil {
		t.Fatal("unused expired credential was accepted")
	}
}

func TestScopeHardExpiryAndTCPDisconnectReleaseAllocation(t *testing.T) {
	worker := udpWorker(t)
	s := newTestService(t, worker, func(c *Config) {
		c.CredentialTTL = 100 * time.Millisecond
		c.ScopeTTL = 200 * time.Millisecond
		c.IdleTimeout = time.Second
	})
	credentials := issueFor(t, s, worker, "view:1")
	client, _ := turnClient(t, s, credentials)
	relay, err := client.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	eventually(t, func() bool { return s.ActiveAllocations() == 0 })
	s2 := newTestService(t, worker, nil)
	c2 := issueFor(t, s2, worker, "second:1")
	client2, conn := turnClient(t, s2, c2)
	relay2, err := client2.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	defer relay2.Close()
	conn.Close()
	eventually(t, func() bool { return s2.ActiveAllocations() == 0 })
}

func TestProbeTimeoutMalformedAndOversizedFrameBounded(t *testing.T) {
	s := newTestService(t, nil, func(c *Config) { c.ProbeTimeout = 30 * time.Millisecond })
	for _, data := range [][]byte{nil, {0, 3}, bytes.Repeat([]byte{0}, 20)} {
		conn, err := net.Dial("tcp4", s.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		conn.SetDeadline(time.Now().Add(time.Second))
		if data != nil {
			conn.Write(data)
		}
		var b [1]byte
		if _, err := conn.Read(b[:]); err == nil {
			t.Fatal("unclassified connection remained open")
		}
		conn.Close()
	}
	valid := make([]byte, 20)
	binary.BigEndian.PutUint16(valid, 3)
	binary.BigEndian.PutUint32(valid[4:], 0x2112a442)
	if !firstTURNRequest(valid, 4096) {
		t.Fatal("valid allocate header rejected")
	}
	binary.BigEndian.PutUint16(valid[2:], 65532)
	if firstTURNRequest(valid, 4096) {
		t.Fatal("oversized STUN initial frame accepted")
	}
	for _, raw := range [][]byte{{0, 3, 255, 252}, {0x40, 0, 255, 255}, {0x80, 0, 0, 0}} {
		if _, err := readTURNFrame(bytes.NewReader(raw), 4096); err == nil {
			t.Fatal("oversized/invalid following frame accepted")
		}
	}
}

func TestCloseAndDrainBounded(t *testing.T) {
	worker := udpWorker(t)
	s := newTestService(t, worker, nil)
	credentials := issueFor(t, s, worker, "view:1")
	client, _ := turnClient(t, s, credentials)
	relay, err := client.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := s.Drain(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("drain: %v", err)
	}
	if _, err := s.IssuePeers("late", []netip.AddrPort{worker.LocalAddr().(*net.UDPAddr).AddrPort()}); !errors.Is(err, ErrClosed) {
		t.Fatal("draining service issued another credential")
	}
	s.Close()
	eventually(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.connections) == 0 })
}

func TestConnectionBoundsAndShutdownDuringMixedProbeBurst(t *testing.T) {
	s := newTestService(t, nil, func(c *Config) {
		c.MaxConnections, c.MaxTURNConnections, c.MaxProbes = 4, 2, 2
		c.HTTPQueue, c.TURNQueue = 2, 2
	})
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			c, err := net.DialTimeout("tcp4", s.Addr().String(), time.Second)
			if err != nil {
				return
			}
			defer c.Close()
			c.SetDeadline(time.Now().Add(time.Second))
			if i%2 == 0 {
				c.Write([]byte{0}) // Incomplete TURN probe remains pending.
			} else {
				c.Write([]byte("GET / HTTP/1.1\r\nHost: pogo.test\r\n\r\n"))
			}
			var b [1]byte
			c.Read(b[:])
		}()
	}
	close(start)
	eventually(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.connections) != 0 })
	s.mu.Lock()
	connections, turnConnections := len(s.connections), len(s.turnConnections)
	s.mu.Unlock()
	if connections > 4 || turnConnections > 2 || len(s.probes) > 2 {
		t.Fatal("resource caps were exceeded")
	}
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("shutdown waited for incomplete network probes")
	}
	wg.Wait()
	s.mu.Lock()
	remaining := len(s.connections)
	s.mu.Unlock()
	if remaining != 0 || len(s.probes) != 0 {
		t.Fatal("shutdown left owned connections or probes")
	}
	select {
	case <-s.acceptDone:
	default:
		t.Fatal("accept worker survived shutdown")
	}
	select {
	case <-s.expireDone:
	default:
		t.Fatal("expiry worker survived shutdown")
	}
}

func TestTLSWrappedListenerPreservesHTTPStateAndBytes(t *testing.T) {
	worker := udpWorker(t)
	certServer := httptest.NewTLSServer(http.NotFoundHandler())
	certs := certServer.TLS.Certificates
	ca := x509.NewCertPool()
	ca.AddCert(certServer.Certificate())
	certServer.Close()
	base, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := worker.LocalAddr().(*net.UDPAddr).AddrPort().Port()
	s, err := New(tls.NewListener(base, &tls.Config{Certificates: certs, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}}), Config{RelayAddr: netip.MustParseAddr("127.0.0.1"), AllowedPeerIPs: []netip.Addr{netip.MustParseAddr("127.0.0.1")}, PeerPortMin: port, PeerPortMax: port, allowLoopback: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := &http.Server{ConnContext: s.ConnContext, Handler: s.HTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			t.Error("TLS metadata lost")
		}
		io.WriteString(w, r.URL.Path)
	}))}
	go server.Serve(s.HTTPListener())
	defer server.Close()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: ca, MinVersion: tls.VersionTLS12}}}
	response, err := client.Get("https://" + s.Addr().String() + "/tls-preserved")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if string(body) != "/tls-preserved" {
		t.Fatal("TLS HTTP request changed")
	}
	credentials := issueFor(t, s, worker, "tls:1")
	conn, err := tls.Dial("tcp4", s.Addr().String(), &tls.Config{RootCAs: ca, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	factory := logging.NewDefaultLoggerFactory()
	factory.Writer = io.Discard
	turnClient, err := turn.NewClient(&turn.ClientConfig{Conn: turn.NewSTUNConn(conn), STUNServerAddr: s.Addr().String(), TURNServerAddr: s.Addr().String(), Username: credentials.Username, Password: credentials.Password, Realm: s.cfg.Realm, LoggerFactory: factory})
	if err != nil {
		t.Fatal(err)
	}
	defer turnClient.Close()
	if err := turnClient.Listen(); err != nil {
		t.Fatal(err)
	}
	relay, err := turnClient.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	relay.WriteTo([]byte("TLS opaque media"), worker.LocalAddr())
	worker.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 100)
	n, _, err := worker.ReadFromUDP(buf)
	if err != nil || string(buf[:n]) != "TLS opaque media" {
		t.Fatalf("TLS TURN failed: %v", err)
	}
}

func TestWorkerIPUpdateRevokesOldAllocationAndDeniesStalePeer(t *testing.T) {
	worker := udpWorker(t)
	s := newTestService(t, worker, nil)
	credentials := issueFor(t, s, worker, "view:1")
	client, _ := turnClient(t, s, credentials)
	relay, err := client.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	if err := s.UpdateAllowedPeerIPs([]netip.Addr{netip.MustParseAddr("127.0.0.1")}); err != nil {
		t.Fatal(err)
	}
	if s.ActiveAllocations() != 1 {
		t.Fatal("identical DNS refresh interrupted the allocation")
	}
	if err := s.UpdateAllowedPeerIPs([]netip.Addr{netip.MustParseAddr("127.0.0.2")}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return s.ActiveAllocations() == 0 })
	if _, err := s.IssuePeers("view:2", []netip.AddrPort{worker.LocalAddr().(*net.UDPAddr).AddrPort()}); !errors.Is(err, ErrPeerDenied) {
		t.Fatal("stale worker address remained allowed")
	}
	if err := s.UpdateAllowedPeerIPs(nil); !errors.Is(err, ErrPeerDenied) {
		t.Fatal("empty DNS update was accepted")
	}
}

func TestConcurrentDNSIssueAndRevokeCannotKeepStaleAuthorization(t *testing.T) {
	s := newTestService(t, nil, nil)
	oldIP, newIP := netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("127.0.0.2")
	peer := []netip.AddrPort{netip.AddrPortFrom(oldIP, 19780)}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for operation := range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := range 200 {
				switch operation {
				case 0:
					_, err := s.IssuePeers(fmt.Sprintf("view:%d", i), peer)
					if err != nil && !errors.Is(err, ErrPeerDenied) {
						t.Errorf("concurrent issue: %v", err)
					}
				case 1:
					ip := oldIP
					if i%2 == 0 {
						ip = newIP
					}
					if err := s.UpdateAllowedPeerIPs([]netip.Addr{ip}); err != nil {
						t.Errorf("concurrent DNS update: %v", err)
					}
				case 2:
					s.RevokeScope("view")
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	if err := s.UpdateAllowedPeerIPs([]netip.Addr{newIP}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.IssuePeers("after:1", peer); !errors.Is(err, ErrPeerDenied) {
		t.Fatal("stale peer was reauthorized after the DNS transition")
	}
	s.mu.Lock()
	remaining := len(s.grants)
	s.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("DNS transition retained %d stale grants", remaining)
	}
}

func TestEpochCannotResetViewHardLifetimeAndPrefixIsExact(t *testing.T) {
	s := newTestService(t, nil, func(c *Config) { c.CredentialTTL = 20 * time.Millisecond; c.ScopeTTL = 80 * time.Millisecond })
	peer := []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:19780")}
	s.IssuePeers("view:1", peer)
	other, err := s.IssuePeers("view-other:1", peer)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the actual expiry loop as well as the per-request guard. The
	// loop must not delete the start and give the same view a fresh lifetime.
	eventually(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.grants) == 0 })
	if _, err := s.IssuePeers("view:2", peer); !errors.Is(err, ErrClosed) {
		t.Fatal("epoch reset extended the hard view lifetime")
	}
	s.RevokeScope("view")
	s.mu.Lock()
	_, exists := s.scopeStarts["view-other"]
	s.mu.Unlock()
	if !exists {
		t.Fatal("scope prefix revoked another view")
	}
	if other.Username == "" {
		t.Fatal("other scope was not issued")
	}
}

func TestIssueRejectsForeignPeerAndBoundsCredentialRegistry(t *testing.T) {
	s := newTestService(t, nil, func(c *Config) { c.MaxCredentials = 2 })
	for _, peer := range []string{"127.0.0.2:19780", "127.0.0.1:80", "127.0.0.1:19781", "8.8.8.8:19780"} {
		if _, err := s.IssuePeers("scope", []netip.AddrPort{netip.MustParseAddrPort(peer)}); !errors.Is(err, ErrPeerDenied) {
			t.Fatalf("foreign endpoint accepted: %s", peer)
		}
	}
	peer := []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:19780")}
	for _, scope := range []string{"one", "two"} {
		c, err := s.IssuePeers(scope, peer)
		if err != nil || c.ExpiresIn != 60 {
			t.Fatalf("issue: %v ttl%d", err, c.ExpiresIn)
		}
	}
	if _, err := s.IssuePeers("three", peer); !errors.Is(err, ErrCapacity) {
		t.Fatal("credential registry unbounded")
	}
	s.RevokeScope("one")
	if _, err := s.IssuePeers("another", peer); err != nil {
		t.Fatal(err)
	}
	if _, err := normalizeConfig(Config{RelayAddr: netip.MustParseAddr("10.0.0.1"), AllowedPeerIPs: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}); err == nil {
		t.Fatal("production config accepted loopback worker")
	}
	if strings.Contains(s.cfg.Realm, "\n") {
		t.Fatal("realm invalid")
	}
}
