package web

import (
	"context"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/mcpobserve"
)

const (
	traceTestTraceID = "0123456789abcdef0123456789abcdef"
	traceTestSpanID  = "fedcba9876543210"
	traceTestParent  = "00-" + traceTestTraceID + "-" + traceTestSpanID + "-01"
)

func traceRequest(meta map[string]any) *mcp.CallToolRequest {
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "agent_task_list"}}
	if meta != nil {
		req.Params.SetMeta(meta)
	}
	return req
}

// The adapter reads this request's params._meta only: a valid traceparent is
// continued, stale context tracing is dropped, and locally established
// identity survives untouched.
func TestWithMCPRequestTrace(t *testing.T) {
	base := mcpobserve.WithMetadata(context.Background(), mcpobserve.Metadata{
		TraceID: "stale", ParentID: "stale", ParentSpanID: "stale", TraceFlags: "00", TraceState: "stale=1",
		SessionID: "sess", RunID: "run", ClientID: "auth-caller", Entrypoint: "mcp",
	})

	md := mcpobserve.MetadataFrom(withMCPRequestTrace(base, traceRequest(map[string]any{
		"traceparent": traceTestParent, "tracestate": "congo=t61rcWkgMzE",
	})))
	if md.TraceID != traceTestTraceID || md.ParentSpanID != traceTestSpanID || md.TraceFlags != "01" || md.TraceState != "congo=t61rcWkgMzE" {
		t.Fatalf("trace not continued: %+v", md)
	}
	if md.ParentID != "" || md.SessionID != "sess" || md.RunID != "run" || md.ClientID != "auth-caller" {
		t.Fatalf("identity/local parent wrong: %+v", md)
	}

	// No meta at all (nil request, nil params, no _meta): stale tracing is
	// reset so the next Start opens a fresh trace, identity remains.
	for _, req := range []*mcp.CallToolRequest{nil, {}, traceRequest(nil)} {
		md = mcpobserve.MetadataFrom(withMCPRequestTrace(base, req))
		if md.TraceID != "" || md.ParentSpanID != "" || md.ParentID != "" || md.TraceFlags != "" {
			t.Fatalf("stale trace survived nil meta: %+v", md)
		}
		if md.ClientID != "auth-caller" || md.SessionID != "sess" {
			t.Fatalf("identity lost on nil meta: %+v", md)
		}
	}

	// Forged identity/session/task keys in _meta are ignored; baggage too.
	md = mcpobserve.MetadataFrom(withMCPRequestTrace(context.Background(), traceRequest(map[string]any{
		"traceparent": traceTestParent,
		"baggage":     "x=y", "client_id": "forged", "session_id": "forged", "task_id": "forged",
	})))
	if md.TraceID != traceTestTraceID || md.ClientID != "" || md.SessionID != "" || md.TaskID != "" {
		t.Fatalf("meta identity claims accepted: %+v", md)
	}
}

// The middleware flow the main agent wires in observeMiddleware: adapter
// first, then the entrypoint mark, then Start — the inbound record carries
// the remote trace as its parent and local identity as client_id.
func TestMCPRequestTraceFeedsInboundSpan(t *testing.T) {
	rec := &observeRecorder{}
	ctx := mcpobserve.WithRecorder(context.Background(), rec)
	ctx = mcpobserve.WithMetadata(ctx, mcpobserve.Metadata{ClientID: "auth-caller"})
	ctx = withMCPRequestTrace(ctx, traceRequest(map[string]any{"traceparent": traceTestParent}))
	md := mcpobserve.MetadataFrom(ctx)
	md.Entrypoint = "mcp"
	ctx = mcpobserve.WithMetadata(ctx, md)
	_, span := mcpobserve.Start(ctx, mcpobserve.Call{Direction: "inbound", Server: "pogo", Tool: "agent_task_list"})
	span.End("ok", "")

	c := rec.finals()[0]
	if c.TraceID != traceTestTraceID || c.ParentSpanID != traceTestSpanID || c.ParentID != "" {
		t.Fatalf("inbound parent wrong: %+v", c)
	}
	if c.SpanID == "" || c.SpanID != mcpobserve.SpanIDFor(c.ID) || c.TraceFlags != "01" {
		t.Fatalf("inbound span fields wrong: %+v", c)
	}
	if c.ClientID != "auth-caller" || c.Entrypoint != "mcp" {
		t.Fatalf("identity/entrypoint wrong: %+v", c)
	}
}

// traceCaptureClient records what the run context carries for both local
// metadata and the outbound W3C meta a nested MCP call would emit.
type traceCaptureClient struct {
	model string
	mu    sync.Mutex
	md    mcpobserve.Metadata
	meta  map[string]any
}

func (c *traceCaptureClient) Model() string { return c.model }

func (c *traceCaptureClient) Stream(ctx context.Context, _ string, _ []llm.Message, _ []llm.ToolSchema, _ func(llm.Delta)) (llm.Response, error) {
	c.mu.Lock()
	c.md = mcpobserve.MetadataFrom(ctx)
	c.meta = mcpobserve.TraceMeta(ctx)
	c.mu.Unlock()
	return textTurn("hi"), nil
}

func (c *traceCaptureClient) captured() (mcpobserve.Metadata, map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.md, c.meta
}

// runMeta keeps the observed caller's new trace/client fields while
// SessionID/RunID/OwnerID stay the session's own authoritative values.
func TestSessionStartObservedTraceFields(t *testing.T) {
	client := &traceCaptureClient{model: "k3"}
	mgr := newObserveManager(t, client)
	sess, err := mgr.Create("", "")
	if err != nil {
		t.Fatal(err)
	}
	runID, err := sess.StartObserved("hi", nil, "", mcpobserve.Metadata{
		TraceID: traceTestTraceID, ParentSpanID: traceTestSpanID, TraceFlags: "00",
		TraceState: "congo=t61rcWkgMzE", ClientID: "auth-caller", TaskID: "task-1",
		SessionID: "FORGED", RunID: "FORGED", OwnerID: "FORGED",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitRunDone(t, sess)

	md, meta := client.captured()
	if md.TraceID != traceTestTraceID || md.ParentSpanID != traceTestSpanID || md.TraceFlags != "00" || md.TraceState != "congo=t61rcWkgMzE" {
		t.Fatalf("trace fields not preserved: %+v", md)
	}
	if md.ClientID != "auth-caller" {
		t.Fatalf("client id not preserved: %+v", md)
	}
	if md.SessionID != sess.ID || md.RunID != runID || md.OwnerID != "" {
		t.Fatalf("authoritative ids forged: %+v (run %q)", md, runID)
	}
	// A nested outbound MCP call from this run would continue the inbound
	// trace with the run's own span as parent.
	tp, _ := meta["traceparent"].(string)
	want := "00-" + traceTestTraceID + "-" + traceTestSpanID + "-00"
	if tp != want {
		t.Fatalf("run outbound traceparent = %q, want %q", tp, want)
	}
	if meta["tracestate"] != "congo=t61rcWkgMzE" {
		t.Fatalf("tracestate not propagated: %#v", meta)
	}
}
