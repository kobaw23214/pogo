//go:build linux

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Run explicitly inside the reviewed client image. An ordinary host test
// cannot establish the image's installed clients or real namespace behavior.
func TestNetworkClientsThroughSandboxedBash(t *testing.T) {
	if os.Getenv("POGO_NETWORK_CLIENTS_IMAGE_TEST") != "1" {
		t.Skip("requires the reviewed Linux network-client runtime image")
	}
	t.Setenv(EnvSandbox, SandboxEnforce)
	t.Setenv(EnvSandboxNet, "1")
	t.Setenv("POGO_NETWORK_CLIENTS_CANARY_API_KEY", "private-parent-environment")
	store := t.TempDir()
	t.Setenv("POGO_SKILL_RUNTIME_STORE", store)
	if err := os.WriteFile(filepath.Join(store, "private-canary"), []byte("private-skill-store"), 0600); err != nil {
		t.Fatal(err)
	}
	policy := InitSandbox()
	if !policy.Active() || !policy.Net || policy.Mode != SandboxEnforce {
		t.Fatalf("expected an active enforce sandbox with network allowed: %+v", policy)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/nonce" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, "pogo-network-client-fixture")
	}))
	defer server.Close()
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	command := "set -eu; test -z \"${POGO_NETWORK_CLIENTS_CANARY_API_KEY:-}\"; " +
		"test ! -e " + quote(filepath.Join(store, "private-canary")) + "; " +
		"for client in curl wget ssh scp sftp psql mysql mariadb clickhouse-client lftp ping ip nc dig traceroute openssl; do command -v \"$client\" >/dev/null; done; " +
		// The compat wrapper must answer --version on any CPU the image lands
		// on: natively where the official binary runs, with fallback wording
		// where it cannot.
		"clickhouse-client --version >/dev/null; " +
		"curl --noproxy '*' --fail --silent --show-error --connect-timeout 2 --max-time 5 " + quote(server.URL+"/nonce")
	args, err := json.Marshal(map[string]any{"command": command, "timeout": 15})
	if err != nil {
		t.Fatal(err)
	}
	result, err := (&Bash{Cwd: t.TempDir()}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("actual Bash tool failed: %v", err)
	}
	if result.Text != "pogo-network-client-fixture" {
		t.Fatalf("unexpected nonce response: %q", result.Text)
	}
	details, ok := result.Details.(BashDetails)
	if !ok || !details.Sandboxed || details.ExitCode != 0 {
		t.Fatalf("actual Bash receipt did not record a successful sandboxed command: %#v", result.Details)
	}
}
