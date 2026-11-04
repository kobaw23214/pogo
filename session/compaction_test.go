package session

import (
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
)

func TestCompactionCheckpointPreservesTranscriptAndRebuildsPrompt(t *testing.T) {
	t.Setenv("POGO_SESSION_INDEX", "off")
	s, err := Create(t.TempDir(), "offline", "offline")
	if err != nil {
		t.Fatal(err)
	}
	original := []llm.Message{
		llm.UserText("first question"),
		{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "first answer"}}},
		llm.UserText("second question"),
		{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "second answer"}}},
	}
	if err := s.AppendAll(original); err != nil {
		t.Fatal(err)
	}
	replacement := llm.UserText(llm.CompactionContinuationOpening + "\n\n<summary>\nshort summary\n</summary>")
	view, err := s.AppendCompaction(Compaction{Summary: "short summary", Before: len(original), Messages: []llm.Message{replacement}})
	if err != nil {
		t.Fatal(err)
	}
	if view.After != 4 {
		t.Fatalf("checkpoint anchor = %d, want 4", view.After)
	}
	if err := s.AppendAll([]llm.Message{
		llm.UserText("what next"),
		{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "next answer"}}},
	}); err != nil {
		t.Fatal(err)
	}

	visible := s.Messages("")
	if len(visible) != 6 {
		t.Fatalf("visible transcript has %d messages, want all 6", len(visible))
	}
	for _, m := range visible {
		if llm.IsCompactionContinuation(&m) {
			t.Fatal("model continuation leaked into visible transcript")
		}
	}
	prompt := s.PromptTimedMessages("")
	if len(prompt) != 3 || !strings.Contains(prompt[0].Message.Text(), "short summary") || prompt[1].Message.Text() != "what next" || prompt[2].Message.Text() != "next answer" {
		t.Fatalf("unexpected prompt reconstruction: %+v", prompt)
	}

	reopened, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got := len(reopened.Messages("")); got != 6 {
		t.Fatalf("reopened transcript has %d messages, want 6", got)
	}
	if got := len(reopened.PromptTimedMessages("")); got != 3 {
		t.Fatalf("reopened prompt has %d messages, want 3", got)
	}
	views := reopened.Compactions("")
	if len(views) != 1 || views[0].After != 4 || views[0].Summary != "short summary" {
		t.Fatalf("reopened compaction views: %+v", views)
	}

	second := llm.UserText(llm.CompactionContinuationOpening + "\n\n<summary>\nsecond summary\n</summary>")
	if _, err := reopened.AppendCompaction(Compaction{Summary: "second summary", Messages: []llm.Message{second}}); err != nil {
		t.Fatal(err)
	}
	if got := len(reopened.Messages("")); got != 6 {
		t.Fatalf("second checkpoint changed transcript length to %d", got)
	}
	if got := len(reopened.PromptTimedMessages("")); got != 1 || !strings.Contains(reopened.PromptTimedMessages("")[0].Message.Text(), "second summary") {
		t.Fatalf("second checkpoint did not become the prompt baseline: %+v", reopened.PromptTimedMessages(""))
	}
	views = reopened.Compactions("")
	if len(views) != 2 || views[0].After != 4 || views[1].After != 6 {
		t.Fatalf("checkpoint order drifted: %+v", views)
	}
}
