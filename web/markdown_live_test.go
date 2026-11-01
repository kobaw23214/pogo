package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/skills"
)

func TestMarkdownRealModelGuidance(t *testing.T) {
	h := newSentinelLiveHarness(t, "k3")
	dirs, err := skills.MaterializeEmbedded(filepath.Join(h.root, "embedded"))
	if err != nil {
		t.Fatal(err)
	}
	skillList, diagnostics := skills.Load(skills.Options{Cwd: h.root, UserDir: filepath.Join(h.root, "user-skills"), Embedded: dirs})
	if len(diagnostics) > 0 {
		t.Fatalf("load isolated rich Markdown skills: %v", diagnostics)
	}
	h.mgr.cfg.Skills = skillList
	h.mgr.skillRoots = skills.Roots(skillList)
	h.mgr.skillSection = skills.PromptSection(skillList)
	h.mgr.uiSection = uiCapabilitiesSection(skillList)
	selectedSkills := []string{}
	for _, sk := range skillList {
		if sk.Name == "web-drawing" {
			selectedSkills = append(selectedSkills, skillIDOf(sk))
		}
	}
	if len(selectedSkills) != 1 {
		t.Fatal("isolated web-drawing skill was not discovered")
	}
	selectedTools := []string{}
	a, err := h.mgr.studio.put("", studioInput{
		Name: "Isolated rich Markdown author", Model: h.model, Status: "ready",
		Instructions: "Write clear Chinese answers and use the answer surface's rich formats only when they improve comprehension. Do not call tools.",
		Skills: &selectedSkills, MCPServers: &selectedTools, Tools: &selectedTools,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := h.mgr.createStudioConversation(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	prompt := "请写一份简短的季度数据分析报告，包含一个目录、一个行内公式、一个块级公式和一个三行两列的 CSV 数据表。数据使用合成的：一月 100、二月 120、三月 150。不要调用工具，不要解释这些格式是什么。"
	if err := h.runSession(t, sess, prompt, 4*time.Minute); err != nil {
		t.Fatal(err)
	}
	store, err := session.Open(h.mgr.transcriptPath(sess.ID))
	if err != nil {
		t.Fatal(err)
	}
	final := ""
	for _, message := range store.Messages("") {
		if message.Role != llm.RoleAssistant {
			continue
		}
		for _, block := range message.Content {
			if block.Type == llm.BlockText {
				final = block.Text
			}
		}
	}
	checks := map[string]bool{
		"toc": strings.Contains(final, "[TOC]"),
		"inline_math": strings.Contains(final, "$"),
		"display_math": strings.Contains(final, "$$") || strings.Contains(final, "```math"),
		"csv": strings.Contains(final, "```csv"),
		"actual_numbers": strings.Contains(final, "100") && strings.Contains(final, "120") && strings.Contains(final, "150"),
	}
	h.evidence(t, "markdown-real-model-guidance", map[string]any{"prompt": prompt, "response": final, "checks": checks})
	for name, passed := range checks {
		if !passed {
			t.Errorf("real Markdown guidance check %s failed; see isolated evidence", name)
		}
	}
	if os.Getenv("POGO_SENTINEL_LIVE_OUT") != "" {
		t.Logf("real Markdown evidence saved under %s", os.Getenv("POGO_SENTINEL_LIVE_OUT"))
	}
}

func TestMarkdownRealModelVisualGuidance(t *testing.T) {
	h := newSentinelLiveHarness(t, "k3")
	dirs, err := skills.MaterializeEmbedded(filepath.Join(h.root, "embedded"))
	if err != nil {
		t.Fatal(err)
	}
	skillList, diagnostics := skills.Load(skills.Options{Cwd: h.root, UserDir: filepath.Join(h.root, "user-skills"), Embedded: dirs})
	if len(diagnostics) > 0 {
		t.Fatalf("load isolated rich Markdown skills: %v", diagnostics)
	}
	h.mgr.cfg.Skills = skillList
	h.mgr.skillRoots = skills.Roots(skillList)
	h.mgr.skillSection = skills.PromptSection(skillList)
	h.mgr.uiSection = uiCapabilitiesSection(skillList)
	selectedSkills := []string{}
	for _, sk := range skillList {
		if sk.Name == "web-drawing" {
			selectedSkills = append(selectedSkills, skillIDOf(sk))
		}
	}
	if len(selectedSkills) != 1 {
		t.Fatal("isolated web-drawing skill was not discovered")
	}
	selectedTools := []string{}
	a, err := h.mgr.studio.put("", studioInput{
		Name: "Isolated visual Markdown author", Model: h.model, Status: "ready",
		Instructions: "Write clear Chinese answers and use the answer surface's rich formats only when they improve comprehension. Do not call tools.",
		Skills: &selectedSkills, MCPServers: &selectedTools, Tools: &selectedTools,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := h.mgr.createStudioConversation(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	prompt := "请把“输入数据 → 分析 → 生成报告”画成流程图，并用柱状图对比一月 100、二月 120、三月 150。不要调用工具，不要解释这些图是什么。"
	if err := h.runSession(t, sess, prompt, 4*time.Minute); err != nil {
		t.Fatal(err)
	}
	store, err := session.Open(h.mgr.transcriptPath(sess.ID))
	if err != nil {
		t.Fatal(err)
	}
	final := ""
	for _, message := range store.Messages("") {
		if message.Role != llm.RoleAssistant {
			continue
		}
		for _, block := range message.Content {
			if block.Type == llm.BlockText {
				final = block.Text
			}
		}
	}
	checks := map[string]bool{
		"mermaid": strings.Contains(final, "```mermaid"),
		"echarts": strings.Contains(final, "```echarts"),
		"actual_numbers": strings.Contains(final, "100") && strings.Contains(final, "120") && strings.Contains(final, "150"),
	}
	h.evidence(t, "markdown-real-model-visual-guidance", map[string]any{"prompt": prompt, "response": final, "checks": checks})
	for name, passed := range checks {
		if !passed {
			t.Errorf("real visual Markdown guidance check %s failed; see isolated evidence", name)
		}
	}
}
