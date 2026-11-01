package agent

import (
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func TestBackgroundHandoffGuidanceFollowsActualToolView(t *testing.T) {
	for _, tool := range []tools.Tool{&tools.Watch{}, &tools.BackgroundTask{}} {
		t.Run(tool.Name(), func(t *testing.T) {
			a := New(Config{Registry: tools.NewRegistry(tool), SystemPrompt: "base"})
			p := a.modelSystemPrompt()
			for _, want := range []string{"background-task setup", "registration", "expiry", "Do not sleep", "Continue other explicit foreground work", "never proof"} {
				if !strings.Contains(p, want) {
					t.Fatalf("handoff prompt missing %q", want)
				}
			}
			if a.systemPrompt != "base" {
				t.Fatal("conditional guide rewrote the saved system prompt")
			}
			if a.systemPromptTokens != int64(len(p)/llm.BytesPerToken) {
				t.Fatal("conditional guidance was missing from fixed-cost accounting")
			}
			a.schemas = nil
			if a.modelSystemPrompt() != "base" {
				t.Fatal("withheld tools left stale task guidance")
			}
		})
	}
	a := New(Config{Registry: tools.NewRegistry(&tools.Todo{}), SystemPrompt: "base"})
	if a.modelSystemPrompt() != "base" {
		t.Fatal("a child without task tools received their instructions")
	}
}

func TestTodoScopesBackgroundSetupWithoutInventingFutureCompletion(t *testing.T) {
	description := (&tools.Todo{}).Description()
	for _, want := range []string{"current conversation", "registration and handoff", "do not add foreground", "does not claim"} {
		if !strings.Contains(description, want) {
			t.Fatalf("todo background-scope contract missing %q", want)
		}
	}
	a := New(Config{Registry: tools.NewRegistry(&tools.Todo{})})
	a.messages = []llm.Message{todoTurn(`[{"task":"wait for backend expiry","status":"in_progress"}]`).Message}
	notice, ok := a.todoFinishNotice()
	if !ok || !strings.Contains(notice, "do not wait out its lifetime") || !strings.Contains(notice, "do not claim future business results") {
		t.Fatal("unfinished-todo reminder encouraged waiting for background completion")
	}
}
