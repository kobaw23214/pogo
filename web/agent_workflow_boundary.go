package web

import (
	"path/filepath"

	"github.com/yosukeno/pogo/skillobserve"
	"github.com/yosukeno/pogo/skills"
)

// Keep historical Skill references resolvable without projecting the retired
// platform authoring instructions into any Agent or its delegated children.
func agentConversationSkill(sk skills.Skill) bool {
	return sk.Name != "workflow-authoring"
}

func agentConversationSkills(list []skills.Skill) []skills.Skill {
	out := make([]skills.Skill, 0, len(list))
	for _, sk := range list {
		if agentConversationSkill(sk) {
			out = append(out, sk)
		}
	}
	return out
}

// retiredPlatformSkillIDs recognizes only the retired, non-executable selection.
// Its hash used the materialized path, so reconstruct it from the trusted built-in
// roots after the file itself is no longer shipped. Saved Agent definitions are
// not rewritten: old internal Run fingerprints and all remaining grants survive.
func (m *Manager) retiredPlatformSkillIDs() map[string]bool {
	ids := map[string]bool{"workflow-authoring": true}
	for _, sk := range m.loadedSkills() {
		if sk.Name == "workflow-authoring" {
			ids[skillIDOf(sk)] = true
		}
	}
	add := func(dir string) {
		if dir == "" {
			return
		}
		file := canonicalSkillPath(filepath.Join(dir, "SKILL.md"))
		ids[skillobserve.Identify("workflow-authoring", "embedded", file).ID] = true
		ids[file], ids[filepath.Dir(file)] = true, true
	}
	for _, dir := range m.SkillOptions().Embedded {
		if filepath.Base(filepath.Clean(dir)) == "workflow-authoring" {
			add(dir)
		} else {
			add(filepath.Join(filepath.Dir(dir), "workflow-authoring"))
		}
	}
	delete(ids, "")
	return ids
}
