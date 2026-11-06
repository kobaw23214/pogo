package web

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

// Observe the committed boundary without calling a sync endpoint: otherwise
// the assertion itself could conceal a missing finish/background hook.
func waitStudioCapabilityPolicies(t *testing.T, s *Session, revision int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		binding, ok := s.mgr.studio.binding(s.ID)
		if ok && binding.skillRevision() == revision && binding.toolsRevision() == revision {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("capability policies did not apply automatically: Skill=%d tools=%d, want %d", binding.skillRevision(), binding.toolsRevision(), revision)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStudioCapabilityReloadOldAndNewChatsRevokeAndRestoreTogether(t *testing.T) {
	sk := writeManifestSkill(t, t.TempDir(), "reloadable", "RELOAD-CAPABILITY-SKILL", "reloadable.echo")
	h, capture := newStudioHarness(t, []llm.Response{textTurn("seed"), textTurn("revoked"), textTurn("restored")}, func(c *Config) {
		c.Skills = []skills.Skill{sk}
	})
	a := postStudioAgent(t, h, studioAgentCapsBody("Capability reload", "ready", map[string]any{
		"skills": []string{skillIDOf(sk)}, "tools": []string{"read", "write"}, "mcp_servers": []string{},
	}), 201)
	sid := startStudioConversation(t, h, a.ID)
	s := h.session(sid)
	studioAsk(t, h, sid, "keep this conversation history")
	s.wait()
	hub, browser, loop := s.hub, s.browser, s.agent
	initial, _ := h.mgr.studio.binding(sid)
	history, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}

	assertMatchesNewChat := func() {
		t.Helper()
		newSID := startStudioConversation(t, h, a.ID)
		oldSkills, newSkills := sessionSkillNamesHTTP(t, h, sid), sessionSkillNamesHTTP(t, h, newSID)
		oldTools, newTools := slices.Sorted(slices.Values(s.agent.NativeToolNames())), slices.Sorted(slices.Values(h.session(newSID).agent.NativeToolNames()))
		if !slices.Equal(oldSkills, newSkills) || !slices.Equal(oldTools, newTools) {
			t.Fatalf("old/new capability mismatch: Skills=%v/%v tools=%v/%v", oldSkills, newSkills, oldTools, newTools)
		}
	}
	a = patchStudioPolicy(t, h, a, map[string]any{"skills": []string{}, "tools": []string{}, "instructions": "NEW-INSTRUCTIONS-STAY-PINNED"})
	assertMatchesNewChat()
	after, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	current, _ := h.mgr.studio.binding(sid)
	if s.hub != hub || s.browser != browser || s.agent != loop || !bytes.Equal(history, after) || !reflect.DeepEqual(initial.Agent, current.Agent) {
		t.Fatal("capability edit recreated state, rewrote history or replaced the original Agent snapshot")
	}
	studioAsk(t, h, sid, "describe the setup after capability revocation")
	s.wait()
	systems, _, _, _ := capture.snapshot()
	schemas := capture.toolSchemas()
	if strings.Contains(systems[1], "RELOAD-CAPABILITY-SKILL") || slices.Contains(schemas[1], "write") || slices.Contains(schemas[1], "reloadable_echo") {
		t.Fatal("revoked capability remained in the model prompt or executable schemas")
	}

	a = patchStudioPolicy(t, h, a, map[string]any{"skills": []string{skillIDOf(sk)}, "tools": []string{"read", "write"}})
	assertMatchesNewChat()
	studioAsk(t, h, sid, "describe the setup after capability restoration")
	s.wait()
	systems, _, histories, _ := capture.snapshot()
	schemas = capture.toolSchemas()
	if !strings.Contains(systems[2], "RELOAD-CAPABILITY-SKILL") || !slices.Contains(schemas[2], "write") || !slices.Contains(schemas[2], "reloadable_echo") {
		t.Fatal("restored capability did not reach the existing model runtime")
	}
	if strings.Contains(systems[2], "NEW-INSTRUCTIONS-STAY-PINNED") || len(histories[2]) <= len(histories[0]) {
		t.Fatal("capability reload replaced pinned instructions or discarded prior conversation history")
	}
}

func TestStudioCapabilityReloadSkillOnlyEditNotifiesOpenChat(t *testing.T) {
	sk := writeTestSkill(t, t.TempDir(), "notice", "NOTICE-SKILL", "# Notice")
	h, _ := newStudioHarness(t, []llm.Response{textTurn("unused")}, func(c *Config) { c.Skills = []skills.Skill{sk} })
	a := postStudioAgent(t, h, studioAgentCapsBody("Skill notice", "ready", map[string]any{
		"skills": []string{skillIDOf(sk)}, "tools": []string{"read"}, "mcp_servers": []string{},
	}), 201)
	sid := startStudioConversation(t, h, a.ID)
	s := h.session(sid)
	before := s.agent.NativeToolNames()
	_, events, stop := s.hub.Subscribe(s.hub.Seq())
	defer stop()
	patchStudioPolicy(t, h, a, map[string]any{"skills": []string{}})
	if !slices.Equal(before, s.agent.NativeToolNames()) {
		t.Fatal("Skill-only edit unexpectedly changed native tools")
	}
	notified := false
	draining := true
	for draining {
		select {
		case event := <-events:
			notified = notified || event.Type == EvCapabilitiesChanged
		default:
			draining = false
		}
	}
	if !notified || len(sessionSkillNamesHTTP(t, h, sid)) != 0 {
		t.Fatal("Skill-only revocation did not notify the open chat and remove the effective Skill")
	}
	if result, err := s.SyncStudioSkillPolicy(); err != nil || result.Changed || result.Deferred {
		t.Fatalf("unchanged Skill sync was not idempotent: %+v %v", result, err)
	}
	select {
	case event := <-events:
		t.Fatalf("unchanged Skill sync emitted another event: %+v", event)
	default:
	}
}

func TestStudioCapabilityReloadApprovalWaitAppliesAfterFinishWithoutManualSync(t *testing.T) {
	sk := writeTestSkill(t, t.TempDir(), "during-approval", "APPROVAL-SKILL", "# Keep until the approved run finishes")
	h, capture := newStudioHarness(t, []llm.Response{toolTurn("bash", `{"command":"printf approved-original-capability"}`), textTurn("done")}, func(c *Config) {
		c.Skills = []skills.Skill{sk}
	})
	a := postStudioAgent(t, h, studioAgentCapsBody("Approval reload", "ready", map[string]any{
		"skills": []string{skillIDOf(sk)}, "tools": []string{"bash"}, "mcp_servers": []string{},
	}), 201)
	sid := startStudioConversation(t, h, a.ID)
	s := h.session(sid)
	s.Policy().Set(ModeStrict, 0)
	stream := h.start(sid, "run the original approved command")
	defer stream.close()
	gate := stream.wait(t, EvGateRequest)
	a = patchStudioPolicy(t, h, a, map[string]any{"skills": []string{}, "tools": []string{}})
	if !slices.Equal(sessionSkillNamesHTTP(t, h, sid), []string{"during-approval"}) || !slices.Contains(s.agent.NativeToolNames(), "bash") {
		t.Fatal("saving during approval prematurely revoked the active run's capabilities")
	}
	if err := s.Gate().Decide(gate.GateID, Verdict{Allow: true}); err != nil {
		t.Fatal(err)
	}
	stream.wait(t, EvRunEnd)
	s.wait()
	waitStudioCapabilityPolicies(t, s, a.Revision)
	if len(sessionSkillNamesHTTP(t, h, sid)) != 0 || slices.Contains(s.agent.NativeToolNames(), "bash") {
		t.Fatal("finish did not apply pending capabilities without a manual sync")
	}
	for _, names := range capture.toolSchemas() {
		if !slices.Contains(names, "bash") {
			t.Fatal("capabilities changed inside the original approved run")
		}
	}
}

func TestStudioCapabilityReloadBackgroundCompletionAppliesSkillsAndTools(t *testing.T) {
	sk := writeTestSkill(t, t.TempDir(), "after-child", "CHILD-SKILL", "# Available after the child settles")
	h, _ := newStudioHarness(t, []llm.Response{textTurn("notification received")}, func(c *Config) { c.Skills = []skills.Skill{sk} })
	a := postStudioAgent(t, h, studioAgentCapsBody("Child capability reload", "ready", map[string]any{
		"skills": []string{}, "tools": []string{}, "mcp_servers": []string{},
	}), 201)
	sid := startStudioConversation(t, h, a.ID)
	s := h.session(sid)
	work := t.TempDir()
	release := filepath.Join(work, "release")
	exe := filepath.Join(work, "child")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nwhile [ ! -f '"+release+"' ]; do sleep 0.02; done\nprintf '%s\\n' '{\"type\":\"run_end\",\"stop_reason\":\"end_turn\"}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	completed := make(chan struct{})
	bg := tools.NewBgRunner(context.Background(), tools.BgHooks{OnComplete: func(task tools.BgTask, result tools.BgResult) {
		s.onBgComplete(task, result)
		// Keep the completion notification queued so its automatic next run
		// cannot accidentally apply a policy the completion hook omitted.
		s.mu.Lock()
		if s.bgWake != nil {
			s.bgWake.Stop()
			s.bgWake = nil
		}
		s.mu.Unlock()
		close(completed)
	}})
	defer bg.Close()
	s.mu.Lock()
	original := s.bg
	s.bg = bg
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.bg = original; s.mu.Unlock() }()
	child := &tools.Subagent{Cwd: work, Exe: exe, Bg: bg}
	if _, err := child.Execute(context.Background(), json.RawMessage(`{"mode":"explore","task":"keep the original capability set"}`)); err != nil {
		t.Fatal(err)
	}
	a = patchStudioPolicy(t, h, a, map[string]any{"skills": []string{skillIDOf(sk)}, "tools": []string{"write"}})
	if !s.BgActive() || len(sessionSkillNamesHTTP(t, h, sid)) != 0 || slices.Contains(s.agent.NativeToolNames(), "write") {
		t.Fatal("running background child was interrupted or desired capabilities were exposed early")
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-completed:
	case <-time.After(5 * time.Second):
		t.Fatal("background child did not settle")
	}
	binding, _ := h.mgr.studio.binding(sid)
	if binding.skillRevision() != a.Revision || binding.toolsRevision() != a.Revision {
		t.Fatalf("completion hook did not commit pending policies: Skill=%d tools=%d, want %d", binding.skillRevision(), binding.toolsRevision(), a.Revision)
	}
	if !slices.Equal(sessionSkillNamesHTTP(t, h, sid), []string{"after-child"}) || !slices.Contains(s.agent.NativeToolNames(), "write") {
		t.Fatal("background completion left Skill or native capability changes pending")
	}
}
