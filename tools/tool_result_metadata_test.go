package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestToolResultMetadataViewsAndRetentionContract(t *testing.T) {
	ctx := context.Background()
	reader, id := resultReaderFixture(t, StoredToolResult{Tool: "report", Text: "PRIVATE_SOURCE_BODY", TextComplete: true})
	listArgs := json.RawMessage(`{"view":"list"}`)
	if err := ValidateArgs(reader, listArgs); err != nil {
		t.Fatal(err)
	}
	result, err := reader.Execute(ctx, listArgs)
	if err != nil || strings.Contains(result.Text, "PRIVATE_SOURCE_BODY") || !strings.Contains(result.Text, id) {
		t.Fatalf("session directory mixed metadata and body: %s, %v", result.Text, err)
	}
	if _, ok := result.Details.(ResultCatalogDetails); !ok {
		t.Fatal("catalog is not typed host metadata")
	}
	if result.ResultID != "" {
		t.Fatal("directory acquired a source result ID")
	}
	for _, view := range []string{"retain", "release"} {
		raw, _ := json.Marshal(map[string]any{"view": view, "result_id": id})
		res, err := reader.Execute(ctx, raw)
		if err != nil {
			t.Fatal(err)
		}
		receipt, ok := res.Details.(ResultRetentionDetails)
		if !ok || receipt.Retained != (view == "retain") || receipt.ResultID != id {
			t.Fatalf("invalid retention receipt: %+v", res)
		}
		if view == "retain" && receipt.RetainedUntil == "" {
			t.Fatal("retention has no expiry")
		}
		// A tiny model budget keeps the bounded action receipt; it must not
		// invent unavailable source data or encourage repeating the mutation.
		r := &Registry{results: reader.Store}
		bounded := r.BudgetResultView(ctx, ModelResultBudgetRequest{Tool: ToolResultReadName, Text: res.Text, Args: raw, Tokens: 0})
		if bounded.Text != res.Text {
			t.Fatal("retention outcome lost at identity floor")
		}
	}
	for _, raw := range []string{
		`{"view":"list","result_id":"` + id + `"}`,
		`{"view":"list","filter":null}`, `{"view":"list","offset":-1}`, `{"view":"list","limit":101}`,
		`{"view":"retain","result_id":"` + id + `","days":0}`, `{"view":"retain","result_id":"` + id + `","days":91}`,
		`{"view":"release","result_id":"` + id + `","days":1}`, `{"view":"retain","result_id":"` + id + `","Days":1}`,
		`{"view":"list","filter":"a","filter":"b"}`, `{"view":"list","path":"/another/session"}`,
	} {
		if _, err := reader.Execute(ctx, json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted invalid metadata request %s", raw)
		}
	}
	if _, err := reader.Execute(ctx, json.RawMessage(`{}`)); err == nil {
		t.Fatal("missing ID silently became a catalog request")
	}
}

func TestToolResultCatalogBudgetKeepsActualScanCursor(t *testing.T) {
	ctx := context.Background()
	reader, _ := resultReaderFixture(t, StoredToolResult{Tool: "catalog", Text: "first", TextComplete: true})
	for i := 0; i < 6; i++ {
		ref, err := reader.Store.Save(StoredToolResult{Tool: "catalog", Text: fmt.Sprint(i), TextComplete: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := reader.Store.ObserveSource(ctx, ref.ID, strings.Repeat("标题", 110), "https://example.test/"+strings.Repeat("path", 120), ""); err != nil {
			t.Fatal(err)
		}
	}
	args := json.RawMessage(`{"view":"list","tool":"catalog","limit":100}`)
	res, err := reader.Execute(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	r := &Registry{results: reader.Store}
	view := r.BudgetResultView(ctx, ModelResultBudgetRequest{Tool: ToolResultReadName, Text: res.Text, Args: args, Tokens: 700})
	var page map[string]any
	if json.Unmarshal([]byte(view.Text), &page) != nil {
		t.Fatal(view.Text)
	}
	items, ok := page["items"].([]any)
	if !ok || len(items) == 0 || len(items) >= 7 || page["page_displayed"] != true {
		t.Fatalf("catalog did not fit actual entries: %s", view.Text)
	}
	last := items[len(items)-1].(map[string]any)
	if page["next_offset"] != last["catalog_offset"].(float64)+1 {
		t.Fatal("budget skipped catalog entries")
	}
	if page["scan_complete"] != false {
		t.Fatal("shortened catalog claimed full scan")
	}
	zero := r.BudgetResultView(ctx, ModelResultBudgetRequest{Tool: ToolResultReadName, Text: res.Text, Args: args, Tokens: 0})
	if !strings.Contains(zero.Text, `"page_displayed":false`) || strings.Contains(zero.Text, `"recovery_available":false`) {
		t.Fatalf("directory became source failure: %s", zero.Text)
	}
}
