package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolImagesFollowEntireToolBatch(t *testing.T) {
	history := []Message{
		{Role: RoleAssistant, Content: []Block{{Type: BlockToolUse, ID: "a", Name: "browser", Input: json.RawMessage(`{}`)}, {Type: BlockToolUse, ID: "b", Name: "read", Input: json.RawMessage(`{}`)}}},
		{Role: RoleUser, Content: []Block{
			{Type: BlockToolResult, ToolUseID: "a", Text: "screenshot", Images: []Block{{Type: BlockImage, ImageRef: "uploads/browser.jpg", DataURL: "data:image/jpeg;base64,eA=="}}},
			{Type: BlockToolResult, ToolUseID: "b", Text: "text result"},
		}},
	}
	wire := toWireMessages("", history)
	if len(wire) != 4 || wire[0].Role != "assistant" || wire[1].Role != "tool" || wire[2].Role != "tool" || wire[3].Role != "user" {
		t.Fatalf("image interrupted required tool results: %+v", wire)
	}
	if wire[1].ToolCallID != "a" || wire[2].ToolCallID != "b" || len(wire[3].Parts) != 2 || wire[3].Parts[1].ImageURL == nil {
		t.Fatalf("missing result pairing/image: %+v", wire)
	}
	if !strings.Contains(wire[3].Parts[0].Text, "tool_call a") {
		t.Fatal("observation lost source call")
	}
	data, err := json.Marshal(history)
	if err != nil || strings.Contains(string(data), "base64") || !strings.Contains(string(data), "uploads/browser.jpg") {
		t.Fatalf("transcript must persist only image references: %s %v", data, err)
	}
}

func TestToolImagesTextModelFallback(t *testing.T) {
	m := Message{Role: RoleUser, Content: []Block{{Type: BlockToolResult, ToolUseID: "a", Text: "screenshot", Images: []Block{
		{Type: BlockImage, ImageRef: "uploads/browser.jpg", ImageName: "uploads/browser.jpg", ImageAlt: "Viewport 1280x800"},
	}}}}
	wire := toWireMessages("", []Message{m})
	if len(wire) != 2 || wire[1].Parts != nil || !strings.Contains(wire[1].Content, "Viewport 1280x800") {
		t.Fatalf("text-only model received bad multimodal shape: %+v", wire)
	}
	before := EstimateTokens([]Message{m})
	m.Content[0].Images[0].DataURL = "data:image/jpeg;base64,x"
	if after := EstimateTokens([]Message{m}); after <= before {
		t.Fatal("tool images must contribute to context budget")
	}
}
