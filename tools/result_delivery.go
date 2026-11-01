package tools

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// ResultDeliveryMode selects the first model view of a saved observation. These
// settings are host configuration, not tool arguments or fields in MCP output.
// Every mode remains subject to the agent's second-stage context budget.
type ResultDeliveryMode string

const (
	ResultDeliveryAuto      ResultDeliveryMode = "auto"
	ResultDeliveryInline    ResultDeliveryMode = "inline"
	ResultDeliveryPreview   ResultDeliveryMode = "preview"
	ResultDeliveryReference ResultDeliveryMode = "reference"
)

// ResultDeliveryRule controls one tool's model view without modifying the
// immutable saved result or its UI/history text. Focuses are literal requests:
// JSONPointers use RFC 6901, Keywords use case-sensitive literal text matching.
type ResultDeliveryRule struct {
	Mode         ResultDeliveryMode
	JSONPointers []string
	Keywords     []string
}

// ResultDeliveryPolicy defaults to auto. Tools uses exact registered tool names;
// no pattern or value in a third-party response can select a policy.
type ResultDeliveryPolicy struct {
	Mode  ResultDeliveryMode
	Tools map[string]ResultDeliveryRule
}

// CloneResultDeliveryPolicy prevents a caller or a new MCP registry snapshot
// from mutating the policy captured by an in-flight run. Registries treat the
// cloned value as immutable, so concurrent executions require no mutable cache.
func CloneResultDeliveryPolicy(policy ResultDeliveryPolicy) ResultDeliveryPolicy {
	cloned := ResultDeliveryPolicy{Mode: resultDeliveryMode(policy.Mode)}
	if policy.Tools != nil {
		cloned.Tools = make(map[string]ResultDeliveryRule, len(policy.Tools))
		for name, rule := range policy.Tools {
			cloned.Tools[name] = cloneResultDeliveryRule(rule)
		}
	}
	return cloned
}

func cloneResultDeliveryRule(rule ResultDeliveryRule) ResultDeliveryRule {
	cloned := ResultDeliveryRule{Mode: rule.Mode}
	if rule.Mode != "" {
		cloned.Mode = resultDeliveryMode(rule.Mode)
	}
	for _, pointer := range rule.JSONPointers {
		if _, err := resultQueryPointer(pointer); err == nil && len(cloned.JSONPointers) < 16 {
			cloned.JSONPointers = append(cloned.JSONPointers, pointer)
		}
	}
	for _, keyword := range rule.Keywords {
		if keyword != "" && len(keyword) <= 256 && len(cloned.Keywords) < 16 {
			cloned.Keywords = append(cloned.Keywords, keyword)
		}
	}
	return cloned
}

func resultDeliveryMode(mode ResultDeliveryMode) ResultDeliveryMode {
	switch mode {
	case ResultDeliveryInline, ResultDeliveryPreview, ResultDeliveryReference:
		return mode
	default:
		return ResultDeliveryAuto
	}
}

func (r *Registry) resultDeliveryRule(tool string) ResultDeliveryRule {
	rule := ResultDeliveryRule{Mode: resultDeliveryMode(r.resultDelivery.Mode)}
	if specific, ok := r.resultDelivery.Tools[tool]; ok {
		rule = cloneResultDeliveryRule(specific)
		if specific.Mode == "" {
			rule.Mode = resultDeliveryMode(r.resultDelivery.Mode)
		}
	}
	return rule
}

// InitialResultView chooses the first request-only view of an already saved
// action. It never saves previews as new observations, changes UI/history text,
// executes a tool, or adopts a textual result ID from untrusted output.
// Reduced means "use this model view", not that the original was discarded.
func (r *Registry) InitialResultView(ctx context.Context, in ModelResultBudgetRequest) ModelResultBudgetView {
	unchanged := ModelResultBudgetView{Text: in.Text, ResultID: in.ResultID}
	if r == nil || r.results == nil || ctx.Err() != nil || !validResultID(in.ResultID) || in.Tool == ToolResultReadName || resultBudgetIsReturnedPage(in.Tool) {
		return unchanged
	}
	record, err := r.results.Load(ctx, in.ResultID)
	if err != nil || record.Tool != in.Tool || ctx.Err() != nil {
		return unchanged
	}
	rule := r.resultDeliveryRule(in.Tool)
	oversized := len(record.Text) > MaxBytes || len(record.Structured) > MaxBytes || countResultLines(record.Text) > MaxLines
	if rule.Mode == ResultDeliveryAuto {
		if !oversized && len(rule.JSONPointers) == 0 && len(rule.Keywords) == 0 {
			return unchanged
		}
		rule.Mode = ResultDeliveryPreview
		// Classification needs only JSON validity. Decode once, when building
		// the model envelope, instead of allocating the same large tree twice.
		isJSON := json.Valid(record.Structured) || json.Valid([]byte(record.Text))
		kind := resultTextKind(record.Tool, record.Text)
		if oversized && (isJSON || kind == "log") {
			rule.Mode = ResultDeliveryReference
		}
	}
	// Inline is the host's preference to present the observation directly. It
	// does not override the later model-context allocator or storage limits.
	if rule.Mode == ResultDeliveryInline {
		text := resultDeliveryInline(record)
		return ModelResultBudgetView{Text: text, ResultID: record.ID, Reduced: text != in.Text, Recoverable: true, InitialDelivery: &rule}
	}
	limit := resultPreviewBytes
	if rule.Mode == ResultDeliveryReference {
		limit = 4096
	}
	text := budgetInitialResultDelivery(record, in.IsError, &rule, limit)
	return ModelResultBudgetView{Text: text, ResultID: record.ID, Reduced: text != in.Text, Recoverable: true, InitialDelivery: &rule}
}

func resultDeliveryInline(record StoredToolResult) string {
	text := record.Text
	if text == "" && len(record.Structured) > 0 {
		text = string(record.Structured)
	}
	status := resultStatusText(record)
	if !record.TextComplete {
		status += "Stored observation is incomplete; missing source content was not recovered.\n"
	}
	return status + text + "\n[Stored result: " + record.ID + "; use tool_result for the saved observation without repeating the action.]"
}

// budgetInitialResultDelivery is shared by the first delivery and subsequent
// budget shrinking. A reference-first result therefore cannot regress to array
// samples simply because the model is allocated less space later.
func budgetInitialResultDelivery(record StoredToolResult, failed bool, rule *ResultDeliveryRule, limit int) string {
	if rule == nil || rule.Mode == ResultDeliveryInline || rule.Mode == ResultDeliveryAuto {
		return budgetStoredResultView(record, failed, limit)
	}
	kind := resultTextKind(record.Tool, record.Text)
	value, isJSON := resultJSONValue(record)
	if isJSON {
		kind = "json"
	}
	envelope := map[string]any{
		"result_id": record.ID, "is_error": failed, "original_is_error": record.IsError,
		"preview_only": true, "stored_text_complete": record.TextComplete,
		"delivery_mode": rule.Mode, "kind": kind,
		"coverage":  "saved observation only; source completeness and unread body are not established",
		"read_more": ToolResultReadName,
	}
	if failed || record.IsError || record.Error != "" {
		errorText := record.Error
		if errorText == "" {
			errorText = record.Text
		}
		envelope["error_summary"] = resultBudgetStringPrefix(errorText, 384)
	}
	if len(resultBudgetJSON(envelope)) > limit {
		// Identity, error status and incompleteness are mandatory even when the
		// requested budget cannot hold them. This is an explicit soft overrun.
		minimal := map[string]any{
			"result_id": record.ID, "is_error": failed, "original_is_error": record.IsError,
			"preview_only": true, "stored_text_complete": record.TextComplete,
			"delivery_mode": rule.Mode, "observation_only": true,
		}
		if diagnostic, ok := envelope["error_summary"].(string); ok {
			minimal["error_summary"] = resultBudgetStringPrefix(diagnostic, 64)
		}
		return resultBudgetJSON(minimal)
	}
	resultBudgetAdd(envelope, "original_tool", resultUTF8Prefix(record.Tool, 128), limit)
	resultBudgetAdd(envelope, "text_bytes", len(record.Text), limit)
	resultBudgetAdd(envelope, "structured_bytes", len(record.Structured), limit)
	resultBudgetAdd(envelope, "native_bytes", len(record.Native), limit)
	if (failed || record.IsError) && record.Text != "" && record.Text != record.Error {
		resultBudgetAdd(envelope, "error_text_fragment", map[string]any{
			"text": resultHead(record.Text, 1024, 12), "body_complete": false,
			"scope": "diagnostic prefix of failed observation only",
		}, limit)
	}
	if !record.TextComplete {
		resultBudgetAdd(envelope, "storage_warning", "The observation is incomplete. Full text, structured or native protocol parts may be missing; inspect saved metadata before relying on recovery.", limit)
	}
	// Focuses are selected by trusted host policy, never inferred from tool text.
	// They are explicit fragments, not samples standing in for unread arrays.
	if isJSON {
		resultDeliveryJSONFocus(envelope, value, rule.JSONPointers, limit)
	}
	resultDeliveryKeywordFocus(envelope, record.Text, rule.Keywords, limit)
	if isJSON {
		directory := resultDeliveryJSONDirectory(value, 0)
		if !resultBudgetAdd(envelope, "json_directory", directory, limit) {
			resultBudgetAdd(envelope, "json_directory", resultJSONCompactDirectory(value), limit)
		}
		resultBudgetAdd(envelope, "reader_usage", map[string]any{
			"json":  "view=json with an RFC 6901 pointer; use offset/limit for arrays",
			"query": "view=query for filtering, projection and exact aggregates over the saved array",
			"text":  "view=text with byte offset/limit; view=search with a literal pattern",
			"scope": "directory counts describe saved values only; no array body or random row sample is delivered",
		}, limit)
	} else {
		resultBudgetAdd(envelope, "reader_usage", "view=search with a literal pattern; view=text with byte offset/limit; reads do not repeat the original action", limit)
		if rule.Mode == ResultDeliveryPreview {
			resultDeliveryTextPreview(envelope, record.Text, kind, limit)
		}
	}
	return resultBudgetJSON(envelope)
}

// This is a bounded structural directory, never an array sample. Object scalar
// values are omitted too: a server's total/status field remains available by
// pointer without being silently treated as proof of complete source coverage.
func resultDeliveryJSONDirectory(value any, depth int) map[string]any {
	directory := resultJSONCompactDirectory(value)
	object, ok := value.(map[string]any)
	if !ok || depth >= 2 {
		return directory
	}
	keys := resultJSONKeys(object)
	fields := make([]any, 0, min(12, len(keys)))
	for _, key := range keys[:min(12, len(keys))] {
		field := resultFieldName(key)
		field["directory"] = resultDeliveryJSONDirectory(object[key], depth+1)
		fields = append(fields, field)
	}
	directory["fields"], directory["fields_omitted"] = fields, len(fields) < len(keys)
	return directory
}

func resultDeliveryJSONFocus(envelope map[string]any, value any, pointers []string, limit int) {
	var fields []any
	for _, pointer := range pointers {
		tokens, err := resultQueryPointer(pointer)
		if err != nil {
			continue
		}
		selected, present := resultQueryLookup(value, tokens)
		field := map[string]any{"pointer": pointer, "present": present, "scope": "selected saved value only"}
		if present {
			remaining := 1024
			if resultDeliverySmallJSON(selected, 0, &remaining) {
				field["value"], field["value_complete"] = selected, true
			} else {
				field["directory"], field["value_complete"] = resultDeliveryJSONDirectory(selected, 0), false
			}
		}
		candidate := append(append([]any(nil), fields...), field)
		if resultBudgetAdd(envelope, "focused_json_fields", candidate, limit) {
			fields = candidate
		}
	}
	if len(fields) < len(pointers) {
		resultBudgetAdd(envelope, "focused_json_fields_omitted", true, limit)
	}
}

func resultDeliveryKeywordFocus(envelope map[string]any, text string, keywords []string, limit int) {
	var excerpts []any
	for _, keyword := range keywords {
		if keyword == "" || len(keyword) > 256 {
			continue
		}
		offset := strings.Index(text, keyword)
		excerpt := map[string]any{"pattern": keyword, "scope": "first literal match only; not an exhaustive search"}
		if offset < 0 {
			excerpt["match_found"] = false
		} else {
			start := max(0, offset-96)
			// Move to a valid rune boundary while retaining exact saved byte
			// coordinates, including when the context starts inside a rune.
			for start > 0 && !utf8.RuneStart(text[start]) {
				start--
			}
			body := resultUTF8Prefix(text[start:], min(len(text)-start, offset-start+len(keyword)+96))
			excerpt["match_found"], excerpt["offset"], excerpt["text"] = true, start, body
		}
		candidate := append(append([]any(nil), excerpts...), excerpt)
		if resultBudgetAdd(envelope, "focused_text_fragments", candidate, limit) {
			excerpts = candidate
		}
	}
	if len(excerpts) < len(keywords) {
		resultBudgetAdd(envelope, "focused_text_fragments_omitted", true, limit)
	}
}

// Conservatively bound a focused JSON value before marshaling the envelope.
// This avoids encoding a many-megabyte selected object just to find that it
// does not fit a kilobyte view. Strings can expand sixfold during JSON escaping.
func resultDeliverySmallJSON(value any, depth int, remaining *int) bool {
	if depth > 4 || *remaining <= 0 {
		return false
	}
	*remaining -= 2
	switch typed := value.(type) {
	case string:
		if len(typed) > *remaining/6 {
			return false
		}
		*remaining -= len(typed) * 6
	case json.Number:
		*remaining -= len(typed)
	case map[string]any:
		if len(typed) > 16 {
			return false
		}
		for key, child := range typed {
			if len(key) > *remaining/6 {
				return false
			}
			*remaining -= len(key)*6 + 4
			if !resultDeliverySmallJSON(child, depth+1, remaining) {
				return false
			}
		}
	case []any:
		if len(typed) > 16 {
			return false
		}
		for _, child := range typed {
			*remaining -= 1
			if !resultDeliverySmallJSON(child, depth+1, remaining) {
				return false
			}
		}
	default:
		*remaining -= 5
	}
	return *remaining >= 0
}

func resultDeliveryTextPreview(envelope map[string]any, text, kind string, limit int) {
	low, high := 0, min(len(text), limit)
	for low < high {
		n := low + (high-low+1)/2
		if resultBudgetAdd(envelope, "text_preview", resultBudgetTextSample(text, kind, n), limit) {
			low = n
		} else {
			high = n - 1
		}
	}
	if low > 0 {
		resultBudgetAdd(envelope, "text_preview", resultBudgetTextSample(text, kind, low), limit)
	}
}
