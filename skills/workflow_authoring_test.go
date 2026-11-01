package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedWorkflowAuthoringMaterializesAndInvokes(t *testing.T) {
	list, diagnostics := Load(Options{Cwd: t.TempDir(), UserDir: t.TempDir(), Embedded: materializeEmbeddedForTest(t)})
	reportSkillDiagnosticsInTest(t, diagnostics)
	canonical, ok := Find(list, "workflow-authoring")
	if !ok || canonical.Source != "embedded" || canonical.BrowserTask {
		t.Fatal("instruction skill absent or gained browser authority", canonical)
	}
	for _, alias := range []string{"workflow-authoring", "搭建工作流", "工作流搭建"} {
		name, args, parsed := ParseCommand("/skill:" + alias + " 创建订单审批")
		sk, found := Find(list, name)
		if !parsed || !found || sk.Name != canonical.Name || sk.Path != canonical.Path {
			t.Fatal("alias lost canonical identity", alias, sk)
		}
		expanded, err := Invocation(sk, args)
		if err != nil || !strings.Contains(expanded, `<skill name="workflow-authoring"`) || !strings.HasSuffix(expanded, args) {
			t.Fatal(expanded, err)
		}
	}
	body, err := os.ReadFile(canonical.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, contract := range []string{"平台工作流", "管理页面", "agent-workflows", "agent_workflow_read", "agent_workflow_edit", "agent_workflow_run", "agent_workflow_enable", "当前用户", "管理员密钥", "绕过"} {
		if !strings.Contains(string(body), contract) {
			t.Fatal("workflow procedure lost boundary", contract)
		}
	}
	if _, err := os.Stat(filepath.Join(canonical.Dir, "tools.json")); !os.IsNotExist(err) {
		t.Fatal("instruction skill must not impersonate host tools", err)
	}
	for _, sk := range list {
		if sk.Name == "搭建工作流" || sk.Name == "工作流搭建" {
			t.Fatal("localized alias became a duplicate catalog entry")
		}
	}
}

func TestEmbeddedWorkflowAliasesRespectExplicitUserSkill(t *testing.T) {
	dirs := materializeEmbeddedForTest(t)
	user := t.TempDir()
	writeSkill(t, user, "workflow-authoring/SKILL.md", skillFile("workflow-authoring", "custom workflow instructions", "external body"))
	list, _ := Load(Options{Cwd: t.TempDir(), UserDir: user, Embedded: dirs})
	sk, ok := Find(list, "workflow-authoring")
	if !ok || sk.Source != "user" || len(sk.Aliases) != 0 {
		t.Fatal("external replacement acquired trusted aliases", sk)
	}
	for _, alias := range []string{"搭建工作流", "工作流搭建"} {
		if _, found := Find(list, alias); found {
			t.Fatal("replacement inherited builtin alias", alias)
		}
	}
	list, _ = Load(Options{Cwd: t.TempDir(), UserDir: t.TempDir(), Embedded: dirs})
	canonical, _ := Find(list, "workflow-authoring")
	explicit := Skill{Name: "搭建工作流", Source: "user", Path: "/explicit/SKILL.md"}
	selected, found := Find(append([]Skill{canonical, explicit}, list...), "搭建工作流")
	if !found || selected.Path != explicit.Path {
		t.Fatal("builtin alias shadowed an explicitly named skill", selected)
	}
}
