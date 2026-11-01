package agentworkflow

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func conditionGraphFixture() Definition {
	return Definition{Name: "分支资料流程", Revision: 1, Inputs: []Input{{Key: "review"}}, Steps: []Step{
		{ID: "route", Kind: "condition", Condition: &Condition{Source: "inputs.review", Operator: "equals", Value: "yes"}},
		{ID: "review", Kind: "confirm", Prompt: "确认资料"},
		{ID: "collect", Kind: "agent", Task: "整理资料", OutputFile: "notes.md"},
		{ID: "merge", Kind: "join"},
		{ID: "report", Kind: "agent", Task: "根据 {{ steps.merge.summary }} 生成报告", OutputFile: "report.md"},
	}, Graph: &Graph{Version: 1, Edges: []Edge{
		{Source: GraphInput, Target: "route"},
		{Source: "route", Target: "review", Branch: "true"},
		{Source: "route", Target: "collect", Branch: "false"},
		{Source: "review", Target: "merge"}, {Source: "collect", Target: "merge"},
		{Source: "merge", Target: "report"}, {Source: "report", Target: GraphOutput},
	}}}
}

func parallelGraphFixture() Definition {
	d := conditionGraphFixture()
	d.Steps[0] = Step{ID: "route", Kind: "parallel"}
	d.Steps[1] = Step{ID: "review", Kind: "agent", Task: "独立检查资料"}
	d.Graph.Edges[1].Branch, d.Graph.Edges[2].Branch = "", ""
	return d
}

func graphDiagnostic(ds []Diagnostic, code string) bool {
	for _, d := range ds {
		if d.Code == code {
			return true
		}
	}
	return false
}

func TestGraphPreservesLegacyHashAndExplicitOptIn(t *testing.T) {
	d := definitionFixture()
	const oldHash = "4e076891a3e522ca69c3d869e6d7ec485ce34b97b2d6fb3a44406af127a414e2"
	if Hash(d) != oldHash {
		t.Fatalf("legacy snapshot hash changed: %s", Hash(d))
	}
	raw, _ := json.Marshal(d)
	if strings.Contains(string(raw), `"graph"`) || strings.Contains(string(raw), `"condition"`) {
		t.Fatal("new optional fields leaked into legacy encoding", string(raw))
	}
	d.Graph = &Graph{Version: 1, Edges: []Edge{{Source: GraphInput, Target: "collect"}, {Source: "collect", Target: "review"}, {Source: "review", Target: "report"}, {Source: "report", Target: GraphOutput}}}
	if ds := Validate(d); len(ds) != 0 {
		t.Fatal(ds)
	}
	if Hash(d) == oldHash {
		t.Fatal("opt-in graph execution did not change reviewed hash")
	}
	d.Graph = nil
	if Hash(d) != oldHash {
		t.Fatal("reading graph-free data changed its legacy hash")
	}
	d.Steps[0] = Step{ID: "collect", Kind: "parallel"}
	if !graphDiagnostic(Validate(d), "invalid_kind") {
		t.Fatal("graph control accepted without explicit graph")
	}
}

func TestGraphValidatesStructureAndGuaranteedReferences(t *testing.T) {
	for i, d := range []Definition{conditionGraphFixture(), parallelGraphFixture()} {
		if ds := Validate(d); len(ds) != 0 {
			t.Fatal(ds)
		}
		want := []string{"route", "merge"}
		if i == 1 {
			want = []string{"route", "review", "collect", "merge"}
		}
		if got := GuaranteedPredecessors(d, "report"); !reflect.DeepEqual(got, want) {
			t.Fatal("guaranteed branch outputs", got)
		}
		d.Steps[4].Task = "{{ steps.collect.summary }}"
		if got := graphDiagnostic(Validate(d), "unknown_step"); got != (i == 0) {
			t.Fatal("conditional arm must remain optional; unconditional parallel result must be available", Validate(d))
		}
	}
	cases := []struct {
		name, code string
		change     func(*Definition)
	}{
		{"unsupported version", "invalid_graph_version", func(d *Definition) { d.Graph.Version = 2 }},
		{"unknown endpoint", "invalid_edge", func(d *Definition) { d.Graph.Edges[0].Target = "absent" }},
		{"self edge", "invalid_edge", func(d *Definition) { d.Graph.Edges[0] = Edge{Source: "route", Target: "route"} }},
		{"reversed boundary", "invalid_edge", func(d *Definition) { d.Graph.Edges[0] = Edge{Source: "route", Target: GraphInput} }},
		{"missing false", "invalid_node_degree", func(d *Definition) { d.Graph.Edges[2].Branch = "true" }},
		{"branch on ordinary", "unexpected_branch", func(d *Definition) { d.Graph.Edges[3].Branch = "true" }},
		{"duplicate edge", "duplicate_edge", func(d *Definition) { d.Graph.Edges = append(d.Graph.Edges, d.Graph.Edges[3]) }},
		{"ordinary missing exit", "invalid_node_degree", func(d *Definition) { d.Graph.Edges = append(d.Graph.Edges[:4], d.Graph.Edges[5:]...) }},
		{"orphan", "unreachable_node", func(d *Definition) { d.Steps = append(d.Steps, Step{ID: "orphan", Kind: "agent", Task: "no path"}) }},
		{"cycle", "graph_cycle", func(d *Definition) { d.Graph.Edges[5] = Edge{Source: "merge", Target: "route"} }},
		{"control instruction", "invalid_control_step", func(d *Definition) { d.Steps[0].Task = "run code" }},
		{"condition elsewhere", "unexpected_condition", func(d *Definition) { d.Steps[2].Condition = d.Steps[0].Condition }},
		{"unknown condition input", "unknown_input", func(d *Definition) { d.Steps[0].Condition.Source = "inputs.absent" }},
		{"future condition result", "unknown_step", func(d *Definition) { d.Steps[0].Condition.Source = "steps.report.summary" }},
		{"expression condition", "invalid_condition_source", func(d *Definition) { d.Steps[0].Condition.Source = "inputs.review || run()" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := conditionGraphFixture()
			tc.change(&d)
			if ds := Validate(d); !graphDiagnostic(ds, tc.code) {
				t.Fatal(tc.code, ds)
			}
		})
	}
	d := conditionGraphFixture()
	// Both empty condition branches converge on one join with distinct labels.
	d.Steps = []Step{d.Steps[0], d.Steps[3], d.Steps[4]}
	d.Graph.Edges = []Edge{{Source: GraphInput, Target: "route"}, {Source: "route", Target: "merge", Branch: "true"}, {Source: "route", Target: "merge", Branch: "false"}, {Source: "merge", Target: "report"}, {Source: "report", Target: GraphOutput}}
	if ds := Validate(d); len(ds) != 0 {
		t.Fatal("empty conditional branch should be explicit and valid", ds)
	}
}

func TestGraphInvalidStructureDefersOnlyUncertainReferences(t *testing.T) {
	d := conditionGraphFixture()
	d.Steps = append(d.Steps, Step{ID: "prefix", Kind: "agent", Task: "读取资料"})
	d.Graph.Edges[0].Target = "prefix"
	d.Graph.Edges = append(d.Graph.Edges, Edge{Source: "prefix", Target: "route"})
	d.Steps[0].Condition.Source = "steps.prefix.summary"
	d.Steps[1].Prompt = "确认 {{ steps.prefix.summary }}"
	d.Steps[4].Task = "汇总 {{ steps.merge.summary }}；错误引用 {{ steps.missing_node.summary }} 和 {{ steps.report.summary }}"
	d.Steps[2].Task = "读取 {{ inputs.absent }}"
	d.Steps[2].OutputFile = "report.md"
	// The exit is disconnected, so no dominator result is available. The
	// prefix/merge references are not independent reference errors.
	for i, edge := range d.Graph.Edges {
		if edge.Target == GraphOutput {
			d.Graph.Edges = append(d.Graph.Edges[:i], d.Graph.Edges[i+1:]...)
			break
		}
	}
	diagnostics := Validate(d)
	if !graphDiagnostic(diagnostics, "unreachable_node") || !graphDiagnostic(diagnostics, "invalid_output_file") || !graphDiagnostic(diagnostics, "unknown_input") {
		t.Fatal("independent diagnostics were hidden", diagnostics)
	}
	var references []Diagnostic
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == "unknown_step" {
			references = append(references, diagnostic)
			if diagnostic.StepID != "report" || !strings.Contains(diagnostic.Message, "missing_node") && !strings.HasSuffix(diagnostic.Message, ":report") && !strings.HasSuffix(diagnostic.Message, "：report") {
				t.Fatal("declared reference was diagnosed using an unavailable graph analysis", diagnostic)
			}
		}
	}
	if len(references) != 2 {
		t.Fatal("missing node and self-reference must still be diagnosed", references)
	}

	for _, source := range []string{"steps.route.summary", "steps.absent.summary", "steps.report.summary"} {
		t.Run(source, func(t *testing.T) {
			candidate := conditionGraphFixture()
			candidate.Graph.Edges = candidate.Graph.Edges[:len(candidate.Graph.Edges)-1]
			candidate.Steps[0].Condition.Source = source
			expectCertainError := source != "steps.report.summary"
			if got := graphDiagnostic(Validate(candidate), "unknown_step"); got != expectCertainError {
				t.Fatalf("invalid graph reference diagnosis for %s: %v", source, Validate(candidate))
			}
			candidate.Graph.Edges = append(candidate.Graph.Edges, Edge{Source: "report", Target: GraphOutput})
			if !graphDiagnostic(Validate(candidate), "unknown_step") {
				t.Fatal("repaired graph stopped enforcing strict predecessor references", source)
			}
		})
	}
}

func TestGraphPatchIsAtomicAcrossNodesAndConnections(t *testing.T) {
	d := conditionGraphFixture()
	before := Hash(d)
	_, err := Patch(d, []Operation{{Op: "remove_step", StepID: "collect"}})
	if err == nil || Hash(d) != before {
		t.Fatal("dangling-edge removal changed original", err)
	}
	graph := *d.Graph
	graph.Edges = append([]Edge(nil), graph.Edges...)
	graph.Edges[2].Target = "merge"
	graph.Edges = append(graph.Edges[:4], graph.Edges[5:]...)
	next, err := Patch(d, []Operation{{Op: "remove_step", StepID: "collect"}, {Op: "set_graph", Graph: &graph}})
	if err != nil || len(next.Steps) != 4 || Hash(d) != before {
		t.Fatal("atomic node/edge patch failed", err)
	}
	_, err = Patch(d, []Operation{{Op: "set_graph"}})
	if err == nil {
		t.Fatal("implicit graph removal accepted")
	}
	_, err = Patch(d, []Operation{{Op: "update_step", StepID: "route", Patch: json.RawMessage(`{"condition":{"source":"inputs.review","operator":"equals","value":"no"}}`)}})
	if err != nil {
		t.Fatal(err)
	}
	independent, err := Patch(d, []Operation{{Op: "set_graph", Graph: d.Graph}})
	if err != nil {
		t.Fatal(err)
	}
	independent.Graph.Edges[0].Target = "changed"
	if Hash(d) != before {
		t.Fatal("returned graph aliases the caller's original definition")
	}
}

func TestConditionEvaluationIsTypedLiteralAndFinite(t *testing.T) {
	cases := []struct {
		operator, input, value string
		want                   bool
	}{
		{"equals", "{{ shell() }}", "{{ shell() }}", true}, {"not_equals", "yes", "no", true},
		{"contains", "保留来源", "来源", true}, {"not_contains", "保留来源", "预算", true},
		{"is_empty", " \n", "", true}, {"not_empty", "0", "", true},
		{"gt", " 4.5 ", "4", true}, {"gte", "4", "4", true}, {"lt", "-1", "0", true}, {"lte", "4", "3", false},
		{"gt", ".5", "1e-1", true}, {"gte", "+5.", "5", true}, {"lt", "-1e2", "-99", true},
	}
	for _, tc := range cases {
		t.Run(tc.operator, func(t *testing.T) {
			step := Step{ID: "route", Kind: "condition", Condition: &Condition{Source: "inputs.value", Operator: tc.operator, Value: tc.value}}
			got, err := EvaluateCondition(step, map[string]string{"value": tc.input}, nil)
			if err != nil || got != tc.want {
				t.Fatal(got, err)
			}
		})
	}
	for _, input := range []string{"NaN", "Inf", "1e10000", "not-a-number", "", "0x1p2", "0x10", "1_000"} {
		step := Step{ID: "route", Kind: "condition", Condition: &Condition{Source: "inputs.value", Operator: "gt", Value: "1"}}
		if _, err := EvaluateCondition(step, map[string]string{"value": input}, nil); err == nil {
			t.Fatal("invalid number became false success", input)
		}
	}
	step := Step{ID: "route", Kind: "condition", Condition: &Condition{Source: "steps.collect.summary", Operator: "contains", Value: "ok"}}
	if _, err := EvaluateCondition(step, nil, []StepRun{{StepID: "collect", Status: Running, Summary: "ok"}}); err == nil {
		t.Fatal("read uncommitted condition data")
	}
	d := conditionGraphFixture()
	d.Steps[0].Condition.Operator = "gt"
	for _, value := range []string{"NaN", "0x1p2", "0x10", "1_000"} {
		d.Steps[0].Condition.Value = value
		if !graphDiagnostic(Validate(d), "invalid_condition_number") {
			t.Fatal("nondecimal or nonfinite literal saved", value)
		}
	}
}

func TestGraphPlannerSkipsUnselectedBranchesAndWaitsAtJoin(t *testing.T) {
	d := conditionGraphFixture()
	p, err := PlanGraph(d, nil)
	if err != nil || !reflect.DeepEqual(p.Ready, []string{"route"}) {
		t.Fatal(p, err)
	}
	state := []StepRun{{StepID: "route", Status: Completed, Branch: "false"}}
	p, err = PlanGraph(d, state)
	if err != nil || !reflect.DeepEqual(p.Ready, []string{"collect"}) || !reflect.DeepEqual(p.Skipped, []string{"review"}) {
		t.Fatal(p, err)
	}
	state = append(state, StepRun{StepID: "review", Status: Skipped}, StepRun{StepID: "collect", Status: Completed})
	p, err = PlanGraph(d, state)
	if err != nil || !reflect.DeepEqual(p.Ready, []string{"merge"}) {
		t.Fatal(p, err)
	}
	state = append(state, StepRun{StepID: "merge", Status: Completed}, StepRun{StepID: "report", Status: Completed})
	p, err = PlanGraph(d, state)
	if err != nil || !p.Done || len(p.Ready) != 0 {
		t.Fatal(p, err)
	}
	d = parallelGraphFixture()
	state = []StepRun{{StepID: "route", Status: Completed}}
	p, err = PlanGraph(d, state)
	if err != nil || !reflect.DeepEqual(p.Ready, []string{"review", "collect"}) || len(p.Skipped) != 0 {
		t.Fatal(p, err)
	}
	state = append(state, StepRun{StepID: "review", Status: Completed}, StepRun{StepID: "collect", Status: Running})
	p, err = PlanGraph(d, state)
	if err != nil || len(p.Ready) != 0 || p.Done {
		t.Fatal("join ran before sibling completed", p, err)
	}
	state[2].Status = Failed
	if _, err = PlanGraph(d, state); err == nil {
		t.Fatal("failed branch treated as successful inactive path")
	}
	d = conditionGraphFixture()
	if _, err = PlanGraph(d, []StepRun{{StepID: "route", Status: Completed}}); err == nil {
		t.Fatal("condition without durable decision advanced")
	}
}

func TestGraphReservesFrozenInputNamespaceWithoutChangingLegacy(t *testing.T) {
	for _, name := range []string{"inputs", "inputs/01-source.md", "Inputs/01-source.md"} {
		d := conditionGraphFixture()
		d.Steps[2].OutputFile = name
		if !graphDiagnostic(Validate(d), "reserved_output_path") {
			t.Fatal("graph output can collide with frozen inputs", name)
		}
		legacy := definitionFixture()
		legacy.Steps[0].OutputFile = name
		if ds := Validate(legacy); len(ds) != 0 {
			t.Fatal("legacy validation contract changed", name, ds)
		}
	}
	d := conditionGraphFixture()
	d.Steps[2].OutputFile = "inputs-summary.md"
	if ds := Validate(d); len(ds) != 0 {
		t.Fatal(ds)
	}
}
