package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func queryOrderIndices(envelope map[string]any) []int {
	items := envelope["items"].([]any)
	indices := make([]int, len(items))
	for position, item := range items {
		value, _ := item.(map[string]any)["original_index"].(json.Number).Int64()
		indices[position] = int(value)
	}
	return indices
}

func TestToolResultQuerySortExactStableAndPaged(t *testing.T) {
	reader, id := resultReaderFixture(t, StoredToolResult{Tool: "mcp", TextComplete: true, Structured: json.RawMessage(`[
		{"score":9007199254740993,"name":"a"},
		{"score":9007199254740992,"name":"b"},
		{"score":9007199254740993.0,"name":"c"},
		{"score":null,"name":"d"},
		{"name":"e"}
	]`)})
	args := map[string]any{"result_id": id, "view": "query", "limit": 2, "select": []string{"/name"}, "sort": []any{map[string]any{"pointer": "/score", "direction": "desc"}}}
	result, page := resultReaderJSON(t, reader, args)
	if got := queryOrderIndices(page); !reflect.DeepEqual(got, []int{0, 2}) {
		t.Fatalf("rounded numeric sort or unstable ties: %v", got)
	}
	if page["next_offset"] != json.Number("2") || page["matched_rows"] != json.Number("5") || page["sort"] == nil {
		t.Fatalf("missing sort page receipt: %#v", page)
	}
	if details := resultReaderDetails(t, result); details.Total != 5 || details.Complete {
		t.Fatalf("bad row page details: %+v", details)
	}
	args["offset"] = 2
	_, page = resultReaderJSON(t, reader, args)
	if got := queryOrderIndices(page); !reflect.DeepEqual(got, []int{1, 3}) {
		t.Fatalf("null was not sorted after concrete values: %v", got)
	}
	args["offset"] = 4
	_, page = resultReaderJSON(t, reader, args)
	if got := queryOrderIndices(page); !reflect.DeepEqual(got, []int{4}) || page["complete"] != true {
		t.Fatalf("missing value or final page wrong: %#v", page)
	}
	args["offset"], args["limit"] = 0, 20
	args["sort"] = []any{map[string]any{"pointer": "/score", "direction": "asc"}}
	_, page = resultReaderJSON(t, reader, args)
	if got := queryOrderIndices(page); !reflect.DeepEqual(got, []int{1, 0, 2, 3, 4}) {
		t.Fatalf("ascending exact order incorrect: %v", got)
	}
}

func TestToolResultQuerySortScalarTypesAndMultikey(t *testing.T) {
	for _, fixture := range []struct {
		data, pointer string
		want          []int
	}{
		{`[{"v":true},{"v":false},{"v":true}]`, "/v", []int{1, 0, 2}},
		{`[{"v":"z"},{"v":"a"},{"v":"a"}]`, "/v", []int{1, 2, 0}},
		{`["z","a","b"]`, "", []int{1, 2, 0}},
	} {
		reader, id := resultReaderFixture(t, StoredToolResult{Tool: "mcp", TextComplete: true, Structured: json.RawMessage(fixture.data)})
		_, page := resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": "query", "sort": []any{map[string]any{"pointer": fixture.pointer, "direction": "asc"}}})
		if got := queryOrderIndices(page); !reflect.DeepEqual(got, fixture.want) {
			t.Fatalf("scalar sort %s: %v", fixture.data, got)
		}
	}
	reader, id := resultReaderFixture(t, StoredToolResult{Tool: "mcp", TextComplete: true, Structured: json.RawMessage(`[{"a":"x","b":1},{"a":"y","b":0},{"a":"x","b":2}]`)})
	_, page := resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": "query", "sort": []any{map[string]any{"pointer": "/a", "direction": "asc"}, map[string]any{"pointer": "/b", "direction": "desc"}}})
	if got := queryOrderIndices(page); !reflect.DeepEqual(got, []int{2, 0, 1}) {
		t.Fatalf("secondary sort incorrect: %v", got)
	}
}

func TestToolResultQueryGroupsDistinguishMissingNullAndExactNumbers(t *testing.T) {
	reader, id := resultReaderFixture(t, StoredToolResult{Tool: "mcp", TextComplete: false, IsError: true, Error: "source has more pages", Structured: json.RawMessage(`[
		{"g":"a","v":0.1},{"g":"a","v":0.2},{"g":"a","v":null},{"g":"a"},{"g":"a","v":"8"},
		{"g":null,"v":4},{"v":5},{"g":1,"v":6},{"g":1.0,"v":7},{"g":"1","v":8}
	]`)})
	result, page := resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": "query", "group_by": []string{"/g"}, "aggregate": map[string]any{"op": "sum", "pointer": "/v"}, "include_rows": false})
	if page["matched_rows"] != json.Number("10") || page["scanned_rows"] != json.Number("10") || page["total_groups"] != json.Number("5") || page["groups_included"] != true || page["rows_included"] != false || page["aggregate"] != nil || page["original_is_error"] != true || page["stored_text_complete"] != false {
		t.Fatalf("group coverage lost original source boundaries: %#v", page)
	}
	items := page["items"].([]any)
	wantSums := []string{"13", "8", "3/10", "4", "5"}
	for index, item := range items {
		group := item.(map[string]any)
		if group["group_index"] != json.Number(fmt.Sprint(index)) || group["original_index"] != nil || group["aggregate"].(map[string]any)["value"] != wantSums[index] {
			t.Fatalf("group output/order wrong: %#v", group)
		}
	}
	numeric := items[0].(map[string]any)
	if numeric["count"] != json.Number("2") {
		t.Fatalf("equivalent numbers were not grouped: %#v", numeric)
	}
	stats := items[2].(map[string]any)["aggregate"].(map[string]any)
	if stats["numeric_count"] != json.Number("2") || stats["missing_count"] != json.Number("1") || stats["null_count"] != json.Number("1") || stats["non_numeric_count"] != json.Number("1") {
		t.Fatalf("group counts lost null/missing/types: %#v", stats)
	}
	nullKey := items[3].(map[string]any)["keys"].([]any)[0].(map[string]any)
	missingKey := items[4].(map[string]any)["keys"].([]any)[0].(map[string]any)
	if nullKey["present"] != true || nullKey["value"] != nil || missingKey["present"] != false {
		t.Fatalf("null/missing groups conflated: %#v %#v", nullKey, missingKey)
	}
	if _, exists := missingKey["value"]; exists {
		t.Fatal("missing group key acquired fabricated null value")
	}
	if details := resultReaderDetails(t, result); details.Total != 5 || !details.Complete || !details.ScanComplete {
		t.Fatalf("group page counts are not group counts: %+v", details)
	}
}

func TestToolResultQueryGroupsSortAggregateBeforePaging(t *testing.T) {
	reader, id := resultReaderFixture(t, StoredToolResult{Tool: "mcp", TextComplete: true, Structured: json.RawMessage(`[
		{"g":"d","v":null},{"g":"b","v":2},{"g":"a","v":10},{"g":"c","v":0.1},{"g":"c","v":0.2},{"g":"e","v":2}
	]`)})
	args := map[string]any{"result_id": id, "view": "query", "group_by": []string{"/g"}, "aggregate": map[string]any{"op": "sum", "pointer": "/v"}, "sort": []any{map[string]any{"pointer": "/aggregate/value", "direction": "desc"}}, "limit": 2}
	_, page := resultReaderJSON(t, reader, args)
	groupName := func(item any) string {
		return item.(map[string]any)["keys"].([]any)[0].(map[string]any)["value"].(string)
	}
	items := page["items"].([]any)
	if groupName(items[0]) != "a" || groupName(items[1]) != "b" || page["next_offset"] != json.Number("2") || page["matched_rows"] != json.Number("6") || page["total_groups"] != json.Number("5") {
		t.Fatalf("aggregate sort became string order or page-local count: %#v", page)
	}
	args["offset"], args["include_rows"] = 2, false
	_, page = resultReaderJSON(t, reader, args)
	items = page["items"].([]any)
	if groupName(items[0]) != "e" || groupName(items[1]) != "c" || items[0].(map[string]any)["group_index"] != json.Number("2") {
		t.Fatalf("group ties/order/continuation changed: %#v", page)
	}
	args["offset"] = 4
	_, page = resultReaderJSON(t, reader, args)
	if groupName(page["items"].([]any)[0]) != "d" || page["complete"] != true {
		t.Fatalf("null aggregate lost or sorted incorrectly: %#v", page)
	}
}

func TestToolResultQueryGroupsCountMultipleKeysAndFilter(t *testing.T) {
	reader, id := resultReaderFixture(t, StoredToolResult{Tool: "mcp", TextComplete: true, Structured: json.RawMessage(`[{"a/b":"x","b":true,"ok":true},{"a/b":"x","b":false,"ok":true},{"a/b":"x","b":true,"ok":true},{"a/b":"y","b":true,"ok":false}]`)})
	_, page := resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": "query", "group_by": []string{"/a~1b", "/b"}, "aggregate": map[string]any{"op": "count"}, "where": []any{map[string]any{"pointer": "/ok", "op": "eq", "value": true}}, "sort": []any{map[string]any{"pointer": "/count", "direction": "desc"}}})
	items := page["items"].([]any)
	first := items[0].(map[string]any)
	if len(items) != 2 || page["matched_rows"] != json.Number("3") || first["count"] != json.Number("2") || first["aggregate"].(map[string]any)["value"] != "2" || first["keys"].([]any)[1].(map[string]any)["value"] != true {
		t.Fatalf("multikey count/filter failed: %#v", page)
	}
}

func TestToolResultQueryOrderRejectsAmbiguousContractsAndTypes(t *testing.T) {
	reader, id := resultReaderFixture(t, StoredToolResult{Tool: "mcp", TextComplete: true, Structured: json.RawMessage(`[{"v":1},{"v":"2"}]`)})
	for _, fragment := range []string{
		`"sort":null`, `"sort":[]`, `"sort":[{"pointer":"/v"}]`, `"sort":[{"pointer":"/v","Direction":"asc"}]`, `"sort":[{"pointer":"/v","direction":"asc","extra":1}]`, `"sort":[{"pointer":"/v","direction":"ascending"}]`, `"sort":[{"pointer":"/v","direction":"asc"},{"pointer":"/v","direction":"desc"}]`,
		`"sort":[{"pointer":"/v","direction":"asc"}]`, `"sort":[{"pointer":"/v","direction":"asc"}],"include_rows":false`,
		`"group_by":null`, `"group_by":[]`, `"group_by":[null]`, `"group_by":["/v"]`, `"group_by":["/v"],"aggregate":{"op":"count"},"include_rows":true`, `"group_by":["/v"],"aggregate":{"op":"count"},"select":[]`, `"group_by":["/v","/v"],"aggregate":{"op":"count"}`, `"group_by":["/v"],"aggregate":{"op":"count"},"sort":[{"pointer":"/group_index","direction":"asc"}]`, `"group_by":["/v"],"aggregate":{"op":"count"},"offset":3`,
	} {
		raw := json.RawMessage(`{"result_id":"` + id + `","view":"query",` + fragment + `}`)
		if result, err := reader.Execute(context.Background(), raw); err == nil || result.Text != "" {
			t.Fatalf("invalid order contract accepted: %s => %s %v", fragment, result.Text, err)
		}
	}
	for _, data := range []string{`[{"v":{}}]`, `[{"v":[]}]`, `[{"v":1e513}]`} {
		reader, id := resultReaderFixture(t, StoredToolResult{Tool: "mcp", TextComplete: true, Structured: json.RawMessage(data)})
		for _, fragment := range []string{`"sort":[{"pointer":"/v","direction":"asc"}]`, `"group_by":["/v"],"aggregate":{"op":"count"}`} {
			raw := json.RawMessage(`{"result_id":"` + id + `","view":"query",` + fragment + `}`)
			if result, err := reader.Execute(context.Background(), raw); err == nil || result.Text != "" {
				t.Fatalf("unsafe sort/group value accepted: %s => %s %v", data, result.Text, err)
			}
		}
	}
}

func TestToolResultQueryGroupsBoundsRejectPartialStatistics(t *testing.T) {
	rows := make([]any, resultQueryMaxGroups+1)
	for index := range rows {
		rows[index] = map[string]any{"g": index}
	}
	encoded, _ := json.Marshal(rows)
	for _, data := range []json.RawMessage{encoded, json.RawMessage(`[{"g":"` + strings.Repeat("x", resultQueryMaxGroupBytes+1) + `"}]`)} {
		reader, id := resultReaderFixture(t, StoredToolResult{Tool: "mcp", TextComplete: true, Structured: data})
		raw, _ := json.Marshal(map[string]any{"result_id": id, "view": "query", "group_by": []string{"/g"}, "aggregate": map[string]any{"op": "count"}, "limit": 1})
		if result, err := reader.Execute(context.Background(), raw); err == nil || result.Text != "" || !strings.Contains(err.Error(), "no partial statistics") {
			t.Fatalf("bounded groups returned partial result: %s %v", result.Text, err)
		}
	}
	reader, id := resultReaderFixture(t, StoredToolResult{Tool: "mcp", TextComplete: true, Structured: json.RawMessage(`[]`)})
	_, page := resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": "query", "group_by": []string{"/g"}, "aggregate": map[string]any{"op": "count"}, "limit": 1})
	if page["complete"] != true || page["total_groups"] != json.Number("0") || len(page["items"].([]any)) != 0 {
		t.Fatalf("empty grouped result was not complete: %#v", page)
	}
}

func TestToolResultQueryStableSortCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	order := []int{4, 3, 2, 1, 0}
	comparisons := 0
	err := resultQueryStableOrder(ctx, order, func(left, right int) int { comparisons++; cancel(); return left - right })
	if !errors.Is(err, context.Canceled) || comparisons != 1 {
		t.Fatalf("sort ignored cancellation: comparisons=%d err=%v", comparisons, err)
	}
	_, err = readResultOrderedQuery(ctx, StoredToolResult{}, toolResultReadArgs{Sort: []resultQuerySort{{Pointer: "", Direction: "asc"}}}, ToolResultReadDetails{}, []any{json.Number("1")}, 20, nil, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ordered scan ignored cancellation: %v", err)
	}
}
