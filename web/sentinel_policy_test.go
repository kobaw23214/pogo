package web

import (
	"encoding/json"
	"testing"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/tools"
)

func TestSentinelPolicyReviewsPersistentExecutionAndScriptTests(t *testing.T) {
	cases := []struct {
		tool, args string
		auto       bool
	}{
		{tools.WatchName, `{"action":"list"}`, true},
		{tools.WatchName, `{"action":"status","watch_id":"w_1"}`, true},
		{tools.WatchName, `{"action":"prepare","mode":"stream","script_path":"sentinels/custom/main.py"}`, false},
		{tools.WatchName, `{"action":"create","title":"custom","task":"observe","mode":"poll","script_path":"sentinels/custom/main.py"}`, false},
		{tools.WatchName, `{"action":"pause","watch_id":"w_1"}`, false},
		{tools.WatchName, `{"action":"resume","watch_id":"w_1"}`, false},
		{tools.WatchName, `{"action":"retry","watch_id":"w_1"}`, false},
		{tools.WatchName, `{"action":"remove","watch_id":"w_1"}`, false},
		{tools.WatchName, `{"action":"list","network":true}`, false},
		{tools.WatchName, `null`, false},
		{tools.WatchTestName, `{"watch_id":"w_1"}`, false},
		{tools.WatchDecideName, `{"decision":"report","summary":"completed"}`, true},
	}
	for _, mode := range []Mode{ModeStandard, ModeStrict} {
		t.Run(string(mode), func(t *testing.T) {
			p := NewPolicy()
			p.Set(mode, 0)
			for _, tc := range cases {
				if _, got := p.Decide(agent.GateRequest{ToolName: tc.tool, Args: json.RawMessage(tc.args)}); got != tc.auto {
					t.Errorf("tool=%s args=%s auto=%v want=%v", tc.tool, tc.args, got, tc.auto)
				}
			}
		})
	}
}
func TestSentinelPolicyHonorsExplicitAutoMode(t *testing.T) {
	p := NewPolicy()
	p.Set(ModeAuto, 0)
	for _, name := range []string{tools.WatchName, tools.WatchTestName, tools.WatchDecideName} {
		rule, auto := p.Decide(agent.GateRequest{ToolName: name, Args: json.RawMessage(`{}`)})
		if !auto || rule != "policy:auto" {
			t.Fatalf("explicit auto not honored for %s: %q %v", name, rule, auto)
		}
	}
	// Automatic policy approval does not validate an action or choose a host
	// binding. Tool schemas/backend still reject invalid and out-of-scope input.
	if err := (&tools.Watch{}).ValidateArgs(json.RawMessage(`{"action":"create","owner_id":"other"}`)); err == nil {
		t.Fatal("auto mode invented valid tool arguments")
	}
}
