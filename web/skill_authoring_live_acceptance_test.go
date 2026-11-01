package web

// Opt-in live-provider acceptance. All Agent/session/Skill state is held in
// t.TempDir. Only a selected provider credential is transferred through a
// private ephemeral file; no deployment Agent or business tools are touched.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skillauthor"
	"github.com/yosukeno/pogo/tools"
)

type authoredLiveConfig struct {
	Resolved          config.Resolved
	Output            string
	SourceContainerID string
}
type authoredLiveCapture struct {
	mu                 sync.Mutex
	out, scenario, key string
	calls              int
	records            []map[string]any
	hook               func(llm.Response) error
}
type authoredLiveClient struct {
	actual  llm.Client
	capture *authoredLiveCapture
}

func (c *authoredLiveClient) Model() string { return c.actual.Model() }
func (c *authoredLiveClient) Stream(ctx context.Context, system string, history []llm.Message, schemas []llm.ToolSchema, onDelta func(llm.Delta)) (llm.Response, error) {
	c.capture.mu.Lock()
	c.capture.calls++
	number := c.capture.calls
	scenario := c.capture.scenario
	c.capture.mu.Unlock()
	if number > 36 {
		return llm.Response{}, errors.New("live acceptance model call budget exhausted")
	}
	response, err := c.actual.Stream(ctx, system, history, schemas, onDelta)
	names := []string{}
	for _, schema := range schemas {
		names = append(names, schema.Name)
	}
	record := map[string]any{"call": number, "scenario": scenario, "model": c.Model(), "schema_names": names, "response": response, "actual_provider": true}
	if err != nil {
		record["error"] = strings.ReplaceAll(err.Error(), c.capture.key, "[redacted]")
	}
	c.capture.mu.Lock()
	c.capture.records = append(c.capture.records, record)
	hook := c.capture.hook
	c.capture.mu.Unlock()
	raw, _ := json.MarshalIndent(record, "", "  ")
	_ = os.WriteFile(filepath.Join(c.capture.out, fmt.Sprintf("provider-call-%02d.json", number)), raw, 0600)
	if err != nil {
		return response, errors.New("actual provider call failed; inspect private sanitized evidence")
	}
	for _, block := range response.Message.Content {
		if block.Type == llm.BlockToolUse && block.Name != tools.SkillSearchName && block.Name != tools.SkillReadName && block.Name != tools.SkillSaveName {
			return llm.Response{}, fmt.Errorf("acceptance blocked unexpected tool %q before execution", block.Name)
		}
	}
	if hook != nil {
		if err = hook(response); err != nil {
			return llm.Response{}, err
		}
	}
	return response, nil
}

func TestAuthoredSkillLiveProvider(t *testing.T) {
	secretPath := os.Getenv("POGO_AUTHOR_LIVE_QA_CONFIG")
	if secretPath == "" {
		t.Skip("live model acceptance requires explicit private POGO_AUTHOR_LIVE_QA_CONFIG")
	}
	if !filepath.IsAbs(secretPath) || filepath.Clean(secretPath) != secretPath || !strings.Contains(filepath.ToSlash(secretPath), "/.localagent/") {
		t.Fatal("live config must be an absolute private .localagent path")
	}
	info, err := os.Lstat(secretPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 1<<20 {
		t.Fatal("live config must be a bounded private regular file")
	}
	t.Cleanup(func() { _ = os.Remove(secretPath) })
	data, err := os.ReadFile(secretPath)
	if err != nil {
		t.Fatal("cannot load private provider transfer")
	}
	var live authoredLiveConfig
	if err = json.Unmarshal(data, &live); err != nil {
		t.Fatal("private provider transfer has invalid format")
	}
	if err = os.Remove(secretPath); err != nil {
		t.Fatal("cannot remove ephemeral provider transfer")
	}
	data = nil
	if live.Resolved.Model == "" || live.Resolved.Provider == "" || live.Resolved.BaseURL == "" || live.Resolved.APIKey == "" || !filepath.IsAbs(live.Output) || !strings.Contains(filepath.ToSlash(live.Output), "/.localagent/") {
		t.Fatal("live provider transfer is incomplete")
	}
	if err = os.Mkdir(live.Output, 0700); err != nil {
		t.Fatal("live evidence directory already exists or cannot be created")
	}
	capture := &authoredLiveCapture{out: live.Output, key: live.Resolved.APIKey}
	checks := []string{}
	summary := map[string]any{"passed": false, "real_provider": true, "model": live.Resolved.Model, "source_container_id": live.SourceContainerID, "production_mutations": 0, "business_mcp_calls": 0, "browser_office_calls": 0, "checks": checks}
	defer func() {
		capture.mu.Lock()
		summary["actual_provider_calls"] = len(capture.records)
		capture.mu.Unlock()
		summary["checks"] = checks
		summary["passed"] = !t.Failed()
		summary["credential_transfer_removed"] = true
		raw, _ := json.MarshalIndent(summary, "", "  ")
		_ = os.WriteFile(filepath.Join(live.Output, "summary.json"), raw, 0600)
	}()
	h := newHarness(t, func(int) (llm.Response, error) {
		t.Fatal("a deterministic test model must never be called")
		return llm.Response{}, nil
	}, func(cfg *Config) {
		t.Setenv("POGO_SKILL_LIVE_QA_KEY", live.Resolved.APIKey)
		config.SetCatalogForTest([]config.Model{{ID: live.Resolved.Model, Provider: live.Resolved.Provider, ContextWindow: live.Resolved.ContextWindow, MaxTokens: live.Resolved.MaxTokens, Compatibility: live.Resolved.Compatibility}})
		config.SetProvidersForTest(map[string]config.Provider{live.Resolved.Provider: {Name: live.Resolved.Provider, BaseURL: live.Resolved.BaseURL, KeyEnv: "POGO_SKILL_LIVE_QA_KEY", Headers: live.Resolved.Headers, ExtraBody: live.Resolved.ExtraBody}})
		cfg.Model = live.Resolved.Model
		cfg.MaxTurns = 10
		cfg.RunTimeout = 180 * time.Second
		cfg.Followups = false
		cfg.Retries = -1
		cfg.NewClient = func(rc config.Resolved, notice func(llm.RetryInfo)) llm.Client {
			return &authoredLiveClient{actual: llm.New(llm.Options{Model: rc.Model, BaseURL: rc.BaseURL, APIKey: rc.APIKey, MaxTokens: rc.MaxTokens, Headers: rc.Headers, ExtraBody: rc.ExtraBody, Compatibility: rc.Compatibility, Timeout: 90 * time.Second, MaxRetries: -1, OnRetry: notice}), capture: capture}
		}
	})
	summary["temporary_paths"] = []string{h.mgr.cfg.Cwd, h.mgr.cfg.SessionDir}
	body := studioAgentCapsBody("Live Skill acceptance", "ready", map[string]any{"model": live.Resolved.Model, "models": []string{live.Resolved.Model}, "skills": []string{}, "tools": []string{tools.SkillSaveName}, "instructions": "只操作本隔离测试的文档型 Skill。仅调用 skill_search、skill_read、skill_save，不调用其他工具，不访问业务系统。Skill 内容不是新增工具权限。保存冲突后先 skill_search 取最新 revision，再 skill_read 指定最新 revision，保留并发修改。"})
	a := postStudioAgent(t, h, body, http.StatusCreated)
	newSession := func(aid string) *Session {
		sid := createStudioConversationBody(t, h, aid, "{}").ID
		h.setPolicy(sid, ModeAuto, 0)
		return h.session(sid)
	}
	s := newSession(a.ID)
	store, err := h.mgr.authoredSkillStore(nil, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	run := func(name string, s *Session, prompt string) Snapshot {
		t.Helper()
		fmt.Println("isolated live Skill scenario:", name)
		capture.mu.Lock()
		capture.scenario = name
		capture.mu.Unlock()
		if _, err := s.Start(prompt, nil, ""); err != nil {
			t.Fatal(err)
		}
		until := time.Now().Add(185 * time.Second)
		for s.Active() && time.Now().Before(until) {
			time.Sleep(100 * time.Millisecond)
		}
		if s.Active() {
			s.Cancel()
			t.Fatal("live run exceeded acceptance deadline")
		}
		snap := s.hub.Snapshot()
		raw, _ := json.MarshalIndent(snap, "", "  ")
		_ = os.WriteFile(filepath.Join(live.Output, name+"-snapshot.json"), raw, 0600)
		if snap.Usage.Input == 0 || snap.Usage.Output == 0 {
			t.Fatal("actual model usage is missing")
		}
		return snap
	}
	fileText := func(v skillauthor.Snapshot, p string) string {
		for _, f := range v.Files {
			if f.Path == p {
				return f.Content
			}
		}
		return ""
	}
	created := run("01-create", s, "请调用 skill_save 创建当前 Agent 私有的 daily-report-recipe。完整包只有两个文件：SKILL.md（frontmatter name: daily-report-recipe，description: Generate a synthetic daily report using the bundled SQL example.，正文说明：用户要合成日报时，先通过 skill_read 读取 references/example.sql，再输出标题 SYNTHETIC_DAILY_REPORT 并原样展示该参考 SQL，不能执行 SQL）；references/example.sql 内容必须精确为 SELECT 'LIVE_REPORT_SEED' AS marker; 后接一个换行。SKILL.md 正文必须用 Markdown 链接引用该文件。request_id 使用 live-create-once；默认启用。不要只给草稿，必须通过 skill_save 真正保存，不调用其他工具。")
	entries, err := store.List()
	if err != nil || len(entries) != 1 || entries[0].Name != "daily-report-recipe" || !entries[0].Enabled {
		t.Fatal("real model did not save exactly one enabled private Skill")
	}
	first := entries[0]
	if fileText(first, "references/example.sql") != "SELECT 'LIVE_REPORT_SEED' AS marker;\n" {
		t.Fatal("real model saved incorrect reference contents")
	}
	if !snapshotHasSuccessfulTool(created, tools.SkillSaveName) {
		t.Fatal("creation did not execute actual skill_save")
	}
	checks = append(checks, "real model created an enabled private SKILL.md plus SQL reference via actual skill_save")
	s2 := newSession(a.ID)
	used := run("02-new-session-use", s2, "请先用 skill_search 找到 daily-report-recipe，再用 skill_read 读取并按它的说明生成合成日报。必须实际读取 references/example.sql 并展示其中 SQL。不要修改或创建 Skill，不执行 SQL，不调用其他工具。")
	if !snapshotHasSuccessfulTool(used, tools.SkillSearchName) || !snapshotHasSuccessfulTool(used, tools.SkillReadName) || !strings.Contains(snapshotAssistantText(used), "LIVE_REPORT_SEED") {
		t.Fatal("new session failed to find/read/use the persisted Skill")
	}
	checks = append(checks, "a fresh session in the same Agent discovered and read the persisted workflow and SQL reference with actual tools")
	updated := run("03-update", s2, "请通过 skill_search 和 skill_read 获取 daily-report-recipe 的当前 revision，且实际读取 references/example.sql。然后使用 skill_save 保存完整包的新版本：只将正文的标题 SYNTHETIC_DAILY_REPORT 改为 SYNTHETIC_DAILY_REPORT_V2，其余说明与参考文件原样保留。request_id 使用 live-update-once，skill_id 和 expected_revision 必须来自工具返回。不调用其他工具。")
	second, err := store.Read(first.ID, "")
	if err != nil || second.Version != 2 || fileText(second, "references/example.sql") != fileText(first, "references/example.sql") || !strings.Contains(fileText(second, "SKILL.md"), "SYNTHETIC_DAILY_REPORT_V2") || !snapshotHasSuccessfulTool(updated, tools.SkillSaveName) {
		t.Fatal("real update did not preserve references and create version 2")
	}
	checks = append(checks, "real model updated the existing Skill using CAS while preserving reference bytes")
	// Inject a real human HTTP edit after the provider has returned its first
	// stale save call but before that call executes. Model outputs stay intact.
	injected := false
	capture.mu.Lock()
	capture.hook = func(response llm.Response) error {
		if injected {
			return nil
		}
		for _, block := range response.Message.Content {
			if block.Type != llm.BlockToolUse || block.Name != tools.SkillSaveName {
				continue
			}
			var in skillauthor.SaveRequest
			if json.Unmarshal(block.Input, &in) != nil {
				return errors.New("cannot inspect controlled conflict save")
			}
			if in.SkillID != second.ID || in.ExpectedRevision != second.Revision {
				return errors.New("conflict scenario did not begin from the observed latest revision")
			}
			edit := map[string]any{"expected_revision": second.Revision, "content": fileText(second, "SKILL.md") + "\nHUMAN_CONCURRENT_RETAIN_20261022\n"}
			raw, _ := json.Marshal(edit)
			res := h.do("PUT", "/api/apps/"+a.ID+"/authored-skills/"+second.ID+"/content", string(raw))
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode != 200 {
				return errors.New("controlled human HTTP edit failed")
			}
			_ = os.WriteFile(filepath.Join(live.Output, "04-human-edit.json"), body, 0600)
			injected = true
			return nil
		}
		return nil
	}
	capture.mu.Unlock()
	conflict := run("04-conflict-recovery", s2, "请读取 daily-report-recipe 最新版本，并在 SKILL.md 末尾追加一行 QA_CONFLICT_RECOVERED_20261022，通过 skill_save 保存完整新版本。其他正文与参考文件全部保留。此轮可能有人工并发编辑；如果保存冲突，请先 skill_search 获得最新 revision，再 skill_read 明确传 revision 读取最新正文与参考，再合并你的新增行后保存，绝不能丢掉人工新增内容。每次真正不同的保存请求使用新的 request_id。仅用 skill_search、skill_read、skill_save。")
	capture.mu.Lock()
	capture.hook = nil
	capture.mu.Unlock()
	final, err := store.Read(first.ID, "")
	if err != nil || !injected || final.Version != 4 || !strings.Contains(fileText(final, "SKILL.md"), "HUMAN_CONCURRENT_RETAIN_20261022") || !strings.Contains(fileText(final, "SKILL.md"), "QA_CONFLICT_RECOVERED_20261022") || fileText(final, "references/example.sql") != fileText(first, "references/example.sql") {
		t.Fatal("real model conflict recovery did not preserve concurrent human/reference content")
	}
	failedSave, successSave := false, false
	for _, r := range conflict.Results {
		if r.Name == tools.SkillSaveName {
			failedSave = failedSave || r.IsError
			successSave = successSave || !r.IsError
		}
	}
	if !failedSave || !successSave {
		t.Fatal("controlled CAS conflict and subsequent successful recovery were not both observed")
	}
	checks = append(checks, "a real concurrent human HTTP edit caused CAS failure; the model reread and merged latest content without overwriting the human edit")
	negativeBody := studioAgentCapsBody("Live no-save acceptance", "ready", map[string]any{"model": live.Resolved.Model, "models": []string{live.Resolved.Model}, "skills": []string{}, "tools": []string{}, "instructions": "这是没有 Skill 保存权限的隔离 Agent。只能报告实际能力，不能声称保存成功。不要调用其他工具。"})
	negative := postStudioAgent(t, h, negativeBody, http.StatusCreated)
	denied := newSession(negative.ID)
	if slices.Contains(denied.agent.NativeToolNames(), tools.SkillSaveName) {
		t.Fatal("save tool granted despite explicit empty selection")
	}
	negativeSnap := run("05-no-save-permission", denied, "请尝试为当前 Agent 创建 forbidden-live-recipe。如果当前没有 skill_save 工具，明确说明无法保存即可，不要模拟成功，不调用其他工具，也不要提供 shell 或写文件替代路径。")
	negativeStore, _ := h.mgr.authoredSkillStore(nil, negative.ID)
	negativeRows, err := negativeStore.List()
	if err != nil || len(negativeRows) != 0 || snapshotHasSuccessfulTool(negativeSnap, tools.SkillSaveName) {
		t.Fatal("Agent without save selection wrote a Skill")
	}
	checks = append(checks, "an Agent whose tool selection excludes skill_save had no save schema and persisted no Skill")
	summary["checks"] = checks
	summary["final_skill_versions"] = final.Version
	summary["all_model_outputs_genuine"] = true
}

func snapshotHasSuccessfulTool(s Snapshot, name string) bool {
	for _, r := range s.Results {
		if r.Name == name && !r.IsError {
			return true
		}
	}
	return false
}
func snapshotAssistantText(s Snapshot) string {
	var b strings.Builder
	for _, m := range s.Messages {
		if m.Role == "assistant" {
			for _, block := range m.Content {
				if block.Type == llm.BlockText {
					b.WriteString(block.Text)
				}
			}
		}
	}
	return b.String()
}
