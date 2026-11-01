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
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

func syncStudioSkillsHTTP(t *testing.T, h *harness, aid, sid string) studioSkillSync {
	t.Helper()
	var out studioSkillSync
	if err := decodeJSONBody(h.do(http.MethodPost, "/api/apps/"+aid+"/sessions/"+sid+"/skill-config/sync", `{}`), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func sessionSkillNamesHTTP(t *testing.T, h *harness, sid string) []string {
	t.Helper()
	var out struct {
		Skills []struct {
			Name string `json:"name"`
		} `json:"skills"`
	}
	if err := decodeJSONBody(h.do(http.MethodGet, "/api/skills?session_id="+sid, ""), &out); err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, sk := range out.Skills {
		names = append(names, sk.Name)
	}
	return names
}
func TestStudioSkillPolicyIdleSaveUpdatesRuntimeAndKeepsSession(t *testing.T) {
	dir := t.TempDir()
	aSkill := writeTestSkill(t, dir, "alpha", "ALPHA skill", "# Alpha body")
	bSkill := writeManifestSkill(t, dir, "beta", "BETA skill", "beta.echo")
	h, capture := newStudioHarness(t, []llm.Response{textTurn("done"), textTurn("done")}, func(c *Config) { c.Skills = []skills.Skill{aSkill, bSkill} })
	a := postStudioAgent(t, h, studioAgentCapsBody("Original", "ready", map[string]any{"skills": []string{skillIDOf(aSkill)}, "tools": []string{"read"}, "mcp_servers": []string{}}), 201)
	sid := startStudioConversation(t, h, a.ID)
	s := h.session(sid)
	oldBinding, _ := h.mgr.studio.binding(sid)
	oldAgentJSON, _ := json.Marshal(oldBinding.Agent)
	hub, browser, loop := s.hub, s.browser, s.agent
	before, _ := os.ReadFile(s.Path())
	a = patchStudioPolicy(t, h, a, map[string]any{"skills": []string{skillIDOf(aSkill), skillIDOf(bSkill)}, "name": "New identity", "instructions": "SHOULD NOT REPLACE", "tools": []string{"read", "bash"}})
	if s.hub != hub || s.browser != browser || s.agent != loop {
		t.Fatal("Skill update recreated session state")
	}
	binding, _ := h.mgr.studio.binding(sid)
	snapshot, _ := json.Marshal(binding.Agent)
	if !bytes.Equal(snapshot, oldAgentJSON) || binding.SkillPolicy == nil || binding.SkillPolicy.Revision != a.Revision {
		t.Fatal("Skill policy rewrote original snapshot or did not persist")
	}
	after, _ := os.ReadFile(s.Path())
	if !bytes.Equal(before, after) {
		t.Fatal("selection edit rewrote history")
	}
	if got := sessionSkillNamesHTTP(t, h, sid); !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Fatalf("old chat Skill list=%v", got)
	}
	caps := sessionCapabilities(t, h, a.ID, sid)
	if got := stringField(t, caps, "tools"); !slices.Contains(got, "read") || !slices.Contains(got, "bash") {
		t.Fatal("independently selected native tools were not synchronized")
	}
	if result := syncStudioSkillsHTTP(t, h, a.ID, sid); result.Changed || result.Deferred {
		t.Fatalf("repeat sync=%+v", result)
	}
	studioAsk(t, h, sid, "/skill:beta inspect")
	systems, _, histories, _ := capture.snapshot()
	if !strings.Contains(systems[0], "BETA skill") || strings.Contains(systems[0], "SHOULD NOT REPLACE") {
		t.Fatal("effective prompt and Skill selection differ")
	}
	if !strings.Contains(studioMessageText(histories[0][len(histories[0])-1]), "beta") {
		t.Fatal("new Skill did not expand")
	}
	if !slices.Contains(capture.toolSchemas()[0], "beta_echo") {
		t.Fatal("new Skill tool not registered")
	}
	newSID := startStudioConversation(t, h, a.ID)
	if got := sessionSkillNamesHTTP(t, h, newSID); !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Fatalf("new chat Skills=%v", got)
	}
	a = patchStudioPolicy(t, h, a, map[string]any{"skills": []string{skillIDOf(aSkill)}})
	if got := sessionSkillNamesHTTP(t, h, sid); !slices.Equal(got, []string{"alpha"}) {
		t.Fatalf("removed Skill stayed visible=%v", got)
	}
	if code := studioStatus(t, h, http.MethodPost, "/api/sessions/"+sid+"/messages", `{"prompt":"/skill:beta"}`); code != 400 {
		t.Fatalf("removed Skill still invokes: %d", code)
	}
	studioAsk(t, h, sid, "check current setup")
	if slices.Contains(capture.toolSchemas()[1], "beta_echo") {
		t.Fatal("removed Skill tool stayed registered")
	}
}
func TestStudioSkillPolicyBusyDefersThenAppliesAtIdle(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	dir := t.TempDir()
	sk := writeTestSkill(t, dir, "later", "LATER skill", "# Later")
	h := newHarness(t, func(n int) (llm.Response, error) {
		if n == 1 {
			close(entered)
			<-release
		}
		return textTurn("done"), nil
	}, func(c *Config) { c.Skills = []skills.Skill{sk} })
	a := postStudioAgent(t, h, studioAgentCapsBody("Busy skills", "ready", map[string]any{"skills": []string{}}), 201)
	sid := startStudioConversation(t, h, a.ID)
	s := h.session(sid)
	stream := h.start(sid, "work")
	defer stream.close()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not start")
	}
	a = patchStudioPolicy(t, h, a, map[string]any{"skills": []string{skillIDOf(sk)}})
	if result := syncStudioSkillsHTTP(t, h, a.ID, sid); !result.Deferred || result.Changed {
		t.Fatalf("busy sync=%+v", result)
	}
	if got := sessionSkillNamesHTTP(t, h, sid); len(got) != 0 {
		t.Fatal("GET exposed desired before runtime switch")
	}
	once.Do(func() { close(release) })
	stream.wait(t, EvRunEnd)
	s.wait()
	deadline := time.Now().Add(3 * time.Second)
	for len(sessionSkillNamesHTTP(t, h, sid)) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := sessionSkillNamesHTTP(t, h, sid); !slices.Equal(got, []string{"later"}) {
		t.Fatalf("idle update absent: %v", got)
	}
}
func TestStudioSkillPolicyFailedCommitKeepsOriginalRuntime(t *testing.T) {
	dir := t.TempDir()
	sk := writeTestSkill(t, dir, "later", "LATER skill", "# Later")
	h, _ := newStudioHarness(t, []llm.Response{textTurn("done")}, func(c *Config) { c.Skills = []skills.Skill{sk} })
	a := postStudioAgent(t, h, studioAgentCapsBody("Atomic skills", "ready", map[string]any{"skills": []string{}}), 201)
	sid := startStudioConversation(t, h, a.ID)
	s := h.session(sid)
	next := a.studioInput
	ids := []string{skillIDOf(sk)}
	next.Skills = &ids
	if _, err := h.mgr.studio.put(a.ID, next, a.Revision); err != nil {
		t.Fatal(err)
	}
	before, _ := h.mgr.studio.binding(sid)
	path := h.mgr.studio.path
	backup := path + ".fixture-backup"
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(path); _ = os.Rename(backup, path) }()
	if _, err := s.SyncStudioSkillPolicy(); err == nil {
		t.Fatal("failed persistence accepted")
	}
	after, _ := h.mgr.studio.binding(sid)
	if !reflect.DeepEqual(before, after) || len(s.invocationSkills) != 0 {
		t.Fatal("failed commit partially changed runtime")
	}
}
func TestStudioSkillSyncOwnerAndAgentBoundary(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	var in studioInput
	if err := json.Unmarshal([]byte(studioBody(studioAgentCapsBody("Owner", "ready", nil))), &in); err != nil {
		t.Fatal(err)
	}
	a, err := h.mgr.studio.put("", in, 0)
	if err != nil {
		t.Fatal(err)
	}
	sid := h.studioConversationAs(aliceToken, a.ID)
	before, _ := h.mgr.studio.binding(sid)
	for _, tc := range []struct{ token, aid string }{{bobToken, a.ID}, {aliceToken, "ag_other"}} {
		r := h.doAs(tc.token, http.MethodPost, "/api/apps/"+tc.aid+"/sessions/"+sid+"/skill-config/sync", `{}`)
		r.Body.Close()
		if r.StatusCode != 404 {
			t.Fatalf("foreign sync=%d", r.StatusCode)
		}
	}
	after, _ := h.mgr.studio.binding(sid)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("foreign sync changed authority")
	}
}

func TestStudioSkillPolicyRestoresOnReloadAndStartWithoutUI(t *testing.T) {
	dir := t.TempDir()
	first := writeTestSkill(t, dir, "first", "FIRST skill", "# First")
	nextSkill := writeTestSkill(t, dir, "second", "SECOND skill", "# Second")
	mutate := func(c *Config) { c.Skills = []skills.Skill{first, nextSkill} }
	h, _ := newStudioHarness(t, []llm.Response{textTurn("done")}, mutate)
	a := postStudioAgent(t, h, studioAgentCapsBody("Reload skills", "ready", map[string]any{"skills": []string{skillIDOf(first)}}), 201)
	sid := startStudioConversation(t, h, a.ID)
	a = patchStudioPolicy(t, h, a, map[string]any{"skills": []string{skillIDOf(first), skillIDOf(nextSkill)}})
	h.mgr.Close()
	h2, cap := newStudioHarness(t, []llm.Response{textTurn("restored")}, func(c *Config) { mutate(c); c.SessionDir = h.mgr.cfg.SessionDir })
	s := h2.session(sid)
	if got := sessionSkillNamesHTTP(t, h2, sid); !slices.Equal(got, []string{"first", "second"}) {
		t.Fatalf("reloaded selection=%v", got)
	}
	input := a.studioInput
	ids := []string{skillIDOf(nextSkill)}
	input.Skills = &ids
	if _, err := h2.mgr.studio.put(a.ID, input, a.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start("/skill:second inspect", nil, ""); err != nil {
		t.Fatal(err)
	}
	s.wait()
	if got := sessionSkillNamesHTTP(t, h2, sid); !slices.Equal(got, []string{"second"}) {
		t.Fatalf("start retained stale selection=%v", got)
	}
	systems, _, _, _ := cap.snapshot()
	if len(systems) != 1 || strings.Contains(systems[0], "FIRST skill") || !strings.Contains(systems[0], "SECOND skill") {
		t.Fatal("reload/start runtime prompt differs from effective catalog")
	}
}
func TestStudioSkillPolicyRetainsRuntimeLockAcrossOrdinarySkillEditAndReload(t *testing.T) {
	h, _, conversation, runtimeID := runtimeSkillObserveFixture(t, []llm.Response{textTurn("done")})
	sk := writeTestSkill(t, t.TempDir(), "added", "ADDED skill", "# Added")
	h.mgr.SetSkills([]skills.Skill{sk})
	s := h.session(conversation.ID)
	before, _ := h.mgr.studio.binding(s.ID)
	runtime := s.runtimeSkill
	a, _ := h.mgr.studio.get(before.Agent.ID)
	a = patchStudioPolicy(t, h, a, map[string]any{"skills": []string{runtimeID, skillIDOf(sk)}})
	after, _ := h.mgr.studio.binding(s.ID)
	effective := after.effectiveSkillBinding()
	if s.runtimeSkill != runtime || !bytes.Equal(effective.SkillLock, before.SkillLock) || !bytes.Equal(effective.Agent.SkillBinding, before.Agent.SkillBinding) {
		t.Fatal("ordinary selection edit replaced pinned runtime authority")
	}
	if !slices.Contains(sessionSkillNamesHTTP(t, h, s.ID), "added") {
		t.Fatal("ordinary skill absent beside runtime package")
	}
	h.mgr.Close()
	h2, _ := newStudioHarness(t, []llm.Response{textTurn("done")}, func(c *Config) {
		c.SessionDir = h.mgr.cfg.SessionDir
		c.SkillRuntime = h.mgr.cfg.SkillRuntime
		c.Skills = []skills.Skill{sk}
	})
	restored := h2.session(s.ID)
	if restored.runtimeSkill == nil || !reflect.DeepEqual(runtime.Descriptors(), restored.runtimeSkill.Descriptors()) {
		t.Fatal("Skill policy runtime lock did not restore")
	}
	_ = a
}

func TestStudioSkillPolicyWaitsForBackgroundChildWithoutCancellingIt(t *testing.T) {
	dir := t.TempDir()
	sk := writeTestSkill(t, dir, "added", "ADDED skill", "# Added")
	h, _ := newStudioHarness(t, []llm.Response{textTurn("unused")}, func(c *Config) { c.Skills = []skills.Skill{sk} })
	a := postStudioAgent(t, h, studioAgentCapsBody("Background boundary", "ready", map[string]any{"skills": []string{}}), 201)
	sid := startStudioConversation(t, h, a.ID)
	s := h.session(sid)
	work := t.TempDir()
	release := filepath.Join(work, "release")
	program := filepath.Join(work, "child")
	source := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"turn_start\",\"turn\":1}'\nwhile [ ! -f '" + release + "' ]; do sleep 0.02; done\nprintf '%s\\n' '{\"type\":\"token\",\"text\":\"finished original grant\"}' '{\"type\":\"run_end\",\"stop_reason\":\"end_turn\"}'\n"
	if err := os.WriteFile(program, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	bg := tools.NewBgRunner(context.Background(), tools.BgHooks{})
	defer bg.Close()
	s.mu.Lock()
	original := s.bg
	s.bg = bg
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.bg = original; s.mu.Unlock() }()
	child := &tools.Subagent{Cwd: work, Exe: program, Bg: bg}
	if _, err := child.Execute(context.Background(), json.RawMessage(`{"mode":"explore","task":"retain old grants"}`)); err != nil {
		t.Fatal(err)
	}
	if !s.BgActive() {
		t.Fatal("background process not registered")
	}
	a = patchStudioPolicy(t, h, a, map[string]any{"skills": []string{skillIDOf(sk)}})
	if result := syncStudioSkillsHTTP(t, h, a.ID, sid); !result.Deferred || result.Changed {
		t.Fatalf("background update=%+v", result)
	}
	if !s.BgActive() {
		t.Fatal("Skill update cancelled running background child")
	}
	if got := sessionSkillNamesHTTP(t, h, sid); len(got) != 0 {
		t.Fatal("new grants exposed before child settled")
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for s.BgActive() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.BgActive() {
		t.Fatal("original background child could not finish")
	}
	if result := syncStudioSkillsHTTP(t, h, a.ID, sid); result.Deferred || !result.Changed {
		t.Fatalf("child finished but update stayed deferred: %+v", result)
	}
}

func TestStudioSkillPolicyMultipleAtomsKeepOrderAndRejectRemoved(t *testing.T) {
	first := writeTestSkill(t, t.TempDir(), "first", "first", "FIRST_HOST_BODY")
	second := writeTestSkill(t, t.TempDir(), "second", "second", "SECOND_HOST_BODY")
	h, capture := newStudioHarness(t, []llm.Response{textTurn("done")}, func(c *Config) { c.Skills = []skills.Skill{first, second} })
	a := postStudioAgent(t, h, studioAgentCapsBody("Atoms", "ready", map[string]any{"skills": []string{skillIDOf(first)}}), 201)
	sid := startStudioConversation(t, h, a.ID)
	a = patchStudioPolicy(t, h, a, map[string]any{"skills": []string{skillIDOf(first), skillIDOf(second)}})
	parts := []MessageInputPart{{Type: "text", Text: "先 "}, {Type: "skill", Name: "first"}, {Type: "text", Text: " 再 "}, {Type: "skill", Name: "second"}, {Type: "text", Text: " 完成"}}
	body, _ := json.Marshal(map[string]any{"parts": parts, "input_layout": "inline"})
	stream := h.stream(sid, "")
	defer stream.close()
	stream.next(t)
	h.post("/api/sessions/"+sid+"/messages", string(body), 202)
	message := stream.wait(t, EvUserMessage)
	stream.wait(t, EvRunEnd)
	h.session(sid).wait()
	if len(message.Content) != 5 || message.Content[1].InputSkill != "first" || message.Content[3].InputSkill != "second" {
		t.Fatal("inline Skill order not retained")
	}
	_, _, histories, _ := capture.snapshot()
	blocks := histories[0][len(histories[0])-1].Content
	if len(blocks) != 5 || !strings.Contains(blocks[1].Text, "FIRST_HOST_BODY") || !strings.Contains(blocks[3].Text, "SECOND_HOST_BODY") {
		t.Fatal("ordered Skill bodies did not reach model input")
	}
	a = patchStudioPolicy(t, h, a, map[string]any{"skills": []string{skillIDOf(first)}})
	h.post("/api/sessions/"+sid+"/messages", string(body), 400)
}

func TestStudioSkillPolicyRevokesCapturedAgentMCPToolCatalog(t *testing.T) {
	sk := writeTestSkill(t, t.TempDir(), "selected", "selected", "SELECTED_BODY")
	h, _ := newStudioHarness(t, []llm.Response{textTurn("unused")}, func(c *Config) { c.Skills = []skills.Skill{sk} })
	a := postStudioAgent(t, h, studioAgentCapsBody("MCP grants", "ready", map[string]any{"skills": []string{skillIDOf(sk)}}), 201)
	sess, err := h.mgr.createStudioBoundConversation(a, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	layer := newMCPLayer(h.mgr, MCPOptions{Policy: "auto"}, true, nil)
	layer.agentID = a.ID
	layer.exportSnapshots = map[string]agentToolSnapshot{}
	listing, _ := json.Marshal(map[string]string{"session_id": sess.ID})
	out, err := layer.agentListTools(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "agent_list_tools", Arguments: listing}})
	if err != nil || out.IsError {
		t.Fatalf("list tools failed: %v %+v", err, out)
	}
	revision := layer.exportSnapshots[sess.ID].revision
	a = patchStudioPolicy(t, h, a, map[string]any{"skills": []string{}})
	invocation, _ := json.Marshal(map[string]any{"session_id": sess.ID, "catalog_revision": revision, "name": "read", "arguments": map[string]string{"path": "unused.txt"}})
	out, err = layer.agentCallTool(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "agent_call_tool", Arguments: invocation}})
	if err != nil || !out.IsError {
		t.Fatalf("old Skill catalog remained callable: %v %+v", err, out)
	}
	raw, _ := json.Marshal(out)
	if !strings.Contains(string(raw), "catalog is stale") {
		t.Fatalf("wrong rejection: %s", raw)
	}
}
