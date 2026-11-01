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

	"github.com/yosukeno/pogo/mcpobserve"
)

// Opt-in isolated interface fixture. Calls are explicit metadata samples, not
// production MCP traffic; the real HTTP handlers enforce their Agent scopes.
func TestMCPHistoryUIFixtureServer(t *testing.T) {
	out := os.Getenv("POGO_MCP_HISTORY_UI_FIXTURE")
	if out == "" {
		t.Skip("opt-in isolated UI fixture")
	}
	dev := os.Getenv("POGO_MCP_HISTORY_UI_DEV")
	u, err := url.Parse(dev)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
		t.Fatal("fixture requires a loopback Vite server")
	}
	h := newHarness(t, scriptedTurns(textTurn("可以查看本 Agent 的调用记录。")))
	srv, err := NewServer(h.mgr, ServerOptions{Token: h.token, DevProxy: dev, PrefsStore: filepath.Join(t.TempDir(), "prefs.json"), MCPClientStore: filepath.Join(t.TempDir(), "mcp.json"), MCPLog: &MCPLogOptions{DBPath: filepath.Join(t.TempDir(), "history.db")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	live := httptest.NewServer(srv.Handler())
	t.Cleanup(live.Close)
	a := createStudioAgent(t, h, "情报助手", "核对资料与调用记录。", "ready")
	b := createStudioAgent(t, h, "报表助手", "汇总报表。", "ready")
	sid := startStudioConversation(t, h, a.ID)
	sid2 := startStudioConversation(t, h, a.ID)
	other := startStudioConversation(t, h, b.ID)
	now := time.Now().UnixMilli()
	seed := func(index int, agentID, agentName, session, status, tool string) {
		owner := ""
		if scope := h.session(session).owner; scope != nil {
			owner = scope.SubjectID
		}
		id := fmt.Sprintf("%032x", index)
		call := mcpobserve.Call{ID: id, TraceID: id, SpanID: fmt.Sprintf("%016x", index), TraceFlags: "01", AgentID: agentID, AgentName: agentName, OwnerID: owner, SessionID: session, Entrypoint: "pogo", Direction: "outbound", Server: "资料服务", Tool: tool, StartedAt: now - int64(index*1000), FinishedAt: now - int64(index*1000) + 35, DurationMs: 35, Status: status}
		if index == 2 {
			call.ErrorKind = "tool_error"
			call.ErrorSummary = "字段 feedback_status 不存在；<b>仅是错误文本</b>"
			call.ErrorCode = "42703"
		}
		srv.mcpCalls.Record(call)
	}
	seed(1, a.ID, a.Name, sid, "ok", "search_records")
	seed(2, a.ID, a.Name, sid2, "error", "read_report")
	seed(3, b.ID, b.Name, other, "error", "foreign_agent_only")
	seed(4, "", "", other, "ok", "legacy_unattributed")
	if err := os.MkdirAll(out, 0700); err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{"url": live.URL, "token": h.token, "agent_id": a.ID, "other_agent_id": b.ID, "session_id": sid, "second_session_id": sid2, "other_session_id": other, "chat_path": "/manage/agents/" + a.ID + "/chat?c=" + sid, "seeded_metadata_fixture": true, "production_data": false}
	raw, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(filepath.Join(out, "fixture.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	fmt.Println("MCP_HISTORY_UI_FIXTURE_READY=" + live.URL)
	deadline := time.Now().Add(20 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(out, "stop")); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("fixture timed out before cleanup")
}
