package tools

import (
	"slices"
	"testing"
)

func TestPrivateMaskRoots(t *testing.T) {
	for _, tc := range []struct {
		name, missing string
		dirs, want    []string
	}{
		{"parent-first", "", []string{"/run/pogo-web", "/run/pogo-web/admin", "/run/pogo-web/registry"}, []string{"/run/pogo-web"}},
		{"children-first-and-duplicate", "", []string{"/run/pogo-web/registry", "/run/pogo-web/admin", "/run/pogo-web/", "/run/pogo-web/./admin", "/run/pogo-web"}, []string{"/run/pogo-web"}},
		{"path-boundaries", "", []string{"/run/control-sibling", "/run/control/admin", "/run/control"}, []string{"/run/control", "/run/control-sibling"}},
		{"case-sensitive", "", []string{"/run/Control", "/run/control/admin"}, []string{"/run/Control", "/run/control/admin"}},
		{"missing-parent-does-not-cover", "/run/control", []string{"/run/control", "/run/control/admin"}, []string{"/run/control/admin"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := privateMaskRoots(tc.dirs, func(p string) bool { return p != tc.missing })
			if !slices.Equal(got, tc.want) {
				t.Fatalf("private roots = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBwrapPrivateParentMaskKeepsItsChildrenHidden(t *testing.T) {
	p := SandboxPolicy{Bwrap: "bwrap", Proc: true, PrivateDirs: []string{
		"/run/pogo-web/admin", "/run/pogo-web/registry", "/run/pogo-web", "/var/lib/pogo-skills",
	}}
	argv := p.bwrapArgv("/workspace", "/home/agent", "/tmp", allExist, nil, []string{"true"})
	for _, root := range []string{"/run/pogo-web", "/var/lib/pogo-skills"} {
		index := argvIndex(argv, "--tmpfs", root, "--remount-ro", root)
		if index < argvIndex(argv, "--bind", "/workspace", "/workspace") || index < 0 {
			t.Fatalf("private mask must be read-only and follow writable mounts: %v", argv)
		}
	}
	for _, child := range []string{"/run/pogo-web/admin", "/run/pogo-web/registry"} {
		if argvIndex(argv, "--tmpfs", child) >= 0 {
			t.Fatalf("child mountpoint would be recreated beneath a read-only mask: %v", argv)
		}
	}
}
