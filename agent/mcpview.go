package agent

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

// This file implements layered MCP discovery: when the external catalog is
// too large to send on every request, the model sees the local tools, the
// mcp_describe helper and a handful of directory-role hints, while the full
// frozen registry — and therefore the helper's index — keeps every tool
// reachable. A successful mcp_describe detail call selects that exact tool
// (matched by wire name and definition fingerprint against the frozen
// registry) and its declaration appears on the next request, and on every
// later request — across runs too — while the result that selected it is in
// the history (historyMCPSelection).
//
// The invariants that keep this safe:
//
//   - Definitions never change mid-run: the view only reveals or hides
//     declarations the current registry already froze, and only at the batch
//     boundary, on the loop goroutine, before the next model request.
//   - Visibility is not authorization. An undisplayed tool is still known and
//     callable; selection, validation, the gate, events and the bound caller
//     run unchanged either way.
//   - Only the genuine local helper can select: typed tool identity plus the
//     structured Details provenance (wire name + fingerprint), never parsed
//     result text, so no other tool can spoof a selection. Re-deriving the
//     set from history reads the same structured Details, only of results
//     paired with mcp_describe calls, and only while the genuine helper is
//     installed.
//
// Name resolution — accepting a unique exact remote (server-declared) tool
// name, optionally scoped by server — lives entirely inside the helper.
// Selection here only ever sees the canonical local wire name and
// fingerprint the resolved detail answer carries, so ambiguity and server
// mismatch fail in the helper and can never activate a wrong tool.

const (
	// mcpDeferredCountThreshold defers the MCP section when it holds more
	// than this many tools.
	mcpDeferredCountThreshold = 32
	// mcpWireByteThreshold defers when the MCP declarations alone exceed this
	// many wire bytes, however few the tools.
	mcpWireByteThreshold = 64 * 1024
	// mcpViewByteBudget bounds the external (MCP) schema bytes in the model
	// view: directory hints plus selected tools together. A single requested
	// schema larger than the whole budget is still included on its own —
	// never truncated, never stranded.
	mcpViewByteBudget = 64 * 1024
	// mcpViewMaxSelected bounds how many explicitly selected tools the view
	// retains; older selections are evicted LRU-style and can be re-selected
	// with another detail call.
	mcpViewMaxSelected = 16
	// mcpViewMaxDirectories bounds the directory-role hints in the initial
	// deferred view.
	mcpViewMaxDirectories = 8
)

// mcpSelection is one tool chosen through mcp_describe. The fingerprint pins
// the selection to the exact frozen definition, so a registry refresh can
// never activate a stale name silently: a refresh re-derives the set and drops
// every entry whose definition changed.
type mcpSelection struct {
	name        string
	fingerprint string
	bytes       int
	// since orders the declarations. A tool keeps the position it was first
	// revealed at for as long as it stays selected, so describing a visible
	// tool again refreshes its recency (eviction order) without moving it on
	// the wire.
	since int
}

// pendingSelection is a selection observed while a batch ran. The index keeps
// same-batch ordering deterministic regardless of goroutine completion order.
type pendingSelection struct {
	index       int
	name        string
	fingerprint string
}

// mcpToolsInOrder lists the registry's MCP wrappers in registry order.
func mcpToolsInOrder(r *tools.Registry) []*tools.MCPTool {
	var out []*tools.MCPTool
	for _, t := range r.All() {
		if mt, ok := t.(*tools.MCPTool); ok {
			out = append(out, mt)
		}
	}
	return out
}

// mcpSchemaBytes is the per-tool counterpart of toolSchemaBytes, used for the
// threshold check and the view budget. Use the provider wire envelope, not
// Go's exported struct field names. Counting array delimiters per tool is a
// small conservative overestimate when multiple declarations share a list.
func mcpSchemaBytes(t *tools.MCPTool) int {
	return toolSchemaBytes([]llm.ToolSchema{{
		Name: t.Name(), Description: t.Description(), InputSchema: t.InputSchema(),
	}})
}

// mcpDeferredFor decides whether this registry's MCP section is served
// lazily. Below both thresholds the eager view stays byte-for-byte what it
// always was. A registry whose helper is missing or shadowed by a caller's
// own tool named mcp_describe fails open to eager visibility: there would be
// no discovery path for hidden tools, and withholding them would only strand
// capabilities. That is compatibility behavior, not an authorization choice.
func mcpDeferredFor(r *tools.Registry) bool {
	mts := mcpToolsInOrder(r)
	if len(mts) == 0 {
		return false
	}
	if len(mts) <= mcpDeferredCountThreshold {
		total := 0
		for _, mt := range mts {
			total += mcpSchemaBytes(mt)
		}
		if total <= mcpWireByteThreshold {
			return false
		}
	}
	helper, ok := r.Get(tools.MCPDescribeName)
	if !ok {
		return false
	}
	_, genuine := helper.(*tools.MCPDescribe)
	return genuine
}

// rebuildMCPView recomputes the model-visible schemas from the registry and
// the current selection set, then fixes the accounting. Called on the loop
// goroutine only: from New, ReplaceMCP, the run boundary, and after a settled
// batch — never while tool goroutines are running.
//
// The order is append-friendly for prefix caches: the stable view first (the
// registry order, minus everything revealed later), then what the
// conversation revealed — loaded Skill-bound groups in load order, then
// selected MCP tools oldest first. A new reveal therefore extends the tools
// array instead of shifting every declaration after its registry position.
func (a *Agent) rebuildMCPView() {
	all := a.registry.All()
	// Skill-bound groups the history has not loaded stay out of both views;
	// they are native tools, so the MCP budget below never counts them.
	skillView := a.skillToolView()
	a.skillWithheld, a.skillDeferrable = skillView.withheld, skillView.deferrable
	withheld := skillToolNames(skillView.withheld)
	revealed := skillToolNames(skillView.loaded)
	if !mcpDeferredFor(a.registry) {
		a.mcpDeferred = false
		a.mcpSelected = nil
		schemas := make([]llm.ToolSchema, 0, len(all))
		for _, t := range all {
			if withheld[t.Name()] || revealed[t.Name()] {
				continue
			}
			schemas = append(schemas, modelToolSchema(t))
		}
		schemas = append(schemas, a.loadedSkillSchemas(skillView.loaded)...)
		a.applyMCPView(schemas, "")
		return
	}
	a.mcpDeferred = true

	// Byte-fit the selection set newest-first: the most recent choices are
	// the ones the model is most likely about to call. One requested schema
	// larger than the whole budget is included alone — alongside the local
	// tools and the helper — rather than truncated or dropped.
	selected := a.mcpSelected
	visible := make(map[string]bool, len(selected)+mcpViewMaxDirectories)
	// Iterate backwards into a separate buffer: reusing selected[:0] would
	// overwrite older entries before the reverse scan reaches them.
	keep := make([]mcpSelection, 0, len(selected))
	budget := mcpViewByteBudget
	if n := len(selected); n > 0 && selected[n-1].bytes > mcpViewByteBudget {
		newest := selected[n-1]
		visible[newest.name] = true
		keep = append(keep, newest)
		budget = 0
	} else {
		for i := len(selected) - 1; i >= 0; i-- {
			s := selected[i]
			if s.bytes <= budget {
				visible[s.name] = true
				keep = append(keep, s)
				budget -= s.bytes
			}
		}
	}
	// keep is newest-first; store oldest-first so the list's tail is always
	// the most recent selection.
	for i, j := 0, len(keep)-1; i < j; i, j = i+1, j-1 {
		keep[i], keep[j] = keep[j], keep[i]
	}
	a.mcpSelected = keep

	// Directory hints ride the remaining budget: up to a handful, in registry
	// order, skipping ones already selected or too large for what is left.
	dirs := 0
	var keyParts []string
	for _, t := range all {
		mt, ok := t.(*tools.MCPTool)
		if !ok {
			continue
		}
		if visible[mt.Name()] {
			continue
		}
		if dirs >= mcpViewMaxDirectories || mt.CatalogRole() != tools.MCPCatalogRoleDirectory {
			continue
		}
		b := mcpSchemaBytes(mt)
		if b > budget {
			continue
		}
		visible[mt.Name()] = true
		budget -= b
		dirs++
	}

	// The stable view: native tools in registry order and the directory
	// hints, which are chosen in registry order too. A selected directory
	// tool keeps its place there rather than moving to the tail.
	schemas := make([]llm.ToolSchema, 0, len(all))
	for _, t := range all {
		if mt, ok := t.(*tools.MCPTool); ok {
			if !visible[mt.Name()] || mt.CatalogRole() != tools.MCPCatalogRoleDirectory {
				continue
			}
			keyParts = append(keyParts, mt.Name()+"="+mt.DefinitionFingerprint())
		} else if withheld[t.Name()] || revealed[t.Name()] {
			continue
		}
		schemas = append(schemas, modelToolSchema(t))
	}
	// What the conversation revealed, in reveal order.
	schemas = append(schemas, a.loadedSkillSchemas(skillView.loaded)...)
	revealOrder := slices.Clone(a.mcpSelected)
	slices.SortStableFunc(revealOrder, func(x, y mcpSelection) int { return cmp.Compare(x.since, y.since) })
	for _, s := range revealOrder {
		t, _ := a.registry.Get(s.name)
		mt, ok := t.(*tools.MCPTool)
		if !ok || !visible[s.name] || mt.CatalogRole() == tools.MCPCatalogRoleDirectory {
			continue
		}
		keyParts = append(keyParts, mt.Name()+"="+mt.DefinitionFingerprint())
		schemas = append(schemas, modelToolSchema(mt))
	}
	a.applyMCPView(schemas, strings.Join(keyParts, ";"))
}

// loadedSkillSchemas declares loaded Skill-bound groups in load order, each
// group's tools in binding order.
func (a *Agent) loadedSkillSchemas(groups []tools.DeferredSkillBinding) []llm.ToolSchema {
	var out []llm.ToolSchema
	for _, g := range groups {
		for _, name := range g.Tools {
			if t, ok := a.registry.Get(name); ok {
				out = append(out, modelToolSchema(t))
			}
		}
	}
	return out
}

// modelToolSchema keeps alias metadata separate from provider declarations.
func modelToolSchema(t tools.Tool) llm.ToolSchema {
	s := llm.ToolSchema{Name: t.Name(), Description: t.Description(), InputSchema: t.InputSchema()}
	if mt, ok := t.(*tools.MCPTool); ok {
		s.RemoteName = mt.ServerToolName()
	}
	return s
}

// applyMCPView installs a schema list and refreshes the fixed-cost accounting.
// The provider's last input measurement covered the previous declarations, so
// any change to the visible MCP set — even one that leaves the byte count
// identical — invalidates it. See ReplaceMCP for the marker's contract.
func (a *Agent) applyMCPView(schemas []llm.ToolSchema, key string) {
	previousWire, previousErr := llm.ToolsWireJSON(a.schemas)
	wire, err := llm.ToolsWireJSON(schemas)
	changed := previousErr != nil || err != nil || key != a.mcpViewKey || !bytes.Equal(previousWire, wire)
	a.schemas = schemas
	a.mcpViewKey = key
	if err == nil {
		a.toolSchemaTokens = int64(len(wire) / llm.BytesPerToken)
	} else {
		a.toolSchemaTokens = int64(toolSchemaBytes(schemas) / llm.BytesPerToken)
	}
	// Itemized on the same view, before the lock: the readers it guards
	// against only ever see a complete pair.
	parts := a.overheadPartsFor(schemas)
	a.overheadMu.Lock()
	a.systemPromptTokens = int64(len(a.modelSystemPrompt()) / llm.BytesPerToken)
	a.fixedCostTokens = a.systemPromptTokens + a.toolSchemaTokens
	a.overheadParts = parts
	a.overheadMu.Unlock()
	if changed {
		a.measuredThrough = math.MaxInt
	}
}

// resetMCPSelection discards in-flight selections and re-derives the selected
// set from the live history (historyMCPSelection), then rebuilds the view.
// Runs, history replacement and registry refreshes all come through here.
//
// Selections used to start empty on every run, so each new run re-declared a
// smaller tool list and the model paid a describe round trip to grow it back:
// two provider cache misses per run for no change in what the conversation
// knew. Deriving the set from the history keeps a tool declared for as long
// as the describe result that revealed it is in the conversation, and drops
// it with that result — on rewind, compaction or a refresh that changed its
// definition.
func (a *Agent) resetMCPSelection() {
	a.selectMu.Lock()
	a.pendingSelect = nil
	a.selectMu.Unlock()
	a.mcpSelected = a.historyMCPSelection()
	a.rebuildMCPView()
}

// historyMCPSelection replays the history's successful mcp_describe detail
// results against the current registry, under the same rules as a live
// selection (applyPendingSelections): structured Details provenance only,
// never result text; the wire name must be an MCP tool of the frozen registry
// with the described definition fingerprint; recency order and the
// mcpViewMaxSelected bound. Index calls, failures and gate refusals carry no
// such details and select nothing.
//
// A shadowing caller tool named mcp_describe disables deferral altogether
// (mcpDeferredFor), so this only runs with the genuine helper installed. A
// history entry still cannot reveal anything but a current, unchanged
// definition, and revealing is never authorization.
func (a *Agent) historyMCPSelection() []mcpSelection {
	if !mcpDeferredFor(a.registry) {
		return nil
	}
	describes := map[string]bool{}
	var selected []mcpSelection
	for _, m := range a.messages {
		for _, b := range m.Content {
			switch b.Type {
			case llm.BlockToolUse:
				if b.Name == tools.MCPDescribeName {
					describes[b.ID] = true
				}
			case llm.BlockToolResult:
				if !describes[b.ToolUseID] || b.IsError || len(b.Details) == 0 {
					continue
				}
				var d tools.MCPDescribeDetails
				if json.Unmarshal(b.Details, &d) != nil {
					continue
				}
				if sel, ok := a.verifiedSelection(d.WireName, d.Fingerprint); ok {
					selected = withSelection(selected, sel)
				}
			}
		}
	}
	return selected
}

// verifiedSelection checks a described tool against the frozen registry.
func (a *Agent) verifiedSelection(name, fingerprint string) (mcpSelection, bool) {
	if name == "" || fingerprint == "" {
		return mcpSelection{}, false
	}
	t, ok := a.registry.Get(name)
	if !ok {
		return mcpSelection{}, false
	}
	mt, ok := t.(*tools.MCPTool)
	if !ok || mt.DefinitionFingerprint() != fingerprint {
		return mcpSelection{}, false
	}
	return mcpSelection{name: name, fingerprint: fingerprint, bytes: mcpSchemaBytes(mt)}, true
}

// withSelection records sel as the most recent selection: re-selection
// refreshes recency rather than duplicating the entry (and keeps its display
// position), a new entry is placed after every current one, and the oldest
// entries beyond mcpViewMaxSelected are evicted.
func withSelection(selected []mcpSelection, sel mcpSelection) []mcpSelection {
	next, found := 0, false
	kept := selected[:0]
	for _, s := range selected {
		next = max(next, s.since+1)
		if s.name == sel.name {
			sel.since, found = s.since, true
			continue
		}
		kept = append(kept, s)
	}
	if !found {
		sel.since = next
	}
	selected = append(kept, sel)
	for len(selected) > mcpViewMaxSelected {
		selected = selected[1:]
	}
	return selected
}

// noteDescribeDetail records a selection from a finished tool call. It runs on
// batch goroutines, so it only appends under a lock; the view itself is
// rebuilt later, on the loop goroutine, once every sibling has settled.
//
// The typed checks are the anti-spoofing boundary: the tool instance must be
// the genuine local helper (a caller's own tool with the same name fails the
// assertion), the call must have succeeded, and the provenance must come from
// the structured Details — never from parsing result text.
func (a *Agent) noteDescribeDetail(index int, tool tools.Tool, details any, err error) {
	if err != nil || !a.mcpDeferred {
		return
	}
	if _, ok := tool.(*tools.MCPDescribe); !ok {
		return
	}
	d, ok := details.(tools.MCPDescribeDetails)
	if !ok || d.WireName == "" || d.Fingerprint == "" {
		return
	}
	a.selectMu.Lock()
	a.pendingSelect = append(a.pendingSelect, pendingSelection{
		index: index, name: d.WireName, fingerprint: d.Fingerprint,
	})
	a.selectMu.Unlock()
}

// applyPendingSelections folds the settled batch's selections into the view,
// in call order. Each candidate is verified against the frozen registry by
// wire name and fingerprint: a name the current snapshot does not know, or
// whose definition changed since the helper described it, selects nothing.
// Failed, blocked and index calls never reach this list.
func (a *Agent) applyPendingSelections() {
	a.selectMu.Lock()
	pending := a.pendingSelect
	a.pendingSelect = nil
	a.selectMu.Unlock()
	if len(pending) == 0 {
		return
	}
	sort.SliceStable(pending, func(i, j int) bool { return pending[i].index < pending[j].index })
	changed := false
	for _, p := range pending {
		sel, ok := a.verifiedSelection(p.name, p.fingerprint)
		if !ok {
			continue
		}
		a.mcpSelected = withSelection(a.mcpSelected, sel)
		changed = true
	}
	if changed {
		a.rebuildMCPView()
	}
}

// mcpViewSummary describes the current view for tests and diagnostics.
func (a *Agent) mcpViewSummary() string {
	names := make([]string, 0, len(a.schemas))
	for _, s := range a.schemas {
		names = append(names, s.Name)
	}
	return fmt.Sprintf("deferred=%t schemas=%d selected=%d", a.mcpDeferred, len(names), len(a.mcpSelected))
}
