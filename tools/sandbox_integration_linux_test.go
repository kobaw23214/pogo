//go:build linux

package tools

// Integration tests that need a real bubblewrap. They skip where there is none,
// so `go test ./tools/` stays useful on a machine without a sandbox, and they
// assert the two properties that argv inspection cannot: that the confinement
// actually took effect, and that a secret in this process's environment does not
// reach the command.
//
// The second one is here rather than in sandbox_test.go because the leak it
// guards against is invisible to argv: bwrap's --clearenv scrubs what bwrap
// spawns, but bwrap is itself spawned by this process and is PID 1 inside its own
// sandbox, so its /proc/1/environ is readable by the sandboxed command at the
// same uid. Measured with a hand-written bwrap invocation that passed
// --clearenv and nothing else: all four of the deployment's API keys were
// recoverable. The fix is to scrub the launcher's environment too, and the only
// way to know it is still in place is to run the whole thing and look.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func requireSandbox(t *testing.T) SandboxPolicy {
	t.Helper()
	p := InitSandbox()
	if !p.Active() {
		reason := p.Unavailable
		if reason == "" {
			reason = "mode is " + p.Mode
		}
		t.Skipf("no working sandbox here (%s)", reason)
	}
	return p
}

func runBash(t *testing.T, cwd, command string) (string, error) {
	t.Helper()
	args, err := json.Marshal(map[string]any{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	res, err := (&Bash{Cwd: cwd}).Execute(context.Background(), args)
	if err != nil {
		// bash reports a non-zero exit as an error whose text is the output,
		// which is exactly what these tests want to read.
		return err.Error(), err
	}
	return res.Text, nil
}

// The canary must not be reachable, by either route.
func TestSandboxedBashCannotReadThisProcessSecrets(t *testing.T) {
	const canary = "sk-canary-must-not-escape-the-sandbox"
	t.Setenv("POGO_TEST_CANARY_API_KEY", canary)
	requireSandbox(t)
	dir := t.TempDir()

	// First the obvious route: the command's own environment.
	out, _ := runBash(t, dir, "env")
	if strings.Contains(out, canary) {
		t.Errorf("the canary is in the sandboxed command's own environment:\n%s", out)
	}

	// Then the one that --clearenv alone does not close.
	out, _ = runBash(t, dir,
		`cat /proc/[0-9]*/environ 2>/dev/null | tr '\0' '\n' | grep -c CANARY_API_KEY || true`)
	if got := strings.TrimSpace(out); got != "0" {
		t.Errorf("the canary is readable through /proc/*/environ (count %q). "+
			"bwrap's own environment is the leak: --clearenv does not scrub it, "+
			"cmd.Env has to.", got)
	}
}

// A test that passes because nothing was confined is worse than no test, so the
// confinement itself is asserted rather than assumed.
func TestSandboxedBashCannotWriteOutsideItsGrants(t *testing.T) {
	requireSandbox(t)
	dir := t.TempDir()

	// The grants: the working directory and TMPDIR.
	if out, err := runBash(t, dir, "echo ok > ./inside && cat ./inside"); err != nil ||
		!strings.Contains(out, "ok") {
		t.Errorf("writing inside the working directory must work: %v\n%s", err, out)
	}
	// Everything else on the root filesystem is read-only.
	for _, path := range []string{"/etc/pogo-probe", "/usr/local/bin/pogo-probe"} {
		out, err := runBash(t, dir, "echo x > "+path)
		if err == nil {
			t.Errorf("writing %s succeeded; the sandbox is not confining writes:\n%s", path, out)
		}
	}
	// And a file that exists but should not be readable stays that way.
	if _, err := runBash(t, dir, "cat /etc/shadow"); err == nil {
		t.Error("reading /etc/shadow succeeded inside the sandbox")
	}
}

// A fresh network namespace may contain kernel-created, DOWN tunnel devices.
// Names are not connectivity: require a distinct namespace and prove that the
// real Bash tool cannot reach a listener reachable from the parent namespace.
func TestSandboxedBashHasNoNetwork(t *testing.T) {
	p := requireSandbox(t)
	if p.Net {
		t.Skip("POGO_SANDBOX_NET=1: the network is allowed on purpose here")
	}
	dir := t.TempDir()
	parentNamespace, err := os.Readlink("/proc/self/ns/net")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	const canary = "parent-network-reachable"
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
			_, _ = fmt.Fprintln(conn, canary)
			conn.Close()
		}
	}()
	probe := fmt.Sprintf(`readlink /proc/self/ns/net || exit 1
if exec 3<>/dev/tcp/127.0.0.1/%d; then
  IFS= read -r -t 1 reply <&3 || exit 2
  printf 'reply=%%s\n' "$reply"
else
  printf 'network-blocked\n'
fi`, listener.Addr().(*net.TCPAddr).Port)
	// The same probe must work outside confinement, so missing socket support
	// or a broken listener cannot masquerade as network isolation.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	plain, err := exec.CommandContext(ctx, "/bin/bash", "-c", probe).CombinedOutput()
	if err != nil || !strings.Contains(string(plain), parentNamespace) || !strings.Contains(string(plain), "reply="+canary) {
		t.Fatalf("parent-network positive control failed: %v\n%s", err, plain)
	}
	raw, err := json.Marshal(map[string]any{"command": probe})
	if err != nil {
		t.Fatal(err)
	}
	result, err := (&Bash{Cwd: dir, Timeout: 3 * time.Second}).Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("sandbox connectivity probe did not complete: %v", err)
	}
	lines := strings.Fields(result.Text)
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "net:[") || lines[0] == parentNamespace {
		t.Fatalf("sandbox did not enter a distinct network namespace: parent=%s output=%q", parentNamespace, result.Text)
	}
	if !strings.Contains(result.Text, "network-blocked") || strings.Contains(result.Text, "reply=") {
		t.Fatalf("sandbox reached the parent network listener:\n%s", result.Text)
	}
}

// The sandbox must not leave anything behind in the working directory. bwrap
// creates a bind target that does not exist, and the zero-byte read-only files
// that produces are a documented upstream problem: they appear in `git status`,
// resist rm, and here they would land in the workspace whose diff a person
// reviews. The defence is checking existence before binding; this is the test
// that would catch losing it.
func TestSandboxLeavesNoFilesInTheWorkingDirectory(t *testing.T) {
	requireSandbox(t)
	dir := t.TempDir()
	if _, err := runBash(t, dir, "true"); err != nil {
		t.Fatalf("a trivial command failed: %v", err)
	}
	out, _ := runBash(t, dir, "ls -A . | wc -l")
	if got := strings.TrimSpace(out); got != "0" {
		t.Errorf("the sandbox left %s entries in the working directory", got)
	}
}

func TestSandboxPrivateParentMasksNestedStores(t *testing.T) {
	p := requireSandbox(t)
	dir := t.TempDir()
	private := filepath.Join(dir, "control")
	admin, registry := filepath.Join(private, "admin"), filepath.Join(private, "registry")
	for _, root := range []string{admin, registry} {
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "secret"), []byte("synthetic-private-canary"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	public := filepath.Join(dir, "control-public")
	if err := os.WriteFile(public, []byte("public-ok"), 0600); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(admin, "control.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		t.Fatalf("private socket positive control failed: %v", err)
	}
	conn.Close()
	p.PrivateDirs = append(p.PrivateDirs, admin, private, registry, private+"/.")
	probe := `set -eu
test "$(cat "$2")" = public-ok
test ! -e "$1/admin/secret"
test ! -e "$1/registry/secret"
test ! -S "$1/admin/control.sock"
test -z "$(ls -A "$1")"
if ( : > "$1/new-file" ) 2>/dev/null; then exit 9; fi
printf 'nested-private-stores-hidden\n'
`
	argv := p.bwrapArgv(dir, dir, os.TempDir(), pathExists, nil, []string{"/bin/sh", "-c", probe, "private-mask-probe", private, public})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = sandboxEnv(p.EnvPass, map[string]string{"HOME": dir, "TMPDIR": os.TempDir()})
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "nested-private-stores-hidden" {
		t.Fatalf("nested private masks broke command setup or isolation: %v\n%s", err, out)
	}
	if contents, err := os.ReadFile(filepath.Join(admin, "secret")); err != nil || string(contents) != "synthetic-private-canary" {
		t.Fatalf("host private data changed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(private, "new-file")); !os.IsNotExist(err) {
		t.Fatalf("sandbox wrote through the private mask: %v", err)
	}
}

// The deny paths are the half of the boundary that protects the processes
// *outside* the sandbox: the agent's own checkpoint commits, and the web
// terminal's interactive shell, both of which consume files the workspace can
// otherwise be talked into writing. A poisoned hooks directory or .bashrc is
// payload execution outside confinement — the measured CVE-2026-26268 chain.
// This builds the whole trap and asserts none of its doors open, from a
// command running inside the sandbox.
func TestSandboxDenyPathsHoldFromInside(t *testing.T) {
	requireSandbox(t)
	dir := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	mk := func(base, rel, content string) {
		p := filepath.Join(base, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk(dir, ".git/hooks/pre-commit", "#!/bin/sh\n")
	mk(dir, ".git/config", "[core]\n\trepositoryformatversion = 0\n")
	mk(dir, "AGENTS.md", "stay put\n")
	mk(home, ".bashrc", "# stock\n")
	mk(home, ".gitconfig", "[user]\n\tname = agent\n")
	if err := os.MkdirAll(filepath.Join(home, ".config", "git"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Writable workspace and ordinary reads must keep working, or the denies
	// below would be passing because everything is broken.
	if out, err := runBash(t, dir, "echo ok > plain.txt && cat plain.txt AGENTS.md"); err != nil ||
		!strings.Contains(out, "ok") || !strings.Contains(out, "stay put") {
		t.Fatalf("ordinary writes and reads must work: %v\n%s", err, out)
	}

	// None of the doors may open: not the existing hook, not a new one, not
	// the configs, not the prompt file, not the shell's startup file.
	for cmd, what := range map[string]string{
		"echo x >> .git/hooks/pre-commit":    "existing hook",
		"echo x >  .git/hooks/post-commit":   "new hook in a read-only hooks dir",
		"echo x >> .git/config":              "repo config",
		"echo x >  AGENTS.md":                "the prompt file",
		"echo x >> $HOME/.bashrc":            "the interactive shell's rc file",
		"echo x >> $HOME/.gitconfig":         "the global git config",
		"echo x >  $HOME/.config/git/config": "the XDG git config",
		"rm -f .git/hooks/pre-commit":        "unlinking through the mount",
		"mv .git/hooks .git/hooks-old":       "renaming the mount point away",
		"mv \"$HOME\" \"$HOME-old\"":         "renaming HOME itself away",
	} {
		if out, err := runBash(t, dir, cmd); err == nil {
			t.Errorf("%s: %q succeeded inside the sandbox:\n%s", what, cmd, out)
		}
	}
	// And the files are byte-for-byte what they were — a failed write that
	// still changed something would pass every check above.
	if out, _ := runBash(t, dir,
		"cat .git/hooks/pre-commit .git/config AGENTS.md $HOME/.bashrc $HOME/.gitconfig"); !strings.Contains(
		out, "stay put") || !strings.Contains(out, "# stock") || strings.Contains(out, "x") {
		t.Errorf("a deny path changed content despite the refused writes:\n%s", out)
	}
}
