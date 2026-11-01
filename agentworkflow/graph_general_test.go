package agentworkflow

import (
	"encoding/json"
	"reflect"
	"testing"
)

func generalDiamondFixture() Definition {
	return Definition{Name: "通用节点分流汇合", Revision: 1, Steps: []Step{
		{ID: "seed", Kind: "agent", Task: "准备"},
		{ID: "left", Kind: "agent", Task: "处理左路 {{ steps.seed.summary }}"},
		{ID: "right", Kind: "agent", Task: "处理右路 {{ steps.seed.summary }}"},
		{ID: "review", Kind: "confirm", Prompt: "核对两路 {{ steps.left.summary }} / {{ steps.right.summary }}"},
		{ID: "publish", Kind: "agent", Task: "保存核对结果 {{ steps.review.feedback }}"},
		{ID: "notify", Kind: "agent", Task: "整理核对结果 {{ steps.review.feedback }}"},
	}, Graph: &Graph{Version: 1, Edges: []Edge{
		{Source: GraphInput, Target: "seed"},
		{Source: "seed", Target: "left"}, {Source: "seed", Target: "right"},
		{Source: "left", Target: "review"}, {Source: "right", Target: "review"},
		{Source: "review", Target: "publish"}, {Source: "review", Target: "notify"},
		{Source: "publish", Target: GraphOutput}, {Source: "notify", Target: GraphOutput},
	}}}
}

func TestGeneralGraphAllowsImplicitForkFanInAndMultipleBoundaryEdges(t *testing.T) {
	d := generalDiamondFixture()
	before, _ := json.Marshal(d)
	if ds := Validate(d); len(ds) != 0 {
		t.Fatal(ds)
	}
	if got := GuaranteedPredecessors(d, "review"); !reflect.DeepEqual(got, []string{"seed", "left", "right"}) {
		t.Fatal("both branches must be safe inputs after fan-in", got)
	}
	if got := GuaranteedPredecessors(d, "left"); !reflect.DeepEqual(got, []string{"seed"}) {
		t.Fatal("a sibling that is not an ancestor must not be exposed", got)
	}
	_, _ = PlanGraph(d, nil)
	after, _ := json.Marshal(d)
	if string(before) != string(after) {
		t.Fatal("analysis or planning changed the saved definition")
	}
	// A start can fan out directly too, without inserting a synthetic control.
	d.Steps = d.Steps[1:]
	d.Steps[0].Task, d.Steps[1].Task = "左路", "右路"
	d.Graph.Edges = append([]Edge{{Source: GraphInput, Target: "left"}, {Source: GraphInput, Target: "right"}}, d.Graph.Edges[3:]...)
	if ds := Validate(d); len(ds) != 0 {
		t.Fatal("multiple start/end connections", ds)
	}
}

func TestGeneralGraphFanInWaitsForBothPathsAndExecutesOnce(t *testing.T) {
	d := generalDiamondFixture()
	check := func(states []StepRun, ready []string, done bool) {
		t.Helper()
		p, err := PlanGraph(d, states)
		if err != nil || !reflect.DeepEqual(p.Ready, ready) || p.Done != done || len(p.Skipped) != 0 {
			t.Fatalf("plan %+v err=%v", p, err)
		}
	}
	check(nil, []string{"seed"}, false)
	states := []StepRun{{StepID: "seed", Status: Completed}}
	check(states, []string{"left", "right"}, false)
	states = append(states, StepRun{StepID: "left", Status: Completed}, StepRun{StepID: "right", Status: Running})
	check(states, []string{}, false)
	states[2].Status = Completed
	check(states, []string{"review"}, false)
	states = append(states, StepRun{StepID: "review", Status: WaitingConfirmation})
	check(states, []string{}, false)
	states[3].Status = Completed
	check(states, []string{"publish", "notify"}, false)
	states = append(states, StepRun{StepID: "publish", Status: Completed}, StepRun{StepID: "notify", Status: Running})
	check(states, []string{}, false)
	states[5].Status = Completed
	check(states, []string{}, true)
	check(states, []string{}, true)
	states[2].Status = Failed
	if _, err := PlanGraph(d, states); err == nil {
		t.Fatal("a failed input became successful fan-in")
	}
}

func generalConditionFixture() Definition {
	return Definition{Name: "条件多路汇合", Revision: 1, Inputs: []Input{{Key: "mode"}}, Steps: []Step{
		{ID: "pre_left", Kind: "agent", Task: "前置左路"},
		{ID: "pre_right", Kind: "agent", Task: "前置右路"},
		{ID: "route", Kind: "condition", Condition: &Condition{Source: "inputs.mode", Operator: "equals", Value: "yes"}},
		{ID: "left", Kind: "agent", Task: "条件左路"},
		{ID: "right", Kind: "agent", Task: "条件右路"},
		{ID: "branch_review", Kind: "confirm", Prompt: "核对同一已选分支中的两项 {{ steps.left.summary }} / {{ steps.right.summary }}"},
		{ID: "fallback", Kind: "agent", Task: "另一条件分支"},
		{ID: "merge", Kind: "join"},
		{ID: "finish", Kind: "agent", Task: "根据已选路径汇聚 {{ steps.merge.summary }}"},
	}, Graph: &Graph{Version: 1, Edges: []Edge{
		{Source: GraphInput, Target: "pre_left"}, {Source: GraphInput, Target: "pre_right"},
		{Source: "pre_left", Target: "route"}, {Source: "pre_right", Target: "route"},
		{Source: "route", Target: "left", Branch: "true"}, {Source: "route", Target: "right", Branch: "true"},
		{Source: "route", Target: "fallback", Branch: "false"},
		{Source: "left", Target: "branch_review"}, {Source: "right", Target: "branch_review"},
		{Source: "branch_review", Target: "merge"}, {Source: "fallback", Target: "merge"},
		{Source: "merge", Target: "finish"}, {Source: "finish", Target: GraphOutput},
	}}}
}

func TestGeneralGraphConditionFanOutSkipsInactivePathsWithoutWaitingForever(t *testing.T) {
	d := generalConditionFixture()
	if ds := Validate(d); len(ds) != 0 {
		t.Fatal(ds)
	}
	for _, branch := range []string{"true", "false"} {
		t.Run(branch, func(t *testing.T) {
			states := []StepRun{{StepID: "pre_left", Status: Completed}, {StepID: "pre_right", Status: Completed}, {StepID: "route", Status: Completed, Branch: branch}}
			p, err := PlanGraph(d, states)
			if err != nil {
				t.Fatal(err)
			}
			if branch == "true" {
				if !reflect.DeepEqual(p.Ready, []string{"left", "right"}) || !reflect.DeepEqual(p.Skipped, []string{"fallback"}) {
					t.Fatal(p)
				}
				states = append(states, StepRun{StepID: "left", Status: Completed}, StepRun{StepID: "right", Status: Running}, StepRun{StepID: "fallback", Status: Skipped})
				if p, _ = PlanGraph(d, states); len(p.Ready) != 0 {
					t.Fatal("fan-in ran before the slower active sibling", p)
				}
				states[4].Status = Completed
				if p, _ = PlanGraph(d, states); !reflect.DeepEqual(p.Ready, []string{"branch_review"}) {
					t.Fatal(p)
				}
				states = append(states, StepRun{StepID: "branch_review", Status: Completed})
			} else {
				if !reflect.DeepEqual(p.Ready, []string{"fallback"}) || !reflect.DeepEqual(p.Skipped, []string{"left", "right", "branch_review"}) {
					t.Fatal("the inactive arm should resolve through fan-in in one plan", p)
				}
				states = append(states, StepRun{StepID: "fallback", Status: Completed}, StepRun{StepID: "left", Status: Skipped}, StepRun{StepID: "right", Status: Skipped}, StepRun{StepID: "branch_review", Status: Skipped})
			}
			p, err = PlanGraph(d, states)
			if err != nil || !reflect.DeepEqual(p.Ready, []string{"merge"}) {
				t.Fatal("join hung on an unselected path", p, err)
			}
		})
	}
}

func TestGeneralGraphReferencesNeverAssumeOptionalConditionResults(t *testing.T) {
	d := generalConditionFixture()
	if got := GuaranteedPredecessors(d, "branch_review"); !reflect.DeepEqual(got, []string{"pre_left", "pre_right", "route", "left", "right"}) {
		t.Fatal("two branches of the same selected arm should both be guaranteed", got)
	}
	guaranteed := GuaranteedPredecessors(d, "finish")
	for _, id := range guaranteed {
		if id == "left" || id == "right" || id == "branch_review" || id == "fallback" {
			t.Fatal("a possibly skipped result became a required value", guaranteed)
		}
	}
	d.Steps[8].Task = "{{ steps.left.summary }}"
	if !graphDiagnostic(Validate(d), "unknown_step") {
		t.Fatal("optional arm reference accepted after reconvergence")
	}
	d = generalDiamondFixture()
	d.Graph.Edges = append(d.Graph.Edges, Edge{Source: "notify", Target: "left"})
	if !graphDiagnostic(Validate(d), "graph_cycle") {
		t.Fatal("arbitrary back edge became an unbounded loop")
	}
}
