package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestAgentWorkflowToolApprovalToolSchema(t *testing.T) {
	for _, mode := range []string{"standard", "strict", "auto"} {
		for _, request := range []map[string]any{
			{"action": "create", "request_id": "mode-create", "name": "test", "steps": []map[string]any{{"id": "one", "kind": "agent", "task": "test"}}, "tool_approval_mode": mode},
			{"action": "patch", "request_id": "mode-patch", "workflow_id": "aw_one", "expected_revision": 2, "operations": []map[string]any{{"op": "set_metadata", "tool_approval_mode": mode}}},
		} {
			raw, _ := json.Marshal(request)
			backend := &agentWorkflowFixture{}
			if _, err := internalFlowTool(t, backend, "edit").Execute(context.Background(), raw); err != nil || backend.calls != 1 {
				t.Fatal("valid review config rejected", mode, err)
			}
		}
	}
	for _, mode := range []any{"", "full", " auto", nil, true} {
		request := map[string]any{"action": "patch", "request_id": "mode-invalid", "workflow_id": "aw_one", "expected_revision": 2, "operations": []map[string]any{{"op": "set_metadata", "tool_approval_mode": mode}}}
		raw, _ := json.Marshal(request)
		backend := &agentWorkflowFixture{}
		if _, err := internalFlowTool(t, backend, "edit").Execute(context.Background(), raw); err == nil || backend.calls != 0 {
			t.Fatal("invalid mode reached host", mode, err)
		}
	}
}
