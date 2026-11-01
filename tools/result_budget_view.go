package tools

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// ModelResultBudgetRequest describes an already prepared observation. Text is
// model-facing text without a trusted citation header; Details and Args retain
// the host's structured call metadata. A budget changes only this model view,
// never the UI/history body or the immutable observation saved by PrepareResult.
type ModelResultBudgetRequest struct {
	Tool, Text, ResultID string
	IsError              bool
	Details, Args        json.RawMessage
	Tokens               int64
	InitialDelivery      *ResultDeliveryRule
}

type ModelResultBudgetView struct {
	Text, ResultID       string
	Reduced, Recoverable bool
	Delivery             *ModelResultDelivery
	InitialDelivery      *ResultDeliveryRule
}

// BudgetResultView uses the same conservative local estimate as the agent's
// allocator: ceil(UTF-8 bytes / 4). The budget is soft only for indispensable
// identity/status metadata, unavailable storage, or an expired result. In those
// cases it never turns an unrecoverable fragment into an apparently complete
// page, even if that means exceeding the requested model-view budget.
func (r *Registry) BudgetResultView(ctx context.Context, in ModelResultBudgetRequest) ModelResultBudgetView {
	view := ModelResultBudgetView{Text: in.Text, ResultID: in.ResultID}
	limit := resultModelBudgetBytes(in.Tokens)
	if in.Tool == ToolResultReadName {
		return r.budgetResultRead(ctx, in, limit)
	}
	if len(in.Text) <= limit {
		return view
	}
	if r == nil || r.results == nil || ctx.Err() != nil {
		return view
	}

	var record StoredToolResult
	if in.ResultID != "" {
		var err error
		record, err = r.results.Load(ctx, in.ResultID)
		if err != nil {
			// Text may be a first-stage preview rather than the source. Saving
			// that preview with text_complete=true would invent a full result.
			return view
		}
	} else {
		record = StoredToolResult{
			Tool: in.Tool, IsError: in.IsError, Text: in.Text,
			TextComplete: resultBudgetIsReturnedPage(in.Tool),
		}
		if !utf8.ValidString(record.Text) {
			record.Text = strings.ToValidUTF8(record.Text, "\uFFFD")
			record.TextComplete = false
		}
		if in.IsError {
			record.Error = resultHead(in.Text, 1024, 8)
		}
		if details, valid := resultDecodeJSON(in.Details); valid {
			// The reader's media fence recognizes top-level protocol content.
			// Apply it before nesting UI metadata beneath /details, otherwise
			// a native pointer could page inline MCP image/audio bytes around
			// that fence. The original UI/history details remain untouched.
			record.Native, _ = json.Marshal(map[string]any{
				"details":       resultBudgetRecoveryDetails(details),
				"details_scope": "UI recovery metadata only; raw media is omitted, not preserved as an original native payload.",
			})
		}
		ref, err := r.results.Save(record)
		if err != nil {
			// read/ls/find/grep pages must remain whole if no recovery copy can
			// be committed. The allocator records this as a soft-budget miss.
			return view
		}
		record.ID = ref.ID
	}
	if in.InitialDelivery != nil {
		view.Text = budgetInitialResultDelivery(record, in.IsError, in.InitialDelivery, limit)
		view.InitialDelivery = in.InitialDelivery
	} else {
		view.Text = budgetStoredResultView(record, in.IsError, limit)
	}
	view.ResultID, view.Reduced, view.Recoverable = record.ID, true, true
	return view
}

func resultBudgetRecoveryDetails(details any) any {
	safe := resultNativeModelView(details)
	root, _ := safe.(map[string]any)
	content, _ := root["content"].([]any)
	for _, item := range content {
		block, _ := item.(map[string]any)
		media, _ := block["data"].(map[string]any)
		if media["omitted_binary"] == true {
			media["reason"] = "Raw media is omitted from this recovery metadata; the original UI/history is unchanged."
		}
		resource, _ := block["resource"].(map[string]any)
		blob, _ := resource["blob"].(map[string]any)
		if blob["omitted_binary"] == true {
			blob["reason"] = "Raw media is omitted from this recovery metadata; the original UI/history is unchanged."
		}
	}
	return safe
}

func resultModelBudgetBytes(tokens int64) int {
	if tokens <= 0 {
		return 0
	}
	maxInt := int(^uint(0) >> 1)
	if tokens > int64(maxInt/4) {
		return maxInt
	}
	return int(tokens) * 4
}

// These tools bypass first-stage storage and preserve their returned page. This
// does not say their file/search/directory source was fully covered. Other
// ID-less tools may already have a legacy preview and are conservatively marked
// incomplete when retaining that observation for budget recovery.
func resultBudgetIsReturnedPage(tool string) bool {
	switch tool {
	case "read", "ls", "find", "grep", "todo":
		return true
	default:
		return false
	}
}

func budgetStoredResultView(record StoredToolResult, failed bool, limit int) string {
	kind := resultTextKind(record.Tool, record.Text)
	value, structured := resultJSONValue(record)
	if structured {
		kind = "json"
	}
	envelope := map[string]any{
		"result_id":            record.ID,
		"is_error":             failed,
		"original_is_error":    record.IsError,
		"preview_only":         true,
		"stored_text_complete": record.TextComplete,
		"coverage":             "saved observation only; source coverage unknown",
		"kind":                 kind,
		"read_more":            ToolResultReadName,
	}
	if failed || record.IsError || record.Error != "" {
		errorText := record.Error
		if errorText == "" {
			errorText = record.Text
		}
		// Keep a bounded error synopsis even at a metadata-only budget. Its
		// encoded size is bounded too, including hostile/control characters.
		envelope["error_summary"] = resultBudgetStringPrefix(errorText, 128)
	}
	minimum := resultBudgetJSON(envelope)
	if len(minimum) > limit {
		// A zero/very small allocation cannot contain a 65-byte reference
		// plus status. Preserve identity and incompleteness; the caller counts
		// the actual emitted estimate as an explicit soft-budget overflow.
		compact := map[string]any{
			"result_id":            record.ID,
			"is_error":             failed,
			"preview_only":         true,
			"stored_text_complete": record.TextComplete,
			"observation_only":     true,
		}
		if record.IsError != failed {
			compact["original_is_error"] = record.IsError
		}
		if summary, exists := envelope["error_summary"].(string); exists {
			compact["error_summary"] = resultBudgetStringPrefix(summary, 64)
		}
		return resultBudgetJSON(compact)
	}
	resultBudgetAdd(envelope, "original_tool", resultUTF8Prefix(record.Tool, 128), limit)
	resultBudgetAdd(envelope, "text_bytes", len(record.Text), limit)
	if structured {
		// Each candidate is a complete JSON representation. Never slice a
		// serialized JSON token or mistake samples for the original schema.
		sourceBytes := len(record.Structured)
		if sourceBytes == 0 {
			sourceBytes = len(record.Text)
		}
		// Do not re-encode a many-megabyte value merely to discover that it
		// cannot fit a kilobyte-scale model view.
		if sourceBytes <= limit && resultBudgetAdd(envelope, "value", value, limit) {
			return resultBudgetJSON(envelope)
		}
		for _, nodes := range []int{28, 12, 6, 2} {
			remaining := nodes
			if resultBudgetAdd(envelope, "observation_shape_and_samples", resultJSONDescription(value, 0, &remaining), limit) {
				return resultBudgetJSON(envelope)
			}
		}
		if !resultBudgetAdd(envelope, "json_directory", resultJSONDirectory(value), limit) {
			resultBudgetAdd(envelope, "json_directory", resultJSONCompactDirectory(value), limit)
		}
		return resultBudgetJSON(envelope)
	}
	if len(record.Text) <= limit && resultBudgetAdd(envelope, "text_preview", map[string]any{"text": record.Text, "omitted": false}, limit) {
		return resultBudgetJSON(envelope)
	}
	// Account for JSON escaping while choosing a UTF-8-safe head/tail sample.
	// Every accepted candidate is checked after JSON escaping; the saved
	// original is never modified by this process.
	low, high := 0, min(len(record.Text), limit)
	for low < high {
		n := low + (high-low+1)/2
		candidate := resultBudgetTextSample(record.Text, kind, n)
		if resultBudgetAdd(envelope, "text_preview", candidate, limit) {
			low = n
		} else {
			high = n - 1
		}
	}
	if low > 0 {
		resultBudgetAdd(envelope, "text_preview", resultBudgetTextSample(record.Text, kind, low), limit)
	}
	return resultBudgetJSON(envelope)
}

func resultBudgetTextSample(text, kind string, bytes int) map[string]any {
	headBytes := bytes / 2
	if kind == "webpage" || kind == "document" {
		headBytes = bytes * 3 / 4
	}
	head := resultUTF8Prefix(text, headBytes)
	tail := resultTail(text[len(head):], bytes-headBytes, resultPreviewLines)
	return map[string]any{"head": head, "tail": tail, "middle_omitted": true}
}

func resultBudgetAdd(envelope map[string]any, key string, value any, limit int) bool {
	previous, existed := envelope[key]
	envelope[key] = value
	encoded, err := json.Marshal(envelope)
	if err == nil && len(encoded) <= limit {
		return true
	}
	if existed {
		envelope[key] = previous
	} else {
		delete(envelope, key)
	}
	return false
}

func resultBudgetJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// limit bounds the JSON string value including its quotes and escaping.
func resultBudgetStringPrefix(text string, limit int) string {
	low, high := 0, min(len(text), max(0, limit-2))
	for low < high {
		n := low + (high-low+1)/2
		encoded, _ := json.Marshal(resultUTF8Prefix(text, n))
		if len(encoded) <= limit {
			low = n
		} else {
			high = n - 1
		}
	}
	return resultUTF8Prefix(text, low)
}

func (r *Registry) budgetResultRead(ctx context.Context, in ModelResultBudgetRequest, limit int) ModelResultBudgetView {
	if metadata, ok := budgetResultMetadata(in, limit); ok {
		return metadata
	}
	if page, ok := r.budgetReadPage(ctx, in, limit); ok {
		return page
	}
	if len(in.Text) <= limit {
		return ModelResultBudgetView{Text: in.Text, ResultID: in.ResultID}
	}
	// Do not create a chain of query-result snapshots. The original call's
	// validated arguments remain in tool_use, including any projection/filter.
	// A smaller limit can reread the same immutable result without reexecuting
	// the action that created it. The base ID is not this query output's ID.
	var args struct {
		ResultID    string          `json:"result_id"`
		View        string          `json:"view"`
		Aggregate   json.RawMessage `json:"aggregate"`
		IncludeRows *bool           `json:"include_rows"`
		GroupBy     []string        `json:"group_by"`
	}
	argsValid := json.Unmarshal(in.Args, &args) == nil
	aggregateArgs := strings.TrimSpace(string(args.Aggregate))
	hasAggregate := argsValid && len(aggregateArgs) > 0 && aggregateArgs[0] == '{'
	includeRows := !hasAggregate
	if args.IncludeRows != nil {
		includeRows = *args.IncludeRows
	}
	readMore := "Repeat tool_result with the same arguments and a smaller limit or deeper pointer. No original action was repeated."
	switch args.View {
	case "", "summary":
		readMore = "Summary has no limit. Read the original result with a text/json view and a bounded limit or deeper pointer."
	case "query":
		if len(args.GroupBy) > 0 {
			readMore = "Grouped statistics were not displayed. Keep group_by, aggregate, sort and the same offset; request a smaller group limit or more context space."
		} else if includeRows {
			readMore = "Read rows with a smaller limit, or use include_rows=false and remove offset/limit/select for statistics only. Keep the original pointer and filters."
		} else {
			readMore = "Statistics were not displayed due to the model budget. Do not add limit: statistics-only queries reject it. Retry when context space is available, or request a narrower aggregate."
		}
	}
	envelope := map[string]any{
		"is_error":             in.IsError,
		"model_budget_omitted": true,
		"page_displayed":       false,
		"read_more":            readMore,
	}
	if args.View == "query" {
		envelope["aggregate_displayed"] = false
	}
	recoverable := false
	if argsValid && validResultID(args.ResultID) && r != nil && r.results != nil && ctx.Err() == nil {
		if record, err := r.results.Load(ctx, args.ResultID); err == nil {
			envelope["original_result_id"] = record.ID
			envelope["original_is_error"] = record.IsError
			envelope["stored_text_complete"] = record.TextComplete
			recoverable = true
		}
	}
	if in.IsError {
		envelope["error_summary"] = resultBudgetStringPrefix(strings.TrimSpace(in.Text), 128)
	}
	if !recoverable {
		envelope["recovery_available"] = false
		envelope["read_more"] = "The original result could not be verified. Do not repeat a side-effecting action to recover it."
	}
	if recoverable && !in.IsError && args.View == "query" && len(args.GroupBy) == 0 {
		// Only the built-in tool_result reaches this path. Parse its successful
		// host-generated JSON, never an external tool's lookalike output or an
		// error string. The actual result ID still comes exclusively from Load.
		if value, valid := resultDecodeJSON([]byte(in.Text)); valid {
			if host, ok := value.(map[string]any); ok && host["view"] == "query" && host["result_id"] == envelope["original_result_id"] {
				if retained, ok := resultBudgetQueryStatistics(envelope, host, hasAggregate, includeRows, limit); ok {
					return ModelResultBudgetView{Text: retained, ResultID: in.ResultID, Reduced: true, Recoverable: true}
				}
			}
		}
	}
	if len(resultBudgetJSON(envelope)) > limit {
		switch {
		case !recoverable:
			envelope["read_more"] = "Original unavailable; do not repeat original action."
		case args.View == "query" && len(args.GroupBy) > 0:
			envelope["read_more"] = "Groups omitted: keep group_by/aggregate/sort and offset; use a smaller limit or more context space."
		case args.View == "query" && !includeRows:
			envelope["read_more"] = "Statistics omitted. Do not add limit. Retry with more context space or a narrower aggregate."
		case args.View == "query":
			envelope["read_more"] = "Rows omitted: smaller limit or include_rows=false (remove offset/limit/select); keep filters."
		case args.View == "" || args.View == "summary":
			envelope["read_more"] = "Summary has no limit; use text/json with a bounded limit/deeper pointer."
		default:
			envelope["read_more"] = "tool_result: same args, smaller limit or deeper pointer"
		}
	}
	return ModelResultBudgetView{
		Text: resultBudgetJSON(envelope), ResultID: in.ResultID,
		Reduced: true, Recoverable: recoverable,
	}
}

// Retaining an aggregate is atomic: its exact value, scan completion and source
// coverage must fit together. It is never an incomplete arithmetic summary or a
// claim that an undisplayed row page has been delivered. include_rows=false has
// no pagination, so its fallback must not recommend an invalid limit argument.
func resultBudgetQueryStatistics(base, host map[string]any, hasAggregate, includeRows bool, limit int) (string, bool) {
	if host["scan_complete"] != true {
		return "", false
	}
	if _, ok := host["matched_rows"].(json.Number); !ok {
		return "", false
	}
	coverage, ok := host["coverage"].(string)
	if !ok || coverage == "" {
		return "", false
	}
	if hasAggregate {
		if _, ok := host["aggregate"].(map[string]any); !ok {
			return "", false
		}
	}
	candidate := make(map[string]any, len(base)+6)
	for key, value := range base {
		candidate[key] = value
	}
	for _, key := range []string{"matched_rows", "scan_complete", "coverage", "original_is_error", "stored_text_complete"} {
		value, exists := host[key]
		if !exists {
			return "", false
		}
		candidate[key] = value
	}
	candidate["aggregate_displayed"] = hasAggregate
	candidate["statistics_displayed"] = true
	if hasAggregate {
		candidate["aggregate"] = host["aggregate"]
	}
	if includeRows {
		candidate["read_more"] = "Statistics shown; row page omitted. Use a smaller limit for rows, retaining the pointer and filters."
	} else {
		delete(candidate, "read_more") // the requested statistics are present
	}
	encoded := resultBudgetJSON(candidate)
	if len(encoded) <= limit {
		return encoded, true
	}
	// The host's long coverage explanation is redundant with these explicit
	// flags and the retained aggregate's valid/missing/null/non-numeric counts.
	// Keep its essential saved-observation scope when management text is the
	// only reason the exact arithmetic would otherwise be hidden.
	candidate["coverage"] = "All matching rows of the selected saved array only; source completeness unknown."
	encoded = resultBudgetJSON(candidate)
	return encoded, len(encoded) <= limit
}
