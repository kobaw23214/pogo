package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func embeddedBackgroundTaskSkill(t *testing.T) ([]Skill, Skill) {
	t.Helper()
	dirs := materializeEmbeddedForTest(t)
	list, diagnostics := Load(Options{Cwd: t.TempDir(), UserDir: t.TempDir(), Embedded: dirs})
	reportSkillDiagnosticsInTest(t, diagnostics)
	sk, ok := Find(list, "background-tasks")
	if !ok {
		t.Fatal("background-tasks did not survive embedded materialization and loading")
	}
	return list, sk
}

func TestEmbeddedBackgroundTasksLocalizedInvocationUsesOneSkill(t *testing.T) {
	list, canonical := embeddedBackgroundTaskSkill(t)
	if canonical.Source != "embedded" || filepath.Base(canonical.Dir) != "background-tasks" || canonical.BrowserTask {
		t.Fatalf("unexpected Skill provenance or browser authority: %+v", canonical)
	}
	visible := 0
	for _, sk := range list {
		if sk.Name == canonical.Name {
			visible++
		}
		if sk.Name == "后台任务" || sk.Name == "哨兵" {
			t.Fatal("localized aliases became duplicate catalog entries")
		}
	}
	if visible != 1 {
		t.Fatalf("canonical catalog entries=%d, want one", visible)
	}
	for _, alias := range []string{"background-tasks", "后台任务", "哨兵"} {
		t.Run(alias, func(t *testing.T) {
			name, args, ok := ParseCommand("/skill:" + alias + " 检查我的后台任务")
			if !ok || name != alias || args != "检查我的后台任务" {
				t.Fatalf("localized explicit invocation did not parse: %q %q %v", name, args, ok)
			}
			sk, ok := Find(list, name)
			if !ok || sk.Name != canonical.Name || sk.Path != canonical.Path {
				t.Fatalf("alias resolved to a different Skill: %+v", sk)
			}
			expanded, err := Invocation(sk, args)
			if err != nil || !strings.Contains(expanded, `<skill name="background-tasks"`) || !strings.HasSuffix(expanded, args) {
				t.Fatalf("localized invocation lost canonical identity or user request: %v", err)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(canonical.Dir, "tools.json")); !os.IsNotExist(err) {
		t.Fatalf("instruction Skill must not shadow native background tools through a manifest: %v", err)
	}
}

func TestEmbeddedBackgroundTasksAliasesDoNotOverrideExplicitNames(t *testing.T) {
	list, canonical := embeddedBackgroundTaskSkill(t)
	// Find's canonical-name pass must win even if an alias entry came first.
	explicit := Skill{Name: "哨兵", Source: "user", Path: "/explicit/SKILL.md"}
	selected, ok := Find(append([]Skill{canonical, explicit}, list...), "哨兵")
	if !ok || selected.Source != "user" || selected.Path != explicit.Path {
		t.Fatalf("builtin alias replaced an explicitly named Skill: %+v", selected)
	}
}

func TestExternalBackgroundTasksCannotAcquireTrustedAliases(t *testing.T) {
	dirs := materializeEmbeddedForTest(t)
	user := t.TempDir()
	writeSkill(t, user, "background-tasks/SKILL.md", skillFile("background-tasks", "custom instructions", "external body"))
	list, _ := Load(Options{Cwd: t.TempDir(), UserDir: user, Embedded: dirs})
	sk, ok := Find(list, "background-tasks")
	if !ok || sk.Source != "user" || len(sk.Aliases) != 0 {
		t.Fatalf("external replacement received trusted builtin aliases: %+v", sk)
	}
	for _, alias := range []string{"后台任务", "哨兵"} {
		if _, found := Find(list, alias); found {
			t.Fatalf("external replacement unexpectedly resolves builtin alias %q", alias)
		}
	}
}
