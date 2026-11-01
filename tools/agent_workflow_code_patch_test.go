package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func codePatchToolRequest(t *testing.T, code map[string]any) map[string]any {
	t.Helper()
	return map[string]any{
		"action": "patch", "workflow_id": "aw_one", "request_id": "code-partial", "expected_revision": 2,
		"operations": []map[string]any{{"op": "update_step", "step_id": "calculate", "patch": map[string]any{"code": code}}},
	}
}

func TestAgentWorkflowCodePartialPatchToolSchema(t *testing.T) {
	for _, tc := range []struct {
		name string
		code map[string]any
	}{
		{"source only", map[string]any{"source": "def main(inputs):\n    return {'total': inputs['amount']}\n"}},
		{"outputs only", map[string]any{"outputs": map[string]any{"total": map[string]any{"type": "number"}}}},
		{"clear inputs", map[string]any{"inputs": map[string]any{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := codePatchToolRequest(t, tc.code)
			raw, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			backend := &agentWorkflowFixture{}
			if _, err = internalFlowTool(t, backend, "edit").Execute(context.Background(), raw); err != nil || backend.calls != 1 || backend.group != "edit" || string(backend.raw) != string(raw) {
				t.Fatalf("partial code patch did not reach the scoped host unchanged: %v", err)
			}
		})
	}
}

func TestAgentWorkflowCodePartialPatchRetainsRevisionAndScopeGuards(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing revision", func(request map[string]any) { delete(request, "expected_revision") }},
		{"foreign owner", func(request map[string]any) { request["owner_id"] = "other" }},
		{"foreign agent", func(request map[string]any) { request["agent_id"] = "other" }},
		{"code cannot grant tools", func(request map[string]any) {
			operations := request["operations"].([]map[string]any)
			operations[0]["patch"].(map[string]any)["code"].(map[string]any)["tools"] = []string{"bash"}
		}},
		{"patch cannot change owner", func(request map[string]any) {
			operations := request["operations"].([]map[string]any)
			operations[0]["patch"].(map[string]any)["owner_id"] = "other"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := codePatchToolRequest(t, map[string]any{"source": "def main(inputs):\n    return {'total': 1}\n"})
			tc.mutate(request)
			raw, _ := json.Marshal(request)
			backend := &agentWorkflowFixture{}
			if _, err := internalFlowTool(t, backend, "edit").Execute(context.Background(), raw); err == nil || backend.calls != 0 {
				t.Fatalf("unsafe partial patch reached the host: %v", err)
			}
		})
	}
}

func TestAgentWorkflowCodeCreateRequiresCompleteCodeSpec(t *testing.T) {
	for _, missing := range []string{"language", "source", "inputs", "outputs"} {
		t.Run(missing, func(t *testing.T) {
			code := map[string]any{
				"language": "python", "source": "def main(inputs):\n    return {'total': 1}\n",
				"inputs": map[string]any{}, "outputs": map[string]any{"total": map[string]any{"type": "number"}},
			}
			delete(code, missing)
			request := map[string]any{"action": "create", "request_id": "incomplete-code", "name": "代码流程", "steps": []map[string]any{{"id": "calculate", "kind": "code", "code": code}}}
			raw, _ := json.Marshal(request)
			backend := &agentWorkflowFixture{}
			if _, err := internalFlowTool(t, backend, "edit").Execute(context.Background(), raw); err == nil || backend.calls != 0 {
				t.Fatalf("create without code.%s reached the host: %v", missing, err)
			}
		})
	}
}
