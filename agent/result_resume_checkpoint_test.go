package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/tools"
)

type durableCheckpointFixture struct {
	store    *session.Store
	options  tools.Options
	config   Config
	agent    *Agent
	origin   *Source
	id, body string
	initial  []llm.Message
}

func newDurableCheckpointFixture(t *testing.T, budgetOnly bool) durableCheckpointFixture {
	t.Helper()
	ctx, dir := context.Background(), t.TempDir()
	store, err := session.Create(filepath.Join(dir, "sessions"), dir, "fake")
	if err != nil {
		t.Fatal(err)
	}
	empty := []string{}
	options := tools.Options{Cwd: dir, SpillDir: filepath.Join(dir, "spill"), ResultScope: "cli:" + store.Path(), Enabled: &empty}
	registry := tools.New(options)
	cfg := Config{Client: &summariser{reply: "Prior work is complete; continue inspecting the saved observation."}, Registry: registry,
		Citations: true, MaxTurns: 1, ToolExecution: tools.Sequential, ToolOutputBudget: ToolOutputBudgetConfig{PerResultTokens: 512, BatchTokens: 2048}}
	a := New(cfg)
	body, tool := `{"rows":[{"id":"a1","amount":3,"status":"failed"},{"id":"b2","amount":7,"status":"ok"}]}`, "mcp_audit_records"
	var detail any = tools.MCPDetails{Server: "audit", Tool: "records"}
	observation := tools.Result{Text: body, Details: detail, Payload: &tools.ResultPayload{Text: body, Structured: json.RawMessage(body)}}
	if budgetOnly {
		tool, body = "read", strings.Repeat("trace=a1 original saved line, including full observation text\n", 900)
		detail = tools.ReadDetails{Path: "observed.log", FirstLine: 1, ShownLines: 900}
		observation = tools.Result{Text: body, Details: detail}
	}
	prepared := registry.PrepareResult(ctx, tool, observation, nil)
	origin := a.sources.Next(tool, detail, "remote-once", body, nil)
	origin.Time = 1234
	call := llm.Block{Type: llm.BlockToolUse, ID: "remote-once", Name: tool, Input: json.RawMessage(`{}`)}
	result := llm.Block{Type: llm.BlockToolResult, ToolUseID: call.ID, Text: fmt.Sprintf("[cite:%s | %s | original observation]\n%s", origin.Label, tool, prepared.Text),
		ResultID: prepared.ResultID, Details: marshalDetails(detail), Source: marshalSource(origin)}
	initial := []llm.Message{
		llm.UserText("Inspect the saved observation; do not repeat a remote action."),
		{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: strings.Repeat("Completed unrelated earlier context. ", 2000) + "[unverified historical label](#cite:S12)"}}},
		{Role: llm.RoleAssistant, Content: []llm.Block{call}},
		{Role: llm.RoleUser, Content: []llm.Block{result}},
	}
	a.SetMessages(initial)
	a.ensureToolHistoryBudgets(ctx)
	id := a.cachedToolPrompt(a.Messages())[3].Content[0].ResultID
	if !sourceResultID(id) || budgetOnly && initial[3].Content[0].ResultID != "" {
		t.Fatal("fixture did not exercise the expected host-issued/cached-only recovery ID")
	}
	if err := store.AppendAll(initial); err != nil {
		t.Fatal(err)
	}
	return durableCheckpointFixture{store: store, options: options, config: cfg, agent: a, origin: origin, id: id, body: body, initial: initial}
}

func checkpointJSONForTest(t *testing.T, message llm.Message) json.RawMessage {
	t.Helper()
	// Inspect the public persisted JSON contract, not checkpoint internals.
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	state := envelope["source_state"]
	if len(state) == 0 || !json.Valid(state) || string(state) == "null" {
		t.Fatalf("compaction lost the host source checkpoint: %.200s", data)
	}
	return state
}

func durableCompactAllForTest(t *testing.T, a *Agent, store *session.Store) {
	t.Helper()
	a.SetClient(&summariser{reply: "Prior work is complete; continue inspecting the saved observation."})
	result, err := a.CompactWithCommit(context.Background(), 0, func(messages []llm.Message) error {
		if len(messages) != 1 {
			t.Fatalf("keep=0 produced an unexpected tool tail: %d messages", len(messages))
		}
		checkpointJSONForTest(t, messages[0])
		return store.ForkWithMessages("", messages)
	})
	if err != nil || result.Kept != 0 || len(a.Messages()) != 1 {
		t.Fatalf("durable keep=0 failed: %+v %v", result, err)
	}
	for _, block := range a.Messages()[0].Content {
		if block.Type == llm.BlockToolUse || block.Type == llm.BlockToolResult {
			t.Fatal("keep=0 left an orphaned tool protocol block")
		}
	}
}

func restartDurableCheckpointForTest(t *testing.T, fixture durableCheckpointFixture) (*Agent, *session.Store) {
	t.Helper()
	store, err := session.Open(fixture.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	config := fixture.config
	config.Client, config.Registry = &fakeClient{}, tools.New(fixture.options)
	a := New(config)
	a.SetMessages(store.Messages(""))
	return a, store
}

func assertDurableOriginForTest(t *testing.T, a *Agent, id string, expected *Source) {
	t.Helper()
	binding, known := a.sources.resultOrigin(id)
	if !known || binding.ambiguous || binding.source == nil || string(marshalSource(binding.source)) != string(marshalSource(expected)) {
		t.Fatalf("checkpoint did not restore exact original label/type/locator/call/time/hash: known=%t binding=%+v expected=%s", known, binding, marshalSource(expected))
	}
	if source, found := a.sources.Get(expected.Label); !found || string(marshalSource(source)) != string(marshalSource(expected)) {
		t.Fatal("original citation label no longer resolves to its original observation")
	}
	if _, reserved := a.sources.reserved["S12"]; !reserved && a.sources.seq < 12 {
		t.Fatal("checkpoint forgot an already reserved unsupported citation label")
	}
	if _, found := a.sources.Get("S12"); found {
		t.Fatal("reserved unsupported citation became a real source during restore")
	}
}

func durableReaderBatchForTest(t *testing.T, a *Agent, store *session.Store, id, prefix string) map[string]*Source {
	t.Helper()
	args := []map[string]any{
		{"result_id": id, "view": "text", "limit": 1000},
		{"result_id": id, "view": "query", "pointer": "/rows", "aggregate": map[string]any{"op": "sum", "pointer": "/amount"}, "include_rows": true, "limit": 2},
		{"result_id": id, "view": "search", "pattern": "a1", "context_lines": 0, "limit": 1},
	}
	calls := make([]llm.Block, len(args))
	for i, input := range args {
		raw, _ := json.Marshal(input)
		calls[i] = llm.Block{Type: llm.BlockToolUse, ID: fmt.Sprintf("%s-%d", prefix, i), Name: tools.ToolResultReadName, Input: raw}
	}
	a.SetClient(&fakeClient{responses: []llm.Response{{Message: llm.Message{Role: llm.RoleAssistant, Content: calls}, StopReason: llm.StopToolUse}}})
	before := len(a.Messages())
	events := drain(a.Run(context.Background(), "Inspect the saved observation using protected local views; do not repeat the original action."))
	sources := make(map[string]*Source)
	for _, event := range events {
		if event.Kind != EventToolEnd {
			continue
		}
		if event.IsError || event.Source == nil {
			t.Fatalf("native saved-result read failed: %s %s", event.ToolCallID, event.ToolOutput)
		}
		sources[event.ToolCallID] = event.Source
	}
	if len(sources) != 3 {
		t.Fatalf("scripted batch did not execute three protected reader calls: %d", len(sources))
	}
	if err := store.AppendAll(a.Messages()[before:]); err != nil {
		t.Fatal(err)
	}
	return sources
}

func TestDurableCompactionCheckpointRestoresRawAndDerivedSourcesAcrossRepeatedCompact(t *testing.T) {
	f := newDurableCheckpointFixture(t, false)
	originalJSON := string(marshalSource(f.origin))
	durableCompactAllForTest(t, f.agent, f.store)
	firstCheckpointHead := f.store.Head()
	resumed, store := restartDurableCheckpointForTest(t, f)
	assertDurableOriginForTest(t, resumed, f.id, f.origin)
	reader, _ := resumed.registry.Get(tools.ToolResultReadName)
	readArgs, _ := json.Marshal(map[string]any{"result_id": f.id, "view": "text"})
	raw, err := reader.Execute(context.Background(), readArgs)
	if err != nil || !strings.Contains(raw.Text, f.body) {
		t.Fatalf("same-scope restart lost original saved bytes: %v", err)
	}
	sources := durableReaderBatchForTest(t, resumed, store, f.id, "after-restart")
	if string(marshalSource(sources["after-restart-0"])) != originalJSON {
		t.Fatal("raw recovery refreshed or reidentified the original source")
	}
	derivedLabels := make(map[string]*Source)
	for i, view := range []string{"query", "search"} {
		source := sources[fmt.Sprintf("after-restart-%d", i+1)]
		if source.Type != SourceTool || source.Label == f.origin.Label || source.SavedResult == nil || source.SavedResult.View != view || source.SavedResult.ResultID != f.id || source.SavedResult.Origin == nil || string(marshalSource(source.SavedResult.Origin)) != originalJSON {
			t.Fatalf("%s did not retain a local derived identity with the precise original parent: %+v", view, source)
		}
		if _, exists := derivedLabels[source.Label]; exists {
			t.Fatal("different local derived views reused a source label")
		}
		derivedLabels[source.Label] = source
	}
	durableCompactAllForTest(t, resumed, store)
	second, secondStore := restartDurableCheckpointForTest(t, f)
	assertDurableOriginForTest(t, second, f.id, f.origin)
	for label, expected := range derivedLabels {
		actual, found := second.sources.Get(label)
		if !found || string(marshalSource(actual)) != string(marshalSource(expected)) {
			t.Fatalf("second compaction/reopen lost derived source %s or its original parent", label)
		}
	}
	previousSequence := second.sources.seq
	if err := secondStore.Fork(firstCheckpointHead); err != nil {
		t.Fatal(err)
	}
	second.SetMessages(secondStore.Messages(""))
	assertDurableOriginForTest(t, second, f.id, f.origin)
	for label := range derivedLabels {
		if _, found := second.sources.Get(label); found {
			t.Fatalf("rewind kept a derived source from the abandoned branch: %s", label)
		}
	}
	afterRewind := durableReaderBatchForTest(t, second, secondStore, f.id, "after-rewind")
	for i := 1; i < 3; i++ {
		source := afterRewind[fmt.Sprintf("after-rewind-%d", i)]
		number, valid := sourceLabelNumber(source.Label)
		if !valid || number <= previousSequence || derivedLabels[source.Label] != nil || source.Label == f.origin.Label {
			t.Fatal("rewind assigned an abandoned or original source label a new meaning")
		}
	}
	otherOptions := f.options
	otherOptions.ResultScope += ":another-session"
	otherConfig := f.config
	otherConfig.Registry = tools.New(otherOptions)
	other := New(otherConfig)
	other.SetMessages(second.Messages()) // Metadata cannot grant another scope's bytes.
	otherReader, _ := other.registry.Get(tools.ToolResultReadName)
	if _, err := otherReader.Execute(context.Background(), readArgs); err == nil {
		t.Fatal("restored checkpoint improperly granted result access across scopes")
	}
	t.Logf("keep=0 durable restart + query/search + second compact + rewind: original %s/time=%d/hash=%s preserved; distinct derived labels and scoped storage verified", f.origin.Label, f.origin.Time, f.origin.Hash)
}

func TestDurableCompactionCheckpointIncludesBudgetOnlyResultID(t *testing.T) {
	f := newDurableCheckpointFixture(t, true)
	if f.initial[3].Content[0].ResultID != "" {
		t.Fatal("fixture must have no raw-history result ID")
	}
	durableCompactAllForTest(t, f.agent, f.store)
	resumed, _ := restartDurableCheckpointForTest(t, f)
	assertDurableOriginForTest(t, resumed, f.id, f.origin)
	reader, _ := resumed.registry.Get(tools.ToolResultReadName)
	args, _ := json.Marshal(map[string]any{"result_id": f.id, "view": "text", "limit": 32768})
	result, err := reader.Execute(context.Background(), args)
	if err != nil || !strings.Contains(result.Text, f.body[:1000]) {
		t.Fatalf("model-view-only R no longer names its original saved page after full compact/reopen: %v", err)
	}
	stamp := resumed.sources.Next(tools.ToolResultReadName, result.Details, "read-after-reopen", result.Text, args)
	if string(marshalSource(stamp)) != string(marshalSource(f.origin)) {
		t.Fatal("model-view-only recovery invented a new source after full compact/reopen")
	}
}

func TestDurableCompactionCheckpointCommitFailureLeavesMetadataAndBranchUnchanged(t *testing.T) {
	f := newDurableCheckpointFixture(t, false)
	beforeMessages, _ := json.Marshal(f.agent.Messages())
	beforeSources, _ := json.Marshal(f.agent.sources.All())
	beforeSequence, beforeHead := f.agent.sources.seq, f.store.Head()
	usage := f.agent.Usage()
	f.agent.SetClient(&summariser{reply: "Short summary without result handles or source labels.", usage: llm.Usage{Input: 100, Output: 10}})
	commitErr := errors.New("test atomic durable commit rejected")
	commits := 0
	_, err := f.agent.CompactWithCommit(context.Background(), 0, func(candidate []llm.Message) error {
		commits++
		checkpointJSONForTest(t, candidate[0])
		inMemory, _ := json.Marshal(f.agent.messages)
		if string(inMemory) != string(beforeMessages) || f.agent.sources.seq != beforeSequence {
			t.Fatal("candidate checkpoint/labels became live before durable commit succeeded")
		}
		return commitErr
	})
	if !errors.Is(err, commitErr) || commits != 1 {
		t.Fatalf("atomic commit failure was not propagated: %v calls=%d", err, commits)
	}
	afterMessages, _ := json.Marshal(f.agent.Messages())
	afterSources, _ := json.Marshal(f.agent.sources.All())
	if string(afterMessages) != string(beforeMessages) || string(afterSources) != string(beforeSources) || f.agent.sources.seq != beforeSequence || f.store.Head() != beforeHead {
		t.Fatal("failed durable commit modified metadata, source identities, allocation or active branch")
	}
	assertDurableOriginForTest(t, f.agent, f.id, f.origin)
	reopened, err := session.Open(f.store.Path())
	if err != nil || !reflect.DeepEqual(reopened.Messages(""), f.initial) {
		t.Fatalf("failed commit changed the persisted branch: %v", err)
	}
	if paid := f.agent.Usage(); paid.Input != usage.Input+100 || paid.Output != usage.Output+10 {
		t.Fatal("failed compaction discarded the real summarization spend")
	}
}
