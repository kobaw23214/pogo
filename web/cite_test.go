package web

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
)

// A live tool_end's stamp lands in the results table, so a snapshot carries the
// same source the SSE frame did.
func TestToolEndSourceReachesTheResultsTable(t *testing.T) {
	h := NewHub()
	h.Publish(Event{Type: EvToolStart, CallID: "c1", Name: "read", TS: 1000})
	h.Publish(Event{
		Type: EvToolEnd, CallID: "c1", Name: "read", Text: "package main", TS: 2400,
		Source: &agent.Source{Label: "S1", Type: agent.SourceFileRead, Title: "main.go"},
	})

	res := h.Snapshot().Results["c1"]
	if res.Source == nil || res.Source.Label != "S1" {
		t.Fatalf("result source = %+v", res.Source)
	}
	blob, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(blob), `"source":{"label":"S1"`) {
		t.Errorf("marshalled result = %s", blob)
	}
}

// A reloaded session resolves its answers' labels from the persisted blocks,
// and the card text shows the raw output — the [cite:...] header is the model's
// copy, not the card's.
func TestSeedRestoresSourcesAndStripsHeaders(t *testing.T) {
	raw, err := json.Marshal(agent.Source{Label: "S7", Type: "command_exec", Title: "go test ./..."})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHub()
	h.Seed([]session.Timed{
		{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
			{Type: llm.BlockToolUse, ID: "c1", Name: "bash"},
		}}},
		{Message: llm.Message{Role: llm.RoleUser, Content: []llm.Block{
			{Type: llm.BlockToolResult, ToolUseID: "c1",
				Text: "[cite:S7 | bash | exit 0 | go test ./...]\nok  ./...", Source: raw},
		}}},
	})

	res := h.Snapshot().Results["c1"]
	if res.Text != "ok  ./..." {
		t.Errorf("replayed text = %q, want the header stripped", res.Text)
	}
	if res.Source == nil || res.Source.Label != "S7" || res.Name != "bash" {
		t.Errorf("replayed source = %+v (name %q)", res.Source, res.Name)
	}
}
