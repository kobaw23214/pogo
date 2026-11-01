package shortcuts

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/yosukeno/pogo/inputref"
	"github.com/yosukeno/pogo/skills"
)

func simpleGraph() Graph {
	return Graph{RootID: "root", Nodes: []Node{{LocalID: "root", Title: "Root", Parts: []Part{{Kind: "text", Text: "before "}, {Kind: "node", NodeID: "child"}, {Kind: "text", Text: " after"}}}, {LocalID: "child", Title: "Child", Parts: []Part{{Kind: "text", Text: "nested text"}}}}}
}
func cloneGraph(g Graph) Graph {
	g.Nodes = append([]Node(nil), g.Nodes...)
	for i := range g.Nodes {
		g.Nodes[i].Parts = append([]Part(nil), g.Nodes[i].Parts...)
	}
	return g
}
func noReference(context.Context, string) (Reference, error) {
	return Reference{}, errors.New("unknown reference")
}
func validationCode(err error) string {
	var v *ValidationError
	if errors.As(err, &v) {
		return v.Code
	}
	return ""
}

func TestCompileOrderedNewAndFrozenNestedReferences(t *testing.T) {
	const page = "<page-element source=\"Example\" url=\"https://example.invalid/\">\nelement: button \"查询\"\n</page-element>"
	office := &inputref.OfficeSnapshot{Path: "uploads/synthetic.xlsx", Title: "选区", Editor: "cell", Sheet: "S1", Range: "A1:B2", Text: "单元格内容", CanvasVersion: "canvas-1"}
	ref := Reference{ScopeID: "owned", Version: "v7", Parts: []inputref.Part{
		{Type: "element", Text: page, Favicon: "data:image/png;base64,aQ=="},
		{Type: "file", Path: "shortcut-assets/" + strings.Repeat("a", 32) + "/名单.csv"},
		{Type: "skill", Name: "office"},
		{Type: "office", Office: office},
		{Type: "shortcut", ShortcutID: "saved", Title: "既有卡片", Text: "旧摘要", Parts: []inputref.Part{{Type: "text", Text: "不可复制进父摘要的正文"}}},
	}}
	ref.SHA256 = SnapshotSHA256(ref.Parts)
	g := simpleGraph()
	g.Nodes[0].Parts = []Part{{Kind: "text", Text: "起始\n"}, {Kind: "ref", RefID: "ref_all"}, {Kind: "node", NodeID: "child"}, {Kind: "text", Text: "\n尾部"}}
	calls := 0
	got, err := Compile(context.Background(), "owned", g, func(_ context.Context, id string) (Reference, error) {
		calls++
		if id != "ref_all" {
			t.Fatal(id)
		}
		return ref, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || got.Stats.Depth != 2 || got.Stats.Nodes != 11 {
		t.Fatalf("unexpected expanded stats: %+v", got.Stats)
	}
	want := []string{"text", "element", "file", "skill", "office", "shortcut", "shortcut", "text"}
	types := []string{}
	for _, p := range got.Root.Parts {
		types = append(types, p.Type)
	}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("order: %v", types)
	}
	if got.Root.Parts[0].Text != "起始\n" || got.Root.Parts[7].Text != "\n尾部" || got.Root.Parts[6].Parts[0].Text != "nested text" {
		t.Fatal("text or nested order changed")
	}
	if got.Root.ID == "root" || got.Root.Parts[6].ShortcutID == "child" || got.Root.Parts[5].ShortcutID != "saved" || got.Root.InputLayout != "inline" {
		t.Fatal("host IDs or frozen existing ID lost")
	}
	if strings.Contains(got.Root.Content, "不可复制进父摘要的正文") || strings.Contains(got.Root.Content, "nested text") {
		t.Fatal("parent fallback duplicated nested content")
	}
	if got.References["ref_all"].SHA256 != ref.SHA256 {
		t.Fatal("reference hash changed")
	}
	hash := SnapshotSHA256(got.Root.Parts)
	ref.Parts[4].Parts[0].Text = "external changed"
	office.Text = "canvas changed"
	g.Nodes[1].Parts[0].Text = "caller changed"
	if SnapshotSHA256(got.Root.Parts) != hash {
		t.Fatal("compiled snapshot aliases a mutable source")
	}
	if msg := skills.ValidateInputParts([]inputref.Part{{Type: "shortcut", ShortcutID: got.Root.ID, Title: got.Root.Title, Text: got.Root.Content, Parts: got.Root.Parts}}, "inline", "shortcut"); msg != "" {
		t.Fatal(msg)
	}
}

func TestCompileInvalidGraphBeforeSourceResolution(t *testing.T) {
	changes := map[string]struct {
		change func(*Graph)
		code   string
	}{
		"duplicate":     {func(g *Graph) { g.Nodes = append(g.Nodes, g.Nodes[0]) }, "DUPLICATE_NODE"},
		"missing root":  {func(g *Graph) { g.RootID = "unknown" }, "MISSING_ROOT"},
		"missing child": {func(g *Graph) { g.Nodes[0].Parts[1].NodeID = "unknown" }, "MISSING_NODE"},
		"cycle":         {func(g *Graph) { g.Nodes[1].Parts = []Part{{Kind: "node", NodeID: "root"}} }, "CYCLE"},
		"unreachable": {func(g *Graph) {
			g.Nodes = append(g.Nodes, Node{LocalID: "extra", Title: "extra", Parts: []Part{{Kind: "text", Text: "x"}}})
		}, "UNREACHABLE_NODE"},
		"mixed atom":   {func(g *Graph) { g.Nodes[0].Parts[0].RefID = "bad" }, "INVALID_PART"},
		"unsupported":  {func(g *Graph) { g.Nodes[0].Parts[0].Kind = "path" }, "INVALID_PART"},
		"bad metadata": {func(g *Graph) { g.Nodes[0].Title = "bad\nname" }, "INVALID_NODE"},
	}
	for name, c := range changes {
		t.Run(name, func(t *testing.T) {
			g := cloneGraph(simpleGraph())
			c.change(&g)
			calls := 0
			_, err := Compile(context.Background(), "owned", g, func(context.Context, string) (Reference, error) { calls++; return Reference{}, nil })
			if validationCode(err) != c.code || calls != 0 {
				t.Fatalf("%v, resolver calls %d", err, calls)
			}
		})
	}
}

func TestCompileExpandedBudgets(t *testing.T) {
	t.Run("root counts as first shortcut level", func(t *testing.T) {
		for depth := 5; depth <= 6; depth++ {
			g := Graph{RootID: "n0"}
			for i := 0; i < depth; i++ {
				id := "n" + string(rune('0'+i))
				n := Node{LocalID: id, Title: id, Parts: []Part{{Kind: "text", Text: "leaf"}}}
				if i < depth-1 {
					n.Parts = []Part{{Kind: "node", NodeID: "n" + string(rune('0'+i+1))}}
				}
				g.Nodes = append(g.Nodes, n)
			}
			got, err := Compile(context.Background(), "owned", g, noReference)
			if depth == 5 && (err != nil || got.Stats.Depth != 5) {
				t.Fatal(err, got.Stats)
			}
			if depth == 6 && validationCode(err) != "BUDGET_DEPTH" {
				t.Fatal(err)
			}
		}
	})
	t.Run("512 includes root", func(t *testing.T) {
		for count := 511; count <= 512; count++ {
			g := Graph{RootID: "root", Nodes: []Node{{LocalID: "root", Title: "Root"}}}
			for i := 0; i < count; i++ {
				g.Nodes[0].Parts = append(g.Nodes[0].Parts, Part{Kind: "text", Text: "x"})
			}
			got, err := Compile(context.Background(), "owned", g, noReference)
			if count == 511 && (err != nil || got.Stats.Nodes != 512) {
				t.Fatal(err, got.Stats)
			}
			if count == 512 && validationCode(err) != "BUDGET_NODES" {
				t.Fatal(err)
			}
		}
	})
	t.Run("shared child charged for each occurrence", func(t *testing.T) {
		g := Graph{RootID: "root", Nodes: []Node{{LocalID: "root", Title: "Root"}, {LocalID: "child", Title: "Child"}}}
		for i := 0; i < 32; i++ {
			g.Nodes[0].Parts = append(g.Nodes[0].Parts, Part{Kind: "node", NodeID: "child"})
			g.Nodes[1].Parts = append(g.Nodes[1].Parts, Part{Kind: "text", Text: "x"})
		}
		_, err := Compile(context.Background(), "owned", g, noReference)
		if validationCode(err) != "BUDGET_NODES" {
			t.Fatal(err)
		}
	})
	t.Run("dense DAG bounded without exponential allocation", func(t *testing.T) {
		g := Graph{RootID: "n0"}
		for depth := 0; depth < 5; depth++ {
			n := Node{LocalID: "n" + string(rune('0'+depth)), Title: "Shared", Parts: []Part{{Kind: "text", Text: "leaf"}}}
			if depth < 4 {
				n.Parts = nil
				for i := 0; i < 100; i++ {
					n.Parts = append(n.Parts, Part{Kind: "node", NodeID: "n" + string(rune('0'+depth+1))})
				}
			}
			g.Nodes = append(g.Nodes, n)
		}
		_, err := Compile(context.Background(), "owned", g, noReference)
		if validationCode(err) != "BUDGET_NODES" {
			t.Fatal(err)
		}
	})
	t.Run("UTF8 byte budget includes repeats and summaries", func(t *testing.T) {
		g := simpleGraph()
		g.Nodes[1].Parts = []Part{{Kind: "text", Text: strings.Repeat("界", 20000)}}
		g.Nodes[0].Parts = nil
		for i := 0; i < 5; i++ {
			g.Nodes[0].Parts = append(g.Nodes[0].Parts, Part{Kind: "node", NodeID: "child"})
		}
		_, err := Compile(context.Background(), "owned", g, noReference)
		if validationCode(err) != "BUDGET_BYTES" {
			t.Fatal(err)
		}
	})
	t.Run("frozen reference depth included", func(t *testing.T) {
		part := inputref.Part{Type: "text", Text: "leaf"}
		for i := 0; i < 5; i++ {
			part = inputref.Part{Type: "shortcut", ShortcutID: "saved" + string(rune('0'+i)), Title: "Saved", Parts: []inputref.Part{part}}
		}
		g := Graph{RootID: "root", Nodes: []Node{{LocalID: "root", Title: "Root", Parts: []Part{{Kind: "ref", RefID: "existing"}}}}}
		_, err := Compile(context.Background(), "owned", g, func(context.Context, string) (Reference, error) {
			return Reference{ScopeID: "owned", Parts: []inputref.Part{part}}, nil
		})
		if validationCode(err) != "BUDGET_DEPTH" {
			t.Fatal(err)
		}
	})
}

func TestCompileRejectsForgedReferenceAndCanceledContext(t *testing.T) {
	g := Graph{RootID: "root", Nodes: []Node{{LocalID: "root", Title: "Root", Parts: []Part{{Kind: "ref", RefID: "ref"}}}}}
	cases := []struct {
		ref  Reference
		code string
	}{
		{Reference{ScopeID: "another", Parts: []inputref.Part{{Type: "text", Text: "private"}}}, "REFERENCE_SCOPE"},
		{Reference{ScopeID: "owned", SHA256: strings.Repeat("0", 64), Parts: []inputref.Part{{Type: "text", Text: "x"}}}, "REFERENCE_DIGEST"},
		{Reference{ScopeID: "owned", Parts: []inputref.Part{{Type: "file", Path: "uploads/raw.csv"}}}, "INVALID_REFERENCE"},
	}
	for _, c := range cases {
		_, err := Compile(context.Background(), "owned", g, func(context.Context, string) (Reference, error) { return c.ref, nil })
		if validationCode(err) != c.code {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	if _, err := Compile(ctx, "owned", g, func(context.Context, string) (Reference, error) { called = true; return Reference{}, nil }); !errors.Is(err, context.Canceled) || called {
		t.Fatal("canceled compile resolved sources", err)
	}
}

func TestFallbackBoundedWithoutLosingActualText(t *testing.T) {
	g := Graph{RootID: "root", Nodes: []Node{{LocalID: "root", Title: "Root", Parts: []Part{{Kind: "text", Text: strings.Repeat("字", 600) + "tail"}}}}}
	got, err := Compile(context.Background(), "owned", g, noReference)
	if err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(got.Root.Content) != 256 || !strings.HasSuffix(got.Root.Content, "…") || !strings.HasSuffix(got.Root.Parts[0].Text, "tail") {
		t.Fatal("summary damaged original text")
	}
}
