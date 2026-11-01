package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestBudgetOrderRequestKeysPreserveSortGroupingAndDefaults(t *testing.T) {
	id := "R" + strings.Repeat("a", 64)
	base := `{"result_id":"` + id + `","view":"query","group_by":["/department"],"aggregate":{"op":"sum","pointer":"/amount"},"sort":[{"pointer":"/aggregate/value","direction":"desc"}]}`
	key, valid := ToolResultRequestKey(json.RawMessage(base))
	if !valid {
		t.Fatal("valid grouped query did not normalize")
	}
	var explicit map[string]any
	_ = json.Unmarshal([]byte(base), &explicit)
	explicit["include_rows"], explicit["offset"], explicit["limit"] = false, 0, 20
	encoded, _ := json.Marshal(explicit)
	if other, valid := ToolResultRequestKey(encoded); !valid || other != key {
		t.Fatalf("group defaults do not normalize identically: %s", encoded)
	}
	for _, change := range []func(){
		func() { explicit["sort"] = []any{map[string]any{"pointer": "/aggregate/value", "direction": "asc"}} },
		func() { delete(explicit, "sort") },
		func() { explicit["group_by"] = []string{"/other"} },
		func() { explicit["aggregate"] = map[string]any{"op": "count"} },
	} {
		change()
		encoded, _ = json.Marshal(explicit)
		if other, valid := ToolResultRequestKey(encoded); !valid || other == key {
			t.Fatalf("semantic query change lost from request key: %s", encoded)
		}
	}
}

func TestBudgetSortedRowsContinueActualOrderAndRejectDuplicates(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	rows := make([]any, 7)
	for index := range rows {
		rows[index] = map[string]any{"score": index, "body": strings.Repeat("body-", 40)}
	}
	data, _ := json.Marshal(rows)
	ref, _ := r.results.Save(StoredToolResult{Tool: "external", Text: string(data), Structured: data, TextComplete: true})
	args, _ := json.Marshal(map[string]any{"result_id": ref.ID, "view": "query", "limit": 7, "sort": []any{map[string]any{"pointer": "/score", "direction": "desc"}}})
	seen := 0
	for turn := 0; seen < 7 && turn < 12; turn++ {
		_, view := budgetPageTestRead(t, r, args, 512)
		if view.Delivery == nil || !view.Delivery.Delivered || view.Delivery.Undeliverable || len(view.Text) > 2048 {
			t.Fatalf("sorted query did not deliver useful bounded content: %#v %s", view.Delivery, view.Text)
		}
		body := decodePreparedOutputJSON(t, view.Text)
		for _, raw := range body["items"].([]any) {
			item := raw.(map[string]any)
			if item["original_index"] != float64(6-seen) || item["value"].(map[string]any)["score"] != float64(6-seen) {
				t.Fatalf("sorted continuation lost/repeated row %d: %#v", seen, item)
			}
			seen++
		}
		if seen < 7 {
			var next map[string]any
			if json.Unmarshal(view.Delivery.NextRequest, &next) != nil || next["sort"] == nil || next["offset"] != float64(seen) || next["view"] != "query" {
				t.Fatalf("sorted continuation changed semantics: %s", view.Delivery.NextRequest)
			}
			args = view.Delivery.NextRequest
		}
	}
	if seen != 7 {
		t.Fatalf("sorted continuation incomplete: %d", seen)
	}
	args, _ = json.Marshal(map[string]any{"result_id": ref.ID, "view": "query", "limit": 2, "sort": []any{map[string]any{"pointer": "/score", "direction": "desc"}}})
	original, _ := budgetPageTestRead(t, r, args, 4096)
	host, ok := resultDecodeJSON([]byte(original.Text))
	if !ok {
		t.Fatal("invalid query output")
	}
	page := host.(map[string]any)
	items := page["items"].([]any)
	items[1] = items[0]
	fake, _ := json.Marshal(page)
	view := r.BudgetResultView(context.Background(), ModelResultBudgetRequest{Tool: ToolResultReadName, Text: string(fake), Args: args, Tokens: 4096})
	if view.Delivery == nil || view.Delivery.Delivered || !view.Delivery.Undeliverable {
		t.Fatalf("duplicate sorted source receipt counted twice: %#v %s", view.Delivery, view.Text)
	}
}

func TestBudgetGroupedPagesContinueWithoutClaimingSourceRows(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	rows := make([]any, 7)
	for index := range rows {
		rows[index] = map[string]any{"g": fmt.Sprintf("group-%d-%s", index, strings.Repeat("x", 120)), "value": index + 1}
	}
	data, _ := json.Marshal(rows)
	ref, _ := r.results.Save(StoredToolResult{Tool: "external", Text: string(data), Structured: data, TextComplete: true})
	args, _ := json.Marshal(map[string]any{"result_id": ref.ID, "view": "query", "group_by": []string{"/g"}, "aggregate": map[string]any{"op": "sum", "pointer": "/value"}, "sort": []any{map[string]any{"pointer": "/aggregate/value", "direction": "desc"}}, "limit": 7})
	seen := 0
	for turn := 0; seen < 7 && turn < 12; turn++ {
		_, view := budgetPageTestRead(t, r, args, 512)
		if view.Delivery == nil || !view.Delivery.Delivered || view.Delivery.Undeliverable || len(view.Text) > 2048 {
			t.Fatalf("group page did not fit useful statistics: %#v %s", view.Delivery, view.Text)
		}
		body := decodePreparedOutputJSON(t, view.Text)
		if body["rows_included"] != false || body["groups_included"] != true || body["total_groups"] != float64(7) || body["matched_rows"] != float64(7) {
			t.Fatalf("group metadata falsely implies source rows: %s", view.Text)
		}
		for _, raw := range body["items"].([]any) {
			item := raw.(map[string]any)
			if item["group_index"] != float64(seen) || item["original_index"] != nil || item["pointer_suffix"] != nil || item["aggregate"].(map[string]any)["value"] != fmt.Sprint(7-seen) {
				t.Fatalf("group page order/index incorrect: %#v", item)
			}
			seen++
		}
		if seen < 7 {
			var next map[string]any
			if json.Unmarshal(view.Delivery.NextRequest, &next) != nil || next["view"] != "query" || next["offset"] != float64(seen) || next["include_rows"] != false || next["group_by"] == nil || next["aggregate"] == nil || next["sort"] == nil {
				t.Fatalf("group continuation lost its calculation: %s", view.Delivery.NextRequest)
			}
			if _, valid := ToolResultRequestKey(view.Delivery.NextRequest); !valid {
				t.Fatal("generated group continuation cannot execute")
			}
			args = view.Delivery.NextRequest
		}
	}
	if seen != 7 {
		t.Fatalf("group paging skipped statistics: %d", seen)
	}
}

func TestBudgetUndeliverableGroupKeepsCalculationAndNeverInventsJSONRow(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	data, _ := json.Marshal([]any{map[string]any{"g": strings.Repeat("large-key", 400), "v": 2}})
	ref, _ := r.results.Save(StoredToolResult{Tool: "external", Text: string(data), Structured: data, TextComplete: true})
	input := map[string]any{"result_id": ref.ID, "view": "query", "group_by": []string{"/g"}, "aggregate": map[string]any{"op": "sum", "pointer": "/v"}, "limit": 2}
	args, _ := json.Marshal(input)
	_, view := budgetPageTestRead(t, r, args, 384)
	if view.Delivery == nil || view.Delivery.Delivered || !view.Delivery.Undeliverable || view.Delivery.StatisticsDelivered {
		t.Fatalf("unseen group aggregate counted as delivered: %#v", view.Delivery)
	}
	body := decodePreparedOutputJSON(t, view.Text)
	if body["page_displayed"] != false || body["complete"] != false || body["groups_included"] != true || body["rows_included"] != false || body["total_groups"] != float64(1) || body["statistics_displayed"] != false {
		t.Fatalf("omitted group receipt claims completed statistics: %s", view.Text)
	}
	var next map[string]any
	if json.Unmarshal(view.Delivery.NextRequest, &next) != nil || next["view"] != "query" || next["offset"] != float64(0) || next["limit"] != float64(1) || next["aggregate"] == nil || !reflect.DeepEqual(next["group_by"], []any{"/g"}) {
		t.Fatalf("computed group was converted into an original JSON row: %s", view.Delivery.NextRequest)
	}
	if _, valid := ToolResultRequestKey(view.Delivery.NextRequest); !valid {
		t.Fatal("group retry is not an executable request")
	}
	_, again := budgetPageTestRead(t, r, view.Delivery.NextRequest, 384)
	if again.Delivery == nil || again.Delivery.Delivered || len(again.Delivery.NextRequest) != 0 {
		t.Fatalf("same unfit group creates an ineffective retry loop: %#v", again.Delivery)
	}
}
