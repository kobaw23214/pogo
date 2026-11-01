package web

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPInboundDiagnosticRealSDK(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("not invoked")))
	rec := &observeRecorder{}
	h.mgr.SetMCPRecorder(rec)
	l := newMCPLayer(h.mgr, MCPOptions{}, true, nil)
	srv := mcp.NewServer(&mcp.Implementation{Name: "diagnostic-inbound-fixture"}, nil)
	srv.AddReceivingMiddleware(l.observeMiddleware())
	for _, name := range []string{"business_failure", "rpc_failure"} {
		srv.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if req.Params.Name == "rpc_failure" {
				return nil, &jsonrpc.Error{Code: -32602, Message: "Unsupported range for private-input; token=private-token", Data: json.RawMessage(`{"payload":"private-rpc-data"}`)}
			}
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: `{"error":{"code":"INVALID_RANGE","message":"Start must precede end for private-input"},"data":["private-body"]}`}}}, nil
		})
	}
	cs := connectInMemory(t, srv, "diagnostic-fixture")
	for _, name := range []string{"business_failure", "rpc_failure"} {
		_, _ = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"value": "private-input"}})
		got := rec.finalForTool(name)
		if got == nil || got.Status != "error" || got.ErrorSummary == "" {
			t.Fatalf("missing actual inbound diagnostic: %+v", got)
		}
		if name == "business_failure" && (got.ErrorCode != "INVALID_RANGE" || !strings.Contains(got.ErrorSummary, "Start must precede end")) {
			t.Fatalf("business reason: %+v", got)
		}
		if name == "rpc_failure" && got.ErrorCode != "rpc_-32602" {
			t.Fatalf("RPC code: %+v", got)
		}
		raw, _ := json.Marshal(got)
		for _, secret := range []string{"private-input", "private-token", "private-rpc-data", "private-body"} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("inbound metadata leaked %q", secret)
			}
		}
	}
}
