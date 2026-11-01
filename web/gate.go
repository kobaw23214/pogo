package web

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/yosukeno/pogo/agent"
)

// DefaultGateTimeout bounds how long a call waits for a human.
const DefaultGateTimeout = 5 * time.Minute

// Verdict is a human's answer to one approval request.
type Verdict struct {
	Allow bool
	// Args, when set, replaces what the model asked for: the approve-after-editing
	// path.
	Args json.RawMessage
	// Reason is fed back to the model on a refusal.
	Reason string
	// Remember is "tool" or "command" to grant a session-scoped pass. Ignored when
	// the call matched a danger pattern.
	Remember string
}

// WebGate implements agent.ToolGate against a browser.
//
// Review blocks in the runner's goroutine while the Hub and every SSE connection
// carry on independently. That is the payoff of tying a run's lifetime to the
// session: the tab can be closed, reloaded, or replaced and the pending approval
// is still there, with a deadline the new page can render correctly.
type WebGate struct {
	hub     *Hub
	policy  *Policy
	timeout time.Duration

	// serialMain and serialSub make sure only one card per lane is on screen at a
	// time: one lane for the session's own calls, one for subagents'.
	//
	// The loop now also reviews a parallel batch one call at a time, which is
	// what gives the cards a deterministic order — a mutex alone hands the queue
	// to whichever goroutine reaches it first, so the cards used to appear in an
	// order nobody chose. This stays anyway: it is the guarantee for any caller
	// that is not the loop, and a gate that stacks cards on a reviewer is worse
	// than a redundant lock.
	//
	// Two lanes rather than one because of background subagents: a delegated task
	// outlives the run that dispatched it, so its card can sit unanswered while
	// the main conversation keeps moving. One shared lane would let that card
	// hold the main agent's own approvals behind it for the whole gate timeout —
	// a hang indistinguishable from a stuck model.
	serialMain sync.Mutex
	serialSub  sync.Mutex

	mu      sync.Mutex
	pending map[string]*pendingGate
	epoch   string // prevents a delayed pre-restart decision from targeting a new request
	n       int
}

type pendingGate struct {
	id string
	// All three are buffered so a UI action never blocks on the reviewer
	// goroutine's scheduling.
	decide chan Verdict
	freeze chan struct{}
	thaw   chan struct{}
}

func NewWebGate(hub *Hub, policy *Policy, timeout time.Duration) *WebGate {
	if timeout <= 0 {
		timeout = DefaultGateTimeout
	}
	return &WebGate{
		hub: hub, policy: policy, timeout: timeout,
		pending: make(map[string]*pendingGate),
		epoch:   rand.Text(),
	}
}

// Review implements agent.ToolGate.
func (g *WebGate) Review(ctx context.Context, req agent.GateRequest) agent.GateDecision {
	if rule, auto := g.policy.Decide(req); auto {
		// An automatic pass still gets published: the timeline should be able to
		// show that a call went through under a rule, without a card.
		g.hub.Publish(Event{
			Type: EvGateAuto, CallID: req.CallID, Name: req.ToolName, Rule: rule,
		})
		return agent.Allow
	}

	// A subagent's card queues behind other subagents' cards, never behind the
	// session's own — see the lane fields for why.
	lane := &g.serialMain
	if req.Origin != "" {
		lane = &g.serialSub
	}
	lane.Lock()
	defer lane.Unlock()

	// The run may have been cancelled while this call waited its turn in the
	// queue behind another card.
	if err := ctx.Err(); err != nil {
		return agent.Deny("Run was cancelled")
	}

	p := g.open()
	defer g.close(p.id)
	// A previous request may have held the lane while the user changed mode
	// or remembered a grant. Register first so a concurrent ApproveAll cannot
	// miss this waiter between the policy check and opening its pending slot.
	if rule, auto := g.policy.Decide(req); auto {
		g.hub.Publish(Event{Type: EvGateAuto, CallID: req.CallID, Name: req.ToolName, Rule: rule})
		return agent.Allow
	}

	danger := Danger(req)
	deadline := time.Now().Add(g.timeout)
	g.hub.Publish(Event{
		Type: EvGateRequest, GateID: p.id, CallID: req.CallID, Name: req.ToolName,
		Args: req.Args, Deadline: deadline.UnixMilli(), Danger: danger, Origin: req.Origin,
	})

	// One resettable timer for the whole wait. Deliberately not created inside
	// the loop with a deferred Stop: that accumulates defers until the function
	// returns.
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()

	var (
		frozen    bool
		remaining time.Duration
	)
	for {
		select {
		case v := <-p.decide:
			d := agent.GateDecision{Allow: v.Allow, Reason: v.Reason, Args: v.Args}
			if !v.Allow && d.Reason == "" {
				d.Reason = "The user rejected this call"
			}
			// A danger match cannot be turned into a standing grant: one fast
			// click should not open something for the rest of the session.
			//
			// Neither can a subagent's call. The grant is session-scoped, so
			// remembering one would let a delegated command widen the policy the
			// user's own calls are judged by — the invariant runs the other way,
			// and this is the one place it could be inverted.
			if v.Allow && v.Remember != "" && len(danger) == 0 && req.Origin == "" {
				g.remember(v.Remember, req)
			}
			g.hub.Publish(Event{
				Type: EvGateResolved, GateID: p.id, CallID: req.CallID,
				Allow: v.Allow, Reason: d.Reason, By: "user",
			})
			return d

		case <-p.freeze:
			// The user started editing the arguments. Rewriting a command takes
			// time and must not be judged as not answering.
			if !frozen {
				frozen, remaining = true, max(time.Duration(0), time.Until(deadline))
				timer.Stop()
				paused := true
				g.hub.Publish(Event{Type: EvGateDeadline, GateID: p.id, Deadline: deadline.UnixMilli(), Frozen: &paused, RemainingMS: remaining.Milliseconds()})
			}

		case <-p.thaw:
			if frozen {
				frozen = false
				deadline = time.Now().Add(remaining)
				timer.Reset(remaining)
				paused := false
				g.hub.Publish(Event{
					Type: EvGateDeadline, GateID: p.id, Deadline: deadline.UnixMilli(),
					Frozen: &paused,
				})
			}

		case <-timer.C:
			// Refusing on timeout, never allowing: an unattended gate must fail
			// closed. The refusal is information, so the loop keeps going and the
			// model can say what it wanted and why it could not.
			reason := fmt.Sprintf("The user did not approve this call within %s", g.timeout)
			g.hub.Publish(Event{
				Type: EvGateResolved, GateID: p.id, CallID: req.CallID,
				Allow: false, Reason: reason, By: "timeout",
			})
			return agent.Deny(reason)

		case <-ctx.Done():
			g.hub.Publish(Event{
				Type: EvGateResolved, GateID: p.id, CallID: req.CallID,
				Allow: false, Reason: "Run was cancelled", By: "cancel",
			})
			return agent.Deny("Run was cancelled")
		}
	}
}

func (g *WebGate) remember(scope string, req agent.GateRequest) {
	var state PolicyState
	switch scope {
	case "tool":
		state = g.policy.AllowTool(req.ToolName)
	case "command":
		// The key, not the bare command: the grant covers the call the user actually
		// looked at, workdir included. See grantKeyOf.
		key := grantKeyOf(req.Args)
		if key == "" {
			return
		}
		state = g.policy.AllowToolCommand(req.ToolName, key)
	default:
		return
	}
	g.hub.Publish(Event{Type: EvPolicyChanged, Policy: &state, By: "user"})
}

func (g *WebGate) open() *pendingGate {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	p := &pendingGate{
		id:     fmt.Sprintf("g%s_%d", g.epoch, g.n),
		decide: make(chan Verdict, 1),
		freeze: make(chan struct{}, 1),
		thaw:   make(chan struct{}, 1),
	}
	g.pending[p.id] = p
	return p
}

func (g *WebGate) close(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.pending, id)
}

func (g *WebGate) get(id string) (*pendingGate, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	p, ok := g.pending[id]
	return p, ok
}

// Decide delivers a verdict from the UI.
func (g *WebGate) Decide(id string, v Verdict) error {
	p, ok := g.get(id)
	if !ok {
		return fmt.Errorf("no pending approval %q", id)
	}
	select {
	case p.decide <- v:
		return nil
	default:
		// Already answered by another tab, or resolved by timeout in between.
		return fmt.Errorf("approval %q is already resolved", id)
	}
}

// ApproveAll allows every card currently waiting on a human. Called when the
// session switches into auto mode: the policy's Decide only gates *future*
// calls, so without this the cards already on screen would sit until a click
// or the timeout even though the user just said "stop asking". No Remember —
// the switch is the standing grant; a card resolved this way must not mint
// another one on top.
func (g *WebGate) ApproveAll() {
	g.mu.Lock()
	pending := make([]*pendingGate, 0, len(g.pending))
	for _, p := range g.pending {
		pending = append(pending, p)
	}
	g.mu.Unlock()
	for _, p := range pending {
		select {
		case p.decide <- Verdict{Allow: true}:
		default:
			// Resolved between the snapshot and the send: nothing to do.
		}
	}
}

// Freeze pauses the countdown while the user edits the arguments.
func (g *WebGate) Freeze(id string) error {
	return g.signal(id, func(p *pendingGate) chan struct{} { return p.freeze })
}

// Thaw resumes a paused countdown from where it stopped.
func (g *WebGate) Thaw(id string) error {
	return g.signal(id, func(p *pendingGate) chan struct{} { return p.thaw })
}

func (g *WebGate) signal(id string, pick func(*pendingGate) chan struct{}) error {
	p, ok := g.get(id)
	if !ok {
		return fmt.Errorf("no pending approval %q", id)
	}
	select {
	case pick(p) <- struct{}{}:
	default:
		// A duplicate click is not an error; the reviewer is already in that state.
	}
	return nil
}
