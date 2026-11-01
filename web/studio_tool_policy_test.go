package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

func syncStudioToolsHTTP(t *testing.T, h *harness, aid, sid string) studioToolSync {
	t.Helper()
	var out studioToolSync
	if err := decodeJSONBody(h.do(http.MethodPost, "/api/apps/"+aid+"/sessions/"+sid+"/tools-config/sync", `{}`), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func nativeCatalogHTTP(t *testing.T, h *harness, sid string) []string {
	t.Helper()
	var out struct {
		Builtin []tools.BuiltinInfo `json:"builtin"`
	}
	if err := decodeJSONBody(h.do(http.MethodGet, "/api/tools?session_id="+sid, ""), &out); err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, row := range out.Builtin {
		names = append(names, row.Name)
	}
	return names
}
func TestStudioToolPolicyIdleRealCreateRemoveAndNoHistoryRewrite(t *testing.T) {
	h, capture := newStudioHarness(t, []llm.Response{toolTurn(tools.ShortcutManageName, `{"action":"create","request_id":"owned-native-policy","root_id":"root","nodes":[{"local_id":"root","title":"Created after sync","parts":[{"kind":"text","text":"synthetic test"}]}]}`), textTurn("saved"), textTurn("revoked")})
	a := postStudioAgent(t, h, studioAgentCapsBody("Original", "ready", map[string]any{"tools": []string{"read"}}), 201)
	sid := startStudioConversation(t, h, a.ID)
	s := h.session(sid)
	before, _ := os.ReadFile(s.Path())
	binding, _ := h.mgr.studio.binding(sid)
	hub, browser, loop := s.hub, s.browser, s.agent
	a = patchStudioPolicy(t, h, a, map[string]any{"tools": []string{tools.ShortcutCreateName}, "name": "Changed title", "instructions": "NEW INSTRUCTIONS MUST NOT REPLACE"})
	if s.hub != hub || s.browser != browser || s.agent != loop {
		t.Fatal("tool sync recreated session")
	}
	after, _ := os.ReadFile(s.Path())
	if !bytes.Equal(before, after) {
		t.Fatal("tool sync rewrote transcript")
	}
	next, _ := h.mgr.studio.binding(sid)
	if !reflect.DeepEqual(binding.Agent, next.Agent) || next.ToolPolicy == nil || next.toolsRevision() != a.Revision {
		t.Fatal("original binding changed or policy not persisted")
	}
	for _, name := range []string{"read", "ls", "find", "grep", tools.ShortcutQueryName, tools.ShortcutManageName} {
		if !slices.Contains(nativeCatalogHTTP(t, h, sid), name) {
			t.Fatalf("effective catalog missing %s", name)
		}
	}
	state := syncStudioToolsHTTP(t, h, a.ID, sid)
	if state.Changed || state.Deferred || state.Revision != a.Revision {
		t.Fatalf("duplicate sync: %+v", state)
	}
	studioAsk(t, h, sid, "create the synthetic shortcut")
	view, err := h.mgr.userShortcuts(s.owner, a.ID)
	if err != nil || len(view.Shortcuts) != 1 || view.Shortcuts[0].Title != "Created after sync" {
		t.Fatal("real create did not execute", view, err)
	}
	if !slices.Contains(capture.toolSchemas()[0], tools.ShortcutManageName) {
		t.Fatal("model did not receive actual create schema")
	}
	a = patchStudioPolicy(t, h, a, map[string]any{"tools": []string{}})
	if slices.Contains(nativeCatalogHTTP(t, h, sid), tools.ShortcutManageName) {
		t.Fatal("removed tool stayed advertised")
	}
	studioAsk(t, h, sid, "show setup after revocation")
	if slices.Contains(capture.toolSchemas()[len(capture.toolSchemas())-1], tools.ShortcutManageName) {
		t.Fatal("removed tool stayed registered")
	}
	if len(s.agent.Messages()) == 0 {
		t.Fatal("history lost")
	}
}

func TestStudioToolPolicyBusyLatestRevisionThenIdle(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	h := newHarness(t, func(n int) (llm.Response, error) {
		if n == 1 {
			close(entered)
			<-release
		}
		return textTurn("done"), nil
	})
	a := postStudioAgent(t, h, studioAgentCapsBody("Busy", "ready", map[string]any{"tools": []string{}}), 201)
	sid := startStudioConversation(t, h, a.ID)
	s := h.session(sid)
	stream := h.start(sid, "work")
	defer stream.close()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider not started")
	}
	a = patchStudioPolicy(t, h, a, map[string]any{"tools": []string{tools.ShortcutCreateName, "bash"}})
	a = patchStudioPolicy(t, h, a, map[string]any{"tools": []string{tools.ShortcutCreateName}})
	state := syncStudioToolsHTTP(t, h, a.ID, sid)
	if !state.Deferred || state.Changed || slices.Contains(state.Tools, tools.ShortcutManageName) {
		t.Fatalf("premature update %+v", state)
	}
	caps := sessionCapabilities(t, h, a.ID, sid)
	if caps["tools_pending"] != true || slices.Contains(stringField(t, caps, "tools"), tools.ShortcutManageName) {
		t.Fatal("capabilities exposed desired grants")
	}
	once.Do(func() { close(release) })
	stream.wait(t, EvRunEnd)
	s.wait()
	deadline := time.Now().Add(3 * time.Second)
	for {
		state = syncStudioToolsHTTP(t, h, a.ID, sid)
		if !state.Deferred && state.Revision == a.Revision {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("idle tools not committed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !slices.Contains(state.Tools, tools.ShortcutManageName) || slices.Contains(state.Tools, "bash") {
		t.Fatalf("wrong final revision %+v", state)
	}
}

func TestStudioToolPolicyApprovalWaitPreservesOldCall(t *testing.T) {
	h, capture := newStudioHarness(t, []llm.Response{toolTurn("bash", `{"command":"printf old-approved-call"}`), textTurn("done")})
	a := postStudioAgent(t, h, studioAgentCapsBody("Approval", "ready", map[string]any{"tools": []string{"bash"}}), 201)
	sid := startStudioConversation(t, h, a.ID)
	s := h.session(sid)
	s.Policy().Set(ModeStrict, 0)
	stream := h.start(sid, "run original command")
	defer stream.close()
	gate := stream.wait(t, EvGateRequest)
	a = patchStudioPolicy(t, h, a, map[string]any{"tools": []string{tools.ShortcutCreateName}})
	if state := syncStudioToolsHTTP(t, h, a.ID, sid); !state.Deferred || !slices.Contains(state.Tools, "bash") || slices.Contains(state.Tools, tools.ShortcutManageName) {
		t.Fatal("approval wait lost original registry")
	}
	if err := s.Gate().Decide(gate.GateID, Verdict{Allow: true}); err != nil {
		t.Fatal(err)
	}
	stream.wait(t, EvRunEnd)
	s.wait()
	state := syncStudioToolsHTTP(t, h, a.ID, sid)
	if state.Deferred || slices.Contains(state.Tools, "bash") || !slices.Contains(state.Tools, tools.ShortcutManageName) {
		t.Fatal("approval completion did not switch")
	}
	if _, auto := s.Policy().Decide(agent.GateRequest{ToolName: tools.ShortcutManageName}); auto {
		t.Fatal("strict approval was weakened")
	}
	if !slices.Contains(capture.toolSchemas()[0], "bash") {
		t.Fatal("old approved call lost its schema")
	}
}

func TestStudioToolPolicyCommitFailureRestoreAndSetSemantics(t *testing.T) {
	h, _ := newStudioHarness(t, []llm.Response{textTurn("unused")})
	a := postStudioAgent(t, h, studioAgentCapsBody("Atomic", "ready", map[string]any{"tools": []string{}}), 201)
	sid := startStudioConversation(t, h, a.ID)
	s := h.session(sid)
	input := a.studioInput
	want := []string{tools.ShortcutCreateName}
	input.Tools = &want
	a, err := h.mgr.studio.put(a.ID, input, a.Revision)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := h.mgr.studio.binding(sid)
	names := s.agent.NativeToolNames()
	path := h.mgr.studio.path
	backup := path + ".backup"
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SyncStudioToolPolicy(); err == nil {
		t.Fatal("failed persistence accepted")
	}
	after, _ := h.mgr.studio.binding(sid)
	if !reflect.DeepEqual(before, after) || !slices.Equal(names, s.agent.NativeToolNames()) {
		t.Fatal("failed commit partially applied")
	}
	_ = os.Remove(path)
	_ = os.Rename(backup, path)
	if state := syncStudioToolsHTTP(t, h, a.ID, sid); !state.Changed {
		t.Fatal("retry did not apply")
	}
	a = patchStudioPolicy(t, h, a, map[string]any{"name": "Only name changes", "tools": []string{"read", tools.ShortcutCreateName, "ls"}})
	if state := syncStudioToolsHTTP(t, h, a.ID, sid); state.Changed || state.Deferred {
		t.Fatal("protected/order-only edit became pending")
	}
	a = patchStudioPolicy(t, h, a, map[string]any{"tools": []string{"ls", tools.ShortcutCreateName, "read"}})
	if state := syncStudioToolsHTTP(t, h, a.ID, sid); state.Changed || state.Deferred {
		t.Fatal("tool reorder changed permission")
	}
	// A dormant session is built from its persisted effective policy, then
	// reconciles any later change before Get returns.
	h.mgr.mu.Lock()
	delete(h.mgr.sessions, sid)
	h.mgr.mu.Unlock()
	s.closeUnpublished()
	loaded, err := h.mgr.Get(sid)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(loaded.agent.NativeToolNames(), tools.ShortcutManageName) {
		t.Fatal("restored policy lost")
	}
	input = a.studioInput
	empty := []string{}
	input.Tools = &empty
	if _, err := h.mgr.studio.put(a.ID, input, a.Revision); err != nil {
		t.Fatal(err)
	}
	h.mgr.mu.Lock()
	delete(h.mgr.sessions, sid)
	h.mgr.mu.Unlock()
	loaded.closeUnpublished()
	loaded, err = h.mgr.Get(sid)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(loaded.agent.NativeToolNames(), tools.ShortcutManageName) {
		t.Fatal("restore did not reconcile latest policy")
	}
}

func TestStudioToolSyncOwnerAndAgentBoundary(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	var in studioInput
	if err := json.Unmarshal([]byte(studioBody(studioAgentCapsBody("Owner", "ready", map[string]any{"tools": []string{}}))), &in); err != nil {
		t.Fatal(err)
	}
	a, err := h.mgr.studio.put("", in, 0)
	if err != nil {
		t.Fatal(err)
	}
	sid := h.studioConversationAs(aliceToken, a.ID)
	before, _ := h.mgr.studio.binding(sid)
	for _, tc := range []struct{ token, aid string }{{bobToken, a.ID}, {aliceToken, "ag_other"}} {
		r := h.doAs(tc.token, http.MethodPost, "/api/apps/"+tc.aid+"/sessions/"+sid+"/tools-config/sync", `{}`)
		r.Body.Close()
		if r.StatusCode != 404 {
			t.Fatalf("foreign sync=%d", r.StatusCode)
		}
	}
	after, _ := h.mgr.studio.binding(sid)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("foreign authority changed")
	}
}

func TestStudioToolPolicyBackgroundChildDefersWithoutCancellation(t *testing.T) {
	h, _ := newStudioHarness(t, []llm.Response{textTurn("unused")})
	a := postStudioAgent(t, h, studioAgentCapsBody("Child", "ready", map[string]any{"tools": []string{}}), 201)
	sid := startStudioConversation(t, h, a.ID)
	s := h.session(sid)
	work := t.TempDir()
	release := filepath.Join(work, "release")
	exe := filepath.Join(work, "child")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nwhile [ ! -f '"+release+"' ]; do sleep 0.02; done\nprintf '%s\\n' '{\"type\":\"run_end\",\"stop_reason\":\"end_turn\"}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	bg := tools.NewBgRunner(context.Background(), tools.BgHooks{})
	defer bg.Close()
	s.mu.Lock()
	original := s.bg
	s.bg = bg
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.bg = original; s.mu.Unlock() }()
	child := &tools.Subagent{Cwd: work, Exe: exe, Bg: bg}
	if _, err := child.Execute(context.Background(), json.RawMessage(`{"mode":"explore","task":"old grants"}`)); err != nil {
		t.Fatal(err)
	}
	a = patchStudioPolicy(t, h, a, map[string]any{"tools": []string{tools.ShortcutCreateName}})
	if state := syncStudioToolsHTTP(t, h, a.ID, sid); !state.Deferred || !s.BgActive() {
		t.Fatal("running child cancelled or grants changed")
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for s.BgActive() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if state := syncStudioToolsHTTP(t, h, a.ID, sid); state.Deferred || !state.Changed {
		t.Fatal("settled child left policy pending")
	}
}

func TestStudioToolPolicyBrowserAndOfficeLinksFollowEffectiveSelection(t *testing.T) {
	sk := writeTestSkill(t, t.TempDir(), "office", "Office", "Office tool")
	h, _ := newStudioHarness(t, []llm.Response{textTurn("unused")}, func(c *Config) {
		c.Skills = append([]skills.Skill{sk}, bindingTestSkills(t)...)
		c.BrowserURL = "http://127.0.0.1:9"
		c.BrowserToken = "test-only"
	})
	browserSkillID := bindingSkillID(t, h.mgr.Skills(), "browser-use")
	link := &officeLink{}
	h.mgr.officeForSession = func(string, string, *identity.Scope, *Hub) tools.OfficeLink { return link }
	a := postStudioAgent(t, h, studioAgentCapsBody("Links", "ready", map[string]any{"skills": []string{skillIDOf(sk)}, "tools": []string{}}), 201)
	_, err := h.mgr.replaceBrowserBookmarks(nil, 0, []browserBookmark{{ID: "own", Title: "Own Agent", URL: "https://synthetic.example/"}}, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	sid := startStudioConversation(t, h, a.ID)
	s := h.session(sid)
	originalBrowser := s.browser
	native := s.nativeBrowser
	if s.browserToolEnabled() || link.commandEnabled {
		t.Fatal("withheld tool became active")
	}
	a = patchStudioPolicy(t, h, a, map[string]any{
		"skills": []string{skillIDOf(sk), browserSkillID}, "tools": []string{"browser", "bash"},
		"skill_tool_bindings": []string{"browser-use"},
	})
	if !s.browserToolEnabled() || !link.commandEnabled || s.browser != originalBrowser || s.nativeBrowser != native {
		t.Fatal("links not updated in place")
	}
	reply, err := s.browser.Execute(context.Background(), json.RawMessage(`{"action":"bookmarks"}`))
	var view browserBookmarkView
	if err != nil || json.Unmarshal(reply.Data, &view) != nil || len(view.Bookmarks) != 1 || view.Bookmarks[0].Title != "Own Agent" || s.browser.Armed() {
		t.Fatal("late browser grant lost scoped bookmarks", err)
	}
	s.browser.Arm()
	a = patchStudioPolicy(t, h, a, map[string]any{"skills": []string{skillIDOf(sk)}, "tools": []string{}})
	if s.browserToolEnabled() || link.commandEnabled || s.browser.Armed() {
		t.Fatal("revocation left a channel armed")
	}
}

func TestStudioToolPolicySetDiffAndStaleRevision(t *testing.T) {
	previous := []string{"browser", "bash", "edit", "write", tools.ShortcutCreateName, tools.SkillSaveName}
	// A nil selection inherits today's complete catalog, including the
	// compatibility grants that are not separate picker entries.
	all := explicitNativeSelection(nil, true)
	nilBinding := studioBinding{}
	if !toolPolicyDiffers(nilBinding, studioAgent{studioInput: studioInput{Tools: &previous}}) {
		t.Fatal("an existing explicit selection must not silently gain newly added management permissions")
	}
	if toolPolicyDiffers(nilBinding, studioAgent{studioInput: studioInput{Tools: &all}}) {
		t.Fatal("equal inherited/catalog-complete grants differed")
	}
	one := []string{tools.ShortcutCreateName, "read"}
	reordered := []string{"ls", tools.ShortcutCreateName, "read"}
	binding := studioBinding{Agent: studioAgent{studioInput: studioInput{Tools: &one}}}
	if toolPolicyDiffers(binding, studioAgent{studioInput: studioInput{Name: "Other", Model: "different-model", Tools: &reordered}}) {
		t.Fatal("name/model/protected/order changed native grants")
	}
	h, _ := newStudioHarness(t, []llm.Response{textTurn("unused")})
	a := postStudioAgent(t, h, studioAgentCapsBody("Concurrent", "ready", map[string]any{"tools": []string{}}), 201)
	sid := startStudioConversation(t, h, a.ID)
	old := a.Revision
	a = patchStudioPolicy(t, h, a, map[string]any{"tools": []string{tools.ShortcutCreateName}})
	before, _ := h.mgr.studio.binding(sid)
	if err := h.mgr.studio.commitToolPolicy(sid, old, &studioToolPolicy{Tools: &all, Revision: old}); err == nil {
		t.Fatal("stale writer accepted")
	}
	after, _ := h.mgr.studio.binding(sid)
	if !reflect.DeepEqual(before, after) || after.toolsRevision() != a.Revision {
		t.Fatal("stale write replaced latest revision")
	}
}

func TestStudioToolPolicyRevokesCapturedAgentMCPCatalog(t *testing.T) {
	sk := writeTestSkill(t, t.TempDir(), "selected", "selected", "SYNTHETIC")
	h, _ := newStudioHarness(t, []llm.Response{textTurn("unused")}, func(c *Config) { c.Skills = []skills.Skill{sk} })
	a := postStudioAgent(t, h, studioAgentCapsBody("MCP boundary", "ready", map[string]any{"skills": []string{skillIDOf(sk)}, "tools": []string{}}), 201)
	s, err := h.mgr.createStudioBoundConversation(a, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	l := newMCPLayer(h.mgr, MCPOptions{Policy: "auto"}, true, nil)
	l.agentID = a.ID
	l.exportSnapshots = map[string]agentToolSnapshot{}
	raw, _ := json.Marshal(map[string]string{"session_id": s.ID})
	out, err := l.agentListTools(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "agent_list_tools", Arguments: raw}})
	if err != nil || out.IsError {
		t.Fatal(err, out)
	}
	revision := l.exportSnapshots[s.ID].revision
	a = patchStudioPolicy(t, h, a, map[string]any{"tools": []string{tools.ShortcutCreateName}})
	raw, _ = json.Marshal(map[string]any{"session_id": s.ID, "catalog_revision": revision, "name": "read", "arguments": map[string]string{"path": "unused"}})
	out, err = l.agentCallTool(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "agent_call_tool", Arguments: raw}})
	encoded, _ := json.Marshal(out)
	if err != nil || !out.IsError || !strings.Contains(string(encoded), "catalog is stale") {
		t.Fatal("captured catalog survived native boundary change", err, string(encoded))
	}
}
