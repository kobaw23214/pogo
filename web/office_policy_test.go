package web

import (
	"encoding/json"
	"testing"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/tools"
)

func TestOfficePolicyUsesHostOperationClassification(t *testing.T) {
	p := NewPolicy()
	read := agent.GateRequest{ToolName: tools.OfficeToolName, Args: json.RawMessage(`{"action":"command","document_session_id":"session_123","operation":"word.read"}`)}
	write := agent.GateRequest{ToolName: tools.OfficeToolName, Args: json.RawMessage(`{"action":"command","document_session_id":"session_123","operation":"history.undo","operation_id":"undo_123"}`)}
	for _, mode := range []Mode{ModeStandard, ModeStrict} {
		p.Set(mode, 0)
		if _, auto := p.Decide(read); !auto {
			t.Fatalf("bounded live read reviewed in %s", mode)
		}
		if _, auto := p.Decide(write); auto != (mode == ModeStandard) {
			t.Fatalf("native history policy did not match confined writes in %s", mode)
		}
	}
	if _, auto := p.Decide(agent.GateRequest{ToolName: tools.OfficeToolName, Args: json.RawMessage(`{"action":"list","script":"x"}`)}); auto {
		t.Fatal("malformed Office arguments skipped strict review")
	}
}
