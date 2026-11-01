package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedAgentWorkflowSkillIdentityAndPermissionBoundary(t *testing.T) {
	dirs := materializeEmbeddedForTest(t)
	list, diags := Load(Options{Cwd: t.TempDir(), UserDir: t.TempDir(), Embedded: dirs})
	reportSkillDiagnosticsInTest(t, diags)
	canonical, ok := Find(list, "agent-workflows")
	if !ok || canonical.Source != "embedded" || canonical.BrowserTask {
		t.Fatal("internal flow guidance absent or gained browser authority", canonical)
	}
	for _, alias := range []string{"agent-workflows", "内部工作流", "我的工作流"} {
		sk, ok := Find(list, alias)
		if !ok || sk.Path != canonical.Path || sk.Name != canonical.Name {
			t.Fatal("alias lost canonical skill identity", alias, sk)
		}
		expanded, err := Invocation(sk, "整理资料并核对提纲")
		if err != nil || !strings.Contains(expanded, `<skill name="agent-workflows"`) {
			t.Fatal("cannot invoke embedded guidance", err)
		}
	}
	if _, err := os.Stat(filepath.Join(canonical.Dir, "tools.json")); !os.IsNotExist(err) {
		t.Fatal("guidance must not impersonate host tools", err)
	}
	body, err := os.ReadFile(canonical.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, boundary := range []string{"agent_workflow_read", "agent_workflow_edit", "agent_workflow_enable", "agent_workflow_run", "expected_revision", "expected_hash", "实际模型和工具执行", "不能自己确认", "文档本身不增加权限"} {
		if !strings.Contains(string(body), boundary) {
			t.Fatal("lost material workflow instruction", boundary)
		}
	}
	user := t.TempDir()
	writeSkill(t, user, "agent-workflows/SKILL.md", skillFile("agent-workflows", "user instructions", "local replacement"))
	list, _ = Load(Options{Cwd: t.TempDir(), UserDir: user, Embedded: dirs})
	replacement, ok := Find(list, "agent-workflows")
	if !ok || replacement.Source != "user" || len(replacement.Aliases) != 0 {
		t.Fatal("external replacement acquired trusted aliases", replacement)
	}
	for _, alias := range []string{"内部工作流", "我的工作流"} {
		if _, found := Find(list, alias); found {
			t.Fatal("external replacement retained trusted alias", alias)
		}
	}
}
