package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

// This is local harness guidance, conditional on the real tool view. It is
// deliberately independent of any server's business catalog or vocabulary.
const mcpDeferredInstruction = `MCP tools are loaded on demand in this session. The native tools you can see are only a subset of the complete frozen catalog.
For EACH planned external operation, check that its own native definition is visible. If it is not, call mcp_describe with that tool's exact local or server tool name (and server when needed), then call the returned wire_name on the next model request.
Use mcp_describe({"filter":"keyword"}) to search when the name is unknown, and mcp_describe({"tool":"EXACT_NAME"}) for detail. The selector is tool, not tool_name. Never invent a tool name from a business phrase.
An index counts available tools, not business records. After discovery, continue the original user task using the relevant operation and its result. Return the requested answer in the user's language, or explain the specific blocker; a plan to read a skill is not a completed result. Load a skill only when it is relevant to that task.
Reading a server's directory, module or capability contract does NOT load its executable tool. Never substitute a visible tool with a different purpose for the operation you intend to perform. Calling one operation repeatedly does not load another. Reuse successful results; if an inspected tool was evicted from the limited view, inspect it again to expose it. Loading a tool never executes or authorizes it.`

// modelSystemPrompt is the system prompt a request carries: the configured
// prompt plus the harness guidance the current tool view calls for.
func (a *Agent) modelSystemPrompt() string {
	prompt := a.systemPrompt
	for _, hint := range a.systemPromptHints() {
		prompt += hint.Text
	}
	return prompt
}

// systemPromptHints is the guidance modelSystemPrompt appends, as labeled
// parts so the overhead breakdown and the request cannot disagree.
func (a *Agent) systemPromptHints() []PromptPart {
	var hints []PromptPart
	if a.hasSchema(tools.WatchName) || a.hasSchema(tools.BackgroundTaskName) {
		hints = append(hints, PromptPart{Key: PromptPartBackground, Text: "\n\n" + backgroundHandoffInstruction})
	}
	if a.mcpDeferred {
		hints = append(hints, PromptPart{Key: PromptPartMCPDeferred, Text: "\n\n" + mcpDeferredInstruction})
	}
	if hint := a.skillToolsInstruction(); hint != "" {
		hints = append(hints, PromptPart{Key: PromptPartSkillTools, Text: "\n\n" + hint})
	}
	return hints
}

// A single model review of a cited answer after multiple successful operation
// reads. It is a prompt to check support, not a claim that the harness can prove
// semantic truth. The existing transcript and SourceStore remain authoritative.
func (r *mcpReadRepeats) reviewNotice(answer string) string {
	if !strings.Contains(answer, "#cite:") {
		return ""
	}
	reads := 0
	for _, n := range r.counts {
		reads += n
	}
	if reads < 2 {
		return ""
	}
	return "[pogo] Evidence review before final delivery: using the existing tool results, check each important " +
		"claim against the actual cited evidence, not just the existence of its label. Reconcile the summary, tables " +
		"and caveats: unknown/null/failed values must not become measured zero; matches on some fields must not " +
		"become complete-record or population-wide agreement. A configuration describes intended processing, not " +
		"proof about all historical data. Keep claims about missing capabilities within the contracts you examined, " +
		"and distinguish inability to prove a precise relationship from impossibility of any comparison. " +
		"Do not infer causes or comparable definitions from numeric differences alone. Narrow unsupported claims " +
		"or state the gap. Use existing evidence only for this review; do not call more tools merely to satisfy it. " +
		"Return the complete final answer with supported citations and material limitations, not a review acknowledgement."
}

// A run-local advisory, not a cache or execution filter. Count successful
// requests with identical arguments, not result bytes: server timestamps can
// legitimately differ. Warn once, preserving deliberate fresh reads, gates,
// tool results and the existing stagnation policy.
type mcpReadRepeats struct {
	counts map[string]int
	warned bool
}

func (r *mcpReadRepeats) notice(a *Agent, calls, results []llm.Block) string {
	if !a.mcpDeferred || r.warned {
		return ""
	}
	byID := make(map[string]llm.Block, len(results))
	for _, result := range results {
		byID[result.ToolUseID] = result
	}
	for _, call := range calls {
		result, ok := byID[call.ID]
		if !ok || result.IsError {
			continue
		}
		tool, _ := a.registry.Get(call.Name)
		mt, ok := tool.(*tools.MCPTool)
		if !ok || mt.CatalogRole() == tools.MCPCatalogRoleDirectory {
			continue
		}
		annotations := mt.Annotations()
		if annotations == nil || !annotations.ReadOnlyHint {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(call.Input))
		decoder.UseNumber()
		var args any
		if decoder.Decode(&args) != nil {
			continue
		}
		canonical, err := json.Marshal(args)
		if err != nil {
			continue
		}
		key := fmt.Sprintf("%x", sha256.Sum256(append([]byte(call.Name+"\x00"), canonical...)))
		if r.counts == nil {
			r.counts = make(map[string]int)
		}
		r.counts[key]++
		if r.counts[key] < 2 {
			continue
		}
		r.warned = true
		return fmt.Sprintf("[pogo] Repeated MCP read: %q has succeeded more than once with the same arguments in this run. "+
			"Reuse the earlier result unless a freshness check is required. If you meant to perform a DIFFERENT operation, "+
			"do not substitute this visible tool: use mcp_describe to inspect that operation's exact tool name, then call "+
			"its wire_name on the next request. Server directory descriptions alone do not load native tools. "+
			"Respect the user's total evidence limits. This reminder has not retried, blocked or approved any call.", call.Name)
	}
	return ""
}
