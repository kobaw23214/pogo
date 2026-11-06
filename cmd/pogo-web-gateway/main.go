// pogo-web-gateway is the opt-in shared TCP gateway. It does not control Docker;
// its management plane is a private Unix socket shared only with Pogo Manager.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/yosukeno/pogo/internal/webgateway"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "pogo-web-gateway:", err)
		os.Exit(1)
	}
}

func run() error {
	listen := flag.String("listen", "0.0.0.0:19770", "shared HTTP/TURN TCP listener")
	mainUpstream := flag.String("main-upstream", "http://pogo:7777", "fixed private Pogo HTTP origin")
	mainHosts := flag.String("main-hosts", "", "comma-separated exact public Pogo authorities")
	publicOrigin := flag.String("public-origin", "", "canonical public Pogo origin, including external port")
	previewMode := flag.String("preview-mode", "subdomain", "preview routing: subdomain or path (private HTTP IP/localhost supported)")
	previewDomain := flag.String("preview-domain", "", "wildcard preview DNS domain, excluding the grant label")
	turnAddress := flag.String("turn-address", "pogo:7777", "fixed private plaintext Pogo shared TURN listener")
	adminSocket := flag.String("admin-socket", "/run/pogo-gateway/admin/control.sock", "private management Unix socket")
	statePath := flag.String("state-path", "", "private durable access state; defaults to gateway-state.json beside the admin socket")
	certFile := flag.String("tls-cert", "", "PEM certificate including main and wildcard preview names")
	keyFile := flag.String("tls-key", "", "PEM private key; never supplied to applications")
	allowHTTP := flag.Bool("allow-http", false, "explicit permission for plaintext public origin (path mode requires private IP/localhost)")
	allowLoopback := flag.Bool("allow-loopback-upstream", false, "explicit local QA mode: allow loopback runtime addresses")
	browserRouteVerified := flag.Bool("browser-route-verified", false, "administrator assertion: the configured native browser page/API/WebSocket route was actually verified")
	probeAddress := flag.String("probe-address", "", "optional private address for self-probes; public Host/SNI are preserved")
	probeCA := flag.String("probe-ca", "", "optional PEM CA used to verify public self-probe TLS")
	flag.Parse()
	if *previewMode != "subdomain" && *previewMode != "path" {
		return errors.New("preview mode must be subdomain or path")
	}
	mainURL, err := url.Parse(*mainUpstream)
	if err != nil || mainURL.Host != *turnAddress || mainURL.Scheme != "http" {
		return errors.New("main upstream and TURN address must name the same private plaintext Pogo listener")
	}
	publicURL, err := url.Parse(*publicOrigin)
	if err != nil {
		return errors.New("invalid public origin")
	}
	var tlsConfig *tls.Config
	if publicURL.Scheme == "https" {
		if *certFile == "" || *keyFile == "" {
			return errors.New("HTTPS requires explicit certificate and key")
		}
		certificate, err := tls.LoadX509KeyPair(*certFile, *keyFile)
		if err != nil {
			return errors.New("unable to load gateway certificate")
		}
		leaf, err := x509.ParseCertificate(certificate.Certificate[0])
		if err != nil {
			return errors.New("unable to parse gateway certificate")
		}
		if err := validateCertificateNames(leaf, publicURL.Hostname(), *previewDomain, *previewMode); err != nil {
			return err
		}
		tlsConfig = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	} else if publicURL.Scheme != "http" || !*allowHTTP || *certFile != "" || *keyFile != "" {
		return errors.New("plaintext HTTP requires explicit -allow-http and no TLS files")
	}
	// Self-probes use distinct short-lived grant hosts. Never retain a pool
	// entry per host on the same listener that serves normal browser traffic.
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxIdleConns: 4, MaxIdleConnsPerHost: 1, IdleConnTimeout: 5 * time.Second}
	defer transport.CloseIdleConnections()
	if *probeAddress != "" {
		if host, port, err := net.SplitHostPort(*probeAddress); err != nil || host == "" || port == "" {
			return errors.New("invalid private probe address")
		}
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", *probeAddress)
		}
	}
	if *probeCA != "" {
		data, err := os.ReadFile(*probeCA)
		if err != nil {
			return errors.New("unable to load probe CA")
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(data) {
			return errors.New("invalid probe CA")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	if *statePath == "" {
		*statePath = filepath.Join(filepath.Dir(*adminSocket), "gateway-state.json")
	}
	router, err := webgateway.NewRouter(webgateway.RouterOptions{MainUpstream: *mainUpstream, MainHosts: strings.Split(*mainHosts, ","), PublicOrigin: *publicOrigin, PreviewDomain: *previewDomain, PreviewMode: *previewMode, AllowInsecureHTTP: *allowHTTP, AllowLoopbackUpstream: *allowLoopback, BrowserURLVerified: *browserRouteVerified, StatePath: *statePath, ProbeClient: &http.Client{Transport: transport, Timeout: 3 * time.Second}})
	if err != nil {
		return err
	}
	defer router.Close()
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	entry, err := webgateway.New(listener, webgateway.Config{TURNAddress: *turnAddress, TLSConfig: tlsConfig})
	if err != nil {
		listener.Close()
		return err
	}
	defer entry.Close()
	admin, err := webgateway.ListenAdmin(*adminSocket, router)
	if err != nil {
		return err
	}
	defer admin.Close()
	server := &http.Server{Handler: entry.HTTPHandler(router.Handler()), ConnContext: entry.ConnContext, ReadHeaderTimeout: 10 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- server.Serve(entry.HTTPListener()) }()
	select {
	case err := <-served:
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	admin.Close()
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdown); err != nil {
		return server.Close()
	}
	return nil
}

func validateCertificateNames(leaf *x509.Certificate, mainHost, previewDomain, previewMode string) error {
	if leaf.VerifyHostname(mainHost) != nil {
		return errors.New("certificate must cover the public main hostname or IP")
	}
	if previewMode == "path" {
		return nil
	}
	if leaf.VerifyHostname("probe."+previewDomain) != nil {
		return errors.New("certificate must cover main and wildcard preview hostnames")
	}
	for _, name := range leaf.DNSNames {
		if strings.EqualFold(strings.TrimSuffix(name, "."), "*."+strings.TrimSuffix(previewDomain, ".")) {
			return nil
		}
	}
	return errors.New("certificate requires an explicit wildcard preview DNS name")
}
