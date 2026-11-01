package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func truncatedOutput() llm.Response {
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
		{Type: llm.BlockThinking, Text: "unfinished planning"},
	}}, StopReason: llm.StopMaxTokens, Usage: llm.Usage{Input: 11, Output: 7, Reasoning: 6}}
}

func continuationNotices(events []Event) []Event {
	var result []Event
	for _, event := range events {
		if event.Kind == EventSteer && strings.Contains(event.Text, "[pogo] 上一段输出达到本次上限") {
			result = append(result, event)
		}
	}
	return result
}

func TestOutputContinuationIsOptInAndOneAttempt(t *testing.T) {
	for _, attempts := range []int{-1, 0, 1, 9} {
		t.Run(fmt.Sprintf("attempts=%d", attempts), func(t *testing.T) {
			client := &fakeClient{responses: []llm.Response{truncatedOutput(), truncatedOutput(), final("must not be reached")}}
			a := New(Config{Client: client, Registry: tools.NewRegistry(), Continuation: ContinuationConfig{MaxAttempts: attempts}})
			events := drain(a.Run(context.Background(), "write the script"))
			calls, notices := 1, 0
			if attempts > 0 {
				calls, notices = 2, 1
			}
			if client.callCount() != calls || len(continuationNotices(events)) != notices || endOf(t, events).EndReason != EndMaxTokens {
				t.Fatalf("calls=%d notices=%d end=%s", client.callCount(), len(continuationNotices(events)), endOf(t, events).EndReason)
			}
		})
	}
}

func TestOutputContinuationKeepsHistoryAndUsage(t *testing.T) {
	complete := final("script is complete")
	complete.Usage = llm.Usage{Input: 20, Output: 5}
	client := &fakeClient{responses: []llm.Response{truncatedOutput(), complete}}
	a := New(Config{Client: client, Registry: tools.NewRegistry(), Continuation: ContinuationConfig{MaxAttempts: 1}})
	events := drain(a.Run(context.Background(), "write the script"))
	if endOf(t, events).EndReason != EndCompleted || client.callCount() != 2 || len(continuationNotices(events)) != 1 {
		t.Fatalf("did not complete one bounded continuation: %+v", endOf(t, events))
	}
	if got := a.Usage(); got.Input != 31 || got.Output != 12 || got.Reasoning != 6 {
		t.Fatalf("continuation reset or double-counted usage: %+v", got)
	}
	seen := client.lastSeen()
	if len(seen) != 3 || seen[0].Text() != "write the script" || len(seen[1].Content) != 1 || seen[1].Content[0].Type != llm.BlockThinking || !strings.Contains(seen[2].Text(), "不要重做") {
		t.Fatalf("continuation did not preserve the settled transcript: %+v", seen)
	}
	// The notice is a normal persisted steer between assistant messages.
	var kinds []EventKind
	for _, event := range events {
		if event.Kind == EventMessage || event.Kind == EventSteer {
			kinds = append(kinds, event.Kind)
		}
	}
	if len(kinds) != 3 || kinds[0] != EventMessage || kinds[1] != EventSteer || kinds[2] != EventMessage {
		t.Fatalf("settlement order = %v", kinds)
	}
}

func TestOutputContinuationDoesNotReplayCompletedTools(t *testing.T) {
	write := &fakeTool{name: "write"}
	client := &fakeClient{responses: []llm.Response{toolCalls("write"), truncatedOutput(), final("done")}}
	a := New(Config{Client: client, Registry: tools.NewRegistry(write), Continuation: ContinuationConfig{MaxAttempts: 1}})
	events := drain(a.Run(context.Background(), "write once"))
	if endOf(t, events).EndReason != EndCompleted || len(write.callArgs()) != 1 || client.callCount() != 3 {
		t.Fatalf("completed tool replayed: calls=%v model=%d end=%s", write.callArgs(), client.callCount(), endOf(t, events).EndReason)
	}
	var results int
	for _, message := range client.lastSeen() {
		for _, block := range message.Content {
			if block.Type == llm.BlockToolResult && !block.IsError {
				results++
			}
		}
	}
	if results != 1 {
		t.Fatalf("settled successful results = %d", results)
	}
}

func TestOutputContinuationSettlesTruncatedCallsBeforeOneCompletion(t *testing.T) {
	for _, secondTruncated := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "still-truncated"}[secondTruncated], func(t *testing.T) {
			write := &fakeTool{name: "write"}
			cut := toolCalls("write")
			cut.StopReason = llm.StopMaxTokens
			responses := []llm.Response{cut, toolCalls("write"), final("done")}
			wantCalls, wantWrites, wantEnd := 3, 1, EndCompleted
			if secondTruncated {
				responses[1] = cut
				wantCalls, wantWrites, wantEnd = 2, 0, EndMaxTokens
			}
			client := &fakeClient{responses: responses}
			a := New(Config{Client: client, Registry: tools.NewRegistry(write), Continuation: ContinuationConfig{MaxAttempts: 1}})
			events := drain(a.Run(context.Background(), "write once"))
			if client.callCount() != wantCalls || len(write.callArgs()) != wantWrites || endOf(t, events).EndReason != wantEnd {
				t.Fatalf("model=%d writes=%d end=%s", client.callCount(), len(write.callArgs()), endOf(t, events).EndReason)
			}
			var rejected, successful, pending int
			for _, message := range a.Messages() {
				for _, block := range message.Content {
					switch block.Type {
					case llm.BlockToolUse:
						pending++
					case llm.BlockToolResult:
						pending--
						if block.IsError && strings.Contains(block.Text, "not executed") {
							rejected++
						} else if !block.IsError {
							successful++
						}
					}
				}
			}
			wantRejected := 1
			if secondTruncated {
				wantRejected = 2
			}
			if pending != 0 || rejected != wantRejected || successful != wantWrites || len(continuationNotices(events)) != 1 {
				t.Fatalf("unpaired or executed truncated calls: pending=%d rejected=%d success=%d", pending, rejected, successful)
			}
			persister := NewTurnPersister(func([]llm.Message) error { return nil }, nil)
			persister.Add(llm.UserText("write once"))
			for _, event := range events {
				persister.OnEvent(event)
			}
			if persister.Failed() || persister.Persisted() != len(a.Messages()) {
				t.Fatalf("continuation crossed a persistence boundary: failed=%v stored=%d history=%d", persister.Failed(), persister.Persisted(), len(a.Messages()))
			}
		})
	}
}

func TestOutputContinuationHonorsRunLimitsAndCancellation(t *testing.T) {
	for _, name := range []string{"turn", "token", "cost", "time", "cancel"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := &fakeClient{responses: []llm.Response{truncatedOutput(), final("not allowed")}}
			cfg := Config{Client: client, Registry: tools.NewRegistry(), Continuation: ContinuationConfig{MaxAttempts: 1}}
			var a *Agent
			var want EndReason
			switch name {
			case "turn":
				cfg.MaxTurns, want = 1, EndTurnLimit
			case "token":
				cfg.TokenBudget, want = 18, EndTokenBudget
			case "cost":
				cfg.CostBudget, cfg.Price, want = 1, &llm.Price{Input: 1_000_000}, EndCostBudget
			case "time":
				cfg.TimeBudget, want = time.Hour, EndTimeBudget
				// Move the existing run clock after the request starts; no new
				// timeout or fresh run may erase this elapsed allowance.
				client.onStream = func(int) { a.runStartTime = time.Now().Add(-2 * time.Hour) }
			case "cancel":
				want = EndAborted
				client.onStream = func(int) { cancel() }
			}
			a = New(cfg)
			events := drain(a.Run(ctx, "write"))
			if client.callCount() != 1 || len(continuationNotices(events)) != 0 || endOf(t, events).EndReason != want {
				t.Fatalf("limit bypassed: model=%d notices=%d end=%s want=%s", client.callCount(), len(continuationNotices(events)), endOf(t, events).EndReason, want)
			}
		})
	}
}

type continuationFailureClient struct {
	err   error
	calls int
}

func (*continuationFailureClient) Model() string { return "failure" }
func (c *continuationFailureClient) Stream(_ context.Context, _ string, _ []llm.Message, _ []llm.ToolSchema, emit func(llm.Delta)) (llm.Response, error) {
	c.calls++
	if emit != nil {
		emit(llm.Delta{Kind: llm.DeltaThinking, Text: "partial output"})
		emit(llm.Delta{Kind: llm.DeltaToolCallStart, ToolName: "write", ToolID: "partial-call"})
	}
	return llm.Response{}, c.err
}

func TestOutputContinuationNeverRetriesTransportOrAPIError(t *testing.T) {
	for _, err := range []error{context.DeadlineExceeded, llm.ErrIncompleteStream, &llm.APIError{Status: 400}, &llm.APIError{Status: 401}, errors.New("unexpected transport failure")} {
		t.Run(err.Error(), func(t *testing.T) {
			write := &fakeTool{name: "write"}
			client := &continuationFailureClient{err: err}
			a := New(Config{Client: client, Registry: tools.NewRegistry(write), Continuation: ContinuationConfig{MaxAttempts: 1}})
			events := drain(a.Run(context.Background(), "write"))
			if client.calls != 1 || len(write.callArgs()) != 0 || len(continuationNotices(events)) != 0 || endOf(t, events).EndReason != EndTransportError {
				t.Fatalf("unsafe replay: calls=%d tools=%v end=%s", client.calls, write.callArgs(), endOf(t, events).EndReason)
			}
			if len(a.Messages()) != 1 {
				t.Fatalf("incomplete preview entered committed history: %+v", a.Messages())
			}
		})
	}
}

func TestOutputContinuationAllowanceResetsOnlyForNewRun(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{truncatedOutput(), final("one"), truncatedOutput(), final("two")}}
	a := New(Config{Client: client, Registry: tools.NewRegistry(), Continuation: ContinuationConfig{MaxAttempts: 1}})
	for _, text := range []string{"one", "two"} {
		events := drain(a.Run(context.Background(), text))
		if len(continuationNotices(events)) != 1 || endOf(t, events).EndReason != EndCompleted {
			t.Fatalf("explicit new run did not get its own bounded completion: %+v", endOf(t, events))
		}
	}
	if client.callCount() != 4 {
		t.Fatalf("model requests = %d", client.callCount())
	}
}

func TestOutputContinuationDoesNotExtendOptionalCitationRepair(t *testing.T) {
	repair := citationAnswer("项目有")
	repair.StopReason = llm.StopMaxTokens
	client := &citationClient{fakeClient: fakeClient{responses: []llm.Response{
		toolCalls("read"), citationAnswer("项目有 10 个提交。"), repair, final("must not be reached"),
	}}}
	a := New(Config{Client: client, Registry: tools.NewRegistry(citationReadTool()), Citations: true,
		Continuation: ContinuationConfig{MaxAttempts: 1}})
	events := citeRun(t, a, "总结项目")
	if client.callCount() != 3 || len(continuationNotices(events)) != 0 || endOf(t, events).EndReason != EndCompleted {
		t.Fatalf("optional rewrite gained an extra allowance: model=%d notices=%d end=%s", client.callCount(), len(continuationNotices(events)), endOf(t, events).EndReason)
	}
}

func TestOutputContinuationDoesNotExtendCompletedAnswer(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{final("done"), final("must not be reached")}}
	a := New(Config{Client: client, Registry: tools.NewRegistry(), Continuation: ContinuationConfig{MaxAttempts: 1}})
	events := drain(a.Run(context.Background(), "go"))
	if client.callCount() != 1 || len(continuationNotices(events)) != 0 || endOf(t, events).EndReason != EndCompleted {
		t.Fatalf("completed answer was resumed: calls=%d end=%s", client.callCount(), endOf(t, events).EndReason)
	}
}
