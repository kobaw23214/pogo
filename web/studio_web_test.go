package web

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/memory"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

type studioConversation struct {
	ID    string `json:"id"`
	Model string `json:"model"`
}

func createStudioConversationBody(t *testing.T, h *harness, agentID, body string) studioConversation {
	t.Helper()
	resp := h.do(http.MethodPost, "/api/apps/"+agentID+"/sessions", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("create conversation for %s: %d (%s)", agentID, resp.StatusCode, msg)
	}
	var out studioConversation
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func sessionCwdAndModel(sess *Session) (string, string) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.cwd, sess.cfg.Model
}

func TestStudioWebRuntimeMatchesAnOrdinarySessionPlusAgentInstructions(t *testing.T) {
	memRoot := filepath.Join(t.TempDir(), "memory")
	if err := os.MkdirAll(filepath.Join(memRoot, ".pogo", "memory", "notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memRoot, ".pogo", "memory", "notes", "note.md"), []byte("studio runtime memory note\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	globalMem, _ := memory.Load(memory.Options{Project: true, Cwd: memRoot})
	sk := writeTestSkill(t, t.TempDir(), "studioskill", "the ordinary workbench skill", "# Studio skill")

	h, cap := newStudioHarness(t, []llm.Response{textTurn("ordinary answer"), textTurn("studio answer")}, func(c *Config) {
		c.Memory = globalMem
		c.Skills = []skills.Skill{sk}
		// Session-owned instruction storage requires canonical private roots.
		var err error
		c.Cwd, err = filepath.EvalSymlinks(c.Cwd)
		if err != nil {
			t.Fatal(err)
		}
		c.SessionDir, err = filepath.EvalSymlinks(c.SessionDir)
		if err != nil {
			t.Fatal(err)
		}
	})
	ordinary := h.createSession()
	studioAsk(t, h, ordinary, "ordinary question")

	a := createStudioAgent(t, h, "Runtime Bot", "RUNTIME-INSTRUCTIONS-61ac: keep persona.", "ready")
	// This parity case represents a definition saved before Skill/tool binding
	// existed. New definitions now require explicit trusted Skill choices.
	h.mgr.studio.mu.Lock()
	legacy := h.mgr.studio.data.Agents[a.ID]
	legacy.Tools, legacy.Skills, legacy.SkillToolBindings = nil, nil, nil
	h.mgr.studio.data.Agents[a.ID] = legacy
	err := h.mgr.studio.save(h.mgr.studio.data)
	h.mgr.studio.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	web := createStudioConversationBody(t, h, a.ID, "")
	studioAsk(t, h, web.ID, "studio question")

	systems, _, histories, _ := cap.snapshot()
	if len(systems) != 2 || len(histories) != 2 {
		t.Fatalf("model calls = %d, want one ordinary and one Studio call", len(systems))
	}
	suffix := "\n\n# Agent configuration\nYou are Runtime Bot.\nRUNTIME-INSTRUCTIONS-61ac: keep persona."
	// Live access guidance belongs to each session's own workspace and policy.
	// Verify it separately before comparing the shared runtime instructions.
	withoutAccess := func(prompt string, sess *Session) string {
		hint := "\n\n" + sess.fileAccessPrompt()
		if !strings.HasSuffix(prompt, hint) {
			t.Fatal("model prompt missed the session's current file access guidance")
		}
		return strings.TrimSuffix(prompt, hint)
	}
	ordinaryPrompt := withoutAccess(systems[0], h.session(ordinary))
	studioSystem := withoutAccess(systems[1], h.session(web.ID))
	// Only Studio owns the host-bound task tools, so it also receives their
	// conditional handoff guide. Its exact content/tool-selection behavior is
	// covered by agent tests; compare the ordinary runtime independently here.
	studioPrompt, handoffGuide, hasHandoff := strings.Cut(studioSystem, "\n\nFor background-task setup requests,")
	if !hasHandoff || !strings.Contains(handoffGuide, "Do not sleep") || !strings.Contains(handoffGuide, "registration alone is never proof") {
		t.Fatal("Studio task tools missed their foreground-handoff guidance")
	}
	// The two sessions' <env> name their own working directories — the
	// ordinary one at the root, the Studio one in its Agent's agents/<id>
	// slice. That difference is by design; everything else must match, so the
	// working-directory line is normalized away before the exact compare.
	envWorkdir := regexp.MustCompile(`(?m)^working directory: .*$`)
	if got, plain := envWorkdir.ReplaceAllString(studioPrompt, "working directory: <ws>"), envWorkdir.ReplaceAllString(ordinaryPrompt+suffix, "working directory: <ws>"); got != plain {
		t.Fatalf("Studio prompt is not the ordinary prompt plus its Agent instructions:\nordinary:\n%s\nstudio:\n%s", plain, got)
	}
	for _, surface := range []string{"note.md", "studioskill"} {
		if !strings.Contains(systems[1], surface) {
			t.Fatalf("Studio prompt missed ordinary surface %q:\n%s", surface, systems[1])
		}
	}

	schemas := cap.toolSchemas()
	if len(schemas) != 2 || len(schemas[0]) == 0 {
		t.Fatalf("missing runtime tool catalogs: %v", schemas)
	}
	// Private shortcut, Skill lifecycle and workflow tools require a host-bound Agent
	// identity; ordinary sessions cannot select an Agent's private stores.
	shortcutNames := []string{tools.ShortcutQueryName, tools.ShortcutManageName, tools.ShortcutDeleteName, tools.ShortcutMergeName, tools.SkillSearchName, tools.SkillReadName, tools.SkillSaveName, tools.BackgroundTaskName, tools.WatchName, tools.WatchTestName, tools.AgentWorkflowReadName, tools.AgentWorkflowEditName, tools.AgentWorkflowEnableName, tools.AgentWorkflowRunName}
	for _, name := range shortcutNames {
		if slices.Contains(schemas[0], name) || !slices.Contains(schemas[1], name) {
			t.Fatalf("shortcut scope registration incorrect: ordinary=%v studio=%v", schemas[0], schemas[1])
		}
	}
	shared := slices.DeleteFunc(slices.Clone(schemas[1]), func(name string) bool { return slices.Contains(shortcutNames, name) })
	if !slices.Equal(schemas[0], shared) {
		t.Fatalf("Studio changed unrelated tools: ordinary=%v shared=%v", schemas[0], shared)
	}
	if !slices.Contains(schemas[1], "read") {
		t.Fatalf("Studio toolset has no read tool: %v", schemas[1])
	}
	if len(histories[0]) != 1 || studioMessageText(histories[0][0]) != "ordinary question" {
		t.Fatalf("ordinary history = %+v", histories[0])
	}
	if len(histories[1]) != 1 || studioMessageText(histories[1][0]) != "studio question" {
		t.Fatalf("Studio history = %+v", histories[1])
	}
}

func TestStudioWebRunExecutesReadToolInSelectedWorkspace(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "project")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	const marker = "STUDIO-REAL-TOOL-8f2b"
	if err := os.WriteFile(filepath.Join(workspace, "fixture.txt"), []byte(marker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	h, cap := newStudioHarness(t, []llm.Response{
		toolTurn("read", `{"path":"fixture.txt"}`),
		textTurn("read completed"),
	}, func(c *Config) { c.Cwd = root })
	a := createStudioAgent(t, h, "Reader Bot", "Read the fixture before answering.", "ready")
	web := createStudioConversationBody(t, h, a.ID, `{"workspace":"project"}`)

	cwd, model := sessionCwdAndModel(h.session(web.ID))
	if cwd != workspace || model != "k3" {
		t.Fatalf("session cwd/model = %q/%q, want %q/k3", cwd, model, workspace)
	}
	studioAsk(t, h, web.ID, "read the fixture")

	schemas := cap.toolSchemas()
	if len(schemas) != 2 || !slices.Contains(schemas[0], "read") {
		t.Fatalf("model schemas = %v, want read on the first call", schemas)
	}
	_, _, histories, _ := cap.snapshot()
	if len(histories) != 2 {
		t.Fatalf("model calls = %d, want a tool call and a final answer", len(histories))
	}
	found := false
	for _, message := range histories[1] {
		for _, block := range message.Content {
			if block.Type == llm.BlockToolResult && strings.Contains(block.Text, marker) {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("no read result carrying %q reached the final model call: %+v", marker, histories[1])
	}
}

func TestStudioWorkspaceValidatedAgainstServerRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "allowed"), 0o755); err != nil {
		t.Fatal(err)
	}
	h, _ := newStudioHarness(t, []llm.Response{textTurn("ok")}, func(c *Config) { c.Cwd = root })
	a := createStudioAgent(t, h, "Workspace Bot", "Use the selected workspace.", "ready")

	ok := createStudioConversationBody(t, h, a.ID, `{"workspace":"allowed"}`)
	if cwd, _ := sessionCwdAndModel(h.session(ok.ID)); cwd != filepath.Join(root, "allowed") {
		t.Fatalf("workspace cwd = %q, want allowed", cwd)
	}
	for name, workspace := range map[string]string{
		"missing":         "does-not-exist",
		"absolute":        filepath.Join(root, "allowed"),
		"parent-escape":   filepath.FromSlash("../outside"),
		"internal-escape": filepath.FromSlash("allowed/../../outside"),
	} {
		body, _ := json.Marshal(map[string]string{"workspace": workspace})
		resp := h.do(http.MethodPost, "/api/apps/"+a.ID+"/sessions", string(body))
		code := resp.StatusCode
		resp.Body.Close()
		if code != http.StatusBadRequest {
			t.Fatalf("%s workspace %q: %d, want 400", name, workspace, code)
		}
	}
}

func createStudioAgentModel(t *testing.T, h *harness, name, instructions, status, model string) studioAgent {
	t.Helper()
	body := studioAgentBody(name, instructions, status)
	body["model"] = model
	resp := h.do(http.MethodPost, "/api/manage/agents", studioBody(body))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("create agent with model %s: %d (%s)", model, resp.StatusCode, msg)
	}
	var a studioAgent
	if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
		t.Fatal(err)
	}
	return a
}

func addStudioTestModel(t *testing.T, id string) {
	t.Helper()
	// The scripted client never contacts this provider; availability must not
	// depend on a developer's ambient credentials (absent in a clean container).
	t.Setenv("ZHIPU_API_KEY", "studio-test-only")
	models := config.Catalog()
	for _, model := range models {
		if model.ID == id {
			return
		}
	}
	models = append(models, config.Model{ID: id, Provider: "zhipu", ContextWindow: 128_000})
	config.SetCatalogForTest(models)
}

func TestStudioModelPolicySynchronizesAcrossAgentEditAndReload(t *testing.T) {
	dir := t.TempDir()
	h, _ := newStudioHarness(t, []llm.Response{textTurn("before edit")}, func(c *Config) { c.SessionDir = dir })
	addStudioTestModel(t, "glm-5.3")
	a := createStudioAgentModel(t, h, "Pinned Model", "PINNED-MODEL persona.", "ready", "glm-5.3")
	sid := startStudioConversation(t, h, a.ID)

	// A later edit moves model policy to k3 independently of the immutable
	// instruction/tool snapshot already bound to this conversation.
	updateStudioAgent(t, h, a.ID, a.Revision, "Pinned Model", "PINNED-MODEL-V2 persona.", "ready")
	h.mgr.Close()
	h2, cap := newStudioHarness(t, []llm.Response{textTurn("after edit")}, func(c *Config) { c.SessionDir = dir })
	addStudioTestModel(t, "glm-5.3")
	if _, err := h2.mgr.Get(sid); err != nil {
		t.Fatal(err)
	}
	studioAsk(t, h2, sid, "still the old model")

	if models := cap.clientModels(); len(models) != 1 || models[0] != "k3" {
		t.Fatalf("reloaded model factories = %v, want synchronized k3", models)
	}
	_, _, _, extras := cap.snapshot()
	if len(extras) != 1 || extras[0]["reasoning_effort"] != "max" {
		t.Fatalf("synchronized model settings = %+v, want maximum reasoning", extras)
	}
	next := startStudioConversation(t, h2, a.ID)
	if _, model := sessionCwdAndModel(h2.session(next)); model != "k3" {
		t.Fatalf("new conversation model = %q, want edited definition model k3", model)
	}
}

func TestStudioOwnerWorkspaceAndBindingOwnershipFailClosed(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("ok")))
	alice, bob := h.alice(t), h.bob(t)
	resp := h.doAs(h.token, http.MethodPost, "/api/manage/agents", studioBody(studioAgentBody("Owned Bot", "OWNED persona.", "ready")))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("admin create agent: %d (%s)", resp.StatusCode, msg)
	}
	var a studioAgent
	if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
		t.Fatal(err)
	}
	scopeOf := func(sub string) identity.Scope {
		return identity.LocalScope(sub)
	}
	aliceRoot := h.mgr.UserRoot(scopeOf("alice"))
	aliceWorkspace := filepath.Join(aliceRoot, "alpha")
	if err := os.MkdirAll(aliceWorkspace, 0o755); err != nil {
		t.Fatal(err)
	}
	mk := h.doAs(alice, http.MethodPost, "/api/apps/"+a.ID+"/sessions", `{"workspace":"alpha"}`)
	if mk.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(mk.Body)
		mk.Body.Close()
		t.Fatalf("alice create workspace conversation: %d (%s)", mk.StatusCode, msg)
	}
	var conv studioConversation
	if err := json.NewDecoder(mk.Body).Decode(&conv); err != nil {
		t.Fatal(err)
	}
	mk.Body.Close()
	if cwd, _ := sessionCwdAndModel(h.session(conv.ID)); cwd != aliceWorkspace {
		t.Fatalf("owner workspace = %q, want %q", cwd, aliceWorkspace)
	}

	// The same relative resolver is owner-rooted: Bob cannot name his way into
	// another user's tree, even with a syntactically relative path.
	bad := h.doAs(bob, http.MethodPost, "/api/apps/"+a.ID+"/sessions", `{"workspace":"../../server"}`)
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("bob escaping workspace: %d, want 400", bad.StatusCode)
	}

	// Tampering with the in-memory owner snapshot after the catalog has spoken
	// must fail the load, not silently build Bob's workspace for Alice's file.
	h.mgr.mu.Lock()
	delete(h.mgr.sessions, conv.ID)
	h.mgr.mu.Unlock()
	tampered := scopeOf("bob")
	h.mgr.studio.mu.Lock()
	binding := h.mgr.studio.data.Bindings[conv.ID]
	binding.Owner = &tampered
	h.mgr.studio.data.Bindings[conv.ID] = binding
	h.mgr.studio.mu.Unlock()
	if _, err := h.mgr.Get(conv.ID); err == nil || !strings.Contains(err.Error(), "ownership does not match") {
		t.Fatalf("binding/catalog owner mismatch = %v, want a closed load failure", err)
	}
}

func TestStudioWebRuntimeBindingMismatchFailsClosed(t *testing.T) {
	dir := t.TempDir()
	h, _ := newStudioHarness(t, []llm.Response{textTurn("ok")}, func(c *Config) { c.SessionDir = dir })
	a := createStudioAgent(t, h, "Runtime Marker Bot", "RUNTIME persona.", "ready")
	sid := startStudioConversation(t, h, a.ID)

	registry := filepath.Join(dir, "agent-studio.json")
	raw, err := os.ReadFile(registry)
	if err != nil {
		t.Fatal(err)
	}
	var data studioData
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	binding := data.Bindings[sid]
	binding.Runtime = studioRuntime
	data.Bindings[sid] = binding
	out, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registry, out, 0o600); err != nil {
		t.Fatal(err)
	}

	h.mgr.Close()
	h2 := newHarness(t, scriptedTurns(textTurn("must not run")), func(c *Config) { c.SessionDir = dir })
	if _, err := h2.mgr.Get(sid); err == nil || !strings.Contains(err.Error(), "runtime binding") {
		t.Fatalf("legacy binding with Web meta loaded as %v, want a runtime failure", err)
	}
	if h2.calls() != 0 {
		t.Fatalf("mismatched runtime reached the model %d times", h2.calls())
	}
}

func TestStudioWebNativeEndpointsWorkAndModelSwitchStaysInAllowlist(t *testing.T) {
	dir := t.TempDir()
	h, _ := newStudioHarness(t, []llm.Response{textTurn("ok")}, func(c *Config) { c.SessionDir = dir })
	addStudioTestModel(t, "glm-5.3")
	a := createStudioAgent(t, h, "Native Bot", "NATIVE persona.", "ready")
	sid := startStudioConversation(t, h, a.ID)

	patch := h.do(http.MethodPatch, "/api/sessions/"+sid, `{"title":"Native title","pinned":true}`)
	patch.Body.Close()
	if patch.StatusCode != http.StatusNoContent {
		t.Fatalf("native PATCH session: %d, want %d", patch.StatusCode, http.StatusNoContent)
	}
	infos, err := session.List(dir)
	if err != nil {
		t.Fatal(err)
	}
	var info *session.Info
	for i := range infos {
		if infos[i].ID == sid {
			info = &infos[i]
			break
		}
	}
	if info == nil || info.Title != "Native title" || !info.Pinned {
		t.Fatalf("native pin/title summary = %+v, want both edits", info)
	}

	upload := upload2(t, h, sid, pngBytes2(t, 4, 4), "studio-native.png")
	if !strings.HasPrefix(filepath.ToSlash(upload.Path), "uploads/") {
		t.Fatalf("attachment path = %q, want an upload path", upload.Path)
	}
	cwd, _ := sessionCwdAndModel(h.session(sid))
	if _, err := os.Stat(filepath.Join(cwd, filepath.FromSlash(upload.Path))); err != nil {
		t.Fatalf("uploaded attachment: %v", err)
	}
	list := h.do(http.MethodGet, "/api/sessions/"+sid+"/attachments", "")
	var attachments struct {
		Attachments []AttachmentMeta `json:"attachments"`
	}
	if err := json.NewDecoder(list.Body).Decode(&attachments); err != nil {
		t.Fatal(err)
	}
	list.Body.Close()
	if len(attachments.Attachments) != 1 || attachments.Attachments[0].Path != upload.Path {
		t.Fatalf("native attachment list = %+v, want %q", attachments.Attachments, upload.Path)
	}

	// Cancel with no active run exercises the control route without creating a
	// terminal or relying on a destructive child process.
	h.post("/api/sessions/"+sid+"/control", `{"action":"cancel"}`, http.StatusOK)
	h.post("/api/apps/"+a.ID+"/sessions/"+sid+"/cancel", "", http.StatusOK)
	// The allowlist of a legacy definition is exactly its default model:
	// anything else is refused, the default itself goes through.
	h.post("/api/sessions/"+sid+"/control", `{"action":"set_model","model":"glm-5.3"}`, http.StatusBadRequest)
	h.post("/api/sessions/"+sid+"/control", `{"action":"set_model","model":"k3"}`, http.StatusOK)
}

func TestStudioLegacyTextOnlyBindingStaysToollessAndRestricted(t *testing.T) {
	dir := t.TempDir()
	h, cap := newStudioHarness(t, []llm.Response{textTurn("legacy answer")}, func(c *Config) { c.SessionDir = dir })
	a := createStudioAgent(t, h, "Legacy Bot", "LEGACY-INSTRUCTIONS-4a19.", "ready")
	store, err := session.CreateAgentConversation(dir, h.mgr.cfg.Cwd, "k3")
	if err != nil {
		t.Fatal(err)
	}
	sid := strings.TrimSuffix(filepath.Base(store.Path()), ".jsonl")
	if err := h.mgr.studio.bind(sid, a, nil); err != nil {
		t.Fatal(err)
	}
	binding, ok := h.mgr.studio.binding(sid)
	if !ok || binding.Runtime != studioRuntime || binding.runtime() != studioRuntime {
		t.Fatalf("legacy binding = %+v, effective runtime %q", binding, binding.runtime())
	}
	if meta := store.Meta(); meta == nil || meta.Runtime != studioRuntime {
		t.Fatalf("legacy transcript runtime = %+v", store.Meta())
	}
	studioAsk(t, h, sid, "legacy question")

	systems, toolCounts, _, _ := cap.snapshot()
	if len(systems) != 1 || toolCounts[0] != 0 {
		t.Fatalf("legacy model calls/tools = %d/%d, want one call with no tools", len(systems), toolCounts[0])
	}
	for _, want := range []string{"no external tools", "LEGACY-INSTRUCTIONS-4a19"} {
		if !strings.Contains(systems[0], want) {
			t.Fatalf("legacy prompt missed %q:\n%s", want, systems[0])
		}
	}
	for _, check := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/sessions/" + sid + "/control", `{"action":"cancel"}`},
		{http.MethodGet, "/api/sessions/" + sid + "/terminal", ""},
		{http.MethodPost, "/api/sessions/" + sid + "/attachments", "{}"},
		{http.MethodGet, "/api/sessions/" + sid + "/attachments", ""},
		{http.MethodPost, "/api/sessions/" + sid + "/share", "{}"},
		{http.MethodPatch, "/api/sessions/" + sid, `{"title":"nope"}`},
		{http.MethodDelete, "/api/sessions/" + sid, ""},
	} {
		if code := studioStatus(t, h, check.method, check.path, check.body); code != http.StatusForbidden {
			t.Fatalf("legacy %s %s: %d, want 403", check.method, check.path, code)
		}
	}

	// Bindings written before the Web runtime existed have no persisted Runtime
	// field; the effective-runtime compatibility rule must keep them text-only.
	registry := filepath.Join(dir, "agent-studio.json")
	raw, err := os.ReadFile(registry)
	if err != nil {
		t.Fatal(err)
	}
	var data studioData
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	legacy := data.Bindings[sid]
	legacy.Runtime = ""
	data.Bindings[sid] = legacy
	out, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registry, out, 0o600); err != nil {
		t.Fatal(err)
	}

	h.mgr.Close()
	h2, cap2 := newStudioHarness(t, []llm.Response{textTurn("legacy reloaded")}, func(c *Config) { c.SessionDir = dir })
	binding, ok = h2.mgr.studio.binding(sid)
	if !ok || binding.Runtime != "" || binding.runtime() != studioRuntime {
		t.Fatalf("reloaded empty-runtime binding = %+v, effective runtime %q", binding, binding.runtime())
	}
	if _, err := h2.mgr.Get(sid); err != nil {
		t.Fatal(err)
	}
	studioAsk(t, h2, sid, "legacy reload")
	systems, toolCounts, _, _ = cap2.snapshot()
	if len(systems) != 1 || toolCounts[0] != 0 {
		t.Fatalf("reloaded legacy calls/tools = %d/%d, want one call with no tools", len(systems), toolCounts[0])
	}
	if !strings.Contains(systems[0], "no external tools") {
		t.Fatalf("reloaded legacy prompt gained workbench tools:\n%s", systems[0])
	}
}

// 不带 workspace 的会话落进该 Agent 自己的默认工作区（服务器根下
// agents/<id>，首次使用自动创建）：同一 Agent 的会话共享一棵树，两个
// Agent 各自一棵；显式 workspace 仍然照选；手工编辑进 agent-studio.json
// 的路径形 id 不参与派生，会话回到根上而不是逃出它。
func TestStudioConversationDefaultsToPerAgentWorkspace(t *testing.T) {
	root := t.TempDir()
	h, _ := newStudioHarness(t, []llm.Response{textTurn("ok")}, func(c *Config) { c.Cwd = root })

	a := createStudioAgent(t, h, "Default Bot", "stay in your lane.", "ready")
	first := createStudioConversationBody(t, h, a.ID, "")
	wantA := filepath.Join(root, "agents", a.ID)
	if _, err := os.Stat(wantA); err != nil {
		t.Fatalf("default agent workspace not created: %v", err)
	}
	if cwd, _ := sessionCwdAndModel(h.session(first.ID)); cwd != wantA {
		t.Fatalf("first session cwd = %q, want %q", cwd, wantA)
	}
	second := createStudioConversationBody(t, h, a.ID, "")
	if cwd, _ := sessionCwdAndModel(h.session(second.ID)); cwd != wantA {
		t.Fatalf("second session cwd = %q, want the same %q", cwd, wantA)
	}

	b := createStudioAgent(t, h, "Other Bot", "another desk.", "ready")
	other := createStudioConversationBody(t, h, b.ID, "")
	wantB := filepath.Join(root, "agents", b.ID)
	if cwd, _ := sessionCwdAndModel(h.session(other.ID)); cwd != wantB {
		t.Fatalf("other agent session cwd = %q, want its own %q", cwd, wantB)
	}

	// 显式选择仍被尊重：共享目录照旧。
	shared := filepath.Join(root, "shared")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	chosen := createStudioConversationBody(t, h, a.ID, `{"workspace":"shared"}`)
	if cwd, _ := sessionCwdAndModel(h.session(chosen.ID)); cwd != shared {
		t.Fatalf("explicit workspace overridden: cwd = %q, want %q", cwd, shared)
	}

	// ä¸ URL å®å¨ï¼ä½è¿ä¸äºå½¢ç¶é¨ï¼å "â¦" ç±» id çä»£è¡¨ï¼ã
	// "ag:evil"：单段、URL 安全，但过不了形状门（".." 类 id 的代表——
	// 它们在 URL 层就会被清掉，到不了这里）。
	const evilID = "ag:evil"
	h.mgr.studio.mu.Lock()
	h.mgr.studio.data.Agents[evilID] = studioAgent{
		studioInput: studioInput{Name: "Evil", Instructions: "hand-edited row", Model: "k3", Status: "ready"},
		ID:          evilID,
	}
	h.mgr.studio.mu.Unlock()
	evil := createStudioConversationBody(t, h, evilID, "")
	if cwd, _ := sessionCwdAndModel(h.session(evil.ID)); cwd != root {
		t.Fatalf("path-shaped id session cwd = %q, want fallback %q", cwd, root)
	}
	if _, err := os.Stat(filepath.Join(root, "agents", evilID)); err == nil {
		t.Fatal("workspace directory materialized for a shape-invalid id")
	}
}
