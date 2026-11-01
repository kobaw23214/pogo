package agent

import (
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

// The itemized fixed cost. OverheadTokens answers "how much does every request
// carry before the conversation starts"; the breakdown answers "which part of
// it", which is the question a person asks when that number jumps — a Skill
// whose tools arrived, a memory that grew, an MCP server with a large catalog.
//
// Same units and same source as the total: system prompt bytes as sent, tool
// declarations through llm.ToolsWireJSON, both divided by llm.BytesPerToken.
// The parts are computed where the total is, when the view is installed
// (applyMCPView), so the two never describe different views.

// Overhead part kinds.
const (
	// OverheadPrompt is one labeled system-prompt share; Name is a PromptPart key.
	OverheadPrompt = "prompt"
	// OverheadTool is one tool declaration; Name is the tool's wire name.
	OverheadTool = "tool"
	// OverheadSkillTools is the declarations a Skill brings: a host binding
	// group or a Skill manifest's tools. Name is the Skill's name.
	OverheadSkillTools = "skill_tools"
	// OverheadMCP is one MCP server's visible declarations; Name is the server.
	OverheadMCP = "mcp"
)

// OverheadPart is one itemized share of the fixed per-request cost.
type OverheadPart struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Tokens int64  `json:"tokens"`
	// Count is how many declarations a grouped part holds; zero otherwise.
	Count int `json:"count,omitempty"`
}

// OverheadBreakdown is OverheadTokens together with its parts, read under one
// lock so both describe the same view. Safe from any goroutine; the slice is
// the caller's own.
func (a *Agent) OverheadBreakdown() (int64, []OverheadPart) {
	a.overheadMu.RLock()
	defer a.overheadMu.RUnlock()
	return a.fixedCostTokens, append([]OverheadPart(nil), a.overheadParts...)
}

// overheadPartsFor itemizes the prompt modelSystemPrompt builds and the given
// declarations. Prompt parts keep prompt order and tool parts view order;
// interfaces sort. Several declarations from one Skill or one MCP server are
// one part, a lone one is listed as itself.
func (a *Agent) overheadPartsFor(schemas []llm.ToolSchema) []OverheadPart {
	var out []OverheadPart
	for _, p := range append(append([]PromptPart(nil), a.systemPromptParts...), a.systemPromptHints()...) {
		if tokens := int64(len(p.Text) / llm.BytesPerToken); tokens > 0 {
			out = append(out, OverheadPart{Kind: OverheadPrompt, Name: p.Key, Tokens: tokens})
		}
	}

	type group struct {
		kind, name, first string
		bytes, count      int
	}
	var groups []*group
	byKey := map[string]*group{}
	for _, s := range schemas {
		// The same fallback toolSchemaBytes uses for a declaration that will
		// not marshal; one array per measurement, whose brackets belong to the
		// whole list rather than to this declaration.
		bytes := len(s.Name) + len(s.Description)
		if wire, err := llm.ToolsWireJSON([]llm.ToolSchema{s}); err == nil {
			bytes = max(0, len(wire)-2)
		}
		kind, name := a.overheadGroupOf(s.Name)
		key := kind + "\x00" + name
		g := byKey[key]
		if g == nil {
			g = &group{kind: kind, name: name, first: s.Name}
			byKey[key] = g
			groups = append(groups, g)
		}
		g.bytes += bytes
		g.count++
	}
	for _, g := range groups {
		part := OverheadPart{Kind: g.kind, Name: g.name, Tokens: int64(g.bytes / llm.BytesPerToken), Count: g.count}
		if g.count == 1 {
			part = OverheadPart{Kind: OverheadTool, Name: g.first, Tokens: part.Tokens}
		}
		if part.Tokens > 0 {
			out = append(out, part)
		}
	}
	return out
}

// overheadGroupOf names the part a declaration belongs to.
func (a *Agent) overheadGroupOf(name string) (kind, group string) {
	if t, ok := a.registry.Get(name); ok {
		switch t := t.(type) {
		case *tools.MCPTool:
			if server := t.ServerName(); server != "" {
				return OverheadMCP, server
			}
		case *tools.SynthTool:
			if skill := t.SkillIdentity().Name; skill != "" {
				return OverheadSkillTools, skill
			}
		}
	}
	if binding := tools.SkillBindingForTool(name); binding != "" {
		return OverheadSkillTools, binding
	}
	return OverheadTool, name
}
