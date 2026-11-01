//go:build !linux

package tools

// The non-Linux twin of sandbox_linux.go. Same shape as procgroup_other.go: the
// package has to build and behave sensibly where the mechanism does not exist.
//
// macOS has a mechanism — Seatbelt, through sandbox-exec, which is what both
// Codex and Anthropic's sandbox-runtime use there — and this is where it would
// go. It is deliberately not started here: a half-written deny-default SBPL
// profile is worse than none, because it invites the same mistake the approval
// gate already warns about, which is trusting something that reads like a
// boundary without being one. Until then macOS is a degrade-mode platform and
// says so out loud.

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"
)

var (
	sandboxOnce   sync.Once
	sandboxCached SandboxPolicy
)

// InitSandbox resolves the mode and reports that no sandbox is available here.
//
// It still honours enforce. That is the whole reason this is not simply a
// function returning nil: a deployment that asked for enforce and got silently
// unconfined execution because it happened to be running on a platform without
// an implementation is exactly the failure the mode exists to prevent.
func InitSandbox() SandboxPolicy {
	sandboxOnce.Do(func() {
		p := SandboxPolicyFromEnv()
		if p.Mode != SandboxOff {
			p.Unavailable = "no OS sandbox is implemented for " + runtime.GOOS
		}
		sandboxCached = p
		// Report in the effective mode: a restored runtime override is part
		// of the truth a startup line tells.
		p = applyOverride(p)
		switch {
		case p.Mode == SandboxOff:
			// Nothing to say: off on a platform with nothing to turn off.
		case p.Refuses():
			fmt.Fprintf(os.Stderr, "pogo: %s=%s but %s — bash and declared tools "+
				"will be refused. Set %s=%s to run them unconfined.\n",
				EnvSandbox, SandboxEnforce, p.Unavailable, EnvSandbox, SandboxDegrade)
		default:
			fmt.Fprintf(os.Stderr, "pogo: %s — bash and declared tools run unconfined "+
				"(the container, where there is one, is the only boundary).\n", p.Unavailable)
		}
	})
	return applyOverride(sandboxCached)
}

// wrapCommand leaves the command alone, unless enforce was asked for.
func wrapCommand(cmd *exec.Cmd) error {
	return wrapCommandWithProtection(cmd, inheritedCommandProtection(cmd))
}

func wrapCommandWithProtection(cmd *exec.Cmd, manifest string) error {
	protection, err := documentCommandProtection(cmd, manifest)
	if err != nil {
		return fmt.Errorf("managed document protection: %w", err)
	}
	if len(protection.files) > 0 || len(protection.privateRoots) > 0 {
		return fmt.Errorf("managed documents require an active OS sandbox; use docs_office transform instead of an unconfined command")
	}
	p := InitSandbox()
	if p.Refuses() {
		return p.RefusalError()
	}
	return nil
}

// annotateSandboxFailure has nothing to add where nothing was confined.
func annotateSandboxFailure(string) string { return "" }
