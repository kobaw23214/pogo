package tools

import (
	"path/filepath"
	"runtime"
	"strings"

	"github.com/yosukeno/pogo/skillobserve"
)

// Skill observation plumbing for the tool layer.
//
// Attribution here is by trusted injection, never by inference: the caller
// that loaded the skills (the web Manager) hands the registry a narrow
// canonical-path-to-identity map, and a SynthTool carries the identity the
// manifest loader bound to its source directory. A read of a file that
// happens to sit inside a skill directory, a bash call made after reading a
// SKILL.md, or an external MCP tool with a skill-sounding name are all
// unowned operations and stay unrecorded — an unknown path or name never
// becomes an attributed event. Identities carry no path; the map's keys and
// the Identify input never enter an event.

// SkillReadTarget binds one registered skill's trusted identity to the
// canonical absolute path of its SKILL.md — the single file whose read
// counts as a skill load. Path is a lookup key only; it is never recorded.
type SkillReadTarget struct {
	Identity skillobserve.Identity
	Path     string
}

// skillReadIndex normalizes the injected targets onto the same canonical
// form resolve() produces, so a relative or symlinked read argument still
// hits the entry the loader registered. Targets without a resolved identity
// or path are dropped: they cannot be attributed and must not be guessed.
func skillReadIndex(targets []SkillReadTarget) map[string]skillobserve.Identity {
	if len(targets) == 0 {
		return nil
	}
	idx := make(map[string]skillobserve.Identity, len(targets))
	for _, tgt := range targets {
		if tgt.Identity.ID == "" || tgt.Path == "" {
			continue
		}
		abs, err := filepath.Abs(tgt.Path)
		if err != nil {
			continue
		}
		idx[skillReadKey(filepath.Clean(abs))] = tgt.Identity
	}
	if len(idx) == 0 {
		return nil
	}
	return idx
}

func skillReadKey(path string) string {
	path = canonical(path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}
