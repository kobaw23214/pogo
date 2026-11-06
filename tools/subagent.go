package tools

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yosukeno/pogo/mcpobserve"
)

// DefaultSubagentTimeout bounds one delegated task.
//
// Lower than it sounds: a subagent that has not finished in ten minutes is
// usually looping, and the parent is blocked on it. The bound is wall clock rather
// than turns because the turn limit is already passed to the child, and the two
// failure modes are different — a slow model and a stuck agent both need to end.
const DefaultSubagentTimeout = 10 * time.Minute

// DefaultSubagentMaxDepth is how many layers of delegation are allowed below the
// main conversation. One means a subagent cannot spawn its own.
//
// Deliberately shallow. Each layer multiplies the fixed per-turn cost (system
// prompt plus tool schemas) and makes the tree harder to reason about, while the
// cases that need depth are rare enough to ask for explicitly.
const DefaultSubagentMaxDepth = 1

// DefaultSubagentConcurrency is how many children one session runs at once.
//
// The number is fixed by web.CheckApprovalBudget rather than chosen: with a five
// minute gate timeout and a thirty minute run, two is what fits alongside the
// parent's own approval budget. Raising it means lowering one of those.
const DefaultSubagentConcurrency = 2

// Environment variables that describe a child's situation to itself. They are
// environment rather than flags because they must not be forgeable by the model:
// a model can propose flags through the tool it is calling, but it never writes
// the environment of a process the parent spawns.
const (
	EnvSubagentDepth       = "POGO_SUBAGENT_DEPTH"
	EnvSubagentReadOnly    = "POGO_SUBAGENT_READONLY"
	EnvSubagentNativeTools = "POGO_SUBAGENT_NATIVE_TOOLS"
	// EnvSubagentEditLeaseFD identifies a child-owned lease descriptor, never a
	// model option. The child seals it against inheritance by tool processes.
	EnvSubagentEditLeaseFD = "POGO_SUBAGENT_EDIT_LEASE_FD"
)

// Writable delegation uses the current workspace and a host-wide writer lease.
// IsolationShared remains as the on-disk vocabulary for existing transcripts.
const IsolationShared = "shared"

// EnvIsolation is accepted only to diagnose retired deployment configuration.
const EnvIsolation = "POGO_SUBAGENT_ISOLATION"

// EnvConcurrency overrides how many children one session runs at once.
//
// Configurable because DefaultSubagentConcurrency is derived from the approval
// gate's arithmetic (see the constant), and a deployment that fans out over a fixed
// set of inputs is bound by CPU instead. Raising it is checked against the timeouts
// by web.CheckApprovalBudget, which is where the two constraints meet.
const EnvConcurrency = "POGO_SUBAGENT_CONCURRENCY"

// IsolationFromEnv accepts the current shared behavior and refuses retired
// worktree configuration explicitly; changing an existing deployment is not silent.
func IsolationFromEnv() (string, error) {
	switch v := strings.TrimSpace(os.Getenv(EnvIsolation)); v {
	case "", IsolationShared:
		return IsolationShared, nil
	case "worktree":
		return "", fmt.Errorf("%s=worktree is no longer supported; remove this setting or use shared: writable subagents now run synchronously in the workspace under a host-wide writer lease", EnvIsolation)
	default:
		return "", fmt.Errorf("%s=%q is not supported; remove this setting or use shared", EnvIsolation, v)
	}
}

// ConcurrencyFromEnv reads EnvConcurrency, returning 0 when it is unset or
// unusable — the same "leave it to the default" value the field takes.
func ConcurrencyFromEnv() (int, error) {
	raw := strings.TrimSpace(os.Getenv(EnvConcurrency))
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s=%q is not a positive whole number of subagents", EnvConcurrency, raw)
	}
	return n, nil
}

// Explore children have no write, edit or shell tools. Edit children share the
// workspace, run synchronously and acquire the same writer lease across sessions.
const (
	ModeExplore = "explore"
	ModeEdit    = "edit"
)

// Subagent delegates a task to a separate pogo process.
type Subagent struct {
	// Trusted parent selection; a child cannot widen it through model arguments.
	Enabled *[]string
	// These are set from the parent's trusted workspace store, never tool args.
	// Empty withholds the library and removes any unrelated process-global grant.
	InformationRoot  string
	InformationScope string
	// Trusted parent document restrictions travel to every retained/background
	// child. This adds restrictions; a model argument can never select a path.
	CommandProtectionManifest string
	// FileAccess is a live parent capability, never a model argument. Children
	// receive its current grant through their private approval channel.
	FileAccess *FileAccess
	// Store enables retained child contexts scoped to this host-owned parent.
	// Nil preserves the one-shot contract for callers without a session store.
	Store *SubagentStore
	// StoreError fails closed when the host could not open durable context.
	StoreError error
	// Cwd is the workspace shared with the parent. No repository is required.
	Cwd string
	// Exe is the pogo binary to spawn. Injected rather than resolved on every
	// call so that tests can point it at a stub instead of requiring `go build`.
	Exe string
	// Model and MaxTurns are passed down. Inheriting them explicitly beats letting
	// the child fall back to its own defaults: a parent running an expensive model
	// silently delegating to the cheap default is the kind of drift nobody notices
	// until the bill.
	Model string
	// Model and ExploreModel are initialization fields. Hosts changing the
	// parent at runtime use SetParentModel; internal readers take a snapshot.
	modelMu sync.RWMutex
	// MaxTurns bounds the child's own run. Zero leaves it to the child's default,
	// which is the usual case.
	//
	// Deliberately *not* the parent's -max-turns, unlike Model. A turn limit bounds
	// one run, and a subagent is a different run: how many steps one delegated task
	// needs has nothing to do with how much conversation the parent has left. Passing
	// it down means `-max-turns 4`, which reads as "do not let this ramble", silently
	// becomes "and cripple everything you delegate" — observed live, where a child
	// spent its four turns reading files and returned nothing at all. Cost stays
	// bounded by Timeout and by the usage rolled up into the parent's token budget,
	// which are the instruments meant for it.
	MaxTurns int
	// ExploreModel overrides Model for read-only children. Empty means inherit,
	// which is the default and the existing behaviour.
	//
	// Only read-only delegation gets its own model, and the asymmetry is deliberate.
	// Finding where something is implemented is lighter work than implementing it,
	// and it is the case where latency shows: the parent's turn is blocked until the
	// answer arrives. An edit child is doing the same work the parent would have
	// done, so quietly downgrading it would change the result of the task rather
	// than the cost of a lookup.
	ExploreModel string
	// ExplorePool spreads read-only children across providers. Each fork runs on
	// the pool entry with the fewest in-flight children (ties break in pool
	// order), so a burst of parallel subagents lands one-per-provider instead of
	// all hitting the same endpoint's concurrency limit. ExploreModel, when set,
	// still wins over the pool; an empty pool means inherit Model.
	ExplorePool []ExploreTarget

	// exploreMu guards exploreInflight, the per-provider running counts the pool
	// balances on. Charged at fork, released when the child exits.
	exploreMu       sync.Mutex
	exploreInflight map[string]int
	// Timeout bounds one call. Zero means DefaultSubagentTimeout.
	Timeout time.Duration
	// SpillDir holds a child's answer when it did not fit in the result. Set by
	// New alongside the readable roots; empty means DefaultSpillDir.
	//
	// Not forwarded to the child on the command line, and it does not need to be:
	// a child resolves its own directory from POGO_SPILL_DIR through
	// DefaultSpillDir, and spawn hands it the parent's whole environment. So
	// parent and child agree on the location by default, and a deployment that
	// moves it moves it for the whole process tree with one variable — the same
	// arrangement POGO_READ_ROOTS uses, for the same reason.
	SpillDir string
	// Depth is this process's depth; a child gets Depth+1. MaxDepth zero means
	// DefaultSubagentMaxDepth.
	Depth    int
	MaxDepth int

	// SkillPaths are the skill bundles this session loaded, forwarded to every
	// child as -skill flags.
	//
	// Skills are an environment property, not a per-delegation choice — the same
	// rule Claude Code settles on for its subagents: capabilities installed for a
	// session are available to the children it spawns, and curation is an opt-in
	// restriction. The parent does not pick skills per task, and the reason is
	// observed rather than principled: a parent that assumes a child has its tools
	// (mal_similar, say) writes a task the child cannot execute at all, and one
	// asked to choose per call will sometimes forget. Forwarding everything and
	// letting the registry's readOnlyHint filter split explore from edit keeps
	// the permission model intact — read-only declared tools reach an explore
	// child, mutating ones only an edit child — with no new decision for the
	// model to get wrong.
	//
	// Empty in a plain coding session, and everything keyed off it (the
	// description paragraph, the explore note's tool list) reads as absent.
	SkillPaths []string
	// ExplicitSkills disables the child's automatic skill discovery. Studio
	// sessions with an explicit selection, including an empty selection, pass
	// only SkillPaths and their readable roots instead of the process defaults.
	ExplicitSkills bool
	SkillReadRoots []string

	// ReadOnlySkillTools names the declared tools an explore child will keep
	// (readOnlyHint in the manifest; the registry applies the filter, this list
	// only reports it). Spelled out in the explore note so a child knows what it
	// holds without inferring it from the tool list — the same silence-as-mystery
	// failure the note exists to prevent.
	ReadOnlySkillTools []string

	// Concurrency bounds how many children this session runs at once. Zero means
	// DefaultSubagentConcurrency.
	//
	// Deliberately not the loop's tool concurrency, which is eight. A subagent is
	// not a tool call: it is a whole context window with its own per-turn fixed cost,
	// its own process, and — under an approval gate — its own claim on the
	// reviewer's attention. Eight of those at once breaks the timeout arithmetic in
	// web.CheckApprovalBudget before it breaks anything else.
	Concurrency int

	// sem is the limiter, built once per tool instance. The registry is per session,
	// so that is per session, which is the right scope: two browser tabs on the same
	// session share a budget, two sessions do not.
	semOnce sync.Once
	sem     chan struct{}

	// Review, when non-nil, decides whether a tool call the child wants to make may
	// run. Nil means the child gets no gate, which is the terminal's situation:
	// there is no human there to ask, and -p has to stay scriptable.
	//
	// The child asks about every call rather than filtering locally, and it does not
	// get a copy of the policy. That is the point: the parent applies its own
	// policy, so "a subagent's permissions never exceed its parent's" holds by
	// construction instead of by a snapshot that could go stale or be forged. The
	// read-only tools cost one pipe round-trip and no human attention, because the
	// parent's policy passes them without a prompt.
	Review ReviewFunc

	// Bg, when non-nil, lets a delegation run in the background: the tool call
	// returns immediately with the task id, and the child's result is delivered
	// through the runner's hooks whenever it finishes. Nil keeps every call
	// synchronous — the terminal's situation, where a one-shot process has no
	// "later" to deliver anything in.
	Bg *BgRunner

	// Browser, when non-nil, lets a foreground child drive the parent's browser
	// pane over a pipe, the same way Review lets it borrow the parent's approval
	// gate. Nil in the terminal, which has no pane, and the channel is withheld
	// from backgrounded children whatever this holds — see wireBrowser.
	//
	// The value of delegating a browsing task is context, not concurrency: a
	// research child spends ten snapshots and the parent's context receives one
	// summary. There is one pane, so browsing children are serialized by
	// browserLease; that is a correctness requirement, since refs belong to a
	// document and two children navigating it invalidate each other's.
	Browser BrowserLink

	// browserLease serializes children over the single pane. Created on first use
	// so a Subagent that never delegates browsing carries no extra state.
	browserLease *browserLease
}

// ReviewFunc is the parent's approval decision for a child's tool call.
type ReviewFunc func(ctx context.Context, req Approval) Decision

// ExploreTarget is one provider an explore child may run on. The pool balances
// per Provider, so two entries naming the same provider share one in-flight
// count; Model is what the child is actually launched with.
type ExploreTarget struct {
	Provider string
	Model    string
}

// pickExplore chooses what one explore child runs and charges the provider one
// in-flight slot. The explicit single-model mapping wins over the pool, and no
// pool means the child inherits its parent's model — the behaviour before pools
// existed. A "" provider means no slot was charged and releaseExplore is a no-op.
func (s *Subagent) pickExplore() (model, provider string) {
	s.modelMu.RLock()
	defer s.modelMu.RUnlock()
	return s.pickExploreLocked()
}

// SetParentModel changes defaults for future delegations. An empty
// ExploreModel follows the parent through fallback; an explicitly configured
// explore model or provider pool remains independent. Already dispatched runs
// and retained child records keep their captured model.
func (s *Subagent) SetParentModel(model string) {
	s.modelMu.Lock()
	s.Model = model
	s.modelMu.Unlock()
}

func (s *Subagent) parentModel() string {
	s.modelMu.RLock()
	defer s.modelMu.RUnlock()
	return s.Model
}

func (s *Subagent) pickDelegationModel(mode string) (model, provider string) {
	s.modelMu.RLock()
	defer s.modelMu.RUnlock()
	model = s.Model
	if mode == ModeExplore {
		picked, p := s.pickExploreLocked()
		provider = p
		if picked != "" {
			model = picked
		}
	}
	return model, provider
}

// modelMu is held so inherited and explicit defaults belong to one snapshot.
func (s *Subagent) pickExploreLocked() (model, provider string) {
	if s.ExploreModel != "" {
		return s.ExploreModel, ""
	}
	if len(s.ExplorePool) == 0 {
		return "", ""
	}
	s.exploreMu.Lock()
	defer s.exploreMu.Unlock()
	// Least in flight wins; ties keep pool order, so the file's writing order is
	// the preference order when everything is equally busy.
	best := 0
	for i := 1; i < len(s.ExplorePool); i++ {
		if s.exploreInflight[s.ExplorePool[i].Provider] < s.exploreInflight[s.ExplorePool[best].Provider] {
			best = i
		}
	}
	t := s.ExplorePool[best]
	if s.exploreInflight == nil {
		s.exploreInflight = make(map[string]int, len(s.ExplorePool))
	}
	s.exploreInflight[t.Provider]++
	return t.Model, t.Provider
}

// releaseExplore returns the slot pickExplore charged. It must run when the
// child exits, or a finished subagent would keep scaring forks away from its
// provider forever.
func (s *Subagent) releaseExplore(provider string) {
	if provider == "" {
		return
	}
	s.exploreMu.Lock()
	s.exploreInflight[provider]--
	s.exploreMu.Unlock()
}

// Approval is one child's request to run one tool.
type Approval struct {
	// Subagent is the task id of the child asking, so a reviewer can tell which
	// delegated task a command belongs to.
	Subagent string
	CallID   string
	Tool     string
	Args     json.RawMessage
}

// Decision is the answer. A refusal carries a reason, which the child hands to its
// own model as the tool result: a blocked call is information, not a crash.
type Decision struct {
	Allow  bool
	Reason string
}

type subagentArgs struct {
	Action  string `json:"action,omitempty" enum:"spawn,continue,list,archive"`
	AgentID string `json:"agent_id,omitempty"`
	Task    string `json:"task,omitempty" required:"true" description:"The complete task for the subagent, written as if to someone who cannot see this conversation"`
	// Explicit mode prevents an editing task from silently becoming read-only.
	Mode string `json:"mode,omitempty" required:"true" enum:"explore,edit" description:"explore for read-only work; edit for commands or changes, always synchronous"`
	// Only explore can opt into the session's background runner.
	Wait bool `json:"wait,omitempty" description:"For explore only: wait for the answer instead of running in the background. Edit always waits for completion regardless of this value."`
}

func (*Subagent) Name() string { return "subagent" }

// Description is prompt text, not documentation: it is loaded into the model's
// context verbatim, so it has to say when to delegate, what the subagent can and
// cannot see, and what comes back.
//
// The paragraph about writing the task is the one that earns its tokens. A
// subagent inherits nothing from this conversation — not the files already read,
// not the decisions already made — and the most common failure is a one-line task
// that was perfectly clear in context and meaningless out of it.
func (s *Subagent) Description() string {
	retainedPara := ""
	if s.Store != nil {
		retainedPara = "\n\nChild contexts are retained after execution; idle children consume no worker slot. " +
			"Use action=continue with the returned agent_id and a follow-up task to reuse that child's history. " +
			"Use action=list to find retained children, and action=archive to retire one while keeping its transcript. " +
			"Continuation preserves its model and permission mode. A running child cannot accept a second task; wait for its result. " +
			"Use a new spawn for unrelated work. Continue never replays unfinished tool calls automatically."
	}
	editPara := "mode \"edit\" also gets write, edit and shell commands in your current workspace. " +
		"Changes are immediately visible and returned in place. It always waits for completion, " +
		"even when wait is false. Writable delegations are serialized across sessions and processes " +
		"for this OS user on this host. This is not a filesystem sandbox: other main sessions or " +
		"external programs can still edit these files. In the task, name the files to change and " +
		"ask for full paths to any output."
	// The background paragraph only exists where a runner does: describing a
	// notification channel the session cannot deliver would teach the model to
	// wait for messages that never come.
	bgPara := ""
	if s.Bg != nil {
		bgPara = "\n\n" +
			"By default an explore delegation runs in the background: this tool returns immediately " +
			"with a task id, the subagent keeps working while you do other things, and its " +
			"result arrives on its own as a <task-notification> message. Never sleep, poll, " +
			"or re-issue the task to wait for it — the notification is the only wait that " +
			"works. Pass \"wait\": true only when you cannot take any useful next step " +
			"without the result. The subagent_tasks tool lists what is still outstanding."
	}
	// The skills paragraph only exists where skills do. It carries the two facts a
	// parent was observed getting wrong: that declared tools DO travel to the child
	// (so a task can name one), and that the read-only split is the registry's, not
	// the parent's guess — a read-only declared tool is usable in explore mode,
	// while anything that must run a command or write still needs edit.
	skillPara := ""
	if len(s.SkillPaths) > 0 {
		skillPara = "\n\n" +
			"Subagents inherit this session's skills and their declared tools. A tool " +
			"declared read-only is available to an explore subagent — delegating " +
			"\"query with mal_similar\" in explore mode works — while a declared tool " +
			"that writes or ingests, or anything that must run a command, needs edit " +
			"mode. When the task should use a specific declared tool, name it in the " +
			"task (the subagent knows its own tool list, but your task saying which to " +
			"prefer saves it a choosing turn and prevents a duplicated approach)."
	}
	return "Delegate a self-contained task to a subagent: a separate agent with its own " +
		"context window. Only its final answer comes back here, so use it when a task would " +
		"otherwise fill this conversation with output you will never refer to again. " +
		"Explore calls can run in parallel; edit calls run sequentially and wait for completion.\n\n" +
		"mode \"explore\" is read-only and runs in your directory, so it sees your " +
		"uncommitted files. Use it for questions — where something is implemented, how a " +
		"subsystem fits together, which callers depend on a function. Prefer it whenever " +
		"you only need to know something. It has no shell, though, and the dividing line " +
		"is commands, not intent: analysis described as read-only still needs edit (or " +
		"your own bash) the moment any part of the answer is produced by running a " +
		"command — even a read-only query CLI.\n\n" +
		editPara + "\n\n" +
		"Write the task as if to a competent colleague who cannot see this conversation. " +
		"A newly spawned subagent starts from an empty context: it does not know which files you have " +
		"read, what the user asked for, or what you have already ruled out. Include the " +
		"file paths, the exact error text, the constraints, and what a good answer looks " +
		"like. \"Fix the failing test\" will fail; \"TestFoo in pkg/foo/foo_test.go fails " +
		"with <error>; find the cause and fix it, then run go test ./pkg/foo/\" will not.\n\n" +
		"A subagent cannot run git and cannot delegate further." + skillPara + bgPara + retainedPara
}

// ExecutionMode is conservative for callers that do not inspect arguments.
func (*Subagent) ExecutionMode() ExecutionMode { return Sequential }

// ExecutionModeForArgs keeps read-only delegation parallel. Unknown or malformed
// requests must not create a write-capable gap in the agent's sequential barrier.
func (s *Subagent) ExecutionModeForArgs(raw json.RawMessage) ExecutionMode {
	// Ordinary argument validation tolerates optional nulls and unknown fields.
	// The scheduler needs a narrower positive proof before admitting overlap.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return Sequential
	}
	for name, value := range fields {
		known := name == "task" || name == "mode" || name == "wait" || (s.Store != nil && (name == "action" || name == "agent_id"))
		if !known || strings.TrimSpace(string(value)) == "null" {
			return Sequential
		}
	}
	var a subagentArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return Sequential
	}
	if s.Store != nil && a.Action == "list" && a.AgentID == "" && a.Task == "" {
		return Parallel
	}
	if s.Store != nil && a.Action == "continue" && strings.TrimSpace(a.Task) != "" {
		r, err := s.Store.read(a.AgentID)
		if err == nil && r.Mode == ModeExplore && (a.Mode == "" || a.Mode == ModeExplore) {
			return Parallel
		}
		return Sequential
	}
	if (a.Action == "" || a.Action == "spawn") && a.AgentID == "" && a.Mode == ModeExplore && strings.TrimSpace(a.Task) != "" {
		return Parallel
	}
	return Sequential
}

func (s *Subagent) InputSchema() map[string]any {
	mode := prop("string", "\"explore\" for read-only work: reading, searching, "+
		"explaining. \"edit\" when the subagent must change files or run commands.")
	mode["enum"] = []string{ModeExplore, ModeEdit}
	props := map[string]any{
		"task": prop("string", "The complete task for the subagent, written as if to "+
			"someone who cannot see this conversation: include file paths, exact error "+
			"text, constraints, and what a good answer looks like."),
		"mode": mode,
	}
	if s.Bg != nil {
		// Only offered where a runner exists to honour it: a terminal session has
		// no way to deliver a late result, so the question would be meaningless.
		props["wait"] = prop("boolean", "For explore only: wait for completion instead of running in the background. Edit always runs synchronously, regardless of this value.")
	}
	if s.Store != nil {
		action := prop("string", "spawn (default): create a child; continue: follow up with an idle child; list: inspect retained contexts; archive: retire an idle child, retaining history. task and mode are required for spawn; task and agent_id for continue; agent_id for archive.")
		action["enum"] = []string{"spawn", "continue", "list", "archive"}
		props["action"] = action
		props["agent_id"] = prop("string", "The stable agent_id returned by this parent's spawn or list. Never a file path or an ID from another conversation.")
		return object(nil, props)
	}
	return object([]string{"task", "mode"}, props)
}

func (s *Subagent) Schema() map[string]any {
	if s.Store != nil {
		return s.InputSchema()
	}
	return GenerateSchema(reflect.TypeOf(subagentArgs{}))
}

// SubagentDetails is the structured record of one delegated run. It is for
// interfaces and the transcript; the model reads Result.Text.
type SubagentDetails struct {
	AgentID string `json:"agent_id,omitempty"`
	RunID   string `json:"run_id,omitempty"`
	Status  string `json:"status,omitempty"`
	ID      string `json:"id"`
	Mode    string `json:"mode"`
	// Isolation records the current shared execution mode. Legacy fields below are
	// retained for reading historical transcripts, never populated by new runs.
	Isolation string `json:"isolation,omitempty"`
	// Workspace is where a writable child leaves its files, including partial work.
	Workspace string `json:"workspace,omitempty"`
	// Model is what the child actually ran, which is not always the parent's: an
	// explore child may be configured onto a different one. Recorded because "why
	// was that answer worse than I expected" is otherwise unanswerable.
	Model string `json:"model,omitempty"`
	// Ref and Commit are historical fields, never set by current execution.
	Ref    string `json:"ref,omitempty"`
	Commit string `json:"commit,omitempty"`
	// Session is the child's own transcript, so a person can read the work that
	// deliberately did not enter the parent's context.
	Session        string `json:"session,omitempty"`
	FullOutputPath string `json:"full_output_path,omitempty"`
	// Worktree is retained only for historical results.
	Worktree string `json:"worktree,omitempty"`
	Turns    int    `json:"turns"`
	InputTok int64  `json:"input_tokens"`
	OutTok   int64  `json:"output_tokens"`
	ExitCode int    `json:"exit_code"`
	// Tampered is retained only for historical results.
	Tampered bool `json:"tampered,omitempty"`
	// Background marks a delegation that was answered at dispatch time: the real
	// result arrived later, through the session's notification channel. Interfaces
	// use it to keep the card live after the call itself has settled.
	Background bool `json:"background,omitempty"`
}

func (s *Subagent) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	return s.ExecuteStreaming(ctx, raw, nil)
}

func (s *Subagent) ExecuteStreaming(ctx context.Context, raw json.RawMessage, onPartial func(Partial)) (Result, error) {
	if s.StoreError != nil {
		return Result{}, fmt.Errorf("retained subagent storage is unavailable: %w", s.StoreError)
	}
	var a subagentArgs
	if err := unmarshal(raw, &a); err != nil {
		return Result{}, err
	}
	if s.Store != nil {
		return s.executeRetained(ctx, a, onPartial)
	}
	if a.Action != "" && a.Action != "spawn" || a.AgentID != "" {
		return Result{}, errors.New("retained subagents are unavailable in this session")
	}
	if strings.TrimSpace(a.Task) == "" {
		return Result{}, errors.New("task must not be empty")
	}
	switch a.Mode {
	case ModeExplore:
	case ModeEdit:
	case "":
		return Result{}, fmt.Errorf("mode is required: %q to read, search and explain "+
			"without changing anything, %q to let the subagent edit files and run commands",
			ModeExplore, ModeEdit)
	default:
		return Result{}, fmt.Errorf("unknown mode %q: use %q or %q", a.Mode, ModeExplore, ModeEdit)
	}
	maxDepth := s.MaxDepth
	if maxDepth <= 0 {
		maxDepth = DefaultSubagentMaxDepth
	}
	if s.Depth >= maxDepth {
		// An error the model can act on, not a bare refusal: the useful next move
		// is to do the work itself.
		return Result{}, fmt.Errorf("subagents cannot be nested more than %d level(s) deep, "+
			"and this agent is already at depth %d: do this task yourself, or split it "+
			"differently", maxDepth, s.Depth)
	}
	exe := s.Exe
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return Result{}, fmt.Errorf("cannot locate the pogo binary to run a subagent: %w", err)
		}
	}

	timeout := s.Timeout
	if timeout <= 0 {
		timeout = DefaultSubagentTimeout
	}

	// The id is minted here rather than by each mode so that a backgrounded call
	// can name its task in the ack it returns immediately — the registry, the
	// approval gate and result record all share it from there on.
	id := newID()
	if a.Mode == ModeExplore && !a.Wait && s.Bg != nil {
		return s.Bg.dispatch(s, exe, id, a.Mode, a.Task, timeout, ctx)
	}

	// The per-session limit bounds context windows and reviewer attention in both modes.
	if err := s.acquire(ctx); err != nil {
		return Result{}, err
	}
	defer s.release()

	if a.Mode == ModeExplore {
		return s.explore(ctx, exe, id, a.Task, timeout, onPartial)
	}
	return s.edit(ctx, exe, id, a.Task, timeout, onPartial)
}

// explore reads the parent workspace without a writer lease.
func (s *Subagent) explore(ctx context.Context, exe, id, task string, timeout time.Duration,
	onPartial func(Partial)) (Result, error) {

	model, provider := s.pickDelegationModel(ModeExplore)
	defer s.releaseExplore(provider)
	return s.exploreWithModel(ctx, exe, id, task, timeout, onPartial, model)
}

func (s *Subagent) exploreWithModel(ctx context.Context, exe, id, task string, timeout time.Duration, onPartial func(Partial), model string) (Result, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	spec := childSpec{id: id, dir: s.Cwd, readOnly: true, model: model, modelPinned: true}
	run, _ := s.spawn(runCtx, exe, spec, s.exploreNote()+task, onPartial)
	d := SubagentDetails{
		ID: spec.id, Mode: ModeExplore, Model: spec.model, Session: run.session,
		Turns: run.turns, InputTok: run.input, OutTok: run.output, ExitCode: run.exit,
	}
	text, err := run.report(&d, timeout, s.SpillDir)
	return Result{Text: text, Details: d}, err
}

// edit holds a host-wide lease for the complete child lifetime. The caller's
// model loop waits, and other writable delegations cannot overlap this one.
func (s *Subagent) edit(ctx context.Context, exe, id, task string, timeout time.Duration,
	onPartial func(Partial)) (Result, error) {
	model, _ := s.pickDelegationModel(ModeEdit)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	lease, err := acquireEditLease(runCtx)
	if err != nil {
		return Result{}, fmt.Errorf("could not acquire the writable delegation lease: %w", err)
	}
	defer lease.Close()
	spec := childSpec{id: id, dir: s.Cwd, editLease: lease, model: model, modelPinned: true}
	run, _ := s.spawn(runCtx, exe, spec, sharedNote()+task, onPartial)
	d := SubagentDetails{
		ID: id, Mode: ModeEdit, Isolation: IsolationShared, Workspace: s.Cwd,
		Model: spec.model, Session: run.session,
		Turns: run.turns, InputTok: run.input, OutTok: run.output, ExitCode: run.exit,
	}
	text, err := run.report(&d, timeout, s.SpillDir)
	return Result{Text: text, Details: d}, err
}

// childRun is what one child process produced.
type childRun struct {
	session string
	// answer accumulates across the whole run and is what report() settles on:
	// a multi-turn child interleaves tool calls with text, and only the whole
	// of it is the answer. Never reset.
	answer strings.Builder
	// thinking and answerTail are display streams, reset per turn: the live
	// card shows "what is being said now", and turn N's text is stale news
	// under turn N+1's header.
	thinking   strings.Builder
	answerTail strings.Builder
	turns      int
	input      int64
	output     int64
	exit       int
	runErr     string // the child's own run_end error
	stderr     string
	spawnErr   error
	aborted    bool
	timedOut   bool
}

// exploreNote is the preamble prepended to a read-only child's task.
//
// Written after watching one fail. A live run gave an explore child a question it
// had effectively answered by its second turn; it then decided to confirm by running
// the tests — announcing that this was read-only, which it is not — and spent its
// three remaining turns hunting for a way to do it: grep for go.mod, list the
// directory, read go.mod. It hit its turn limit having thrown the answer away.
//
// The cause is that nothing told it what it was. It has no shell in its tool list,
// but an absent tool is a silence, and a model reads silence as "look harder". So
// the note says the capability is absent rather than merely unlisted, and — the part
// that actually changes behaviour — says what to do instead of searching for it.
//
// The declared-tools sentence appears only when there are declared read-only tools:
// in a plain coding session it would name nothing and cost a turn of reading. Named
// explicitly for the same reason the note exists at all — a parent that delegated
// "query the sample library with mal_similar" was observed sending the child a task
// it had no tool for, and the child burned its turns hunting. The names are the
// child's real tool list (the registry filtered it by readOnlyHint before spawn),
// so the note and the tool schema can never disagree.
func (s *Subagent) exploreNote() string {
	toolsPara := ""
	if names := s.ReadOnlySkillTools; len(names) > 0 {
		toolsPara = "You also hold this session's declared read-only tools: " +
			strings.Join(names, ", ") +
			". Prefer them for what they do — they need no shell and are the intended way " +
			"to answer questions in their domain.\n"
	}
	return "<your-role>\nYou are a read-only subagent. You can read, list, find and " +
		"grep, in the directory the parent agent is working in — so you see its files as " +
		"they are now, including uncommitted ones.\n" +
		toolsPara +
		"You have no shell and cannot edit anything: running tests, builds or any command " +
		"is not available to you, and there is no way around it worth looking for. If the " +
		"task cannot be finished without one, say exactly what you would have run and why, " +
		"and stop — the parent can run it. Answer from the source you can read.\n" +
		"</your-role>\n\n"
}

// sharedNote names the real output contract without promising version history.
func sharedNote() string {
	return "<your-directory>\nYou are working directly in the parent's workspace. " +
		"You see its current files, including uncommitted ones, and writes take effect immediately. " +
		"You have no git here: leave files in place and give the full path of each output.\n" +
		"Other subagents with write tools wait for this task to finish. This lease does not block " +
		"external editors or other main sessions. Touch only what your task names, and never " +
		"delete unrelated files.\n</your-directory>\n\n"
}

// childSpec holds the per-run process parameters.
type childSpec struct {
	id           string
	dir          string
	readOnly     bool
	model        string
	modelPinned  bool
	resume       string
	contextLease *os.File
	// Inherited after the approval/browser descriptors. If the parent crashes,
	// the child keeps the writer lease until it exits.
	editLease *os.File
}

// modelOr resolves which model this child actually runs.
func (c childSpec) modelOr(inherited string) string {
	if c.model != "" {
		return c.model
	}
	return inherited
}

// spawn runs the child and reads its event stream.
//
// The child speaks the same JSONL contract as `pogo -mode json`, which is the
// whole reason Phase 0 came first: there is no private parent-child format to
// maintain, and anything a script could read from a subagent, the parent reads
// the same way.
func (s *Subagent) spawn(ctx context.Context, exe string, spec childSpec,
	task string, onPartial func(Partial)) (*childRun, error) {

	args := []string{"-mode", "json", "-C", spec.dir, "-p", task}
	if spec.resume != "" {
		args = append(args, "-resume", spec.resume, "-compact", "auto")
	}
	if s.ExplicitSkills {
		args = append(args, "-no-skills")
	}
	// The session's skills travel with the child (see SkillPaths): the child is
	// a full pogo, so -skill gives it the same prompt section and the same
	// declared tools, and the registry's readOnlyHint filter does the explore
	// split on the child's side, exactly as it would for this process.
	for _, p := range s.SkillPaths {
		if p = strings.TrimSpace(p); p != "" {
			args = append(args, "-skill", p)
		}
	}
	m := spec.model
	if m == "" && !spec.modelPinned {
		m = s.parentModel()
	}
	if m != "" {
		args = append(args, "-model", m)
	}
	if s.MaxTurns > 0 {
		args = append(args, "-max-turns", strconv.Itoa(s.MaxTurns))
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = spec.dir
	// Passed as environment, not flags: see the note on EnvSubagentDepth. The
	// read-only marker belongs in the same class — a child that could talk its way
	// out of being read-only would make the mode decorative.
	cmd.Env = append(os.Environ(),
		EnvSubagentDepth+"="+strconv.Itoa(s.Depth+1),
	)
	cmd.Env = informationChildEnv(cmd.Env, s.InformationRoot, s.InformationScope)
	cmd.Env = nativeSelectionChildEnv(cmd.Env, s.Enabled)
	cmd.Env = hostPrivateChildEnv(cmd.Env)
	// Strip unrelated process grants even when this parent has no gate. Only
	// wireGate may install this child's authenticated, live capability.
	cmd.Env = fileAccessChildEnv(cmd.Env, "", false)
	if s.CommandProtectionManifest != "" {
		env := cmd.Env[:0]
		for _, entry := range cmd.Env {
			if !strings.HasPrefix(entry, EnvCommandProtectionManifest+"=") {
				env = append(env, entry)
			}
		}
		cmd.Env = append(env, EnvCommandProtectionManifest+"="+s.CommandProtectionManifest)
	}
	if s.ExplicitSkills {
		// Do not inherit other sessions' globally installed skill roots. Keep
		// this command-local: changing the process environment races sessions.
		env := cmd.Env[:0]
		for _, entry := range cmd.Env {
			key, _, _ := strings.Cut(entry, "=")
			if !strings.EqualFold(key, EnvReadRoots) {
				env = append(env, entry)
			}
		}
		cmd.Env = append(env, EnvReadRoots+"="+strings.Join(s.SkillReadRoots, string(os.PathListSeparator)))
	}
	if spec.readOnly {
		cmd.Env = append(cmd.Env, EnvSubagentReadOnly+"=1")
	}
	closeGate, err := s.wireGate(cmd, spec.id)
	if err != nil {
		return &childRun{spawnErr: err}, err
	}
	defer closeGate()
	// After the gate, so the descriptor numbering is stable and documented in one
	// place. Both cleanups run after cmd.Wait below, which is what ties the
	// browser lease to the child's actual lifetime.
	closeBrowser, err := s.wireBrowser(cmd, spec)
	if err != nil {
		return &childRun{spawnErr: err}, err
	}
	defer closeBrowser()
	if spec.editLease != nil {
		cmd.Env = append(cmd.Env, EnvSubagentEditLeaseFD+"="+strconv.Itoa(3+len(cmd.ExtraFiles)))
		cmd.ExtraFiles = append(cmd.ExtraFiles, spec.editLease)
	}
	if spec.contextLease != nil {
		cmd.Env = append(cmd.Env, envSubagentContextLeaseFD+"="+strconv.Itoa(3+len(cmd.ExtraFiles)))
		cmd.ExtraFiles = append(cmd.ExtraFiles, spec.contextLease)
	}
	closeObservation, err := mcpobserve.AttachSubagentMetadata(cmd, ctx, spec.id)
	if err != nil {
		return &childRun{spawnErr: err}, err
	}
	defer closeObservation()
	// Give a real Pogo child time to cancel its tools, which own separate process
	// groups. An unresponsive child is force-killed after a bounded grace period.
	setProcessGroup(cmd)
	finishCancellation := configureSubagentCancellation(cmd)
	defer finishCancellation()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return &childRun{spawnErr: err}, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	run := &childRun{}
	if err := cmd.Start(); err != nil {
		run.spawnErr = err
		return run, err
	}
	run.read(stdout, onPartial)
	waitErr := cmd.Wait()
	finishCancellation()
	run.stderr = strings.TrimSpace(stderr.String())
	run.exit = cmd.ProcessState.ExitCode()
	run.aborted = ctx.Err() != nil
	run.timedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
	return run, waitErr
}

// read consumes the child's JSONL stream.
//
// Unparseable lines are ignored rather than fatal. The stream is a contract, but a
// child that dies mid-write leaves a half line, and losing a turn because of the
// last 30 bytes of a crash would be its own bug.
func (r *childRun) read(stdout io.Reader, onPartial func(Partial)) {
	sc := bufio.NewScanner(stdout)
	// Tool output can be long; the default 64KB line limit is not enough.
	sc.Buffer(make([]byte, 0, 64*1024), MaxBytes+64*1024)
	for sc.Scan() {
		var e struct {
			Type    string `json:"type"`
			Session string `json:"session"`
			Turn    int    `json:"turn"`
			Name    string `json:"name"`
			Text    string `json:"text"`
			IsError bool   `json:"is_error"`
			Error   string `json:"error"`
			Usage   *struct {
				Input  int64 `json:"input"`
				Output int64 `json:"output"`
			} `json:"usage"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		// Forwarded whole, before being interpreted. The child speaks the same event
		// contract the parent's own consumers speak, so an interface that can render a
		// run can render a delegated one with the same code — and the alternative,
		// flattening it to a line of text, throws away everything but the tool name.
		//
		// Copied because the scanner reuses its buffer, and the frame outlives this
		// iteration.
		if frameWorthForwarding(e.Type) {
			emitFrame(onPartial, append(json.RawMessage(nil), sc.Bytes()...))
		}
		switch e.Type {
		case "session":
			r.session = e.Session
		case "turn_start":
			r.turns = e.Turn
			// A new turn retires the previous one's display streams; the
			// settled answer accumulator is untouched — see the struct.
			r.answerTail.Reset()
			r.thinking.Reset()
		case "thinking":
			// The child's thinking, forwarded as a coalesced tail frame — the
			// same live display the parent's own run gets, one level down. See
			// emitTailFrame for why the frame carries the accumulated tail
			// rather than the delta.
			r.thinking.WriteString(e.Text)
			r.emitTailFrame(onPartial, "thinking", e.Turn, r.thinking.String())
		case "token":
			// The answer is assembled from the deltas, which is also what the
			// parent shows live: the point of a subagent is that only this part
			// comes back.
			r.answer.WriteString(e.Text)
			r.answerTail.WriteString(e.Text)
			r.emitTailFrame(onPartial, "token", e.Turn, r.answerTail.String())
		case "tool_start":
			// One line per tool call, not the arguments: the parent is watching
			// progress, and the details are in the child's own transcript.
			emit(onPartial, "· "+e.Name+"\n")
		case "run_end":
			r.runErr = e.Error
			if e.Usage != nil {
				r.input, r.output = e.Usage.Input, e.Usage.Output
			}
		}
	}
}

// tailFrameBytes bounds the text a thinking or token frame carries. The live
// display is a rolling tail — no card shows more than the last screen of a
// stream — so the frame repeats at most this much, keeping both the SSE fan-out
// and the per-frame re-serialisation linear in what anybody can read.
const tailFrameBytes = 2048

// emitTailFrame forwards one coalesced progress frame for a delta stream.
//
// Each delta emits a frame carrying the accumulated text's tail, not the delta:
// a consumer that joins mid-stream (or reconnects off the ring, which keeps
// only the newest frame of each kind) must be able to render from the last
// frame alone, which a delta cannot support. The volume this implies — a frame
// per delta — is bounded twice: by tailFrameBytes here, and by the frame rings
// replacing a trailing frame of the same kind instead of appending (see
// appendLiveFrame in web/hub.go), so the ring holds one thinking slot and one
// token slot per run no matter how long it thinks.
func (r *childRun) emitTailFrame(onPartial func(Partial), kind string, turn int, text string) {
	if onPartial == nil || text == "" {
		return
	}
	if len(text) > tailFrameBytes {
		cut := len(text) - tailFrameBytes
		// Land on a rune boundary: slicing mid-rune renders as replacement
		// characters at exactly the spot a rolling display stares at.
		for cut < len(text) && !utf8.RuneStart(text[cut]) {
			cut++
		}
		text = text[cut:]
	}
	frame, err := json.Marshal(struct {
		Type string `json:"type"`
		Turn int    `json:"turn"`
		Text string `json:"text"`
	}{kind, turn, text})
	if err != nil {
		return
	}
	emitFrame(onPartial, frame)
}

func emit(onPartial func(Partial), s string) {
	if onPartial != nil {
		onPartial(Partial{Text: s})
	}
}

func emitFrame(onPartial func(Partial), frame json.RawMessage) {
	if onPartial != nil {
		onPartial(Partial{Frame: frame})
	}
}

// frameWorthForwarding filters the child's stream down to what an interface shows.
//
// An allowlist, not a denylist, and measurement is why. The first version excluded
// only the per-token deltas; a live run then spent 40% of its frames on `thinking` —
// another delta stream, arriving twenty times across four turns to say something no
// interface displays. A denylist has to be revisited every time the event contract
// grows, and the failure mode when it is not is a silent flood.
//
// What is left is the shape of a delegated run: where its transcript is, its turn
// boundaries, its tool calls and how each went, and how it ended. The answer is not
// here because it arrives whole with the result.
func frameWorthForwarding(kind string) bool {
	switch kind {
	case "session", "turn_start", "tool_start", "tool_end", "run_end":
		return true
	}
	return false
}

// report turns a finished run into what the model reads.
//
// Failures are described in terms of what to do next, because the model is the
// one who has to react: a bare "exit status 1" gives it nothing to work with,
// while "the subagent produced no answer, its stderr says X" lets it retry with a
// better task or do the work itself.
func (r *childRun) report(d *SubagentDetails, timeout time.Duration, spillDir string) (string, error) {
	answer := strings.TrimSpace(r.answer.String())
	if tr := TruncateTail(answer); tr.Truncated {
		// Spilled rather than only pointed at the transcript, because the
		// transcript is the wrong file for this job twice over: it is JSONL that
		// has to be parsed to find the answer inside it, and it lives under the
		// session directory, which is agent state — a deployment that sets
		// POGO_READ_ROOTS does not put it in the readable roots, so the pointer
		// resolves to a refusal. The spill directory is a readable root by
		// construction (see Options.SpillDir), and holds the answer as text.
		//
		// The wording stays subagent-specific instead of borrowing KeepTail's
		// generic note: "the subagent's answer was truncated" says which of the
		// two nested runs lost output, which "Showing lines 40-80 of 300" does
		// not. The transcript is still on the details for the interface.
		where := "its full transcript is at " + d.Session
		if path, ok := Spill(spillDir, "subagent", answer); ok {
			d.FullOutputPath = path
			where = "the whole answer is at " + path +
				" — read it with offset/limit, or grep it for what you need"
		}
		answer = tr.Content + fmt.Sprintf(
			"\n\n[the subagent's answer was truncated to the last %d lines; %s]",
			tr.OutputLines, where)
	}

	switch {
	case r.spawnErr != nil:
		return "", fmt.Errorf("could not start the subagent: %w", r.spawnErr)
	case r.timedOut:
		return "", fmt.Errorf("the subagent did not finish within %s and was stopped. "+
			"Partial answer: %s%s", timeout, quoteOrNone(answer), partial(d))
	case r.aborted:
		return "", fmt.Errorf("the subagent was aborted%s", partial(d))
	case r.runErr != "":
		return "", fmt.Errorf("the subagent stopped with an error: %s. Partial answer: %s%s",
			r.runErr, quoteOrNone(answer), partial(d))
	case r.exit != 0:
		return "", fmt.Errorf("the subagent exited with code %d: %s. Partial answer: %s%s",
			r.exit, firstLine(r.stderr), quoteOrNone(answer), partial(d))
	case answer == "":
		// stderr is included because this is the case where it is the only clue: the
		// child ran, said nothing, and exited zero. Without it the message says
		// "nothing happened" and stops there.
		why := ""
		if r.stderr != "" {
			why = " It wrote this to stderr: " + firstLine(r.stderr) + "."
		}
		return "", fmt.Errorf("the subagent finished without producing an answer after %d turn(s).%s "+
			"Its transcript is at %s. Re-issue the task with the context it was missing",
			d.Turns, why, d.Session)
	}

	if d.Mode == ModeEdit {
		answer += fmt.Sprintf("\n\n[the subagent worked in %s; its output files remain in that workspace]", d.Workspace)
	}
	return answer, nil
}

func partial(d *SubagentDetails) string {
	if d.Workspace == "" {
		return ""
	}
	return fmt.Sprintf(". Partial files remain in %s; inspect them before retrying", d.Workspace)
}

func quoteOrNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return strconv.Quote(s)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	if s == "" {
		return "(no output)"
	}
	return s
}

// acquire waits for a subagent slot.
func (s *Subagent) acquire(ctx context.Context) error {
	s.semOnce.Do(func() {
		n := s.Concurrency
		if n <= 0 {
			n = DefaultSubagentConcurrency
		}
		s.sem = make(chan struct{}, n)
	})
	select {
	case s.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("gave up waiting for a subagent slot: %w", ctx.Err())
	}
}

func (s *Subagent) release() { <-s.sem }

// newID names a delegated task and its transcript events. Random rather than sequential
// because two parents in the same repository must not pick the same name, and
// there is no shared counter to ask.
func newID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "sub" + hex.EncodeToString(b[:])
}
