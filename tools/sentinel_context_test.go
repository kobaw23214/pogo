package tools

import "testing"

func TestSentinelContextRequiresHostEventAndSurvivesRegistryRefresh(t *testing.T) {
	selected := []string{BackgroundTaskName, WatchDecideName}
	backend := &fakeWatchBackend{}
	ordinary := New(Options{Cwd: t.TempDir(), Enabled: &selected, Watches: backend, BackgroundTasks: &fakeBackgroundTaskBackend{}})
	if _, ok := ordinary.Get(WatchDecideName); ok {
		t.Fatal("a saved selection activated a current-event tool")
	}
	template, ok := ordinary.HostNativeTool(WatchDecideName)
	if !ok {
		t.Fatal("host decision template must survive while dormant")
	}
	next, apply, err := ordinary.WithNativeSelection(&selected, nil)
	if err != nil {
		t.Fatal(err)
	}
	apply()
	if _, ok := next.Get(WatchDecideName); ok {
		t.Fatal("ordinary native sync activated event context")
	}
	event, err := next.WithSentinelEvent(true)
	if err != nil {
		t.Fatal(err)
	}
	if tool, ok := event.Get(WatchDecideName); !ok || tool != template {
		t.Fatal("event did not receive its original host decision instance")
	}
	if _, ok := event.Get(WatchName); ok {
		t.Fatal("event context expanded the selected lifecycle permissions")
	}
	event = event.WithMCP([]Tool{&MCPTool{wireName: WatchDecideName}})
	event, apply, err = event.WithSkillContext(SkillContextUpdate{Tools: []Tool{nameStub{name: WatchDecideName}}})
	if err != nil {
		t.Fatal(err)
	}
	apply()
	event, apply, err = event.WithNativeSelection(&selected, nil)
	if err != nil {
		t.Fatal(err)
	}
	apply()
	if tool, ok := event.Get(WatchDecideName); !ok || tool != template {
		t.Fatal("refresh replaced or dropped the active event decision")
	}
	finished, err := event.WithSentinelEvent(false)
	if err != nil {
		t.Fatal(err)
	}
	finished = finished.WithMCP(nil)
	finished, apply, err = finished.WithNativeSelection(&selected, nil)
	if err != nil {
		t.Fatal(err)
	}
	apply()
	if _, ok := finished.Get(WatchDecideName); ok {
		t.Fatal("next ordinary run inherited the finished event decision")
	}
	if _, ok := ordinary.Get(WatchDecideName); ok {
		t.Fatal("context snapshots mutated the original registry")
	}
	if same, err := finished.WithSentinelEvent(false); err != nil || same != finished {
		t.Fatal("an unchanged ordinary context should not replace its registry")
	}
}

func TestSentinelContextCannotCreateAnUnavailableHost(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts Options
	}{
		{name: "no host", opts: Options{}},
		{name: "read only", opts: Options{ReadOnly: true, Watches: &fakeWatchBackend{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.opts.Cwd = t.TempDir()
			r := New(tc.opts)
			if _, err := r.WithSentinelEvent(true); err == nil {
				t.Fatal("invented unavailable event authority")
			}
			if _, ok := r.Get(WatchDecideName); ok {
				t.Fatal("failure changed the original registry")
			}
		})
	}
}
