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
	"strings"
	"testing"
	"time"

	af "github.com/yosukeno/pogo/agentworkflow"
	"github.com/yosukeno/pogo/catalog"
	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
)

func TestAgentWorkflowGraphUIFixture(t *testing.T) {
	output := os.Getenv("POGO_AGENT_WORKFLOW_GRAPH_FIXTURE")
	if output == "" {
		t.Skip("opt-in isolated internal workflow canvas browser fixture")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	wantOutput := filepath.Clean(filepath.Join(wd, "..", "..", ".ci-artifacts", "agent-workflow-dag-20261028", "private"))
	liveOutput := filepath.Clean(filepath.Join(wd, "..", "..", ".ci-artifacts", "agent-workflow-split-live-20261029", "private"))
	if filepath.Clean(output) != wantOutput && filepath.Clean(output) != liveOutput {
		t.Fatal("fixture output must be one of this checkout's isolated workflow fixture directories")
	}
	if err = os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(output, 0700); err != nil {
		t.Fatal(err)
	}
	// Fail before creating runtime state if another process already owns the
	// requested port. Never attach to or stop that process.
	listener, err := net.Listen("tcp", "127.0.0.1:19841")
	if err != nil {
		t.Fatal("isolated fixture port 19841 is unavailable")
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
	editReleaseFile := ""
	if os.Getenv("POGO_AGENT_WORKFLOW_EDIT_HOLD") == "1" {
		editReleaseFile = filepath.Join(output, "finish-chat-edit")
	}
	manager, err := NewManager(Config{
		Cwd: filepath.Join(state, "workspace"), SessionDir: filepath.Join(state, "sessions"), Catalog: cat,
		Model: "k3", MaxTurns: 12, GateTimeout: 30 * time.Second, RunTimeout: 3 * time.Minute, IdleTimeout: time.Hour,
		MCPClientStore: mcpPath,
		NewClient: func(config.Resolved, func(llm.RetryInfo)) llm.Client {
			return &awGraphFixtureClient{awCanvasFixtureClient: awCanvasFixtureClient{editReleaseFile: editReleaseFile}}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	devProxy := ""
	if os.Getenv("POGO_AGENT_WORKFLOW_GRAPH_DEV") == "1" {
		devProxy = "http://127.0.0.1:19842"
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
	a := awTestAgent(t, h, "分支并行验收助手（隔离数据）", "write")
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
	example := awGraphExampleDefinition()
	graph := awTestValue[af.Definition](t, awTestCall(t, h, accounts["alice"]["token"], a.ID, "edit", map[string]any{
		"action": "create", "request_id": "graph-ui-seed", "name": example.Name, "description": example.Description,
		"inputs": example.Inputs, "steps": example.Steps, "graph": example.Graph,
	}, 200), "workflow")

	stop := filepath.Join(output, "stop")
	_ = os.Remove(stop)
	writeJSON(filepath.Join(output, "ready.json"), map[string]any{
		"url": live.URL, "state_dir": state, "accounts": accounts,
		"agent_id": a.ID, "workflow_id": d.ID, "source_session_id": source, "source_file": "source.md",
		"scripted_model": true, "production_data": false, "graph_workflow_id": graph.ID,
		"chat_create_example":    "创建一个条件并行资料报告流程",
		"chat_edit_example":      "把工作流 " + d.ID + " 的整理节点标题改为「整理资料与来源」",
		"stop_file":              stop,
		"chat_edit_release_file": editReleaseFile,
	})
	t.Log("isolated canvas fixture ready at http://127.0.0.1:19841; private ready.json written; create the private stop file to stop")
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if _, err := os.Stat(stop); err == nil {
			return
		}
	}
}

// The graph fixture uses the same real read/write behavior as the sequential
// fixture. Only the explicitly named chat request has a scripted graph authoring
// response. This verifies host/tool/UI integration, not external model quality.
type awGraphFixtureClient struct {
	awCanvasFixtureClient
	authoringPhase int
}

func (c *awGraphFixtureClient) Stream(ctx context.Context, system string, history []llm.Message, schemas []llm.ToolSchema, emit func(llm.Delta)) (llm.Response, error) {
	user := ""
	for _, m := range history {
		if m.Role == llm.RoleUser {
			for _, b := range m.Content {
				if b.Type == llm.BlockText && b.Text != "" {
					user = b.Text
				}
			}
		}
	}
	if !strings.Contains(user, "创建一个条件并行资料报告流程") {
		return c.awCanvasFixtureClient.Stream(ctx, system, history, schemas, emit)
	}
	c.authoringPhase++
	if c.authoringPhase == 1 {
		example := awGraphExampleDefinition()
		args, _ := json.Marshal(map[string]any{"action": "create", "request_id": fmt.Sprintf("graph-conversation-%d", time.Now().UnixNano()), "name": "对话创建的条件并行报告", "description": example.Description, "inputs": example.Inputs, "steps": example.Steps, "graph": example.Graph})
		result := toolTurn("agent_workflow_edit", string(args))
		result.Message.Content[0].ID = "graph_authoring_create"
		return result, nil
	}
	for i := len(history) - 1; i >= 0; i-- {
		for _, b := range history[i].Content {
			if b.Type != llm.BlockToolResult {
				continue
			}
			var result struct {
				OK       bool          `json:"ok"`
				Workflow af.Definition `json:"workflow"`
			}
			if json.NewDecoder(strings.NewReader(b.Text)).Decode(&result) == nil && result.OK && result.Workflow.Graph != nil {
				return textTurn("已通过当前 Agent 的内部工作流工具创建条件与并行流程。草稿已保存，可在工作流画布查看。此回复来自独立测试脚本模型。"), nil
			}
		}
	}
	return textTurn("创建没有得到成功回执，请查看工具结果。此回复来自独立测试脚本模型。"), nil
}
