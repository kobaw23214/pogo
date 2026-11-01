package workflow

// Task templates: {{ nodes.<id>.artifact }}, {{ nodes.<id>.summary }} and
// {{ run.workspace }} inside a node's task. References are validated at save
// time (the node must exist, sit in the referencer's upstream closure, and
// declare expect for artifact references) and evaluated when the Runner
// assembles the node's input. No template engine — one regexp, one Replacer.
import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// templatePattern matches one balanced {{ expr }} span. Unbalanced braces are
// literal text; a balanced span whose expression parses as no known reference
// is a save-time error, because the author clearly meant a reference.
var templatePattern = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)

var inputKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// ValidInputKey keeps external business fields separate from graph paths.
func ValidInputKey(key string) bool { return inputKeyPattern.MatchString(key) }

// templateRef is one parsed reference. Node is empty for run.workspace.
type templateRef struct {
	raw   string
	node  string
	field string // "artifact" | "summary" | "workspace"
	input string
}

func parseTemplateRef(expr string) (templateRef, bool) {
	expr = strings.TrimSpace(expr)
	if expr == "run.workspace" {
		return templateRef{raw: expr, field: "workspace"}, true
	}
	if key, ok := strings.CutPrefix(expr, "inputs."); ok && ValidInputKey(key) {
		return templateRef{raw: expr, field: "input", input: key}, true
	}
	rest, ok := strings.CutPrefix(expr, "nodes.")
	if !ok {
		return templateRef{}, false
	}
	// Node ids may carry dots themselves, so the field is what follows the
	// last dot.
	dot := strings.LastIndex(rest, ".")
	if dot <= 0 {
		return templateRef{}, false
	}
	ref := templateRef{raw: expr, node: rest[:dot], field: rest[dot+1:]}
	if ref.field != "artifact" && ref.field != "summary" {
		return templateRef{}, false
	}
	return ref, true
}

// validateTemplates checks every reference in every task and prompt against
// the graph: the referenced node must exist and sit upstream of the
// referencer, and an artifact reference needs the referenced node to produce
// one — an expect declaration, or human kind (its decision file is the
// artifact).
func (d Definition) validateTemplates(ids map[string]bool) error {
	for _, n := range d.Nodes {
		for _, text := range []string{n.Task, n.Prompt} {
			refs := templatePattern.FindAllStringSubmatch(text, -1)
			if len(refs) == 0 {
				continue
			}
			upstream := d.closure(n.ID)
			for _, m := range refs {
				ref, ok := parseTemplateRef(m[1])
				if !ok {
					return fmt.Errorf("node %q: unknown template reference %q", n.ID, strings.TrimSpace(m[1]))
				}
				if ref.field == "workspace" || ref.field == "input" {
					continue
				}
				if !ids[ref.node] {
					return fmt.Errorf("node %q: template references unknown node %q", n.ID, ref.node)
				}
				if !upstream[ref.node] {
					return fmt.Errorf("node %q: template reference %s is not an upstream dependency", n.ID, ref.raw)
				}
				if ref.field == "artifact" {
					dep := d.node(ref.node)
					if dep.Kind != KindHuman && (dep.Expect == nil || dep.Expect.Path == "") {
						return fmt.Errorf("node %q: template reference %s but node %q declares no expect", n.ID, ref.raw, ref.node)
					}
				}
			}
		}
	}
	return nil
}

// InputKeys lists the external values referenced by this immutable definition.
// Publication schemas must declare them before accepting an integration.
func (d Definition) InputKeys() []string {
	keys := map[string]bool{}
	for _, n := range d.Nodes {
		for _, value := range []string{n.Task, n.Prompt} {
			for _, match := range templatePattern.FindAllStringSubmatch(value, -1) {
				if ref, ok := parseTemplateRef(match[1]); ok && ref.field == "input" {
					keys[ref.input] = true
				}
			}
		}
	}
	out := make([]string, 0, len(keys))
	for key := range keys {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// evalTemplates resolves a task's references against the settled node states.
// Any reference whose value is empty — the upstream is not in a value-bearing
// state, or ran dry — is an error naming the reference: substituting a blank
// would hand the node a brief that reads like success.
func evalTemplates(task string, states map[string]NodeState, workspace string, runInputs ...map[string]string) (string, map[string]string, error) {
	if !templatePattern.MatchString(task) {
		return task, nil, nil
	}
	inputs := map[string]string{}
	var failed error
	out := templatePattern.ReplaceAllStringFunc(task, func(span string) string {
		if failed != nil {
			return span
		}
		inner := templatePattern.FindStringSubmatch(span)[1]
		ref, ok := parseTemplateRef(inner)
		if !ok {
			failed = fmt.Errorf("unknown template reference %q", strings.TrimSpace(inner))
			return span
		}
		if ref.field == "workspace" {
			inputs[ref.raw] = workspace
			return workspace
		}
		if ref.field == "input" {
			value, exists := "", false
			if len(runInputs) > 0 {
				value, exists = runInputs[0][ref.input]
			}
			if !exists || strings.TrimSpace(value) == "" {
				failed = fmt.Errorf("template reference %s has no input value", ref.raw)
				return span
			}
			inputs[ref.raw] = value
			return value
		}
		st, ok := states[ref.node]
		val := ""
		if ok {
			switch ref.field {
			case "artifact":
				val = st.Artifact
			case "summary":
				val = st.Summary
			}
		}
		if val == "" {
			status := "missing"
			if ok {
				status = st.Status
			}
			failed = fmt.Errorf("template reference %s has no value (node %q is %s)", ref.raw, ref.node, status)
			return span
		}
		inputs[ref.raw] = val
		return val
	})
	if failed != nil {
		return "", nil, failed
	}
	return out, inputs, nil
}
