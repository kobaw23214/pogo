package web

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

func workspaceManifestSkill(t *testing.T) skills.Skill {
	t.Helper()
	dir := t.TempDir()
	manifest := `{"tools":[{
		"name":"fixture.read", "description":"Read a workspace-relative fixture file.",
		"inputSchema":{"type":"object","properties":{"file":{"type":"string"}},"required":["file"],"additionalProperties":false},
		"annotations":{"readOnlyHint":true,"idempotentHint":true},
		"x-pogo-exec":{"argv":["cat","--"],"map":{"file":"append"}}
	}]}`
	writeSkillFile(t, filepath.Join(dir, "SKILL.md"), "---\nname: fixture\ndescription: Workspace fixture.\n---\nRead the requested file.\n")
	writeSkillFile(t, filepath.Join(dir, "tools.json"), manifest)
	return skills.Skill{Name: "fixture", Description: "Workspace fixture", Path: filepath.Join(dir, "SKILL.md"), Dir: dir}
}

func TestManagerSynthToolsExportIndependentWorkspaceBindings(t *testing.T) {
	sk := workspaceManifestSkill(t)
	templates, _, diags := tools.LoadSynthTools([]tools.SynthSource{{Skill: sk.Name, Dir: sk.Dir}})
	if len(diags) != 0 || len(templates) != 1 {
		t.Fatalf("load manifest: tools=%d diagnostics=%+v", len(templates), diags)
	}
	prototype := templates[0].(*tools.SynthTool)
	spill := t.TempDir()
	t.Setenv(tools.SpillDirEnv, spill)
	a := &Manager{cfg: Config{Cwd: t.TempDir()}, synthTools: templates}
	b := &Manager{cfg: Config{Cwd: t.TempDir()}, synthTools: templates}
	first := a.SynthTools()[0].(*tools.SynthTool)
	second := b.SynthTools()[0].(*tools.SynthTool)
	if first == prototype || second == prototype || first == second {
		t.Fatal("MCP exports share mutable runtime bindings")
	}
	if first.Cwd != a.Cwd() || second.Cwd != b.Cwd() || first.SpillDir != spill || second.SpillDir != spill {
		t.Fatalf("incorrect bindings: first=(%q,%q) second=(%q,%q)", first.Cwd, first.SpillDir, second.Cwd, second.SpillDir)
	}
	first.Cwd, first.SpillDir = "changed export", "changed spill"
	again := a.SynthTools()[0].(*tools.SynthTool)
	if again == first || again.Cwd != a.Cwd() || again.SpillDir != spill || second.Cwd != b.Cwd() {
		t.Fatal("changing one export contaminated another binding")
	}
	if prototype.Cwd != "" || prototype.SpillDir != "" {
		t.Fatalf("catalog template was modified: cwd=%q spill=%q", prototype.Cwd, prototype.SpillDir)
	}
}

func TestMCPSynthRelativePathsRemainBoundAcrossSessionsAndReload(t *testing.T) {
	if _, err := exec.LookPath("cat"); err != nil {
		t.Skip("cat is not available")
	}
	for _, key := range []string{"POGO_MCP", "POGO_MCP_TOKEN", "POGO_MCP_POLICY"} {
		t.Setenv(key, "")
	}
	sk := workspaceManifestSkill(t)
	h := newHarness(t, func(int) (llm.Response, error) {
		t.Error("direct manifest invocation must not call a model")
		return textTurn("unused"), nil
	}, func(c *Config) { c.Skills = []skills.Skill{sk} })
	rel := "uploads/同名 文档.txt"
	writeInput := func(root, content string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		writeSkillFile(t, path, content)
	}
	writeInput(h.mgr.Cwd(), "manager workspace")
	srv, err := NewServer(h.mgr, ServerOptions{Token: h.token, MCP: &MCPOptions{Policy: "auto"}})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	cs := connectStreamable(t, ts.URL, h.token)
	assertManagerRead := func() {
		t.Helper()
		result := callTool(t, cs, "fixture.read", map[string]any{"file": rel})
		if result.IsError || strings.TrimSpace(resultText(result)) != "manager workspace" {
			t.Fatalf("direct MCP tool used the wrong workspace: is_error=%v text=%q", result.IsError, resultText(result))
		}
	}
	assertManagerRead()
	for _, workspace := range []string{"conversation-a", "conversation-b"} {
		writeInput(filepath.Join(h.mgr.Cwd(), workspace), workspace)
		if _, err := h.mgr.Create("", workspace); err != nil {
			t.Fatal(err)
		}
		// Mirror a registry binding with a different spill destination as well.
		registry := tools.New(tools.Options{Cwd: filepath.Join(h.mgr.Cwd(), workspace), SpillDir: t.TempDir(), Synth: h.mgr.skillViews().synthTools})
		tool, ok := registry.Get("fixture_read")
		if !ok {
			t.Fatal("session fixture tool is missing")
		}
		raw, err := json.Marshal(map[string]string{"file": rel})
		if err != nil {
			t.Fatal(err)
		}
		result, err := tool.Execute(context.Background(), raw)
		if err != nil || strings.TrimSpace(result.Text) != workspace {
			t.Fatalf("session tool used the wrong workspace: %q %v", result.Text, err)
		}
		assertManagerRead()
	}
	h.mgr.SetSkills([]skills.Skill{sk})
	assertManagerRead()
}
