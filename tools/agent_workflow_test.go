package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	af "github.com/yosukeno/pogo/agentworkflow"
	"strings"
	"testing"
)

type agentWorkflowFixture struct {
	calls  int
	group  string
	raw    json.RawMessage
	result json.RawMessage
}

func (f *agentWorkflowFixture) AgentWorkflowCall(_ context.Context, group string, raw json.RawMessage) (json.RawMessage, error) {
	f.calls++
	f.group, f.raw = group, append(json.RawMessage(nil), raw...)
	if f.result != nil {
		return f.result, nil
	}
	return json.RawMessage(`{"ok":true,"saved":true}`), nil
}

func internalFlowTool(t *testing.T, backend AgentWorkflowBackend, group string) Tool {
	t.Helper()
	for _, tool := range NewAgentWorkflowTools(backend) {
		if tool.Name() == "agent_workflow_"+group {
			return tool
		}
	}
	t.Fatal("internal flow tool absent", group)
	return nil
}

func TestAgentWorkflowRepeatAndCodePolicyToolContract(t *testing.T) {
	requests := []string{
		`{"action":"create","request_id":"iterate-create","name":"批量校验","steps":[{"id":"batch","kind":"iteration","iteration":{"items":{"type":"array","value":[{"amount":3}]},"concurrency":2,"max_items":10,"on_item_error":"continue","body":{"kind":"agent","task":"检查{{ iteration.item.amount }}，当前下标{{ iteration.index }}","skills":["skill:check"]}}}]}`,
		`{"action":"create","request_id":"loop-create","name":"累加到目标","steps":[{"id":"rounds","kind":"loop","loop":{"initial":{"type":"object","value":{"n":0}},"body":{"kind":"code","code":{"language":"javascript","source":"function main(inputs){return {n:inputs.n+1}}","inputs":{"n":{"type":"integer","source":"loop.state.n"}},"outputs":{"n":{"type":"integer"}}},"policy":{"max_retries":1,"retry_delay_ms":100,"on_failure":"stop"}},"until":{"source":"loop.state.n","operator":"gte","value":"3"},"max_iterations":5}}]}`,
		`{"action":"patch","workflow_id":"aw_one","request_id":"policy-change","expected_revision":1,"operations":[{"op":"update_step","step_id":"calculate","patch":{"policy":{"max_retries":2,"retry_delay_ms":200,"on_failure":"fallback","fallback":{"total":0}}}}]}`,
	}
	for _, args := range requests {
		backend := &agentWorkflowFixture{}
		if _, err := internalFlowTool(t, backend, "edit").Execute(context.Background(), json.RawMessage(args)); err != nil || backend.calls != 1 {
			t.Fatal(args, err)
		}
	}
	for _, args := range []string{
		`{"action":"create","request_id":"bad-body","name":"bad","steps":[{"id":"batch","kind":"iteration","iteration":{"items":{"type":"array","value":[]},"body":{"kind":"confirm","prompt":"confirm"}}}]}`,
		`{"action":"create","request_id":"bad-source","name":"bad","steps":[{"id":"batch","kind":"iteration","iteration":{"items":{"type":"object","value":{}},"body":{"kind":"agent","task":"check"}}}]}`,
	} {
		backend := &agentWorkflowFixture{}
		if _, err := internalFlowTool(t, backend, "edit").Execute(context.Background(), json.RawMessage(args)); err == nil || backend.calls != 0 {
			t.Fatal("invalid repeat schema reached host", args, err)
		}
	}
}

func TestAgentWorkflowIterationArtifactToolContract(t *testing.T) {
	valid := json.RawMessage(`{"action":"artifact","workflow_id":"aw_one","run_id":"run_one","step_id":"batch","item_index":0}`)
	backend := &agentWorkflowFixture{}
	if _, err := internalFlowTool(t, backend, "read").Execute(context.Background(), valid); err != nil || backend.calls != 1 {
		t.Fatal("iteration artifact request rejected", err)
	}
	for _, args := range []string{
		`{"action":"artifact","workflow_id":"aw_one","run_id":"run_one","step_id":"batch","item_index":-1}`,
		`{"action":"artifact","workflow_id":"aw_one","run_id":"run_one","step_id":"batch","item_index":100}`,
		`{"action":"run","workflow_id":"aw_one","run_id":"run_one","item_index":0}`,
	} {
		backend := &agentWorkflowFixture{}
		if _, err := internalFlowTool(t, backend, "read").Execute(context.Background(), json.RawMessage(args)); err == nil || backend.calls != 0 {
			t.Fatal("invalid item selector reached host", args, err)
		}
	}
}

func TestAgentWorkflowCodeAndTypedDataContract(t *testing.T) {
	hash := strings.Repeat("a", 64)
	for _, request := range []struct{ group, args string }{
		{"edit", `{"action":"create","request_id":"code-create","name":"订单计算","inputs":[{"key":"orders","type":"array","required":true}],"steps":[{"id":"sum","kind":"code","code":{"language":"python","source":"def main(inputs):\n return {'total': sum(inputs['orders'])}","inputs":{"orders":{"type":"array","source":"inputs.orders"},"rate":{"type":"number","value":1.5}},"outputs":{"total":{"type":"number"},"rows":{"type":"array","items":{"type":"object","properties":{"amount":{"type":"number"}},"required":["amount"]}}},"timeout_seconds":10}}]}`},
		{"edit", `{"action":"patch","workflow_id":"aw_one","request_id":"code-patch","expected_revision":1,"operations":[{"op":"update_step","step_id":"sum","patch":{"code":{"language":"javascript","source":"function main(inputs) { return { total: inputs.n }; }","inputs":{"n":{"type":"number","value":4}},"outputs":{"total":{"type":"number"}}}}}]}`},
		{"read", `{"action":"validate","workflow_id":"aw_one","inputs":{"orders":[1,2],"meta":{"name":"sample"},"count":3,"review":false}}`},
		{"run", `{"action":"trial","workflow_id":"aw_one","request_id":"code-run","expected_revision":1,"expected_hash":"` + hash + `","inputs":{"orders":[1,2],"count":3,"review":true}}`},
		{"run", `{"action":"test_node","workflow_id":"aw_one","request_id":"code-test","expected_revision":1,"expected_hash":"` + hash + `","step_id":"sum","node_inputs":{"orders":[1,2],"meta":{"flag":true}}}`},
	} {
		backend := &agentWorkflowFixture{}
		if _, err := internalFlowTool(t, backend, request.group).Execute(context.Background(), json.RawMessage(request.args)); err != nil || backend.calls != 1 || string(backend.raw) != request.args {
			t.Fatal(request.group, request.args, err)
		}
	}
	for _, args := range []string{
		`{"action":"test_node","workflow_id":"aw_one","request_id":"code-test","step_id":"sum","node_inputs":{}}`,
		`{"action":"test_node","workflow_id":"aw_one","request_id":"code-test","expected_revision":1,"expected_hash":"` + hash + `","step_id":"sum"}`,
		`{"action":"test_node","workflow_id":"aw_one","request_id":"code-test","expected_revision":1,"expected_hash":"` + hash + `","step_id":"sum","node_inputs":{},"inputs":{"secret":"x"}}`,
		`{"action":"test_node","workflow_id":"aw_one","request_id":"code-test","expected_revision":1,"expected_hash":"` + hash + `","step_id":"sum","node_inputs":{},"owner_id":"other"}`,
		`{"action":"retry","workflow_id":"aw_one","request_id":"code-retry","run_id":"run_one","node_inputs":{}}`,
	} {
		backend := &agentWorkflowFixture{}
		if _, err := internalFlowTool(t, backend, "run").Execute(context.Background(), json.RawMessage(args)); err == nil || backend.calls != 0 {
			t.Fatal("unsafe code test reached host", args, err)
		}
	}
}

func TestAgentWorkflowTypedFormsAndResourceDiscoveryContract(t *testing.T) {
	for _, request := range []struct{ group, args string }{
		{"read", `{"action":"resources"}`},
		{"read", `{"action":"resources","source_session_id":"session-one"}`},
		{"edit", `{"action":"create","request_id":"typed-1","name":"报告","inputs":[{"key":"topic","type":"text","required":true},{"key":"need_review","type":"boolean","default":"yes","options":[{"value":"yes","label":"需要"},{"value":"no","label":"无需"}]},{"key":"audience","type":"select","options":[{"value":"team","label":"项目组"}],"default":"team"}],"steps":[{"id":"report","kind":"agent","task":"报告","skills":["embedded:office"],"shortcuts":["shortcut:report"]}]}`},
		{"edit", `{"action":"patch","workflow_id":"aw_one","request_id":"typed-2","expected_revision":1,"operations":[{"op":"update_step","step_id":"report","patch":{"skills":[],"shortcuts":["shortcut:next"]}},{"op":"set_metadata","inputs":[{"key":"need_review","type":"boolean","default":"no"}]}]}`},
	} {
		backend := &agentWorkflowFixture{}
		if _, err := internalFlowTool(t, backend, request.group).Execute(context.Background(), json.RawMessage(request.args)); err != nil || backend.calls != 1 || string(backend.raw) != request.args {
			t.Fatal("typed input/resource request did not reach the scoped host unchanged", request, err)
		}
	}
	for _, request := range []struct{ group, args string }{
		{"read", `{"action":"resources","owner_id":"other"}`},
		{"read", `{"action":"resources","agent_id":"other"}`},
		{"read", `{"action":"resources","source_session_id":""}`},
		{"read", `{"action":"list","source_session_id":"session-one"}`},
		{"edit", `{"action":"create","request_id":"typed-3","name":"x","inputs":[{"key":"review","type":"boolean","default":true}],"steps":[{"id":"x","kind":"agent","task":"x"}]}`},
		{"edit", `{"action":"create","request_id":"typed-4","name":"x","inputs":[{"key":"audience","type":"select","options":["team"]}],"steps":[{"id":"x","kind":"agent","task":"x"}]}`},
		{"edit", `{"action":"create","request_id":"typed-5","name":"x","steps":[{"id":"x","kind":"agent","task":"x","skills":[{"name":"office"}]}]}`},
	} {
		backend := &agentWorkflowFixture{}
		if _, err := internalFlowTool(t, backend, request.group).Execute(context.Background(), json.RawMessage(request.args)); err == nil || backend.calls != 0 {
			t.Fatal("invalid typed resource/identity request reached the host", request, err)
		}
	}
	read := internalFlowTool(t, &agentWorkflowFixture{}, "read")
	if !strings.Contains(read.Description(), "stable resource IDs") || !strings.Contains(read.Description(), "does not grant") {
		t.Fatal("discovery instructions lost the permission boundary")
	}
}

func TestAgentWorkflowStructuredCallsAndDiagnostics(t *testing.T) {
	hash := strings.Repeat("a", 64)
	requests := []struct{ group, args string }{
		{"read", `{"action":"capabilities"}`},
		{"read", `{"action":"validate","definition":{"name":"资料整理","inputs":[{"key":"topic","required":true}],"steps":[{"id":"collect","kind":"agent","task":"整理 {{ inputs.topic }}","output_file":"notes.md"},{"id":"review","kind":"confirm","prompt":"确认资料"}]},"inputs":{"topic":"测试主题"}}`},
		{"read", `{"action":"artifact","workflow_id":"af_one","run_id":"ar_one","step_id":"report"}`},
		{"edit", `{"action":"create","request_id":"new-1","name":"资料整理","steps":[{"id":"collect","kind":"agent","task":"整理资料"}]}`},
		{"edit", `{"action":"patch","request_id":"patch-1","workflow_id":"af_one","expected_revision":1,"operations":[{"op":"add_step","step":{"id":"review","kind":"confirm","prompt":"核对提纲"},"index":1},{"op":"update_step","step_id":"collect","patch":{"task":"整理资料并保留出处"}},{"op":"move_step","step_id":"review","index":0},{"op":"set_metadata","description":"独立测试","inputs":[]}]}`},
		{"edit", `{"action":"archive","request_id":"archive-1","workflow_id":"af_one","expected_revision":2}`},
		{"enable", `{"action":"enable","workflow_id":"af_one","expected_revision":2,"expected_hash":"` + hash + `"}`},
		{"run", `{"action":"trial","workflow_id":"af_one","request_id":"trial-1","expected_revision":2,"expected_hash":"` + hash + `","inputs":{"topic":"样例主题"},"files":["资料/one.md"]}`},
		{"run", `{"action":"start","workflow_id":"af_one","request_id":"start-1","version_id":"` + hash + `"}`},
		{"run", `{"action":"cancel","workflow_id":"af_one","run_id":"ar_one","request_id":"cancel-1"}`},
		{"run", `{"action":"retry","workflow_id":"af_one","run_id":"ar_one","request_id":"retry-1"}`},
	}
	for _, request := range requests {
		backend := &agentWorkflowFixture{}
		tool := internalFlowTool(t, backend, request.group)
		result, err := tool.Execute(context.Background(), json.RawMessage(request.args))
		if err != nil || backend.calls != 1 || backend.group != request.group || string(backend.raw) != request.args {
			t.Fatal(request, result, err, backend)
		}
		details := result.Details.(map[string]any)
		if details["type"] != "agent_workflow" || details["action"] == nil || details["group"] != request.group || result.Payload == nil || string(result.Payload.Structured) != result.Text {
			t.Fatal("lost typed durable receipt", result)
		}
	}
	backend := &agentWorkflowFixture{result: json.RawMessage(`{"ok":false,"error":{"code":"revision_conflict","message":"草稿已更新"},"diagnostics":[{"step_id":"report","code":"missing_output"}]}`)}
	result, err := internalFlowTool(t, backend, "read").Execute(context.Background(), json.RawMessage(`{"action":"get","workflow_id":"af_one"}`))
	if err == nil || !strings.Contains(err.Error(), "草稿") || !strings.Contains(result.Text, "missing_output") || result.Payload == nil {
		t.Fatal("error lost diagnostic payload", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := backend.calls
	if _, err := internalFlowTool(t, backend, "read").Execute(ctx, json.RawMessage(`{"action":"list"}`)); !errors.Is(err, context.Canceled) || backend.calls != before {
		t.Fatal("cancelled request reached host", err)
	}
}

func TestAgentWorkflowRejectsIdentityConfirmationAndBlindEdits(t *testing.T) {
	invalid := []struct{ group, args string }{
		{"read", `{"action":"list","owner_id":"other"}`},
		{"read", `{"action":"capabilities","agent_id":"another"}`},
		{"read", `{"action":"get"}`},
		{"read", `{"action":"validate"}`},
		{"read", `{"action":"artifact","workflow_id":"af_one","run_id":"ar_one","path":"../../users.json"}`},
		{"read", `{"action":"validate","definition":{"name":"x","steps":[{"id":"x","kind":"agent","agent":"other","task":"x"}]}}`},
		{"edit", `{"action":"create","request_id":"new-1","name":"x","steps":[],"owner_id":"other"}`},
		{"edit", `{"action":"patch","workflow_id":"af_one","request_id":"patch-1","operations":[]}`},
		{"edit", `{"action":"patch","workflow_id":"af_one","request_id":"patch-1","expected_revision":1,"operations":[{"op":"update_step","step_id":"x","patch":{"id":"replace"}}]}`},
		{"edit", `{"action":"patch","workflow_id":"af_one","request_id":"patch-1","expected_revision":1,"operations":[{"op":"update_step","step":{"id":"x","kind":"agent","task":"x"}}]}`},
		{"edit", `{"action":"patch","workflow_id":"af_one","request_id":"patch-1","expected_revision":1,"operations":[{"op":"move_step","step_id":"x","index":-1}]}`},
		{"enable", `{"action":"enable","workflow_id":"af_one","expected_revision":1,"expected_hash":"latest"}`},
		{"run", `{"action":"confirm","workflow_id":"af_one","run_id":"ar_one","request_id":"yes"}`},
		{"run", `{"action":"trial","workflow_id":"af_one","request_id":"try-1","expected_revision":1}`},
		{"run", `{"action":"retry","workflow_id":"af_one","run_id":"ar_one","request_id":"again","inputs":{"topic":"changed"}}`},
		{"run", `{"action":"start","workflow_id":"af_one","request_id":"start-1","approval_mode":"auto"}`},
	}
	for _, request := range invalid {
		backend := &agentWorkflowFixture{}
		if _, err := internalFlowTool(t, backend, request.group).Execute(context.Background(), json.RawMessage(request.args)); err == nil || backend.calls != 0 {
			t.Fatal("unsafe args reached host", request, err, backend.calls)
		}
	}
}

func TestAgentWorkflowOptionalSelectionReadOnlyAndImpersonation(t *testing.T) {
	backend := &agentWorkflowFixture{}
	empty := []string{}
	all := []string{AgentWorkflowReadName, AgentWorkflowEditName, AgentWorkflowEnableName, AgentWorkflowRunName}
	r := New(Options{Cwd: t.TempDir(), AgentWorkflows: backend, Enabled: &empty})
	for _, name := range all {
		if _, ok := r.Get(name); ok {
			t.Fatal("unselected grant registered", name)
		}
	}
	next, apply, err := r.WithNativeSelection(&all, nil)
	if err != nil {
		t.Fatal(err)
	}
	apply()
	for _, name := range all {
		if _, ok := next.Get(name); !ok {
			t.Fatal("new grant cannot sync", name)
		}
	}
	next, apply, err = next.WithNativeSelection(&empty, nil)
	if err != nil {
		t.Fatal(err)
	}
	apply()
	for _, name := range all {
		if _, ok := next.Get(name); ok {
			t.Fatal("revoked grant remains", name)
		}
	}
	ro := New(Options{Cwd: t.TempDir(), AgentWorkflows: backend, ReadOnly: true, Enabled: &all})
	for _, name := range all {
		tool, ok := ro.Get(name)
		if ok != (name == AgentWorkflowReadName) {
			t.Fatal("read-only grant mismatch", name, ok)
		}
		if ok && tool.ExecutionMode() != Parallel {
			t.Fatal("read operation not parallel")
		}
	}
	var typedNil *agentWorkflowFixture
	for _, b := range []AgentWorkflowBackend{nil, typedNil, backend} {
		var synthetic, projected []Tool
		for _, name := range all {
			synthetic = append(synthetic, nameStub{name: name})
			projected = append(projected, &MCPTool{wireName: name})
		}
		r := New(Options{Cwd: t.TempDir(), Enabled: &empty, AgentWorkflows: b, Synth: synthetic, MCP: projected}).WithMCP(projected)
		for _, name := range all {
			if _, ok := r.Get(name); ok {
				t.Fatal("external tool impersonates scoped host", name)
			}
		}
	}
}

func TestAgentWorkflowGraphToolsKeepStructuredAndScopedContract(t *testing.T) {
	graph := `{"version":1,"edges":[{"source":"__input","target":"route"},{"source":"route","target":"join","branch":"true"},{"source":"route","target":"join","branch":"false"},{"source":"join","target":"__output"}]}`
	requests := []string{
		`{"action":"create","request_id":"graph-create","name":"条件流程","steps":[{"id":"route","kind":"condition","condition":{"source":"inputs.review","operator":"equals","value":"yes"}},{"id":"join","kind":"join"}],"graph":` + graph + `}`,
		`{"action":"patch","workflow_id":"af_one","expected_revision":1,"request_id":"graph-patch","operations":[{"op":"set_graph","graph":` + graph + `}]}`,
		`{"action":"patch","workflow_id":"af_one","expected_revision":1,"request_id":"graph-change-kind","operations":[{"op":"update_step","step_id":"route","patch":{"kind":"agent","condition":null,"task":"整理资料"}},{"op":"set_graph","graph":` + graph + `}]}`,
	}
	for _, raw := range requests {
		backend := &agentWorkflowFixture{}
		if _, err := internalFlowTool(t, backend, "edit").Execute(context.Background(), json.RawMessage(raw)); err != nil || backend.calls != 1 || string(backend.raw) != raw {
			t.Fatal("structured graph request lost before host semantic validation", err, raw)
		}
	}
	invalid := []string{
		`{"action":"patch","workflow_id":"af_one","expected_revision":1,"request_id":"bad","operations":[{"op":"set_graph","graph":null}]}`,
		`{"action":"patch","workflow_id":"af_one","expected_revision":1,"request_id":"bad","operations":[{"op":"set_graph","graph":` + graph + `,"owner_id":"other"}]}`,
		`{"action":"patch","workflow_id":"af_one","expected_revision":1,"request_id":"bad","operations":[{"op":"set_metadata","graph":` + graph + `}]}`,
		`{"action":"patch","workflow_id":"af_one","expected_revision":1,"request_id":"bad","graph":` + graph + `,"operations":[{"op":"set_metadata","name":"x"}]}`,
		`{"action":"create","request_id":"bad","name":"x","steps":[{"id":"route","kind":"condition","condition":{"source":"inputs.x","operator":"eval","value":"run()"}}],"graph":` + graph + `}`,
		`{"action":"create","request_id":"bad","name":"x","steps":[{"id":"route","kind":"condition","condition":{"source":"inputs.x","operator":"equals","credentials":"secret"}}],"graph":` + graph + `}`,
		`{"action":"create","request_id":"bad","name":"x","steps":[],"graph":{"version":1,"edges":[{"source":"a","target":"b","branch":"else"}]}}`,
	}
	for _, raw := range invalid {
		backend := &agentWorkflowFixture{}
		if _, err := internalFlowTool(t, backend, "edit").Execute(context.Background(), json.RawMessage(raw)); err == nil || backend.calls != 0 {
			t.Fatal("unstructured or scoped graph request reached host", err, raw)
		}
	}
}

func TestAgentWorkflowExpandedAuthoringSchemaAndRequestBudget(t *testing.T) {
	backend := &agentWorkflowFixture{}
	tool := internalFlowTool(t, backend, "edit")
	properties := tool.InputSchema()["properties"].(map[string]any)
	if properties["steps"].(map[string]any)["maxItems"] != 200 || properties["operations"].(map[string]any)["maxItems"] != 402 {
		t.Fatal("Agent-facing schema does not advertise full authoring limits", properties["steps"], properties["operations"])
	}
	steps := make([]map[string]any, 200)
	for i := range steps {
		steps[i] = map[string]any{"id": fmt.Sprintf("node_%03d", i), "kind": "agent", "task": strings.Repeat("x", 8192)}
	}
	raw, _ := json.Marshal(map[string]any{"action": "create", "request_id": "large-create", "name": "Two hundred nodes", "steps": steps})
	if len(raw) <= 512<<10 {
		t.Fatal("fixture no longer exceeds the former argument budget")
	}
	if err := ValidateArgs(tool, raw); err != nil {
		t.Fatal("200 long nodes blocked before reaching the host", err)
	}
	if _, err := tool.Execute(context.Background(), raw); err != nil || backend.calls != 1 {
		t.Fatal("large structured request was not forwarded", err)
	}
	if err := ValidateArgs(tool, append(raw, []byte(strings.Repeat(" ", af.MaxRequestBytes))...)); err == nil {
		t.Fatal("unbounded argument payload accepted")
	}
}

func TestAgentWorkflowAcceptsWholeTwoHundredNodePatchBatch(t *testing.T) {
	tool := internalFlowTool(t, &agentWorkflowFixture{}, "edit")
	operations := []map[string]any{{"op": "set_metadata", "name": "Replacement"}}
	for i := 0; i < 200; i++ {
		operations = append(operations, map[string]any{"op": "remove_step", "step_id": fmt.Sprintf("old_%03d", i)})
	}
	edges := []map[string]any{}
	previous := "__input"
	for i := 0; i < 200; i++ {
		id := fmt.Sprintf("new_%03d", i)
		operations = append(operations, map[string]any{"op": "add_step", "step": map[string]any{"id": id, "kind": "agent", "task": "Independent input"}})
		edges = append(edges, map[string]any{"source": previous, "target": id})
		previous = id
	}
	edges = append(edges, map[string]any{"source": previous, "target": "__output"})
	operations = append(operations, map[string]any{"op": "set_graph", "graph": map[string]any{"version": 1, "edges": edges}})
	body := map[string]any{"action": "patch", "workflow_id": "aw_one", "request_id": "replace-200", "expected_revision": 1, "operations": operations}
	raw, _ := json.Marshal(body)
	if len(operations) != 402 {
		t.Fatal("replacement fixture", len(operations))
	}
	if err := ValidateArgs(tool, raw); err != nil {
		t.Fatal("complete atomic replacement rejected", err)
	}
	body["operations"] = append(operations, map[string]any{"op": "set_metadata", "name": "One too many"})
	raw, _ = json.Marshal(body)
	if err := ValidateArgs(tool, raw); err == nil {
		t.Fatal("unbounded patch operation batch accepted")
	}
}
