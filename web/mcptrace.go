package web

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/mcpobserve"
)

// withMCPRequestTrace continues W3C trace context carried in this JSONRPC
// request's params._meta (MCP 2026-07-28: unprefixed traceparent/tracestate),
// identical for the SSE and streamable inbound transports. Extraction is
// per-request from Params.Meta, never from the transport's retained handshake
// context: each tools/call re-derives its parent from its own params, and a
// call without a valid traceparent starts a fresh trace at Start.
//
// Called from mcpLayer.observeMiddleware before mcpobserve.Start.
func withMCPRequestTrace(ctx context.Context, req *mcp.CallToolRequest) context.Context {
	var meta map[string]any
	if req != nil && req.Params != nil {
		meta = req.Params.GetMeta()
	}
	return mcpobserve.WithIncomingTrace(ctx, meta)
}
