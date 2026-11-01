package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func searchItemInt(t *testing.T, item map[string]any, key string) int {
	t.Helper()
	number, ok := item[key].(json.Number)
	if !ok {
		t.Fatalf("missing integer %s: %#v", key, item)
	}
	n, err := strconv.Atoi(number.String())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestToolResultSearchLiteralUTF8AndExactRanges(t *testing.T) {
	text := "前置🙂\nheader\n中文trace.*[42] 错误\nnext\n尾部\n中文trace.*[42] 恢复\n"
	reader, id := resultReaderFixture(t, StoredToolResult{Tool: "logs", Text: text, TextComplete: false, IsError: true, Error: "upstream stopped"})
	result, body := resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": "search", "pattern": "trace.*[42]", "context_lines": 1, "limit": 1})
	if body["scan_complete"] != true || body["total_matches"] != json.Number("2") || body["complete"] != false || body["next_offset"] != json.Number("1") || body["stored_text_complete"] != false || body["original_is_error"] != true || body["original_error"] != "upstream stopped" {
		t.Fatalf("search lost source status or scan/page distinction: %s", result.Text)
	}
	item := body["items"].([]any)[0].(map[string]any)
	start, end := searchItemInt(t, item, "match_start"), searchItemInt(t, item, "match_end")
	left, right := searchItemInt(t, item, "context_start"), searchItemInt(t, item, "context_end")
	if text[start:end] != "trace.*[42]" || item["text"] != text[left:right] || item["text"] != "header\n中文trace.*[42] 错误\nnext\n" || searchItemInt(t, item, "line_number") != 3 || searchItemInt(t, item, "match_index") != 0 || item["context_truncated"] != false {
		t.Fatalf("not a literal hit with exact UTF8 byte ranges: %#v", item)
	}
	details := resultReaderDetails(t, result)
	if !details.ScanComplete || details.Complete || details.Pattern != "trace.*[42]" || details.ContextLines != 1 || details.Total != 2 || !details.OriginalIsError || details.TextComplete {
		t.Fatalf("search details do not describe saved observation: %+v", details)
	}
	_, second := resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": "search", "pattern": "trace.*[42]", "offset": 1, "limit": 1, "context_lines": 0})
	if second["complete"] != true || second["next_offset"] != nil || searchItemInt(t, second["items"].([]any)[0].(map[string]any), "match_index") != 1 {
		t.Fatal("match-ordinal continuation did not reach last hit")
	}
	_, absent := resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": "search", "pattern": "TRACE.*[42]"})
	if absent["total_matches"] != json.Number("0") || absent["complete"] != true || absent["scan_complete"] != true || len(absent["items"].([]any)) != 0 {
		t.Fatal("case-sensitive no-hit scan was not an explicit complete empty result")
	}
}

func TestToolResultSearchScansOnlySavedText(t *testing.T) {
	reader, id := resultReaderFixture(t, StoredToolResult{Tool: "mcp", Text: "visible only", Structured: json.RawMessage(`{"secret":"needle"}`), Native: json.RawMessage(`{"blob":"needle"}`), TextComplete: true})
	_, body := resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": "search", "pattern": "needle"})
	if body["total_matches"] != json.Number("0") || body["stored_text_bytes"] != json.Number(strconv.Itoa(len("visible only"))) {
		t.Fatal("search escaped saved Text into structured/native data")
	}
}

func TestToolResultSearchStreamingBoundariesAndPagination(t *testing.T) {
	text := strings.Repeat("x", resultSearchScanChunk-2) + "汉🙂needle\n" + strings.Repeat("a", resultSearchScanChunk) + "汉🙂needle\n"
	hits, total, err := scanResultLiteral(context.Background(), text, "汉🙂needle", 0, 1)
	if err != nil || total != 2 || len(hits) != 1 || hits[0].start != resultSearchScanChunk-2 || hits[0].line != 1 {
		t.Fatalf("cross-window UTF8 literal lost: total=%d hits=%+v err=%v", total, hits, err)
	}
	hits, total, err = scanResultLiteral(context.Background(), strings.Repeat("aa", 100000), "aa", 99998, 2)
	if err != nil || total != 100000 || len(hits) != 2 || hits[0].index != 99998 || hits[1].start != 199998 {
		t.Fatalf("dense scan retained wrong requested suffix: %d %+v %v", total, hits, err)
	}
	reader, id := resultReaderFixture(t, StoredToolResult{Tool: "logs", Text: strings.Repeat("hit\n", 45), TextComplete: true})
	seen := 0
	for turn := 0; seen < 45 && turn < 4; turn++ {
		_, body := resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": "search", "pattern": "hit", "offset": seen, "context_lines": 0})
		items := body["items"].([]any)
		if len(items) != min(20, 45-seen) {
			t.Fatal("search default hit limit changed")
		}
		for _, raw := range items {
			item := raw.(map[string]any)
			if searchItemInt(t, item, "match_index") != seen || searchItemInt(t, item, "line_number") != seen+1 {
				t.Fatal("match-ordinal pagination skipped or duplicated a hit")
			}
			seen++
		}
		if seen < 45 && body["next_offset"] != json.Number(strconv.Itoa(seen)) {
			t.Fatal("next_offset was not actual displayed match count")
		}
	}
	_, terminal := resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": "search", "pattern": "hit", "offset": 45})
	if seen != 45 || terminal["complete"] != true || len(terminal["items"].([]any)) != 0 {
		t.Fatal("terminal empty match page is not complete")
	}
	raw, _ := json.Marshal(map[string]any{"result_id": id, "view": "search", "pattern": "hit", "offset": 46})
	if _, err := reader.Execute(context.Background(), raw); err == nil {
		t.Fatal("out-of-range match offset accepted")
	}
}

func TestToolResultSearchHugeUTF8LineAndMultilineLiteral(t *testing.T) {
	pattern := "trace中文🙂"
	text := strings.Repeat("左🙂", 10000) + pattern + strings.Repeat("右🙂", 10000)
	reader, id := resultReaderFixture(t, StoredToolResult{Tool: "logs", Text: text, TextComplete: true})
	_, body := resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": "search", "pattern": pattern, "context_lines": 5})
	item := body["items"].([]any)[0].(map[string]any)
	start, end := searchItemInt(t, item, "context_start"), searchItemInt(t, item, "context_end")
	snippet := item["text"].(string)
	if len(snippet) > resultSearchSnippetMax || !utf8.ValidString(snippet) || snippet != text[start:end] || !strings.Contains(snippet, pattern) || item["context_truncated"] != true {
		t.Fatal("huge UTF8 line did not retain complete literal with bounded exact snippet")
	}
	reader, id = resultReaderFixture(t, StoredToolResult{Tool: "logs", Text: "before\nstart\nfinish\nafter\n", TextComplete: true})
	_, body = resultReaderJSON(t, reader, map[string]any{"result_id": id, "view": "search", "pattern": "start\nfinish\n", "context_lines": 0})
	item = body["items"].([]any)[0].(map[string]any)
	if item["text"] != "start\nfinish\n" || searchItemInt(t, item, "line_number") != 2 || searchItemInt(t, item, "match_end") != len("before\nstart\nfinish\n") {
		t.Fatalf("multiline literal ranges changed: %#v", item)
	}
}

// Count Err checks deterministically, avoiding cancellation timing assumptions.
type searchCancelContext struct{ checks, cancelAt int }

func (*searchCancelContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (*searchCancelContext) Done() <-chan struct{}       { return nil }
func (*searchCancelContext) Value(any) any               { return nil }
func (c *searchCancelContext) Err() error {
	c.checks++
	if c.checks >= c.cancelAt {
		return context.Canceled
	}
	return nil
}

func TestToolResultSearchCancellationIsCheckedDuringAndAfterScan(t *testing.T) {
	for _, text := range []string{strings.Repeat("x", 10*resultSearchScanChunk), strings.Repeat("hit", 10000)} {
		ctx := &searchCancelContext{cancelAt: 3}
		if hits, total, err := scanResultLiteral(ctx, text, "hit", 0, 20); !errors.Is(err, context.Canceled) || hits != nil || total != 0 {
			t.Fatalf("canceled full scan claimed a page/count: %d %+v %v", total, hits, err)
		}
	}
	ctx := &searchCancelContext{cancelAt: 1}
	if _, _, err := scanResultLiteral(ctx, "", "hit", 0, 20); !errors.Is(err, context.Canceled) {
		t.Fatal("zero-iteration no-hit scan skipped final cancellation check")
	}
}

func TestToolResultSearchStrictArgumentsAndRequestKeys(t *testing.T) {
	reader, id := resultReaderFixture(t, StoredToolResult{Tool: "logs", Text: "hit", TextComplete: true})
	prefix := `{"result_id":"` + id + `","view":"search","pattern":"hit"`
	invalid := []string{
		`{"result_id":"` + id + `","view":"search"}`,
		`{"result_id":"` + id + `","view":"search","pattern":""}`,
		`{"result_id":"` + id + `","view":"search","pattern":null}`,
		`{"result_id":"` + id + `","view":"search","Pattern":"hit"}`,
		prefix + `,"pattern":"hit"}`, prefix + `,"sort":"line"}`, prefix + `,"pointer":""}`,
		prefix + `,"context_lines":null}`, prefix + `,"context_lines":-1}`, prefix + `,"context_lines":6}`, prefix + `,"context_lines":0.5}`,
		prefix + `,"limit":0}`, prefix + `,"limit":201}`, prefix + `,"offset":-1}`, prefix + `,"case_sensitive":true}`, prefix + `,"regex":false}`,
		prefix + `,"where":[]}`, prefix + `,"select":[]}`, prefix + `,"include_rows":true}`,
	}
	tooLong, _ := json.Marshal(map[string]any{"result_id": id, "view": "search", "pattern": strings.Repeat("中", 342)})
	invalid = append(invalid, string(tooLong))
	for _, raw := range invalid {
		if result, err := reader.Execute(context.Background(), json.RawMessage(raw)); err == nil || result.Text != "" {
			t.Fatalf("invalid search became a successful page: %s", raw)
		}
		if _, ok := ToolResultRequestKey(json.RawMessage(raw)); ok {
			t.Fatalf("invalid search acquired recovery key: %s", raw)
		}
	}
	for _, view := range []string{"summary", "text", "json", "native", "query"} {
		for _, field := range []string{`"pattern":""`, `"Pattern":null`, `"context_lines":0`, `"Context_Lines":null`} {
			raw := json.RawMessage(`{"result_id":"` + id + `","view":"` + view + `",` + field + `}`)
			if _, err := reader.Execute(context.Background(), raw); err == nil {
				t.Fatalf("search-only parameter silently ignored by %s: %s", view, raw)
			}
			if _, ok := ToolResultRequestKey(raw); ok {
				t.Fatal("ignored parameter acquired a guard key")
			}
		}
	}
	base := json.RawMessage(prefix + `}`)
	explicit := json.RawMessage(`{"limit":20,"offset":0,"context_lines":1,"pattern":"hit","view":"search","result_id":"` + id + `"}`)
	key, ok := ToolResultRequestKey(base)
	other, otherOK := ToolResultRequestKey(explicit)
	if !ok || !otherOK || key != other {
		t.Fatal("search defaults or JSON key order bypass semantic key")
	}
	for _, suffix := range []string{`,"context_lines":0}`, `,"offset":1}`, `,"limit":1}`} {
		other, ok := ToolResultRequestKey(json.RawMessage(prefix + suffix))
		if !ok || other == key {
			t.Fatal("different search request blocked by same semantic key")
		}
	}
}

func TestBudgetReadSearch384DeliversWholeHitsAndActualContinuation(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	text := strings.Repeat("header\ntrace=id42 status=failed\nfooter\n", 8)
	ref, _ := r.results.Save(StoredToolResult{Tool: "logs", Text: text, TextComplete: false, IsError: true, Error: "partial stream"})
	args, _ := json.Marshal(map[string]any{"result_id": ref.ID, "view": "search", "pattern": "id42", "limit": 5})
	seen := 0
	for turn := 0; seen < 5 && turn < 10; turn++ {
		original, view := budgetPageTestRead(t, r, args, 384)
		if view.Delivery == nil || !view.Delivery.Delivered || view.Delivery.Undeliverable || len(view.Text) > 1536 || view.ResultID != "" {
			t.Fatalf("search did not deliver bounded real hits: %#v %s", view.Delivery, view.Text)
		}
		body := decodePreparedOutputJSON(t, view.Text)
		items := body["items"].([]any)
		if len(items) == 0 || body["offset"] != float64(seen) || body["shown_items"] != float64(len(items)) || body["scan_complete"] != true || body["total_matches"] != float64(8) || body["stored_text_complete"] != false || body["original_is_error"] != true {
			t.Fatalf("search page omitted counts or source status: %s", view.Text)
		}
		for _, raw := range items {
			item := raw.(map[string]any)
			if item["match_index"] != float64(seen) || !strings.Contains(item["text"].(string), "id42") {
				t.Fatal("lost or repeated actual hit")
			}
			seen++
		}
		if body["next_offset"] != float64(seen) {
			t.Fatal("budget page skipped an undisplayed hit")
		}
		if original.Text == view.Text && turn == 0 {
			t.Fatal("fixture did not exercise budget shrinking")
		}
		args = view.Delivery.NextRequest
	}
	if seen != 5 {
		t.Fatal("could not recover requested first five matches")
	}
	stored, err := r.results.Load(context.Background(), ref.ID)
	if err != nil || stored.Text != text {
		t.Fatal("model view altered saved/UI original")
	}
}

func TestBudgetReadSearchTinyRecoveryAndEmptyScan(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	text := strings.Repeat("x", 5000) + "needle" + strings.Repeat("y", 5000)
	ref, _ := r.results.Save(StoredToolResult{Tool: "logs", Text: text, TextComplete: false})
	args, _ := json.Marshal(map[string]any{"result_id": ref.ID, "view": "search", "pattern": "needle", "limit": 1})
	_, view := budgetPageTestRead(t, r, args, 128)
	if view.Delivery == nil || view.Delivery.Delivered || !view.Delivery.Undeliverable {
		t.Fatal("unshown search hit claimed content delivery")
	}
	body := decodePreparedOutputJSON(t, view.Text)
	if body["page_displayed"] != false || body["complete"] != false || body["next_offset"] != nil || body["scan_complete"] != true || body["total_matches"] != float64(1) {
		t.Fatalf("scan complete confused with page complete: %s", view.Text)
	}
	var next map[string]any
	if json.Unmarshal(view.Delivery.NextRequest, &next) != nil || next["view"] != "search" || next["context_lines"] != float64(0) || next["offset"] != float64(0) || next["limit"] != float64(1) || body["next_request"] == nil {
		t.Fatalf("missing smaller same-hit search recovery: %s %s", view.Delivery.NextRequest, view.Text)
	}
	_, view = budgetPageTestRead(t, r, view.Delivery.NextRequest, 128)
	if json.Unmarshal(view.Delivery.NextRequest, &next) != nil || next["view"] != "text" || next["offset"] != float64(5000) || next["limit"].(float64) > 32768 || next["pointer"] != nil {
		t.Fatalf("missing exact-byte recovery: %s", view.Delivery.NextRequest)
	}
	args, _ = json.Marshal(map[string]any{"result_id": ref.ID, "view": "search", "pattern": "absent"})
	_, view = budgetPageTestRead(t, r, args, 1)
	body = decodePreparedOutputJSON(t, view.Text)
	if view.Delivery == nil || !view.Delivery.Delivered || view.Delivery.Undeliverable || body["scan_complete"] != true || body["complete"] != true || body["total_matches"] != float64(0) || len(body["items"].([]any)) != 0 || len(view.Delivery.NextRequest) != 0 {
		t.Fatalf("empty full scan falsely blocked by tiny budget: %s", view.Text)
	}
}
