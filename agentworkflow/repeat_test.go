package agentworkflow

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func repeatCodeBody() *Step {
	return &Step{ID: "body", Kind: "code", Code: &CodeSpec{Language: "python", Source: "def main(inputs):\n    return {'value': inputs['value'] * inputs['factor']}\n", Inputs: map[string]CodeBinding{"value": {Type: "number", Source: "iteration.item.amount"}, "factor": {Type: "number", Source: "inputs.factor"}, "index": {Type: "integer", Source: "iteration.index"}}, Outputs: map[string]ValueSchema{"value": {Type: "number"}}}}
}
func iterationDefinition() Definition {
	return Definition{Name: "批量计算", Inputs: []Input{{Key: "orders", Type: "array", Required: true}, {Key: "factor", Type: "number", Default: "2"}}, Steps: []Step{{ID: "batch", Kind: "iteration", Iteration: &IterationSpec{Items: CodeBinding{Type: "array", Source: "inputs.orders"}, Body: repeatCodeBody(), Concurrency: 2, MaxItems: 10}}}}
}
func loopDefinition() Definition {
	body := &Step{Kind: "code", Code: &CodeSpec{Language: "javascript", Source: "function main(inputs) { return {n:inputs.n+1}; }", Inputs: map[string]CodeBinding{"n": {Type: "integer", Source: "loop.state.n"}, "index": {Type: "integer", Source: "loop.index"}}, Outputs: map[string]ValueSchema{"n": {Type: "integer"}}}}
	return Definition{Name: "有界累加", Steps: []Step{{ID: "repeat", Kind: "loop", Loop: &LoopSpec{Initial: CodeBinding{Type: "object", Value: json.RawMessage(`{"n":0}`)}, Body: body, Until: Condition{Source: "loop.state.n", Operator: "gte", Value: "3"}, MaxIterations: 5}}}}
}

func TestRepeatTypedInputsBodyResolutionAndIsolation(t *testing.T) {
	d := iterationDefinition()
	if ds := Validate(d); len(ds) > 0 {
		t.Fatal(ds)
	}
	run := Run{Definition: d, Inputs: map[string]string{"orders": `[{"amount":3},{"amount":7}]`}}
	items, err := ResolveIterationItems(run, "batch")
	if err != nil || len(items) != 2 {
		t.Fatal(items, err)
	}
	body, err := ResolveRepeatBody(run, "batch", items[1], 1)
	if err != nil {
		t.Fatal(err)
	}
	if body.ID != "batch" || body.Code.Inputs["value"].Source != "" || string(body.Code.Inputs["value"].Value) != "7" || string(body.Code.Inputs["factor"].Value) != "2" || string(body.Code.Inputs["index"].Value) != "1" {
		t.Fatal(body)
	}
	if d.Steps[0].Iteration.Body.Code.Inputs["value"].Source != "iteration.item.amount" {
		t.Fatal("body resolution mutated frozen definition")
	}
	d.Steps[0].Iteration.MaxItems = 1
	run.Definition = d
	if _, err = ResolveIterationItems(run, "batch"); err == nil {
		t.Fatal("oversized batch truncated instead of rejected")
	}
	d.Steps[0].Iteration.MaxItems = 10
	run.Definition = d
	run.Inputs["orders"] = `["` + strings.Repeat("x", MaxRepeatItemBytes) + `"]`
	if _, err = ResolveIterationItems(run, "batch"); err == nil {
		t.Fatal("oversized item admitted")
	}
}

func TestAgentIterationTemplatesAreOnePassAndResourcesStayScoped(t *testing.T) {
	d := iterationDefinition()
	d.Steps[0].Iteration.Body = &Step{Kind: "agent", Task: "处理第{{ iteration.index }}项：{{ iteration.item.text }}；系数{{ inputs.factor }}", Skills: []string{"skill:report"}}
	if ds := Validate(d); len(ds) > 0 {
		t.Fatal(ds)
	}
	run := Run{Definition: d, Inputs: map[string]string{"orders": "[]", "factor": "2"}}
	body, err := ResolveRepeatBody(run, "batch", json.RawMessage(`{"text":"{{ inputs.factor }}"}`), 0)
	if err != nil || body.Task != "处理第0项：{{ inputs.factor }}；系数2" {
		t.Fatal(body.Task, err)
	}
	resources := ResourceSteps(d)
	if resources[0].ID != "batch" || resources[0].Kind != "agent" || resources[0].Skills[0] != "skill:report" || d.Steps[0].Iteration.Body.ID != "" {
		t.Fatal("resource owner projection changed body")
	}
	if ValidRunResources(d, nil) {
		t.Fatal("body resources omitted from freeze validation")
	}
	hash := strings.Repeat("a", 64)
	path := "resources/batch/skill/SKILL.md"
	valid := &RunResources{Version: 1, Nodes: map[string]NodeResources{"batch": {Skills: []ResourceDocument{{ID: "skill:report", Name: "report", Version: "v1", Path: path, SHA256: hash}}, Files: []ResourceFile{{Path: path, Name: "SKILL.md", SHA256: hash, Size: 1}}}}}
	if !ValidRunResources(d, valid) {
		t.Fatal("outer-scoped body resources rejected")
	}
	delete(valid.Nodes, "batch")
	if ValidRunResources(d, valid) {
		t.Fatal("missing body resource owner accepted")
	}
}

func TestRepeatDefinitionRejectsScopeNestingAndUnboundedControl(t *testing.T) {
	cases := []func(*Definition){
		func(d *Definition) { d.Steps[0].Iteration.Concurrency = 11 },
		func(d *Definition) { d.Steps[0].Iteration.MaxItems = 101 },
		func(d *Definition) { d.Steps[0].Iteration.Items.Type = "object" },
		func(d *Definition) { d.Steps[0].Iteration.Items.Source = "iteration.item" },
		func(d *Definition) { d.Steps[0].Iteration.Body.Kind = "confirm" },
		func(d *Definition) { d.Steps[0].Iteration.Body.Iteration = &IterationSpec{} },
		func(d *Definition) {
			d.Steps[0].Iteration.Body.Code.Inputs["value"] = CodeBinding{Type: "number", Source: "loop.state.n"}
		},
		func(d *Definition) {
			d.Steps[0].Iteration.Body.Code.Inputs["value"] = CodeBinding{Type: "number", Source: "steps.later.output.value"}
			d.Steps = append(d.Steps, Step{ID: "later", Kind: "agent", Task: "later"})
		},
		func(d *Definition) {
			d.Steps[0].Iteration.Body.Code.Inputs["index"] = CodeBinding{Type: "string", Source: "iteration.index"}
		},
	}
	for i, change := range cases {
		d := iterationDefinition()
		change(&d)
		if len(Validate(d)) == 0 {
			t.Fatalf("invalid repeat %d accepted", i)
		}
	}
	d := iterationDefinition()
	d.Steps[0].Iteration.Body = nil
	if len(Validate(d)) == 0 {
		t.Fatal("nil body accepted")
	}
	standalone := Definition{Name: "局部变量越界", Steps: []Step{*repeatCodeBody()}}
	if len(Validate(standalone)) == 0 {
		t.Fatal("local values available outside container")
	}
	d = iterationDefinition()
	d.Steps[0].Iteration.Body = &Step{Kind: "agent", Task: "task", Policy: &CodePolicy{MaxRetries: 1}}
	if len(Validate(d)) == 0 {
		t.Fatal("Agent external effects given automatic retries")
	}
	large := iterationDefinition()
	for len(large.Steps) < MaxSteps {
		large.Steps = append(large.Steps, Step{ID: fmt.Sprintf("extra_%d", len(large.Steps)), Kind: "agent", Task: "task"})
	}
	if len(Validate(large)) == 0 {
		t.Fatal("body not counted toward node limit")
	}
}

func TestIterationBodyCannotReadUnresolvedParallelSibling(t *testing.T) {
	d := iterationDefinition()
	batch := d.Steps[0]
	batch.Iteration.Items.Source = "steps.left.output.rows"
	d.Steps = []Step{{ID: "fork", Kind: "parallel"}, {ID: "left", Kind: "code", Code: &CodeSpec{Language: "javascript", Source: "function main(inputs){return {rows:[]}}", Inputs: map[string]CodeBinding{}, Outputs: map[string]ValueSchema{"rows": {Type: "array", Items: &ValueSchema{Type: "object"}}}}}, batch, {ID: "join", Kind: "join"}}
	d.Graph = &Graph{Version: 1, Edges: []Edge{{Source: "__input", Target: "fork"}, {Source: "fork", Target: "left"}, {Source: "fork", Target: "batch"}, {Source: "left", Target: "join"}, {Source: "batch", Target: "join"}, {Source: "join", Target: "__output"}}}
	found := false
	for _, diagnostic := range Validate(d) {
		if diagnostic.Code == "invalid_repeat_binding" && diagnostic.StepID == "batch" {
			found = true
		}
	}
	if !found {
		t.Fatal("iteration body read a sibling which is not guaranteed completed")
	}
}

func TestLoopStateAndLiteralExitCondition(t *testing.T) {
	d := loopDefinition()
	if ds := Validate(d); len(ds) > 0 {
		t.Fatal(ds)
	}
	run := Run{Definition: d, Inputs: map[string]string{}}
	initial, err := ResolveLoopInitial(run, "repeat")
	if err != nil || string(initial) != `{"n":0}` {
		t.Fatal(string(initial), err)
	}
	body, err := ResolveRepeatBody(run, "repeat", json.RawMessage(`{"n":2}`), 2)
	if err != nil || string(body.Code.Inputs["n"].Value) != "2" || string(body.Code.Inputs["index"].Value) != "2" {
		t.Fatal(body, err)
	}
	spec := d.Steps[0].Loop
	if done, err := EvaluateLoopUntil(spec, json.RawMessage(`{"n":2}`), 1); err != nil || done {
		t.Fatal(done, err)
	}
	if done, err := EvaluateLoopUntil(spec, json.RawMessage(`{"n":3}`), 2); err != nil || !done {
		t.Fatal(done, err)
	}
	spec.MaxIterations = 0
	if len(Validate(d)) == 0 {
		t.Fatal("unbounded loop accepted")
	}
	spec.MaxIterations = 5
	spec.Until.Source = "steps.other.output.n"
	if ValidateLoopUntil(spec) == nil {
		t.Fatal("loop exit escaped state scope")
	}
	spec.Until.Source = "loop.state.missing"
	if len(Validate(d)) == 0 {
		t.Fatal("undeclared loop output used as exit condition")
	}
}

func TestCodePoliciesFreezeAndValidateFallbacks(t *testing.T) {
	d := iterationDefinition()
	spec := d.Steps[0].Iteration.Body.Code
	for _, policy := range []*CodePolicy{{MaxRetries: 3}, {MaxRetries: -1}, {RetryDelayMS: 5001}, {OnFailure: "continue"}, {OnFailure: "fallback", Fallback: json.RawMessage(`{"value":"wrong"}`)}, {Fallback: json.RawMessage(`{"value":1}`)}} {
		if ValidateCodePolicy(spec, policy) == nil {
			t.Fatal("invalid code policy accepted", policy)
		}
	}
	policy := &CodePolicy{MaxRetries: 2, RetryDelayMS: 100, OnFailure: "fallback", Fallback: json.RawMessage(`{"value":0}`)}
	if err := ValidateCodePolicy(spec, policy); err != nil {
		t.Fatal(err)
	}
	d.Steps[0].Iteration.Body.Policy = policy
	old := Hash(d)
	copy := cloneStep(d.Steps[0])
	copy.Iteration.Body.Policy.Fallback[9] = '9'
	if Hash(d) != old {
		t.Fatal("policy fallback alias mutated original")
	}
	patched, err := Patch(d, []Operation{{Op: "update_step", StepID: "batch", Patch: json.RawMessage(`{"iteration":{"items":{"type":"array","value":[]},"body":{"kind":"code","code":{"language":"python","source":"def main(inputs):\n return {'value':1}","inputs":{},"outputs":{"value":{"type":"number"}}}}}}`)}})
	if err != nil || Hash(patched) == old || d.Steps[0].Iteration.Items.Source != "inputs.orders" {
		t.Fatal("repeat patch did not preserve original", err)
	}
}

func TestRecoverableCodeExecutionRequiresAnActualExitedProcess(t *testing.T) {
	zero, killed := 0, -1
	for _, code := range []string{"code_error", "invalid_output", "output_schema_invalid", "timeout", "memory_limit", "output_limit"} {
		if !RecoverableCodeExecution(&CodeExecution{ErrorCode: code, ExitCode: &zero}) || !RecoverableCodeExecution(&CodeExecution{ErrorCode: code, ExitCode: &killed}) {
			t.Fatal("actual script failure rejected", code)
		}
		if RecoverableCodeExecution(&CodeExecution{ErrorCode: code}) {
			t.Fatal("unstarted process allowed retry/fallback", code)
		}
	}
	for _, code := range []string{"", "execution_failed", "sandbox_unavailable", "runtime_unavailable", "invalid_source", "invalid_input", "invalid_timeout", "cancelled"} {
		if RecoverableCodeExecution(&CodeExecution{ErrorCode: code, ExitCode: &zero}) {
			t.Fatal("non-script failure allowed retry/fallback", code)
		}
	}
	if RecoverableCodeExecution(nil) {
		t.Fatal("missing execution allowed retry/fallback")
	}
}

func TestIterationArtifactNamespaceDoesNotChangeLegacyDefinitions(t *testing.T) {
	legacy := Definition{Name: "旧路径", Steps: []Step{{ID: "write", Kind: "agent", Task: "写文件", OutputFile: "iteration-results/existing.md"}}}
	oldHash := Hash(legacy)
	if ds := Validate(legacy); len(ds) > 0 {
		t.Fatal("old output namespace changed", ds)
	}
	d := iterationDefinition()
	d.Steps = append(d.Steps, Step{ID: "write", Kind: "agent", Task: "写文件", OutputFile: "Iteration-Results/overwrite.md"})
	found := false
	for _, diagnostic := range Validate(d) {
		found = found || diagnostic.Code == "reserved_iteration_output"
	}
	if !found {
		t.Fatal("downstream output can overwrite per-item snapshot namespace")
	}
	d = iterationDefinition()
	d.Steps[0].Iteration.Body = &Step{Kind: "agent", Task: "写文件", OutputFile: "iteration-results/overwrite.md"}
	if len(Validate(d)) == 0 {
		t.Fatal("body can claim reserved item snapshot namespace")
	}
	if Hash(legacy) != oldHash {
		t.Fatal("legacy definition changed while validating new namespace")
	}
}
