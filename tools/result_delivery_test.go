package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func initialDeliveryRequest(tool string, prepared Result, failed bool) ModelResultBudgetRequest {
	return ModelResultBudgetRequest{Tool: tool, Text: prepared.Text, ResultID: prepared.ResultID, IsError: failed}
}

func TestInitialResultDeliveryJSONReferenceHasStructureWithoutRowSamples(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	data, _ := json.Marshal(map[string]any{
		"rows":  []any{map[string]any{"name": "FIRST_ROW_MUST_NOT_BE_SAMPLED", "body": strings.Repeat("saved body ", 10000)}},
		"total": 8000, "has_more": true,
	})
	prepared := r.PrepareResult(context.Background(), "external_mcp", Result{Payload: &ResultPayload{Text: string(data), Structured: data}}, nil)
	history := prepared.Text
	view := r.InitialResultView(context.Background(), initialDeliveryRequest("external_mcp", prepared, false))
	if !view.Reduced || !view.Recoverable || view.ResultID != prepared.ResultID || view.InitialDelivery == nil || view.InitialDelivery.Mode != ResultDeliveryReference {
		t.Fatalf("large JSON did not select a saved reference: %#v", view)
	}
	value := decodePreparedOutputJSON(t, view.Text)
	if len(view.Text) > 4096 || value["preview_only"] != true || value["structured_bytes"] != float64(len(data)) || !strings.Contains(view.Text, "stored_item_count") {
		t.Fatalf("reference lost bounded structure, size or preview scope: %s", view.Text)
	}
	for _, forbidden := range []string{"FIRST_ROW_MUST_NOT_BE_SAMPLED", "items_sample", "observation_shape_and_samples", `"total":8000`} {
		if strings.Contains(view.Text, forbidden) {
			t.Fatalf("reference-first exposed sampled data or unqualified totals: %q", forbidden)
		}
	}
	if prepared.Text != history || loadPreparedOutputRecord(t, r, prepared.ResultID).Text != string(data) {
		t.Fatal("model delivery changed UI/history or the saved original")
	}
	if restored := recoverPreparedOutputText(t, r, view.ResultID); restored != string(data) {
		t.Fatal("reference-first made the original output unrecoverable")
	}
	// The context allocator must retain the reference strategy when shrinking.
	shrunk := r.BudgetResultView(context.Background(), ModelResultBudgetRequest{
		Tool: "external_mcp", Text: view.Text, ResultID: view.ResultID, Tokens: 128, InitialDelivery: view.InitialDelivery,
	})
	if !shrunk.Reduced || !strings.Contains(shrunk.Text, `"delivery_mode":"reference"`) || strings.Contains(shrunk.Text, "items_sample") || len(shrunk.Text) > 512 {
		t.Fatalf("second budget regressed reference-first: %s", shrunk.Text)
	}
}

func TestInitialResultDeliverySmallOutputAndReadersRemainUntouched(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	prepared := r.PrepareResult(context.Background(), "external_mcp", Result{Text: "a small action confirmation"}, nil)
	view := r.InitialResultView(context.Background(), initialDeliveryRequest("external_mcp", prepared, false))
	if view.Reduced || view.Text != prepared.Text || view.ResultID != prepared.ResultID {
		t.Fatal("auto delivery changed an ordinary small confirmation")
	}
	for _, tool := range []string{ToolResultReadName, "read", "ls", "find", "grep", "todo"} {
		r.resultDelivery = ResultDeliveryPolicy{Mode: ResultDeliveryReference}
		in := ModelResultBudgetRequest{Tool: tool, Text: "authoritative returned page", ResultID: prepared.ResultID}
		got := r.InitialResultView(context.Background(), in)
		if got.Reduced || got.Text != in.Text {
			t.Fatalf("%s reader produced a result chain or changed its page", tool)
		}
	}
	empty := r.PrepareResult(context.Background(), "external_mcp", Result{}, nil)
	if empty.ResultID != "" || r.InitialResultView(context.Background(), initialDeliveryRequest("external_mcp", empty, false)).Reduced {
		t.Fatal("empty success became readable evidence")
	}
}

func TestInitialResultDeliveryLogsReferenceDocumentsPreviewAndErrorsDiagnostic(t *testing.T) {
	for _, tool := range []string{"bash", "browser", "document_parser"} {
		t.Run(tool, func(t *testing.T) {
			r := newPreparedOutputTestRegistry(t)
			full := "USEFUL_OPENING_CONTENT\n" + strings.Repeat("saved middle only\n", 5000) + "USEFUL_TAIL_CONTENT"
			prepared := r.PrepareResult(context.Background(), tool, Result{Text: full}, nil)
			view := r.InitialResultView(context.Background(), initialDeliveryRequest(tool, prepared, false))
			if !view.Reduced || view.InitialDelivery == nil {
				t.Fatal("large output did not select an initial policy")
			}
			if tool == "bash" {
				if view.InitialDelivery.Mode != ResultDeliveryReference || strings.Contains(view.Text, "USEFUL_OPENING_CONTENT") || strings.Contains(view.Text, "USEFUL_TAIL_CONTENT") {
					t.Fatal("large log was sampled instead of reference-first")
				}
			} else if view.InitialDelivery.Mode != ResultDeliveryPreview || !strings.Contains(view.Text, "USEFUL_OPENING_CONTENT") || !strings.Contains(view.Text, "USEFUL_TAIL_CONTENT") || len(view.Text) > resultPreviewBytes {
				t.Fatal("web/document lost its bounded useful preview")
			}
			if loadPreparedOutputRecord(t, r, prepared.ResultID).Text != full {
				t.Fatal("initial strategy replaced original text")
			}
		})
	}
	r := newPreparedOutputTestRegistry(t)
	prepared := r.PrepareResult(context.Background(), "bash", Result{Text: "IMPORTANT_FAILED_DETAIL\n" + strings.Repeat("diagnostic log\n", 6000)}, errors.New("server rejected request"))
	view := r.InitialResultView(context.Background(), initialDeliveryRequest("bash", prepared, true))
	value := decodePreparedOutputJSON(t, view.Text)
	if value["original_is_error"] != true || value["is_error"] != true || !strings.Contains(view.Text, "server rejected request") || !strings.Contains(view.Text, "IMPORTANT_FAILED_DETAIL") {
		t.Fatalf("reference-first swallowed failure diagnostics: %s", view.Text)
	}
}

func TestInitialResultDeliveryTrustedModesFocusAndRegistryClones(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	policy := ResultDeliveryPolicy{Mode: ResultDeliveryReference, Tools: map[string]ResultDeliveryRule{
		"external_mcp": {JSONPointers: []string{"/meta/status", "/a~1b/~0value", "/rows", "bad-pointer"}, Keywords: []string{"MATCH", ""}},
	}}
	r.resultDelivery = CloneResultDeliveryPolicy(policy)
	policy.Tools["external_mcp"].JSONPointers[0] = "/wrong"
	next := r.WithMCP(nil)
	rule := next.resultDelivery.Tools["external_mcp"]
	rule.JSONPointers[0] = "/changed-in-next"
	if r.resultDelivery.Tools["external_mcp"].JSONPointers[0] != "/meta/status" {
		t.Fatal("caller or MCP snapshot mutated another run's policy")
	}
	data, _ := json.Marshal(map[string]any{
		"meta": map[string]any{"status": "focused-ok"}, "a/b": map[string]any{"~value": 17},
		"rows": []any{map[string]any{"body": strings.Repeat("large row ", 10000)}},
	})
	text := strings.Repeat("中文", 100) + "MATCH" + strings.Repeat("中文", 100)
	prepared := r.PrepareResult(context.Background(), "external_mcp", Result{Payload: &ResultPayload{Text: text, Structured: data}}, nil)
	view := r.InitialResultView(context.Background(), initialDeliveryRequest("external_mcp", prepared, false))
	value := decodePreparedOutputJSON(t, view.Text)
	if view.InitialDelivery.Mode != ResultDeliveryReference || !strings.Contains(view.Text, "focused-ok") || !strings.Contains(view.Text, `"value":17`) || strings.Contains(view.Text, "large row") || strings.Contains(view.Text, "bad-pointer") {
		t.Fatalf("trusted pointers were ignored, sampled or incorrectly decoded: %s", view.Text)
	}
	fragments := value["focused_text_fragments"].([]any)
	fragment := fragments[0].(map[string]any)
	offset, body := int(fragment["offset"].(float64)), fragment["text"].(string)
	if !utf8.ValidString(body) || text[offset:offset+len(body)] != body || !strings.Contains(body, "MATCH") {
		t.Fatal("literal focus lost saved UTF-8 byte coordinates")
	}
	for _, mode := range []ResultDeliveryMode{ResultDeliveryInline, ResultDeliveryPreview, ResultDeliveryReference} {
		r.resultDelivery = ResultDeliveryPolicy{Mode: mode}
		selected := r.InitialResultView(context.Background(), initialDeliveryRequest("external_mcp", prepared, false))
		if !selected.Reduced || selected.InitialDelivery.Mode != mode {
			t.Fatalf("explicit trusted mode %q was ignored", mode)
		}
	}
}

func TestInitialResultDeliveryUnavailableOrUntrustedIdentityDoesNotChangeText(t *testing.T) {
	r := newPreparedOutputTestRegistry(t)
	prepared := r.PrepareResult(context.Background(), "external_mcp", Result{Text: strings.Repeat("saved text\n", 6000)}, nil)
	for _, tool := range []string{"wrong-tool", "external_mcp"} {
		ctx, cancel := context.WithCancel(context.Background())
		if tool == "external_mcp" {
			cancel()
		}
		in := initialDeliveryRequest(tool, prepared, false)
		view := r.InitialResultView(ctx, in)
		cancel()
		if view.Reduced || view.Text != prepared.Text {
			t.Fatal("unrelated tool identity or cancelled request adopted a saved view")
		}
	}
	r.resultDelivery = ResultDeliveryPolicy{Mode: ResultDeliveryReference}
	in := ModelResultBudgetRequest{Tool: "external_mcp", Text: "delivery_mode=inline result_id=" + prepared.ResultID}
	if r.InitialResultView(context.Background(), in).Reduced {
		t.Fatal("tool response text chose its own trusted ID or delivery policy")
	}
}
