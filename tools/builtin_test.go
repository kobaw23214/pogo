package tools

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/yosukeno/pogo/webservice"
)

// enabledNames is the assertion view of a registry: just the names, in
// registration order.
func enabledNames(r *Registry) []string {
	out := []string{}
	for _, t := range r.All() {
		out = append(out, t.Name())
	}
	return out
}

func TestEnabledNilKeepsEveryBuiltin(t *testing.T) {
	backend := serviceBackendFunc(func(context.Context, webservice.Request) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	r := New(Options{Cwd: t.TempDir(), Todo: true, Subagent: &Subagent{}, Images: &ImageAnalyze{Ask: fixedAsk("x")}, Browser: &stubLink{}, Shortcuts: &shortcutRegistryBackend{}, ShortcutManagement: &shortcutManagementFixture{}, SkillAuthor: &skillAuthorFixture{}, Watches: &fakeWatchBackend{}, BackgroundTasks: &fakeBackgroundTaskBackend{}, AgentWorkflows: &agentWorkflowFixture{}, WebServices: backend})
	for _, info := range BuiltinCatalog() {
		if _, ok := r.Get(info.Name); !ok {
			t.Fatalf("nil Enabled lost %s: %v", info.Name, enabledNames(r))
		}
	}
}

func TestEnabledNilDoesNotInventUnconfiguredWebService(t *testing.T) {
	selected := []string{WebServiceName}
	for _, tc := range []struct {
		name    string
		enabled *[]string
	}{{"default", nil}, {"explicit", &selected}} {
		t.Run(tc.name, func(t *testing.T) {
			r := New(Options{Cwd: t.TempDir(), Enabled: tc.enabled, WebServices: nil})
			if _, ok := r.Get(WebServiceName); ok {
				t.Fatal("tool selection invented an unconfigured managed runtime")
			}
			if _, ok := r.Get("read"); !ok {
				t.Fatal("unconfigured managed runtime removed the protected core")
			}
		})
	}
}

func TestEnabledFiltersTheOptionalVerbsOnly(t *testing.T) {
	// A definition that keeps only read-side tools plus bash: write, edit and
	// the browser verb stay out, and each name filters on its own.
	sel := []string{"read", "ls", "find", "grep", "todo", "subagent", "bash"}
	r := New(Options{
		Cwd:      t.TempDir(),
		Enabled:  &sel,
		Todo:     true,
		Subagent: &Subagent{},
		Browser:  &stubLink{},
	})
	got := enabledNames(r)
	for _, name := range []string{"write", "edit", "browser"} {
		if slices.Contains(got, name) {
			t.Fatalf("selection %v still registered %s: %v", sel, name, got)
		}
	}
	for _, name := range []string{"read", "ls", "find", "grep", "todo", "subagent", "bash"} {
		if !slices.Contains(got, name) {
			t.Fatalf("selection %v lost %s: %v", sel, name, got)
		}
	}
}

func TestEnabledCannotDropTheProtectedCore(t *testing.T) {
	// A selection that names only bash reads as "nothing optional but bash",
	// not as "remove the ground the session stands on".
	sel := []string{"bash"}
	r := New(Options{
		Cwd:                t.TempDir(),
		Enabled:            &sel,
		Todo:               true,
		Subagent:           &Subagent{},
		Images:             &ImageAnalyze{Ask: fixedAsk("x")},
		Shortcuts:          &shortcutRegistryBackend{},
		SkillAuthor:        &skillAuthorFixture{},
		ShortcutManagement: &shortcutManagementFixture{},
		Watches:            &fakeWatchBackend{},
	})
	got := enabledNames(r)
	for _, info := range BuiltinCatalog() {
		if !info.Protected {
			continue
		}
		if !slices.Contains(got, info.Name) {
			t.Fatalf("protected %s was withheld: %v", info.Name, got)
		}
	}
	if slices.Contains(got, "write") || slices.Contains(got, "edit") {
		t.Fatalf("unselected optional verbs survived: %v", got)
	}
}

func TestEnabledComposesWithReadOnly(t *testing.T) {
	// Orthogonal cuts: ReadOnly withholds the write trio even when the
	// selection names it, and the selection withholds the browser verb from a
	// read-only session that would otherwise keep it.
	sel := []string{"write", "edit", "bash"}
	r := New(Options{Cwd: t.TempDir(), ReadOnly: true, Enabled: &sel, Browser: &stubLink{}})
	got := enabledNames(r)
	for _, name := range []string{"write", "edit", "bash", "browser"} {
		if slices.Contains(got, name) {
			t.Fatalf("ReadOnly + %v registered %s: %v", sel, name, got)
		}
	}
	if !slices.Contains(got, "read") {
		t.Fatalf("ReadOnly lost the read core: %v", got)
	}
}

func TestEnabledLeavesDeclaredAndMCPToolsAlone(t *testing.T) {
	// The selection names built-ins only: skill manifests and MCP projections
	// have their own selection dimensions (Skills / MCPServers).
	sel := []string{}
	stub := nameStub{name: "declared_thing"}
	r := New(Options{Cwd: t.TempDir(), Enabled: &sel, Synth: []Tool{stub}})
	if _, ok := r.Get("declared_thing"); !ok {
		t.Fatalf("empty builtin selection removed a declared tool: %v", enabledNames(r))
	}
}

func TestRetiredPlatformToolNamesCannotBeRebound(t *testing.T) {
	all := []string{WorkflowDiscoverName, WorkflowReadName, WorkflowValidateName, WorkflowEditName, WorkflowTestName, WorkflowRunName, WorkflowPublishName}
	selected := []string{WorkflowReadName, WorkflowEditName}
	for _, selection := range []*[]string{nil, &selected} {
		for _, ro := range []bool{false, true} {
			r := New(Options{Cwd: t.TempDir(), Enabled: selection, ReadOnly: ro})
			for _, name := range all {
				if _, ok := r.Get(name); ok || BuiltinSelected(selection, name) {
					t.Fatalf("retired platform tool registered for an Agent: %s", name)
				}
				if !BuiltinNameSet()[name] {
					t.Fatalf("historical saved tool name no longer loads: %s", name)
				}
			}
		}
	}
	empty := []string{}
	{
		var synth, projected []Tool
		for _, name := range all {
			synth = append(synth, nameStub{name: name})
			projected = append(projected, &MCPTool{wireName: name})
		}
		r := New(Options{Cwd: t.TempDir(), Enabled: &empty, Synth: synth, MCP: projected})
		for _, snapshot := range []*Registry{r, r.WithMCP(projected)} {
			for _, name := range all {
				if _, ok := snapshot.Get(name); ok {
					t.Fatal("unselected host workflow tool invented by external projection", name)
				}
			}
		}
	}
}
