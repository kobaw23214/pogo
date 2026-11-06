package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The embedded tree must stay loadable: these tests run against the real
// go:embed content, not a fixture, so a malformed built-in fails here first.

func materializeEmbeddedForTest(t *testing.T) []string {
	t.Helper()
	dirs, err := MaterializeEmbedded(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) == 0 {
		t.Fatal("no embedded skills found in builtin/")
	}
	return dirs
}

func TestEmbeddedLoad(t *testing.T) {
	dirs := materializeEmbeddedForTest(t)
	list, diags := Load(Options{Cwd: t.TempDir(), Embedded: dirs})
	reportSkillDiagnosticsInTest(t, diags)

	// web-drawing states the drawing contract; browser-use arms the browser
	// channel; docs extracts text-layer content from pdf/word/excel
	// attachments. All are framework capabilities, and all must survive the
	// embed→materialize→load pipeline — frontmatter included.
	for _, want := range []string{"web-drawing", "web-development", "browser-use", "office", "skill-creator"} {
		t.Run(want, func(t *testing.T) {
			var s *Skill
			for i := range list {
				if list[i].Name == want {
					s = &list[i]
					break
				}
			}
			if s == nil {
				t.Fatalf("%s not loaded from embedded dirs", want)
			}
			if s.Source != "embedded" {
				t.Errorf("source = %q, want embedded", s.Source)
			}
			if _, err := os.Stat(filepath.Join(s.Dir, "SKILL.md")); err != nil {
				t.Errorf("materialized SKILL.md unreadable: %v", err)
			}
		})
	}
	// The browser tool's intent gate hangs off this flag: losing it in the
	// frontmatter would register the skill but never arm the channel.
	for _, s := range list {
		if s.Name == "browser-use" && !s.BrowserTask {
			t.Error("browser-use browser-task flag not parsed")
		}
		if s.Name == "web-development" {
			if s.BrowserTask {
				t.Error("web-development must not arm a browser task")
			}
			if _, err := os.ReadFile(filepath.Join(s.Dir, "references", "python-web.md")); err != nil {
				t.Fatalf("web-development reference was not materialized: %v", err)
			}
			invocation, err := Invocation(s, "检查现有应用")
			if err != nil || !strings.Contains(invocation, `<skill name="web-development"`) || !strings.Contains(invocation, "检查现有应用") {
				t.Fatalf("web-development invocation failed: %v", err)
			}
		}
	}
	// The docs skill's scripts are exec'd by absolute path (the mal-*
	// convention), so materialization must carry the execute bit.
	var docsDir string
	for _, d := range dirs {
		if filepath.Base(d) == "docs" {
			docsDir = d
			break
		}
	}
	if docsDir == "" {
		t.Fatal("docs dir not among materialized dirs")
	}
	pdfScripts, err := filepath.Glob(filepath.Join(docsDir, "scripts", "*"))
	if err != nil || len(pdfScripts) == 0 {
		t.Fatalf("docs scripts not materialized (err=%v)", err)
	}
	for _, p := range pdfScripts {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if fi.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s: mode %v, want executable (scripts/ carries the exec bit)", p, fi.Mode().Perm())
		}
	}
}

func TestEmbeddedOfficeKeepsLegacyInvocationAndScriptLocations(t *testing.T) {
	dst := t.TempDir()
	dirs, err := MaterializeEmbedded(dst)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := Load(Options{Cwd: t.TempDir(), UserDir: t.TempDir(), Embedded: dirs})
	canonical, ok := Find(list, "office")
	legacy, oldOK := Find(list, "docs")
	if !ok || !oldOK || canonical.Name != "office" || legacy.Path != canonical.Path || filepath.Base(canonical.Dir) != "docs" {
		t.Fatalf("Office alias or stable legacy path missing: canonical=%+v legacy=%+v", canonical, legacy)
	}
	for _, sk := range list {
		if sk.Name == "docs" {
			t.Fatal("legacy alias must not appear as another visible skill")
		}
	}
	invocation, err := Invocation(legacy, "检查文件")
	if err != nil || !strings.Contains(invocation, `<skill name="office"`) || !strings.Contains(invocation, "检查文件") {
		t.Fatalf("old invocation not expanded to Office: %v %s", err, invocation)
	}
	if _, err := os.Stat(filepath.Join(dst, "docs", "scripts", "xlsx-sheet")); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"references/online-office.md", "references/file-formats.md", "tools.json"} {
		if _, err := os.Stat(filepath.Join(canonical.Dir, rel)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOfficeAliasDoesNotOverrideExplicitExternalDocs(t *testing.T) {
	dirs := materializeEmbeddedForTest(t)
	user := t.TempDir()
	writeSkill(t, user, "docs/SKILL.md", skillFile("docs", "my document tool", "custom body"))
	list, _ := Load(Options{Cwd: t.TempDir(), UserDir: user, Embedded: dirs})
	s, ok := Find(list, "docs")
	if !ok || s.Source != "user" || s.Name != "docs" {
		t.Fatalf("explicit external skill was replaced by builtin alias: %+v", s)
	}
	other := writeSkill(t, user, "other/SKILL.md", skillFile("office", "my office tool", "custom body"))
	list, _ = Load(Options{Cwd: t.TempDir(), UserDir: t.TempDir(), Paths: []string{other}})
	if _, ok := Find(list, "docs"); ok {
		t.Fatal("a user skill named office gained the builtin compatibility alias")
	}
}

// A skill on disk replaces a built-in of the same name: deployments override by
// writing, not by rebuilding.
func TestEmbeddedLosesNameCollisions(t *testing.T) {
	embedded := materializeEmbeddedForTest(t)
	user := t.TempDir()
	writeSkill(t, user, "web-drawing/SKILL.md",
		skillFile("web-drawing", "user replacement", "custom body"))

	list, diags := Load(Options{Cwd: t.TempDir(), UserDir: user, Embedded: embedded})
	var collisions int
	for _, d := range diags {
		if d.Kind == "collision" {
			collisions++
		}
	}
	if collisions == 0 {
		t.Error("expected a collision diagnostic for the shadowed embedded skill")
	}
	s, ok := Find(list, "web-drawing")
	if !ok {
		t.Fatal("web-drawing missing")
	}
	if s.Source != "user" || s.Description != "user replacement" {
		t.Errorf("winner = %s/%s, want the user skill", s.Source, s.Description)
	}
}

// Embedded skills are a default, not an explicit -skill path: -no-skills
// removes them along with the user directory.
func TestEmbeddedDisabled(t *testing.T) {
	embedded := materializeEmbeddedForTest(t)
	list, _ := Load(Options{Cwd: t.TempDir(), Embedded: embedded, Disabled: true})
	for _, name := range []string{"web-drawing", "web-development"} {
		if _, ok := Find(list, name); ok {
			t.Errorf("embedded %s loaded despite Disabled", name)
		}
	}
}

func TestEmbeddedMaterializeIsRepeatable(t *testing.T) {
	dst := t.TempDir()
	if _, err := MaterializeEmbedded(dst); err != nil {
		t.Fatal(err)
	}
	if _, err := MaterializeEmbedded(dst); err != nil {
		t.Fatalf("second materialization into the same dir failed: %v", err)
	}
}

func reportSkillDiagnosticsInTest(t *testing.T, diags []Diagnostic) {
	t.Helper()
	for _, d := range diags {
		if d.Kind == "collision" {
			t.Errorf("unexpected collision: %s (%s)", d.Message, d.Path)
		}
	}
}
