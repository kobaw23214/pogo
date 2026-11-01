package agent

import (
	"errors"
	"slices"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func TestReplaceNativeSelectionCommitFailureAndBusyAreAtomic(t *testing.T) {
	empty := []string{}
	child := &tools.Subagent{}
	r := tools.New(tools.Options{Cwd: t.TempDir(), Enabled: &empty, Todo: true, Subagent: child})
	a := New(Config{Registry: r, SystemPrompt: "keep prompt", Citations: true})
	a.SetMessages([]llm.Message{{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockText, Text: "keep history"}}}})
	beforeSources := a.sources
	want := []string{"bash"}
	if err := a.ReplaceNativeSelection(&want, nil, func() error { return errors.New("storage failed") }); err == nil {
		t.Fatal("failed storage accepted")
	}
	if a.registry != r || child.Enabled != nil || a.sources != beforeSources || a.systemPrompt != "keep prompt" || a.Messages()[0].Content[0].Text != "keep history" {
		t.Fatal("failed commit partially applied")
	}
	a.running = true
	called := false
	if err := a.ReplaceNativeSelection(&want, nil, func() error { called = true; return nil }); err == nil || called {
		t.Fatal("active run configuration changed")
	}
	a.running = false
	if err := a.ReplaceNativeSelection(&want, nil, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(a.NativeToolNames(), "bash") || a.sources != beforeSources || a.systemPrompt != "keep prompt" || a.Messages()[0].Content[0].Text != "keep history" {
		t.Fatal("successful tool sync changed unrelated context")
	}
}

func TestNativeSelectionWithheldBrowserTracksModelVision(t *testing.T) {
	empty := []string{}
	r := tools.New(tools.Options{Cwd: t.TempDir(), Enabled: &empty, Browser: capabilityBrowserLink{}})
	a := New(Config{Registry: r})
	if err := a.SetBrowserModelVision(true); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(a.NativeToolNames(), "browser") {
		t.Fatal("metadata refresh granted browser")
	}
	selected := []string{"browser"}
	if err := a.ReplaceNativeSelection(&selected, nil, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	tool, _ := a.registry.Get("browser")
	if tool.(*tools.Browser).ImageMode != tools.BrowserImageDirect {
		t.Fatal("new browser grant retained an old model's image mode")
	}
}
