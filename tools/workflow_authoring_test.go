package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type workflowToolFixture struct {
	calls  int
	action string
	raw    json.RawMessage
	result json.RawMessage
	err    error
}

func (f *workflowToolFixture) WorkflowCall(_ context.Context, action string, raw json.RawMessage) (json.RawMessage, error) {
	f.calls++
	f.action, f.raw = action, append(json.RawMessage(nil), raw...)
	if f.result == nil {
		return json.RawMessage(`{"ok":true,"verification":"structural_only"}`), f.err
	}
	return f.result, f.err
}

func workflowFixtureTool(t *testing.T, b WorkflowBackend, action string) Tool {
	t.Helper()
	for _, tool := range NewWorkflowTools(b) {
		if tool.Name() == "workflow_"+action {
			return tool
		}
	}
	t.Fatal("tool missing", action)
	return nil
}

func TestWorkflowAuthoringToolsDispatchAndStrictArguments(t *testing.T) {
	cases := []struct{ action, args string }{
		{"discover", `{}`},
		{"read", `{"action":"get","workflow_id":"wf_fixture"}`},
		{"validate", `{"definition":{"name":"订单审核","nodes":[{"id":"review","kind":"human","approval_policy":{"prevent_self_approval":true,"assignment":"fixed","approvers":["bob"]},"after":["start",{"node":"other","when":"failed"}]}]},"inputs":{"order_id":"123"}}`},
		{"validate", `{"workflow_id":"wf_fixture","expected_revision":2,"definition":{"name":"局部授权内的修改预览","nodes":[]}}`},
		{"edit", `{"action":"create","request_id":"create-1","name":"订单审核","nodes":[]}`},
		{"edit", `{"action":"patch","workflow_id":"wf_fixture","expected_revision":1,"request_id":"patch-1","operations":[{"op":"update_node","node_id":"review","patch":{"approval_policy":null,"tools":[],"after":["upstream"]}}]}`},
		{"test", `{"workflow_id":"wf_fixture","expected_revision":2,"expected_hash":"012345abcdef","request_id":"test-1","inputs":{"order_id":"fixture"}}`},
		{"run", `{"workflow_id":"wf_fixture","request_id":"run-1","version_id":"012345abcdef"}`},
		{"publish", `{"workflow_id":"wf_fixture","expected_revision":2,"expected_hash":"012345abcdef","name":"审阅版本"}`},
	}
	for _, tc := range cases {
		t.Run(tc.action+tc.args[:min(20, len(tc.args))], func(t *testing.T) {
			b := &workflowToolFixture{}
			tool := workflowFixtureTool(t, b, tc.action)
			if err := ValidateArgs(tool, json.RawMessage(tc.args)); err != nil {
				t.Fatal(err)
			}
			result, err := tool.Execute(context.Background(), json.RawMessage(tc.args))
			if err != nil || b.calls != 1 || b.action != tc.action || !json.Valid(b.raw) {
				t.Fatal(result, err, b)
			}
			details, ok := result.Details.(map[string]any)
			if !ok || details["type"] != "workflow_authoring" || details["action"] != tc.action || result.Payload == nil || string(result.Payload.Structured) != result.Text {
				t.Fatal("lost structured result", result)
			}
		})
	}
	invalid := []struct{ action, args string }{
		{"discover", `{"owner_id":"admin"}`},
		{"discover", `null`},
		{"read", `{"action":"run","workflow_id":"wf_fixture"}`},
		{"read", `{"action":"artifact","workflow_id":"wf_fixture","run_id":"run_fixture"}`},
		{"validate", `{}`},
		{"validate", `{"workflow_id":"wf_fixture","definition":{"name":"x","nodes":[]}}`},
		{"validate", `{"expected_revision":2,"definition":{"name":"x","nodes":[]}}`},
		{"validate", `{"definition":{"name":"x","nodes":[{"id":"a","owner_id":"other"}]}}`},
		{"validate", `{"definition":{"name":"x","nodes":[{"id":"a","approval_policy":{"prevent_self_approval":true,"assignment":"fixed","actor":"admin"}}]}}`},
		{"edit", `{"action":"create","name":"x","request_id":"k","username":"admin"}`},
		{"edit", `{"action":"patch","workflow_id":"wf_fixture","expected_revision":1,"request_id":"k","operations":[]}`},
		{"edit", `{"action":"patch","workflow_id":"wf_fixture","expected_revision":1,"request_id":"k","operations":[{"op":"update_node","node_id":"a","patch":{"id":"renamed"}}]}`},
		{"edit", `{"action":"patch","workflow_id":"wf_fixture","expected_revision":1,"request_id":"k","operations":[{"op":"add_node","node":{"id":"a","approval_policy":{"prevent_self_approval":true,"assignment":"business","admin_token":"bad"}}}]}`},
		{"test", `{"workflow_id":"wf_fixture","expected_revision":0,"expected_hash":"012345abcdef","request_id":"k"}`},
		{"test", `{"workflow_id":"wf_fixture","expected_revision":1,"expected_hash":"latest","request_id":"k"}`},
		{"test", `{"workflow_id":"wf_fixture","expected_revision":1,"expected_hash":"012345abcdef","request_id":"k","inputs":{"amount":100}}`},
		{"run", `{"workflow_id":"wf_fixture","request_id":"has space"}`},
		{"run", `{"workflow_id":"wf_fixture","request_id":"k","approver_subjects":{"review":["admin"]}}`},
		{"publish", `{"workflow_id":"wf_fixture","expected_revision":1,"expected_hash":"012345abcdef","token":"bad"}`},
	}
	for _, tc := range invalid {
		b := &workflowToolFixture{}
		if _, err := workflowFixtureTool(t, b, tc.action).Execute(context.Background(), json.RawMessage(tc.args)); err == nil || b.calls != 0 {
			t.Fatalf("invalid %s reached host: %s (%v)", tc.action, tc.args, err)
		}
	}
}

func TestWorkflowAuthoringErrorsKeepStructuredDiagnostics(t *testing.T) {
	b := &workflowToolFixture{result: json.RawMessage(`{"ok":false,"error":{"code":"revision_conflict","message":"草稿已被更新","status":409},"diagnostics":[{"node_id":"review","field_path":"nodes.review.agent","code":"unavailable"}]}`)}
	out, err := workflowFixtureTool(t, b, "read").Execute(context.Background(), json.RawMessage(`{"action":"get","workflow_id":"wf_fixture"}`))
	if err == nil || !strings.Contains(err.Error(), "草稿") || !strings.Contains(out.Text, "revision_conflict") || out.Payload == nil || out.Details == nil {
		t.Fatal("structured host failure was lost", out, err)
	}
	b.err = errors.New("host unavailable")
	if out, err := workflowFixtureTool(t, b, "discover").Execute(context.Background(), json.RawMessage(`{}`)); err == nil || out.Details != nil {
		t.Fatal("backend failure rendered success", out, err)
	}
	b.err = nil
	b.result = json.RawMessage(`[]`)
	if _, err := workflowFixtureTool(t, b, "discover").Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("invalid host envelope accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := b.calls
	if _, err := workflowFixtureTool(t, b, "discover").Execute(ctx, json.RawMessage(`{}`)); !errors.Is(err, context.Canceled) || b.calls != before {
		t.Fatal("cancelled call reached host", err)
	}
}

func TestWorkflowAuthoringRegistrySelectionAndHostBoundary(t *testing.T) {
	all := []string{WorkflowDiscoverName, WorkflowReadName, WorkflowValidateName, WorkflowEditName, WorkflowTestName, WorkflowRunName, WorkflowPublishName}
	b := &workflowToolFixture{}
	selected := []string{WorkflowReadName, WorkflowEditName}
	for _, selection := range []*[]string{nil, &selected} {
		for _, ro := range []bool{false, true} {
			r := New(Options{Cwd: t.TempDir(), Workflow: b, Enabled: selection, ReadOnly: ro})
			for _, name := range all {
				if _, ok := r.Get(name); ok || BuiltinSelected(selection, name) {
					t.Fatalf("retired platform tool registered for an Agent: %s", name)
				}
				if !BuiltinNameSet()[name] {
					t.Fatalf("historical saved tool name no longer loads: %s", name)
				}
			}
		}
	}
	empty := []string{}
	var nilBackend *workflowToolFixture
	for _, backend := range []WorkflowBackend{nil, nilBackend, b} {
		var synth, projected []Tool
		for _, name := range all {
			synth = append(synth, nameStub{name: name})
			projected = append(projected, &MCPTool{wireName: name})
		}
		r := New(Options{Cwd: t.TempDir(), Enabled: &empty, Workflow: backend, Synth: synth, MCP: projected})
		for _, snapshot := range []*Registry{r, r.WithMCP(projected)} {
			for _, name := range all {
				if _, ok := snapshot.Get(name); ok {
					t.Fatal("unselected host workflow tool invented by external projection", name)
				}
			}
		}
	}
}

func TestWorkflowAuthoringSchemasAdvertiseExactBoundaries(t *testing.T) {
	for _, tool := range NewWorkflowTools(&workflowToolFixture{}) {
		s := tool.InputSchema()
		raw, err := json.Marshal(s)
		if err != nil || s["additionalProperties"] != false || strings.Contains(string(raw), `"oneOf"`) || strings.Contains(string(raw), `"$ref"`) || s["required"] == nil {
			t.Fatal(tool.Name(), string(raw), err)
		}
		if IsProtectedBuiltin(tool.Name()) {
			t.Fatal("new workflow tool unexpectedly became an unwithholdable grant", tool.Name())
		}
		if !BuiltinNameSet()[tool.Name()] {
			t.Fatal("tool absent from native catalog", tool.Name())
		}
	}
	text := workflowFixtureTool(t, &workflowToolFixture{}, "test").Description()
	if !strings.Contains(text, "actual model/tool execution") || !strings.Contains(text, "without publishing") {
		t.Fatal("trial conceals real side effects or publication boundary")
	}
}
