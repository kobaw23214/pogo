package workflow

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestRunInputsSurviveHumanPauseAndRecordReload(t *testing.T) {
	def := defWith(
		Node{ID: "review", Kind: KindHuman, Prompt: "审核 {{ inputs.order_id }}"},
		Node{ID: "finish", Agent: "ag_a", Task: "订单 {{inputs.order_id}}：{{ inputs.note }}", After: deps("review")},
	)
	if err := def.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := def.InputKeys(); !reflect.DeepEqual(got, []string{"note", "order_id"}) {
		t.Fatalf("input keys = %v", got)
	}
	log := &recordedRun{}
	original := startRecord("run_inputs")
	original.Definition = def
	original.Inputs = map[string]string{"order_id": "ORDER-123", "note": "literal {{ nodes.secret.summary }}"}
	runner := &Runner{Record: original, Sink: log.sink(), Workspace: t.TempDir()}
	paused := runner.Run(context.Background())
	if paused.Status != RunWorking || paused.Nodes["review"].Human.Prompt != "审核 ORDER-123" {
		t.Fatalf("paused record = %+v", paused)
	}
	// The authoritative serialized record alone must be enough to continue.
	raw, err := json.Marshal(paused)
	if err != nil {
		t.Fatal(err)
	}
	var reloaded RunRecord
	if err := json.Unmarshal(raw, &reloaded); err != nil {
		t.Fatal(err)
	}
	original.Inputs["order_id"] = "MUTATED"
	if log.events[0].Record.Inputs["order_id"] != "ORDER-123" {
		t.Fatal("event snapshot shares the caller's input map")
	}
	resumed := &Runner{Record: reloaded, Resume: &HumanDecision{Node: "review", Action: "approve"}, Workspace: runner.Workspace,
		Executor: func(_ context.Context, in NodeInput) (NodeResult, error) {
			if in.Node.Task != "订单 ORDER-123：literal {{ nodes.secret.summary }}" {
				t.Errorf("task was not resolved exactly once: %q", in.Node.Task)
			}
			return NodeResult{Summary: "done"}, nil
		}}
	final := resumed.Run(context.Background())
	if final.Status != RunCompleted || final.ID != original.ID || final.Inputs["order_id"] != "ORDER-123" {
		t.Fatalf("resumed record = %+v", final)
	}
	if !strings.Contains(final.Definition.Nodes[1].Task, "{{inputs.order_id}}") {
		t.Fatal("input values changed the immutable definition")
	}
}

func TestRunMissingInputFailsBeforeCallingExecutor(t *testing.T) {
	def := defWith(Node{ID: "a", Agent: "ag_a", Task: "订单 {{inputs.order_id}}"})
	if err := def.Validate(); err != nil {
		t.Fatal(err)
	}
	called := false
	runner := &Runner{Record: startRecord("run_missing_input"), Executor: func(context.Context, NodeInput) (NodeResult, error) {
		called = true
		return NodeResult{}, nil
	}}
	runner.Record.Definition = def
	final := runner.Run(context.Background())
	if called || final.Status != RunFailed || !strings.Contains(final.Error, "inputs.order_id") {
		t.Fatalf("called=%v record=%+v", called, final)
	}
	for _, invalid := range []string{"inputs.", "inputs.a.b", "inputs.order-id", "inputs.__proto__.x"} {
		bad := defWith(Node{ID: "a", Agent: "ag_a", Task: "{{" + invalid + "}}"})
		if err := bad.Validate(); err == nil {
			t.Fatalf("accepted ambiguous input path %q", invalid)
		}
	}
}
