// Command measure-schema reports what the tool declarations cost on the wire.
//
// It exists because tool schemas are the larger half of the fixed per-turn cost —
// SystemPrompt's own header puts them at several times the system prompt and names
// them as what to optimise first — and because the batching-and-parallelism section
// of docs/harness-design.md ("一个批次分两个阶段：review 然后 execute") requires a
// change that touches a description to quote measured bytes rather than an
// impression. This is the instrument those quotes come from.
//
// It measures the *whole* registry a top-level session gets, not just the file and
// shell built-ins. The earlier version registered seven tools and so could not see
// the three largest: subagent, todo and subagent_tasks together are more than half
// the total, and a measurement that omits the biggest items is worse than none.
//
// The bytes come from llm.ToolsWireJSON, which is the same function the request
// path uses. This program used to carry its own copy of that conversion with a
// comment saying it was copied "to maintain consistency"; a second renderer of the
// same bytes is where the two quietly disagree, and the one thing a size
// measurement must not do is measure something other than what is sent.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func main() {
	// The shape a browser session has: the file and shell built-ins, the task list,
	// and delegation with a background runner. Skill-declared tools are deliberately
	// absent — they belong to a deployment, not to pogo, and their size is the
	// bundle author's business (see MaxSynthDescriptionLength).
	//
	// SkillPaths is left at its zero value, so this reports the
	// default deployment rather than the largest one. Both make the subagent
	// description longer; -verbose prints the spread.
	registry := tools.New(tools.Options{
		Cwd:         ".",
		Todo:        true,
		Information: &tools.Information{},
		Browser:     schemaBrowserLink{},
		Subagent:    &tools.Subagent{Cwd: ".", Bg: &tools.BgRunner{}, Browser: schemaBrowserLink{}},
	})

	schemas := make([]llm.ToolSchema, 0, len(registry.All()))
	for _, t := range registry.All() {
		schemas = append(schemas, llm.ToolSchema{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}

	all, err := llm.ToolsWireJSON(schemas)
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshalling tool schemas: %v\n", err)
		os.Exit(1)
	}

	// Per tool, measured the same way: one tool through the same function, so the
	// parts and the whole are the same kind of number. They do not add up to the
	// total — each single-tool array carries its own brackets — and the difference
	// is reported rather than hidden.
	type row struct {
		name       string
		wire, desc int
		params     int
	}
	rows := make([]row, 0, len(schemas))
	sum := 0
	for _, s := range schemas {
		b, err := llm.ToolsWireJSON([]llm.ToolSchema{s})
		if err != nil {
			fmt.Fprintf(os.Stderr, "marshalling %s: %v\n", s.Name, err)
			os.Exit(1)
		}
		rows = append(rows, row{s.Name, len(b), len(s.Description), paramDescBytes(s.InputSchema)})
		sum += len(b)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].wire > rows[j].wire })

	fmt.Printf("Tool schemas on the wire: %d bytes across %d tools\n\n", len(all), len(schemas))
	fmt.Printf("%-16s %8s %8s %8s %8s\n", "tool", "wire", "desc", "params", "struct")
	fmt.Println("-------------------------------------------------------")
	for _, r := range rows {
		fmt.Printf("%-16s %8d %8d %8d %8d\n", r.name, r.wire, r.desc, r.params, r.wire-r.desc-r.params)
	}
	fmt.Println("-------------------------------------------------------")
	fmt.Printf("%-16s %8d\n", "sum of parts", sum)
	fmt.Printf("%-16s %8d  (one array, not %d)\n", "whole array", len(all), len(schemas))

	var desc, params int
	for _, r := range rows {
		desc += r.desc
		params += r.params
	}
	fmt.Printf("\nprose %d bytes (%.0f%%): descriptions %d, parameter descriptions %d\n",
		desc+params, 100*float64(desc+params)/float64(sum), desc, params)
	fmt.Printf("JSON structure %d bytes (%.0f%%)\n", sum-desc-params, 100*float64(sum-desc-params)/float64(sum))

	// The same number the browser's overhead figure is built from: agent.New measures
	// these bytes through llm.ToolsWireJSON too, so this program and the meter no
	// longer disagree. They did until the padded estimator that used to live in
	// agent/loop.go was deleted — it read about 13% high on this registry.
	//
	// What remains approximate is only the conversion: exact bytes, nominal rate.
	fmt.Printf("\nAt %d bytes per token: ~%d tokens per request\n", llm.BytesPerToken, len(all)/llm.BytesPerToken)

	if len(os.Args) > 1 && os.Args[1] == "-verbose" {
		fmt.Println("\nSubagent description by session shape (it is assembled conditionally):")
		for _, v := range []struct {
			label string
			s     *tools.Subagent
		}{
			{"terminal (no background runner)", &tools.Subagent{Cwd: "."}},
			{"browser (background runner)", &tools.Subagent{Cwd: ".", Bg: &tools.BgRunner{}}},
			{"browser + skills", &tools.Subagent{Cwd: ".", Bg: &tools.BgRunner{}, SkillPaths: []string{"/opt/skills/x"}}},
		} {
			fmt.Printf("  %-34s %5d bytes\n", v.label, len(v.s.Description()))
		}
	}
}

// Advertise the same native executor as the web host without connecting to a
// browser. Measuring declarations must not create or operate a real session.
type schemaBrowserLink struct{}

func (schemaBrowserLink) BrowserBackend() string { return tools.BrowserBackendNative }
func (schemaBrowserLink) Execute(context.Context, json.RawMessage) (tools.BrowserReply, error) {
	return tools.BrowserReply{}, fmt.Errorf("schema measurement cannot execute browser actions")
}

// paramDescBytes totals the description strings anywhere inside a parameter
// schema. It separates the prose a person writes from the JSON a schema needs,
// which is the split that says whether a tool is expensive because it explains a
// lot or because it takes a lot.
func paramDescBytes(v any) int {
	switch t := v.(type) {
	case map[string]any:
		n := 0
		for k, val := range t {
			if s, ok := val.(string); ok && k == "description" {
				n += len(s)
				continue
			}
			n += paramDescBytes(val)
		}
		return n
	case []any:
		n := 0
		for _, e := range t {
			n += paramDescBytes(e)
		}
		return n
	case json.Marshaler:
		// orderedProps and anything else with its own marshaller: round-trip it so
		// the walk sees the same shape the wire does rather than skipping it.
		b, err := t.MarshalJSON()
		if err != nil {
			return 0
		}
		var back any
		if json.Unmarshal(b, &back) != nil {
			return 0
		}
		return paramDescBytes(back)
	case map[string]json.RawMessage:
		return 0
	}
	return 0
}
