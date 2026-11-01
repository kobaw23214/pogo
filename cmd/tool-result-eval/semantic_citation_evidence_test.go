package main

import (
	"strings"
	"testing"
)

func TestSemanticCitationAvailabilityUsesActualBoundSuccessfulRequest(t *testing.T) {
	f := makeLogFixture()
	registry, id := prepareSearchLog(t, f)
	call, view := semanticReadForTest(t, registry, f, id, "actual", f.Expected.TraceID, 0, 20)
	call.SourceLabel = "S2"
	view.SourceHeader = "[cite:S2 | tool_result | 已保存的工具结果 · search | stored search view]"
	requests := []requestReport{
		{Index: 8, ErrorClass: "transport_error", ToolViews: []toolView{view}},
		{Index: 9, ToolViews: []toolView{view}},
		{Index: 10, ToolViews: []toolView{view}},
	}
	ledger := buildSemanticDeliveryLedger([]toolCallReport{call}, requests, id, f)
	if len(ledger.Citations) != 1 || ledger.Citations[0].RequestIndex != 9 || ledger.Citations[0].Label != "S2" || !semanticCitationValid(ledger.Citations[0], id) {
		t.Fatalf("citation receipt must bind to one actually delivered successful request: %+v", ledger.Citations)
	}
	if len(ledger.Searches) != 1 || ledger.Searches[0].ScannedBytes != len(f.Text) {
		t.Fatal("actual verified scan byte count missing")
	}
	receipt := ledger.Citations[0].ID
	claim := semanticEvidenceClaimForTest(ledger, "scope_metadata", "none", "", nil, receipt)
	if !checkSemanticEvidenceClaim(ledger, claim).Passed {
		t.Fatal("actual citation availability receipt cannot support its own availability")
	}
	claim.EvidenceKind = "body_property"
	if checkSemanticEvidenceClaim(ledger, claim).Passed {
		t.Fatal("citation header was used as evidence of log-body contents")
	}
	claim.EvidenceKind = "scope_metadata"
	ledger.Citations[0].HeaderSHA256 = strings.Repeat("a", 64)
	if checkSemanticEvidenceClaim(ledger, claim).Passed {
		t.Fatal("forged citation header hash accepted")
	}
}

func TestSemanticCitationAvailabilityDoesNotInferLabelsFromBodyOrRegistry(t *testing.T) {
	f := makeLogFixture()
	registry, id := prepareSearchLog(t, f)
	call, view := semanticReadForTest(t, registry, f, id, "actual", f.Expected.TraceID, 0, 20)
	call.SourceLabel = "S2"
	for _, header := range []string{
		"", "[cite:S3 | tool_result | wrong label]", "[cite:S2 | browser | wrong tool]",
		"[cite:S2 | tool_result | bad\nheader]", "[cite:S2 | tool_result | " + strings.Repeat("x", 1024) + "]",
	} {
		view.SourceHeader = header
		ledger := buildSemanticDeliveryLedger([]toolCallReport{call}, []requestReport{{Index: 9, ToolViews: []toolView{view}}}, id, f)
		if len(ledger.Citations) != 0 || len(ledger.Fragments) == 0 {
			t.Fatalf("unobserved/mismatching header established citation availability or lost independent body evidence: %q", header)
		}
	}
	view.SourceHeader = "[cite:S2 | tool_result | actual header]"
	view.BodySHA256 = strings.Repeat("a", 64)
	ledger := buildSemanticDeliveryLedger([]toolCallReport{call}, []requestReport{{Index: 9, ToolViews: []toolView{view}}}, id, f)
	if len(ledger.Citations) != 0 {
		t.Fatal("citation from an unbound native reader body entered the ledger")
	}
}
