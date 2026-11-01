package web

import (
	"encoding/json"
	"testing"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
)

func TestAnswerReviewSnapshotRetainsRunningStageAndCompleteThinking(t *testing.T) {
	h := NewHub()
	h.Publish(Event{Type: EvRunStart, RunID: "review-run"})
	h.Publish(Event{Type: EvTurnStart, Turn: 3})
	status := &agent.AnswerReviewStatus{Kind: "citation", State: "running", Turn: 3, StartedAt: 1000}
	h.Publish(Event{Type: EvAnswerReview, AnswerReview: status})
	h.Publish(Event{Type: EvThinking, Text: "先核对资料。"})
	h.Publish(Event{Type: EvThinking, Text: "再补充正文引用。"})
	snapshot := h.Snapshot()
	if snapshot.AnswerReview == nil || snapshot.AnswerReview.State != "running" || snapshot.AnswerReview.StartedAt != 1000 || snapshot.Live.Thinking != "先核对资料。再补充正文引用。" {
		t.Fatalf("reconnect lost active review or streamed thinking: %+v", snapshot)
	}
	encoded, err := json.Marshal(Event{Type: EvSnapshot, Snapshot: &snapshot})
	if err != nil {
		t.Fatalf("review snapshot cannot reach the UI: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	public := decoded["snapshot"].(map[string]any)["answer_review"].(map[string]any)
	if public["kind"] != "citation" || public["state"] != "running" {
		t.Fatalf("wrong review wire contract: %+v", public)
	}
	// Publishing a mutable pointer must not let another goroutine silently
	// change a previously committed snapshot without an event.
	status.State = "completed"
	if h.Snapshot().AnswerReview.State != "running" {
		t.Fatal("review status pointer leaked into Hub state")
	}
}

func TestAnswerReviewTerminalStateSurvivesHistoryReload(t *testing.T) {
	for _, state := range []string{"completed", "fallback", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			status := &llm.AnswerReviewStatus{Kind: "citation", State: state, Turn: 3, StartedAt: 1000, FinishedAt: 1300, DurationMS: 300}
			h := NewHub()
			h.Seed([]session.Timed{{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
				{Type: llm.BlockThinking, Text: "完整保留引用整理时的思考。"},
				{Type: llm.BlockText, Text: "原有或已验证的回答。"},
			}, AnswerReview: status}, Time: 1300}})
			snapshot := h.Snapshot()
			if snapshot.AnswerReview == nil || snapshot.AnswerReview.State != state || snapshot.AnswerReview.DurationMS != 300 || len(snapshot.Messages) != 1 {
				t.Fatalf("reloaded review lost its settled status: %+v", snapshot)
			}
			encoded, err := json.Marshal(snapshot)
			if err != nil || !json.Valid(encoded) {
				t.Fatalf("reloaded review snapshot cannot serialize: %v", err)
			}
			h.Publish(Event{Type: EvRunStart, RunID: "next-question"})
			if next := h.Snapshot(); next.AnswerReview != nil && next.AnswerReview.State == "running" {
				t.Fatal("next question resurrected an old review spinner")
			}
		})
	}
}

func TestAnswerReviewRunEndCannotLeaveRunningSpinner(t *testing.T) {
	h := NewHub()
	h.Publish(Event{Type: EvRunStart, RunID: "review-run"})
	h.Publish(Event{Type: EvTurnStart, Turn: 3})
	h.Publish(Event{Type: EvAnswerReview, AnswerReview: &agent.AnswerReviewStatus{Kind: "quote", State: "running", Turn: 3, StartedAt: 1000}})
	h.Publish(Event{Type: EvRunEnd, StopReason: "aborted"})
	if snapshot := h.Snapshot(); snapshot.AnswerReview != nil && snapshot.AnswerReview.State == "running" {
		t.Fatalf("run ended but snapshot still claims review is running: %+v", snapshot.AnswerReview)
	}
}

func TestAnswerReviewAgentEventUsesSharedWireContract(t *testing.T) {
	status := &agent.AnswerReviewStatus{Kind: "citation", State: "running", Turn: 3, StartedAt: 1000}
	event, ok := fromAgentEvent(agent.Event{Kind: agent.EventAnswerReview, AnswerReview: status})
	if !ok || event.Type != EvAnswerReview || event.AnswerReview == nil || event.AnswerReview.State != "running" {
		t.Fatalf("review event was dropped before reaching SSE: %+v ok=%t", event, ok)
	}
	encoded, err := json.Marshal(event)
	if err != nil || !json.Valid(encoded) {
		t.Fatalf("review event cannot serialize: %v", err)
	}
}
