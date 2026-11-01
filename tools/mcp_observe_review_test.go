package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/mcpobserve"
)

func TestMCPObservePanicFinalizesUnknownOutcome(t *testing.T) {
	rec := &observeRecorder{}
	mt := observeTool(t, time.Minute, func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
		panic("synthetic private panic")
	})
	func() {
		defer func() {
			if recover() == nil {
				t.Error("instrumentation swallowed original panic")
			}
		}()
		mt.Execute(observeCtx(rec, mcpobserve.Metadata{}), json.RawMessage(`{}`))
	}()
	c := finalOf(t, rec.snapshot())
	if c.Status != "interrupted" || c.ErrorKind != "unknown_outcome" {
		t.Fatalf("panic left a running/success event: %+v", c)
	}
}

func TestMCPObserveReturnedContextError(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status string
	}{{context.Canceled, "cancelled"}, {context.DeadlineExceeded, "timeout"}} {
		t.Run(tc.status, func(t *testing.T) {
			rec := &observeRecorder{}
			mt := observeTool(t, time.Minute, func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) { return nil, tc.err })
			_, err := mt.Execute(observeCtx(rec, mcpobserve.Metadata{}), json.RawMessage(`{}`))
			if err != tc.err {
				t.Fatalf("caller error changed: %v", err)
			}
			if c := finalOf(t, rec.snapshot()); c.Status != tc.status {
				t.Fatalf("outcome: %+v", c)
			}
		})
	}
}
