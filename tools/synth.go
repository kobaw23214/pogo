package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/yosukeno/pogo/skillobserve"
)

// SynthInfo is the catalog projection of a manifest tool: everything an
// interface (or a future MCP server) needs to present the tool without
// running it. It is the manifest's MCP fields plus the pogo extensions,
// which is the whole reason the manifest keeps the two apart.
type SynthInfo struct {
	Name        string         `json:"name"`
	WireName    string         `json:"wire_name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
	ReadOnly    bool           `json:"read_only,omitempty"`
	Destructive bool           `json:"destructive,omitempty"`
	Idempotent  bool           `json:"idempotent,omitempty"`
	OpenWorld   bool           `json:"open_world,omitempty"`
	Renderer    string         `json:"renderer"`
	Skill       string         `json:"skill,omitempty"`
	// Registered reports whether the model sees this tool as a first-class
	// call. The catalog holds every declared tool either way.
	Registered bool `json:"registered"`
}

// synthExec is the validated run spec behind a SynthTool.
type synthExec struct {
	argv    []string
	argMap  map[string]argMapping
	timeout int // seconds
}

// SynthDetails accompanies a synthesized tool call. Same contract as
// BashDetails: populated even when the call fails, so a card can show the
// exit code and the argv next to the output — the argv rather than a command
// string, because there is no command string: this is what actually ran.
type SynthDetails struct {
	Tool       string   `json:"tool"`
	Skill      string   `json:"skill,omitempty"`
	Argv       []string `json:"argv"`
	Renderer   string   `json:"renderer,omitempty"`
	ExitCode   int      `json:"exit_code"`
	DurationMS int64    `json:"duration_ms"`
	TimedOut   bool     `json:"timed_out,omitempty"`
	Truncated  bool     `json:"truncated,omitempty"`
	// FullOutputPath names the file holding the whole output when it did not fit.
	// Same field and same json name as BashDetails, because it answers the same
	// question and an interface should not need two ways to ask it.
	FullOutputPath string `json:"full_output_path,omitempty"`
}

// SynthTool is a tool declared by a skill manifest and executed by running a
// command with a validated argument vector. It implements Tool, so everything
// the loop already does for a tool it does here: schema validation before the
// gate, per-tool review, truncation, the wire events, the analyzer's per-tool
// attribution. The command runs in the session's directory, without a shell —
// see synthExec above for why that is load-bearing.
type SynthTool struct {
	info SynthInfo
	exec synthExec
	// identity is the owning skill's trusted identity, bound by the manifest
	// loader from its SynthSource — never reverse-looked-up from a runtime
	// name, so a session built before a skill reload keeps the identity its
	// snapshot was built with. Zero when the source had no resolvable
	// identity, and a zero identity records nothing.
	identity skillobserve.Identity
	Cwd      string
	// SpillDir holds output that did not fit in the result. Set by New, which
	// also makes it readable; empty means DefaultSpillDir.
	SpillDir string
	// ProtectionManifest is the trusted Web session's document registry. It
	// does not alter a manifest tool's ordinary command or sandbox permissions.
	ProtectionManifest string
}

// WithWorkspace returns a session-local copy bound to its working and output
// directories. Loaded manifests are shared across sessions; their declaration
// data is read-only after loading, but these directories belong to the caller.
func (t *SynthTool) WithWorkspace(cwd, spillDir string) *SynthTool {
	clone := *t
	clone.Cwd = cwd
	clone.SpillDir = spillDir
	return &clone
}

func (t *SynthTool) Name() string        { return t.info.WireName }
func (t *SynthTool) Description() string { return t.info.Description }

// SkillIdentity reports the identity the loader bound to this tool, for the
// MCP layer and tests that need to present provenance without re-deriving it.
func (t *SynthTool) SkillIdentity() skillobserve.Identity { return t.identity }

// InputSchema returns the manifest's schema as-is. The runtime validator only
// reads the shallow keywords the loader checked (type/enum/required), so by
// the time this is served the schema has been vetted for exactly the parts
// that will be enforced.
func (t *SynthTool) InputSchema() map[string]any { return t.info.InputSchema }

// IsReadOnly reports the manifest's readOnlyHint. Honoured inside the skill
// trust boundary — a read-only annotated tool reaches read-only sessions
// (explore subagents, the chain evaluator's registry shape) the same way the
// read-only built-ins do, by being registered where writes are withheld.
func (t *SynthTool) IsReadOnly() bool { return t.info.ReadOnly }

// ExecutionMode is Parallel only for read-only idempotent tools. Everything
// else runs alone, bash's rule for anything with unknown blast radius; a
// declared tool is exactly that, declared by a bundle author rather than
// audited here.
func (t *SynthTool) ExecutionMode() ExecutionMode {
	if t.info.ReadOnly && t.info.Idempotent {
		return Parallel
	}
	return Sequential
}

func (t *SynthTool) Execute(ctx context.Context, raw json.RawMessage) (res Result, err error) {
	// One observation span per call that actually entered Execute, opened at
	// the real start and closed exactly once (Span.End is idempotent, so the
	// early classifications below and the deferred fallback cannot double up).
	// Only the outcome category is recorded — never the arguments, the
	// output, the argv or the error text. An unbound tool gets a span with no
	// recorder from Start and behaves exactly as before.
	wireName := t.info.WireName
	if caller, entrypoint := skillobserve.SourceFrom(ctx); caller == "external" && entrypoint == "mcp" {
		// MCP exposes the manifest's dotted name; only provider-facing
		// agent tools use the sanitized name (see web.addSynthTools).
		wireName = t.info.Name
	}
	_, span := skillobserve.Start(ctx, t.identity, "execute", "manifest", t.info.Name, wireName)
	// runCtx is captured for the deferred classification: its deadline is the
	// tool's own timeout, distinct from the run's cancellation.
	var runCtx context.Context
	defer func() {
		if r := recover(); r != nil {
			span.End("error", "internal_error")
			panic(r)
		}
		status, kind := "ok", ""
		if err != nil {
			status, kind = "error", "execution_error"
			switch {
			case errors.Is(ctx.Err(), context.Canceled):
				status, kind = "cancelled", "cancelled"
			case errors.Is(ctx.Err(), context.DeadlineExceeded),
				runCtx != nil && errors.Is(runCtx.Err(), context.DeadlineExceeded):
				status, kind = "timeout", "timeout"
			}
		}
		span.End(status, kind)
	}()
	args, err := objectFields(raw)
	if err != nil {
		span.End("error", "validation_error")
		return Result{}, err
	}

	argv, err := t.buildArgv(args)
	if err != nil {
		span.End("error", "validation_error")
		return Result{}, err
	}

	// The loader guarantees a sane timeout; the guard is for direct
	// construction, where zero would mean "expire before starting" rather
	// than "no limit".
	timeout := t.exec.timeout
	if timeout <= 0 {
		timeout = DefaultSynthTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	// Same process-group discipline as bash: a decompiler that spawns workers
	// must not leave them behind when a turn is cancelled.
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Dir = t.Cwd
	// Same sandbox as bash, for the same reason: a declared tool is a command
	// line from a skill bundle, and the bundle is the trust boundary rather than
	// the harness. It matters more here than it looks, because these are the
	// tools the approval gate waves through — a manifest's readOnlyHint makes one
	// permanently unreviewed, so the gate is not what stands behind them.
	wrap := wrapCommand
	if t.ProtectionManifest != "" {
		wrap = func(cmd *exec.Cmd) error { return wrapCommandWithProtection(cmd, t.ProtectionManifest) }
	}
	if err := wrap(cmd); err != nil {
		span.End("error", "configuration_error")
		return Result{}, err
	}
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return KillGroup(cmd) }
	cmd.WaitDelay = 2 * time.Second
	capture := newOutputCapture(t.SpillDir, t.info.WireName, cancel)
	output := &taggedOutput{capture: capture}
	cmd.Stdout = outputChannel{output: output}
	cmd.Stderr = outputChannel{output: output, stderr: true}

	started := time.Now()
	runErr := cmd.Run()
	details := SynthDetails{
		Tool: t.info.WireName, Skill: t.info.Skill, Argv: argv,
		Renderer: t.info.Renderer, DurationMS: time.Since(started).Milliseconds(),
		ExitCode: cmd.ProcessState.ExitCode(),
	}

	k := capture.finish()
	details.Truncated, details.FullOutputPath = k.Truncated, k.Path
	text := k.Text()

	fail := func(status string) (Result, error) {
		return Result{Details: details}, errors.New(withStatus(text, "", status))
	}
	switch {
	case capture.Err() != nil:
		return fail(capture.Err().Error())
	case ctx.Err() != nil:
		return fail("Tool call aborted")
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		details.TimedOut = true
		return fail(fmt.Sprintf("Tool call timed out after %d seconds", t.exec.timeout))
	case runErr != nil:
		var exit *exec.ExitError
		if errors.As(runErr, &exit) {
			return fail(fmt.Sprintf("Tool exited with code %d", exit.ExitCode()))
		}
		return fail(runErr.Error())
	}
	if text == "" {
		return Result{Text: "(no output)", Details: details}, nil
	}
	return Result{Text: text, Details: details}, nil
}

// buildArgv assembles the argument vector from the fixed prefix and the
// mapped arguments. Properties are visited in declaration order (sorted, as
// nowhere else guarantees one) so the same call builds the same argv every
// time — an argv that reorders itself between identical calls is a testing
// nightmare and a reproducibility bug.
func (t *SynthTool) buildArgv(args map[string]json.RawMessage) ([]string, error) {
	argv := append([]string{}, t.exec.argv...)
	names := make([]string, 0, len(t.exec.argMap))
	for name := range t.exec.argMap {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		raw, present := args[name]
		if !present || jsonKind(raw) == "null" {
			continue
		}
		m := t.exec.argMap[name]
		switch m.Kind {
		case "presence":
			var b bool
			if err := json.Unmarshal(raw, &b); err != nil {
				return nil, fmt.Errorf("field %q must be a boolean", name)
			}
			if b {
				argv = append(argv, m.Flag)
			}
		case "flag":
			v, err := argValue(name, raw)
			if err != nil {
				return nil, err
			}
			argv = append(argv, m.Flag, v)
		default: // "append"
			v, err := argValue(name, raw)
			if err != nil {
				return nil, err
			}
			// A positional argument that begins with '-' is a flag the model
			// smuggled past the manifest: getopt parses argv by shape, not by
			// intent, and malquery's reorderArgs actively hoists dash-prefixed
			// positionals into the flag area. Measured: search("--sha256-only")
			// changed the output shape. Declared flags go through the "flag"
			// kind above, where the value travels as a flag's argument; a bare
			// positional has no such protection, so it is refused instead.
			if strings.HasPrefix(v, "-") {
				return nil, fmt.Errorf("field %q: positional values must not begin with '-' "+
					"(that is a flag this manifest did not declare)", name)
			}
			argv = append(argv, v)
		}
	}
	return argv, nil
}

// argValue renders one mapped argument as a single argv element. Numbers keep
// their JSON literal (json.Number) rather than a float round trip, so 1099511627776
// does not arrive as 1.099511627776e+12.
func argValue(name string, raw json.RawMessage) (string, error) {
	switch jsonKind(raw) {
	case "string":
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", err
		}
		return s, nil
	case "number":
		var n json.Number
		if err := json.Unmarshal(raw, &n); err != nil {
			return "", err
		}
		return n.String(), nil
	default:
		return "", fmt.Errorf("field %q cannot be passed as a command argument (type %s); use a string, a number, or a presence flag", name, jsonKind(raw))
	}
}

// ReadOnlyCapable is the optional interface New uses to filter a read-only
// session's tool set. Structurally the same idea as Options.ReadOnly
// withholding write/edit/bash: an explore session gets the read-only declared
// tools and cannot even attempt the rest. Absent for the built-ins, whose
// read-only four are enumerated by hand in New.
type ReadOnlyCapable interface {
	IsReadOnly() bool
}

// ReadOnlySynthNames names the declared tools a read-only session keeps — the
// registry's readOnlyHint filter (see Options.Synth), reported rather than
// re-decided. Used to tell an explore subagent what it holds: the names come
// from the same tool list the registry filtered, so the report and the tool
// schema cannot disagree.
func ReadOnlySynthNames(synth []Tool) []string {
	var names []string
	for _, t := range synth {
		if ro, ok := t.(ReadOnlyCapable); ok && ro.IsReadOnly() {
			names = append(names, t.Name())
		}
	}
	return names
}

// SkillDirToken is the placeholder a manifest's argv may use for the owning
// skill's directory. It exists because bundled scripts live beside the
// SKILL.md that teaches them, and where the bundle is deployed is the one
// thing the manifest author cannot know: the same skill loads from
// /opt/skills, ~/.pogo/skills or a -skill path. Substituted at load, after
// validation, so the token never reaches exec and the expanded argv stays a
// plain absolute path the runner treats like any other.
const SkillDirToken = "$SKILL_DIR"

// expandSkillDir substitutes SkillDirToken throughout the argv with the
// directory the manifest was loaded from.
func (t *SynthTool) expandSkillDir(dir string) {
	if dir == "" {
		return
	}
	for i, a := range t.exec.argv {
		if strings.Contains(a, SkillDirToken) {
			t.exec.argv[i] = strings.ReplaceAll(a, SkillDirToken, dir)
		}
	}
}

var _ Tool = (*SynthTool)(nil)
