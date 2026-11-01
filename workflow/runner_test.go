package workflow

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordedRun struct {
	mu       sync.Mutex
	events   []Event
	executed []string
	inputs   map[string]NodeInput
}

func (r *recordedRun) sink() *eventLog { return &eventLog{run: r} }

type eventLog struct{ run *recordedRun }

func (l *eventLog) Publish(e Event) error {
	l.run.mu.Lock()
	defer l.run.mu.Unlock()
	l.run.events = append(l.run.events, e)
	return nil
}

func (r *recordedRun) executor(run func(context.Context, NodeInput) (NodeResult, error)) NodeExecutor {
	return func(ctx context.Context, in NodeInput) (NodeResult, error) {
		r.mu.Lock()
		r.executed = append(r.executed, in.Node.ID)
		if r.inputs == nil {
			r.inputs = map[string]NodeInput{}
		}
		r.inputs[in.Node.ID] = in
		r.mu.Unlock()
		return run(ctx, in)
	}
}

func (r *recordedRun) kinds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.events))
	for i, e := range r.events {
		kind := e.Kind
		if e.Node != "" {
			kind += ":" + e.Node + "=" + e.State.Status
		}
		out[i] = kind
	}
	return out
}

func startRecord(id string) RunRecord {
	return RunRecord{ID: id, WorkflowID: "wf_x", Revision: 1, CreatedAt: "2026-01-01T00:00:00Z"}
}

func TestRunnerExecutesInTopoOrderAndFeedsArtifacts(t *testing.T) {
	def := defWith(
		Node{ID: "analyze", Agent: "ag_a", Task: "look", Expect: &Expect{Path: "analyze.md"}},
		Node{ID: "review", Agent: "ag_b", Task: "check", After: deps("analyze"), Expect: &Expect{Path: "review.md"}},
		Node{ID: "report", Agent: "ag_c", Task: "sum", After: deps("review")},
	)
	rec := &recordedRun{}
	r := &Runner{
		Record: startRecord("run_1"),
		Executor: rec.executor(func(_ context.Context, in NodeInput) (NodeResult, error) {
			artifact := ""
			if in.Node.Expect != nil {
				artifact = in.Node.Expect.Path
			}
			return NodeResult{Artifact: artifact, Summary: "done " + in.Node.ID}, nil
		}),
		Sink:      rec.sink(),
		Workspace: t.TempDir(),
		Timeout:   time.Minute,
	}
	r.Record.Definition = def
	final := r.Run(context.Background())

	if final.Status != RunCompleted {
		t.Fatalf("status = %q (%q)", final.Status, final.Error)
	}
	if strings.Join(rec.executed, ",") != "analyze,review,report" {
		t.Fatalf("execution order = %v", rec.executed)
	}
	reviewIn := rec.inputs["review"]
	if len(reviewIn.Artifacts) != 1 || reviewIn.Artifacts[0] != (ArtifactRef{Node: "analyze", Path: "analyze.md"}) {
		t.Fatalf("review artifacts = %+v", reviewIn.Artifacts)
	}
	// Report depends on review only: the transitive analyze artifact stays out
	// of its list — the shared workspace already holds the file.
	if arts := rec.inputs["report"].Artifacts; len(arts) != 1 || arts[0].Node != "review" {
		t.Fatalf("report artifacts = %+v", arts)
	}
	if final.Nodes["review"].Summary != "done review" || final.Nodes["report"].Artifact != "" {
		t.Fatalf("node states = %+v", final.Nodes)
	}
	kinds := strings.Join(rec.kinds(), " ")
	for _, want := range []string{
		"run_start", "node_start:analyze=running", "node_end:analyze=success",
		"node_start:review=running", "node_end:report=success", "run_end",
	} {
		if !strings.Contains(kinds, want) {
			t.Fatalf("event stream %q misses %q", kinds, want)
		}
	}
	// Every event carries a full, independent snapshot of the record.
	for _, e := range rec.events {
		if e.Record.ID != "run_1" || len(e.Record.Nodes) != 3 {
			t.Fatalf("event snapshot = %+v", e.Record)
		}
	}
	if final.EndedAt == "" {
		t.Fatal("terminal record has no ended_at")
	}
}

func TestRunnerFailureSkipsDownstream(t *testing.T) {
	def := defWith(
		Node{ID: "analyze", Agent: "ag_a", Task: "look"},
		Node{ID: "review", Agent: "ag_b", Task: "check", After: deps("analyze")},
		Node{ID: "report", Agent: "ag_c", Task: "sum", After: deps("review")},
	)
	rec := &recordedRun{}
	r := &Runner{
		Record: startRecord("run_2"),
		Executor: rec.executor(func(_ context.Context, in NodeInput) (NodeResult, error) {
			if in.Node.ID == "analyze" {
				return NodeResult{}, errors.New("expected artifact analyze.md was not produced")
			}
			return NodeResult{}, nil
		}),
		Sink: rec.sink(),
	}
	r.Record.Definition = def
	final := r.Run(context.Background())

	if final.Status != RunFailed || !strings.Contains(final.Error, "analyze.md") {
		t.Fatalf("status = %q error = %q", final.Status, final.Error)
	}
	if final.Nodes["analyze"].Status != NodeFailed || final.Nodes["review"].Status != NodeSkipped || final.Nodes["report"].Status != NodeSkipped {
		t.Fatalf("cascade = %+v", final.Nodes)
	}
	if strings.Join(rec.executed, ",") != "analyze" {
		t.Fatalf("executed = %v, want only analyze", rec.executed)
	}
	kinds := strings.Join(rec.kinds(), " ")
	if !strings.Contains(kinds, "node_end:review=skipped") || !strings.Contains(kinds, "node_end:report=skipped") {
		t.Fatalf("skip cascade missing from %q", kinds)
	}
}

func TestRunnerCancelStopsChain(t *testing.T) {
	def := defWith(
		Node{ID: "analyze", Agent: "ag_a", Task: "look"},
		Node{ID: "review", Agent: "ag_b", Task: "check", After: deps("analyze")},
	)
	ctx, cancel := context.WithCancel(context.Background())
	rec := &recordedRun{}
	r := &Runner{
		Record: startRecord("run_3"),
		Executor: rec.executor(func(ctx context.Context, in NodeInput) (NodeResult, error) {
			cancel()
			<-ctx.Done()
			return NodeResult{}, ctx.Err()
		}),
		Sink: rec.sink(),
	}
	r.Record.Definition = def
	final := r.Run(ctx)

	if final.Status != RunCancelled {
		t.Fatalf("status = %q (%q)", final.Status, final.Error)
	}
	if final.Nodes["analyze"].Status != NodeCancelled || final.Nodes["review"].Status != NodeCancelled {
		t.Fatalf("cancel cascade = %+v", final.Nodes)
	}
}

func TestRunnerTimeoutFailsRatherThanCancels(t *testing.T) {
	def := defWith(Node{ID: "slow", Agent: "ag_a", Task: "wait"})
	rec := &recordedRun{}
	r := &Runner{
		Record: startRecord("run_4"),
		Executor: rec.executor(func(ctx context.Context, _ NodeInput) (NodeResult, error) {
			<-ctx.Done()
			return NodeResult{}, ctx.Err()
		}),
		Sink:    rec.sink(),
		Timeout: 50 * time.Millisecond,
	}
	r.Record.Definition = def
	final := r.Run(context.Background())

	if final.Status != RunFailed || final.Error != "run timed out" {
		t.Fatalf("status = %q error = %q", final.Status, final.Error)
	}
	if final.Nodes["slow"].Status != NodeCancelled {
		t.Fatalf("node = %+v", final.Nodes["slow"])
	}
}

func TestRunnerInvalidDefinitionFailsFast(t *testing.T) {
	def := defWith(
		Node{ID: "a", Agent: "ag_a", Task: "t", After: deps("b")},
		Node{ID: "b", Agent: "ag_b", Task: "t", After: deps("a")},
	)
	rec := &recordedRun{}
	r := &Runner{Record: startRecord("run_5"), Executor: rec.executor(func(context.Context, NodeInput) (NodeResult, error) {
		t.Fatal("executor must not run for a cyclic definition")
		return NodeResult{}, nil
	}), Sink: rec.sink()}
	r.Record.Definition = def
	final := r.Run(context.Background())

	if final.Status != RunFailed || !strings.Contains(final.Error, "cycle") {
		t.Fatalf("status = %q error = %q", final.Status, final.Error)
	}
	if final.Nodes["a"].Status != NodeSkipped || final.Nodes["b"].Status != NodeSkipped {
		t.Fatalf("nodes = %+v", final.Nodes)
	}
	if len(rec.executed) != 0 {
		t.Fatalf("executed = %v", rec.executed)
	}
}

func TestRunnerCancelBetweenNodes(t *testing.T) {
	def := defWith(
		Node{ID: "one", Agent: "ag_a", Task: "t"},
		Node{ID: "two", Agent: "ag_b", Task: "t", After: deps("one")},
	)
	ctx, cancel := context.WithCancel(context.Background())
	rec := &recordedRun{}
	r := &Runner{
		Record: startRecord("run_6"),
		Executor: rec.executor(func(_ context.Context, _ NodeInput) (NodeResult, error) {
			cancel() // the first node finishes, then the world goes away
			return NodeResult{}, nil
		}),
		Sink: rec.sink(),
	}
	r.Record.Definition = def
	final := r.Run(ctx)

	if final.Status != RunCancelled {
		t.Fatalf("status = %q", final.Status)
	}
	if final.Nodes["one"].Status != NodeSuccess || final.Nodes["two"].Status != NodeCancelled {
		t.Fatalf("nodes = %+v", final.Nodes)
	}
}
