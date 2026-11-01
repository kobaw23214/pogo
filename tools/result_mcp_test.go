package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestManagedMCPFailurePreservesRecoverablePayloadAndBoundedError(t *testing.T) {
	full := strings.Repeat("remote diagnostic\n", 10000) + "EXACT_END_OF_ERROR"
	cause := errors.New(full)
	registry := New(Options{Cwd: t.TempDir(), SpillDir: t.TempDir(), ResultScope: t.Name()})
	tool := observeTool(t, time.Second, func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
		return nil, cause
	}).withManagedResults()
	result, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if !errors.Is(err, cause) || len(err.Error()) > 4096 {
		t.Fatalf("error cause lost or marker unbounded: len=%d, same=%t", len(err.Error()), errors.Is(err, cause))
	}
	prepared := registry.PrepareResult(context.Background(), tool.Name(), result, err)
	if !validResultID(prepared.ResultID) || len(prepared.Text) > MaxBytes {
		t.Fatalf("failure cannot be recovered or preview unbounded: ID=%q bytes=%d", prepared.ResultID, len(prepared.Text))
	}
	stored, loadErr := registry.results.Load(context.Background(), prepared.ResultID)
	if loadErr != nil || stored.Text != full || !stored.IsError || !stored.TextComplete {
		t.Fatalf("original failure payload lost: %v, stored bytes=%d", loadErr, len(stored.Text))
	}
}

func TestManagedMCPContextSentinelsRetainIdentity(t *testing.T) {
	for _, want := range []error{context.Canceled, context.DeadlineExceeded} {
		tool := observeTool(t, time.Second, func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
			return nil, want
		}).withManagedResults()
		if _, got := tool.Execute(context.Background(), json.RawMessage(`{}`)); got != want {
			t.Fatalf("context sentinel changed: got=%v want=%v", got, want)
		}
	}
}
