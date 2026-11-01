package web

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/tools"
)

// A browser can keep its last event cursor and auto-mode label across a Docker
// replacement. A rebuilt Hub starts a new sequence and approval policy, so an
// ahead-of-server cursor must never leave that old permission label on screen.
func TestPolicyReconnectAheadCursorReceivesCurrentModeAndGateState(t *testing.T) {
	previous := NewHub()
	previous.Publish(Event{Type: EvPolicyChanged, Policy: &PolicyState{Mode: string(ModeAuto)}})
	for range 8 {
		previous.Publish(Event{Type: EvGateAuto, CallID: "old", Name: tools.AgentWorkflowReadName, Rule: "policy:auto"})
	}
	oldCursor := previous.Seq()

	h := NewHub()
	p := NewPolicy()
	g := NewWebGate(h, p, time.Second)
	h.Publish(Event{Type: EvRunStart, RunID: "fresh-run"})
	backlog, events, unsubscribe := h.Subscribe(oldCursor)
	defer unsubscribe()
	if len(backlog) != 1 || backlog[0].Type != EvSnapshot || backlog[0].Snapshot == nil {
		t.Fatalf("new Hub did not replace stale browser state: %+v", backlog)
	}
	snapshot := backlog[0].Snapshot
	if snapshot.Policy.Mode != string(ModeStandard) || snapshot.Seq != h.Seq() || len(snapshot.Live.PendingGates) != 0 {
		t.Fatalf("reconnected state retained previous permissions or gates: %+v", snapshot)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan agent.GateDecision, 1)
	go func() {
		done <- g.Review(ctx, agent.GateRequest{CallID: "new-edit", ToolName: tools.AgentWorkflowEditName, Args: json.RawMessage(`{"action":"patch"}`)})
	}()
	gate := waitEvent(t, events, EvGateRequest)
	if gate.Name != tools.AgentWorkflowEditName || h.Snapshot().Policy.Mode != string(ModeStandard) {
		t.Fatal("reviewed call and displayed mode disagree")
	}

	// The user can explicitly select auto again. Exercise the same method the
	// session endpoint uses, including releasing a card already awaiting them.
	s := &Session{policy: p, hub: h, gate: g}
	state := s.SetPolicy(ModeAuto, 0)
	if state.Mode != string(ModeAuto) {
		t.Fatal("explicit full access was not applied")
	}
	select {
	case decision := <-done:
		if !decision.Allow {
			t.Fatal("explicit full access did not release the existing tool review")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("full access left an old approval card waiting")
	}
	resolved := waitEvent(t, events, EvGateResolved)
	if !resolved.Allow || resolved.GateID != gate.GateID {
		t.Fatal("the original pending card was not resolved")
	}
	if got := h.Snapshot(); got.Policy.Mode != string(ModeAuto) || len(got.Live.PendingGates) != 0 {
		t.Fatal("full access display or pending cards did not settle")
	}
	current, _, closeCurrent := h.Subscribe(h.Seq())
	defer closeCurrent()
	if len(current) != 0 {
		t.Fatal("an exactly current cursor needlessly received a snapshot")
	}
}

func TestPolicyFullAccessAllowsWorkflowToolsWithoutReviewCards(t *testing.T) {
	for _, name := range []string{tools.AgentWorkflowReadName, tools.AgentWorkflowEditName, tools.AgentWorkflowEnableName, tools.AgentWorkflowRunName} {
		t.Run(name, func(t *testing.T) {
			h := NewHub()
			p := NewPolicy()
			g := NewWebGate(h, p, time.Second)
			s := &Session{policy: p, hub: h, gate: g}
			s.SetPolicy(ModeAuto, 0)
			_, events, unsubscribe := h.Subscribe(h.Seq())
			defer unsubscribe()
			decision := g.Review(context.Background(), agent.GateRequest{CallID: "authorized-tool", ToolName: name})
			if !decision.Allow {
				t.Fatal("full access requested an extra workflow tool approval")
			}
			event := waitEvent(t, events, EvGateAuto)
			if event.Name != name || event.Rule != "policy:auto" || len(h.Snapshot().Live.PendingGates) != 0 {
				t.Fatal("full access should publish its audit rule without creating a card")
			}
		})
	}
}

func TestPolicyStreamEpochRejectsCollidingAndLegacyCursors(t *testing.T) {
	old := NewHub()
	old.Publish(Event{Type: EvPolicyChanged, Policy: &PolicyState{Mode: string(ModeAuto)}})
	oldSnapshot := old.Snapshot()
	for _, extra := range []int{0, 5} {
		t.Run(fmt.Sprintf("new_sequence_extra_%d", extra), func(t *testing.T) {
			h := NewHub()
			for i := int64(0); i < oldSnapshot.Seq+int64(extra); i++ {
				h.Publish(Event{Type: EvGateAuto, CallID: "new", Name: tools.AgentWorkflowReadName, Rule: "policy:standard"})
			}
			current := h.Snapshot()
			if current.StreamInstance == "" || current.StreamInstance == oldSnapshot.StreamInstance {
				t.Fatal("reconstructed Hub reused the old stream identity")
			}
			for _, epoch := range []string{oldSnapshot.StreamInstance, ""} {
				backlog, _, closeStream := h.Subscribe(oldSnapshot.Seq, epoch)
				closeStream()
				if len(backlog) != 1 || backlog[0].Type != EvSnapshot || backlog[0].Snapshot.StreamInstance != current.StreamInstance || backlog[0].Snapshot.Policy.Mode != string(ModeStandard) {
					t.Fatalf("colliding/legacy cursor retained an obsolete permission: %+v", backlog)
				}
			}
			backlog, _, closeStream := h.Subscribe(oldSnapshot.Seq, current.StreamInstance)
			closeStream()
			if len(backlog) != extra {
				t.Fatalf("same-instance replay returned %d events, want %d", len(backlog), extra)
			}
			for _, event := range backlog {
				if event.Type == EvSnapshot || event.Seq <= oldSnapshot.Seq {
					t.Fatal("same-instance replay unnecessarily reset or duplicated state")
				}
			}
		})
	}
}

func TestPolicySessionStreamEnforcesEpochBeforeSendingBacklog(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("unused")))
	sid := h.createSession()
	hub := h.session(sid).Hub()
	previousInstance := NewHub().Snapshot().StreamInstance
	for range 4 {
		hub.Publish(Event{Type: EvGateAuto, CallID: "read", Name: tools.AgentWorkflowReadName, Rule: "policy:standard"})
	}
	current := hub.Snapshot()
	for _, epoch := range []string{previousInstance, ""} {
		cursor := "1"
		if epoch != "" {
			cursor += "&epoch=" + epoch
		}
		stream := h.stream(sid, cursor)
		first := stream.next(t)
		stream.close()
		if first.Type != EvSnapshot || first.Snapshot == nil || first.Snapshot.StreamInstance != current.StreamInstance || first.Snapshot.Policy.Mode != string(ModeStandard) {
			t.Fatalf("HTTP stream exposed a different instance's tail instead of resetting: %+v", first)
		}
	}
	stream := h.stream(sid, "1&epoch="+current.StreamInstance)
	first := stream.next(t)
	stream.close()
	if first.Type != EvGateAuto || first.Seq != 2 {
		t.Fatalf("same-instance HTTP reconnect lost incremental replay: %+v", first)
	}
}
