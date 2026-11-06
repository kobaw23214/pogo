//go:build linux

package tools

// The OS-touching half of the sandbox: find bubblewrap, prove it works, and
// rewrite a command to run inside it. The policy and argv construction live in
// sandbox.go, which is portable so the interesting decisions stay testable on a
// machine with no sandbox at all.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

var (
	sandboxPolicies sandboxPolicyCache
	sandboxReport   sync.Once
)

// probeTimeout bounds the startup check. bwrap setup is milliseconds; anything
// approaching this means the environment is pathological and the answer we want
// is "unavailable", not a hung startup.
const probeTimeout = 10 * time.Second

// InitSandbox resolves the effective policy and probes bubblewrap on the first
// enabled policy, caching machine facts independently of the deployment mode.
//
// It reports the outcome on stderr once, which is the point of doing it here
// rather than lazily inside the first command. Codex learned the same thing:
// a sandbox that cannot initialise should say so while someone is still
// watching the startup output, not on tool call forty when the failure reads
// like a broken command. In enforce mode this is the difference between "the
// deployment refused to start commands" and "the deployment ran forty commands
// unconfined".
func InitSandbox() SandboxPolicy {
	policy := sandboxPolicies.resolve(SandboxPolicyFromEnv, func(p SandboxPolicy) SandboxPolicy {
		p.PrivateDirs = runtimeSkillPrivateDirs()
		return probeSandbox(p)
	})
	policy.PrivateDirs = runtimeSkillPrivateDirs()
	sandboxReport.Do(func() { reportSandbox(policy) })
	return policy
}

// probeSandbox fills in Bwrap, Proc and Unavailable.
//
// The trial runs the *real* argv with /bin/true as the command rather than a
// hand-written minimal one. That matters: a probe that tests a different flag
// set than the one commands will use can pass while every command fails, and the
// flags most likely to be rejected (the namespace ones) are exactly the ones a
// simplified probe tends to leave out.
func probeSandbox(p SandboxPolicy) SandboxPolicy {
	if p.Mode == SandboxOff {
		return p
	}
	path, err := exec.LookPath("bwrap")
	if err != nil {
		p.Unavailable = "bubblewrap (bwrap) was not found on PATH"
		return p
	}
	p.Bwrap = path

	// Probed without a private /proc first, because that is the part that can
	// fail on its own. Whether the kernel allows mounting a fresh procfs inside
	// a container whose /proc has masked submounts varies with kernel and
	// container-runtime version — measured working on one machine and failing on
	// another with the same bwrap. So it is a capability to detect, not a
	// decision to hard-code, and the fallback is a working sandbox that merely
	// sees the outer container's process list.
	dir := os.TempDir()
	if out, err := trialRun(p, dir); err != nil {
		p.Unavailable = firstLine(strings.TrimSpace(out))
		if p.Unavailable == "" {
			p.Unavailable = err.Error()
		}
		return p
	}
	withProc := p
	withProc.Proc = true
	if _, err := trialRun(withProc, dir); err == nil {
		p.Proc = true
	}
	return p
}

func trialRun(p SandboxPolicy, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	// No deny list: the probe answers "can this kernel give us the namespaces",
	// and a read-only self-bind needs no capability the writable binds do not.
	argv := p.bwrapArgv(dir, dir, dir, pathExists, nil, []string{"/bin/true"})
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// The same double-scrubbed environment a real command gets, so the probe
	// cannot succeed on the strength of a variable the real thing will not have.
	cmd.Env = sandboxEnv(p.EnvPass, map[string]string{"HOME": dir, "TMPDIR": dir})
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// reportSandbox says what happened, once, in the voice the rest of pogo's
// startup lines use. Silence on success is deliberate everywhere except enforce:
// a line that prints on every start is a line people stop reading, and the
// interesting cases are the two where containment is not what was asked for.
func reportSandbox(p SandboxPolicy) {
	switch {
	case p.Refuses():
		fmt.Fprintf(os.Stderr, "pogo: %v\n", p.RefusalError())
	case p.Mode == SandboxOff:
		// Say where "off" came from: the env, or the runtime override — a
		// distinction that matters when someone is hunting for why the
		// boundary is gone.
		if SandboxOverride() == SandboxOff {
			fmt.Fprintf(os.Stderr, "pogo: sandbox off (runtime override, %s=%s is the baseline) — "+
				"bash and declared tools run unconfined.\n", EnvSandbox, SandboxPolicyFromEnv().Mode)
		} else {
			fmt.Fprintf(os.Stderr, "pogo: %s=%s — bash and declared tools run unconfined.\n",
				EnvSandbox, SandboxOff)
		}
	case !p.Active():
		fmt.Fprintf(os.Stderr, "pogo: sandbox unavailable (%s) — bash and declared tools "+
			"run unconfined, as they did before sandboxing existed. Set %s=%s to refuse instead.\n",
			p.Unavailable, EnvSandbox, SandboxEnforce)
	default:
		net := "network denied"
		if p.Net {
			net = "network ALLOWED"
		}
		proc := ""
		if !p.Proc {
			// Worth a word rather than silence: it is the one way this sandbox is
			// weaker than the argv suggests, and someone reading a process list
			// from inside a command should know why they can see the container.
			proc = ", /proc shared with the container"
		}
		fmt.Fprintf(os.Stderr, "pogo: sandbox on (bubblewrap, %s%s)\n", net, proc)
	}
}

// wrapCommand rewrites cmd to run inside a fresh sandbox, or returns an error
// when the mode says a command must not run unconfined.
//
// Called immediately before the command starts, and it changes only the three
// fields that decide what executes: Path, Args and Env. Everything the callers
// set up around it — the process group, the cancellation hook, the wait delay,
// the streaming sinks — keeps working unchanged, because from their point of view
// there is still one child process to signal and read from. That child is now
// bwrap, and killing its group takes the whole PID namespace with it.
func wrapCommand(cmd *exec.Cmd) error {
	return wrapCommandWithProtection(cmd, inheritedCommandProtection(cmd))
}

func wrapCommandWithProtection(cmd *exec.Cmd, manifest string) error {
	protection, err := documentCommandProtection(cmd, manifest)
	if err != nil {
		return fmt.Errorf("managed document protection: %w", err)
	}
	p := InitSandbox()
	if p.Refuses() {
		return p.RefusalError()
	}
	if !p.Active() {
		if len(protection.files) > 0 || len(protection.privateRoots) > 0 {
			return fmt.Errorf("managed documents require an active OS sandbox; use docs_office transform instead of an unconfined command")
		}
		return nil
	}
	// cmd.Dir is the session's directory for bash and SynthTool alike; an empty
	// one would mean "inherit ours", which is not something to hand a sandbox.
	workdir := cmd.Dir
	if workdir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("sandbox: cannot determine the working directory: %w", err)
		}
		workdir = wd
	}
	home := sandboxHome(workdir)
	tmp := sandboxTmpDir()

	// cmd.Args, not cmd.Path: Args[0] is what the caller asked for, and letting
	// the sandbox resolve it against the PATH it was given keeps the lookup
	// inside the sandbox's own view of the filesystem.
	inner := cmd.Args
	if len(inner) == 0 {
		inner = []string{cmd.Path}
	}
	// The deny scan runs per command rather than once per session: a repository
	// the command itself just created is exactly the one whose hooks it would
	// poison, and only a fresh scan sees it. Three levels deep is bounded and
	// cheap next to the fork that follows.
	gitDirs := findGitDirs(workdir, 3)
	if home != workdir {
		gitDirs = append(gitDirs, findGitDirs(home, 3)...)
	}
	deny := sandboxDenyPaths(workdir, home, gitDirs, pathExists)
	mounts := protection.mountArgs(p.writableSet(workdir, home, tmp, pathExists))
	// An outer /proc/PID/root would expose the unprotected host mounts. Keep
	// the existing private-proc behavior, or mask proc for managed documents
	// when that optional probe was unavailable; never silently expose aliases.
	if (len(protection.files) > 0 || len(protection.privateRoots) > 0) && !p.Proc {
		mounts = append(mounts, "--tmpfs", "/proc", "--remount-ro", "/proc")
	}
	argv := p.bwrapArgv(workdir, home, tmp, pathExists, deny, inner, mounts)
	cmd.Path = argv[0]
	cmd.Args = argv
	cmd.Env = sandboxEnv(p.EnvPass, map[string]string{"HOME": home, "TMPDIR": tmp})
	// The child's own Dir is now meaningless — bwrap chdirs inside the sandbox —
	// but leaving it set means a workspace that vanished still fails before the
	// sandbox is built, with the error naming the directory.
	return nil
}

// annotateSandboxFailure adds a line to a command's output when the failure
// looks like the sandbox refusing something, so the model adjusts instead of
// reading "permission denied" as a broken tool and retrying verbatim.
//
// Pattern matching on stderr rather than a structured report because bubblewrap
// has none to give: the denial happens in the kernel, to the command, and all
// that reaches us is the command's own complaint. The two strings below are what
// a read-only bind and a severed network actually produce.
func annotateSandboxFailure(output string) string {
	if !InitSandbox().Active() {
		return ""
	}
	low := strings.ToLower(output)
	switch {
	case strings.Contains(low, "read-only file system"):
		return "\n[sandbox] This command ran in a sandbox where only the working directory, " +
			"HOME and TMPDIR are writable. Write outputs there instead."
	case strings.Contains(low, "operation not permitted"), strings.Contains(low, "permission denied"):
		return "\n[sandbox] This command ran in a sandbox: paths outside the working directory, " +
			"HOME and TMPDIR are read-only."
	case strings.Contains(low, "temporary failure in name resolution"),
		strings.Contains(low, "could not resolve host"),
		strings.Contains(low, "network is unreachable"):
		return "\n[sandbox] This command ran in a sandbox with no network. " +
			"Work from local files, or ask the user to run the networked step."
	}
	return ""
}
