package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

// DefaultBashTimeout caps runaway commands. pi leaves this open-ended, but an
// unattended server agent needs a ceiling more than it needs the flexibility.
const DefaultBashTimeout = 120 * time.Second

// MaxBashTimeout is the ceiling on the timeout argument. Ten minutes, chosen to
// match the longest thing a declared tool is allowed to ask for in practice
// (mal.decompile's manifest asks for 600s) while staying far below
// MaxSynthTimeout, which is the loader's outer bound rather than a working
// value.
//
// It exists because the default above was a ceiling the model could raise: the
// timeout argument was applied with no upper bound, so one call could park a
// command for as long as it liked.
const MaxBashTimeout = 600 * time.Second

type Bash struct {
	// Jobs is a host-owned session runner; nil preserves the foreground-only tool.
	Jobs  *ShellJobRunner
	Cwd   string
	Scope *CommandScope
	// ProtectionManifest adds managed document mounts to the normal sandbox
	// without changing its network policy or its existing filesystem grants.
	ProtectionManifest string
	// Timeout overrides DefaultBashTimeout when non-zero.
	Timeout time.Duration
	// Guard catches accidental directory escapes and Git operations in a child.
	// Main sessions leave it nil; OS sandboxing is a separate control.
	Guard *Guard
	// SpillDir holds output that did not fit in the result. Empty means
	// DefaultSpillDir; New sets it and adds it to the readable roots, which is
	// what makes the path in the note one the model can actually open.
	SpillDir string
}

type bashArgs struct {
	Command string `json:"command" required:"true" description:"Bash command to execute"`
	// Workdir is where to run, relative to the session's directory or absolute.
	// Optional; empty means the session's own directory, which is what every call
	// did before this existed.
	Workdir         string  `json:"workdir,omitempty" description:"Directory to run in, relative or absolute. Defaults to the working directory"`
	Timeout         float64 `json:"timeout,omitempty" description:"Timeout in seconds"`
	RunInBackground bool    `json:"run_in_background,omitempty" description:"Launch a managed background command and return a task ID immediately"`
	YieldAfter      float64 `json:"yield_after,omitempty" description:"Optional seconds to wait before returning a running task ID; does not change the hard timeout"`
	Description     string  `json:"description,omitempty" description:"Short description for a background command"`
}

func (*Bash) Name() string { return "bash" }

// Description states that the shell does not persist, which is the one thing about
// this tool a model cannot discover and is likely to assume the opposite of: the
// bash tool most models have seen documented keeps one session alive across calls,
// so `cd` there carries over. Here every call is a new `bash -c`, and a model acting
// on the other assumption runs its next command in the wrong directory and gets a
// plausible wrong answer rather than an error.
//
// Chaining is mentioned in the same breath because it is the remedy, not because
// long chains are wanted: output is tail-truncated and one exit code is reported for
// the whole string, so a chain trades attribution for a round trip. Saying "chain
// what depends on the same directory" gets the fix without inviting `a && b && c &&
// d` where the first failure is the one that scrolls off.
//
// workdir is named before the chaining remedy, because for the "run it over there"
// case it is the better answer and the model will otherwise reach for `cd x && …`,
// which it has to get syntactically right, which the guard then has to parse back
// out of the command text, and which spends the exit code and the output budget on
// the `cd`. Codex's shell tool has the same parameter for the same reason. The
// chaining sentence stays because `cd` is still the only answer for the rest of what
// does not persist: environment variables, an activated virtualenv, a shell function.
func (t *Bash) Description() string {
	desc := fmt.Sprintf(
		"Execute a bash command in the working directory. Returns combined stdout and stderr. "+
			"Output is truncated to the last %d lines or %dKB, whichever is hit first; when truncated, the full "+
			"output is written to a temp file whose path is reported. Optionally provide a timeout in seconds (default %.0f). "+
			"Each call is a fresh shell: cd and environment variables do not carry over. To run somewhere else, "+
			"pass workdir rather than prefixing cd; for anything else that does not persist, chain it into the one "+
			"command (. venv/bin/activate && pytest).",
		MaxLines, MaxBytes/1024, DefaultBashTimeout.Seconds())
	if t.Jobs != nil {
		desc += " Use run_in_background=true for a long command, or yield_after (seconds) to wait briefly before handing the same process to the background. Continue only independent work while it runs; commands can still change shared files. The hard timeout is unchanged. Use shell_tasks, shell_output and shell_stop to manage this session's jobs."
	}
	return desc
}

// workdir resolves the requested directory to an absolute path, or returns an
// error the model can act on.
//
// Deliberately *not* run through resolve(): that is the path guard, and bash has
// never had one. Confining workdir while `cd /etc` in the command string still works
// would be a restriction that reads like a boundary without being one, and it would
// refuse the legitimate case this parameter is for — running a build in a checkout
// beside the session's directory. The real boundary for bash is the container, which
// is stated in the type comment and in the Guard's.
//
// What it does check is that the directory exists and is a directory, because the
// alternative is bash failing to start with "chdir: no such file or directory" and
// an exit code of -1: a message about the harness, not about the mistake. The error
// names the resolved path, not the argument, since a relative one is exactly the
// case where the model's mental model of the base directory is what went wrong.
func (t *Bash) workdir(requested string) (string, error) {
	if t.Scope != nil {
		spec, err := normalizedCommandScope(t.Scope, requested)
		return spec.workdir, err
	}
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return t.Cwd, nil
	}
	dir := requested
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(t.Cwd, dir)
	}
	dir = filepath.Clean(dir)
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("workdir %s does not exist (resolved from %q against %s): %w",
			dir, requested, t.Cwd, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workdir %s is a file, not a directory", dir)
	}
	return dir, nil
}

// ExecutionMode is Sequential, which is a deliberate divergence from pi (whose
// bash runs in parallel like everything else).
//
// Three reasons. Commands that matter here are already internally parallel
// (`go build`, `go test`, `make -j`), so overlapping them contends on the build
// cache instead of saving time. Two commands writing one output file corrupt it
// and nothing here can detect that. And under the approval gate, N parallel bash
// calls mean N approval prompts at once.
//
// Because a sequential call overlaps nothing in its batch, this also stops a read
// from racing a command that rewrites the file being read. That still holds now that
// a sequential tool no longer serializes its whole batch (agent.segments): what the
// promise was ever about is this call running alone, not its siblings running one at
// a time. And it is load-bearing here rather than decorative — bash writes through
// arbitrary shell commands, which never pass the per-path lock the file tools share,
// so being alone is the only protection there is.
func (*Bash) ExecutionMode() ExecutionMode { return Sequential }

// InputSchema keeps command as the only required field. Codex declares its
// equivalent of workdir required — a consequence of the strict-schema rule that
// every property must be listed, not a judgement that a caller should always supply
// one — and requiring it here would mean every single call carrying a redundant "."
// and every existing transcript becoming invalid against the schema.
func (t *Bash) InputSchema() map[string]any {
	props := map[string]any{
		"command": prop("string", "Bash command to execute"),
		"workdir": prop("string", "Directory to run in, relative to the working directory or "+
			"absolute. Defaults to the working directory; use this instead of a cd prefix."),
		"timeout": prop("number", "Timeout in seconds"),
	}
	if t.Jobs != nil {
		props["run_in_background"] = prop("boolean", "Return a managed task ID immediately; completion is reported automatically")
		props["yield_after"] = prop("number", "Seconds to wait before handing the same process to the background (greater than 0, at most 600); hard timeout is unchanged")
		props["description"] = prop("string", "Short description for the background task")
	}
	return object([]string{"command"}, props)
}

// Schema returns the JSON schema for bash tool using reflection.
func (t *Bash) Schema() map[string]any {
	schema := GenerateSchema(reflect.TypeOf(bashArgs{}))
	if t.Jobs == nil {
		if props, ok := schema["properties"].(map[string]any); ok {
			delete(props, "run_in_background")
			delete(props, "yield_after")
			delete(props, "description")
		}
	}
	return schema
}

// Execute runs the command without an observer. It delegates so that there is a
// single implementation to get right; see StreamingTool.
func (t *Bash) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	return t.ExecuteStreaming(ctx, raw, nil)
}

// ExecuteStreaming runs the command, reporting output as it appears.
//
// bash is the only built-in that implements this, and the reason is `go test`:
// a command that takes a minute used to show nothing at all until it finished, so
// there was no way to tell a slow test suite from a hung one.
func (t *Bash) ExecuteStreaming(ctx context.Context, raw json.RawMessage, onPartial func(Partial)) (Result, error) {
	var a bashArgs
	if err := unmarshal(raw, &a); err != nil {
		return Result{}, err
	}
	if a.YieldAfter < 0 || a.YieldAfter > MaxBashTimeout.Seconds() || math.IsNaN(a.YieldAfter) || math.IsInf(a.YieldAfter, 0) {
		return Result{}, errors.New("yield_after must be between 0 and 600 seconds")
	}
	if a.RunInBackground || a.YieldAfter > 0 {
		if t.Jobs == nil {
			return Result{}, errors.New("background shell execution is unavailable in this session")
		}
		if strings.TrimSpace(a.Command) == "" {
			return Result{}, errors.New("command must not be empty")
		}
		if err := t.Guard.Check(a.Command); err != nil {
			return Result{}, err
		}
		dir, err := t.workdir(a.Workdir)
		if err != nil {
			return Result{}, err
		}
		if err := t.Guard.CheckDir(dir, a.Workdir); err != nil {
			return Result{}, err
		}
		a.Workdir = dir
		return t.Jobs.dispatch(ctx, t, a, onPartial)
	}
	return t.executeCommand(ctx, a, onPartial, nil)
}

func (t *Bash) executeCommand(ctx context.Context, a bashArgs, onPartial func(Partial), rawOutput io.Writer) (Result, error) {
	if strings.TrimSpace(a.Command) == "" {
		return Result{}, fmt.Errorf("command must not be empty")
	}
	// Checked before anything runs, and before the timeout is even resolved: a
	// refused command must leave no trace at all.
	if err := t.Guard.Check(a.Command); err != nil {
		return Result{}, err
	}
	dir, err := t.workdir(a.Workdir)
	if err != nil {
		return Result{}, err
	}
	// The guard has to see workdir as well as the command, and this is the part of
	// adding the parameter that is not free. Its `cd` check works by parsing an
	// absolute target back out of the command text; a workdir field reaches the same
	// place without ever appearing there, so leaving it unchecked would turn a new
	// convenience into a hole in the one containment a subagent has.
	if err := t.Guard.CheckDir(dir, a.Workdir); err != nil {
		return Result{}, err
	}

	timeout := t.Timeout
	if timeout == 0 {
		timeout = DefaultBashTimeout
	}
	if a.Timeout > 0 {
		timeout = time.Duration(a.Timeout * float64(time.Second))
		// Clamped, which it was not before. The model chose this number, and
		// the ceiling in DefaultBashTimeout's comment — "an unattended server
		// agent needs a ceiling more than it needs the flexibility" — was one
		// the model could raise without limit. SynthTool already holds itself to
		// exactly this standard from the other side: its timeout comes from the
		// manifest and the loader refuses anything outside 1..MaxSynthTimeout,
		// so the model cannot touch it at all.
		//
		// The <= 0 arm is not defensive padding: a large enough seconds value
		// overflows int64 nanoseconds and lands negative, and a negative
		// duration makes context.WithTimeout expire immediately — so before this,
		// asking for a very long timeout produced "timed out after -922337
		// seconds" instead of a long-running command.
		if timeout > MaxBashTimeout || timeout <= 0 {
			timeout = MaxBashTimeout
		}
	}

	// CommandContext ties the child process to the cancellation chain, so Ctrl-C
	// on the agent kills the command instead of orphaning it.
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// The command string is passed as a single argv element to bash -c, never
	// interpolated into a larger shell string, so there is nothing to escape.
	cmd := exec.CommandContext(runCtx, "bash", "-c", a.Command)
	cmd.Dir = dir
	// Confine it, if this platform and deployment can. Placed after Dir because
	// the policy is built around it, and before everything below because those
	// all describe "the child process" — which, once this returns, is bubblewrap
	// rather than bash. Nothing below has to know that: signalling the group
	// still stops the work, because the group leader is bwrap and its PID
	// namespace dies with it.
	//
	// An error here is only ever the enforce refusal, and it must surface as the
	// tool's error rather than as a command that quietly ran unconfined.
	wrap := wrapCommand
	if t.ProtectionManifest != "" {
		wrap = func(cmd *exec.Cmd) error { return wrapCommandWithProtection(cmd, t.ProtectionManifest) }
	}
	if t.Scope != nil {
		scope := *t.Scope
		if scope.ProtectionManifest == "" {
			scope.ProtectionManifest = t.ProtectionManifest
		}
		wrap = func(cmd *exec.Cmd) error { return WrapScopedCommand(cmd, &scope) }
	}
	if err := wrap(cmd); err != nil {
		return Result{}, err
	}
	// Kill the whole process group, not just bash.
	//
	// The default behaviour signals the direct child only, so `sleep 45` inside
	// `bash -c` survives, gets reparented to init, and keeps running after the
	// agent thinks it stopped. For a coding agent that is the common case, not an
	// edge one: cancelling a turn has to stop the `go test` it launched.
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return KillGroup(cmd) }
	// Give the group a moment to die on its own, then stop waiting on pipes a
	// stubborn grandchild might still hold open.
	cmd.WaitDelay = 2 * time.Second
	capture := newOutputCapture(t.SpillDir, "bash", cancel)
	if t.Scope != nil {
		capture, err = newScopedOutputCapture(t.Scope.Cwd, "bash", cancel)
		if err != nil {
			return Result{}, err
		}
	}
	sink := newPartialSink(capture, onPartial)
	var outputSink io.Writer = sink
	if rawOutput != nil {
		outputSink = io.MultiWriter(rawOutput, sink)
	}
	cmd.Stdout = outputSink
	cmd.Stderr = outputSink

	started := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(started)
	// Flush before rendering, so the last lines are not held back by coalescing.
	// Run has returned, so nothing else will write to the sink.
	k := sink.close()
	output, note := k.Content, k.Note
	details := BashDetails{TotalLines: k.TotalLines, Truncated: k.Truncated, FullOutputPath: k.Path}
	details.Command = a.Command
	// Recorded as the resolved absolute path rather than what the model typed, and
	// only when it differs from the session's directory: "." and "" describe the
	// default, and a card announcing the default on every call says nothing.
	if dir != t.Cwd {
		details.Workdir = dir
	}
	details.DurationMS = elapsed.Milliseconds()
	details.ExitCode = -1
	if cmd.ProcessState != nil {
		details.ExitCode = cmd.ProcessState.ExitCode()
	}
	// Recorded so the card can say the command ran confined rather than leaving
	// anyone to infer the boundary from the failure text. True even when the
	// command failed: a refusal by the sandbox is still the sandbox working.
	if t.Scope != nil {
		details.Sandboxed = true
	} else {
		details.Sandboxed = InitSandbox().Active()
	}

	// Details travel with the error too: a failed command's exit code and output
	// are exactly what the UI needs to show.
	fail := func(status string) (Result, error) {
		// A sandbox denial reaches us as the command's own "permission denied",
		// which reads to a model like a broken tool and earns a verbatim retry.
		// Naming the boundary turns it into information it can act on — pick a
		// different path, or say the networked step needs a human. Only on
		// failure: a successful command whose output happens to contain those
		// words is not a denial.
		if t.Scope != nil {
			note += "\n[session sandbox] Only this session's workspace and private temporary directories are writable; other users' files and host agent state are not mounted."
		} else if ann := annotateSandboxFailure(output); ann != "" {
			note += ann
		}
		return Result{Details: details}, errors.New(withStatus(output, note, status))
	}
	switch {
	case sink.output.Err() != nil:
		return fail(sink.output.Err().Error())
	case ctx.Err() != nil:
		return fail("Command aborted")
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		details.TimedOut = true
		return fail(fmt.Sprintf("Command timed out after %.0f seconds", timeout.Seconds()))
	case runErr != nil:
		var exit *exec.ExitError
		if errors.As(runErr, &exit) {
			return fail(fmt.Sprintf("Command exited with code %d", exit.ExitCode()))
		}
		return fail(runErr.Error())
	}

	if output == "" {
		return Result{Text: "(no output)", Details: details}, nil
	}
	return Result{Text: output + note, Details: details}, nil
}

func withStatus(output, note, status string) string {
	if output == "" {
		return status
	}
	return output + note + "\n\n" + status
}
