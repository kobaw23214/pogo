package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestToolResultQueryExactAggregatesAndCoverage(t *testing.T) {
	reader, id := resultReaderFixture(t, StoredToolResult{
		Tool: "mcp", IsError: true, Error: "upstream page incomplete", TextComplete: false,
		Structured: json.RawMessage(`{"total":999,"has_more":true,"rows":[{"price":0.1},{"price":0.2},{"price":null},{},{"price":"3"}]}`),
	})
	for operation, want := range map[string]string{"sum": "3/10", "avg": "3/20", "min": "1/10", "max": "1/5", "count": "5"} {
		t.Run(operation, func(t *testing.T) {
			aggregate := map[string]any{"op": operation}
			if operation != "count" {
				aggregate["pointer"] = "/price"
			}
			result, envelope := resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": "query", "pointer": "/rows", "aggregate": aggregate})
			stats := envelope["aggregate"].(map[string]any)
			if stats["value"] != want || envelope["matched_rows"] != json.Number("5") || envelope["scanned_rows"] != json.Number("5") {
				t.Fatalf("aggregate lost exactness or confused source total: %#v", envelope)
			}
			if operation != "count" && (stats["numeric_count"] != json.Number("2") || stats["null_count"] != json.Number("1") || stats["missing_count"] != json.Number("1") || stats["non_numeric_count"] != json.Number("1")) {
				t.Fatalf("aggregate confused missing/null/non-numeric: %#v", stats)
			}
			if envelope["items"] != nil || envelope["rows_included"] != false || envelope["page_displayed"] != false || envelope["scan_complete"] != true || envelope["complete"] != true {
				t.Fatalf("aggregate-only view pretends rows were delivered: %#v", envelope)
			}
			if envelope["original_is_error"] != true || envelope["stored_text_complete"] != false || envelope["original_error"] != "upstream page incomplete" || !strings.Contains(envelope["coverage"].(string), "does not establish remote/source completeness") {
				t.Fatal("successful local analysis erased original failure/source coverage")
			}
			if details := resultReaderDetails(t, result); !details.OriginalIsError || details.TextComplete || !details.Complete {
				t.Fatalf("UI details lost original/local status distinction: %+v", details)
			}
		})
	}
}

func TestToolResultQueryNoNumbersIsNull(t *testing.T) {
	for _, rows := range []string{`[]`, `[{"v":null},{},{"v":"0"}]`} {
		reader, id := resultReaderFixture(t, StoredToolResult{Tool: "mcp", Structured: json.RawMessage(rows), TextComplete: true})
		for _, operation := range []string{"sum", "avg", "min", "max"} {
			_, envelope := resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": "query", "aggregate": map[string]any{"op": operation, "pointer": "/v"}})
			stats := envelope["aggregate"].(map[string]any)
			if stats["value"] != nil || stats["numeric_count"] != json.Number("0") {
				t.Fatalf("%s fabricated zero for no numeric observations: %#v", operation, stats)
			}
		}
	}
}

func TestToolResultQueryExactFilterProjectionAndPage(t *testing.T) {
	reader, id := resultReaderFixture(t, StoredToolResult{Tool: "mcp", TextComplete: true, Structured: json.RawMessage(`[
		{"n":9007199254740992,"name":"skip"},
		{"n":9007199254740993,"name":"a","optional":null},
		{"n":9007199254740994,"name":"b"},
		{"n":9007199254740995,"name":"c","optional":false}
	]`)})
	args := map[string]any{
		"result_id": id, "view": "query", "limit": 1,
		"where":  []any{map[string]any{"pointer": "/n", "op": "gte", "value": json.Number("9007199254740993")}},
		"select": []string{"/name", "/optional"},
	}
	_, first := resultReaderJSON(t, reader, args)
	row := first["items"].([]any)[0].(map[string]any)
	fields := row["fields"].(map[string]any)
	if row["original_index"] != json.Number("1") || row["pointer_suffix"] != "/1" || first["next_offset"] != json.Number("1") || first["matched_rows"] != json.Number("3") || fields["/name"].(map[string]any)["value"] != "a" || fields["/optional"].(map[string]any)["present"] != true || fields["/optional"].(map[string]any)["value"] != nil {
		t.Fatalf("exact filter/projection/null failed: %#v", first)
	}
	args["offset"] = 1
	_, second := resultReaderJSON(t, reader, args)
	row = second["items"].([]any)[0].(map[string]any)
	if row["original_index"] != json.Number("2") || row["fields"].(map[string]any)["/optional"].(map[string]any)["present"] != false || second["next_offset"] != json.Number("2") {
		t.Fatalf("matching-row pagination or missing projection failed: %#v", second)
	}
	args["offset"] = 2
	_, last := resultReaderJSON(t, reader, args)
	if last["complete"] != true || last["next_offset"] != nil || last["items"].([]any)[0].(map[string]any)["original_index"] != json.Number("3") {
		t.Fatalf("last filtered page incorrect: %#v", last)
	}
}

func TestToolResultQueryExistsContainsAndEscapedPointers(t *testing.T) {
	reader, id := resultReaderFixture(t, StoredToolResult{Tool: "mcp", TextComplete: true, Structured: json.RawMessage(`[{"a/b":{"~key":2},"tags":[1,2],"name":"alpha"},{"name":"beta"}]`)})
	_, envelope := resultReaderJSON(t, reader, map[string]any{
		"result_id": id, "view": "query", "aggregate": map[string]any{"op": "count"},
		"where": []any{
			map[string]any{"pointer": "/a~1b/~0key", "op": "exists"},
			map[string]any{"pointer": "/a~1b/~0key", "op": "eq", "value": 2},
			map[string]any{"pointer": "/tags", "op": "contains", "value": 2},
			map[string]any{"pointer": "/name", "op": "contains", "value": "pha"},
		},
	})
	if envelope["aggregate"].(map[string]any)["value"] != "1" {
		t.Fatalf("exists/contains/escaped field filter failed: %#v", envelope)
	}
	_, absent := resultReaderJSON(t, reader, map[string]any{
		"result_id": id, "view": "query", "include_rows": false,
		"where": []any{map[string]any{"pointer": "/a~1b", "op": "exists", "value": false}},
	})
	if absent["matched_rows"] != json.Number("1") || absent["items"] != nil {
		t.Fatalf("exists=false count-only view incorrect: %#v", absent)
	}
}

func TestToolResultQueryOversizedRowsRecoverAndAggregateStillExact(t *testing.T) {
	structured, _ := json.Marshal([]any{map[string]any{"v": 1, "body": strings.Repeat("\x00", 20000)}, map[string]any{"v": 2}})
	reader, id := resultReaderFixture(t, StoredToolResult{Tool: "mcp", Structured: structured, TextComplete: true})
	args := map[string]any{"result_id": id, "view": "query", "include_rows": true, "limit": 1, "aggregate": map[string]any{"op": "sum", "pointer": "/v"}}
	_, page := resultReaderJSON(t, reader, args)
	row := page["items"].([]any)[0].(map[string]any)
	if row["omitted_value"] != true || row["original_index"] != json.Number("0") || row["pointer_suffix"] != "/0" || page["next_offset"] != json.Number("1") || page["omitted"] != true || page["aggregate"].(map[string]any)["value"] != "3" {
		t.Fatalf("oversized row dropped or aggregate limited to page: %#v", page)
	}
	_, recovered := resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": "json", "pointer": "/0/body", "limit": 3})
	if recovered["value_fragment"] != "\x00\x00\x00" {
		t.Fatalf("omitted query row cannot be read: %#v", recovered)
	}
	args["offset"] = 1
	_, next := resultReaderJSON(t, reader, args)
	if next["complete"] != true || next["items"].([]any)[0].(map[string]any)["original_index"] != json.Number("1") || next["aggregate"].(map[string]any)["value"] != "3" {
		t.Fatalf("query continuation changed statistics/skipped row: %#v", next)
	}
}

func TestToolResultQueryRejectsAmbiguousOrUnsupportedArguments(t *testing.T) {
	reader, id := resultReaderFixture(t, StoredToolResult{Tool: "mcp", Structured: json.RawMessage(`[{"v":1}]`), TextComplete: true})
	for _, fragment := range []string{
		`"sort":"v"`, `"group_by":"v"`, `"Pointer":""`, `"pointer":null`, `"offset":null`, `"limit":null`,
		`"where":null`, `"select":null`, `"aggregate":null`, `"include_rows":null`, `"select":[null]`,
		`"where":[{"op":"eq","value":1}]`, `"where":[{"pointer":null,"op":"eq","value":1}]`,
		`"where":[{"pointer":"/v","op":null,"value":1}]`, `"where":[{"pointer":"/v","op":"eq"}]`,
		`"where":[{"pointer":"/v","op":"exists","value":null}]`, `"where":[{"pointer":"/v","op":"eq","value":1,"sort":true}]`,
		`"aggregate":{}`, `"aggregate":{"op":null}`, `"aggregate":{"op":"sum","pointer":null}`, `"aggregate":{"op":"count","group_by":"v"}`,
		`"include_rows":false,"offset":0`, `"include_rows":false,"limit":1`, `"include_rows":false,"select":[]`,
		`"aggregate":{"op":"count"},"limit":1`, `"limit":0`, `"limit":201`,
		`"where":[{"pointer":"/v","op":"gt","value":{}}]`,
		`"where":[{"pointer":"/v","op":"eq","value":1,"value":2}]`,
	} {
		t.Run(fragment, func(t *testing.T) {
			raw := json.RawMessage(`{"result_id":"` + id + `","view":"query",` + fragment + `}`)
			result, err := reader.Execute(context.Background(), raw)
			if err == nil || result.Text != "" {
				t.Fatalf("invalid query was silently accepted: %s => %q, %v", raw, result.Text, err)
			}
		})
	}
}

func TestToolResultQueryTypeFailuresAreNotSilentNonMatches(t *testing.T) {
	reader, id := resultReaderFixture(t, StoredToolResult{Tool: "mcp", Structured: json.RawMessage(`[{"v":1}]`), TextComplete: true})
	for _, condition := range []map[string]any{
		{"pointer": "/missing", "op": "eq", "value": ""},
		{"pointer": "/v", "op": "eq", "value": "1"},
		{"pointer": "/v", "op": "lt", "value": "1"},
		{"pointer": "/v", "op": "contains", "value": 1},
	} {
		raw, _ := json.Marshal(map[string]any{"result_id": id, "view": "query", "where": []any{condition}})
		result, err := reader.Execute(context.Background(), raw)
		if err == nil || result.Text != "" {
			t.Fatalf("type/missing error turned into empty result: %#v => %q, %v", condition, result.Text, err)
		}
	}
	_, distinct := resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": "query", "where": []any{map[string]any{"pointer": "/v", "op": "ne", "value": "1"}}})
	if distinct["matched_rows"] != json.Number("1") {
		t.Fatal("ne did not distinguish JSON number from string")
	}
}

func TestToolResultQueryScanPrecisionAndDepthBounds(t *testing.T) {
	rows := make([]int, resultQueryMaxRows+1)
	encoded, _ := json.Marshal(rows)
	reader, id := resultReaderFixture(t, StoredToolResult{Tool: "mcp", Structured: encoded, TextComplete: true})
	raw, _ := json.Marshal(map[string]any{"result_id": id, "view": "query", "aggregate": map[string]any{"op": "count"}})
	if result, err := reader.Execute(context.Background(), raw); err == nil || result.Text != "" || !strings.Contains(err.Error(), "no partial statistics") {
		t.Fatalf("oversized scan produced partial statistics: %q, %v", result.Text, err)
	}
	for _, number := range []string{"1e513", "1e-513", strings.Repeat("9", resultQueryMaxNumberDigits+1)} {
		reader, id := resultReaderFixture(t, StoredToolResult{Tool: "mcp", Structured: json.RawMessage(`[` + number + `]`), TextComplete: true})
		raw, _ := json.Marshal(map[string]any{"result_id": id, "view": "query", "aggregate": map[string]any{"op": "sum"}})
		if result, err := reader.Execute(context.Background(), raw); err == nil || result.Text != "" {
			t.Fatalf("unsafe numeric work accepted: %s => %q, %v", number, result.Text, err)
		}
	}
	deep := strings.Repeat("[", resultQueryMaxDepth+1) + "1" + strings.Repeat("]", resultQueryMaxDepth+1)
	reader, id = resultReaderFixture(t, StoredToolResult{Tool: "mcp", Structured: json.RawMessage(`[` + deep + `]`), TextComplete: true})
	raw = json.RawMessage(`{"result_id":"` + id + `","view":"query","where":[{"pointer":"","op":"eq","value":` + deep + `}]}`)
	if result, err := reader.Execute(context.Background(), raw); err == nil || result.Text != "" || !strings.Contains(err.Error(), "nested levels") {
		t.Fatalf("unbounded nested equality accepted: %q, %v", result.Text, err)
	}
}
