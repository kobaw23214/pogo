package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func expectedResultStringHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func decodeStringHashEnvelope(t *testing.T, text string) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var envelope map[string]any
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	return envelope
}

func assertResultStringHashes(t *testing.T, envelope map[string]any, selected string) string {
	t.Helper()
	fragment, ok := envelope["value_fragment"].(string)
	if !ok || !utf8.ValidString(fragment) {
		t.Fatalf("missing valid decoded string fragment: %#v", envelope)
	}
	if envelope["stored_value_sha256"] != expectedResultStringHash(selected) || envelope["page_sha256"] != expectedResultStringHash(fragment) {
		t.Fatal("fingerprints do not match the selected saved value and actual decoded page")
	}
	if envelope["page_string_bytes"] != json.Number(strconv.Itoa(len(fragment))) {
		t.Fatalf("page byte count does not cover the actual decoded fragment: %#v", envelope["page_string_bytes"])
	}
	start, startErr := strconv.Atoi(string(envelope["offset"].(json.Number)))
	end, endErr := strconv.Atoi(string(envelope["end_offset"].(json.Number)))
	if startErr != nil || endErr != nil || end-start != len(fragment) || selected[start:end] != fragment {
		t.Fatal("fingerprinted range differs from the returned UTF-8 byte range")
	}
	return fragment
}

func TestToolResultStringHashesUseDecodedBytesAcrossJSONAndNative(t *testing.T) {
	selected := "quotes \" and slash \\ & <tag>\n汉🙂"
	structured, _ := json.Marshal(map[string]any{"output": selected, "large": json.Number("9007199254740993")})
	// Both wire spellings mean the same selected string. The fingerprint is
	// never over JSON escaping or the surrounding result representation.
	literal := bytes.ReplaceAll(structured, []byte(`\u0026`), []byte("&"))
	native, _ := json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": selected}}})
	for _, tc := range []struct {
		name, view, pointer string
		record              StoredToolResult
	}{
		{"escaped-json", "json", "/output", StoredToolResult{Tool: "mcp", Structured: structured, Text: "sample", TextComplete: false}},
		{"literal-json", "json", "/output", StoredToolResult{Tool: "mcp", Structured: literal, Text: "sample", TextComplete: false}},
		{"native", "native", "/content/0/text", StoredToolResult{Tool: "mcp", Native: native, Text: "sample", TextComplete: false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader, id := resultReaderFixture(t, tc.record)
			_, envelope := resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": tc.view, "pointer": tc.pointer})
			if assertResultStringHashes(t, envelope, selected) != selected || envelope["stored_text_complete"] != false {
				t.Fatal("fingerprinting changed characters or promoted incomplete source text")
			}
		})
	}
	reader, id := resultReaderFixture(t, StoredToolResult{Tool: "mcp", Structured: structured, TextComplete: true})
	_, number := resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": "json", "pointer": "/large"})
	if number["value"] != json.Number("9007199254740993") || number["stored_value_sha256"] != nil {
		t.Fatal("string fingerprints changed non-string numeric precision or scope")
	}
}

func TestToolResultStringPageHashesFollowUnicodePagination(t *testing.T) {
	selected := "汉🙂&\n\\\"tail"
	data, _ := json.Marshal(map[string]any{"output": selected})
	reader, id := resultReaderFixture(t, StoredToolResult{Tool: "mcp", Structured: data, TextComplete: false, IsError: true, Error: "upstream stopped"})
	offset := 0
	var recovered strings.Builder
	for pages := 0; ; pages++ {
		if pages > len(selected) {
			t.Fatal("string pagination did not finish")
		}
		_, envelope := resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": "json", "pointer": "/output", "offset": offset, "limit": 1})
		recovered.WriteString(assertResultStringHashes(t, envelope, selected))
		if envelope["original_is_error"] != true || envelope["original_error"] != "upstream stopped" || envelope["stored_text_complete"] != false {
			t.Fatal("hashes erased original failure or source incompleteness")
		}
		if envelope["complete"] == true {
			break
		}
		offset, _ = strconv.Atoi(string(envelope["next_offset"].(json.Number)))
	}
	if recovered.String() != selected {
		t.Fatal("fingerprinted pages changed or duplicated original characters")
	}
	_, endPage := resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": "json", "pointer": "/output", "offset": len(selected), "limit": 1})
	if assertResultStringHashes(t, endPage, selected) != "" || endPage["complete"] != true {
		t.Fatal("empty terminal page retained another fragment's hash")
	}
}

func TestToolResultStringHashUsesEmptyValueRatherThanPreviousPage(t *testing.T) {
	reader, id := resultReaderFixture(t, StoredToolResult{Tool: "mcp", Structured: json.RawMessage(`{"output":""}`), TextComplete: false})
	_, envelope := resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": "json", "pointer": "/output"})
	if assertResultStringHashes(t, envelope, "") != "" || envelope["complete"] != true || envelope["stored_text_complete"] != false {
		t.Fatal("empty selected value lost its exact hash or source boundary")
	}
}

func TestToolResultNativeMediaMetadataIsNotHashedAsSavedContent(t *testing.T) {
	reader, id := resultReaderFixture(t, StoredToolResult{Tool: "mcp", Native: json.RawMessage(`{"content":[{"type":"image","mimeType":"image/png","data":"PRIVATE_ENCODED_MEDIA"}]}`), TextComplete: true})
	result, envelope := resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": "native", "pointer": "/content/0/data/reason"})
	if _, ok := envelope["value_fragment"].(string); !ok || strings.Contains(result.Text, "PRIVATE_ENCODED_MEDIA") {
		t.Fatal("native media fence stopped preserving readable metadata only")
	}
	for _, key := range []string{"stored_value_sha256", "page_sha256", "page_string_bytes", "hash_scope"} {
		if _, exists := envelope[key]; exists {
			t.Fatalf("synthetic media metadata was labelled as saved content: %s", key)
		}
	}
}

func TestBudgetReadStringHashCoversFinalModelPage(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	selected := strings.Repeat("&汉🙂\n\\\"", 20000)
	data, _ := json.Marshal(map[string]any{"output": selected})
	ref, err := r.results.Save(StoredToolResult{Tool: "mcp", Structured: data, TextComplete: false})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"result_id": ref.ID, "view": "json", "pointer": "/output"})
	host, view := budgetPageTestRead(t, r, args, 512)
	if view.Delivery == nil || !view.Delivery.Delivered || !view.Reduced {
		t.Fatal("fixture did not produce a delivered reduced model page")
	}
	envelope := decodeStringHashEnvelope(t, view.Text)
	fragment := assertResultStringHashes(t, envelope, selected)
	if len(fragment) == 0 || len(fragment) >= len(selected) || envelope["stored_text_complete"] != false {
		t.Fatal("model page did not preserve a bounded saved-string read")
	}
	var original map[string]any
	_ = json.Unmarshal([]byte(host.Text), &original)
	if envelope["page_sha256"] == original["page_sha256"] {
		t.Fatal("smaller model page retained the original host page hash")
	}
}

func TestBudgetReadStringDropsOptionalHashesBeforeUsefulContent(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	selected := strings.Repeat("abc", 5000)
	data, _ := json.Marshal(map[string]any{"output": selected})
	ref, err := r.results.Save(StoredToolResult{Tool: "mcp", Structured: data, TextComplete: true})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"result_id": ref.ID, "view": "json", "pointer": "/output"})
	_, view := budgetPageTestRead(t, r, args, 192)
	if view.Delivery == nil || !view.Delivery.Delivered {
		t.Fatal("optional fingerprints prevented useful small-budget content")
	}
	envelope := decodeStringHashEnvelope(t, view.Text)
	if fragment, _ := envelope["value_fragment"].(string); fragment == "" {
		t.Fatal("small-budget page delivered no actual string bytes")
	}
	for _, key := range []string{"stored_value_sha256", "page_sha256", "page_string_bytes", "hash_scope"} {
		if _, exists := envelope[key]; exists {
			t.Fatalf("optional metadata did not yield to useful content: %s", key)
		}
	}
}

func TestBudgetReadStringOmissionCarriesNoPageFingerprint(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	key := strings.Repeat("wide", 500)
	data, _ := json.Marshal(map[string]any{key: "汉"})
	ref, err := r.results.Save(StoredToolResult{Tool: "mcp", Structured: data, TextComplete: false})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"result_id": ref.ID, "view": "json", "pointer": "/" + key})
	_, view := budgetPageTestRead(t, r, args, 128)
	if view.Delivery == nil || !view.Delivery.Undeliverable || view.Delivery.Delivered {
		t.Fatal("fixture did not omit a page whose identifying pointer exceeds the budget")
	}
	envelope := decodeStringHashEnvelope(t, view.Text)
	for _, key := range []string{"value_fragment", "stored_value_sha256", "page_sha256", "page_string_bytes", "hash_scope"} {
		if _, exists := envelope[key]; exists {
			t.Fatalf("omitted page retained content/fingerprint: %s", key)
		}
	}
}

func TestSelectedStringHashHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if digest, err := resultSelectedStringSHA256(ctx, strings.Repeat("x", 100000)); err != context.Canceled || digest != "" {
		t.Fatal("canceled fingerprint read produced a digest")
	}
}
