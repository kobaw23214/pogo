package main

import (
	"crypto/x509"
	"flag"
	"io"
	"net"
	"os"
	"testing"
)

func TestBrowserRouteVerifiedRequiresExplicitBoolCLIOptIn(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{{"default", nil, false}, {"false", []string{"-browser-route-verified=false"}, false}, {"true", []string{"-browser-route-verified=true"}, true}, {"zero", []string{"-browser-route-verified=0"}, false}} {
		t.Run(tc.name, func(t *testing.T) {
			previousFlags, previousArgs := flag.CommandLine, os.Args
			defer func() { flag.CommandLine, os.Args = previousFlags, previousArgs }()
			flag.CommandLine = flag.NewFlagSet("gateway-test", flag.ContinueOnError)
			flag.CommandLine.SetOutput(io.Discard)
			// Reject the preview mode after parsing to exercise the real CLI
			// without opening sockets, reading credentials or starting a server.
			os.Args = append([]string{"gateway-test", "-preview-mode", "invalid"}, tc.args...)
			if err := run(); err == nil || err.Error() != "preview mode must be subdomain or path" {
				t.Fatalf("unexpected pre-listener result: %v", err)
			}
			value := flag.CommandLine.Lookup("browser-route-verified")
			if value == nil || value.Value.(flag.Getter).Get() != tc.want {
				t.Fatalf("native route assertion did not preserve explicit bool %v", tc.want)
			}
		})
	}
}

func TestCertificateRequirementsFollowPreviewMode(t *testing.T) {
	mainOnly := &x509.Certificate{DNSNames: []string{"pogo.example.test"}}
	if err := validateCertificateNames(mainOnly, "pogo.example.test", "", "path"); err != nil {
		t.Fatalf("path required wildcard: %v", err)
	}
	if err := validateCertificateNames(mainOnly, "pogo.example.test", "preview.example.test", "subdomain"); err == nil {
		t.Fatal("missing wildcard accepted")
	}
	if err := validateCertificateNames(mainOnly, "wrong.example.test", "", "path"); err == nil {
		t.Fatal("main SAN unchecked")
	}
	both := &x509.Certificate{DNSNames: []string{"pogo.example.test", "*.preview.example.test"}}
	if err := validateCertificateNames(both, "pogo.example.test", "preview.example.test", "subdomain"); err != nil {
		t.Fatal(err)
	}
	ipOnly := &x509.Certificate{IPAddresses: []net.IP{net.ParseIP("192.168.1.50")}}
	if err := validateCertificateNames(ipOnly, "192.168.1.50", "", "path"); err != nil {
		t.Fatal(err)
	}
}
