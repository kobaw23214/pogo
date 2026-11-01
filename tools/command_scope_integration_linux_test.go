//go:build linux

package tools

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

const commandScopeCanary = "synthetic-command-scope-key-never-exposed"

func requireCommandScopeIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("SCOPE_REQUIRE_SANDBOX") != "1" {
		t.Skip("set SCOPE_REQUIRE_SANDBOX=1 in the isolated test container to require OS confinement")
	}
}

func runScopedFixture(t *testing.T, scope *CommandScope, dir string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = []string{"HOME=/untrusted", "PATH=/untrusted/bin", "SCOPED_CANARY_API_KEY=" + commandScopeCanary,
		"BASH_ENV=/untrusted/startup", "LD_PRELOAD=/untrusted/library.so", "POGO_USERS_FILE=/untrusted/users"}
	if err := WrapScopedCommand(cmd, scope); err != nil {
		return "", err
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestCommandScopeLinuxPrivateFilesystemAndRuntime(t *testing.T) {
	requireCommandScopeIntegration(t)
	parent := t.TempDir()
	workspace := filepath.Join(parent, "own")
	other := filepath.Join(parent, "other", ".pogo")
	shared := filepath.Join(parent, "builtin-docs")
	for _, dir := range []string{workspace, other, shared} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(other, "provider-canary")
	if err := os.WriteFile(outside, []byte(commandScopeCanary), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "SKILL.md"), []byte("trusted synthetic skill"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(workspace, "escape")); err != nil {
		t.Fatal(err)
	}
	scope := &CommandScope{Cwd: workspace, ReadRoots: []string{shared}}
	code := `import os, pathlib, ssl, openpyxl
assert os.environ['HOME'] == os.getcwd()
assert os.environ['TMPDIR'] == '/tmp'
assert os.environ.get('SCOPED_CANARY_API_KEY') is None
assert os.environ.get('BASH_ENV') is None
assert os.environ.get('LD_PRELOAD') is None
assert not any(k.startswith('POGO_') for k in os.environ)
pathlib.Path('own-output').write_text('private-ok')
pathlib.Path('/tmp/scratch').write_text('temporary-ok')
print('runtime-private-ok')`
	if out, err := runScopedFixture(t, scope, workspace, "/opt/venv/bin/python3", "-c", code); err != nil || !strings.Contains(out, "runtime-private-ok") {
		t.Fatalf("required sandbox/runtime failed: %v\n%s", err, out)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "own-output")); err != nil || string(data) != "private-ok" {
		t.Fatalf("own writable mount did not persist: %v %q", err, data)
	}
	for _, target := range []string{outside, filepath.Join(workspace, "escape", "provider-canary"), "/etc/shadow", "/home/pogo/.pogo/providers.json"} {
		if out, err := runScopedFixture(t, scope, workspace, "/bin/cat", target); err == nil || strings.Contains(out, commandScopeCanary) {
			t.Fatalf("outside path was readable: target=%s err=%v output=%q", target, err, out)
		}
	}
	if out, err := runScopedFixture(t, scope, workspace, "/bin/cat", filepath.Join(shared, "SKILL.md")); err != nil || !strings.Contains(out, "synthetic skill") {
		t.Fatalf("explicit builtin read root failed: %v %s", err, out)
	}
	if out, err := runScopedFixture(t, scope, workspace, "/bin/sh", "-c", `printf changed > "$1"`, "sh", filepath.Join(shared, "SKILL.md")); err == nil {
		t.Fatalf("shared builtin was writable: %s", out)
	}
	if out, err := runScopedFixture(t, scope, workspace, "/bin/bash", "-c", `for p in /proc/[0-9]*/environ; do cat "$p" 2>/dev/null; done`); err != nil || strings.Contains(out, commandScopeCanary) || strings.Contains(out, "SCOPED_CANARY_API_KEY") {
		t.Fatalf("private proc/launcher environment failed: err=%v canaryPresent=%v", err, strings.Contains(out, commandScopeCanary))
	}
	for _, dir := range []string{parent, other, filepath.Join(workspace, "escape")} {
		cmd := exec.Command("/bin/true")
		cmd.Dir = dir
		if err := WrapScopedCommand(cmd, scope); err == nil {
			t.Fatalf("escaped workdir accepted: %s", dir)
		}
	}
}

func TestCommandScopeLinuxNetworkIsExplicit(t *testing.T) {
	requireCommandScopeIntegration(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_, _ = conn.Write([]byte("allowed"))
			conn.Close()
			if ctx.Err() != nil {
				return
			}
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	code := fmt.Sprintf("import socket; s=socket.create_connection(('127.0.0.1',%d), timeout=1); print(s.recv(16).decode())", port)
	scope := &CommandScope{Cwd: t.TempDir()}
	if out, err := runScopedFixture(t, scope, scope.Cwd, "/opt/venv/bin/python3", "-c", code); err == nil {
		t.Fatalf("default network boundary was open: %s", out)
	}
	scope.Network = true
	if out, err := runScopedFixture(t, scope, scope.Cwd, "/opt/venv/bin/python3", "-c", code); err != nil || !strings.Contains(out, "allowed") {
		t.Fatalf("explicit network grant failed: %v %s", err, out)
	}
}

func TestCommandScopeLinuxCancellationKillsDescendants(t *testing.T) {
	requireCommandScopeIntegration(t)
	cwd := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/bash", "-c", `(sleep 1; printf escaped > survived)& printf 'ready\n'; wait`)
	cmd.Dir = cwd
	if err := WrapScopedCommand(cmd, &CommandScope{Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return KillGroup(cmd) }
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "ready\n" {
		cancel()
		_ = cmd.Wait()
		t.Fatalf("sandbox command did not start: %v %q %s", err, line, stderr.String())
	}
	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatal("cancelled command succeeded")
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(cwd, "survived")); !os.IsNotExist(err) {
		t.Fatal("a descendant survived the cancelled sandbox")
	}
}

func TestCommandScopeLinuxDedicatedPTY(t *testing.T) {
	requireCommandScopeIntegration(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cwd := t.TempDir()
	cmd := exec.CommandContext(ctx, "/bin/bash", "-c", "test -t 0 && test -t 1 && printf pty-ok")
	cmd.Dir = cwd
	if err := WrapScopedCommand(cmd, &CommandScope{Cwd: cwd, Interactive: true}); err != nil {
		t.Fatal(err)
	}
	terminal, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	out, _ := io.ReadAll(terminal) // Linux PTYs finish with EIO.
	if err := cmd.Wait(); err != nil || !strings.Contains(string(out), "pty-ok") {
		t.Fatalf("scoped PTY failed: %v %s", err, out)
	}
}
