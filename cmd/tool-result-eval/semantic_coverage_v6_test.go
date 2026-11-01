package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// These classifications exercise the protocol/host evidence boundary. They do
// not pretend that a scripted grader demonstrates real-language accuracy; the
// unchanged historical answers and explicitly labelled contrasts are replayed
// with the real independent grader separately.
func semanticV6ClaimDecision(request semanticJudgeRequest, kind, quantifier, pattern string, evidenceIDs []string) semanticDecision {
	decision := semanticDecision{Scope: "saved_only", AllClaimsReviewed: true,
		Reviews: []semanticUnitReview{}, AnchorReviews: []semanticAnchorReview{}, Assertions: []semanticAssertion{}}
	for index, unit := range request.Units {
		id := semanticSequenceID("assert", index+1)
		assertion := semanticAssertion{ID: id, UnitID: unit.ID, Quote: unit.Text,
			EvidenceKind: kind, Quantifier: quantifier, Pattern: pattern,
			EvidenceIDs: evidenceIDs, Verdict: "supported", ReasonCode: "supported_by_delivered_text"}
		decision.Assertions = append(decision.Assertions, assertion)
		decision.Reviews = append(decision.Reviews, semanticUnitReview{UnitID: unit.ID, Complete: true, AssertionIDs: []string{id}})
		interpretation := "affirmative"
		if kind == "scope_metadata" {
			interpretation = "scope_metadata"
		}
		for _, anchor := range unit.Anchors {
			decision.AnchorReviews = append(decision.AnchorReviews, semanticAnchorReview{AnchorID: anchor.ID, AssertionID: id, Interpretation: interpretation})
		}
	}
	return decision
}

func semanticV6Selection(t *testing.T, ledger SemanticDeliveryLedger, pattern string) (string, []string) {
	t.Helper()
	for _, page := range ledger.Searches {
		if page.Pattern == pattern && page.ScanComplete && len(page.FragmentIDs) > 0 {
			return page.ID, append([]string(nil), page.FragmentIDs...)
		}
	}
	t.Fatalf("no real delivered search selection for %q", pattern)
	return "", nil
}

func TestSemanticCoverageV6DeliveredSubsetDoesNotBecomeEntireSelection(t *testing.T) {
	for _, coverage := range []string{
		"已交付的20/21处REMOTE_BUSY匹配全部属于无关任务",
		"实际交付的REMOTE_BUSY行均已恢复",
		"已送达的REMOTE_BUSY行全部属于无关任务",
		"All delivered rows matching REMOTE_BUSY are recovered",
	} {
		t.Run(coverage, func(t *testing.T) {
			input := semanticRegressionInput(t, coverage)
			_, fragments := semanticV6Selection(t, input.Ledger, "REMOTE_BUSY")
			judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
				return semanticV6ClaimDecision(request, "body_property", "displayed_subset", "REMOTE_BUSY", fragments)
			}}
			report := runSemanticCoverage(context.Background(), judge, input)
			if !report.Passed || judge.calls != 1 || len(report.ValidationErrors) != 0 {
				t.Fatalf("the entire delivered subset was forced into the unseen selected domain: %+v", report)
			}
		})
	}
	for _, coverage := range []string{
		"REMOTE_BUSY行全部属于无关任务",
		"REMOTE_BUSY的21处匹配已全部交付",
	} {
		t.Run("noSubset-"+coverage, func(t *testing.T) {
			input := semanticRegressionInput(t, coverage)
			_, fragments := semanticV6Selection(t, input.Ledger, "REMOTE_BUSY")
			judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
				return semanticV6ClaimDecision(request, "body_property", "displayed_subset", "REMOTE_BUSY", fragments)
			}}
			report := runSemanticCoverage(context.Background(), judge, input)
			if report.Passed || !semanticRegressionContains(report.ValidationErrors, "affirmative_universal_not_checked_as_all") {
				t.Fatalf("a broad all-set quote acquired a delivered-subset limitation: %+v", report)
			}
		})
	}
}

func TestSemanticCoverageV6SubsetRequiresActualBodyNotScanCount(t *testing.T) {
	input := semanticRegressionInput(t, "已交付的REMOTE_BUSY行全部属于无关任务")
	pageID, _ := semanticV6Selection(t, input.Ledger, "REMOTE_BUSY")
	for i := range input.Ledger.Searches {
		if input.Ledger.Searches[i].ID == pageID {
			// A real successful scan can exist with no actually displayed body.
			// Keep its trusted total while removing its delivery references.
			input.Ledger.Searches[i].FragmentIDs = nil
			input.Ledger.Searches[i].ShownItems = 0
			input.Ledger.Searches[i].Hits = nil
		}
	}
	judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
		return semanticV6ClaimDecision(request, "body_property", "displayed_subset", "REMOTE_BUSY", []string{pageID})
	}}
	report := runSemanticCoverage(context.Background(), judge, input)
	if report.Passed {
		t.Fatal("a complete scan receipt was promoted to the delivered rows' body properties")
	}
	found := false
	for _, guard := range report.AssertionGuards {
		found = found || semanticRegressionContains(guard.Reasons, "displayed_subset_has_no_complete_property_evidence")
	}
	if !found {
		t.Fatalf("missing actual body was not diagnosed by the evidence gate: %+v", report)
	}
}

func TestSemanticCoverageV6SubsetDomainMustBelongToItsOwnOriginalQuote(t *testing.T) {
	input := semanticRegressionInput(t, "已交付的20条另行展示而保存的全部21条REMOTE_BUSY均已恢复")
	_, fragments := semanticV6Selection(t, input.Ledger, "REMOTE_BUSY")
	judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
		decision := semanticV6ClaimDecision(request, "body_property", "displayed_subset", "REMOTE_BUSY", fragments)
		for i := range decision.Assertions {
			// The same unit mentions a delivered subset, but the assertion's
			// original quote specifically claims the entire saved selection.
			decision.Assertions[i].Quote = "保存的全部21条REMOTE_BUSY均已恢复"
		}
		return decision
	}}
	report := runSemanticCoverage(context.Background(), judge, input)
	if report.Passed || !semanticRegressionContains(report.ValidationErrors, "affirmative_universal_not_checked_as_all") {
		t.Fatalf("a separate predicate borrowed a neighbouring subset modifier: %+v", report)
	}
}

func TestSemanticCoverageV6BoundaryAnchorAcceptsActualScanReceiptWithoutScanWord(t *testing.T) {
	for _, mode := range []string{"successfulPage", "bodyOnly", "incompleteScan"} {
		t.Run(mode, func(t *testing.T) {
			input := semanticRegressionInput(t, "对REMOTE_BUSY匹配的完整性不做断言")
			pageID, fragments := semanticV6Selection(t, input.Ledger, "REMOTE_BUSY")
			ids := []string{pageID}
			if mode == "bodyOnly" {
				ids = fragments
			}
			if mode == "incompleteScan" {
				for i := range input.Ledger.Searches {
					if input.Ledger.Searches[i].ID == pageID {
						input.Ledger.Searches[i].ScanComplete = false
					}
				}
			}
			judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
				return semanticV6ClaimDecision(request, "scope_metadata", "none", "REMOTE_BUSY", ids)
			}}
			report := runSemanticCoverage(context.Background(), judge, input)
			if report.Passed != (mode == "successfulPage") {
				t.Fatalf("scope receipt and anchor validators disagreed or accepted unsupported metadata: %+v", report)
			}
			if mode != "successfulPage" && !semanticRegressionContains(report.ValidationErrors, "scope_anchor_has_no_scope_or_scan_receipt") {
				t.Fatalf("a body fragment/unfinished scan became a completed scope receipt: %+v", report)
			}
		})
	}
}

func TestSemanticCoverageV6NegativeBoundaryCannotHideLaterAffirmativePredicate(t *testing.T) {
	for _, test := range []struct {
		coverage string
		wantPass bool
	}{
		{"不能断言所有未读项已恢复", true},
		{"不能断言所有未读项均已恢复", true},
		{"Cannot conclude that all unread rows are recovered", true},
		{"不能确认全量性且未交付项均已恢复", false},
		{"不能断言所有未读项均已恢复而未交付项均已恢复", false},
		{"Cannot conclude that all unread rows are recovered and all unread rows are unrelated", false},
		{"不能断言所有未读项已恢复。未交付项均已恢复", false},
	} {
		t.Run(test.coverage, func(t *testing.T) {
			input := semanticRegressionInput(t, test.coverage)
			pageID, _ := semanticV6Selection(t, input.Ledger, "REMOTE_BUSY")
			judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
				decision := semanticV6ClaimDecision(request, "scope_metadata", "none", "REMOTE_BUSY", []string{pageID})
				for i := range decision.AnchorReviews {
					// Deliberately overoptimistic classification: every original
					// anchor, including the second affirmative clause, is negated.
					decision.AnchorReviews[i].Interpretation = "negated"
				}
				return decision
			}}
			report := runSemanticCoverage(context.Background(), judge, input)
			if report.Passed != test.wantPass {
				t.Fatalf("a remote uncertainty prefix swallowed a later affirmative body property: %+v", report)
			}
			if !test.wantPass && !semanticRegressionContains(report.ValidationErrors, "affirmative_body_property_downgraded_to_scope_metadata") {
				t.Fatalf("mixed business predicate was not retained as a typed classification error: %+v", report)
			}
		})
	}
}

func TestSemanticCoverageV6FiniteNegativeObservationRequiresItsExactDeliveredDomain(t *testing.T) {
	for _, mode := range []string{"selectedFive", "missingOne", "displayedTwenty", "selectedTwentyOne", "wholeSaved", "remote", "ambiguous"} {
		t.Run(mode, func(t *testing.T) {
			coverage := "在保存日志的目标trace完整5条匹配中未见直接请求失败行"
			quantifier := "all"
			input := semanticRegressionInput(t, coverage)
			pattern := semanticActualTargetPattern(t, input.Ledger)
			_, fragments := semanticV6Selection(t, input.Ledger, pattern)
			switch mode {
			case "missingOne":
				fragments = fragments[:len(fragments)-1]
			case "displayedTwenty":
				coverage, quantifier, pattern = "已交付的REMOTE_BUSY行中未见outcome=failed", "displayed_subset", "REMOTE_BUSY"
				_, fragments = semanticV6Selection(t, input.Ledger, pattern)
			case "selectedTwentyOne":
				coverage, pattern = "保存日志的REMOTE_BUSY全部匹配中未见outcome=failed", "REMOTE_BUSY"
				_, fragments = semanticV6Selection(t, input.Ledger, pattern)
			case "wholeSaved":
				coverage, pattern = "完整保存日志中未见直接请求失败行", ""
			case "remote":
				coverage = "远程所有日志中未见直接请求失败行"
			case "ambiguous":
				coverage, quantifier = "未见直接请求失败行", "unknown"
			}
			answer, _ := json.Marshal(map[string]string{"coverage": coverage})
			input.Answer = string(answer)
			judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
				decision := semanticV6ClaimDecision(request, "body_property", quantifier, pattern, fragments)
				if mode == "remote" {
					decision.Scope = "remote_full"
				}
				if mode == "ambiguous" {
					decision.Scope = "unknown"
					decision.Assertions[0].Verdict, decision.Assertions[0].ReasonCode = "unknown", "ambiguous"
				}
				return decision
			}}
			report := runSemanticCoverage(context.Background(), judge, input)
			wantPassed := mode == "selectedFive" || mode == "displayedTwenty"
			if report.Passed != wantPassed {
				t.Fatalf("a bounded negative observation changed domain or filled undisplayed rows: mode=%s %+v", mode, report)
			}
			if mode == "ambiguous" && (judge.calls != 1 || report.ErrorClass != "") {
				t.Fatalf("genuine ambiguity was turned into a retry-until-positive protocol error: %+v", report)
			}
		})
	}
}

func TestSemanticCoverageV6PromptMakesDomainLimitsExplicitWithoutOracle(t *testing.T) {
	judge := &semanticRegressionJudge{decide: semanticScriptedDecision}
	report := runSemanticCoverage(context.Background(), judge, semanticRegressionInput(t, "仅依据本次保存日志范围"))
	if !report.Passed || report.Version != semanticJudgeVersion {
		t.Fatalf("v6 fixed system contract did not reach the grader: %+v", report)
	}
	for _, instruction := range []string{
		"EVERY member of that subset",
		"It is not merely a count assertion.",
		"Missing even one selected match prevents an exhaustive absence verdict.",
		"A short '未见...' with no clear bounded antecedent may remain unknown",
		"A statement that the unread item is unrelated, recovered, harmless or irrelevant is a BODY claim",
		"do not infer listing from reading",
	} {
		if !strings.Contains(judge.system, instruction) {
			t.Fatalf("fixed grader prompt lost a finite-domain or genuine-unknown boundary: %q", instruction)
		}
	}
	for _, forbidden := range []string{logTargetTrace, "PRICE_VERSION_MISMATCH", "14:05:10.410", "REMOTE_BUSY"} {
		if strings.Contains(judge.system, forbidden) {
			t.Fatalf("generic semantic policy acquired a fixture-specific oracle: %q", forbidden)
		}
	}
}
