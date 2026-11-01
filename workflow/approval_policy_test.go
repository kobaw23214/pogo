package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestApprovalPolicyDraftPublishAndLegacyRoundTrip(t *testing.T) {
	draft := defWith(Node{ID: "review", Kind: KindHuman, Prompt: "review", ApprovalPolicy: &ApprovalPolicy{PreventSelfApproval: true, Assignment: "fixed"}})
	if err := draft.ValidateShape(); err != nil {
		t.Fatalf("incomplete approval draft cannot be saved: %v", err)
	}
	if err := draft.Validate(); err == nil {
		t.Fatal("published an approval node without any fixed approver")
	}
	draft.Nodes[0].ApprovalPolicy.Approvers = []string{"bob"}
	if err := draft.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, n := range []Node{
		{ID: "review", Kind: KindHuman, Prompt: "review", ApprovalPolicy: &ApprovalPolicy{Assignment: "unknown"}},
		{ID: "review", Kind: KindHuman, Prompt: "review", ApprovalPolicy: &ApprovalPolicy{Assignment: "business", Approvers: []string{"bob"}}},
		{ID: "agent", Agent: "ag_a", Task: "work", ApprovalPolicy: &ApprovalPolicy{Assignment: "fixed", Approvers: []string{"bob"}}},
	} {
		if err := defWith(n).Validate(); err == nil {
			t.Fatalf("invalid approval configuration accepted: %+v", n)
		}
	}
	legacy := defWith(Node{ID: "review", Kind: KindHuman, Prompt: "old review"})
	raw, _ := json.Marshal(legacy)
	if strings.Contains(string(raw), "approval_policy") {
		t.Fatal("legacy definition gained a new publish-hash field")
	}
	var restored Definition
	if err := json.Unmarshal(raw, &restored); err != nil || restored.Nodes[0].ApprovalPolicy != nil {
		t.Fatal("legacy approval semantics changed")
	}
}

func TestApprovalPolicySnapshotsDoNotShareEditableCandidates(t *testing.T) {
	policy := &ApprovalPolicy{PreventSelfApproval: true, Approvers: []string{" bob ", "bob", "carol"}}
	def := defWith(Node{ID: "review", Kind: KindHuman, Prompt: "review", ApprovalPolicy: policy})
	def.Normalize()
	if policy.Assignment != "" || len(policy.Approvers) != 3 || policy.Approvers[0] != " bob " {
		t.Fatal("normalization wrote through the saved draft policy")
	}
	if def.Nodes[0].ApprovalPolicy.Assignment != "fixed" || len(def.Nodes[0].ApprovalPolicy.Approvers) != 2 {
		t.Fatal("approval defaults and duplicate candidates did not normalize")
	}
	log := &recordedRun{}
	runner := &Runner{Record: startRecord("run_policy_snapshot"), Sink: log.sink(), Workspace: t.TempDir()}
	runner.Record.Definition = def
	final := runner.Run(context.Background())
	if final.Nodes["review"].Status != NodeWaitingHuman {
		t.Fatalf("human node did not pause: %+v", final)
	}
	def.Nodes[0].ApprovalPolicy.Approvers[0] = "mallory"
	if final.Definition.Nodes[0].ApprovalPolicy.Approvers[0] != "bob" || log.events[0].Record.Definition.Nodes[0].ApprovalPolicy.Approvers[0] != "bob" {
		t.Fatal("caller edits changed a durable/event approval snapshot")
	}
}
