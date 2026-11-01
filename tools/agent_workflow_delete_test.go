package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestAgentWorkflowDeleteToolContract(t *testing.T) {
	for _, request := range []struct{ group, args string }{
		{"edit", `{"action":"delete","request_id":"delete-1","workflow_id":"aw_one","expected_revision":2}`},
		{"read", `{"action":"list","include_deleted":true}`},
		{"read", `{"action":"list","include_deleted":false}`},
	} {
		backend := &agentWorkflowFixture{}
		if _, err := internalFlowTool(t, backend, request.group).Execute(context.Background(), json.RawMessage(request.args)); err != nil || backend.calls != 1 || string(backend.raw) != request.args {
			t.Fatalf("delete/list did not reach scoped host unchanged: %s %v", request.args, err)
		}
	}
	for _, request := range []struct{ group, args string }{
		{"edit", `{"action":"delete","request_id":"delete-1","workflow_id":"aw_one"}`},
		{"edit", `{"action":"delete","request_id":"delete-1","expected_revision":2}`},
		{"edit", `{"action":"delete","workflow_id":"aw_one","expected_revision":2}`},
		{"edit", `{"action":"delete","request_id":"delete-1","workflow_id":"aw_one","expected_revision":2,"operations":[{"op":"remove_step","step_id":"x"}]}`},
		{"edit", `{"action":"delete","request_id":"delete-1","workflow_id":"aw_one","expected_revision":2,"owner_id":"another-user"}`},
		{"edit", `{"action":"delete","request_id":"delete-1","workflow_id":"aw_one","expected_revision":2,"agent_id":"another-agent"}`},
		{"read", `{"action":"list","include_deleted":"yes"}`},
		{"read", `{"action":"capabilities","include_deleted":true}`},
		{"read", `{"action":"get","workflow_id":"aw_one","include_deleted":true}`},
	} {
		backend := &agentWorkflowFixture{}
		if _, err := internalFlowTool(t, backend, request.group).Execute(context.Background(), json.RawMessage(request.args)); err == nil || backend.calls != 0 {
			t.Fatalf("unsafe delete/list reached host: %s %v", request.args, err)
		}
	}
}
