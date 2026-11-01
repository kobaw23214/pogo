package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/tools"
)

// This exercises the actual CLI persistence shape, rather than treating
// SetMessages(copy) as a process restart. No external model/tool is contacted.
func TestDurableCompactionKeptResultRestoresScopeAndOriginalStamp(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := session.Create(filepath.Join(dir, "sessions"), dir, "fake")
	if err != nil {
		t.Fatal(err)
	}
	enabled := []string{}
	options := tools.Options{Cwd: dir, SpillDir: filepath.Join(dir, "spill"), ResultScope: "cli:" + store.Path(), Enabled: &enabled}
	registry := tools.New(options)
	text := "trace=a1 saved observation, not a fresh remote read\n"
	details := tools.MCPDetails{Server: "audit", Tool: "logs"}
	prepared := registry.PrepareResult(ctx, "mcp_audit_logs", tools.Result{Text: text, Details: details}, nil)
	if !sourceResultID(prepared.ResultID) {
		t.Fatal("host did not commit an immutable result")
	}
	model := &summariser{reply: "Earlier unrelated work is complete; continue the most recent inspection."}
	cfg := Config{Client: model, Registry: registry, Citations: true, ToolOutputBudget: ToolOutputBudgetConfig{PerResultTokens: 512, BatchTokens: 1024}}
	a := New(cfg)
	origin := a.sources.Next("mcp_audit_logs", details, "remote-once", prepared.Text, nil)
	origin.Time = 1234
	beforeSource := marshalSource(origin)
	call := llm.Block{Type: llm.BlockToolUse, ID: "remote-once", Name: "mcp_audit_logs", Input: json.RawMessage(`{}`)}
	result := llm.Block{Type: llm.BlockToolResult, ToolUseID: call.ID, Text: "[cite:" + origin.Label + " | audit logs]\n" + prepared.Text, Details: marshalDetails(details), ResultID: prepared.ResultID, Source: beforeSource}
	msgs := append(history(3, 2000), llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{call}}, llm.Message{Role: llm.RoleUser, Content: []llm.Block{result}})
	a.SetMessages(msgs)
	if err := store.AppendAll(msgs); err != nil {
		t.Fatal(err)
	}
	oldHead := store.Head()
	compacted, err := a.CompactWithCommit(ctx, 1, func(replacement []llm.Message) error {
		return store.ForkWithMessages("", replacement)
	})
	if err != nil || compacted.Kept != 2 {
		t.Fatalf("real durable compaction did not retain the whole latest pair: %+v %v", compacted, err)
	}
	reopened, err := session.Open(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	active := reopened.Messages("")
	if len(active) != 3 || !reflect.DeepEqual(active[1:], msgs[len(msgs)-2:]) {
		t.Fatal("persisted replacement changed the kept tool pair or its rich metadata")
	}
	if abandoned := reopened.Messages(oldHead); !reflect.DeepEqual(abandoned, msgs) {
		t.Fatal("compaction destroyed the original branch instead of only replacing the active branch")
	}
	registryAfterRestart := tools.New(options)
	cfg.Client, cfg.Registry = &fakeClient{}, registryAfterRestart
	resumed := New(cfg)
	resumed.SetMessages(active)
	binding, known := resumed.sources.resultOrigin(prepared.ResultID)
	if !known || binding.ambiguous || binding.source == nil || string(marshalSource(binding.source)) != string(beforeSource) {
		t.Fatalf("restart lost or restamped original result lineage: %+v / %v", binding, known)
	}
	reader, _ := registryAfterRestart.Get(tools.ToolResultReadName)
	args, _ := json.Marshal(map[string]any{"result_id": prepared.ResultID, "view": "text", "limit": 1000})
	recovered, err := reader.Execute(ctx, args)
	if err != nil || !strings.Contains(recovered.Text, text) {
		t.Fatalf("same stable scope could not recover the committed observation: %v", err)
	}
	stamp := resumed.sources.Next(tools.ToolResultReadName, recovered.Details, "local-after-restart", recovered.Text, args)
	if stamp == nil || string(marshalSource(stamp)) != string(beforeSource) || stamp.Time != 1234 || stamp.CallID != "remote-once" {
		t.Fatal("local recovery changed source time/hash/identity or minted a fresh remote observation")
	}
	resumed.ensureToolHistoryBudgets(ctx)
	if got := resumed.cachedToolPrompt(resumed.Messages())[2].Content[0]; got.ResultID != prepared.ResultID {
		t.Fatal("restoring model budgets changed the host recovery ID")
	}
	otherOptions := options
	otherOptions.ResultScope += ":different-conversation"
	otherReader, _ := tools.New(otherOptions).Get(tools.ToolResultReadName)
	if _, err := otherReader.Execute(ctx, args); err == nil {
		t.Fatal("a retained citation or known ID became a cross-session storage grant")
	}
	t.Logf("durable keep=1: messages %d -> %d; kept=%d; same-scope R readable; source %s/time=%d/hash=%s preserved; original branch retained; different scope denied", len(msgs), len(active), compacted.Kept, stamp.Label, stamp.Time, stamp.Hash)
}
