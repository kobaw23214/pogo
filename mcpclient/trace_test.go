package mcpclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/mcpobserve"
)

// metaCapture records the _meta map each tools/call arrives with.
type metaCapture struct {
	mu    sync.Mutex
	metas []map[string]any
}

func (c *metaCapture) add(m map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.metas = append(c.metas, m)
}

func (c *metaCapture) last() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.metas[len(c.metas)-1]
}

// spanRecorder captures the outbound observation spans MCPTool.Execute opens.
type spanRecorder struct {
	mu    sync.Mutex
	calls []mcpobserve.Call
}

func (r *spanRecorder) Record(c mcpobserve.Call) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
}

func (r *spanRecorder) first() mcpobserve.Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[0]
}

const (
	traceTraceID = "0123456789abcdef0123456789abcdef"
	traceSpanID  = "fedcba9876543210"
)

func tracedContext(rec mcpobserve.Recorder) context.Context {
	ctx := mcpobserve.WithMetadata(context.Background(), mcpobserve.Metadata{
		TraceID: traceTraceID, ParentSpanID: traceSpanID,
		TraceFlags: "01", TraceState: "congo=t61rcWkgMzE",
	})
	if rec != nil {
		ctx = mcpobserve.WithRecorder(ctx, rec)
	}
	return ctx
}

// Both outbound call paths — the catalog-bound callBound and the unbound
// conn.call — inject W3C trace context into params._meta; a context without
// an active parent span leaves _meta untouched.
func TestOutboundCallToolTraceMeta(t *testing.T) {
	cap := &metaCapture{}
	srv := mcp.NewServer(&mcp.Implementation{Name: "trace-fixture"}, nil)
	srv.AddTool(&mcp.Tool{Name: "echo", Description: "d", InputSchema: map[string]any{"type": "object"}},
		func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			cap.add(req.Params.GetMeta())
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
		})
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(ts.Close)

	p := newPool(t)
	p.Reconcile(context.Background(), Config{Servers: map[string]*ServerConfig{"fx": {URL: ts.URL, Required: true}}})
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.WaitReady(waitCtx); err != nil {
		t.Fatal(err)
	}

	// Catalog-bound path: callBound through the projected tool's Execute.
	// Execute opens its own outbound span first, so the wire parent is that
	// span, itself a child of the caller's span carried in the context.
	spans := &spanRecorder{}
	tool := projectedTool(t, p, "mcp_fx_echo")
	if _, err := tool.Execute(tracedContext(spans), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	toolSpan := spans.first()
	if toolSpan.TraceID != traceTraceID || toolSpan.ParentSpanID != traceSpanID || toolSpan.SpanID == "" {
		t.Fatalf("tool span chain wrong: %+v", toolSpan)
	}
	meta := cap.last()
	if got, want := meta["traceparent"], "00-"+traceTraceID+"-"+toolSpan.SpanID+"-01"; got != want {
		t.Fatalf("callBound traceparent = %#v", meta)
	}
	if meta["tracestate"] != "congo=t61rcWkgMzE" {
		t.Fatalf("callBound tracestate = %#v", meta)
	}

	// Unbound path: conn.call against the live session.
	c := p.conns["fx"]
	if c == nil {
		t.Fatal("conn missing")
	}
	if _, err := c.call(tracedContext(nil), "echo", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if got := cap.last()["traceparent"]; got != "00-"+traceTraceID+"-"+traceSpanID+"-01" {
		t.Fatalf("conn.call traceparent = %v", got)
	}

	// No active parent span: the bare conn.call path leaves _meta untouched.
	if _, err := c.call(context.Background(), "echo", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if meta := cap.last(); len(meta) != 0 {
		t.Fatalf("untraced call carried meta: %#v", meta)
	}
	// The bound path always has a parent: Execute opens its own span even on
	// an untraced context, so the wire sees a FRESH trace, never a recycled
	// one, and no tracestate it was never handed.
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	meta = cap.last()
	tp, _ := meta["traceparent"].(string)
	if parts := strings.Split(tp, "-"); len(parts) != 4 || parts[1] == traceTraceID {
		t.Fatalf("untraced bound call traceparent = %q, want fresh trace", tp)
	}
	if _, ok := meta["tracestate"]; ok {
		t.Fatalf("untraced bound call carried tracestate: %#v", meta)
	}

	// Untrusted trace metadata must not leak session/run/task identity.
	if _, err := c.call(tracedContext(nil), "echo", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	for key := range cap.last() {
		if key != "traceparent" && key != "tracestate" {
			t.Fatalf("unexpected meta key %q", key)
		}
	}
}
