package web

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/tools"
)

// Mode is how much of the agent's work needs a human's eyes.
type Mode string

const (
	// ModeStrict reviews every mutation, including file writes.
	ModeStrict Mode = "strict"
	// ModeStandard reviews bash only. The file tools are already confined to the
	// working directory by the path guard, and their effect is visible as a diff
	// and usually recoverable from git. bash has neither property, which makes it
	// the one tool worth interrupting for by default.
	ModeStandard Mode = "standard"
	// ModeAuto reviews nothing.
	ModeAuto Mode = "auto"
)

// ParseMode validates a mode name coming off the wire.
func ParseMode(s string) (Mode, bool) {
	switch Mode(s) {
	case ModeStrict, ModeStandard, ModeAuto:
		return Mode(s), true
	}
	return "", false
}

// Policy decides which calls can skip review. It is session state, deliberately
// not per-connection: reloading the browser must not silently re-arm a gate the
// user turned off, nor leave the banner off while auto mode is still active.
type Policy struct {
	mu   sync.Mutex
	mode Mode
	// remaining counts down loop turns for a turn-limited auto mode. Turns, not
	// tool calls, because "let it run three more rounds" is the unit people
	// actually think in.
	remaining int
	limited   bool

	// allowTool and allowCmd are session-scoped "always allow" grants.
	allowTool map[string]bool
	allowCmd  map[string]bool
	// MCP grants are tied to immutable pool entries. Unchanged entries retain
	// identity across list pulls; definition/connection changes replace them.
	mcpTools    map[string]tools.Tool
	mcpTrusted  map[string]bool
	mcpCommands map[string]map[string]bool

	// readOnly names tools declared read-only by a skill manifest
	// (annotations.readOnlyHint). Set once at session creation from the
	// manager's fixed catalog. The annotation is only the bundle author's
	// claim, though, and an author's claim is not what grants the exemption:
	// Decide honours it solely while the OS sandbox is active and the mode is
	// not strict — the exemption rests on a host-controlled fact about where
	// the command runs, never on the manifest's say-so alone.
	readOnly map[string]bool

	// sandboxActive is the fact the readOnlyHint exemption rests on: whether
	// commands will really run inside the OS sandbox. A field rather than a
	// direct call so the policy's decisions stay testable on machines with no
	// sandbox at all; production code never touches it after NewPolicy.
	sandboxActive func() bool
}

func NewPolicy() *Policy {
	return &Policy{
		mode:          ModeStandard,
		allowTool:     make(map[string]bool),
		allowCmd:      make(map[string]bool),
		readOnly:      make(map[string]bool),
		mcpTools:      make(map[string]tools.Tool),
		mcpTrusted:    make(map[string]bool),
		mcpCommands:   make(map[string]map[string]bool),
		sandboxActive: func() bool { return tools.InitSandbox().Active() },
	}
}

// MarkReadOnly registers declared read-only tool names. The declaration alone
// does not skip review: it becomes effective only while the sandbox is active
// (and never in strict mode) — see Decide.
func (p *Policy) MarkReadOnly(names ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, n := range names {
		p.readOnly[n] = true
	}
}

func (p *Policy) State() PolicyState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stateLocked()
}

func (p *Policy) stateLocked() PolicyState {
	return PolicyState{Mode: string(p.mode), RemainingTurns: p.remaining}
}

// Set switches mode. A positive turns argument only means anything for auto and
// makes it expire on its own.
func (p *Policy) Set(mode Mode, turns int) PolicyState {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mode = mode
	p.remaining, p.limited = 0, false
	if mode == ModeAuto && turns > 0 {
		p.remaining, p.limited = turns, true
	}
	return p.stateLocked()
}

// AllowTool grants a tool a pass for the rest of the session.
func (p *Policy) AllowTool(name string) PolicyState {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.allowTool[name] = true
	return p.stateLocked()
}

// ReplaceMCP synchronizes explicit server trust with the run's projection.
// Remembered approvals belong to a particular definition and connection, not
// just a reusable wire name. Operator-configured server trust is applied anew.
func (p *Policy) ReplaceMCP(projected []tools.Tool, trusted []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	next := make(map[string]tools.Tool, len(projected))
	for _, t := range projected {
		next[t.Name()] = t
	}
	for name, old := range p.mcpTools {
		if next[name] != old {
			delete(p.allowTool, name)
			delete(p.mcpCommands, name)
		}
	}
	for name, t := range next {
		if p.mcpTools[name] != t {
			delete(p.allowTool, name)
			delete(p.mcpCommands, name)
		}
	}
	p.mcpTools = next
	p.mcpTrusted = make(map[string]bool, len(trusted))
	for _, name := range trusted {
		if next[name] != nil {
			p.mcpTrusted[name] = true
		}
	}
}

// AllowToolCommand scopes MCP argument grants to their current tool binding.
// Ordinary shell grants keep their existing command/workdir semantics.
func (p *Policy) AllowToolCommand(name, command string) PolicyState {
	p.mu.Lock()
	if p.mcpTools[name] != nil {
		if p.mcpCommands[name] == nil {
			p.mcpCommands[name] = make(map[string]bool)
		}
		p.mcpCommands[name][command] = true
		state := p.stateLocked()
		p.mu.Unlock()
		return state
	}
	p.mu.Unlock()
	return p.AllowCommand(command)
}

// AllowCommand grants one exact command string a pass for the rest of the
// session.
//
// Exact text, never a prefix. Prefix matching a shell command is not a security
// boundary — `ls; rm -rf ~` starts with `ls` — and its real damage is convincing
// people something is being blocked. An exact string has no syntax left to
// subvert, and it still fixes the fatigue case of the same `go build ./...`
// twenty times in one session.
//
// The argument is a grant key, not necessarily a command: the gate passes
// grantKeyOf, which folds in bash's workdir. A caller with only a command string —
// the HTTP endpoint, where that is all the request can carry — therefore grants the
// default-directory form and nothing else, which is the honest reading of what it
// asked for.
func (p *Policy) AllowCommand(cmd string) PolicyState {
	p.mu.Lock()
	defer p.mu.Unlock()
	if cmd = strings.TrimSpace(cmd); cmd != "" {
		p.allowCmd[cmd] = true
	}
	return p.stateLocked()
}

// TurnStarted spends one turn of a turn-limited auto mode. It reports the mode
// it left when the budget ran out, so the caller can tell the user rather than
// let the gate quietly re-arm.
//
// The budget is spent at the start of a turn and the reversion happens at the
// start of the turn after the last one, so `/auto 3` covers exactly three turns
// rather than two and a half.
func (p *Policy) TurnStarted() (from Mode, state PolicyState, reverted bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mode != ModeAuto || !p.limited {
		return p.mode, p.stateLocked(), false
	}
	if p.remaining > 0 {
		p.remaining--
		return p.mode, p.stateLocked(), false
	}
	p.mode, p.limited = ModeStandard, false
	return ModeAuto, p.stateLocked(), true
}

// Decide reports whether a call may skip review, and which rule said so. The
// rule name is published as an audit trail, so an automatic pass still leaves a
// mark on the timeline.
func (p *Policy) Decide(req agent.GateRequest) (rule string, auto bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.mode == ModeAuto {
		return "policy:auto", true
	}
	if p.mcpTrusted[req.ToolName] {
		return "mcp:trusted:" + req.ToolName, true
	}
	if p.allowTool[req.ToolName] {
		return "session:tool:" + req.ToolName, true
	}
	if key := grantKeyOf(req.Args); key != "" && req.ToolName != tools.ShellStopName {
		if p.mcpTools[req.ToolName] != nil {
			if p.mcpCommands[req.ToolName][key] {
				return "session:command", true
			}
		} else if p.allowCmd[key] {
			return "session:command", true
		}
	}
	// A manifest's readOnlyHint is the bundle author's claim, and an author's
	// claim cannot be what grants an exemption — qwen-code#6917 is that design
	// failing in the wild, and MCP's own spec calls annotations untrusted.
	// What grants the exemption here is a fact the bundle author cannot
	// control: this command will run inside the OS sandbox. So the hint skips
	// review only while containment is actually on — and never in strict mode,
	// which is the operator's statement that they want eyes on everything.
	// Without the sandbox the hint is display text and the call falls through
	// to the ordinary matrix.
	if p.readOnly[req.ToolName] && p.mode != ModeStrict && p.sandboxActive() {
		return "sandbox:auto-allow:" + req.ToolName, true
	}
	if !p.reviewsLocked(req) {
		return "policy:" + string(p.mode), true
	}
	return "", false
}

// reviewsLocked is the mode matrix.
func (p *Policy) reviewsLocked(req agent.GateRequest) bool {
	switch req.ToolName {
	case "read", "ls", "find", "grep", tools.InformationToolName, tools.ToolResultReadName, tools.ShortcutQueryName, tools.ShortcutSearchName, tools.ShortcutReadName, tools.ShortcutListName, tools.SkillSearchName, tools.SkillReadName, tools.ShellTasksName, tools.ShellOutputName, "workflow_discover", "workflow_read", "workflow_validate", tools.AgentWorkflowReadName, tools.RequestUserInputName, tools.RequestUserInputAsyncName:
		// Read-only and confined by the path guard. Reviewing them would train
		// the habit of clicking approve, which is what makes the bash prompt
		// stop meaning anything.
		return false
	case tools.WatchName:
		return !tools.WatchReadOnly(req.Args)
	case tools.BackgroundTaskName:
		return !tools.BackgroundTaskReadOnly(req.Args)
	case tools.WatchTestName:
		return true
	case tools.WatchDecideName:
		return false
	case "browser":
		// The browser channel carries its own approval at task granularity: the
		// user typed /skill:browser-use <task>, or approved the arm card the
		// first unarmed call raised (BrowserGate.askArm). Either way the grant
		// is explicit and task-scoped, and a per-action card here would charge
		// one decision twice — twenty times in a twenty-step task, which is
		// precisely the reflex that makes the bash card stop meaning anything.
		// So the loop passes browser calls unreviewed on both sides of the
		// armed check, and page operations are decided in exactly one place:
		// the channel, which refuses, asks once, or runs.
		return false
	case "write", "edit", tools.ShortcutManageName, tools.ShortcutCreateName, tools.ShortcutFolderCreateName, tools.ShortcutUpdateName, tools.ShortcutFolderRenameName, tools.ShortcutMoveName, tools.SkillSaveName:
		return p.mode == ModeStrict
	case tools.ShortcutDeleteName:
		// Removing private cards deserves a visible decision in reviewed modes.
		// Fully automatic access and explicit per-tool grants remain respected
		// by Decide above; this does not silently grant a missing capability.
		return true
	case tools.ShortcutMergeName:
		// Inspecting the host's exact-duplicate plan does not write anything.
		// Applying it removes entries and follows the delete approval policy.
		return !tools.ShortcutMergeReadOnly(req.Args)
	case tools.OfficeToolName:
		// This is a host-owned, path-confined editor capability, not a shell
		// manifest hint. Reads are observed without review; strict mode reviews
		// changes just as it reviews write/edit. Invalid arguments stay on the
		// mutating side until the tool rejects them.
		return p.mode == ModeStrict && !tools.OfficeReadOnly(req.Args)
	default:
		// bash, and any declared tool that did not claim to be read-only:
		// unknown blast radius, so ask.
		return true
	}
}

// commandOf pulls the command out of bash arguments, for the danger highlight and
// for anything that wants to show the user what was asked for.
func commandOf(args json.RawMessage) string {
	var a struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(args, &a) != nil {
		return ""
	}
	return strings.TrimSpace(a.Command)
}

// grantKeyOf is what an "always allow this command" grant is stored under.
//
// The command plus its workdir, not the command alone, and the difference is a real
// one rather than tidiness. Before bash had a workdir parameter, "always allow `go
// build ./...`" could only ever mean "in the session's directory", because there was
// nowhere else for it to run. Keying on the command alone now would silently promote
// that same click to "in any directory the agent names" — a widening the user was
// never shown and could not have intended.
//
// So a grant made for a bare command does not cover a workdir'd call, and one made
// for a workdir'd call does not cover the bare form. That is the same standard
// AllowCommand already holds itself to: exact text, no generalisation.
//
// NUL is the separator because it cannot occur in either field — both come out of a
// JSON string, and a NUL would have had to survive as an escape.
func grantKeyOf(args json.RawMessage) string {
	var a struct {
		Command string `json:"command"`
		Workdir string `json:"workdir"`
	}
	if json.Unmarshal(args, &a) != nil {
		return ""
	}
	cmd := strings.TrimSpace(a.Command)
	if cmd == "" {
		return ""
	}
	if dir := strings.TrimSpace(a.Workdir); dir != "" {
		return cmd + "\x00" + dir
	}
	return cmd
}

// dangerPatterns is a list for highlighting an approval card in red.
//
// It is a prompt for the human, not a control. It never blocks and never changes
// which calls get reviewed; treating it as protection would repeat the mistake
// that rules out a command whitelist. Its one behavioural effect is that a
// matching approval cannot be turned into an "always allow" grant, so a fast
// click cannot open something permanently.
var dangerPatterns = []string{
	"rm -rf", "rm -fr", "sudo ", "dd ", "mkfs", "chmod 777", "chown ",
	"curl | sh", "curl|sh", "wget | sh", "wget|sh", "| sh", "| bash",
	"> /dev/", "shutdown", "reboot", ":(){", "truncate -s 0",
	"git push --force", "git push -f", "git reset --hard", "git clean -fd",
	"npm publish", "docker rm", "kubectl delete",
}

// Danger returns the patterns a call matches.
func Danger(req agent.GateRequest) []string {
	text := commandOf(req.Args)
	if text == "" {
		text = string(req.Args)
	}
	lower := strings.ToLower(text)
	var hits []string
	for _, p := range dangerPatterns {
		if strings.Contains(lower, p) {
			hits = append(hits, strings.TrimSpace(p))
		}
	}
	return hits
}
