package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
)

// skillAndNotesTurn is one model turn that reads a registered SKILL.md and an
// ordinary file of the same size in the same batch.
func skillAndNotesTurn(skillPath string) llm.Response {
	return llm.Response{StopReason: llm.StopToolUse, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
		{Type: llm.BlockToolUse, ID: "skill", Name: "read", Input: json.RawMessage(fmt.Sprintf(`{"path":%q}`, skillPath))},
		{Type: llm.BlockToolUse, ID: "notes", Name: "read", Input: json.RawMessage(`{"path":"notes.md"}`)},
	}}}
}

// resultsByID collects tool_result texts from one message list by call ID.
func resultsByID(messages []llm.Message) map[string]string {
	out := map[string]string{}
	for _, message := range messages {
		for _, block := range message.Content {
			if block.Type == llm.BlockToolResult {
				out[block.ToolUseID] = block.Text
			}
		}
	}
	return out
}

// A loaded Skill is a rule set the model follows word for word, so the batch
// budget hands it over whole: a preview of it is a partial rule set the model
// cannot tell from the real one. An ordinary file of the same size in the same
// batch is still bounded, and a context with no room left still cuts the Skill
// short, keeping its recovery handle.
func TestToolBudgetDeliversRegisteredSkillInstructionsWhole(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cfg       ToolOutputBudgetConfig
		skillKept bool
	}{
		{name: "outside the per-result and batch limits", cfg: ToolOutputBudgetConfig{PerResultTokens: 512, BatchTokens: 1024}, skillKept: true},
		{name: "not past a full context", cfg: ToolOutputBudgetConfig{ContextWindow: 2000, MaxOutputTokens: 500}, skillKept: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry, sk, cwd := embeddedSkillRegistry(t)
			body, err := os.ReadFile(sk.Path)
			if err != nil {
				t.Fatal(err)
			}
			notes := strings.Repeat("n", len(body))
			if err := os.WriteFile(filepath.Join(cwd, "notes.md"), []byte(notes), 0o644); err != nil {
				t.Fatal(err)
			}
			client := &fakeClient{responses: []llm.Response{skillAndNotesTurn(sk.Path)}}
			a := New(Config{Client: client, Registry: registry, ToolOutputBudget: tc.cfg})
			events := drain(a.Run(context.Background(), "load the workflow Skill and my notes"))
			if end := events[len(events)-1]; end.Kind != EventAgentEnd || end.Err != nil {
				t.Fatalf("run failed: %+v", end)
			}

			stored, sent := resultsByID(a.Messages()), resultsByID(client.lastSeen())
			if !strings.Contains(stored["skill"], strings.TrimSpace(string(body))[:200]) || len(stored["notes"]) < len(notes) {
				t.Fatalf("fixture did not read both files whole: %d / %d bytes", len(stored["skill"]), len(stored["notes"]))
			}
			if cost := estimatedToolText(stored["skill"]); cost <= 512 {
				t.Fatalf("fixture Skill costs %d tokens, too small to exercise the budget", cost)
			}
			if got := sent["skill"] == stored["skill"]; got != tc.skillKept {
				t.Fatalf("Skill delivered whole = %v, want %v (sent %d of %d bytes)", got, tc.skillKept, len(sent["skill"]), len(stored["skill"]))
			}
			if !tc.skillKept && !strings.Contains(sent["skill"], `"result_id"`) {
				t.Fatalf("a Skill cut short by a full context lost its recovery handle: %.200s", sent["skill"])
			}
			if len(sent["notes"]) >= len(stored["notes"]) {
				t.Fatalf("the ordinary file was not bounded: sent %d of %d bytes", len(sent["notes"]), len(stored["notes"]))
			}
			if tc.skillKept {
				stats := a.toolBudgetStats()
				if stats.LastBatchTarget < estimatedToolText(stored["skill"]) || stats.ReducedResults != 1 {
					t.Fatalf("batch accounting does not include the whole Skill: %+v", stats)
				}
			}
		})
	}
}
