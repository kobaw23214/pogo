package workflow

import (
	"strings"
	"testing"
)

func defWith(nodes ...Node) Definition {
	return Definition{ID: "wf_test", Name: "test", Revision: 1, Nodes: nodes}
}

// deps builds plain success edges — the v1 string form as a Go literal.
func deps(ids ...string) []Dep {
	out := make([]Dep, len(ids))
	for i, id := range ids {
		out[i] = Dep{Node: id, When: WhenSuccess}
	}
	return out
}

func TestValidateAcceptsAPlainChain(t *testing.T) {
	d := defWith(
		Node{ID: "analyze", Agent: "ag_a", Task: "look"},
		Node{ID: "review", Agent: "ag_b", Task: "check", After: deps("analyze")},
	)
	d.Normalize()
	if err := d.Validate(); err != nil {
		t.Fatalf("valid chain rejected: %v", err)
	}
}

func TestValidateMatrix(t *testing.T) {
	cases := []struct {
		name string
		def  Definition
		want string
	}{
		{"no nodes", defWith(), "no nodes"},
		{"too many nodes", defWith(make([]Node, MaxNodes+1)...), "limit"},
		{"bad id", defWith(Node{ID: "a/b", Agent: "ag_a", Task: "t"}), "invalid id"},
		{"dotdot id", defWith(Node{ID: "..", Agent: "ag_a", Task: "t"}), "invalid id"},
		{"duplicate id", defWith(
			Node{ID: "a", Agent: "ag_a", Task: "t"},
			Node{ID: "a", Agent: "ag_b", Task: "t"},
		), "duplicate"},
		{"missing agent", defWith(Node{ID: "a", Task: "t"}), "agent is required"},
		{"missing task", defWith(Node{ID: "a", Agent: "ag_a"}), "task is required"},
		{"long task", defWith(Node{ID: "a", Agent: "ag_a", Task: strings.Repeat("x", MaxTaskLen+1)}), "exceeds"},
		{"negative turns", defWith(Node{ID: "a", Agent: "ag_a", Task: "t", MaxTurns: -1}), "negative"},
		{"self dependency", defWith(Node{ID: "a", Agent: "ag_a", Task: "t", After: deps("a")}), "itself"},
		{"unknown dependency", defWith(Node{ID: "a", Agent: "ag_a", Task: "t", After: deps("ghost")}), "unknown dependency"},
		{"absolute expect", defWith(Node{ID: "a", Agent: "ag_a", Task: "t", Expect: &Expect{Path: "/tmp/x"}}), "invalid expect"},
		{"escaping expect", defWith(Node{ID: "a", Agent: "ag_a", Task: "t", Expect: &Expect{Path: "../x"}}), "invalid expect"},
		{"windows expect", defWith(Node{ID: "a", Agent: "ag_a", Task: "t", Expect: &Expect{Path: `C:\x`}}), "invalid expect"},
		{"two-cycle", defWith(
			Node{ID: "a", Agent: "ag_a", Task: "t", After: deps("b")},
			Node{ID: "b", Agent: "ag_b", Task: "t", After: deps("a")},
		), "cycle"},
		{"longer cycle", defWith(
			Node{ID: "a", Agent: "ag_a", Task: "t", After: deps("c")},
			Node{ID: "b", Agent: "ag_b", Task: "t", After: deps("a")},
			Node{ID: "c", Agent: "ag_c", Task: "t", After: deps("b")},
		), "cycle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.def.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want something containing %q", err, tc.want)
			}
		})
	}
}

func TestValidateAcceptsNestedExpectPath(t *testing.T) {
	d := defWith(Node{ID: "a", Agent: "ag_a", Task: "t", Expect: &Expect{Path: "out/result.md"}})
	if err := d.Validate(); err != nil {
		t.Fatalf("nested expect path rejected: %v", err)
	}
}

func TestTopoOrderKeepsDefinitionOrderAmongIndependents(t *testing.T) {
	d := defWith(
		Node{ID: "analyze", Agent: "ag_a", Task: "t"},
		Node{ID: "review", Agent: "ag_b", Task: "t", After: deps("analyze")},
		Node{ID: "report", Agent: "ag_c", Task: "t", After: deps("analyze", "review")},
		Node{ID: "lone", Agent: "ag_d", Task: "t"},
	)
	order, err := d.TopoOrder()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"analyze", "review", "report", "lone"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

func TestNormalizeTrimsAndDeduplicates(t *testing.T) {
	d := defWith(Node{
		ID: " a ", Agent: " ag_x ", Task: " t ",
		Expect: &Expect{Path: " out.md "},
		After:  []Dep{{Node: "b"}, {Node: "b"}, {Node: " "}, {Node: ""}},
	})
	d.Normalize()
	n := d.Nodes[0]
	if n.ID != "a" || n.Agent != "ag_x" || n.Task != "t" || n.Expect.Path != "out.md" {
		t.Fatalf("normalize left %+v", n)
	}
	if len(n.After) != 1 || n.After[0].Node != "b" || n.After[0].When != WhenSuccess {
		t.Fatalf("after = %v, want [{b success}]", n.After)
	}
	empty := defWith(Node{ID: "a", Agent: "ag_x", Task: "t", Expect: &Expect{Path: "  "}})
	empty.Normalize()
	if empty.Nodes[0].Expect != nil {
		t.Fatal("blank expect path should drop the expect block")
	}
	if empty.Nodes[0].After == nil {
		t.Fatal("after should normalize to an explicit empty list")
	}
}
