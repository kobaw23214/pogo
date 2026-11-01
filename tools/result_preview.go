package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	resultPreviewBytes = 12 * 1024
	resultPreviewLines = 200
)

// previewToolResult is a deterministic view of an immutable observation. Counts
// describe that observation only: a tool may itself have returned one page or
// partial coverage. The original JSON is never cut in the middle of a token.
func previewToolResult(record StoredToolResult) (text, kind string) {
	if value, ok := resultJSONValue(record); ok {
		kind = "json"
		sourceBytes := len(record.Structured)
		if !json.Valid(record.Structured) {
			sourceBytes = len(record.Text)
		}
		if sourceBytes <= resultPreviewBytes-2048 {
			envelope := resultPreviewEnvelope(record, false)
			envelope["value"] = value
			if encoded, err := json.Marshal(envelope); err == nil && len(encoded) <= resultPreviewBytes {
				return string(encoded), kind
			}
		}
		envelope := resultPreviewEnvelope(record, true)
		remaining := 28
		envelope["observation_shape_and_samples"] = resultJSONDescription(value, 0, &remaining)
		encoded, _ := json.Marshal(envelope)
		if len(encoded) > resultPreviewBytes {
			envelope["observation_shape_and_samples"] = resultJSONCompactDirectory(value)
			encoded, _ = json.Marshal(envelope)
		}
		return string(encoded), kind
	}

	kind = resultTextKind(record.Tool, record.Text)
	status := ""
	if record.IsError || record.Error != "" {
		status = "Original tool result failed. " + resultHead(record.Error, 1024, 8) + "\n"
	}
	if !record.TextComplete {
		status += "Stored text is partial; this is not proof of full source coverage.\n"
	}
	budget := resultPreviewBytes - len(status)
	if len(record.Text) <= budget && resultLineCount(record.Text)+resultLineCount(status) < resultPreviewLines {
		return status + record.Text, kind
	}
	note := "\n[Preview only: middle omitted. Use tool_result search with a literal pattern to locate text, or text byte ranges. Saved observation only; source completeness unknown.]\n"
	budget -= len(note)
	headBudget := budget / 2
	if kind == "webpage" || kind == "document" {
		headBudget = budget * 3 / 4
	}
	head := resultHead(record.Text, headBudget, 110)
	tail := resultTail(record.Text[len(head):], budget-headBudget, 65)
	return status + head + note + tail, kind
}

func resultPreviewEnvelope(record StoredToolResult, partial bool) map[string]any {
	envelope := map[string]any{
		"preview_only":         partial,
		"original_is_error":    record.IsError,
		"stored_text_complete": record.TextComplete,
		"coverage":             "Counts and samples describe the stored observation only; they do not establish complete source coverage or whole-dataset statistics.",
	}
	if record.Error != "" {
		envelope["original_error"] = resultUTF8Prefix(record.Error, 1024)
	}
	if partial {
		envelope["read_more"] = "Use tool_result with this result ID and a JSON pointer or text byte range. Samples are not a replacement for the original schema."
	}
	return envelope
}

func resultJSONValue(record StoredToolResult) (any, bool) {
	if value, ok := resultDecodeJSON(record.Structured); ok {
		return value, true
	}
	return resultDecodeJSON([]byte(record.Text))
}

func resultDecodeJSON(raw []byte) (any, bool) {
	if len(raw) == 0 || !json.Valid(raw) {
		return nil, false
	}
	// Duplicate object keys have no unambiguous field projection. Keep the
	// original text addressable instead of silently selecting the last value.
	if duplicate, err := jsonHasDuplicateObjectKeys(raw); err != nil || duplicate {
		return nil, false
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	return value, true
}

func resultJSONType(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	default:
		return "number"
	}
}

// Descriptions are explicitly a separate representation, rather than JSON with
// silently removed fields that might be mistaken for the original tool schema.
func resultJSONDescription(value any, depth int, remaining *int) any {
	if *remaining <= 0 || depth >= 3 {
		return resultJSONDirectory(value)
	}
	*remaining = *remaining - 1
	switch typed := value.(type) {
	case map[string]any:
		keys := resultJSONKeys(typed)
		limit := min(8, len(keys))
		for limit < min(16, len(keys)) && resultJSONKeyPriority(keys[limit]) < 100 {
			limit++
		}
		fields := make([]any, 0, limit)
		for _, key := range keys[:limit] {
			field := resultFieldName(key)
			if *remaining > 0 {
				field["sample"] = resultJSONDescription(typed[key], depth+1, remaining)
			} else {
				field["type"] = resultJSONType(typed[key])
			}
			fields = append(fields, field)
		}
		return map[string]any{"type": "object", "stored_field_count": len(keys), "fields_sample": fields, "fields_omitted": len(keys) > len(fields)}
	case []any:
		items := make([]any, 0, min(3, len(typed)))
		for _, item := range typed[:min(3, len(typed))] {
			if *remaining <= 0 {
				break
			}
			items = append(items, resultJSONDescription(item, depth+1, remaining))
		}
		return map[string]any{"type": "array", "stored_item_count": len(typed), "items_sample": items, "items_omitted": len(items) < len(typed)}
	case string:
		if len(typed) > 160 {
			return map[string]any{"type": "string", "bytes": len(typed), "prefix_sample": resultUTF8Prefix(typed, 160), "omitted": true}
		}
		return typed
	case json.Number:
		if len(typed) > 64 {
			return map[string]any{"type": "number", "stored_number_bytes": len(typed), "omitted": true}
		}
		return typed
	default:
		return typed
	}
}

// Directory views never expose scalar values. In particular native MCP image
// data cannot enter the model context merely because the caller asks for an index.
func resultJSONDirectory(value any) map[string]any {
	directory := map[string]any{"type": resultJSONType(value)}
	switch typed := value.(type) {
	case map[string]any:
		keys := resultJSONKeys(typed)
		fields := make([]any, 0, min(16, len(keys)))
		for _, key := range keys[:min(16, len(keys))] {
			field := resultFieldName(key)
			field["type"] = resultJSONType(typed[key])
			fields = append(fields, field)
		}
		directory["stored_field_count"], directory["fields"], directory["fields_omitted"] = len(keys), fields, len(keys) > len(fields)
	case []any:
		directory["stored_item_count"] = len(typed)
	case string:
		directory["bytes"] = len(typed)
	}
	return directory
}

func resultJSONCompactDirectory(value any) map[string]any {
	directory := map[string]any{"type": resultJSONType(value)}
	switch typed := value.(type) {
	case map[string]any:
		directory["stored_field_count"] = len(typed)
	case []any:
		directory["stored_item_count"] = len(typed)
	case string:
		directory["bytes"] = len(typed)
	}
	return directory
}

func resultFieldName(key string) map[string]any {
	if len(key) <= 128 {
		return map[string]any{"name": key}
	}
	return map[string]any{"name_prefix": resultUTF8Prefix(key, 128), "name_bytes": len(key), "name_omitted": true}
}

func resultJSONKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, right := resultJSONKeyPriority(keys[i]), resultJSONKeyPriority(keys[j])
		if left != right {
			return left < right
		}
		return keys[i] < keys[j]
	})
	return keys
}

// These are existing navigation/status fields, never inferred totals or a
// fabricated aggregation over the samples. Ordinary fields remain alphabetical.
func resultJSONKeyPriority(key string) int {
	if len(key) > 32 {
		return 100
	}
	key = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
	switch key {
	case "status", "iserror", "error", "errors":
		return 0
	case "code":
		return 1
	case "total", "totalcount", "count":
		return 2
	case "hasmore", "nextcursor", "next":
		return 3
	case "page", "cursor", "offset", "limit":
		return 4
	default:
		return 100
	}
}

func resultTextKind(tool, body string) string {
	tool = strings.ToLower(tool)
	prefix := strings.ToLower(resultUTF8Prefix(strings.TrimSpace(body), 256))
	if strings.Contains(tool, "browser") || strings.HasPrefix(prefix, "<!doctype html") || strings.HasPrefix(prefix, "<html") {
		return "webpage"
	}
	if tool == "read" || strings.Contains(tool, "document") || strings.Contains(tool, "information") {
		return "document"
	}
	if tool == "bash" || strings.Contains(tool, "exec") || strings.Contains(tool, "log") || strings.HasPrefix(tool, "skill") {
		return "log"
	}
	return "text"
}

func resultLineCount(value string) int {
	if value == "" {
		return 0
	}
	return strings.Count(value, "\n") + 1
}

func resultUTF8Prefix(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}

func resultHead(value string, bytes, lines int) string {
	value = resultUTF8Prefix(value, bytes)
	for offset, count := 0, 0; offset < len(value); {
		n := strings.IndexByte(value[offset:], '\n')
		if n < 0 {
			break
		}
		offset += n + 1
		count++
		if count >= lines {
			return value[:offset]
		}
	}
	return value
}

func resultTail(value string, bytes, lines int) string {
	if bytes <= 0 {
		return ""
	}
	start := max(0, len(value)-bytes)
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	value = value[start:]
	for end, count := len(value), 0; end > 0; {
		n := strings.LastIndexByte(value[:end], '\n')
		if n < 0 {
			break
		}
		count++
		if count >= lines {
			return value[n+1:]
		}
		end = n
	}
	return value
}

func resultStatusText(record StoredToolResult) string {
	status := "success"
	if record.IsError {
		status = "error"
	}
	text := fmt.Sprintf("Original tool: %s; original status: %s; stored_text_complete: %t.\n", resultUTF8Prefix(record.Tool, 256), status, record.TextComplete)
	if record.Error != "" {
		text += "Original error: " + resultUTF8Prefix(record.Error, 1024) + "\n"
	}
	return text
}
