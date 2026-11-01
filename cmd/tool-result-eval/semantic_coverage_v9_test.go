package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/tools"
)

func semanticV9Input(t *testing.T, ledger SemanticDeliveryLedger, coverage string) semanticCoverageInput {
	t.Helper()
	answer, err := json.Marshal(map[string]string{"coverage": coverage})
	if err != nil {
		t.Fatal(err)
	}
	return semanticCoverageInput{Answer: string(answer), Ledger: ledger}
}

func TestSemanticCoverageV9OmittedEchoUsesOnlyVerifiedSubjectAndPreservesDecision(t *testing.T) {
	l := semanticV8ActualSearchLedger(t, logTargetTrace, "ERROR")
	p := semanticV8Page(t, l, logTargetTrace)
	quote := "尾部审计的original_event_time表示业务失败时间"
	input := semanticV9Input(t, l, quote)
	units, err := semanticCoverageInventory(input.Answer)
	if err != nil {
		t.Fatal(err)
	}
	d := semanticV6ClaimDecision(semanticJudgeRequest{Units: units, Ledger: l}, "body_property", "none", "", p.FragmentIDs[len(p.FragmentIDs)-1:])
	d.Assertions[0].Domain = &SemanticDomainRef{Kind: "delivered_subset", SearchPageIDs: []string{p.ID}, Quote: quote}
	for _, omitted := range []bool{false, true} {
		raw, _ := json.Marshal(d)
		if omitted {
			raw = []byte(strings.Replace(string(raw), `"pattern":"",`, "", 1))
		}
		judge := &semanticRegressionJudge{raw: string(raw)}
		r := runSemanticCoverage(context.Background(), judge, input)
		if !r.Passed || judge.calls != 1 || r.Decision == nil || r.Decision.Assertions[0].Pattern != "" || r.OutputSHA256 != digest(raw) {
			t.Fatalf("a verified subject's omitted echo was rejected or raw decision/hash was rewritten: %+v", r)
		}
	}

	d.Assertions[0].Pattern = "ERROR"
	raw, _ := json.Marshal(d)
	r := runSemanticCoverage(context.Background(), &semanticRegressionJudge{raw: string(raw)}, input)
	if r.Passed || !semanticRegressionContains(r.ValidationErrors, "subject_domain_pattern_echo_mismatch") {
		t.Fatalf("a nonempty conflicting echo was silently replaced: %+v", r)
	}
	d.Assertions[0].Pattern = ""
	for _, ids := range [][]string{{"unknown_page"}, {p.ID, semanticV8Page(t, l, "ERROR").ID}} {
		d.Assertions[0].Domain.SearchPageIDs = ids
		raw, _ = json.Marshal(d)
		r = runSemanticCoverage(context.Background(), &semanticRegressionJudge{raw: string(raw)}, input)
		if r.Passed || len(r.ValidationErrors) == 0 {
			t.Fatal("an empty echo manufactured a subject from unknown or mixed-literal pages")
		}
	}
}

func TestSemanticCoverageV9OmittedEchoDoesNotChangeWholeTextOrCountContract(t *testing.T) {
	l := semanticV8ActualSearchLedger(t, "REMOTE_BUSY")
	p := semanticV8Page(t, l, "REMOTE_BUSY")
	c := semanticV8Claim(l, p, "delivered_subset", "all", "已交付的20条均已恢复", p.FragmentIDs)
	c.Pattern = ""
	if g := checkSemanticEvidenceClaim(l, c); !g.Passed {
		t.Fatalf("omitted echo lost the complete explicitly delivered subset: %+v", g)
	}
	c.Domain.Kind = "selection_all"
	if g := checkSemanticEvidenceClaim(l, c); g.Passed {
		t.Fatal("omitted echo turned twenty actual rows into all twenty-one selected rows")
	}
	c.Domain.SearchPageIDs = nil
	if g := checkSemanticEvidenceClaim(l, c); g.Passed || !semanticRegressionContains(g.Reasons, "whole_saved_text_was_not_actually_delivered") {
		t.Fatalf("whole-text empty selector was inferred from convenient fragments: %+v", g)
	}
	count := 21
	c.EvidenceKind, c.Quantifier, c.Count, c.Domain = "count", "count", &count, nil
	c.Quote, c.EvidenceIDs = "REMOTE_BUSY检索总共21条", []string{p.ID}
	if g := checkSemanticEvidenceClaim(l, c); g.Passed {
		t.Fatal("count assertion with no subject pages silently inferred its required literal")
	}
}

func semanticV9ExtentDecision(request semanticJudgeRequest, page SemanticDeliveredSearch, quote string, ids, relationIDs []string) semanticDecision {
	d := semanticV6ClaimDecision(request, "body_property", "none", page.Pattern, ids)
	d.Assertions[0].Quote = quote
	d.Assertions[0].Domain = &SemanticDomainRef{Kind: "selector_undelivered", SearchPageIDs: []string{page.ID}, Quote: quote}
	d.Assertions[0].RelationIDs = relationIDs
	for i := range d.AnchorReviews {
		d.AnchorReviews[i].Interpretation = "particular_extent"
	}
	return d
}

func TestSemanticCoverageV9ParticularExtentNeedsActualCompleteRemainderRow(t *testing.T) {
	l := semanticV8ActualSearchLedger(t, logTargetTrace, "ERROR")
	p := semanticV8Page(t, l, "ERROR")
	relation := semanticV8Relation(t, l, "ERROR", logTargetTrace)
	ids := relation.Witnesses[0].Left.FragmentIDs
	for _, quote := range []string{
		"ERROR选择器未通过该选择器交付的5条匹配中至少有一条已由目标trace搜索完整交付且正文同时含有ERROR与目标trace_id",
		"At least one remaining ERROR selector row was completely delivered through the trace search",
	} {
		input := semanticV9Input(t, l, quote)
		judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
			return semanticV9ExtentDecision(request, p, quote, ids, []string{relation.ID})
		}}
		r := runSemanticCoverage(context.Background(), judge, input)
		if !r.Passed || judge.calls != 1 {
			t.Fatalf("particular completion was confused with all five remaining rows: %+v", r)
		}
		for _, variant := range []string{"noRelation", "metadataOnly", "businessRHS"} {
			judge = &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
				d := semanticV9ExtentDecision(request, p, quote, ids, []string{relation.ID})
				switch variant {
				case "noRelation":
					d.Assertions[0].RelationIDs = nil
				case "metadataOnly":
					d.Assertions[0].EvidenceIDs = []string{"scope_metadata_002"}
				case "businessRHS":
					d.Assertions[0].RelationRightSearchPageIDs = []string{semanticV8Page(t, l, logTargetTrace).ID}
				}
				return d
			}}
			if r := runSemanticCoverage(context.Background(), judge, input); r.Passed {
				t.Fatalf("particular extent bypassed actual remainder/body or RHS-kind guard: %s", variant)
			}
		}
	}
}

func TestSemanticCoverageV9ExtentCannotDismissUniversalOrAnotherPredicate(t *testing.T) {
	l := semanticV8ActualSearchLedger(t, logTargetTrace, "ERROR")
	p := semanticV8Page(t, l, "ERROR")
	relation := semanticV8Relation(t, l, "ERROR", logTargetTrace)
	ids := relation.Witnesses[0].Left.FragmentIDs
	for _, quote := range []string{
		"ERROR选择器未交付的全部5条匹配已完整交付",
		"All remaining ERROR selector rows were completely delivered",
		"ERROR选择器余域至少有一条完整交付，且其余匹配均已恢复",
	} {
		judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
			return semanticV9ExtentDecision(request, p, quote, ids, []string{relation.ID})
		}}
		r := runSemanticCoverage(context.Background(), judge, semanticV9Input(t, l, quote))
		if r.Passed || !semanticRegressionContains(r.ValidationErrors, "particular_extent_not_bound_to_single_row_proposition") {
			t.Fatalf("a real universal anchor or second predicate was hidden as one-row extent: %+v", r)
		}
	}
	quote := "ERROR选择器未交付的5条匹配均已完整交付"
	judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
		d := semanticV9ExtentDecision(request, p, quote, ids, []string{relation.ID})
		d.Assertions[0].Quantifier = "all"
		for i := range d.AnchorReviews {
			d.AnchorReviews[i].Interpretation = "affirmative"
		}
		return d
	}}
	r := runSemanticCoverage(context.Background(), judge, semanticV9Input(t, l, quote))
	if r.Passed || len(r.ValidationErrors) != 0 || !semanticRegressionContains(r.AssertionGuards[0].Reasons, "selector_undelivered_all_properties_not_actually_delivered") {
		t.Fatalf("properly classified all-five completion lost its actual one-of-five body rejection: %+v", r)
	}
}

func TestSemanticCoverageV9UnclearParticularExtentRemainsOneUnknown(t *testing.T) {
	l := semanticV8ActualSearchLedger(t, "ERROR")
	input := semanticV9Input(t, l, "某条记录已完整交付")
	units, _ := semanticCoverageInventory(input.Answer)
	d := semanticV6ClaimDecision(semanticJudgeRequest{Units: units, Ledger: l}, "body_property", "none", "", nil)
	d.Assertions[0].Verdict, d.Assertions[0].ReasonCode = "unknown", "ambiguous"
	for i := range d.AnchorReviews {
		d.AnchorReviews[i].Interpretation = "particular_extent"
	}
	raw, _ := json.Marshal(d)
	judge := &semanticRegressionJudge{raw: string(raw)}
	r := runSemanticCoverage(context.Background(), judge, input)
	if r.Passed || judge.calls != 1 || r.ErrorClass != "" || len(r.ValidationErrors) != 0 {
		t.Fatalf("a genuine unclear single-row extent invented a domain or retried its unknown into a pass: %+v", r)
	}
}

func TestSemanticCoverageV9WholeTextParticularExtentRequiresActuallyCompleteRow(t *testing.T) {
	f := makeLogFixture()
	registry, resultID := prepareSearchLog(t, f)
	reader, _ := registry.Get(tools.ToolResultReadName)
	row := f.RequiredEvidence[0]
	quote := "保存文本中某一条日志已完整交付"
	for _, complete := range []bool{false, true} {
		start, end := row.Start+5, row.End-5
		if complete {
			start, end = row.Start, row.End
		}
		args, _ := json.Marshal(map[string]any{"result_id": resultID, "view": "text", "offset": start, "limit": end - start})
		result, err := reader.Execute(context.Background(), args)
		if err != nil {
			t.Fatalf("actual native text range failed: err=%v", err)
		}
		call, view := logSearchCallAndView("range", args, result.Text, f)
		ledger := buildSemanticDeliveryLedger([]toolCallReport{call}, []requestReport{{ToolViews: []toolView{view}}}, resultID, f)
		if len(ledger.Fragments) != 1 || len(ledger.Searches) != 0 {
			t.Fatalf("test must use one actual raw text range, without selector pages: %+v", ledger.Rejected)
		}
		judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
			d := semanticV6ClaimDecision(request, "body_property", "none", "", []string{ledger.Fragments[0].ID})
			d.Assertions[0].Domain = &SemanticDomainRef{Kind: "selection_all", SearchPageIDs: []string{}, Quote: quote}
			for i := range d.AnchorReviews {
				d.AnchorReviews[i].Interpretation = "particular_extent"
			}
			return d
		}}
		r := runSemanticCoverage(context.Background(), judge, semanticV9Input(t, ledger, quote))
		if r.Passed != complete || len(r.ValidationErrors) != 0 {
			t.Fatalf("whole-text particular extent bypassed a partial line or rejected an actual full row: complete=%v %+v", complete, r)
		}
		if !complete && !semanticRegressionContains(r.AssertionGuards[0].Reasons, "particular_extent_has_no_actual_complete_row") {
			t.Fatalf("partial extent lost its independent actual-row diagnostic: %+v", r)
		}
		// An ordinary field/content proposition may legitimately quote only a
		// partial string. The new completeness obligation is extent-only.
		ordinary := SemanticEvidenceClaim{ID: "field", Quote: "所读片段含该事件字段", EvidenceKind: "body_property", Quantifier: "none", ResultID: resultID, EvidenceIDs: []string{ledger.Fragments[0].ID}, Domain: &SemanticDomainRef{Kind: "selection_all"}}
		if g := checkSemanticEvidenceClaim(ledger, ordinary); !g.Passed {
			t.Fatalf("extent obligation changed the ordinary partial-content contract: %+v", g)
		}
	}
}
