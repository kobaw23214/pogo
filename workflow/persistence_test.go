package workflow

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

type failingSink struct {
	fail  func(Event) bool
	saved []Event
}

func (s *failingSink) Publish(e Event) error {
	if s.fail != nil && s.fail(e) {
		return errors.New("simulated disk full")
	}
	s.saved = append(s.saved, e)
	return nil
}

func TestRunnerPersistenceFailureStopsBeforeUnrecordedWork(t *testing.T) {
	for _, kind := range []string{EventRunStart, EventNodeStart, EventNodeEnd, EventRunEnd} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			initial := startRecord("run_fault")
			initial.Status = RunWorking
			initial.Definition = defWith(Node{ID: "write", Agent: "ag_a", Task: "write once"})
			sink := &failingSink{fail: func(e Event) bool { return e.Kind == kind }}
			exec := func(context.Context, NodeInput) (NodeResult, error) {
				calls.Add(1)
				return NodeResult{Summary: "written"}, nil
			}
			r := &Runner{Record: initial, Sink: sink, Executor: exec}
			last := r.Run(context.Background())
			if r.Err() == nil || !strings.Contains(r.Err().Error(), "disk full") {
				t.Fatalf("missing persistence error: %v", r.Err())
			}
			want := initial
			if len(sink.saved) > 0 {
				want = sink.saved[len(sink.saved)-1].Record
			}
			if !reflect.DeepEqual(last, want) {
				t.Fatalf("returned unsaved state: got=%+v want=%+v", last, want)
			}
			if kind == EventRunStart || kind == EventNodeStart {
				if calls.Load() != 0 {
					t.Fatalf("executed before durable start: %d", calls.Load())
				}
			} else if calls.Load() != 1 {
				t.Fatalf("executions = %d", calls.Load())
			}
			// Repair storage and reconstruct from its last reliable state. A
			// running marker must fail closed; a failed start had done no work.
			recovered := (&Runner{Record: last, Sink: &failingSink{}, Executor: exec}).Run(context.Background())
			if calls.Load() != 1 {
				t.Fatalf("business work replayed on recovery: %d", calls.Load())
			}
			if kind == EventNodeEnd && recovered.Status != RunFailed {
				t.Fatalf("unknown result recovered as %s", recovered.Status)
			}
			if kind != EventNodeEnd && recovered.Status != RunCompleted {
				t.Fatalf("safe recovery failed: %+v", recovered)
			}
		})
	}
}

func TestRunnerPersistenceFailureCancelsInflightAndNeverSchedulesDownstream(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	var downstream atomic.Int32
	initial := startRecord("run_parallel_disk")
	initial.Definition = defWith(Node{ID: "first", Agent: "ag_a", Task: "t"}, Node{ID: "inflight", Agent: "ag_b", Task: "t"}, Node{ID: "later", Agent: "ag_c", Task: "t", After: deps("first")})
	sink := &failingSink{fail: func(e Event) bool { return e.Kind == EventNodeEnd && e.Node == "first" }}
	r := &Runner{Record: initial, Sink: sink, Executor: func(ctx context.Context, in NodeInput) (NodeResult, error) {
		switch in.Node.ID {
		case "first":
			<-started
			return NodeResult{}, nil
		case "inflight":
			close(started)
			<-ctx.Done()
			close(cancelled)
			return NodeResult{}, ctx.Err()
		default:
			downstream.Add(1)
			return NodeResult{}, nil
		}
	}}
	last := r.Run(context.Background())
	if r.Err() == nil || downstream.Load() != 0 {
		t.Fatalf("err=%v downstream=%d", r.Err(), downstream.Load())
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("returned before in-flight executor stopped")
	}
	if last.Nodes["first"].Status != NodeRunning || last.Nodes["inflight"].Status != NodeRunning {
		t.Fatalf("invented durable terminal states: %+v", last.Nodes)
	}
}

func TestRunnerFailedHumanDecisionWriteDoesNotReleaseDownstream(t *testing.T) {
	initial := startRecord("run_human_disk")
	initial.Definition = defWith(Node{ID: "gate", Kind: KindHuman, Prompt: "Approve"}, Node{ID: "write", Agent: "ag_a", Task: "t", After: deps("gate")})
	workspace := t.TempDir()
	var calls atomic.Int32
	exec := func(context.Context, NodeInput) (NodeResult, error) { calls.Add(1); return NodeResult{}, nil }
	suspended := (&Runner{Record: initial, Sink: &failingSink{}, Executor: exec, Workspace: workspace}).Run(context.Background())
	sink := &failingSink{fail: func(e Event) bool { return e.Kind == EventNodeEnd && e.Node == "gate" }}
	r := &Runner{Record: suspended, Sink: sink, Executor: exec, Workspace: workspace, Resume: &HumanDecision{Node: "gate", Action: "approve"}}
	last := r.Run(context.Background())
	if r.Err() == nil || calls.Load() != 0 || last.Nodes["gate"].Status != NodeWaitingHuman || last.Nodes["gate"].Human.Action != "" {
		t.Fatalf("unsaved decision released work: %+v err=%v calls=%d", last, r.Err(), calls.Load())
	}
	final := (&Runner{Record: last, Sink: &failingSink{}, Executor: exec, Workspace: workspace, Resume: &HumanDecision{Node: "gate", Action: "approve"}}).Run(context.Background())
	if final.Status != RunCompleted || calls.Load() != 1 {
		t.Fatalf("repaired resume=%+v calls=%d", final, calls.Load())
	}
}

func TestRunnerCancelledAgentCannotCompleteRun(t *testing.T) {
	initial := startRecord("run_agent_cancelled")
	initial.Definition = defWith(Node{ID: "agent", Agent: "ag_a", Task: "t"})
	final := (&Runner{Record: initial, Executor: func(context.Context, NodeInput) (NodeResult, error) { return NodeResult{}, context.Canceled }}).Run(context.Background())
	if final.Status != RunCancelled || final.Nodes["agent"].Status != NodeCancelled {
		t.Fatalf("cancelled execution became success: %+v", final)
	}
}
