package web

// Opt-in real-provider browser acceptance. Every account, Agent, conversation,
// workspace, queue and configuration copy lives in an independent TempDir.
// The selected provider credential is read only from the existing environment.
import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/catalog"
	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/inputref"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

const (
	awRealMaxCalls  = 80
	awRealMaxInput  = 1_200_000
	awRealMaxOutput = 128_000
)

type awRealModelClient struct {
	actual                             llm.Client
	capture                            *sentinelLiveCapture
	endpoint, provider                 string
	expectedEndpoint, expectedProvider string
}

func (c *awRealModelClient) Model() string { return c.actual.Model() }
func (c *awRealModelClient) Stream(ctx context.Context, system string, history []llm.Message, schemas []llm.ToolSchema, onDelta func(llm.Delta)) (llm.Response, error) {
	if c.endpoint != c.expectedEndpoint || c.provider != c.expectedProvider || c.Model() != "k3" {
		return llm.Response{}, errors.New("real workflow fixture rejected an unselected model endpoint")
	}
	// Ordinary authoring conversations always carry delegation as protected
	// session coordination. A subprocess would bypass this fixture's shared
	// provider recorder and budget, so withhold only these schemas here. The
	// production registry and per-Agent grants are left unchanged.
	allowed := make([]llm.ToolSchema, 0, len(schemas))
	withheld := []string{}
	for _, schema := range schemas {
		if schema.Name == "subagent" || schema.Name == "subagent_tasks" {
			withheld = append(withheld, schema.Name)
			continue
		}
		if awRealForbiddenTool(schema.Name) {
			return llm.Response{}, fmt.Errorf("isolated real workflow fixture exposed unexpected tool %q", schema.Name)
		}
		allowed = append(allowed, schema)
	}
	c.capture.mu.Lock()
	if c.capture.started >= awRealMaxCalls || c.capture.usage.Input >= awRealMaxInput || c.capture.usage.Output >= awRealMaxOutput {
		c.capture.mu.Unlock()
		return llm.Response{}, errors.New("real workflow acceptance model budget exhausted")
	}
	c.capture.started++
	number := c.capture.started
	c.capture.mu.Unlock()
	started := time.Now()
	record := sentinelLiveCall{Number: number, Model: c.Model(), StartedAt: started.UTC().Format(time.RFC3339Nano), FirstDeltaMS: -1, LastDeltaMS: -1, System: system, History: history, Tools: allowed}
	// Clone before streaming because each node's Agent appends history later.
	raw, _ := json.Marshal(record)
	_ = json.Unmarshal(raw, &record)
	var deltaMu sync.Mutex
	response, err := c.actual.Stream(ctx, system, history, allowed, func(delta llm.Delta) {
		elapsed := time.Since(started).Milliseconds()
		deltaMu.Lock()
		if record.DeltaCount == 0 {
			record.FirstDeltaMS = elapsed
		}
		record.LastDeltaMS = elapsed
		record.DeltaCount++
		deltaMu.Unlock()
		if onDelta != nil {
			onDelta(delta)
		}
	})
	for _, block := range response.Message.Content {
		if block.Type == llm.BlockToolUse && awRealForbiddenTool(block.Name) {
			err = fmt.Errorf("real workflow acceptance rejected unexpected tool %q before execution", block.Name)
			break
		}
	}
	deltaMu.Lock()
	record.DurationMS = time.Since(started).Milliseconds()
	record.Response = response.Message
	record.StopReason = response.StopReason
	record.Usage = response.Usage
	if err != nil {
		record.Error = err.Error()
	}
	deltaMu.Unlock()
	c.capture.mu.Lock()
	c.capture.calls = append(c.capture.calls, record)
	c.capture.usage.Input += response.Usage.Input
	c.capture.usage.Output += response.Usage.Output
	c.capture.usage.CacheRead += response.Usage.CacheRead
	c.capture.usage.Reasoning += response.Usage.Reasoning
	c.capture.mu.Unlock()
	evidence := struct {
		sentinelLiveCall
		Endpoint        string   `json:"endpoint"`
		Provider        string   `json:"provider"`
		ActualProvider  bool     `json:"actual_provider"`
		WithheldSchemas []string `json:"harness_withheld_schemas"`
		HarnessLimit    string   `json:"harness_limit"`
	}{record, c.endpoint, c.provider, true, withheld, "subagent delegation withheld to retain all model calls inside the shared acceptance budget; product permissions unchanged"}
	if data, e := c.capture.sanitizedJSON(evidence); e == nil {
		_ = os.WriteFile(filepath.Join(c.capture.output, fmt.Sprintf("model-call-%02d.json", number)), data, 0600)
	}
	awRealWriteBudget(c.capture, false)
	if err != nil {
		return llm.Response{}, errors.New("real provider call failed or exceeded acceptance boundaries; inspect private sanitized model evidence")
	}
	return response, nil
}
func awRealForbiddenTool(name string) bool {
	return strings.HasPrefix(name, "workflow_") || name == "bash" || name == "subagent" || name == "subagent_tasks" || name == "background_task" || name == "browser" || strings.HasPrefix(name, "browser_") || strings.HasPrefix(name, "watch")
}

func awRealWriteBudget(capture *sentinelLiveCapture, final bool) {
	calls := capture.Snapshot()
	complete := len(calls) == capture.Count()
	for _, call := range calls {
		if call.Error != "" || call.Usage.Input <= 0 || call.StopReason == "" {
			complete = false
		}
	}
	budget := map[string]any{"actual_provider": true, "calls_started": capture.Count(), "calls_completed": len(calls), "usage": capture.Usage(), "usage_complete": complete, "usage_scope": "provider-reported usage; cancelled requests may not report usage", "max_calls": awRealMaxCalls, "max_input": awRealMaxInput, "max_output": awRealMaxOutput, "final": final, "harness_withheld_tools": []string{"subagent", "subagent_tasks"}, "harness_limit": "no delegation outside shared model budget; product permissions unchanged"}
	if raw, e := capture.sanitizedJSON(budget); e == nil {
		// Model nodes can finish concurrently. A unique temporary file keeps a
		// browser reader from observing a partial budget document.
		if tmp, e := os.CreateTemp(capture.output, "budget-*.tmp"); e == nil {
			name := tmp.Name()
			_, werr := tmp.Write(raw)
			cerr := tmp.Close()
			if werr == nil && cerr == nil {
				_ = os.Rename(name, filepath.Join(capture.output, "model-budget.json"))
			}
			_ = os.Remove(name)
		}
	}
}

func TestAgentWorkflowRealModelUIFixture(t *testing.T) {
	output := os.Getenv("POGO_AGENT_WORKFLOW_REAL_FIXTURE")
	natural := os.Getenv("POGO_AGENT_WORKFLOW_NATURAL_FIXTURE")
	artifactName, address, viteURL := "agent-workflow-real-model-20261028", "127.0.0.1:19851", "http://127.0.0.1:19852"
	if natural != "" {
		if output != "" {
			t.Fatal("select only one isolated real-model fixture")
		}
		output, artifactName, address, viteURL = natural, "agent-workflow-natural-language-20261029", "127.0.0.1:19855", "http://127.0.0.1:19856"
	}
	if output == "" {
		t.Skip("opt-in isolated real-provider Agent workflow acceptance")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Clean(filepath.Join(wd, "..", "..", ".ci-artifacts", artifactName, "private"))
	if filepath.Clean(output) != want {
		t.Fatal("real fixture output must be this checkout's dedicated private acceptance directory")
	}
	if err = os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(output, 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal("isolated real-model fixture port is unavailable")
	}
	t.Cleanup(func() { _ = listener.Close() })
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"workspace", "sessions", "config", "embedded", "user-skills", "evidence"} {
		if err = os.MkdirAll(filepath.Join(state, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	scheduleEnv(t)
	for _, name := range []string{"POGO_OFFICE_BROWSER_URL", "POGO_OFFICE_SERVICE_URL", "POGO_OFFICE_PUBLIC_URL", "POGO_OFFICE_FETCH_URL", "POGO_OFFICE_JWT_SECRET_FILE", "POGO_READ_ROOTS", "POGO_PROVIDER_CONFIG", "POGO_MODEL_CONFIG"} {
		t.Setenv(name, "")
	}
	// This is the existing deployment catalog's metadata, copied without editing
	// the original. config.Load pins its provider URLs and reads credentials from
	// the already-present environment. No real credential is copied to disk.
	sourceCatalog := filepath.Clean(filepath.Join(wd, "..", "..", "w_deploy", "init", "providers.json"))
	providerBytes, err := os.ReadFile(sourceCatalog)
	if err != nil {
		t.Fatal("cannot read the existing provider catalog metadata")
	}
	copiedCatalog := filepath.Join(state, "config", config.FileName)
	if err = os.WriteFile(copiedCatalog, providerBytes, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.PathEnv, copiedCatalog)
	savedModels, savedProviders := config.Catalog(), config.Providers()
	t.Cleanup(func() { config.SetCatalogForTest(savedModels); config.SetProvidersForTest(savedProviders) })
	if _, err = config.Load(); err != nil {
		t.Fatal("real provider catalog cannot be loaded")
	}
	resolved, err := config.Resolve("k3")
	if err != nil {
		t.Fatal("the existing k3 model credential is not available")
	}
	if resolved.Provider != "kimi" || resolved.APIKey == "" || !strings.HasPrefix(resolved.BaseURL, "https://") {
		t.Fatal("the selected k3 provider metadata or credential is incomplete")
	}
	capture := &sentinelLiveCapture{output: filepath.Join(state, "evidence")}
	capture.addSecrets(resolved)
	// Evidence is kept outside TempDir, while all executable state is ephemeral.
	evidenceDir := filepath.Join(output, "model-evidence", time.Now().UTC().Format("20060102T150405.000000000Z"))
	if err = os.MkdirAll(evidenceDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(evidenceDir, 0700); err != nil {
		t.Fatal(err)
	}
	capture.output = evidenceDir
	awRealWriteBudget(capture, false)
	dirs, err := skills.MaterializeEmbedded(filepath.Join(state, "embedded"))
	if err != nil {
		t.Fatal(err)
	}
	skillOptions := skills.Options{Cwd: filepath.Join(state, "workspace"), UserDir: filepath.Join(state, "user-skills"), Embedded: dirs}
	if natural != "" {
		dir := filepath.Join(skillOptions.UserDir, "evidence-report")
		if err = os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		guide := "---\nname: evidence-report\ndescription: 核对项目台账、费用与验收证据，整理有来源的中文报告；适合资料整理和人工确认前后的报告生成。\n---\n# 独立证据报告指南\n\n先实际读取显式资料与快捷方式附件，不把任务进度当成验收结果。费用应核对分项与总额，未知信息明确标注。报告保留事实、费用、风险、本人确认意见与资料来源。此合成测试指南编号 GUIDE-NL-731，报告末尾标注指南编号便于核验。\n"
		if err = os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(guide), 0600); err != nil {
			t.Fatal(err)
		}
	}
	skillList, diagnostics := skills.Load(skillOptions)
	if len(diagnostics) > 0 {
		t.Fatalf("isolated embedded Skill load has %d diagnostics", len(diagnostics))
	}
	workflowSkillID := bindingSkillID(t, skillList, "agent-workflows")
	gateTimeout, runTimeout := 90*time.Second, 10*time.Minute
	if natural != "" {
		gateTimeout, runTimeout = 5*time.Minute, 30*time.Minute
	}
	selectedSkills := []string{workflowSkillID}
	if natural != "" {
		for _, sk := range skillList {
			if sk.Name == "evidence-report" {
				selectedSkills = append(selectedSkills, skillIDOf(sk))
			}
		}
		if len(selectedSkills) != 2 {
			t.Fatal("isolated report guide was not discovered")
		}
	}
	writeJSON := func(path string, value any) {
		t.Helper()
		raw, e := json.MarshalIndent(value, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	secret := func() string {
		b := make([]byte, 20)
		if _, e := rand.Read(b); e != nil {
			t.Fatal(e)
		}
		return hex.EncodeToString(b)
	}
	var users usersFile
	accounts := map[string]map[string]string{}
	for _, name := range []string{"admin", "alice"} {
		password, token := "Real-"+secret(), secret()
		hash, e := HashPassword(password)
		if e != nil {
			t.Fatal(e)
		}
		users.Users = append(users.Users, userRecord{Username: name, Hash: hash, Token: token, Admin: name == "admin", Created: time.Now().UTC().Format(time.RFC3339)})
		accounts[name] = map[string]string{"username": name, "password": password, "token": token}
	}
	usersPath, mcpPath := filepath.Join(state, "config", "users.json"), filepath.Join(state, "config", "mcp.json")
	writeJSON(usersPath, users)
	writeJSON(mcpPath, map[string]any{"servers": map[string]any{}})
	cat, err := catalog.Open(filepath.Join(state, "sessions", "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cat.Close() })
	manager, err := NewManager(Config{
		Cwd: filepath.Join(state, "workspace"), SessionDir: filepath.Join(state, "sessions"), Catalog: cat, Model: "k3",
		Skills: skillList, SkillOptions: skillOptions, MaxTurns: 32, Retries: 1, GateTimeout: gateTimeout, RunTimeout: runTimeout, IdleTimeout: time.Hour, MCPClientStore: mcpPath, Followups: false,
		NewClient: func(rc config.Resolved, retry func(llm.RetryInfo)) llm.Client {
			capture.addSecrets(rc)
			actual := llm.New(llm.Options{Compatibility: rc.Compatibility, BaseURL: rc.BaseURL, APIKey: rc.APIKey, Model: rc.Model, MaxTokens: rc.MaxTokens, Headers: rc.Headers, ExtraBody: rc.ExtraBody, Timeout: 6 * time.Minute, MaxRetries: 1, OnRetry: retry})
			return &awRealModelClient{actual: actual, capture: capture, endpoint: rc.BaseURL, provider: rc.Provider, expectedEndpoint: resolved.BaseURL, expectedProvider: resolved.Provider}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Registered after the manager exists so it drains all real streams before
	// final evidence is written. The server cleanup below precedes this cleanup.
	t.Cleanup(func() {
		manager.Close()
		awRealWriteBudget(capture, true)
		if raw, e := capture.sanitizedJSON(capture.Snapshot()); e == nil {
			_ = os.WriteFile(filepath.Join(evidenceDir, "model-trace.json"), raw, 0600)
		}
	})
	devProxy := ""
	if os.Getenv("POGO_AGENT_WORKFLOW_REAL_DEV") == "1" {
		devProxy = viteURL
	}
	server, err := NewServer(manager, ServerOptions{DevProxy: devProxy, Token: accounts["admin"]["token"], UsersFile: usersPath, PrefsStore: filepath.Join(state, "config", "prefs.json"), MCPClientStore: mcpPath, SharesStore: filepath.Join(state, "config", "shares.json"), StartersStore: filepath.Join(state, "config", "starters.json"), PanelStore: filepath.Join(state, "config", "panels.json")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	live := httptest.NewUnstartedServer(server.Handler())
	_ = live.Listener.Close()
	live.Listener = listener
	live.Start()
	t.Cleanup(func() { live.CloseClientConnections(); live.Close() })
	h := &muHarness{t: t, mgr: manager, psrv: server, srv: live, token: accounts["admin"]["token"]}
	body := studioAgentBody("真实模型工作流搭建验收（隔离数据）", "你帮助用户为当前 Agent 搭建和运行内部工作流。先发现实际能力并读取适用的内置指南，保留稳定节点标识，修改后校验。只报告真实工具回执与实际产物。人工确认由用户在页面处理，工具审批仍遵守现有规则。", "ready")
	body["description"] = "独立合成资料与真实模型验收，不连接现有业务系统。"
	body["models"] = []string{"k3"}
	body["skills"] = selectedSkills
	body["mcp_servers"] = []string{}
	body["tools"] = []string{"read", "write", tools.AgentWorkflowReadName, tools.AgentWorkflowEditName, tools.AgentWorkflowEnableName, tools.AgentWorkflowRunName}
	response := h.doAs(accounts["admin"]["token"], http.MethodPost, "/api/manage/agents", studioBody(body))
	if response.StatusCode != http.StatusCreated {
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		t.Fatal("cannot create isolated real-model Agent")
	}
	var a studioAgent
	if err = json.NewDecoder(response.Body).Decode(&a); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if natural != "" {
		owner := identity.LocalScope("alice")
		entry := userShortcut{ID: "report-checklist", Title: "项目报告核对清单", Content: "SHORTCUT-NL-731：报告必须有事实、费用核对、风险、本人确认意见、来源五部分；缺少验收签字时明确尚未验收。", Parts: []inputref.Part{{Type: "text", Text: "SHORTCUT-NL-731：报告必须有事实、费用核对、风险、本人确认意见、来源五部分；缺少验收签字时明确尚未验收。"}}}
		if _, err = manager.replaceUserShortcuts(&owner, a.ID, 0, []userShortcut{entry}); err != nil {
			t.Fatal(err)
		}
	}
	source := h.studioConversationAs(accounts["alice"]["token"], a.ID)
	sources := map[string]string{
		"project.md":    "# 合成项目台账\n\n标记：QA-REAL-731。所有内容为独立合成测试数据。\n项目总预算 12000 元；已使用 4500 元；剩余预算 7500 元。\n共有 18 项任务，已完成 12 项，未完成 6 项。\n请以 costs.md 核对费用，以 acceptance.md 核对验收状态。\n",
		"costs.md":      "# 合成费用明细\n\nQA-REAL-731。硬件费用 1800 元，软件费用 1200 元，服务费用 1500 元，合计 4500 元。\n项目总预算 12000 元，剩余预算 7500 元。所有数字均为合成数据。\n",
		"acceptance.md": "# 合成验收记录\n\nQA-REAL-731。本批资料未提供验收签字，不能假定项目已经验收。\n任务完成 12/18 仅代表任务进度，不等同于正式验收通过。\n报告必须明确缺少签字与尚未验收，不得编造审批或验收结论。\n",
	}
	for name, contents := range sources {
		if err = os.WriteFile(filepath.Join(h.session(source).cwd, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	stop := filepath.Join(output, "stop")
	_ = os.Remove(stop)
	writeJSON(filepath.Join(output, "ready.json"), map[string]any{"url": live.URL, "state_dir": state, "accounts": accounts, "agent_id": a.ID, "source_session_id": source, "source_files": []string{"project.md", "costs.md", "acceptance.md"}, "real_provider": true, "scripted_model": false, "production_data": false, "harness_withheld_tools": []string{"subagent", "subagent_tasks"}, "harness_limit": "no delegation outside shared model budget; product permissions unchanged", "model": map[string]any{"alias": "k3", "model": resolved.Model, "provider": resolved.Provider, "endpoint": resolved.BaseURL, "context_window": resolved.ContextWindow, "max_tokens": resolved.MaxTokens}, "budget_path": filepath.Join(evidenceDir, "model-budget.json"), "evidence_dir": evidenceDir, "stop_file": stop})
	t.Log("isolated real-model fixture ready at " + live.URL + "; private ready.json written; create the private stop file to stop")
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if _, err = os.Stat(stop); err == nil {
			return
		}
	}
}

// Recorder-boundary regressions use a probe only; the opt-in fixture above
// always constructs llm.New against the selected real provider.
type awRealRecorderProbe struct {
	calls    int
	schemas  []llm.ToolSchema
	response llm.Response
}

func (*awRealRecorderProbe) Model() string { return "k3" }
func (p *awRealRecorderProbe) Stream(_ context.Context, _ string, _ []llm.Message, schemas []llm.ToolSchema, _ func(llm.Delta)) (llm.Response, error) {
	p.calls++
	p.schemas = append([]llm.ToolSchema(nil), schemas...)
	return p.response, nil
}
func TestAgentWorkflowRealRecorderDelegationBoundary(t *testing.T) {
	probeResponse := func(name string) llm.Response {
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, ID: "probe-call", Name: name, Input: json.RawMessage(`{"action":"capabilities"}`)}}}, StopReason: llm.StopToolUse, Usage: llm.Usage{Input: 25, Output: 5}}
	}
	setup := func(probe *awRealRecorderProbe) *awRealModelClient {
		return &awRealModelClient{actual: probe, capture: &sentinelLiveCapture{output: t.TempDir()}, endpoint: "https://selected.example/v1", expectedEndpoint: "https://selected.example/v1", provider: "kimi", expectedProvider: "kimi"}
	}
	t.Run("only delegation schemas are withheld and actual response is unchanged", func(t *testing.T) {
		want := probeResponse(tools.AgentWorkflowReadName)
		probe := &awRealRecorderProbe{response: want}
		client := setup(probe)
		got, err := client.Stream(context.Background(), "system", nil, []llm.ToolSchema{{Name: "subagent"}, {Name: "subagent_tasks"}, {Name: "read"}, {Name: "write"}, {Name: tools.AgentWorkflowReadName}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		names := []string{}
		for _, schema := range probe.schemas {
			names = append(names, schema.Name)
		}
		if strings.Join(names, ",") != "read,write,"+tools.AgentWorkflowReadName {
			t.Fatalf("wrong provider schema set: %v", names)
		}
		gotRaw, _ := json.Marshal(got)
		wantRaw, _ := json.Marshal(want)
		if string(gotRaw) != string(wantRaw) {
			t.Fatal("recorder changed the actual provider response")
		}
		raw, err := os.ReadFile(filepath.Join(client.capture.output, "model-call-01.json"))
		if err != nil {
			t.Fatal(err)
		}
		var evidence struct {
			Withheld []string    `json:"harness_withheld_schemas"`
			Response llm.Message `json:"response"`
		}
		if json.Unmarshal(raw, &evidence) != nil || strings.Join(evidence.Withheld, ",") != "subagent,subagent_tasks" || evidence.Response.Content[0].Name != tools.AgentWorkflowReadName {
			t.Fatal("missing explicit delegation boundary or actual provider evidence")
		}
	})
	t.Run("an unsolicited delegation response cannot reach product execution", func(t *testing.T) {
		probe := &awRealRecorderProbe{response: probeResponse("subagent")}
		client := setup(probe)
		got, err := client.Stream(context.Background(), "system", nil, []llm.ToolSchema{{Name: "read"}}, nil)
		if err == nil || len(got.Message.Content) != 0 || probe.calls != 1 {
			t.Fatal("unsolicited delegation was not blocked before execution")
		}
		captured := client.capture.Snapshot()
		if len(captured) != 1 || captured[0].Response.Content[0].Name != "subagent" || captured[0].Error == "" {
			t.Fatal("blocked actual response was not retained in sanitized evidence")
		}
	})
	t.Run("unexpected write grants still fail before a provider call", func(t *testing.T) {
		probe := &awRealRecorderProbe{response: probeResponse("read")}
		client := setup(probe)
		if _, err := client.Stream(context.Background(), "system", nil, []llm.ToolSchema{{Name: "bash"}}, nil); err == nil || probe.calls != 0 || client.capture.Count() != 0 {
			t.Fatal("unexpected write grant reached provider")
		}
	})
}
