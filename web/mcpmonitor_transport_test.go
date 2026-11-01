package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/mcpobserve"
)

func TestMCPMonitorRequestIdentityPerTransport(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("done")))
	s, err := NewServer(h.mgr, ServerOptions{Token: h.token, MCP: &MCPOptions{Token: h.token}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.mgr.Close(); s.Close() })
	endpoint := httptest.NewServer(s.Handler())
	t.Cleanup(endpoint.Close)
	for _, transport := range []string{"streamable", "sse"} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		var wire mcp.Transport
		client := &http.Client{Transport: bearerRT{h.token}}
		if transport == "sse" {
			wire = &mcp.SSEClientTransport{Endpoint: endpoint.URL + "/sse", HTTPClient: client}
		} else {
			wire = &mcp.StreamableClientTransport{Endpoint: endpoint.URL + "/mcp", HTTPClient: client}
		}
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "transport-" + transport}, nil).Connect(ctx, wire, nil)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			callTool(t, cs, "agent_task_list", map[string]any{})
		}
		cs.Close()
		p, err := s.mcpCalls.Query(ctx, mcpobserve.Filter{Client: "transport-" + transport})
		cancel()
		if err != nil || len(p.Calls) != 2 {
			t.Fatalf("%s calls: %+v %v", transport, p, err)
		}
		for _, c := range p.Calls {
			if c.Transport != transport || c.Status != "ok" {
				t.Fatalf("wrong transport/outcome: %+v", c)
			}
		}
		if transport == "streamable" {
			if p.Calls[0].RequestID == "" || p.Calls[0].RequestID == p.Calls[1].RequestID {
				t.Fatalf("request ID reused connection ID: %+v", p.Calls)
			}
		} else {
			for _, c := range p.Calls {
				if c.RequestID != "" {
					t.Fatal("SSE execution falsely attributed to HTTP handshake")
				}
			}
		}
	}
}
