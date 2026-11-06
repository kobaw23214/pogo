// Package agent contains the loop. It is the whole program in one function:
// call the model, run whatever tools it asked for, feed the results back, repeat
// until the model stops asking for tools.
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/tools"
)

// DefaultMaxTurns bounds a single Run. A model stuck in a tool-calling cycle
// would otherwise spend the whole budget unattended.
const DefaultMaxTurns = 300

// DefaultSoftTurns is the CLI's checkpoint interval (see softcap.go). It is a
// flag default only: New does not apply it, so every embedding that constructs
// Config directly — web, tests — keeps the zero value, which is off.
const DefaultSoftTurns = 10

// DefaultToolConcurrency caps how many calls in one batch run at once.
//
// A batch is rarely larger than ten, so eight is not a bottleneck in practice.
// It exists as a ceiling on file descriptors and buffered output: without it, a
// turn that asks for fifty reads would open fifty files and hold fifty truncated
// buffers at once.
const DefaultToolConcurrency = 8

type Agent struct {
	// client is an interface so switching models mid-session is one assignment
	// and the conversation carries over untouched.
	client       llm.Client
	registry     *tools.Registry
	schemas      []llm.ToolSchema
	systemPrompt string
	// runtimePrompt reads trusted, live session facts for each model request.
	runtimePrompt         func() string
	runtimePromptSnapshot string
	// systemPromptParts labels systemPrompt for the overhead breakdown. Always
	// a validated description of systemPrompt (see labeledSystemPrompt), set
	// together with it.
	systemPromptParts []PromptPart
	// maxTurns is atomic because SetMaxTurns may be called by the web
	// settings path while a Run is between turns. The run loop reads it
	// per turn, never caches it across one.
	maxTurns atomic.Int64
	// An explicit session/task limit survives changes to the ordinary default.
	// Zero inherits maxTurns, -1 is unlimited, and a positive value is finite.
	turnLimitOverride atomic.Int64
	// softTurns is the checkpoint interval, zero disables it. See softcap.go.
	softTurns int
	// continuation is opt-in and bounded independently of the model's own
	// per-response output limit. Its attempt counter belongs to one run.
	continuation ContinuationConfig

	// gate is optional. Nil means every call runs, which is what the CLI wants:
	// -p has no human to ask and must stay scriptable.
	gate ToolGate
	// toolExecution can force a whole session sequential regardless of what the
	// individual tools declare.
	toolExecution         tools.ExecutionMode
	toolConcurrency       int
	toolOutputBudget      ToolOutputBudgetConfig
	toolResultViews       map[toolResultPosition]cachedToolResultView
	resultLeaseRefresh    time.Time
	lastToolBudgetPlan    toolResultBudgetPlan
	lastToolBudgetMessage int
	lastPromptEstimate    int64
	toolReadRecovery      toolReadRecoveryState
	// reviewBudget bounds how long one batch may spend waiting for approvals.
	// Zero means unbounded, which is right when there is no gate.
	reviewBudget time.Duration

	// stagnationThreshold is how many identical tool results in a row count as stagnation.
	// Zero means disabled.
	stagnationThreshold int

	// budgets are run-level limits. Zero means unbounded.
	tokenBudget         int64
	costBudget          float64
	timeBudget          time.Duration
	answerReviewTimeout time.Duration
	// price is the rate costBudget is measured against, or nil when none was
	// declared. Moves with the model on a switch: see SetPrice.
	price *llm.Price

	// runStartTime tracks when the current run started, for time-budget enforcement.
	runStartTime time.Time

	// messages is the conversation so far, kept across Run calls so a REPL gets
	// multi-turn behaviour for free.
	messages []llm.Message
	usage    llm.Usage
	// delegated is the part of usage that was spent inside subagents. A subset of
	// usage, not a sibling of it: see the rollup in Run.
	delegated llm.Usage
	timing    timingAccum

	// Fixed cost tracking for overhead metrics (RFC B1).
	fixedCostTokens    int64
	overheadMu         sync.RWMutex // guards concurrent OverheadTokens readers
	systemPromptTokens int64
	toolSchemaTokens   int64
	// overheadParts itemizes fixedCostTokens (overhead.go). Written with it,
	// under overheadMu, whenever the view is installed.
	overheadParts []OverheadPart

	// Layered MCP discovery state: see mcpview.go. mcpDeferred marks that the
	// model-visible tool view hides most MCP declarations behind mcp_describe;
	// mcpSelected is the bounded recent-selection set (oldest first);
	// mcpViewKey identifies the last built view so accounting is invalidated
	// exactly when the wire definitions change; pendingSelect collects the
	// current batch's describe-detail activations, applied on the loop
	// goroutine only after the whole batch settles.
	mcpDeferred   bool
	mcpSelected   []mcpSelection
	mcpViewKey    string
	pendingSelect []pendingSelection
	selectMu      sync.Mutex
	// skillWithheld lists the Skill-bound groups the current view withholds
	// until the conversation loads their Skill; skillDeferrable is every group
	// that could be withheld here, loaded or not: see skillview.go.
	skillWithheld   []tools.DeferredSkillBinding
	skillDeferrable []tools.DeferredSkillBinding

	// lastInput is the provider's own token count for the most recent prompt. It is
	// the only authoritative measure of how full the window is: usage.Input is a
	// running total and grows quadratically with turn count, so it reads far past
	// the window on a long session.
	//
	// Written by the run goroutine beside the usage counters and read under the same
	// rule as Usage — after the run, on the goroutine that owns it.
	lastInput int64
	// measuredThrough is how many messages were in the prompt lastInput measured.
	// Together the two give promptTokens a measured baseline plus an estimated
	// increment, instead of an estimate of the whole history.
	measuredThrough int

	// contextEdit is the clearing policy; a zero Trigger disables it.
	contextEdit ContextEditConfig

	// compactCfg is the auto-compaction policy; a zero Threshold leaves
	// compaction manual. The loop itself never acts on it — the trigger is the
	// driver's, between runs (see autocompact.go).
	compactCfg CompactConfig

	// imageResolver comes from Config.ImageResolver; see there.
	imageResolver func(ref string) (dataURL string, ok bool)
	// cleared is the set of tool-result call ids already blanked in the prompt.
	// Carried between turns so a cleared result stays cleared — see editContext on
	// why anything else flaps the prompt prefix. Single-writer, like the counters.
	cleared map[string]bool
	// clearedTokens is how much the most recent pass took out of the prompt. Kept
	// so a composition snapshot can say what the difference between the history and
	// the prompt was: without it the estimate covers text the provider never
	// counted, and the ratio between them stops being a tokenizer ratio.
	clearedTokens int64

	// todoReminded is whether this run has already spent its one task-list
	// closing reminder (see todofinish.go). Reset at the top of every run, and
	// written only by the run goroutine, like the counters above.
	todoReminded bool
	// officeFinish tracks only this run's confirmed live Office changes, using
	// typed host receipts rather than document text or human-only dirty state.
	officeFinish *officeFinishState

	// sources is the citation registry, nil when Config.Citations is off — the
	// zero value costs nothing: no stamp, no header, no validation. Next is
	// called from tool goroutines; the store owns its own lock.
	sources *SourceStore
	// citeRetried is whether this run has already spent its one citation retry
	// (see citecheck.go). Reset at the top of every run, like todoReminded.
	citeRetried bool

	// mu guards steering only: pending and running, nothing else.
	//
	// It deliberately does not guard messages. Steer is called from another
	// goroutine, but it only appends to pending; the run goroutine moves those
	// into messages at a turn boundary, so messages keeps its single writer and
	// the "do not read Messages() mid-run" rule stays true.
	mu      sync.Mutex
	pending []llm.Message
	running bool
}

type Config struct {
	Client       llm.Client
	Registry     *tools.Registry
	SystemPrompt string
	// RuntimePrompt supplements the configured prompt without rebuilding the
	// agent or freezing mutable access policy into conversation history.
	RuntimePrompt func() string
	// SystemPromptParts optionally labels SystemPrompt for the itemized
	// overhead breakdown. Ignored unless the parts concatenate to exactly
	// SystemPrompt; see SystemPromptParts.
	SystemPromptParts []PromptPart
	MaxTurns          int
	// SoftTurns turns the run up to MaxTurns into a series of checkpoints: every
	// this many turns the model is told where it stands and either finishes or
	// says what is left (see softcap.go). Zero disables it, and New applies no
	// default — the CLI owns that (DefaultSoftTurns), so embeddings constructing
	// Config directly, web among them, see no behaviour change.
	SoftTurns int
	// Continuation permits at most one completion after max_tokens, within this
	// run's existing turn, token, cost and time limits. Zero disables it.
	Continuation ContinuationConfig
	// Gate is optional; nil disables review entirely.
	Gate ToolGate
	// ToolExecution forces sequential execution for the whole session when set to
	// tools.Sequential. The default respects each tool's own declaration.
	ToolExecution tools.ExecutionMode
	// ToolConcurrency bounds how many calls in one batch run at once.
	ToolConcurrency int
	// ToolOutputBudget bounds model-visible tool text without rewriting UI or
	// transcript output. A zero config uses bounded defaults for unknown models.
	ToolOutputBudget ToolOutputBudgetConfig
	// ReviewBudget bounds the time one batch may spend waiting for approvals
	// before refusing the rest of its calls unreviewed. Zero means unbounded.
	// Only meaningful alongside a Gate; see reviewBudgetSpent.
	ReviewBudget time.Duration

	// StagnationThreshold is how many identical tool results in a row count as stagnation.
	// Default is 3, meaning three identical results trigger a stop.
	StagnationThreshold int

	// TokenBudget is the maximum total tokens (input+output) allowed for this run.
	// Zero means unbounded. Reaching this limit is treated as failure.
	//
	// Input and Output only: CacheRead is a subset of Input and Reasoning is a subset
	// of Output, so neither is added. See checkBudgets.
	TokenBudget int64

	// CostBudget is the maximum estimated cost allowed for this run, in the same
	// unit Price is declared in. Zero means unbounded. Reaching this limit is
	// treated as failure.
	//
	// Needs Price to do anything. A caller setting this without one gets no ceiling,
	// which is why the CLI refuses that combination up front rather than leaving the
	// agent to ignore it.
	CostBudget float64

	// Price is the per-million-token rate CostBudget is measured against, or nil
	// when the model has no declared price. pogo ships no built-in prices; see
	// llm.Price for why.
	Price *llm.Price

	// TimeBudget is the maximum wall-clock time allowed for this run.
	// Zero means unbounded. Reaching this limit is treated as failure.
	TimeBudget time.Duration

	// ContextEdit clears the payload of old tool results from the prompt once it
	// grows past ContextEdit.Trigger. A zero Trigger — the zero value — disables it,
	// so every existing caller keeps sending byte-identical prompts until it opts in.
	ContextEdit ContextEditConfig

	// Compact arms auto-compaction at the driver's between-runs check; a zero
	// Threshold — the zero value — keeps compaction manual, so every existing
	// caller sees no behaviour change. The loop itself never reads it.
	Compact CompactConfig

	// ImageResolver turns an image reference into a data URL at request time.
	// Nil means image blocks ride the prompt unresolved — which is also the
	// correct signal, because convert.go then substitutes each block's
	// description text for the pixels. The resolver is asked per model call, so
	// a session that switches to a vision-capable model mid-conversation starts
	// sending pixels without any history rewrite.
	ImageResolver func(ref string) (dataURL string, ok bool)

	// Citations turns on source citation: every finished tool call is stamped
	// with a source label, the tool_result text entering the conversation gets a
	// [cite:...] header naming it, and a final answer citing a label the session
	// never produced, or omitting all citations after task evidence reads, gets
	// one text-only rewrite (source.go, citecheck.go). The prompt
	// contract is the caller's half of the deal — append CitationSection to the
	// system prompt when this is on. The zero value is off, so every existing
	// caller sees byte-identical prompts until it opts in.
	Citations bool
	// Optional citation/quotation review must not hold a complete answer for
	// the normal provider timeout. Zero uses DefaultAnswerReviewTimeout.
	AnswerReviewTimeout time.Duration

	// Cwd is the session's working directory, used only to shorten source titles
	// and locators to relative paths. Empty leaves paths exactly as the tool
	// reported them.
	Cwd string
}

func New(c Config) *Agent {
	if c.AnswerReviewTimeout <= 0 {
		c.AnswerReviewTimeout = DefaultAnswerReviewTimeout
	}
	if c.MaxTurns <= 0 {
		c.MaxTurns = DefaultMaxTurns
	}
	if c.ToolConcurrency <= 0 {
		c.ToolConcurrency = DefaultToolConcurrency
	}
	schemas := make([]llm.ToolSchema, 0, len(c.Registry.All()))
	for _, t := range c.Registry.All() {
		schemas = append(schemas, llm.ToolSchema{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}
	agent := &Agent{
		client:                c.Client,
		registry:              c.Registry,
		schemas:               schemas,
		systemPrompt:          c.SystemPrompt,
		runtimePrompt:         c.RuntimePrompt,
		systemPromptParts:     labeledSystemPrompt(c.SystemPrompt, c.SystemPromptParts),
		softTurns:             c.SoftTurns,
		continuation:          c.Continuation.normalized(),
		gate:                  c.Gate,
		toolExecution:         c.ToolExecution,
		toolConcurrency:       c.ToolConcurrency,
		toolOutputBudget:      c.ToolOutputBudget,
		lastToolBudgetPlan:    toolResultBudgetPlan{Headroom: -1},
		lastToolBudgetMessage: -1,
		reviewBudget:          c.ReviewBudget,
		stagnationThreshold:   c.StagnationThreshold,
		tokenBudget:           c.TokenBudget,
		costBudget:            c.CostBudget,
		price:                 c.Price,
		timeBudget:            c.TimeBudget,
		answerReviewTimeout:   c.AnswerReviewTimeout,
		contextEdit:           c.ContextEdit,
		compactCfg:            c.Compact,
		imageResolver:         c.ImageResolver,
	}
	if c.RuntimePrompt != nil {
		agent.runtimePromptSnapshot = strings.TrimSpace(c.RuntimePrompt())
	}
	agent.maxTurns.Store(int64(c.MaxTurns))
	if c.Citations {
		agent.sources = newSourceStore(c.Cwd)
	}

	// The fixed per-request cost: the system prompt and the tool declarations, both
	// resent on every turn. Divided by llm.BytesPerToken rather than a literal 4, so
	// that this number stays in the same unit as everything else derived from it —
	// session.Composition sums Fixed with byte counts that do use the constant, and a
	// divisor that differed between them would make Estimated a mixed-unit total and
	// silently wrong every percentage in the analyze report.
	agent.systemPromptTokens = int64(len(agent.modelSystemPrompt()) / llm.BytesPerToken)
	agent.toolSchemaTokens = int64(toolSchemaBytes(schemas) / llm.BytesPerToken)
	agent.fixedCostTokens = agent.systemPromptTokens + agent.toolSchemaTokens

	// Layered discovery decides what the model actually sees: below the
	// thresholds this is a no-op over the eager list built above; past them the
	// MCP section defers to mcp_describe. See mcpview.go.
	agent.rebuildMCPView()

	return agent
}

// SetMaxTurns replaces the turn cap on a live agent. It exists for the web
// settings path: the Manager calls it on cached sessions when the preferences
// file changes, so a run that starts after the save — including one in an
// already-open conversation — uses the new cap without a reload. A run that is
// already between turns picks the new value up at its next boundary unless
// its host selected an explicit session override. A finished run is unaffected.
func (a *Agent) SetMaxTurns(n int) {
	if n <= 0 {
		return
	}
	a.maxTurns.Store(int64(n))
}

// SetUnlimitedTurns explicitly removes only the hard turn cap. New and
// SetMaxTurns retain their existing defaults; cancellation and other budgets
// still apply. A negative internal sentinel cannot be selected by MaxTurns=0.
func (a *Agent) SetUnlimitedTurns() { a.SetTurnLimitOverride(-1) }

// SetTurnLimitOverride pins an explicit session limit independently of default
// preference updates. Zero clears it, -1 removes the hard cap, and positive
// values set a finite cap. Other negatives are unsupported and ignored.
func (a *Agent) SetTurnLimitOverride(n int) {
	if n < -1 {
		return
	}
	a.turnLimitOverride.Store(int64(n))
}

func (a *Agent) effectiveTurnLimit() int64 {
	if limit := a.turnLimitOverride.Load(); limit != 0 {
		return limit
	}
	return a.maxTurns.Load()
}

func (a *Agent) hasTurnCapacity(turn int) bool {
	limit := a.effectiveTurnLimit()
	return limit < 0 || int64(turn) <= limit
}

// toolSchemaBytes is what the tool declarations weigh on the wire.
//
// It replaced a hand-written estimator that padded every schema key by ten bytes and
// every nested object by forty, which on JSON Schema — a shape where each property is
// its own object — read high by a wide margin. The browser's overhead figure was that
// number, so it overstated the fixed cost and, because ContextMeter derives the
// conversation's share by subtracting overhead from the provider's measured total,
// understated everything else by the same amount.
//
// Measuring instead of estimating is possible here only because llm.ToolsWireJSON
// exists. The alternative was a second renderer of the same bytes living in this
// package — the arrangement cmd/measure-schema was already carrying, and the one
// ToolsWireJSON was added to remove.
func toolSchemaBytes(schemas []llm.ToolSchema) int {
	wire, err := llm.ToolsWireJSON(schemas)
	if err == nil {
		return len(wire)
	}
	// Not reachable through any registry this repository builds, and deliberately not
	// worth a second estimator: a schema that will not marshal here will not marshal
	// in the request either, so the first turn fails with the same error and there is
	// no session left to report a size for. The prose length keeps the answer a lower
	// bound rather than a zero, because zero would read as "these tools are free".
	n := 0
	for _, s := range schemas {
		n += len(s.Name) + len(s.Description)
	}
	return n
}

func (a *Agent) Messages() []llm.Message { return a.messages }
func (a *Agent) Model() string           { return a.client.Model() }

// Usage is the accumulated token total across every turn so far.
//
// Only safe to call while no run is in flight: the loop adds to these counters
// from the run goroutine without a lock, and mu deliberately guards steering
// alone. A concurrent caller needs to take that on properly rather than assume
// this is safe — see the note in web/runner.go about the accessor that did.
func (a *Agent) Usage() llm.Usage { return a.usage }

// Delegated is the part of Usage that subagents spent. Same concurrency caveat.
//
// Reported separately because the two numbers are fixed by different decisions: a
// large Usage with a small Delegated means the conversation itself is expensive, and
// the answer is compaction or a cheaper model; the other way round means delegation
// is, and the answer is fewer or narrower tasks. A single total cannot tell them
// apart, and "3–10× the tokens" is the usual reason to look.
func (a *Agent) Delegated() llm.Usage { return a.delegated }

// Timing is the latency summary across every model call so far. Same concurrency
// caveat as Usage.
func (a *Agent) Timing() Timing { return a.timing.summary() }

// promptTokens is how large the prompt about to be sent is: the provider's own
// count for the last one, plus an estimate of everything appended since.
//
// Neither half alone is usable, and the reason is the failure this whole mechanism
// exists to prevent. The measured count is authoritative but always one turn stale,
// and the turn it is stale by is exactly the dangerous one — a batch of tool results
// can add a hundred thousand tokens between two measurements, so a trigger reading
// only the last measurement would sail past the window on the very turn that filled
// it. Estimating the whole history instead would drop the one number that is not a
// guess: the measured total already accounts for the system prompt, the tool
// schemas and the wire envelope, none of which llm.EstimateTokens models.
//
// So: measured baseline, estimated increment. The same split Codex's
// body_after_prefix scope makes, and for the same reason.
//
// The direction of the estimate's error was wrong here for a while, and it matters
// because the trigger is chosen from it. It reads *high*, not low: measured against
// the providers' own counts across 25 real sessions the ratio is 0.98 for ASCII and
// 0.83 for Chinese-heavy text, because both tokenizers were trained on Chinese and a
// two-character word costing six bytes is often one token. So four bytes per token is
// the conservative side on these providers, not the dangerous one — but it is still
// part estimate, which is why the trigger keeps a fifth of the window in hand rather
// than sitting on the ceiling. See ParseContextEdit.
func (a *Agent) promptTokens() int64 {
	if a.contextEdit.Trigger <= 0 && a.compactCfg.Threshold <= 0 && a.toolOutputBudget.Disabled {
		return 0
	}
	// lastInput measured the actual model view, never the UI transcript. Compare
	// like with like when adding new observations or tightening cached views.
	view := a.cachedToolPrompt(a.messages)
	view, _ = editContext(view, a.activeContextEditConfig(), a.cleared, 0)
	estimate := toolBudgetAdd(max(int64(0), llm.EstimateTokensFor(a.client, view)), max(int64(0), a.fixedCostTokens))
	if a.lastInput > 0 && a.measuredThrough > 0 && a.measuredThrough <= len(a.messages) {
		if estimate >= a.lastPromptEstimate {
			return toolBudgetAdd(a.lastInput, estimate-a.lastPromptEstimate)
		}
		return max(int64(0), a.lastInput-(a.lastPromptEstimate-estimate))
	}
	return estimate
}

// LastInput is the provider's token count for the most recent prompt, or 0 before
// any call has returned. Same concurrency caveat as Usage.
//
// Distinct from Usage().Input on purpose, and the difference is the whole point:
// this one is how full the window is, that one is what the session has cost. Both
// front ends already tracked their own copy of this for their gauges; having it on
// the agent is what lets the measurement written to the transcript come from the
// same place the gauges read.
func (a *Agent) LastInput() int64 { return a.lastInput }

// forceClear runs a clearing pass that the provider has already justified,
// returning the smaller prompt and whether it is actually smaller than the one that
// was rejected.
//
// Three parts of the normal policy are overridden, and each for the same reason —
// they exist to avoid paying for clearing that might not be needed, and the
// rejection settles that question:
//
//   - The trigger, because it is compared against a figure that is part measured
//     and part estimated. The provider's count is neither.
//   - ClearAtLeast, which exists so a small saving does not buy a cache miss. A
//     cache miss is not the alternative here; a dead session is.
//   - Keep, reduced to the single most recent result. One rather than zero because
//     clearing the result the model is about to reason about would make the retry
//     answer a question whose evidence had just been removed. Zero is not even
//     expressible: editContext reads a non-positive Keep as its default, so the
//     guard is there rather than here — which is why the property is tested through
//     a forced pass rather than by inspecting this value.
//
// ExcludeTools is *not* overridden. It is the caller saying "never clear this", and
// a mechanism that discards an explicit instruction the moment it becomes
// inconvenient is worse than one that reports it could not help. If the exclusion
// is what makes the prompt too large, the rejection is returned and says so.
//
// ok is false when the pass freed nothing new, which is also what bounds this: the
// frozen set only grows, so a second overflow in the same run cannot loop — the
// second pass finds nothing to add and the error is returned.
func (a *Agent) forceClear() ([]llm.Message, bool) {
	before := len(a.cleared)
	cfg := ContextEditConfig{
		Trigger:      1,
		Keep:         1,
		ClearAtLeast: 1,
		ExcludeTools: a.contextEdit.ExcludeTools,
	}
	prompt, edit := editContext(a.cachedToolPrompt(a.messages), cfg, a.cleared, math.MaxInt64)
	if edit.cleared == nil || len(edit.cleared) <= before {
		return nil, false
	}
	a.cleared = edit.cleared
	a.clearedTokens = a.toolBudgetStats().FreedTokens + edit.ClearedTokens + edit.ClearedArgTokens
	return prompt, true
}

// ClearedFromPrompt is how many estimated tokens context editing removed from the
// prompt LastInput describes. Zero when clearing is off or did nothing. Same
// concurrency caveat as Usage.
//
// It exists so a composition snapshot can compare like with like: Compose measures
// the whole history, LastInput measured a prompt that clearing had shrunk, and the
// ratio of those two is only a tokenizer ratio once this is subtracted.
func (a *Agent) ClearedFromPrompt() int64 { return a.clearedTokens }

// OverheadTokens is the fixed per-request cost: system prompt plus tool schemas,
// estimated at four characters per token. Unlike Usage it is safe to read from
// any goroutine. The tool view can change between model requests, so readers
// synchronize with those updates without touching the run's other counters.
func (a *Agent) OverheadTokens() int64 {
	a.overheadMu.RLock()
	defer a.overheadMu.RUnlock()
	return a.fixedCostTokens
}

// SetMessages restores a previous conversation, e.g. from a session file.
//
// The measured baseline is reset with it: lastInput described a prompt this history
// is not a continuation of, so promptTokens has to estimate from scratch until the
// next call reports a real number. Leaving it would let a resumed session read as
// whatever the previous one last measured.
func (a *Agent) SetMessages(m []llm.Message) {
	a.messages = m
	// The tool view is a function of the history (loaded Skills, selected MCP
	// tools), so it follows the history it now describes — and the fixed-cost
	// figure an interface reads before the next run is the one that run sends.
	a.resetMCPSelection()
	a.resetToolReadRecovery()
	a.lastInput, a.measuredThrough = 0, 0
	a.lastPromptEstimate = 0
	a.toolResultViews = nil
	a.resultLeaseRefresh = time.Time{}
	a.lastToolBudgetMessage = -1
	a.lastToolBudgetPlan = toolResultBudgetPlan{Headroom: -1}
	// A restored history may already carry stamps; the label sequence continues
	// past them rather than rehanding one. See SourceStore.observe.
	if a.sources != nil {
		a.sources.observe(m)
	}
}

// SetUsage restores the cost totals of a resumed session, so reopening a
// transcript does not zero the counters. Same single-writer caveat as Usage:
// call it before any run starts.
func (a *Agent) SetUsage(u, delegated llm.Usage) { a.usage, a.delegated = u, delegated }

// SetClient switches models between turns. The history stays as-is: every
// provider here speaks the same wire format, and thinking blocks are not
// replayed, so there is nothing model-specific left in the transcript to
// translate.
func (a *Agent) SetClient(c llm.Client) {
	a.client = c
	a.resetToolReadRecovery()
	a.lastInput, a.measuredThrough, a.lastPromptEstimate = 0, 0, 0
}

// SetPrice re-points the rate a cost budget is measured against, which a model
// switch has to do for the same reason SetContextEdit exists: the figure belongs to
// the model, not to the run, and carrying the old one over would measure the new
// model's tokens at the old model's rate.
//
// nil is a legitimate argument — it means the new model has no declared price — and
// it disables the comparison rather than treating the model as free. A caller that
// has a cost budget and does not want it silently dropped has to refuse the switch
// itself; the agent cannot, because by the time it is told, the client has changed.
// The CLI does refuse: see the /model command.
//
// Same rule as SetClient: between runs only, on the goroutine that owns the agent.
func (a *Agent) SetPrice(p *llm.Price) { a.price = p }

// CostBudget is the run's spend ceiling, and Price is the rate it is measured
// against. Both are read by the CLI to decide whether a model switch would silently
// remove a ceiling the user asked for.
func (a *Agent) CostBudget() float64 { return a.costBudget }

// Price is the rate currently in effect, or nil when the model has no declared one.
func (a *Agent) Price() *llm.Price { return a.price }

// SetContextEdit re-points the clearing policy, which a model switch has to do:
// "auto" is a fraction of the model's window, and the catalogue spans 262K to 1M.
// Left alone, moving from a 262K model to a 1M one would keep clearing at a fifth of
// the new window for no reason, and moving the other way would keep a trigger the new
// window cannot reach.
//
// Same rule as SetClient: between runs only, on the goroutine that owns the agent.
// The already-cleared set is deliberately kept — those results are gone from the
// prompt whatever the new threshold is, and restoring them would flap the prefix.
func (a *Agent) SetContextEdit(cfg ContextEditConfig) { a.contextEdit = cfg }

// ContextEditTrigger is the prompt size at which clearing begins, or 0 when it is
// off. Safe to read from any goroutine: it is only assigned between runs, by the
// goroutine that owns the agent.
//
// It exists for the interfaces rather than for the loop. A gauge drawn against fixed
// percentages of the window went permanently amber once the trigger moved to four
// fifths, because clearing holds occupancy just below it — so the thing a warning
// colour has to be measured against is this, not a constant.
func (a *Agent) ContextEditTrigger() int64 { return a.contextEdit.Trigger }

// estimateToolSchemaSize and estimateParameterSize are deleted rather than kept
// behind a flag. They estimated the wire size of tool schemas by padding — fifty
// bytes per tool, forty per nested object, ten per key — and the padding was the
// whole problem: on JSON Schema every property is a nested object, so the sum ran
// well over the bytes actually sent, and that inflated number was what the browser
// showed as "system overhead".
//
// Nothing referenced them but the line above, and no test pinned their output, so
// there was no behaviour to preserve. toolSchemaBytes marshals instead.

// Steer queues a message for delivery inside the run that is currently in
// flight. It reports whether there was one to queue.
//
// The message arrives after the current turn's tool calls have finished and
// before the next model call — the earliest point at which adding to the
// conversation is legal, since a tool_use must be answered by its tool_result and
// nothing else. This is what makes "no, do it the other way" possible without
// cancelling and losing the turn.
//
// A false return means the caller should start a new run instead. Checking
// Active() first would be a race; the answer has to come from the same lock that
// accepts the message.
func (a *Agent) Steer(text string) bool {
	return a.SteerMessage(llm.UserText(text))
}

// SteerMessage retains a driver's local receipt identity on the ordinary
// steering path. A nonempty InboxID requires a persistence acknowledgement.
func (a *Agent) SteerMessage(message llm.Message) bool {
	if !hasUserInput(message) {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.running {
		return false
	}
	a.pending = append(a.pending, message)
	return true
}

func hasUserInput(message llm.Message) bool {
	if message.Role != llm.RoleUser {
		return false
	}
	if strings.TrimSpace(message.Text()) != "" {
		return true
	}
	for _, block := range message.Content {
		if (block.Type == llm.BlockFile && block.FilePath != "") || (block.Type == llm.BlockImage && block.ImageRef != "") {
			return true
		}
	}
	return false
}

// takePending hands the queued messages to the run goroutine.
func (a *Agent) takePending() []llm.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.pending
	a.pending = nil
	return out
}

// resolveImages returns msgs with every image block either carrying its data
// URL or left for convert.go's description fallback. It copies only the
// messages it actually rewrites, and returns msgs itself when there is nothing
// to do — the common conversation pays nothing for this feature existing.
func (a *Agent) resolveImages(msgs []llm.Message) []llm.Message {
	if a.imageResolver == nil {
		return msgs
	}
	var out []llm.Message
	for i, m := range msgs {
		has := false
		for _, b := range m.Content {
			if b.Type == llm.BlockImage || len(b.Images) > 0 {
				has = true
				break
			}
		}
		if !has {
			continue
		}
		if out == nil {
			out = append([]llm.Message(nil), msgs...)
		}
		nm := m
		nm.Content = append([]llm.Block(nil), m.Content...)
		for j := range nm.Content {
			if len(nm.Content[j].Images) > 0 {
				nm.Content[j].Images = append([]llm.Block(nil), nm.Content[j].Images...)
				for k := range nm.Content[j].Images {
					im := &nm.Content[j].Images[k]
					im.DataURL = ""
					if im.Type == llm.BlockImage {
						if url, ok := a.imageResolver(im.ImageRef); ok {
							im.DataURL = url
						}
					}
				}
			}
			if nm.Content[j].Type != llm.BlockImage {
				continue
			}
			if url, ok := a.imageResolver(nm.Content[j].ImageRef); ok {
				nm.Content[j].DataURL = url
			}
		}
		out[i] = nm
	}
	if out == nil {
		return msgs
	}
	return out
}

func (a *Agent) hasPending() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.pending) > 0
}

// finishIfNoPending closes the normal completion boundary under the same lock
// that accepts steering. A late sender either joins this run or sees it closed.
func (a *Agent) finishIfNoPending() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.pending) != 0 {
		return false
	}
	a.running = false
	return true
}

// stopRunning closes the run to further steering and returns anything that was
// accepted but never delivered.
//
// There is always a window for that: a message can be accepted microseconds
// before the loop hits its turn limit or a transport error. Returning it rather
// than dropping it silently is the difference between the user being told and the
// user watching their message vanish.
func (a *Agent) stopRunning() []llm.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.running = false
	out := a.pending
	a.pending = nil
	return out
}

// Run appends the prompt and drives the loop until the model stops calling
// tools. The returned channel is closed after EventAgentEnd; the caller must
// drain it, which is what keeps the loop and its consumer in lockstep without
// any buffering policy.
func (a *Agent) Run(ctx context.Context, prompt string) <-chan Event {
	return a.RunMessage(ctx, llm.UserText(prompt))
}

// RunMessage drives the loop with an already-built first message — the entry
// point for turns whose content is more than text, which today means user
// messages carrying image blocks. Everything else about the run is identical.
func (a *Agent) RunMessage(ctx context.Context, first llm.Message) <-chan Event {
	return a.RunMessageWithSteering(ctx, first, nil)
}

// RunMessageWithSteering accepts messages queued while the driver prepared the
// run (for example, while refreshing MCP catalogs). Reserve steering before
// launching the goroutine so returning from this method never leaves a gap in
// which the run exists but cannot receive input.
func (a *Agent) RunMessageWithSteering(ctx context.Context, first llm.Message, steering []string) <-chan Event {
	pending := make([]llm.Message, 0, len(steering))
	for _, text := range steering {
		if strings.TrimSpace(text) != "" {
			pending = append(pending, llm.UserText(text))
		}
	}
	return a.RunMessageWithPending(ctx, first, pending)
}

// RunMessageWithPending is the same run entrypoint with durable receipt fields
// preserved for input accepted while the driver prepared a run.
func (a *Agent) RunMessageWithPending(ctx context.Context, first llm.Message, pending []llm.Message) <-chan Event {
	a.mu.Lock()
	a.running = true
	for _, m := range pending {
		if hasUserInput(m) {
			a.pending = append(a.pending, m)
		}
	}
	a.mu.Unlock()
	out := make(chan Event)
	go func() {
		defer close(out)
		a.runMessage(ctx, first, out)
	}()
	return out
}

func (a *Agent) runMessage(ctx context.Context, first llm.Message, out chan<- Event) {
	a.resetToolReadRecovery()
	a.messages = append(a.messages, first)

	// Every run starts from the view its history implies: tools the
	// conversation selected earlier stay declared (the same list the previous
	// request carried, so a prefix cache survives the run boundary), and
	// nothing outside the history's verified describe results can ride in.
	// In-flight selections from an interrupted batch are dropped. The
	// registry itself is untouched.
	a.resetMCPSelection()

	// Initialize tracking for this run
	a.runStartTime = time.Now()
	a.todoReminded = false
	a.officeFinish = &officeFinishState{}
	a.citeRetried = false
	var citationReads []string
	var citationFallback *llm.Message
	var quoteFallback *llm.Message
	var coveragePending bool
	var coverageAnswer *llm.Message
	var quoteRetried bool
	var lastQuoteCheck *EvidenceQuoteCheck
	var lastQuoteRepair *EvidenceQuoteRepairStatus
	evidence := newEvidenceLedger(a)
	var stagnationHistory []string
	var repeatedMCPReads mcpReadRepeats
	mcpReportReviewed := false
	protocolRetried := false
	continuation := outputContinuation{limit: a.continuation.MaxAttempts}

	// finish is the single exit: it closes the run to steering and reports
	// anything accepted but not delivered, so no terminal path can drop a message
	// without saying so.
	finish := func(e Event) {
		coverage := evidence.Snapshot()
		e.EvidenceCoverage = &coverage
		e.QuoteCheck, e.QuoteRepair = lastQuoteCheck, lastQuoteRepair
		for _, m := range a.stopRunning() {
			// Durable messages remain visible in the driver's inbox, rather than
			// returning a second copy to its legacy text-only composer.
			if m.InboxID == "" {
				e.Undelivered = append(e.Undelivered, m.Text())
			}
		}
		// Stamped here rather than at each call site so that no exit path — turn
		// limit, budget, stagnation, transport error — can forget it.
		e.Timing = a.timing.summary()
		a.emitFinal(out, e)
	}

	a.emit(ctx, out, Event{Kind: EventAgentStart})

	var stop llm.StopReason
	for turn := 1; ; turn++ {
		// Cancellation before the next model boundary must leave newly accepted
		// steering undelivered, rather than committing it into an already stopped
		// run only to issue a provider request with a cancelled context.
		if ctx.Err() != nil {
			finish(Event{Kind: EventAgentEnd, EndReason: EndAborted, StopReason: llm.StopAborted, Usage: a.usage})
			return
		}
		// Check budgets
		if reason, detail := a.checkBudgets(); reason != "" {
			if citationFallback != nil || quoteFallback != nil || coveragePending {
				// The task already produced a complete answer. An optional citation
				// rewrite must not turn that answer into a budget failure.
				finish(Event{Kind: EventAgentEnd, EndReason: endReasonForStop(stop), StopReason: stop, Usage: a.usage})
				return
			}
			finish(Event{
				Kind: EventAgentEnd, EndReason: reason, Usage: a.usage,
				Err: fmt.Errorf("budget exceeded: %s", detail),
			})
			return
		}

		if limit := int(a.effectiveTurnLimit()); limit >= 0 && turn > limit {
			if coveragePending {
				finish(Event{Kind: EventAgentEnd, EndReason: endReasonForStop(stop), StopReason: stop, Usage: a.usage})
				return
			}
			finish(Event{
				Kind: EventAgentEnd, EndReason: EndTurnLimit, Usage: a.usage,
				Err: fmt.Errorf("stopped after %d turns without a final answer", limit),
			})
			return
		}

		// The soft cap is a checkpoint, not an exit: one message, and the loop
		// continues. It lands before steering so that when both arrive at the
		// same boundary, the human's words are the fresher ones.
		if notice, ok := a.softCheckpoint(turn); ok {
			a.messages = append(a.messages, llm.UserText(notice))
			a.emit(ctx, out, Event{Kind: EventSteer, Text: notice})
		}

		// Steering lands here: after the previous turn's tool results, before the
		// next model call. Any earlier and it would come between a tool_use and its
		// tool_result, which the API rejects.
		for _, m := range a.takePending() {
			// Fresh human steering starts a new evidence scope. Old reads stay
			// citable but cannot force citations into an unrelated follow-up.
			citationReads = nil
			citationFallback = nil
			quoteFallback = nil
			coveragePending = false
			coverageAnswer = nil
			lastQuoteCheck, lastQuoteRepair = nil, nil
			evidence = newEvidenceLedger(a)
			a.messages = append(a.messages, m)
			e := Event{Kind: EventSteer, Text: m.Text(), Message: m}
			if m.InboxID != "" {
				e.SteerAck = make(chan error, 1)
			}
			a.emit(ctx, out, e)
			if e.SteerAck != nil {
				if err := <-e.SteerAck; err != nil {
					finish(Event{Kind: EventAgentEnd, EndReason: EndAborted, StopReason: llm.StopAborted, Usage: a.usage, Err: err})
					return
				}
			}
		}

		// A Skill read or invoked since the last request declares its bound tools
		// on this one (skillview.go). Before context editing and the turn-start
		// event, so both see the fixed cost this request actually carries.
		a.refreshSkillToolView()
		a.refreshRuntimePrompt()

		// Context editing runs here: after steering has landed, immediately before the
		// request is built, on the history exactly as it will be sent. a.messages is
		// not touched — prompt is a view for this one call, so the transcript, the
		// session file and every audit keep the original output.
		a.ensureToolHistoryBudgets(ctx)
		budgetStats := a.toolBudgetStats()
		prompt, edit := editContext(a.cachedToolPrompt(a.messages), a.activeContextEditConfig(), a.cleared, a.promptTokens())
		if edit.cleared != nil {
			a.cleared = edit.cleared
		}
		// Overwritten rather than accumulated: it describes the gap between the
		// history and the one prompt about to be sent, which is exactly the prompt
		// LastInput will report on.
		a.clearedTokens = budgetStats.FreedTokens + edit.ClearedTokens + edit.ClearedArgTokens

		turnStart := Event{Kind: EventTurnStart, Turn: turn}
		if budgetStats.Batches > 0 && !a.toolOutputBudget.Disabled {
			turnStart.ToolOutputBudget = &budgetStats
		}
		if edit.Cleared() {
			turnStart.ContextEdit = &edit
		}
		a.emit(ctx, out, turnStart)

		// Recorded before the call so promptTokens knows which messages the count it
		// is about to receive covers.
		a.measuredThrough = len(a.messages)

		// Images resolve against the live model decision: the same history sends
		// pixels to a model that sees them and description text to one that does
		// not. A view is built rather than mutating a.messages so the transcript
		// keeps the reference form.
		sentHistoryEstimate := llm.EstimateTokensFor(a.client, prompt)
		prompt = a.resolveImages(prompt)

		repairingCitation := citationFallback != nil
		repairingQuote := quoteFallback != nil
		repairingFinal := repairingCitation || repairingQuote
		var quoteRepair *EvidenceQuoteRepairStatus
		var citationRepair *CitationRepairStatus
		schemas := a.schemas
		if repairingFinal {
			schemas = nil
		}
		sentFixedCost := a.fixedCostTokens
		if repairingFinal {
			sentFixedCost -= a.toolSchemaTokens
		}
		sentPromptEstimate := sentHistoryEstimate + sentFixedCost
		callCtx := ctx
		cancelReview := func() {}
		var reviewStatus *AnswerReviewStatus
		var reviewStarted time.Time
		var reviewThinking strings.Builder
		if repairingFinal {
			reviewStarted = time.Now()
			kind := "citation"
			if repairingQuote {
				kind = "quote"
			}
			reviewStatus = &AnswerReviewStatus{Kind: kind, State: "running", Turn: turn, StartedAt: reviewStarted.UnixMilli()}
			a.emit(ctx, out, Event{Kind: EventAnswerReview, AnswerReview: reviewStatus.Clone()})
			callCtx, cancelReview = context.WithTimeout(ctx, a.answerReviewTimeout)
		}
		// The optional checkpoint has now reached a real request. If it asks
		// for further tools, their subsequent work obeys the normal budgets.
		coveragePending = false
		a.refreshRuntimePrompt()
		sentFixedCost = a.fixedCostTokens
		if repairingFinal {
			sentFixedCost -= a.toolSchemaTokens
		}
		sentPromptEstimate = sentHistoryEstimate + sentFixedCost
		a.measuredThrough = len(a.messages)
		resp, err := a.client.Stream(callCtx, a.modelSystemPrompt(), prompt, schemas, func(d llm.Delta) {
			// Only proposed prose/tool calls stay buffered until review. Actual
			// reasoning is visible while it arrives and retained on any fallback.
			if repairingFinal {
				if d.Kind == llm.DeltaThinking {
					reviewThinking.WriteString(d.Text)
					a.emit(ctx, out, Event{Kind: EventThinkingDelta, Text: d.Text})
				}
				return
			}
			if coverageAnswer != nil && d.Kind == llm.DeltaText {
				return
			}
			switch d.Kind {
			case llm.DeltaText:
				a.emit(ctx, out, Event{Kind: EventTextDelta, Text: d.Text})
			case llm.DeltaThinking:
				a.emit(ctx, out, Event{Kind: EventThinkingDelta, Text: d.Text})
			case llm.DeltaToolCallStart:
				// Name-bearing and fragment-less: it lets a consumer open a
				// preview card as early as possible, before any arguments arrive.
				a.emit(ctx, out, Event{Kind: EventToolArgs, ToolCallID: d.ToolID, ToolName: d.ToolName})
			case llm.DeltaToolCallArgs:
				a.emit(ctx, out, Event{Kind: EventToolArgs, ToolCallID: d.ToolID, Text: d.Text})
			}
		})
		reviewTimedOut := repairingFinal && ctx.Err() == nil && errors.Is(callCtx.Err(), context.DeadlineExceeded)
		cancelReview()
		reviewAttempt := resp.Message
		if err != nil {
			// One failure is recoverable without the model's help: the prompt did
			// not fit. Clearing normally waits for a threshold, and a threshold is a
			// guess — it is compared against a number that is part measured and part
			// estimated, so it can be crossed without anyone noticing. The provider's
			// rejection is not a guess, so it gets to force a pass.
			//
			// This is the difference between a failed turn and a dead session. The
			// history only grows, so without this the next prompt in the same session
			// is larger again: the first overflow made every later message in that
			// session fail too, and 400 is not in llm/retry.go's retryable set, so
			// nothing else was going to intervene. Recovering here is what makes the
			// failure a hiccup rather than "start a new session".
			//
			// No event announces this. A second EventTurnStart for the same turn
			// would be wrong — web/hub.go mints the turn's message id there, on the
			// stated contract that one turn produces one assistant message — and the
			// right home is a new event kind, which is a change to the wire contract
			// rather than to this file. It is not silent in the end: editContext
			// reports the whole frozen set on every pass, not just what it added, so
			// the next turn's turn_start carries the larger figure in both front ends.
			if !repairingFinal && llm.IsContextOverflow(err) {
				if retryPrompt, ok := a.forceClear(); ok {
					a.refreshRuntimePrompt()
					sentFixedCost = a.fixedCostTokens
					sentPromptEstimate = llm.EstimateTokensFor(a.client, retryPrompt) + sentFixedCost
					prompt = a.resolveImages(retryPrompt)
					a.measuredThrough = len(a.messages)
					// nil observer: the first attempt streamed nothing (an overflow is
					// rejected before generation), but a consumer that had already been
					// handed fragments must not receive a second copy of them.
					resp, err = a.client.Stream(ctx, a.modelSystemPrompt(), prompt, a.schemas, nil)
				}
			}
		}
		// Record the bounded prompt that this successful request actually used,
		// including the retry prompt after context overflow. Host-only full pages
		// and failed requests never count as delivered evidence.
		if err == nil && resp.StopReason != llm.StopAborted {
			evidence.ObservePrompt(prompt)
		}
		if err == nil && coverageAnswer != nil && len(resp.Message.ToolCalls()) == 0 &&
			(resp.StopReason == llm.StopEndTurn || resp.StopReason == llm.StopMaxTokens) {
			if resp.StopReason == llm.StopMaxTokens {
				resp.Message = *coverageAnswer
				evidence.RejectCheckpointAnswer("checkpoint_output_incomplete")
			} else if !evidenceCheckpointFormatMatches(coverageAnswer.Text(), resp.Message.Text()) {
				resp.Message = *coverageAnswer
				evidence.RejectCheckpointAnswer("checkpoint_structured_format_changed")
			}
			coverageAnswer = nil
		}
		if repairingQuote {
			quoteRepair = &EvidenceQuoteRepairStatus{Attempted: true}
			failure := ""
			if ctx.Err() != nil {
				failure = "cancelled"
			} else if reviewTimedOut || err != nil || resp.ProtocolIssue != nil || resp.StopReason != llm.StopEndTurn ||
				len(resp.Message.ToolCalls()) != 0 || strings.TrimSpace(resp.Message.Text()) == "" {
				failure = "rewrite_failed"
			} else {
				failure = EvidenceQuoteRewriteFailure(quoteFallback.Text(), resp.Message.Text(), evidence)
			}
			if failure != "" {
				quoteRepair.Reason = failure
				resp.Message = *quoteFallback
				resp.StopReason = llm.StopEndTurn
				if ctx.Err() != nil {
					resp.StopReason = llm.StopAborted
				}
				resp.ProtocolIssue, err = nil, nil
			} else {
				quoteRepair.Accepted = true
			}
			lastQuoteRepair = quoteRepair
			quoteFallback = nil
		}
		if repairingCitation {
			citationRepair = &CitationRepairStatus{Attempted: true}
			structuredFailure := structuredCitationRewriteFailure(citationFallback.Text(), resp.Message.Text(), a.sources)
			// Tool declarations being absent is not an execution boundary by
			// itself. Reject every tool request before persistence and dispatch,
			// including a provider that ignores the text-only correction request.
			if ctx.Err() != nil {
				citationRepair.Reason = "cancelled"
				resp.Message = *citationFallback
				resp.StopReason = llm.StopAborted
				resp.ProtocolIssue = nil
				err = nil
			} else if reviewTimedOut || err != nil || resp.ProtocolIssue != nil ||
				resp.StopReason != llm.StopEndTurn || len(resp.Message.ToolCalls()) != 0 ||
				strings.TrimSpace(resp.Message.Text()) == "" ||
				citationRewriteAcknowledgement(resp.Message.Text()) ||
				structuredFailure == "" && (len(ValidateCitations(resp.Message.Text(), a.sources)) > 0 || len(FindPageMarkers(resp.Message.Text())) > 0) {
				citationRepair.Reason = "rewrite_failed"
				resp.Message = *citationFallback
				resp.StopReason = llm.StopEndTurn
				resp.ProtocolIssue = nil
				err = nil
			} else if structuredFailure != "" {
				citationRepair.Reason = structuredFailure
				resp.Message = *citationFallback
				resp.StopReason = llm.StopEndTurn
				resp.ProtocolIssue = nil
				err = nil
			} else {
				citationRepair.Accepted = true
			}
			citationFallback = nil
		}
		if reviewStatus != nil {
			status := reviewStatus.Clone()
			elapsed := time.Since(reviewStarted)
			status.FinishedAt, status.DurationMS = time.Now().UnixMilli(), elapsed.Milliseconds()
			accepted := citationRepair != nil && citationRepair.Accepted || quoteRepair != nil && quoteRepair.Accepted
			status.State = "completed"
			if !accepted {
				status.State = "fallback"
				if citationRepair != nil {
					status.Reason = citationRepair.Reason
				} else if quoteRepair != nil {
					status.Reason = quoteRepair.Reason
				}
			}
			if reviewTimedOut {
				status.Reason = "timeout"
				if citationRepair != nil {
					citationRepair.Reason = "timeout"
				}
				if quoteRepair != nil {
					quoteRepair.Reason = "timeout"
				}
			} else if ctx.Err() != nil {
				status.State, status.Reason = "cancelled", "cancelled"
			}
			resp.Message = preserveAnswerReviewThinking(resp.Message, reviewAttempt, reviewThinking.String(), !accepted, elapsed)
			resp.Message.AnswerReview = status.Clone()
			a.emit(ctx, out, Event{Kind: EventAnswerReview, AnswerReview: status})
		}
		if err != nil {
			// A transport failure is the one thing the loop cannot hand back to
			// the model, so it ends the run.
			//
			// Still an overflow here means the branch above did not save it: either
			// clearing is off, or the frozen set had nothing left to add. Reported
			// apart from a transport error because the two are fixed by opposite
			// actions — this one by compacting or starting a session, that one by
			// waiting — and both are 400s on the same provider.
			reason := EndTransportError
			if llm.IsContextOverflow(err) {
				reason = EndContextOverflow
			}
			var protocolIssue *llm.ToolProtocolIssue
			if errors.As(err, &protocolIssue) {
				reason = EndToolProtocol
			}
			finish(Event{
				Kind: EventAgentEnd, EndReason: reason,
				StopReason: llm.StopError, Usage: a.usage, Err: err,
			})
			return
		}

		a.messages = append(a.messages, resp.Message)
		if a.sources != nil {
			a.sources.reserveCitations(resp.Message.Text())
		}
		a.usage.Input += resp.Usage.Input
		a.usage.Output += resp.Usage.Output
		a.usage.CacheRead += resp.Usage.CacheRead
		a.usage.Reasoning += resp.Usage.Reasoning
		a.lastInput = resp.Usage.Input
		a.lastPromptEstimate = sentPromptEstimate
		a.timing.add(resp.Timing)
		// This turn's own usage rides along, not the accumulated total: it is the
		// only signal that says how full the context window currently is. Timing is
		// per-call for the same reason: an average is for the end of the run.
		event := Event{
			Kind: EventMessage, Message: resp.Message,
			Usage: resp.Usage, CallTiming: resp.Timing,
			CitationRepair: citationRepair,
			AnswerReview:   resp.Message.AnswerReview.Clone(),
			QuoteRepair:    quoteRepair,
		}

		if resp.StopReason == llm.StopEndTurn && len(resp.Message.ToolCalls()) == 0 {
			check := CheckEvidenceQuotes(resp.Message.Text(), evidence)
			lastQuoteCheck = nil
			if check.OptedIn {
				lastQuoteCheck = &check
				event.QuoteCheck = &check
			}
			coverage := evidence.Snapshot()
			event.EvidenceCoverage = &coverage
		}

		// Calculate overhead metrics for RFC B1
		if resp.Usage.Input > 0 {
			event.OverheadMetrics = &OverheadMetrics{
				FixedCostTokens:  a.fixedCostTokens,
				TotalInputTokens: resp.Usage.Input,
				OverheadRatio:    float64(a.fixedCostTokens) / float64(resp.Usage.Input),
			}
		}

		a.emit(ctx, out, event)
		stop = resp.StopReason

		if stop == llm.StopAborted {
			break
		}
		if issue := resp.ProtocolIssue; issue != nil {
			// The client guarantees an issue response contains no executable
			// calls. One correction per run, under the existing turn budgets.
			if !protocolRetried && len(resp.Message.ToolCalls()) == 0 &&
				(stop == llm.StopEndTurn || stop == llm.StopToolUse) {
				protocolRetried = true
				notice := issue.Error()
				a.messages = append(a.messages, llm.UserText(notice))
				a.emit(ctx, out, Event{Kind: EventSteer, Text: notice})
				continue
			}
			finish(Event{Kind: EventAgentEnd, EndReason: EndToolProtocol,
				StopReason: llm.StopError, Usage: a.usage, Err: issue})
			return
		}

		calls := resp.Message.ToolCalls()
		if len(calls) == 0 {
			// The model is finished — unless the user queued something while it
			// was finishing. Breaking with a message still in the queue would
			// swallow it, and it is exactly the message someone typed because they
			// saw where the answer was heading.
			if a.hasPending() {
				continue
			}
			if stop == llm.StopMaxTokens && continuation.limit > 0 && !repairingFinal {
				if again, terminal := a.continueOutput(ctx, turn, &continuation, out); again {
					continue
				} else if terminal != nil {
					finish(*terminal)
					return
				}
				break
			}
			if stop == llm.StopEndTurn && a.hasTurnCapacity(turn+1) {
				if notice, ok := a.officeFinish.notice(); ok {
					a.messages = append(a.messages, llm.UserText(notice))
					a.emit(ctx, out, Event{Kind: EventSteer, Text: notice})
					continue
				}
			}
			// Last call for the task list. The model has just decided it is
			// finished; if its own list says otherwise, this is the only moment
			// anyone can still ask it which of the remaining items are done,
			// dropped or stuck. Once per run — see todofinish.go.
			if notice, ok := a.todoFinishNotice(); ok {
				a.todoReminded = true
				a.messages = append(a.messages, llm.UserText(notice))
				a.emit(ctx, out, Event{Kind: EventSteer, Text: notice})
				continue
			}
			// A partial page is not a body-wide proof. This checkpoint is about
			// observed delivery, not a natural-language verdict: the model may
			// read a necessary next page or retain a suitably limited conclusion.
			if stop == llm.StopEndTurn && !repairingFinal && a.hasTurnCapacity(turn+1) &&
				(parseCitationJSONObject(resp.Message.Text()) != nil || citationReviewableProse(resp.Message.Text())) {
				if reason, _ := a.checkBudgets(); reason == "" {
					if notice, ok := evidence.CoverageNotice(); ok {
						coveragePending = true
						if parseCitationJSONObject(resp.Message.Text()) != nil {
							complete := resp.Message
							coverageAnswer = &complete
						}
						a.messages = append(a.messages, llm.UserText(notice))
						a.emit(ctx, out, Event{Kind: EventSteer, Text: notice})
						continue
					}
				}
			}
			// Only the declared JSON evidence[].quote contract opts into a
			// literal check. One text-only repair may change quotations, while
			// every other value remains protected by the rewrite validator.
			if !quoteRetried && !repairingFinal && stop == llm.StopEndTurn && a.hasTurnCapacity(turn+1) {
				check := CheckEvidenceQuotes(resp.Message.Text(), evidence)
				if notice := EvidenceQuoteRepairNotice(check); notice != "" {
					quoteRetried = true
					fallback := resp.Message
					quoteFallback = &fallback
					a.messages = append(a.messages, llm.UserText(notice))
					a.emit(ctx, out, Event{Kind: EventSteer, Text: notice})
					continue
				}
			}
			// Invalid anchors, bare page markers and wholly missing attribution
			// share one bounded, text-only rewrite. A label's existence is not a
			// judgment that it supports the claim: that remains the model's review.
			if a.sources != nil && !a.citeRetried && stop == llm.StopEndTurn && a.hasTurnCapacity(turn+1) {
				text := resp.Message.Text()
				invalid := ValidateCitations(text, a.sources)
				markers := FindPageMarkers(text)
				missing := missingCitationNotice(text, citationReads, a.sources)
				if len(invalid) > 0 || len(markers) > 0 || missing != "" {
					a.citeRetried = true
					fallback := resp.Message
					citationFallback = &fallback
					var parts []string
					if len(invalid) > 0 {
						parts = append(parts, citeSteerNotice(invalid))
					}
					if len(markers) > 0 {
						parts = append(parts, pageMarkerSteerNotice(markers))
					}
					if missing != "" {
						parts = append(parts, missing)
					}
					notice := strings.Join(parts, " ") + structuredCitationNotice(text, citationReads) + citationRewriteOnlyNotice
					a.messages = append(a.messages, llm.UserText(notice))
					a.emit(ctx, out, Event{Kind: EventSteer, Text: notice})
					continue
				}
			}
			if a.sources != nil && !mcpReportReviewed && !repairingFinal {
				if notice := repeatedMCPReads.reviewNotice(resp.Message.Text()); notice != "" {
					mcpReportReviewed = true
					a.messages = append(a.messages, llm.UserText(notice))
					a.emit(ctx, out, Event{Kind: EventSteer, Text: notice})
					continue
				}
			}
			if !a.finishIfNoPending() {
				continue
			}
			break
		}

		evidence.Register(calls)

		// Every tool_result for one assistant message must ride in a single user
		// message, in the same order as the tool_use blocks. Pre-allocating and
		// writing by index gives that ordering for free and needs no mutex, since
		// each goroutine owns one slot.
		//
		// Every tool_use must get a result even when the run is cancelled
		// mid-batch: an assistant message with an unpaired tool_use is rejected
		// by the API on the next request, which would make the session
		// unresumable.
		results := make([]llm.Block, len(calls))
		// delegated collects tokens spent by subagents, one slot per call for the
		// same reason results has one: each goroutine owns its own, so no lock is
		// needed. It is folded into the run's totals below, after the batch, on this
		// goroutine — the usage counters have a single writer by design (see the note
		// on Usage), and adding to them from a batch goroutine would be a race that
		// -race would catch and a reader would not.
		delegated := make([]llm.Usage, len(calls))
		// One approval budget for the whole batch, not per segment: it exists to
		// bound how long one assistant message can leave a run waiting on a person,
		// and that question does not reset because the batch changed shape.
		started := time.Now()
		for _, seg := range a.segments(calls) {
			if !seg.parallel {
				// Review and execution interleave on purpose. It is the better
				// order here: approving the second command after seeing what the
				// first one did beats approving both blind.
				//
				// No budget check, matching what a wholly sequential batch has
				// always done. The budget answers "how long may a batch sit in the
				// gate before the rest is refused unreviewed", and refusing a
				// command whose predecessor's output is the reason to allow it
				// would trade a wait for a wrong answer.
				for i := seg.from; i < seg.to; i++ {
					results[i] = a.runToolCall(ctx, i, a.review(ctx, calls[i], turn, stop), &delegated[i], out)
				}
				continue
			}
			// Review the whole segment before running any of it, one call at a
			// time. The gate is a person: reviewing concurrently means three
			// approval cards appearing at once, in an order nobody chose, each
			// with its own countdown. Execution still overlaps — that is what the
			// parallel segment is for — but the questions are asked in call order.
			//
			// Asking one at a time means the waits add up, so the phase gets a
			// budget: see reviewBudgetSpent for why the run cannot be allowed to
			// spend its whole life here.
			reviews := make([]reviewedCall, seg.to-seg.from)
			for j := range reviews {
				i := seg.from + j
				if a.reviewBudgetSpent(started, i) {
					reviews[j] = settledCall(calls[i], fmt.Sprintf(
						"Tool call %q was not executed: the batch spent its whole approval budget (%s) waiting on "+
							"earlier calls in the same batch. Re-issue it, or ask the user to approve sooner.",
						calls[i].Name, a.reviewBudget))
					continue
				}
				reviews[j] = a.review(ctx, calls[i], turn, stop)
			}
			a.runBatchParallel(ctx, seg.from, reviews, results[seg.from:seg.to], delegated[seg.from:seg.to], out)
		}
		// The batch has fully settled. Any mcp_describe detail calls in it now
		// become view selections, rebuilt here on the loop goroutine — before
		// the next request and never while tool goroutines are running.
		a.applyPendingSelections()
		// A subagent's tokens are the parent's tokens: they were spent on the
		// parent's behalf, and without this a token budget would bound only the work
		// the agent did not delegate. The cost is one turn of latency — the budget is
		// checked at the top of the loop, so a child's spending lands on the next
		// check rather than this one, which is the price of keeping a single writer.
		// Counted twice on purpose, into two different questions. a.usage answers
		// "what has this run cost, all in", which is what a budget has to bound.
		// a.delegated answers "how much of that was spent by someone else", which is
		// the only way to tell an expensive conversation from an expensive delegation
		// after the fact — and the two are fixed by very different decisions.
		for _, u := range delegated {
			a.usage.Input += u.Input
			a.usage.Output += u.Output
			a.usage.CacheRead += u.CacheRead
			a.usage.Reasoning += u.Reasoning

			a.delegated.Input += u.Input
			a.delegated.Output += u.Output
			a.delegated.CacheRead += u.CacheRead
			a.delegated.Reasoning += u.Reasoning
		}
		// Text budgets are resolved after the complete batch, in call order.
		// Original results below remain unchanged for persistence and UI ranges.
		a.cacheToolBatch(ctx, calls, results, len(a.messages), a.promptTokens())
		a.messages = append(a.messages, llm.Message{Role: llm.RoleUser, Content: results})
		citationReads = append(citationReads, a.citationReadLabels(calls, a.citationResultViews(results, len(a.messages)-1))...)
		// The turn is now complete on disk terms: the assistant message and its
		// results can persist together, never one without the other.
		a.emit(ctx, out, Event{Kind: EventToolResults, Message: a.messages[len(a.messages)-1]})

		// review refused every call from a max_tokens response because its
		// arguments may be truncated. Settle and persist those pairs before
		// considering one completion, and never run them as part of recovery.
		if stop == llm.StopMaxTokens && continuation.limit > 0 && !repairingFinal {
			if a.hasPending() {
				continue
			}
			if again, terminal := a.continueOutput(ctx, turn, &continuation, out); again {
				continue
			} else if terminal != nil {
				finish(*terminal)
				return
			}
			break
		}

		// Check for stagnation after tool results
		if a.stagnationThreshold > 0 && len(results) > 0 {
			// Compute hash of this turn's calls and their results
			resultHash := a.hashToolResults(calls, results)
			stagnationHistory = append(stagnationHistory, resultHash)

			// Check if we're stagnating
			if stagnant, reason := a.detectStagnation(stagnationHistory); stagnant {
				finish(Event{
					Kind: EventAgentEnd, EndReason: EndStagnation, Usage: a.usage,
					Err: fmt.Errorf("stagnation detected: %s", reason),
				})
				return
			}
		}

		if ctx.Err() != nil {
			stop = llm.StopAborted
			break
		}
		if notice := repeatedMCPReads.notice(a, calls, results); notice != "" {
			a.messages = append(a.messages, llm.UserText(notice))
			a.emit(ctx, out, Event{Kind: EventSteer, Text: notice})
		}
	}

	finish(Event{
		Kind: EventAgentEnd, EndReason: endReasonForStop(stop),
		StopReason: stop, Usage: a.usage,
	})
}

// segment is a run of consecutive calls in one batch that are dispatched together.
// The bounds are indices into the batch, [from, to), so that results and delegated
// slots stay addressed by tool_use position however the batch is carved up.
type segment struct {
	from, to int
	parallel bool
}

// segments splits a batch into runs of consecutive calls that may overlap.
//
// This replaces the rule inherited from pi, which was that one sequential tool in a
// batch serializes all of it. That rule is safe and too coarse: a turn that reads
// three files and runs a build has four calls, only one of which cannot overlap, and
// it used to run all four one at a time. The reads were serialized for a reason that
// had nothing to do with them.
//
// What the coarse rule bought is kept exactly. A sequential call still gets a
// segment to itself, so it overlaps nothing — which is the whole content of the
// promise, and the reason bash can keep assuming no sibling is reading a file it is
// about to rewrite. Grouping is over *consecutive* calls, so a sequential call also
// still acts as a barrier: nothing before it runs alongside anything after it.
//
// The two dispatch shapes are unchanged too, which is why this needed no change to
// the gate: a parallel segment is reviewed in call order and then executed
// concurrently, a sequential one interleaves review and execution so a person can
// see the first command's output before approving the second.
//
// A lone parallel call is marked sequential, because dispatching one call through
// the concurrent path buys nothing and would put it under the approval budget for
// no reason. That also keeps a single-call batch byte-for-byte what it was.
func (a *Agent) segments(calls []llm.Block) []segment {
	var out []segment
	for i := 0; i < len(calls); {
		j := i + 1
		if a.mayOverlap(calls[i]) {
			for j < len(calls) && a.mayOverlap(calls[j]) {
				j++
			}
		}
		out = append(out, segment{from: i, to: j, parallel: j-i > 1})
		i = j
	}
	return out
}

// mayOverlap reports whether one call is allowed to run alongside a sibling.
func (a *Agent) mayOverlap(c llm.Block) bool {
	if a.toolExecution == tools.Sequential {
		return false
	}
	t, ok := a.registry.Get(c.Name)
	// An unknown tool fails instantly, but treat it as sequential anyway rather
	// than guessing about a tool we know nothing about.
	if !ok {
		return false
	}
	if dynamic, ok := t.(tools.ArgumentExecutionMode); ok {
		return dynamic.ExecutionModeForArgs(c.Input) == tools.Parallel
	}
	return t.ExecutionMode() == tools.Parallel
}

// runBatchParallel executes already-reviewed calls with a bounded number in
// flight.
//
// A plain semaphore rather than errgroup: the only feature needed is the limit,
// and errgroup's error propagation is actively wrong here. Cancelling siblings
// when one call fails would leave tool_use blocks without results.
func (a *Agent) runBatchParallel(
	ctx context.Context, batchOffset int, reviews []reviewedCall, results []llm.Block,
	delegated []llm.Usage, out chan<- Event,
) {
	// Approval can rewrite a read-only operation into a writable one. Recheck
	// the approved arguments before starting goroutines; the original segment
	// must not make a rewritten edit overlap another call.
	for _, rv := range reviews {
		if rv.settled {
			continue
		}
		if dynamic, ok := rv.tool.(tools.ArgumentExecutionMode); ok && dynamic.ExecutionModeForArgs(rv.args) != tools.Parallel {
			for i, reviewed := range reviews {
				results[i] = a.runToolCall(ctx, batchOffset+i, reviewed, &delegated[i], out)
			}
			return
		}
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, a.toolConcurrency)
	for i, rv := range reviews {
		wg.Add(1)
		go func(i int, rv reviewedCall) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = a.runToolCall(ctx, batchOffset+i, rv, &delegated[i], out)
		}(i, rv)
	}
	wg.Wait()
}

// reviewBudgetSpent reports whether a batch has spent its approval budget and
// the calls still queued should be refused unreviewed.
//
// Reviewing one call at a time makes the waits additive, and each one can run to
// the gate's own timeout. Eight unanswered calls at five minutes apiece outlast a
// thirty-minute run, and then the run's own deadline kills everything: every
// call comes back "aborted", the turn is lost, and nothing explains why.
//
// Spending the budget instead refuses the remainder with a reason the model can
// act on and leaves the run alive. The bound is the budget plus at most one gate
// timeout, since a review already under way is allowed to finish.
//
// i > 0 because the first call must always get its question asked: a budget that
// can refuse everything unreviewed is just a broken gate.
func (a *Agent) reviewBudgetSpent(started time.Time, i int) bool {
	return a.reviewBudget > 0 && i > 0 && time.Since(started) >= a.reviewBudget
}

// settledCall builds a call that was decided without running, carrying the text
// the model will read as its result.
func settledCall(call llm.Block, msg string) reviewedCall {
	return reviewedCall{call: call, args: call.Input, settled: true, output: msg, isError: true}
}

// reviewedCall is one tool call after review: either a decision that settles it
// without running anything, or a tool plus the arguments to run it with.
type reviewedCall struct {
	call llm.Block
	tool tools.Tool
	// args are what will execute and what tool_start reports. The gate's rewrite
	// lands here, which is why the event cannot be emitted before review.
	args json.RawMessage

	// settled marks a call decided during review: an unknown tool, arguments that
	// may be truncated, a refusal, or a cancellation. output and isError carry the
	// result the model will see.
	settled bool
	output  string
	isError bool
	// A host recovery receipt is not another observation of the saved data.
	recoverySuppressed bool
}

// review decides whether and how a call will run, without emitting anything and
// without touching the filesystem.
//
// Splitting this out from execution is what lets a parallel batch ask its
// approval questions one at a time while still running the work concurrently.
// It also means the phase that can block on a human is the phase that does no
// I/O, which is easier to reason about.
//
// Nothing here is fatal: an unknown tool, malformed arguments, or a refusal all
// become text the model can read and react to. Turning these into hard errors is
// the classic way to make an agent brittle.
func (a *Agent) review(ctx context.Context, call llm.Block, turn int, stop llm.StopReason) reviewedCall {
	rv := reviewedCall{call: call, args: call.Input}
	if len(rv.args) == 0 {
		rv.args = json.RawMessage("{}")
	}
	settle := func(msg string) reviewedCall {
		rv.settled, rv.output, rv.isError = true, msg, true
		return rv
	}

	if stop == llm.StopMaxTokens {
		// The response was cut off mid-stream, so the arguments of every tool
		// call in it may be silently incomplete. None are safe to run.
		return settle(fmt.Sprintf("Tool call %q was not executed: the response hit the output token limit, so its "+
			"arguments may be truncated. Re-issue the call with complete arguments.", call.Name))
	}
	if ctx.Err() != nil {
		return settle("Operation aborted")
	}
	if issue := llm.ToolInputJSONError(rv.args); issue != "" || call.InputInvalid {
		if issue == "" {
			issue = "arguments were recorded as invalid JSON"
		}
		return settle(fmt.Sprintf("Tool %q was not executed: invalid JSON arguments: %s. "+
			"Re-issue the call with complete, valid JSON; no operation was performed. "+
			`JSON strings do not escape '$': use '$' directly, or '\\' to encode a literal backslash.`, call.Name, issue))
	}

	tool, ok := a.registry.Get(call.Name)
	if !ok {
		return settle(fmt.Sprintf("Tool %q not found", call.Name))
	}
	rv.tool = tool

	// Validate before the gate, not after. A call with a missing field cannot run
	// whatever the human says, and asking someone to approve it wastes the one
	// thing the gate is spending: their attention.
	if err := tools.ValidateArgs(tool, rv.args); err != nil {
		return settle(fmt.Sprintf("Tool %q was not executed: %s", call.Name, err))
	}

	// A gate that needs to tell the UI a call is awaiting approval does so through
	// its own channel; the loop only consumes the verdict.
	if a.gate != nil {
		d := a.gate.Review(ctx, GateRequest{
			CallID: call.ID, ToolName: call.Name, Args: rv.args, Turn: turn,
		})
		if !d.Allow {
			reason := d.Reason
			if reason == "" {
				reason = "Tool execution was blocked"
			}
			// A refusal is information for the model, not a fatal error. It can
			// explain itself, try another approach, or ask the user. Ending the
			// run instead would make one click cost the whole turn.
			return settle(reason)
		}
		if d.Args != nil {
			rv.args = d.Args
		}
	}
	if _, native := rv.tool.(*tools.ToolResultRead); native {
		// The gate may have rewritten arguments. Normalize and guard only the
		// actual native request, never the model's earlier spelling of it.
		if err := tools.ValidateArgs(rv.tool, rv.args); err != nil {
			return settle(fmt.Sprintf("Tool %q was not executed: %s", call.Name, err))
		}
		if receipt, suppressed := a.repeatedToolReadReceipt(rv.args); suppressed {
			rv.recoverySuppressed = true
			return settle(receipt)
		}
		a.rememberToolReadExecution(rv.call.ID, rv.args)
	}
	return rv
}

// runToolCall executes a reviewed call and reports it, returning its result
// block. It never returns an error: failures are results.
//
// tool_start is emitted here rather than during review so that it carries the
// arguments that actually execute, including any the reviewer rewrote.
// delegatedUsage reports what a tool spent on the model's behalf inside another
// process. Only the subagent tool has any, and it reports it in its details rather
// than in its text: the model reads the answer, the accounting is the harness's
// business.
func delegatedUsage(details any) llm.Usage {
	d, ok := details.(tools.SubagentDetails)
	if !ok {
		return llm.Usage{}
	}
	return llm.Usage{Input: d.InputTok, Output: d.OutTok}
}

func (a *Agent) runToolCall(ctx context.Context, batchIndex int, rv reviewedCall, delegated *llm.Usage, out chan<- Event) llm.Block {
	a.emit(ctx, out, Event{
		Kind: EventToolStart, ToolCallID: rv.call.ID,
		ToolName: rv.call.Name, ToolArgs: string(rv.args),
	})
	started := time.Now()

	output, isError := rv.output, rv.isError
	sourceOutput := output
	// details are for interfaces only and never enter the conversation. A tool may
	// supply them alongside an error, which is how a failed command still reports
	// its exit code to the UI.
	var details any
	var images []llm.Block
	var resultID string

	switch {
	case rv.settled:
	case ctx.Err() != nil:
		// Reviewing the batch may have taken a while — a whole gate timeout, in the
		// worst case. Work nobody is waiting for should not be started.
		output, isError = "Operation aborted", true
	default:
		res, err := a.execute(ctx, rv, out)
		a.officeFinish.note(res.OfficeCompletion)
		sourceOutput = res.Text
		if res.Payload != nil {
			sourceOutput = res.Payload.Text
		}
		if sourceOutput == "" && err != nil {
			sourceOutput = err.Error()
		}
		res = a.registry.PrepareResult(ctx, rv.call.Name, res, err)
		resultID = res.ResultID
		details = res.Details
		for _, im := range res.Images {
			if im.Type == llm.BlockImage && im.ImageRef != "" {
				// Keep only reference metadata supplied by the trusted tool.
				images = append(images, llm.Block{Type: llm.BlockImage, ImageRef: im.ImageRef,
					ImageMime: im.ImageMime, ImageName: im.ImageName, ImageAlt: im.ImageAlt,
					ImageW: im.ImageW, ImageH: im.ImageH})
			}
		}
		// A successful mcp_describe detail is a layered-discovery selection.
		// Recorded here, applied once the batch settles; the typed checks
		// inside decide whether it counts. Settled (blocked/invalid) calls
		// never reach this branch.
		a.noteDescribeDetail(batchIndex, rv.tool, details, err)
		// Recorded even when the call failed: a subagent that crashed after four
		// turns still spent those tokens, and a budget that ignored them would be
		// wrong in the direction that costs money.
		if delegated != nil {
			*delegated = delegatedUsage(details)
		}
		// A tool's bounded observation and its failure status are separate.
		// Replacing Text with err.Error here used to resurrect oversized MCP
		// errors and discard both additional content blocks and spill paths.
		output, isError = res.Text, err != nil
	}

	// The stamp covers every completed call, errors included — a failed command
	// is evidence too (its exit code is the fact), and a refusal is the answer
	// to "did that run". Process state and account bookmark metadata are
	// exempt; see SourceStore.Next.
	var src *Source
	if a.sources != nil && !rv.recoverySuppressed {
		if sourceOutput == "" {
			sourceOutput = output
		}
		// Source extraction uses the actual observation. A preview footer or
		// shape/sample envelope must not corrupt Information JSON citation
		// extraction or change evidence into a management notice.
		sourceDetails := details
		if rv.call.Name == tools.ToolResultReadName {
			if _, nativeReader := rv.tool.(*tools.ToolResultRead); !nativeReader {
				// Only the protected native reader can supply recovery provenance.
				sourceDetails = nil
			}
		}
		src = a.sources.Next(rv.call.Name, sourceDetails, rv.call.ID, sourceOutput, rv.args)
		a.sources.bindResult(resultID, src)
		a.noteResultCatalogSource(resultID, src)
	}
	// The header goes only into the copy the model reads: the event keeps the
	// raw output, so a tool card does not gain a metadata line, and an empty
	// output is registered but gets no header — there is nothing to quote.
	blockText := output
	if src != nil && output != "" {
		blockText = fmt.Sprintf("[cite:%s | %s | %s]\n%s",
			src.Label, rv.call.Name, citeSummary(details, src), output)
	}

	a.emit(ctx, out, Event{
		Kind: EventToolEnd, ToolCallID: rv.call.ID, ToolName: rv.call.Name,
		ToolOutput: output, ToolDetails: details, IsError: isError, Source: src,
	})
	return llm.Block{
		Type:      llm.BlockToolResult,
		ToolUseID: rv.call.ID,
		ResultID:  resultID,
		Text:      blockText,
		IsError:   isError,
		Images:    images,
		Details:   marshalDetails(details),
		Source:    marshalSource(src),
		// Display-only, like the thinking duration: the work log and the subagent
		// card read it to say how long the call took.
		DurationMS: time.Since(started).Milliseconds(),
	}
}

// execute runs the tool, forwarding output as it appears when the tool can
// produce it incrementally.
//
// The type assertion is the whole mechanism: a tool that does not implement
// StreamingTool is called exactly as before, so adding this changed nothing for
// six of the seven built-ins.
//
// Fragments go through emit, which means they can be dropped if the consumer has
// stopped reading — the right trade for output that is only ever a preview. The
// settled output rides on tool_end, so a consumer that missed every fragment
// still ends up correct.
func (a *Agent) execute(ctx context.Context, rv reviewedCall, out chan<- Event) (tools.Result, error) {
	// Stamp this call's provider-side id onto the observation metadata, so an
	// MCP tool wrapper down the line can name which llm tool call it served.
	// WithMetadata derives a fresh context per call — the shared run context is
	// never mutated, which is what makes concurrent batch calls safe.
	if md := mcpobserve.MetadataFrom(ctx); md.ToolCallID != rv.call.ID {
		md.ToolCallID = rv.call.ID
		ctx = mcpobserve.WithMetadata(ctx, md)
	}
	st, ok := rv.tool.(tools.StreamingTool)
	if !ok {
		return rv.tool.Execute(ctx, rv.args)
	}
	return st.ExecuteStreaming(ctx, rv.args, func(p tools.Partial) {
		a.emit(ctx, out, Event{
			Kind: EventToolPartial, ToolCallID: rv.call.ID,
			ToolName: rv.call.Name, Text: p.Text, ToolFrame: p.Frame,
		})
	})
}

// marshalDetails renders a tool's structured payload for the transcript.
//
// Details ride in the message so that they survive to the session file: a diff
// and an exit code are the parts of a transcript worth looking at afterwards, and
// before this they existed only in the live event stream. They still never reach
// the model — see llm.Block.Details.
//
// A payload that will not marshal is dropped rather than reported. It is display
// data; losing a rendering is not worth failing a tool call that already
// succeeded.
func marshalDetails(details any) json.RawMessage {
	if details == nil {
		return nil
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return nil
	}
	return raw
}

// marshalSource renders a citation stamp for the transcript, so a reloaded
// session can still resolve the labels its answers cite. Same rule as
// marshalDetails: a stamp that will not marshal is dropped rather than
// reported — it is display data, and the tool call already succeeded.
func marshalSource(src *Source) json.RawMessage {
	if src == nil {
		return nil
	}
	raw, err := json.Marshal(src)
	if err != nil {
		return nil
	}
	return raw
}

// emitFinal delivers the terminating event with a plain blocking send.
//
// It must not go through emit: once ctx is cancelled, emit's second select is a
// coin flip between delivering and dropping, and dropping this particular event
// means the consumer never learns the run ended. A UI would sit there showing a
// turn in progress forever after a single cancel. Blocking is safe because the
// contract is that callers drain the channel, and this is the last event on it.
func (a *Agent) emitFinal(out chan<- Event, e Event) { out <- e }

// emit keeps transcript events lossless, including after cancellation. A slow
// persister must receive the same prefix as a.messages or its count-based final
// reconciliation can omit or duplicate messages. Callers already must drain the
// channel through emitFinal; cancellation stops work, not transcript delivery.
// Display-only events may be dropped after cancellation. Try a ready consumer
// first so draining callers need not lose those events to ctx.Done either.
func (a *Agent) emit(ctx context.Context, out chan<- Event, e Event) {
	switch e.Kind {
	case EventMessage, EventToolResults, EventSteer, EventAnswerReview:
		out <- e
		return
	}
	select {
	case out <- e:
		return
	default:
	}
	select {
	case out <- e:
	case <-ctx.Done():
	}
}

// hashToolResults computes a hash of one turn's tool calls and their results
// for stagnation detection. It hashes tool names, arguments, and outcomes —
// the shape of "the same action produced the same result" — and deliberately
// not the tool_use id, which is fresh on every call and would make two
// identical turns hash differently. That was the bug that kept this check from
// ever firing against a real provider: the id was the first thing hashed, and
// only the fixed ids of test doubles ever produced two equal hashes.
func (a *Agent) hashToolResults(calls, results []llm.Block) string {
	byID := make(map[string]llm.Block, len(calls))
	for _, c := range calls {
		byID[c.ID] = c
	}
	h := sha256.New()
	for _, r := range results {
		// A result with no matching call hashes as empty name and args — still
		// stable, which is all the detector asks. The citation header is stripped
		// for the same reason the tool_use id is not hashed: the label is fresh
		// on every call, and hashing it would make two identical turns differ.
		c := byID[r.ToolUseID]
		fmt.Fprintf(h, "%s|%s|%s|%v|", c.Name, c.Input, StripCiteHeader(r.Text), r.IsError)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// detectStagnation checks if the agent is stuck in a loop by looking at
// recent tool result hashes. It returns true if the same result pattern
// repeats threshold times consecutively.
func (a *Agent) detectStagnation(history []string) (stagnant bool, reason string) {
	if len(history) < a.stagnationThreshold {
		return false, ""
	}

	// Check if the last N results are identical
	lastHash := history[len(history)-1]
	consecutiveCount := 0

	// Count backwards from the most recent result
	for i := len(history) - 1; i >= 0; i-- {
		if history[i] == lastHash {
			consecutiveCount++
			if consecutiveCount >= a.stagnationThreshold {
				return true, fmt.Sprintf("%d identical tool results in a row", consecutiveCount)
			}
		} else {
			// Pattern broken
			break
		}
	}

	return false, ""
}

// checkBudgets verifies that token, cost, and time budgets haven't been exceeded.
// It returns the EndReason to stop with and a detail string for the message, or a
// zero EndReason when every budget still has room.
//
// The reason is returned rather than derived from the detail text because the
// caller's only other option is matching prose, and which budget was hit changes
// what a driver should do about it: a token budget is refilled by spending more, a
// time budget by waiting.
func (a *Agent) checkBudgets() (EndReason, string) {
	// Input + Output, and deliberately not + Reasoning.
	//
	// Reasoning is a subset of Output (llm.Usage says so), so adding it charged
	// thinking tokens twice. That was the bug this line used to have, and it was the
	// wrong direction in a way worth naming: a thinking-heavy run hit its ceiling
	// early, so the budget looked like it was working while actually stopping runs
	// that had room left. CacheRead is a subset of Input and was already, correctly,
	// left out — so the pattern was known here and applied to only one of the two.
	if a.tokenBudget > 0 {
		totalTokens := a.usage.Input + a.usage.Output
		if totalTokens >= a.tokenBudget {
			return EndTokenBudget, fmt.Sprintf("token budget exceeded: %d/%d", totalTokens, a.tokenBudget)
		}
	}

	// The nil check is load-bearing in the plain sense — Cost is a method on a value
	// and a.price is a pointer, so dropping it panics — and it is worth saying that
	// this is the *only* thing it does. Substituting a zero Price for nil would not
	// stop a run either: a zero rate makes every cost zero, and zero never exceeds a
	// positive budget. The two are indistinguishable from inside this function.
	//
	// So the guarantee that an unpriced run cannot proceed uncapped does not live
	// here. It lives in checkCostBudget, which refuses to start at all. This function
	// only declines to invent a number, which is the most it can do once a run is
	// already going.
	if a.costBudget > 0 && a.price != nil {
		spent := a.price.Cost(a.usage)
		if spent >= a.costBudget {
			// %g rather than a fixed number of decimal places: the unit is whatever the
			// user's prices were written in (llm.Price declares no currency), so pogo
			// has no basis for deciding that two digits after the point is the right
			// precision, and a rate quoted in fractions of a cent needs more.
			return EndCostBudget, fmt.Sprintf("cost budget exceeded: %g/%g", spent, a.costBudget)
		}
	}

	// Check time budget
	if a.timeBudget > 0 {
		elapsed := time.Since(a.runStartTime)
		if elapsed >= a.timeBudget {
			return EndTimeBudget, fmt.Sprintf("time budget exceeded: %v/%v", elapsed, a.timeBudget)
		}
	}

	return "", ""
}
