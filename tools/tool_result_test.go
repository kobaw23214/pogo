package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func resultReaderFixture(t *testing.T, record StoredToolResult) (*ToolResultRead, string) {
	t.Helper()
	store := newResultStore(t.TempDir())
	ref, err := store.Save(record)
	if err != nil {
		t.Fatal(err)
	}
	return &ToolResultRead{Store: store}, ref.ID
}

func resultReaderJSON(t *testing.T, reader *ToolResultRead, args map[string]any) (Result, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	result, err := reader.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Text) > resultReadOutputMax || !json.Valid([]byte(result.Text)) {
		t.Fatalf("reader returned an oversized or invalid JSON page: %d bytes", len(result.Text))
	}
	decoder := json.NewDecoder(strings.NewReader(result.Text))
	decoder.UseNumber()
	var envelope map[string]any
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	return result, envelope
}

func resultReaderDetails(t *testing.T, result Result) ToolResultReadDetails {
	t.Helper()
	details, ok := result.Details.(ToolResultReadDetails)
	if !ok {
		t.Fatalf("unexpected reader details: %T", result.Details)
	}
	return details
}

func TestToolResultUTF8TextPagination(t *testing.T) {
	text := "汉字🙂tail"
	reader, id := resultReaderFixture(t, StoredToolResult{Tool: "mcp", Text: text, TextComplete: true})
	offset := 0
	var fragments strings.Builder
	for page := 0; ; page++ {
		if page > len(text) {
			t.Fatal("text paging did not advance")
		}
		raw, _ := json.Marshal(map[string]any{"result_id": id, "view": "text", "offset": offset, "limit": 1})
		result, err := reader.Execute(context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.SplitN(result.Text, "---\n", 2)
		if len(parts) != 2 || !utf8.ValidString(parts[1]) || parts[1] == "" {
			t.Fatalf("invalid UTF-8 fragment: %q", result.Text)
		}
		fragments.WriteString(parts[1])
		details := resultReaderDetails(t, result)
		if details.Complete {
			if details.NextOffset != nil {
				t.Fatal("complete text range has continuation")
			}
			break
		}
		if details.NextOffset == nil || *details.NextOffset <= offset {
			t.Fatal("text continuation did not advance")
		}
		offset = *details.NextOffset
	}
	if fragments.String() != text {
		t.Fatalf("text pages lost or duplicated content: %q", fragments.String())
	}
	// An arbitrary offset within a rune repeats that whole rune rather than
	// returning invalid UTF-8, and reports the adjusted actual range.
	raw, _ := json.Marshal(map[string]any{"result_id": id, "view": "text", "offset": 1, "limit": 1})
	result, err := reader.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if details := resultReaderDetails(t, result); details.Offset != 0 || details.NextOffset == nil || *details.NextOffset != 3 {
		t.Fatalf("unexpected adjusted rune range: %+v", details)
	}
}

func TestToolResultEscapedJSONStringPagination(t *testing.T) {
	text := strings.Repeat("\x00\"汉\n", 10000)
	structured, _ := json.Marshal(map[string]any{"body": text})
	reader, id := resultReaderFixture(t, StoredToolResult{Tool: "mcp", Text: "partial", Structured: structured, IsError: true, Error: "upstream failed", TextComplete: false})
	offset := 0
	var fragments strings.Builder
	for page := 0; ; page++ {
		if page > 30 {
			t.Fatal("escaped JSON string paging did not advance")
		}
		result, envelope := resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": "json", "pointer": "/body", "offset": offset, "limit": resultReadTextMax})
		fragment, ok := envelope["value_fragment"].(string)
		if !ok || !utf8.ValidString(fragment) || fragment == "" {
			t.Fatalf("bad JSON string fragment: %#v", envelope)
		}
		fragments.WriteString(fragment)
		if envelope["original_is_error"] != true || envelope["stored_text_complete"] != false || envelope["original_error"] != "upstream failed" {
			t.Fatal("reading a string erased original status/coverage")
		}
		details := resultReaderDetails(t, result)
		if details.Complete {
			break
		}
		if details.NextOffset == nil || *details.NextOffset <= offset {
			t.Fatal("JSON string continuation did not advance")
		}
		offset = *details.NextOffset
	}
	if fragments.String() != text {
		t.Fatal("escaped JSON string pages lost or duplicated bytes")
	}
}

func TestToolResultJSONArrayOmissionAndRecovery(t *testing.T) {
	large := strings.Repeat("large", 10000)
	structured, _ := json.Marshal([]any{large, map[string]any{"ok": true}})
	reader, id := resultReaderFixture(t, StoredToolResult{Tool: "mcp", Structured: structured, TextComplete: true})
	result, envelope := resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": "json", "limit": 2})
	items := envelope["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["omitted_value"] != true || items[0].(map[string]any)["pointer_suffix"] != "/0" || items[1].(map[string]any)["ok"] != true {
		t.Fatalf("large row was silently lost: %#v", items)
	}
	if details := resultReaderDetails(t, result); details.Complete || !details.Omitted || details.NextOffset != nil {
		t.Fatalf("omission reported complete: %+v", details)
	}
	_, recovered := resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": "json", "pointer": "/0", "limit": 5})
	if recovered["value_fragment"] != "large" || recovered["next_offset"] != json.Number("5") {
		t.Fatalf("cannot recover omitted row: %#v", recovered)
	}
}

func TestToolResultJSONArrayPagination(t *testing.T) {
	reader, id := resultReaderFixture(t, StoredToolResult{Tool: "mcp", Structured: json.RawMessage(`[0,1,2,3,4]`), TextComplete: true})
	_, first := resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": "json", "offset": 1, "limit": 2})
	if first["next_offset"] != json.Number("3") || first["complete"] != false {
		t.Fatalf("incorrect first page: %#v", first)
	}
	_, last := resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": "json", "offset": 3, "limit": 2})
	if last["complete"] != true || last["next_offset"] != nil || len(last["items"].([]any)) != 2 {
		t.Fatalf("incorrect last page: %#v", last)
	}
}

func TestToolResultNativeMediaIsolationAndTextPaging(t *testing.T) {
	secret := strings.Repeat("BINARY_SECRET", 8000)
	native, _ := json.Marshal(map[string]any{"content": []any{
		map[string]any{"type": "image", "data": secret},
		map[string]any{"type": "audio", "data": secret},
		map[string]any{"type": "resource", "resource": map[string]any{"blob": secret, "mimeType": "application/pdf"}},
		map[string]any{"type": "text", "text": "汉字"},
	}})
	reader, id := resultReaderFixture(t, StoredToolResult{Tool: "mcp", Text: "not JSON", Native: native, TextComplete: true})
	for _, pointer := range []string{"", "/content", "/content/0", "/content/0/data", "/content/1/data", "/content/2/resource/blob"} {
		t.Run(pointer, func(t *testing.T) {
			result, _ := resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": "native", "pointer": pointer})
			if strings.Contains(result.Text, "BINARY_SECRET") {
				t.Fatal("native binary entered model-facing text")
			}
			if pointer != "" && !strings.Contains(result.Text, "omitted_binary") {
				t.Fatal("native binary lost its omission metadata")
			}
		})
	}
	_, text := resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": "native", "pointer": "/content/3/text", "limit": 1})
	if text["value_fragment"] != "汉" || text["next_offset"] != json.Number("3") {
		t.Fatalf("native text is not UTF-8 paginated: %#v", text)
	}
	query, _ := json.Marshal(map[string]any{"result_id": id, "view": "query", "pointer": "/content"})
	if result, err := reader.Execute(context.Background(), query); err == nil || result.Text != "" {
		t.Fatal("query unexpectedly read native protocol instead of structured/text JSON")
	}
}

func TestToolResultCanceledReadReturnsNoPage(t *testing.T) {
	reader, id := resultReaderFixture(t, StoredToolResult{Tool: "mcp", Structured: json.RawMessage(`[1,2,3]`), TextComplete: true})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, view := range []string{"summary", "text", "json", "native", "query"} {
		raw, _ := json.Marshal(map[string]any{"result_id": id, "view": view})
		result, err := reader.Execute(ctx, raw)
		if !errors.Is(err, context.Canceled) || result.Text != "" {
			t.Fatalf("%s delivered a page after cancellation: %q, %v", view, result.Text, err)
		}
	}
	// Exercise the local JSON worker directly: it must also observe cancellation
	// after decoding, rather than relying only on the initial store-read check.
	result, err := readResultJSON(ctx, StoredToolResult{Structured: json.RawMessage(`[1,2,3]`)}, toolResultReadArgs{View: "json"}, ToolResultReadDetails{View: "json"})
	if !errors.Is(err, context.Canceled) || result.Text != "" {
		t.Fatalf("local JSON reader ignored cancellation: %q, %v", result.Text, err)
	}
}
