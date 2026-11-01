package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func sourceCheckpointAgentForTest(t *testing.T) (*Agent, *summariser, string) {
	t.Helper()
	registry, _ := budgetRegistryForTest(t)
	client := &summariser{reply: "接收的观察已保存，后续再分析。", usage: llm.Usage{Input: 31, Output: 7}}
	a := New(Config{Client: client, Registry: registry, Citations: true, ToolOutputBudget: ToolOutputBudgetConfig{PerResultTokens: 256, BatchTokens: 512}})
	body := strings.Repeat("A recoverable observation line.\n", 3000)
	details := tools.ReadDetails{Path: "observation.txt", FirstLine: 1, ShownLines: 3000}
	source := a.sources.Next("read", details, "early-observation", body, nil)
	call := llm.Block{Type: llm.BlockToolUse, ID: "early-observation", Name: "read", Input: json.RawMessage(`{"path":"observation.txt"}`)}
	result := llm.Block{Type: llm.BlockToolResult, ToolUseID: call.ID, Text: "[cite:" + source.Label + " | read | observation]\n" + body, Details: marshalDetails(details), Source: marshalSource(source)}
	a.SetMessages([]llm.Message{llm.UserText("先接收资料，以后再分析。"), {Role: llm.RoleAssistant, Content: []llm.Block{call}}, {Role: llm.RoleUser, Content: []llm.Block{result}}, {Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "已收到。"}}}})
	return a, client, source.Label
}

func TestSourceCheckpointCapturesIDsFirstCreatedDuringSummarise(t *testing.T) {
	a, client, label := sourceCheckpointAgentForTest(t)
	if len(a.toolResultViews) != 0 || len(a.sources.resultSources) != 0 || a.Messages()[2].Content[0].ResultID != "" {
		t.Fatal("fixture must have no persisted or cached result reference before compaction")
	}
	if _, err := a.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := decodeSourceCheckpoint(a.Messages()[0].SourceState)
	if err != nil || len(checkpoint.Bindings) != 1 || checkpoint.Bindings[0].State != "source" || checkpoint.Bindings[0].SourceLabel != label || !sourceResultID(checkpoint.Bindings[0].ResultID) {
		t.Fatalf("summarise-only budget reference was lost: %+v / %v", checkpoint, err)
	}
	if strings.Contains(a.Messages()[0].Text(), checkpoint.Bindings[0].ResultID) || strings.Contains(client.reply, checkpoint.Bindings[0].ResultID) {
		t.Fatal("host ledger was added to the model's summary text")
	}
	resumed := New(Config{Client: client, Registry: a.registry, Citations: true})
	resumed.SetMessages(a.Messages())
	reader, _ := a.registry.Get(tools.ToolResultReadName)
	args := json.RawMessage(`{"result_id":"` + checkpoint.Bindings[0].ResultID + `","view":"text","limit":100}`)
	result, readErr := reader.Execute(context.Background(), args)
	if readErr != nil {
		t.Fatal(readErr)
	}
	read := result.Details.(tools.ToolResultReadDetails)
	recovered := resumed.sources.Next(tools.ToolResultReadName, read, "late", result.Text, args)
	original, _ := a.sources.Get(label)
	if !sourceCheckpointSameIdentity(original, recovered) || recovered.SavedResult != nil {
		t.Fatal("checkpoint did not restore the original raw-read stamp")
	}
}

func TestSourceCheckpointFailedCommitRestoresSummariseBindings(t *testing.T) {
	a, _, _ := sourceCheckpointAgentForTest(t)
	before, _ := json.Marshal(checkpointFromSnapshot(a.sources.sourceSnapshot()))
	oldMessages, _ := json.Marshal(a.Messages())
	denied := errors.New("commit denied")
	_, err := a.CompactWithCommit(context.Background(), 0, func(candidate []llm.Message) error {
		if len(candidate[0].SourceState) == 0 {
			t.Fatal("commit candidate does not carry its atomic host checkpoint")
		}
		if len(a.Messages()[0].SourceState) != 0 {
			t.Fatal("candidate checkpoint was installed before durable commit")
		}
		return denied
	})
	if !errors.Is(err, denied) {
		t.Fatalf("commit rejection disappeared: %v", err)
	}
	after, _ := json.Marshal(checkpointFromSnapshot(a.sources.sourceSnapshot()))
	newMessages, _ := json.Marshal(a.Messages())
	if string(before) != string(after) || string(oldMessages) != string(newMessages) || len(a.sources.resultSources) != 0 || a.sources.seq != 1 {
		t.Fatal("failed compaction changed source identity/bindings/sequence or history")
	}
	if len(a.toolResultViews) == 0 || a.Usage().Input != 31 || a.Usage().Output != 7 {
		t.Fatal("fixture must exercise summary-time budgeting and still account for model spend")
	}
}

func TestSourceCheckpointRejectsMalformedMetadataAndKeepsNilAmbiguity(t *testing.T) {
	store := newSourceStore("")
	original := savedPageSourceForTest(store, "one", "https://example.test/observed")
	ids := []string{savedResultIDForTest("a"), savedResultIDForTest("b"), savedResultIDForTest("c")}
	store.bindResult(ids[0], original)
	store.bindResult(ids[1], nil)
	store.bindResult(ids[2], original)
	store.bindResult(ids[2], savedPageSourceForTest(store, "two", "https://other.test/observed"))
	store.reserveCitations("Unknown anchor [1](#cite:S19)")
	checkpoint := checkpointFromSnapshot(store.sourceSnapshot())
	raw, _ := json.Marshal(checkpoint)
	resumed := newSourceStore("")
	resumed.observe([]llm.Message{{Role: llm.RoleUser, SourceState: raw}})
	if binding, found := resumed.resultOrigin(ids[1]); !found || binding.source != nil || binding.ambiguous {
		t.Fatal("explicit non-evidence binding changed")
	}
	if binding, found := resumed.resultOrigin(ids[2]); !found || !binding.ambiguous || binding.source != nil {
		t.Fatal("ambiguous binding selected an original artifact")
	}
	if _, found := resumed.reserved["S19"]; !found {
		t.Fatal("reserved unsupported anchor was forgotten")
	}
	for name, malformed := range map[string]json.RawMessage{
		"future-version": json.RawMessage(`{"version":99,"seq":1,"sources":[],"bindings":[],"reserved":[]}`),
		"duplicate-key":  json.RawMessage(`{"version":1,"version":1,"seq":1,"sources":[],"bindings":[],"reserved":[]}`),
		"unknown-field":  json.RawMessage(`{"version":1,"seq":1,"sources":[],"bindings":[],"reserved":[],"grant":"/etc"}`),
		"missing-source": json.RawMessage(`{"version":1,"seq":1,"sources":[],"bindings":[{"result_id":"` + ids[0] + `","state":"source","source_label":"S1"}],"reserved":[]}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeSourceCheckpoint(malformed); err == nil {
				t.Fatal("invalid checkpoint was accepted")
			}
			empty := newSourceStore("")
			empty.observe([]llm.Message{{Role: llm.RoleUser, SourceState: malformed}})
			if len(empty.All()) != 0 || len(empty.resultSources) != 0 {
				t.Fatal("invalid checkpoint partially changed attribution")
			}
		})
	}
	unsupported := newSourceStore("")
	unsupported.observe([]llm.Message{{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockText, Text: "Earlier citation [1](#cite:S1)"}}, SourceState: json.RawMessage(`{"version":99}`)}})
	if _, found := unsupported.Get("S1"); found {
		t.Fatal("unsupported metadata created evidence")
	}
	if next := unsupported.Next("tool", nil, "new", "new observation", nil); next == nil || next.Label == "S1" {
		t.Fatal("unsupported checkpoint let an old quoted anchor acquire a new identity")
	}
}

func TestSourceCheckpointConflictingKeptLabelFailsClosed(t *testing.T) {
	store := newSourceStore("")
	id := savedResultIDForTest("d")
	original := savedPageSourceForTest(store, "original", "https://original.test/")
	store.bindResult(id, original)
	raw, _ := json.Marshal(checkpointFromSnapshot(store.sourceSnapshot()))
	conflict := *original
	conflict.CallID = "different"
	conflict.Locator = "https://different.test/"
	block := llm.Block{Type: llm.BlockToolResult, ToolUseID: conflict.CallID, ResultID: id, Source: marshalSource(&conflict), Text: "different observation"}
	for _, reverse := range []bool{false, true} {
		messages := []llm.Message{{Role: llm.RoleUser, SourceState: raw}, {Role: llm.RoleUser, Content: []llm.Block{block}}}
		if reverse {
			messages[0], messages[1] = messages[1], messages[0]
		}
		resumed := newSourceStore("")
		resumed.observe(messages)
		if _, found := resumed.Get(original.Label); found {
			t.Fatal("conflicting host records kept a citable attribution under the reused label")
		}
		if binding, found := resumed.resultOrigin(id); !found || !binding.ambiguous || binding.source != nil {
			t.Fatalf("conflicting checkpoint selected a source: %+v / %v", binding, found)
		}
	}
}

func TestSourceCheckpointOversizeRefusesCompactionAndSequenceCannotWrap(t *testing.T) {
	a, _, label := sourceCheckpointAgentForTest(t)
	source, _ := a.sources.Get(label)
	source.Title = strings.Repeat("x", sourceCheckpointMaxBytes)
	before := a.Messages()
	called := false
	if _, err := a.CompactWithCommit(context.Background(), 0, func([]llm.Message) error { called = true; return nil }); err == nil || called {
		t.Fatal("oversize host metadata was silently dropped or committed")
	}
	if len(a.Messages()) != len(before) || len(a.Messages()[0].SourceState) != 0 || len(a.sources.resultSources) != 0 {
		t.Fatal("oversize refusal changed history or source bindings")
	}
	store := newSourceStore("")
	store.seq = int(^uint(0)>>1) - 1
	if store.Next("tool", nil, "last", "result", nil) == nil {
		t.Fatal("last representable source label was not usable")
	}
	if source := store.Next("tool", nil, "overflow", "result", nil); source != nil || store.seq < 0 {
		t.Fatal("source sequence overflowed and could reuse a label")
	}
}

func TestSourceCheckpointNativeDerivedReaderDoesNotCreateResultOfResult(t *testing.T) {
	registry, _ := budgetRegistryForTest(t)
	ctx := context.Background()
	text := strings.Repeat("target: observed log line\n", 100)
	prepared := registry.PrepareResult(ctx, "mcp_logs_get_logs", tools.Result{Text: text, Payload: &tools.ResultPayload{Text: text, Structured: json.RawMessage(`{"rows":[{"x":1},{"x":2}]}`)}}, nil)
	if !sourceResultID(prepared.ResultID) {
		t.Fatal("original observation was not saved")
	}
	reader, _ := registry.Get(tools.ToolResultReadName)
	for _, view := range []string{"query", "search"} {
		t.Run(view, func(t *testing.T) {
			args := map[string]any{"result_id": prepared.ResultID, "view": view}
			if view == "query" {
				args["pointer"], args["aggregate"] = "/rows", map[string]any{"op": "sum", "pointer": "/x"}
			} else {
				args["pattern"], args["limit"] = "target", 20
			}
			raw, _ := json.Marshal(args)
			result, err := reader.Execute(ctx, raw)
			if err != nil {
				t.Fatal(err)
			}
			for _, budget := range []int64{1, 128, 384} {
				bounded := registry.BudgetResultView(ctx, tools.ModelResultBudgetRequest{Tool: tools.ToolResultReadName, Text: result.Text, Args: raw, Details: marshalDetails(result.Details), Tokens: budget})
				if bounded.ResultID != "" {
					t.Fatalf("%s page produced a second saved result at %d tokens: %s", view, budget, bounded.ResultID)
				}
			}
			if final := registry.PrepareResult(ctx, tools.ToolResultReadName, result, nil); final.ResultID != "" {
				t.Fatal("final output boundary saved a result about a derived reader result")
			}
		})
	}
}
