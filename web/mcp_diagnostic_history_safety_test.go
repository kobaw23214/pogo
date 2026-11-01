package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/catalog"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/mcpobserve"
)

// Historical explanations are a read-only detail projection. A telemetry row
// mentioning a session ID cannot substitute for that session's real owner or
// pinned Agent binding, even when the row itself is visible to the requester.
func TestMCPDiagnosticHistorySafetyHTTPReadOnlyAndOwnerAgentIsolation(t *testing.T) {
	t.Setenv("POGO_MCP_CALLS", "1")
	h := newMultiuserHarness(t, scriptedTurns(textTurn("must never run")))
	createAgent := func(name string) studioAgent {
		t.Helper()
		a, err := h.mgr.studio.put("", studioInput{Name: name, Instructions: "synthetic fixture", Model: "k3", Status: "ready"}, 0)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	a, otherAgent := createAgent("Historical Alpha"), createAgent("Historical Beta")
	alice, bob := identity.LocalScope("alice"), identity.LocalScope("bob")
	type fixture struct {
		sid, path, toolID, reason, secret, input, payload string
		owner                                             identity.Scope
		agent                                             studioAgent
	}
	filesBefore := map[string][]byte{}
	catalogBefore := map[string]catalog.Session{}
	makeHistory := func(a studioAgent, owner identity.Scope, label string) fixture {
		t.Helper()
		sess, err := h.mgr.createStudioConversation(a, &owner)
		if err != nil {
			t.Fatal(err)
		}
		f := fixture{sid: sess.ID, path: sess.store.Path(), toolID: "historical-tool-" + label,
			reason: label + "HistoricalReason", secret: label + "SecretCanary", input: label + "InputCanary", payload: label + "PayloadCanary", owner: owner, agent: a}
		args, _ := json.Marshal(map[string]string{"password": f.secret, "database": f.input})
		result, _ := json.Marshal(map[string]any{
			"message": f.reason + " permission denied " + f.input + " password=" + f.secret,
			"code":    "HIST_DENIED", "rows": []string{f.payload},
		})
		if err := sess.store.AppendAll([]llm.Message{
			{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, ID: f.toolID, Name: "fixture_query", Input: args}}},
			{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: f.toolID, IsError: true, Text: string(result)}}},
		}); err != nil {
			t.Fatal(err)
		}
		// Leave only the authoritative catalog/binding and JSONL. A detail read
		// must not call Manager.Get and silently recreate a live runtime.
		sess.closeUnpublished()
		h.mgr.mu.Lock()
		delete(h.mgr.sessions, sess.ID)
		h.mgr.mu.Unlock()
		raw, err := os.ReadFile(f.path)
		if err != nil {
			t.Fatal(err)
		}
		filesBefore[f.path] = raw
		row, err := h.mgr.cfg.Catalog.Get(context.Background(), owner, sess.ID)
		if err != nil || row.State != "active" {
			t.Fatalf("history fixture lacks an active authoritative owner: %+v, %v", row, err)
		}
		catalogBefore[f.sid] = row
		return f
	}
	ah := makeHistory(a, alice, "Alice")
	bh := makeHistory(a, bob, "Bob")
	otherHistory := makeHistory(otherAgent, alice, "OtherAgent")
	studioBytes, err := os.ReadFile(h.mgr.studio.path)
	if err != nil {
		t.Fatal(err)
	}
	filesBefore[h.mgr.studio.path] = studioBytes
	makeCall := func(f fixture, owner, agent string) mcpobserve.Call {
		return mcpobserve.Call{
			ID: mcpobserve.NewID(), Direction: "outbound", Server: "fixture", Tool: "query", WireTool: "fixture_query",
			ToolCallID: f.toolID, SessionID: f.sid, OwnerID: owner, AgentID: agent,
			StartedAt: time.Now().Add(-time.Minute).UnixMilli(), Status: "error", ErrorKind: "tool_error",
			ErrorSource: "session", // A caller-supplied provenance claim must not persist.
		}
	}
	calls := []mcpobserve.Call{
		makeCall(ah, alice.SubjectID, a.ID),
		makeCall(bh, bob.SubjectID, a.ID),
		makeCall(otherHistory, alice.SubjectID, otherAgent.ID),
		// Both rows below are visible to Alice in Alpha's telemetry scope, but
		// neither grants access to the transcript it claims as its origin.
		makeCall(bh, alice.SubjectID, a.ID),
		makeCall(otherHistory, alice.SubjectID, a.ID),
	}
	for _, c := range calls {
		h.psrv.mcpCalls.Record(c)
	}
	if err := h.psrv.mcpCalls.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, err := h.psrv.mcpCalls.Query(context.Background(), mcpobserve.Filter{})
	if err != nil || len(before.Calls) != len(calls) {
		t.Fatalf("store fixture did not retain all synthetic rows: %+v, %v", before, err)
	}
	for _, c := range before.Calls {
		if c.ErrorSummary != "" || c.ErrorCode != "" || c.ErrorSource != "" {
			t.Fatalf("fixture already had persisted diagnostic/provenance: %+v", c)
		}
	}
	get := func(token, path string) (int, []byte) {
		t.Helper()
		response := h.doAs(token, http.MethodGet, path, "")
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.Header.Get("Cache-Control") != "no-store" && response.StatusCode == 200 {
			t.Fatal("historical diagnostic detail was cacheable")
		}
		return response.StatusCode, raw
	}
	assertAbsent := func(raw []byte, values ...string) {
		t.Helper()
		for _, value := range values {
			if strings.Contains(string(raw), value) {
				t.Fatalf("history HTTP response exposed a protected synthetic value: %s", value)
			}
		}
	}
	allSensitive := []string{ah.secret, ah.input, ah.payload, bh.secret, bh.input, bh.payload, otherHistory.secret, otherHistory.input, otherHistory.payload}
	prefix := "/api/apps/" + a.ID + "/mcp/calls"
	for _, tc := range []struct {
		name, token, path, reason string
		status                    int
		wantSession               bool
	}{
		{"Alice-own", aliceToken, prefix + "/" + calls[0].ID, ah.reason, 200, true},
		{"Bob-own", bobToken, prefix + "/" + calls[1].ID, bh.reason, 200, true},
		{"Alice-other-Agent-own", aliceToken, "/api/apps/" + otherAgent.ID + "/mcp/calls/" + calls[2].ID, otherHistory.reason, 200, true},
		{"Alice-cannot-open-Bob-row", aliceToken, prefix + "/" + calls[1].ID, "", 404, false},
		{"Bob-cannot-open-Alice-row", bobToken, prefix + "/" + calls[0].ID, "", 404, false},
		{"wrong-Agent-route", aliceToken, "/api/apps/" + otherAgent.ID + "/mcp/calls/" + calls[0].ID, "", 404, false},
		{"owned-row-foreign-session", aliceToken, prefix + "/" + calls[3].ID, "", 200, false},
		{"owned-row-mismatched-Agent-binding", aliceToken, prefix + "/" + calls[4].ID, "", 200, false},
		{"forged-owner-filter", aliceToken, prefix + "/" + calls[0].ID + "?owner_id=" + url.QueryEscape(bob.SubjectID), "", 400, false},
		{"unauthenticated", "", prefix + "/" + calls[0].ID, "", 401, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, raw := get(tc.token, tc.path)
			if code != tc.status {
				t.Fatalf("history detail status %d, want %d: %s", code, tc.status, raw)
			}
			assertAbsent(raw, allSensitive...)
			for _, reason := range []string{ah.reason, bh.reason, otherHistory.reason} {
				if reason != tc.reason {
					assertAbsent(raw, reason)
				}
			}
			if code != 200 {
				return
			}
			var payload struct {
				Call mcpobserve.Call `json:"call"`
			}
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			if tc.wantSession {
				if payload.Call.ErrorSource != "session" || payload.Call.ErrorCode != "HIST_DENIED" || !strings.Contains(payload.Call.ErrorSummary, tc.reason) {
					t.Fatalf("authorized exact historical join did not produce a safe attributed reason: %+v", payload.Call)
				}
			} else if payload.Call.ErrorSource != "" || payload.Call.ErrorSummary != "" || payload.Call.ErrorCode != "" {
				t.Fatalf("visible observation row was mistaken for transcript authority: %+v", payload.Call)
			}
		})
	}
	// Lists/stats do not perform historical recovery, even after a detail has
	// displayed its explanation. Nor can searches probe transcript-only text.
	for _, path := range []string{prefix, prefix + "?q=" + url.QueryEscape(ah.reason), prefix + "/stats"} {
		code, raw := get(aliceToken, path)
		if code != 200 {
			t.Fatalf("history projection changed normal list/stat access: %d %s", code, raw)
		}
		assertAbsent(raw, append(allSensitive, ah.reason, bh.reason, otherHistory.reason, `"error_source":"session"`)...)
	}
	// The global administrator route may view rows across owners, but must still
	// verify each row's actual catalog owner and pinned Agent before recovery.
	h.armUsers(map[string]userRecord{"alice": {Username: "alice", Token: aliceToken, Admin: true}, "bob": {Username: "bob", Token: bobToken}})
	code, raw := get(aliceToken, "/api/mcp/calls/"+calls[1].ID)
	if code != 200 || !strings.Contains(string(raw), bh.reason) || !strings.Contains(string(raw), `"error_source":"session"`) {
		t.Fatalf("authorized global detail did not use the verified row owner: %d %s", code, raw)
	}
	assertAbsent(raw, allSensitive...)
	code, raw = get(aliceToken, "/api/mcp/calls/"+calls[3].ID)
	if code != 200 {
		t.Fatalf("visible invalid historical metadata changed detail addressability: %d %s", code, raw)
	}
	assertAbsent(raw, append(allSensitive, ah.reason, bh.reason, otherHistory.reason, `"error_source":"session"`)...)

	after, err := h.psrv.mcpCalls.Query(context.Background(), mcpobserve.Filter{})
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("detail-only history recovery mutated original observation store: before %+v, after %+v, error %v", before, after, err)
	}
	for path, original := range filesBefore {
		current, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(original, current) {
			t.Fatalf("history recovery modified original session/binding file %s: %v", path, err)
		}
	}
	for _, f := range []fixture{ah, bh, otherHistory} {
		current, err := h.mgr.cfg.Catalog.Get(context.Background(), f.owner, f.sid)
		if err != nil || !reflect.DeepEqual(current, catalogBefore[f.sid]) {
			t.Fatalf("history recovery modified authoritative catalog row: %+v, %v", current, err)
		}
	}
	h.mgr.mu.Lock()
	live, loads := len(h.mgr.sessions), len(h.mgr.sessionLoads)
	h.mgr.mu.Unlock()
	if live != 0 || loads != 0 || len(h.rec.systems()) != 0 {
		t.Fatalf("read-only history recovery reactivated a runtime/model: sessions=%d loads=%d model calls=%d", live, loads, len(h.rec.systems()))
	}
}
