package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	resultSearchPatternMax = 1024
	resultSearchContextMax = 5
	resultSearchSnippetMax = 4096
	resultSearchScanChunk  = 64 * 1024
)

func resultHasSearchFields(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	for field := range fields {
		if strings.EqualFold(field, "pattern") || strings.EqualFold(field, "context_lines") {
			return true
		}
	}
	return false
}

// A search is a literal operation on the decoded saved Text, never a filename,
// regular expression, native media decoder or request to the original tool.
func resultDecodeSearchArgs(raw json.RawMessage) (toolResultReadArgs, error) {
	var args toolResultReadArgs
	duplicate, err := jsonHasDuplicateObjectKeys(raw)
	if err != nil || duplicate || !utf8.Valid(raw) {
		return args, fmt.Errorf("search arguments must be unambiguous UTF-8 JSON without duplicate keys")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return args, fmt.Errorf("search arguments must be one JSON object")
	}
	allowed := map[string]bool{"result_id": true, "view": true, "pattern": true, "context_lines": true, "offset": true, "limit": true}
	for field, value := range fields {
		if !allowed[field] {
			return args, fmt.Errorf("search has unsupported field %q; use result_id, view, pattern, context_lines, offset and limit", resultUTF8Prefix(field, 64))
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return args, fmt.Errorf("search field %q cannot be null", field)
		}
	}
	for _, required := range []string{"result_id", "view", "pattern"} {
		if _, present := fields[required]; !present {
			return args, fmt.Errorf("search requires %q", required)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return args, fmt.Errorf("invalid search arguments: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return args, fmt.Errorf("search accepts exactly one JSON object")
	}
	if args.View != "search" || !validResultID(args.ResultID) {
		return args, fmt.Errorf("search requires view=search and a host-assigned result_id")
	}
	if args.Pattern == "" || len(args.Pattern) > resultSearchPatternMax || !utf8.ValidString(args.Pattern) {
		return args, fmt.Errorf("search pattern must be a nonempty literal of at most %d UTF-8 bytes", resultSearchPatternMax)
	}
	if args.Offset < 0 || args.Limit != nil && (*args.Limit < 1 || *args.Limit > resultReadArrayMax) {
		return args, fmt.Errorf("search offset must be nonnegative and limit must be 1..%d matches", resultReadArrayMax)
	}
	if args.ContextLines != nil && (*args.ContextLines < 0 || *args.ContextLines > resultSearchContextMax) {
		return args, fmt.Errorf("search context_lines must be 0..%d", resultSearchContextMax)
	}
	return args, nil
}

type resultSearchHit struct{ index, start, end, line int }

type resultSearchItem struct {
	MatchIndex       int    `json:"match_index"`
	MatchStart       int    `json:"match_start"`
	MatchEnd         int    `json:"match_end"`
	LineNumber       int    `json:"line_number"`
	ContextStart     int    `json:"context_start"`
	ContextEnd       int    `json:"context_end"`
	Text             string `json:"text"`
	ContextTruncated bool   `json:"context_truncated"`
}

// Count every non-overlapping match, retaining only the requested <=200 hits.
// The bounded start-position window overlaps by pattern length minus one so a
// literal crossing a window boundary is found exactly once. Cancellation never
// waits for an unbounded strings.Index over the complete stored observation.
func scanResultLiteral(ctx context.Context, text, pattern string, offset, limit int) ([]resultSearchHit, int, error) {
	hits := make([]resultSearchHit, 0, min(limit, resultReadArrayMax))
	position, line, total := 0, 1, 0
	for position <= len(text)-len(pattern) {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		end := min(len(text), position+resultSearchScanChunk+len(pattern)-1)
		found := strings.Index(text[position:end], pattern)
		if found < 0 {
			next := min(len(text), position+resultSearchScanChunk)
			line += strings.Count(text[position:next], "\n")
			position = next
			continue
		}
		start := position + found
		line += strings.Count(text[position:start], "\n")
		matchEnd := start + len(pattern)
		if total >= offset && len(hits) < limit {
			hits = append(hits, resultSearchHit{index: total, start: start, end: matchEnd, line: line})
		}
		total++
		line += strings.Count(text[start:matchEnd], "\n")
		position = matchEnd
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	return hits, total, nil
}

func resultSearchContext(text string, hit resultSearchHit, contextLines int) resultSearchItem {
	start := strings.LastIndexByte(text[:hit.start], '\n') + 1
	end := hit.end
	if end == 0 || text[end-1] != '\n' {
		if newline := strings.IndexByte(text[end:], '\n'); newline >= 0 {
			end += newline + 1
		} else {
			end = len(text)
		}
	}
	for before := 0; before < contextLines && start > 0; before++ {
		start = strings.LastIndexByte(text[:start-1], '\n') + 1
	}
	for after := 0; after < contextLines && end < len(text); after++ {
		if newline := strings.IndexByte(text[end:], '\n'); newline >= 0 {
			end += newline + 1
		} else {
			end = len(text)
		}
	}
	desiredStart, desiredEnd := start, end
	if end-start > resultSearchSnippetMax {
		spare := resultSearchSnippetMax - (hit.end - hit.start)
		left := min(hit.start-start, spare/2)
		right := min(end-hit.end, spare-left)
		left = min(hit.start-start, spare-right)
		start, end = hit.start-left, hit.end+right
		for start < hit.start && !utf8.RuneStart(text[start]) {
			start++
		}
		for end > hit.end && end < len(text) && !utf8.RuneStart(text[end]) {
			end--
		}
	}
	return resultSearchItem{MatchIndex: hit.index, MatchStart: hit.start, MatchEnd: hit.end, LineNumber: hit.line,
		ContextStart: start, ContextEnd: end, Text: text[start:end], ContextTruncated: start != desiredStart || end != desiredEnd}
}

func readResultSearch(ctx context.Context, record StoredToolResult, args toolResultReadArgs, details ToolResultReadDetails) (Result, error) {
	limit, contextLines := 20, 1
	if args.Limit != nil {
		limit = *args.Limit
	}
	if args.ContextLines != nil {
		contextLines = *args.ContextLines
	}
	hits, total, err := scanResultLiteral(ctx, record.Text, args.Pattern, args.Offset, limit)
	if err != nil {
		return Result{}, err
	}
	if args.Offset > total {
		return Result{}, fmt.Errorf("search offset exceeds the %d matching saved-text occurrences", total)
	}
	details.Kind, details.Pattern, details.ContextLines, details.ScanComplete, details.Total = "text", args.Pattern, contextLines, true, total
	envelope := resultReadEnvelope(record, details)
	envelope["pattern"], envelope["context_lines"], envelope["case_sensitive"] = args.Pattern, contextLines, true
	envelope["range_unit"], envelope["byte_range_unit"] = "matching non-overlapping literal occurrences", "UTF-8 bytes of stored text; ends exclusive"
	envelope["scan_complete"], envelope["total_matches"] = true, total
	envelope["stored_text_bytes"], envelope["scanned_text_bytes"] = len(record.Text), len(record.Text)
	envelope["coverage"] = "Search scanned saved text only, not native/structured data or a fresh source. complete concerns displayed matches; scan_complete does not establish source completeness."
	items := make([]resultSearchItem, 0, len(hits))
	setPage := func() {
		next := args.Offset + len(items)
		details.Complete = next == total
		details.NextOffset = nil
		delete(envelope, "next_offset")
		if next < total {
			details.NextOffset = &next
			envelope["next_offset"] = next
		}
		envelope["items"], envelope["shown_items"], envelope["complete"] = items, len(items), details.Complete
		envelope["page_displayed"] = len(items) > 0 || details.Complete
	}
	setPage()
	for _, hit := range hits {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		items = append(items, resultSearchContext(record.Text, hit, contextLines))
		setPage()
		encoded, err := json.Marshal(envelope)
		if err != nil {
			return Result{}, fmt.Errorf("encode saved-text search: %w", err)
		}
		if len(encoded) > resultReadOutputMax {
			items = items[:len(items)-1]
			setPage()
			if len(items) == 0 {
				return Result{}, fmt.Errorf("search context cannot fit; read saved text at byte offset %d with a bounded limit", hit.start)
			}
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	return boundedToolResultRead(envelope, details)
}
