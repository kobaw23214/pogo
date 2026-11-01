package web

import (
	"encoding/json"
	"testing"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/tools"
)

func TestShellObservationDoesNotRequireMutationApproval(t *testing.T) {
	for _, mode := range []Mode{ModeStandard, ModeStrict} {
		policy := NewPolicy()
		policy.Set(mode, 0)
		for _, name := range []string{tools.ShellTasksName, tools.ShellOutputName} {
			if _, allowed := policy.Decide(agent.GateRequest{ToolName: name, Args: json.RawMessage(`{"task_id":"shell_fixture"}`)}); !allowed {
				t.Fatalf("%s should observe an existing task in %s mode", name, mode)
			}
		}
		policy.AllowCommand("approved command")
		if _, allowed := policy.Decide(agent.GateRequest{
			ToolName: tools.ShellStopName,
			Args:     json.RawMessage(`{"task_id":"shell_fixture","command":"approved command"}`),
		}); allowed {
			t.Fatal("an unrelated remembered shell command granted cancellation authority")
		}
		policy.Set(ModeAuto, 0)
		if _, allowed := policy.Decide(agent.GateRequest{ToolName: tools.ShellStopName}); !allowed {
			t.Fatal("explicit fully automatic policy did not apply to shell cancellation")
		}
	}
}

func TestShellStopGrantIsRevokedWithBash(t *testing.T) {
	policy := NewPolicy()
	policy.AllowTool(tools.ShellStopName)
	previous, current := []string{"bash"}, []string{}
	policy.replaceNativeGrants(&previous, &current)
	policy.replaceNativeGrants(&current, &previous)
	if _, allowed := policy.Decide(agent.GateRequest{ToolName: tools.ShellStopName}); allowed {
		t.Fatal("restoring Bash revived a cancelled shell_stop grant")
	}
}
