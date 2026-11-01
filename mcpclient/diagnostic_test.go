package mcpclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/mcpobserve"
)

// Real SDK HTTP transport, pool catalog binding and MCPTool execution; no model.
func TestPoolFailureDiagnosticThroughRealSDK(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "diagnostic-outbound-fixture"}, nil)
	for _, name := range []string{"business_failure", "rpc_failure"} {
		server.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if req.Params.Name == "rpc_failure" {
				return nil, &jsonrpc.Error{Code: -32602, Message: "Invalid date range private-input; password=private-password", Data: json.RawMessage(`{"request":"private-data"}`)}
			}
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: `{"code":"INVALID_COLUMN","message":"Required column missing for private-input","rows":["private-data"]}`}}}, nil
		})
	}
	endpoint := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	t.Cleanup(endpoint.Close)
	pool := newPool(t)
	pool.Reconcile(context.Background(), Config{Servers: map[string]*ServerConfig{"diag": {URL: endpoint.URL, Required: true}}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"business_failure", "rpc_failure"} {
		rec := &spanRecorder{}
		run := mcpobserve.WithRecorder(mcpobserve.WithMetadata(ctx, mcpobserve.Metadata{AgentID: "trusted-agent", OwnerID: "trusted-owner"}), rec)
		_, err := projectedTool(t, pool, "mcp_diag_"+name).Execute(run, json.RawMessage(`{"value":"private-input"}`))
		if err == nil {
			t.Fatal("expected actual MCP failure")
		}
		rec.mu.Lock()
		calls := append([]mcpobserve.Call(nil), rec.calls...)
		rec.mu.Unlock()
		if len(calls) != 2 {
			t.Fatalf("snapshots=%d", len(calls))
		}
		got := calls[1]
		if got.Status != "error" || got.AgentID != "trusted-agent" || got.OwnerID != "trusted-owner" || got.ErrorSummary == "" {
			t.Fatalf("missing/bad scoped diagnostic: %+v", got)
		}
		code, reason := "INVALID_COLUMN", "Required column missing"
		if name == "rpc_failure" {
			code, reason = "rpc_-32602", "Invalid date range"
		}
		if got.ErrorCode != code || !strings.Contains(got.ErrorSummary, reason) {
			t.Fatalf("actual SDK failure lost reason/code: %+v", got)
		}
		raw, _ := json.Marshal(calls)
		for _, secret := range []string{"private-input", "private-password", "private-data"} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("MCP observed payload/credential: %q", secret)
			}
		}
	}
}
