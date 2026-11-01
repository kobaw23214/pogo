package main

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yosukeno/pogo/tools"
)

type logObservation struct {
	Kind             string           `json:"kind"`
	ResultID         string           `json:"result_id,omitempty"`
	Start            int              `json:"start,omitempty"`
	End              int              `json:"end,omitempty"`
	Total            int              `json:"total,omitempty"`
	FragmentBytes    int              `json:"fragment_bytes"`
	FragmentSHA256   string           `json:"fragment_sha256"`
	SourceExact      bool             `json:"source_exact"`
	StatusKnown      bool             `json:"status_known"`
	OriginalIsError  bool             `json:"original_is_error"`
	TextComplete     bool             `json:"stored_text_complete"`
	RangeComplete    bool             `json:"range_complete"`
	EvidenceKeys     []string         `json:"evidence_keys,omitempty"`
	Ranges           []logSearchRange `json:"ranges,omitempty"`
	Pattern          string           `json:"pattern,omitempty"`
	ContextLines     int              `json:"context_lines,omitempty"`
	ScanComplete     bool             `json:"scan_complete"`
	TotalMatches     int              `json:"total_matches,omitempty"`
	SearchOffset     int              `json:"search_offset,omitempty"`
	SearchNextOffset *int             `json:"search_next_offset,omitempty"`
	ShownItems       int              `json:"shown_items,omitempty"`
	SearchPageValid  bool             `json:"search_page_valid"`
}

type logSearchRange struct {
	Start            int      `json:"context_start"`
	End              int      `json:"context_end"`
	MatchIndex       int      `json:"match_index"`
	MatchStart       int      `json:"match_start"`
	MatchEnd         int      `json:"match_end"`
	LineNumber       int      `json:"line_number"`
	FragmentBytes    int      `json:"fragment_bytes"`
	SHA256           string   `json:"sha256"`
	SourceExact      bool     `json:"source_exact"`
	LiteralExact     bool     `json:"literal_exact"`
	ContextTruncated bool     `json:"context_truncated"`
	EvidenceKeys     []string `json:"evidence_keys,omitempty"`
}

type logEvidenceTrace struct {
	Passed                 bool     `json:"passed"`
	MatchedSourceCalls     int      `json:"matched_source_calls"`
	ActualModelRanges      int      `json:"actual_model_ranges"`
	RejectedViews          int      `json:"rejected_views"`
	CoveredSourceBytes     int      `json:"covered_source_bytes"`
	RequiredEvidence       []string `json:"required_evidence"`
	DeliveredEvidence      []string `json:"delivered_evidence"`
	MissingEvidence        []string `json:"missing_evidence"`
	SourceLabels           []string `json:"source_labels"`
	Scope                  string   `json:"scope"`
	SearchScansComplete    int      `json:"search_scans_complete"`
	SearchPagesDelivered   int      `json:"search_pages_delivered"`
	SearchMatchesDelivered int      `json:"search_matches_delivered"`
}

var logTextHeader = regexp.MustCompile(`^Result (R[0-9a-f]{64}); text bytes \[([0-9]+),([0-9]+)\) of ([0-9]+); range_complete=(true|false)\.$`)
var logStoredFooter = regexp.MustCompile(`\[Stored result: (R[0-9a-f]{64});`)

func summarizeLogView(body string, fixture logFixture) *logObservation {
	if first := strings.SplitN(body, "\n", 2)[0]; strings.HasPrefix(first, "Result ") {
		matched := logTextHeader.FindStringSubmatch(first)
		separator := strings.Index(body, "\n---\n")
		if len(matched) != 6 || separator < 0 {
			return nil
		}
		start, err1 := strconv.Atoi(matched[2])
		end, err2 := strconv.Atoi(matched[3])
		total, err3 := strconv.Atoi(matched[4])
		if err1 != nil || err2 != nil || err3 != nil {
			return nil
		}
		fragment, header := body[separator+5:], body[:separator]
		observation := logRangeObservation(matched[1], start, end, total, fragment, fixture)
		observation.RangeComplete = matched[5] == "true"
		observation.StatusKnown = strings.Contains(header, "original status: success;") && strings.Contains(header, "stored_text_complete: true.")
		observation.OriginalIsError = strings.Contains(header, "original status: error;")
		observation.TextComplete = strings.Contains(header, "stored_text_complete: true.")
		return observation
	}
	var view struct {
		ResultID        string  `json:"result_id"`
		View            string  `json:"view"`
		Pointer         string  `json:"pointer"`
		Offset          *int    `json:"offset"`
		End             *int    `json:"end_offset"`
		Total           *int    `json:"stored_string_bytes"`
		Fragment        *string `json:"value_fragment"`
		Complete        bool    `json:"complete"`
		PageDisplayed   *bool   `json:"page_displayed"`
		OriginalIsError *bool   `json:"original_is_error"`
		TextComplete    *bool   `json:"stored_text_complete"`
		PreviewOnly     bool    `json:"preview_only"`
		TextPreview     struct {
			Text string `json:"text"`
			Head string `json:"head"`
			Tail string `json:"tail"`
		} `json:"text_preview"`
	}
	if json.Unmarshal([]byte(body), &view) == nil && view.ResultID != "" {
		if view.View == "search" {
			return summarizeLogSearch(body, fixture)
		}
		if (view.View == "text" || view.View == "native" && view.Pointer == "/content/0/text") && view.Fragment != nil && view.Offset != nil && view.End != nil && view.Total != nil {
			observed := logRangeObservation(view.ResultID, *view.Offset, *view.End, *view.Total, *view.Fragment, fixture)
			observed.RangeComplete = view.Complete
			observed.StatusKnown = view.OriginalIsError != nil && view.TextComplete != nil
			observed.OriginalIsError = view.OriginalIsError != nil && *view.OriginalIsError
			observed.TextComplete = view.TextComplete != nil && *view.TextComplete
			if view.PageDisplayed != nil && !*view.PageDisplayed {
				observed.SourceExact = false
			}
			return observed
		}
		if view.PreviewOnly {
			observed := &logObservation{Kind: "preview", ResultID: view.ResultID, FragmentBytes: len(body), FragmentSHA256: digest([]byte(body)), StatusKnown: view.OriginalIsError != nil && view.TextComplete != nil,
				OriginalIsError: view.OriginalIsError != nil && *view.OriginalIsError, TextComplete: view.TextComplete != nil && *view.TextComplete}
			for _, part := range []string{view.TextPreview.Text, view.TextPreview.Head, view.TextPreview.Tail} {
				observed.EvidenceKeys = appendEvidenceKeys(observed.EvidenceKeys, part, fixture)
			}
			return observed
		}
	}
	if footer := logStoredFooter.FindStringSubmatch(body); len(footer) == 2 {
		return &logObservation{Kind: "preview", ResultID: footer[1], FragmentBytes: len(body), FragmentSHA256: digest([]byte(body)),
			StatusKnown: strings.Contains(body, "original_status=success;") && strings.Contains(body, "text_complete=true."), TextComplete: strings.Contains(body, "text_complete=true."), EvidenceKeys: appendEvidenceKeys(nil, body, fixture)}
	}
	return nil
}

// Search observations retain only verified byte ranges and hashes. In
// particular, a pattern in request/page metadata is never observed log text.
func summarizeLogSearch(body string, fixture logFixture) *logObservation {
	var page struct {
		ResultID        string `json:"result_id"`
		View            string `json:"view"`
		Pattern         string `json:"pattern"`
		CaseSensitive   *bool  `json:"case_sensitive"`
		ContextLines    *int   `json:"context_lines"`
		Offset          *int   `json:"offset"`
		NextOffset      *int   `json:"next_offset"`
		TotalMatches    *int   `json:"total_matches"`
		ShownItems      *int   `json:"shown_items"`
		Complete        *bool  `json:"complete"`
		PageDisplayed   *bool  `json:"page_displayed"`
		ScanComplete    *bool  `json:"scan_complete"`
		StoredBytes     *int   `json:"stored_text_bytes"`
		ScannedBytes    *int   `json:"scanned_text_bytes"`
		OriginalIsError *bool  `json:"original_is_error"`
		TextComplete    *bool  `json:"stored_text_complete"`
		Items           []struct {
			Index      *int    `json:"match_index"`
			MatchStart *int    `json:"match_start"`
			MatchEnd   *int    `json:"match_end"`
			LineNumber *int    `json:"line_number"`
			Start      *int    `json:"context_start"`
			End        *int    `json:"context_end"`
			Text       *string `json:"text"`
			Truncated  *bool   `json:"context_truncated"`
		} `json:"items"`
	}
	if json.Unmarshal([]byte(body), &page) != nil || page.View != "search" || page.ResultID == "" {
		return nil
	}
	observed := &logObservation{Kind: "search", ResultID: page.ResultID, Pattern: page.Pattern,
		FragmentBytes: len(body), FragmentSHA256: digest([]byte(body)), StatusKnown: page.OriginalIsError != nil && page.TextComplete != nil,
		OriginalIsError: page.OriginalIsError != nil && *page.OriginalIsError, TextComplete: page.TextComplete != nil && *page.TextComplete}
	if page.ContextLines != nil {
		observed.ContextLines = *page.ContextLines
	}
	if page.Offset != nil {
		observed.SearchOffset = *page.Offset
	}
	if page.TotalMatches != nil {
		observed.TotalMatches = *page.TotalMatches
	}
	if page.ShownItems != nil {
		observed.ShownItems = *page.ShownItems
	}
	observed.SearchNextOffset = page.NextOffset
	observed.ScanComplete = page.ScanComplete != nil && *page.ScanComplete
	if page.Complete != nil {
		observed.RangeComplete = *page.Complete
	}
	if page.Pattern == "" || len(page.Pattern) > 1024 || !utf8.ValidString(page.Pattern) || page.CaseSensitive == nil || !*page.CaseSensitive ||
		page.ContextLines == nil || *page.ContextLines < 0 || *page.ContextLines > 5 || page.Offset == nil || *page.Offset < 0 ||
		page.TotalMatches == nil || *page.TotalMatches < 0 || page.ShownItems == nil || *page.ShownItems != len(page.Items) || len(page.Items) > 200 ||
		page.Complete == nil || page.PageDisplayed == nil || page.ScanComplete == nil || !*page.ScanComplete ||
		page.StoredBytes == nil || *page.StoredBytes != len(fixture.Text) || page.ScannedBytes == nil || *page.ScannedBytes != len(fixture.Text) {
		return observed
	}
	// Independently check count and ordinal locations against the saved source;
	// scan completion alone does not deliver any of those matching bytes.
	var starts []int
	for cursor := 0; cursor <= len(fixture.Text)-len(page.Pattern); {
		found := strings.Index(fixture.Text[cursor:], page.Pattern)
		if found < 0 {
			break
		}
		start := cursor + found
		starts = append(starts, start)
		cursor = start + len(page.Pattern)
	}
	if len(starts) != *page.TotalMatches || *page.Offset > len(starts) || len(page.Items) > len(starts)-*page.Offset {
		return observed
	}
	next := *page.Offset + len(page.Items)
	if *page.Complete != (next == len(starts)) || *page.PageDisplayed != (len(page.Items) > 0 || *page.Complete) ||
		(next < len(starts) && (page.NextOffset == nil || *page.NextOffset != next)) || (next == len(starts) && page.NextOffset != nil) {
		return observed
	}
	valid := true
	for i, item := range page.Items {
		if item.Index == nil || item.MatchStart == nil || item.MatchEnd == nil || item.LineNumber == nil || item.Start == nil || item.End == nil || item.Text == nil || item.Truncated == nil {
			valid = false
			continue
		}
		entry := logSearchRange{Start: *item.Start, End: *item.End, MatchIndex: *item.Index, MatchStart: *item.MatchStart, MatchEnd: *item.MatchEnd,
			LineNumber: *item.LineNumber, FragmentBytes: len(*item.Text), SHA256: digest([]byte(*item.Text)), ContextTruncated: *item.Truncated}
		entry.SourceExact = entry.Start >= 0 && entry.End >= entry.Start && entry.End <= len(fixture.Text) && entry.End-entry.Start == len(*item.Text) && len(*item.Text) <= 4096 &&
			logUTF8Boundary(fixture.Text, entry.Start) && logUTF8Boundary(fixture.Text, entry.End) && utf8.ValidString(*item.Text) && fixture.Text[entry.Start:entry.End] == *item.Text
		entry.LiteralExact = entry.SourceExact && entry.MatchIndex == *page.Offset+i && entry.MatchStart == starts[*page.Offset+i] && entry.MatchEnd == entry.MatchStart+len(page.Pattern) &&
			entry.MatchStart >= entry.Start && entry.MatchEnd <= entry.End && logUTF8Boundary(fixture.Text, entry.MatchStart) && logUTF8Boundary(fixture.Text, entry.MatchEnd) &&
			fixture.Text[entry.MatchStart:entry.MatchEnd] == page.Pattern && entry.LineNumber == strings.Count(fixture.Text[:entry.MatchStart], "\n")+1
		if entry.SourceExact && entry.LiteralExact {
			entry.EvidenceKeys = appendEvidenceKeys(nil, *item.Text, fixture)
		} else {
			valid = false
		}
		observed.Ranges = append(observed.Ranges, entry)
	}
	observed.SourceExact, observed.SearchPageValid = valid, valid
	return observed
}

func logUTF8Boundary(text string, offset int) bool {
	return offset >= 0 && offset <= len(text) && (offset == len(text) || utf8.RuneStart(text[offset]))
}

func logRangeObservation(id string, start, end, total int, fragment string, fixture logFixture) *logObservation {
	observed := &logObservation{Kind: "text_range", ResultID: id, Start: start, End: end, Total: total, FragmentBytes: len(fragment), FragmentSHA256: digest([]byte(fragment))}
	observed.SourceExact = start >= 0 && end >= start && end <= len(fixture.Text) && total == len(fixture.Text) && end-start == len(fragment) && fixture.Text[start:end] == fragment
	if observed.SourceExact {
		observed.EvidenceKeys = appendEvidenceKeys(nil, fragment, fixture)
	}
	return observed
}

func appendEvidenceKeys(existing []string, text string, fixture logFixture) []string {
	for _, evidence := range fixture.RequiredEvidence {
		if !strings.Contains(text, evidence.Line) {
			continue
		}
		present := false
		for _, key := range existing {
			present = present || key == evidence.Key
		}
		if !present {
			existing = append(existing, evidence.Key)
		}
	}
	return existing
}

func checkLogDelivery(calls []toolCallReport, requests []requestReport, originalID string, fixture logFixture) logEvidenceTrace {
	trace := logEvidenceTrace{RequiredEvidence: []string{}, DeliveredEvidence: []string{}, MissingEvidence: []string{}, SourceLabels: []string{}, Scope: "Evidence coverage is from actual model-request log fragments only, bound to the single saved source; source completeness remains unknown."}
	byCall := make(map[string]toolCallReport)
	for _, call := range calls {
		byCall[call.ID] = call
	}
	var ranges [][2]int
	seen := make(map[string]bool)
	keys, labels := make(map[string]bool), make(map[string]bool)
	for _, request := range requests {
		// Only an actual successful, non-aborted request delivered these views.
		// Filter before deduplication so a failed attempt cannot hide a later
		// successful retry carrying the same native body.
		if request.ErrorClass != "" || request.StopReason == "aborted" {
			continue
		}
		for _, view := range request.ToolViews {
			observed := view.Log
			if observed == nil {
				continue
			}
			call, known := byCall[view.CallID]
			if !known || call.IsError || observed.ResultID != originalID || !observed.StatusKnown || observed.OriginalIsError || !observed.TextComplete {
				trace.RejectedViews++
				continue
			}
			allowed := call.Name == "mcp_eval_get_logs" && observed.Kind == "preview"
			if call.Name == tools.ToolResultReadName {
				var args struct {
					ResultID     string `json:"result_id"`
					View         string `json:"view"`
					Pointer      string `json:"pointer"`
					Pattern      string `json:"pattern"`
					ContextLines *int   `json:"context_lines"`
					Offset       int    `json:"offset"`
					Limit        *int   `json:"limit"`
				}
				allowed = false
				if json.Unmarshal(call.Args, &args) == nil && args.ResultID == originalID {
					allowed = (args.View == "text" || args.View == "native" && args.Pointer == "/content/0/text") && observed.Kind == "text_range"
					if args.View == "search" && observed.Kind == "search" {
						contextLines, limit := 1, 20
						if args.ContextLines != nil {
							contextLines = *args.ContextLines
						}
						if args.Limit != nil {
							limit = *args.Limit
						}
						allowed = observed.Pattern == args.Pattern && observed.ContextLines == contextLines && observed.SearchOffset == args.Offset && observed.ShownItems <= limit &&
							observed.FragmentSHA256 == view.BodySHA256 && observed.SearchPageValid && observed.SourceExact
					}
				}
			}
			if !allowed {
				trace.RejectedViews++
				continue
			}
			identity := view.CallID + ":" + view.BodySHA256
			if seen[identity] {
				continue
			}
			seen[identity] = true
			trace.MatchedSourceCalls++
			if call.SourceLabel != "" {
				labels[call.SourceLabel] = true
			}
			if observed.Kind == "search" {
				if observed.ScanComplete {
					trace.SearchScansComplete++
				}
				if len(observed.Ranges) > 0 {
					trace.SearchPagesDelivered++
				}
				for _, part := range observed.Ranges {
					if !part.SourceExact || !part.LiteralExact || part.Start >= part.End {
						trace.RejectedViews++
						continue
					}
					ranges = append(ranges, [2]int{part.Start, part.End})
					trace.ActualModelRanges++
					trace.SearchMatchesDelivered++
				}
			} else if observed.Kind == "text_range" {
				if !observed.SourceExact || observed.Start == observed.End {
					trace.RejectedViews++
					continue
				}
				ranges = append(ranges, [2]int{observed.Start, observed.End})
				trace.ActualModelRanges++
			} else {
				for _, key := range observed.EvidenceKeys {
					keys[key] = true
				}
			}
		}
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i][0] < ranges[j][0] })
	var merged [][2]int
	for _, interval := range ranges {
		if len(merged) == 0 || interval[0] > merged[len(merged)-1][1] {
			merged = append(merged, interval)
		} else if interval[1] > merged[len(merged)-1][1] {
			merged[len(merged)-1][1] = interval[1]
		}
	}
	for _, interval := range merged {
		trace.CoveredSourceBytes += interval[1] - interval[0]
	}
	for _, evidence := range fixture.RequiredEvidence {
		trace.RequiredEvidence = append(trace.RequiredEvidence, evidence.Key)
		for _, interval := range merged {
			if interval[0] <= evidence.Start && interval[1] >= evidence.End {
				keys[evidence.Key] = true
			}
		}
		if keys[evidence.Key] {
			trace.DeliveredEvidence = append(trace.DeliveredEvidence, evidence.Key)
		} else {
			trace.MissingEvidence = append(trace.MissingEvidence, evidence.Key)
		}
	}
	for label := range labels {
		trace.SourceLabels = append(trace.SourceLabels, label)
	}
	sort.Strings(trace.SourceLabels)
	trace.Passed = originalID != "" && len(fixture.RequiredEvidence) > 0 && len(trace.MissingEvidence) == 0
	return trace
}
