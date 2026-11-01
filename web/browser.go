package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/tools"
)

// DefaultBrowserOpTimeout bounds a native worker operation, including slow
// navigation and the tool's bounded waits. The worker has a 60-second action
// budget; leave time for its final observation and response before cancelling.
const DefaultBrowserOpTimeout = time.Duration(tools.MaxBrowserWaitSeconds)*time.Second + 15*time.Second

// ErrBrowserNotArmed is the intent gate's teeth. The browser tool is
// registered in every web session, but page operations only happen inside an
// armed task: the user starts one with /skill:browser-use, or approves the arm
// card the first unarmed call raises. A refusal — the user declined the card,
// a subagent asked outside a task, a session with no gate to ask — is the same
// answer: this run does not touch the page. A prompt-level suggestion cannot
// provide this — the model could still call the tool — so the refusal lives in
// the channel, where the model cannot argue with it.
var ErrBrowserNotArmed = errors.New(
	"page operations are not enabled for this run; they start when the user sends " +
		"/skill:browser-use <task> or approves the request the first call raises — " +
		"answer without touching the page")

// ErrBrowserUnavailable means no native runtime was configured for this session.
// A connected web client never becomes an alternative browser executor.
var ErrBrowserUnavailable = errors.New("native browser is unavailable; configure the browser runtime service")

// BrowserArmRequest is the synthetic tool name an agent-initiated arm request
// is reviewed under. It is not a real tool; the name exists so the request
// rides the existing gate card (verdict routing, deadline, fail-closed
// timeout, audit events) and lands on the ask side of the policy's mode
// matrix by falling through to its default, the way an undeclared tool does.
const BrowserArmRequest = "browser-arm"

// BrowserGate owns the task grant and dispatches to the session's native worker.
// Authorization remains separate from browser availability and image capability.
type BrowserGate struct {
	timeout time.Duration
	// gate is where an agent-initiated arm request is asked. Nil in tests that
	// only exercise the channel: without a gate there is nobody to ask, and
	// Execute keeps the structural refusal.
	gate *WebGate
	// executor is fixed at session construction. No worker means unavailable.
	executor tools.BrowserLink
	// bookmarks reads account metadata bound to the session's trusted owner.
	// It never grants page control or starts the native worker.
	bookmarks func(context.Context, string, int, int) (tools.BrowserReply, error)

	mu sync.Mutex
	// armed is the task grant: opened by the user's /skill:browser-use or by an
	// approved arm card, closed with the run that used it.
	armed bool
	// armRefused remembers a denied arm request for the rest of the run, so a
	// model that calls the browser again gets the refusal immediately instead
	// of another card — one ask per decision, the same discipline the per-action
	// cards follow.
	armRefused bool
}

func NewBrowserGate(gate *WebGate) *BrowserGate {
	return &BrowserGate{gate: gate, timeout: DefaultBrowserOpTimeout}
}

// The executor is fixed at session construction. This metadata describes
// available operations; it never grants authorization to execute them.
func (g *BrowserGate) BrowserBackend() string {
	if g.executor != nil {
		return tools.BrowserBackendOf(g.executor)
	}
	return ""
}

// Arm enables page operations for the current session state. Called when a
// message expands a browser-task skill; Disarm runs at the end of the run that
// message started, so the grant is per task, never a standing permission.
//
// Arming is also what lets page actions skip the per-action approval card: the
// user's /skill:browser-use <task> is the approval, and re-asking twenty times
// inside one task trains the click-through reflex the bash card depends on not
// existing.
func (g *BrowserGate) Arm() {
	g.mu.Lock()
	g.armed = true
	g.armRefused = false
	g.mu.Unlock()
}

// Disarm closes the task gate. Idempotent, and safe to call speculatively at
// every run end — an unarmed gate disarming is nothing. The refusal memory
// goes with it: a new run starts with a clean ask.
func (g *BrowserGate) Disarm() {
	g.mu.Lock()
	g.armed = false
	g.armRefused = false
	g.mu.Unlock()
}

func (g *BrowserGate) Armed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.armed
}

// Execute implements tools.BrowserLink through the native worker.
// The main conversation's entry point — an unarmed gate asks before refusing.
func (g *BrowserGate) Execute(ctx context.Context, op json.RawMessage) (tools.BrowserReply, error) {
	if reply, handled, err := g.readDownloads(ctx, op); handled {
		return reply, err
	}
	if reply, handled, err := g.readBookmarks(ctx, op); handled {
		return reply, err
	}
	if !g.Armed() {
		g.mu.Lock()
		refused := g.armRefused
		g.mu.Unlock()
		if refused {
			return tools.BrowserReply{}, ErrBrowserNotArmed
		}
		if ok, reason := g.askArm(ctx, op); !ok {
			err := ErrBrowserNotArmed
			if reason != "" {
				err = fmt.Errorf("%w — %s", err, reason)
			}
			return tools.BrowserReply{}, err
		}
	}
	return g.execute(ctx, op)
}

// ExecuteSub is the browser loaned to a subagent: the armed check applies
// unchanged, but an unarmed gate refuses instead of asking. A delegated run
// must not be the one to open page operations — the parent asked for the
// delegation, not for the browser; if the task needs the page, the main
// conversation raises the card.
func (g *BrowserGate) ExecuteSub(ctx context.Context, op json.RawMessage) (tools.BrowserReply, error) {
	if reply, handled, err := g.readDownloads(ctx, op); handled {
		return reply, err
	}
	if reply, handled, err := g.readBookmarks(ctx, op); handled {
		return reply, err
	}
	if !g.Armed() {
		return tools.BrowserReply{}, ErrBrowserNotArmed
	}
	return g.execute(ctx, op)
}

func (g *BrowserGate) readBookmarks(ctx context.Context, op json.RawMessage) (tools.BrowserReply, bool, error) {
	if tools.BrowserActionOf(op) != tools.BrowserActionBookmarks {
		return tools.BrowserReply{}, false, nil
	}
	if err := ctx.Err(); err != nil {
		return tools.BrowserReply{}, true, err
	}
	// Direct FD callers must satisfy the same closed schema as model calls.
	// An action name alone cannot bypass the page authorization boundary.
	browser := tools.Browser{Link: g}
	query, err := browser.ParseBookmarkQuery(op)
	if err != nil {
		return tools.BrowserReply{}, true, err
	}
	if g.bookmarks == nil {
		return tools.BrowserReply{}, true, ErrBrowserUnavailable
	}
	reply, err := g.bookmarks(ctx, query.Query, query.Limit, query.Offset)
	return reply, true, err
}

func (g *BrowserGate) readDownloads(ctx context.Context, op json.RawMessage) (tools.BrowserReply, bool, error) {
	if tools.BrowserActionOf(op) != tools.BrowserActionDownloads {
		return tools.BrowserReply{}, false, nil
	}
	if err := ctx.Err(); err != nil {
		return tools.BrowserReply{}, true, err
	}
	// This reads only the executor's existing conversation download store. It
	// cannot create a page, navigate or grant control. FD callers receive the
	// same strict standalone schema before skipping the page authorization card.
	if err := (&tools.Browser{Link: g}).ValidateArgs(op); err != nil {
		return tools.BrowserReply{}, true, err
	}
	reply, err := g.execute(ctx, op)
	return reply, true, err
}

// askArm raises the approval card that turns an ordinary run into a browser
// task. It reports whether the channel is armed on return, and on denial the
// reason the card closed — the difference between "the user declined" and
// "nobody answered" is information the model can act on. A denial is
// remembered for the rest of the run so the answer stands without re-asking.
//
// The request carries the first operation as its arguments: that is the most
// honest preview of what the agent wants the browser for, and it is exactly what
// would run the moment the card is approved.
func (g *BrowserGate) askArm(ctx context.Context, op json.RawMessage) (bool, string) {
	if g.gate == nil {
		return false, ""
	}
	d := g.gate.Review(ctx, agent.GateRequest{ToolName: BrowserArmRequest, Args: op})
	if !d.Allow {
		g.mu.Lock()
		g.armRefused = true
		g.mu.Unlock()
		return false, d.Reason
	}
	g.Arm()
	return true, ""
}

// execute dispatches after the page grant or a validated metadata-only read.
// It does not depend on an open UI or SSE subscription and never retries an
// uncertain operation.
func (g *BrowserGate) execute(ctx context.Context, op json.RawMessage) (tools.BrowserReply, error) {
	if g.executor == nil {
		return tools.BrowserReply{}, ErrBrowserUnavailable
	}
	bounded, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	return g.executor.Execute(bounded, op)
}

// subPane is the BrowserLink a subagent is handed: the same gate object, so
// the armed check and the run-scoped disarm apply unchanged, but arming is
// main-session-only — see ExecuteSub.
type subPane struct{ g *BrowserGate }

func (w subPane) BrowserBackend() string { return w.g.BrowserBackend() }

func (w subPane) Execute(ctx context.Context, op json.RawMessage) (tools.BrowserReply, error) {
	return w.g.ExecuteSub(ctx, op)
}
