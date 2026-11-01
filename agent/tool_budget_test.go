package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func budgetRegistryForTest(t *testing.T, extra ...tools.Tool) (*tools.Registry, tools.Options) {
	t.Helper()
	dir := t.TempDir()
	enabled := []string{}
	options := tools.Options{Cwd: dir, SpillDir: filepath.Join(dir, "spill"), ResultScope: t.Name(), Enabled: &enabled, Synth: extra}
	return tools.New(options), options
}

func TestToolBudgetRealRegistryPreservesMixedHistoryAndRecovery(t *testing.T) {
	ctx := context.Background()
	registry, options := budgetRegistryForTest(t)
	body := strings.Repeat("First line: the original observation.\n", 700)
	detail := tools.ReadDetails{Path: "observed.txt", FirstLine: 1, ShownLines: 700}
	prepared := registry.PrepareResult(ctx, "read", tools.Result{Text: body, Details: detail}, nil)
	if prepared.ResultID != "" {
		t.Fatal("fixture must exercise an ID-less host read page")
	}
	details, _ := json.Marshal(detail)
	call := llm.Block{Type: llm.BlockToolUse, ID: "read-original", Name: "read", Input: json.RawMessage(`{"path":"observed.txt"}`)}
	result := llm.Block{Type: llm.BlockToolResult, ToolUseID: call.ID, Text: prepared.Text, Details: details}
	history := []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.Block{call}},
		{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockText, Text: "before"}, result, {Type: llm.BlockText, Text: "after"}}},
	}
	before, _ := json.Marshal(history)
	cfg := ToolOutputBudgetConfig{ContextWindow: 128000, MaxOutputTokens: 1000, PerResultTokens: 512, BatchTokens: 1024}
	a := New(Config{Client: &fakeClient{}, Registry: registry, ToolOutputBudget: cfg})
	a.SetMessages(history)
	a.ensureToolHistoryBudgets(ctx)
	view := a.cachedToolPrompt(a.Messages())
	bounded := view[1].Content[1]
	if bounded.Text == body || estimatedToolText(bounded.Text) > 512 || !sourceResultID(bounded.ResultID) {
		t.Fatalf("large read was not recoverably budgeted: %+v", bounded)
	}
	if view[1].Content[0].Text != "before" || view[1].Content[2].Text != "after" || string(bounded.Details) != string(details) {
		t.Fatal("budget moved mixed blocks or changed UI details")
	}
	after, _ := json.Marshal(a.Messages())
	if string(before) != string(after) {
		t.Fatal("building the model view changed the persisted transcript")
	}
	reader, _ := registry.Get(tools.ToolResultReadName)
	args, _ := json.Marshal(map[string]any{"result_id": bounded.ResultID, "view": "text", "limit": 32768})
	recovered, err := reader.Execute(ctx, args)
	if err != nil || !strings.Contains(recovered.Text, body) {
		t.Fatalf("saved page did not recover the original observation: %v / %.200s", err, recovered.Text)
	}

	// A model with more room must not re-expand an established prompt prefix.
	a.SetToolOutputBudget(ToolOutputBudgetConfig{PerResultTokens: 8192, BatchTokens: 24576})
	a.ensureToolHistoryBudgets(ctx)
	if got := a.cachedToolPrompt(a.Messages())[1].Content[1]; got.Text != bounded.Text || got.ResultID != bounded.ResultID {
		t.Fatal("larger limits expanded or changed the established result view")
	}

	// Recreate the registry against the durable test scope; no original action
	// is executed while rebuilding the request view after resume.
	resumed := New(Config{Client: &fakeClient{}, Registry: tools.New(options), ToolOutputBudget: cfg})
	resumed.SetMessages(history)
	resumed.ensureToolHistoryBudgets(ctx)
	if got := resumed.cachedToolPrompt(resumed.Messages())[1].Content[1]; got.Text != bounded.Text || got.ResultID != bounded.ResultID {
		t.Fatalf("resume changed immutable recovery identity: %+v", got)
	}
	stats := resumed.toolBudgetStats()
	if stats.LastBatchKnown || stats.Batches != 1 || stats.ReducedResults != 1 || stats.FreedTokens <= 0 {
		t.Fatalf("historical reconstruction claimed live headroom or lost savings: %+v", stats)
	}
	view[1].Content[1].Text = "caller mutation"
	if a.Messages()[1].Content[1].Text != body {
		t.Fatal("model-view mutation leaked into the transcript")
	}
}

func TestToolBudgetPromptBaselineAcceptsZeroTextEstimate(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{{
		Message:    llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "x"}}},
		StopReason: llm.StopEndTurn,
		Usage:      llm.Usage{Input: 5000},
	}}}
	a := New(Config{Client: client, Registry: tools.NewRegistry()})
	events := drain(a.Run(context.Background(), "hi"))
	if end := events[len(events)-1]; end.Err != nil {
		t.Fatalf("mock loop failed: %+v", end)
	}
	if a.lastPromptEstimate != 0 || a.LastInput() != 5000 {
		t.Fatalf("fixture did not produce a measured prompt with zero local text estimate: estimate %d input %d", a.lastPromptEstimate, a.LastInput())
	}
	if got := a.promptTokens(); got != 5000 {
		t.Fatalf("zero text estimate discarded authoritative provider input: %d, want 5000", got)
	}
}

func TestToolBudgetGeneratedReadIDRetainsOriginalSourceAcrossResume(t *testing.T) {
	ctx := context.Background()
	registry, options := budgetRegistryForTest(t)
	cfg := ToolOutputBudgetConfig{PerResultTokens: 512, BatchTokens: 1024}
	a := New(Config{Client: &fakeClient{}, Registry: registry, Citations: true, ToolOutputBudget: cfg})
	body := strings.Repeat("This is the observed file body.\n", 900)
	detail := tools.ReadDetails{Path: "observed.txt", FirstLine: 1, ShownLines: 900}
	source := a.sources.Next("read", detail, "read-file", body, nil)
	call := llm.Block{Type: llm.BlockToolUse, ID: source.CallID, Name: "read", Input: json.RawMessage(`{"path":"observed.txt"}`)}
	block := llm.Block{Type: llm.BlockToolResult, ToolUseID: call.ID, Text: "[cite:" + source.Label + " | read | observed.txt]\n" + body, Details: marshalDetails(detail), Source: marshalSource(source)}
	history := []llm.Message{{Role: llm.RoleAssistant, Content: []llm.Block{call}}, {Role: llm.RoleUser, Content: []llm.Block{block}}}
	a.SetMessages(history)
	a.ensureToolHistoryBudgets(ctx)
	view := a.cachedToolPrompt(a.Messages())[1].Content[0]
	if !sourceResultID(view.ResultID) || block.ResultID != "" || !strings.HasPrefix(view.Text, "[cite:"+source.Label+" | read |") {
		t.Fatal("budget-generated read ID changed original citation or transcript metadata")
	}

	for _, current := range []*Agent{a, New(Config{Client: &fakeClient{}, Registry: tools.New(options), Citations: true, ToolOutputBudget: cfg})} {
		if current != a {
			current.SetMessages(history)
			current.ensureToolHistoryBudgets(ctx)
		}
		binding, found := current.sources.resultOrigin(view.ResultID)
		if !found || binding.ambiguous || !sameResultSource(binding.source, source) {
			t.Fatalf("new/resumed budget ID lost the original file source: %+v", binding)
		}
		reader, _ := current.registry.Get(tools.ToolResultReadName)
		args, _ := json.Marshal(map[string]any{"result_id": view.ResultID, "view": "text", "limit": 32768})
		recovered, err := reader.Execute(ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		read, ok := recovered.Details.(tools.ToolResultReadDetails)
		if !ok {
			t.Fatalf("reader returned untrusted detail shape: %T", recovered.Details)
		}
		got := current.sources.Next(tools.ToolResultReadName, read, "recover-body", recovered.Text, args)
		if got == nil || got.Label != source.Label || got.Time != source.Time || got.CallID != source.CallID || got.Type != SourceFileRead {
			t.Fatalf("saved read recovered as a different artifact: %+v", got)
		}
	}
}

func TestToolBudgetLoopBudgetsCompleteBatchWithoutChangingEvents(t *testing.T) {
	ctx := context.Background()
	fastFinished := make(chan struct{})
	body := strings.Repeat("original tool output\n", 900)
	slow := &fakeTool{name: "budget_slow", mode: tools.Parallel, run: func(ctx context.Context, _ json.RawMessage) (tools.Result, error) {
		select {
		case <-fastFinished:
			return tools.Result{Text: body}, nil
		case <-ctx.Done():
			return tools.Result{}, ctx.Err()
		}
	}}
	fast := &fakeTool{name: "budget_fast", mode: tools.Parallel, run: func(context.Context, json.RawMessage) (tools.Result, error) {
		close(fastFinished)
		return tools.Result{Text: body}, errors.New("recorded failure")
	}}
	registry, _ := budgetRegistryForTest(t, slow, fast)
	client := &fakeClient{responses: []llm.Response{toolCalls(slow.name, fast.name)}}
	a := New(Config{Client: client, Registry: registry, ToolOutputBudget: ToolOutputBudgetConfig{PerResultTokens: 512, BatchTokens: 768}})
	events := drain(a.Run(ctx, "Collect both observations"))
	if end := events[len(events)-1]; end.Kind != EventAgentEnd || end.Err != nil {
		t.Fatalf("mock loop failed: %+v", end)
	}
	var stored []llm.Block
	for _, message := range a.Messages() {
		for _, block := range message.Content {
			if block.Type == llm.BlockToolResult {
				stored = append(stored, block)
			}
		}
	}
	if len(stored) != 2 || stored[0].ToolUseID != "call0" || stored[1].ToolUseID != "call1" || !stored[1].IsError {
		t.Fatalf("completion order changed result pairing or error identity: %+v", stored)
	}
	var sent []llm.Block
	for _, message := range client.lastSeen() {
		for _, block := range message.Content {
			if block.Type == llm.BlockToolResult {
				sent = append(sent, block)
			}
		}
	}
	if len(sent) != 2 {
		t.Fatalf("model saw %d results, want both", len(sent))
	}
	var sentCost int64
	for i := range stored {
		if !strings.Contains(stored[i].Text, body) || len(sent[i].Text) >= len(stored[i].Text) || sent[i].ToolUseID != stored[i].ToolUseID || sent[i].IsError != stored[i].IsError {
			t.Fatalf("budget changed original data/status or did not bound result %d", i)
		}
		sentCost += estimatedToolText(sent[i].Text)
		found := false
		for _, event := range events {
			if event.Kind == EventToolEnd && event.ToolCallID == stored[i].ToolUseID {
				found = event.ToolOutput == stored[i].Text
			}
		}
		if !found {
			t.Fatalf("UI event for %s was changed to the model preview", stored[i].ToolUseID)
		}
	}
	if sentCost > 768 || len(slow.callArgs()) != 1 || len(fast.callArgs()) != 1 {
		t.Fatalf("batch exceeded ordinary budget or replayed an action: cost %d", sentCost)
	}
	stats := a.toolBudgetStats()
	if !stats.LastBatchKnown || stats.LastBatchTarget != 768 || stats.LastBatchViewTokens != sentCost || stats.LastBatchOverrunTokens != 0 {
		t.Fatalf("live-batch accounting disagrees with delivered text: %+v / %d", stats, sentCost)
	}
}

func TestToolBudgetUnavailableStoreRetainsOnlyCopyAndReportsZeroTarget(t *testing.T) {
	body := strings.Repeat("cannot discard this only copy\n", 300)
	a := New(Config{Client: &fakeClient{}, Registry: tools.NewRegistry(), ToolOutputBudget: ToolOutputBudgetConfig{ContextWindow: 1}})
	result := llm.Block{Type: llm.BlockToolResult, ToolUseID: "call", Text: body, IsError: true}
	a.cacheToolBatch(context.Background(), []llm.Block{{Name: "lookup"}}, []llm.Block{result}, 0, 0)
	a.messages = []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{result}}}
	if got := a.cachedToolPrompt(a.messages)[0].Content[0]; got.Text != body || !got.IsError || got.ResultID != "" {
		t.Fatal("storage failure discarded an unrecoverable observation")
	}
	stats := a.toolBudgetStats()
	if !stats.LastBatchKnown || stats.LastBatchTarget != 0 || stats.LastHeadroom != 0 || !stats.IdentityFloorOverrun || stats.UnrecoverableOverruns != 1 || stats.LastBatchOverrunTokens != estimatedToolText(body) {
		t.Fatalf("zero target / identity / actual overflow was hidden: %+v", stats)
	}
}

func TestToolBudgetPromptBaselineUsesCachedTextAndFrozenRecovery(t *testing.T) {
	registry, _ := budgetRegistryForTest(t)
	a := New(Config{Client: &fakeClient{}, Registry: registry, ToolOutputBudget: ToolOutputBudgetConfig{PerResultTokens: 512, BatchTokens: 1024}})
	var calls, results []llm.Block
	for _, id := range []string{"first", "second"} {
		calls = append(calls, llm.Block{Type: llm.BlockToolUse, ID: id, Name: "read", Input: json.RawMessage(`{"path":"data.txt"}`)})
		results = append(results, llm.Block{Type: llm.BlockToolResult, ToolUseID: id, Text: strings.Repeat(id+" observation\n", 900)})
	}
	a.SetMessages([]llm.Message{{Role: llm.RoleAssistant, Content: calls}, {Role: llm.RoleUser, Content: results}})
	a.ensureToolHistoryBudgets(context.Background())
	view := a.cachedToolPrompt(a.messages)
	a.lastInput, a.measuredThrough = 7000, len(a.messages)
	a.lastPromptEstimate = llm.EstimateTokensFor(a.client, view) + a.fixedCostTokens
	if got := a.promptTokens(); got != 7000 {
		t.Fatalf("same model view changed the measured baseline: %d", got)
	}
	a.messages = append(a.messages, llm.UserText(strings.Repeat("x", 4000)))
	if got := a.promptTokens(); got != 8000 {
		t.Fatalf("increment included original large result rather than cached view: %d", got)
	}
	before, _ := json.Marshal(a.messages)
	cleared, ok := a.forceClear()
	if !ok || !strings.Contains(cleared[1].Content[0].Text, "tool_result(result_id=") {
		t.Fatalf("forced clearing lost the trusted recovery handle: %+v / %v", cleared, ok)
	}
	frozen, _ := editContext(a.cachedToolPrompt(a.messages), a.activeContextEditConfig(), a.cleared, 0)
	if frozen[1].Content[0].Text != cleared[1].Content[0].Text {
		t.Fatal("disabled automatic clearing resurrected a forced-clear payload")
	}
	after, _ := json.Marshal(a.messages)
	if string(before) != string(after) {
		t.Fatal("forced clearing changed persisted tool results")
	}
}

func TestToolBudgetLoopComparisonPreservesRecoveryAndAvoidsReplay(t *testing.T) {
	ctx := context.Background()
	body := strings.Repeat("Observed row: measured value 42, status verified.\n", 600)
	type observedRun struct {
		resultText, modelText, resultID string
		resultTokens, requestTokens     int64
		actions, modelCalls             int
	}
	run := func(disabled bool) observedRun {
		t.Helper()
		action := &fakeTool{name: "budget_compare", run: func(context.Context, json.RawMessage) (tools.Result, error) {
			return tools.Result{Text: body}, nil
		}}
		registry, _ := budgetRegistryForTest(t, action)
		client := &fakeClient{responses: []llm.Response{toolCalls(action.name)}}
		a := New(Config{Client: client, Registry: registry, ToolOutputBudget: ToolOutputBudgetConfig{PerResultTokens: 512, BatchTokens: 512, Disabled: disabled}})
		events := drain(a.Run(ctx, "Read the observed rows once and report the result."))
		if end := events[len(events)-1]; end.Kind != EventAgentEnd || end.Err != nil {
			t.Fatalf("disabled=%v: mock loop failed: %+v", disabled, end)
		}
		var stored, presented llm.Block
		for _, message := range a.Messages() {
			for _, block := range message.Content {
				if block.Type == llm.BlockToolResult {
					stored = block
				}
			}
		}
		for _, message := range client.lastSeen() {
			for _, block := range message.Content {
				if block.Type == llm.BlockToolResult {
					presented = block
				}
			}
		}
		if !strings.Contains(stored.Text, body) || stored.ToolUseID != "call0" || presented.ToolUseID != stored.ToolUseID || !sourceResultID(stored.ResultID) || presented.ResultID != stored.ResultID {
			t.Fatalf("disabled=%v: original body, pairing or immutable recovery ID was lost", disabled)
		}
		var uiText string
		for _, event := range events {
			if event.Kind == EventToolEnd && event.ToolCallID == stored.ToolUseID {
				uiText = event.ToolOutput
			}
		}
		if uiText != stored.Text {
			t.Fatalf("disabled=%v: UI received a different body from the original transcript", disabled)
		}
		reader, _ := registry.Get(tools.ToolResultReadName)
		args, _ := json.Marshal(map[string]any{"result_id": presented.ResultID, "view": "text", "limit": 32768})
		recovered, err := reader.Execute(ctx, args)
		if err != nil || !strings.Contains(recovered.Text, body) {
			t.Fatalf("disabled=%v: model's final result ID could not recover the original: %v", disabled, err)
		}
		if got := len(action.callArgs()); got != 1 || client.callCount() != 2 {
			t.Fatalf("disabled=%v: budgeting/recovery added action/model calls: action=%d model=%d", disabled, got, client.callCount())
		}
		return observedRun{
			resultText: stored.Text, modelText: presented.Text, resultID: presented.ResultID,
			resultTokens: estimatedToolText(presented.Text), requestTokens: llm.EstimateTokensFor(client, client.lastSeen()),
			actions: len(action.callArgs()), modelCalls: client.callCount(),
		}
	}
	baseline, budgeted := run(true), run(false)
	if baseline.resultText != budgeted.resultText || baseline.resultID != budgeted.resultID {
		t.Fatal("enabling the budget changed the immutable observation or its content-addressed ID")
	}
	if baseline.modelText != baseline.resultText || budgeted.resultTokens > 512 || budgeted.resultTokens >= baseline.resultTokens || budgeted.requestTokens >= baseline.requestTokens {
		t.Fatalf("request view was not bounded/reduced: baseline=%d budgeted=%d result tokens; request=%d/%d", baseline.resultTokens, budgeted.resultTokens, baseline.requestTokens, budgeted.requestTokens)
	}
	saved := baseline.resultTokens - budgeted.resultTokens
	t.Logf("mock-only text estimates: original result=%d tokens; budgeted result=%d; saved=%d (%.1f%%); full request=%d -> %d; action calls=%d/%d; model calls=%d/%d; immutable ID unchanged and full original recovered in both runs",
		baseline.resultTokens, budgeted.resultTokens, saved, 100*float64(saved)/float64(baseline.resultTokens), baseline.requestTokens, budgeted.requestTokens,
		baseline.actions, budgeted.actions, baseline.modelCalls, budgeted.modelCalls)
}

func TestToolBudgetFittingJSONAndCitationRemainWholeForEveryByteAlignment(t *testing.T) {
	ctx := context.Background()
	registry, _ := budgetRegistryForTest(t)
	counterfactualReductions := 0
	for headerMod := 0; headerMod < 4; headerMod++ {
		for bodyMod := 0; bodyMod < 4; bodyMod++ {
			for _, disabled := range []bool{false, true} {
				t.Run(fmt.Sprintf("header_mod_%d/body_mod_%d/disabled_%v", headerMod, bodyMod, disabled), func(t *testing.T) {
					prefix := "[cite:S1 | alignment_fixture | header"
					prefix += strings.Repeat("x", (headerMod-(len(prefix)+len("]\n"))%4+4)%4) + "]\n"
					body := `{"fact":"the original fact must remain directly visible","snapshot_sha256":"` + strings.Repeat("f", 64) + `","pad":"` + strings.Repeat("a", 500)
					body += strings.Repeat("x", (bodyMod-(len(body)+len(`"}`))%4+4)%4) + `"}`
					if len(prefix)%4 != headerMod || len(body)%4 != bodyMod || !json.Valid([]byte(body)) {
						t.Fatal("fixture did not produce the requested valid JSON/header alignment")
					}
					prepared := registry.PrepareResult(ctx, "alignment_fixture", tools.Result{Text: body}, nil)
					if !sourceResultID(prepared.ResultID) {
						t.Fatal("fixture lacks a committed immutable observation")
					}
					source := &Source{Label: "S1", Type: SourceTool, CallID: "alignment-call", Title: "alignment fixture", Locator: "tool://alignment/fixture", Hash: "123456789abc"}
					block := llm.Block{Type: llm.BlockToolResult, ToolUseID: source.CallID, Text: prefix + body, ResultID: prepared.ResultID, Source: marshalSource(source)}
					cost := estimatedToolText(block.Text)
					if !disabled && cost < estimatedToolText(prefix)+estimatedToolText(body) {
						// Prove that this fixture detects the former split-rounding
						// path, rather than passing solely because its fallback
						// metadata happens to be larger than the original JSON.
						incorrectlyReduced := registry.BudgetResultView(ctx, tools.ModelResultBudgetRequest{
							Tool: "alignment_fixture", Text: body, ResultID: prepared.ResultID, Tokens: cost - estimatedToolText(prefix),
						})
						if incorrectlyReduced.Text != body && estimatedToolText(prefix+incorrectlyReduced.Text) < cost {
							counterfactualReductions++
						}
					}
					a := New(Config{Client: &fakeClient{}, Registry: registry, ToolOutputBudget: ToolOutputBudgetConfig{PerResultTokens: cost, BatchTokens: cost, Disabled: disabled}})
					a.messages = []llm.Message{{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, ID: block.ToolUseID, Name: "alignment_fixture", Input: json.RawMessage(`{}`)}}}}
					a.cacheToolBatch(ctx, a.messages[0].Content, []llm.Block{block}, 1, 0)
					a.messages = append(a.messages, llm.Message{Role: llm.RoleUser, Content: []llm.Block{block}})
					for pass := 0; pass < 2; pass++ {
						// Also exercise historical reconstruction of the established
						// view: separate ceil(header/4)+ceil(body/4) can exceed the
						// actual ceil((header+body)/4) by one estimated token.
						if pass > 0 {
							a.ensureToolHistoryBudgets(ctx)
						}
						view := a.cachedToolPrompt(a.messages)[1].Content[0]
						if view.Text != block.Text || view.ResultID != prepared.ResultID || a.messages[1].Content[0].Text != block.Text {
							t.Fatalf("pass %d: a fitting JSON observation was unnecessarily rendered as a preview (full=%d header=%d body=%d estimated tokens): %.180s", pass, cost, estimatedToolText(prefix), estimatedToolText(body), view.Text)
						}
						if stat := a.toolBudgetStats(); stat.ReducedResults != 0 || stat.FreedTokens != 0 {
							t.Fatalf("fitting observation reported budget reduction: %+v", stat)
						}
					}
				})
			}
		}
	}
	if counterfactualReductions == 0 {
		t.Fatal("alignment fixtures did not exercise the former unnecessary-JSON-preview failure")
	}
}
