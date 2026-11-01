package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/internal/evidencebounds"
)

func semanticV8ScriptedDomains(decision *semanticDecision, request semanticJudgeRequest) {
	for i := range decision.Assertions {
		a := &decision.Assertions[i]
		if a.EvidenceKind != "body_property" || a.Domain != nil || a.Verdict == "unknown" {
			continue
		}
		kind := "selection_all"
		if a.Quantifier == "displayed_subset" {
			kind = "delivered_subset"
		}
		d := &SemanticDomainRef{Kind: kind, Quote: a.Quote, SearchPageIDs: []string{}}
		if a.Pattern != "" {
			for _, p := range request.Ledger.Searches {
				if p.Pattern == a.Pattern {
					d.SearchPageIDs = append(d.SearchPageIDs, p.ID)
				}
			}
		}
		a.Domain = d
	}
}

func semanticV8ActualSearchLedger(t *testing.T, patterns ...string) SemanticDeliveryLedger {
	t.Helper()
	f := makeLogFixture()
	registry, id := prepareSearchLog(t, f)
	var calls []toolCallReport
	var views []toolView
	for i, pattern := range patterns {
		call, view := semanticRealSearchPage(t, registry, f, id, semanticSequenceID("call", i+1), pattern, 0, 20)
		calls = append(calls, call)
		views = append(views, view)
	}
	return buildSemanticDeliveryLedger(calls, []requestReport{{Index: 1, ToolViews: views}}, id, f)
}

func semanticV8Page(t *testing.T, l SemanticDeliveryLedger, pattern string) SemanticDeliveredSearch {
	t.Helper()
	for _, p := range l.Searches {
		if p.Pattern == pattern {
			return p
		}
	}
	t.Fatalf("missing actual selector %q", pattern)
	return SemanticDeliveredSearch{}
}

func semanticV8Claim(l SemanticDeliveryLedger, p SemanticDeliveredSearch, kind, quantifier, quote string, ids []string) SemanticEvidenceClaim {
	return SemanticEvidenceClaim{ID: "claim", Quote: quote, EvidenceKind: "body_property", Quantifier: quantifier, ResultID: l.ResultID, Pattern: p.Pattern, EvidenceIDs: ids, Domain: &SemanticDomainRef{Kind: kind, SearchPageIDs: []string{p.ID}, Quote: quote}}
}

func semanticV8Relation(t *testing.T, l SemanticDeliveryLedger, left, right string) evidencebounds.Relation {
	t.Helper()
	lp := semanticV8Page(t, l, left)
	rp := semanticV8Page(t, l, right)
	for _, r := range l.LiteralBounds.Relations {
		leftMatch, rightMatch := false, false
		for _, id := range r.LeftIDs {
			leftMatch = leftMatch || id == lp.ID
		}
		for _, id := range r.RightIDs {
			rightMatch = rightMatch || id == rp.ID
		}
		if leftMatch && rightMatch {
			return r
		}
	}
	t.Fatalf("no actual relation %q to %q: %+v", left, right, l.LiteralBounds)
	return evidencebounds.Relation{}
}

func TestSemanticCoverageV8ExplicitDeliveredDomainRequiresEveryReturnedRow(t *testing.T) {
	l := semanticV8ActualSearchLedger(t, "REMOTE_BUSY")
	p := semanticV8Page(t, l, "REMOTE_BUSY")
	if p.TotalMatches != 21 || len(p.Hits) != 20 {
		t.Fatal("actual native fixture must have 20 of 21")
	}
	c := semanticV8Claim(l, p, "delivered_subset", "all", "仅就已交付的20条匹配，这20条均已恢复", p.FragmentIDs[:1])
	if g := checkSemanticEvidenceClaim(l, c); g.Passed {
		t.Fatal("one actual complete row established all twenty delivered rows")
	}
	c.EvidenceIDs = p.FragmentIDs
	if g := checkSemanticEvidenceClaim(l, c); !g.Passed {
		t.Fatalf("all twenty actual delivered rows lost their explicit subset domain: %+v", g)
	}
	c.Domain.Kind = "selection_all"
	if g := checkSemanticEvidenceClaim(l, c); g.Passed {
		t.Fatal("the same twenty rows established all twenty-one selected properties")
	}
}

func TestSemanticCoverageV8PrecedingDomainSpanBindsDependentOriginalQuote(t *testing.T) {
	l := semanticV8ActualSearchLedger(t, "REMOTE_BUSY")
	p := semanticV8Page(t, l, "REMOTE_BUSY")
	whole := "仅就实际已交付的20条REMOTE_BUSY完整日志，这20条均为service=other-job且outcome=recovered"
	answer, _ := json.Marshal(map[string]string{"coverage": whole})
	judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
		d := semanticV6ClaimDecision(request, "body_property", "all", "REMOTE_BUSY", p.FragmentIDs)
		d.Assertions[0].Quote = "这20条均为service=other-job且outcome=recovered"
		d.Assertions[0].Domain = &SemanticDomainRef{Kind: "delivered_subset", SearchPageIDs: []string{p.ID}, Quote: whole}
		property := d.Assertions[0]
		property.ID = "property"
		prefix := semanticAssertion{ID: "prefix", UnitID: request.Units[0].ID, Quote: "仅就实际已交付的20条REMOTE_BUSY完整日志", EvidenceKind: "scope_metadata", Quantifier: "none", Pattern: "REMOTE_BUSY", EvidenceIDs: []string{p.ID}, Verdict: "supported", ReasonCode: "observed_metadata"}
		d.Assertions = []semanticAssertion{prefix, property}
		d.Reviews[0].AssertionIDs = []string{"prefix", "property"}
		for i := range d.AnchorReviews {
			if i == 0 {
				d.AnchorReviews[i].AssertionID = "prefix"
				d.AnchorReviews[i].Interpretation = "scope_metadata"
			} else {
				d.AnchorReviews[i].AssertionID = "property"
			}
		}
		return d
	}}
	r := runSemanticCoverage(context.Background(), judge, semanticCoverageInput{Answer: string(answer), Ledger: l})
	if !r.Passed || r.LiteralBounds == nil {
		t.Fatalf("explicit subject domain lost its actual twenty-row proof or report receipt: %+v", r)
	}
}

func TestSemanticCoverageV8WrongSubjectCannotBorrowCompleteOtherSelector(t *testing.T) {
	l := semanticV8ActualSearchLedger(t, logTargetTrace, "ERROR")
	target := semanticV8Page(t, l, logTargetTrace)
	quote := "ERROR选择器未送达的5条匹配均不属于目标trace"
	c := semanticV8Claim(l, target, "selection_all", "all", quote, target.FragmentIDs)
	if !semanticExplicitDomainSelectorMismatch(quote, c.Pattern, l) {
		t.Fatal("explicit ERROR selector was silently replaced by the convenient complete target selector")
	}
	answer, _ := json.Marshal(map[string]string{"coverage": quote})
	units, _ := semanticCoverageInventory(string(answer))
	d := semanticV6ClaimDecision(semanticJudgeRequest{Units: units, Ledger: l}, "body_property", "all", c.Pattern, c.EvidenceIDs)
	d.Assertions[0].Domain = c.Domain
	if errors := validateSemanticDecision(d, units, l); !semanticRegressionContains(errors, "subject_domain_explicit_selector_mismatch") {
		t.Fatalf("wrong subject reached the actual-body gate as if it were target-five: %v", errors)
	}
}

func TestSemanticCoverageV8UndeliveredParticularWitnessDoesNotEstablishWholeRemainder(t *testing.T) {
	l := semanticV8ActualSearchLedger(t, logTargetTrace, "ERROR")
	p := semanticV8Page(t, l, "ERROR")
	r := semanticV8Relation(t, l, "ERROR", logTargetTrace)
	if p.TotalMatches != 25 || len(p.Hits) != 20 || r.Kind != "counterexample" || len(r.Witnesses) == 0 {
		t.Fatalf("fixture must expose the already-delivered target tail in ERROR's native remainder: %+v %+v", p, r)
	}
	w := r.Witnesses[0].Left
	c := semanticV8Claim(l, p, "selector_undelivered", "none", "ERROR选择器未交付的一条记录包含status=failed", w.FragmentIDs)
	c.RelationIDs = []string{r.ID}
	if g := checkSemanticEvidenceClaim(l, c); !g.Passed {
		t.Fatalf("actual complete cross-selector row was treated as globally unread: %+v", g)
	}
	overlap := c
	overlap.EvidenceKind = "selection_relation"
	overlap.RelationPredicate = "overlap"
	target := semanticV8Page(t, l, logTargetTrace)
	overlap.RelationRightSearchPageIDs = []string{target.ID}
	if g := checkSemanticEvidenceClaim(l, overlap); !g.Passed {
		t.Fatalf("a bound actual remainder/target row could not prove the finite existential overlap: %+v", g)
	}
	c.Quantifier = "all"
	if g := checkSemanticEvidenceClaim(l, c); g.Passed {
		t.Fatal("one real remainder witness established business properties of the entire five-row remainder")
	}
	c.Quantifier = "none"
	c.RelationIDs = nil
	if g := checkSemanticEvidenceClaim(l, c); g.Passed {
		t.Fatal("a body fragment without a bound selector-remainder receipt proved its remainder membership")
	}
}

func TestSemanticCoverageV8LiteralRelationIsIsolatedFromBusinessProperties(t *testing.T) {
	l := semanticV8ActualSearchLedger(t, "REMOTE_BUSY", "PRICE_VERSION_MISMATCH")
	p := semanticV8Page(t, l, "REMOTE_BUSY")
	right := semanticV8Page(t, l, "PRICE_VERSION_MISMATCH")
	r := semanticV8Relation(t, l, "REMOTE_BUSY", "PRICE_VERSION_MISMATCH")
	if r.Kind != "verified_disjoint" {
		t.Fatalf("complete actual price row contains no REMOTE_BUSY literal: %+v", r)
	}
	c := semanticV8Claim(l, p, "selector_undelivered", "all", "REMOTE_BUSY选择器余域不含PRICE_VERSION_MISMATCH检索行", right.FragmentIDs)
	c.EvidenceKind = "selection_relation"
	c.RelationPredicate = "disjoint"
	c.RelationIDs = []string{r.ID}
	c.RelationRightSearchPageIDs = []string{right.ID}
	if g := checkSemanticEvidenceClaim(l, c); !g.Passed {
		t.Fatalf("finite verified-disjoint relation and actual complete proof body were lost: %+v", g)
	}
	c.EvidenceKind = "body_property"
	c.RelationPredicate = ""
	c.RelationRightSearchPageIDs = nil
	c.Quote = "REMOTE_BUSY选择器余域均已恢复"
	c.Domain.Quote = c.Quote
	if g := checkSemanticEvidenceClaim(l, c); g.Passed {
		t.Fatal("finite literal disjointness metadata proved arbitrary recovery of the unread body")
	}
}

func TestSemanticCoverageV8StrongDisjointReceiptCannotReplaceRightOperand(t *testing.T) {
	l := semanticV8ActualSearchLedger(t, logTargetTrace, "ERROR", "PRICE_VERSION_MISMATCH")
	p := semanticV8Page(t, l, "ERROR")
	target := semanticV8Page(t, l, logTargetTrace)
	price := semanticV8Page(t, l, "PRICE_VERSION_MISMATCH")
	r := semanticV8Relation(t, l, "ERROR", "PRICE_VERSION_MISMATCH")
	if r.Kind != "verified_disjoint" {
		t.Fatalf("expected a strong but irrelevant right-selector proof: %s", r.Kind)
	}
	c := semanticV8Claim(l, p, "selector_undelivered", "all", "ERROR选择器余域不含目标trace", price.FragmentIDs)
	c.EvidenceKind = "selection_relation"
	c.RelationPredicate = "disjoint"
	c.RelationIDs = []string{r.ID}
	c.RelationRightSearchPageIDs = []string{target.ID}
	if g := checkSemanticEvidenceClaim(l, c); g.Passed {
		t.Fatal("ERROR-to-price disjoint proof silently replaced the original target-trace operand")
	}
}

func TestSemanticCoverageV8WitnessAllowsAlternativeCompleteBytesButNotGapOrWrongHash(t *testing.T) {
	l := semanticV8ActualSearchLedger(t, logTargetTrace, "checkout-72b1", "ERROR")
	r := semanticV8Relation(t, l, "ERROR", logTargetTrace)
	if len(r.Witnesses) == 0 {
		t.Fatal("missing actual shared tail witness")
	}
	w := r.Witnesses[0].Left
	var original SemanticDeliveredFragment
	for _, f := range l.Fragments {
		if f.Start <= w.LineStart && f.End >= w.LineEnd {
			original = f
			break
		}
	}
	if original.ID == "" {
		t.Fatal("expected at least one complete actual row copy")
	}
	merged, valid := semanticMergeFragments([]SemanticDeliveredFragment{original}, l.StoredTextBytes)
	if !valid {
		t.Fatal("invalid actual body")
	}
	if !semanticWitnessSelected(w, map[string]bool{original.ID: true}, merged) {
		t.Fatal("one full duplicate copy required all redundant overlapping references")
	}
	bad := w
	bad.LineSHA256 = strings.Repeat("0", 64)
	if semanticWitnessSelected(bad, map[string]bool{original.ID: true}, merged) {
		t.Fatal("wrong row hash passed")
	}
	cut := original
	cut.Start = w.LineStart + 1
	cut.End = w.LineEnd
	cut.Text = original.Text[cut.Start-original.Start : cut.End-original.Start]
	cut.SHA256 = semanticSHA(cut.Text)
	partial, _ := semanticMergeFragments([]SemanticDeliveredFragment{cut}, l.StoredTextBytes)
	if semanticWitnessSelected(w, map[string]bool{original.ID: true}, partial) {
		t.Fatal("missing one actual row byte was filled from another unselected copy")
	}
}

func TestSemanticCoverageV8StrongProtocolCannotFallBackToLegacyPatternOnly(t *testing.T) {
	l := semanticV8ActualSearchLedger(t, "REMOTE_BUSY")
	p := semanticV8Page(t, l, "REMOTE_BUSY")
	answer, _ := json.Marshal(map[string]string{"coverage": "REMOTE_BUSY全部匹配均已恢复"})
	units, _ := semanticCoverageInventory(string(answer))
	d := semanticV6ClaimDecision(semanticJudgeRequest{Units: units, Ledger: l}, "body_property", "all", "REMOTE_BUSY", p.FragmentIDs)
	// Raw output bypasses the test-only legacy-schema adapter deliberately.
	raw, _ := json.Marshal(d)
	judge := &semanticRegressionJudge{raw: string(raw)}
	r := runSemanticCoverage(context.Background(), judge, semanticCoverageInput{Answer: string(answer), Ledger: l})
	if r.Passed || !semanticRegressionContains(r.ValidationErrors, "body_assertion_subject_domain_missing") {
		t.Fatalf("v8 response without a subject domain used legacy pattern coverage as a strong acceptance path: %+v", r)
	}
}

func TestSemanticCoverageV8MalformedNativeHitCannotPanicBeforeValidation(t *testing.T) {
	l := semanticV8ActualSearchLedger(t, "REMOTE_BUSY")
	for _, hit := range []SemanticDeliveredHit{{Ordinal: 0, Start: 10, End: -1}, {Ordinal: 0, Start: 20, End: 10}} {
		bad := l
		bad.Searches = append([]SemanticDeliveredSearch(nil), l.Searches...)
		bad.Searches[0].Hits = append([]SemanticDeliveredHit(nil), l.Searches[0].Hits...)
		bad.Searches[0].Hits[0] = hit
		if semanticLedgerInputValid(bad) {
			t.Fatal("negative or reversed actual-hit range was accepted")
		}
	}
}

func TestSemanticCoverageV8UnclearRelationAbstainsWithoutInventingOperand(t *testing.T) {
	l := semanticV8ActualSearchLedger(t, "REMOTE_BUSY")
	answer, _ := json.Marshal(map[string]string{"coverage": "该余域与另一选择器的关系无法确定"})
	judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
		d := semanticV6ClaimDecision(request, "selection_relation", "unknown", "", nil)
		d.Assertions[0].Verdict = "unknown"
		d.Assertions[0].ReasonCode = "ambiguous"
		return d
	}}
	r := runSemanticCoverage(context.Background(), judge, semanticCoverageInput{Answer: string(answer), Ledger: l})
	if r.Passed || r.ErrorClass != "" || len(r.ValidationErrors) > 0 || r.StreamCalls != 1 {
		t.Fatalf("ambiguous relation was forced to invent a domain/right operand or retry for a pass: %+v", r)
	}
}

func TestSemanticCoverageV8FailedActualRequestsNeverBecomeDeliveredEvidence(t *testing.T) {
	f := makeLogFixture()
	replay := semanticReplayByName(t, "sourcefix-control-auto")
	for _, kind := range []string{"http_error", "aborted"} {
		t.Run(kind, func(t *testing.T) {
			failed := append([]requestReport(nil), replay.Requests...)
			for i := range failed {
				if kind == "http_error" {
					failed[i].ErrorClass = "http_status_500"
				} else {
					failed[i].ErrorClass = ""
					failed[i].StopReason = "aborted"
				}
			}
			ledger := buildSemanticDeliveryLedger(replay.Calls, failed, replay.OriginalResultID, f)
			trace := checkLogDelivery(replay.Calls, failed, replay.OriginalResultID, f)
			if len(ledger.Fragments) != 0 || len(ledger.Searches) != 0 || len(ledger.Citations) != 0 || len(ledger.LiteralBounds.Relations) != 0 || trace.Passed || trace.ActualModelRanges != 0 || trace.MatchedSourceCalls != 0 || len(trace.DeliveredEvidence) != 0 || len(trace.MissingEvidence) != 4 {
				t.Fatalf("failed/aborted request views became actual native/citation/body evidence: fragments=%d searches=%d citations=%d trace=%+v", len(ledger.Fragments), len(ledger.Searches), len(ledger.Citations), trace)
			}
		})
	}
}

func TestSemanticCoverageV8FailedSameBodyBeforeSuccessDoesNotConsumeDeduplication(t *testing.T) {
	f := makeLogFixture()
	replay := semanticReplayByName(t, "sourcefix-control-auto")
	baselineLedger := buildSemanticDeliveryLedger(replay.Calls, replay.Requests, replay.OriginalResultID, f)
	baselineTrace := checkLogDelivery(replay.Calls, replay.Requests, replay.OriginalResultID, f)
	if !baselineTrace.Passed || len(baselineLedger.Citations) == 0 {
		t.Fatal("fixture needs actual complete late evidence and citations")
	}
	for _, kind := range []string{"http_error", "aborted"} {
		t.Run(kind, func(t *testing.T) {
			failed := append([]requestReport(nil), replay.Requests...)
			for i := range failed {
				if kind == "http_error" {
					failed[i].ErrorClass = "context_overflow"
				} else {
					failed[i].ErrorClass = ""
					failed[i].StopReason = "aborted"
				}
			}
			// Minimal historical scripted receipts need not declare StopReason.
			// A later successful view still carries the original positive index,
			// source header, body hash and actual range/call binding unchanged.
			success := append([]requestReport(nil), replay.Requests...)
			for i := range success {
				success[i].StopReason = ""
			}
			requests := append(failed, success...)
			ledger := buildSemanticDeliveryLedger(replay.Calls, requests, replay.OriginalResultID, f)
			trace := checkLogDelivery(replay.Calls, requests, replay.OriginalResultID, f)
			if !reflect.DeepEqual(ledger, baselineLedger) || !reflect.DeepEqual(trace, baselineTrace) {
				t.Fatal("a failed same-body prefix consumed deduplication, added phantom citations or changed successful source evidence")
			}
		})
	}
}
