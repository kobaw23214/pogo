package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/mcpobserve"
)

// observeRecorder is a mcpobserve.Recorder fake: it collects every snapshot,
// running and completed, for assertion. No Store involvement — the store is
// another worker's surface and these tests pin the instrumentation, not the
// persistence.
type observeRecorder struct {
	mu    sync.Mutex
	calls []mcpobserve.Call
}

func (r *observeRecorder) Record(c mcpobserve.Call) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
}

func (r *observeRecorder) snapshot() []mcpobserve.Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]mcpobserve.Call(nil), r.calls...)
}

// observeCtx builds a context carrying rec and md, the way the web runner
// arms a run and the loop stamps a tool call.
func observeCtx(rec mcpobserve.Recorder, md mcpobserve.Metadata) context.Context {
	ctx := mcpobserve.WithMetadata(context.Background(), md)
	if rec != nil {
		ctx = mcpobserve.WithRecorder(ctx, rec)
	}
	return ctx
}

func observeTool(t *testing.T, timeout time.Duration, caller MCPCaller) *MCPTool {
	t.Helper()
	mt, err := NewMCPTool("srv", mcpSpec("echo", "d", nil), timeout, caller)
	if err != nil {
		t.Fatal(err)
	}
	return mt
}

// finalOf collapses the recorded snapshots of one call id to the last one —
// running first, completed last, same id throughout.
func finalOf(t *testing.T, calls []mcpobserve.Call) mcpobserve.Call {
	t.Helper()
	if len(calls) == 0 {
		t.Fatal("no calls recorded")
	}
	last := calls[len(calls)-1]
	for _, c := range calls {
		if c.ID != last.ID {
			t.Fatalf("one Execute produced several call ids: %q and %q", c.ID, last.ID)
		}
	}
	return last
}

func TestMCPToolObserveSuccess(t *testing.T) {
	rec := &observeRecorder{}
	// Bigger than any inline body threshold: the record must carry metadata
	// only, however large the result is.
	big := strings.Repeat("payload-marker ", 600) // > 4 KiB
	mt := observeTool(t, time.Minute, fakeCaller(t, func(_ context.Context, toolName string, _ json.RawMessage) (*mcp.CallToolResult, error) {
		if toolName != "echo" {
			t.Errorf("caller got tool %q", toolName)
		}
		return textResult(big), nil
	})).WithTransport("streamable")

	md := mcpobserve.Metadata{
		TraceID: "trace-1", SessionID: "sess-1", RunID: "run-1",
		TaskID: "task-1", OwnerID: "owner-1", ToolCallID: "call-9",
	}
	res, err := mt.Execute(observeCtx(rec, md), json.RawMessage(`{"q":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "payload-marker") {
		t.Fatal("execution result must be unchanged by observation")
	}

	calls := rec.snapshot()
	if len(calls) != 2 {
		t.Fatalf("recorded %d snapshots, want running+completed (2)", len(calls))
	}
	if calls[0].Status != "running" || calls[0].FinishedAt != 0 {
		t.Fatalf("first snapshot = %+v, want running with no finish time", calls[0])
	}
	got := finalOf(t, calls)
	if got.Status != "ok" || got.ErrorKind != "" {
		t.Fatalf("final = %+v, want ok", got)
	}
	if got.Direction != "outbound" {
		t.Errorf("Direction = %q, want outbound", got.Direction)
	}
	if got.Server != "srv" || got.Tool != "echo" || got.WireTool != "mcp_srv_echo" {
		t.Errorf("identity = %q/%q/%q, want srv/echo/mcp_srv_echo", got.Server, got.Tool, got.WireTool)
	}
	if got.Transport != "streamable" {
		t.Errorf("Transport = %q, want streamable", got.Transport)
	}
	if got.TraceID != "trace-1" || got.SessionID != "sess-1" || got.RunID != "run-1" ||
		got.TaskID != "task-1" || got.OwnerID != "owner-1" || got.ToolCallID != "call-9" {
		t.Errorf("correlation mismatch: %+v", got)
	}
	if got.StartedAt <= 0 || got.FinishedAt < got.StartedAt || got.DurationMs < 0 {
		t.Fatalf("bad timing: %+v", got)
	}
	// The whole point of the contract: no payload in the record, however big.
	encoded, _ := json.Marshal(calls)
	if strings.Contains(string(encoded), "payload-marker") {
		t.Fatal("result payload leaked into the observation record")
	}
}

func TestMCPToolObserveOutcomes(t *testing.T) {
	t.Run("isError is tool_error, not transport", func(t *testing.T) {
		rec := &observeRecorder{}
		mt := observeTool(t, time.Minute, fakeCaller(t, func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "server says no"}},
				IsError: true,
			}, nil
		}))
		if _, err := mt.Execute(observeCtx(rec, mcpobserve.Metadata{}), nil); err == nil {
			t.Fatal("isError result must surface as an error, unchanged")
		}
		got := finalOf(t, rec.snapshot())
		if got.Status != "error" || got.ErrorKind != "tool_error" {
			t.Fatalf("final = %+v, want error/tool_error", got)
		}
	})

	t.Run("caller failure is transport_error", func(t *testing.T) {
		rec := &observeRecorder{}
		mt := observeTool(t, time.Minute, fakeCaller(t, func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
			return nil, errors.New("session reconnecting")
		}))
		if _, err := mt.Execute(observeCtx(rec, mcpobserve.Metadata{}), nil); err == nil {
			t.Fatal("caller failure must surface")
		}
		got := finalOf(t, rec.snapshot())
		if got.Status != "error" || got.ErrorKind != "transport_error" {
			t.Fatalf("final = %+v, want error/transport_error", got)
		}
		// The monitor now retains a bounded, sanitized actual failure reason.
		if got.ErrorSummary != "session reconnecting" {
			t.Fatalf("missing diagnostic: %+v", got)
		}
	})

	t.Run("nil result is protocol_error, never success", func(t *testing.T) {
		rec := &observeRecorder{}
		mt := observeTool(t, time.Minute, fakeCaller(t, func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
			return nil, nil
		}))
		if _, err := mt.Execute(observeCtx(rec, mcpobserve.Metadata{}), nil); err == nil {
			t.Fatal("nil result with nil error must not read as success")
		}
		got := finalOf(t, rec.snapshot())
		if got.Status != "error" || got.ErrorKind != "protocol_error" {
			t.Fatalf("final = %+v, want error/protocol_error", got)
		}
	})

	t.Run("tool timeout is timeout", func(t *testing.T) {
		rec := &observeRecorder{}
		mt := observeTool(t, 30*time.Millisecond, fakeCaller(t, func(ctx context.Context, _ string, _ json.RawMessage) (*mcp.CallToolResult, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}))
		_, err := mt.Execute(observeCtx(rec, mcpobserve.Metadata{}), nil)
		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("err = %v, want the named timeout", err)
		}
		got := finalOf(t, rec.snapshot())
		if got.Status != "timeout" || got.ErrorKind != "timeout" {
			t.Fatalf("final = %+v, want timeout", got)
		}
	})

	t.Run("parent cancel is cancelled", func(t *testing.T) {
		rec := &observeRecorder{}
		mt := observeTool(t, time.Minute, fakeCaller(t, func(ctx context.Context, _ string, _ json.RawMessage) (*mcp.CallToolResult, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}))
		ctx, cancel := context.WithCancel(observeCtx(rec, mcpobserve.Metadata{}))
		go func() {
			time.Sleep(20 * time.Millisecond)
			cancel()
		}()
		_, err := mt.Execute(ctx, nil)
		if err == nil || !strings.Contains(err.Error(), "aborted") {
			t.Fatalf("err = %v, want the named abort", err)
		}
		got := finalOf(t, rec.snapshot())
		if got.Status != "cancelled" || got.ErrorKind != "cancelled" {
			t.Fatalf("final = %+v, want cancelled", got)
		}
	})
}

func TestMCPToolObserveNestedTraceAndIsolation(t *testing.T) {
	rec := &observeRecorder{}
	mt := observeTool(t, time.Minute, fakeCaller(t, func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
		return textResult("ok"), nil
	}))

	// A parent span (say the inbound submit) owns the trace; the outbound call
	// must continue it and name the parent, not open a new trace.
	parentCtx, parent := mcpobserve.Start(observeCtx(rec, mcpobserve.Metadata{}), mcpobserve.Call{
		Direction: "inbound", Server: "pogo", Tool: "agent_submit_task",
	})
	if _, err := mt.Execute(parentCtx, nil); err != nil {
		t.Fatal(err)
	}
	parent.End("ok", "")

	calls := rec.snapshot()
	if len(calls) != 4 {
		t.Fatalf("recorded %d snapshots, want 2 per call", len(calls))
	}
	child := calls[2]
	if child.Direction != "outbound" {
		t.Fatalf("child direction = %q", child.Direction)
	}
	parentID := calls[0].ID
	if child.TraceID != calls[0].TraceID || child.TraceID == "" {
		t.Fatalf("child trace %q does not continue parent %q", child.TraceID, calls[0].TraceID)
	}
	if child.ParentID != parentID {
		t.Fatalf("child parent = %q, want the inbound call id %q", child.ParentID, parentID)
	}

	// Two sessions executing the same tool concurrently must not contaminate
	// each other's metadata.
	rec2 := &observeRecorder{}
	ctxA := observeCtx(rec2, mcpobserve.Metadata{TraceID: "trace-A", SessionID: "sess-A", ToolCallID: "call-A"})
	ctxB := observeCtx(rec2, mcpobserve.Metadata{TraceID: "trace-B", SessionID: "sess-B", ToolCallID: "call-B"})
	var wg sync.WaitGroup
	for _, ctx := range []context.Context{ctxA, ctxB} {
		wg.Add(1)
		go func(ctx context.Context) {
			defer wg.Done()
			if _, err := mt.Execute(ctx, nil); err != nil {
				t.Error(err)
			}
		}(ctx)
	}
	wg.Wait()
	bySession := map[string]mcpobserve.Call{}
	for _, c := range rec2.snapshot() {
		if c.Status != "ok" {
			continue
		}
		bySession[c.SessionID] = c
	}
	if len(bySession) != 2 {
		t.Fatalf("completed calls by session = %d, want 2: %+v", len(bySession), rec2.snapshot())
	}
	if bySession["sess-A"].TraceID != "trace-A" || bySession["sess-A"].ToolCallID != "call-A" {
		t.Fatalf("sess-A record contaminated: %+v", bySession["sess-A"])
	}
	if bySession["sess-B"].TraceID != "trace-B" || bySession["sess-B"].ToolCallID != "call-B" {
		t.Fatalf("sess-B record contaminated: %+v", bySession["sess-B"])
	}
}

func TestMCPToolObserveNoRecorderUnchanged(t *testing.T) {
	// No recorder on the context: behavior — results, errors, timeouts — is
	// identical to the pre-observation contract, and nothing panics.
	okTool := observeTool(t, time.Minute, fakeCaller(t, func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
		return textResult("fine"), nil
	}))
	res, err := okTool.Execute(context.Background(), nil)
	if err != nil || res.Text != "fine" {
		t.Fatalf("success path changed: %v %q", err, res.Text)
	}

	failTool := observeTool(t, time.Minute, fakeCaller(t, func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
		return nil, errors.New("boom")
	}))
	if _, err := failTool.Execute(context.Background(), nil); err == nil || err.Error() != "boom" {
		t.Fatalf("error path changed: %v", err)
	}

	timeoutTool := observeTool(t, 20*time.Millisecond, fakeCaller(t, func(ctx context.Context, _ string, _ json.RawMessage) (*mcp.CallToolResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	if _, err := timeoutTool.Execute(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout path changed: %v", err)
	}
}
