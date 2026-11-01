package web

// Opt-in local browser acceptance fixture. HTTP, authentication, workflow
// execution and SQLite are real; the model is an explicitly scripted local
// fake with no network, tools or production data access.
import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yosukeno/pogo/catalog"
	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/workflow"
)

func TestWorkflowUIFixtureServer(t *testing.T) {
	output := os.Getenv("POGO_WORKFLOW_UI_FIXTURE")
	if output == "" {
		t.Skip("opt-in isolated workflow browser acceptance fixture")
	}
	dev := os.Getenv("POGO_WORKFLOW_UI_DEV")
	u, err := url.Parse(dev)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
		t.Fatal("fixture requires a loopback Vite server")
	}
	if !filepath.IsAbs(output) {
		t.Fatal("fixture output must be an absolute temporary directory")
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	output, err = filepath.EvalSymlinks(output)
	if err != nil || !strings.HasPrefix(output, "/private/tmp/pogo-workflow-ui.") {
		t.Fatal("fixture output must be a dedicated /private/tmp/pogo-workflow-ui.* directory")
	}
	state := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(state); err != nil || resolved != state {
		t.Fatal("run fixture with TMPDIR=/private/tmp to avoid aliased storage paths")
	}
	scheduleDir := scheduleEnv(t)
	for _, name := range []string{"POGO_OFFICE_BROWSER_URL", "POGO_OFFICE_SERVICE_URL", "POGO_OFFICE_PUBLIC_URL", "POGO_OFFICE_FETCH_URL", "POGO_OFFICE_JWT_SECRET_FILE", "POGO_READ_ROOTS", "POGO_PROVIDER_CONFIG", "POGO_MODEL_CONFIG"} {
		t.Setenv(name, "")
	}
	t.Setenv("KIMI_API_KEY", "fixture-only-no-network")
	installTestCatalog(t)
	for _, dir := range []string{"workspace", "sessions", "config"} {
		if err := os.MkdirAll(filepath.Join(state, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	usersPath := filepath.Join(state, "config", "users.json")
	accounts := []struct {
		Name, Password, Token string
		Admin                 bool
	}{
		{"admin", "PogoUiAdmin2026!", "workflow-ui-admin-fixture-token", true},
		{"alice", "PogoUiAlice2026!", aliceToken, false},
		{"bob", "PogoUiBob2026!", bobToken, false},
	}
	var users usersFile
	publicAccounts := map[string]any{}
	for _, account := range accounts {
		hash, err := HashPassword(account.Password)
		if err != nil {
			t.Fatal(err)
		}
		users.Users = append(users.Users, userRecord{Username: account.Name, Hash: hash, Token: account.Token, Admin: account.Admin, Created: time.Now().UTC().Format(time.RFC3339)})
		publicAccounts[account.Name] = map[string]any{"username": account.Name, "password": account.Password, "token": account.Token}
	}
	writeJSONFile := func(path string, data any) {
		t.Helper()
		raw, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeJSONFile(usersPath, users)
	mcpPath := filepath.Join(state, "config", "mcp.json")
	writeJSONFile(mcpPath, map[string]any{"servers": map[string]any{}})
	cat, err := catalog.Open(filepath.Join(state, "sessions", "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cat.Close() })
	var modelCalls, failures atomic.Int32
	stream := func(ctx context.Context, _, user string) (llm.Response, error) {
		modelCalls.Add(1)
		if strings.Contains(user, "UI_FIXTURE_SLOW") {
			select {
			case <-ctx.Done():
				return llm.Response{}, ctx.Err()
			case <-time.After(60 * time.Second):
			}
		}
		if strings.Contains(user, "UI_FIXTURE_FAIL_ONCE") && failures.Add(1) == 1 {
			return llm.Response{}, fmt.Errorf("隔离验收：首次执行模拟失败，请使用重试验证新运行记录")
		}
		return textTurn("隔离页面验收已完成。这是本地脚本模型的响应，未调用任何外部模型、工具或业务系统。"), nil
	}
	var authoringHarness *muHarness
	cfg := Config{Cwd: filepath.Join(state, "workspace"), SessionDir: filepath.Join(state, "sessions"), Catalog: cat,
		Model: "k3", MaxTurns: 5, GateTimeout: 5 * time.Second, RunTimeout: 2 * time.Minute, IdleTimeout: time.Hour,
		MCPClientStore: mcpPath,
		NewClient: func(rc config.Resolved, _ func(llm.RetryInfo)) llm.Client {
			if os.Getenv("POGO_WORKFLOW_UI_AUTHORING") == "1" {
				return &workflowAuthoringFixtureClient{normal: &wfNodeClient{model: rc.Model, stream: stream}, builder: &authoringLoopClient{t: t, h: &authoringHarness}}
			}
			return &wfNodeClient{model: rc.Model, stream: stream}
		},
	}
	if os.Getenv("POGO_WORKFLOW_UI_AUTHORING") == "1" {
		cfg.MaxTurns = 16
	}
	opts := ServerOptions{Token: accounts[0].Token, UsersFile: usersPath, DevProxy: dev,
		PrefsStore: filepath.Join(state, "config", "prefs.json"), MCPClientStore: mcpPath,
		SharesStore: filepath.Join(state, "config", "shares.json"), StartersStore: filepath.Join(state, "config", "starters.json"), PanelStore: filepath.Join(state, "config", "panels.json")}
	var manager *Manager
	var server *Server
	var handlerMu sync.RWMutex
	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "isolated fixture restarting", http.StatusServiceUnavailable)
	})
	open := func() error {
		m, err := NewManager(cfg)
		if err != nil {
			return err
		}
		s, err := NewServer(m, opts)
		if err != nil {
			m.Close()
			return err
		}
		manager, server = m, s
		authoringHarness = &muHarness{t: t, mgr: m, psrv: s}
		handlerMu.Lock()
		handler = s.Handler()
		handlerMu.Unlock()
		return nil
	}
	if err := open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Close(); server.Close() })
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerMu.RLock()
		h := handler
		handlerMu.RUnlock()
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(live.Close)
	agent, err := manager.studio.put("", studioInput{Name: "隔离验收助手", Instructions: "仅用于本地页面验收，按当前环节返回简短说明。", Model: "k3", Status: "ready"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	flows := map[string]string{}
	create := func(key, name string, nodes []workflow.Node) {
		t.Helper()
		wf, err := manager.workflows.put("", workflowInput{Name: name, Description: "独立临时数据；使用本地脚本模型进行真实页面操作验收。", Nodes: nodes}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.workflows.publish(wf.ID, "隔离验收 v1", "本地脚本模型，不代表真实模型业务验收"); err != nil {
			t.Fatal(err)
		}
		flows[key] = wf.ID
	}
	create("approval", "审批续跑与历史验收", []workflow.Node{
		{ID: "review", Kind: workflow.KindHuman, Prompt: "请审核这次隔离测试。填写审批意见和审核结论后选择批准或拒绝。", Fields: []workflow.FieldSpec{{Key: "note", Label: "审批意见", Type: "text", Required: true}, {Key: "category", Label: "审核结论", Type: "select", Options: []string{"同意继续", "需要补充"}, Required: true}}},
		{ID: "finish", Agent: agent.ID, Task: "UI_FIXTURE_NORMAL：根据人工审批结果给出确认。", After: []workflow.Dep{{Node: "review"}}},
	})
	create("slow", "取消与重试验收", []workflow.Node{{ID: "slow", Agent: agent.ID, Task: "UI_FIXTURE_SLOW：等待 60 秒，允许用户取消。"}})
	create("failure", "失败后重试验收", []workflow.Node{{ID: "unstable", Agent: agent.ID, Task: "UI_FIXTURE_FAIL_ONCE：首次模拟失败，显式重试后成功。"}})
	owner := identity.LocalScope("alice")
	start := func(key, intent string) string {
		t.Helper()
		def, err := manager.workflows.publishedDefinition(flows[key])
		if err != nil {
			t.Fatal(err)
		}
		meta, err := manager.admitWorkflow(def, &owner, "manual", def.ID, intent, "")
		if err != nil {
			t.Fatal(err)
		}
		return meta.ID
	}
	wait := func(id string, condition func(workflow.RunRecord) bool) workflow.RunRecord {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if rec, ok := manager.wfruns.snapshot(id); ok && condition(rec) {
				return rec
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("fixture run %s did not reach expected state", id)
		return workflow.RunRecord{}
	}
	historical := start("approval", "fixture-completed-history")
	rec := wait(historical, func(r workflow.RunRecord) bool { return r.Nodes["review"].Status == workflow.NodeWaitingHuman })
	if err := manager.enqueueWorkflowDecision(rec, workflow.HumanDecision{Node: "review", Action: "approve", Values: map[string]string{"note": "预置历史基线：由真实队列和脚本模型完成。", "category": "同意继续"}, Actor: sessionResultOwnerKey(&owner)}); err != nil {
		t.Fatal(err)
	}
	wait(historical, func(r workflow.RunRecord) bool { return r.Status == workflow.RunCompleted })
	pending := start("approval", "fixture-pending-review")
	wait(pending, func(r workflow.RunRecord) bool { return r.Nodes["review"].Status == workflow.NodeWaitingHuman })
	control, err := manager.taskRuns.database(false)
	if err != nil || control == nil {
		t.Fatalf("fixture control database: %v", err)
	}
	bob := identity.LocalPrincipal(identity.LocalScope("bob"), "bob", false)
	for _, wid := range flows {
		if err := control.SetWorkflowGrant(wid, bob.ID, []string{wfRead}); err != nil {
			t.Fatal(err)
		}
	}
	ready := map[string]any{"url": live.URL, "token": accounts[0].Token, "accounts": publicAccounts, "agent_id": agent.ID,
		"workflows": flows, "runs": map[string]string{"completed": historical, "pending": pending}, "state_dir": state,
		"schedule_dir": scheduleDir, "production_data": false, "real_model_calls": false, "scripted_model": true,
		"dispatcher_enabled": true, "source_connections": false, "restart_count": 0,
		"paths": map[string]string{"list": "/manage/workflows", "approval": "/manage/workflows/" + flows["approval"] + "?run=" + pending,
			"slow": "/manage/workflows/" + flows["slow"], "failure": "/manage/workflows/" + flows["failure"]}}
	if os.Getenv("POGO_WORKFLOW_UI_AUTHORING") == "1" {
		ready["authoring"] = prepareWorkflowAuthoringUIFixture(t, manager, live.URL, accounts[0].Token)
	}
	if os.Getenv("POGO_WORKFLOW_UI_STAGE2") == "1" || os.Getenv("POGO_WORKFLOW_UI_STAGE3") == "1" || os.Getenv("POGO_WORKFLOW_UI_STAGE4") == "1" {
		ready["embedding"] = startWorkflowEmbedUIHost(t, manager, live.URL, accounts[0].Token, agent.ID, output)
	}
	writeJSONFile(filepath.Join(output, "fixture.json"), ready)
	fmt.Println("WORKFLOW_UI_FIXTURE_READY=" + live.URL)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(45 * time.Minute)
	defer timeout.Stop()
	restarts := 0
	for {
		select {
		case <-timeout.C:
			t.Fatal("browser fixture timed out before explicit cleanup")
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(output, "stop")); err == nil {
				writeJSONFile(filepath.Join(output, "fixture-summary.json"), map[string]any{"model_calls": modelCalls.Load(), "simulated_failures": min(failures.Load(), 1), "restart_count": restarts, "production_data": false, "external_model_calls": 0})
				return
			}
			if _, err := os.Stat(filepath.Join(output, "restart")); err == nil {
				if err := os.Remove(filepath.Join(output, "restart")); err != nil {
					t.Fatal(err)
				}
				handlerMu.Lock()
				handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					http.Error(w, "isolated fixture restarting", http.StatusServiceUnavailable)
				})
				handlerMu.Unlock()
				manager.Close()
				server.Close()
				if err := open(); err != nil {
					t.Fatal(err)
				}
				restarts++
				ready["restart_count"] = restarts
				writeJSONFile(filepath.Join(output, "fixture.json"), ready)
				writeJSONFile(filepath.Join(output, "restarted.json"), map[string]any{"restart_count": restarts, "url": live.URL, "model_calls": modelCalls.Load()})
			}
		}
	}
}
