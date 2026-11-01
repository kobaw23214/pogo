package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// PrepareResult is the last output boundary before an executed tool reaches
// the agent history. Tools may keep their existing UI details and previews;
// external errors and newly registered tools cannot bypass this boundary.
// Storage failure changes only observability, never whether an action ran.
func (r *Registry) PrepareResult(ctx context.Context, tool string, res Result, callErr error) Result {
	res.ResultID = "" // a tool cannot choose its own trusted result reference
	text := res.Text
	if text == "" && callErr != nil {
		text = callErr.Error()
	}
	if text == "" && callErr == nil {
		p := res.Payload
		legacyPath, legacyTruncated := legacyResultPath(tool, res.Details)
		emptyPayload := p == nil || p.Text == "" && len(p.Structured) == 0 && len(p.Native) == 0 && p.FullTextPath == ""
		if emptyPayload && (!legacyTruncated || legacyPath == "") {
			// Empty successful output is not readable evidence. Adding a
			// placeholder/footer would invent a nonempty body, causing the
			// citation layer to stamp it and demand attribution for an empty read.
			res.Text, res.Payload = "", nil
			return res
		}
	}
	if tool == ToolResultReadName || tool == "read" || tool == "ls" || tool == "find" || tool == "grep" || tool == "todo" {
		// Re-readable sources and the result reader must not create chains of
		// results about results. Their own pagination remains authoritative.
		// Do not cut an already bounded host page a second time: its offsets,
		// JSON syntax and UI body ranges describe exactly the original page.
		res.Text = text
		if callErr != nil {
			res.Text = boundResultText(text, MaxBytes, MaxLines)
		}
		return res
	}
	if r == nil || r.results == nil {
		res.Text = boundResultText(text, MaxBytes, MaxLines)
		return res
	}
	record := StoredToolResult{Tool: tool, IsError: callErr != nil, Text: text, TextComplete: true}
	if callErr != nil {
		record.Error = boundResultText(callErr.Error(), 2048, 16)
	}
	if res.Payload != nil {
		p := res.Payload
		record.Text, record.Structured, record.Native = p.Text, p.Structured, p.Native
		record.TextComplete = !p.Incomplete
		if p.FullTextPath != "" {
			if full, err := r.readLegacyResult(ctx, p.FullTextPath); err == nil {
				// Recovering the text cannot repair missing structured/native
				// payload parts. Incomplete covers that observation as well.
				record.Text, record.TextComplete = full, !p.Incomplete
			} else {
				record.TextComplete = false
			}
		}
	} else if path, truncated := legacyResultPath(tool, res.Details); truncated {
		record.TextComplete = false
		if path != "" {
			if full, err := r.readLegacyResult(ctx, path); err == nil {
				record.Text, record.TextComplete = full, true
			}
		}
	}
	if !utf8.ValidString(record.Text) {
		record.Text = strings.ToValidUTF8(record.Text, "\uFFFD")
		record.TextComplete = false
	}
	ref, saveErr := r.results.Save(record)
	storageFallback := ""
	if saveErr != nil && (len(record.Structured) > 0 || len(record.Native) > 0) {
		// A large/invalid protocol payload must not make otherwise retainable
		// observations disappear behind a preview. Prefer retaining Structured
		// when dropping Native is sufficient, then try all text intact. Each
		// fallback explicitly records the missing parts and stays incomplete.
		keepStructured := []bool{false}
		if len(record.Native) > 0 && len(record.Structured) > 0 {
			keepStructured = []bool{true, false}
		}
		for _, keep := range keepStructured {
			if !keep && record.Text == "" {
				continue // an ID for fallback metadata alone cannot recover output
			}
			fallback, note := resultStorageFallback(record, keep)
			if fallbackRef, err := r.results.Save(fallback); err == nil {
				record, ref, storageFallback = fallback, fallbackRef, note
				break
			}
		}
	}
	// Previewing is deterministic: no extra model call and no fabricated
	// aggregate over a sample. Large structured values stay addressable as JSON.
	view, kind := previewToolResult(record)
	oversized := len(record.Text) > MaxBytes || len(record.Structured) > MaxBytes || countResultLines(record.Text) > MaxLines
	if !oversized && !(record.Text == "" && len(record.Structured) > 0) {
		view = record.Text
	}
	if record.IsError && record.Error != "" && !strings.Contains(view, record.Error) {
		view += "\n[Original tool failed: " + record.Error + "]"
	}
	if !record.TextComplete {
		view += "\n[This observation is incomplete; missing or unserializable source content was not recovered.]"
	}
	if ref != nil {
		ref.Kind = kind
		res.ResultID = ref.ID
			note := fmt.Sprintf("\n\n[Stored result: %s; kind=%s; original_status=%s; text_complete=%t. Use tool_result with result_id to search text, inspect fields or read byte ranges without repeating the action.]",
			ref.ID, kind, resultOutcome(record.IsError), record.TextComplete) + storageFallback
		res.Text = boundResultText(view, MaxBytes-len(note), MaxLines-countResultLines(note)-1) + note
		res.Payload = nil
		return res
	}
	// Never advertise an ID that was not committed. Keep a bounded, useful
	// view, preserving legacy spill notes when present, and state the failure.
	note := "\n\n[This observation could not be saved as a tool result. No result_id is available; do not repeat a side-effecting action just to recover its output.]"
	// Existing previews can include a working legacy path even if the new
	// structured record could not be committed. Do not erase that escape hatch.
	if path, truncated := legacyResultPath(tool, res.Details); truncated && path != "" {
		if _, err := r.readLegacyResult(ctx, path); err == nil {
			note += "\n[Legacy text output remains available at " + path + "; read it with the existing file tools.]"
		}
	}
	res.Text = boundResultText(view, MaxBytes-len(note), MaxLines-3) + note
	res.Payload = nil
	return res
}

// The saved native fallback is host metadata, explicitly distinguished from an
// original protocol response. The text and any retained structured bytes remain
// unmodified, so text/JSON recovery keeps its original offsets and values.
func resultStorageFallback(record StoredToolResult, keepStructured bool) (StoredToolResult, string) {
	fallback := record
	fallback.TextComplete = false
	metadata := map[string]any{
		"text_retained": true,
		"reason":        "The complete original payload could not be stored. This is host fallback metadata, not the original native response.",
	}
	var missing []string
	if len(record.Native) > 0 {
		metadata["native_not_preserved"] = true
		missing = append(missing, "native protocol")
	}
	retained := "tool text"
	if keepStructured {
		metadata["structured_retained"] = true
		retained += " and structured data"
	} else {
		fallback.Structured = nil
		if len(record.Structured) > 0 {
			metadata["structured_not_preserved"] = true
			missing = append(missing, "structured")
		}
	}
	fallback.Native, _ = json.Marshal(map[string]any{"pogo_storage_fallback": metadata})
	note := fmt.Sprintf("\n[Storage fallback: original %s payload was not preserved. Saved %s remains readable with tool_result; the observation is incomplete.]", strings.Join(missing, "/"), retained)
	return fallback, note
}

func resultOutcome(failed bool) string {
	if failed {
		return "error"
	}
	return "success"
}

// Only host-defined detail types can nominate a legacy file. Arbitrary external
// tool details must not turn a display field into a host filesystem read.
func legacyResultPath(tool string, details any) (string, bool) {
	switch d := details.(type) {
	case BashDetails:
		return d.FullOutputPath, d.Truncated
	case SynthDetails:
		return d.FullOutputPath, d.Truncated
	case MCPDetails:
		return d.FullOutputPath, d.Truncated
	case MCPDescribeDetails:
		return d.FullOutputPath, d.Truncated
	case SubagentDetails:
		return d.FullOutputPath, d.FullOutputPath != ""
	case map[string]any:
		if tool == "browser" || tool == MCPDescribeName {
			path, _ := d["full_output_path"].(string)
			truncated, _ := d["truncated"].(bool)
			return path, truncated
		}
	}
	return "", false
}

func (r *Registry) readLegacyResult(ctx context.Context, path string) (string, error) {
	bases := []string{r.spillDir}
	if r.scopedOutputRoot != "" {
		bases = append(bases, r.scopedOutputRoot)
	}
	for _, base := range bases {
		if base == "" {
			continue
		}
		absBase, err := filepath.Abs(base)
		if err != nil {
			continue
		}
		absPath, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(absBase, absPath)
		if err != nil || !filepath.IsLocal(rel) || !strings.HasSuffix(rel, ".log") {
			continue
		}
		root, err := os.OpenRoot(absBase)
		if err != nil {
			continue
		}
		f, err := scopedOpen(root, rel, false)
		if err != nil {
			root.Close()
			continue
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > MaxCommandOutputBytes {
			f.Close()
			root.Close()
			continue
		}
		data, err := readResultBytes(ctx, f, MaxCommandOutputBytes)
		f.Close()
		root.Close()
		return string(data), err
	}
	return "", fmt.Errorf("original output is not available in this session")
}

func countResultLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(s, "\n"), "\n") + 1
}

// Unlike the legacy line-only truncator this always makes progress through a
// long single line and preserves both the beginning and the final status.
func boundResultText(text string, bytes, lines int) string {
	if len(text) <= bytes && countResultLines(text) <= lines {
		return text
	}
	const marker = "\n…[middle omitted from model view]…\n"
	budget := bytes - len(marker)
	if budget < 8 || lines < 4 {
		return ""
	}
	headN, tailN := budget/2, budget-budget/2
	if headN > len(text) {
		headN = len(text)
	}
	for headN > 0 && headN < len(text) && !utf8.RuneStart(text[headN]) {
		headN--
	}
	start := len(text) - min(tailN, len(text)-headN)
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	head, tail := text[:headN], text[start:]
	if parts := strings.Split(head, "\n"); len(parts) > lines/2 {
		head = strings.Join(parts[:lines/2], "\n")
	}
	if parts := strings.Split(tail, "\n"); len(parts) > lines/2-2 {
		tail = strings.Join(parts[len(parts)-(lines/2-2):], "\n")
	}
	return head + marker + tail
}
