package web

// Opt-in browser fixture: real authentication, Agent tools, queue, workspace
// writes and SQLite, with an explicitly scripted offline model. It is never
// started by the normal test suite and never opens production state.
import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	af "github.com/yosukeno/pogo/agentworkflow"
	"github.com/yosukeno/pogo/catalog"
	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
)

func TestAgentWorkflowCanvasUIFixture(t *testing.T) {
	output := os.Getenv("POGO_AGENT_WORKFLOW_CANVAS_FIXTURE")
	if output == "" {
		t.Skip("opt-in isolated internal workflow canvas browser fixture")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	wantOutput := filepath.Clean(filepath.Join(wd, "..", "..", ".ci-artifacts", "agent-workflow-canvas-20261028", "private"))
	if filepath.Clean(output) != wantOutput {
		t.Fatal("fixture output must be this checkout's .ci-artifacts/agent-workflow-canvas-20261028/private directory")
	}
	if err = os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(output, 0700); err != nil {
		t.Fatal(err)
	}
	// Fail before creating runtime state if another process already owns the
	// requested port. Never attach to or stop that process.
	listener, err := net.Listen("tcp", "127.0.0.1:19831")
	if err != nil {
		t.Fatal("isolated fixture port 19831 is unavailable")
	}
	t.Cleanup(func() { _ = listener.Close() })
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	scheduleEnv(t)
	for _, name := range []string{"POGO_OFFICE_BROWSER_URL", "POGO_OFFICE_SERVICE_URL", "POGO_OFFICE_PUBLIC_URL", "POGO_OFFICE_FETCH_URL", "POGO_OFFICE_JWT_SECRET_FILE", "POGO_READ_ROOTS", "POGO_PROVIDER_CONFIG", "POGO_MODEL_CONFIG"} {
		t.Setenv(name, "")
	}
	t.Setenv("KIMI_API_KEY", "fixture-only-no-network")
	installTestCatalog(t)
	for _, dir := range []string{"workspace", "sessions", "config"} {
		if err = os.MkdirAll(filepath.Join(state, dir), 0700); err != nil {
			t.Fatal(err)
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
	for _, name := range []string{"admin", "alice", "bob"} {
		password, token := "Canvas-"+secret(), secret()
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
		Cwd: filepath.Join(state, "workspace"), SessionDir: filepath.Join(state, "sessions"), Catalog: cat,
		Model: "k3", MaxTurns: 12, GateTimeout: 30 * time.Second, RunTimeout: 3 * time.Minute, IdleTimeout: time.Hour,
		MCPClientStore: mcpPath,
		NewClient:      func(config.Resolved, func(llm.RetryInfo)) llm.Client { return &awCanvasFixtureClient{} },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	devProxy := ""
	if os.Getenv("POGO_AGENT_WORKFLOW_CANVAS_DEV") == "1" {
		devProxy = "http://127.0.0.1:19832"
	}
	server, err := NewServer(manager, ServerOptions{
		DevProxy: devProxy,
		Token:    accounts["admin"]["token"], UsersFile: usersPath,
		PrefsStore: filepath.Join(state, "config", "prefs.json"), MCPClientStore: mcpPath,
		SharesStore: filepath.Join(state, "config", "shares.json"), StartersStore: filepath.Join(state, "config", "starters.json"), PanelStore: filepath.Join(state, "config", "panels.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	live := httptest.NewUnstartedServer(server.Handler())
	_ = live.Listener.Close()
	live.Listener = listener
	live.Start()
	t.Cleanup(live.Close)
	h := &muHarness{t: t, mgr: manager, psrv: server, srv: live, token: accounts["admin"]["token"]}
	a := awTestAgent(t, h, "画布验收助手（隔离数据）", "write")
	source := h.studioConversationAs(accounts["alice"]["token"], a.ID)
	if err = os.WriteFile(filepath.Join(h.session(source).cwd, "source.md"), []byte("# 独立验收资料\n\nQA-CANVAS-731：资料整理项目，预算1000元，预计3天完成。所有信息均为合成测试数据。\n"), 0600); err != nil {
		t.Fatal(err)
	}
	d := awTestValue[af.Definition](t, awTestCall(t, h, accounts["alice"]["token"], a.ID, "edit", map[string]any{
		"action": "create", "request_id": "canvas-fixture-create", "name": "资料整理与本人确认报告", "description": "独立页面验收数据，使用脚本模型；不代表真实模型能力验收。",
		"inputs": []af.Input{{Key: "topic", Label: "报告主题", Required: true}},
		"steps": []af.Step{
			{ID: "collect", Kind: "agent", Title: "整理资料", Task: "阅读所选资料，围绕 {{ inputs.topic }} 整理要点和出处。", OutputFile: "notes.md"},
			{ID: "review", Kind: "confirm", Title: "本人确认", Prompt: "请核对要点并给出意见：{{ steps.collect.summary }}"},
			{ID: "report", Kind: "agent", Title: "生成报告", Task: "根据 {{ steps.collect.artifact }} 和本人确认意见生成报告，保留来源。", OutputFile: "report.md"},
		},
	}, 200), "workflow")
	stop := filepath.Join(output, "stop")
	_ = os.Remove(stop)
	writeJSON(filepath.Join(output, "ready.json"), map[string]any{
		"url": live.URL, "state_dir": state, "accounts": accounts,
		"agent_id": a.ID, "workflow_id": d.ID, "source_session_id": source, "source_file": "source.md",
		"scripted_model": true, "production_data": false,
		"chat_edit_example": "把工作流 " + d.ID + " 的整理节点标题改为「整理资料与来源」",
		"stop_file":         stop,
	})
	t.Log("isolated canvas fixture ready at http://127.0.0.1:19831; private ready.json written; create the private stop file to stop")
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if _, err := os.Stat(stop); err == nil {
			return
		}
	}
}

// The fixture can run arbitrary sequential agent nodes that declare an output
// file. The seeded flow reads a frozen synthetic material through the real
// read tool, then writes notes/report through the real write tool.
type awCanvasFixtureClient struct {
	mu     sync.Mutex
	user   string
	phase  int
	serial int
	// Opt-in fixture gate keeps the reply running after a real edit receipt,
	// allowing the UI to prove it updates before the conversation finishes.
	editReleaseFile string
}

func (c *awCanvasFixtureClient) Model() string { return "k3" }

var awCanvasOutput = regexp.MustCompile(`请将本步骤实际产物写入工作目录内的 ([^\n]+)，完成后`)
var awCanvasChatEdit = regexp.MustCompile(`把工作流 ([A-Za-z0-9_-]+) 的整理节点标题改为「([^」]+)」`)

func (c *awCanvasFixtureClient) Stream(ctx context.Context, _ string, history []llm.Message, _ []llm.ToolSchema, _ func(llm.Delta)) (llm.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	user := ""
	for _, m := range history {
		if m.Role == llm.RoleUser {
			var text strings.Builder
			for _, b := range m.Content {
				if b.Type == llm.BlockText {
					text.WriteString(b.Text)
				}
			}
			if text.Len() > 0 {
				user = text.String()
			}
		}
	}
	if c.user != user {
		c.user, c.phase = user, 0
	}
	c.phase++
	c.serial++
	tool := func(name string, args any) llm.Response {
		raw, _ := json.Marshal(args)
		r := toolTurn(name, string(raw))
		r.Message.Content[0].ID = fmt.Sprintf("canvas_fixture_%d", c.serial)
		return r
	}
	if edit := awCanvasChatEdit.FindStringSubmatch(user); len(edit) == 3 {
		if c.phase == 1 {
			return tool("agent_workflow_read", map[string]any{"action": "get", "workflow_id": edit[1]}), nil
		}
		if c.phase == 2 {
			for i := len(history) - 1; i >= 0; i-- {
				for _, b := range history[i].Content {
					if b.Type != llm.BlockToolResult {
						continue
					}
					var result struct {
						Workflow af.Definition `json:"workflow"`
					}
					// The actual host appends a saved-result recovery hint after
					// the JSON. Decode its first value, as a model would read it.
					if json.NewDecoder(strings.NewReader(b.Text)).Decode(&result) == nil && result.Workflow.ID == edit[1] {
						return tool("agent_workflow_edit", map[string]any{"action": "patch", "workflow_id": edit[1], "expected_revision": result.Workflow.Revision, "request_id": fmt.Sprintf("canvas-chat-%d", time.Now().UnixNano()), "operations": []any{map[string]any{"op": "update_step", "step_id": "collect", "patch": map[string]string{"title": edit[2]}}}}), nil
					}
				}
			}
			return llm.Response{}, fmt.Errorf("isolated fixture could not read the requested workflow")
		}
		if c.editReleaseFile != "" {
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				if _, err := os.Stat(c.editReleaseFile); err == nil {
					break
				}
				select {
				case <-ctx.Done():
					return llm.Response{}, ctx.Err()
				case <-ticker.C:
				}
			}
		}
		return textTurn("已通过当前 Agent 的内部工作流工具更新整理节点标题。此回复来自本地脚本模型。"), nil
	}
	if !strings.Contains(user, "你正在执行当前 Agent 的内部流程") {
		return textTurn("这是隔离画布验收的脚本模型。可用页面创建、编辑和试跑流程；预置流程支持读取资料、本人确认和生成报告。"), nil
	}
	select {
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	case <-time.After(250 * time.Millisecond):
	}
	output := awCanvasOutput.FindStringSubmatch(user)
	if len(output) != 2 {
		return textTurn("本步骤的独立验收任务已完成；脚本模型没有声明文件产物。"), nil
	}
	hasSource := strings.Contains(user, "inputs/01-source.md")
	if hasSource && c.phase == 1 {
		return tool("read", map[string]string{"path": "inputs/01-source.md"}), nil
	}
	writePhase := 1
	if hasSource {
		writePhase = 2
	}
	if c.phase == writePhase {
		if hasSource {
			readSource := false
			for _, m := range history {
				for _, b := range m.Content {
					readSource = readSource || (b.Type == llm.BlockToolResult && !b.IsError && strings.Contains(b.Text, "QA-CANVAS-731"))
				}
			}
			if !readSource {
				return llm.Response{}, fmt.Errorf("isolated fixture did not actually read its synthetic source")
			}
		}
		content := "# 隔离验收产物\n\nQA-CANVAS-731：资料整理项目，预算1000元，预计3天完成。\n\n来源：inputs/01-source.md（独立合成测试资料）。\n"
		if !hasSource {
			content = "# 隔离验收产物\n\n这是页面验收脚本生成的内容。本次未选择资料，未引用外部事实。\n"
		}
		if strings.Contains(user, `"user_feedback"`) {
			content += "\n已收到本人确认后继续生成。\n"
		}
		return tool("write", map[string]string{"path": output[1], "content": content}), nil
	}
	return textTurn("已实际写入 " + output[1] + "。这是独立合成资料的脚本模型验收产物。"), nil
}
