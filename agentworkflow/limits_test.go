package agentworkflow

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func boundedDefinition(n int, prefix string, graph bool) Definition {
	d := Definition{Name: "独立容量测试", Revision: 1}
	previous := GraphInput
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%s_%03d", prefix, i)
		d.Steps = append(d.Steps, Step{ID: id, Kind: "agent", Task: "整理独立测试资料"})
		if graph {
			if d.Graph == nil {
				d.Graph = &Graph{Version: 1}
			}
			d.Graph.Edges = append(d.Graph.Edges, Edge{Source: previous, Target: id})
			previous = id
		}
	}
	if graph {
		d.Graph.Edges = append(d.Graph.Edges, Edge{Source: previous, Target: GraphOutput})
	}
	return d
}

func TestDefinitionSupportsTwoHundredNodesAndRejectsTwoHundredOne(t *testing.T) {
	for _, graph := range []bool{false, true} {
		d := boundedDefinition(200, "node", graph)
		if diagnostics := Validate(d); len(diagnostics) != 0 {
			t.Fatalf("200 nodes graph=%v: %+v", graph, diagnostics)
		}
		tooMany := boundedDefinition(201, "node", graph)
		diagnostics := Validate(tooMany)
		found := false
		for _, diagnostic := range diagnostics {
			if diagnostic.Code == "invalid_step_count" && strings.Contains(diagnostic.Message, "200") {
				found = true
			}
		}
		if !found {
			t.Fatalf("201 nodes graph=%v did not report the actual boundary: %+v", graph, diagnostics)
		}
	}
}

func TestTwoHundredNodeAtomicReplacementIncludesMetadataAndGraph(t *testing.T) {
	base := boundedDefinition(200, "old", true)
	before := Hash(base)
	target := boundedDefinition(200, "new", true)
	name := "完整原子替换"
	operations := []Operation{{Op: "set_metadata", Name: &name}}
	for _, step := range base.Steps {
		operations = append(operations, Operation{Op: "remove_step", StepID: step.ID})
	}
	for _, step := range target.Steps {
		copy := step
		operations = append(operations, Operation{Op: "add_step", Step: &copy})
	}
	operations = append(operations, Operation{Op: "set_graph", Graph: target.Graph})
	if len(operations) != 402 {
		t.Fatal("fixture no longer covers a full replacement", len(operations))
	}
	next, err := Patch(base, operations)
	if err != nil || len(next.Steps) != 200 || next.Name != name || next.Steps[0].ID != "new_000" || Hash(base) != before {
		t.Fatalf("full replacement failed or mutated the original: %v", err)
	}
	if _, err := Patch(base, append(operations, Operation{Op: "set_metadata", Name: &name})); err == nil {
		t.Fatal("unbounded operation batch accepted")
	}
}

func TestTwoHundredNodeLayoutFitsExistingRequestBudget(t *testing.T) {
	positions := map[string]Position{LayoutInputNode: {}, LayoutOutputNode: {X: 999999.9999999999}}
	for i := 0; i < 200; i++ {
		id := fmt.Sprintf("n%03d%s", i, strings.Repeat("x", 60))
		positions[id] = Position{X: 999999.9999999999, Y: -999999.9999999999}
	}
	if err := ValidateLayout(positions, &Viewport{Zoom: 0.1}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(LayoutSave{RequestID: strings.Repeat("r", 128), ExpectedDefinitionRevision: 1, Positions: positions, Viewport: &Viewport{Zoom: 0.1}})
	if len(raw) >= 64<<10 {
		t.Fatal("200 maximum-length IDs and coordinates no longer fit the layout transport", len(raw))
	}
	positions["extra"] = Position{}
	if ValidateLayout(positions, nil) == nil {
		t.Fatal("layout accepted more than 200 steps plus its two boundary nodes")
	}
}
