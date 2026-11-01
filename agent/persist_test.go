package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

// collectPersister runs a fake session, feeding every event to a persister
// whose sink records instead of writing, and returns what it "persisted".
func collectPersister(t *testing.T, a *Agent, prompt string) []llm.Message {
	t.Helper()
	var disk []llm.Message
	p := NewTurnPersister(func(ms []llm.Message) error {
		disk = append(disk, ms...)
		return nil
	}, func(err error) { t.Errorf("append failed: %v", err) })
	p.Add(llm.UserText(prompt))
	drain(tap(a.Run(context.Background(), prompt), p.OnEvent))
	return disk
}

// tap is the test-local copy of the CLI's tapEvents: f sees each event first.
func tap(in <-chan Event, f func(Event)) <-chan Event {
	out := make(chan Event)
	go func() {
		defer close(out)
		for e := range in {
			f(e)
			out <- e
		}
	}()
	return out
}

// The whole point, stated as an invariant: prompt plus the events equals the
// transcript, message for message, in order — including a steered message and
// the final answer.
func TestTurnPersisterReproducesTheRun(t *testing.T) {
	tool := &fakeTool{name: "t"}
	c := &fakeClient{responses: []llm.Response{
		toolCalls("t"),
		toolCalls("t"),
		{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "done"}}},
			StopReason: llm.StopEndTurn},
	}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool), MaxTurns: 10, SoftTurns: 1})

	disk := collectPersister(t, a, "go")

	if !reflect.DeepEqual(disk, a.Messages()) {
		t.Fatalf("persisted != transcript:\npersisted:  %v\ntranscript: %v", rolesTexts(disk), rolesTexts(a.Messages()))
	}
	// The soft cap at 1 fired, so the run contained a checkpoint notice — the
	// comparison above then proves notices persist too, in position.
	if len(disk) < 6 {
		t.Errorf("got %d messages, want prompt + 2 turns + notices + answer", len(disk))
	}
}

func rolesTexts(ms []llm.Message) []string {
	var out []string
	for _, m := range ms {
		out = append(out, string(m.Role)+":"+m.Text())
	}
	return out
}

// A kill can land between any two events. At every such point the persisted
// prefix must end at a clean boundary: never an assistant message still
// waiting on its tool results, because that history is unresumable.
func TestTurnPersisterNeverLeavesAnUnpairedToolUseOnDisk(t *testing.T) {
	tool := &fakeTool{name: "t"}
	c := &fakeClient{responses: append(alwaysTools(3), llm.Response{
		Message:    llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "done"}}},
		StopReason: llm.StopEndTurn,
	})}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool), MaxTurns: 10})

	var events []Event
	drain(tap(a.Run(context.Background(), "go"), func(e Event) { events = append(events, e) }))

	for cut := 0; cut <= len(events); cut++ {
		var disk []llm.Message
		p := NewTurnPersister(func(ms []llm.Message) error { disk = append(disk, ms...); return nil }, nil)
		p.Add(llm.UserText("go"))
		for _, e := range events[:cut] {
			p.OnEvent(e)
		}
		if len(disk) == 0 {
			continue
		}
		last := disk[len(disk)-1]
		if last.Role == llm.RoleAssistant && len(last.ToolCalls()) > 0 {
			t.Errorf("killed after event %d: disk ends with tool calls awaiting results", cut)
		}
	}
}

// The first append error latches: nothing later is written — nothing later is
// even *attempted* — so the on-disk messages are always a prefix of the run and
// the end flush can reconcile by count.
func TestTurnPersisterLatchesTheFirstFailure(t *testing.T) {
	boom := errors.New("disk full")
	var disk []llm.Message
	calls, errs := 0, 0
	p := NewTurnPersister(func(ms []llm.Message) error {
		calls++
		if len(disk) >= 1 {
			return boom
		}
		disk = append(disk, ms...)
		return nil
	}, func(error) { errs++ })

	p.Add(llm.UserText("go"))
	p.OnEvent(Event{Kind: EventMessage, Message: llm.Message{Role: llm.RoleAssistant,
		Content: []llm.Block{{Type: llm.BlockText, Text: "answer"}}}})
	p.OnEvent(Event{Kind: EventAgentEnd})

	if p.Persisted() != 1 {
		t.Errorf("persisted = %d, want 1 (the prompt)", p.Persisted())
	}
	if !p.Failed() {
		t.Error("Failed should be latched after the first append error")
	}
	// Later events are no-ops — and that has to be observable: one more append
	// attempt would be one more error, so count calls, not just successes.
	p.OnEvent(Event{Kind: EventSteer, Text: "late"})
	if calls != 2 {
		t.Errorf("appendFn called %d times, want 2: after the latch no append may be attempted", calls)
	}
	if errs != 1 {
		t.Errorf("onError fired %d times, want exactly 1", errs)
	}
	if p.Persisted() != 1 {
		t.Errorf("persisted = %d after a latched failure, want 1", p.Persisted())
	}
}

// A text-only answer kept open by steering: the answer must hit disk before
// the steering message, which is the order they hold in the history.
func TestTurnPersisterFlushesAnAnswerBeforeLandingSteering(t *testing.T) {
	tool := &fakeTool{name: "t"}
	c := &fakeClient{responses: []llm.Response{
		toolCalls("t"),
		{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "first answer"}}},
			StopReason: llm.StopEndTurn},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "second answer"}}},
			StopReason: llm.StopEndTurn},
	}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool), MaxTurns: 10})
	c.onStream = func(n int) {
		if n == 2 && !a.Steer("one more thing") {
			t.Error("steering was refused mid-run")
		}
	}

	disk := collectPersister(t, a, "go")

	if !reflect.DeepEqual(disk, a.Messages()) {
		t.Fatalf("persisted != transcript:\npersisted:  %v\ntranscript: %v", rolesTexts(disk), rolesTexts(a.Messages()))
	}
	// Spot the order directly: first answer, then the steering, then the second.
	var seq []string
	for _, m := range disk {
		seq = append(seq, string(m.Role)+":"+m.Text())
	}
	want := []string{"user:go", "assistant:", "user:", "assistant:first answer", "user:one more thing", "assistant:second answer"}
	if !reflect.DeepEqual(seq, want) {
		t.Errorf("sequence = %v, want %v", seq, want)
	}
}

func TestTurnPersisterSubmitsToolPairInOneBatch(t *testing.T) {
	var batches [][]llm.Message
	p := NewTurnPersister(func(ms []llm.Message) error { batches = append(batches, ms); return nil }, nil)
	m := toolCalls("read").Message
	p.OnEvent(Event{Kind: EventMessage, Message: m})
	if len(batches) != 0 {
		t.Fatal("tool call persisted before result")
	}
	p.OnEvent(Event{Kind: EventToolResults, Message: llm.Message{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: m.ToolCalls()[0].ID}}}})
	if len(batches) != 1 || len(batches[0]) != 2 || p.Persisted() != 2 {
		t.Fatalf("tool pair not committed as one batch: %#v", batches)
	}
}

func TestTurnPersisterRejectsAnInterruptedToolPair(t *testing.T) {
	calls := 0
	p := NewTurnPersister(func(ms []llm.Message) error { calls++; return nil }, nil)
	p.OnEvent(Event{Kind: EventMessage, Message: toolCalls("read").Message})
	p.OnEvent(Event{Kind: EventAgentEnd})
	if !p.Failed() || calls != 0 || p.Persisted() != 0 {
		t.Fatal("interrupted tool call entered the transcript")
	}
}

// The consumer can be busy writing the previous event when cancellation lands.
// The finished batch must still arrive: dropping its results makes the event
// transcript incomplete even though the agent's in-memory history is paired.
func TestTurnPersisterCancellationWaitsForSlowConsumer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		tool := &fakeTool{name: "wait", run: func(ctx context.Context, _ json.RawMessage) (tools.Result, error) {
			close(started)
			<-ctx.Done()
			return tools.Result{}, ctx.Err()
		}}
		response := toolCalls("wait")
		response.Usage = llm.Usage{Input: 120, Output: 18, CacheRead: 40, Reasoning: 6}
		a := New(Config{Client: &fakeClient{responses: []llm.Response{response}}, Registry: tools.NewRegistry(tool)})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var disk []llm.Message
		p := NewTurnPersister(func(ms []llm.Message) error {
			disk = append(disk, ms...)
			return nil
		}, func(err error) { t.Errorf("cancelled turn failed incremental persistence: %v", err) })
		p.Add(llm.UserText("go"))
		events := a.Run(ctx, "go")
		for e := range events {
			p.OnEvent(e)
			if e.Kind == EventToolStart {
				break
			}
		}
		<-started
		cancel()
		// No consumer is reading. Wait until the producer is blocked rather
		// than sleeping and hoping it has reached the post-cancellation send.
		synctest.Wait()
		var end Event
		for e := range events {
			p.OnEvent(e)
			if e.Kind == EventAgentEnd {
				end = e
			}
		}
		if p.Failed() || !reflect.DeepEqual(disk, a.Messages()) {
			t.Fatalf("cancelled transcript is incomplete: persisted=%#v transcript=%#v", disk, a.Messages())
		}
		if end.StopReason != llm.StopAborted || end.Usage != response.Usage {
			t.Fatalf("cancelled run end = %+v, want aborted with %+v", end, response.Usage)
		}
	})
}
