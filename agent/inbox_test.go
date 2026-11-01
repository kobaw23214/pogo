package agent

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func TestDurableSteeringWaitsForPersistenceBeforeCallingModel(t *testing.T) {
	c := &fakeClient{}
	a := New(Config{Client: c, Registry: tools.NewRegistry()})
	m := llm.UserText("steer")
	m.InboxID = "receipt"
	m.RunID = "run"
	m.Steering = true
	events := a.RunMessageWithPending(context.Background(), llm.UserText("first"), []llm.Message{m})
	var got Event
	for e := range events {
		if e.Kind == EventSteer {
			got = e
			break
		}
	}
	if got.SteerAck == nil || got.Message.InboxID != "receipt" {
		t.Fatal("missing durable delivery handshake")
	}
	if c.callCount() != 0 {
		t.Fatal("model called before transcript persistence")
	}
	got.SteerAck <- nil
	drain(events)
	if c.callCount() != 1 {
		t.Fatal("model did not continue after persistence")
	}
}

func TestDurableSteeringPersistenceFailureStopsBeforeModel(t *testing.T) {
	c := &fakeClient{}
	a := New(Config{Client: c, Registry: tools.NewRegistry()})
	m := llm.UserText("steer")
	m.InboxID = "receipt"
	events := a.RunMessageWithPending(context.Background(), llm.UserText("first"), []llm.Message{m})
	var end Event
	for e := range events {
		if e.SteerAck != nil {
			e.SteerAck <- errors.New("write failed")
		}
		if e.Kind == EventAgentEnd {
			end = e
		}
	}
	if c.callCount() != 0 || end.EndReason != EndAborted || end.Err == nil {
		t.Fatalf("model ran on unpersisted receipt: %+v", end)
	}
}

func TestFinalBoundaryAndSteeringAcceptanceAreAtomic(t *testing.T) {
	for i := 0; i < 300; i++ {
		a := &Agent{running: true}
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var accepted, finished bool
		go func() { defer wg.Done(); <-start; accepted = a.Steer("late") }()
		go func() { defer wg.Done(); <-start; finished = a.finishIfNoPending() }()
		close(start)
		wg.Wait()
		if accepted == finished {
			t.Fatalf("accepted=%v finished=%v", accepted, finished)
		}
		if finished && a.hasPending() {
			t.Fatal("accepted message stranded after final boundary")
		}
	}
}
