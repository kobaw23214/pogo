package web

import (
	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/tools"
	"testing"
)

func TestShortcutPolicyUsesExistingWriteApproval(t *testing.T) {
	for _, mode := range []Mode{ModeStrict, ModeStandard, ModeAuto} {
		p := NewPolicy()
		p.mode = mode
		for _, name := range []string{tools.ShortcutSearchName, tools.ShortcutReadName, tools.ShortcutCreateName} {
			_, auto := p.Decide(agent.GateRequest{ToolName: name})
			want := name != tools.ShortcutCreateName || mode != ModeStrict
			if auto != want {
				t.Fatalf("%s %s auto=%v, want %v", mode, name, auto, want)
			}
		}
	}
}
