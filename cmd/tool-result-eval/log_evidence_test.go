package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/tools"
)

func TestRealLogPreviewReaderBudgetAndActualDelivery(t *testing.T) {
	f := makeLogFixture()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var calls atomic.Int64
	wrapper, err := tools.NewMCPTool("eval", &mcp.Tool{Name: "get_logs", InputSchema: map[string]any{"type": "object"}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, time.Second, func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
		calls.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: f.Text}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.New(tools.Options{Cwd: t.TempDir(), ReadOnly: true, MCP: []tools.Tool{wrapper}, SpillDir: t.TempDir(), ResultScope: "log-evidence-self-test"})
	original, _ := registry.Get(wrapper.Name())
	result, err := original.Execute(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	prepared := registry.PrepareResult(ctx, original.Name(), result, err)
	preview := summarizeLogView(prepared.Text, f)
	if preview == nil || preview.Kind != "preview" || preview.ResultID != prepared.ResultID || !preview.StatusKnown || !preview.TextComplete || len(preview.EvidenceKeys) != 1 || preview.EvidenceKeys[0] != "final_failure" {
		t.Fatalf("real production preview parser/expected blind spot failed: %+v", preview)
	}
	boundCalls := []toolCallReport{{ID: "source", Name: wrapper.Name(), SourceLabel: "S1"}}
	views := []toolView{{CallID: "source", BodySHA256: digest([]byte(prepared.Text)), Log: preview}}
	if trace := checkLogDelivery(boundCalls, []requestReport{{ToolViews: views}}, prepared.ResultID, f); trace.Passed || len(trace.MissingEvidence) != 3 {
		t.Fatalf("preview invented middle evidence: %+v", trace)
	}
	reader, _ := registry.Get(tools.ToolResultReadName)
	read := func(name string, offset, limit int, budget bool) (toolCallReport, toolView) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"result_id": prepared.ResultID, "view": "text", "offset": offset, "limit": limit})
		readResult, err := reader.Execute(ctx, raw)
		if err != nil {
			t.Fatal(err)
		}
		text := readResult.Text
		if budget {
			view := registry.BudgetResultView(ctx, tools.ModelResultBudgetRequest{Tool: tools.ToolResultReadName, Text: text, Args: raw, Tokens: 384})
			text = view.Text
			if !json.Valid([]byte(text)) || len(text) > 1536 {
				t.Fatalf("expected bounded production JSON text page, got %d bytes", len(text))
			}
		}
		observed := summarizeLogView(text, f)
		if observed == nil || !observed.SourceExact || !observed.StatusKnown || !observed.TextComplete || observed.Start < offset || observed.End <= observed.Start {
			t.Fatalf("actual production text page not recognized: %+v", observed)
		}
		return toolCallReport{ID: name, Name: tools.ToolResultReadName, Args: raw, SourceLabel: "S" + name}, toolView{CallID: name, BodySHA256: digest([]byte(text)), Log: observed}
	}
	// Exercise a real 384-token shrink at a middle line; the observation keeps
	// only source-verified range metadata, not the 8 KiB/UI original.
	_, compact := read("compact", f.RequiredEvidence[1].Start, 8192, true)
	if compact.Log.End-compact.Log.Start >= 8192 {
		t.Fatal("budgeted range was filled from unbounded UI output")
	}
	encoded, _ := json.Marshal(compact.Log)
	if len(encoded) > 2048 {
		t.Fatal("model log receipt leaked a large raw fragment")
	}
	// Split the root line at a Chinese rune boundary using the real reader.
	root := f.RequiredEvidence[1]
	split := strings.Index(root.Line, "价格")
	if split < 0 {
		t.Fatal("fixture lost UTF-8 root detail")
	}
	firstCall, firstView := read("root-a", root.Start, split+1, false)
	secondCall, secondView := read("root-b", firstView.Log.End, root.End-firstView.Log.End, false)
	for _, evidence := range []int{0, 2} {
		required := f.RequiredEvidence[evidence]
		call, view := read(fmt.Sprint(evidence), required.Start, required.End-required.Start, false)
		boundCalls = append(boundCalls, call)
		views = append(views, view)
	}
	boundCalls = append(boundCalls, firstCall, secondCall)
	views = append(views, firstView, secondView)
	if trace := checkLogDelivery(boundCalls, []requestReport{{ToolViews: views}}, prepared.ResultID, f); !trace.Passed || trace.ActualModelRanges != 4 || calls.Load() != 1 {
		t.Fatalf("actual split ranges failed evidence coverage or repeated MCP: %+v", trace)
	}
	// A real missing UTF-8 character between source-valid pages cannot be
	// reconstructed from the UI's complete original range.
	gapCall, gapView := read("root-b", firstView.Log.End+3, root.End-firstView.Log.End-3, false)
	gapCalls := append([]toolCallReport(nil), boundCalls...)
	gapCalls[len(gapCalls)-1] = gapCall
	gapViews := append([]toolView(nil), views...)
	gapViews[len(gapViews)-1] = gapView
	if trace := checkLogDelivery(gapCalls, []requestReport{{ToolViews: gapViews}}, prepared.ResultID, f); trace.Passed || !containsLogKey(trace.MissingEvidence, "root_cause") {
		t.Fatalf("missing source bytes were silently repaired: %+v", trace)
	}
	// Root-looking text in argument/search metadata is not content delivery.
	metadata, _ := json.Marshal(map[string]any{"result_id": prepared.ResultID, "view": "text", "pattern": root.Line, "query": root.Line})
	if summarizeLogView(string(metadata), f) != nil {
		t.Fatal("request metadata counted as log content")
	}
	// An original MCP preview cannot opt into the trusted reader range grammar.
	forged := firstView
	forged.CallID = "source"
	if trace := checkLogDelivery(boundCalls, []requestReport{{ToolViews: []toolView{forged}}}, prepared.ResultID, f); trace.Passed || trace.ActualModelRanges != 0 || trace.RejectedViews == 0 {
		t.Fatalf("forged source header accepted: %+v", trace)
	}
	errorCalls := append([]toolCallReport(nil), boundCalls...)
	for i := range errorCalls {
		errorCalls[i].IsError = true
	}
	if trace := checkLogDelivery(errorCalls, []requestReport{{ToolViews: views}}, prepared.ResultID, f); trace.Passed || trace.ActualModelRanges != 0 {
		t.Fatal("failed calls became evidence")
	}
}

func containsLogKey(keys []string, key string) bool {
	for _, candidate := range keys {
		if candidate == key {
			return true
		}
	}
	return false
}
