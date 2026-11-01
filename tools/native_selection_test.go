package tools

import (
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestNativeSelectionRetainsInstancesAndSurvivesProjectionRefresh(t *testing.T) {
	selected := []string{}
	child := &Subagent{}
	r := New(Options{Cwd: t.TempDir(), Enabled: &selected, Shortcuts: &shortcutBackendFixture{}, Todo: true, Subagent: child})
	todo, _ := r.Get("todo")
	search, _ := r.Get(ShortcutQueryName)
	if _, ok := r.Get(ShortcutManageName); ok {
		t.Fatal("creation initially granted")
	}
	enabled := []string{ShortcutCreateName, "bash"}
	next, apply, err := r.WithNativeSelection(&enabled, nil)
	if err != nil {
		t.Fatal(err)
	}
	if child.Enabled != nil {
		t.Fatal("uncommitted update changed retained child")
	}
	apply()
	for _, name := range []string{"read", "ls", "find", "grep", ShortcutQueryName, ShortcutManageName, "bash", "todo", "subagent"} {
		if _, ok := next.Get(name); !ok {
			t.Fatalf("missing %s", name)
		}
	}
	if got, _ := next.Get("todo"); got != todo {
		t.Fatal("todo state replaced")
	}
	if got, _ := next.Get(ShortcutQueryName); got != search {
		t.Fatal("shortcut handles replaced")
	}
	create, _ := next.HostNativeTool(ShortcutManageName)
	visible, _ := next.Get(ShortcutManageName)
	if actions := visible.(*ShortcutManage).actionTools(); len(actions) != 1 || actions["create"] == nil {
		t.Fatal("legacy create grant changed the selected action set", actions)
	}
	next = next.WithMCP(nil)
	next, apply, err = next.WithSkillContext(SkillContextUpdate{})
	if err != nil {
		t.Fatal(err)
	}
	apply()
	next, apply, err = next.WithNativeSelection(&selected, nil)
	if err != nil {
		t.Fatal(err)
	}
	apply()
	if _, ok := next.Get(ShortcutManageName); ok {
		t.Fatal("creation survived revocation")
	}
	next, apply, err = next.WithNativeSelection(&enabled, nil)
	if err != nil {
		t.Fatal(err)
	}
	apply()
	if got, _ := next.HostNativeTool(ShortcutManageName); got != create {
		t.Fatal("host instance was recreated")
	}
	if child.Enabled == nil || !slices.Equal(*child.Enabled, enabled) {
		t.Fatal("child selection not updated")
	}
	if _, ok := r.Get(ShortcutManageName); ok {
		t.Fatal("old registry snapshot changed")
	}
}

func TestNativeSelectionCannotGrantUnavailableOrReadOnlyCapabilities(t *testing.T) {
	for _, readonly := range []bool{false, true} {
		empty := []string{}
		all := []string{"write", "edit", "bash", ShortcutCreateName, "browser"}
		r := New(Options{Cwd: t.TempDir(), Enabled: &empty, ReadOnly: readonly})
		next, apply, err := r.WithNativeSelection(&all, nil)
		if err != nil {
			t.Fatal(err)
		}
		apply()
		for _, name := range []string{ShortcutManageName, "browser"} {
			if _, ok := next.Get(name); ok {
				t.Fatalf("invented host %s", name)
			}
		}
		for _, name := range []string{"write", "edit", "bash"} {
			_, ok := next.Get(name)
			if ok == readonly {
				t.Fatalf("readonly=%v tool=%s registered=%v", readonly, name, ok)
			}
		}
		if _, ok := next.Get("read"); !ok {
			t.Fatal("protected tool removed")
		}
	}
}

func TestNativeSelectionChildEnvironmentAndRetainedBoundary(t *testing.T) {
	selected := []string{"read", ShortcutCreateName}
	s := &Subagent{Cwd: t.TempDir(), Enabled: &selected}
	before := s.permissionKey(ModeEdit)
	other := []string{"read"}
	s.Enabled = &other
	if before == s.permissionKey(ModeEdit) {
		t.Fatal("retained child grants did not distinguish revocation")
	}
	env := nativeSelectionChildEnv([]string{"X=1", EnvSubagentNativeTools + `=["bash"]`, strings.ToLower(EnvSubagentNativeTools) + `=["write"]`}, &other)
	if !reflect.DeepEqual(env, []string{"X=1", EnvSubagentNativeTools + `=["read"]`}) {
		t.Fatalf("stale parent grants: %v", env)
	}
	t.Setenv(EnvSubagentNativeTools, `["read"]`)
	got, err := NativeSelectionFromEnvironment()
	if err != nil || got == nil || !slices.Equal(*got, other) {
		t.Fatal(got, err)
	}
	for _, bad := range []string{`null`, `garbage`, `["nonexistent"]`} {
		t.Setenv(EnvSubagentNativeTools, bad)
		if _, err := NativeSelectionFromEnvironment(); err == nil {
			t.Fatal("invalid child grants accepted")
		}
	}
	t.Setenv(EnvSubagentNativeTools, `[]`)
	got, err = NativeSelectionFromEnvironment()
	if err != nil || got == nil || len(*got) != 0 {
		t.Fatal("empty selection became inheritance")
	}
	_ = os.Unsetenv(EnvSubagentNativeTools)
	got, err = NativeSelectionFromEnvironment()
	if err != nil || got != nil {
		t.Fatal("legacy inheritance changed")
	}
}
