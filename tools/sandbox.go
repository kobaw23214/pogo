package tools

// OS-level sandboxing for the two tools that run external commands: bash and the
// manifest-declared SynthTools. Everything else in this package is in-process Go
// confined by resolve(); these two hand a command line to the kernel, and until
// now the only boundary around them was the container.
//
// The gap is documented in docs/harness-design.md, which names an OS sandbox as
// the one missing piece that would change pogo's security properties rather
// than its ergonomics, and sets its own acceptance test: `grep -ri
// 'landlock\|seccomp\|seatbelt'` returning nothing. This file is that grep
// finally matching.
//
// Why bubblewrap rather than Landlock
// -----------------------------------
// Landlock restricts *the calling process*. Applying it to a child needs the
// ruleset installed between fork and exec, which Go does not expose (see
// golang/go#68595), and Go's thread model makes per-thread application unsafe
// (golang/go#70993). bwrap is a separate binary that does the whole setup for
// us, which is also what Codex settled on for Linux after starting from
// Landlock.
//
// What this file is and is not
// ---------------------------
// It is the *policy and argv* half: pure functions over strings, so the argv a
// given policy produces can be tested anywhere, including on a developer's
// Windows machine where no sandbox exists. The half that touches the OS —
// probing whether bwrap works at all, and rewriting an *exec.Cmd — is in
// sandbox_linux.go, with a no-op twin in sandbox_other.go.
//
// The threat model is not "contain hostile native code". bwrap shares the host
// kernel; a determined exploit of a kernel bug walks out. What it does contain
// is everything one layer up: a command that writes outside the workspace, reads
// a credential file, or dials home — the actual consequences of prompt
// injection, which for this deployment arrives inside the malware strings,
// decompiler output and sandbox logs the model is asked to read.

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Paths inside the sandbox are handled with the path package, not filepath, and
// that is a correctness choice rather than a style one. These are Linux paths
// destined for a Linux binary's argv, so slash semantics are the right ones
// everywhere — including on a developer's Windows machine, where
// filepath.IsAbs("/home/agent") is false and every one of these functions would
// silently discard its input. Using path also makes them testable on that
// machine, which is where the argv these produce actually gets reviewed.

// Sandbox mode names. Set through EnvSandbox.
const (
	// SandboxEnforce refuses to run a command when the sandbox is unavailable.
	// The production setting: a deployment that wanted containment and silently
	// stopped having it is worse off than one that fails loudly, because the
	// first kind is discovered by an incident.
	SandboxEnforce = "enforce"
	// SandboxDegrade uses the sandbox when it works and falls back to running
	// unconfined, with a warning, when it does not. The default, because pogo
	// also runs on macOS and on Linux hosts where unprivileged user namespaces
	// are switched off, and turning those into hard failures would be a
	// breaking change dressed up as a security improvement.
	SandboxDegrade = "degrade"
	// SandboxOff never sandboxes. Kept as an explicit escape hatch so that
	// "turn it off" is a recorded decision rather than an undocumented
	// side-effect of an unset variable.
	SandboxOff = "off"
)

const (
	// EnvSandbox selects the mode above.
	EnvSandbox = "POGO_SANDBOX"
	// EnvSandboxNet leaves the network reachable from inside the sandbox when
	// set to 1. Off by default: filesystem confinement without network
	// confinement leaves the return path open, which is the half that turns a
	// read of a credential into a leak of it.
	EnvSandboxNet = "POGO_SANDBOX_NET"
	// EnvSandboxEnvPass names extra environment variables to carry in, comma
	// separated. Deployment-specific tool chains need their own (JAVA_HOME,
	// CAPA_RULES); this package should not have to know about Ghidra to let a
	// deployment use it.
	EnvSandboxEnvPass = "POGO_SANDBOX_ENV"
	// EnvSandboxWritable names extra directories the sandbox may write to,
	// os.PathListSeparator separated. The workspace, HOME and TMPDIR are always
	// writable; this is for a deployment that keeps outputs somewhere else.
	EnvSandboxWritable = "POGO_SANDBOX_WRITABLE"
)

// sandboxEnvAlways are carried into every sandbox. Deliberately short.
//
// The list is what a shell command needs to behave like one — where to find
// binaries, where home is, where scratch space is, and how to render text — and
// nothing that describes the agent. No POGO_*: those name the session directory,
// the users file and the panel store, all of which are agent state the command
// has no business locating. No *_API_KEY, which is the whole point.
var sandboxEnvAlways = []string{
	"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "TERM", "TZ",
}

// sandboxOverride is the process-wide runtime override of the deployment's
// mode, set through the settings dialog (PUT /api/sandbox) and restored at
// startup from prefs.json. It is a deliberate escape hatch, and two things
// keep it honest: it is logged every time it changes, and the exemption that
// rests on the sandbox (readOnlyHint's auto-allow) collapses the moment the
// override takes the boundary away.
var sandboxOverride atomic.Value // string: "", SandboxEnforce, SandboxDegrade, SandboxOff

// SetSandboxOverride installs (or, with "", clears) the runtime mode
// override. Anything else is a caller's typo and rejected, because a security
// setting must not silently accept a value it does not understand.
func SetSandboxOverride(mode string) error {
	switch mode {
	case "", SandboxEnforce, SandboxDegrade, SandboxOff:
		sandboxOverride.Store(mode)
		return nil
	}
	return fmt.Errorf("sandbox override must be empty or %s|%s|%s, got %q",
		SandboxEnforce, SandboxDegrade, SandboxOff, mode)
}

// SandboxOverride is the installed runtime override, "" when none.
func SandboxOverride() string {
	if v := sandboxOverride.Load(); v != nil {
		return v.(string)
	}
	return ""
}

// applyOverride resolves the effective policy: the runtime override wins over
// the deployment's env when one is installed. Probe results (Bwrap, Proc,
// Unavailable) are untouched — they are facts about the machine, not the
// mode. Kept pure so the resolution is testable without a probe.
func applyOverride(p SandboxPolicy) SandboxPolicy {
	if ov := SandboxOverride(); ov != "" {
		p.Mode = ov
	}
	return p
}

// SandboxPolicy is one session's containment, built once and reused for every
// command in it.
//
// Session-scoped rather than per-command because the answer does not change
// between commands and probing bwrap on each one would put a fork in front of
// every tool call. Per-command is the *sandbox instance*, not the policy: each
// command gets a fresh set of namespaces that is torn down when it exits, which
// is what makes "no state survives a command" structural instead of a promise.
type SandboxPolicy struct {
	// Mode is one of the three constants above.
	Mode string
	// Bwrap is the resolved path to the bubblewrap binary. Empty when it was
	// not found, which is what Mode has to answer for.
	Bwrap string
	// Unavailable explains why the sandbox cannot be used. Non-empty means
	// enforce must refuse and degrade must warn.
	Unavailable string
	// Net leaves the network reachable.
	Net bool
	// Proc says whether a private /proc can be mounted. Probed rather than
	// assumed: whether the kernel allows it inside a container with a masked
	// /proc varies by kernel and Docker version, and it was measured going both
	// ways on two machines. Without it the sandbox still gets its own PID
	// namespace, but /proc shows the outer container's processes.
	Proc bool
	// Writable are the directories the command may modify. Everything else is
	// read-only.
	Writable []string
	// EnvPass are the variable names carried into the sandbox.
	EnvPass []string
	// PrivateDirs must be invisible, including Unix sockets (read-only mounts
	// do not prevent connecting to a socket). No unconfined fallback is allowed.
	PrivateDirs []string
}

// Deployment mode and machine capabilities have different lifetimes. An off
// baseline must not cache "no bwrap" forever: an operator can enable confinement
// later through the settings dialog, including an override restored at startup.
type sandboxPolicyCache struct {
	baselineOnce sync.Once
	baseline     SandboxPolicy
	probeOnce    sync.Once
	facts        atomic.Pointer[sandboxProbeFacts]
}

type sandboxProbeFacts struct {
	bwrap, unavailable string
	proc               bool
}

func (c *sandboxPolicyCache) resolve(read func() SandboxPolicy, probe func(SandboxPolicy) SandboxPolicy) SandboxPolicy {
	c.baselineOnce.Do(func() { c.baseline = read() })
	p := applyOverride(c.baseline)
	if p.Mode != SandboxOff {
		c.probeOnce.Do(func() {
			observed := probe(p)
			c.facts.Store(&sandboxProbeFacts{bwrap: observed.Bwrap, unavailable: observed.Unavailable, proc: observed.Proc})
		})
	}
	if facts := c.facts.Load(); facts != nil {
		p.Bwrap, p.Unavailable, p.Proc = facts.bwrap, facts.unavailable, facts.proc
	}
	return p
}

// SandboxPolicyFromEnv reads the mode and the deployment's additions. It does
// not probe anything: the caller fills Bwrap/Unavailable/Proc, because those
// need to run a process and this has to stay testable everywhere.
func SandboxPolicyFromEnv() SandboxPolicy {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv(EnvSandbox)))
	switch mode {
	case SandboxEnforce, SandboxDegrade, SandboxOff:
	case "":
		mode = SandboxDegrade
	default:
		// An unreadable value is a deployment mistake, and the safe reading of a
		// mistake in a security setting is the stricter one — but silently
		// picking enforce would turn a typo into a total outage. Degrade plus a
		// visible complaint is the honest middle: containment still applies
		// wherever it can.
		fmt.Fprintf(os.Stderr, "pogo: %s=%q is not one of %s|%s|%s; using %s\n",
			EnvSandbox, mode, SandboxEnforce, SandboxDegrade, SandboxOff, SandboxDegrade)
		mode = SandboxDegrade
	}
	return SandboxPolicy{
		Mode:        mode,
		Net:         os.Getenv(EnvSandboxNet) == "1",
		EnvPass:     sandboxEnvPass(os.Getenv(EnvSandboxEnvPass)),
		Writable:    splitPathList(os.Getenv(EnvSandboxWritable)),
		PrivateDirs: runtimeSkillPrivateDirs(),
	}
}

func runtimeSkillPrivateDirs() []string {
	var dirs []string
	add := func(dir string) {
		abs, err := filepath.Abs(dir)
		if err == nil {
			dir = abs
		}
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			dir = resolved
		}
		dirs = append(dirs, filepath.ToSlash(dir))
	}
	if store := strings.TrimSpace(os.Getenv("POGO_SKILL_RUNTIME_STORE")); store != "" {
		add(store)
	}
	for _, dir := range registeredHostPrivateDirs() {
		add(dir)
	}
	return dirs
}

// ProtectRuntimeSkillHostCommand applies the same private-store masking to a human
// terminal so installed runtimes and private runtimeSkill data stay inaccessible.
func ProtectRuntimeSkillHostCommand(cmd *exec.Cmd) error {
	if len(runtimeSkillPrivateDirs()) == 0 {
		return nil
	}
	return wrapCommand(cmd)
}

// sandboxEnvPass merges the always-carried names with a deployment's extras.
func sandboxEnvPass(extra string) []string {
	var out []string
	seen := make(map[string]bool, len(sandboxEnvAlways))
	for _, n := range append(append([]string{}, sandboxEnvAlways...), strings.Split(extra, ",")...) {
		if n = strings.TrimSpace(n); n != "" && !seen[n] && !isHostControlSecretEnv(n) {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

func splitPathList(s string) []string {
	var out []string
	for _, p := range filepath.SplitList(s) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Active reports whether commands should be wrapped.
func (p SandboxPolicy) Active() bool {
	return p.Mode != SandboxOff && p.Unavailable == "" && p.Bwrap != ""
}

// Refuses reports whether a command must be rejected outright: enforce was
// asked for and the sandbox is not there.
func (p SandboxPolicy) Refuses() bool {
	if len(p.PrivateDirs) > 0 && !p.Active() {
		return true
	}
	return p.Mode == SandboxEnforce && (p.Unavailable != "" || p.Bwrap == "")
}

// RefusalError is what a refused command returns. It says what to change,
// because a message the operator cannot act on becomes three more attempts at
// the same thing.
func (p SandboxPolicy) RefusalError() error {
	reason := p.Unavailable
	if p.Mode == SandboxOff {
		reason = "the effective " + EnvSandbox + " mode is off"
	} else if reason == "" && p.Bwrap == "" {
		reason = "bubblewrap (bwrap) was not found on PATH"
	}
	if len(p.PrivateDirs) > 0 && !p.Active() {
		return &sandboxUnavailableError{
			mode: p.Mode, reason: reason, requiredProtection: "private_host_storage",
			recovery: "Ask the platform administrator to enable a working OS sandbox; after repairing runtime dependencies or namespace permissions, restart the host service to refresh its capability probe. " +
				"Private host and Skill storage cannot be exposed by disabling confinement. Do not retry this command unchanged or route it through another shell or subagent.",
		}
	}
	return &sandboxUnavailableError{
		mode: p.Mode, reason: reason, requiredProtection: "enforced_sandbox",
		recovery: fmt.Sprintf("Install working bubblewrap with the required namespaces and restart the host service to refresh its capability probe, or have the administrator explicitly set %s=%s to run unconfined with a warning where private host storage does not require isolation.", EnvSandbox, SandboxDegrade),
	}
}

// sandboxEnv builds the environment for a sandboxed command, drawn from the
// current process's but keeping only the named variables.
//
// Two things have to be set, and setting only one of them looks like it works.
// bwrap's --clearenv clears the environment of the processes bwrap *spawns* —
// but bwrap itself is spawned by this process with the full environment, and
// bwrap is PID 1 inside its own sandbox, so /proc/1/environ is readable by the
// sandboxed command at the same uid. Measured: with --clearenv alone, an API-key
// canary is still recoverable from /proc/1/environ; with the launcher's
// environment filtered as well, it is not. So the whitelist is applied twice, to
// the argv (--setenv) and to bwrap's own environment (this function).
//
// overrides win over the process's values, for the ones the sandbox decides
// rather than inherits (HOME and TMPDIR when the policy relocates them).
func sandboxEnv(pass []string, overrides map[string]string) []string {
	keep := make(map[string]bool, len(pass))
	for _, n := range pass {
		if !isHostControlSecretEnv(n) {
			keep[n] = true
		}
	}
	var out []string
	for _, kv := range os.Environ() {
		i := strings.IndexByte(kv, '=')
		if i < 0 {
			continue
		}
		name := kv[:i]
		if !keep[name] || isHostControlSecretEnv(name) {
			continue
		}
		if _, shadowed := overrides[name]; shadowed {
			continue
		}
		out = append(out, kv)
	}
	names := make([]string, 0, len(overrides))
	for n := range overrides {
		if !isHostControlSecretEnv(n) {
			names = append(names, n)
		}
	}
	// Sorted so the same policy produces the same environment every time; an
	// argv or env that reorders itself between identical calls is a testing
	// nightmare before it is anything else.
	sort.Strings(names)
	for _, n := range names {
		out = append(out, n+"="+overrides[n])
	}
	return out
}

// bwrapArgv assembles the command line. Pure: same inputs, same output, no
// filesystem access — which is what lets the interesting decisions be tested
// without a Linux box.
//
// exists is injected rather than called directly for the same reason. It reports
// whether a path is present, and it is consulted before binding anything:
// binding a path that does not exist makes bwrap *create* it, and the zero-byte
// read-only files that leaves behind in a git work tree are a documented plague
// upstream — they show up in `git status`, resist `rm` with "device or resource
// busy", and here they would land in the workspace whose diff a person reviews.
func (p SandboxPolicy) bwrapArgv(workdir, home, tmpdir string, exists func(string) bool, deny []string, argv []string, protection ...[]string) []string {
	a := []string{p.Bwrap,
		// A user namespace is what makes the rest possible without privilege;
		// the others are cheap and remove whole categories of reach.
		//
		// cgroup gets the -try form because it is the one that can be absent on
		// an otherwise fine kernel, and losing the whole sandbox over the least
		// valuable of the five namespaces would be a bad trade.
		"--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts",
		"--unshare-cgroup-try",
		// Tie the sandbox's lifetime to ours. --die-with-parent alone is not
		// enough: bubblewrap#529 spells out that it does not kill spawned
		// processes unless the PID namespace is unshared too, which it is above.
		// The existing process-group SIGKILL stays as the inner backstop.
		"--die-with-parent",
		// Detach from the controlling terminal so a sandboxed process cannot
		// push characters back into it with TIOCSTI.
		"--new-session",
	}
	if !p.Net {
		a = append(a, "--unshare-net")
	}
	// Read-only view of the whole filesystem first, then writable holes punched
	// through it. The order matters: bwrap applies operations in sequence, so a
	// later, more specific bind wins over this one.
	a = append(a, "--ro-bind", "/", "/")
	// A minimal /dev. The sandbox needs /dev/null and /dev/urandom; it does not
	// need the container's device nodes.
	a = append(a, "--dev", "/dev")
	if p.Proc {
		a = append(a, "--proc", "/proc")
	} else if len(p.PrivateDirs) > 0 {
		// A shared /proc/PID/root would reveal the host's unmasked mounts.
		a = append(a, "--tmpfs", "/proc", "--remount-ro", "/proc")
	}
	for _, dir := range p.writableSet(workdir, home, tmpdir, exists) {
		a = append(a, "--bind", dir, dir)
	}
	for _, mounts := range protection {
		a = append(a, mounts...)
	}
	// The deny paths come after every writable bind: bwrap applies operations
	// in order, so a read-only bind stacked on top of the writable one is what
	// makes "writable workspace, immutable hooks directory" expressible at all.
	// Each one binds the path onto itself read-only rather than masking it with
	// /dev/null — the model keeps being able to *read* AGENTS.md or .git/config,
	// it just cannot write, replace or delete them, because a bind mount point
	// answers unlink and rename with EBUSY and writes with EROFS.
	for _, dir := range deny {
		a = append(a, "--ro-bind", dir, dir)
	}
	for _, dir := range privateMaskRoots(p.PrivateDirs, exists) {
		a = append(a, "--tmpfs", dir, "--remount-ro", dir)
	}
	a = append(a, "--chdir", workdir)
	// --clearenv then --setenv, rather than trusting the inherited environment:
	// see sandboxEnv for why both halves are needed.
	a = append(a, "--clearenv")
	env := sandboxEnv(p.EnvPass, map[string]string{"HOME": home, "TMPDIR": tmpdir})
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 {
			a = append(a, "--setenv", kv[:i], kv[i+1:])
		}
	}
	return append(append(a, "--"), argv...)
}

// Masking a directory hides all of its descendants, including nested mounts.
// Keep only the outermost existing roots: after an empty parent tmpfs becomes
// read-only, bubblewrap cannot create a second mask's child mountpoint in it.
func privateMaskRoots(dirs []string, exists func(string) bool) []string {
	seen := make(map[string]bool, len(dirs))
	var candidates []string
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		dir = path.Clean(dir)
		if !seen[dir] && exists(dir) {
			seen[dir] = true
			candidates = append(candidates, dir)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if len(candidates[i]) != len(candidates[j]) {
			return len(candidates[i]) < len(candidates[j])
		}
		return candidates[i] < candidates[j]
	})
	var roots []string
	for _, dir := range candidates {
		covered := false
		for _, root := range roots {
			if underPath(dir, root) {
				covered = true
				break
			}
		}
		if !covered {
			roots = append(roots, dir)
		}
	}
	return roots
}

// writableSet is the deduplicated, existence-checked, specificity-ordered list
// of directories the command may write to.
//
// Ordered shortest path first so that a parent is bound before its children.
// Binding /home/agent after /home/agent/.eydm would replace the second mount
// with a fresh view of the first and silently undo it, which is the kind of bug
// that looks like a permissions problem for an afternoon.
//
// The three anchors — workdir, home and tmpdir — each get a bind of their own
// even when one covers another (a HOME inside the workspace is the ordinary
// case). That is not redundancy: a bind mount point cannot be unlinked or
// renamed away from inside the sandbox, and the deny paths below are only as
// strong as the anchors above them. If HOME were reached through the workspace
// bind instead of being one, `mv .eydm old && mkdir .eydm` would give the
// command a fresh HOME with a writable .bashrc in it — and the interactive
// shell that sources that file lives outside the sandbox.
//
// Deployment extras covered by an anchor are still pruned: they add argv noise
// without adding an anchor anyone depends on.
func (p SandboxPolicy) writableSet(workdir, home, tmpdir string, exists func(string) bool) []string {
	seen := make(map[string]bool, len(p.Writable)+3)
	var out []string
	add := func(dir string, anchor bool) {
		if dir == "" || !path.IsAbs(dir) {
			return
		}
		dir = path.Clean(dir)
		if seen[dir] || !exists(dir) {
			return
		}
		if !anchor {
			for _, prev := range out {
				if underPath(dir, prev) {
					return
				}
			}
		}
		seen[dir] = true
		out = append(out, dir)
	}
	for _, dir := range []string{workdir, home, tmpdir} {
		add(dir, true)
	}
	for _, dir := range p.Writable {
		add(dir, false)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) < len(out[j])
		}
		return out[i] < out[j]
	})
	return out
}

// underPath reports whether child is root or sits beneath it, in slash
// semantics.
//
// Not tool.go's within(): that one is filepath-based and case-folds for the
// benefit of macOS and Windows volumes, neither of which is where these paths
// are interpreted. A case-insensitive comparison here would prune /home/Agent as
// covered by /home/agent and produce a sandbox missing a writable directory it
// was told to grant.
func underPath(child, root string) bool {
	child, root = path.Clean(child), path.Clean(root)
	if child == root {
		return true
	}
	if root == "/" {
		return true
	}
	return strings.HasPrefix(child, root+"/")
}

// promptFiles are read into the system prompt from the working directory on
// every run, by the agent process itself. A command that can rewrite them
// rewrites its own instructions, and the next subagent or scheduled run is a
// fresh process that picks the new version up. Read into the prompt by
// agent/prompt.go; kept readable but immutable inside the sandbox.
var promptFiles = []string{"AGENTS.md", "CLAUDE.md"}

// homeDenyFiles are the files in HOME that a later, less confined consumer
// reads. The sandbox's writable regions are also where the deployment's
// unsandboxed processes live: the web terminal is an interactive shell that
// sources .bashrc on startup, and any git the human runs there reads the global
// and XDG configs. A command that writes those files inside the sandbox gets
// its payload executed outside it. Only existing files are listed — binding a
// missing one would make bwrap create it (see bwrapArgv's exists argument).
//
// prefs.json is here for the mirror-image reason: it holds the runtime
// sandbox override, and a settings file the sandbox can rewrite is an escape
// hatch with extra steps. The main process creates it early at startup, so it
// exists to be bound.
var homeDenyFiles = []string{
	".bashrc", ".bash_profile", ".bash_logout",
	".profile", ".zshrc", ".zprofile",
	".gitconfig",
	".pogo/prefs.json",
}

// homeDenyDirs are the HOME directories that must not gain entries: git's XDG
// config location. Binding the directory read-only blocks creation inside it,
// which is the hole binding single files cannot close.
var homeDenyDirs = []string{".config/git"}

// sandboxDenyPaths assembles the paths a sandboxed command must not modify,
// drawn from the writable anchors so nothing already read-only is bound twice.
// gitDirs holds the .git directories found under the anchors (findGitDirs);
// their hooks directories and config files are what a command would poison to
// be run by the next git operation outside the sandbox — the CVE-2026-26268
// chain, which is a measured escape here, not a citation.
//
// Sorted and deduplicated: the same filesystem produces the same argv, and a
// HOME inside the workspace turns up the same .git from both walks.
func sandboxDenyPaths(workdir, home string, gitDirs []string, exists func(string) bool) []string {
	seen := make(map[string]bool, len(gitDirs)*2+len(homeDenyFiles)+len(promptFiles))
	var out []string
	add := func(p string) {
		p = path.Clean(p)
		if seen[p] || !exists(p) {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, f := range promptFiles {
		add(path.Join(workdir, f))
	}
	if home != "" {
		for _, f := range homeDenyFiles {
			add(path.Join(home, f))
		}
		for _, d := range homeDenyDirs {
			add(path.Join(home, d))
		}
	}
	for _, g := range gitDirs {
		add(path.Join(g, "hooks"))
		add(path.Join(g, "config"))
	}
	sort.Strings(out)
	return out
}

// findGitDirs locates .git directories within maxDepth of root, so their hooks
// and config can be deny-bound. Depth-bounded rather than exhaustive for the
// same reason srt's scan is: the walk runs per command, and three levels covers
// the workspace root, its modules and their vendored checkouts without paying
// for the whole tree.
//
// This walks host paths with filepath, not the slash semantics of the rest of
// the file: it runs before the sandbox exists, on the real filesystem. The
// paths it returns are the same strings on Linux, where it matters.
//
// A .git that is a file (a worktree's gitdir pointer) is skipped rather than
// followed: its real metadata lives wherever the pointer says, which may be
// anywhere. Walking "/" is declined for the obvious reason.
func findGitDirs(root string, maxDepth int) []string {
	if root == "" || root == "/" || maxDepth < 1 {
		return nil
	}
	root = filepath.Clean(root)
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		if strings.Count(filepath.ToSlash(rel), "/")+1 > maxDepth {
			return filepath.SkipDir
		}
		if d.Name() == ".git" {
			out = append(out, p)
			// A repository's metadata is never a place to look for more
			// repositories, and it is the largest tree in any checkout.
			return filepath.SkipDir
		}
		return nil
	})
	return out
}

// sandboxTmpDir is where a sandboxed command's scratch space lives.
//
// The container's own /tmp, bound read-write, rather than a fresh tmpfs per
// command. A per-command tmpfs is the tidier idea and it breaks real usage: a
// model writes an intermediate file in one call and reads it in the next, since
// each bash call is already documented as a fresh shell, and /tmp is the obvious
// place to leave something. It would also put Ghidra's project directory — which
// reaches gigabytes on a large sample — in memory, charged against the
// container's memory limit.
func sandboxTmpDir() string {
	if d := strings.TrimSpace(os.Getenv("TMPDIR")); d != "" && path.IsAbs(d) {
		return path.Clean(d)
	}
	return "/tmp"
}

// sandboxHome is HOME, or the workspace when HOME is unset or relative. A
// sandbox whose HOME points at a path it cannot write is worse than one with an
// odd HOME: the tools that keep dot-file state (Ghidra writes preferences, pip
// writes a cache) fail in ways that read as tool bugs.
func sandboxHome(workdir string) string {
	if h := strings.TrimSpace(os.Getenv("HOME")); h != "" && path.IsAbs(h) {
		return path.Clean(h)
	}
	return workdir
}
