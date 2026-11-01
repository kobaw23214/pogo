package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func TestResultMetadataDoesNotChangeCitationsAfterReopen(t *testing.T) {
	f := newCoverageFixture(t, "original evidence")
	binding, ok := f.a.sources.resultOrigin(f.id)
	if !ok || binding.source == nil {
		t.Fatal("fixture missing original source")
	}
	origin := binding.source
	history := []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, ID: origin.CallID, Name: "read", Input: json.RawMessage(`{"path":"observed.log"}`)}}},
		{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: origin.CallID, ResultID: f.id, Text: "original evidence", Source: marshalSource(origin)}}},
	}
	for _, view := range []string{"list", "retain", "release"} {
		args := map[string]any{"view": view}
		if view != "list" {
			args["result_id"] = f.id
		}
		raw, _ := json.Marshal(args)
		res, err := f.reader.Execute(context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		if source := f.a.sources.Next(tools.ToolResultReadName, res.Details, view, res.Text, raw); source != nil {
			t.Fatal("host metadata gained a document citation")
		}
		history = append(history,
			llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, ID: view, Name: tools.ToolResultReadName, Input: raw}}},
			llm.Message{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: view, Text: res.Text, Details: marshalDetails(res.Details)}}},
		)
	}
	reopened := New(Config{Registry: f.a.registry, Citations: true})
	reopened.SetMessages(history)
	after, ok := reopened.sources.resultOrigin(f.id)
	if !ok || after.ambiguous || after.source == nil || after.source.Label != origin.Label || after.source.Hash != origin.Hash {
		t.Fatalf("retention metadata corrupted restored origin: %+v", after)
	}
}
