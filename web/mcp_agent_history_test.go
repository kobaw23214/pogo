package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/mcpclient"
	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/schedule"
)

func TestAgentMCPHistoryHTTPAccountAgentAndPagination(t *testing.T) {
	t.Setenv("POGO_MCP_CALLS", "1")
	h := newMultiuserHarness(t, scriptedTurns(textTurn("ok")))
	create := func(name string) studioAgent {
		a, err := h.mgr.studio.put("", studioInput{Name: name, Instructions: "fixture", Model: "k3", Status: "ready"}, 0)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	a, b := create("Alpha MCP"), create("Beta MCP")
	ownA, ownB := identity.LocalScope("alice").SubjectID, identity.LocalScope("bob").SubjectID
	makeCall := func(aid, name, owner, sid string) mcpobserve.Call {
		return mcpobserve.Call{ID: mcpobserve.NewID(), AgentID: aid, AgentName: name, OwnerID: owner, SessionID: sid, Direction: "outbound", Server: "fixture", Tool: "echo", Status: "ok", StartedAt: time.Now().Add(-time.Minute).UnixMilli(), DurationMs: 1}
	}
	calls := []mcpobserve.Call{makeCall(a.ID, a.Name, ownA, "alice-session-1"), makeCall(a.ID, a.Name, ownA, "alice-session-2"), makeCall(a.ID, a.Name, ownB, "bob-session"), makeCall(b.ID, b.Name, ownA, "other-agent"), makeCall("", "", ownA, "alice-session-1"), makeCall(a.ID, a.Name, "", "unowned-export")}
	for _, c := range calls {
		h.psrv.mcpCalls.Record(c)
	}
	if err := h.psrv.mcpCalls.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	get := func(token, path string) (int, []byte) {
		resp := h.doAs(token, http.MethodGet, path, "")
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, raw
	}
	prefix := "/api/apps/" + a.ID + "/mcp/calls"
	code, raw := get(aliceToken, prefix+"?limit=1")
	var first mcpobserve.Page
	_ = json.Unmarshal(raw, &first)
	if code != 200 || len(first.Calls) != 1 || first.NextCursor == "" {
		t.Fatalf("first %d %s", code, raw)
	}
	code, raw = get(aliceToken, prefix+"?limit=1&before="+first.NextCursor)
	var second mcpobserve.Page
	_ = json.Unmarshal(raw, &second)
	if code != 200 || len(second.Calls) != 1 || second.Calls[0].ID == first.Calls[0].ID {
		t.Fatalf("second %d %s", code, raw)
	}
	for _, c := range append(first.Calls, second.Calls...) {
		if c.AgentID != a.ID || c.OwnerID != ownA {
			t.Fatalf("scope leaked %+v", c)
		}
	}
	code, raw = get(aliceToken, prefix+"/stats?limit=1")
	var st mcpobserve.Stats
	_ = json.Unmarshal(raw, &st)
	if code != 200 || st.Total != 2 {
		t.Fatalf("scoped stats %d %s", code, raw)
	}
	for i, c := range calls {
		want := 404
		if i < 2 {
			want = 200
		}
		if code, raw := get(aliceToken, prefix+"/"+c.ID); code != want {
			t.Fatalf("detail %d want%d: %d %s", i, want, code, raw)
		}
	}
	for _, path := range []string{prefix + "?owner_id=" + ownB, prefix + "/stats?agent_id=" + b.ID, prefix + "/" + calls[0].ID + "?owner_id=" + ownB} {
		if code, raw := get(aliceToken, path); code != 400 {
			t.Fatalf("forged scope %d %s", code, raw)
		}
	}
	if code, raw := get(bobToken, prefix); code != 200 {
		t.Fatalf("bob own Agent view %d %s", code, raw)
	} else {
		var p mcpobserve.Page
		_ = json.Unmarshal(raw, &p)
		if len(p.Calls) != 1 || p.Calls[0].OwnerID != ownB {
			t.Fatalf("bob leak %s", raw)
		}
	}
	if code, _ := get("", prefix); code != 401 {
		t.Fatalf("unauthenticated %d", code)
	}
	if code, _ := get(aliceToken, "/api/mcp/calls"); code != 403 {
		t.Fatalf("global non-admin %d", code)
	}
	h.armUsers(map[string]userRecord{"alice": {Username: "alice", Token: aliceToken, Admin: true}, "bob": {Username: "bob", Token: bobToken}})
	code, raw = get(aliceToken, "/api/mcp/calls?agent_id="+a.ID)
	var global mcpobserve.Page
	_ = json.Unmarshal(raw, &global)
	if code != 200 || len(global.Calls) != 4 {
		t.Fatalf("admin all-owner Agent filter %d %s", code, raw)
	}
	code, raw = get(aliceToken, "/api/mcp/calls?q=Alpha%20MCP")
	_ = json.Unmarshal(raw, &global)
	if code != 200 || len(global.Calls) != 4 {
		t.Fatalf("admin name search %d %s", code, raw)
	}
	if code, _ := get(aliceToken, prefix); code != 200 {
		t.Fatal("admin scoped endpoint unavailable")
	}
}

func TestAgentMCPRunIdentityUsesPinnedSessionNotObservedClaims(t *testing.T) {
	rec := &observeRecorder{}
	client := &ctxCaptureClient{model: "k3", probe: rec}
	mgr := newObserveManager(t, client)
	mgr.SetMCPRecorder(rec)
	a, err := mgr.studio.put("", studioInput{Name: "Trusted Agent", Instructions: "fixture", Model: "k3", Status: "ready"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	owner := identity.LocalScope("alice")
	sess, err := mgr.createStudioConversation(a, &owner, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = sess.StartObserved("fixture", nil, "", mcpobserve.Metadata{AgentID: "model-forged", AgentName: "model-forged", OwnerID: "forged-owner"})
	if err != nil {
		t.Fatal(err)
	}
	waitRunDone(t, sess)
	md := client.last()
	if md.AgentID != a.ID || md.AgentName != a.Name || md.OwnerID != owner.SubjectID {
		t.Fatalf("runtime identity %+v", md)
	}
	call := rec.finalForTool("ctx_probe")
	if call == nil || call.AgentID != a.ID || call.AgentName != a.Name || call.OwnerID != owner.SubjectID {
		t.Fatalf("actual recorded call %+v", call)
	}
	// This is the same bound Session activation path used by Agent schedules.
	ordinary, err := mgr.Create("", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = ordinary.StartObserved("fixture", nil, "", mcpobserve.Metadata{AgentID: a.ID, AgentName: a.Name})
	if err != nil {
		t.Fatal(err)
	}
	waitRunDone(t, ordinary)
	if md := client.last(); md.AgentID != "" || md.AgentName != "" {
		t.Fatalf("ordinary run claimed Agent %+v", md)
	}
}

func TestAgentMCPScheduledRuntimeAttribution(t *testing.T) {
	rec := &observeRecorder{}
	client := &ctxCaptureClient{model: "k3", probe: rec}
	mgr := newObserveManager(t, client)
	mgr.SetMCPRecorder(rec)
	mgr.mu.Lock()
	mgr.scheduleOwnerValid = func(scope *identity.Scope) bool { return scope == nil }
	mgr.mu.Unlock()
	a, err := mgr.studio.put("", studioInput{Name: "定时任务 Agent", Instructions: "fixture", Model: "k3", Status: "ready"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	job := schedule.Job{ID: "trusted-scheduled-job", AgentID: a.ID, Prompt: "fixture", Workdir: mgr.Cwd(), Timezone: "UTC", Kind: schedule.KindEvery, EverySec: 3600, Enabled: true}
	logs := schedule.LogPaths{Out: filepath.Join(t.TempDir(), "out.json"), Err: filepath.Join(t.TempDir(), "err.log")}
	outcome := mgr.runAgentSchedule(context.Background(), job, logs, io.Discard)
	if outcome.Err != "" || outcome.SessionID == "" {
		t.Fatalf("scheduled execution %+v", outcome)
	}
	call := rec.finalForTool("ctx_probe")
	if call == nil || call.AgentID != a.ID || call.AgentName != a.Name || call.TaskID != job.ID || call.SessionID != outcome.SessionID {
		t.Fatalf("scheduled MCP metadata %+v", call)
	}
}
func TestAgentMCPInboundSDKIgnoresModelAgentClaims(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("ok")))
	a := createStudioAgent(t, h, "Exported Agent", "fixture", "ready")
	rec := &observeRecorder{}
	h.mgr.SetMCPRecorder(rec)
	layer := newMCPLayer(h.mgr, MCPOptions{}, false, &mcpclient.ServeConfig{Enabled: true})
	layer.agentID = a.ID
	client := connectInMemory(t, layer.server(), "external-fixture")
	params := &mcp.CallToolParams{Name: "agent_task_list", Arguments: map[string]any{}}
	params.SetMeta(map[string]any{"agent_id": "model-forged", "agent_name": "model-forged", "owner_id": "other-owner"})
	_, err := client.CallTool(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	call := rec.finalForTool("agent_task_list")
	if call == nil || call.AgentID != a.ID || call.AgentName != a.Name || call.Direction != "inbound" || call.OwnerID != "" {
		t.Fatalf("fixed endpoint attribution %+v", call)
	}
}
