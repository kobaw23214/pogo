package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	af "github.com/yosukeno/pogo/agentworkflow"
	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/inputref"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skills"
)

type awResourceTestClient struct{ phase int }

func (c *awResourceTestClient) Model() string { return "k3" }

var awResourceAttachment = regexp.MustCompile(`附件：(resources/[^\s]+)`)

func (c *awResourceTestClient) Stream(_ context.Context, _ string, history []llm.Message, _ []llm.ToolSchema, emit func(llm.Delta)) (llm.Response, error) {
	user := ""
	for _, m := range history {
		if m.Role == llm.RoleUser {
			for _, b := range m.Content {
				if b.Type == llm.BlockText {
					user = b.Text
				}
			}
		}
	}
	match := awGraphTestStep.FindStringSubmatch(user)
	if len(match) != 2 {
		return textTurn("独立资源验收模型"), nil
	}
	c.phase++
	if !strings.Contains(user, "DOC-RES-731") {
		return llm.Response{}, fmt.Errorf("frozen Skill guide was not loaded")
	}
	if match[1] == "report" && strings.Contains(user, "SHORTCUT-RES-731") {
		return llm.Response{}, fmt.Errorf("another node's shortcut leaked into report")
	}
	if emit != nil {
		emit(llm.Delta{Kind: llm.DeltaThinking, Text: "依据绑定指南核对实际资料。"})
	}
	tool := func(name string, args any) llm.Response {
		raw, _ := json.Marshal(args)
		r := toolTurn(name, string(raw))
		r.Message.Content[0].ID = fmt.Sprintf("resource_%s_%d", match[1], c.phase)
		return r
	}
	switch c.phase {
	case 1:
		path := "notes.md"
		if match[1] == "collect" {
			file := awResourceAttachment.FindStringSubmatch(user)
			if len(file) != 2 || !strings.Contains(user, "SHORTCUT-RES-731") {
				return llm.Response{}, fmt.Errorf("ordered shortcut/file snapshot not loaded")
			}
			path = file[1]
		}
		return tool("read", map[string]string{"path": path}), nil
	case 2:
		found := false
		for _, m := range history {
			for _, b := range m.Content {
				found = found || b.Type == llm.BlockToolResult && !b.IsError && strings.Contains(b.Text, "FACT-RES-731")
			}
		}
		if !found {
			return llm.Response{}, fmt.Errorf("bound resource file was not actually read")
		}
		output := awCanvasOutput.FindStringSubmatch(user)
		if len(output) != 2 {
			return llm.Response{}, fmt.Errorf("output missing")
		}
		return tool("write", map[string]string{"path": output[1], "content": "# 独立资源验收\n\nFACT-RES-731: 已实际读取绑定附件并按DOC-RES-731指南整理。\n"}), nil
	case 3:
		return textTurn("已实际写入本节点要求的产物。"), nil
	}
	return llm.Response{}, fmt.Errorf("unexpected resource fixture turn %d", c.phase)
}

func awSeedResourceAgent(t *testing.T, h *muHarness, alice string) (studioAgent, string, string, string) {
	t.Helper()
	dir := t.TempDir()
	doc := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(doc, []byte("---\nname: resource-guide\ndescription: synthetic report instructions\n---\n# DOC-RES-731\n先实际读取附件，再保留资料编号与来源生成报告。\n"), 0600); err != nil {
		t.Fatal(err)
	}
	loaded := h.mgr.Skills()
	if !slices.ContainsFunc(loaded, func(sk skills.Skill) bool { return sk.Name == "agent-workflows" }) {
		loaded = bindingTestSkills(t)
	}
	sk := skills.Skill{Name: "resource-guide", Description: "独立资料报告指南", Path: doc, Dir: dir, Source: "project"}
	loaded = append(loaded, sk)
	h.mgr.SetSkills(loaded)
	a := awTestAgent(t, h, "资源与类型验收助手", "write")
	selected := append(append([]string{}, (*a.Skills)...), skillIDOf(sk))
	body := studioAgentBody(a.Name, a.Instructions, "ready")
	body["revision"], body["tools"], body["skills"], body["mcp_servers"] = a.Revision, *a.Tools, selected, []string{}
	res := h.doAs(h.token, http.MethodPatch, "/api/manage/agents/"+a.ID, studioBody(body))
	defer res.Body.Close()
	if res.StatusCode != 200 || json.NewDecoder(res.Body).Decode(&a) != nil {
		t.Fatal("could not authorize synthetic Skill", res.StatusCode)
	}
	owner := identity.LocalScope("alice")
	namespace, err := h.mgr.shortcutAssetNamespace(&owner, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	assetID := strings.Repeat("a", 32)
	assetDir := filepath.Join(namespace, assetID)
	if err = os.MkdirAll(assetDir, 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("FACT-RES-731：这是一份独立合成资料，预算1000元，预计3天。\n")
	meta, _ := json.Marshal(starterAssetRecord{Name: "facts.txt", Size: int64(len(data)), SHA256: awResourceHash(data)})
	for name, content := range map[string][]byte{"data": data, "meta.json": meta} {
		if err = os.WriteFile(filepath.Join(assetDir, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	entry := userShortcut{ID: "report-material", Title: "报告资料与格式", Content: "SHORTCUT-RES-731", Parts: []inputref.Part{{Type: "text", Text: "SHORTCUT-RES-731：开始\n"}, {Type: "shortcut", ShortcutID: "nested", Title: "嵌套资料", Parts: []inputref.Part{{Type: "skill", Name: "resource-guide"}, {Type: "file", Path: "shortcut-assets/" + assetID + "/facts.txt", AsFile: true}}}, {Type: "text", Text: "\nSHORTCUT-RES-731：结束"}}}
	if _, err = h.mgr.replaceUserShortcuts(&owner, a.ID, 0, []userShortcut{entry}); err != nil {
		t.Fatal(err)
	}
	source := h.studioConversationAs(alice, a.ID)
	if err = os.WriteFile(filepath.Join(h.session(source).cwd, "source.md"), []byte("FACT-RES-731 source-only synthetic input\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return a, source, skillIDOf(sk), doc
}

func awResourceDefinition(skillID string, graph bool) af.Definition {
	d := af.Definition{Name: "绑定指南、资料与确认报告", Inputs: []af.Input{{Key: "topic", Label: "报告主题", Type: "text", Required: true}, {Key: "need_review", Label: "本人确认", Type: "boolean", Required: true, Default: "yes"}, {Key: "audience", Label: "报告读者", Type: "select", Default: "ops", Options: []af.InputOption{{Value: "ops", Label: "执行团队"}, {Value: "leadership", Label: "管理层"}}}}, Steps: []af.Step{
		{ID: "collect", Kind: "agent", Title: "整理资料", Task: "围绕 {{ inputs.topic }} 为 {{ inputs.audience }} 整理资料", Skills: []string{skillID}, Shortcuts: []string{"shortcut:report-material"}, OutputFile: "notes.md"},
		{ID: "confirm", Kind: "confirm", Title: "本人确认", Prompt: "请核对 {{ steps.collect.summary }}，给出意见。"},
		{ID: "report", Kind: "agent", Title: "生成报告", Task: "按 {{ inputs.audience }} 读者需求与本人意见生成报告", Skills: []string{skillID}, OutputFile: "report.md"},
	}}
	if graph {
		d.Graph = &af.Graph{Version: 1, Edges: []af.Edge{{Source: af.GraphInput, Target: "collect"}, {Source: "collect", Target: "confirm"}, {Source: "confirm", Target: "report"}, {Source: "report", Target: af.GraphOutput}}}
	}
	return d
}
func awCreateResourceDefinition(t *testing.T, h *muHarness, token string, a studioAgent, d af.Definition) af.Definition {
	return awTestValue[af.Definition](t, awTestCall(t, h, token, a.ID, "edit", map[string]any{"action": "create", "request_id": "resources-create", "name": d.Name, "inputs": d.Inputs, "steps": d.Steps, "graph": d.Graph}, 200), "workflow")
}

func TestAgentWorkflowResourcesDiscoveryFreezeConfirmationAndIsolation(t *testing.T) {
	for _, graph := range []bool{false, true} {
		t.Run(fmt.Sprint(graph), func(t *testing.T) {
			h := newMultiuserHarness(t, nil, func(c *Config) {
				if canonical, err := filepath.EvalSymlinks(c.SessionDir); err == nil {
					c.SessionDir = canonical
				}
				c.NewClient = func(config.Resolved, func(llm.RetryInfo)) llm.Client { return &awResourceTestClient{} }
			})
			a, source, skillID, _ := awSeedResourceAgent(t, h, aliceToken)
			catalog := awTestValue[awResourceCatalog](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "resources", "source_session_id": source}, 200), "resources")
			if !slices.ContainsFunc(catalog.Skills, func(v awSkillResource) bool { return v.ID == skillID && v.Available }) || !slices.ContainsFunc(catalog.Shortcuts, func(v awShortcutResource) bool { return v.ID == "shortcut:report-material" && v.Available }) || !slices.ContainsFunc(catalog.Files, func(v awResourceFileInfo) bool { return v.Path == "source.md" }) {
				t.Fatal("missing authorized resources", catalog)
			}
			awTestCall(t, h, bobToken, a.ID, "read", map[string]any{"action": "resources", "source_session_id": source}, 404)
			other := awTestAgent(t, h, "另一个Agent")
			awTestCall(t, h, aliceToken, other.ID, "read", map[string]any{"action": "resources", "source_session_id": source}, 404)
			d := awCreateResourceDefinition(t, h, aliceToken, a, awResourceDefinition(skillID, graph))
			bad := awResourceDefinition(skillID, graph)
			bad.Steps[0].Shortcuts = []string{"sr_temporary"}
			validation := awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "validate", "definition": bad}, 200)
			if awTestValue[bool](t, validation, "valid") {
				t.Fatal("temporary main-chat handle accepted as stable binding")
			}
			awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "trial", "workflow_id": d.ID, "request_id": "bad-typed-input", "expected_revision": d.Revision, "expected_hash": d.DefinitionHash, "source_session_id": source, "inputs": map[string]string{"topic": "x", "need_review": "true"}}, 400)
			run := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "trial", "workflow_id": d.ID, "request_id": "resource-trial", "expected_revision": d.Revision, "expected_hash": d.DefinitionHash, "source_session_id": source, "inputs": map[string]string{"topic": "合成项目"}}, 200), "run")
			waiting := awTestRun(t, h, aliceToken, a.ID, d.ID, run.ID, af.WaitingConfirmation)
			if waiting.Resources == nil || waiting.Inputs["need_review"] != "yes" || waiting.Inputs["audience"] != "ops" || len(waiting.Resources.Nodes["collect"].Skills) != 1 || len(waiting.Resources.Nodes["collect"].Shortcuts) != 1 {
				t.Fatal("bound snapshots/defaults missing", waiting)
			}
			owner := identity.LocalScope("alice")
			private, err := taskRunsDB(t, h.mgr).AWRun(sessionResultOwnerKey(&owner), a.ID, d.ID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			collector := h.session(private.Steps[0].SessionID)
			if !h.mgr.agentWorkflowSessionOwned(collector.ID, h.mgr.agentWorkflowMarker(collector.ID)) {
				t.Fatal("bound sequential node result became unreadable")
			}
			// Changing the current library cannot rewrite the already frozen text.
			view, err := h.mgr.userShortcuts(&owner, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			view.Shortcuts[0].Parts = []inputref.Part{{Type: "text", Text: "NEW-LIBRARY-CONTENT"}}
			if _, err = h.mgr.replaceUserShortcuts(&owner, a.ID, view.Revision, view.Shortcuts); err != nil {
				t.Fatal(err)
			}
			frozen := waiting.Resources.Nodes["collect"].Shortcuts[0]
			text, err := h.mgr.awReadOwned(&owner, filepath.Join(private.Workspace, "_resources", "collect", filepath.FromSlash(frozen.Path)), 4<<20)
			if err != nil || !strings.Contains(string(text), "SHORTCUT-RES-731") || strings.Contains(string(text), "NEW-LIBRARY") {
				t.Fatal("frozen shortcut changed", err, string(text))
			}
			awTestCall(t, h, aliceToken, a.ID, "confirm", map[string]any{"workflow_id": d.ID, "run_id": run.ID, "step_id": "confirm", "request_id": "resources-confirm", "feedback": "资料正确，请继续"}, 200)
			done := awTestRun(t, h, aliceToken, a.ID, d.ID, run.ID, af.Completed)
			if done.Steps[0].SessionID == done.Steps[2].SessionID || done.Steps[2].Artifact != "report.md" {
				t.Fatal("resource node sessions were shared or report missing", done)
			}
			worker := h.session(done.Steps[2].SessionID)
			for _, f := range done.Resources.Nodes["collect"].Files {
				if _, err = os.Stat(filepath.Join(worker.cwd, filepath.FromSlash(f.Path))); !os.IsNotExist(err) {
					t.Fatal("another node's bound file leaked", f.Path, err)
				}
			}
		})
	}
}

func TestAgentWorkflowResourcesRejectUnsupportedAndRevokedAndRetryFrozen(t *testing.T) {
	h := newMultiuserHarness(t, nil, func(c *Config) {
		if canonical, err := filepath.EvalSymlinks(c.SessionDir); err == nil {
			c.SessionDir = canonical
		}
		c.NewClient = func(config.Resolved, func(llm.RetryInfo)) llm.Client { return &awResourceTestClient{} }
	})
	a, source, skillID, _ := awSeedResourceAgent(t, h, aliceToken)
	d := awCreateResourceDefinition(t, h, aliceToken, a, awResourceDefinition(skillID, true))
	run := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "trial", "workflow_id": d.ID, "request_id": "cancel-resource-trial", "expected_revision": d.Revision, "expected_hash": d.DefinitionHash, "source_session_id": source, "inputs": map[string]string{"topic": "合成项目"}}, 200), "run")
	awTestRun(t, h, aliceToken, a.ID, d.ID, run.ID, af.WaitingConfirmation)
	awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "cancel", "workflow_id": d.ID, "run_id": run.ID, "request_id": "resource-cancel"}, 200)
	owner := identity.LocalScope("alice")
	view, err := h.mgr.userShortcuts(&owner, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	view.Shortcuts[0].Parts = []inputref.Part{{Type: "text", Text: "LATEST-SHOULD-NOT-REPLACE-SNAPSHOT"}}
	view, err = h.mgr.replaceUserShortcuts(&owner, a.ID, view.Revision, view.Shortcuts)
	if err != nil {
		t.Fatal(err)
	}
	retried := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "retry", "workflow_id": d.ID, "run_id": run.ID, "request_id": "resource-retry"}, 200), "run")
	oldBytes, _ := json.Marshal(run.Resources)
	newBytes, _ := json.Marshal(retried.Resources)
	if string(oldBytes) != string(newBytes) {
		t.Fatal("retry silently selected current resources")
	}
	waiting := awTestRun(t, h, aliceToken, a.ID, d.ID, retried.ID, af.WaitingConfirmation)
	if _, err = h.mgr.replaceUserShortcuts(&owner, a.ID, view.Revision, []userShortcut{}); err != nil {
		t.Fatal(err)
	}
	awTestCall(t, h, aliceToken, a.ID, "confirm", map[string]any{"workflow_id": d.ID, "run_id": waiting.ID, "step_id": "confirm", "request_id": "revoked-resource-confirm", "feedback": "continue"}, 400)
	retained := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "run", "workflow_id": d.ID, "run_id": waiting.ID}, 200), "run")
	if retained.Status != af.WaitingConfirmation {
		t.Fatal("revoked resource confirmation changed Run", retained)
	}
}

func TestAgentWorkflowResourcesSourceDirectoryEscapeAndInlineBudget(t *testing.T) {
	h := newMultiuserHarness(t, nil, func(c *Config) {
		if p, e := filepath.EvalSymlinks(c.SessionDir); e == nil {
			c.SessionDir = p
		}
	})
	a, source, skillID, doc := awSeedResourceAgent(t, h, aliceToken)
	owner := identity.LocalScope("alice")
	// A source directory must stay beneath the trusted owner root even if a
	// mutable Agent-directory ancestor is replaced by a symlink after binding.
	cwd := h.session(source).cwd
	backup := cwd + "-resource-backup"
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "outside-secret.txt"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(cwd, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, cwd); err != nil {
		os.Rename(backup, cwd)
		t.Fatal(err)
	}
	_, escapeErr := h.psrv.agentWorkflowResources(context.Background(), &owner, a, source)
	os.Remove(cwd)
	os.Rename(backup, cwd)
	if escapeErr == nil {
		t.Fatal("source-directory symlink escaped the trusted owner root")
	}
	for _, name := range []string{"invalid\\name.txt", "invalid\nname.txt", "invalid:name.txt"} {
		if err := os.WriteFile(filepath.Join(cwd, name), []byte("unselectable"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	catalog, catalogErr := h.psrv.agentWorkflowResources(context.Background(), &owner, a, source)
	if catalogErr != nil {
		t.Fatal("invalid filenames made the authorized catalog unavailable", catalogErr)
	}
	for _, file := range catalog.Files {
		if !af.SafePath(file.Path) {
			t.Fatal("unselectable filename leaked into resource catalog", file.Path)
		}
	}
	// Independently bounded documents must also fit the combined inline budget.
	if err := os.WriteFile(doc, []byte(strings.Repeat("d", 220<<10)), 0600); err != nil {
		t.Fatal(err)
	}
	view, err := h.mgr.userShortcuts(&owner, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	view.Shortcuts[0].Parts = []inputref.Part{{Type: "text", Text: strings.Repeat("s", 64<<10)}}
	if _, err = h.mgr.replaceUserShortcuts(&owner, a.ID, view.Revision, view.Shortcuts); err != nil {
		t.Fatal(err)
	}
	d := awResourceDefinition(skillID, false)
	if err = h.psrv.validateAgentWorkflowResources(context.Background(), &owner, a, d); err == nil || !strings.Contains(err.Error(), "256KiB") {
		t.Fatal("oversized combined inline resources were not rejected before execution", err)
	}
	known, err := h.mgr.awResourceSkills(&owner, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.mgr.awCompileShortcut(context.Background(), &owner, a, known, shortcutToolReference{parts: []inputref.Part{{Type: "element", Text: "unsupported live page"}}}, nil); err == nil {
		t.Fatal("unsupported live reference silently ignored")
	}
}

func TestAgentWorkflowResourcesIncludePrivateAndRuntimeSkillDocuments(t *testing.T) {
	t.Run("private", func(t *testing.T) {
		h := newMultiuserHarness(t, nil, func(c *Config) {
			if p, e := filepath.EvalSymlinks(c.SessionDir); e == nil {
				c.SessionDir = p
			}
		})
		a, _, _, _ := awSeedResourceAgent(t, h, aliceToken)
		alice, bob := identity.LocalScope("alice"), identity.LocalScope("bob")
		store, err := h.mgr.authoredSkillStore(&alice, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		saved, err := store.Save(context.Background(), authoredRequest("resource-private", "private-resource-guide", "PRIVATE-RESOURCE-GUIDE"))
		if err != nil {
			t.Fatal(err)
		}
		allowed, err := h.mgr.awResourceSkills(&alice, a)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := allowed[saved.Skill.ID]; !ok {
			t.Fatal("Agent-owned private Skill omitted")
		}
		other, err := h.mgr.awResourceSkills(&bob, a)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := other[saved.Skill.ID]; ok {
			t.Fatal("another owner's private Skill leaked")
		}
	})
	t.Run("runtime", func(t *testing.T) {
		h, _, conversation, id := runtimeSkillObserveFixture(t, nil)
		sess := h.session(conversation.ID)
		a, _ := h.mgr.studio.get(sess.studioID)
		known, err := h.mgr.awResourceSkills(nil, a)
		if err != nil {
			t.Fatal(err)
		}
		sk, ok := known[id]
		if !ok || sk.Source != "bundle" {
			t.Fatal("authorized runtime Skill omitted", known)
		}
		if data, err := awReadResourceSkill(sk); err != nil || len(data) == 0 {
			t.Fatal("runtime main document not readable through metadata boundary", err)
		}
		for _, installed := range h.mgr.cfg.SkillRuntime.Catalog() {
			if installed.Skill == sk.Name {
				if err := h.mgr.cfg.SkillRuntime.Revoke(installed.Ref.PackageDigest); err != nil {
					t.Fatal(err)
				}
				break
			}
		}
		unavailable, err := h.mgr.awResourceSkills(nil, a)
		if err != nil || awSkillResourceReason(unavailable[id]) == "" {
			t.Fatal("revoked runtime package still bindable", err)
		}
		selection := []string{}
		a.Skills = &selection
		a.SkillBinding = nil
		revoked, err := h.mgr.awResourceSkills(nil, a)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok = revoked[id]; ok {
			t.Fatal("removed runtime Skill still authorized")
		}
	})
}

func TestAgentWorkflowResourcesSequenceCanBeginWithConfirmation(t *testing.T) {
	h := newMultiuserHarness(t, nil, func(c *Config) {
		if p, e := filepath.EvalSymlinks(c.SessionDir); e == nil {
			c.SessionDir = p
		}
		c.NewClient = func(config.Resolved, func(llm.RetryInfo)) llm.Client { return &awResourceTestClient{} }
	})
	a, source, skillID, _ := awSeedResourceAgent(t, h, aliceToken)
	d := awResourceDefinition(skillID, false)
	d.Steps = append([]af.Step{{ID: "preconfirm", Kind: "confirm", Prompt: "请确认后开始读取资料"}}, d.Steps...)
	d = awCreateResourceDefinition(t, h, aliceToken, a, d)
	run := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "trial", "workflow_id": d.ID, "request_id": "resource-preconfirm-trial", "expected_revision": d.Revision, "expected_hash": d.DefinitionHash, "source_session_id": source, "inputs": map[string]string{"topic": "合成资料"}}, 200), "run")
	first := awTestRun(t, h, aliceToken, a.ID, d.ID, run.ID, af.WaitingConfirmation)
	if first.ExecutionSessionID != "" || first.CurrentStep != 0 {
		t.Fatal("resource sequence created a shared execution session before an Agent node", first)
	}
	awTestCall(t, h, aliceToken, a.ID, "confirm", map[string]any{"workflow_id": d.ID, "run_id": run.ID, "step_id": "preconfirm", "request_id": "resource-preconfirm", "feedback": "开始"}, 200)
	second := awTestRun(t, h, aliceToken, a.ID, d.ID, run.ID, af.WaitingConfirmation)
	if second.CurrentStep != 2 || second.ExecutionSessionID == "" || second.ExecutionSessionID != second.Steps[1].SessionID {
		t.Fatal("first Agent node did not establish the fixed execution session", second)
	}
	awTestCall(t, h, aliceToken, a.ID, "confirm", map[string]any{"workflow_id": d.ID, "run_id": run.ID, "step_id": "confirm", "request_id": "resource-preconfirm-finish", "feedback": "继续"}, 200)
	awTestRun(t, h, aliceToken, a.ID, d.ID, run.ID, af.Completed)
}

func TestAgentWorkflowResourcesMaterializationNeverOverwritesExistingNodeFiles(t *testing.T) {
	for _, graph := range []bool{false, true} {
		t.Run(fmt.Sprint(graph), func(t *testing.T) {
			h := newMultiuserHarness(t, nil, func(c *Config) {
				if p, e := filepath.EvalSymlinks(c.SessionDir); e == nil {
					c.SessionDir = p
				}
				c.NewClient = func(config.Resolved, func(llm.RetryInfo)) llm.Client { return &awResourceTestClient{} }
			})
			a, source, skillID, _ := awSeedResourceAgent(t, h, aliceToken)
			d := awCreateResourceDefinition(t, h, aliceToken, a, awResourceDefinition(skillID, graph))
			run := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "trial", "workflow_id": d.ID, "request_id": "collision-resource-trial", "expected_revision": d.Revision, "expected_hash": d.DefinitionHash, "source_session_id": source, "inputs": map[string]string{"topic": "合成项目"}}, 200), "run")
			awTestRun(t, h, aliceToken, a.ID, d.ID, run.ID, af.WaitingConfirmation)
			owner := identity.LocalScope("alice")
			stored, err := taskRunsDB(t, h.mgr).AWRun(sessionResultOwnerKey(&owner), a.ID, d.ID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			file := stored.Resources.Nodes["report"].Files[0]
			target := filepath.Join(stored.Workspace, "nodes", "report", filepath.FromSlash(file.Path))
			if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(target, []byte("DO-NOT-OVERWRITE"), 0600); err != nil {
				t.Fatal(err)
			}
			awTestCall(t, h, aliceToken, a.ID, "confirm", map[string]any{"workflow_id": d.ID, "run_id": run.ID, "step_id": "confirm", "request_id": "collision-resource-confirm", "feedback": "继续"}, 200)
			awGraphWaitTerminal(t, h, a, d, run.ID, af.Failed)
			data, err := os.ReadFile(target)
			if err != nil || string(data) != "DO-NOT-OVERWRITE" {
				t.Fatal("resource materialization overwrote existing node bytes", err, string(data))
			}
		})
	}
}

func TestAgentWorkflowResourcesPrivateSkillEditsRejectConfirmationAndRetry(t *testing.T) {
	for _, graph := range []bool{false, true} {
		t.Run(fmt.Sprint(graph), func(t *testing.T) {
			h := newMultiuserHarness(t, nil, func(c *Config) {
				if p, e := filepath.EvalSymlinks(c.SessionDir); e == nil {
					c.SessionDir = p
				}
				c.NewClient = func(config.Resolved, func(llm.RetryInfo)) llm.Client { return &awResourceTestClient{} }
			})
			a, source, skillID, _ := awSeedResourceAgent(t, h, aliceToken)
			owner := identity.LocalScope("alice")
			store, err := h.mgr.authoredSkillStore(&owner, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			original, err := store.Save(context.Background(), authoredRequest("private-bound-create", "private-bound-guide", "PRIVATE-OLD-GUIDANCE"))
			if err != nil {
				t.Fatal(err)
			}
			d := awResourceDefinition(skillID, graph)
			for i := range d.Steps {
				if d.Steps[i].Kind == "agent" {
					d.Steps[i].Skills = append(d.Steps[i].Skills, original.Skill.ID)
				}
			}
			d = awCreateResourceDefinition(t, h, aliceToken, a, d)
			run := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "trial", "workflow_id": d.ID, "request_id": "private-bound-trial", "expected_revision": d.Revision, "expected_hash": d.DefinitionHash, "source_session_id": source, "inputs": map[string]string{"topic": "合成资料"}}, 200), "run")
			waiting := awTestRun(t, h, aliceToken, a.ID, d.ID, run.ID, af.WaitingConfirmation)
			awTestSegments(t, h, a.ID, d.ID, run.ID, 1)
			before, _ := json.Marshal(waiting)
			fingerprint := h.mgr.agentWorkflowFingerprint(a, &owner)
			change := authoredRequest("private-bound-edit", "private-bound-guide", "PRIVATE-NEW-GUIDANCE")
			change.SkillID, change.ExpectedRevision = original.Skill.ID, original.Skill.Revision
			if _, err = store.Save(context.Background(), change); err != nil {
				t.Fatal(err)
			}
			if fingerprint != h.mgr.agentWorkflowFingerprint(a, &owner) {
				t.Fatal("test must exercise the unchanged legacy fingerprint gap")
			}
			failed := awTestCall(t, h, aliceToken, a.ID, "confirm", map[string]any{"workflow_id": d.ID, "run_id": run.ID, "step_id": "confirm", "request_id": "private-bound-confirm", "feedback": "继续"}, 400)
			if !strings.Contains(string(failed["error"]), "主文档已变化") {
				t.Fatal("confirmation did not explain the frozen guide mismatch", failed)
			}
			retained := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "run", "workflow_id": d.ID, "run_id": run.ID}, 200), "run")
			after, _ := json.Marshal(retained)
			if string(before) != string(after) {
				t.Fatal("rejected confirmation changed the frozen Run")
			}
			legacy := retained
			legacy.Resources = nil
			if err = h.mgr.checkAgentWorkflowResources(context.Background(), &owner, a, legacy); err != nil {
				t.Fatal("legacy nil-resource behavior changed", err)
			}
			cancelled := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "cancel", "workflow_id": d.ID, "run_id": run.ID, "request_id": "private-bound-cancel"}, 200), "run")
			cancelledBytes, _ := json.Marshal(cancelled)
			failed = awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "retry", "workflow_id": d.ID, "run_id": run.ID, "request_id": "private-bound-retry"}, 400)
			if !strings.Contains(string(failed["error"]), "主文档已变化") {
				t.Fatal("retry silently accepted a newer private guide", failed)
			}
			retained = awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "run", "workflow_id": d.ID, "run_id": run.ID}, 200), "run")
			after, _ = json.Marshal(retained)
			if string(cancelledBytes) != string(after) {
				t.Fatal("rejected retry changed the original Run")
			}
		})
	}
}
