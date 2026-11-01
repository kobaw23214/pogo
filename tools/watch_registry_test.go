package tools

import "testing"

func TestWatchRegistryOptionalSelectionAndCurrentEventDecision(t *testing.T) {
	empty := []string{}
	backend := &fakeWatchBackend{}
	registry := New(Options{Cwd: t.TempDir(), Enabled: &empty, Watches: backend, SentinelEvent: true})
	for _, name := range []string{WatchName, WatchTestName} {
		if _, ok := registry.Get(name); ok {
			t.Fatalf("unselected capability registered: %s", name)
		}
	}
	decide, ok := registry.Get(WatchDecideName)
	if !ok || decide.(*WatchDecide).Backend != backend {
		t.Fatal("protected current-batch decision unavailable")
	}
	if !IsProtectedBuiltin(WatchDecideName) || IsProtectedBuiltin(WatchName) || IsProtectedBuiltin(WatchTestName) {
		t.Fatal("incorrect protected boundary")
	}
	for _, info := range BuiltinCatalog() {
		if info.Name == WatchDecideName {
			t.Fatal("event-only decision must not be an Agent configuration checkbox")
		}
	}
	selected := []string{WatchName, WatchTestName}
	next, commit, err := registry.WithNativeSelection(&selected, nil)
	if err != nil {
		t.Fatal(err)
	}
	commit()
	for _, name := range []string{WatchName, WatchTestName, WatchDecideName} {
		if _, ok = next.Get(name); !ok {
			t.Fatalf("selected host capability missing: %s", name)
		}
	}
	if _, ok = registry.Get(WatchName); ok {
		t.Fatal("selection update mutated original registry")
	}
}

func TestWatchRegistryNativeSelectionKeepsHostInstancesAcrossRevocation(t *testing.T) {
	empty := []string{}
	selected := []string{WatchName, WatchTestName}
	backend := &fakeWatchBackend{}
	registry := New(Options{Cwd: t.TempDir(), Enabled: &empty, Watches: backend, SentinelEvent: true})
	originals := map[string]Tool{}
	for _, name := range []string{WatchName, WatchTestName, WatchDecideName} {
		tool, ok := registry.HostNativeTool(name)
		if !ok {
			t.Fatalf("host capability template missing: %s", name)
		}
		originals[name] = tool
	}
	applySelection := func(registry *Registry, names *[]string) *Registry {
		t.Helper()
		next, commit, err := registry.WithNativeSelection(names, nil)
		if err != nil {
			t.Fatal(err)
		}
		commit()
		return next
	}
	next := applySelection(registry, &selected)
	next = next.WithMCP(nil)
	next = applySelection(next, &empty)
	if _, ok := next.Get(WatchName); ok {
		t.Fatal("watch survived revocation")
	}
	if _, ok := next.Get(WatchTestName); ok {
		t.Fatal("test survived revocation")
	}
	if decision, ok := next.Get(WatchDecideName); !ok || decision != originals[WatchDecideName] {
		t.Fatal("decision was revoked or replaced")
	}
	next = applySelection(next, &selected)
	for name, want := range originals {
		got, ok := next.Get(name)
		if !ok || got != want {
			t.Fatalf("host instance %s was replaced", name)
		}
	}
}

func TestWatchRegistryCannotInventHostOrBypassReadOnly(t *testing.T) {
	all := []string{WatchName, WatchTestName, WatchDecideName}
	for _, tc := range []struct {
		name     string
		readonly bool
		backend  WatchBackend
	}{{"missing host", false, nil}, {"read only", true, &fakeWatchBackend{}}} {
		t.Run(tc.name, func(t *testing.T) {
			empty := []string{}
			registry := New(Options{Cwd: t.TempDir(), Enabled: &empty, ReadOnly: tc.readonly, Watches: tc.backend, SentinelEvent: true})
			next, commit, err := registry.WithNativeSelection(&all, nil)
			if err != nil {
				t.Fatal(err)
			}
			commit()
			for _, name := range all {
				if _, ok := next.Get(name); ok {
					t.Fatalf("invented unavailable host capability: %s", name)
				}
			}
		})
	}
}
func TestWatchRegistryNamesReservedEvenWhenHostToolsAreWithheld(t *testing.T) {
	names := []string{WatchName, WatchTestName, WatchDecideName}
	malicious := []Tool{}
	projections := []Tool{}
	for _, name := range names {
		malicious = append(malicious, nameStub{name: name})
		projections = append(projections, &MCPTool{wireName: name})
	}
	empty := []string{}
	without := New(Options{Cwd: t.TempDir(), Enabled: &empty, Synth: malicious, MCP: projections})
	without = without.WithMCP(projections)
	for _, name := range names {
		if _, ok := without.Get(name); ok {
			t.Fatalf("external tool impersonated missing host capability: %s", name)
		}
	}
	backend := &fakeWatchBackend{}
	with := New(Options{Cwd: t.TempDir(), Watches: backend, SentinelEvent: true, Synth: malicious, MCP: projections})
	before := map[string]Tool{}
	for _, name := range names {
		before[name], _ = with.Get(name)
	}
	refreshed := with.WithMCP(projections)
	for _, name := range names {
		got, ok := refreshed.Get(name)
		if !ok || got != before[name] {
			t.Fatalf("projection replaced host capability: %s", name)
		}
		count := 0
		for _, tool := range refreshed.All() {
			if tool.Name() == name {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("duplicate host capability %s: %d", name, count)
		}
	}
}

func TestWatchRegistrySkillRefreshCannotRetireOrImpersonateHost(t *testing.T) {
	backend := &fakeWatchBackend{}
	malicious := []Tool{nameStub{name: WatchName}, nameStub{name: WatchTestName}, nameStub{name: WatchDecideName}}
	for _, host := range []bool{true, false} {
		opts := Options{Cwd: t.TempDir()}
		if host {
			opts.Watches = backend
			opts.SentinelEvent = true
		}
		registry := New(opts)
		before := map[string]Tool{}
		for _, name := range []string{WatchName, WatchTestName, WatchDecideName} {
			before[name], _ = registry.Get(name)
		}
		next, apply, err := registry.WithSkillContext(SkillContextUpdate{Previous: malicious, Tools: malicious})
		if err != nil {
			t.Fatal(err)
		}
		apply()
		for name, want := range before {
			got, ok := next.Get(name)
			if host && (!ok || got != want) || !host && ok {
				t.Fatalf("host=%v capability=%s was replaced or invented", host, name)
			}
		}
	}
}
