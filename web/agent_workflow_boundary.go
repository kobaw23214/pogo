package web

import "github.com/yosukeno/pogo/skills"

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
