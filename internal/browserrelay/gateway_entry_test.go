package browserrelay

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/turn/v5"
	"github.com/yosukeno/pogo/internal/webgateway"
)

// This checks the existing real TURN implementation behind the proposed shared
// entry, not just an echo server accepting a STUN-shaped byte sequence.
func TestGatewayEntryPreservesPogoHTTPAndTURNRevocation(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		name := "tcp"
		if encrypted {
			name = "tls-without-turn-alpn"
		}
		t.Run(name, func(t *testing.T) {
			worker := udpWorker(t)
			pogo := newTestService(t, worker, nil)
			origin := "http://pogo.example.test:19770"
			if encrypted {
				origin = "https://pogo.example.test:19770"
			}
			backend := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Host != "pogo.example.test:19770" || r.Header.Get("Origin") != origin {
					t.Errorf("public Host/Origin changed: %q %q", r.Host, r.Header.Get("Origin"))
				}
				if r.URL.Path == "/socket" {
					connection, buffer, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					defer connection.Close()
					_, _ = buffer.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
					_ = buffer.Flush()
					data := make([]byte, 4)
					if _, err := io.ReadFull(buffer, data); err != nil || string(data) != "PING" {
						t.Errorf("upgrade lost buffered bytes: %q %v", data, err)
						return
					}
					_, _ = connection.Write([]byte("PONG"))
					return
				}
				_, _ = io.WriteString(w, "pogo-http-ok")
			}), ReadHeaderTimeout: time.Second}
			go func() { _ = backend.Serve(pogo.HTTPListener()) }()
			t.Cleanup(func() { _ = backend.Close() })
			base, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			config := webgateway.Config{TURNAddress: pogo.Addr().String()}
			clientTLS := &tls.Config{MinVersion: tls.VersionTLS12}
			scheme := "http"
			if encrypted {
				certificate := httptest.NewTLSServer(http.NotFoundHandler())
				config.TLSConfig = &tls.Config{Certificates: certificate.TLS.Certificates, MinVersion: tls.VersionTLS12}
				clientTLS.RootCAs = x509.NewCertPool()
				clientTLS.RootCAs.AddCert(certificate.Certificate())
				certificate.Close()
				scheme = "https"
			}
			entry, err := webgateway.New(base, config)
			if err != nil {
				_ = base.Close()
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = entry.Close() })
			upstream, _ := url.Parse("http://" + pogo.Addr().String())
			proxy := &http.Server{ConnContext: entry.ConnContext, Handler: entry.HTTPHandler(httputil.NewSingleHostReverseProxy(upstream)), ReadHeaderTimeout: time.Second}
			go func() { _ = proxy.Serve(entry.HTTPListener()) }()
			t.Cleanup(func() { _ = proxy.Close() })
			transport := &http.Transport{TLSClientConfig: clientTLS}
			t.Cleanup(transport.CloseIdleConnections)
			httpClient := &http.Client{Transport: transport, Timeout: 2 * time.Second}
			request, _ := http.NewRequestWithContext(context.Background(), "GET", scheme+"://"+base.Addr().String()+"/api/probe", nil)
			request.Host = "pogo.example.test:19770"
			request.Header.Set("Origin", origin)
			response, err := httpClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil || string(body) != "pogo-http-ok" {
				t.Fatalf("shared HTTP failed: %q %v", body, err)
			}
			var socket net.Conn
			if encrypted {
				socket, err = tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp4", base.Addr().String(), clientTLS)
			} else {
				socket, err = net.DialTimeout("tcp4", base.Addr().String(), time.Second)
			}
			if err != nil {
				t.Fatal(err)
			}
			_ = socket.SetDeadline(time.Now().Add(2 * time.Second))
			// Include upgrade payload in the same write as HTTP headers, exercising
			// all three buffering layers: entry, ReverseProxy, and Pogo's relay.
			_, err = io.WriteString(socket, "GET /socket HTTP/1.1\r\nHost: pogo.example.test:19770\r\nOrigin: "+origin+"\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\nPING")
			if err != nil {
				_ = socket.Close()
				t.Fatal(err)
			}
			buffer := bufio.NewReader(socket)
			upgrade, err := http.ReadResponse(buffer, &http.Request{Method: http.MethodGet})
			if err != nil || upgrade.StatusCode != http.StatusSwitchingProtocols {
				_ = socket.Close()
				t.Fatalf("gateway upgrade failed: %v %v", upgrade, err)
			}
			pong := make([]byte, 4)
			_, err = io.ReadFull(buffer, pong)
			_ = socket.Close()
			if err != nil || string(pong) != "PONG" {
				t.Fatalf("gateway upgrade bytes changed: %q %v", pong, err)
			}

			var raw net.Conn
			if encrypted {
				// Intentionally no ALPN. Existing TURN/TLS clients support this.
				raw, err = tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp4", base.Addr().String(), clientTLS)
			} else {
				raw, err = net.DialTimeout("tcp4", base.Addr().String(), time.Second)
			}
			if err != nil {
				t.Fatal(err)
			}
			connection := &gatewayEOFConn{Conn: raw, eof: make(chan struct{})}
			defer connection.Close()
			credentials := issueFor(t, pogo, worker, "gateway:1")
			factory := logging.NewDefaultLoggerFactory()
			factory.Writer = io.Discard
			client, err := turn.NewClient(&turn.ClientConfig{Conn: turn.NewSTUNConn(connection), STUNServerAddr: base.Addr().String(), TURNServerAddr: base.Addr().String(), Username: credentials.Username, Password: credentials.Password, Realm: pogo.cfg.Realm, RTO: 30 * time.Millisecond, LoggerFactory: factory})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if err := client.Listen(); err != nil {
				t.Fatal(err)
			}
			relay, err := client.Allocate()
			if err != nil {
				t.Fatal(err)
			}
			defer relay.Close()
			if _, err := relay.WriteTo([]byte("gateway-to-worker"), worker.LocalAddr()); err != nil {
				t.Fatal(err)
			}
			packet := make([]byte, 100)
			_ = worker.SetReadDeadline(time.Now().Add(time.Second))
			n, _, err := worker.ReadFromUDP(packet)
			if err != nil || string(packet[:n]) != "gateway-to-worker" {
				t.Fatalf("outgoing TURN: %q %v", packet[:n], err)
			}
			_, err = worker.WriteToUDP([]byte("worker-to-gateway"), relay.LocalAddr().(*net.UDPAddr))
			if err != nil {
				t.Fatal(err)
			}
			_ = relay.SetReadDeadline(time.Now().Add(time.Second))
			n, _, err = relay.ReadFrom(packet)
			if err != nil || string(packet[:n]) != "worker-to-gateway" {
				t.Fatalf("incoming TURN: %q %v", packet[:n], err)
			}
			pogo.RevokeScope("gateway")
			eventually(t, func() bool { return pogo.ActiveAllocations() == 0 })
			select {
			case <-connection.eof:
			case <-time.After(time.Second):
				t.Fatal("Pogo revocation did not close the external gateway connection")
			}
		})
	}
}

type gatewayEOFConn struct {
	net.Conn
	eof  chan struct{}
	once sync.Once
}

func (c *gatewayEOFConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if err != nil {
		c.once.Do(func() { close(c.eof) })
	}
	return n, err
}
