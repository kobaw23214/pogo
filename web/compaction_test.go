package web

import (
	"context"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
)

func TestHubPreservesTranscriptAroundCompactionMarkers(t *testing.T) {
	h := NewHub()
	legacy := llm.UserText(llm.CompactionContinuationOpening + "\n\n<summary>\nkept summary\n</summary>")
	h.Seed([]session.Timed{{Message: legacy, Time: 1}, {Message: llm.UserText("next real question"), Time: 2}})

	snap := h.Snapshot()
	if len(snap.Messages) != 1 || textOf(snap.Messages[0]) != "next real question" {
		t.Fatalf("legacy continuation polluted visible messages: %+v", snap.Messages)
	}
	if len(snap.Compactions) != 1 || snap.Compactions[0].Summary != "kept summary" || snap.Compactions[0].After != 0 {
		t.Fatalf("legacy compaction was not projected safely: %+v", snap.Compactions)
	}

	h.Publish(Event{Type: EvCompacted, Compaction: &session.CompactionView{ID: "c1", After: 1, Time: 3, Summary: "new summary", Before: 2, BeforeTokens: 100, AfterTokens: 20}})
	snap = h.Snapshot()
	if len(snap.Messages) != 1 || textOf(snap.Messages[0]) != "next real question" {
		t.Fatalf("compaction cleared visible history: %+v", snap.Messages)
	}
	if len(snap.Compactions) != 2 || snap.Compactions[1].ID != "c1" || snap.Compactions[1].After != 1 {
		t.Fatalf("compaction marker did not append in order: %+v", snap.Compactions)
	}
}

func TestHubCompactionContextTracksEstimateThenMeasurement(t *testing.T) {
	h := NewHub()
	h.Seed([]session.Timed{{Message: llm.UserText("old question")}, {Message: llm.UserText("old answer")}})
	prompt := newContextPrompt([]llm.Message{llm.UserText("summary")}, 100)
	h.Publish(Event{Type: EvCompacted, Compaction: &session.CompactionView{ID: "c1", After: 2}, ContextPrompt: prompt})
	snap := h.Snapshot()
	if !snap.ContextEstimated || snap.ContextTokens != prompt.Composition.Estimated || len(snap.ContextPrompt.Messages) != 1 {
		t.Fatalf("compaction did not publish its model prompt: %+v", snap)
	}
	if len(snap.Messages) != 2 {
		t.Fatalf("context accounting changed visible history: %+v", snap.Messages)
	}
	h.Publish(Event{Type: EvMessage, Usage: &llm.Usage{Input: 700}})
	next := newContextPrompt([]llm.Message{llm.UserText("summary"), llm.UserText("next")}, 100)
	h.Publish(Event{Type: EvRunEnd, ContextPrompt: next})
	snap = h.Snapshot()
	if snap.ContextEstimated || snap.ContextTokens != 700 || len(snap.ContextPrompt.Messages) != 2 {
		t.Fatalf("new prompt replaced the provider measurement: %+v", snap)
	}
}

func TestCompactedPromptSurvivesSessionReload(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("a concise summary")))
	s := h.session(h.createSession())
	seedReliabilityConversation(t, s)
	result, err := s.Compact(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := result.AfterTokens + s.agent.OverheadTokens()
	assertSnapshot := func(snap Snapshot) {
		t.Helper()
		if !snap.ContextEstimated || snap.ContextTokens != want || snap.ContextPrompt == nil || len(snap.ContextPrompt.Messages) != 1 {
			t.Fatalf("unexpected compacted context: %+v", snap)
		}
		if len(snap.Messages) != 2 {
			t.Fatalf("visible history changed during compaction: %+v", snap.Messages)
		}
	}
	assertSnapshot(s.hub.Snapshot())
	h.mgr.evictAll()
	reopened := h.session(s.ID)
	assertSnapshot(reopened.hub.Snapshot())
}
