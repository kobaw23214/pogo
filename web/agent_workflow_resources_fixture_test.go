package web

// Opt-in isolated typed-input and bound-resource browser acceptance fixture.
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

func TestAgentWorkflowResourcesUIFixture(t *testing.T) {
	output := os.Getenv("POGO_AGENT_WORKFLOW_RESOURCES_FIXTURE")
	if output == "" {
		t.Skip("opt-in isolated workflow activity browser fixture")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Clean(filepath.Join(wd, "..", "..", ".ci-artifacts", "agent-workflow-resources-20261029", "private"))
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
	var manager *Manager
	manager, err = NewManager(Config{
		Cwd: filepath.Join(state, "workspace"), SessionDir: filepath.Join(state, "sessions"), Catalog: cat,
		Model: "k3", MaxTurns: 12, GateTimeout: 5 * time.Minute, RunTimeout: 40 * time.Minute, IdleTimeout: time.Hour,
		MCPClientStore: mcpPath,
		NewClient: func(config.Resolved, func(llm.RetryInfo)) llm.Client {
			return &awResourceTestClient{}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	devProxy := ""
	if os.Getenv("POGO_AGENT_WORKFLOW_RESOURCES_DEV") == "1" {
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
	a, source, skillID, skillPath := awSeedResourceAgent(t, h, accounts["alice"]["token"])
	definition := awResourceDefinition(skillID, false)
	bound := awCreateResourceDefinition(t, h, accounts["alice"]["token"], a, definition)
	for i := range definition.Steps {
		definition.Steps[i].Skills = nil
		definition.Steps[i].Shortcuts = nil
	}
	unbound := awTestValue[af.Definition](t, awTestCall(t, h, accounts["alice"]["token"], a.ID, "edit", map[string]any{
		"action": "create", "request_id": "resource-ui-unbound", "name": "请在画布绑定资源后试跑", "inputs": definition.Inputs, "steps": definition.Steps,
	}, 200), "workflow")
	stop := filepath.Join(output, "stop")
	os.Remove(stop)
	writeJSON(filepath.Join(output, "ready.json"), map[string]any{
		"url": live.URL, "vite_url": "http://127.0.0.1:19842", "state_dir": state, "accounts": accounts,
		"agent_id": a.ID, "workflow_id": bound.ID, "bound_workflow_id": bound.ID, "unbound_workflow_id": unbound.ID,
		"source_session_id": source, "source_file": "source.md", "skill_id": skillID, "skill_path": skillPath,
		"shortcut_id": "shortcut:report-material", "shortcut_title": "报告资料与格式", "scripted_model": true, "production_data": false, "stop_file": stop,
		"test_inputs":  map[string]string{"topic": "合成资料项目", "need_review": "yes", "audience": "ops"},
		"input_types":  map[string]string{"topic": "text", "need_review": "boolean", "audience": "select"},
		"instructions": "bound工作流可直接试跑；unbound用于在collect节点绑定synthetic Skill与报告资料快捷方式、report节点仅绑定同Skill。真实read附件→write notes→本人确认→read notes→write report；本fixture不自动运行。",
	})
	t.Log("isolated resource fixture ready at http://127.0.0.1:19841; credentials only in private ready.json")
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if _, err = os.Stat(stop); err == nil {
			return
		}
	}
}
