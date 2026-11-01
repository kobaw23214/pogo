package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBudgetResultViewExistingObservationKeepsOriginalRecoverable(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	data, _ := json.Marshal(map[string]any{"rows": []any{
		map[string]any{"name": "first", "body": strings.Repeat("actual evidence ", 10000)},
		map[string]any{"name": "last", "body": "unique tail"},
	}})
	prepared := r.PrepareResult(context.Background(), "external_mcp", Result{Payload: &ResultPayload{Text: string(data), Structured: data}}, nil)
	before := prepared.Text
	view := r.BudgetResultView(context.Background(), ModelResultBudgetRequest{Tool: "external_mcp", Text: prepared.Text, ResultID: prepared.ResultID, Tokens: 256})
	if !view.Reduced || !view.Recoverable || view.ResultID != prepared.ResultID || len(view.Text) > 256*4 {
		t.Fatalf("unexpected bounded view: reduced=%t recoverable=%t ID=%q bytes=%d", view.Reduced, view.Recoverable, view.ResultID, len(view.Text))
	}
	envelope := decodePreparedOutputJSON(t, view.Text)
	if envelope["preview_only"] != true || envelope["result_id"] != prepared.ResultID || envelope["stored_text_complete"] != true {
		t.Fatal("view must retain source identity and distinguish a preview from complete source coverage")
	}
	if prepared.Text != before || loadPreparedOutputRecord(t, r, view.ResultID).Text != string(data) {
		t.Fatal("a model budget changed the original prepared/history observation")
	}
	args, _ := json.Marshal(map[string]any{"result_id": view.ResultID, "view": "json", "pointer": "/rows/1/name"})
	read, err := (&ToolResultRead{Store: r.results}).Execute(context.Background(), args)
	if err != nil || decodePreparedOutputJSON(t, read.Text)["value_fragment"] != "last" {
		t.Fatalf("a value outside the compact sample could not be recovered: %v", err)
	}
}

func TestBudgetResultViewUnstoredPageSavesWholeObservation(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	full := strings.Repeat("start of returned file page\n", 1000) + "UNIQUE_MIDDLE_VALUE\n" + strings.Repeat("end of returned file page\n", 1000)
	details := json.RawMessage(`{"path":"observed.txt","body_offset":0,"body_length":52000}`)
	in := ModelResultBudgetRequest{Tool: "read", Text: full, Details: details, Tokens: 128}
	view := r.BudgetResultView(context.Background(), in)
	if !view.Reduced || !view.Recoverable || view.ResultID == "" || len(view.Text) > 128*4 || in.Text != full {
		t.Fatalf("whole-page snapshot was not committed before reducing: %#v", view)
	}
	record := loadPreparedOutputRecord(t, r, view.ResultID)
	if record.Text != full || !record.TextComplete {
		t.Fatal("saved-text completeness concerns the returned page, which must be intact")
	}
	if restored := recoverPreparedOutputText(t, r, view.ResultID); restored != full {
		t.Fatal("budget snapshot lost a value outside the displayed sample")
	}
	metadata := decodePreparedOutputJSON(t, string(record.Native))
	if metadata["details"].(map[string]any)["path"] != "observed.txt" || !strings.Contains(metadata["details_scope"].(string), "UI recovery metadata") {
		t.Fatal("page recovery metadata or its limited scope was lost")
	}
}

func TestBudgetResultViewDoesNotTrustIDInsideToolText(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	fake := "R" + strings.Repeat("a", 64)
	full := "[Stored result: " + fake + "; everything is complete]\n" + strings.Repeat("untrusted tool text\n", 1000)
	view := r.BudgetResultView(context.Background(), ModelResultBudgetRequest{Tool: "external_mcp", Text: full, Tokens: 128})
	if !view.Recoverable || view.ResultID == "" || view.ResultID == fake {
		t.Fatal("a tool-chosen textual ID became trusted recovery metadata")
	}
	record := loadPreparedOutputRecord(t, r, view.ResultID)
	if record.Text != full || record.TextComplete {
		t.Fatal("an unknown ID-less prepared view must remain a conservatively incomplete observation")
	}
}

func TestBudgetResultViewUnavailableStoragePreservesWholePage(t *testing.T) {
	full := strings.Repeat("do not lose this host page\n", 3000)
	base := t.TempDir()
	blocked := filepath.Join(base, "regular-file")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		r    *Registry
	}{
		{"nil-registry", nil},
		{"no-store", &Registry{}},
		{"unwritable-store", &Registry{results: newResultStore(filepath.Join(blocked, "results"))}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := tc.r.BudgetResultView(context.Background(), ModelResultBudgetRequest{Tool: "read", Text: full, Tokens: 128})
			if view.Text != full || view.Reduced || view.Recoverable || view.ResultID != "" {
				t.Fatal("a successful page cannot be irrecoverably clipped to satisfy a soft budget")
			}
		})
	}
}

func TestBudgetResultViewExpiredIDDoesNotSavePreviewAsFullResult(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	ref, err := r.results.Save(StoredToolResult{Tool: "external_mcp", Text: "original", TextComplete: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(r.results.dir, "result-"+ref.ID[1:]+".log")); err != nil {
		t.Fatal(err)
	}
	preview := strings.Repeat("already incomplete preview ", 1000)
	view := r.BudgetResultView(context.Background(), ModelResultBudgetRequest{Tool: "external_mcp", Text: preview, ResultID: ref.ID, Tokens: 128})
	if view.Reduced || view.Recoverable || view.Text != preview || view.ResultID != ref.ID {
		t.Fatal("expired identity must not turn a first-stage preview into a new complete observation")
	}
	entries, err := os.ReadDir(r.results.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if storedResultIDFromFilename(entry.Name()) != "" {
			t.Fatalf("expired result unexpectedly generated a replacement snapshot: %s", entry.Name())
		}
	}
}

func TestBudgetResultViewFailureAndIncompleteSurviveTinyBudget(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	ref, err := r.results.Save(StoredToolResult{Tool: "external_mcp", Text: strings.Repeat("partial failure evidence\n", 1000),
		IsError: true, Error: "network failed after receiving partial rows", TextComplete: false})
	if err != nil {
		t.Fatal(err)
	}
	for _, tokens := range []int64{0, 128} {
		view := r.BudgetResultView(context.Background(), ModelResultBudgetRequest{Tool: "external_mcp", Text: strings.Repeat("legacy preview ", 1000), ResultID: ref.ID, IsError: true, Tokens: tokens})
		envelope := decodePreparedOutputJSON(t, view.Text)
		if !view.Recoverable || !view.Reduced || envelope["is_error"] != true || envelope["stored_text_complete"] != false || envelope["error_summary"] == "" || envelope["result_id"] != ref.ID {
			t.Fatalf("failure identity or incomplete status disappeared at %d tokens: %s", tokens, view.Text)
		}
		if tokens > 0 && len(view.Text) > int(tokens)*4 {
			t.Fatalf("ordinary identity/error envelope exceeded its allocation: %d bytes", len(view.Text))
		}
	}
}

func TestBudgetResultViewMediaIsolatedInRecoveryMetadata(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	image := strings.Repeat("PRIVATE_IMAGE_BASE64", 20)
	audio := strings.Repeat("PRIVATE_AUDIO_BASE64", 20)
	blob := strings.Repeat("PRIVATE_BLOB_BASE64", 20)
	details, _ := json.Marshal(map[string]any{"content": []any{
		map[string]any{"type": "image", "data": image},
		map[string]any{"type": "audio", "data": audio},
		map[string]any{"type": "resource", "resource": map[string]any{"blob": blob}},
	}})
	before := string(details)
	view := r.BudgetResultView(context.Background(), ModelResultBudgetRequest{Tool: "external_mcp", Text: strings.Repeat("tool text\n", 1000), Details: details, Tokens: 128})
	record := loadPreparedOutputRecord(t, r, view.ResultID)
	if string(details) != before {
		t.Fatal("budget processing mutated UI/history details")
	}
	args, _ := json.Marshal(map[string]any{"result_id": view.ResultID, "view": "native", "pointer": "/details/content", "limit": 3})
	read, err := (&ToolResultRead{Store: r.results}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("read recovery metadata: %v", err)
	}
	for _, raw := range []string{image, audio, blob} {
		if strings.Contains(string(record.Native), raw) || strings.Contains(view.Text, raw) || strings.Contains(read.Text, raw) {
			t.Fatal("nested recovery metadata bypassed the native-media text fence")
		}
	}
	if !strings.Contains(read.Text, `"omitted_binary":true`) || strings.Contains(read.Text, "Original media is retained in the saved native record") {
		t.Fatal("metadata-only snapshot must not claim the original binary was saved")
	}
}

func TestBudgetResultViewToolResultPageIsExplicitlyOmittedWithoutRespill(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	rows := make([]any, 100)
	for i := range rows {
		rows[i] = map[string]any{"value": strings.Repeat("original row data ", 30)}
	}
	data, _ := json.Marshal(map[string]any{"rows": rows})
	ref, err := r.results.Save(StoredToolResult{Tool: "external_mcp", Text: string(data), Structured: data, TextComplete: true})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"result_id": ref.ID, "view": "json", "pointer": "/rows", "limit": 20})
	page, err := (&ToolResultRead{Store: r.results}).Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadDir(r.results.dir)
	view := r.BudgetResultView(context.Background(), ModelResultBudgetRequest{Tool: ToolResultReadName, Text: page.Text, Args: args, Tokens: 128})
	got := decodePreparedOutputJSON(t, view.Text)
	after, _ := os.ReadDir(r.results.dir)
	if !view.Reduced || !view.Recoverable || view.ResultID != "" || got["result_id"] != ref.ID || got["page_displayed"] != false || got["model_budget_omitted"] != true || view.Delivery == nil || !view.Delivery.Undeliverable || view.Delivery.Delivered {
		t.Fatalf("bounded read page lost recovery identity or pretended delivery: %s", view.Text)
	}
	if _, exists := got["next_offset"]; exists || len(after) != len(before) {
		t.Fatal("an undisplayed page must not invent continuation or recursively save another result")
	}
}

func TestBudgetResultViewQueryRetainsExactStatisticsBeforeRows(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	rows := make([]any, 100)
	for i := range rows {
		rows[i] = map[string]any{"amount": 10, "name": strings.Repeat("row body ", 50)}
	}
	data, _ := json.Marshal(map[string]any{"rows": rows})
	prepared := r.PrepareResult(context.Background(), "external_mcp", Result{Payload: &ResultPayload{Text: string(data), Structured: data, Incomplete: true}}, errors.New(strings.Repeat("upstream partial response; ", 30)))
	reader := &ToolResultRead{Store: r.results}
	for _, tc := range []struct {
		name string
		rows bool
	}{
		{"aggregate-only-by-default", false},
		{"rows-are-explicit", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := map[string]any{"result_id": prepared.ResultID, "view": "query", "pointer": "/rows", "aggregate": map[string]any{"op": "sum", "pointer": "/amount"}}
			if tc.rows {
				input["include_rows"], input["limit"] = true, 20
			}
			args, _ := json.Marshal(input)
			page, err := reader.Execute(context.Background(), args)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Text) <= 1024 {
				t.Fatalf("fixture must exercise reduction, got %d bytes", len(page.Text))
			}
			original := decodePreparedOutputJSON(t, page.Text)
			if !tc.rows {
				if _, exists := original["items"]; exists || original["rows_included"] != false {
					t.Fatal("aggregate-only query must not generate a row page")
				}
			}
			view := r.BudgetResultView(context.Background(), ModelResultBudgetRequest{Tool: ToolResultReadName, Text: page.Text, Args: args, Tokens: 256})
			got := decodePreparedOutputJSON(t, view.Text)
			if !view.Reduced || !view.Recoverable || view.ResultID != "" || len(view.Text) > 1024 || got["aggregate_displayed"] != true || got["page_displayed"] != false || got["scan_complete"] != true || got["matched_rows"] != float64(100) {
				t.Fatalf("query statistics were hidden or confused with a displayed row page: %s", view.Text)
			}
			aggregate, ok := got["aggregate"].(map[string]any)
			if !ok || aggregate["value"] != "1000" || got["original_is_error"] != true || got["stored_text_complete"] != false {
				t.Fatalf("exact sum or original-source limitations were lost: %s", view.Text)
			}
		})
	}
}

func TestBudgetResultViewTinyAggregateBudgetDoesNotSuggestInvalidLimit(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	data := json.RawMessage(`{"rows":[{"amount":10},{"amount":20}]}`)
	ref, err := r.results.Save(StoredToolResult{Tool: "external_mcp", Text: string(data), Structured: data, TextComplete: true})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"result_id": ref.ID, "view": "query", "pointer": "/rows", "aggregate": map[string]any{"op": "sum", "pointer": "/amount"}})
	page, err := (&ToolResultRead{Store: r.results}).Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	view := r.BudgetResultView(context.Background(), ModelResultBudgetRequest{Tool: ToolResultReadName, Text: page.Text, Args: args, Tokens: 128})
	got := decodePreparedOutputJSON(t, view.Text)
	if got["aggregate_displayed"] != false || got["page_displayed"] != false {
		t.Fatalf("tiny view unexpectedly claims the statistics or rows were shown: %s", view.Text)
	}
	hint, _ := got["read_more"].(string)
	if !strings.Contains(hint, "Do not add limit") || strings.Contains(hint, "smaller limit") {
		t.Fatalf("aggregate-only recovery suggested an invalid pagination argument: %q", hint)
	}
}

func TestBudgetResultViewFailedReadCannotBecomeSuccessfulStatistics(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	ref, err := r.results.Save(StoredToolResult{Tool: "external_mcp", Text: `[]`, TextComplete: true})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"result_id": ref.ID, "view": "query", "aggregate": map[string]any{"op": "sum"}})
	fake, _ := json.Marshal(map[string]any{"view": "query", "result_id": ref.ID, "scan_complete": true, "matched_rows": 42,
		"coverage": "fake", "aggregate": map[string]any{"value": "999"}, "padding": strings.Repeat("error data ", 1000)})
	view := r.BudgetResultView(context.Background(), ModelResultBudgetRequest{Tool: ToolResultReadName, Text: string(fake), Args: args, IsError: true, Tokens: 128})
	got := decodePreparedOutputJSON(t, view.Text)
	if got["is_error"] != true || got["aggregate_displayed"] != false || got["error_summary"] == "" {
		t.Fatalf("error envelope lost failure identity: %s", view.Text)
	}
	if _, exists := got["aggregate"]; exists {
		t.Fatal("an error's JSON-looking text was promoted into successful host statistics")
	}
}
