package web

// Explicitly opted-in browser acceptance fixture. All accounts, files, queue
// rows and sessions live in a fresh temporary directory. The model is scripted
// offline; the read/write tools, approvals, conversations and Run engine are real.
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
	"github.com/yosukeno/pogo/mcpobserve"
)

func TestAgentWorkflowObservableUIFixture(t *testing.T) {
	output := os.Getenv("POGO_AGENT_WORKFLOW_OBSERVABLE_FIXTURE")
	if output == "" {
		t.Skip("opt-in isolated workflow activity browser fixture")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Clean(filepath.Join(wd, "..", "..", ".ci-artifacts", "agent-workflow-observable-20261029", "private"))
	if filepath.Clean(output) != want {
		t.Fatal("fixture output must be this checkout's isolated observable directory")
	}
	// Refuse a busy port before touching the ready file. Never stop or reuse
	// another fixture, and never read a deployment's configuration or volume.
	listener, err := net.Listen("tcp", "127.0.0.1:19841")
	if err != nil {
		t.Fatal("isolated fixture port 19841 is unavailable")
	}
	t.Cleanup(func() { listener.Close() })
	if err = os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(output, 0700); err != nil {
		t.Fatal(err)
	}
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
	for _, dir := range []string{"workspace", "sessions", "config", "controls"} {
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
		password, token := "Observable-"+secret(), secret()
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
	t.Cleanup(func() { cat.Close() })
	controls := filepath.Join(state, "controls")
	var manager *Manager
	manager, err = NewManager(Config{
		Cwd: filepath.Join(state, "workspace"), SessionDir: filepath.Join(state, "sessions"), Catalog: cat,
		Model: "k3", MaxTurns: 12, GateTimeout: 5 * time.Minute, RunTimeout: 40 * time.Minute, IdleTimeout: time.Hour,
		MCPClientStore: mcpPath,
		NewClient: func(config.Resolved, func(llm.RetryInfo)) llm.Client {
			return &awObservableFixtureClient{controls: controls, manager: func() *Manager { return manager }}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	devProxy := ""
	if os.Getenv("POGO_AGENT_WORKFLOW_OBSERVABLE_DEV") == "1" {
		devProxy = "http://127.0.0.1:19842"
	}
	server, err := NewServer(manager, ServerOptions{
		DevProxy: devProxy, Token: accounts["admin"]["token"], UsersFile: usersPath,
		PrefsStore: filepath.Join(state, "config", "prefs.json"), MCPClientStore: mcpPath,
		SharesStore: filepath.Join(state, "config", "shares.json"), StartersStore: filepath.Join(state, "config", "starters.json"), PanelStore: filepath.Join(state, "config", "panels.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	live := httptest.NewUnstartedServer(server.Handler())
	live.Listener.Close()
	live.Listener = listener
	live.Start()
	t.Cleanup(live.Close)
	h := &muHarness{t: t, mgr: manager, psrv: server, srv: live, token: accounts["admin"]["token"]}
	a := awTestAgent(t, h, "节点执行过程验收助手（独立数据）", "write")
	source := h.studioConversationAs(accounts["alice"]["token"], a.ID)
	if err = os.WriteFile(filepath.Join(h.session(source).cwd, "source.md"), []byte("# 独立合成验收资料\n\nQA-OBS-731：资料整理项目，预算1000元，预计3天完成。仅用于本地观察性验收，不含业务数据。\n"), 0600); err != nil {
		t.Fatal(err)
	}
	d := awTestValue[af.Definition](t, awTestCall(t, h, accounts["alice"]["token"], a.ID, "edit", map[string]any{
		"action": "create", "request_id": "observable-fixture-create", "name": "并行资料整理与执行过程验收", "description": "使用合成资料和离线脚本模型，验证多轮过程、真实工具审批、本人确认和报告。",
		"inputs": []af.Input{{Key: "topic", Label: "报告主题", Required: true}},
		"steps": []af.Step{
			{ID: "parallel", Kind: "parallel", Title: "并行处理资料"},
			{ID: "research", Kind: "agent", Title: "事实整理", Task: "阅读实际资料，整理 {{ inputs.topic }} 的事实和出处。", OutputFile: "research.md"},
			{ID: "sources", Kind: "agent", Title: "来源核对", Task: "阅读实际资料，独立核对 {{ inputs.topic }} 的来源。", OutputFile: "sources.md"},
			{ID: "merge", Kind: "join", Title: "汇总并行资料"},
			{ID: "confirm", Kind: "confirm", Title: "本人确认", Prompt: "请本人核对事实与来源，并给出报告意见：{{ steps.merge.summary }}"},
			{ID: "report", Kind: "agent", Title: "生成报告", Task: "依据 {{ steps.merge.summary }} 与本人意见生成报告，保留来源。", OutputFile: "report.md"},
		},
		"graph": af.Graph{Version: 1, Edges: []af.Edge{
			{Source: af.GraphInput, Target: "parallel"}, {Source: "parallel", Target: "research"}, {Source: "parallel", Target: "sources"},
			{Source: "research", Target: "merge"}, {Source: "sources", Target: "merge"}, {Source: "merge", Target: "confirm"}, {Source: "confirm", Target: "report"}, {Source: "report", Target: af.GraphOutput},
		}},
	}, 200), "workflow")
	run := awTestValue[af.Run](t, awTestCall(t, h, accounts["alice"]["token"], a.ID, "run", map[string]any{
		"action": "trial", "workflow_id": d.ID, "request_id": "observable-fixture-trial", "expected_revision": d.Revision, "expected_hash": d.DefinitionHash,
		"source_session_id": source, "inputs": map[string]string{"topic": "合成资料项目"}, "files": []string{"source.md"},
	}, 200), "run")
	stop := filepath.Join(output, "stop")
	os.Remove(stop)
	writeJSON(filepath.Join(output, "ready.json"), map[string]any{
		"url": live.URL, "vite_url": "http://127.0.0.1:19842", "state_dir": state, "accounts": accounts,
		"agent_id": a.ID, "workflow_id": d.ID, "graph_workflow_id": d.ID, "run_id": run.ID, "source_session_id": source, "source_file": "source.md",
		"scripted_model": true, "production_data": false, "control_dir": controls, "stop_file": stop,
		"release_all_file":       filepath.Join(controls, "all.release"),
		"node_release_files":     map[string]string{"research": filepath.Join(controls, "research-all.release"), "sources": filepath.Join(controls, "sources-all.release"), "report": filepath.Join(controls, "report-all.release")},
		"stage_release_pattern":  "<control_dir>/<research|sources|report>-turn-<1|2|3>-<thinking|answer>.release",
		"waiting_record_pattern": "<control_dir>/<node>-turn-<turn>-<stage>.waiting.json",
		"research_write_policy":  "research第2轮真实write前仅将该执行Session设为strict，必须在页面真实审批；sources保持standard。",
		"initial_state":          "试跑已创建，research和sources将分别停在第1轮thinking；仅显式创建对应release文件后前进。",
	})
	t.Log("isolated observable fixture ready at http://127.0.0.1:19841; credentials only in private ready.json")
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if _, err = os.Stat(stop); err == nil {
			return
		}
	}
}

type awObservableFixtureClient struct {
	controls string
	manager  func() *Manager
	phase    int
	node     string
}

func (c *awObservableFixtureClient) Model() string { return "k3" }

func (c *awObservableFixtureClient) wait(ctx context.Context, stage string) error {
	prefix := fmt.Sprintf("%s-turn-%d-%s", c.node, c.phase, stage)
	release := filepath.Join(c.controls, prefix+".release")
	raw, _ := json.Marshal(map[string]any{"node": c.node, "turn": c.phase, "stage": stage, "release_file": release, "updated_at": time.Now().UnixMilli()})
	if err := os.WriteFile(filepath.Join(c.controls, prefix+".waiting.json"), raw, 0600); err != nil {
		return err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		for _, path := range []string{release, filepath.Join(c.controls, c.node+"-all.release"), filepath.Join(c.controls, "all.release")} {
			if _, err := os.Stat(path); err == nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *awObservableFixtureClient) progressive(ctx context.Context, emit func(llm.Delta), kind llm.DeltaKind, chunks []string) error {
	for _, chunk := range chunks {
		if emit != nil {
			emit(llm.Delta{Kind: kind, Text: chunk})
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	return nil
}

func (c *awObservableFixtureClient) Stream(ctx context.Context, _ string, history []llm.Message, _ []llm.ToolSchema, emit func(llm.Delta)) (llm.Response, error) {
	user := ""
	for _, message := range history {
		if message.Role == llm.RoleUser {
			for _, block := range message.Content {
				if block.Type == llm.BlockText {
					user = block.Text
				}
			}
		}
	}
	match := awGraphTestStep.FindStringSubmatch(user)
	if len(match) != 2 || match[1] != "research" && match[1] != "sources" && match[1] != "report" {
		return textTurn("这是独立观察性验收的离线脚本模型。请在内部工作流中查看已启动的试跑。"), nil
	}
	c.node = match[1]
	c.phase++
	if c.phase > 3 {
		return llm.Response{}, fmt.Errorf("unexpected fixture model turn %d", c.phase)
	}
	output := awCanvasOutput.FindStringSubmatch(user)
	if len(output) != 2 {
		return llm.Response{}, fmt.Errorf("observable fixture output not declared")
	}
	thinking := fmt.Sprintf("本地脚本验收：%s节点第%d轮。", c.node, c.phase)
	if err := c.progressive(ctx, emit, llm.DeltaThinking, []string{thinking, "先核对真实工具结果，", "再执行当前步骤。"}); err != nil {
		return llm.Response{}, err
	}
	if err := c.wait(ctx, "thinking"); err != nil {
		return llm.Response{}, err
	}
	prose := fmt.Sprintf("%s节点第%d轮正在继续。", c.node, c.phase)
	if err := c.progressive(ctx, emit, llm.DeltaText, []string{prose, "本轮过程和工具结果可在执行抽屉查看。"}); err != nil {
		return llm.Response{}, err
	}
	if err := c.wait(ctx, "answer"); err != nil {
		return llm.Response{}, err
	}
	blocks := []llm.Block{{Type: llm.BlockThinking, Text: thinking + "先核对真实工具结果，再执行当前步骤。"}, {Type: llm.BlockText, Text: prose + "本轮过程和工具结果可在执行抽屉查看。"}}
	if c.phase == 3 {
		result := textTurn("已实际写入 " + output[1] + "，可在本节点产物中查看。")
		result.Message.Content = append(blocks, result.Message.Content...)
		return result, nil
	}
	name, path := "read", "inputs/01-source.md"
	args := map[string]string{"path": path}
	if c.phase == 1 && c.node == "report" {
		args["path"] = "research.md"
	}
	if c.phase == 2 {
		actualRead := false
		for _, message := range history {
			for _, block := range message.Content {
				actualRead = actualRead || block.Type == llm.BlockToolResult && !block.IsError && strings.Contains(block.Text, "QA-OBS-731")
			}
		}
		if !actualRead {
			return llm.Response{}, fmt.Errorf("observable fixture did not actually read its synthetic source")
		}
		name = "write"
		args = map[string]string{"path": output[1], "content": "# 独立观察性验收产物\n\nQA-OBS-731：资料整理项目，预算1000元，预计3天完成。\n\n来源：实际read工具读取的独立合成资料。\n"}
		if c.node == "report" {
			args["content"] += "\n已在同一Run完成本人确认后生成此报告。\n"
		}
		if c.node == "research" {
			// Fixture-only explicit policy: exercise the ordinary real write
			// gate. The production workflow's standard policy is unchanged.
			meta := mcpobserve.MetadataFrom(ctx)
			worker, err := c.manager().Get(meta.SessionID)
			if err != nil || worker == nil {
				return llm.Response{}, fmt.Errorf("fixture execution session unavailable")
			}
			worker.SetPolicy(ModeStrict, 0)
		}
	}
	raw, _ := json.Marshal(args)
	result := toolTurn(name, string(raw))
	call := &result.Message.Content[0]
	call.ID = fmt.Sprintf("observable_%s_%d", c.node, c.phase)
	if emit != nil {
		emit(llm.Delta{Kind: llm.DeltaToolCallStart, ToolID: call.ID, ToolName: name})
		emit(llm.Delta{Kind: llm.DeltaToolCallArgs, ToolID: call.ID, Text: string(raw)})
	}
	result.Message.Content = append(blocks, result.Message.Content...)
	return result, nil
}
