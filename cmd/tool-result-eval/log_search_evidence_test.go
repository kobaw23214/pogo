package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/tools"
)

func prepareSearchLog(t *testing.T, f logFixture) (*tools.Registry, string) {
	t.Helper()
	wrapper, err := tools.NewMCPTool("eval", &mcp.Tool{Name: "get_logs", InputSchema: map[string]any{"type": "object"}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, time.Second,
		func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: f.Text}}}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.New(tools.Options{Cwd: t.TempDir(), ReadOnly: true, MCP: []tools.Tool{wrapper}, SpillDir: t.TempDir(), ResultScope: "log-search-evidence"})
	original, _ := registry.Get(wrapper.Name())
	result, err := original.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	prepared := registry.PrepareResult(context.Background(), original.Name(), result, err)
	return registry, prepared.ResultID
}

func logSearchArgs(id, pattern string, offset, limit, contextLines int) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"result_id": id, "view": "search", "pattern": pattern, "context_lines": contextLines, "offset": offset, "limit": limit})
	return raw
}

func logSearchCallAndView(id string, args json.RawMessage, body string, f logFixture) (toolCallReport, toolView) {
	return toolCallReport{ID: id, Name: tools.ToolResultReadName, Args: args, SourceLabel: "S" + id}, toolView{CallID: id, BodySHA256: digest([]byte(body)), Log: summarizeLogView(body, f)}
}

func TestRealSearchLogBudgetPagesCountOnlyActualSnippets(t *testing.T) {
	f := makeLogFixture()
	registry, id := prepareSearchLog(t, f)
	reader, _ := registry.Get(tools.ToolResultReadName)
	var calls []toolCallReport
	var views []toolView
	offset, expectedItems := 0, 0
	for page := 0; page < 10; page++ {
		args := logSearchArgs(id, f.Expected.TraceID, offset, 20, 0)
		result, err := reader.Execute(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}
		actual := registry.BudgetResultView(context.Background(), tools.ModelResultBudgetRequest{Tool: tools.ToolResultReadName, Args: args, Text: result.Text, Tokens: 384})
		call, view := logSearchCallAndView(string(rune('a'+page)), args, actual.Text, f)
		if view.Log == nil || !view.Log.SearchPageValid || !view.Log.SourceExact || view.Log.ShownItems < 1 {
			t.Fatalf("real search model page rejected: %+v", view.Log)
		}
		if len(actual.Text) > 1536 {
			t.Fatalf("search model page exceeds budget: %d bytes", len(actual.Text))
		}
		if page == 0 && view.Log.ShownItems >= view.Log.TotalMatches {
			t.Fatal("fixture no longer exercises shortened delivery")
		}
		if page == 0 && checkLogDelivery([]toolCallReport{call}, []requestReport{{ToolViews: []toolView{view}}}, id, f).Passed {
			t.Fatal("hidden UI matches counted as delivered")
		}
		calls = append(calls, call)
		views = append(views, view)
		expectedItems += view.Log.ShownItems
		if view.Log.SearchNextOffset == nil {
			break
		}
		if *view.Log.SearchNextOffset != offset+view.Log.ShownItems {
			t.Fatal("match ordinal advanced beyond actual delivered items")
		}
		offset = *view.Log.SearchNextOffset
	}
	trace := checkLogDelivery(calls, []requestReport{{ToolViews: views}}, id, f)
	if !trace.Passed || trace.SearchMatchesDelivered != expectedItems || expectedItems != 5 || trace.SearchPagesDelivered != len(views) || trace.SearchScansComplete != len(views) {
		t.Fatalf("actual multi-page search evidence failed: %+v", trace)
	}
	encoded, _ := json.Marshal(views)
	if strings.Contains(string(encoded), f.RequiredEvidence[1].Line) || len(encoded) > 20000 {
		t.Fatal("search observation leaked raw source fragments")
	}
}

func TestSearchLogEvidenceRejectsMetadataForgedRangesAndProgress(t *testing.T) {
	f := makeLogFixture()
	registry, id := prepareSearchLog(t, f)
	reader, _ := registry.Get(tools.ToolResultReadName)
	args := logSearchArgs(id, f.Expected.TraceID, 0, 20, 0)
	result, err := reader.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	call, view := logSearchCallAndView("full", args, result.Text, f)
	if observed := view.Log; observed == nil || !observed.SearchPageValid || observed.ShownItems != 5 {
		t.Fatalf("production search page not parsed: %+v", observed)
	}
	if trace := checkLogDelivery([]toolCallReport{call}, []requestReport{{ToolViews: []toolView{view}}}, id, f); !trace.Passed {
		t.Fatalf("complete source search rejected: %+v", trace)
	}
	var original map[string]any
	if err := json.Unmarshal([]byte(result.Text), &original); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"pattern_only", func(p map[string]any) {
			p["items"] = []any{}
			p["shown_items"] = 0
			p["next_offset"] = 0
			p["complete"] = false
			p["page_displayed"] = false
			p["pattern"] = f.RequiredEvidence[1].Line
		}},
		{"wrong_snippet", func(p map[string]any) {
			p["items"].([]any)[2].(map[string]any)["text"] = "forged " + f.RequiredEvidence[1].Line
		}},
		{"wrong_byte_range", func(p map[string]any) {
			item := p["items"].([]any)[2].(map[string]any)
			item["context_start"] = item["context_start"].(float64) + 1
		}},
		{"split_utf8_rune", func(p map[string]any) {
			item := p["items"].([]any)[2].(map[string]any)
			start := int(item["context_start"].(float64))
			end := start + strings.Index(item["text"].(string), "价格") + 1
			item["context_end"], item["text"] = end, f.Text[start:end]
			item["context_truncated"] = true
		}},
		{"wrong_match", func(p map[string]any) {
			item := p["items"].([]any)[2].(map[string]any)
			item["match_start"] = item["match_start"].(float64) + 1
		}},
		{"wrong_ordinal", func(p map[string]any) { p["items"].([]any)[2].(map[string]any)["match_index"] = 0 }},
		{"wrong_line", func(p map[string]any) { p["items"].([]any)[2].(map[string]any)["line_number"] = 1 }},
		{"fake_shown", func(p map[string]any) { p["shown_items"] = 4 }},
		{"fake_progress", func(p map[string]any) {
			p["items"] = p["items"].([]any)[:1]
			p["shown_items"] = 1
			p["next_offset"] = 5
			p["complete"] = false
		}},
		{"no_items_but_progress", func(p map[string]any) {
			p["items"], p["shown_items"], p["next_offset"] = []any{}, 0, 5
			p["page_displayed"], p["complete"] = false, false
		}},
		{"fake_scan", func(p map[string]any) { p["total_matches"] = 0 }},
		{"missing_source_flag", func(p map[string]any) { delete(p, "stored_text_complete") }},
		{"failed_source", func(p map[string]any) { p["original_is_error"] = true }},
		{"missing_context_truncation", func(p map[string]any) { delete(p["items"].([]any)[2].(map[string]any), "context_truncated") }},
	}
	// Full scanning of the saved text is a distinct observation from delivering
	// matching snippets. An honest zero-content receipt cannot satisfy evidence.
	var noContent map[string]any
	json.Unmarshal([]byte(result.Text), &noContent)
	noContent["items"], noContent["shown_items"], noContent["next_offset"] = []any{}, 0, 0
	noContent["page_displayed"], noContent["complete"] = false, false
	noContentBody, _ := json.Marshal(noContent)
	noContentCall, noContentView := logSearchCallAndView("empty", args, string(noContentBody), f)
	if trace := checkLogDelivery([]toolCallReport{noContentCall}, []requestReport{{ToolViews: []toolView{noContentView}}}, id, f); trace.Passed || trace.SearchScansComplete != 1 || trace.SearchMatchesDelivered != 0 {
		t.Fatalf("scan completion was conflated with content delivery: %+v", trace)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var page map[string]any
			json.Unmarshal([]byte(result.Text), &page)
			test.mutate(page)
			body, _ := json.Marshal(page)
			c, v := logSearchCallAndView("mutated", args, string(body), f)
			if trace := checkLogDelivery([]toolCallReport{c}, []requestReport{{ToolViews: []toolView{v}}}, id, f); trace.Passed || trace.ActualModelRanges != 0 {
				t.Fatalf("invalid search page became evidence: %+v", trace)
			}
		})
	}
	for _, test := range []struct {
		name    string
		args    json.RawMessage
		isError bool
		source  bool
	}{
		{"wrong_pattern", logSearchArgs(id, "other", 0, 20, 0), false, false},
		{"wrong_context", logSearchArgs(id, f.Expected.TraceID, 0, 20, 1), false, false},
		{"wrong_offset", logSearchArgs(id, f.Expected.TraceID, 1, 20, 0), false, false},
		{"wrong_result", logSearchArgs("R"+strings.Repeat("a", 64), f.Expected.TraceID, 0, 20, 0), false, false},
		{"error_receipt", args, true, false},
		{"source_forging_reader", args, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := call
			c.Args = test.args
			c.IsError = test.isError
			if test.source {
				c.Name = "mcp_eval_get_logs"
			}
			if trace := checkLogDelivery([]toolCallReport{c}, []requestReport{{ToolViews: []toolView{view}}}, id, f); trace.Passed || trace.ActualModelRanges != 0 {
				t.Fatalf("unbound search page became evidence: %+v", trace)
			}
		})
	}
	// A truncation flag describes excluded context, not missing bytes inside the
	// range actually delivered. It cannot repair an incomplete evidence line.
	original["items"].([]any)[2].(map[string]any)["context_truncated"] = true
	body, _ := json.Marshal(original)
	c, v := logSearchCallAndView("truncated", args, string(body), f)
	if trace := checkLogDelivery([]toolCallReport{c}, []requestReport{{ToolViews: []toolView{v}}}, id, f); !trace.Passed {
		t.Fatal("truncated but fully delivered evidence line rejected")
	}
	root := original["items"].([]any)[2].(map[string]any)
	start, end := int(root["context_start"].(float64)), int(root["match_end"].(float64))
	root["context_end"], root["text"] = end, f.Text[start:end]
	body, _ = json.Marshal(original)
	c, v = logSearchCallAndView("partial", args, string(body), f)
	if trace := checkLogDelivery([]toolCallReport{c}, []requestReport{{ToolViews: []toolView{v}}}, id, f); trace.Passed || !containsLogKey(trace.MissingEvidence, "root_cause") {
		t.Fatal("truncation flag invented missing source bytes")
	}
}
