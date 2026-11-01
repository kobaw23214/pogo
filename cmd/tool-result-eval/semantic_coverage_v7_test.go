package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSemanticCoverageV7CommaRetainsOriginalQualifiedProposition(t *testing.T) {
	qualified := "仅就实际已交付的20条REMOTE_BUSY完整日志，这20条均为service=other-job且outcome=recovered"
	input := semanticRegressionInput(t, qualified)
	units, err := semanticCoverageInventory(input.Answer)
	if err != nil || len(units) != 1 || units[0].Text != qualified || len(units[0].Anchors) != 2 {
		t.Fatalf("comma detached the property from its original bounded antecedent: %v %+v", err, units)
	}
	_, fragments := semanticV6Selection(t, input.Ledger, "REMOTE_BUSY")
	judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
		return semanticV6ClaimDecision(request, "body_property", "displayed_subset", "REMOTE_BUSY", fragments)
	}}
	report := runSemanticCoverage(context.Background(), judge, input)
	if !report.Passed || report.StreamCalls != 1 || report.Version != semanticJudgeVersion {
		t.Fatalf("an exact original qualifier-and-property quote was rejected despite actual complete shown rows: %+v", report)
	}
	if !strings.Contains(judge.seen.Units[0].Text, "，") || !strings.Contains(judge.system, "original contiguous qualifier-and-property span") {
		t.Fatal("the grader did not receive the original comma/span and explicit quotation boundary")
	}
}

func TestSemanticCoverageV7SentenceBoundariesRemainBounded(t *testing.T) {
	for _, separator := range []string{"；", ";", "。", ". ", "\n"} {
		t.Run(separator, func(t *testing.T) {
			coverage := "保存文本2,881,729字节，范围未知" + separator + "仅讨论已交付内容"
			answer, _ := json.Marshal(map[string]string{"coverage": coverage})
			units, err := semanticCoverageInventory(string(answer))
			if err != nil || len(units) != 2 || units[0].Text != "保存文本2,881,729字节，范围未知" {
				t.Fatalf("sentence boundary changed or commas/numeric separators were discarded: %q %v %+v", separator, err, units)
			}
		})
	}
}

func TestSemanticCoverageV7SeparateAllSelectionPredicateCannotBorrowCommaQualifier(t *testing.T) {
	first := "已交付的20条REMOTE_BUSY均已恢复"
	second := "保存的全部21条REMOTE_BUSY也均已恢复"
	input := semanticRegressionInput(t, first+"，但"+second)
	_, fragments := semanticV6Selection(t, input.Ledger, "REMOTE_BUSY")
	for _, mode := range []string{"separateAllDomain", "borrowedSubsetDomain"} {
		t.Run(mode, func(t *testing.T) {
			judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
				if len(request.Units) != 1 {
					t.Fatal("the two propositions must exercise a shared comma-containing unit")
				}
				unit := request.Units[0]
				assertions := []semanticAssertion{
					{ID: "displayed", UnitID: unit.ID, Quote: first, EvidenceKind: "body_property", Quantifier: "displayed_subset", Pattern: "REMOTE_BUSY", EvidenceIDs: fragments, Verdict: "supported", ReasonCode: "visible_subset_only"},
					{ID: "selected", UnitID: unit.ID, Quote: second, EvidenceKind: "body_property", Quantifier: "all", Pattern: "REMOTE_BUSY", EvidenceIDs: fragments, Verdict: "supported", ReasonCode: "supported_by_complete_selection"},
				}
				if mode == "borrowedSubsetDomain" {
					assertions[1].Quantifier = "displayed_subset"
				}
				decision := semanticDecision{Scope: "saved_only", AllClaimsReviewed: true,
					Reviews:    []semanticUnitReview{{UnitID: unit.ID, Complete: true, AssertionIDs: []string{"displayed", "selected"}}},
					Assertions: assertions, AnchorReviews: []semanticAnchorReview{}}
				firstAnchors := len(semanticQuantifierRe.FindAllStringIndex(first, -1))
				for index, anchor := range unit.Anchors {
					id := "selected"
					if index < firstAnchors {
						id = "displayed"
					}
					decision.AnchorReviews = append(decision.AnchorReviews, semanticAnchorReview{AnchorID: anchor.ID, AssertionID: id, Interpretation: "affirmative"})
				}
				return decision
			}}
			report := runSemanticCoverage(context.Background(), judge, input)
			if report.Passed || report.Decision == nil || len(report.Decision.Assertions) != 2 {
				t.Fatalf("a true delivered-subset predicate erased an independent all-selected predicate: %+v", report)
			}
			if mode == "borrowedSubsetDomain" {
				if !semanticRegressionContains(report.ValidationErrors, "affirmative_universal_not_checked_as_all") {
					t.Fatalf("a separate all-21 quote borrowed the other predicate's subset modifier: %+v", report)
				}
			} else {
				found := false
				for _, guard := range report.AssertionGuards {
					found = found || guard.AssertionID == "selected" && semanticRegressionContains(guard.Reasons, "not_all_domain_properties_were_delivered_as_complete_lines")
				}
				if !found {
					t.Fatalf("the independent saved-selection claim lost its genuine 20/21 evidence failure: %+v", report)
				}
			}
		})
	}
}
