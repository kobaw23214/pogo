package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// A read whose whole result is one registered Skill's instructions is what the
// model-text budget keeps whole, so attribution has to be exact: the registered
// file, alone, found through the read tool's own resolution. A same-named copy, a
// read of several files or a malformed call is an ordinary page.
func TestSkillDocumentReadAloneNeedsTheRegisteredFileAlone(t *testing.T) {
	root, skillPath, target := skillTree(t, "# demo\nfollow these rules\n")
	other := filepath.Join(root, "notes.md")
	copyPath := filepath.Join(root, "copy", "SKILL.md")
	for path, body := range map[string]string{other: "notes", copyPath: "# demo\nfollow these rules\n"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	registry := New(Options{Cwd: root, ReadRoots: []string{filepath.Join(root, "skills")}, SpillDir: t.TempDir(),
		SkillReads: []SkillReadTarget{target}})
	rel, err := filepath.Rel(root, skillPath)
	if err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { b, _ := json.Marshal(s); return string(b) }

	for _, tc := range []struct {
		name, input string
		want        bool
	}{
		{"relative path", fmt.Sprintf(`{"path":%s}`, quote(rel)), true},
		{"a page of it", fmt.Sprintf(`{"path":%s,"offset":2,"limit":1}`, quote(skillPath)), true},
		{"paths with only it", fmt.Sprintf(`{"paths":[%s]}`, quote(skillPath)), true},
		{"with another file", fmt.Sprintf(`{"paths":[%s,%s]}`, quote(skillPath), quote(other)), false},
		{"same-named copy", fmt.Sprintf(`{"path":%s}`, quote(copyPath)), false},
		{"ordinary file", fmt.Sprintf(`{"path":%s}`, quote(other)), false},
		{"malformed", `{"path":`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, ok := registry.SkillDocumentReadAlone(json.RawMessage(tc.input))
			if ok != tc.want || ok && doc.Name != "demo" {
				t.Fatalf("SkillDocumentReadAlone(%s) = %+v, %v; want %v", tc.input, doc, ok, tc.want)
			}
		})
	}
}
