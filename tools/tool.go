// Package tools holds the four built-in tools. Each one is small, synchronous,
// and reports failures as text rather than aborting the agent loop.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yosukeno/pogo/webservice"
)

// Tool is the contract the agent loop depends on.
//
// A non-nil error is not fatal: the loop turns it into an is_error tool result
// and lets the model correct itself. This is the single most important behaviour
// inherited from pi.
//
// A tool may populate Result.Details even when it returns an error — a failed
// bash command still has an exit code and output worth showing.
type Tool interface {
	Name() string
	Description() string
	InputSchema() map[string]any
	// ExecutionMode reports whether this tool may run alongside its siblings in
	// the same batch. It is part of the interface rather than an optional method
	// so that adding a tool forces the question to be answered.
	ExecutionMode() ExecutionMode
	Execute(ctx context.Context, args json.RawMessage) (Result, error)
}

// ArgumentExecutionMode lets a tool make a read-only operation parallel while
// keeping its writable operation a barrier. Invalid arguments must return
// Sequential; input validation still runs before the tool executes.
type ArgumentExecutionMode interface {
	ExecutionModeForArgs(json.RawMessage) ExecutionMode
}

// SchemaProvider is an optional interface that tools can implement to provide
// their schema through reflection rather than hand-written map literals. It exists
// to remove the class of bug described on object below, where a tool with no
// mandatory arguments marshalled required as JSON null and took every request on
// one provider down with it.
type SchemaProvider interface {
	Schema() map[string]any
}

// ExecutionMode controls whether sibling tool calls in one assistant message may
// overlap.
type ExecutionMode int

const (
	// Parallel is safe for tools whose effects do not interfere.
	Parallel ExecutionMode = iota
	// Sequential means this call runs alone: nothing else in its batch overlaps
	// it, and it acts as a barrier between the calls before and after it.
	//
	// It no longer serializes the whole batch, which is what pi does and what this
	// did until the batch was split into segments — see agent.segments. Parallel
	// siblings still overlap each other; they just never overlap this.
	Sequential
)

// Registry looks tools up by name.
type Registry struct {
	byName map[string]Tool
	order  []Tool
	// Preserve the MCP insertion point and session options across run snapshots.
	mcpStart         int
	readOnly         bool
	sentinelEvent    bool
	spillDir         string
	results          *ResultStore
	scopedOutputRoot string
	resultDelivery   ResultDeliveryPolicy
	// Host-created optional built-ins survive selection updates without losing
	// their workspace, protection wrappers, browser link or private store.
	nativeTools map[string]Tool
}

func NewRegistry(ts ...Tool) *Registry {
	r := &Registry{byName: make(map[string]Tool, len(ts)), mcpStart: len(ts)}
	for _, t := range ts {
		if _, ok := t.(*MCPTool); ok && r.mcpStart == len(ts) {
			r.mcpStart = len(r.order)
		}
		r.byName[t.Name()] = t
		r.order = append(r.order, t)
	}
	return r
}

func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.byName[name]
	return t, ok
}

func (r *Registry) All() []Tool { return r.order }

// WithMCP creates a new registry for a new run. Only MCP entries are replaced;
// built-in and skill tool instances retain their state and order. The old
// registry remains usable by the run that captured it.
func (r *Registry) WithMCP(projected []Tool) *Registry {
	base := make([]Tool, 0, len(r.order))
	taken := make(map[string]bool)
	for _, t := range r.order {
		switch t.(type) {
		case *MCPTool:
			// Replaced by the projection below.
		case *MCPDescribe:
			// The old snapshot's helper must not leak into the new one: it
			// is rebuilt from the freshly accepted tools, or omitted when
			// the new catalog is empty.
		default:
			base = append(base, t)
			taken[t.Name()] = true
		}
	}
	start := min(r.mcpStart, len(base))
	ts := append([]Tool(nil), base[:start]...)
	for _, t := range projected {
		mt, ok := t.(*MCPTool)
		if !ok || taken[t.Name()] || hostShellJobName(t.Name()) || hostShortcutName(t.Name()) || hostSkillAuthorName(t.Name()) || hostWatchName(t.Name()) || hostWorkflowName(t.Name()) || hostUserInputName(t.Name()) || t.Name() == WebServiceName || (r.readOnly && !mt.IsReadOnly()) {
			continue
		}
		taken[t.Name()] = true
		ts = append(ts, mt.WithSpillDir(r.spillDir).withManagedResults())
	}
	if helper := newMCPDescribe(mcpToolsOf(ts[start:]), r.spillDir); helper != nil && !taken[MCPDescribeName] {
		ts = append(ts, helper)
	}
	ts = append(ts, base[start:]...)
	next := NewRegistry(ts...)
	next.mcpStart, next.readOnly, next.spillDir = start, r.readOnly, r.spillDir
	next.results, next.scopedOutputRoot = r.results, r.scopedOutputRoot
	next.resultDelivery = CloneResultDeliveryPolicy(r.resultDelivery)
	next.nativeTools = r.nativeTools
	next.sentinelEvent = r.sentinelEvent
	return next
}

// mcpToolsOf collects the MCP wrappers in a registry slice, in order. The
// slice boundaries the callers pass already delimit the MCP section.
func mcpToolsOf(ts []Tool) []*MCPTool {
	var out []*MCPTool
	for _, t := range ts {
		if mt, ok := t.(*MCPTool); ok {
			out = append(out, mt)
		}
	}
	return out
}

// nameTaken reports whether a tool with the given name already sits in the
// list — the precedence check that keeps a caller's own tool from being
// shadowed by an auto-installed helper.
func nameTaken(ts []Tool, name string) bool {
	for _, t := range ts {
		if t.Name() == name {
			return true
		}
	}
	return false
}

// EnvReadRoots carries extra readable roots to every pogo process in a tree.
//
// An environment variable rather than a flag field because a subagent child is
// re-invoked as a fresh process with a command line of its own — spawn forwards
// the environment, not the parent's flag set — so the environment is the one
// channel that reaches parent and children alike. -read-root folds its entries
// into this variable at startup for exactly that reason.
const EnvReadRoots = "POGO_READ_ROOTS"

// Options describes one session's tool set.
//
// A struct rather than more positional parameters because the three call sites now
// want three different sets: the terminal, the web server, and a subagent child
// that must not have the same tools its parent does.
type Options struct {
	// WebServices is an optional host-bound managed runtime, absent in children and hosted runs.
	WebServices webservice.Backend
	// ShellJobs is the host-owned process and notification lifetime for this session.
	ShellJobs *ShellJobRunner
	Cwd       string
	// FileAccess may widen ordinary file reads and writes to the authenticated
	// owner's workspace while that session is in full-access mode. Nil keeps
	// the existing working-directory/read-root rules, including CLI callers.
	FileAccess *FileAccess
	// Scope is an optional strict per-session OS boundary for Bash. It does
	// not enable shell tools in ReadOnly mode or change unscoped behavior.
	Scope *CommandScope
	// CommandProtectionManifest is trusted Web host state shared by ordinary
	// Bash and manifest Skill executors, independent of stricter CommandScope.
	CommandProtectionManifest string
	// ReadRoots are additional directories the read-only tools may reach, for skill
	// bundles kept outside the project. Never granted to write or edit: a skill is
	// instructions, and a model that could rewrite its own instructions has no
	// instructions.
	ReadRoots []string
	// WriteRoots are additional directories the file tools may reach *and modify*,
	// for the agent's memory notes.
	//
	// The same mechanism as ReadRoots with the opposite grant, and the two are kept
	// as separate fields rather than one field plus a flag so that the asymmetry is
	// impossible to get wrong by accident: New hands ReadRoots to four tools and
	// WriteRoots to six, and no caller can widen a skill bundle into a writable one
	// by passing a different boolean.
	//
	// bash gets neither, because bash never had a path restriction to widen. Memory
	// is therefore not protected from bash — nothing here pretends otherwise.
	WriteRoots []string
	// Subagent, when non-nil, registers the subagent tool. Nil in a child, which is
	// how nesting is bounded structurally in addition to the depth counter.
	Subagent *Subagent
	// Guard, when non-nil, restricts what bash may run. Set in a subagent child;
	// nil everywhere else, so the terminal and web paths are byte-for-byte what
	// they were.
	Guard *Guard
	// UserInput binds the main conversation to a real person's structured answers.
	// Nil in unattended runs and subagent children, where the tool is not registered.
	UserInput      UserInputBroker
	AsyncUserInput AsyncUserInputBroker
	// Todo registers the task-list tool. Set for a top-level session and withheld
	// from a subagent child, which is what every comparable harness does: OpenCode
	// denies todowrite to a spawned agent by default in the same rule that denies
	// it further delegation, and neither Claude Code's nor Gemini CLI's read-only
	// children are given a task list at all.
	//
	// The reason is that a child's list would have no reader. A task list earns its
	// tokens two ways — showing a person the progress, and surviving a compaction
	// boundary so the agent still knows where it was. A child lives for one run
	// under a ten-minute timeout, so it never reaches a boundary; and the progress
	// is already visible, in more detail, through the frames it forwards to the
	// parent's subagent card. What is left is a second writer for "what is being
	// worked on now", a question that has to have one answer.
	Todo bool
	// ReadOnly withholds write, edit and bash. Set in an explore-mode subagent
	// child: a session that cannot
	// reach a mutating tool cannot change anything, whatever directory it runs in.
	//
	// Withheld rather than refused. A tool that is absent costs no schema tokens and
	// cannot be attempted; one that always says no invites the model to try again
	// with different arguments.
	ReadOnly bool
	// Enabled, when non-nil, narrows the optional built-ins to the named set —
	// the Agent definition's tool selection (web/studio.go). Nil is the legacy
	// default: everything, which is what the terminal and every pre-selection
	// caller still get. Verbs with side effects answer to it
	// (write, edit, bash, browser, shortcut_manage); the protected core — read, ls, find, grep,
	// todo, subagent, analyze_image — is kept whatever the list says, because
	// the rest of the harness stands on it. Orthogonal to ReadOnly: the two
	// compose, ReadOnly being the session-level promise and Enabled the
	// definition-level choice. Declared (Synth) and projected (MCP) tools are
	// not named here; they have their own selection dimensions.
	Enabled *[]string
	// Journal, when non-nil, snapshots each file's pre-image the first time a
	// session changes it (the web workspace-changes view's data source). Nil in
	// the terminal, where no one is watching.
	Journal Journal
	// Synth are tools declared by skill manifests (LoadSynthTools). They sit
	// between the built-ins and the coordination tools in the registry: the
	// harness's own verbs first, the session's domain verbs next, and the ones
	// that describe a session last.
	//
	// A read-only session keeps only the declared tools that say they are
	// read-only (annotations.readOnlyHint), by the same structural rule that
	// withholds write and edit: what is not registered cannot be attempted.
	Synth []Tool
	// SkillReads is the trusted canonical-SKILL.md-to-identity snapshot of the
	// skills registered to this session, built by whoever loaded the skill
	// set. It lets the read tool record a real skill load — nothing else:
	// paths not in the map read unrecorded, and the map's paths never enter
	// an event. Nil where no skill telemetry exists.
	SkillReads []SkillReadTarget
	// MCP are tools projected from the external MCP servers the mcpclient
	// pool is connected to (Pool.Snapshot). They sit after Synth: the
	// harness's own verbs first, the skills' domain verbs next, the outside
	// world's verbs last.
	//
	// The pool rebuilds the projection as servers come and go; web sessions
	// take a fresh snapshot before each run using Registry.WithMCP. A
	// read-only session keeps only the tools whose server-sent annotations
	// say read-only — the same structural rule as Synth, with the caveat
	// that these hints cross a trust boundary the manifest's do not (see
	// the mcp.go file comment).
	MCP []Tool
	// Browser, when non-nil, registers the browser tool. It is the one built-in
	// whose executor does not live in this process: the link carries each
	// operation to the browser pane in the user's web UI and waits for its
	// answer. Nil in the terminal and in subagent children — no pane, no tool,
	// the same structural rule as every other capability grant here.
	//
	// Registered even for read-only sessions, unlike write/edit/bash: a
	// snapshot cannot change the page, and the link (not the registry) enforces
	// the armed-task gate that keeps ordinary conversations from driving pages.
	Browser BrowserLink
	// Information is the host-selected workspace library. It has no mutation
	// actions and is inherited independently of optional Skills or MCP servers.
	Information *Information
	// Shortcuts is bound by the web host to one owner and Agent. Reads expose
	// authorized snapshot handles; creation is an optional native capability.
	Shortcuts ShortcutBackend
	// ShortcutManagement is an optional host-bound management interface. When
	// omitted, an extended Shortcuts backend may provide it without changing
	// the older search/read/create contract or granting unselected mutations.
	ShortcutManagement ShortcutManagementBackend
	// SkillAuthor is bound by the host to an owner, Agent and session. It
	// exposes opaque, read-only document views plus optional controlled save;
	// it never adds the private Skill store to ordinary filesystem roots.
	SkillAuthor SkillAuthorBackend
	// Watches is a host-bound, source-agnostic persistent script service.
	Watches WatchBackend
	// SentinelEvent is set only by the host for a Sentinel event execution.
	// Ordinary conversations do not receive the current-batch decision tool,
	// even if an old selection explicitly lists watch_decide.
	SentinelEvent bool
	// BackgroundTasks is a separate optional grant; selecting watch alone must
	// never add permission to create scheduled Agent executions.
	BackgroundTasks BackgroundTaskBackend
	// AgentWorkflows manages procedures bound to the current user and Agent.
	AgentWorkflows AgentWorkflowBackend
	// BrowserImageMode describes the actual image delivery path. A configured
	// helper upgrades DOM-only mode below, only when that tool is registered.
	BrowserImageMode BrowserImageMode
	// Images, when non-nil, registers the analyze_image tool: the bridge a
	// text-only model uses to ask the configured vision model about an attached
	// image. Nil where no vision helper exists or a child should not spend its
	// turn on images — the same structural rule as Browser.
	Images *ImageAnalyze
	// SpillDir is where a tool writes output too large for its result. Empty
	// means DefaultSpillDir.
	//
	// It is here, on the same struct that carries ReadRoots, because New adds it
	// to the readable roots — and that union is the whole feature. A spill note
	// hands the model a path and tells it to read the file; read refuses paths
	// outside the roots; so a spill directory that is not a root produces a
	// pointer to nothing, which is worse than plain truncation because the model
	// believes it can recover the content. Deciding both in one place makes that
	// impossible to get wrong from a deployment's side, which is where it was
	// already wrong: bash has spilled to /tmp since the beginning, and any
	// deployment setting POGO_READ_ROOTS has had unreadable spill files ever
	// since.
	//
	// Never added to WriteRoots. The model reads spilled output; nothing is
	// served by letting it rewrite the record of what a command printed.
	SpillDir string
	// ResultScope is a trusted, stable owner/session identity. Web and CLI
	// supply it so result IDs survive resume without granting other sessions'
	// observations. Empty gives this registry an ephemeral result namespace.
	ResultScope string
	// ResultDelivery controls only the request view of saved tool observations.
	// It never changes third-party tool arguments, stored bytes or UI history.
	ResultDelivery ResultDeliveryPolicy
	// LegacyResultPaths is supplied by the host from persisted tool details.
	// Only exact, regular, direct-child legacy spill files are retained as
	// readable roots; this never grants the shared spill directory.
	LegacyResultPaths []string
}

// Default returns the file and shell built-ins rooted at cwd, with no subagent,
// no guard and no task list. Kept as the plain entry point because most callers
// want exactly this: the tools that do something to the workspace, without the
// two that describe a session.
func Default(cwd string, readRoots ...string) *Registry {
	return New(Options{Cwd: cwd, ReadRoots: readRoots})
}

// New builds a registry from an explicit description of the session.
func New(o Options) *Registry {
	spillDir := o.SpillDir
	if strings.TrimSpace(spillDir) == "" {
		spillDir = DefaultSpillDir()
	}
	legacyRoots := legacyResultReadRoots(spillDir, o.LegacyResultPaths)
	spillDir, resultStore := newSessionResultStorage(spillDir, o.ResultScope)

	// Writable roots are readable too — a note you cannot read is not a note — so the
	// read-only four get the union while write and edit get only the writable half.
	// Canonicalised once here rather than per call, like ReadRoots.
	writeRoots := CanonicalRoots(o.WriteRoots)
	readRoots := append(CanonicalRoots(o.ReadRoots), writeRoots...)
	readRoots = append(readRoots, legacyRoots...)
	// The spill directory joins the readable roots, and this is the line that makes
	// a spill note worth anything: it hands the model a path and tells it to read
	// the file, and read refuses everything outside these roots.
	//
	// Added without CanonicalRoots' existence check, and deliberately so. The
	// directory is created on first spill rather than here, because building a
	// registry should not touch the filesystem — every test that constructs one
	// would otherwise make a directory under the developer's home. Dropping a root
	// for not existing yet would take the guarantee with it, and canonical()
	// already tolerates a path whose tail is not there: that is the same property
	// write and edit rely on for files they are about to create.
	if abs, err := filepath.Abs(spillDir); err == nil {
		readRoots = append(readRoots, canonical(abs))
	}
	ts := []Tool{
		// The read-only four are truncated and deliberately never spilled. What
		// they were reading is still there: read takes an offset, grep and find
		// take a narrower pattern, ls takes a subdirectory. Spilling would write a
		// file on every large read, hand the model a copy that goes stale the
		// moment anything edits the original, and — since the spill file is itself
		// read with these tools — set up a truncate-spill-read-truncate cascade
		// with nothing at the bottom of it.
		&Read{Cwd: o.Cwd, Roots: readRoots, FileAccess: o.FileAccess, skillReads: skillReadIndex(o.SkillReads)},
		&Ls{Cwd: o.Cwd, Roots: readRoots, FileAccess: o.FileAccess},
		&Find{Cwd: o.Cwd, Roots: readRoots, FileAccess: o.FileAccess},
		&Grep{Cwd: o.Cwd, Roots: readRoots, FileAccess: o.FileAccess},
		&ToolResultRead{Store: resultStore},
	}
	if o.Information != nil {
		copy := *o.Information
		copy.Cwd = o.Cwd
		ts = append(ts, &copy)
	}
	management := shortcutManagementFor(o)
	if shortcutBackendAvailable(o.Shortcuts) || management != nil {
		ts = append(ts, &ShortcutQuery{Backend: o.Shortcuts, Management: management})
		if !o.ReadOnly {
			manage := (&ShortcutManage{Backend: o.Shortcuts, Management: management}).withSelection(o.Enabled)
			if len(manage.actionTools()) > 0 {
				ts = append(ts, manage)
			}
		}
	}
	if management != nil && !o.ReadOnly {
		for _, tool := range []Tool{&ShortcutDelete{Backend: management}, &ShortcutMerge{Backend: management}} {
			if o.keeps(tool.Name()) {
				ts = append(ts, tool)
			}
		}
	}
	if o.Watches != nil && !o.ReadOnly {
		for _, t := range NewWatchTools(o.Watches) {
			if t.Name() == WatchDecideName && !o.SentinelEvent {
				continue
			}
			if o.keeps(t.Name()) {
				ts = append(ts, t)
			}
		}
	}
	if o.WebServices != nil && !o.ReadOnly && o.keeps(WebServiceName) {
		ts = append(ts, &WebService{Backend: o.WebServices})
	}
	if o.BackgroundTasks != nil && !o.ReadOnly && o.keeps(BackgroundTaskName) {
		ts = append(ts, &BackgroundTask{Backend: o.BackgroundTasks})
	}
	for _, tool := range NewAgentWorkflowTools(o.AgentWorkflows) {
		if o.keeps(tool.Name()) && (!o.ReadOnly || tool.(ReadOnlyCapable).IsReadOnly()) {
			ts = append(ts, tool)
		}
	}
	if skillAuthorBackendAvailable(o.SkillAuthor) {
		ts = append(ts, &SkillSearch{Backend: o.SkillAuthor}, &RuntimeSkillRead{Backend: o.SkillAuthor})
		if !o.ReadOnly && o.keeps(SkillSaveName) {
			ts = append(ts, &SkillSave{Backend: o.SkillAuthor})
		}
	}
	if !o.ReadOnly {
		// The write trio is the selectable half of the built-ins: a definition
		// may withhold any of them (Options.Enabled), and each is filtered on
		// its own name so e.g. a no-bash analyst keeps write and edit.
		if o.keeps("write") {
			ts = append(ts, &Write{Cwd: o.Cwd, Roots: writeRoots, FileAccess: o.FileAccess, Journal: o.Journal, ProtectionManifest: o.CommandProtectionManifest})
		}
		if o.keeps("edit") {
			ts = append(ts, &Edit{Cwd: o.Cwd, Roots: writeRoots, FileAccess: o.FileAccess, Journal: o.Journal, ProtectionManifest: o.CommandProtectionManifest})
		}
		if o.keeps("bash") {
			ts = append(ts, &Bash{Cwd: o.Cwd, Guard: o.Guard, SpillDir: spillDir, Scope: o.Scope, ProtectionManifest: o.CommandProtectionManifest, Jobs: o.ShellJobs})
			ts = append(ts, shellJobTools(o.ShellJobs)...)
		}
	}
	// Declared tools, filtered for a read-only session by each tool's own
	// statement — see Options.Synth. Type-asserted rather than a second slice
	// on Options because the annotation lives on the tool and the filter is
	// only ever asked here.
	for _, t := range o.Synth {
		if t.Name() == ToolResultReadName || hostShellJobName(t.Name()) || hostShortcutName(t.Name()) || hostWatchName(t.Name()) || hostWorkflowName(t.Name()) || hostUserInputName(t.Name()) || t.Name() == WebServiceName {
			continue // the host's read-only recovery boundary cannot be replaced
		}
		if hostSkillAuthorName(t.Name()) {
			// A runtime projection may supply its trusted reader only when
			// there is no native host view. Manifests cannot impersonate it.
			if reader, ok := t.(*RuntimeSkillRead); !ok || t.Name() != SkillReadName || reader.Session == nil || skillAuthorBackendAvailable(o.SkillAuthor) || nameTaken(ts, SkillReadName) {
				continue
			}
		}
		if ro, ok := t.(ReadOnlyCapable); ok && o.ReadOnly && !ro.IsReadOnly() {
			continue
		}
		// Bind a private copy: the loader's instances are shared across sessions,
		// but relative paths and spilled output belong to this workspace.
		if st, ok := t.(*SynthTool); ok {
			clone := st.WithWorkspace(o.Cwd, spillDir)
			clone.ProtectionManifest = o.CommandProtectionManifest
			t = clone
		}
		ts = append(ts, t)
	}
	// MCP-projected tools, same shape as the Synth loop above and for the
	// same two reasons: the read-only filter is per-tool, and the caller
	// (the pool) builds the tool while the session owns the spill directory.
	mcpStart := len(ts)
	for _, t := range o.MCP {
		if t.Name() == ToolResultReadName || hostShellJobName(t.Name()) || hostShortcutName(t.Name()) || hostSkillAuthorName(t.Name()) || hostWatchName(t.Name()) || hostWorkflowName(t.Name()) || hostUserInputName(t.Name()) || t.Name() == WebServiceName {
			continue
		}
		if ro, ok := t.(ReadOnlyCapable); ok && o.ReadOnly && !ro.IsReadOnly() {
			continue
		}
		if mt, ok := t.(*MCPTool); ok {
			t = mt.WithSpillDir(spillDir).withManagedResults()
		}
		ts = append(ts, t)
	}
	// The inspection helper closes the MCP section: it is built from exactly
	// the tools that survived the filters above, so it can never describe a
	// tool this session does not have. Skipped when the name is taken — a
	// caller's own tool named mcp_describe wins, same precedence rule as
	// every other name in the registry.
	if helper := newMCPDescribe(mcpToolsOf(ts[mcpStart:]), spillDir); helper != nil {
		if !nameTaken(ts, MCPDescribeName) {
			ts = append(ts, helper)
		}
	}
	// The browser tool sits with the domain verbs, not the file verbs: it acts
	// on a page the way mal.search acts on a library, and a read-only child
	// keeps it for the same reason it keeps the declared read-only tools —
	// whether an operation mutates is decided per action inside the link's
	// armed-task gate, and the policy reviews the mutating ones. A definition
	// that withholds it (Options.Enabled) also closes the armed-task grant,
	// because the skill that raises it selected nothing the arm would serve.
	if o.Browser != nil && o.keeps("browser") {
		mode := o.BrowserImageMode
		if mode == BrowserImageNone && o.Images != nil {
			mode = BrowserImageHelper
		}
		ts = append(ts, &Browser{Link: o.Browser, SpillDir: spillDir, ImageMode: mode})
	}
	// The image bridge sits with the browser tool among the domain verbs: it
	// also reaches outside this process (to the vision model) and is kept in
	// read-only sessions for the same reason — asking about a picture changes
	// nothing.
	if o.Images != nil {
		ts = append(ts, o.Images)
	}
	// Conversation-local coordination goes after the tools that touch files.
	// Questions require a host-bound user; task lists belong to the main session.
	if o.UserInput != nil {
		ts = append(ts, &RequestUserInput{Broker: o.UserInput})
	}
	if o.AsyncUserInput != nil {
		ts = append(ts, &RequestUserInputAsync{Broker: o.AsyncUserInput})
	}
	if o.Todo {
		ts = append(ts, &Todo{})
	}
	// Enforced here rather than only at the call site: delegating is not itself a
	// write, but a read-only session that could spawn a read-write one would be a
	// way around the promise it was built on, and that promise is what lets explore
	// mode run in the parent's own directory.
	if o.Subagent != nil && !o.ReadOnly {
		// Same assignment as the synth tools above and for the same reason: the
		// caller builds the tool, the session decides where output goes.
		o.Subagent.SpillDir = spillDir
		o.Subagent.FileAccess = o.FileAccess
		ts = append(ts, o.Subagent)
		// The status tool only exists alongside a background runner: without one
		// every delegation is synchronous and there is never anything to list.
		if o.Subagent.Bg != nil {
			ts = append(ts, &SubagentTasks{Bg: o.Subagent.Bg})
		}
	}
	if o.Information != nil && o.Information.Store != nil {
		for i, tool := range ts {
			ts[i] = protectInformationFileTool(tool, o.Cwd, o.Information.Store.Root())
		}
	}
	r := NewRegistry(ts...)
	r.mcpStart, r.readOnly, r.spillDir = mcpStart, o.ReadOnly, spillDir
	r.results = resultStore
	r.nativeTools = nativeToolInstances(o, writeRoots, spillDir, r)
	r.sentinelEvent = o.SentinelEvent && !o.ReadOnly
	r.resultDelivery = CloneResultDeliveryPolicy(o.ResultDelivery)
	if o.Scope != nil {
		r.scopedOutputRoot = o.Scope.Cwd
	}
	return r
}

// Schemas used to render the registry as tool declarations and is deleted rather
// than kept for symmetry: it had no caller anywhere, not even a test, because the
// declarations the model actually receives are built in agent.New straight into
// llm.ToolSchema. It could not have acquired one either — the slice it returned held
// an unexported type, so no package outside this one could name the result.
//
// Left as a note because the shape is worth not rebuilding: rendering belongs where
// the wire type is known, and a second renderer here would be a place for the two to
// drift while both compiled.

// --- shared helpers ---

// object is a tiny helper for the hand-written JSON Schema literals.
//
// A nil required list becomes an empty array rather than JSON null. Go marshals a
// nil slice to null, and at least one provider (moonshot/kimi) rejects the whole
// request with "required must be an array" — so a tool with no mandatory
// arguments would take down every call, not just its own.
func object(required []string, props map[string]any) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}

func prop(typ, desc string) map[string]any {
	return map[string]any{"type": typ, "description": desc}
}

// orderedProps is a JSON object that marshals its keys in declaration order,
// unlike encoding/json's alphabetical sort of maps. Property order is the one
// hint that decides which argument a model emits first, and a streaming UI
// wants path before content: the path is what names the in-progress preview,
// so it must arrive in the first fragments, not after the whole content has
// streamed. Verified against k3 (2026-08): an alphabetical — content-first —
// schema makes it emit the path last; a path-first schema flips it.
type orderedProps []propPair

type propPair struct {
	name string
	spec any
}

func (o orderedProps) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, p := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		name, _ := json.Marshal(p.name) // a plain string key never fails
		spec, err := json.Marshal(p.spec)
		if err != nil {
			return nil, err
		}
		b.Write(name)
		b.WriteByte(':')
		b.Write(spec)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// objectOrdered is object with a preserved property order; see orderedProps.
func objectOrdered(required []string, props orderedProps) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}

// resolve turns a possibly relative tool path into an absolute one and refuses
// to escape the working root. Sandboxing here is deliberately coarse: it stops
// accidental writes outside the project, not a determined adversary.
//
// extraRoots widens the check for read-only tools only. It exists so that skill
// bundles, which live outside the project by design, can be read without
// becoming writable: write and edit never pass any, so the widened set is
// readable and nothing more. The roots must already be canonical — see
// CanonicalRoots — because this runs on every tool call.
func resolve(cwd, path string, extraRoots ...string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cwd, abs)
	}
	abs = filepath.Clean(abs)

	real := canonical(abs)
	if within(real, canonical(cwd)) {
		return abs, nil
	}
	for _, root := range extraRoots {
		if within(real, root) {
			return abs, nil
		}
	}
	if len(extraRoots) > 0 {
		return "", fmt.Errorf("path %s is outside the working directory %s and the readable roots %s",
			path, cwd, strings.Join(extraRoots, ", "))
	}
	return "", fmt.Errorf("path %s is outside the working directory %s", path, cwd)
}

// Resolve is resolve exported for callers outside the package — the web file
// browser lists and reads workspace files over HTTP and needs the exact same
// canonicalisation and escape refusal the tools get. Same coarse-sandbox
// caveat: this stops accidents, not adversaries.
func Resolve(cwd, path string, extraRoots ...string) (string, error) {
	return resolve(cwd, path, extraRoots...)
}

// CanonicalRoots resolves extra read-only roots once, at construction, so that
// the per-call path check stays free of filesystem work. Empty entries and paths
// that do not resolve are dropped: a root that is not there cannot grant
// anything, and keeping it would only produce confusing error messages.
func CanonicalRoots(paths []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range paths {
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		root := canonical(abs)
		if _, err := os.Stat(root); err != nil || seen[root] {
			continue
		}
		seen[root] = true
		out = append(out, root)
	}
	return out
}

// canonical resolves symlinks as far down the path as it exists, keeping the
// non-existent tail intact.
//
// Resolving symlinks matters because otherwise a link inside the root pointing
// outside it would slip through. Tolerating a non-existent tail matters because
// write and edit legitimately target files — and, via MkdirAll, whole
// directories — that do not exist yet.
func canonical(path string) string {
	remainder := ""
	for cur := path; ; {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(resolved, remainder)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return path // reached the filesystem root with nothing resolvable
		}
		remainder = filepath.Join(filepath.Base(cur), remainder)
		cur = parent
	}
}

// within reports whether path is root or sits inside it. Both are expected to have
// been through canonical already.
//
// The byte comparison is the entire answer on a case-sensitive filesystem, and it
// stays the fast path everywhere: no syscall, which is what keeps a per-call check
// cheap enough to sit in front of every read and write.
//
// The fallback exists because macOS and Windows default to case-insensitive
// volumes, where /Users/x and /users/x are one directory with two spellings.
// canonical does not normalise case — EvalSymlinks resolves links and hands back
// the components as spelled — so a model that lowercases a path gets a refusal for
// a directory it is plainly inside. Fail-closed, so nothing ever escaped; write
// simply stopped working, with an error naming two paths that look the same.
//
// Folding the comparison outright would be the wrong repair. On a case-sensitive
// filesystem /tmp/Foo and /tmp/foo are different directories, and treating one as
// the other would put a real hole in the only check that keeps the tools inside the
// workspace. So instead of guessing the volume's collation, the fallback asks the
// filesystem: same device and inode means one directory whatever the spelling, and
// on a case-sensitive volume two spellings cannot be the same file. Two stats, paid
// only on a path that was about to be refused anyway.
//
// Only the root-length prefix is stat'ed, never the full path: write and edit
// legitimately target files that do not exist yet, and the question here is about
// the ancestor, not the leaf.
func within(path, root string) bool {
	if path == root || strings.HasPrefix(path, root+string(os.PathSeparator)) {
		return true
	}
	if !foldWithin(path, root) {
		return false
	}
	return sameDir(path[:len(root)], root)
}

// foldWithin is the same containment test with case ignored. It decides only
// whether asking the filesystem is worth the syscalls; it never grants anything on
// its own.
func foldWithin(path, root string) bool {
	if len(path) < len(root) {
		return false
	}
	if !strings.EqualFold(path[:len(root)], root) {
		return false
	}
	return len(path) == len(root) || path[len(root)] == os.PathSeparator
}

// sameDir reports whether two spellings name one directory. A path that does not
// resolve is not the same as anything, which keeps the failure direction the same
// as the rest of the guard.
func sameDir(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
}

func unmarshal(args json.RawMessage, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(args)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		// Retry leniently: models routinely add harmless extra keys, and a hard
		// failure there wastes a turn.
		return json.Unmarshal(args, v)
	}
	return nil
}
