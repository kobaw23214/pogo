package llm

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestMalformedToolInputJSONRoundTripPreservesOriginalText(t *testing.T) {
	for _, raw := range []string{
		`{"text":"echo \$HOME"}`,
		`{"text":"中文和引用 ${name}","path":"C:\Users\a"}`,
		`{"request_id":"retry-1","nodes":[`,
		`{"text":"abc"} trailing`,
	} {
		t.Run(raw, func(t *testing.T) {
			original := Block{Type: BlockToolUse, ID: "call", Name: "shortcut_manage", Input: json.RawMessage(raw)}
			encoded, err := json.Marshal(original)
			if err != nil {
				t.Fatalf("invalid model arguments broke JSON serialization: %v", err)
			}
			var view struct {
				Input        string `json:"input"`
				InputInvalid bool   `json:"input_invalid"`
				InputError   string `json:"input_error"`
			}
			if err := json.Unmarshal(encoded, &view); err != nil || view.Input != raw || !view.InputInvalid || view.InputError == "" {
				t.Fatalf("UI lost the failed argument text/status: %+v err=%v", view, err)
			}
			if string(original.Input) != raw || original.InputInvalid {
				t.Fatal("serialization mutated executable argument state")
			}
			var restored Block
			if err := json.Unmarshal(encoded, &restored); err != nil || string(restored.Input) != raw || !restored.InputInvalid || ToolInputJSONError(restored.Input) == "" {
				t.Fatalf("reload lost invalid state or repaired the original arguments: %+v err=%v", restored, err)
			}
			again, err := json.Marshal(restored)
			if err != nil || !bytes.Equal(again, encoded) {
				t.Fatalf("repeat export/reload changed stored call: %s err=%v", again, err)
			}
		})
	}
}

func TestLegacyValidToolInputJSONKeepsItsExistingShape(t *testing.T) {
	for _, raw := range []string{"", `{}`, `{"text":"$HOME ${name}","regex":"\\$","nested":{"ordered":[1,"引用",null]}}`, `"ordinary string"`, `null`, `[1,2]`} {
		t.Run(raw, func(t *testing.T) {
			original := Block{Type: BlockToolUse, ID: "old-call", Name: "read", Input: json.RawMessage(raw)}
			encoded, err := json.Marshal(original)
			if err != nil || strings.Contains(string(encoded), "input_invalid") || strings.Contains(string(encoded), "input_error") {
				t.Fatalf("legacy valid input gained failure metadata: %s err=%v", encoded, err)
			}
			var restored Block
			if err := json.Unmarshal(encoded, &restored); err != nil || !bytes.Equal(restored.Input, original.Input) || restored.InputInvalid {
				t.Fatalf("legacy input changed after loading: %s err=%v", restored.Input, err)
			}
		})
	}
}

func TestMalformedToolInputStaysOriginalOnProviderWireAfterReload(t *testing.T) {
	raw := `{"text":"\$HOME"}`
	original := Message{Role: RoleAssistant, Content: []Block{{Type: BlockToolUse, ID: "failed", Name: "shortcut_manage", Input: json.RawMessage(raw)}}}
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var restored Message
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	history := []Message{restored, {Role: RoleUser, Content: []Block{{Type: BlockToolResult, ToolUseID: "failed", IsError: true, Text: "Invalid JSON; re-issue the call"}}}}
	wire := toWireMessages("", history)
	if len(wire) != 2 || len(wire[0].ToolCalls) != 1 || wire[0].ToolCalls[0].Function.Arguments != raw || wire[1].ToolCallID != "failed" {
		t.Fatalf("provider history changed arguments or lost call/result adjacency: %+v", wire)
	}
	request, err := json.Marshal(wire)
	if err != nil || strings.Contains(string(request), "input_invalid") || strings.Contains(string(request), "input_error") {
		t.Fatalf("local failure metadata escaped into a provider request: %s err=%v", request, err)
	}
}
