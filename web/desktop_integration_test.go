package web

// Opt-in real Go -> desktop-owned worker integration. The only model is the
// deterministic scripted client; no provider or production endpoint is called.
// The launcher creates a new output directory and passes its private config.
import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

type desktopIntegrationConfig struct {
	WorkerURL       string `json:"workerURL"`
	WorkerTokenFile string `json:"workerTokenFile"`
	Listen          string `json:"listen"`
	Output          string `json:"output"`
	Hold            bool   `json:"hold"`
	WorkerWait      int    `json:"workerWaitSeconds"`
}

func TestDesktopBrowserGoIntegration(t *testing.T) {
	configPath := os.Getenv("POGO_DESKTOP_GO_TEST_CONFIG")
	if configPath == "" {
		t.Skip("desktop integration requires an explicitly owned worker and private config")
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal("cannot read desktop integration config")
	}
	var cfg desktopIntegrationConfig
	if json.Unmarshal(raw, &cfg) != nil {
		t.Fatal("invalid desktop integration config")
	}
	u, err := url.Parse(cfg.WorkerURL)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		t.Fatal("worker must be a direct owned loopback HTTP endpoint")
	}
	host, _, err := net.SplitHostPort(cfg.Listen)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("Go test server must bind IPv4 loopback")
	}
	if !filepath.IsAbs(cfg.Output) {
		t.Fatal("absolute fresh output directory required")
	}
	secret, err := os.ReadFile(cfg.WorkerTokenFile)
	if err != nil || len(strings.TrimSpace(string(secret))) < 24 {
		t.Fatal("fresh desktop worker token file is missing or too short")
	}
	info, err := os.Stat(cfg.WorkerTokenFile)
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatal("desktop worker token file must be private")
	}
	workerToken := strings.TrimSpace(string(secret))
	private := filepath.Join(cfg.Output, "private")
	if err := os.MkdirAll(private, 0o700); err != nil {
		t.Fatal(err)
	}
	stateRoot := t.TempDir()
	for key, suffix := range map[string]string{
		"POGO_SESSION_DIR": "sessions", "POGO_SPILL_DIR": "spill", "POGO_WORKTREE_DIR": "worktrees", "POGO_MEMORY_DIR": "memory", "POGO_SCHEDULE_DIR": "schedule",
		"POGO_PREFS_FILE": "prefs.json", "POGO_MCP_FILE": "mcp.json", "POGO_MCP_LOG_DB": "mcplog.db",
	} {
		t.Setenv(key, filepath.Join(stateRoot, suffix))
	}
	t.Setenv("POGO_SESSION_INDEX", "")
	t.Setenv("POGO_MCP", "0")
	t.Setenv("POGO_SKILL_RUNTIME_STORE", "")
	for _, key := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_HEADERS"} {
		t.Setenv(key, "")
	}
	checks := []string{}
	runNonce := randomID(16)
	report := map[string]any{"passed": false, "scope": "real Studio/Go agent loop/tools/BrowserGate/native HTTP with desktop-owned Chromium; deterministic scripted model only", "started_at": time.Now().UTC().Format(time.RFC3339Nano), "run_nonce": runNonce,
		"limitations": []string{"No real model/provider call or production API request.", "No replacement headless Chromium is started by this harness.", "Automation is not physical keyboard/IME acceptance.", "Desktop ownership and visible WebContents identity must additionally be proved by the desktop host/UI harness."}}
	save := func(name string, value any) {
		t.Helper()
		desktopIntegrationWrite(t, filepath.Join(cfg.Output, name), value)
	}
	t.Cleanup(func() {
		report["checks"] = checks
		report["finished_at"] = time.Now().UTC().Format(time.RFC3339Nano)
		if t.Failed() {
			report["passed"] = false
		}
		save("summary.json", report)
	})
	check := func(name string, ok bool) {
		t.Helper()
		if !ok {
			report["failed_check"] = name
			report["checks"] = checks
			save("summary.json", report)
			t.Fatal(name)
		}
		checks = append(checks, name)
	}
	var siteMu sync.Mutex
	saves := 0
	savedValue := ""
	slowEntered := make(chan struct{}, 1)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path == "/slow" {
			select {
			case slowEntered <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			return
		}
		if r.URL.Path == "/save" && r.Method == "POST" {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
			siteMu.Lock()
			saves++
			savedValue = string(body)
			siteMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"saved":true}`)
			return
		}
		if r.URL.Path != "/page" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><title>Pogo desktop Go integration</title><style>body{font:22px system-ui;padding:32px;background:#eef4fa}input,button{font:22px system-ui;margin:12px;padding:12px;max-width:100%;box-sizing:border-box}input[type=range]{display:block;width:min(100%,520px);padding:0;margin:16px 0}</style><h1>Owned desktop fixture</h1><label>Value <input id="value"></label><button id="save" onclick="fetch('/save',{method:'POST',body:document.querySelector('#value').value}).then(()=>document.querySelector('#result').textContent='Saved exactly once')">Save once</button><p id="result">Ready</p><label for="range">普通滑块（本地交互测试）</label><input type="range" id="range" min="0" max="100" value="25" oninput="document.querySelector('#range-value').textContent=this.value"><output id="range-value">25</output><script>window.__desktopGoFixture='same-visible-page';</script>`)
	}))
	t.Cleanup(func() { fixture.CloseClientConnections(); fixture.Close() })
	var scriptMu sync.Mutex
	phase, step := 0, 0
	actions := []map[string]any{
		{"action": "navigate", "url": fixture.URL + "/page"}, {"action": "snapshot"},
		{"action": "type", "target": "#value", "text": "desktop-go-中文"}, {"action": "click", "target": "#save"},
		{"action": "wait", "text": "Saved exactly once", "seconds": 5},
		{"action": "devtools", "method": "Runtime.evaluate", "params": map[string]any{"expression": "({marker:window.__desktopGoFixture,value:document.querySelector('#value').value})", "returnByValue": true}},
	}
	h := newHarness(t, func(_ int) (llm.Response, error) {
		scriptMu.Lock()
		defer scriptMu.Unlock()
		step++
		var action map[string]any
		switch phase {
		case 0:
			if step <= len(actions) {
				action = actions[step-1]
			}
		case 1:
			action = map[string]any{"action": "navigate", "url": fixture.URL + "/slow"}
		case 2:
			if step == 1 {
				action = map[string]any{"action": "navigate", "url": fixture.URL + "/page"}
			}
		}
		if action == nil {
			return textTurn("隔离的桌面浏览器集成验收已完成。此回答由确定性脚本产生，未调用真实模型。"), nil
		}
		args, _ := json.Marshal(action)
		r := toolTurn("browser", string(args))
		r.Message.Content[0].ID = fmt.Sprintf("desktop_%d_%d", phase, step)
		return r, nil
	}, func(c *Config) {
		c.BrowserURL, c.BrowserToken = cfg.WorkerURL, workerToken
		c.BrowserDesktopOwner = bootstrapUser
		c.Cwd = filepath.Join(stateRoot, "workspace")
		c.SessionDir = filepath.Join(stateRoot, "sessions")
		if err := os.MkdirAll(c.Cwd, 0o700); err != nil {
			t.Fatal(err)
		}
		c.MaxTurns = 20
		c.Followups = false
		c.Citations = true
		c.MCPClientStore = ""
		c.ScheduleTickInterval = 0
		c.SpillSweepInterval = 0
	})
	// Rebind only this owned test server to the agreed Studio port and replace
	// the unit harness token with a fresh secret kept outside public reports.
	h.srv.Close()
	h.token = randomID(32)
	server, err := NewServer(h.mgr, ServerOptions{Token: h.token})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		t.Fatal(err)
	}
	h.srv = httptest.NewUnstartedServer(server.Handler())
	h.srv.Listener = listener
	h.srv.Start()
	t.Cleanup(func() { h.srv.CloseClientConnections(); h.srv.Close() })
	webTokenFile := filepath.Join(private, "web-token")
	if err := os.WriteFile(webTokenFile, []byte(h.token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := postStudioAgent(t, h, studioAgentCapsBody("Desktop isolated integration", "ready", map[string]any{"tools": []string{"browser"}, "skills": []string{}, "mcp_servers": []string{}}), http.StatusCreated)
	sid := startStudioConversation(t, h, a.ID)
	foreignSID := startStudioConversation(t, h, a.ID)
	sess := h.session(sid)
	studioURL := h.srv.URL + "/a/" + a.ID + "?c=" + url.QueryEscape(sid)
	ready := map[string]any{"pid": os.Getpid(), "run_nonce": runNonce, "appURL": h.srv.URL, "studioURL": studioURL, "agent_id": a.ID, "session_id": sid, "foreign_session_id": foreignSID, "workerURL": cfg.WorkerURL, "webTokenFile": webTokenFile, "fixtureURL": fixture.URL + "/page", "finishFile": filepath.Join(cfg.Output, "finish.json"), "state_root": stateRoot, "scope": "owned temporary Studio; scripted model; no provider traffic"}
	save("go-ready.json", ready)
	t.Logf("isolated Go Studio ready; metadata=%s", filepath.Join(cfg.Output, "go-ready.json"))
	index := h.get("/a/" + a.ID)
	html, _ := io.ReadAll(index.Body)
	index.Body.Close()
	check("real embedded Studio UI is served", index.StatusCode == 200 && strings.Contains(string(html), "<html") && !strings.Contains(string(html), placeholderMarker))
	unauth := h.get("/api/sessions")
	unauth.Body.Close()
	check("Go API rejects missing authentication", unauth.StatusCode == 401)
	waitSeconds := cfg.WorkerWait
	if waitSeconds <= 0 || waitSeconds > 300 {
		waitSeconds = 120
	}
	deadline := time.Now().Add(time.Duration(waitSeconds) * time.Second)
	healthClient := &http.Client{Timeout: 2 * time.Second}
	for {
		req, _ := http.NewRequest("GET", cfg.WorkerURL+"/healthz", nil)
		req.Header.Set("Authorization", "Bearer "+workerToken)
		res, e := healthClient.Do(req)
		if e == nil {
			_, _ = io.Copy(io.Discard, res.Body)
			res.Body.Close()
			if res.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("owned desktop worker did not become healthy before deadline")
		}
		time.Sleep(200 * time.Millisecond)
	}
	check("desktop worker became healthy without launching another browser", true)
	toolResults := []map[string]any{}
	recordTool := func(e Event) {
		details, _ := e.Details.(map[string]any)
		toolResults = append(toolResults, map[string]any{
			"call_id": e.CallID, "tool": e.Name, "is_error": e.IsError, "details_ok": details["ok"], "duration_ms": e.DurationMS,
			"text": desktopIntegrationRedact(e.Text, workerToken, h.token), "error": desktopIntegrationRedact(e.Error, workerToken, h.token),
		})
		report["tool_results"] = toolResults
		report["checks"] = checks
		save("summary.json", report)
	}
	// Run the model-facing browser tool through the actual Go agent loop, public
	// message endpoint, event stream and persistence; only inference is scripted.
	sess.Browser().Arm()
	stream := h.start(sid, "Run the owned synthetic desktop browser acceptance.")
	toolEnds := 0
	sourceLabels := []string{}
	for {
		e := stream.next(t)
		if e.Type == EvToolEnd {
			recordTool(e)
			check("scripted browser tool completed "+e.CallID, desktopIntegrationToolOK(e))
			if e.CallID == "desktop_0_6" {
				check("CDP observes the fixture and typed value on the same page", strings.Contains(e.Text, "same-visible-page") && strings.Contains(e.Text, "desktop-go-中文"))
			}
			if e.Source != nil {
				sourceLabels = append(sourceLabels, e.Source.Label)
			}
			toolEnds++
		}
		if e.Type == EvRunEnd {
			break
		}
	}
	stream.close()
	waitFor(t, 5*time.Second, func() bool { return !sess.Active() })
	check("six browser calls crossed the real Go agent loop", toolEnds == len(actions))
	report["source_labels"] = sourceLabels
	siteMu.Lock()
	count, value := saves, savedValue
	siteMu.Unlock()
	check("bound desktop page submitted exactly once with Unicode text", count == 1 && value == "desktop-go-中文")
	report["initial_saved_value"] = value
	// Context isolation is established through the Go public state surfaces.
	state := desktopIntegrationState(t, h, sid)
	check("worker explicitly advertises desktop-only human display", state["display_mode"] == "desktop")
	check("Go confirms the exact configured desktop account binding", state["device_owner_bound"] == true)
	frame := h.do(http.MethodGet, "/api/sessions/"+sid+"/browser/frame", "")
	_, _ = io.Copy(io.Discard, frame.Body)
	frame.Body.Close()
	check("legacy human JPEG frame is explicitly retired", frame.StatusCode == http.StatusGone)
	afterFrame := desktopIntegrationState(t, h, sid)
	check("retired frame request does not recreate the desktop context", afterFrame["context_ref"] == state["context_ref"] && afterFrame["generation"] == state["generation"])
	other := desktopIntegrationState(t, h, foreignSID)
	check("Go sessions receive different desktop contexts", state["context_ref"] != "" && state["context_ref"] != other["context_ref"])
	// Cancel only after the fixture observed navigation, so this cannot pass by
	// cancelling before any browser request was dispatched.
	scriptMu.Lock()
	phase, step = 1, 0
	scriptMu.Unlock()
	sess.Browser().Arm()
	stream = h.start(sid, "Cancel a dispatched owned navigation.")
	select {
	case <-slowEntered:
	case <-time.After(15 * time.Second):
		t.Fatal("slow navigation was not dispatched")
	}
	h.post("/api/sessions/"+sid+"/control", `{"action":"cancel"}`, http.StatusOK)
	for {
		e := stream.next(t)
		if e.Type == EvToolEnd {
			recordTool(e)
		}
		if e.Type == EvRunEnd {
			break
		}
	}
	stream.close()
	waitFor(t, 20*time.Second, func() bool { return !sess.Active() })
	check("public Go cancel ends a dispatched browser run", true)
	scriptMu.Lock()
	phase, step = 2, 0
	scriptMu.Unlock()
	sess.Browser().Arm()
	stream = h.start(sid, "Resume with a fresh owned browser navigation.")
	for {
		e := stream.next(t)
		if e.Type == EvToolEnd {
			recordTool(e)
			check("post-cancel browser tool succeeds", desktopIntegrationToolOK(e))
		}
		if e.Type == EvRunEnd {
			break
		}
	}
	stream.close()
	waitFor(t, 5*time.Second, func() bool { return !sess.Active() })
	state = desktopIntegrationState(t, h, sid)
	check("post-cancel state belongs to the intended fixture", state["url"] == fixture.URL+"/page")
	siteMu.Lock()
	count = saves
	siteMu.Unlock()
	check("cancellation did not replay the earlier mutation", count == 1)
	// Public lease handoff on the same context, with a model-facing tool blocked
	// during human control. The UI harness can repeat actual native input later.
	controlBody, _ := json.Marshal(map[string]any{"mode": "human", "context_ref": state["context_ref"], "tab_id": state["active_tab"]})
	control := h.do("POST", "/api/sessions/"+sid+"/browser/control", string(controlBody))
	var human map[string]any
	err = json.NewDecoder(control.Body).Decode(&human)
	control.Body.Close()
	lease, _ := human["lease_token"].(string)
	check("public same-context human takeover grants a lease", err == nil && control.StatusCode == 200 && lease != "" && human["context_ref"] == state["context_ref"])
	retiredInput, _ := json.Marshal(map[string]any{"type": "text", "text": "must-not-reach-native-page", "lease_token": lease, "context_ref": human["context_ref"], "target_tab": human["viewed_tab"], "generation": human["generation"]})
	inputReply := h.do(http.MethodPost, "/api/sessions/"+sid+"/browser/input", string(retiredInput))
	_, _ = io.Copy(io.Discard, inputReply.Body)
	inputReply.Body.Close()
	check("legacy human text forwarding is explicitly retired", inputReply.StatusCode == http.StatusGone)
	sess.Browser().Arm()
	browserTool := &tools.Browser{Link: sess.Browser(), ImageMode: tools.BrowserImageNone, SpillDir: filepath.Join(stateRoot, "spill")}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	denied, deniedErr := browserTool.Execute(ctx, json.RawMessage(`{"action":"click","target":"#save"}`))
	cancel()
	check("agent tool is blocked during human control", deniedErr != nil || strings.Contains(denied.Text, "browser click failed:"))
	back, _ := json.Marshal(map[string]any{"mode": "agent", "context_ref": state["context_ref"], "tab_id": human["viewed_tab"], "lease_token": human["lease_token"]})
	h.post("/api/sessions/"+sid+"/browser/control", string(back), http.StatusOK)
	readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	unmodified, readErr := browserTool.Execute(readCtx, json.RawMessage(`{"action":"devtools","method":"Runtime.evaluate","params":{"expression":"document.querySelector('#value').value === '' ? 'retired-input-left-page-unchanged' : 'unexpected-input-change'","returnByValue":true}}`))
	readCancel()
	check("retired HTTP text input did not change the real page", readErr == nil && strings.Contains(unmodified.Text, "retired-input-left-page-unchanged"))
	siteMu.Lock()
	count = saves
	siteMu.Unlock()
	check("blocked agent input did not mutate the human-controlled page", count == 1)
	sess.Browser().Disarm()
	scriptMu.Lock()
	phase, step = 3, 0
	scriptMu.Unlock()
	report["passed"] = true
	report["checks"] = checks
	report["session_id"] = sid
	report["agent_id"] = a.ID
	report["fixture_save_count"] = count
	report["state"] = desktopIntegrationPublicState(state)
	save("summary.json", report)
	save("go-acceptance-ready.json", map[string]any{"passed": true, "run_nonce": runNonce, "studioURL": studioURL, "session_id": sid, "checks": len(checks), "hold": cfg.Hold})
	t.Logf("desktop Go integration passed %d checks; hold=%t", len(checks), cfg.Hold)
	if cfg.Hold {
		for {
			data, err := os.ReadFile(filepath.Join(cfg.Output, "finish.json"))
			if err == nil {
				var finish struct {
					Action string `json:"action"`
					Nonce  string `json:"run_nonce"`
				}
				if json.Unmarshal(data, &finish) == nil && finish.Action == "finish" && finish.Nonce == runNonce {
					break
				}
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	check("owned Go server released through explicit finish lifecycle", true)
}

func desktopIntegrationState(t *testing.T, h *harness, sid string) map[string]any {
	t.Helper()
	res := h.do("GET", "/api/sessions/"+sid+"/browser", "")
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("owned browser state HTTP %d", res.StatusCode)
	}
	var state map[string]any
	if json.NewDecoder(res.Body).Decode(&state) != nil {
		t.Fatal("invalid owned browser state")
	}
	return state
}

func desktopIntegrationPublicState(state map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{"context_ref", "active_tab", "viewed_tab", "generation", "url", "title", "control", "display_mode", "device_owner_bound"} {
		if value, ok := state[key]; ok {
			out[key] = value
		}
	}
	return out
}

func desktopIntegrationToolOK(e Event) bool {
	details, ok := e.Details.(map[string]any)
	return !e.IsError && ok && details["ok"] == true
}

func desktopIntegrationRedact(text string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	text = regexp.MustCompile(`(?i)([?&](?:token|access_token|api_key|key)=)[^&\s"<>]+`).ReplaceAllString(text, "${1}[redacted]")
	text = regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)[^\s"<>]+`).ReplaceAllString(text, "${1}[redacted]")
	runes := []rune(text)
	if len(runes) > 4096 {
		text = string(runes[:4096]) + "… [truncated]"
	}
	return text
}

func desktopIntegrationWrite(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}
