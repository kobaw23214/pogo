package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAnswerReviewMetadataRoundTripsLocallyAndStaysOffProviderWire(t *testing.T) {
	message := Message{Role: RoleAssistant, Content: []Block{
		{Type: BlockThinking, Text: "provider reasoning received during review"},
		{Type: BlockText, Text: "Original supported answer"},
	}, AnswerReview: &AnswerReviewStatus{Kind: "citation", State: "fallback", Turn: 3, StartedAt: 1000, FinishedAt: 31000, DurationMS: 30000, Reason: "timeout"}}
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var restored Message
	if err := json.Unmarshal(raw, &restored); err != nil || restored.AnswerReview == nil || restored.AnswerReview.Reason != "timeout" || restored.Content[0].Text != message.Content[0].Text {
		t.Fatalf("review progress/reasoning did not survive transcript reload: %+v err=%v", restored, err)
	}
	wire, err := json.Marshal(toWireMessages("", []Message{restored}))
	if err != nil || strings.Contains(string(wire), "answer_review") || strings.Contains(string(wire), "duration_ms") || strings.Contains(string(wire), "provider reasoning") {
		t.Fatalf("host-only review state escaped into a model request: %s err=%v", wire, err)
	}
	legacy, err := json.Marshal(UserText("original legacy input"))
	if err != nil || strings.Contains(string(legacy), "answer_review") {
		t.Fatalf("old messages gained synthetic review state: %s err=%v", legacy, err)
	}
}
