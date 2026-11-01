package web

import (
	"encoding/json"
	"testing"

	"github.com/yosukeno/pogo/agent"
)

// The manifest's readOnlyHint is the bundle author's claim, and an author's
// claim cannot be what grants an exemption (qwen-code#6917 is that design
// failing). What grants it is the sandbox being on — a fact the author cannot
// control. These cases pin the boundary both ways: on, and off.
func TestPolicyDeclaredReadOnlySkipsReviewOnlyUnderSandbox(t *testing.T) {
	args, _ := json.Marshal(map[string]any{"sha256": "ab"})

	// Sandbox on: the hint is honoured, and the rule name records what granted
	// the pass — an automatic exemption must still leave its basis on the
	// timeline, because an audit that cannot say why is not an audit.
	on := NewPolicy()
	on.sandboxActive = func() bool { return true }
	on.MarkReadOnly("mal_lookup")
	rule, auto := on.Decide(agent.GateRequest{ToolName: "mal_lookup", Args: args})
	if !auto || rule != "sandbox:auto-allow:mal_lookup" {
		t.Fatalf("read-only tool under an active sandbox: rule=%q auto=%v", rule, auto)
	}

	// Sandbox off (a degrade deployment, a platform with no implementation):
	// the same call is reviewed like any other — the hint degrades to display
	// text rather than a permanent pass.
	off := NewPolicy()
	off.sandboxActive = func() bool { return false }
	off.MarkReadOnly("mal_lookup")
	if _, auto := off.Decide(agent.GateRequest{ToolName: "mal_lookup", Args: args}); auto {
		t.Fatal("readOnlyHint must not exempt when no sandbox is containing the command")
	}

	// Strict mode never honours it: strict is the operator saying they want
	// eyes on everything, and a bundle cannot talk them out of that.
	strict := NewPolicy()
	strict.sandboxActive = func() bool { return true }
	strict.MarkReadOnly("mal_lookup")
	strict.Set(ModeStrict, 0)
	if _, auto := strict.Decide(agent.GateRequest{ToolName: "mal_lookup", Args: args}); auto {
		t.Fatal("readOnlyHint must not exempt in strict mode")
	}

	// An undeclared tool still asks even with the sandbox on — that is the
	// default that makes the annotation meaningful rather than decorative.
	if _, auto := on.Decide(agent.GateRequest{ToolName: "mal_wipe", Args: args}); auto {
		t.Fatal("undeclared tool must still be reviewed")
	}
}

// Browser calls pass the loop unreviewed — the channel owns the one decision
// that matters (arm or not, asked once per run). This test pins that the loop
// side does not accidentally re-grow a per-action ask, which would charge the
// arm decision twice.
func TestPolicyBrowserPassesUnreviewed(t *testing.T) {
	p := NewPolicy()
	click, _ := json.Marshal(map[string]any{"action": "click", "target": "e1"})

	if _, auto := p.Decide(agent.GateRequest{ToolName: "browser", Args: click}); !auto {
		t.Fatal("browser calls should pass to the channel unreviewed")
	}
}

// bash is the tool the whole approval story exists for; nothing about the
// browser channel widens it.
func TestPolicyBrowserDoesNotWidenBash(t *testing.T) {
	p := NewPolicy()
	args, _ := json.Marshal(map[string]any{"command": "rm -rf /"})
	if _, auto := p.Decide(agent.GateRequest{ToolName: "bash", Args: args}); auto {
		t.Fatal("bash must stay reviewed in standard mode")
	}
}
