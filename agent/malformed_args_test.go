package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

// Tools may customize schema checks, but they must never bypass the host's
// malformed-input fence or make a restored invalid call executable.
type malformedPermissiveTool struct {
	fakeTool
	validated atomic.Int32
}

func (t *malformedPermissiveTool) ValidateArgs(json.RawMessage) error {
	t.validated.Add(1)
	return nil
}

func TestMalformedArgumentsCannotBypassHostFenceViaCustomValidator(t *testing.T) {
	for _, tc := range []struct {
		name    string
		input   string
		invalid bool
	}{
		{"malformed escape", `{"text":"echo \$HOME"}`, false},
		{"restored invalid marker", `{"text":"valid JSON must still remain rejected"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := &malformedPermissiveTool{fakeTool: fakeTool{name: "permissive"}}
			gate := &fakeGate{}
			client := &fakeClient{responses: []llm.Response{{
				Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
					Type: llm.BlockToolUse, ID: "invalid-call", Name: tool.Name(), Input: json.RawMessage(tc.input), InputInvalid: tc.invalid,
				}}}, StopReason: llm.StopToolUse,
			}}}
			a := New(Config{Client: client, Registry: tools.NewRegistry(tool), Gate: gate})
			events := drain(a.Run(context.Background(), "try the invalid call"))
			if tool.validated.Load() != 0 || len(tool.callArgs()) != 0 {
				t.Fatal("custom validator or executor received an invalid call")
			}
			gate.mu.Lock()
			asked := len(gate.seen)
			gate.mu.Unlock()
			if asked != 0 {
				t.Fatal("invalid input reached approval")
			}
			var rejected bool
			for _, event := range events {
				if event.Kind == EventToolEnd && event.ToolCallID == "invalid-call" {
					rejected = event.IsError && strings.Contains(event.ToolOutput, "invalid JSON") && strings.Contains(event.ToolOutput, "Re-issue")
				}
			}
			if !rejected || client.callCount() != 2 {
				t.Fatal("invalid call did not yield a recoverable model-facing error")
			}
		})
	}
}
