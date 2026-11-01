package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

const answerReviewOriginal = "报告有 10 个提交。"
const answerReviewThought = "正在核对已有来源。保留证据边界。"

type answerReviewClient struct {
	mode    string
	calls   int
	release <-chan struct{}
}

func (*answerReviewClient) Model() string { return "review-fixture" }
func (c *answerReviewClient) Stream(ctx context.Context, _ string, _ []llm.Message, schemas []llm.ToolSchema, delta func(llm.Delta)) (llm.Response, error) {
	c.calls++
	switch c.calls {
	case 1:
		return toolCalls("read"), nil
	case 2:
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
			{Type: llm.BlockThinking, Text: "原始回答的思考。"},
			{Type: llm.BlockText, Text: answerReviewOriginal},
		}}, StopReason: llm.StopEndTurn}, nil
	case 3:
		if len(schemas) != 0 {
			return llm.Response{}, fmt.Errorf("answer review received executable tools")
		}
		if delta != nil {
			delta(llm.Delta{Kind: llm.DeltaThinking, Text: "正在核对已有来源。"})
			delta(llm.Delta{Kind: llm.DeltaThinking, Text: "保留证据边界。"})
			delta(llm.Delta{Kind: llm.DeltaText, Text: "尚未校验的回答草稿"})
		}
		if c.release != nil {
			select {
			case <-c.release:
			case <-ctx.Done():
				return llm.Response{}, ctx.Err()
			}
		}
		if c.mode == "timeout" || c.mode == "cancel" {
			<-ctx.Done()
			return llm.Response{}, ctx.Err()
		}
		if c.mode == "tool" {
			out := toolCalls("write")
			out.Message.Content = append([]llm.Block{{Type: llm.BlockThinking, Text: answerReviewThought}}, out.Message.Content...)
			return out, nil
		}
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
			{Type: llm.BlockThinking, Text: answerReviewThought},
			{Type: llm.BlockText, Text: answerReviewOriginal + " [1](#cite:S1)"},
		}}, StopReason: llm.StopEndTurn}, nil
	default:
		return llm.Response{}, fmt.Errorf("answer review repeated unexpectedly: %d calls", c.calls)
	}
}

func answerReviewThinking(message llm.Message) string {
	var out strings.Builder
	for _, block := range message.Content {
		if block.Type == llm.BlockThinking {
			out.WriteString(block.Text)
		}
	}
	return out.String()
}

func nextAnswerReviewEvent(t *testing.T, events <-chan Event) Event {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("answer review stream ended before expected event")
		}
		return event
	case <-time.After(3 * time.Second):
		t.Fatal("answer review stopped reporting progress")
		return Event{}
	}
}

func TestAnswerReviewStreamsAllThinkingBeforeCommit(t *testing.T) {
	release := make(chan struct{})
	client := &answerReviewClient{release: release}
	a := New(Config{Client: client, Registry: tools.NewRegistry(citationReadTool()), Citations: true})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream := a.Run(ctx, "总结报告")
	var events []Event
	var thought strings.Builder
	var running bool
	for thought.String() != answerReviewThought {
		event := nextAnswerReviewEvent(t, stream)
		events = append(events, event)
		if event.Kind == EventAnswerReview && event.AnswerReview != nil {
			running = event.AnswerReview.Kind == "citation" && event.AnswerReview.State == "running" && event.AnswerReview.StartedAt > 0
		}
		if event.Kind == EventThinkingDelta {
			thought.WriteString(event.Text)
		}
		if event.Kind == EventTextDelta && strings.Contains(event.Text, "尚未校验") {
			t.Fatal("unvalidated review answer replaced the completed answer")
		}
	}
	if !running {
		t.Fatal("model thinking arrived without an active review status")
	}
	// The provider is still blocked: the complete thinking stream must have
	// reached the UI before the response is accepted or rejected.
	close(release)
	for event := range stream {
		events = append(events, event)
	}
	stages := eventsOf(events, EventAnswerReview)
	if len(stages) != 2 || stages[1].AnswerReview == nil || stages[1].AnswerReview.State != "completed" || stages[1].AnswerReview.FinishedAt < stages[0].AnswerReview.StartedAt {
		t.Fatalf("review did not settle once: %+v", stages)
	}
	final := a.Messages()[len(a.Messages())-1]
	if final.Text() != answerReviewOriginal+" [1](#cite:S1)" || answerReviewThinking(final) != answerReviewThought || final.AnswerReview == nil || final.AnswerReview.State != "completed" {
		t.Fatalf("accepted answer lost thoughts or terminal status: %+v", final)
	}
	for _, event := range events {
		if event.Kind == EventTextDelta && strings.Contains(event.Text, "尚未校验") {
			t.Fatal("review draft escaped after completion")
		}
	}
}

func TestAnswerReviewRejectedToolsKeepOriginalAnswerAndReviewThinking(t *testing.T) {
	client := &answerReviewClient{mode: "tool"}
	write := &fakeTool{name: "write"}
	a := New(Config{Client: client, Registry: tools.NewRegistry(citationReadTool(), write), Citations: true})
	events := citeRun(t, a, "总结报告")
	final := a.Messages()[len(a.Messages())-1]
	if final.Text() != answerReviewOriginal || answerReviewThinking(final) != answerReviewThought || len(write.callArgs()) != 0 || len(final.ToolCalls()) != 0 {
		t.Fatalf("fallback changed answer, duplicated original thought or executed a tool: %+v", final)
	}
	stages := eventsOf(events, EventAnswerReview)
	if len(stages) != 2 || stages[1].AnswerReview == nil || stages[1].AnswerReview.State != "fallback" || final.AnswerReview == nil || final.AnswerReview.State != "fallback" {
		t.Fatalf("failed rewrite left a spinning review state: %+v", stages)
	}
}

func TestAnswerReviewShortTimeoutPreservesCompletedTaskAndStreamedThoughts(t *testing.T) {
	client := &answerReviewClient{mode: "timeout"}
	a := New(Config{Client: client, Registry: tools.NewRegistry(citationReadTool()), Citations: true, AnswerReviewTimeout: 20 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var events []Event
	for event := range a.Run(ctx, "总结报告") {
		events = append(events, event)
	}
	final := a.Messages()[len(a.Messages())-1]
	if ctx.Err() != nil || final.Text() != answerReviewOriginal || answerReviewThinking(final) != answerReviewThought {
		t.Fatalf("optional review timeout cancelled task or erased streamed thoughts: %+v ctx=%v", final, ctx.Err())
	}
	stages := eventsOf(events, EventAnswerReview)
	if len(stages) != 2 || stages[1].AnswerReview == nil || stages[1].AnswerReview.State != "fallback" || stages[1].AnswerReview.Reason != "timeout" {
		t.Fatalf("timeout has no settled UI status: %+v", stages)
	}
	ends := eventsOf(events, EventAgentEnd)
	if len(ends) != 1 || ends[0].Err != nil || ends[0].StopReason != llm.StopEndTurn {
		t.Fatalf("review timeout failed an already-completed task: %+v", ends)
	}
}

func TestAnswerReviewCancellationSettlesStatusAndKeepsPartialThoughts(t *testing.T) {
	client := &answerReviewClient{mode: "cancel"}
	a := New(Config{Client: client, Registry: tools.NewRegistry(citationReadTool()), Citations: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := a.Run(ctx, "总结报告")
	var events []Event
	for {
		event := nextAnswerReviewEvent(t, stream)
		events = append(events, event)
		if event.Kind == EventThinkingDelta {
			cancel()
			break
		}
	}
	for event := range stream {
		events = append(events, event)
	}
	stages := eventsOf(events, EventAnswerReview)
	if len(stages) != 2 || stages[1].AnswerReview == nil || stages[1].AnswerReview.State != "cancelled" {
		t.Fatalf("cancelled rewrite left a running UI state: %+v", stages)
	}
	final := a.Messages()[len(a.Messages())-1]
	if final.Text() != answerReviewOriginal || !strings.HasPrefix(answerReviewThinking(final), "正在核对已有来源。") {
		t.Fatalf("cancellation erased the original answer or the already-streamed thought: %+v", final)
	}
}

func TestAnswerReviewQuoteRepairStreamsThinkingWithoutChangingOtherValues(t *testing.T) {
	registry, history, id := nativeCitationCheckpoint(t, "record=1 note=\"审核需要逐项签字\"\n")
	wrong, exact := quoteReviewAnswer("审核需要签字", 1), quoteReviewAnswer("审核需要逐项签字", 1)
	client := &citationClient{fakeClient: fakeClient{responses: []llm.Response{
		evidenceReviewRead(id, "quote-read", map[string]any{"view": "search", "pattern": "record=1"}),
		citationAnswer(wrong), citationAnswer(exact),
	}, deltas: func(n int, emit func(llm.Delta)) {
		if n == 3 {
			emit(llm.Delta{Kind: llm.DeltaThinking, Text: answerReviewThought})
			emit(llm.Delta{Kind: llm.DeltaText, Text: "尚未验证的引文草稿"})
		}
	}}}
	a := New(Config{Client: client, Registry: registry, Citations: true})
	a.SetMessages(history)
	events := citeRun(t, a, "Return the observation with its verbatim evidence.")
	stages := eventsOf(events, EventAnswerReview)
	if len(stages) != 2 || stages[0].AnswerReview == nil || stages[0].AnswerReview.Kind != "quote" || stages[1].AnswerReview.State != "completed" {
		t.Fatalf("quote repair has no matching running/settled UI status: %+v", stages)
	}
	thoughts := eventsOf(events, EventThinkingDelta)
	if len(thoughts) != 1 || thoughts[0].Text != answerReviewThought {
		t.Fatalf("quote repair thinking was hidden or duplicated: %+v", thoughts)
	}
	if len(eventsOf(events, EventTextDelta)) != 0 {
		t.Fatal("unvalidated quote draft escaped to the answer")
	}
	final := a.Messages()[len(a.Messages())-1]
	if final.Text() != exact || answerReviewThinking(final) != answerReviewThought || final.AnswerReview == nil || final.AnswerReview.Kind != "quote" || len(eventsOf(events, EventToolEnd)) != 1 {
		t.Fatalf("quote repair lost thinking, changed values or repeated evidence reads: %+v", final)
	}
}
