package web

import (
	"encoding/json"
	"testing"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/tools"
)

func TestBackgroundTaskPolicyOnlyExemptsValidatedReads(t *testing.T) {
	for _, mode := range []Mode{ModeStandard, ModeStrict} {
		p := NewPolicy()
		p.Set(mode, 0)
		for _, tc := range []struct {
			args string
			read bool
		}{
			{`{"action":"list","source":"schedule"}`, true},
			{`{"action":"status","source":"sentinel","task_id":"sentinel:watch-1"}`, true},
			{`{"action":"resume","source":"schedule","task_id":"schedule:job-1"}`, false},
			{`{"action":"remove","source":"sentinel","task_id":"sentinel:watch-1"}`, false},
			{`{"action":"list","source":"schedule","enabled":true}`, false},
			{`{"action":"list","source":null}`, false},
		} {
			_, automatic := p.Decide(agent.GateRequest{ToolName: tools.BackgroundTaskName, Args: json.RawMessage(tc.args)})
			if automatic != tc.read {
				t.Errorf("mode=%s args=%s automatic=%v want %v", mode, tc.args, automatic, tc.read)
			}
		}
	}
}
