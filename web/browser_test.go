package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

type browserGateExecutor struct {
	execute func(context.Context, json.RawMessage) (tools.BrowserReply, error)
}

func (browserGateExecutor) BrowserBackend() string { return tools.BrowserBackendNative }
func (b browserGateExecutor) Execute(ctx context.Context, op json.RawMessage) (tools.BrowserReply, error) {
	return b.execute(ctx, op)
}

func TestBrowserGateRequiresArming(t *testing.T) {
	g := NewBrowserGate(nil)
	calls := 0
	g.executor = browserGateExecutor{execute: func(context.Context, json.RawMessage) (tools.BrowserReply, error) {
		calls++
		return tools.BrowserReply{OK: true}, nil
	}}
	if _, err := g.Execute(context.Background(), json.RawMessage(`{"action":"snapshot"}`)); !errors.Is(err, ErrBrowserNotArmed) || calls != 0 {
		t.Fatalf("unarmed execution: %v calls=%d", err, calls)
	}
	g.Arm()
	if reply, err := g.Execute(context.Background(), json.RawMessage(`{"action":"snapshot"}`)); err != nil || !reply.OK || calls != 1 {
		t.Fatalf("armed execution: %+v %v calls=%d", reply, err, calls)
	}
	g.Disarm()
	if g.Armed() {
		t.Fatal("Disarm must close the task grant")
	}
	if _, err := (subPane{g}).Execute(context.Background(), json.RawMessage(`{"action":"snapshot"}`)); !errors.Is(err, ErrBrowserNotArmed) || calls != 1 {
		t.Fatalf("delegated execution escaped grant: %v calls=%d", err, calls)
	}
}
func TestBrowserGateWithoutRuntimeNeverUsesConnectedUI(t *testing.T) {
	for _, connected := range []bool{false, true} {
		h := NewHub()
		var events <-chan Event
		if connected {
			_, ch, cancel := h.Subscribe(0)
			defer cancel()
			events = ch
		}
		g := NewBrowserGate(nil)
		g.Arm()
		if _, err := g.Execute(context.Background(), json.RawMessage(`{"action":"snapshot"}`)); !errors.Is(err, ErrBrowserUnavailable) {
			t.Fatalf("connected=%v: %v", connected, err)
		}
		if _, err := (subPane{g}).Execute(context.Background(), json.RawMessage(`{"action":"snapshot"}`)); !errors.Is(err, ErrBrowserUnavailable) {
			t.Fatalf("child connected=%v: %v", connected, err)
		}
		select {
		case event := <-events:
			t.Fatalf("unavailable runtime emitted UI work: %+v", event)
		default:
		}
	}
}
func TestBrowserGateBoundsNativeCancellationAndDoesNotRetry(t *testing.T) {
	g := NewBrowserGate(nil)
	g.Arm()
	g.timeout = 10 * time.Millisecond
	calls := 0
	g.executor = browserGateExecutor{execute: func(ctx context.Context, _ json.RawMessage) (tools.BrowserReply, error) {
		calls++
		<-ctx.Done()
		return tools.BrowserReply{}, ctx.Err()
	}}
	if _, err := g.Execute(context.Background(), json.RawMessage(`{"action":"click","target":"e1"}`)); !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatalf("timeout or repeated execution: %v calls=%d", err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.ExecuteSub(ctx, json.RawMessage(`{"action":"snapshot"}`)); !errors.Is(err, context.Canceled) || calls != 2 {
		t.Fatalf("cancellation: %v calls=%d", err, calls)
	}
}

func TestBrowserGateMinuteWaitKeepsResponseHeadroomAndParentDeadline(t *testing.T) {
	g := NewBrowserGate(nil)
	g.Arm()
	var remaining time.Duration
	g.executor = browserGateExecutor{execute: func(ctx context.Context, _ json.RawMessage) (tools.BrowserReply, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("native operation must remain bounded")
		}
		remaining = time.Until(deadline)
		return tools.BrowserReply{OK: true}, nil
	}}
	if _, err := g.Execute(context.Background(), json.RawMessage(`{"action":"wait","text":"ready","seconds":60}`)); err != nil {
		t.Fatal(err)
	}
	if remaining < 65*time.Second || remaining > 75*time.Second {
		t.Fatalf("60s worker wait needs response headroom, got %v", remaining)
	}
	parent, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := g.ExecuteSub(parent, json.RawMessage(`{"action":"wait","text":"ready","seconds":60}`)); err != nil {
		t.Fatal(err)
	}
	if remaining <= 0 || remaining > 50*time.Millisecond {
		t.Fatalf("shorter parent deadline was widened: %v", remaining)
	}
}

// The loop passes browser calls unreviewed on both sides of the armed check:
// page operations are decided in the channel, which refuses, asks once, or
// runs. What still asks at the loop level is the arm request itself.
func TestPolicyBrowserLeavesReviewToTheChannel(t *testing.T) {
	p := NewPolicy()
	for _, args := range []string{
		`{"action":"snapshot"}`,
		`{"action":"click","target":"e1"}`,
		`not json`,
	} {
		if _, auto := p.Decide(gateReq("browser", args)); !auto {
			t.Fatalf("browser %s should pass to the channel unreviewed", args)
		}
	}
	if _, auto := p.Decide(gateReq(BrowserArmRequest, `{"action":"click","target":"e1"}`)); auto {
		t.Fatal("an arm request must be reviewed — it is the one decision that opens page operations")
	}
}

// The arm card: an unarmed gate with nobody to ask keeps the structural
// refusal; with a gate, the first call raises one card, approval arms the run
// and the op proceeds, denial refuses with the reason and is remembered.
func TestBrowserGateArmCard(t *testing.T) {
	// No gate to ask: the refusal is structural, as in the channel-only tests.
	g := NewBrowserGate(nil)
	if _, err := g.Execute(context.Background(), json.RawMessage(`{"action":"snapshot"}`)); !errors.Is(err, ErrBrowserNotArmed) {
		t.Fatalf("want ErrBrowserNotArmed without a gate, got %v", err)
	}

	h := NewHub()
	_, ch, cancel := h.Subscribe(0)
	defer cancel()
	gate := NewWebGate(h, NewPolicy(), time.Minute)
	g = NewBrowserGate(gate)
	g.executor = browserGateExecutor{execute: func(context.Context, json.RawMessage) (tools.BrowserReply, error) {
		return tools.BrowserReply{OK: true}, nil
	}}
	click := json.RawMessage(`{"action":"click","target":"e1"}`)

	done := make(chan error, 1)
	go func() {
		_, err := g.Execute(context.Background(), click)
		done <- err
	}()

	// Approval authorizes a native dispatch; no browser SSE request is needed.
	select {
	case e := <-ch:
		if e.Type != EvGateRequest || e.Name != BrowserArmRequest {
			t.Fatalf("unexpected card: %+v", e)
		}
		if err := gate.Decide(e.GateID, Verdict{Allow: true}); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("arm card never arrived")
	}
	if err := <-done; err != nil {
		t.Fatalf("approved arm should run the op, got %v", err)
	}
	if !g.Armed() {
		t.Fatal("approval should arm the channel for the rest of the run")
	}

	// A new run starts disarmed; a denial refuses, carries the reason, and is
	// remembered: the immediate retry gets the same refusal without a new card.
	g.Disarm()
	done = make(chan error, 1)
	go func() {
		_, err := g.Execute(context.Background(), click)
		done <- err
	}()
	for {
		select {
		case e := <-ch:
			if e.Type != EvGateRequest {
				continue // the denial's resolve events may land first
			}
			if err := gate.Decide(e.GateID, Verdict{}); err != nil {
				t.Fatal(err)
			}
			err := <-done
			if !errors.Is(err, ErrBrowserNotArmed) || !strings.Contains(err.Error(), "rejected") {
				t.Fatalf("want a refusal carrying the deny reason, got %v", err)
			}
			goto refused
		case <-time.After(5 * time.Second):
			t.Fatal("denial card never arrived")
		}
	}
refused:

	// Drain anything the denial published, then the retry must refuse without
	// asking again.
	for {
		select {
		case <-ch:
			continue
		default:
		}
		break
	}
	if _, err := g.Execute(context.Background(), click); !errors.Is(err, ErrBrowserNotArmed) {
		t.Fatalf("a remembered denial must refuse immediately, got %v", err)
	}
	select {
	case e := <-ch:
		if e.Type == EvGateRequest {
			t.Fatal("a refused arm must not raise a second card")
		}
	default:
	}
}

func gateReq(tool string, args string) agent.GateRequest {
	return agent.GateRequest{ToolName: tool, Args: json.RawMessage(args)}
}

// A stale or malicious UI cannot complete an operation owned by the worker.
func TestRetiredBrowserResultCannotCompleteNativeOperation(t *testing.T) {
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	sid := h.createSession()
	gate := h.session(sid).Browser()
	started, release := make(chan struct{}), make(chan struct{})
	gate.executor = browserGateExecutor{execute: func(ctx context.Context, _ json.RawMessage) (tools.BrowserReply, error) {
		close(started)
		select {
		case <-release:
			return tools.BrowserReply{OK: true, Title: "worker result"}, nil
		case <-ctx.Done():
			return tools.BrowserReply{}, ctx.Err()
		}
	}}
	gate.Arm()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan tools.BrowserReply, 1)
	go func() { r, _ := gate.Execute(ctx, json.RawMessage(`{"action":"snapshot"}`)); done <- r }()
	<-started
	h.post("/api/sessions/"+sid+"/control", `{"action":"browser_result","browser_op_id":"stale-operation","browser_reply":{"ok":true,"title":"forged UI result"}}`, http.StatusGone)
	select {
	case r := <-done:
		t.Fatalf("UI completed native operation: %+v", r)
	default:
	}
	close(release)
	if r := <-done; !r.OK || r.Title != "worker result" {
		t.Fatalf("worker response lost: %+v", r)
	}
}
