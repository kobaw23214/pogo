package web

import (
	"encoding/json"
	"testing"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/tools"
)

func TestShortcutManagementApprovalPolicy(t *testing.T) {
	for _, mode := range []Mode{ModeStandard, ModeStrict, ModeAuto} {
		p := NewPolicy()
		p.Set(mode, 0)
		check := func(name, body string, want bool) {
			t.Helper()
			_, auto := p.Decide(agent.GateRequest{ToolName: name, Args: json.RawMessage(body)})
			if auto != want {
				t.Fatalf("mode=%s tool=%s auto=%v want %v", mode, name, auto, want)
			}
		}
		check(tools.ShortcutListName, `{}`, true)
		for _, name := range []string{tools.ShortcutFolderCreateName, tools.ShortcutUpdateName, tools.ShortcutFolderRenameName, tools.ShortcutMoveName} {
			check(name, `{}`, mode != ModeStrict)
		}
		check(tools.ShortcutDeleteName, `{}`, mode == ModeAuto)
		check(tools.ShortcutMergeName, `{}`, true)
		check(tools.ShortcutMergeName, `{"apply":false,"expected_revision":2}`, true)
		check(tools.ShortcutMergeName, `{"apply":true,"plan_id":"mp_1","request_id":"request_1","expected_revision":2}`, mode == ModeAuto)
		check(tools.ShortcutMergeName, `{"apply":"false"}`, mode == ModeAuto)
		check(tools.ShortcutMergeName, `{"unexpected":true}`, mode == ModeAuto)
	}
}
