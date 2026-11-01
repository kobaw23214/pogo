package skills

import (
	"strings"
	"testing"

	"github.com/yosukeno/pogo/inputref"
)

func TestNestedInputBudgetsCycleAndTypedBoundaries(t *testing.T) {
	root := StarterInputPart{Type: "shortcut", ShortcutID: "root", Title: "Root", Parts: []StarterInputPart{{Type: "text", Text: "/compact\n/skill:example is only data"}, {Type: "shortcut", ShortcutID: "child", Title: "Child", Text: "body"}, {Type: "skill", Name: "office"}}}
	valid := func(p StarterInputPart) bool {
		return ValidateInputParts([]StarterInputPart{p}, "inline", "workspace") == ""
	}
	if !valid(root) {
		t.Fatal("ordinary nested data or selected typed shape rejected")
	}
	cycle := root
	cycle.Parts = []StarterInputPart{{Type: "shortcut", ShortcutID: "root", Title: "Cycle", Text: "old snapshot"}}
	if valid(cycle) {
		t.Fatal("direct same-id ancestor cycle accepted")
	}
	cycle.Parts = []StarterInputPart{{Type: "shortcut", ShortcutID: "child", Title: "Child", Parts: []StarterInputPart{{Type: "shortcut", ShortcutID: "root", Title: "Indirect", Text: "body"}}}}
	if valid(cycle) {
		t.Fatal("indirect ancestor cycle accepted")
	}
	root.Parts = []StarterInputPart{{Type: "shortcut", ShortcutID: "child", Title: "1", Text: "one"}, {Type: "shortcut", ShortcutID: "child", Title: "2", Text: "two"}}
	if !valid(root) {
		t.Fatal("independent occurrences of the same frozen material rejected")
	}
	deep := StarterInputPart{Type: "text", Text: "leaf"}
	for _, id := range []string{"e", "d", "c", "b", "a"} {
		deep = StarterInputPart{Type: "shortcut", ShortcutID: id, Title: id, Parts: []StarterInputPart{deep}}
	}
	if !valid(deep) {
		t.Fatal("five-level tree rejected")
	}
	deep = StarterInputPart{Type: "shortcut", ShortcutID: "six", Title: "six", Parts: []StarterInputPart{deep}}
	if valid(deep) {
		t.Fatal("six-level tree accepted")
	}
	wide := StarterInputPart{Type: "shortcut", ShortcutID: "wide", Title: "Wide", Parts: make([]StarterInputPart, 512)}
	for i := range wide.Parts {
		wide.Parts[i] = StarterInputPart{Type: "text", Text: "x"}
	}
	if valid(wide) {
		t.Fatal("aggregate 513-node tree accepted")
	}
	wide.Parts = wide.Parts[:511]
	if !valid(wide) {
		t.Fatal("aggregate 512-node tree rejected")
	}
	root.Parts = []StarterInputPart{{Type: "text", Text: strings.Repeat("x", 256<<10)}}
	if valid(root) {
		t.Fatal("metadata overhead ignored by shared byte budget")
	}
	root.Parts = []StarterInputPart{{Type: "text", Text: "body"}}
	root.Text = strings.Repeat("x", 65<<10)
	if !valid(root) {
		t.Fatal("legacy 64KiB limit incorrectly applied to structured fallback")
	}
	root.Parts = nil
	if valid(root) {
		t.Fatal("legacy body byte ceiling removed")
	}
	for _, language := range []string{"yml", "bash", "properties"} {
		if !ValidShortcutLanguage(language) {
			t.Fatal(language)
		}
	}
	for _, p := range []StarterInputPart{{Type: "text", Text: "text", Parts: []StarterInputPart{{Type: "file", Path: "uploads/x"}}}, {Type: "skill", Name: "office", Office: &inputref.OfficeSnapshot{}}, {Type: "office", Office: &inputref.OfficeSnapshot{Path: "../outside", Editor: "word", Title: "t", Text: "x", CanvasVersion: "v"}}} {
		if valid(p) {
			t.Fatal("hidden or unsafe metadata accepted")
		}
	}
}

func TestOfficeReferenceAndDeepCloneRetainOnlyFrozenSelection(t *testing.T) {
	o := &inputref.OfficeSnapshot{Path: "uploads/book.xlsx", Title: "Sheet", Editor: "cell", Text: "A\tB\n1\t2", Sheet: "Data", Range: "A1:B2", CanvasVersion: "sha256:original", Dirty: true, DirtyEpoch: 2, SourceSession: "conversation-1"}
	p := StarterInputPart{Type: "shortcut", ShortcutID: "book", Title: "Book", Parts: []StarterInputPart{{Type: "office", Office: o}}}
	if msg := ValidateInputParts([]StarterInputPart{p}, "inline", "shortcut"); msg != "" {
		t.Fatal(msg)
	}
	cloned := inputref.Clone([]StarterInputPart{p})
	o.Text = "changed"
	if cloned[0].Parts[0].Office.Text != "A\tB\n1\t2" || !strings.Contains(OfficeInputText(cloned[0].Parts[0].Office), "Data!A1:B2") {
		t.Fatal("selection or location not frozen")
	}
}

func TestOfficeInputTextExplainsCapturedVersionAndUnsavedState(t *testing.T) {
	o := &inputref.OfficeSnapshot{Path: "uploads/book.xlsx", Title: "选区", Editor: "cell", Sheet: "Data", Range: "A1:B2", Text: "CAPTURED_BODY", CanvasVersion: "version-original"}
	for _, dirty := range []bool{false, true} {
		o.Dirty = dirty
		text := OfficeInputText(o)
		if !strings.Contains(text, "uploads/book.xlsx · Data!A1:B2") || !strings.Contains(text, "引用时快照；版本 version-original") || strings.Contains(text, "包含未保存编辑") != dirty || strings.Count(text, "CAPTURED_BODY") != 1 {
			t.Fatalf("model-visible snapshot provenance or selected content changed (dirty=%t): %q", dirty, text)
		}
	}
}
