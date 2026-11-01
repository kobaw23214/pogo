package tools

// The shipped docs bundle's tools.json is load-bearing: it turns the skill's
// scripts into first-class tools. This test loads the real file so an
// authoring mistake (bad mapping, invisible character, missing exec) fails
// here instead of surfacing as a skipped tool on some server's stderr.

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDocsManifestLoadsClean(t *testing.T) {
	dir := filepath.Join("..", "skills", "builtin", "docs")
	toolsOut, catalog, diags := LoadSynthTools([]SynthSource{{Skill: "docs", Dir: dir}})
	if len(diags) != 0 {
		for _, d := range diags {
			t.Errorf("diag: %s (%s)", d.Message, d.Path)
		}
		t.FailNow()
	}
	want := map[string]bool{
		"docs.pdf-info": true, "docs.pdf-text": true, "docs.pdf-links": true,
		"docs.docx-info": true, "docs.docx-text": true,
		"docs.xlsx-info": true, "docs.xlsx-sheet": true,
	}
	if len(catalog) != len(want) {
		t.Fatalf("catalog has %d tools, want %d: %+v", len(catalog), len(want), catalog)
	}
	for _, info := range catalog {
		if !want[info.Name] {
			t.Fatalf("unexpected tool %q", info.Name)
		}
		if !info.Registered {
			t.Errorf("%q is not registered", info.Name)
		}
		if !info.ReadOnly {
			t.Errorf("%q should carry the readOnly hint — every docs script only reads and prints", info.Name)
		}
		want[info.Name] = false
	}
	if len(toolsOut) != len(catalog) {
		t.Fatalf("registered %d tools, catalog has %d", len(toolsOut), len(catalog))
	}
	for _, tool := range toolsOut {
		st, ok := tool.(*SynthTool)
		if !ok {
			t.Fatalf("tool %T is not a SynthTool", tool)
		}
		script := st.exec.argv[len(st.exec.argv)-1]
		if script == SkillDirToken || script == "" || !strings.Contains(script, "/scripts/") {
			t.Errorf("%q argv was not expanded under the skill dir: %v", st.info.Name, st.exec.argv)
		}
	}
}
