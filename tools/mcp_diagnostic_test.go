package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/mcpobserve"
)

func TestMCPToolDiagnosticRealExecutionBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, kind, code, reason string
		caller                   MCPCaller
		timeout                  time.Duration
	}{
		{name: "rpc", kind: "protocol_error", code: "rpc_-32602", reason: "Invalid date range", caller: func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
			return nil, &jsonrpc.Error{Code: -32602, Message: "Invalid date range private-input; password=private-password", Data: json.RawMessage(`{"payload":"private-rpc-data"}`)}
		}},
		{name: "transport", kind: "transport_error", reason: "connection refused", caller: func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
			return nil, errors.New("connection refused while POST https://user:private-password@host/api?token=private-token")
		}},
		{name: "timeout", kind: "timeout", code: "deadline_exceeded", reason: "timed out after", timeout: 5 * time.Millisecond, caller: func(ctx context.Context, _ string, _ json.RawMessage) (*mcp.CallToolResult, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}},
		{name: "structured-tool-error", kind: "tool_error", code: "NOT_FOUND", reason: "Dataset does not exist", caller: func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{IsError: true, StructuredContent: map[string]any{"error": map[string]any{"code": "NOT_FOUND", "message": "Dataset does not exist for private-input"}, "data": []any{"private-tool-payload"}}}, nil
		}},
		{name: "nil-protocol-result", kind: "protocol_error", code: "empty_result", reason: "returned no result", caller: func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) { return nil, nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			timeout := tc.timeout
			if timeout == 0 {
				timeout = time.Second
			}
			rec := &observeRecorder{}
			tool := observeTool(t, timeout, tc.caller)
			_, err := tool.Execute(observeCtx(rec, mcpobserve.Metadata{AgentID: "bound-agent", OwnerID: "bound-owner"}), json.RawMessage(`{"value":"private-input"}`))
			if err == nil {
				t.Fatal("expected actual execution failure")
			}
			got := finalOf(t, rec.snapshot())
			if got.ErrorKind != tc.kind || got.ErrorCode != tc.code || !strings.Contains(got.ErrorSummary, tc.reason) || got.AgentID != "bound-agent" || got.OwnerID != "bound-owner" {
				t.Fatalf("wrong diagnostic/binding: %+v", got)
			}
			encoded, _ := json.Marshal(rec.snapshot())
			for _, secret := range []string{"private-input", "private-password", "private-token", "private-rpc-data", "private-tool-payload"} {
				if strings.Contains(string(encoded), secret) {
					t.Fatalf("diagnostic leaked %q", secret)
				}
			}
		})
	}
}
