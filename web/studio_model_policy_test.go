package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
)

func patchStudioPolicy(t *testing.T, h *harness, a studioAgent, values map[string]any) studioAgent {
	t.Helper()
	raw, _ := json.Marshal(a.studioInput)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	for k, v := range values {
		body[k] = v
	}
	body["revision"] = a.Revision
	var updated studioAgent
	if err := decodeJSONBody(h.do(http.MethodPatch, "/api/manage/agents/"+a.ID, studioBody(body)), &updated); err != nil {
		t.Fatal(err)
	}
	return updated
}

func TestStudioModelPolicyIdleSaveReloadAndCapabilityBoundary(t *testing.T) {
	dir := t.TempDir()
	h, _ := newStudioHarness(t, []llm.Response{textTurn("ok")}, func(c *Config) { c.SessionDir = dir })
	addStudioTestModel(t, "glm-5.3")
	addStudioTestModel(t, "other-model")
	a := postStudioAgent(t, h, studioAgentCapsBody("Original identity", "ready", map[string]any{"models": []string{"k3"}, "tools": []string{"read"}, "skills": []string{}, "mcp_servers": []string{}}), 201)
	sid := startStudioConversation(t, h, a.ID)
	s := h.session(sid)
	before, _ := h.mgr.studio.binding(sid)
	oldJSON, _ := json.Marshal(before.Agent)
	history, _ := os.ReadFile(s.Path())
	a = patchStudioPolicy(t, h, a, map[string]any{"model": "glm-5.3", "models": []string{"glm-5.3", "other-model"}, "name": "New identity", "instructions": "NEW INSTRUCTIONS", "tools": []string{"read", "bash"}})
	if s.Model().Model != "glm-5.3" {
		t.Fatal("idle conversation retained old default")
	}
	b, _ := h.mgr.studio.binding(sid)
	afterJSON, _ := json.Marshal(b.Agent)
	if !bytes.Equal(oldJSON, afterJSON) || !bytes.Equal(before.SkillLock, b.SkillLock) || !bytes.Equal(before.Agent.SkillBinding, b.Agent.SkillBinding) {
		t.Fatal("model sync rewrote pinned identity or skill lock")
	}
	if b.ModelPolicy == nil || b.ModelPolicy.Revision != a.Revision || b.CurrentModel != "glm-5.3" {
		t.Fatalf("model policy not committed: %+v", b)
	}
	afterHistory, _ := os.ReadFile(s.Path())
	if !bytes.Equal(history, afterHistory) {
		t.Fatal("model sync changed transcript history")
	}
	caps := sessionCapabilities(t, h, a.ID, sid)
	if caps["model_sync"] != true || !slices.Equal(stringField(t, caps, "models"), []string{"glm-5.3", "other-model"}) || !slices.Contains(stringField(t, caps, "tools"), "bash") || b.ToolPolicy == nil || b.ToolPolicy.Revision != a.Revision {
		t.Fatalf("independent model and tool policies did not both apply: %+v", caps)
	}
	registryBefore, _ := os.ReadFile(h.mgr.studio.path)
	_ = sessionCapabilities(t, h, a.ID, sid)
	registryAfter, _ := os.ReadFile(h.mgr.studio.path)
	if !bytes.Equal(registryBefore, registryAfter) {
		t.Fatal("GET capabilities mutated binding")
	}
	h.mgr.Close()
	h2, _ := newStudioHarness(t, []llm.Response{textTurn("ok")}, func(c *Config) { c.SessionDir = dir })
	addStudioTestModel(t, "glm-5.3")
	addStudioTestModel(t, "other-model")
	if h2.session(sid).Model().Model != "glm-5.3" {
		t.Fatal("reloaded model policy lost")
	}
	newSID := startStudioConversation(t, h2, a.ID)
	newCaps := sessionCapabilities(t, h2, a.ID, newSID)
	if !slices.Contains(stringField(t, newCaps, "tools"), "bash") {
		t.Fatal("new conversation did not receive latest non-model definition")
	}
}

func TestStudioModelPolicyKeepsManualChoiceOnNonModelEditAndAllowedListChanges(t *testing.T) {
	h, _ := newStudioHarness(t, []llm.Response{textTurn("ok")})
	addStudioTestModel(t, "glm-5.3")
	addStudioTestModel(t, "other-model")
	a := postStudioAgent(t, h, studioAgentCapsBody("Model choices", "ready", map[string]any{"models": []string{"k3", "glm-5.3"}}), 201)
	sid := startStudioConversation(t, h, a.ID)
	s := h.session(sid)
	if _, err := s.SetModel("glm-5.3"); err != nil {
		t.Fatal(err)
	}
	a = patchStudioPolicy(t, h, a, map[string]any{"description": "renamed only", "instructions": "different instructions"})
	if s.Model().Model != "glm-5.3" {
		t.Fatal("non-model edit reset manual choice")
	}
	a = patchStudioPolicy(t, h, a, map[string]any{"models": []string{"k3", "glm-5.3", "other-model"}})
	if s.Model().Model != "glm-5.3" {
		t.Fatal("allowlist addition reset permitted choice")
	}
	a = patchStudioPolicy(t, h, a, map[string]any{"models": []string{"k3", "other-model"}})
	if s.Model().Model != "k3" {
		t.Fatal("removed choice remained in use")
	}
	if _, err := s.SetModel("glm-5.3"); err == nil {
		t.Fatal("removed model still authorized")
	}
	a = patchStudioPolicy(t, h, a, map[string]any{"model": "other-model"})
	if s.Model().Model != "other-model" {
		t.Fatal("default change did not take effect")
	}
}

func TestStudioModelPolicyBusyDefersAndRunStartSynchronizesWithoutUI(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	h := newHarness(t, func(n int) (llm.Response, error) {
		if n == 1 {
			close(entered)
			<-release
		}
		return textTurn("done"), nil
	})
	addStudioTestModel(t, "glm-5.3")
	a := postStudioAgent(t, h, studioAgentCapsBody("Busy policy", "ready", map[string]any{"models": []string{"k3"}}), 201)
	sid := startStudioConversation(t, h, a.ID)
	s := h.session(sid)
	stream := h.start(sid, "work")
	defer stream.close()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not start")
	}
	a = patchStudioPolicy(t, h, a, map[string]any{"model": "glm-5.3", "models": []string{"glm-5.3"}})
	var syncResult studioModelSync
	if err := decodeJSONBody(h.do(http.MethodPost, "/api/apps/"+a.ID+"/sessions/"+sid+"/model-config/sync", `{}`), &syncResult); err != nil {
		t.Fatal(err)
	}
	if !syncResult.Deferred || syncResult.Changed || syncResult.Model != "k3" || !slices.Equal(syncResult.Models, []string{"k3"}) {
		t.Fatalf("busy policy reported premature switch: %+v", syncResult)
	}
	if _, err := s.SetModel("glm-5.3"); err != ErrRunActive {
		t.Fatalf("busy manual switch: %v", err)
	}
	if got := sessionCapabilities(t, h, a.ID, sid)["model"]; got != "k3" {
		t.Fatalf("busy GET exposed desired instead of effective model: %v", got)
	}
	once.Do(func() { close(release) })
	stream.wait(t, EvRunEnd)
	ev := stream.wait(t, EvModelChanged)
	if ev.Model != "glm-5.3" || ev.Provider != "zhipu" || ev.ContextWindow != 128000 {
		t.Fatalf("incomplete switch SSE: %+v", ev)
	}
	// Direct store update represents a saved definition while the UI is absent;
	// the next ordinary run must apply it before taking Studio's read lease.
	next := a.studioInput
	next.Model = "k3"
	models := []string{"k3"}
	next.Models = &models
	if _, err := h.mgr.studio.put(a.ID, next, a.Revision); err != nil {
		t.Fatal(err)
	}
	if s.Model().Model != "glm-5.3" {
		t.Fatal("direct store save unexpectedly changed active client")
	}
	if _, err := s.Start("next run", nil, ""); err != nil {
		t.Fatal(err)
	}
	stream.wait(t, EvRunEnd)
	if s.Model().Model != "k3" {
		t.Fatal("run start did not apply latest model without UI")
	}
}

func TestStudioModelSyncOwnerAndAgentBindingAreRequired(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("ok")))
	addStudioTestModel(t, "glm-5.3")
	var input studioInput
	if err := json.Unmarshal([]byte(studioBody(studioAgentCapsBody("Owner policy", "ready", map[string]any{"models": []string{"k3"}}))), &input); err != nil {
		t.Fatal(err)
	}
	a, err := h.mgr.studio.put("", input, 0)
	if err != nil {
		t.Fatal(err)
	}
	sid := h.studioConversationAs(aliceToken, a.ID)
	before, _ := h.mgr.studio.binding(sid)
	for _, tc := range []struct{ token, aid string }{{bobToken, a.ID}, {aliceToken, "ag_other"}} {
		resp := h.doAs(tc.token, http.MethodPost, "/api/apps/"+tc.aid+"/sessions/"+sid+"/model-config/sync", `{}`)
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Fatalf("foreign sync status %d", resp.StatusCode)
		}
	}
	after, _ := h.mgr.studio.binding(sid)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("foreign sync changed model policy")
	}
}

func TestStudioModelPolicyAlsoUpdatesLegacyTextOnlyRuntime(t *testing.T) {
	dir := t.TempDir()
	h, capture := newStudioHarness(t, []llm.Response{textTurn("legacy result")}, func(c *Config) { c.SessionDir = dir })
	addStudioTestModel(t, "glm-5.3")
	a := createStudioAgent(t, h, "Legacy model", "ORIGINAL LEGACY RULES", "ready")
	store, err := session.CreateAgentConversation(dir, h.mgr.cfg.Cwd, "k3")
	if err != nil {
		t.Fatal(err)
	}
	sid := strings.TrimSuffix(filepath.Base(store.Path()), ".jsonl")
	if err := h.mgr.studio.bind(sid, a, nil); err != nil {
		t.Fatal(err)
	}
	s := h.session(sid)
	a = patchStudioPolicy(t, h, a, map[string]any{"model": "glm-5.3", "models": []string{"glm-5.3"}, "instructions": "NEW RULES MUST NOT REPLACE THE OLD CONTEXT", "tools": []string{"bash"}})
	if s.Model().Model != "glm-5.3" || !slices.Equal(s.studioModels, []string{"glm-5.3"}) {
		t.Fatal("legacy runtime did not receive model-only policy")
	}
	if _, err := s.Start("hello", nil, ""); err != nil {
		t.Fatal(err)
	}
	s.wait()
	systems, tools, _, _ := capture.snapshot()
	if len(systems) != 1 || tools[0] != 0 || !strings.Contains(systems[0], "ORIGINAL LEGACY RULES") || strings.Contains(systems[0], "NEW RULES MUST") {
		t.Fatal("model sync expanded the legacy runtime")
	}
}

func TestStudioModelPolicyFailedCommitPreservesLiveAndDurableState(t *testing.T) {
	h, _ := newStudioHarness(t, []llm.Response{textTurn("ok")})
	addStudioTestModel(t, "glm-5.3")
	a := createStudioAgent(t, h, "Atomic model", "rules", "ready")
	sid := startStudioConversation(t, h, a.ID)
	s := h.session(sid)
	next := a.studioInput
	next.Model = "glm-5.3"
	models := []string{"glm-5.3"}
	next.Models = &models
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
	if _, err := s.SyncStudioModelPolicy(); err == nil {
		t.Fatal("failed policy write was accepted")
	}
	after, _ := h.mgr.studio.binding(sid)
	if !reflect.DeepEqual(before, after) || s.Model().Model != "k3" || s.Hub().Snapshot().Run.Model != "k3" {
		t.Fatal("failed commit partially changed model state")
	}
}

func TestStudioModelPolicyStartCompactionSaveAndSyncDoNotDeadlock(t *testing.T) {
	compacting := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	big := textTurn(strings.Repeat("x", 24000))
	big.Usage = llm.Usage{Input: 8000, Output: 6000}
	h := newHarness(t, func(n int) (llm.Response, error) {
		if n == 1 {
			return big, nil
		}
		if n == 2 {
			close(compacting)
			<-release
			return textTurn("Summary: original work and constraints retained."), nil
		}
		return textTurn("new run complete"), nil
	}, func(c *Config) { c.Compact = "auto:2000" })
	addStudioTestModel(t, "glm-5.3")
	a := createStudioAgent(t, h, "Concurrent policy", "rules", "ready")
	sid := startStudioConversation(t, h, a.ID)
	s := h.session(sid)
	stream := h.start(sid, "first request")
	defer stream.close()
	stream.wait(t, EvRunEnd)
	s.wait()
	started := make(chan error, 1)
	go func() { _, err := s.Start("second request", nil, ""); started <- err }()
	select {
	case <-compacting:
	case <-time.After(5 * time.Second):
		t.Fatal("pre-run compaction never started")
	}
	next := a.studioInput
	next.Model = "glm-5.3"
	models := []string{"glm-5.3"}
	next.Models = &models
	saved := make(chan error, 1)
	go func() { _, err := h.mgr.studio.put(a.ID, next, a.Revision); saved <- err }()
	// Start cannot hold a Studio read lease across its compaction network call.
	select {
	case err := <-saved:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("definition save blocked behind Start's registry lease")
	}
	synced := make(chan studioModelSync, 1)
	go func() { result, _ := s.SyncStudioModelPolicy(); synced <- result }()
	select {
	case result := <-synced:
		if !result.Deferred || result.Model != "k3" {
			t.Fatalf("compacting sync=%+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent model sync deadlocked with Start")
	}
	once.Do(func() { close(release) })
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start could not resume after compaction")
	}
	s.wait()
	if s.Model().Model != "glm-5.3" {
		t.Fatal("Start failed to recheck policy after compaction")
	}
}

func TestStudioModelPolicyOnlyChangePublishesOneRefreshEvent(t *testing.T) {
	h, _ := newStudioHarness(t, []llm.Response{textTurn("ok")})
	addStudioTestModel(t, "glm-5.3")
	a := createStudioAgent(t, h, "Policy event", "rules", "ready")
	sid := startStudioConversation(t, h, a.ID)
	s := h.session(sid)
	mark := s.Hub().Seq()
	a = patchStudioPolicy(t, h, a, map[string]any{"models": []string{"k3", "glm-5.3"}})
	if _, err := s.SyncStudioModelPolicy(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SyncStudioModelPolicy(); err != nil {
		t.Fatal(err)
	}
	events, _, cancel := s.Hub().Subscribe(mark)
	defer cancel()
	changed := 0
	for _, e := range events {
		if e.Type == EvModelChanged {
			changed++
			if e.Model != "k3" || e.Provider != "kimi" || e.ContextWindow != 200000 {
				t.Fatalf("policy refresh event incomplete: %+v", e)
			}
		}
	}
	if changed != 1 {
		t.Fatalf("model-policy event count=%d want1", changed)
	}
}

func TestStudioModelPolicyManualCompactionAppliesDeferredUpdate(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "cancel"} {
		t.Run(outcome, func(t *testing.T) { testStudioModelPolicyManualCompactionOutcome(t, outcome) })
	}
}

func testStudioModelPolicyManualCompactionOutcome(t *testing.T, outcome string) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	h := newHarness(t, func(n int) (llm.Response, error) {
		if n == 1 {
			return textTurn(strings.Repeat("historical context ", 2000)), nil
		}
		close(entered)
		<-release
		if outcome == "failure" {
			return llm.Response{}, errors.New("fixture summary failure")
		}
		return textTurn("A compact summary preserving original work."), nil
	})
	addStudioTestModel(t, "glm-5.3")
	a := createStudioAgent(t, h, "Manual compact policy", "rules", "ready")
	sid := startStudioConversation(t, h, a.ID)
	s := h.session(sid)
	stream := h.start(sid, "first work")
	defer stream.close()
	stream.wait(t, EvRunEnd)
	s.wait()
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _, err := s.Compact(ctx); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("manual summary did not start")
	}
	a = patchStudioPolicy(t, h, a, map[string]any{"model": "glm-5.3", "models": []string{"glm-5.3"}})
	result, err := s.SyncStudioModelPolicy()
	if err != nil || !result.Deferred || result.Model != "k3" {
		t.Fatalf("manual compact did not defer: %+v %v", result, err)
	}
	if outcome == "cancel" {
		cancel()
	}
	once.Do(func() { close(release) })
	select {
	case err := <-done:
		if (err == nil) != (outcome == "success") {
			t.Fatalf("summary outcome %s: %v", outcome, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("manual compaction did not finish")
	}
	if s.Model().Model != "glm-5.3" || s.Hub().Snapshot().Run.Model != "glm-5.3" {
		t.Fatal("manual compaction left the old model until another user action")
	}
}

func TestStudioModelPolicyRetainsNonemptyRuntimeSkillBindingAcrossReload(t *testing.T) {
	h, _, conversation, _ := runtimeSkillObserveFixture(t, []llm.Response{textTurn("ok")})
	addStudioTestModel(t, "glm-5.3")
	s := h.session(conversation.ID)
	before, ok := h.mgr.studio.binding(s.ID)
	if !ok || len(before.SkillLock) == 0 || len(before.Agent.SkillBinding) == 0 || s.runtimeSkill == nil {
		t.Fatal("fixture must carry actual nonempty runtime binding and lock")
	}
	descriptors := s.runtimeSkill.Descriptors()
	a, _ := h.mgr.studio.get(before.Agent.ID)
	a = patchStudioPolicy(t, h, a, map[string]any{"model": "glm-5.3", "models": []string{"glm-5.3"}})
	after, _ := h.mgr.studio.binding(s.ID)
	if !bytes.Equal(before.SkillLock, after.SkillLock) || !bytes.Equal(before.Agent.SkillBinding, after.Agent.SkillBinding) || !reflect.DeepEqual(descriptors, s.runtimeSkill.Descriptors()) {
		t.Fatal("model-only update changed runtime-skill authority")
	}
	if _, err := s.runtimeSkill.Call(context.Background(), descriptors[0].ID, json.RawMessage(`{"query":"before reload"}`)); err != nil {
		t.Fatal("bound runtime no longer executes after model sync", err)
	}
	h.mgr.Close()
	h2, _ := newStudioHarness(t, []llm.Response{textTurn("ok")}, func(c *Config) { c.SessionDir = h.mgr.cfg.SessionDir; c.SkillRuntime = h.mgr.cfg.SkillRuntime })
	addStudioTestModel(t, "glm-5.3")
	restored := h2.session(s.ID)
	if restored.runtimeSkill == nil || restored.Model().Model != "glm-5.3" || !reflect.DeepEqual(descriptors, restored.runtimeSkill.Descriptors()) {
		t.Fatal("reloaded runtime lost its original binding or new model")
	}
	if _, err := restored.runtimeSkill.Call(context.Background(), descriptors[0].ID, json.RawMessage(`{"query":"after reload"}`)); err != nil {
		t.Fatal("original runtime lock rejected after reload", err)
	}
}

func TestStudioModelPolicyDirectMCPReservationAppliesDeferredUpdate(t *testing.T) {
	h, _ := newStudioHarness(t, []llm.Response{textTurn("ok")})
	addStudioTestModel(t, "glm-5.3")
	a := createStudioAgent(t, h, "MCP policy boundary", "rules", "ready")
	s := h.session(startStudioConversation(t, h, a.ID))
	layer := &mcpLayer{mgr: h.mgr, agentID: a.ID}
	_, done, err := layer.reserveAgentCall(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(done) }
	defer release()
	a = patchStudioPolicy(t, h, a, map[string]any{"model": "glm-5.3", "models": []string{"glm-5.3"}})
	result, err := s.SyncStudioModelPolicy()
	if err != nil || !result.Deferred || result.Model != "k3" {
		release()
		t.Fatalf("direct-call reservation did not defer: %+v %v", result, err)
	}
	release()
	if s.Model().Model != "glm-5.3" {
		t.Fatal("direct-call release left model policy pending")
	}
}
