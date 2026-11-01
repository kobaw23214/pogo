package agent

import (
	"errors"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
	"strings"
	"testing"
)

func TestReplaceSkillContextCommitFailurePreservesHistoryAndRegistry(t *testing.T) {
	registry := tools.New(tools.Options{Cwd: t.TempDir(), Todo: true})
	a := New(Config{Registry: registry, SystemPrompt: "old prompt", Citations: true})
	msgs := []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockText, Text: "history"}}}}
	a.SetMessages(msgs)
	sources := a.sources
	overhead := a.OverheadTokens()
	if err := a.ReplaceSkillContext("new prompt", tools.SkillContextUpdate{}, func() error { return errors.New("write failed") }); err == nil {
		t.Fatal("failed persistence accepted")
	}
	if a.registry != registry || a.systemPrompt != "old prompt" || a.sources != sources || a.Messages()[0].Content[0].Text != "history" || a.OverheadTokens() != overhead {
		t.Fatal("failed persistence partially changed agent")
	}
	if err := a.ReplaceSkillContext(strings.Repeat("new prompt ", 20), tools.SkillContextUpdate{}, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if a.sources != sources || len(a.Messages()) != 1 || a.Messages()[0].Content[0].Text != "history" {
		t.Fatal("skill context changed history or citations")
	}
	if a.OverheadTokens() <= overhead {
		t.Fatal("fixed overhead reading stayed stale")
	}
}
