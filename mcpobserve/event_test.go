package mcpobserve

import (
	"context"
	"strings"
	"sync"
	"testing"
)

type recordingSink struct {
	mu    sync.Mutex
	calls []Call
}

func (r *recordingSink) Record(c Call) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
}

func TestSpanParentAndExactlyOneCompletion(t *testing.T) {
	sink := &recordingSink{}
	ctx := WithRecorder(context.Background(), sink)
	ctx = WithMetadata(ctx, Metadata{SessionID: "session", RunID: "run", OwnerID: "owner"})
	parentCtx, parent := Start(ctx, Call{Direction: "inbound", Tool: "submit"})
	_, child := Start(parentCtx, Call{Direction: "outbound", Server: "upstream", Tool: "search"})
	child.Correlate(Metadata{TaskID: "task", TraceID: "must-not-replace"})
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() { child.End("ok", "") })
	}
	wg.Wait()
	parent.End("ok", "")
	if len(sink.calls) != 4 {
		t.Fatalf("got %d snapshots, want 2 starts and 2 ends", len(sink.calls))
	}
	a, b := sink.calls[0], sink.calls[1]
	if a.ID == b.ID || a.TraceID != b.TraceID || b.ParentID != a.ID || b.RunID != "run" || b.OwnerID != "owner" {
		t.Fatalf("bad correlation: %+v %+v", a, b)
	}
	end := sink.calls[2]
	if end.TaskID != "task" || end.TraceID != a.TraceID || end.StartedAt != b.StartedAt || end.Status != "ok" || end.FinishedAt < end.StartedAt {
		t.Fatalf("bad completion: %+v", end)
	}
}

func TestSpanMetadataIsBoundedAndNoGlobalRecorder(t *testing.T) {
	sink := &recordingSink{}
	ctx := WithRecorder(context.Background(), sink)
	ctx = WithRequestInfo(ctx, RequestInfo{Transport: "sse", RequestID: "request"})
	_, span := Start(ctx, Call{Direction: "inbound", Client: strings.Repeat("a", 2048) + "\n"})
	span.End("unknown", "")
	_, silent := Start(context.Background(), Call{Direction: "outbound", Tool: "not-recorded"})
	silent.End("ok", "")
	if len(sink.calls) != 2 || len(sink.calls[0].Client) > 256 || sink.calls[0].RequestID != "request" || sink.calls[0].Transport != "sse" {
		t.Fatalf("bad capture: %+v", sink.calls)
	}
	if sink.calls[1].Status != "interrupted" {
		t.Fatal("unknown completion became success")
	}
}
