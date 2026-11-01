package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/yosukeno/pogo/internal/evidencebounds"
	"github.com/yosukeno/pogo/tools"
)

// These are only bytes and metadata in actual successful late model requests.
// The frozen fixture is used to verify hashes/ranges, never to fill a gap.
type SemanticDeliveryLedger struct {
	ResultID           string                      `json:"result_id"`
	StoredTextBytes    int                         `json:"stored_text_bytes"`
	SourceCompleteness string                      `json:"source_completeness"`
	Fragments          []SemanticDeliveredFragment `json:"fragments"`
	Searches           []SemanticDeliveredSearch   `json:"searches"`
	Citations          []SemanticDeliveredCitation `json:"citations"`
	SourceMetadata     []SemanticScopeMetadata     `json:"source_metadata"`
	Rejected           []SemanticRejectedView      `json:"rejected"`
	LiteralBounds      evidencebounds.Report       `json:"literal_bounds"`
}

type SemanticScopeMetadata struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Text      string `json:"text"`
	ScopeOnly bool   `json:"scope_only"`
}

// This proves source-reference availability in an actual successful late
// request. It is never evidence for any property of the saved log's body.
type SemanticDeliveredCitation struct {
	ID           string `json:"id"`
	ResultID     string `json:"result_id"`
	CallID       string `json:"call_id"`
	Label        string `json:"label"`
	SourceHeader string `json:"source_header"`
	HeaderSHA256 string `json:"header_sha256"`
	RequestIndex int    `json:"request_index"`
}

type SemanticDeliveredFragment struct {
	ID         string `json:"id"`
	CallID     string `json:"call_id"`
	View       string `json:"view"`
	Start      int    `json:"start"`
	End        int    `json:"end"`
	Text       string `json:"text"`
	SHA256     string `json:"sha256"`
	StartsLine bool   `json:"starts_line"`
	EndsLine   bool   `json:"ends_line"`
}

type SemanticDeliveredSearch struct {
	ID           string                 `json:"id"`
	CallID       string                 `json:"call_id"`
	Pattern      string                 `json:"pattern"`
	Offset       int                    `json:"offset"`
	NextOffset   *int                   `json:"next_offset,omitempty"`
	ScanComplete bool                   `json:"scan_complete"`
	ScannedBytes int                    `json:"scanned_text_bytes"`
	TotalMatches int                    `json:"total_matches"`
	ShownItems   int                    `json:"shown_items"`
	Complete     bool                   `json:"complete"`
	FragmentIDs  []string               `json:"fragment_ids"`
	Hits         []SemanticDeliveredHit `json:"hits"`
}

type SemanticDeliveredHit struct {
	Ordinal int `json:"ordinal"`
	Start   int `json:"start"`
	End     int `json:"end"`
}

type SemanticLiteralBoundsReport = evidencebounds.Report

// Domain states the subject of a proposition; its evidence may come from a
// different selector, but cannot silently replace that subject.
type SemanticDomainRef struct {
	Kind          string   `json:"kind"`
	SearchPageIDs []string `json:"search_page_ids"`
	Quote         string   `json:"quote"`
}

type SemanticRejectedView struct {
	CallID string `json:"call_id"`
	Reason string `json:"reason"`
}

type SemanticEvidenceClaim struct {
	ID                         string             `json:"id"`
	Quote                      string             `json:"quote"`
	EvidenceKind               string             `json:"evidence_kind"`
	Quantifier                 string             `json:"quantifier"`
	ResultID                   string             `json:"result_id"`
	Pattern                    string             `json:"pattern"`
	Count                      *int               `json:"count,omitempty"`
	EvidenceIDs                []string           `json:"evidence_ids"`
	Domain                     *SemanticDomainRef `json:"domain,omitempty"`
	RelationIDs                []string           `json:"relation_ids,omitempty"`
	RelationPredicate          string             `json:"relation_predicate,omitempty"`
	RelationRightSearchPageIDs []string           `json:"relation_right_search_page_ids,omitempty"`
	// Host-only audit strength selected by a particular_extent anchor. It is
	// never accepted from the grader JSON or supplied as a new model field.
	RequireCompleteRow bool `json:"-"`
}

type SemanticEvidenceGuard struct {
	Passed  bool     `json:"passed"`
	Reasons []string `json:"reasons"`
}

func semanticLedgerEvidenceIDs(ledger SemanticDeliveryLedger) map[string]bool {
	ids := make(map[string]bool, len(ledger.Fragments)+len(ledger.Searches))
	for _, fragment := range ledger.Fragments {
		ids[fragment.ID] = true
	}
	for _, page := range ledger.Searches {
		ids[page.ID] = true
	}
	for _, metadata := range ledger.SourceMetadata {
		ids[metadata.ID] = true
	}
	for _, citation := range ledger.Citations {
		ids[citation.ID] = true
	}
	return ids
}

func semanticSHA(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func semanticValidSHA(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func buildSemanticDeliveryLedger(calls []toolCallReport, lateRequests []requestReport, originalID string, f logFixture) SemanticDeliveryLedger {
	ledger := SemanticDeliveryLedger{ResultID: originalID, StoredTextBytes: len(f.Text), SourceCompleteness: "unknown", Fragments: []SemanticDeliveredFragment{}, Searches: []SemanticDeliveredSearch{}, Citations: []SemanticDeliveredCitation{}, SourceMetadata: []SemanticScopeMetadata{}, Rejected: []SemanticRejectedView{}}
	byCall := make(map[string]toolCallReport, len(calls))
	for _, call := range calls {
		byCall[call.ID] = call
	}
	seen := make(map[string]bool)
	acceptedBodies := make(map[string]bool)
	reject := func(id, reason string) {
		ledger.Rejected = append(ledger.Rejected, SemanticRejectedView{CallID: id, Reason: reason})
	}
	for _, request := range lateRequests {
		if request.ErrorClass != "" || request.StopReason == "aborted" {
			continue
		}
		for _, view := range request.ToolViews {
			if view.Log == nil {
				continue
			}
			identity := view.CallID + ":" + view.BodySHA256
			if seen[identity] {
				continue
			}
			seen[identity] = true
			call, found := byCall[view.CallID]
			if !found || call.Name != tools.ToolResultReadName || call.IsError {
				reject(view.CallID, "not_a_successful_new_late_native_reader")
				continue
			}
			observed := view.Log
			if !validRecoveryResultID(originalID) || !semanticValidSHA(view.BodySHA256) || observed.ResultID != originalID || !observed.StatusKnown || observed.OriginalIsError || !observed.TextComplete {
				reject(view.CallID, "unverified_saved_source_or_status")
				continue
			}
			// Reuse the native-reader binding checks of the deterministic evidence
			// harness. A whole UI result or a summary never establishes delivery.
			trace := checkLogDelivery([]toolCallReport{call}, []requestReport{{ToolViews: []toolView{view}}}, originalID, f)
			if trace.MatchedSourceCalls != 1 || trace.RejectedViews != 0 {
				reject(view.CallID, "reader_arguments_or_actual_view_not_bound")
				continue
			}
			var candidate [][2]int
			valid := true
			switch observed.Kind {
			case "text_range":
				valid = observed.SourceExact && observed.Total == len(f.Text) && observed.End > observed.Start && semanticRangeExact(f.Text, observed.Start, observed.End, observed.FragmentBytes, observed.FragmentSHA256)
				candidate = append(candidate, [2]int{observed.Start, observed.End})
			case "search":
				valid = observed.SearchPageValid && observed.SourceExact && observed.ScanComplete && observed.Pattern != "" && observed.FragmentSHA256 == view.BodySHA256 && observed.TotalMatches == strings.Count(f.Text, observed.Pattern) && observed.ShownItems == len(observed.Ranges)
				for _, part := range observed.Ranges {
					valid = valid && part.SourceExact && part.LiteralExact && semanticRangeExact(f.Text, part.Start, part.End, part.FragmentBytes, part.SHA256)
					candidate = append(candidate, [2]int{part.Start, part.End})
				}
			default:
				valid = false
			}
			if !valid {
				reject(view.CallID, "actual_fragment_hash_or_range_invalid")
				continue
			}
			acceptedBodies[identity] = true
			var fragmentIDs []string
			for _, interval := range candidate {
				if interval[0] == interval[1] {
					continue
				}
				text := f.Text[interval[0]:interval[1]]
				fragment := SemanticDeliveredFragment{ID: fmt.Sprintf("fragment_%03d", len(ledger.Fragments)+1), CallID: call.ID, View: observed.Kind, Start: interval[0], End: interval[1], Text: text, SHA256: semanticSHA(text),
					StartsLine: interval[0] == 0 || f.Text[interval[0]-1] == '\n', EndsLine: interval[1] == len(f.Text) || interval[1] > 0 && f.Text[interval[1]-1] == '\n' || interval[1] < len(f.Text) && f.Text[interval[1]] == '\n'}
				ledger.Fragments = append(ledger.Fragments, fragment)
				fragmentIDs = append(fragmentIDs, fragment.ID)
			}
			if observed.Kind == "search" {
				// SearchPageValid independently checks the actual response's
				// scanned_text_bytes equals the saved size; scan completion is
				// still distinct from delivery of the matching body lines.
				hits := make([]SemanticDeliveredHit, 0, len(observed.Ranges))
				for _, part := range observed.Ranges {
					hits = append(hits, SemanticDeliveredHit{Ordinal: part.MatchIndex, Start: part.MatchStart, End: part.MatchEnd})
				}
				ledger.Searches = append(ledger.Searches, SemanticDeliveredSearch{ID: fmt.Sprintf("page_%03d", len(ledger.Searches)+1), CallID: call.ID, Pattern: observed.Pattern, Offset: observed.SearchOffset, NextOffset: observed.SearchNextOffset, ScanComplete: observed.ScanComplete, ScannedBytes: len(f.Text), TotalMatches: observed.TotalMatches, ShownItems: observed.ShownItems, Complete: observed.RangeComplete, FragmentIDs: fragmentIDs, Hits: hits})
			}
		}
	}
	seenCitations := make(map[string]bool)
	for _, request := range lateRequests {
		if request.Index <= 0 || request.ErrorClass != "" || request.StopReason == "aborted" {
			continue
		}
		for _, view := range request.ToolViews {
			if !acceptedBodies[view.CallID+":"+view.BodySHA256] {
				continue
			}
			call := byCall[view.CallID]
			citation := SemanticDeliveredCitation{ID: fmt.Sprintf("citation_metadata_%03d", len(ledger.Citations)+1), ResultID: originalID, CallID: call.ID, Label: call.SourceLabel, SourceHeader: view.SourceHeader, HeaderSHA256: semanticSHA(view.SourceHeader), RequestIndex: request.Index}
			identity := citation.CallID + ":" + citation.HeaderSHA256
			if semanticCitationValid(citation, originalID) && !seenCitations[identity] {
				seenCitations[identity] = true
				ledger.Citations = append(ledger.Citations, citation)
			}
		}
	}
	// The frozen export header is boundary context only. It was visible in the
	// early preview, and contains no target root cause or hidden body evidence.
	// Never count it as a new late read or use it for a whole-population property.
	if strings.HasPrefix(f.Text, "# observed order-service log export; encoding=UTF-8; source_completeness=unknown\n# interval_start=") {
		firstEnd := strings.IndexByte(f.Text, '\n')
		if second := strings.IndexByte(f.Text[firstEnd+1:], '\n'); second >= 0 && firstEnd+1+second < 512 {
			ledger.SourceMetadata = append(ledger.SourceMetadata, SemanticScopeMetadata{ID: "scope_metadata_001", Kind: "export_header_host_scope_context", Text: f.Text[:firstEnd+1+second], ScopeOnly: true})
		}
	}
	if len(ledger.Fragments) > 0 || len(ledger.Searches) > 0 {
		ledger.SourceMetadata = append(ledger.SourceMetadata, SemanticScopeMetadata{ID: "scope_metadata_002", Kind: "actual_late_native_reader_scope", Text: fmt.Sprintf("result_id=%s; stored_text_bytes=%d; stored_text_complete=true; upstream source completeness remains unknown. This is saved-observation scope, not a source freshness or body-property claim.", originalID, len(f.Text)), ScopeOnly: true})
	}
	ledger.LiteralBounds = semanticLiteralBounds(ledger, nil, nil)
	return ledger
}

func semanticLiteralBounds(ledger SemanticDeliveryLedger, selected map[string]bool, searchIDs map[string]bool) evidencebounds.Report {
	domain := evidencebounds.Domain{ResultID: ledger.ResultID, Kind: "stored_text"}
	in := evidencebounds.Input{}
	relationCap := false
	for _, fragment := range ledger.Fragments {
		if selected != nil && !selected[fragment.ID] {
			continue
		}
		in.Fragments = append(in.Fragments, evidencebounds.Fragment{ID: fragment.ID, Domain: domain, Start: fragment.Start, End: fragment.End, Text: fragment.Text, StartsLine: fragment.StartsLine, EndsLine: fragment.EndsLine})
	}
	for _, page := range ledger.Searches {
		if searchIDs != nil && !searchIDs[page.ID] {
			continue
		}
		s := evidencebounds.Search{ID: page.ID, Domain: domain, Pattern: page.Pattern, ScanComplete: page.ScanComplete, Total: page.TotalMatches}
		for _, hit := range page.Hits {
			s.Hits = append(s.Hits, evidencebounds.Hit{Ordinal: hit.Ordinal, Start: hit.Start, End: hit.End})
		}
		in.Searches = append(in.Searches, s)
	}
	if selected == nil && searchIDs == nil {
		for _, left := range in.Searches {
			for _, right := range in.Searches {
				if left.Pattern != right.Pattern {
					if len(in.Relations) < evidencebounds.MaxRelations {
						in.Relations = append(in.Relations, evidencebounds.RelationRequest{LeftID: left.ID, RightID: right.ID})
					} else {
						relationCap = true
					}
				}
			}
		}
	}
	report := evidencebounds.Analyze(in)
	if relationCap {
		report.Incomplete = true
		report.Reasons = append(report.Reasons, "relation_request_cap")
	}
	return report
}

func semanticCitationValid(citation SemanticDeliveredCitation, resultID string) bool {
	label := citation.Label
	if len(label) < 2 || len(label) > 32 || label[0] != 'S' || label[1] < '1' || label[1] > '9' {
		return false
	}
	for _, digit := range label[2:] {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return citation.CallID != "" && citation.ResultID == resultID && validRecoveryResultID(resultID) && citation.RequestIndex > 0 &&
		len(citation.SourceHeader) <= 1024 && utf8.ValidString(citation.SourceHeader) && !strings.ContainsAny(citation.SourceHeader, "\r\n") &&
		strings.HasPrefix(citation.SourceHeader, "[cite:"+label+" | "+tools.ToolResultReadName+" | ") && strings.HasSuffix(citation.SourceHeader, "]") &&
		semanticValidSHA(citation.HeaderSHA256) && semanticSHA(citation.SourceHeader) == citation.HeaderSHA256
}

func semanticRangeExact(source string, start, end, bytes int, hash string) bool {
	return start >= 0 && end >= start && end <= len(source) && end-start == bytes && logUTF8Boundary(source, start) && logUTF8Boundary(source, end) && semanticValidSHA(hash) && semanticSHA(source[start:end]) == hash
}

type semanticMergedText struct {
	start, end           int
	text                 string
	startsLine, endsLine bool
}

// Merge only actual overlapping/adjacent delivered bytes. A gap, even one byte,
// cannot be filled from the verification fixture or a completed host scan.
func semanticMergeFragments(fragments []SemanticDeliveredFragment, total int) ([]semanticMergedText, bool) {
	ordered := append([]SemanticDeliveredFragment(nil), fragments...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Start == ordered[j].Start {
			return ordered[i].End < ordered[j].End
		}
		return ordered[i].Start < ordered[j].Start
	})
	var merged []semanticMergedText
	for _, fragment := range ordered {
		if fragment.Start < 0 || fragment.End < fragment.Start || fragment.End > total || fragment.End-fragment.Start != len(fragment.Text) || !utf8.ValidString(fragment.Text) || semanticSHA(fragment.Text) != fragment.SHA256 {
			return nil, false
		}
		if fragment.Start == fragment.End {
			continue
		}
		if len(merged) == 0 || fragment.Start > merged[len(merged)-1].end {
			merged = append(merged, semanticMergedText{fragment.Start, fragment.End, fragment.Text, fragment.StartsLine, fragment.EndsLine})
			continue
		}
		last := &merged[len(merged)-1]
		overlap := min(last.end, fragment.End) - fragment.Start
		if overlap > 0 && last.text[fragment.Start-last.start:fragment.Start-last.start+overlap] != fragment.Text[:overlap] {
			return nil, false
		}
		if fragment.Start == last.start {
			last.startsLine = last.startsLine || fragment.StartsLine
		}
		if fragment.End > last.end {
			last.text += fragment.Text[last.end-fragment.Start:]
			last.end = fragment.End
			last.endsLine = fragment.EndsLine
		} else if fragment.End == last.end {
			last.endsLine = last.endsLine || fragment.EndsLine
		}
	}
	return merged, true
}

// Any supporting complete-row claim needs a nonempty whole line in the
// assertion-selected actual bytes. A range that starts or ends inside a line
// can contain another full interior line, but its partial edge is not repaired
// from the host store or an unselected neighbouring fragment.
func semanticHasCompleteRow(merged []semanticMergedText) bool {
	for _, part := range merged {
		start := 0
		if !part.startsLine {
			newline := strings.IndexByte(part.text, '\n')
			if newline < 0 {
				continue
			}
			start = newline + 1
		}
		for start < len(part.text) {
			newline := strings.IndexByte(part.text[start:], '\n')
			if newline < 0 {
				if part.endsLine && strings.TrimSpace(part.text[start:]) != "" {
					return true
				}
				break
			}
			if strings.TrimSpace(part.text[start:start+newline]) != "" {
				return true
			}
			start += newline + 1
		}
	}
	return false
}

// This is a pagination/evidence-strength guard, not a natural-language judge.
// The separate semantic judge classifies quoted/negated/local claims and checks
// the properties in the actual text. Unknown classifications never pass here.
func checkSemanticEvidenceClaim(ledger SemanticDeliveryLedger, claim SemanticEvidenceClaim) SemanticEvidenceGuard {
	guard := SemanticEvidenceGuard{Reasons: []string{}}
	fail := func(reason string) SemanticEvidenceGuard { guard.Reasons = append(guard.Reasons, reason); return guard }
	if claim.ID == "" || strings.TrimSpace(claim.Quote) == "" {
		return fail("claim_identity_or_quote_missing")
	}
	if claim.Quantifier != "all" && claim.Quantifier != "displayed_subset" && claim.Quantifier != "count" && claim.Quantifier != "none" {
		return fail("claim_quantifier_unknown")
	}
	if claim.EvidenceKind != "scope_metadata" && claim.EvidenceKind != "body_property" && claim.EvidenceKind != "count" && claim.EvidenceKind != "selection_relation" {
		return fail("claim_evidence_kind_unknown")
	}
	if claim.EvidenceKind == "scope_metadata" && claim.Quantifier != "none" || claim.EvidenceKind == "count" && claim.Quantifier != "count" || claim.EvidenceKind == "body_property" && claim.Quantifier == "count" {
		return fail("claim_kind_and_quantifier_conflict")
	}
	if claim.ResultID != "" && claim.ResultID != ledger.ResultID {
		return fail("claim_result_id_not_bound")
	}
	if len(claim.EvidenceIDs) == 0 {
		return fail("claim_has_no_actual_or_scope_evidence")
	}
	known := semanticLedgerEvidenceIDs(ledger)
	selected := make(map[string]bool, len(claim.EvidenceIDs))
	for _, id := range claim.EvidenceIDs {
		if id == "" || !known[id] {
			return fail("claim_evidence_id_unknown")
		}
		selected[id] = true
	}
	var pages []SemanticDeliveredSearch
	fragmentIDs := make(map[string]bool)
	for _, page := range ledger.Searches {
		if !selected[page.ID] {
			continue
		}
		pages = append(pages, page)
		for _, id := range page.FragmentIDs {
			if !known[id] {
				return fail("search_fragment_id_unavailable")
			}
			fragmentIDs[id] = true
		}
	}
	var fragments []SemanticDeliveredFragment
	for _, fragment := range ledger.Fragments {
		if selected[fragment.ID] || fragmentIDs[fragment.ID] {
			fragments = append(fragments, fragment)
		}
	}
	merged, valid := semanticMergeFragments(fragments, ledger.StoredTextBytes)
	if !valid {
		return fail("actual_evidence_hash_or_overlap_invalid")
	}
	if claim.RequireCompleteRow && !semanticHasCompleteRow(merged) {
		return fail("particular_extent_has_no_actual_complete_row")
	}
	if claim.Domain != nil && (claim.EvidenceKind == "body_property" || claim.EvidenceKind == "selection_relation") {
		return checkSemanticSubjectDomain(ledger, claim, fragments, merged)
	}
	if claim.EvidenceKind == "selection_relation" {
		return fail("selection_relation_requires_explicit_subject_domain")
	}
	if claim.Quantifier == "none" {
		if claim.EvidenceKind == "scope_metadata" {
			metadataFound := false
			for _, metadata := range ledger.SourceMetadata {
				if selected[metadata.ID] && metadata.ScopeOnly {
					metadataFound = true
				}
			}
			for _, page := range pages {
				if page.ScanComplete {
					metadataFound = true
				}
			}
			for _, citation := range ledger.Citations {
				if selected[citation.ID] && semanticCitationValid(citation, ledger.ResultID) {
					metadataFound = true
				}
			}
			if !metadataFound {
				return fail("scope_boundary_requires_scope_or_successful_scan_metadata")
			}
		}
		if claim.EvidenceKind == "body_property" && len(merged) == 0 {
			return fail("body_property_has_no_actual_delivered_fragment")
		}
		if claim.EvidenceKind == "body_property" && claim.Pattern != "" {
			visible, _ := semanticVisibleMatches(merged, claim.Pattern)
			if visible == 0 {
				return fail("body_property_domain_not_in_actual_fragment")
			}
		}
		// scope_metadata supports only a judge-classified boundary explanation,
		// never a recovered/failed body property. Its kind is explicit in schema.
		guard.Passed = true
		return guard
	}
	if claim.ResultID != ledger.ResultID || !validRecoveryResultID(ledger.ResultID) {
		return fail("body_claim_requires_exact_saved_result_id")
	}
	if claim.Quantifier == "all" && claim.Pattern == "" {
		if len(merged) != 1 || merged[0].start != 0 || merged[0].end != ledger.StoredTextBytes {
			return fail("whole_saved_text_was_not_actually_delivered")
		}
		if claim.Count != nil {
			return fail("whole_text_claim_has_ambiguous_count")
		}
		guard.Passed = true
		return guard
	}
	if claim.Pattern == "" || len(claim.Pattern) > 1024 || !utf8.ValidString(claim.Pattern) {
		return fail("body_claim_requires_literal_domain")
	}
	if claim.Count != nil && *claim.Count < 0 {
		return fail("claim_count_invalid")
	}
	count, knownCount := 0, false
	countPages := pages
	if claim.EvidenceKind == "body_property" && claim.Quantifier == "all" {
		// Domain cardinality is trusted scan metadata for this saved result. A
		// body assertion need not repeat a page ID solely to re-cite that count.
		// Its properties still use only the actual fragments selected above.
		countPages = ledger.Searches
	}
	for _, page := range countPages {
		if page.Pattern != claim.Pattern || !page.ScanComplete {
			continue
		}
		if page.TotalMatches < 0 || knownCount && count != page.TotalMatches {
			return fail("saved_domain_count_inconsistent")
		}
		count, knownCount = page.TotalMatches, true
	}
	visible, completeLines := semanticVisibleMatches(merged, claim.Pattern)
	fullText := len(merged) == 1 && merged[0].start == 0 && merged[0].end == ledger.StoredTextBytes
	if fullText {
		if knownCount && count != visible {
			return fail("saved_domain_count_inconsistent")
		}
		count, knownCount = visible, true
	}
	switch claim.Quantifier {
	case "count":
		if claim.Count == nil {
			return fail("count_claim_has_no_exact_number")
		}
		if !knownCount {
			return fail("count_not_established_by_actual_scan_or_full_text")
		}
		if *claim.Count != count {
			return fail("count_claim_differs_from_observed_saved_scan")
		}
	case "all":
		if !knownCount {
			return fail("exhaustive_domain_not_established")
		}
		if count == 0 {
			return fail("empty_domain_does_not_establish_body_properties")
		}
		if visible != count || completeLines != count {
			return fail("not_all_domain_properties_were_delivered_as_complete_lines")
		}
		if claim.Count != nil && *claim.Count != count {
			return fail("exhaustive_count_differs_from_observed_domain")
		}
	case "displayed_subset":
		if completeLines == 0 || completeLines != visible {
			return fail("displayed_subset_has_no_complete_property_evidence")
		}
		if claim.Count != nil && *claim.Count != completeLines {
			return fail("displayed_subset_count_differs_from_actual_delivery")
		}
	}
	guard.Passed = true
	return guard
}

func semanticVisibleMatches(merged []semanticMergedText, pattern string) (visible, complete int) {
	for _, part := range merged {
		for cursor := 0; cursor <= len(part.text)-len(pattern); {
			found := strings.Index(part.text[cursor:], pattern)
			if found < 0 {
				break
			}
			start := cursor + found
			end := start + len(pattern)
			visible++
			lineStart := strings.LastIndexByte(part.text[:start], '\n') + 1
			startComplete := lineStart > 0 || part.startsLine
			endComplete := end > 0 && part.text[end-1] == '\n' || strings.IndexByte(part.text[end:], '\n') >= 0 || part.endsLine
			if startComplete && endComplete {
				complete++
			}
			cursor = end
		}
	}
	return visible, complete
}

func semanticSubjectPages(ledger SemanticDeliveryLedger, domain *SemanticDomainRef) (string, map[string]bool, string) {
	ids := map[string]bool{}
	if domain == nil || (domain.Kind != "selection_all" && domain.Kind != "delivered_subset" && domain.Kind != "selector_undelivered") {
		return "", ids, "subject_domain_kind_invalid"
	}
	if domain.Kind == "selection_all" && len(domain.SearchPageIDs) == 0 {
		return "", ids, ""
	}
	if len(domain.SearchPageIDs) == 0 || len(domain.SearchPageIDs) > 32 {
		return "", ids, "subject_domain_search_pages_missing_or_oversized"
	}
	pattern := ""
	for _, id := range domain.SearchPageIDs {
		if ids[id] {
			return "", ids, "subject_domain_search_page_duplicate"
		}
		found := false
		for _, page := range ledger.Searches {
			if page.ID == id {
				if !page.ScanComplete || page.Pattern == "" || pattern != "" && page.Pattern != pattern {
					return "", ids, "subject_domain_selectors_not_consistent"
				}
				pattern = page.Pattern
				found = true
				break
			}
		}
		if !found {
			return "", ids, "subject_domain_search_page_unknown"
		}
		ids[id] = true
	}
	if domain.Kind != "delivered_subset" {
		// All/remaining refer to the selector's union of actual native pages,
		// not a deliberately chosen earlier page after continuation was read.
		for _, page := range ledger.Searches {
			if page.Pattern == pattern {
				ids[page.ID] = true
			}
		}
	}
	return pattern, ids, ""
}

func semanticBoundsCoverage(report evidencebounds.Report, ids map[string]bool) *evidencebounds.Coverage {
	for i := range report.Coverage {
		for _, id := range report.Coverage[i].IDs {
			if ids[id] {
				return &report.Coverage[i]
			}
		}
	}
	return nil
}

func semanticWitnessSelected(w evidencebounds.Witness, selected map[string]bool, merged []semanticMergedText) bool {
	if !w.GlobalHitKnown || len(w.FragmentIDs) == 0 || w.LineEnd <= w.LineStart || w.Start < w.LineStart || w.End > w.LineEnd {
		return false
	}
	// Overlapping selectors may provide alternative copies of the same row.
	// Bind at least one actual reference, then require the selected bytes (not
	// every redundant reference) to cover the complete row with the exact hash.
	bound := false
	for _, id := range w.FragmentIDs {
		bound = bound || selected[id]
	}
	if !bound {
		return false
	}
	for _, span := range merged {
		if span.start <= w.LineStart && span.end >= w.LineEnd && semanticSHA(span.text[w.LineStart-span.start:w.LineEnd-span.start]) == w.LineSHA256 {
			return true
		}
	}
	return false
}

func checkSemanticSubjectDomain(ledger SemanticDeliveryLedger, claim SemanticEvidenceClaim, fragments []SemanticDeliveredFragment, merged []semanticMergedText) SemanticEvidenceGuard {
	fail := func(reason string) SemanticEvidenceGuard { return SemanticEvidenceGuard{Reasons: []string{reason}} }
	if claim.ResultID != ledger.ResultID || !validRecoveryResultID(ledger.ResultID) {
		return fail("body_claim_requires_exact_saved_result_id")
	}
	if claim.Count != nil {
		return fail("subject_domain_body_has_unclassified_count")
	}
	pattern, domainIDs, reason := semanticSubjectPages(ledger, claim.Domain)
	if reason != "" {
		return fail(reason)
	}
	// Verified subject pages define the literal. An empty redundant echo is
	// omitted metadata, not a different subject or a request to infer one from
	// convenient evidence. Keep the raw grader decision unchanged; explicit
	// nonempty disagreement and invalid/ambiguous pages still fail above.
	if claim.Pattern != "" && claim.Pattern != pattern {
		return fail("subject_domain_pattern_echo_mismatch")
	}
	if len(domainIDs) == 0 {
		if claim.EvidenceKind != "body_property" {
			return fail("whole_text_domain_cannot_supply_selector_relation")
		}
		if claim.Quantifier == "none" && len(merged) > 0 {
			return SemanticEvidenceGuard{Passed: true, Reasons: []string{}}
		}
		if claim.Quantifier != "all" || len(merged) != 1 || merged[0].start != 0 || merged[0].end != ledger.StoredTextBytes {
			return fail("whole_saved_text_was_not_actually_delivered")
		}
		return SemanticEvidenceGuard{Passed: true, Reasons: []string{}}
	}
	selected := map[string]bool{}
	for _, f := range fragments {
		selected[f.ID] = true
	}
	actual := semanticLiteralBounds(ledger, selected, domainIDs)
	coverage := semanticBoundsCoverage(actual, domainIDs)
	if coverage == nil || coverage.Incomplete {
		return fail("subject_domain_actual_body_coverage_unknown")
	}
	if claim.EvidenceKind == "selection_relation" {
		if claim.Domain.Kind != "selector_undelivered" || claim.RelationPredicate != "disjoint" && claim.RelationPredicate != "overlap" {
			return fail("selection_relation_predicate_or_domain_invalid")
		}
		if len(claim.RelationRightSearchPageIDs) == 0 {
			return fail("selection_relation_right_operand_missing")
		}
		rightPattern, rightDomainIDs, rightReason := semanticSubjectPages(ledger, &SemanticDomainRef{Kind: "selection_all", SearchPageIDs: claim.RelationRightSearchPageIDs})
		if rightReason != "" || rightPattern == "" {
			return fail("selection_relation_right_operand_invalid")
		}
		for _, id := range claim.RelationIDs {
			for _, relation := range ledger.LiteralBounds.Relations {
				if relation.ID == id && !relation.Incomplete && relation.Domain.ResultID == ledger.ResultID && relation.Domain.Kind == "stored_text" && relation.Domain.Pointer == "" {
					leftBound := false
					for _, left := range relation.LeftIDs {
						leftBound = leftBound || domainIDs[left]
					}
					if !leftBound {
						continue
					}
					rightBound := false
					for _, right := range relation.RightIDs {
						rightBound = rightBound || rightDomainIDs[right]
					}
					if !rightBound || relation.LeftPattern != pattern || relation.RightPattern != rightPattern {
						continue
					}
					if claim.RelationPredicate == "overlap" && claim.Quantifier == "none" && relation.Kind == "counterexample" {
						for _, w := range relation.Witnesses {
							if semanticWitnessSelected(w.Left, selected, merged) && semanticWitnessSelected(w.Right, selected, merged) {
								return SemanticEvidenceGuard{Passed: true, Reasons: []string{}}
							}
						}
					}
					if claim.RelationPredicate == "disjoint" && claim.Quantifier == "all" && relation.Kind == "verified_disjoint" {
						rightIDs := rightDomainIDs
						right := semanticBoundsCoverage(semanticLiteralBounds(ledger, selected, rightIDs), rightIDs)
						if right != nil && !right.Incomplete && right.Total > 0 && right.CompleteBody {
							return SemanticEvidenceGuard{Passed: true, Reasons: []string{}}
						}
					}
				}
			}
		}
		return fail("selection_relation_not_proved_by_actual_body_and_bound_receipt")
	}
	if len(claim.RelationIDs) > 32 || claim.RelationPredicate != "" || len(claim.RelationRightSearchPageIDs) > 0 {
		return fail("body_property_has_invalid_relation_fields")
	}
	if claim.Domain.Kind == "selector_undelivered" {
		remaining := coverage.Total - coverage.NativeDelivered
		if remaining <= 0 {
			return fail("selector_undelivered_domain_empty_or_unknown")
		}
		positions := map[[2]int]bool{}
		for _, id := range claim.RelationIDs {
			for _, relation := range ledger.LiteralBounds.Relations {
				if relation.ID == id && relation.Kind == "counterexample" && !relation.Incomplete && relation.Domain.ResultID == ledger.ResultID && relation.Domain.Kind == "stored_text" && relation.Domain.Pointer == "" {
					leftBound := false
					for _, left := range relation.LeftIDs {
						leftBound = leftBound || domainIDs[left]
					}
					if !leftBound {
						continue
					}
					for _, w := range relation.Witnesses {
						if semanticWitnessSelected(w.Left, selected, merged) {
							positions[[2]int{w.Left.Start, w.Left.End}] = true
						}
					}
				}
			}
		}
		if len(positions) == 0 {
			return fail("selector_undelivered_property_requires_relation_and_actual_complete_row")
		}
		if claim.Quantifier == "all" && (len(positions) != remaining || coverage.OtherBodyCount != remaining) {
			return fail("selector_undelivered_all_properties_not_actually_delivered")
		}
		if claim.Quantifier != "all" && claim.Quantifier != "none" {
			return fail("selector_undelivered_quantifier_invalid")
		}
		return SemanticEvidenceGuard{Passed: true, Reasons: []string{}}
	}
	if claim.Quantifier == "none" {
		if coverage.KnownCompleteBody == 0 {
			return fail("subject_domain_particular_property_has_no_actual_complete_row")
		}
	} else if claim.Quantifier == "all" || claim.Quantifier == "displayed_subset" {
		if claim.Domain.Kind == "delivered_subset" {
			if coverage.NativeDelivered == 0 || coverage.NativeCompleteBody != coverage.NativeDelivered {
				return fail("displayed_subset_has_no_complete_property_evidence")
			}
		} else if claim.Quantifier == "displayed_subset" {
			return fail("subject_domain_quantifier_conflict")
		} else if coverage.Total == 0 || !coverage.CompleteBody {
			return fail("not_all_domain_properties_were_delivered_as_complete_lines")
		}
	} else {
		return fail("subject_domain_body_quantifier_invalid")
	}
	return SemanticEvidenceGuard{Passed: true, Reasons: []string{}}
}
