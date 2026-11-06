package tools

import (
	"fmt"
	"path/filepath"
)

// SkillContextUpdate replaces only the host-selected Skill projection. The
// owning session must be idle, including background delegations.
type SkillContextUpdate struct {
	Previous []Tool
	Tools    []Tool
	// MCP, when present, is projected against Tools by the host. Prepare the
	// Skill and external replacements together so a new Skill can take over a
	// colliding wire name, or release it back to MCP, in the same commit.
	// Nil retains the old external registry for callers without an MCP pool.
	MCP                *[]Tool
	ReadRoots          []string
	ChildReadRoots     []string
	SkillReads         []SkillReadTarget
	SkillPaths         []string
	ExplicitSkills     bool
	ReadOnlySkillTools []string
	Cwd                string
	ProtectionManifest string
}

// WithSkillContext keeps stateful native tools (todo, browser, Office and
// retained delegation), replacing the file-read grants and declared tools.
// apply updates the retained subagent only after the caller persists policy.
func (r *Registry) WithSkillContext(u SkillContextUpdate) (*Registry, func(), error) {
	if u.MCP != nil {
		r = r.WithMCP(nil)
	}
	old := map[string]bool{}
	for _, t := range u.Previous {
		if hostShellJobName(t.Name()) || hostShortcutName(t.Name()) || hostWatchName(t.Name()) {
			continue
		}
		if t.Name() == SkillSearchName || t.Name() == SkillSaveName {
			continue
		}
		if reader, ok := t.(*RuntimeSkillRead); ok && skillAuthorBackendAvailable(reader.Backend) {
			continue
		}
		old[t.Name()] = true
	}
	roots := CanonicalRoots(u.ReadRoots)
	if abs, err := filepath.Abs(r.spillDir); err == nil {
		roots = append(roots, canonical(abs))
	}
	ts := make([]Tool, 0, len(r.order)+len(u.Tools))
	taken := map[string]bool{}
	start := 0
	var child *Subagent
	for i, t := range r.order {
		if i == r.mcpStart {
			start = len(ts)
		}
		if old[t.Name()] {
			reader, hostRead := t.(*RuntimeSkillRead)
			if !hostRead || !skillAuthorBackendAvailable(reader.Backend) {
				continue
			}
		}
		switch x := t.(type) {
		case *Read:
			c := *x
			c.Roots = roots
			c.skillReads = skillReadIndex(u.SkillReads)
			t = &c
		case *Ls:
			c := *x
			c.Roots = roots
			t = &c
		case *Find:
			c := *x
			c.Roots = roots
			t = &c
		case *Grep:
			c := *x
			c.Roots = roots
			t = &c
		case *Subagent:
			child = x
		}
		ts = append(ts, t)
		taken[t.Name()] = true
	}
	if r.mcpStart >= len(r.order) {
		start = len(ts)
	}
	insert := make([]Tool, 0, len(u.Tools))
	for _, tool := range u.Tools {
		if hostShellJobName(tool.Name()) || hostShortcutName(tool.Name()) || hostWatchName(tool.Name()) || hostUserInputName(tool.Name()) {
			continue
		}
		if hostSkillAuthorName(tool.Name()) {
			if reader, ok := tool.(*RuntimeSkillRead); !ok || taken[tool.Name()] || skillAuthorBackendAvailable(reader.Backend) {
				continue
			}
		}
		if taken[tool.Name()] {
			return nil, nil, fmt.Errorf("Skill tool conflicts with existing tool %q", tool.Name())
		}
		if ro, ok := tool.(ReadOnlyCapable); ok && r.readOnly && !ro.IsReadOnly() {
			continue
		}
		if st, ok := tool.(*SynthTool); ok {
			c := st.WithWorkspace(u.Cwd, r.spillDir)
			c.ProtectionManifest = u.ProtectionManifest
			tool = c
		}
		insert = append(insert, tool)
		taken[tool.Name()] = true
	}
	all := append(append(append([]Tool(nil), ts[:start]...), insert...), ts[start:]...)
	next := NewRegistry(all...)
	next.mcpStart, next.readOnly, next.spillDir = start+len(insert), r.readOnly, r.spillDir
	next.results, next.scopedOutputRoot, next.nativeTools = r.results, r.scopedOutputRoot, r.nativeTools
	next.sentinelEvent = r.sentinelEvent
	next.resultDelivery = CloneResultDeliveryPolicy(r.resultDelivery)
	if u.MCP != nil {
		next = next.WithMCP(*u.MCP)
	}
	apply := func() {
		if child != nil {
			child.SkillPaths = append([]string(nil), u.SkillPaths...)
			child.SkillReadRoots = append([]string(nil), u.ChildReadRoots...)
			child.ExplicitSkills = u.ExplicitSkills
			child.ReadOnlySkillTools = append([]string(nil), u.ReadOnlySkillTools...)
		}
	}
	return next, apply, nil
}
