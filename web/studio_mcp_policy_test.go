package web

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/mcpclient"
	"github.com/yosukeno/pogo/tools"
)

func newStudioMCPPolicyHarness(t *testing.T, trusted bool) (*harness, *catalogModel, studioAgent, *Session) {
	t.Helper()
	alpha, beta := newWebCatalogFixture(t, false, true), newWebCatalogFixture(t, false, true)
	store := filepath.Join(t.TempDir(), "mcp.json")
	if err := mcpclient.Save(store, mcpclient.Config{Servers: map[string]*mcpclient.ServerConfig{
		"alpha": {URL: alpha.url, Trust: trusted, StartupTimeoutMs: 5000},
		"beta":  {URL: beta.url, Trust: trusted, StartupTimeoutMs: 5000},
	}}); err != nil {
		t.Fatal(err)
	}
	model := &catalogModel{requests: make(chan catalogModelCall, 16)}
	h := newHarness(t, nil, func(c *Config) {
		c.MCPClientStore = store
		c.NewClient = func(config.Resolved, func(llm.RetryInfo)) llm.Client { return model }
	})
	waitCatalog(t, h.mgr.mcpPool, mcpToolPresent("mcp_alpha_echo"))
	waitCatalog(t, h.mgr.mcpPool, mcpToolPresent("mcp_beta_echo"))
	a := postStudioAgent(t, h, studioAgentCapsBody("Live MCP selection", "ready", map[string]any{"tools": []string{"read"}, "skills": []string{}, "mcp_servers": []string{"alpha"}}), http.StatusCreated)
	s := h.session(startStudioConversation(t, h, a.ID))
	return h, model, a, s
}

func syncStudioMCPHTTP(t *testing.T, h *harness, aid, sid string) studioMCPSync {
	t.Helper()
	var out studioMCPSync
	if err := decodeJSONBody(h.do(http.MethodPost, "/api/apps/"+aid+"/sessions/"+sid+"/mcp-config/sync", `{}`), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func studioMCPGroupsHTTP(t *testing.T, h *harness, sid string) []string {
	t.Helper()
	var out struct {
		MCP []struct {
			Name string `json:"name"`
		} `json:"mcp"`
	}
	if err := decodeJSONBody(h.do(http.MethodGet, "/api/tools?session_id="+sid, ""), &out); err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, group := range out.MCP {
		names = append(names, group.Name)
	}
	return names
}

func waitStudioMCPPolicy(t *testing.T, s *Session, names []string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.mu.Lock()
		current := maps.Clone(s.mcpOnly)
		binding, _ := s.mgr.studio.binding(s.ID)
		s.mu.Unlock()
		want := mcpSelectionOnly(&names)
		if binding.MCPPolicy != nil && maps.Equal(current, want) && maps.Equal(mcpSelectionOnly(binding.effectiveMCPServers()), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("MCP policy stayed stale: runtime=%v, effective=%v", current, binding.effectiveMCPServers())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStudioMCPPolicyIdleSaveUpdatesRuntimeCatalogAndKeepsSession(t *testing.T) {
	h, model, a, s := newStudioMCPPolicyHarness(t, true)
	first := startCatalogRun(t, s, model)
	modelSchema(t, first, "mcp_alpha_echo")
	withoutModelTool(t, first, "mcp_beta_echo")
	finishCatalogRun(t, s, first)
	s.wait()
	before, _ := h.mgr.studio.binding(s.ID)
	agentJSON, _ := json.Marshal(before.Agent)
	history, _ := os.ReadFile(s.Path())
	loop, hub, browser := s.agent, s.hub, s.browser
	s.Policy().AllowTool("mcp_alpha_echo")
	a = patchStudioPolicy(t, h, a, map[string]any{"mcp_servers": []string{"beta"}})
	if s.agent != loop || s.hub != hub || s.browser != browser {
		t.Fatal("MCP update recreated the conversation")
	}
	after, _ := h.mgr.studio.binding(s.ID)
	afterJSON, _ := json.Marshal(after.Agent)
	if !bytes.Equal(agentJSON, afterJSON) || after.MCPPolicy == nil || after.MCPPolicy.Revision != a.Revision {
		t.Fatal("MCP update lost its policy or rewrote the original Agent snapshot")
	}
	newHistory, _ := os.ReadFile(s.Path())
	if !bytes.Equal(history, newHistory) {
		t.Fatal("MCP update rewrote chat history")
	}
	if got := stringField(t, sessionCapabilities(t, h, a.ID, s.ID), "mcp_servers"); !slices.Equal(got, []string{"beta"}) {
		t.Fatalf("old conversation capabilities=%v", got)
	}
	if got := studioMCPGroupsHTTP(t, h, s.ID); !slices.Equal(got, []string{"beta"}) {
		t.Fatalf("old conversation catalog=%v", got)
	}
	s.policy.mu.Lock()
	remembered, trusted := s.policy.allowTool["mcp_alpha_echo"], s.policy.mcpTrusted["mcp_alpha_echo"]
	s.policy.mu.Unlock()
	if remembered || trusted {
		t.Fatal("removed MCP retained remembered approval or server trust")
	}
	second := startCatalogRun(t, s, model)
	modelSchema(t, second, "mcp_beta_echo")
	withoutModelTool(t, second, "mcp_alpha_echo")
	finishCatalogRun(t, s, second)
	newSID := startStudioConversation(t, h, a.ID)
	if got := studioMCPGroupsHTTP(t, h, newSID); !slices.Equal(got, []string{"beta"}) {
		t.Fatalf("new conversation catalog differs=%v", got)
	}
	a = patchStudioPolicy(t, h, a, map[string]any{"mcp_servers": []string{}})
	if got := studioMCPGroupsHTTP(t, h, s.ID); len(got) != 0 {
		t.Fatalf("explicit removal still advertises servers=%v", got)
	}
	third := startCatalogRun(t, s, model)
	withoutModelTool(t, third, "mcp_alpha_echo")
	withoutModelTool(t, third, "mcp_beta_echo")
	finishCatalogRun(t, s, third)
}

func TestStudioMCPPolicyBusyDefersWithoutChangingRunAndAppliesAtIdle(t *testing.T) {
	h, model, a, s := newStudioMCPPolicyHarness(t, true)
	active := startCatalogRun(t, s, model)
	a = patchStudioPolicy(t, h, a, map[string]any{"mcp_servers": []string{"beta"}})
	state := syncStudioMCPHTTP(t, h, a.ID, s.ID)
	if !state.Deferred || state.Changed || !slices.Equal(state.MCPServers, []string{"alpha"}) {
		t.Fatalf("busy policy=%+v", state)
	}
	if got := studioMCPGroupsHTTP(t, h, s.ID); !slices.Equal(got, []string{"alpha"}) {
		t.Fatalf("busy catalog exposed desired selection=%v", got)
	}
	modelSchema(t, active, "mcp_alpha_echo")
	withoutModelTool(t, active, "mcp_beta_echo")
	finishCatalogRun(t, s, active)
	waitStudioMCPPolicy(t, s, []string{"beta"})
	next := startCatalogRun(t, s, model)
	modelSchema(t, next, "mcp_beta_echo")
	withoutModelTool(t, next, "mcp_alpha_echo")
	finishCatalogRun(t, s, next)
}

func TestStudioMCPPolicyApprovalKeepsApprovedCallUntilItSettles(t *testing.T) {
	h, model, a, s := newStudioMCPPolicyHarness(t, false)
	s.Policy().Set(ModeStrict, 0)
	stream := h.start(s.ID, "run original external tool")
	defer stream.close()
	first := model.next(t)
	first.reply <- toolTurn("mcp_alpha_echo", `{"text":"approved original"}`)
	gate := stream.wait(t, EvGateRequest)
	a = patchStudioPolicy(t, h, a, map[string]any{"mcp_servers": []string{}})
	if state := syncStudioMCPHTTP(t, h, a.ID, s.ID); !state.Deferred || !slices.Equal(state.MCPServers, []string{"alpha"}) {
		t.Fatalf("approval policy=%+v", state)
	}
	if err := s.Gate().Decide(gate.GateID, Verdict{Allow: true}); err != nil {
		t.Fatal(err)
	}
	last := model.next(t)
	last.reply <- textTurn("done")
	stream.wait(t, EvRunEnd)
	s.wait()
	waitStudioMCPPolicy(t, s, []string{})
	if _, auto := s.Policy().Decide(agent.GateRequest{ToolName: "mcp_beta_echo"}); auto {
		t.Fatal("MCP selection update weakened strict approval")
	}
}

func TestStudioMCPPolicyCommitFailureLeavesRuntimeAndBindingUntouched(t *testing.T) {
	h, model, a, s := newStudioMCPPolicyHarness(t, true)
	in := a.studioInput
	want := []string{"beta"}
	in.MCPServers = &want
	if _, err := h.mgr.studio.put(a.ID, in, a.Revision); err != nil {
		t.Fatal(err)
	}
	before, _ := h.mgr.studio.binding(s.ID)
	path, backup := h.mgr.studio.path, h.mgr.studio.path+".mcp-backup"
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	restored := false
	restore := func() {
		if !restored {
			_ = os.Remove(path)
			_ = os.Rename(backup, path)
			restored = true
		}
	}
	defer restore()
	if _, err := s.SyncStudioMCPPolicy(); err == nil {
		t.Fatal("failed policy persistence was accepted")
	}
	after, _ := h.mgr.studio.binding(s.ID)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(s.mcpOnly, map[string]bool{"alpha": true}) {
		t.Fatal("failed commit partially changed selection")
	}
	s.policy.mu.Lock()
	_, oldPresent := s.policy.mcpTools["mcp_alpha_echo"]
	_, newPresent := s.policy.mcpTools["mcp_beta_echo"]
	s.policy.mu.Unlock()
	if !oldPresent || newPresent {
		t.Fatal("failed commit changed live approvals")
	}
	restore()
	if state, err := s.SyncStudioMCPPolicy(); err != nil || !state.Changed {
		t.Fatalf("policy retry=%+v, %v", state, err)
	}
	call := startCatalogRun(t, s, model)
	modelSchema(t, call, "mcp_beta_echo")
	withoutModelTool(t, call, "mcp_alpha_echo")
	finishCatalogRun(t, s, call)
}

func TestStudioMCPPolicyDormantReloadReconcilesLatestWithoutFrontend(t *testing.T) {
	h, model, a, s := newStudioMCPPolicyHarness(t, true)
	a = patchStudioPolicy(t, h, a, map[string]any{"mcp_servers": []string{"beta"}})
	reopened, err := openStudioStore(filepath.Dir(h.mgr.studio.path))
	if err != nil {
		t.Fatal(err)
	}
	persisted, ok := reopened.binding(s.ID)
	if !ok || persisted.MCPPolicy == nil || !slices.Equal(*persisted.effectiveMCPServers(), []string{"beta"}) {
		t.Fatal("MCP policy was not restored from disk")
	}
	sid := s.ID
	h.mgr.mu.Lock()
	delete(h.mgr.sessions, sid)
	h.mgr.mu.Unlock()
	s.closeUnpublished()
	in := a.studioInput
	empty := []string{}
	in.MCPServers = &empty
	if _, err := h.mgr.studio.put(a.ID, in, a.Revision); err != nil {
		t.Fatal(err)
	}
	loaded, err := h.mgr.Get(sid)
	if err != nil {
		t.Fatal(err)
	}
	if state := syncStudioMCPHTTP(t, h, a.ID, sid); state.Changed || state.Deferred || len(state.MCPServers) != 0 {
		t.Fatalf("reload did not reconcile before Get returned: %+v", state)
	}
	call := startCatalogRun(t, loaded, model)
	withoutModelTool(t, call, "mcp_alpha_echo")
	withoutModelTool(t, call, "mcp_beta_echo")
	finishCatalogRun(t, loaded, call)
}

func TestStudioMCPPolicyBackgroundChildDefersWithoutCancellingIt(t *testing.T) {
	h, _, a, s := newStudioMCPPolicyHarness(t, true)
	work := t.TempDir()
	release := filepath.Join(work, "release")
	program := filepath.Join(work, "child")
	source := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"turn_start\",\"turn\":1}'\nwhile [ ! -f '" + release + "' ]; do sleep 0.02; done\nprintf '%s\\n' '{\"type\":\"token\",\"text\":\"finished original grant\"}' '{\"type\":\"run_end\",\"stop_reason\":\"end_turn\"}'\n"
	if err := os.WriteFile(program, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	settled := make(chan struct{})
	bg := tools.NewBgRunner(context.Background(), tools.BgHooks{OnComplete: func(tools.BgTask, tools.BgResult) {
		s.syncStudioCapabilitiesAtIdle()
		close(settled)
	}})
	defer bg.Close()
	s.mu.Lock()
	original := s.bg
	s.bg = bg
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.bg = original; s.mu.Unlock() }()
	child := &tools.Subagent{Cwd: work, Exe: program, Bg: bg}
	if _, err := child.Execute(context.Background(), json.RawMessage(`{"mode":"explore","task":"retain old server selection"}`)); err != nil {
		t.Fatal(err)
	}
	if !s.BgActive() {
		t.Fatal("background child was not registered")
	}
	a = patchStudioPolicy(t, h, a, map[string]any{"mcp_servers": []string{}})
	if state := syncStudioMCPHTTP(t, h, a.ID, s.ID); !state.Deferred || state.Changed || !slices.Equal(state.MCPServers, []string{"alpha"}) {
		t.Fatalf("background policy=%+v", state)
	}
	if !s.BgActive() {
		t.Fatal("MCP edit cancelled the running background child")
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-settled:
	case <-time.After(5 * time.Second):
		t.Fatal("background child did not finish and synchronize")
	}
	waitStudioMCPPolicy(t, s, []string{})
}

func TestStudioMCPSyncOwnerAndAgentBoundary(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	var input studioInput
	if err := json.Unmarshal([]byte(studioBody(studioAgentCapsBody("MCP owner boundary", "ready", nil))), &input); err != nil {
		t.Fatal(err)
	}
	a, err := h.mgr.studio.put("", input, 0)
	if err != nil {
		t.Fatal(err)
	}
	sid := h.studioConversationAs(aliceToken, a.ID)
	before, _ := h.mgr.studio.binding(sid)
	for _, tc := range []struct{ token, aid string }{{bobToken, a.ID}, {aliceToken, "ag_other"}} {
		response := h.doAs(tc.token, http.MethodPost, "/api/apps/"+tc.aid+"/sessions/"+sid+"/mcp-config/sync", `{}`)
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("foreign MCP sync=%d", response.StatusCode)
		}
	}
	after, _ := h.mgr.studio.binding(sid)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("foreign synchronization changed MCP grants")
	}
}

func TestStudioMCPPolicySetSemanticsAndDirectCallIdleBoundary(t *testing.T) {
	h, _, a, s := newStudioMCPPolicyHarness(t, true)
	a = patchStudioPolicy(t, h, a, map[string]any{"mcp_servers": nil})
	if state := syncStudioMCPHTTP(t, h, a.ID, s.ID); state.Deferred || !slices.Equal(state.MCPServers, []string{"alpha", "beta"}) {
		t.Fatalf("nil inheritance=%+v", state)
	}
	a = patchStudioPolicy(t, h, a, map[string]any{"mcp_servers": []string{"beta", "alpha"}})
	a = patchStudioPolicy(t, h, a, map[string]any{"mcp_servers": []string{"alpha", "beta"}})
	if state := syncStudioMCPHTTP(t, h, a.ID, s.ID); state.Changed || state.Deferred {
		t.Fatalf("order-only change applied=%+v", state)
	}
	layer := &mcpLayer{mgr: h.mgr, agentID: a.ID}
	_, done, err := layer.reserveAgentCall(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	a = patchStudioPolicy(t, h, a, map[string]any{"mcp_servers": []string{}})
	if state := syncStudioMCPHTTP(t, h, a.ID, s.ID); !state.Deferred {
		t.Fatalf("direct call was not protected=%+v", state)
	}
	done()
	if state := syncStudioMCPHTTP(t, h, a.ID, s.ID); state.Changed || state.Deferred || len(state.MCPServers) != 0 {
		t.Fatalf("direct-call release did not apply=%+v", state)
	}
}
