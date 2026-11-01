package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunnerParallelNodesActuallyOverlap(t *testing.T) {
	def := defWith(
		Node{ID: "a", Agent: "ag_a", Task: "t"},
		Node{ID: "b", Agent: "ag_b", Task: "t"},
	)
	started := make(chan string, 2)
	release := make(chan struct{})
	rec := &recordedRun{}
	r := &Runner{
		Record: startRecord("run_p1"),
		Executor: rec.executor(func(ctx context.Context, in NodeInput) (NodeResult, error) {
			started <- in.Node.ID
			select {
			case <-release:
				return NodeResult{Summary: in.Node.ID}, nil
			case <-ctx.Done():
				return NodeResult{}, ctx.Err()
			}
		}),
		Sink:      rec.sink(),
		Workspace: t.TempDir(),
	}
	r.Record.Definition = def
	fin := make(chan RunRecord, 1)
	go func() { fin <- r.Run(context.Background()) }()

	seen := map[string]bool{}
	deadline := time.After(10 * time.Second)
	for len(seen) < 2 {
		select {
		case id := <-started:
			seen[id] = true
		case <-deadline:
			t.Fatalf("only %v started; parallelism broken", seen)
		}
	}
	close(release)
	final := <-fin
	if final.Status != RunCompleted {
		t.Fatalf("status = %q (%q)", final.Status, final.Error)
	}
	// Both node_starts precede any node_end: true overlap, not interleaving.
	kinds := rec.kinds()
	firstEnd := len(kinds)
	starts := 0
	for i, k := range kinds {
		if strings.HasPrefix(k, "node_end") && i < firstEnd {
			firstEnd = i
		}
		if strings.HasPrefix(k, "node_start") && i < firstEnd {
			starts++
		}
	}
	if starts != 2 {
		t.Fatalf("starts before first end = %d, want 2: %v", starts, kinds)
	}
}

func TestRunnerParallelismIsBounded(t *testing.T) {
	def := defWith(
		Node{ID: "n1", Agent: "ag", Task: "t"}, Node{ID: "n2", Agent: "ag", Task: "t"},
		Node{ID: "n3", Agent: "ag", Task: "t"}, Node{ID: "n4", Agent: "ag", Task: "t"},
		Node{ID: "n5", Agent: "ag", Task: "t"}, Node{ID: "n6", Agent: "ag", Task: "t"},
	)
	var cur, max atomic.Int32
	rec := &recordedRun{}
	r := &Runner{
		Record: startRecord("run_p2"),
		Executor: rec.executor(func(_ context.Context, _ NodeInput) (NodeResult, error) {
			c := cur.Add(1)
			for {
				m := max.Load()
				if c <= m || max.CompareAndSwap(m, c) {
					break
				}
			}
			time.Sleep(50 * time.Millisecond)
			cur.Add(-1)
			return NodeResult{}, nil
		}),
		Sink: rec.sink(),
	}
	r.Record.Definition = def
	final := r.Run(context.Background())
	if final.Status != RunCompleted {
		t.Fatalf("status = %q", final.Status)
	}
	if m := max.Load(); m > maxParallelNodes || m < 2 {
		t.Fatalf("peak concurrency = %d, want 2..%d", m, maxParallelNodes)
	}
}

func TestRunnerConditionalEdges(t *testing.T) {
	build := func() (*recordedRun, *Runner) {
		def := defWith(
			Node{ID: "a", Agent: "ag_a", Task: "t"},
			Node{ID: "on_success", Agent: "ag_b", Task: "t", After: deps("a")},
			Node{ID: "on_failed", Agent: "ag_c", Task: "t", After: []Dep{{Node: "a", When: WhenFailed}}},
			Node{ID: "on_always", Agent: "ag_d", Task: "t", After: []Dep{{Node: "a", When: WhenAlways}}},
		)
		rec := &recordedRun{}
		r := &Runner{Record: startRecord("run_c"), Sink: rec.sink()}
		r.Record.Definition = def
		return rec, r
	}

	t.Run("upstream succeeds", func(t *testing.T) {
		rec, r := build()
		r.Executor = rec.executor(func(_ context.Context, in NodeInput) (NodeResult, error) {
			return NodeResult{Summary: in.Node.ID}, nil
		})
		final := r.Run(context.Background())
		if final.Status != RunCompleted {
			t.Fatalf("status = %q (%q)", final.Status, final.Error)
		}
		if final.Nodes["on_success"].Status != NodeSuccess || final.Nodes["on_success"].Branch != WhenSuccess {
			t.Fatalf("on_success = %+v", final.Nodes["on_success"])
		}
		if final.Nodes["on_always"].Status != NodeSuccess || final.Nodes["on_always"].Branch != WhenAlways {
			t.Fatalf("on_always = %+v", final.Nodes["on_always"])
		}
		if final.Nodes["on_failed"].Status != NodeSkipped || final.Nodes["on_failed"].SkippedBy != "a" {
			t.Fatalf("on_failed = %+v", final.Nodes["on_failed"])
		}
	})

	t.Run("upstream fails", func(t *testing.T) {
		rec, r := build()
		r.Executor = rec.executor(func(_ context.Context, in NodeInput) (NodeResult, error) {
			if in.Node.ID == "a" {
				return NodeResult{}, errors.New("boom")
			}
			return NodeResult{Summary: in.Node.ID}, nil
		})
		final := r.Run(context.Background())
		if final.Status != RunFailed || final.Error != "boom" {
			t.Fatalf("status = %q (%q)", final.Status, final.Error)
		}
		if final.Nodes["on_failed"].Status != NodeSuccess || final.Nodes["on_failed"].Branch != WhenFailed {
			t.Fatalf("on_failed = %+v", final.Nodes["on_failed"])
		}
		if final.Nodes["on_always"].Status != NodeSuccess || final.Nodes["on_always"].Branch != WhenAlways {
			t.Fatalf("on_always = %+v", final.Nodes["on_always"])
		}
		if final.Nodes["on_success"].Status != NodeSkipped || final.Nodes["on_success"].SkippedBy != "a" {
			t.Fatalf("on_success = %+v", final.Nodes["on_success"])
		}
	})
}

func TestRunnerSkipCascadeNamesEachCulprit(t *testing.T) {
	def := defWith(
		Node{ID: "a", Agent: "ag_a", Task: "t"},
		Node{ID: "b", Agent: "ag_b", Task: "t", After: deps("a")},
		Node{ID: "c", Agent: "ag_c", Task: "t", After: deps("b")},
		Node{ID: "d", Agent: "ag_d", Task: "t", After: deps("c")},
	)
	rec := &recordedRun{}
	r := &Runner{
		Record: startRecord("run_s"),
		Executor: rec.executor(func(_ context.Context, in NodeInput) (NodeResult, error) {
			if in.Node.ID == "b" {
				return NodeResult{}, errors.New("b broke")
			}
			return NodeResult{}, nil
		}),
		Sink: rec.sink(),
	}
	r.Record.Definition = def
	final := r.Run(context.Background())

	if final.Status != RunFailed {
		t.Fatalf("status = %q", final.Status)
	}
	if final.Nodes["c"].Status != NodeSkipped || final.Nodes["c"].SkippedBy != "b" {
		t.Fatalf("c = %+v", final.Nodes["c"])
	}
	if final.Nodes["d"].Status != NodeSkipped || final.Nodes["d"].SkippedBy != "c" {
		t.Fatalf("d = %+v", final.Nodes["d"])
	}
}

func TestRunnerAlwaysDoesNotFireOnSkippedUpstream(t *testing.T) {
	def := defWith(
		Node{ID: "a", Agent: "ag_a", Task: "t"},
		Node{ID: "b", Agent: "ag_b", Task: "t", After: deps("a")},
		Node{ID: "c", Agent: "ag_c", Task: "t", After: []Dep{{Node: "b", When: WhenAlways}}},
	)
	rec := &recordedRun{}
	r := &Runner{
		Record: startRecord("run_as"),
		Executor: rec.executor(func(_ context.Context, in NodeInput) (NodeResult, error) {
			if in.Node.ID == "a" {
				return NodeResult{}, errors.New("a broke")
			}
			return NodeResult{}, nil
		}),
		Sink: rec.sink(),
	}
	r.Record.Definition = def
	final := r.Run(context.Background())

	if final.Nodes["b"].Status != NodeSkipped || final.Nodes["c"].Status != NodeSkipped {
		t.Fatalf("cascade = %+v", final.Nodes)
	}
	if final.Nodes["c"].SkippedBy != "b" {
		t.Fatalf("c.skipped_by = %q, want b", final.Nodes["c"].SkippedBy)
	}
	for _, id := range rec.executed {
		if id == "c" {
			t.Fatal("c executed on a skipped upstream")
		}
	}
}

func TestRunnerFailStopDoesNotPreemptIndependentBranches(t *testing.T) {
	def := defWith(
		Node{ID: "a", Agent: "ag_a", Task: "t"},
		Node{ID: "x", Agent: "ag_x", Task: "t"},
		Node{ID: "b", Agent: "ag_b", Task: "t", After: deps("a")},
		Node{ID: "handler", Agent: "ag_h", Task: "t", After: []Dep{{Node: "a", When: WhenFailed}}},
	)
	xDone := make(chan struct{})
	rec := &recordedRun{}
	r := &Runner{
		Record: startRecord("run_fs"),
		Executor: rec.executor(func(_ context.Context, in NodeInput) (NodeResult, error) {
			switch in.Node.ID {
			case "a":
				return NodeResult{}, errors.New("a broke")
			case "x":
				time.Sleep(150 * time.Millisecond)
				close(xDone)
				return NodeResult{Summary: "x finished"}, nil
			default:
				return NodeResult{Summary: in.Node.ID}, nil
			}
		}),
		Sink: rec.sink(),
	}
	r.Record.Definition = def
	final := r.Run(context.Background())

	select {
	case <-xDone:
	default:
		t.Fatal("independent branch was preempted by the failure")
	}
	if final.Status != RunFailed || final.Error != "a broke" {
		t.Fatalf("status = %q (%q)", final.Status, final.Error)
	}
	if final.Nodes["x"].Status != NodeSuccess || final.Nodes["x"].Summary != "x finished" {
		t.Fatalf("x = %+v", final.Nodes["x"])
	}
	if final.Nodes["handler"].Status != NodeSuccess || final.Nodes["handler"].Branch != WhenFailed {
		t.Fatalf("handler = %+v", final.Nodes["handler"])
	}
	if final.Nodes["b"].Status != NodeSkipped {
		t.Fatalf("b = %+v", final.Nodes["b"])
	}
}

func TestRunnerBarrierFanIn(t *testing.T) {
	build := func() (*recordedRun, *Runner) {
		def := defWith(
			Node{ID: "a", Agent: "ag_a", Task: "t"},
			Node{ID: "b", Agent: "ag_b", Task: "t"},
			Node{ID: "join", Agent: "ag_c", Task: "t", After: deps("a", "b")},
		)
		rec := &recordedRun{}
		r := &Runner{Record: startRecord("run_bar"), Sink: rec.sink()}
		r.Record.Definition = def
		return rec, r
	}

	t.Run("all succeed releases the join", func(t *testing.T) {
		rec, r := build()
		r.Executor = rec.executor(func(_ context.Context, _ NodeInput) (NodeResult, error) {
			return NodeResult{}, nil
		})
		final := r.Run(context.Background())
		if final.Nodes["join"].Status != NodeSuccess || final.Nodes["join"].Branch != WhenSuccess {
			t.Fatalf("join = %+v", final.Nodes["join"])
		}
	})

	t.Run("one failure holds the join", func(t *testing.T) {
		rec, r := build()
		r.Executor = rec.executor(func(_ context.Context, in NodeInput) (NodeResult, error) {
			if in.Node.ID == "b" {
				return NodeResult{}, errors.New("b broke")
			}
			return NodeResult{}, nil
		})
		final := r.Run(context.Background())
		if final.Nodes["join"].Status != NodeSkipped || final.Nodes["join"].SkippedBy != "b" {
			t.Fatalf("join = %+v", final.Nodes["join"])
		}
		for _, id := range rec.executed {
			if id == "join" {
				t.Fatal("join executed with a failed barrier input")
			}
		}
	})
}

func TestRunnerMixedEdgesPickFirstActive(t *testing.T) {
	def := defWith(
		Node{ID: "a", Agent: "ag_a", Task: "t"},
		Node{ID: "b", Agent: "ag_b", Task: "t"},
		Node{ID: "c", Agent: "ag_c", Task: "t", After: []Dep{{Node: "a", When: WhenSuccess}, {Node: "b", When: WhenFailed}}},
	)
	rec := &recordedRun{}
	r := &Runner{
		Record: startRecord("run_mix"),
		Executor: rec.executor(func(_ context.Context, in NodeInput) (NodeResult, error) {
			if in.Node.ID == "b" {
				return NodeResult{}, errors.New("b broke")
			}
			return NodeResult{}, nil
		}),
		Sink: rec.sink(),
	}
	r.Record.Definition = def
	final := r.Run(context.Background())

	// a succeeded (barrier holds) and b failed: both edges are live, the first
	// in after order names the branch.
	if final.Nodes["c"].Status != NodeSuccess || final.Nodes["c"].Branch != WhenSuccess {
		t.Fatalf("c = %+v", final.Nodes["c"])
	}
}

func TestRunnerTemplatesResolveIntoNodeInput(t *testing.T) {
	workspace := t.TempDir()
	def := defWith(
		Node{ID: "a", Agent: "ag_a", Task: "t", Expect: &Expect{Path: "a.md"}},
		Node{ID: "b", Agent: "ag_b", Task: "Read {{ nodes.a.artifact }} then {{nodes.a.summary}} in {{ run.workspace }}", After: deps("a")},
	)
	rec := &recordedRun{}
	r := &Runner{
		Record: startRecord("run_t"),
		Executor: rec.executor(func(_ context.Context, in NodeInput) (NodeResult, error) {
			if in.Node.ID == "a" {
				return NodeResult{Artifact: "a.md", Summary: "sum-a"}, nil
			}
			return NodeResult{Summary: "b done"}, nil
		}),
		Sink:      rec.sink(),
		Workspace: workspace,
	}
	r.Record.Definition = def
	final := r.Run(context.Background())

	if final.Status != RunCompleted {
		t.Fatalf("status = %q (%q)", final.Status, final.Error)
	}
	got := rec.inputs["b"].Node.Task
	want := "Read a.md then sum-a in " + workspace
	if got != want {
		t.Fatalf("b task = %q, want %q", got, want)
	}
	inputs := final.Nodes["b"].Inputs
	if len(inputs) != 3 || inputs["nodes.a.artifact"] != "a.md" || inputs["nodes.a.summary"] != "sum-a" || inputs["run.workspace"] != workspace {
		t.Fatalf("inputs = %+v", inputs)
	}
	// Snapshots handed to the sink are independent of the settled record.
	for _, e := range rec.events {
		if in, ok := e.Record.Nodes["b"].Inputs["nodes.a.summary"]; ok && in != "sum-a" {
			t.Fatalf("event snapshot inputs corrupted: %+v", e.Record.Nodes["b"].Inputs)
		}
	}
	// The definition snapshot in the record keeps the raw task.
	if !strings.Contains(final.Definition.Nodes[1].Task, "{{ nodes.a.artifact }}") {
		t.Fatalf("definition snapshot lost the template: %q", final.Definition.Nodes[1].Task)
	}
}

func TestRunnerTemplateEvalFailureFailsTheNode(t *testing.T) {
	def := defWith(
		Node{ID: "a", Agent: "ag_a", Task: "t", Expect: &Expect{Path: "a.md"}},
		Node{ID: "b", Agent: "ag_b", Task: "read {{ nodes.a.artifact }}", After: deps("a")},
		Node{ID: "c", Agent: "ag_c", Task: "t", After: deps("b")},
	)
	rec := &recordedRun{}
	r := &Runner{
		Record: startRecord("run_tf"),
		Executor: rec.executor(func(_ context.Context, _ NodeInput) (NodeResult, error) {
			// a succeeds but reports no artifact: the reference has no value.
			return NodeResult{Summary: "a done"}, nil
		}),
		Sink: rec.sink(),
	}
	r.Record.Definition = def
	final := r.Run(context.Background())

	if final.Status != RunFailed {
		t.Fatalf("status = %q", final.Status)
	}
	b := final.Nodes["b"]
	if b.Status != NodeFailed || !strings.Contains(b.Error, "nodes.a.artifact") {
		t.Fatalf("b = %+v", b)
	}
	if final.Nodes["c"].Status != NodeSkipped || final.Nodes["c"].SkippedBy != "b" {
		t.Fatalf("c = %+v", final.Nodes["c"])
	}
	for _, id := range rec.executed {
		if id == "b" || id == "c" {
			t.Fatalf("%s executed despite the failed evaluation", id)
		}
	}
}

func TestRunnerTemplateOnFailedUpstreamHasNoValue(t *testing.T) {
	def := defWith(
		Node{ID: "a", Agent: "ag_a", Task: "t"},
		Node{ID: "handler", Agent: "ag_h", Task: "explain {{ nodes.a.summary }}", After: []Dep{{Node: "a", When: WhenFailed}}},
	)
	rec := &recordedRun{}
	r := &Runner{
		Record: startRecord("run_th"),
		Executor: rec.executor(func(_ context.Context, in NodeInput) (NodeResult, error) {
			if in.Node.ID == "a" {
				return NodeResult{}, errors.New("a broke")
			}
			return NodeResult{Summary: "handled"}, nil
		}),
		Sink: rec.sink(),
	}
	r.Record.Definition = def
	final := r.Run(context.Background())

	// The escape edge released the handler, but a failed node has no summary
	// to give: the handler fails naming the reference, not silently blank.
	h := final.Nodes["handler"]
	if h.Status != NodeFailed || !strings.Contains(h.Error, "nodes.a.summary") || h.Branch != WhenFailed {
		t.Fatalf("handler = %+v", h)
	}
}

func TestRunnerV1JSONDefinitionRunsUnchanged(t *testing.T) {
	// The v1 wire shape: after as a plain string list.
	raw := `{"id":"wf_v1","name":"legacy","revision":2,"nodes":[
		{"id":"analyze","agent":"ag_a","task":"look","expect":{"path":"a.md"}},
		{"id":"review","agent":"ag_b","task":"check","after":["analyze"]}
	]}`
	var def Definition
	if err := json.Unmarshal([]byte(raw), &def); err != nil {
		t.Fatal(err)
	}
	def.Normalize()
	if err := def.Validate(); err != nil {
		t.Fatal(err)
	}
	rec := &recordedRun{}
	r := &Runner{
		Record: RunRecord{ID: "run_v1", WorkflowID: def.ID, Revision: def.Revision, Definition: def, CreatedAt: "2026-01-01T00:00:00Z"},
		Executor: rec.executor(func(_ context.Context, in NodeInput) (NodeResult, error) {
			if in.Node.Expect != nil {
				return NodeResult{Artifact: in.Node.Expect.Path, Summary: "done " + in.Node.ID}, nil
			}
			return NodeResult{Summary: "done " + in.Node.ID}, nil
		}),
		Sink: rec.sink(),
	}
	final := r.Run(context.Background())

	if final.Status != RunCompleted || strings.Join(rec.executed, ",") != "analyze,review" {
		t.Fatalf("status = %q executed = %v", final.Status, rec.executed)
	}
	if arts := rec.inputs["review"].Artifacts; len(arts) != 1 || arts[0] != (ArtifactRef{Node: "analyze", Path: "a.md"}) {
		t.Fatalf("review artifacts = %+v", arts)
	}
	// The record's JSON keeps the v1 string form: old readers stay compatible.
	out, err := json.Marshal(final.Definition.Nodes[1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"after":["analyze"]`) {
		t.Fatalf("node JSON = %s, want string-form after", out)
	}
}

func TestRunnerIndependentNodeFinishesAfterDependents(t *testing.T) {
	// A root defined after a chain still schedules with the chain's head —
	// definition order among ready nodes, parallelism preserved.
	def := defWith(
		Node{ID: "a", Agent: "ag_a", Task: "t"},
		Node{ID: "b", Agent: "ag_b", Task: "t", After: deps("a")},
		Node{ID: "lone", Agent: "ag_l", Task: "t"},
	)
	rec := &recordedRun{}
	r := &Runner{
		Record: startRecord("run_ind"),
		Executor: rec.executor(func(context.Context, NodeInput) (NodeResult, error) {
			return NodeResult{}, nil
		}),
		Sink: rec.sink(),
	}
	r.Record.Definition = def
	final := r.Run(context.Background())

	if final.Status != RunCompleted {
		t.Fatalf("status = %q", final.Status)
	}
	// node_start is the synchronous scheduling/persistence boundary. Actual
	// executor goroutines may first run in any order: the scheduler can launch
	// lone before b even when Go does not give lone CPU time until after b.
	// Still require both ready roots to be scheduled first, and require a's
	// successful terminal event before its dependent is released.
	var startOrder []string
	aSucceeded := false
	for _, event := range rec.events {
		if event.Kind == EventNodeEnd && event.Node == "a" {
			aSucceeded = event.State.Status == NodeSuccess
		}
		if event.Kind != EventNodeStart {
			continue
		}
		startOrder = append(startOrder, event.Node)
		if event.Node == "b" && (!aSucceeded || event.Record.Nodes["a"].Status != NodeSuccess) {
			t.Fatalf("b was scheduled without a durable successful dependency: %+v", event)
		}
	}
	if strings.Join(startOrder, ",") != "a,lone,b" {
		t.Fatalf("ready roots were not scheduled before the dependent: %v", startOrder)
	}
}
