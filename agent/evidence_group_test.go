package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func groupCoverageArgs() map[string]any {
	return map[string]any{
		"view": "query", "pointer": "/rows", "limit": 1,
		"group_by":  []string{"/department"},
		"aggregate": map[string]any{"op": "sum", "pointer": "/amount"},
		"sort":      []any{map[string]any{"pointer": "/aggregate/value", "direction": "desc"}},
	}
}

func TestEvidenceGroupedCoverageTracksGroupsAndKeepsExecutableContinuation(t *testing.T) {
	f := newCoverageFixture(t, `{"rows":[{"department":"alpha","amount":1},{"department":"alpha","amount":2},{"department":"beta","amount":4},{"department":"beta","amount":6},{"department":"gamma","amount":2},{"department":"gamma","amount":3}]}`)
	l := newEvidenceLedger(f.a)
	call, result := f.page(t, "first-group", groupCoverageArgs(), -1)
	l.Register([]llm.Block{call})
	prompt := coveragePrompt(call, result)
	l.ObservePrompt(prompt)
	l.ObservePrompt(prompt)
	status := l.Snapshot()
	if status.TrackedSelections != 1 || status.IncompleteSelections != 1 || len(status.Partial) != 1 || status.Incomplete {
		t.Fatalf("valid first group did not create one partial selector: %+v", status)
	}
	receipt := status.Partial[0]
	if receipt.Total != 3 || receipt.Delivered != 1 || receipt.Undelivered != 2 || receipt.Complete || !receipt.ScanComplete || !strings.Contains(receipt.RangeUnit, "groups") {
		t.Fatalf("scanning six source rows became delivery of three computed groups: %+v", receipt)
	}
	if len(l.Fragments()) != 0 {
		t.Fatal("computed group key or exact aggregate became original source JSON body")
	}
	var next map[string]any
	if json.Unmarshal(receipt.NextRequest, &next) != nil || next["result_id"] != f.id || next["view"] != "query" || next["pointer"] != "/rows" || next["offset"] != float64(1) || next["include_rows"] != false ||
		!reflect.DeepEqual(next["group_by"], []any{"/department"}) || !reflect.DeepEqual(next["aggregate"], map[string]any{"op": "sum", "pointer": "/amount"}) ||
		!reflect.DeepEqual(next["sort"], []any{map[string]any{"pointer": "/aggregate/value", "direction": "desc"}}) {
		t.Fatalf("group continuation lost its calculation/index domain: %s", receipt.NextRequest)
	}
	if _, valid := tools.ToolResultRequestKey(receipt.NextRequest); !valid {
		t.Fatalf("coverage recommended an invalid group continuation: %s", receipt.NextRequest)
	}
	if notice, ok := l.CoverageNotice(); !ok || !strings.Contains(notice, "computed groups") || !strings.Contains(notice, `"undelivered_matches":2`) {
		t.Fatalf("partial group scope did not reach the checkpoint: %s", notice)
	}
	lastCall, lastResult := f.page(t, "remaining-groups", next, -1)
	l.Register([]llm.Block{lastCall})
	prompt = append(prompt, coveragePrompt(lastCall, lastResult)...)
	l.ObservePrompt(prompt)
	status = l.Snapshot()
	if status.TrackedSelections != 1 || status.IncompleteSelections != 0 || len(status.Partial) != 0 || status.Incomplete {
		t.Fatalf("actual final group page did not merge into the same selector: %+v", status)
	}
	if len(l.Fragments()) != 0 {
		t.Fatal("complete computed-group delivery was misregistered as raw source-body delivery")
	}
	for _, selection := range l.selections {
		if receipt := l.selectionReceipt(selection); receipt.Total != 3 || receipt.Delivered != 3 || !receipt.Complete {
			t.Fatalf("terminal selector falsely complete/incomplete: %+v", receipt)
		}
	}
}

func TestEvidenceGroupedZeroBudgetNeverCountsScanAsGroupDelivery(t *testing.T) {
	f := newCoverageFixture(t, `{"rows":[{"department":"a","amount":1},{"department":"b","amount":2},{"department":"c","amount":3}]}`)
	l := newEvidenceLedger(f.a)
	args := groupCoverageArgs()
	args["limit"] = 3
	call, result := f.page(t, "zero-budget-groups", args, 0)
	page, valid := evidenceJSON(StripCiteHeader(result.Text))
	if !valid || page["groups_included"] != true || page["page_displayed"] != false || page["complete"] != false || page["model_budget_omitted"] != true {
		t.Fatalf("fixture did not exercise an actual undisplayed group page: %s", result.Text)
	}
	l.Register([]llm.Block{call})
	l.ObservePrompt(coveragePrompt(call, result))
	status := l.Snapshot()
	if status.FragmentCount != 0 || len(l.Fragments()) != 0 || len(status.Partial) != 0 || status.Incomplete {
		t.Fatalf("budget-zero scan receipt became group or body evidence: %+v", status)
	}
	for _, selection := range l.selections {
		receipt := l.selectionReceipt(selection)
		if receipt.Total != 3 || receipt.Delivered != 0 || receipt.Undelivered != 3 || receipt.Complete {
			t.Fatalf("scan completion masqueraded as group delivery: %+v", receipt)
		}
	}
	if notice, ok := l.CoverageNotice(); ok {
		t.Fatalf("zero group delivery forced a scope-correction model call: %s", notice)
	}
}

func TestEvidenceSortedRowsKeepOriginalPointersAndMergeDescendingPages(t *testing.T) {
	f := newCoverageFixture(t, `{"rows":[{"name":"first source row","amount":30},{"name":"second source row","amount":10},{"name":"third source row","amount":20}]}`)
	l := newEvidenceLedger(f.a)
	args := map[string]any{
		"view": "query", "pointer": "/rows", "limit": 2, "include_rows": true,
		"select": []string{"/name"}, "aggregate": map[string]any{"op": "sum", "pointer": "/amount"},
		"sort": []any{map[string]any{"pointer": "/amount", "direction": "desc"}},
	}
	call, result := f.page(t, "sorted-prefix", args, -1)
	l.Register([]llm.Block{call})
	prompt := coveragePrompt(call, result)
	l.ObservePrompt(prompt)
	status := l.Snapshot()
	if status.Incomplete || len(status.Partial) != 1 || status.Partial[0].Total != 3 || status.Partial[0].Delivered != 2 || status.Partial[0].Undelivered != 1 {
		t.Fatalf("descending index sequence was rejected or counted as complete: %+v", status)
	}
	fragments := l.Fragments()
	if len(fragments) != 2 || fragments[0].Pointer != "/rows/0/name" || fragments[0].Text != "first source row" || fragments[1].Pointer != "/rows/2/name" || fragments[1].Text != "third source row" {
		t.Fatalf("sorted ordinals replaced actual original JSON pointers: %+v", fragments)
	}
	var next map[string]any
	if json.Unmarshal(status.Partial[0].NextRequest, &next) != nil || next["offset"] != float64(2) || next["include_rows"] != true || next["aggregate"] != nil || next["sort"] == nil {
		t.Fatalf("sorted row continuation lost its order or retained unnecessary aggregate: %s", status.Partial[0].NextRequest)
	}
	lastCall, lastResult := f.page(t, "sorted-tail", next, -1)
	l.Register([]llm.Block{lastCall})
	prompt = append(prompt, coveragePrompt(lastCall, lastResult)...)
	l.ObservePrompt(prompt)
	l.ObservePrompt(prompt)
	status = l.Snapshot()
	if status.Incomplete || status.TrackedSelections != 1 || status.IncompleteSelections != 0 || len(l.Fragments()) != 3 {
		t.Fatalf("actual nonmonotonic original-index pages failed to merge: %+v", status)
	}
	last := l.Fragments()[2]
	if last.Pointer != "/rows/1/name" || last.Text != "second source row" || last.Domain != "json" {
		t.Fatalf("sorted tail lost original row attribution: %+v", last)
	}
}

func TestEvidenceGroupAggregateAndSortSelectorsStayDistinct(t *testing.T) {
	f := newCoverageFixture(t, `{"rows":[{"department":"a","amount":1},{"department":"b","amount":2},{"department":"c","amount":3}]}`)
	l := newEvidenceLedger(f.a)
	for index := 0; index < 3; index++ {
		args := groupCoverageArgs()
		if index == 1 {
			args["aggregate"] = map[string]any{"op": "count"}
		}
		if index == 2 {
			args["sort"] = []any{map[string]any{"pointer": "/aggregate/value", "direction": "asc"}}
		}
		call, result := f.page(t, []string{"sum-desc", "count-desc", "sum-asc"}[index], args, -1)
		l.Register([]llm.Block{call})
		l.ObservePrompt(coveragePrompt(call, result))
	}
	status := l.Snapshot()
	if status.Incomplete || status.TrackedSelections != 3 || len(status.Partial) != 3 || len(l.Fragments()) != 0 {
		t.Fatalf("distinct grouped calculations/orders merged into one selector: %+v", status)
	}
	for _, receipt := range status.Partial {
		if receipt.Total != 3 || receipt.Delivered != 1 || receipt.Undelivered != 2 || !strings.Contains(receipt.RangeUnit, "groups") {
			t.Fatalf("distinct group scope receipt incorrect: %+v", receipt)
		}
	}
}
