package agent

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

type contextWatchBackend struct{}

func (contextWatchBackend) Watch(context.Context, json.RawMessage) (any, error) {
	return nil, nil
}
func (contextWatchBackend) Test(context.Context, json.RawMessage) (any, error) {
	return nil, nil
}
func (contextWatchBackend) Decide(context.Context, json.RawMessage) (any, error) {
	return nil, nil
}

func TestSentinelEventContextUpdatesModelSchemasOnlyBetweenRuns(t *testing.T) {
	selection := []string{tools.WatchName, tools.WatchDecideName}
	registry := tools.New(tools.Options{Cwd: t.TempDir(), Enabled: &selection, Watches: contextWatchBackend{}})
	a := New(Config{Registry: registry, SystemPrompt: "unchanged instructions", Citations: true})
	a.SetMessages([]llm.Message{{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockText, Text: "unchanged history"}}}})
	sources := a.sources
	hasDecision := func() bool {
		for _, schema := range a.schemas {
			if schema.Name == tools.WatchDecideName {
				return true
			}
		}
		return false
	}
	if hasDecision() || slices.Contains(a.NativeToolNames(), tools.WatchDecideName) {
		t.Fatal("ordinary conversation advertised the event decision tool")
	}
	if err := a.SetSentinelEvent(true); err != nil || !hasDecision() || !slices.Contains(a.NativeToolNames(), tools.WatchDecideName) {
		t.Fatalf("genuine event context unavailable: %v", err)
	}
	eventRegistry := a.registry
	a.running = true
	if err := a.SetSentinelEvent(false); err == nil || a.registry != eventRegistry || !hasDecision() {
		t.Fatal("live event registry was modified")
	}
	a.running = false
	if err := a.SetSentinelEvent(false); err != nil || hasDecision() || slices.Contains(a.NativeToolNames(), tools.WatchDecideName) {
		t.Fatalf("finished event leaked into ordinary model schemas: %v", err)
	}
	if _, ok := a.registry.Get(tools.WatchName); !ok || a.sources != sources || a.systemPrompt != "unchanged instructions" || a.Messages()[0].Content[0].Text != "unchanged history" {
		t.Fatal("event context switch changed unrelated grants or conversation state")
	}
}

func TestSentinelEventContextRejectsMissingHostWithoutChangingSchemas(t *testing.T) {
	registry := tools.Default(t.TempDir())
	a := New(Config{Registry: registry})
	if err := a.SetSentinelEvent(true); err == nil || a.registry != registry {
		t.Fatal("missing host context was accepted or changed the registry")
	}
	if err := a.SetSentinelEvent(false); err != nil || a.registry != registry {
		t.Fatal("ordinary context unnecessarily replaced its registry")
	}
}
