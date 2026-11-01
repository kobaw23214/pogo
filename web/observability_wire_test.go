package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/mcpobserve"
)

// Real SDK transports prove the adapter is actually installed, and that a
// session's next request does not retain its previous call's trace context.
func TestMCPTracePerRequestOverBothTransports(t *testing.T) {
	for _, transport := range []string{"streamable", "sse"} {
		t.Run(transport, func(t *testing.T) {
			h := newHarness(t, scriptedTurns(textTurn("done")))
			s, err := NewServer(h.mgr, ServerOptions{Token: h.token, MCP: &MCPOptions{Token: "fixture"}})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ts := httptest.NewServer(s.Handler())
			defer ts.Close()
			client := mcp.NewClient(&mcp.Implementation{Name: "trace-fixture"}, nil)
			httpClient := &http.Client{Transport: bearerRT{"fixture"}}
			var conn mcp.Transport = &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp", HTTPClient: httpClient}
			if transport == "sse" {
				conn = &mcp.SSEClientTransport{Endpoint: ts.URL + "/sse", HTTPClient: httpClient}
			}
			cs, err := client.Connect(context.Background(), conn, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer cs.Close()
			knownTrace := "0123456789abcdef0123456789abcdef"
			knownParent := "fedcba9876543210"
			for _, meta := range []map[string]any{
				{"traceparent": "00-" + knownTrace + "-" + knownParent + "-00", "tracestate": "vendor=fixture", "client_id": "forged", "owner_id": "forged"},
				nil,
			} {
				params := &mcp.CallToolParams{Name: "agent_task_list", Arguments: map[string]any{}}
				params.SetMeta(meta)
				if _, err := cs.CallTool(context.Background(), params); err != nil {
					t.Fatal(err)
				}
			}
			page, err := s.mcpCalls.Query(context.Background(), mcpobserve.Filter{Direction: "inbound"})
			if err != nil || len(page.Calls) != 2 {
				t.Fatalf("calls: %+v %v", page, err)
			}
			var continued, fresh bool
			for _, call := range page.Calls {
				if call.ClientID != "" || call.OwnerID != "" {
					t.Fatalf("untrusted identity accepted: %+v", call)
				}
				if call.SpanID != mcpobserve.SpanIDFor(call.ID) || call.ParentID != "" || call.Entrypoint != "mcp" {
					t.Fatalf("invalid span: %+v", call)
				}
				if call.TraceID == knownTrace {
					continued = call.ParentSpanID == knownParent && call.TraceFlags == "00"
				} else {
					fresh = len(call.TraceID) == 32 && call.ParentSpanID == "" && call.TraceFlags == "01"
				}
			}
			if !continued || !fresh {
				t.Fatalf("request propagation or isolation failed: %+v", page.Calls)
			}
		})
	}
}
