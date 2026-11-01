package web

import (
	"fmt"
	"slices"

	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

// These bindings belong to the host's embedded catalog. An imported/authored
// Skill with the same display name never gains a native capability.
func trustedNativeSkill(sk skills.Skill) bool {
	return sk.Source == "embedded" && len(tools.SkillBindingTools(sk.Name)) > 0
}

func selectedNativeSkill(selection *[]string, catalog map[string]skills.Skill, name string) bool {
	for id, sk := range catalog {
		if sk.Name == name && trustedNativeSkill(sk) && (selection == nil || slices.Contains(*selection, id)) {
			return true
		}
	}
	return false
}

func nativeBindingNames(in []string) ([]string, error) {
	out := []string{}
	for _, name := range in {
		if !slices.Contains(tools.SkillBindingNames(), name) {
			return nil, fmt.Errorf("未知的内置 Skill 工具关联：%s", name)
		}
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out, nil
}

func nativeBindingTools(name string) []string {
	return append(tools.SkillBindingTools(name), tools.SkillBindingLegacyTools(name)...)
}

func sameNativeBindingGrants(a, b *[]string, name string) bool {
	for _, tool := range nativeBindingTools(name) {
		if tools.BuiltinSelected(a, tool) != tools.BuiltinSelected(b, tool) {
			return false
		}
	}
	return true
}

func explicitNativeSelection(selection *[]string, keepLegacy bool) []string {
	if selection != nil {
		return slices.Clone(*selection)
	}
	out := []string{}
	for _, info := range tools.BuiltinCatalog() {
		out = append(out, info.Name)
	}
	if keepLegacy {
		for _, name := range tools.SkillBindingNames() {
			out = append(out, tools.SkillBindingLegacyTools(name)...)
		}
	}
	return out
}

// New definitions follow linked selection. A legacy definition keeps its exact
// native grants until its operator deliberately changes/adopts a Skill group.
// Never migrate old automatically checked Skill names into new executable rights.
func (m *Manager) normalizeStudioSkillTools(in *studioInput, previous *studioAgent) error {
	catalog := m.skillCatalogIDs()
	requested, err := nativeBindingNames(in.SkillToolBindings)
	if err != nil {
		return err
	}
	managed := requested
	if previous == nil {
		managed = tools.SkillBindingNames()
		if in.Skills == nil {
			empty := []string{}
			in.Skills = &empty
		}
	} else {
		old, err := nativeBindingNames(previous.SkillToolBindings)
		if err != nil {
			return err
		}
		managed = append(managed, old...)
		for _, name := range tools.SkillBindingNames() {
			changed := selectedNativeSkill(previous.Skills, catalog, name) != selectedNativeSkill(in.Skills, catalog, name)
			// A legacy nil tool list included hidden aliases. Materializing the
			// modern picker during an unrelated edit must retain those old grants.
			if previous.Tools == nil && in.Tools != nil && !changed && !slices.Contains(managed, name) {
				kept := slices.Clone(*in.Tools)
				for _, legacy := range tools.SkillBindingLegacyTools(name) {
					if !slices.Contains(kept, legacy) {
						kept = append(kept, legacy)
					}
				}
				in.Tools = &kept
			}
			if changed {
				managed = append(managed, name)
			}
			if !slices.Contains(managed, name) && !sameNativeBindingGrants(previous.Tools, in.Tools, name) {
				return fmt.Errorf("%s 的关联工具需通过对应 Skill 启用或停用，不能单独修改", name)
			}
		}
	}
	managed, err = nativeBindingNames(managed)
	if err != nil {
		return err
	}
	if len(managed) == 0 {
		in.SkillToolBindings = nil
		return nil
	}
	selected := explicitNativeSelection(in.Tools, previous != nil)
	for _, name := range managed {
		// Older editors auto-checked embedded guidance even when its native
		// tools were deliberately withheld. A stale client must never have its
		// explicit restriction silently expanded, including on a managed group.
		if selectedNativeSkill(in.Skills, catalog, name) && !slices.Contains(requested, name) {
			for _, tool := range tools.SkillBindingTools(name) {
				if !tools.BuiltinSelected(in.Tools, tool) {
					return fmt.Errorf("%s 将关联启用 %s；请刷新 Agent 配置页面并确认该 Skill 的关联工具后保存", name, tool)
				}
			}
		}
		group := nativeBindingTools(name)
		selected = slices.DeleteFunc(selected, func(tool string) bool { return slices.Contains(group, tool) })
		if selectedNativeSkill(in.Skills, catalog, name) {
			selected = append(selected, tools.SkillBindingTools(name)...)
		}
	}
	selected, err = normalizeIDList(selected, "内置工具")
	if err != nil {
		return err
	}
	in.Tools = &selected
	in.SkillToolBindings = managed
	return nil
}

// Runtime authority is the intersection of the saved native grant and a live
// trusted Skill binding. Catalog replacement/revocation cannot add permissions.
func studioBoundNativeSelection(a studioAgent, catalog map[string]skills.Skill) *[]string {
	if len(a.SkillToolBindings) == 0 {
		return a.Tools
	}
	selected := explicitNativeSelection(a.Tools, true)
	for _, name := range a.SkillToolBindings {
		group := nativeBindingTools(name)
		ready := selectedNativeSkill(a.Skills, catalog, name)
		selected = slices.DeleteFunc(selected, func(tool string) bool {
			return slices.Contains(group, tool) && (!ready || !slices.Contains(tools.SkillBindingTools(name), tool))
		})
	}
	return &selected
}

func (m *Manager) studioNativeSelection(a studioAgent) *[]string {
	return studioBoundNativeSelection(a, m.skillCatalogIDs())
}

func (m *Manager) studioNativeSelected(a studioAgent, name string) bool {
	return tools.BuiltinSelected(m.studioNativeSelection(a), name)
}

func (b studioBinding) effectiveNativeAgent() studioAgent {
	a := b.effectiveSkillBinding().Agent
	a.Tools = b.effectiveTools()
	if b.ToolPolicy != nil {
		a.SkillToolBindings = slices.Clone(b.ToolPolicy.SkillToolBindings)
	}
	return a
}

func nativeToolsForSkill(sk skills.Skill) []string {
	if trustedNativeSkill(sk) {
		return tools.SkillBindingTools(sk.Name)
	}
	return nil
}
func legacyNativeToolsForSkill(sk skills.Skill) []string {
	if trustedNativeSkill(sk) {
		return tools.SkillBindingLegacyTools(sk.Name)
	}
	return nil
}
