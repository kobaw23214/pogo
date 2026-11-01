package web

import (
	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/tools"
	"testing"
	"time"
)

func TestAgentWorkflowReadPolicyAndMutationSeparation(t *testing.T) {
	for _, mode := range []Mode{ModeStandard, ModeStrict} {
		p := NewPolicy()
		p.Set(mode, 0)
		if _, allow := p.Decide(agent.GateRequest{ToolName: tools.AgentWorkflowReadName}); !allow {
			t.Fatalf("private scoped reads unnecessarily require approval in %s", mode)
		}
		for _, tool := range []string{tools.AgentWorkflowEditName, tools.AgentWorkflowEnableName, tools.AgentWorkflowRunName} {
			if _, allow := p.Decide(agent.GateRequest{ToolName: tool}); allow {
				t.Fatalf("mutation %s bypassed %s review", tool, mode)
			}
		}
	}
}

// A source conversation and an execution conversation are both reconstructed
// after restart. A delayed old click must never decide a new tool request.
func TestAgentWorkflowRestartDoesNotReuseToolApprovalIdentity(t *testing.T) {
	before := NewWebGate(nil, NewPolicy(), time.Second)
	old := before.open()
	before.close(old.id)
	after := NewWebGate(nil, NewPolicy(), time.Second)
	pending := after.open()
	defer after.close(pending.id)
	if old.id == pending.id {
		t.Fatal("tool approval identity was reused across reconstructed sessions")
	}
	if err := after.Decide(old.id, Verdict{Allow: true}); err == nil {
		t.Fatal("stale pre-restart decision approved a new request")
	}
	select {
	case <-pending.decide:
		t.Fatal("new request was decided by stale identity")
	default:
	}
	if err := after.Decide(pending.id, Verdict{Allow: true}); err != nil {
		t.Fatal(err)
	}
}
