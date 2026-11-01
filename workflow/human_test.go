package workflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunnerHumanSuspendsAndApproveResumes(t *testing.T) {
	def := defWith(
		Node{ID: "a", Agent: "ag_a", Task: "t", Expect: &Expect{Path: "a.md"}},
		Node{ID: "gate", Kind: KindHuman, Prompt: "Review {{ nodes.a.artifact }}", Fields: []FieldSpec{{Key: "note", Required: true}}, After: deps("a")},
		Node{ID: "c", Agent: "ag_c", Task: "t", After: deps("gate")},
	)
	workspace := t.TempDir()
	rec := &recordedRun{}
	r := &Runner{
		Record: startRecord("run_h1"),
		Executor: rec.executor(func(_ context.Context, in NodeInput) (NodeResult, error) {
			artifact := ""
			if in.Node.Expect != nil {
				artifact = in.Node.Expect.Path
			}
			return NodeResult{Artifact: artifact, Summary: "done " + in.Node.ID}, nil
		}),
		Sink:      rec.sink(),
		Workspace: workspace,
	}
	r.Record.Definition = def
	first := r.Run(context.Background())

	// Suspended: run still working, gate waiting on a person, c not started.
	if first.Status != RunWorking || IsTerminal(first.Status) {
		t.Fatalf("status = %q, want working (suspended)", first.Status)
	}
	gate := first.Nodes["gate"]
	if gate.Status != NodeWaitingHuman || gate.Human == nil {
		t.Fatalf("gate = %+v", gate)
	}
	if gate.Human.Prompt != "Review a.md" || gate.Human.Deadline == "" || gate.Human.OnTimeout != "reject" {
		t.Fatalf("human state = %+v", gate.Human)
	}
	if len(gate.Human.Actions) != 2 || gate.Human.Actions[0] != "approve" {
		t.Fatalf("actions = %v", gate.Human.Actions)
	}
	if gate.Inputs["nodes.a.artifact"] != "a.md" {
		t.Fatalf("inputs = %+v", gate.Inputs)
	}
	if first.Nodes["c"].Status != NodeWaiting {
		t.Fatalf("c = %+v", first.Nodes["c"])
	}
	for _, id := range rec.executed {
		if id == "gate" || id == "c" {
			t.Fatalf("%s executed past the human gate", id)
		}
	}

	// Approve: the same record resumes, gate succeeds, downstream runs.
	r2 := &Runner{
		Record:    first,
		Executor:  r.Executor,
		Sink:      rec.sink(),
		Workspace: workspace,
		Resume:    &HumanDecision{Node: "gate", Action: "approve", Values: map[string]string{"note": "looks fine"}},
	}
	final := r2.Run(context.Background())
	if final.Status != RunCompleted {
		t.Fatalf("status = %q (%q)", final.Status, final.Error)
	}
	gate = final.Nodes["gate"]
	if gate.Status != NodeSuccess || gate.Artifact != "_human/gate.json" || gate.Human.Action != "approve" || gate.Human.Values["note"] != "looks fine" {
		t.Fatalf("settled gate = %+v", gate)
	}
	if final.Nodes["c"].Status != NodeSuccess {
		t.Fatalf("c = %+v", final.Nodes["c"])
	}
	// The decision file exists and reads back.
	raw, err := os.ReadFile(filepath.Join(workspace, "_human", "gate.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decision map[string]any
	if err := json.Unmarshal(raw, &decision); err != nil {
		t.Fatal(err)
	}
	if decision["action"] != "approve" {
		t.Fatalf("decision file = %s", raw)
	}
	// Events: node_start(waiting_human) then node_end(success) on resume.
	kinds := strings.Join(rec.kinds(), " ")
	if !strings.Contains(kinds, "node_start:gate=waiting_human") || !strings.Contains(kinds, "node_end:gate=success") {
		t.Fatalf("events = %s", kinds)
	}
}

func TestRunnerHumanRejectReleasesRejectedBranch(t *testing.T) {
	def := defWith(
		Node{ID: "gate", Kind: KindHuman, Prompt: "ok?"},
		Node{ID: "on_yes", Agent: "ag_a", Task: "t", After: deps("gate")},
		Node{ID: "on_no", Agent: "ag_b", Task: "t", After: []Dep{{Node: "gate", When: WhenRejected}}},
	)
	rec := &recordedRun{}
	r := &Runner{
		Record: startRecord("run_h2"),
		Executor: rec.executor(func(_ context.Context, in NodeInput) (NodeResult, error) {
			return NodeResult{Summary: in.Node.ID}, nil
		}),
		Sink: rec.sink(),
	}
	r.Record.Definition = def
	suspended := r.Run(context.Background())
	r2 := &Runner{Record: suspended, Executor: r.Executor, Sink: rec.sink(),
		Resume: &HumanDecision{Node: "gate", Action: "reject"}}
	final := r2.Run(context.Background())

	// rejected is not a failure: the run completes, only the rejected edge ran.
	if final.Status != RunCompleted {
		t.Fatalf("status = %q (%q)", final.Status, final.Error)
	}
	if final.Nodes["gate"].Status != NodeRejected || final.Nodes["gate"].Human.Action != "reject" {
		t.Fatalf("gate = %+v", final.Nodes["gate"])
	}
	if final.Nodes["on_no"].Status != NodeSuccess || final.Nodes["on_no"].Branch != WhenRejected {
		t.Fatalf("on_no = %+v", final.Nodes["on_no"])
	}
	if final.Nodes["on_yes"].Status != NodeSkipped || final.Nodes["on_yes"].SkippedBy != "gate" {
		t.Fatalf("on_yes = %+v", final.Nodes["on_yes"])
	}
}

func TestRunnerApprovalNodeWaitsForApproval(t *testing.T) {
	def := defWith(
		Node{ID: "a", Agent: "ag_a", Task: "t", Approval: true, Expect: &Expect{Path: "draft.md"}},
		Node{ID: "b", Agent: "ag_b", Task: "t", After: deps("a")},
	)
	workspace := t.TempDir()
	rec := &recordedRun{}
	r := &Runner{
		Record: startRecord("run_h3"),
		Executor: rec.executor(func(_ context.Context, in NodeInput) (NodeResult, error) {
			return NodeResult{Artifact: "draft.md", Summary: "draft done"}, nil
		}),
		Sink:      rec.sink(),
		Workspace: workspace,
	}
	r.Record.Definition = def
	suspended := r.Run(context.Background())

	a := suspended.Nodes["a"]
	if a.Status != NodeWaitingHuman || a.Human == nil || !strings.Contains(a.Human.Prompt, "draft done") || !strings.Contains(a.Human.Prompt, "draft.md") {
		t.Fatalf("a = %+v", a)
	}
	// The agent's result is stashed in the waiting state, b never started.
	if a.Summary != "draft done" || a.Artifact != "draft.md" {
		t.Fatalf("stashed result = %+v", a)
	}
	if suspended.Nodes["b"].Status != NodeWaiting || strings.Join(rec.executed, ",") != "a" {
		t.Fatalf("b = %+v executed = %v", suspended.Nodes["b"], rec.executed)
	}

	r2 := &Runner{Record: suspended, Executor: r.Executor, Sink: rec.sink(), Workspace: workspace,
		Resume: &HumanDecision{Node: "a", Action: "approve"}}
	final := r2.Run(context.Background())
	if final.Status != RunCompleted {
		t.Fatalf("status = %q", final.Status)
	}
	// Approve keeps the agent's own artifact and summary.
	if final.Nodes["a"].Status != NodeSuccess || final.Nodes["a"].Artifact != "draft.md" || final.Nodes["a"].Summary != "draft done" {
		t.Fatalf("a = %+v", final.Nodes["a"])
	}
	if final.Nodes["b"].Status != NodeSuccess {
		t.Fatalf("b = %+v", final.Nodes["b"])
	}
}

func TestRunnerApprovalRejectSettlesRejected(t *testing.T) {
	def := defWith(Node{ID: "a", Agent: "ag_a", Task: "t", Approval: true})
	rec := &recordedRun{}
	r := &Runner{
		Record: startRecord("run_h4"),
		Executor: rec.executor(func(_ context.Context, _ NodeInput) (NodeResult, error) {
			return NodeResult{Summary: "draft"}, nil
		}),
		Sink: rec.sink(),
	}
	r.Record.Definition = def
	suspended := r.Run(context.Background())
	r2 := &Runner{Record: suspended, Executor: r.Executor, Sink: rec.sink(),
		Resume: &HumanDecision{Node: "a", Action: "reject"}}
	final := r2.Run(context.Background())
	if final.Status != RunCompleted || final.Nodes["a"].Status != NodeRejected {
		t.Fatalf("status = %q a = %+v", final.Status, final.Nodes["a"])
	}
}

func TestRunnerHumanTimeoutContract(t *testing.T) {
	build := func(onTimeout string) (*recordedRun, *Runner) {
		n := Node{ID: "gate", Kind: KindHuman, Prompt: "ok?"}
		n.OnTimeout = onTimeout
		def := defWith(n)
		rec := &recordedRun{}
		r := &Runner{Record: startRecord("run_ht"), Sink: rec.sink(),
			Executor: rec.executor(func(context.Context, NodeInput) (NodeResult, error) { return NodeResult{}, nil })}
		r.Record.Definition = def
		return rec, r
	}

	t.Run("default reject", func(t *testing.T) {
		rec, r := build("")
		suspended := r.Run(context.Background())
		r2 := &Runner{Record: suspended, Executor: r.Executor, Sink: rec.sink(), Resume: &HumanDecision{Node: "gate", Timeout: true}}
		final := r2.Run(context.Background())
		if final.Status != RunCompleted || final.Nodes["gate"].Status != NodeRejected || final.Nodes["gate"].Human.Action != "timeout" {
			t.Fatalf("status = %q gate = %+v", final.Status, final.Nodes["gate"])
		}
	})

	t.Run("fail", func(t *testing.T) {
		rec, r := build("fail")
		suspended := r.Run(context.Background())
		r2 := &Runner{Record: suspended, Executor: r.Executor, Sink: rec.sink(), Resume: &HumanDecision{Node: "gate", Timeout: true}}
		final := r2.Run(context.Background())
		if final.Status != RunFailed || final.Nodes["gate"].Status != NodeFailed || !strings.Contains(final.Nodes["gate"].Error, "timed out") {
			t.Fatalf("status = %q gate = %+v", final.Status, final.Nodes["gate"])
		}
	})
}

func TestRunnerSuspendResumeDoesNotResetRunTimer(t *testing.T) {
	// The runner's Timeout bounds one execution segment; a suspended run that
	// resumes starts a fresh segment. Two segments of 60ms each under a 5s
	// timeout settle fine — a shared clock would not matter here, but a
	// reset-per-segment bug would show as an instant timeout on resume.
	def := defWith(Node{ID: "gate", Kind: KindHuman, Prompt: "ok?"})
	rec := &recordedRun{}
	r := &Runner{Record: startRecord("run_hr"), Sink: rec.sink(), Timeout: 5 * time.Second, Workspace: t.TempDir(),
		Executor: rec.executor(func(context.Context, NodeInput) (NodeResult, error) { return NodeResult{}, nil })}
	r.Record.Definition = def
	suspended := r.Run(context.Background())
	time.Sleep(60 * time.Millisecond)
	r2 := &Runner{Record: suspended, Executor: r.Executor, Sink: rec.sink(), Timeout: 5 * time.Second, Workspace: r.Workspace,
		Resume: &HumanDecision{Node: "gate", Action: "approve"}}
	final := r2.Run(context.Background())
	if final.Status != RunCompleted {
		t.Fatalf("status = %q (%q)", final.Status, final.Error)
	}
}

func TestRunnerHumanPromptEvalFailureFailsNode(t *testing.T) {
	def := defWith(
		Node{ID: "a", Agent: "ag_a", Task: "t", Expect: &Expect{Path: "a.md"}},
		Node{ID: "gate", Kind: KindHuman, Prompt: "see {{ nodes.a.artifact }}", After: deps("a")},
	)
	rec := &recordedRun{}
	r := &Runner{
		Record: startRecord("run_hf"),
		Executor: rec.executor(func(_ context.Context, _ NodeInput) (NodeResult, error) {
			return NodeResult{Summary: "no artifact"}, nil
		}),
		Sink: rec.sink(),
	}
	r.Record.Definition = def
	final := r.Run(context.Background())
	gate := final.Nodes["gate"]
	if gate.Status != NodeFailed || !strings.Contains(gate.Error, "nodes.a.artifact") {
		t.Fatalf("gate = %+v", gate)
	}
	if final.Status != RunFailed {
		t.Fatalf("status = %q", final.Status)
	}
}

func TestRunnerDispatchesRideTheRecord(t *testing.T) {
	def := defWith(Node{ID: "orch", Agent: "ag_o", Task: "t",
		Orchestrate: &Orchestrate{Candidates: []Candidate{{Agent: "ag_x", Hint: "h"}}}})
	rec := &recordedRun{}
	r := &Runner{
		Record: startRecord("run_d"),
		Executor: rec.executor(func(_ context.Context, _ NodeInput) (NodeResult, error) {
			return NodeResult{
				Summary: "orch done",
				Dispatches: []DispatchRecord{
					{Candidate: "ag_x", Task: "sub", Status: NodeSuccess, Summary: "sub done", StartedAt: "2026-01-01T00:00:00Z", EndedAt: "2026-01-01T00:00:01Z"},
				},
			}, nil
		}),
		Sink: rec.sink(),
	}
	r.Record.Definition = def
	final := r.Run(context.Background())
	ds := final.Nodes["orch"].Dispatches
	if len(ds) != 1 || ds[0].Candidate != "ag_x" || ds[0].Status != NodeSuccess {
		t.Fatalf("dispatches = %+v", ds)
	}
	// Deep copy: mutating an earlier event's snapshot must not reach the record.
	for _, e := range rec.events {
		if len(e.Record.Nodes["orch"].Dispatches) > 0 {
			e.Record.Nodes["orch"].Dispatches[0].Summary = "corrupted"
		}
	}
	if final.Nodes["orch"].Dispatches[0].Summary != "sub done" {
		t.Fatalf("record shares memory with an old snapshot: %+v", final.Nodes["orch"].Dispatches)
	}
}

func TestValidateHumanAndOrchestrateShape(t *testing.T) {
	cases := []struct {
		name string
		node Node
		want string
	}{
		{"human ok", Node{ID: "h", Kind: KindHuman, Prompt: "ok?"}, ""},
		{"human no prompt", Node{ID: "h", Kind: KindHuman}, "prompt is required"},
		{"human with agent", Node{ID: "h", Kind: KindHuman, Prompt: "x", Agent: "ag_a"}, "human nodes take no"},
		{"human with expect", Node{ID: "h", Kind: KindHuman, Prompt: "x", Expect: &Expect{Path: "x.md"}}, "human nodes take no"},
		{"bad kind", Node{ID: "h", Kind: "robot", Task: "t", Agent: "ag_a"}, "invalid kind"},
		{"bad on_timeout", Node{ID: "h", Kind: KindHuman, Prompt: "x", OnTimeout: "skip"}, "invalid on_timeout"},
		{"bad timeout", Node{ID: "h", Kind: KindHuman, Prompt: "x", Timeout: "forever"}, "invalid timeout"},
		{"select needs options", Node{ID: "h", Kind: KindHuman, Prompt: "x", Fields: []FieldSpec{{Key: "k", Type: "select"}}}, "needs options"},
		{"duplicate field", Node{ID: "h", Kind: KindHuman, Prompt: "x", Fields: []FieldSpec{{Key: "k"}, {Key: "k"}}}, "duplicate field key"},
		{"actions on plain agent", Node{ID: "h", Agent: "ag_a", Task: "t", Actions: []string{"yes"}}, "belong to human or approval"},
		{"fields on plain agent", Node{ID: "h", Agent: "ag_a", Task: "t", Fields: []FieldSpec{{Key: "k"}}}, "belong to human nodes"},
		{"orchestrate ok", Node{ID: "o", Agent: "ag_a", Task: "t", Orchestrate: &Orchestrate{Candidates: []Candidate{{Agent: "ag_b"}}}}, ""},
		{"orchestrate empty", Node{ID: "o", Agent: "ag_a", Task: "t", Orchestrate: &Orchestrate{}}, "at least one candidate"},
		{"orchestrate dup", Node{ID: "o", Agent: "ag_a", Task: "t", Orchestrate: &Orchestrate{Candidates: []Candidate{{Agent: "ag_b"}, {Agent: "ag_b"}}}}, "duplicate candidate"},
		{"orchestrate budget", Node{ID: "o", Agent: "ag_a", Task: "t", Orchestrate: &Orchestrate{Candidates: []Candidate{{Agent: "ag_b"}}, MaxDispatches: 21}}, "max_dispatches"},
		{"approval ok", Node{ID: "o", Agent: "ag_a", Task: "t", Approval: true, Timeout: "1h"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := defWith(tc.node).Validate()
			if tc.want == "" && err != nil {
				t.Fatalf("Validate() = %v, want clean", err)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("Validate() = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestHumanArtifactRefValidatesWithoutExpect(t *testing.T) {
	// A human node produces _human/<id>.json on approve, so downstream may
	// reference its artifact without an expect declaration.
	d := defWith(
		Node{ID: "gate", Kind: KindHuman, Prompt: "ok?"},
		Node{ID: "b", Agent: "ag_b", Task: "read {{ nodes.gate.artifact }}", After: deps("gate")},
	)
	if err := d.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

func TestSuspendedRunCancellationSettlesNodes(t *testing.T) {
	// A run cancelled while suspended settles every open node cancelled, by
	// the platform's hand (no runner in flight) — exercised here through the
	// record the runner left behind.
	def := defWith(
		Node{ID: "gate", Kind: KindHuman, Prompt: "ok?"},
		Node{ID: "b", Agent: "ag_b", Task: "t", After: deps("gate")},
	)
	rec := &recordedRun{}
	r := &Runner{Record: startRecord("run_sc"), Sink: rec.sink(),
		Executor: rec.executor(func(context.Context, NodeInput) (NodeResult, error) { return NodeResult{}, nil })}
	r.Record.Definition = def
	suspended := r.Run(context.Background())
	if suspended.Status != RunWorking || IsTerminal(suspended.Status) {
		t.Fatalf("suspended run = %q, want non-terminal working", suspended.Status)
	}
}
