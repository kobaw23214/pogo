package tools

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func budgetPageTestRead(t *testing.T, r *Registry, args json.RawMessage, tokens int64) (Result, ModelResultBudgetView) {
	t.Helper()
	page, err := (&ToolResultRead{Store: r.results}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("actual local read: %v; args=%s", err, args)
	}
	details, _ := json.Marshal(page.Details)
	view := r.BudgetResultView(context.Background(), ModelResultBudgetRequest{Tool: ToolResultReadName, Text: page.Text, Args: args, Details: details, Tokens: tokens})
	return page, view
}

func TestBudgetReadPage384DeliversAndContinuesFirstFive(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	rows := make([]any, 8)
	for i := range rows {
		rows[i] = map[string]any{"id": i, "name": "row-" + strconv.Itoa(i), "description": strings.Repeat("abcdefghij", 9)}
	}
	data, _ := json.Marshal(map[string]any{"rows": rows})
	ref, err := r.results.Save(StoredToolResult{Tool: "external", Text: string(data), Structured: data, TextComplete: true})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"result_id": ref.ID, "view": "query", "pointer": "/rows", "limit": 5})
	seen := 0
	for turn := 0; seen < 5 && turn < 10; turn++ {
		original, view := budgetPageTestRead(t, r, args, 384)
		if turn == 0 && len(original.Text) <= 384*4 {
			t.Fatalf("fixture must exercise a page larger than 384 tokens: %d bytes", len(original.Text))
		}
		if view.Delivery == nil || !view.Delivery.Delivered || view.Delivery.Undeliverable || len(view.Text) > 384*4 || view.ResultID != "" {
			t.Fatalf("expected useful bounded row delivery, got %#v: %s", view.Delivery, view.Text)
		}
		body := decodePreparedOutputJSON(t, view.Text)
		items, ok := body["items"].([]any)
		if !ok || len(items) == 0 || body["offset"] != float64(seen) || body["shown_items"] != float64(len(items)) {
			t.Fatalf("invalid delivered page: %s", view.Text)
		}
		for _, raw := range items {
			item := raw.(map[string]any)
			if item["original_index"] != float64(seen) || item["value"].(map[string]any)["id"] != float64(seen) {
				t.Fatalf("lost, duplicated or reordered row at %d: %#v", seen, item)
			}
			seen++
		}
		if body["next_offset"] != float64(seen) || body["complete"] != false {
			t.Fatalf("continuation must match actual displayed rows and saved-source scope: %s", view.Text)
		}
		args = view.Delivery.NextRequest
		if seen < 5 && len(args) == 0 {
			t.Fatal("partial delivered prefix has no executable continuation")
		}
	}
	if seen != 5 {
		t.Fatalf("could not reconstruct exactly the first five rows: %d", seen)
	}
}

func TestBudgetReadPageBusinessOmittedFieldIsRealData(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	data := json.RawMessage(`[{"omitted_value":true,"name":"real business object"}]`)
	ref, _ := r.results.Save(StoredToolResult{Tool: "external", Text: string(data), Structured: data, TextComplete: true})
	args, _ := json.Marshal(map[string]any{"result_id": ref.ID, "view": "json", "limit": 1})
	_, view := budgetPageTestRead(t, r, args, 384)
	if view.Delivery == nil || !view.Delivery.Delivered || view.Delivery.Undeliverable {
		t.Fatalf("business field was interpreted as a host receipt: %s", view.Text)
	}
	item := decodePreparedOutputJSON(t, view.Text)["items"].([]any)[0].(map[string]any)
	if item["name"] != "real business object" || item["omitted_value"] != true {
		t.Fatal("business object content was altered")
	}
}

func TestBudgetReadPageHugeRowNeverAdvancesPastOmission(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	data, _ := json.Marshal(map[string]any{"rows": []any{map[string]any{"id": 0, "body": strings.Repeat("huge row ", 10000)}, map[string]any{"id": 1}}})
	ref, _ := r.results.Save(StoredToolResult{Tool: "external", Text: string(data), Structured: data, TextComplete: true})
	args, _ := json.Marshal(map[string]any{"result_id": ref.ID, "view": "query", "pointer": "/rows", "limit": 2})
	original, view := budgetPageTestRead(t, r, args, 384)
	if !strings.Contains(original.Text, `"omitted_value":true`) {
		t.Fatal("fixture did not exercise the original host omission receipt")
	}
	if view.Delivery == nil || view.Delivery.Delivered || !view.Delivery.Undeliverable {
		t.Fatalf("missing giant row was counted as useful delivery: %#v", view.Delivery)
	}
	body := decodePreparedOutputJSON(t, view.Text)
	if _, exists := body["next_offset"]; exists || body["page_displayed"] != false {
		t.Fatalf("omitted giant row was silently skipped: %s", view.Text)
	}
	var next map[string]any
	if json.Unmarshal(view.Delivery.NextRequest, &next) != nil || next["view"] != "json" || next["pointer"] != "/rows/0" {
		t.Fatalf("missing precise original-row recovery: %s", view.Delivery.NextRequest)
	}
	if _, exists := body["next_request"]; !exists {
		t.Fatal("first model receipt must expose the exact selected-row request")
	}
}

func TestBudgetReadPageUTF8TextAndEscapedJSONStringsContinueExactly(t *testing.T) {
	for _, viewName := range []string{"text", "json"} {
		t.Run(viewName, func(t *testing.T) {
			r := newPreparedOutputTestRegistry(t)
			full := "prefix" + strings.Repeat("中文\n\"\\\t<>", 600) + "结束"
			record := StoredToolResult{Tool: "external", Text: full, TextComplete: false, IsError: true, Error: "original was partial"}
			if viewName == "json" {
				record.Structured, _ = json.Marshal(map[string]any{"body": full})
			}
			ref, _ := r.results.Save(record)
			input := map[string]any{"result_id": ref.ID, "view": viewName, "offset": 6, "limit": 8192}
			if viewName == "json" {
				input["pointer"] = "/body"
			}
			args, _ := json.Marshal(input)
			var restored strings.Builder
			offset := 6
			for turn := 0; turn < 100 && offset < len(full); turn++ {
				_, view := budgetPageTestRead(t, r, args, 384)
				if view.Delivery == nil || !view.Delivery.Delivered || len(view.Text) > 1536 {
					t.Fatalf("UTF8 fragment could not be delivered: %s", view.Text)
				}
				// A last small original text page may fit without conversion.
				if !json.Valid([]byte(view.Text)) {
					_, fragment, ok := strings.Cut(view.Text, "\n---\n")
					if !ok {
						t.Fatal("missing original text page boundary")
					}
					restored.WriteString(fragment)
					offset += len(fragment)
				} else {
					body := decodePreparedOutputJSON(t, view.Text)
					fragment, _ := body["value_fragment"].(string)
					if fragment == "" || !utf8.ValidString(fragment) || body["offset"] != float64(offset) || body["end_offset"] != float64(offset+len(fragment)) || body["original_is_error"] != true || body["stored_text_complete"] != false {
						t.Fatalf("invalid range or original status: %s", view.Text)
					}
					restored.WriteString(fragment)
					offset += len(fragment)
					if next, exists := body["next_offset"]; exists && next != float64(offset) {
						t.Fatal("continuation does not describe the shown UTF8 bytes")
					}
				}
				args = view.Delivery.NextRequest
				if offset < len(full) && len(args) == 0 {
					t.Fatal("missing byte continuation")
				}
			}
			if restored.String() != full[6:] {
				t.Fatal("UTF8/escaped-string continuation lost or duplicated bytes")
			}
		})
	}
}

func TestToolResultRequestKeyDefaultsAndInvalidInputs(t *testing.T) {
	id := "R" + strings.Repeat("a", 64)
	for _, pair := range [][2]string{
		{`{"result_id":"` + id + `"}`, `{"offset":0,"result_id":"` + id + `","view":"summary","pointer":""}`},
		{`{"result_id":"` + id + `","view":"text"}`, `{"view":"text","result_id":"` + id + `","offset":0,"limit":8192}`},
		{`{"result_id":"` + id + `","view":"query"}`, `{"view":"query","result_id":"` + id + `","offset":0,"limit":20,"include_rows":true,"where":[],"select":[]}`},
	} {
		a, okA := ToolResultRequestKey(json.RawMessage(pair[0]))
		b, okB := ToolResultRequestKey(json.RawMessage(pair[1]))
		if !okA || !okB || a != b {
			t.Fatalf("equivalent requests did not normalize: %s / %s", pair[0], pair[1])
		}
	}
	for _, raw := range []string{
		`{"result_id":"` + id + `","view":"query","aggregate":{"op":"sum"},"limit":20}`,
		`{"result_id":"` + id + `","view":"query","sort":"name"}`,
		`{"result_id":"` + id + `","view":"text","offset":-1}`,
		`{"result_id":"` + id + `","result_id":"` + id + `"}`,
		`{"result_id":"` + id + `","view":"native","limit":20}`,
	} {
		if _, ok := ToolResultRequestKey(json.RawMessage(raw)); ok {
			t.Fatalf("invalid request acquired a guard key: %s", raw)
		}
	}
}

func TestBudgetReadPageFinalSmallPageIsDeliveredWithoutFalseContinuation(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	data := json.RawMessage(`{"rows":[1,2,3,4]}`)
	ref, _ := r.results.Save(StoredToolResult{Tool: "external", Text: string(data), Structured: data, TextComplete: true})
	args, _ := json.Marshal(map[string]any{"result_id": ref.ID, "view": "query", "pointer": "/rows", "offset": 3, "limit": 5})
	page, view := budgetPageTestRead(t, r, args, 384)
	if view.Delivery == nil || !view.Delivery.Delivered || view.Delivery.Undeliverable || len(view.Delivery.NextRequest) != 0 {
		t.Fatalf("terminal page delivery misreported: %#v", view.Delivery)
	}
	got := decodePreparedOutputJSON(t, view.Text)
	if got["complete"] != true || got["offset"] != float64(3) || got["shown_items"] != float64(1) {
		t.Fatalf("terminal page coverage changed: %s; original %s", view.Text, page.Text)
	}
	if _, exists := got["next_offset"]; exists {
		t.Fatal("terminal page invented continuation")
	}
}

func TestBudgetReadPageStatisticsCannotFitDoesNotClaimDelivery(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	data := json.RawMessage(`[1,2,3]`)
	ref, _ := r.results.Save(StoredToolResult{Tool: "external", Text: string(data), Structured: data, TextComplete: true})
	args, _ := json.Marshal(map[string]any{"result_id": ref.ID, "view": "query", "aggregate": map[string]any{"op": "sum"}})
	_, view := budgetPageTestRead(t, r, args, 128)
	if view.Delivery == nil || view.Delivery.Delivered || view.Delivery.StatisticsDelivered || !view.Delivery.Undeliverable {
		t.Fatalf("statistics marker survived after statistics were removed: %#v; %s", view.Delivery, view.Text)
	}
}

func TestBudgetReadPagePrefixBeforeHostOmissionKeepsMissingRowNext(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	data, _ := json.Marshal(map[string]any{"rows": []any{map[string]any{"id": 0}, map[string]any{"id": 1, "body": strings.Repeat("large", 10000)}, map[string]any{"id": 2}}})
	ref, _ := r.results.Save(StoredToolResult{Tool: "external", Text: string(data), Structured: data, TextComplete: true})
	args, _ := json.Marshal(map[string]any{"result_id": ref.ID, "view": "query", "pointer": "/rows", "limit": 2})
	first, _ := budgetPageTestRead(t, r, args, 384)
	secondArgs, _ := json.Marshal(map[string]any{"result_id": ref.ID, "view": "query", "pointer": "/rows", "offset": 1, "limit": 1})
	second, _ := budgetPageTestRead(t, r, secondArgs, 384)
	a := decodePreparedOutputJSON(t, first.Text)
	b := decodePreparedOutputJSON(t, second.Text)
	// A future host page may append an explicit omission after real rows.
	// Compose only actual host items, keeping their original-index receipts.
	a["items"] = append(a["items"].([]any), b["items"].([]any)...)
	a["shown_items"], a["next_offset"], a["omitted"] = 2, 2, true
	combined, _ := json.Marshal(a)
	view := r.BudgetResultView(context.Background(), ModelResultBudgetRequest{Tool: ToolResultReadName, Text: string(combined), Args: args, Tokens: 384})
	got := decodePreparedOutputJSON(t, view.Text)
	if view.Delivery == nil || !view.Delivery.Delivered || got["shown_items"] != float64(1) || got["next_offset"] != float64(1) {
		t.Fatalf("real prefix should advance only to the missing row: %s", view.Text)
	}
	var next map[string]any
	if json.Unmarshal(view.Delivery.NextRequest, &next) != nil || next["offset"] != float64(1) {
		t.Fatalf("continuation skipped the missing row: %s", view.Delivery.NextRequest)
	}
}
