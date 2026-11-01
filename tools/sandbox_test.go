package tools

// The sandbox's argv and environment are pure functions of a policy, and this is
// where they are pinned. Deliberately platform-independent: the decisions worth
// testing are which paths become writable and which variables cross the
// boundary, and neither needs a Linux kernel to check. Running here means they
// are checked on every developer's machine rather than only in a container.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// allExist is the existence oracle for tests that are not about existence.
func allExist(string) bool { return true }

func argvIndex(argv []string, want ...string) int {
	for i := 0; i+len(want) <= len(argv); i++ {
		if slices.Equal(argv[i:i+len(want)], want) {
			return i
		}
	}
	return -1
}

func TestSandboxModeFromEnv(t *testing.T) {
	for _, tc := range []struct {
		set  string
		want string
	}{
		{"", SandboxDegrade},
		{"enforce", SandboxEnforce},
		{"degrade", SandboxDegrade},
		{"off", SandboxOff},
		{"  ENFORCE  ", SandboxEnforce},
		// A typo must not silently become the strictest setting (that turns a
		// misspelling into an outage) nor the loosest (that turns it into a
		// missing boundary). Degrade plus a complaint is the documented middle.
		{"enfoce", SandboxDegrade},
	} {
		t.Run("POGO_SANDBOX="+tc.set, func(t *testing.T) {
			t.Setenv(EnvSandbox, tc.set)
			if got := SandboxPolicyFromEnv().Mode; got != tc.want {
				t.Errorf("mode = %q, want %q", got, tc.want)
			}
		})
	}
}

// enforce must refuse rather than fall through. This is the whole reason the
// mode exists, and the failure it guards against is silent.
func TestSandboxRefusesOnlyUnderEnforce(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mode        string
		bwrap       string
		unavailable string
		active      bool
		refuses     bool
	}{
		{"enforce with a working sandbox", SandboxEnforce, "/usr/bin/bwrap", "", true, false},
		{"enforce with no bwrap", SandboxEnforce, "", "", false, true},
		{"enforce with bwrap that fails", SandboxEnforce, "/usr/bin/bwrap", "no namespaces", false, true},
		{"degrade with no bwrap", SandboxDegrade, "", "", false, false},
		{"degrade with bwrap that fails", SandboxDegrade, "/usr/bin/bwrap", "no namespaces", false, false},
		{"off with a working sandbox", SandboxOff, "/usr/bin/bwrap", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := SandboxPolicy{Mode: tc.mode, Bwrap: tc.bwrap, Unavailable: tc.unavailable}
			if got := p.Active(); got != tc.active {
				t.Errorf("Active() = %v, want %v", got, tc.active)
			}
			if got := p.Refuses(); got != tc.refuses {
				t.Errorf("Refuses() = %v, want %v", got, tc.refuses)
			}
			if tc.refuses {
				// The message has to name the way out, or the operator's next
				// move is a search rather than a fix.
				msg := p.RefusalError().Error()
				for _, want := range []string{EnvSandbox, SandboxDegrade} {
					if !strings.Contains(msg, want) {
						t.Errorf("refusal message does not mention %q: %s", want, msg)
					}
				}
			}
		})
	}
}

// The env whitelist is the API-key boundary, so it gets an explicit negative
// test rather than only a positive one.
func TestSandboxEnvCarriesTheWhitelistAndNothingElse(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("LANG", "C.UTF-8")
	t.Setenv("JAVA_HOME", "/opt/jdk")
	t.Setenv("KIMI_API_KEY", "sk-should-never-appear")
	t.Setenv("ZHIPU_API_KEY", "also-not")
	t.Setenv("POGO_SESSION_DIR", "/var/lib/agent/sessions")
	t.Setenv("POGO_WEB_TOKEN", "tok")
	// A name that is deliberately not set anywhere, to check that listing a
	// variable does not invent it. Not a plausible one like CAPA_RULES: that is
	// set for real in the deployment this feature was built for, so the
	// assertion below passed on a laptop and failed in the container.
	const unsetName = "POGO_TEST_DEFINITELY_UNSET"
	os.Unsetenv(unsetName)
	t.Setenv(EnvSandboxEnvPass, "JAVA_HOME, "+unsetName+" ,,")

	pass := sandboxEnvPass(os.Getenv(EnvSandboxEnvPass))
	env := sandboxEnv(pass, map[string]string{"HOME": "/home/agent/.eydm", "TMPDIR": "/tmp"})
	joined := strings.Join(env, "\n")

	for _, want := range []string{
		"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "JAVA_HOME=/opt/jdk",
		"HOME=/home/agent/.eydm", "TMPDIR=/tmp",
	} {
		if !slices.Contains(env, want) {
			t.Errorf("env is missing %q\ngot:\n%s", want, joined)
		}
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, unsetName+"=") {
			t.Errorf("%s was unset but appears as %q", unsetName, kv)
		}
	}
	for _, forbidden := range []string{"API_KEY", "POGO_"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("env carries %q into the sandbox:\n%s", forbidden, joined)
		}
	}
	// HOME comes from the policy, not from the process, because the policy is
	// what decided which directory is writable.
	t.Setenv("HOME", "/root")
	env2 := sandboxEnv(pass, map[string]string{"HOME": "/home/agent/.eydm", "TMPDIR": "/tmp"})
	if slices.Contains(env2, "HOME=/root") {
		t.Error("the process's HOME overrode the policy's")
	}
}

func TestWritableSetDedupesPrunesAndOrders(t *testing.T) {
	p := SandboxPolicy{Writable: []string{
		"/home/agent/mal-out", // covered by the workspace below
		"/srv/outputs",
		"relative/path", // not absolute: nothing sensible to bind
		"/srv/outputs/", // same directory spelled differently
	}}
	got := p.writableSet("/home/agent", "/home/agent/.eydm", "/tmp", allExist)
	// Shortest first: /tmp (4), /home/agent (11), /srv/outputs (12),
	// /home/agent/.eydm (17). /home/agent/mal-out is dropped as covered by the
	// workspace, and the trailing-slash spelling of /srv/outputs collapses into
	// the first.
	//
	// HOME stays although the workspace covers it: the three anchors each get a
	// bind of their own, because a bind mount point cannot be renamed away from
	// inside — and the deny binds stacked beneath HOME are only as strong as
	// that anchor. Only deployment extras are pruned when covered.
	want := []string{"/tmp", "/home/agent", "/srv/outputs", "/home/agent/.eydm"}
	if !slices.Equal(got, want) {
		t.Fatalf("writableSet = %v, want %v", got, want)
	}
	// Shortest first, so a parent is always bound before anything beneath it: the
	// reverse order replaces the child's mount with a fresh view of the parent
	// and silently drops the grant.
	for i := 1; i < len(got); i++ {
		if len(got[i]) < len(got[i-1]) {
			t.Errorf("not ordered shortest-first: %v", got)
		}
	}
}

// Binding a path that does not exist makes bwrap create it, and the zero-byte
// leftovers that produces in a git work tree are a known upstream plague. So a
// missing directory is skipped, not bound.
func TestWritableSetSkipsMissingPaths(t *testing.T) {
	p := SandboxPolicy{Writable: []string{"/srv/gone", "/srv/here"}}
	exists := func(s string) bool { return s != "/srv/gone" }
	got := p.writableSet("/home/agent", "/home/agent", "/tmp", exists)
	if slices.Contains(got, "/srv/gone") {
		t.Errorf("bound a path that does not exist: %v", got)
	}
	if !slices.Contains(got, "/srv/here") {
		t.Errorf("dropped a path that does exist: %v", got)
	}
}

func TestBwrapArgvShape(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("KIMI_API_KEY", "sk-should-never-appear")
	p := SandboxPolicy{
		Mode: SandboxDegrade, Bwrap: "/usr/bin/bwrap",
		EnvPass: sandboxEnvPass(""),
	}
	deny := []string{"/home/agent/.git/hooks", "/home/agent/.eydm/.bashrc"}
	argv := p.bwrapArgv("/home/agent", "/home/agent/.eydm", "/tmp", allExist, deny,
		[]string{"bash", "-c", "echo hi"})

	if argv[0] != "/usr/bin/bwrap" {
		t.Errorf("argv[0] = %q, want the bwrap path", argv[0])
	}
	for _, want := range [][]string{
		{"--unshare-user"}, {"--unshare-pid"}, {"--unshare-net"},
		{"--die-with-parent"}, {"--new-session"}, {"--clearenv"},
		{"--ro-bind", "/", "/"}, {"--dev", "/dev"},
		{"--chdir", "/home/agent"},
		{"--bind", "/tmp", "/tmp"},
		{"--bind", "/home/agent", "/home/agent"},
	} {
		if argvIndex(argv, want...) < 0 {
			t.Errorf("argv is missing %v\ngot: %v", want, argv)
		}
	}
	// The read-only root has to come before the writable holes, because bwrap
	// applies operations in order and the last one wins.
	roRoot := argvIndex(argv, "--ro-bind", "/", "/")
	firstBind := argvIndex(argv, "--bind", "/tmp", "/tmp")
	if roRoot < 0 || firstBind < 0 || roRoot > firstBind {
		t.Errorf("--ro-bind / / must precede the writable binds: %v", argv)
	}
	// HOME is inside the workspace in this layout, and it is still bound
	// separately: as an anchor it gets a mount point of its own, which is what
	// keeps `mv .eydm old && mkdir .eydm` from minting a fresh, unprotected HOME.
	homeBind := argvIndex(argv, "--bind", "/home/agent/.eydm", "/home/agent/.eydm")
	if homeBind < 0 {
		t.Errorf("HOME is not bound as its own anchor: %v", argv)
	}
	// The deny paths are read-only self-binds, stacked after every writable bind
	// so they actually cover it.
	for _, d := range deny {
		i := argvIndex(argv, "--ro-bind", d, d)
		if i < 0 {
			t.Errorf("deny path %s is not bound: %v", d, argv)
		} else if i < homeBind {
			t.Errorf("deny path %s comes before the writable binds and loses to them: %v", d, argv)
		}
	}
	// Everything after -- is the command, unmodified.
	sep := argvIndex(argv, "--")
	if sep < 0 || !slices.Equal(argv[sep+1:], []string{"bash", "-c", "echo hi"}) {
		t.Errorf("the command after -- is not what was passed: %v", argv)
	}
	if strings.Contains(strings.Join(argv, " "), "sk-should-never-appear") {
		t.Errorf("an API key reached the argv: %v", argv)
	}
	// --proc is a probed capability, absent unless the probe said yes.
	if argvIndex(argv, "--proc", "/proc") >= 0 {
		t.Errorf("--proc appeared without Proc being set: %v", argv)
	}
	withProc := p
	withProc.Proc = true
	if argvIndex(withProc.bwrapArgv("/home/agent", "/home/agent", "/tmp", allExist, nil, []string{"true"}),
		"--proc", "/proc") < 0 {
		t.Error("Proc was set but --proc is missing")
	}
}

// Network confinement is the half that turns reading a secret into leaking it,
// so the flag's presence is tied to the policy in both directions.
func TestBwrapArgvNetwork(t *testing.T) {
	p := SandboxPolicy{Mode: SandboxDegrade, Bwrap: "bwrap", EnvPass: sandboxEnvPass("")}
	denied := p.bwrapArgv("/w", "/w", "/tmp", allExist, nil, []string{"true"})
	if argvIndex(denied, "--unshare-net") < 0 {
		t.Error("network is denied by default but --unshare-net is missing")
	}
	p.Net = true
	allowed := p.bwrapArgv("/w", "/w", "/tmp", allExist, nil, []string{"true"})
	if argvIndex(allowed, "--unshare-net") >= 0 {
		t.Error("POGO_SANDBOX_NET=1 must not unshare the network namespace")
	}
}

// The same policy has to produce the same argv every time; an argv that reorders
// itself between identical calls is unreviewable before it is anything else.
func TestBwrapArgvIsDeterministic(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("LANG", "C")
	t.Setenv("TZ", "Asia/Shanghai")
	p := SandboxPolicy{
		Mode: SandboxDegrade, Bwrap: "bwrap",
		EnvPass:  sandboxEnvPass("JAVA_HOME,CAPA_RULES"),
		Writable: []string{"/srv/a", "/srv/b"},
	}
	first := p.bwrapArgv("/w", "/h", "/tmp", allExist, nil, []string{"true"})
	for i := 0; i < 5; i++ {
		if got := p.bwrapArgv("/w", "/h", "/tmp", allExist, nil, []string{"true"}); !slices.Equal(got, first) {
			t.Fatalf("argv changed between identical calls:\n%v\n%v", first, got)
		}
	}
}

func TestUnderPathIsCaseSensitive(t *testing.T) {
	if !underPath("/home/agent/mal-out", "/home/agent") {
		t.Error("a child must be reported as under its parent")
	}
	if underPath("/home/agentx", "/home/agent") {
		t.Error("a sibling sharing a prefix must not count as under it")
	}
	// Case folding here would prune a genuinely distinct directory as already
	// covered, leaving the sandbox without a grant it was told to make.
	if underPath("/home/Agent/out", "/home/agent") {
		t.Error("comparison must be case-sensitive: these are two directories on Linux")
	}
}

// The deny list is what stands between a poisoned hooks directory and the
// unsandboxed processes that would run it, so its membership is pinned exactly:
// what must be there, and what must not be invented.
func TestSandboxDenyPaths(t *testing.T) {
	present := map[string]bool{
		"/home/agent/AGENTS.md":         true,
		"/home/agent/.eydm/.bashrc":     true,
		"/home/agent/.eydm/.gitconfig":  true,
		"/home/agent/.eydm/.config/git": true,
		"/home/agent/.git/hooks":        true,
		"/home/agent/.git/config":       true,
		"/home/agent/sub/.git/hooks":    true,
		"/home/agent/sub/.git/config":   true,
	}
	exists := func(p string) bool { return present[p] }
	got := sandboxDenyPaths("/home/agent", "/home/agent/.eydm",
		[]string{"/home/agent/.git", "/home/agent/sub/.git"}, exists)

	for _, want := range []string{
		"/home/agent/AGENTS.md",
		"/home/agent/.eydm/.bashrc",
		"/home/agent/.eydm/.gitconfig",
		"/home/agent/.eydm/.config/git",
		"/home/agent/.git/hooks",
		"/home/agent/.git/config",
		"/home/agent/sub/.git/hooks",
		"/home/agent/sub/.git/config",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("deny list is missing %s\ngot: %v", want, got)
		}
	}
	// CLAUDE.md and the files that do not exist must not appear: binding a
	// missing path makes bwrap create it, which is the ghost-file plague the
	// exists check exists to prevent.
	for _, unwanted := range []string{
		"/home/agent/CLAUDE.md",
		"/home/agent/.eydm/.zshrc",
		"/home/agent/.eydm/.profile",
	} {
		if slices.Contains(got, unwanted) {
			t.Errorf("deny list names %s, which does not exist: %v", unwanted, got)
		}
	}
	if !slices.IsSorted(got) {
		t.Errorf("deny list is not sorted, so the argv is not deterministic: %v", got)
	}
}

// A .git that is itself under a scanned anchor must be found without the walk
// paying for the repository's whole metadata tree or the world's node_modules.
func TestFindGitDirs(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string) {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mk(".git/hooks")
	mk("sub/mod/.git/hooks")
	mk("sub/mod/.git/objects/ab")
	mk("deep/a/b/c/.git")  // depth 4: beyond the bound
	mk("node_modules/x/y") // walked, but contains nothing

	got := findGitDirs(root, 3)
	slash := make([]string, 0, len(got))
	for _, g := range got {
		rel, err := filepath.Rel(root, g)
		if err != nil {
			t.Fatal(err)
		}
		slash = append(slash, filepath.ToSlash(rel))
	}
	for _, want := range []string{".git", "sub/mod/.git"} {
		if !slices.Contains(slash, want) {
			t.Errorf("findGitDirs missed %s\ngot: %v", want, slash)
		}
	}
	if slices.Contains(slash, "deep/a/b/c/.git") {
		t.Errorf("findGitDirs exceeded its depth bound: %v", slash)
	}
	// The walk must not look inside a .git it already found: if it did, a
	// nested .git under .git/modules would show up here, and the walk would be
	// paying for the largest tree in every checkout.
	for _, g := range slash {
		if strings.Count(g, ".git") > 1 {
			t.Errorf("walk descended into a .git directory: %v", slash)
		}
	}
	if got := findGitDirs("/", 3); got != nil {
		t.Errorf("walking / is declined, got: %v", got)
	}
}

// The runtime override wins over the deployment's env, and only over the env:
// probe facts (Bwrap present, Unavailable) are machine facts it must not
// rewrite. An invalid value is rejected rather than silently accepted,
// because a security setting must not shrug at a typo.
func TestSandboxOverrideWinsOverEnv(t *testing.T) {
	t.Cleanup(func() { _ = SetSandboxOverride("") })

	base := SandboxPolicy{Mode: SandboxEnforce, Bwrap: "/usr/bin/bwrap"}
	if !applyOverride(base).Active() {
		t.Fatal("with no override the env policy decides")
	}
	if err := SetSandboxOverride(SandboxOff); err != nil {
		t.Fatal(err)
	}
	if got := applyOverride(base); got.Active() || got.Mode != SandboxOff {
		t.Errorf("override off must silence the sandbox: %+v", got)
	}
	// The override cannot invent a working sandbox, only change the mode:
	// degrade with no bwrap is unconfined but must not refuse.
	if err := SetSandboxOverride(SandboxDegrade); err != nil {
		t.Fatal(err)
	}
	got := applyOverride(SandboxPolicy{Mode: SandboxEnforce, Unavailable: "no bwrap"})
	if got.Active() {
		t.Errorf("override must not rewrite probe facts: %+v", got)
	}
	if got.Refuses() {
		t.Errorf("override degrade must not refuse where enforce would: %+v", got)
	}
	if err := SetSandboxOverride("paranoid"); err == nil {
		t.Error("an unknown mode must be rejected")
	}
	if SandboxOverride() != SandboxDegrade {
		t.Error("a rejected mode must not clobber the installed one")
	}
}
