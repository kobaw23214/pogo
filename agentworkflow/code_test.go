package agentworkflow

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func codeFixture() Definition {
	return Definition{Name: "订单金额计算", Revision: 1, Inputs: []Input{{Key: "orders", Type: "array", Required: true}, {Key: "review", Type: "boolean", Default: "yes"}}, Steps: []Step{
		{ID: "calculate", Kind: "code", Code: &CodeSpec{Language: "python", Source: "def main(inputs):\n    return {'total': sum(inputs['orders'])}\n", Inputs: map[string]CodeBinding{"orders": {Type: "array", Source: "inputs.orders"}}, Outputs: map[string]ValueSchema{"total": {Type: "number"}}}},
		{ID: "describe", Kind: "agent", Task: "说明金额 {{ steps.calculate.output.total }}"},
	}}
}

func TestCodeDefinitionHashPatchAndCopies(t *testing.T) {
	d := codeFixture()
	if ds := Validate(d); len(ds) != 0 {
		t.Fatal(ds)
	}
	before := Hash(d)
	next, err := Patch(d, []Operation{{Op: "update_step", StepID: "calculate", Patch: json.RawMessage(`{"code":{"language":"javascript","source":"function main(inputs) { return { total: inputs.orders.length }; }","inputs":{"orders":{"type":"array","source":"inputs.orders"}},"outputs":{"total":{"type":"number"}}}}`)}})
	if err != nil || Hash(next) == before || Hash(d) != before {
		t.Fatal("code patch did not freeze independently", err)
	}
	added := Step{ID: "copy", Kind: "code", Code: &CodeSpec{Language: "python", Source: "def main(inputs):\n return {'n': inputs['n']}", Inputs: map[string]CodeBinding{"n": {Type: "integer", Value: json.RawMessage("3")}}, Outputs: map[string]ValueSchema{"n": {Type: "integer"}}}}
	next, err = Patch(d, []Operation{{Op: "add_step", Step: &added}})
	if err != nil {
		t.Fatal(err)
	}
	added.Code.Inputs["n"] = CodeBinding{Type: "integer", Value: json.RawMessage("9")}
	added.Code.Outputs["n"] = ValueSchema{Type: "string"}
	if string(next.Steps[2].Code.Inputs["n"].Value) != "3" || next.Steps[2].Code.Outputs["n"].Type != "integer" {
		t.Fatal("caller mutated saved nested code")
	}
	if _, err := Patch(d, []Operation{{Op: "update_step", StepID: "calculate", Patch: json.RawMessage(`{"code":null}`)}}); err == nil || Hash(d) != before {
		t.Fatal("invalid patch mutated original")
	}
	legacy := definitionFixture()
	raw, _ := json.Marshal(legacy)
	if strings.Contains(string(raw), `"code"`) || Hash(legacy) != "4e076891a3e522ca69c3d869e6d7ec485ce34b97b2d6fb3a44406af127a414e2" {
		t.Fatal("legacy encoding changed")
	}
}

func TestCodeStructuredInputsPreserveLegacyStorage(t *testing.T) {
	d := codeFixture()
	d.Inputs = append(d.Inputs, Input{Key: "rate", Type: "number", Default: "1.5"}, Input{Key: "count", Type: "integer", Required: true}, Input{Key: "meta", Type: "object", Required: true})
	inputs, err := DecodeInputs(d, json.RawMessage(`{"orders":[1,2.5],"review":false,"count":3,"meta":{"name":"订单"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if inputs["review"] != "no" || inputs["count"] != "3" || inputs["rate"] != "1.5" || inputs["orders"] != "[1,2.5]" {
		t.Fatal(inputs)
	}
	copy := map[string]string{}
	for k, v := range inputs {
		copy[k] = v
	}
	typed, err := TypedInputs(d, inputs)
	if err != nil {
		t.Fatal(err)
	}
	if string(typed["review"]) != "false" || string(typed["orders"]) != "[1,2.5]" || !reflect.DeepEqual(copy, inputs) {
		t.Fatal("typing changed stored inputs", typed, inputs)
	}
	for _, raw := range []string{`{"orders":{},"count":3,"meta":{}}`, `{"orders":[],"count":3.1,"meta":{}}`, `{"orders":[],"count":9007199254740993.5,"meta":{}}`, `{"orders":[],"count":3,"meta":[],"review":false}`, `{"orders":[],"count":3,"meta":{},"unknown":1}`} {
		if _, err := DecodeInputs(d, json.RawMessage(raw)); err == nil {
			t.Fatal("invalid native input accepted", raw)
		}
	}
	legacy := Definition{Inputs: []Input{{Key: "text"}}}
	if _, err := DecodeInputs(legacy, json.RawMessage(`{"text":3}`)); err == nil {
		t.Fatal("numeric value became legacy text silently")
	}
}

func TestCodeBindingDependenciesAndSchemaValidation(t *testing.T) {
	for name, mutate := range map[string]func(*Definition){
		"future": func(d *Definition) {
			d.Steps[0].Code.Inputs["orders"] = CodeBinding{Type: "string", Source: "steps.describe.summary"}
		},
		"self": func(d *Definition) {
			d.Steps[0].Code.Inputs["orders"] = CodeBinding{Type: "array", Source: "steps.calculate.output.total"}
		},
		"unknown input": func(d *Definition) {
			d.Steps[0].Code.Inputs["orders"] = CodeBinding{Type: "array", Source: "inputs.missing"}
		},
		"expression": func(d *Definition) {
			d.Steps[0].Code.Inputs["orders"] = CodeBinding{Type: "array", Source: "inputs.orders.map(run)"}
		},
		"double binding": func(d *Definition) {
			d.Steps[0].Code.Inputs["orders"] = CodeBinding{Type: "array", Source: "inputs.orders", Value: json.RawMessage("[]")}
		},
		"wrong constant": func(d *Definition) {
			d.Steps[0].Code.Inputs["orders"] = CodeBinding{Type: "array", Value: json.RawMessage("4")}
		},
		"unknown output":          func(d *Definition) { d.Steps[1].Task = "{{ steps.calculate.output.missing }}" },
		"invalid language":        func(d *Definition) { d.Steps[0].Code.Language = "bash" },
		"long source":             func(d *Definition) { d.Steps[0].Code.Source = strings.Repeat("x", MaxCodeSourceBytes+1) },
		"invalid timeout":         func(d *Definition) { d.Steps[0].Code.TimeoutSeconds = 61 },
		"missing array items":     func(d *Definition) { d.Steps[0].Code.Outputs["total"] = ValueSchema{Type: "array"} },
		"hidden instructions":     func(d *Definition) { d.Steps[0].Task = "do something else" },
		"unsupported file output": func(d *Definition) { d.Steps[0].OutputFile = "calculated.json" },
		"resource grant":          func(d *Definition) { d.Steps[0].Skills = []string{"skill-id"} },
		"code on agent":           func(d *Definition) { d.Steps[1].Code = d.Steps[0].Code },
	} {
		t.Run(name, func(t *testing.T) {
			d := codeFixture()
			mutate(&d)
			if len(Validate(d)) == 0 {
				t.Fatal("invalid code definition accepted")
			}
		})
	}
	d := parallelGraphFixture()
	d.Steps[1] = codeFixture().Steps[0]
	d.Steps[1].ID = "review"
	d.Inputs = codeFixture().Inputs
	d.Steps[4].Task = "{{ steps.merge.output.review.total }}"
	if ds := Validate(d); len(ds) != 0 {
		t.Fatal("join structured output rejected", ds)
	}
	d.Steps[4].Task = "{{ steps.review.output.total }}"
	if ds := Validate(d); len(ds) != 0 {
		t.Fatal("completed unconditional branch output unavailable after fan-in", ds)
	}
	d.Inputs = append(d.Inputs, Input{Key: "branch"})
	d.Steps[0] = Step{ID: "route", Kind: "condition", Condition: &Condition{Source: "inputs.branch", Operator: "equals", Value: "yes"}}
	d.Graph.Edges[1].Branch, d.Graph.Edges[2].Branch = "true", "false"
	if !graphDiagnostic(Validate(d), "unknown_step") {
		t.Fatal("a possibly skipped condition branch became a required output")
	}
}

func TestCodeResolveTypedDataAndExplicitNodeTest(t *testing.T) {
	d := codeFixture()
	d.Steps[0].Code.Inputs["review"] = CodeBinding{Type: "boolean", Source: "inputs.review"}
	d.Steps[0].Code.Inputs["literal"] = CodeBinding{Type: "string", Value: json.RawMessage(`"{{ literal data }}"`)}
	run := Run{Definition: d, Inputs: map[string]string{"orders": "[1,2]"}}
	raw, err := ResolveCodeInputs(run, "calculate")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	if got["review"] != true || got["literal"] != "{{ literal data }}" || len(run.Inputs) != 1 {
		t.Fatal(got, run.Inputs)
	}
	run.NodeTest = &NodeTest{StepID: "calculate", Inputs: json.RawMessage(`{"orders":[9],"review":false,"literal":"test"}`)}
	run.Inputs = nil
	raw, err = ResolveCodeInputs(run, "calculate")
	if err != nil || string(raw) != string(run.NodeTest.Inputs) {
		t.Fatal("explicit test tried to resolve upstream", string(raw), err)
	}
	run.NodeTest.Inputs = json.RawMessage(`{"orders":[9],"review":"no","literal":"test"}`)
	if _, err := ResolveCodeInputs(run, "calculate"); err == nil {
		t.Fatal("test bypassed input typing")
	}
	run = Run{Definition: d, Inputs: map[string]string{"orders": "[1]"}, Steps: []StepRun{{StepID: "upstream", Status: Completed, Output: json.RawMessage(`{"object":{"items":[7]}}`)}}}
	d.Steps[0].Code.Inputs = map[string]CodeBinding{"n": {Type: "integer", Source: "steps.upstream.output.object.items.0"}}
	run.Definition = d
	raw, err = ResolveCodeInputs(run, "calculate")
	if err != nil || string(raw) != "{\"n\":7}" {
		t.Fatal(string(raw), err)
	}
	run.Steps[0].Status = Running
	if _, err := ResolveCodeInputs(run, "calculate"); err == nil {
		t.Fatal("read uncommitted upstream result")
	}
}

func TestCodeOutputValidationAndBoundedData(t *testing.T) {
	spec := codeFixture().Steps[0].Code
	spec.Outputs = map[string]ValueSchema{"total": {Type: "integer"}, "meta": {Type: "object", Properties: map[string]ValueSchema{"tags": {Type: "array", Items: &ValueSchema{Type: "string"}}}, Required: []string{"tags"}}}
	if err := ValidateCodeOutput(spec, json.RawMessage(`{"total":3,"meta":{"tags":["ok"]}}`)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"total":3}`, `{"total":3,"meta":{"tags":[]},"extra":1}`, `{"total":3.2,"meta":{"tags":[]}}`, `{"total":9007199254740993.5,"meta":{"tags":[]}}`, `{"total":3,"meta":{"tags":[1]}}`, `{"total":3,"meta":{"tags":[],"hidden":1}}`, `{"total":3,"meta":{}}`, `null`, `[]`, `{"total":1e999,"meta":{"tags":[]}}`, `{"total":3,"meta":{"tags":[]}} {}`} {
		if err := ValidateCodeOutput(spec, json.RawMessage(raw)); err == nil {
			t.Fatal("bad output accepted", raw)
		}
	}
	if err := ValidateCodeOutput(spec, json.RawMessage(strings.Repeat(" ", MaxCodeDataBytes+1))); err == nil {
		t.Fatal("oversize output accepted")
	}
}

func TestCodeOutputTemplatesAndConditionsRemainDataOnly(t *testing.T) {
	steps := []StepRun{{StepID: "calculate", Status: Completed, Output: json.RawMessage(`{"total":12.5,"approved":true,"note":"{{ not evaluated }}","items":[{"name":"one"}]}`)}}
	text, err := Render("{{ steps.calculate.output.note }} / {{ steps.calculate.output.items.0.name }}", nil, steps)
	if err != nil || text != "{{ not evaluated }} / one" {
		t.Fatal(text, err)
	}
	for _, source := range []string{"steps.calculate.output.total", "steps.calculate.output.approved"} {
		step := Step{Kind: "condition", Condition: &Condition{Source: source, Operator: "equals", Value: "true"}}
		if strings.HasSuffix(source, "total") {
			step.Condition.Operator = "gt"
			step.Condition.Value = "10"
		}
		if ok, err := EvaluateCondition(step, nil, steps); err != nil || !ok {
			t.Fatal(source, ok, err)
		}
	}
	for _, source := range []string{"steps.calculate.output.items.9.name", "steps.calculate.output.note.length()", "steps.calculate.output.__proto__"} {
		if _, err := Render("{{ "+source+" }}", nil, steps); err == nil {
			t.Fatal("invalid traversal accepted", source)
		}
	}
}

func TestCodeOutputTemplateExpansionIsBoundedBeforeAllocation(t *testing.T) {
	value := strings.Repeat("x", 240<<10)
	raw, _ := json.Marshal(map[string]any{"text": value})
	steps := []StepRun{{StepID: "calculate", Status: Completed, Output: raw}}
	if rendered, err := Render(strings.Repeat("{{ steps.calculate.output.text }}", 5), nil, steps); err == nil || rendered != "" || !strings.Contains(err.Error(), "1MiB") {
		t.Fatal("large output amplification was retained", len(rendered), err)
	}
	if rendered, err := Render(strings.Repeat("{{ steps.calculate.output.text }}", 4), nil, steps); err != nil || len(rendered) != 4*len(value) {
		t.Fatal("bounded typed output rejected", len(rendered), err)
	}
	// Existing definitions without typed output references keep their original
	// expansion contract; reading history does not change old behavior.
	legacyValue := strings.Repeat("a", 16<<10)
	if rendered, err := Render(strings.Repeat("{{ inputs.text }}", 65), map[string]string{"text": legacyValue}, nil); err != nil || len(rendered) != 65*len(legacyValue) {
		t.Fatal("legacy template behavior changed", len(rendered), err)
	}
}
