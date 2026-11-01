package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

// The tool declarations are sent on every turn of every session, exactly like the
// system prompt, and they are the larger of the two: SystemPrompt's own header puts
// tool schemas at several times its size and names them as what to optimise first.
// Until now only the smaller half had a ceiling.
//
// This is TestSystemPromptStaysSmall applied to the bigger cost, and for the same
// reason: to make that cost a decision instead of a drift. Every paragraph in a tool
// description was argued for in a comment beside it, and docs/tool-batching-and-parallelism.md
// requires a change that touches one to quote measured bytes. A ceiling is what makes
// the next such change argue past a failing test rather than past nobody.
//
// The number is deliberately close to the current size, on the same principle the
// prompt ceiling states: a ceiling with room to spare is a ceiling nobody notices
// moving. The headroom here is about one reworded sentence — the same absolute slack
// the prompt ceiling carries, which is proportionally tighter because this number is
// larger and matters more.
//
// Measured through llm.ToolsWireJSON, the function the request path itself uses, so
// this counts what is sent rather than a second rendering of it. cmd/measure-schema
// reports the same number with a per-tool breakdown; run it when this fails.
// The ceiling moved from 10600 to 15000 in 2026-08, and the raise has two parts.
//
// About 4300 of it is the browser tool arriving under the ceiling at all. It was
// always registered in every web session and always sent on every turn of one;
// leaving it out of browserSessionOptions measured a session shape that does not
// exist. That part is not growth, it is the ceiling starting to count what was
// already being paid.
//
// The rest is what the browser tool gained: find, scroll, select, press, hover,
// eval and batch, at roughly 2000 bytes of declaration between them. Those bytes
// buy round trips, and a round trip is the expensive unit here — one snapshot
// costs thousands of *output* bytes and a whole model turn, so the schema growth
// pays for itself the first time find replaces a snapshot or batch collapses a
// six-field form into one call. The alternative was keeping a tool that could
// only advise the model to scroll without giving it a scroll action.
//
// The native-browser contract measured on 2026-10-13 is 15654 bytes across 11
// tools (browser: 5367 bytes, including 907 description bytes). Raise the budget
// to 15900 with only 246 bytes of headroom: the native declaration now exposes
// page-scoped claim fences (fresh context_ref/control_epoch), account bookmarks,
// scoped DevTools and calculations over observed quantity refs. These fields
// let the model discover/claim the intended page and inspect bounded evidence
// without guessing ownership or replaying writes. Skill-selected Office tools
// remain outside this baseline; this increase is not an Office-tool allowance.
//
// On 2026-10-18 the current native-browser baseline is 17491 bytes; the compact
// information tool adds 1175 bytes to the array (1176 as a one-tool array).
// Its list/search/versioned-read schema replaces eager library prompt content.
// Measure the actual twelve-tool web session at 18666 bytes and allow 18900,
// retaining 234 bytes of headroom rather than an unmeasured growth allowance.
//
// The saved-result reader adds 5827 wire bytes to that same 18666-byte
// baseline (24493 total across 13 tools). Its scoped recovery, UTF-8/JSON
// pagination and deterministic filtering/aggregation replace replaying actions
// or reading full raw observations into the model. Count this fixed cost
// explicitly rather than hiding the always-registered protected tool.
// The ceiling leaves 207 bytes of headroom; see docs/tool-result-management.md.
// Saved-text literal search adds 501 bytes (24994 total), replacing repeated
// byte-range guesses with bounded matching snippets in the same reader. Allow
// 206 bytes of headroom; no additional always-registered tool was introduced.
// The 2026-10-21 delivery/catalog/order changes reuse tool_result and shorten
// duplicate schema prose: 25145 bytes / 13 tools, still within the same ceiling.
func TestToolSchemasStaySmall(t *testing.T) {
	const ceiling = 25200

	schemas := browserSessionSchemas(t)
	wire, err := llm.ToolsWireJSON(schemas)
	if err != nil {
		t.Fatalf("tool schemas do not marshal, which would fail every request: %v", err)
	}

	// Logged per tool as well as in total, because "the schemas grew by 400 bytes"
	// is not actionable and "subagent grew by 400 bytes" is.
	t.Logf("tool schemas are %d bytes across %d tools", len(wire), len(schemas))
	for _, s := range schemas {
		one, err := llm.ToolsWireJSON([]llm.ToolSchema{s})
		if err != nil {
			t.Fatalf("marshalling %s: %v", s.Name, err)
		}
		t.Logf("  %-16s %5d bytes (description %d)", s.Name, len(one), len(s.Description))
	}

	if len(wire) > ceiling {
		t.Errorf("tool schemas are %d bytes, over the %d ceiling. Adding to them is allowed — "+
			"raise this deliberately and say what the new text buys, the way "+
			"docs/tool-batching-and-parallelism.md quotes the earlier ones. "+
			"Run cmd/measure-schema to see which tool moved", len(wire), ceiling)
	}
}

// A schema that cannot marshal takes down every request, not just its own call, and
// the failure arrives from the provider as a 400 rather than from Go as an error —
// which is how the null `required` list got as far as production once (see the note
// on tools.object). This checks the whole set the way the request path does.
//
// Separate from the ceiling test because the two fail for unrelated reasons: one
// means someone wrote too much, the other means someone built a schema out of
// something JSON cannot express.
func TestToolSchemasMarshalForEverySessionShape(t *testing.T) {
	for _, c := range []struct {
		name string
		opts tools.Options
	}{
		{"terminal", tools.Options{Cwd: t.TempDir()}},
		{"terminal with task list", tools.Options{Cwd: t.TempDir(), Todo: true}},
		{"read-only child", tools.Options{Cwd: t.TempDir(), ReadOnly: true}},
		{"browser", browserSessionOptions(t.TempDir())},
		{"browser, not a git repository", tools.Options{
			Cwd: t.TempDir(), Todo: true,
			Subagent: &tools.Subagent{Cwd: t.TempDir(), Bg: &tools.BgRunner{}},
		}},
		{"browser with skills", tools.Options{
			Cwd: t.TempDir(), Todo: true,
			Subagent: &tools.Subagent{
				Cwd: t.TempDir(), Bg: &tools.BgRunner{},
				SkillPaths: []string{"/opt/skills/example"},
			},
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var schemas []llm.ToolSchema
			for _, tool := range tools.New(c.opts).All() {
				schemas = append(schemas, llm.ToolSchema{
					Name:        tool.Name(),
					Description: tool.Description(),
					InputSchema: tool.InputSchema(),
				})
			}
			wire, err := llm.ToolsWireJSON(schemas)
			if err != nil {
				t.Fatalf("tool schemas do not marshal: %v", err)
			}
			t.Logf("%d tools, %d bytes", len(schemas), len(wire))
		})
	}
}

// browserSessionOptions is the shape a browser session has: the file and shell
// built-ins, shared information, the task list, and background delegation.
//
// Skill-declared tools are deliberately absent. They belong to a deployment rather
// than to pogo, their size is the bundle author's business — bounded separately by
// tools.MaxSynthDescriptionLength — and including them would make this repository's
// ceiling depend on what someone else installed.
//
// Isolation and SkillPaths stay at their zero values, so the ceiling describes the
// default deployment. Both make the subagent description longer, which is why the
// marshal test above covers those shapes and this one does not: they are a
// deployment's cost to account for, not a drift for pogo to guard.
func browserSessionOptions(cwd string) tools.Options {
	return tools.Options{
		Cwd:         cwd,
		Todo:        true,
		Information: &tools.Information{},
		// Browser on both: the session's own pane, and the same link lent to
		// foreground children over a pipe. Mirrors web/manager.go so this measures
		// the session shape that exists rather than one assembled for the test.
		Subagent: &tools.Subagent{Cwd: cwd, Bg: &tools.BgRunner{}, Browser: stubBrowserLink{}},
		// The browser tool is registered in every web session, so it was always
		// part of what a browser session sends and its absence here was an
		// oversight rather than a decision: the tool grew a batch action and six
		// more page actions without this ceiling noticing.
		Browser: stubBrowserLink{},
	}
}

// stubBrowserLink advertises the native executor used by a configured web
// session. Nothing calls it: the ceiling measures production declarations.
type stubBrowserLink struct{}

func (stubBrowserLink) BrowserBackend() string { return tools.BrowserBackendNative }

func (stubBrowserLink) Execute(context.Context, json.RawMessage) (tools.BrowserReply, error) {
	return tools.BrowserReply{}, nil
}

// browserSessionSchemas flattens that registry the way agent.New does.
//
// The loop is repeated here rather than reached through an accessor on Agent: the
// registry used to expose one and it was deleted for having no caller, and adding
// exported surface so a test can see an internal field is how that comes back. Six
// lines of duplication in a test is the cheaper of the two.
func browserSessionSchemas(t *testing.T) []llm.ToolSchema {
	t.Helper()
	registry := tools.New(browserSessionOptions(t.TempDir()))
	schemas := make([]llm.ToolSchema, 0, len(registry.All()))
	for _, tool := range registry.All() {
		schemas = append(schemas, llm.ToolSchema{
			Name:        tool.Name(),
			Description: tool.Description(),
			InputSchema: tool.InputSchema(),
		})
	}
	return schemas
}
