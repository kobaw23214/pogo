package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/tools"
)

func TestRecoveryEvidenceNeedsNewLateReadNotAnswerOrEarlyReplay(t *testing.T) {
	f := makeRecoveryFixture()
	registry, id := prepareSearchLog(t, f.Logs)
	reader, _ := registry.Get(tools.ToolResultReadName)
	args := logSearchArgs(id, logTargetTrace, 0, 20, 0)
	result, err := reader.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	earlyCall, earlyView := logSearchCallAndView("early-read", args, result.Text, f.Logs)
	if checkpoint := checkRecoveryEarlyDelivery([]toolCallReport{earlyCall}, []requestReport{{ToolViews: []toolView{earlyView}}}, id, f); checkpoint.Passed || len(checkpoint.DeliveredMiddleEvidence) != 3 {
		t.Fatal("early access to late evidence went unnoticed")
	}
	root := f.Logs.RequiredEvidence[1]
	codeOffset := root.Start + strings.Index(root.Line, f.Logs.Expected.RootCauseCode)
	codeArgs, _ := json.Marshal(map[string]any{"result_id": id, "view": "text", "offset": codeOffset, "limit": len(f.Logs.Expected.RootCauseCode)})
	codeResult, err := reader.Execute(context.Background(), codeArgs)
	if err != nil {
		t.Fatal(err)
	}
	codeCall, codeView := logSearchCallAndView("early-code", codeArgs, codeResult.Text, f.Logs)
	if checkpoint := checkRecoveryEarlyDelivery([]toolCallReport{codeCall}, []requestReport{{ToolViews: []toolView{codeView}}}, id, f); checkpoint.Passed || !containsLogKey(checkpoint.DeliveredMiddleEvidence, "root_cause") {
		t.Fatal("partial early root-code read escaped the blind-spot guard")
	}
	answer := f.Logs.Expected
	answer.Sources = []string{"[1](#cite:S2)"}
	encoded, _ := json.Marshal(answer)
	if checked := checkLogAnswer(string(encoded), f.Logs); !checked.FormatCorrect || !checked.FactsCorrect || !checked.CoverageCorrect {
		t.Fatal("test oracle must be otherwise entirely correct")
	}
	// Even a correct final answer and replay of genuine early evidence are not
	// proof of a saved-result read after compaction/reload/reconnect.
	replayed := []requestReport{{ToolViews: []toolView{earlyView}}}
	if trace := checkRecoveryDelivery(nil, replayed, id, f); trace.Passed || trace.ActualModelRanges != 0 {
		t.Fatal("early history replay counted as late recovery")
	}
	lateCall, lateView := logSearchCallAndView("late-read", args, result.Text, f.Logs)
	if trace := checkRecoveryDelivery([]toolCallReport{lateCall}, []requestReport{{ToolViews: []toolView{earlyView, lateView}}}, id, f); !trace.Passed || trace.ActualModelRanges != 5 {
		t.Fatalf("new successful late read did not establish recovery: %+v", trace)
	}
	// A source preview in late history cannot supply final_failure: all four
	// lines, including the tail line, must be delivered by new late reads.
	partialArgs := logSearchArgs(id, logTargetTrace, 0, 4, 0)
	partial, err := reader.Execute(context.Background(), partialArgs)
	if err != nil {
		t.Fatal(err)
	}
	partialCall, partialView := logSearchCallAndView("partial", partialArgs, partial.Text, f.Logs)
	fakePreview := toolView{CallID: "original", BodySHA256: "preview", Log: &logObservation{Kind: "preview", ResultID: id, StatusKnown: true, TextComplete: true, EvidenceKeys: []string{"final_failure"}}}
	if trace := checkRecoveryDelivery([]toolCallReport{partialCall, {ID: "original", Name: "mcp_eval_get_logs"}}, []requestReport{{ToolViews: []toolView{partialView, fakePreview}}}, id, f); trace.Passed || !containsLogKey(trace.MissingEvidence, "final_failure") {
		t.Fatal("original preview repaired missing late tail evidence")
	}
	// Summary/oracle-looking metadata does not become a source fragment.
	summaryCall := toolCallReport{ID: "summary", Name: tools.ToolResultReadName, Args: json.RawMessage(`{"result_id":"` + id + `","view":"summary"}`)}
	summaryView := earlyView
	summaryView.CallID = "summary"
	if trace := checkRecoveryDelivery([]toolCallReport{summaryCall}, []requestReport{{ToolViews: []toolView{summaryView}}}, id, f); trace.Passed {
		t.Fatal("summary content impersonated an original saved-text read")
	}
	badCall := lateCall
	badCall.IsError = true
	if trace := checkRecoveryDelivery([]toolCallReport{badCall}, []requestReport{{ToolViews: []toolView{lateView}}}, id, f); trace.Passed {
		t.Fatal("failed late read counted as recovery")
	}
	if probe := checkRecoveryEarlyDelivery([]toolCallReport{{ID: "source", Name: "mcp_eval_get_logs"}}, []requestReport{{ToolViews: []toolView{{CallID: "source", Log: fakePreview.Log}}}}, id, f); !probe.Passed {
		t.Fatal("normal tail-only early preview must preserve middle blind spot")
	}
}

func TestRecoverySourceRetainsRawStampAndDerivedParent(t *testing.T) {
	id := "R" + strings.Repeat("a", 64)
	original := agent.Source{Label: "S1", Type: agent.SourceMCP, Title: "eval/get_logs", Locator: "mcp://eval/get_logs", CallID: "original", Hash: "123456789abc", Time: 1800000000123}
	copySource := func(source agent.Source) agent.Source {
		t.Helper()
		raw, _ := json.Marshal(source)
		var copy agent.Source
		if json.Unmarshal(raw, &copy) != nil {
			t.Fatal("source roundtrip failed")
		}
		return copy
	}
	restored := copySource(original)
	query := json.RawMessage(`{"result_id":"` + id + `","view":"search","pattern":"trace"}`)
	derived := agent.Source{Label: "S2", Type: agent.SourceTool, Title: "保存结果检索", Locator: "tool://tool-result/" + id, CallID: "late-read", Hash: "abcdef123456", Time: original.Time + 1000,
		SavedResult: &agent.SavedResultCitation{ResultID: id, View: "search", Origin: &restored, Query: query}}
	if checked := checkRecoverySource(&original, &restored, []agent.Source{restored, derived}, id); !checked.Passed || !checked.OriginalStampRetained || checked.LateRawSources != 1 || checked.LateDerivedSources != 1 || len(checked.ParentStamps) != 1 || checked.ParentStamps[0].Time != original.Time {
		t.Fatalf("retained source stamps rejected: %+v", checked)
	}
	for _, test := range []struct {
		name   string
		mutate func(*agent.Source, *agent.Source)
	}{
		{"restamped_original", func(after, late *agent.Source) { after.Time++ }},
		{"changed_original_hash", func(after, late *agent.Source) { after.Hash = "changed" }},
		{"missing_parent", func(after, late *agent.Source) { late.SavedResult.Origin = nil }},
		{"restamped_parent", func(after, late *agent.Source) { late.SavedResult.Origin.Time++ }},
		{"wrong_parent_id", func(after, late *agent.Source) { late.SavedResult.Origin.Label = "S99" }},
		{"wrong_parent_hash", func(after, late *agent.Source) { late.SavedResult.Origin.Hash = "changed" }},
		{"wrong_result", func(after, late *agent.Source) { late.SavedResult.ResultID = "R" + strings.Repeat("b", 64) }},
		{"ambiguous_parent", func(after, late *agent.Source) { late.SavedResult.OriginAmbiguous = true }},
		{"forged_original_label", func(after, late *agent.Source) { late.Label = "S1" }},
		{"wrong_native_request", func(after, late *agent.Source) {
			late.SavedResult.Query = json.RawMessage(`{"result_id":"other","view":"search"}`)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			after, late := copySource(original), copySource(derived)
			test.mutate(&after, &late)
			if checked := checkRecoverySource(&original, &after, []agent.Source{late}, id); checked.Passed {
				t.Fatalf("invalid recovery source accepted: %+v", checked)
			}
		})
	}
	if checked := checkRecoverySource(&original, &restored, nil, id); checked.Passed {
		t.Fatal("source restoration alone impersonated a successful late read")
	}
	if checked := checkRecoverySource(&original, &restored, []agent.Source{restored}, "fake"); checked.Passed {
		t.Fatal("invalid recovery result identity accepted")
	}
	refreshed := copySource(original)
	refreshed.Time++
	if checked := checkRecoverySource(&original, &restored, []agent.Source{refreshed}, id); checked.Passed {
		t.Fatal("raw recovery invented a fresh observation")
	}
}
