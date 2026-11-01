package web

// Opt-in isolated code-node and natural-language-authoring browser acceptance fixture.
import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	af "github.com/yosukeno/pogo/agentworkflow"

	"github.com/yosukeno/pogo/catalog"
	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
)

func TestAgentWorkflowCodeUIFixture(t *testing.T) {
	output := os.Getenv("POGO_AGENT_WORKFLOW_CODE_FIXTURE")
	if output == "" {
		t.Skip("opt-in isolated workflow activity browser fixture")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Clean(filepath.Join(wd, "..", "..", ".ci-artifacts", "agent-workflow-code-20261029", "private"))
	if filepath.Clean(output) != want {
		t.Fatal("fixture output must be this checkout's isolated observable directory")
	}
	// Refuse a busy port before touching the ready file. Never stop or reuse
	// another fixture, and never read a deployment's configuration or volume.
	listener, err := net.Listen("tcp", "127.0.0.1:19845")
	if err != nil {
		t.Fatal("isolated fixture port 19845 is unavailable")
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
		password, token := "Code-"+secret(), secret()
		if name == "alice" {
			token = aliceToken
		}
		if name == "bob" {
			token = bobToken
		}
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
	var manager *Manager
	manager, err = NewManager(Config{
		Cwd: filepath.Join(state, "workspace"), SessionDir: filepath.Join(state, "sessions"), Catalog: cat,
		Model: "k3", MaxTurns: 12, GateTimeout: 5 * time.Minute, RunTimeout: 40 * time.Minute, IdleTimeout: time.Hour,
		MCPClientStore: mcpPath,
		NewClient: func(config.Resolved, func(llm.RetryInfo)) llm.Client {
			return &awCodeAuthorClient{}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	devProxy := ""
	if os.Getenv("POGO_AGENT_WORKFLOW_CODE_DEV") == "1" {
		devProxy = "http://127.0.0.1:19846"
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
	a := awTestAgent(t, h, "代码工作流验收助手")
	source := h.studioConversationAs(aliceToken, a.ID)
	var screenshotDefinition af.Definition
	var screenshotRun af.Run
	if os.Getenv("POGO_AGENT_WORKFLOW_CODE_SCREENSHOT") == "1" {
		// A fresh real execution for a readable result screenshot after the
		// interactive acceptance fixture has already been cleaned up.
		screenshotDefinition = awCreateCodeDefinition(t, h, a, awCodeAcceptanceDefinition(), "code-screenshot-create")
		screenshotRun = awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "test_node", "request_id": "code-screenshot-run", "workflow_id": screenshotDefinition.ID, "step_id": "calculate", "source_session_id": source, "expected_revision": screenshotDefinition.Revision, "expected_hash": screenshotDefinition.DefinitionHash, "node_inputs": map[string]any{"amount": 12, "factor": 3}}, 200), "run")
		screenshotRun = awTestRun(t, h, aliceToken, a.ID, screenshotDefinition.ID, screenshotRun.ID, af.Completed)
	}
	stop := filepath.Join(output, "stop")
	os.Remove(stop)
	writeJSON(filepath.Join(output, "ready.json"), map[string]any{
		"url": live.URL, "state_dir": state, "accounts": accounts,
		"agent_id": a.ID, "source_session_id": source,
		"screenshot_workflow_id": screenshotDefinition.ID, "screenshot_run_id": screenshotRun.ID,
		"scripted_model": true, "production_data": false, "stop_file": stop,
		"instructions": "在当前对话要求创建订单计算流程并试跑calculate节点。脚本模型通过实际agent_workflow_*工具发现、创建、校验、单节点试跑。代码由真实受隔离Python/JS执行；本fixture不自动发起对话或运行。",
	})
	t.Log("isolated code fixture ready at http://127.0.0.1:19845; credentials only in private ready.json")
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if _, err = os.Stat(stop); err == nil {
			return
		}
	}
}
