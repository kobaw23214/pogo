package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDepDualWireForm(t *testing.T) {
	var deps []Dep
	if err := json.Unmarshal([]byte(`["a", {"node":"b","when":"failed"}, {"node":"c"}, {"node":"d","when":"always"}]`), &deps); err != nil {
		t.Fatal(err)
	}
	want := []Dep{
		{Node: "a", When: WhenSuccess},
		{Node: "b", When: WhenFailed},
		{Node: "c", When: WhenSuccess},
		{Node: "d", When: WhenAlways},
	}
	if len(deps) != len(want) {
		t.Fatalf("deps = %+v", deps)
	}
	for i := range want {
		if deps[i] != want[i] {
			t.Fatalf("dep %d = %+v, want %+v", i, deps[i], want[i])
		}
	}
	// Success edges round-trip as plain strings — v1 files stay byte-identical;
	// only an explicit non-success condition upgrades to the object shape.
	out, err := json.Marshal(deps)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `["a",{"node":"b","when":"failed"},"c",{"node":"d","when":"always"}]` {
		t.Fatalf("marshal = %s", out)
	}
}

func TestValidateRejectsBadWhen(t *testing.T) {
	d := defWith(
		Node{ID: "a", Agent: "ag_a", Task: "t"},
		Node{ID: "b", Agent: "ag_b", Task: "t", After: []Dep{{Node: "a", When: "sometimes"}}},
	)
	if err := d.Validate(); err == nil || !strings.Contains(err.Error(), "invalid edge condition") {
		t.Fatalf("Validate() = %v, want invalid edge condition", err)
	}
}

func TestValidateCycleThroughConditionalEdges(t *testing.T) {
	d := defWith(
		Node{ID: "a", Agent: "ag_a", Task: "t", After: []Dep{{Node: "b", When: WhenFailed}}},
		Node{ID: "b", Agent: "ag_b", Task: "t", After: []Dep{{Node: "a", When: WhenAlways}}},
	)
	if err := d.Validate(); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("Validate() = %v, want cycle", err)
	}
}

func TestTemplateValidationMatrix(t *testing.T) {
	chain := func(taskB string) Definition {
		return defWith(
			Node{ID: "a", Agent: "ag_a", Task: "t", Expect: &Expect{Path: "a.md"}},
			Node{ID: "b", Agent: "ag_b", Task: taskB, After: deps("a")},
		)
	}
	cases := []struct {
		name string
		def  Definition
		want string // empty: must validate
	}{
		{"summary ref", chain("Check {{ nodes.a.summary }}"), ""},
		{"artifact ref", chain("Read {{ nodes.a.artifact }}"), ""},
		{"workspace ref", defWith(Node{ID: "a", Agent: "ag_a", Task: "work in {{ run.workspace }}"}), ""},
		{"spaced ref", chain("Read {{  nodes.a.artifact  }}"), ""},
		{"unknown reference", chain("Do {{ foo }}"), "unknown template reference"},
		{"bad field", chain("Do {{ nodes.a.report }}"), "unknown template reference"},
		{"unknown node", chain("Do {{ nodes.ghost.summary }}"), "unknown node"},
		{"not upstream", defWith(
			Node{ID: "a", Agent: "ag_a", Task: "t"},
			Node{ID: "b", Agent: "ag_b", Task: "see {{ nodes.a.summary }}"},
		), "not an upstream dependency"},
		{"artifact without expect", defWith(
			Node{ID: "a", Agent: "ag_a", Task: "t"},
			Node{ID: "b", Agent: "ag_b", Task: "read {{ nodes.a.artifact }}", After: deps("a")},
		), "declares no expect"},
		{"transitive closure ok", defWith(
			Node{ID: "a", Agent: "ag_a", Task: "t", Expect: &Expect{Path: "a.md"}},
			Node{ID: "b", Agent: "ag_b", Task: "t", After: deps("a")},
			Node{ID: "c", Agent: "ag_c", Task: "use {{ nodes.a.artifact }}", After: deps("b")},
		), ""},
		{"sibling not in closure", defWith(
			Node{ID: "a", Agent: "ag_a", Task: "t", Expect: &Expect{Path: "a.md"}},
			Node{ID: "b", Agent: "ag_b", Task: "t", After: deps("a")},
			Node{ID: "c", Agent: "ag_c", Task: "use {{ nodes.b.artifact }}", After: deps("a")},
		), "declares no expect|not an upstream"},
		{"dotted node id", defWith(
			Node{ID: "stage.one", Agent: "ag_a", Task: "t", Expect: &Expect{Path: "one.md"}},
			Node{ID: "b", Agent: "ag_b", Task: "use {{ nodes.stage.one.artifact }}", After: deps("stage.one")},
		), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.def.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want clean", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() clean, want %q", tc.want)
			}
			matched := false
			for _, alt := range strings.Split(tc.want, "|") {
				matched = matched || strings.Contains(err.Error(), alt)
			}
			if !matched {
				t.Fatalf("Validate() = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestEdgeActiveFourConditions(t *testing.T) {
	statuses := []string{NodeSuccess, NodeFailed, NodeRejected, NodeSkipped, NodeCancelled}
	want := map[string][]bool{
		WhenSuccess:  {true, false, false, false, false},
		WhenFailed:   {false, true, false, false, false},
		WhenRejected: {false, false, true, false, false},
		WhenAlways:   {true, true, true, false, false},
	}
	for when, expects := range want {
		for i, st := range statuses {
			if got := edgeActive(when, st); got != expects[i] {
				t.Errorf("edgeActive(%s, %s) = %v, want %v", when, st, got, expects[i])
			}
		}
	}
}
