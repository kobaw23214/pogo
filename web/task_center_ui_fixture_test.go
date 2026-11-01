package web

// Opt-in browser fixture. Real HTTP handlers, SQLite admissions and Studio
// bindings are used, but all model, timer and collector execution is disabled.
import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yosukeno/pogo/sentinel"
	"github.com/yosukeno/pogo/taskqueue"
)

func TestTaskCenterUIFixtureServer(t *testing.T) {
	output := os.Getenv("POGO_TASK_CENTER_UI_FIXTURE")
	if output == "" {
		t.Skip("opt-in isolated background-task browser fixture")
	}
	dev := os.Getenv("POGO_TASK_CENTER_UI_DEV")
	u, err := url.Parse(dev)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
		t.Fatal("fixture requires a loopback Vite server")
	}
	scheduleEnv(t)
	t.Setenv("HOME", t.TempDir())
	for _, name := range []string{"POGO_OFFICE_BROWSER_URL", "POGO_OFFICE_SERVICE_URL", "POGO_OFFICE_PUBLIC_URL", "POGO_OFFICE_FETCH_URL", "POGO_OFFICE_JWT_SECRET_FILE"} {
		t.Setenv(name, "")
	}
	h, capture := newSentinelHarness(t, nil)
	queue := taskCenterTestQueue(t, h.mgr)
	// No source process or source-owned dispatcher is started by this fixture.
	h.mgr.sentinels.cancel()
	observer := newSentinelRuntime(h.mgr)
	observationDB, err := sentinel.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	observer.store = observationDB
	h.mgr.sentinels = observer
	t.Cleanup(func() { observer.cancel(); _ = observationDB.Close() })
	server, err := NewServer(h.mgr, ServerOptions{Token: h.token, DevProxy: dev, PrefsStore: filepath.Join(t.TempDir(), "prefs.json"), MCPClientStore: filepath.Join(t.TempDir(), "mcp.json")})
	if err != nil {
		t.Fatal(err)
	}
	live := httptest.NewServer(server.Handler())
	t.Cleanup(live.Close)
	a := createStudioAgent(t, h, "后台任务验收助手", "使用用户提供的信息处理后台工作。", "ready")
	sid := startStudioConversation(t, h, a.ID)
	workspace := h.session(sid).cwd
	jobs := map[string]string{}
	titles := map[string]string{
		"queued":      "每日情报汇总",
		"running":     "质量检查正在执行",
		"interrupted": "上次中断的检查",
		"completed":   "完成的数据汇总",
		"waiting":     "等待确认的发布",
	}
	admissions := map[string]taskqueue.Run{}
	for _, key := range []string{"interrupted", "completed", "queued", "running", "waiting"} {
		body := map[string]any{"name": "fixture-" + key, "title": titles[key], "agent_id": a.ID, "source_session_id": sid, "prompt": "检查工作区中的演示资料，提炼变化并保存一份简短报告。此任务仅用于隔离页面验收。", "timezone": "Asia/Shanghai", "kind": "cron", "cron": "0 9 * * *"}
		response := agentScheduleResponse(t, h.do("POST", "/api/schedule/jobs", studioBody(body)), 201)
		var stored struct {
			Job struct {
				ID string `json:"id"`
			} `json:"job"`
		}
		raw, _ := json.Marshal(response)
		if err := json.Unmarshal(raw, &stored); err != nil || stored.Job.ID == "" {
			t.Fatalf("created schedule did not return identity: %s %v", raw, err)
		}
		jobs[key] = stored.Job.ID
		job, err := server.scheduleStore().Find(stored.Job.ID)
		if err != nil {
			t.Fatal(err)
		}
		admission, _, err := queue.Enqueue(taskqueue.Request{TaskID: "schedule:" + job.ID, Source: "schedule", SourceID: job.ID, Trigger: "manual", TriggerID: "fixture-" + key, DefinitionHash: job.DefHash, OwnerID: sessionResultOwnerKey(nil), AgentID: a.ID, Title: job.Title, Payload: json.RawMessage(`{"fixture":true}`)})
		if err != nil {
			t.Fatal(err)
		}
		if key != "queued" {
			admission, err = queue.Claim(admission.ID, 8)
			if err != nil {
				t.Fatal(err)
			}
			resultSID := startStudioConversation(t, h, a.ID)
			admission, err = queue.AttachSession(admission.ID, admission.Revision, resultSID)
			if err != nil {
				t.Fatal(err)
			}
			switch key {
			case "interrupted":
				if err = queue.Recover(); err != nil {
					t.Fatal(err)
				}
				admission, err = queue.Get(admission.ID)
			case "completed":
				admission, err = queue.Finish(admission.ID, admission.Revision, taskqueue.Outcome{Status: taskqueue.Completed, SessionID: resultSID, Summary: "共检查 12 项演示记录，发现 2 项变化。结果已保存，未调用外部服务。", EndReason: "completed"})
			case "waiting":
				h.session(resultSID).Hub().Publish(Event{Type: EvGateRequest, GateID: "fixture-review", CallID: "fixture-call", Name: "write", Args: json.RawMessage(`{"path":"uploads/example-report.txt","content":"演示报告"}`), Deadline: time.Now().Add(5 * time.Minute).UnixMilli()})
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		admissions[key] = admission
	}
	watch, err := observationDB.Create(sentinel.Spec{ID: "fixture-idle-watch", OwnerID: sessionResultOwnerKey(nil), AgentID: a.ID, Workspace: workspace, ScriptDir: workspace, Command: []string{"/bin/false"}, Title: "业务记录条件监听", Task: "等待符合条件的新记录；普通变化保持安静。演示页面不启动来源连接。", Mode: "stream", TimeoutSeconds: 10, MaxTurns: 5, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = observationDB.SetRuntimeGeneration(watch.Spec.ID, watch.Spec.Generation, "watching", "", 0, 0); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	ready := map[string]any{"url": live.URL, "token": h.token, "agent_id": a.ID, "session_id": sid, "chat_path": "/manage/agents/" + a.ID + "/chat?c=" + sid, "jobs": jobs, "runs": admissions, "data": "isolated temporary fixture only", "real_model_calls": false, "source_connections": false, "dispatcher_enabled": false}
	raw, _ := json.MarshalIndent(ready, "", "  ")
	if err = os.WriteFile(filepath.Join(output, "fixture.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	fmt.Println("TASK_CENTER_UI_FIXTURE_READY=" + live.URL)
	deadline := time.Now().Add(20 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(output, "stop")); err == nil {
			if sentinelModelCalls(capture) != 0 {
				t.Fatal("UI fixture unexpectedly invoked a scripted model")
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("browser fixture timed out before explicit cleanup")
}
