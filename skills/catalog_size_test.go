package skills

import (
	"sort"
	"testing"
)

// The embedded Skills' catalog entries are system-prompt text: every session
// that selects them sends each name, description and location on every
// request, before the conversation starts. This is the same ceiling the core
// tool declarations and every binding group's declarations have
// (agent.TestToolSchemasStaySmall, tools.TestSkillBindingGroupSchemasStaySmall),
// for the same reason: growth should be a decision, not a drift.
//
// Locations are replaced with one short path per Skill, so the number
// measures prose a person wrote rather than where a deployment unpacked it.
// Measured on 2026-11-01: 7 embedded Skills, 1978 description bytes, a
// 3666-byte section. Raise the ceiling deliberately and say what the new text
// buys; MaxDescriptionLength still bounds a single description.
func TestEmbeddedSkillCatalogStaysSmall(t *testing.T) {
	const ceiling = 3850
	dirs, err := MaterializeEmbedded(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	list, diagnostics := Load(Options{Embedded: dirs, UserDir: t.TempDir()})
	if len(diagnostics) > 0 {
		t.Fatalf("embedded Skill diagnostics: %+v", diagnostics)
	}
	for i := range list {
		if list[i].Source != "embedded" {
			t.Fatalf("measured a non-embedded Skill: %s (%s)", list[i].Name, list[i].Source)
		}
		list[i].Path = "/skills/" + list[i].Name + "/SKILL.md"
	}
	section := PromptSection(list)
	byDescription := append([]Skill(nil), list...)
	sort.Slice(byDescription, func(i, j int) bool { return len(byDescription[i].Description) > len(byDescription[j].Description) })
	t.Logf("catalog section is %d bytes across %d Skills", len(section), len(list))
	for _, s := range byDescription {
		t.Logf("  %-20s description %4d bytes", s.Name, len(s.Description))
	}
	if len(section) > ceiling {
		t.Errorf("embedded Skill catalog is %d bytes, over the %d ceiling. Raise it deliberately "+
			"and say what the new text buys", len(section), ceiling)
	}
}
