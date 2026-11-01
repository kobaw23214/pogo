package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/tools"
)

// The loop stamps each tool call's provider-side id onto the observation
// metadata before Execute, on a per-call derived context: two concurrent calls
// in one batch must each see their own id, and the run's shared context must
// stay untouched.
func TestObserveToolCallIDInjection(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]mcpobserve.Metadata{}
	capture := func(name string) *fakeTool {
		return &fakeTool{name: name, run: func(ctx context.Context, _ json.RawMessage) (tools.Result, error) {
			// A short sleep overlaps the two calls, so a shared-context bug
			// (both calls reading whichever id was stamped last) shows up.
			time.Sleep(10 * time.Millisecond)
			mu.Lock()
			seen[name] = mcpobserve.MetadataFrom(ctx)
			mu.Unlock()
			return tools.Result{Text: "ok:" + name}, nil
		}}
	}
	slow, fast := capture("slow"), capture("fast")
	c := &fakeClient{responses: []llm.Response{toolCalls("slow", "fast")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(slow, fast)})

	base := mcpobserve.WithMetadata(context.Background(), mcpobserve.Metadata{
		TraceID: "trace-run", SessionID: "sess-1", RunID: "run-1", TaskID: "task-1",
	})
	drain(a.Run(base, "go"))

	if len(seen) != 2 {
		t.Fatalf("captured %d calls, want 2", len(seen))
	}
	// toolCalls numbers the blocks in order: slow is call0, fast call1.
	for name, wantID := range map[string]string{"slow": "call0", "fast": "call1"} {
		md, ok := seen[name]
		if !ok {
			t.Fatalf("%s never ran", name)
		}
		if md.ToolCallID != wantID {
			t.Errorf("%s saw ToolCallID %q, want %q", name, md.ToolCallID, wantID)
		}
		// The run's correlation survives the per-call stamp.
		if md.TraceID != "trace-run" || md.SessionID != "sess-1" || md.RunID != "run-1" || md.TaskID != "task-1" {
			t.Errorf("%s lost run metadata: %+v", name, md)
		}
	}
	// The caller's context was never rewritten.
	if md := mcpobserve.MetadataFrom(base); md.ToolCallID != "" {
		t.Fatalf("shared run context mutated: %+v", md)
	}
}

// Without any metadata on the run context the stamp still happens — an MCP
// wrapper only needs the id — and nothing else changes.
func TestObserveToolCallIDWithoutMetadata(t *testing.T) {
	var got mcpobserve.Metadata
	tool := &fakeTool{name: "probe", run: func(ctx context.Context, _ json.RawMessage) (tools.Result, error) {
		got = mcpobserve.MetadataFrom(ctx)
		return tools.Result{Text: "ok"}, nil
	}}
	c := &fakeClient{responses: []llm.Response{toolCalls("probe")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool)})
	drain(a.Run(context.Background(), "go"))
	if got.ToolCallID != "call0" {
		t.Fatalf("ToolCallID = %q, want call0", got.ToolCallID)
	}
	if got.TraceID != "" || got.SessionID != "" {
		t.Fatalf("metadata appeared from nowhere: %+v", got)
	}
}
