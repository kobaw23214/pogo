package web

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yosukeno/pogo/inputref"
)

// Opt-in full-interface fixture: real handlers/session runtime with a scripted
// model and private t.TempDir storage. Never starts a production schedule.
func TestScheduleUIFixtureServer(t *testing.T) {
	output := os.Getenv("POGO_SCHEDULE_UI_FIXTURE")
	if output == "" {
		t.Skip("opt-in isolated browser fixture")
	}
	dev := os.Getenv("POGO_SCHEDULE_UI_DEV")
	u, err := url.Parse(dev)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
		t.Fatal("fixture requires a loopback Vite server")
	}
	_ = scheduleEnv(t)
	h := newHarness(t, scriptedTurns(textTurn("本次汇总已完成。共检查 3 项记录，未发现需要人工处理的问题。")))
	server, err := NewServer(h.mgr, ServerOptions{Token: h.token, DevProxy: dev, PrefsStore: filepath.Join(t.TempDir(), "prefs.json"), MCPClientStore: filepath.Join(t.TempDir(), "mcp.json")})
	if err != nil {
		t.Fatal(err)
	}
	live := httptest.NewServer(server.Handler())
	t.Cleanup(live.Close)
	a := createStudioAgent(t, h, "每日情报助手", "使用用户提供的资料生成简短报告。", "ready")
	sid := startStudioConversation(t, h, a.ID)
	cwd := h.session(sid).cwd
	if err := os.MkdirAll(filepath.Join(cwd, "uploads"), 0700); err != nil {
		t.Fatal(err)
	}
	file := "uploads/统计样例.csv"
	if err := os.WriteFile(filepath.Join(cwd, file), []byte("date,count\n2026-10-18,3\n"), 0600); err != nil {
		t.Fatal(err)
	}
	parts := []inputref.Part{
		{Type: "text", Text: "请检查 "}, {Type: "file", Path: file, AsFile: true},
		{Type: "text", Text: "，结合选区 "}, {Type: "office", Office: &inputref.OfficeSnapshot{Path: "uploads/report.xlsx", Title: "季度统计", Editor: "cell", Sheet: "数据", Range: "B2:C4", CanvasVersion: "fixture-saved-v1", Text: "本周记录：3；异常：0"}},
		{Type: "text", Text: "，生成日报。"},
	}
	h.post("/api/sessions/"+sid+"/messages", studioBody(map[string]any{"prompt": "检查数据并生成日报", "parts": parts, "input_layout": "inline"}), 202)
	deadline := time.Now().Add(5 * time.Second)
	for h.session(sid).Active() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if h.session(sid).Active() {
		t.Fatal("initial fixture conversation did not finish")
	}
	for _, entry := range []struct{ ID, Title string }{{"daily-report", "每日情报汇总"}, {"weekly-audit", "每周质量检查"}} {
		body := map[string]any{"name": entry.ID, "title": entry.Title, "agent_id": a.ID, "source_session_id": sid, "prompt": "请汇总资料并列出变化", "parts": parts, "input_layout": "inline", "timezone": "Asia/Shanghai", "kind": "cron", "cron": "0 9 * * *"}
		agentScheduleResponse(t, h.do("POST", "/api/schedule/jobs", studioBody(body)), 201)
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	ready := map[string]any{"url": live.URL, "token": h.token, "agent_id": a.ID, "session_id": sid, "chat_path": "/manage/agents/" + a.ID + "/chat?c=" + sid, "data": "temporary fixture only", "scripted_model": true}
	raw, _ := json.MarshalIndent(ready, "", "  ")
	if err := os.WriteFile(filepath.Join(output, "fixture.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	fmt.Println("SCHEDULE_UI_FIXTURE_READY=" + live.URL)
	deadline = time.Now().Add(20 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(output, "stop")); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("fixture timed out before explicit cleanup")
}
