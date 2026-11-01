package tools

import (
	"reflect"
	"slices"
	"testing"
)

func TestSkillBindingCatalogKeepsIndependentPermissionsSeparate(t *testing.T) {
	want := map[string][]string{
		"agent-workflows":  {AgentWorkflowReadName, AgentWorkflowEditName, AgentWorkflowEnableName, AgentWorkflowRunName},
		"background-tasks": {BackgroundTaskName},
		"browser-use":      {"browser"},
		"skill-creator":    {SkillSaveName},
	}
	names := SkillBindingNames()
	if len(names) != len(want) {
		t.Fatalf("binding groups: %v", names)
	}
	for _, name := range names {
		if !reflect.DeepEqual(SkillBindingTools(name), want[name]) {
			t.Fatalf("binding %s: %v", name, SkillBindingTools(name))
		}
	}
	seen := map[string]bool{}
	for _, info := range BuiltinCatalog() {
		seen[info.Name] = true
		if info.SkillBinding != SkillBindingForTool(info.Name) {
			t.Fatalf("catalog association differs: %+v", info)
		}
		if info.SkillBinding != "" && (!slices.Contains(want[info.SkillBinding], info.Name) || info.Protected) {
			t.Fatalf("unreviewed or non-revocable associated tool: %+v", info)
		}
	}
	for _, name := range []string{"read", "ls", "find", "grep", "write", "edit", "bash", SkillSearchName, SkillReadName, ShortcutQueryName, ShortcutManageName, ShortcutDeleteName, ShortcutMergeName} {
		if !seen[name] || SkillBindingForTool(name) != "" {
			t.Fatalf("general permission was hidden or absorbed by a Skill: %s", name)
		}
	}
	for _, name := range []string{WatchName, WatchTestName, WatchDecideName} {
		if seen[name] || !BuiltinNameSet()[name] {
			t.Fatalf("compatibility/context name must be hidden but loadable: %s", name)
		}
	}
	runtimeOnly := map[string]BuiltinInfo{}
	for _, info := range BuiltinRuntimeCatalog() {
		if !seen[info.Name] {
			runtimeOnly[info.Name] = info
		}
	}
	if len(runtimeOnly) != 6 || !runtimeOnly[WatchName].Legacy || !runtimeOnly[WatchTestName].Legacy || !runtimeOnly[WatchDecideName].ContextOnly || runtimeOnly[WatchDecideName].ReadOnly {
		t.Fatalf("runtime-only compatibility/context metadata: %+v", runtimeOnly)
	}
	for _, name := range []string{ShellTasksName, ShellOutputName, ShellStopName} {
		if !runtimeOnly[name].ContextOnly {
			t.Fatalf("shell helper is not marked context-only: %+v", runtimeOnly[name])
		}
	}
}

func TestSkillBindingCatalogCannotBeMutatedByCallers(t *testing.T) {
	names := SkillBindingNames()
	names[0] = "outside"
	grants := SkillBindingTools("agent-workflows")
	grants[0] = "bash"
	legacy := SkillBindingLegacyTools("background-tasks")
	legacy[0] = BackgroundTaskName
	if SkillBindingNames()[0] != "agent-workflows" || SkillBindingTools("agent-workflows")[0] != AgentWorkflowReadName || SkillBindingLegacyTools("background-tasks")[0] != WatchName {
		t.Fatal("caller changed the host-owned binding catalog")
	}
	if SkillBindingTools("outside") != nil || SkillBindingLegacyTools("outside") != nil || SkillBindingForTool("outside") != "" {
		t.Fatal("unknown names acquired a binding")
	}
}

func TestLegacySentinelSelectionsDoNotAcquireUnifiedBackgroundAuthority(t *testing.T) {
	for _, grant := range []string{WatchName, WatchTestName} {
		t.Run(grant, func(t *testing.T) {
			selected := []string{grant}
			r := New(Options{Cwd: t.TempDir(), Enabled: &selected, Watches: &fakeWatchBackend{}, BackgroundTasks: &fakeBackgroundTaskBackend{}})
			if _, ok := r.Get(grant); !ok {
				t.Fatalf("historical permission was dropped: %s", grant)
			}
			for _, other := range []string{WatchName, WatchTestName, BackgroundTaskName} {
				if other == grant {
					continue
				}
				if _, ok := r.Get(other); ok {
					t.Fatalf("%s escalated into %s", grant, other)
				}
			}
		})
	}
}
