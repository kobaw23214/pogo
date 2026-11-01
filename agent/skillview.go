package agent

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

// Skill-bound tool layering. A host binding group whose trusted Skill is in this
// session's catalog keeps its declarations out of the model's view until the
// conversation loads that Skill, so a conversation that never uses the
// capability does not pay for it on every request. The layers are:
//
//   - always: the Skill's catalog entry (name, description, location);
//   - once loaded: the group's tool declarations, beside the Skill's own text;
//   - on demand: the Skill's reference documents.
//
// "Loaded" is derived from the live history and never stored separately, so a
// reloaded session gets the same view. Compaction restores a loaded Skill's
// text into the continuation (compact_rehydrate.go), which keeps its group
// loaded; a Skill too large to restore drops out and is read again. Any of
// these counts:
//
//   - a successful read of the trusted embedded SKILL.md;
//   - an explicit invocation: a skill atom, or a /skill expansion whose
//     location is that trusted file;
//   - a call to any of the group's tools, which also keeps every tool the
//     history used declared.
//
// A loaded group's declarations go after the rest of the view, in load order,
// rather than back at their registry positions: loading then appends to the
// tools array instead of rewriting it from the middle, which keeps a prefix
// cache over the earlier declarations valid. The guidance below is constant
// for as long as a deferrable group exists, for the same reason: loading
// changes the declarations, never the system prompt.
//
// Visibility is not authorization. The registry, grants, validation and the
// approval gate are untouched, and a withheld tool called by name runs exactly
// as before. The registry decides which groups may be withheld at all (see
// tools.Registry.DeferredSkillBindings); without a discovery path nothing is.
// The view only changes on the loop goroutine, before a model request.

const skillToolsDeferredInstruction = `Some native tools in this conversation are declared only after their Skill is loaded. If a task needs one of the tools listed below and it is not declared yet, read that Skill's SKILL.md from available_skills first; its tools are declared on the next request. Loading a Skill changes what you can see, not what you are allowed to do.`

// skillInvocationRe matches skills.Invocation's opening tag for a /skill
// expansion. Both attributes are Go-quoted there, so they are unquoted here.
var skillInvocationRe = regexp.MustCompile(`^<skill name=("(?:[^"\\]|\\.)*") location=("(?:[^"\\]|\\.)*")>`)

// skillToolView is the Skill-bound half of the tool view, derived from the
// registry and the history together.
type skillToolView struct {
	// deferrable is every group that could be withheld here, loaded or not.
	deferrable []tools.DeferredSkillBinding
	// loaded are the groups the history loaded, in load order.
	loaded []tools.DeferredSkillBinding
	// withheld are the rest, in registry order.
	withheld []tools.DeferredSkillBinding
}

func (a *Agent) skillToolView() skillToolView {
	if a.registry == nil {
		return skillToolView{}
	}
	groups := a.registry.DeferredSkillBindings()
	if len(groups) == 0 {
		return skillToolView{}
	}
	v := skillToolView{deferrable: groups}
	order := a.loadedSkillBindings(groups)
	for _, name := range order {
		if i := slices.IndexFunc(groups, func(g tools.DeferredSkillBinding) bool { return g.Name == name }); i >= 0 {
			v.loaded = append(v.loaded, groups[i])
		}
	}
	for _, g := range groups {
		if !slices.Contains(order, g.Name) {
			v.withheld = append(v.withheld, g)
		}
	}
	return v
}

// withheldSkillBindings returns the deferrable groups the history has not
// loaded, in registry order.
func (a *Agent) withheldSkillBindings() []tools.DeferredSkillBinding {
	return a.skillToolView().withheld
}

// loadedSkillBindings names the groups the history loaded, ordered by their
// first evidence.
func (a *Agent) loadedSkillBindings(groups []tools.DeferredSkillBinding) []string {
	var order []string
	owner := map[string]string{}
	for _, g := range groups {
		for _, name := range g.Tools {
			owner[name] = g.Name
		}
	}
	mark := func(name string) {
		if slices.Contains(order, name) {
			return
		}
		if slices.ContainsFunc(groups, func(g tools.DeferredSkillBinding) bool { return g.Name == name }) {
			order = append(order, name)
		}
	}
	reads := map[string]llm.Block{}
	for _, m := range a.messages {
		for _, b := range m.Content {
			switch b.Type {
			case llm.BlockToolUse:
				if group, ok := owner[b.Name]; ok {
					mark(group)
				} else if b.Name == "read" && strings.Contains(string(b.Input), "SKILL.md") {
					reads[b.ID] = b
				}
			case llm.BlockToolResult:
				if call, ok := reads[b.ToolUseID]; ok && !b.IsError {
					for _, name := range a.registry.SkillBindingsLoadedByRead(call.Input, b.Details) {
						mark(name)
					}
				}
			case llm.BlockText:
				if m.Role != llm.RoleUser {
					continue
				}
				if b.InputSkill != "" {
					mark(b.InputSkill)
				}
				if name, location, ok := parseSkillInvocation(b.Text); ok {
					if group, ok := a.registry.SkillBindingForDocument(location); ok && group == name {
						mark(group)
					}
				}
			}
		}
		if len(order) == len(groups) {
			break
		}
	}
	return order
}

// parseSkillInvocation reads the name and location of a skills.Invocation
// block. The caller still has to verify the location against the registry:
// the text alone proves nothing.
func parseSkillInvocation(text string) (name, location string, ok bool) {
	match := skillInvocationRe.FindStringSubmatch(text)
	if match == nil {
		return "", "", false
	}
	name, errName := strconv.Unquote(match[1])
	location, errLocation := strconv.Unquote(match[2])
	if errName != nil || errLocation != nil {
		return "", "", false
	}
	return name, location, true
}

// skillToolNames flattens groups for the view filter.
func skillToolNames(groups []tools.DeferredSkillBinding) map[string]bool {
	if len(groups) == 0 {
		return nil
	}
	out := map[string]bool{}
	for _, g := range groups {
		for _, name := range g.Tools {
			out[name] = true
		}
	}
	return out
}

// refreshSkillToolView rebuilds the view when the history changed which
// groups are loaded. It runs before each model request, after the previous
// batch settled and steering landed, so a Skill read in one turn declares its
// tools on the next request.
func (a *Agent) refreshSkillToolView() {
	next := a.withheldSkillBindings()
	if slices.EqualFunc(next, a.skillWithheld, func(x, y tools.DeferredSkillBinding) bool {
		return x.Name == y.Name && slices.Equal(x.Tools, y.Tools)
	}) {
		return
	}
	a.rebuildMCPView()
}

// skillToolsInstruction tells the model how to load Skill-bound tools. It
// depends only on which groups could be withheld, not on which are, so loading
// a Skill leaves the system prompt byte-for-byte unchanged.
func (a *Agent) skillToolsInstruction() string {
	if len(a.skillDeferrable) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(skillToolsDeferredInstruction)
	for _, g := range a.skillDeferrable {
		b.WriteString("\n- " + g.Name + ": " + strings.Join(g.Tools, ", "))
	}
	return b.String()
}
