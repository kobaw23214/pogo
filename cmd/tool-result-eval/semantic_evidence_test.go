package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/tools"
)

func semanticReadForTest(t *testing.T, registry *tools.Registry, f logFixture, id, callID, pattern string, offset, limit int) (toolCallReport, toolView) {
	t.Helper()
	reader, _ := registry.Get(tools.ToolResultReadName)
	args := logSearchArgs(id, pattern, offset, limit, 0)
	result, err := reader.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	return logSearchCallAndView(callID, args, result.Text, f)
}

func semanticEvidenceClaimForTest(ledger SemanticDeliveryLedger, kind, quantifier, pattern string, count *int, ids ...string) SemanticEvidenceClaim {
	return SemanticEvidenceClaim{ID: "claim", Quote: "actual quoted coverage assertion", EvidenceKind: kind, Quantifier: quantifier, ResultID: ledger.ResultID, Pattern: pattern, Count: count, EvidenceIDs: ids}
}

func TestSemanticEvidenceAllPropertiesNeedDeliveredRowsNotScanCount(t *testing.T) {
	f := makeLogFixture()
	registry, id := prepareSearchLog(t, f)
	call, view := semanticReadForTest(t, registry, f, id, "remote-first", "REMOTE_BUSY", 0, 20)
	ledger := buildSemanticDeliveryLedger([]toolCallReport{call}, []requestReport{{ToolViews: []toolView{view}}}, id, f)
	if len(ledger.Searches) != 1 || ledger.Searches[0].TotalMatches != 21 || ledger.Searches[0].ShownItems != 20 || len(ledger.Fragments) != 20 {
		t.Fatalf("actual narrow-page ledger incorrect: pages=%d fragments=%d rejected=%+v", len(ledger.Searches), len(ledger.Fragments), ledger.Rejected)
	}
	page := ledger.Searches[0].ID
	all := semanticEvidenceClaimForTest(ledger, "body_property", "all", "REMOTE_BUSY", nil, page)
	if result := checkSemanticEvidenceClaim(ledger, all); result.Passed {
		t.Fatal("20/21 page plus full scan falsely established all recovery properties")
	}
	n := 21
	if result := checkSemanticEvidenceClaim(ledger, semanticEvidenceClaimForTest(ledger, "count", "count", "REMOTE_BUSY", &n, page)); !result.Passed {
		t.Fatalf("actual complete scan count need not deliver properties: %+v", result)
	}
	n = 20
	local := semanticEvidenceClaimForTest(ledger, "body_property", "displayed_subset", "REMOTE_BUSY", &n, page)
	local.Quote = "并非断言全部已恢复；仅这20条已展示记录均恢复，其他未知"
	if result := checkSemanticEvidenceClaim(ledger, local); !result.Passed {
		t.Fatalf("local/negated global wording was treated as an exhaustive claim: %+v", result)
	}
	if result := checkSemanticEvidenceClaim(ledger, semanticEvidenceClaimForTest(ledger, "count", "count", "REMOTE_BUSY", &n, page)); result.Passed {
		t.Fatal("displayed20 was confused with global scan count21")
	}
	unknown := all
	unknown.Quantifier = "unknown"
	if result := checkSemanticEvidenceClaim(ledger, unknown); result.Passed {
		t.Fatal("unknown quantifier defaulted to supported")
	}
	metadata := ledger.SourceMetadata[0].ID
	if result := checkSemanticEvidenceClaim(ledger, semanticEvidenceClaimForTest(ledger, "body_property", "none", "REMOTE_BUSY", nil, metadata)); result.Passed {
		t.Fatal("scope-only export header established a body property")
	}
	scope := semanticEvidenceClaimForTest(ledger, "scope_metadata", "none", "", nil, metadata)
	if result := checkSemanticEvidenceClaim(ledger, scope); !result.Passed {
		t.Fatalf("safe saved interval/unknown boundary explanation rejected: %+v", result)
	}
	if len(ledger.SourceMetadata[0].Text) > 512 || strings.Contains(ledger.SourceMetadata[0].Text, f.Expected.RootCauseCode) {
		t.Fatal("scope context leaked a hidden root-cause body")
	}
}

func TestSemanticEvidencePaginationOverlapAndWideActualReadCompleteDomain(t *testing.T) {
	f := makeLogFixture()
	registry, id := prepareSearchLog(t, f)
	firstCall, firstView := semanticReadForTest(t, registry, f, id, "first", "REMOTE_BUSY", 0, 20)
	for _, test := range []struct {
		name, pattern string
		offset, limit int
	}{
		{"next_page", "REMOTE_BUSY", 20, 20},
		{"overlap_page", "REMOTE_BUSY", 19, 2},
		{"wide_ERROR_page", "ERROR", 0, 100},
	} {
		t.Run(test.name, func(t *testing.T) {
			secondCall, secondView := semanticReadForTest(t, registry, f, id, "second", test.pattern, test.offset, test.limit)
			ledger := buildSemanticDeliveryLedger([]toolCallReport{firstCall, secondCall}, []requestReport{{ToolViews: []toolView{firstView, secondView}}}, id, f)
			if len(ledger.Searches) != 2 {
				t.Fatalf("actual pages rejected: %+v", ledger.Rejected)
			}
			claim := semanticEvidenceClaimForTest(ledger, "body_property", "all", "REMOTE_BUSY", nil, ledger.Searches[0].ID, ledger.Searches[1].ID)
			if result := checkSemanticEvidenceClaim(ledger, claim); !result.Passed {
				t.Fatalf("actual unique complete domain was not accepted: %+v", result)
			}
			if test.pattern == "ERROR" {
				claim.EvidenceIDs = []string{ledger.Searches[1].ID}
				if result := checkSemanticEvidenceClaim(ledger, claim); !result.Passed {
					t.Fatalf("trusted narrow scan count plus selected complete wide delivery rejected: %+v", result)
				}
				onlyWide := buildSemanticDeliveryLedger([]toolCallReport{secondCall}, []requestReport{{ToolViews: []toolView{secondView}}}, id, f)
				claim.EvidenceIDs = []string{onlyWide.Searches[0].ID}
				if result := checkSemanticEvidenceClaim(onlyWide, claim); result.Passed {
					t.Fatal("wide ERROR scan alone proved absence of REMOTE_BUSY outside its selector")
				}
			}
		})
	}
	duplicate := buildSemanticDeliveryLedger([]toolCallReport{firstCall}, []requestReport{{ToolViews: []toolView{firstView, firstView}}, {ToolViews: []toolView{firstView}}}, id, f)
	if len(duplicate.Searches) != 1 || len(duplicate.Fragments) != 20 {
		t.Fatal("repeated request history inflated delivered pages")
	}
	claim := semanticEvidenceClaimForTest(duplicate, "body_property", "all", "REMOTE_BUSY", nil, duplicate.Searches[0].ID)
	if result := checkSemanticEvidenceClaim(duplicate, claim); result.Passed {
		t.Fatal("overlapping/repeated delivery invented missing21st row")
	}
}

func TestSemanticEvidenceAllDomainCountDoesNotRequireRepeatedPageReference(t *testing.T) {
	f := makeLogFixture()
	registry, id := prepareSearchLog(t, f)
	call, view := semanticReadForTest(t, registry, f, id, "complete-trace", logTargetTrace, 0, 20)
	ledger := buildSemanticDeliveryLedger([]toolCallReport{call}, []requestReport{{ToolViews: []toolView{view}}}, id, f)
	if len(ledger.Fragments) != 5 || len(ledger.Searches) != 1 || ledger.Searches[0].TotalMatches != 5 {
		t.Fatalf("complete trace delivery changed: fragments=%d searches=%+v", len(ledger.Fragments), ledger.Searches)
	}
	var selected []string
	for _, fragment := range ledger.Fragments {
		selected = append(selected, fragment.ID)
	}
	claim := semanticEvidenceClaimForTest(ledger, "body_property", "all", logTargetTrace, nil, selected...)
	if guard := checkSemanticEvidenceClaim(ledger, claim); !guard.Passed {
		t.Fatalf("all five actual lines should use the trusted scan count without repeating page ID: %+v", guard)
	}
	// A count assertion has its own explicit receipt evidence requirement; this
	// change only avoids redundant metadata references for body/all claims.
	five := 5
	claim.EvidenceKind, claim.Quantifier, claim.Count = "count", "count", &five
	if guard := checkSemanticEvidenceClaim(ledger, claim); guard.Passed {
		t.Fatal("body/all metadata lookup silently relaxed count receipt references")
	}
}

func TestSemanticEvidenceAllDomainCountNeverDeliversUnselectedProperties(t *testing.T) {
	f := makeLogFixture()
	registry, id := prepareSearchLog(t, f)
	for _, test := range []struct {
		name, pattern string
		limit, keep   int
	}{
		{"selected_four_of_five", logTargetTrace, 20, 4},
		{"delivered_twenty_of_twenty_one", "REMOTE_BUSY", 20, 20},
	} {
		t.Run(test.name, func(t *testing.T) {
			call, view := semanticReadForTest(t, registry, f, id, test.name, test.pattern, 0, test.limit)
			ledger := buildSemanticDeliveryLedger([]toolCallReport{call}, []requestReport{{ToolViews: []toolView{view}}}, id, f)
			if len(ledger.Fragments) < test.keep || len(ledger.Searches) != 1 || ledger.Searches[0].TotalMatches <= test.keep {
				t.Fatalf("partial selection fixture changed: fragments=%d searches=%+v", len(ledger.Fragments), ledger.Searches)
			}
			var selected []string
			for _, fragment := range ledger.Fragments[:test.keep] {
				selected = append(selected, fragment.ID)
			}
			claim := semanticEvidenceClaimForTest(ledger, "body_property", "all", test.pattern, nil, selected...)
			guard := checkSemanticEvidenceClaim(ledger, claim)
			if guard.Passed || len(guard.Reasons) != 1 || guard.Reasons[0] != "not_all_domain_properties_were_delivered_as_complete_lines" {
				t.Fatalf("trusted scan metadata supplied missing body properties: %+v", guard)
			}
		})
	}
}

func TestSemanticEvidenceBuilderRejectsPreviewWrongSourceErrorAndForgedHash(t *testing.T) {
	f := makeLogFixture()
	registry, id := prepareSearchLog(t, f)
	call, view := semanticReadForTest(t, registry, f, id, "actual", "REMOTE_BUSY", 0, 20)
	clone := func() toolView {
		t.Helper()
		raw, _ := json.Marshal(view)
		var copied toolView
		if json.Unmarshal(raw, &copied) != nil {
			t.Fatal("clone failed")
		}
		return copied
	}
	for _, test := range []struct {
		name   string
		change func(*toolCallReport, *toolView)
	}{
		{"failed_native_read", func(c *toolCallReport, v *toolView) { c.IsError = true }},
		{"other_tool", func(c *toolCallReport, v *toolView) { c.Name = "mcp_eval_get_logs" }},
		{"wrong_call_id", func(c *toolCallReport, v *toolView) { v.CallID = "not-executed" }},
		{"wrong_result", func(c *toolCallReport, v *toolView) { v.Log.ResultID = "R" + strings.Repeat("a", 64) }},
		{"wrong_body_hash", func(c *toolCallReport, v *toolView) { v.BodySHA256 = strings.Repeat("a", 64) }},
		{"wrong_fragment_hash", func(c *toolCallReport, v *toolView) { v.Log.Ranges[0].SHA256 = strings.Repeat("a", 64) }},
		{"preview_metadata", func(c *toolCallReport, v *toolView) { v.Log.Kind = "preview" }},
		{"hidden_page", func(c *toolCallReport, v *toolView) { v.Log.SearchPageValid = false }},
		{"source_failed", func(c *toolCallReport, v *toolView) { v.Log.OriginalIsError = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, v := call, clone()
			test.change(&c, &v)
			ledger := buildSemanticDeliveryLedger([]toolCallReport{c}, []requestReport{{ToolViews: []toolView{v}}}, id, f)
			if len(ledger.Fragments) != 0 || len(ledger.Searches) != 0 || len(ledger.Rejected) == 0 {
				t.Fatalf("unverified view entered semantic body ledger: %d fragments/%d pages", len(ledger.Fragments), len(ledger.Searches))
			}
		})
	}
	ledger := buildSemanticDeliveryLedger([]toolCallReport{call}, []requestReport{{ToolViews: []toolView{view}}}, id, f)
	claim := semanticEvidenceClaimForTest(ledger, "body_property", "all", "REMOTE_BUSY", nil, "invented-id")
	if result := checkSemanticEvidenceClaim(ledger, claim); result.Passed {
		t.Fatal("unknown judge evidence ID accepted")
	}
	claim = semanticEvidenceClaimForTest(ledger, "body_property", "displayed_subset", "REMOTE_BUSY", nil, ledger.Searches[0].ID)
	ledger.Fragments[0].Text = "FORGED PROPERTY"
	if result := checkSemanticEvidenceClaim(ledger, claim); result.Passed {
		t.Fatal("modified ledger fragment bypassed hash revalidation")
	}
}

func TestSemanticEvidenceWholeSavedTextAndIncompleteLineGap(t *testing.T) {
	id := "R" + strings.Repeat("b", 64)
	text := "REMOTE_BUSY outcome=recovered note=中文完整行\nREMOTE_BUSY outcome=recovered\n"
	fragment := SemanticDeliveredFragment{ID: "full", CallID: "read", View: "text_range", Start: 0, End: len(text), Text: text, SHA256: semanticSHA(text), StartsLine: true, EndsLine: true}
	ledger := SemanticDeliveryLedger{ResultID: id, StoredTextBytes: len(text), Fragments: []SemanticDeliveredFragment{fragment}}
	all := semanticEvidenceClaimForTest(ledger, "body_property", "all", "REMOTE_BUSY", nil, "full")
	if result := checkSemanticEvidenceClaim(ledger, all); !result.Passed {
		t.Fatalf("actual entire saved text needs no extra search oracle: %+v", result)
	}
	whole := all
	whole.Pattern = ""
	if result := checkSemanticEvidenceClaim(ledger, whole); !result.Passed {
		t.Fatalf("whole saved-text actual delivery rejected: %+v", result)
	}
	boundary := semanticEvidenceClaimForTest(ledger, "scope_metadata", "none", "", nil, "full")
	if result := checkSemanticEvidenceClaim(ledger, boundary); result.Passed {
		t.Fatal("arbitrary text fragment impersonated scope metadata")
	}
	split := strings.Index(text, "中文")
	left, right := fragment, fragment
	left.ID, left.End, left.Text, left.EndsLine = "left", split, text[:split], false
	left.SHA256 = semanticSHA(left.Text)
	right.ID, right.Start, right.Text, right.StartsLine = "right", split, text[split:], false
	right.SHA256 = semanticSHA(right.Text)
	ledger.Fragments = []SemanticDeliveredFragment{left, right}
	all.EvidenceIDs = []string{"left", "right"}
	if result := checkSemanticEvidenceClaim(ledger, all); !result.Passed {
		t.Fatalf("adjacent actual UTF-8 chunks failed to reassemble full line: %+v", result)
	}
	whole.EvidenceIDs = all.EvidenceIDs
	if result := checkSemanticEvidenceClaim(ledger, whole); !result.Passed {
		t.Fatalf("whole saved-text adjacent chunks rejected: %+v", result)
	}
	right.Start += len("中")
	right.Text = text[right.Start:]
	right.SHA256 = semanticSHA(right.Text)
	ledger.Fragments[1] = right
	if result := checkSemanticEvidenceClaim(ledger, all); result.Passed {
		t.Fatal("unseen UTF-8 gap was repaired from host source or scan metadata")
	}
	if result := checkSemanticEvidenceClaim(ledger, whole); result.Passed {
		t.Fatal("unseen gap was treated as whole saved-text read")
	}
	// A literal-only observation can establish a count if the source is only
	// that literal, but cannot claim attributes from an incomplete property line.
	first := fragment
	first.End = len("REMOTE_BUSY")
	first.Text = text[:first.End]
	first.SHA256 = semanticSHA(first.Text)
	first.EndsLine = false
	ledger.Fragments = []SemanticDeliveredFragment{first}
	ledger.Searches = []SemanticDeliveredSearch{{ID: "scan", Pattern: "REMOTE_BUSY", ScanComplete: true, TotalMatches: 2, ShownItems: 1, FragmentIDs: []string{"full"}}}
	all.EvidenceIDs = []string{"scan"}
	if result := checkSemanticEvidenceClaim(ledger, all); result.Passed {
		t.Fatal("needle-only partial line proved unseen recovery properties")
	}
}
