package mcpobserve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDiagnosticObservedFailureExtraction(t *testing.T) {
	args := json.RawMessage(`{"customer":"private-customer-value","password":"private-password"}`)
	rpc := fmt.Errorf("call failed: %w", &jsonrpc.Error{Code: -32602, Message: "Date range is invalid for private-customer-value; password=private-password", Data: json.RawMessage(`{"rows":["private-data-canary"]}`)})
	d := DiagnoseError(rpc, args)
	if d.Code != "rpc_-32602" || !strings.Contains(d.Summary, "Date range is invalid") {
		t.Fatalf("actual RPC reason/code lost: %+v", d)
	}
	for _, secret := range []string{"private-customer-value", "private-password", "private-data-canary"} {
		if strings.Contains(d.Summary, secret) {
			t.Fatalf("secret leaked: %q", secret)
		}
	}
	structured := &mcp.CallToolResult{IsError: true, StructuredContent: map[string]any{"error": map[string]any{"code": "MISSING_COLUMN", "message": "Required column is missing for private-customer-value"}, "rows": []any{"private-data-canary"}}, Content: []mcp.Content{&mcp.TextContent{Text: "must not override structured error"}}}
	d = DiagnoseResult(structured, args)
	if d.Code != "MISSING_COLUMN" || !strings.Contains(d.Summary, "Required column is missing") || strings.Contains(d.Summary, "private-customer-value") || strings.Contains(d.Summary, "private-data-canary") {
		t.Fatalf("unsafe/incorrect structured diagnostic: %+v", d)
	}
	for _, content := range []string{`{"error":{"code":"NOT_FOUND","reason":"Dataset does not exist"},"data":["private-data-canary"]}`, `{"code":"NOT_FOUND","detail":"Dataset does not exist","query":"SELECT secret FROM data"}`, `{"code":"NOT_FOUND","msg":"Dataset does not exist","data":["private-data-canary"]}`} {
		d = DiagnoseResult(&mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: content}}}, nil)
		if d.Code != "NOT_FOUND" || d.Summary != "Dataset does not exist" {
			t.Fatalf("JSON error selection: %+v", d)
		}
	}
}

func TestDiagnosticBoundsSuccessAndLegacyUnknown(t *testing.T) {
	if DiagnoseError(nil, nil) != (Diagnostic{}) || DiagnoseResult(&mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "private success body"}}}, nil) != (Diagnostic{}) {
		t.Fatal("success output captured")
	}
	d := SafeDiagnostic(Diagnostic{Summary: strings.Repeat("界", 700) + "\npassword=private", Code: strings.Repeat("C", 65)})
	if utf8.RuneCountInString(d.Summary) > 512 || strings.ContainsAny(d.Summary, "\r\n\t") || d.Code != "" {
		t.Fatalf("unbounded diagnostic: %d %+v", utf8.RuneCountInString(d.Summary), d)
	}
	sink := &recordingSink{}
	ctx := WithRecorder(context.Background(), sink)
	_, legacy := Start(ctx, Call{ErrorSummary: "must be discarded", ErrorCode: "FORGED"})
	legacy.End("error", "tool_error")
	if c := sink.calls[len(sink.calls)-1]; c.ErrorSummary != "" || c.ErrorCode != "" {
		t.Fatalf("old end fabricated a cause: %+v", c)
	}
	_, success := Start(ctx, Call{})
	success.EndDiagnostic("ok", "", Diagnostic{Summary: "private success payload", Code: "PRIVATE"})
	if c := sink.calls[len(sink.calls)-1]; c.ErrorSummary != "" || c.ErrorCode != "" {
		t.Fatal("success retained diagnostics")
	}
}

func TestDiagnosticConcurrentExactlyOneCompletion(t *testing.T) {
	sink := &recordingSink{}
	_, span := Start(WithRecorder(context.Background(), sink), Call{})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Go(func() {
			span.EndDiagnostic("error", "transport_error", DiagnoseError(errors.New("connection refused; token=private-secret"), nil))
		})
	}
	wg.Wait()
	if len(sink.calls) != 2 || sink.calls[1].ErrorSummary == "" || strings.Contains(sink.calls[1].ErrorSummary, "private-secret") {
		t.Fatalf("bad exactly-once/sanitization: %+v", sink.calls)
	}
}
