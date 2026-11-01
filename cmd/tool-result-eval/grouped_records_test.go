package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/tools"
)

func groupedEvidenceFixture(t *testing.T) (fixture, string, []toolCallReport, []requestReport, []string) {
	t.Helper()
	f := makeFixture()
	empty := []string{}
	r := tools.New(tools.Options{Cwd: t.TempDir(), ReadOnly: true, Enabled: &empty, SpillDir: filepath.Join(t.TempDir(), "spill"), ResultScope: "grouped-test"})
	prepared := r.PrepareResult(context.Background(), "mcp_eval_get_records", tools.Result{Text: string(f.PayloadJSON), Payload: &tools.ResultPayload{Text: string(f.PayloadJSON), Structured: f.PayloadJSON}}, nil)
	if !sourceResultIDForEval(prepared.ResultID) {
		t.Fatal("fixture result did not persist")
	}
	initial := r.InitialResultView(context.Background(), tools.ModelResultBudgetRequest{Tool: "mcp_eval_get_records", Text: prepared.Text, ResultID: prepared.ResultID})
	calls := []toolCallReport{{ID: "original", Name: "mcp_eval_get_records", SourceLabel: "S1", OutputSHA256: digest([]byte(prepared.Text))}}
	views := []toolView{{CallID: "original", SourceHeader: "[cite:S1 | mcp | synthetic observation]", BodySHA256: digest([]byte(initial.Text)), GroupedBody: groupedMeasuredBody(initial.Text)}}
	reader, _ := r.Get(tools.ToolResultReadName)
	for position, spec := range []struct {
		group         bool
		offset, limit int
	}{{true, 0, 1}, {true, 1, 2}, {false, 0, 2}, {false, 2, 3}} {
		args := map[string]any{"result_id": prepared.ResultID, "view": "query", "pointer": "/rows", "where": []any{map[string]any{"pointer": "/status", "op": "eq", "value": "active"}}, "offset": spec.offset, "limit": spec.limit}
		if spec.group {
			args["group_by"] = []string{"/region"}
			args["aggregate"] = map[string]any{"op": "sum", "pointer": "/amount_cents"}
			args["sort"] = groupedRegionSort
		} else {
			args["sort"] = groupedRecordSort
			args["select"] = []string{"/id", "/amount_cents"}
		}
		raw, _ := json.Marshal(args)
		result, err := reader.Execute(context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		id, label := fmt.Sprint("page-", position), fmt.Sprint("S", position+2)
		calls = append(calls, toolCallReport{ID: id, Name: tools.ToolResultReadName, SourceLabel: label, ReadDetails: result.Details, Args: raw, OutputSHA256: digest([]byte(result.Text)), Query: summarizeQuery(result.Text)})
		views = append(views, toolView{CallID: id, SourceHeader: "[cite:" + label + " | tool_result | saved local query]", BodySHA256: digest([]byte(result.Text)), GroupedBody: groupedMeasuredBody(result.Text), Query: summarizeQuery(result.Text)})
	}
	requests := []requestReport{{Index: 1, StopReason: "end_turn", ToolViews: views}}
	return f, prepared.ResultID, calls, requests, []string{"[1](#cite:S2)", "[1](#cite:S3)", "[1](#cite:S4)", "[1](#cite:S5)"}
}

func cloneGroupedTest[T any](value T) T {
	raw, _ := json.Marshal(value)
	var copy T
	_ = json.Unmarshal(raw, &copy)
	return copy
}
func mutateGroupedBody(t *testing.T, view *toolView, change func(map[string]any)) {
	t.Helper()
	var page map[string]any
	if json.Unmarshal(view.GroupedBody, &page) != nil {
		t.Fatal("invalid test body")
	}
	change(page)
	view.GroupedBody, _ = json.Marshal(page)
	view.BodySHA256 = digest(view.GroupedBody)
}

func TestGroupedRecordsFixtureAndScenarioOracle(t *testing.T) {
	f := makeFixture()
	want := groupedOracle{Matched: 3333, GroupCount: 4, Scope: "saved_observation", Regions: []groupedRegion{{"南区", 833, 41119121}, {"西区", 834, 41104258}, {"东区", 833, 40976758}}, Records: []pair{{9489, 99993}, {7299, 99963}, {5109, 99933}, {2919, 99903}, {729, 99873}}}
	if got := groupedExpected(f); !reflect.DeepEqual(got, want) {
		t.Fatalf("group fixture oracle drifted: %+v", got)
	}
	scenario := newEvalScenario(options{Scenario: "grouped-records"}, f)
	if !validScenarioVariant(options{Scenario: "grouped-records"}) || validScenarioVariant(options{Scenario: "grouped-records", LogTraceOnly: true}) || !scenario.grouped || scenario.logs != nil || scenario.system != fullSystemPrompt() || scenario.task != groupedTaskPrompt || !reflect.DeepEqual(scenario.summaryFields()["expected"], want) {
		t.Fatal("grouped scenario did not preserve isolated records transport and explicit task identity")
	}
	oracle, _ := json.Marshal(want)
	if strings.Contains(scenario.task, string(oracle)) || strings.Contains(scenario.system, string(oracle)) {
		t.Fatal("expected grouped answer leaked into prompt")
	}
}

func TestGroupedRecordsEvidenceRequiresSuccessfulActualPages(t *testing.T) {
	f, id, calls, requests, sources := groupedEvidenceFixture(t)
	withOrigin := append([]string{"[1](#cite:S1)"}, sources...)
	if trace := groupedCheckEvidence(calls, requests, id, f, withOrigin); !trace.Passed || !trace.CitationsSupported {
		t.Fatalf("validated extra original provenance rejected despite both computed views: %+v", trace)
	}
	for _, subset := range [][]string{
		{"[1](#cite:S1)"},
		{"[1](#cite:S1)", "[1](#cite:S2)", "[1](#cite:S3)"},
		{"[1](#cite:S1)", "[1](#cite:S4)", "[1](#cite:S5)"},
		{"[1](#cite:S1)", "[1](#cite:S999)", "[1](#cite:S2)", "[1](#cite:S4)"},
	} {
		if trace := groupedCheckEvidence(calls, requests, id, f, subset); trace.Passed || trace.CitationsSupported {
			t.Fatalf("original provenance replaced missing/invalid computed-source support: citations=%v trace=%+v", subset, trace)
		}
	}
	good := groupedCheckEvidence(calls, requests, id, f, sources)
	if !good.Passed || !good.InitialReferenceOnly || !good.Groups || !good.Records || !good.CitationsSupported || !reflect.DeepEqual(good.GroupRanks, []int{0, 1, 2}) || !reflect.DeepEqual(good.RecordRanks, []int{0, 1, 2, 3, 4}) {
		t.Fatalf("valid actual paginated receipts rejected: %+v", good)
	}
	for _, kind := range []string{"host_only", "failed", "aborted", "no_success_marker", "no_initial", "wrong_id", "missing_group_tail", "missing_row_tail", "wrong_source", "unknown_answer_source", "initial_only_answer_source", "raw_sha_mismatch", "group_not_displayed", "partial_scan", "wrong_group_sort", "wrong_group_value", "wrong_projection", "duplicate_sorted_row", "native_scan_contradiction"} {
		t.Run(kind, func(t *testing.T) {
			cs, rs, ss := cloneGroupedTest(calls), cloneGroupedTest(requests), cloneGroupedTest(sources)
			testID := id
			switch kind {
			case "host_only":
				rs = nil
			case "failed":
				rs[0].ErrorClass = "http_status_402"
			case "aborted":
				rs[0].StopReason = "aborted"
			case "no_success_marker":
				rs[0].StopReason = ""
			case "no_initial":
				rs[0].ToolViews = rs[0].ToolViews[1:]
			case "wrong_id":
				testID = "R" + strings.Repeat("f", 64)
			case "missing_group_tail":
				rs[0].ToolViews = append(rs[0].ToolViews[:2], rs[0].ToolViews[3:]...)
			case "missing_row_tail":
				rs[0].ToolViews = rs[0].ToolViews[:4]
			case "wrong_source":
				rs[0].ToolViews[1].SourceHeader = "[cite:S999 | fake | source]"
			case "unknown_answer_source":
				ss = []string{"[1](#cite:S999)"}
			case "initial_only_answer_source":
				ss = []string{"[1](#cite:S1)"}
			case "raw_sha_mismatch":
				rs[0].ToolViews[1].BodySHA256 = strings.Repeat("0", 64)
			case "group_not_displayed":
				mutateGroupedBody(t, &rs[0].ToolViews[1], func(p map[string]any) {
					p["groups_displayed"] = false
					p["page_displayed"] = false
					p["model_budget_omitted"] = true
				})
			case "partial_scan":
				mutateGroupedBody(t, &rs[0].ToolViews[1], func(p map[string]any) { p["scanned_rows"] = 100 })
			case "wrong_group_sort":
				mutateGroupedBody(t, &rs[0].ToolViews[1], func(p map[string]any) { p["sort"].([]any)[0].(map[string]any)["direction"] = "asc" })
			case "wrong_group_value":
				mutateGroupedBody(t, &rs[0].ToolViews[1], func(p map[string]any) {
					p["items"].([]any)[0].(map[string]any)["aggregate"].(map[string]any)["value"] = "1"
				})
			case "native_scan_contradiction":
				details := cs[1].ReadDetails.(map[string]any)
				details["scan_complete"] = false
			case "wrong_projection":
				var args map[string]any
				_ = json.Unmarshal(cs[3].Args, &args)
				args["select"] = []string{"/id"}
				cs[3].Args, _ = json.Marshal(args)
			case "duplicate_sorted_row":
				mutateGroupedBody(t, &rs[0].ToolViews[3], func(p map[string]any) { items := p["items"].([]any); items[1] = items[0] })
			}
			if trace := groupedCheckEvidence(cs, rs, testID, f, ss); trace.Passed {
				t.Fatalf("false positive %s: %+v", kind, trace)
			}
		})
	}
}

func TestGroupedFailedRequestDoesNotReserveSuccessfulDedupKeys(t *testing.T) {
	f, id, calls, requests, sources := groupedEvidenceFixture(t)
	failed := cloneGroupedTest(requests[0])
	failed.Index = 0
	failed.ErrorClass = "http_status_402"
	if trace := groupedCheckEvidence(calls, append([]requestReport{failed}, requests...), id, f, sources); !trace.Passed || trace.SuccessfulRequests != 1 {
		t.Fatalf("failed earlier request claimed successful body dedup keys: %+v", trace)
	}
}

func TestGroupedBadInitialCannotBeRepairedByLaterReference(t *testing.T) {
	f, id, calls, requests, sources := groupedEvidenceFixture(t)
	for _, kind := range []string{"oversized_or_missing", "hash_mismatch", "row_sample", "nested_row_sample", "source_mismatch"} {
		t.Run(kind, func(t *testing.T) {
			rs := cloneGroupedTest(requests)
			bad := cloneGroupedTest(rs[0].ToolViews[0])
			switch kind {
			case "oversized_or_missing":
				bad.GroupedBody = nil
			case "hash_mismatch":
				bad.BodySHA256 = strings.Repeat("0", 64)
			case "row_sample":
				mutateGroupedBody(t, &bad, func(p map[string]any) { p["rows"] = []any{map[string]any{"id": 1}} })
			case "nested_row_sample":
				mutateGroupedBody(t, &bad, func(p map[string]any) {
					p["json_directory"] = map[string]any{"items_sample": []any{map[string]any{"id": 1}}}
				})
			case "source_mismatch":
				bad.SourceHeader = "[cite:S999 | fake | source]"
			}
			rs = append([]requestReport{{Index: 0, StopReason: "tool_use", ToolViews: []toolView{bad}}}, rs...)
			trace := groupedCheckEvidence(calls, rs, id, f, sources)
			if trace.Passed || trace.InitialReferenceOnly || trace.InitialViews < 1 {
				t.Fatalf("bad earlier initial receipt was forgiven: %+v", trace)
			}
		})
	}
}

func TestGroupedStrictAnswerAndFinalAcceptance(t *testing.T) {
	f, id, calls, requests, sources := groupedEvidenceFixture(t)
	oracle := groupedExpected(f)
	answer := groupedAnswer{Regions: oracle.Regions, Records: oracle.Records, Scope: oracle.Scope, Sources: sources}
	raw, _ := json.Marshal(answer)
	if _, err := groupedParseAnswer(string(raw)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{string(raw) + `{}`, strings.Replace(string(raw), `"scope":"saved_observation"`, `"scope":"remote_all"`, 1)} {
		parsed, err := groupedParseAnswer(bad)
		if err == nil && parsed.Scope == "saved_observation" {
			t.Fatalf("invalid scope/extra object accepted: %s", bad)
		}
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"scope":"saved_observation"`, `"scope":"remote_all","scope":"saved_observation"`, 1),
		strings.Replace(string(raw), `"scope":"saved_observation"`, `"Scope":"saved_observation"`, 1),
		strings.Replace(string(raw), `"region":`, `"Region":`, 1),
	} {
		if _, err := groupedParseAnswer(bad); err == nil {
			t.Fatalf("duplicate or case-variant strict fields accepted: %s", bad)
		}
	}
	labels := map[string]bool{"S1": true, "S2": true, "S3": true, "S4": true, "S5": true}
	report := runReport{FinalAnswer: string(raw), EndReason: "completed", ToolCalls: calls, Requests: requests, MCPCalls: 1, Recovery: recoveryReport{Complete: true, MatchesSource: true}}
	scenario := newEvalScenario(options{Scenario: "grouped-records"}, f)
	scenario.finishGrouped(&report, id, labels)
	if !report.Passed || report.GroupedAnswer == nil || report.GroupedEvidence == nil || !report.GroupedEvidence.Passed {
		t.Fatalf("good grouped task did not pass: %+v", report)
	}
	report.Requests = nil
	scenario.finishGrouped(&report, id, labels)
	if report.Passed {
		t.Fatal("correct host answer/oracle passed without an actual successful model request")
	}
}
