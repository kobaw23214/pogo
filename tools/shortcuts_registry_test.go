package tools

import (
	"context"
	"testing"

	"github.com/yosukeno/pogo/shortcuts"
)

type shortcutRegistryBackend struct{}

func (*shortcutRegistryBackend) Search(context.Context, shortcuts.SearchRequest) (shortcuts.SearchResult, error) {
	return shortcuts.SearchResult{}, nil
}
func (*shortcutRegistryBackend) Read(context.Context, shortcuts.ReadRequest) (shortcuts.ReadResult, error) {
	return shortcuts.ReadResult{}, nil
}
func (*shortcutRegistryBackend) Create(context.Context, shortcuts.CreateRequest) (shortcuts.CreateResult, error) {
	return shortcuts.CreateResult{}, nil
}

func TestShortcutRegistryRequiresHostAndOptionalWriteGrant(t *testing.T) {
	empty := []string{}
	write := []string{ShortcutCreateName}
	for _, tc := range []struct {
		name          string
		backend       ShortcutBackend
		enabled       *[]string
		readOnly      bool
		reads, writes bool
	}{
		{"no-host", nil, nil, false, false, false},
		{"typed-nil-host", (*shortcutRegistryBackend)(nil), nil, false, false, false},
		{"legacy-all", &shortcutRegistryBackend{}, nil, false, true, true},
		{"pinned-without-new-write", &shortcutRegistryBackend{}, &empty, false, true, false},
		{"explicit-write", &shortcutRegistryBackend{}, &write, false, true, true},
		{"read-only-despite-write-selection", &shortcutRegistryBackend{}, &write, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := New(Options{Cwd: t.TempDir(), Shortcuts: tc.backend, Enabled: tc.enabled, ReadOnly: tc.readOnly})
			for _, name := range []string{ShortcutQueryName, ShortcutManageName} {
				_, got := r.Get(name)
				want := tc.reads
				if name == ShortcutManageName {
					want = tc.writes
				}
				if got != want {
					t.Fatalf("%s registered=%v, want %v", name, got, want)
				}
			}
			if query, ok := r.Get(ShortcutQueryName); ok {
				actions := query.(*ShortcutQuery).actionTools()
				if len(actions) != 2 || actions["search"] == nil || actions["read"] == nil || query.(*ShortcutQuery).Backend != tc.backend {
					t.Fatal("read group lost its trusted host or actions")
				}
			}
			if manage, ok := r.Get(ShortcutManageName); ok {
				actions := manage.(*ShortcutManage).actionTools()
				if len(actions) != 1 || actions["create"] == nil || manage.(*ShortcutManage).Backend != tc.backend {
					t.Fatal("write grant changed grouped create authority")
				}
			}
		})
	}
}

func TestShortcutHostNamesCannotBeInjectedBySkills(t *testing.T) {
	empty := []string{}
	for _, name := range []string{ShortcutQueryName, ShortcutManageName, ShortcutSearchName, ShortcutReadName, ShortcutCreateName} {
		r := New(Options{Cwd: t.TempDir(), Enabled: &empty, Synth: []Tool{nameStub{name: name}}})
		if _, ok := r.Get(name); ok {
			t.Fatalf("untrusted tool replaced host verb %s", name)
		}
	}
}
