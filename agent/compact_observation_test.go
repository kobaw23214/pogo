package agent

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
)

type observedCompactor struct {
	response llm.Response
	err      error
	purpose  llm.RequestPurpose
	deadline time.Time
	calls    int
}

func (client *observedCompactor) Model() string { return "glm-5.3" }

func (client *observedCompactor) Stream(ctx context.Context, _ string, _ []llm.Message, schemas []llm.ToolSchema, _ func(llm.Delta)) (llm.Response, error) {
	client.calls++
	client.purpose = llm.RequestPurposeFromContext(ctx)
	client.deadline, _ = ctx.Deadline()
	if len(schemas) != 0 {
		return llm.Response{}, errors.New("compaction acquired tools")
	}
	return client.response, client.err
}

func TestCompactFailurePreservesObservationsWithoutCommittingPartialSummary(t *testing.T) {
	deadline := time.Now().Add(time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	observed := llm.Timing{HeadersReceived: true, TTFB: time.Millisecond, TTFT: 2 * time.Millisecond, LastContent: 30 * time.Millisecond, ThinkingBytes: 15, Total: 50 * time.Millisecond}
	profile := &llm.RequestProfile{Purpose: llm.PurposeCompaction, Applied: true, Reason: "compaction_reasoning_default", ReasoningEffort: "low"}
	wantErr := errors.New("bounded request failed")
	client := &observedCompactor{response: llm.Response{Timing: observed, RequestProfile: profile, Usage: llm.Usage{Input: 17, Output: 3}}, err: wantErr}
	a := compacting(t, client)
	before := a.Messages()
	commits := 0
	result, err := a.CompactWithCommit(ctx, 0, func([]llm.Message) error { commits++; return nil })
	if !errors.Is(err, wantErr) || commits != 0 || client.calls != 1 {
		t.Fatalf("failed summary committed or retried: err=%v commits=%d calls=%d", err, commits, client.calls)
	}
	if client.purpose != llm.PurposeCompaction || client.deadline != deadline {
		t.Fatalf("purpose bypassed wrapper or changed deadline: purpose=%q deadline=%v", client.purpose, client.deadline)
	}
	if !reflect.DeepEqual(a.Messages(), before) || result.Before != result.After || result.Summary != "" {
		t.Fatal("failed call replaced the original transcript")
	}
	if result.Timing != observed || result.RequestProfile != profile || result.Usage != client.response.Usage {
		t.Fatalf("failed call discarded observations: %+v", result)
	}
	if got := a.Usage(); got.Input != 17 || got.Output != 3 {
		t.Fatalf("failed call did not charge known provider usage: %+v", got)
	}
}

func TestCompactSuccessfulProfileRemainsOutsideContinuation(t *testing.T) {
	client := &observedCompactor{response: llm.Response{
		Message: llm.UserText("keep the exact constraints and continue"), StopReason: llm.StopEndTurn,
		Timing:         llm.Timing{HeadersReceived: true, FirstText: time.Millisecond, TextBytes: 44},
		RequestProfile: &llm.RequestProfile{Purpose: llm.PurposeCompaction, Applied: true, ReasoningEffort: "low"},
	}}
	a := compacting(t, client)
	result, err := a.Compact(context.Background())
	if err != nil || result.StopReason != llm.StopEndTurn || result.Timing.FirstText != time.Millisecond || result.RequestProfile == nil {
		t.Fatalf("successful compaction observations lost: result=%+v err=%v", result, err)
	}
	if text := a.Messages()[0].Text(); text != continuation(pinRequest(history(4, 2_000)), client.response.Message.Text()) {
		t.Fatal("request profile or measurements changed the model-facing continuation")
	}
}
