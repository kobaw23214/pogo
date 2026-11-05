package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func TestDeterministicFixtureOracle(t *testing.T) {
	f := makeFixture()
	want := answer{MatchedCount: 330, Sum: 16058400, Min: 90, Max: 99746, Coverage: coverageText,
		FirstFive: []pair{{186, 25482}, {198, 27126}, {210, 28770}, {222, 30414}, {234, 32058}}}
	if len(f.Rows) != 10000 || !reflect.DeepEqual(f.Expected, want) {
		t.Fatalf("fixture/oracle mismatch: rows=%d, got=%+v", len(f.Rows), f.Expected)
	}
	if digest(f.RowsJSON) != digest(makeFixture().RowsJSON) {
		t.Fatal("fixture fingerprint is nondeterministic")
	}
}

func TestSDKManagedResultRoundTripWithoutModel(t *testing.T) {
	// This checks 50 durable pages and exact recovery, not a latency target.
	// Share the evaluator's independent budget, including race-instrumented CI.
	ctx, cancel := context.WithTimeout(context.Background(), hostRecoveryTimeout)
	defer cancel()
	f := makeFixture()
	local, err := newLocalMCP(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	defer local.close()
	base := t.TempDir()
	empty := []string{}
	registry := tools.New(tools.Options{Cwd: t.TempDir(), ReadOnly: true, Enabled: &empty, MCP: []tools.Tool{local.wrapper}, SpillDir: filepath.Join(base, "spill"), ResultScope: "fixture-self-test"})
	tool, ok := registry.Get(local.wrapper.Name())
	if !ok {
		t.Fatal("MCP wrapper not registered")
	}
	result, err := tool.Execute(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	prepared := registry.PrepareResult(ctx, tool.Name(), result, err)
	if prepared.ResultID == "" || prepared.Payload != nil || len(prepared.Text) >= len(f.PayloadJSON) {
		t.Fatal("real SDK result did not pass through managed persistence/preview")
	}
	recovered := recoverRows(ctx, registry, prepared.ResultID, f)
	if !recovered.MatchesSource || !recovered.Complete || recovered.Pages < 2 || recovered.Rows != 10000 || local.attempts.Load() != 1 {
		t.Fatalf("recovery failed or repeated MCP: %+v, calls=%d", recovered, local.attempts.Load())
	}
	reader, _ := registry.Get(tools.ToolResultReadName)
	raw, _ := json.Marshal(map[string]any{
		"result_id": prepared.ResultID, "view": "query", "pointer": "/rows",
		"where":     []any{map[string]any{"pointer": "/region", "op": "eq", "value": "东区"}, map[string]any{"pointer": "/status", "op": "eq", "value": "active"}, map[string]any{"pointer": "/risk_score", "op": "gte", "value": 60}},
		"aggregate": map[string]any{"op": "sum", "pointer": "/amount_cents"},
	})
	query, err := reader.Execute(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Matched   int `json:"matched_rows"`
		Aggregate struct {
			Value string `json:"value"`
		} `json:"aggregate"`
	}
	if json.Unmarshal([]byte(query.Text), &envelope) != nil || envelope.Matched != 330 || envelope.Aggregate.Value != "16058400" || local.attempts.Load() != 1 {
		t.Fatalf("real saved JSON query mismatch: %s", query.Text)
	}
	// A legal three-query solution combines the prefix projection with sum,
	// then obtains min/max independently. Count comes from matched_rows.
	var analysisArgs map[string]any
	if json.Unmarshal(raw, &analysisArgs) != nil {
		t.Fatal("invalid self-test arguments")
	}
	var calls []toolCallReport
	var views []toolView
	for _, operation := range []string{"sum", "min", "max"} {
		analysisArgs["aggregate"] = map[string]any{"op": operation, "pointer": "/amount_cents"}
		if operation == "sum" {
			analysisArgs["include_rows"], analysisArgs["select"], analysisArgs["limit"] = true, []string{"/id", "/amount_cents"}, 5
		} else {
			delete(analysisArgs, "include_rows")
			delete(analysisArgs, "select")
			delete(analysisArgs, "limit")
		}
		queryArgs, _ := json.Marshal(analysisArgs)
		result, err := reader.Execute(ctx, queryArgs)
		if err != nil {
			t.Fatal(err)
		}
		observation := summarizeQuery(result.Text)
		if observation == nil {
			t.Fatalf("query summary did not decode: %s", result.Text)
		}
		calls = append(calls, toolCallReport{ID: operation, Name: tools.ToolResultReadName, Args: queryArgs, Query: observation, SourceLabel: "S" + operation, OutputSHA256: digest([]byte(result.Text))})
		views = append(views, toolView{CallID: operation, BodySHA256: digest([]byte(agent.StripCiteHeader("[cite:S1 | tool_result | saved data]\n" + result.Text))), Query: summarizeQuery(result.Text)})
	}
	if trace := checkEvidence(calls, prepared.ResultID, f.Expected); trace.Passed || trace.FirstFive || trace.UnverifiedDeliveryQueries != 3 {
		t.Fatalf("host-only query output counted as model evidence: %+v", trace)
	}
	calls = visibleCalls(calls, []requestReport{{ToolViews: views}})
	if trace := checkEvidence(calls, prepared.ResultID, f.Expected); !trace.Passed || trace.SuccessfulQueries != 3 {
		t.Fatalf("valid three-query trace rejected: %+v", trace)
	}
	originalArgs := calls[0].Args
	var weakened map[string]any
	_ = json.Unmarshal(originalArgs, &weakened)
	weakened["where"] = weakened["where"].([]any)[:2]
	calls[0].Args, _ = json.Marshal(weakened)
	if trace := checkEvidence(calls, prepared.ResultID, f.Expected); trace.Passed || trace.Sum || trace.FirstFive {
		t.Fatalf("oracle-correct output with missing risk filter counted as evidence: %+v", trace)
	}
	calls[0].Args = originalArgs
	calls[0].ModelQueries[0].Page.PageDisplayed = false
	if trace := checkEvidence(calls, prepared.ResultID, f.Expected); trace.Passed || trace.FirstFive {
		t.Fatalf("undisplayed row page counted as projection evidence: %+v", trace)
	}
	calls[0].ModelQueries[0].Page.PageDisplayed = true
	views[0].BodySHA256 = digest([]byte(`{"model_budget_omitted":true,"page_displayed":false}`))
	views[0].Query = nil
	budgeted := visibleCalls(calls, []requestReport{{ToolViews: views}})
	if trace := checkEvidence(budgeted, prepared.ResultID, f.Expected); trace.Passed || trace.FirstFive || trace.Sum || trace.UnverifiedDeliveryQueries != 1 || trace.ByteIdenticalDeliveryOnly || !trace.ActualModelViewsParsed {
		t.Fatalf("host prefix rows hidden by the model budget counted as delivered evidence: %+v", trace)
	}
	// Exercise the production budget renderer too: actual compact query bodies
	// at 384 tokens must retain honest offsets and be usable as model evidence.
	var compactCalls []toolCallReport
	var compactViews []toolView
	for offset, pages := 0, 0; offset < 5 && pages < 5; pages++ {
		var rowArgs map[string]any
		_ = json.Unmarshal(raw, &rowArgs)
		delete(rowArgs, "aggregate")
		rowArgs["include_rows"], rowArgs["offset"], rowArgs["limit"], rowArgs["select"] = true, offset, 5-offset, []string{"/id", "/amount_cents"}
		queryArgs, _ := json.Marshal(rowArgs)
		result, err := reader.Execute(ctx, queryArgs)
		if err != nil {
			t.Fatal(err)
		}
		view := registry.BudgetResultView(ctx, tools.ModelResultBudgetRequest{Tool: tools.ToolResultReadName, Text: result.Text, Args: queryArgs, Tokens: 384})
		modelPage := summarizeQuery(view.Text)
		if !queryScopeMatches(modelPage, prepared.ResultID, "/rows", f.Expected.MatchedCount) || !validModelRowPage(modelPage, offset) {
			t.Fatalf("real 384-token query page violates evaluator contract: %s", view.Text)
		}
		callID := fmt.Sprintf("compact-%d", offset)
		compactCalls = append(compactCalls, toolCallReport{ID: callID, Name: tools.ToolResultReadName, Args: queryArgs, OutputSHA256: digest([]byte(result.Text)), Query: summarizeQuery(result.Text)})
		compactViews = append(compactViews, toolView{CallID: callID, BodySHA256: digest([]byte(view.Text)), Query: modelPage})
		offset = *modelPage.NextOffset
	}
	if trace := checkEvidence(visibleCalls(compactCalls, []requestReport{{ToolViews: compactViews}}), prepared.ResultID, f.Expected); !trace.FirstFive || !trace.Count || trace.RejectedModelPages != 0 || local.attempts.Load() != 1 {
		t.Fatalf("real compact-page continuation lost rows or fetched MCP again: %+v", trace)
	}
}

func TestActualModelBudgetPagesAndContinuationEvidence(t *testing.T) {
	f := makeFixture()
	id := "R" + strings.Repeat("a", 64)
	where := []any{map[string]any{"pointer": "/region", "op": "eq", "value": "东区"}, map[string]any{"pointer": "/status", "op": "eq", "value": "active"}, map[string]any{"pointer": "/risk_score", "op": "gte", "value": 60}}
	page := func(offset, count int, operation string, includeRows bool) map[string]any {
		body := map[string]any{"view": "query", "result_id": id, "pointer": "/rows", "offset": offset,
			"scan_complete": true, "matched_rows": f.Expected.MatchedCount, "original_is_error": false, "stored_text_complete": true,
			"coverage": "All matching rows of selected saved array only; source completeness unknown.", "rows_included": includeRows, "page_displayed": includeRows,
			"complete": !includeRows, "omitted": false, "shown_items": count}
		if operation != "" {
			value := map[string]int64{"sum": f.Expected.Sum, "min": f.Expected.Min, "max": f.Expected.Max}[operation]
			body["aggregate"] = map[string]any{"op": operation, "pointer": "/amount_cents", "value": fmt.Sprint(value), "scope": "all matched saved rows"}
		}
		if includeRows {
			items := []any{}
			for index := offset; index < offset+count; index++ {
				want := f.Expected.FirstFive[index]
				items = append(items, map[string]any{"original_index": want.ID - 1, "pointer_suffix": fmt.Sprintf("/%d", want.ID-1), "fields": map[string]any{
					"/id": map[string]any{"present": true, "value": want.ID}, "/amount_cents": map[string]any{"present": true, "value": want.AmountCents}}})
			}
			body["items"], body["next_offset"] = items, offset+count
		}
		return body
	}
	var calls []toolCallReport
	var views []toolView
	appendCall := func(name string, offset, originalRows, modelRows int, operation string, includeRows bool) {
		args := map[string]any{"result_id": id, "view": "query", "pointer": "/rows", "where": where}
		if operation != "" {
			args["aggregate"] = map[string]any{"op": operation, "pointer": "/amount_cents"}
		}
		if includeRows {
			args["include_rows"], args["offset"], args["limit"], args["select"] = true, offset, originalRows, []string{"/id", "/amount_cents"}
		}
		hostJSON, _ := json.Marshal(page(offset, originalRows, operation, includeRows))
		model := page(offset, modelRows, operation, includeRows)
		if originalRows != modelRows {
			model["model_budget_page"] = true
		}
		modelJSON, _ := json.Marshal(model)
		argsJSON, _ := json.Marshal(args)
		calls = append(calls, toolCallReport{ID: name, Name: tools.ToolResultReadName, Args: argsJSON, OutputSHA256: digest(hostJSON), Query: summarizeQuery(string(hostJSON)), SourceLabel: "S" + name})
		views = append(views, toolView{CallID: name, BodySHA256: digest(modelJSON), Query: summarizeQuery(string(modelJSON))})
	}
	appendCall("prefix", 0, 5, 2, "sum", true)
	appendCall("min", 0, 0, 0, "min", false)
	appendCall("max", 0, 0, 0, "max", false)
	requests := []requestReport{{ToolViews: append([]toolView(nil), views...)}}
	initial := visibleCalls(calls, requests)
	if trace := checkEvidence(initial, id, f.Expected); trace.FirstFive || !trace.Sum || !trace.Count || initial[0].ModelBodyDelivered || len(initial[0].ModelQueries) != 1 {
		t.Fatalf("host five-row UI page counted as five rows delivered to model: %+v", trace)
	}
	appendCall("next", 2, 3, 2, "", true)
	appendCall("last", 4, 1, 1, "", true)
	requests = []requestReport{{ToolViews: views}}
	if trace := checkEvidence(visibleCalls(calls, requests), id, f.Expected); !trace.Passed || !trace.FirstFive || trace.RejectedModelPages != 0 {
		t.Fatalf("actual 2+2+1 model pages did not establish first five: %+v", trace)
	}
	// Mutations below affect only the model receipt. The host/UI continues to
	// have all five correct rows and therefore cannot mask a false continuation.
	for _, mutation := range []struct {
		name  string
		apply func(map[string]any)
	}{
		{"false progress", func(body map[string]any) { body["next_offset"] = 5 }},
		{"false shown count", func(body map[string]any) { body["shown_items"] = 5 }},
		{"missing continuation", func(body map[string]any) { delete(body, "next_offset") }},
		{"wrong offset", func(body map[string]any) { body["offset"] = 1 }},
		{"false completion", func(body map[string]any) { body["complete"] = true }},
		{"row omitted", func(body map[string]any) { body["omitted"] = true }},
		{"wrong saved result", func(body map[string]any) { body["result_id"] = "R" + strings.Repeat("b", 64) }},
		{"missing source state", func(body map[string]any) { delete(body, "stored_text_complete") }},
		{"no saved scope", func(body map[string]any) { delete(body, "coverage") }},
		{"zero row advance", func(body map[string]any) { body["items"], body["shown_items"], body["next_offset"] = []any{}, 0, 5 }},
		{"statistics only", func(body map[string]any) {
			body["rows_included"], body["page_displayed"] = false, false
			delete(body, "items")
		}},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			body := page(0, 2, "sum", true)
			body["model_budget_page"] = true
			mutation.apply(body)
			encoded, _ := json.Marshal(body)
			changed := append([]toolView(nil), views...)
			changed[0].BodySHA256, changed[0].Query = digest(encoded), summarizeQuery(string(encoded))
			if trace := checkEvidence(visibleCalls(calls, []requestReport{{ToolViews: changed}}), id, f.Expected); trace.Passed || trace.FirstFive {
				t.Fatalf("model page omission/false continuation became read evidence: %+v", trace)
			}
		})
	}
	// Atomic statistics in the earlier compact envelope are valid evidence of
	// count/aggregate, but carry no rows and must not borrow rows from the UI.
	compact := page(0, 0, "sum", false)
	delete(compact, "result_id")
	delete(compact, "view")
	delete(compact, "pointer")
	compact["original_result_id"], compact["model_budget_omitted"], compact["statistics_displayed"], compact["aggregate_displayed"] = id, true, true, true
	compactJSON, _ := json.Marshal(compact)
	compactViews := append([]toolView(nil), views[:3]...)
	compactViews[0].BodySHA256, compactViews[0].Query = digest(compactJSON), summarizeQuery(string(compactJSON))
	if trace := checkEvidence(visibleCalls(calls[:3], []requestReport{{ToolViews: compactViews}}), id, f.Expected); !trace.Sum || !trace.Count || trace.FirstFive || trace.Passed {
		t.Fatalf("atomic model statistics lost or undisplayed UI rows were borrowed: %+v", trace)
	}
}

func TestStrictAnswerFormat(t *testing.T) {
	valid := `{"matched_count":1,"amount_cents_sum":2,"amount_cents_min":2,"amount_cents_max":2,"first_five":[{"id":3,"amount_cents":2}],"coverage":"仅依据本次保存的数据范围","sources":["[1](#cite:S1)"]}`
	for _, input := range []string{valid, "```json\n" + valid + "\n```"} {
		if _, err := parseAnswer(input); err != nil {
			t.Fatal(err)
		}
	}
	for _, input := range []string{valid + " extra", `{"extra":1}`, `{"matched_count":"1"}`} {
		if _, err := parseAnswer(input); err == nil {
			t.Fatalf("accepted malformed/unknown output: %s", input)
		}
	}
	if !knownCitations([]string{"[1](#cite:S1)"}, map[string]bool{"S1": true}) || knownCitations([]string{"[1](#cite:S2)"}, map[string]bool{"S1": true}) {
		t.Fatal("source validation accepted an unknown label")
	}
	if known, correct := citationChecks([]string{"[2](#cite:S1)"}, map[string]bool{"S1": true}); !known || correct {
		t.Fatal("known source with wrong display number was confused with fabricated identity")
	}
	if known, correct := citationChecks([]string{"[1](#cite:S2)"}, map[string]bool{"S1": true}); known || !correct {
		t.Fatal("unknown source identity was confused with display-format failure")
	}
}

func TestCompleteUsageTotalsNeverSumPartialComponents(t *testing.T) {
	first := usageOf(llm.Usage{Input: 10, Output: 5, CacheRead: 2, Reasoning: 1})
	second := usageOf(llm.Usage{Output: 3})
	total := totalUsage([]requestReport{{Usage: first}, {Usage: second}})
	if total.Input != nil || total.CacheRead != nil || total.Reasoning != nil || total.Output == nil || *total.Output != 8 || total.Known || total.InputComplete || !total.OutputComplete {
		t.Fatalf("partial usage component presented as a complete measured total: %+v", total)
	}
	complete := totalUsage([]requestReport{{Usage: first}, {Usage: first}})
	if !complete.Known || complete.Input == nil || *complete.Input != 20 || complete.Output == nil || *complete.Output != 10 || complete.CacheRead == nil || *complete.CacheRead != 4 {
		t.Fatalf("fully reported usage did not add correctly: %+v", complete)
	}
	if empty := totalUsage(nil); empty.Known || empty.Input != nil || empty.Output != nil {
		t.Fatal("no model requests were reported as zero-cost measured usage")
	}
}

func TestSystemMatchesProductionCitationContract(t *testing.T) {
	actual := fullSystemPrompt()
	if !strings.HasSuffix(actual, agent.CitationSection()) || digest([]byte(actual)) == digest([]byte(systemPrompt)) {
		t.Fatal("measured system omitted the production citation contract or hashed the minimal prefix")
	}
}
