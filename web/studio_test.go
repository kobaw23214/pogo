package web

// Agent Studio boundary tests. Everything here drives the real HTTP surface
// with the scripted model: definitions, pinned conversations, the Web runtime,
// authorization, and the legacy text-only compatibility boundary.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/memory"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/skills"
)

// --- observing client and small helpers ---------------------------------------

// studioCapture records what each model call in a Studio conversation really
// saw: system prompt, tool schemas, history, and the resolved provider body.
type studioCapture struct {
	mu        sync.Mutex
	turns     int
	model     string
	systems   []string
	tools     []int
	toolNames [][]string
	models    []string
	histories [][]llm.Message
	extras    []map[string]any
}

type studioClient struct {
	cap  *studioCapture
	next llm.Client
}

func (c *studioClient) Model() string { return c.next.Model() }

func (c *studioClient) Stream(ctx context.Context, system string, history []llm.Message, schemas []llm.ToolSchema, onDelta func(llm.Delta)) (llm.Response, error) {
	c.cap.mu.Lock()
	c.cap.systems = append(c.cap.systems, system)
	c.cap.tools = append(c.cap.tools, len(schemas))
	names := make([]string, len(schemas))
	for i, schema := range schemas {
		names[i] = schema.Name
	}
	c.cap.toolNames = append(c.cap.toolNames, names)
	c.cap.histories = append(c.cap.histories, append([]llm.Message(nil), history...))
	c.cap.mu.Unlock()
	return c.next.Stream(ctx, system, history, schemas, onDelta)
}

func (c *studioCapture) snapshot() (systems []string, toolCounts []int, histories [][]llm.Message, extras []map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.systems...), append([]int(nil), c.tools...),
		append([][]llm.Message(nil), c.histories...), append([]map[string]any(nil), c.extras...)
}

func (c *studioCapture) toolSchemas() [][]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]string(nil), c.toolNames...)
}

func (c *studioCapture) clientModels() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.models...)
}

// newStudioHarness is newHarness with the model factory swapped for one that
// records the Studio boundary facts these tests assert on.
func newStudioHarness(t *testing.T, turns []llm.Response, tweaks ...func(*Config)) (*harness, *studioCapture) {
	t.Helper()
	script := scriptedTurns(turns...)
	cap := &studioCapture{}
	wire := func(c *Config) {
		c.NewClient = func(rc config.Resolved, _ func(llm.RetryInfo)) llm.Client {
			cap.mu.Lock()
			cap.model = rc.Model
			cap.models = append(cap.models, rc.Model)
			cap.extras = append(cap.extras, maps.Clone(rc.ExtraBody))
			cap.mu.Unlock()
			inner := &scriptedClient{model: rc.Model, respond: func() (llm.Response, error) {
				cap.mu.Lock()
				cap.turns++
				n := cap.turns
				cap.mu.Unlock()
				return script(n)
			}}
			return &studioClient{cap: cap, next: inner}
		}
	}
	h := newHarness(t, script, append(append([]func(*Config){}, tweaks...), wire)...)
	return h, cap
}

func studioBody(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestStudioConversationApprovalMode(t *testing.T) {
	h, _ := newStudioHarness(t, nil)
	a := createStudioAgent(t, h, "approval mode", "", "ready")
	for _, tc := range []struct {
		body string
		want Mode
	}{
		{body: `{}`, want: ModeStandard},
		{body: `{"approval_mode":"standard"}`, want: ModeStandard},
		{body: `{"approval_mode":"strict"}`, want: ModeStrict},
		{body: `{"approval_mode":"auto"}`, want: ModeAuto},
	} {
		resp := h.do(http.MethodPost, "/api/apps/"+a.ID+"/sessions", tc.body)
		var out struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			resp.Body.Close()
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create %s: status %d", tc.body, resp.StatusCode)
		}
		if got := h.session(out.ID).policy.State().Mode; got != string(tc.want) {
			t.Fatalf("create %s: policy = %q, want %q", tc.body, got, tc.want)
		}
	}
	resp := h.do(http.MethodPost, "/api/apps/"+a.ID+"/sessions", `{"approval_mode":"full"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid approval mode: status %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestStudioMaxReasoningIsPerAgentAndModel(t *testing.T) {
	for _, model := range []string{"k3", "kimi-k3", "GLM-5.3"} {
		t.Run(model, func(t *testing.T) {
			original := config.Resolved{Model: model, ExtraBody: map[string]any{"reasoning_effort": "low", "thinking": map[string]any{"type": "disabled"}, "vendor_flag": true}}
			resolved := applyStudioEffort(original)
			if resolved.ExtraBody["reasoning_effort"] != "max" || resolved.ExtraBody["thinking"].(map[string]any)["type"] != "enabled" {
				t.Fatalf("Studio did not select maximum reasoning: %+v", resolved.ExtraBody)
			}
			if original.ExtraBody["reasoning_effort"] != "low" || original.ExtraBody["thinking"].(map[string]any)["type"] != "disabled" || resolved.ExtraBody["vendor_flag"] != true {
				t.Fatal("Studio changed shared provider settings or discarded a provider flag")
			}
		})
	}
	other := config.Resolved{Model: "other-model", ExtraBody: map[string]any{"reasoning_effort": "high"}}
	if applyStudioEffort(other).ExtraBody["reasoning_effort"] != "high" {
		t.Fatal("Studio forced an unsupported reasoning option on another model")
	}
}

func studioAgentBody(name, instructions, status string) map[string]any {
	return map[string]any{
		"name":         name,
		"description":  "studio boundary test agent",
		"instructions": instructions,
		"model":        "k3",
		"status":       status,
		"color":        "indigo",
		"icon":         "spark",
		"category":     "general",
		"starters":     []string{"hello"},
	}
}

func createStudioAgent(t *testing.T, h *harness, name, instructions, status string) studioAgent {
	t.Helper()
	resp := h.do(http.MethodPost, "/api/manage/agents", studioBody(studioAgentBody(name, instructions, status)))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("create agent: %d (%s)", resp.StatusCode, msg)
	}
	var a studioAgent
	if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
		t.Fatal(err)
	}
	return a
}

func updateStudioAgent(t *testing.T, h *harness, id string, revision int, name, instructions, status string) studioAgent {
	t.Helper()
	body := studioAgentBody(name, instructions, status)
	body["revision"] = revision
	resp := h.do(http.MethodPatch, "/api/manage/agents/"+id, studioBody(body))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("update agent %s: %d (%s)", id, resp.StatusCode, msg)
	}
	var a studioAgent
	if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
		t.Fatal(err)
	}
	return a
}

func deleteStudioAgent(t *testing.T, h *harness, id string) {
	t.Helper()
	resp := h.do(http.MethodDelete, "/api/manage/agents/"+id, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("delete agent %s: %d (%s)", id, resp.StatusCode, msg)
	}
}

func studioListAgents(t *testing.T, h *harness) []studioAgent {
	t.Helper()
	resp := h.do(http.MethodGet, "/api/manage/agents", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("list agents: %d (%s)", resp.StatusCode, msg)
	}
	var out struct {
		Agents []studioAgent `json:"agents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Agents
}

func startStudioConversation(t *testing.T, h *harness, agentID string) string {
	t.Helper()
	resp := h.do(http.MethodPost, "/api/apps/"+agentID+"/sessions", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("create conversation for %s: %d (%s)", agentID, resp.StatusCode, msg)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.ID
}

func studioStatus(t *testing.T, h *harness, method, path, body string) int {
	t.Helper()
	resp := h.do(method, path, body)
	code := resp.StatusCode
	resp.Body.Close()
	// The body is intentionally not read: some denied endpoints stream and would block.
	t.Logf("%s %s -> %d", method, path, code)
	return code
}

// studioAsk subscribes, sends one prompt, and waits for the run to settle.
func studioAsk(t *testing.T, h *harness, sid, prompt string) Event {
	t.Helper()
	s := h.stream(sid, "")
	if first := s.next(t); first.Type != EvSnapshot {
		t.Fatalf("first frame = %s, want snapshot", first.Type)
	}
	h.post("/api/sessions/"+sid+"/messages", fmt.Sprintf(`{"prompt":%q}`, prompt), http.StatusAccepted)
	end := s.wait(t, EvRunEnd)
	s.close()
	if end.Error != "" {
		t.Fatalf("run ended with error %q", end.Error)
	}
	return end
}

func studioBlocksText(blocks []llm.Block) string {
	var b strings.Builder
	for _, block := range blocks {
		if block.Type == llm.BlockText {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}

func studioMessageText(m llm.Message) string { return studioBlocksText(m.Content) }

// --- definitions ---------------------------------------------------------------

func TestStudioAgentLifecyclePersistsRevisionAndAvailability(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, scriptedTurns(textTurn("ok")), func(c *Config) { c.SessionDir = dir })

	a := createStudioAgent(t, h, "Greeter", "Always greet warmly.", "ready")
	if !strings.HasPrefix(a.ID, "ag_") || a.Revision != 1 || a.Status != "ready" {
		t.Fatalf("created agent = %+v", a)
	}

	// The definition lives on disk: a second process sees it intact.
	h.mgr.Close()
	h2 := newHarness(t, scriptedTurns(textTurn("ok")), func(c *Config) { c.SessionDir = dir })
	list := studioListAgents(t, h2)
	if len(list) != 1 || list[0].ID != a.ID || list[0].Revision != 1 || list[0].Instructions != "Always greet warmly." {
		t.Fatalf("reloaded registry = %+v", list)
	}

	// Update bumps the revision; replaying the old one is a conflict.
	up := updateStudioAgent(t, h2, a.ID, 1, "Greeter", "Always greet briefly.", "ready")
	if up.Revision != 2 {
		t.Fatalf("updated revision = %d, want 2", up.Revision)
	}
	stale := studioAgentBody("Greeter", "Always greet warmly.", "ready")
	stale["revision"] = 1
	if code := studioStatus(t, h2, http.MethodPatch, "/api/manage/agents/"+a.ID, studioBody(stale)); code != http.StatusConflict {
		t.Fatalf("stale revision update: %d, want 409", code)
	}

	// Drafts are definitions, not conversable apps.
	updateStudioAgent(t, h2, a.ID, 2, "Greeter", "Always greet briefly.", "draft")
	if code := studioStatus(t, h2, http.MethodPost, "/api/apps/"+a.ID+"/sessions", ""); code != http.StatusNotFound {
		t.Fatalf("draft agent conversation: %d, want 404", code)
	}

	// Removal disables new runs immediately.
	deleteStudioAgent(t, h2, a.ID)
	if list := studioListAgents(t, h2); len(list) != 0 {
		t.Fatalf("deleted agent still listed: %+v", list)
	}
	if code := studioStatus(t, h2, http.MethodPost, "/api/apps/"+a.ID+"/sessions", ""); code != http.StatusNotFound {
		t.Fatalf("deleted agent conversation: %d, want 404", code)
	}
}

// --- the runtime boundary --------------------------------------------------------

func TestStudioConversationBoundaryEndToEnd(t *testing.T) {
	memRoot := filepath.Join(t.TempDir(), "global-memory")
	if err := os.MkdirAll(filepath.Join(memRoot, ".pogo", "memory", "notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memRoot, ".pogo", "memory", "notes", "secret.md"), []byte("global memory secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	globalMem, _ := memory.Load(memory.Options{Project: true, Cwd: memRoot})
	sk := writeTestSkill(t, t.TempDir(), "leakskill", "a legacy skill that must not leak", "# Leak")

	h, cap := newStudioHarness(t, []llm.Response{textTurn("studio answer 42")}, func(c *Config) {
		c.Memory = globalMem
		c.Skills = []skills.Skill{sk}
	})
	// Provider-level extra body must survive the Studio defaults, and the
	// defaults must stay on the conversation's copy only.
	providers := config.Providers()
	if kimi, ok := providers["kimi"]; ok {
		kimi.ExtraBody = map[string]any{"vendor": "original"}
		providers["kimi"] = kimi
		config.SetProvidersForTest(providers)
	}

	a := createStudioAgent(t, h, "Boundary Bot", "STUDIO-INSTRUCTIONS-7f3a: stay in persona.", "ready")
	a = makeStudioAgentLegacy(t, h, a)
	sid := startStudioConversation(t, h, a.ID)

	s := h.stream(sid, "")
	first := s.next(t)
	if first.Type != EvSnapshot || first.Snapshot == nil {
		t.Fatalf("first frame = %+v, want snapshot", first)
	}
	if first.Snapshot.Run.Model != "k3" {
		t.Fatalf("snapshot model = %q, want k3", first.Snapshot.Run.Model)
	}
	h.post("/api/sessions/"+sid+"/messages", `{"prompt":"what is the answer"}`, http.StatusAccepted)
	end := s.wait(t, EvRunEnd)
	if end.Error != "" {
		t.Fatalf("run ended with error %q", end.Error)
	}
	s.close()

	systems, toolCounts, histories, extras := cap.snapshot()
	if len(systems) != 1 || len(toolCounts) != 1 || len(histories) != 1 {
		t.Fatalf("model calls = %d, want exactly one", len(systems))
	}
	if !strings.Contains(systems[0], "You are Boundary Bot") || !strings.Contains(systems[0], "STUDIO-INSTRUCTIONS-7f3a") {
		t.Fatalf("system prompt does not carry the Agent definition:\n%s", systems[0])
	}
	// A Studio conversation enters the ordinary Pogo Web runtime: the normal
	// workbench surfaces are present, then its immutable Agent persona follows.
	for _, inherited := range []string{"secret.md", "leakskill", "# Agent configuration", "STUDIO-INSTRUCTIONS-7f3a"} {
		if !strings.Contains(systems[0], inherited) {
			t.Fatalf("system prompt missed %q:\n%s", inherited, systems[0])
		}
	}
	names := cap.toolSchemas()[0]
	foundRead := false
	for _, name := range names {
		foundRead = foundRead || name == "read"
	}
	if toolCounts[0] == 0 || !foundRead {
		t.Fatalf("tool schemas = %v, want the real Pogo read tool", names)
	}
	if len(histories[0]) != 1 || histories[0][0].Role != "user" || studioMessageText(histories[0][0]) != "what is the answer" {
		t.Fatalf("first-call history = %+v, want only the prompt", histories[0])
	}

	if len(extras) != 1 {
		t.Fatalf("client factories = %d, want one", len(extras))
	}
	extra := extras[0]
	if extra["reasoning_effort"] != "max" {
		t.Fatalf("reasoning_effort = %v, want max", extra["reasoning_effort"])
	}
	thinking, ok := extra["thinking"].(map[string]any)
	if !ok || thinking["type"] != "enabled" {
		t.Fatalf("thinking = %v, want enabled", extra["thinking"])
	}
	if extra["vendor"] != "original" {
		t.Fatalf("provider extra body lost: %v", extra)
	}
	fresh, err := config.Resolve("k3")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ExtraBody["reasoning_effort"] != nil || fresh.ExtraBody["thinking"] != nil {
		t.Fatalf("Studio defaults leaked into provider settings: %v", fresh.ExtraBody)
	}
	if fresh.ExtraBody["vendor"] != "original" {
		t.Fatalf("provider settings changed: %v", fresh.ExtraBody)
	}

	snap := h.session(sid).Hub().Snapshot()
	if len(snap.Messages) != 2 || snap.Messages[0].Role != "user" || snap.Messages[1].Role != "assistant" {
		t.Fatalf("snapshot messages = %+v, want one user and one assistant turn", snap.Messages)
	}
	if !strings.Contains(studioBlocksText(snap.Messages[1].Content), "studio answer 42") {
		t.Fatalf("assistant snapshot text = %q", studioBlocksText(snap.Messages[1].Content))
	}
}

func TestStudioConversationPinsSnapshotAndAgentsDoNotShareHistory(t *testing.T) {
	dir := t.TempDir()
	h, _ := newStudioHarness(t, []llm.Response{textTurn("alpha answer")}, func(c *Config) { c.SessionDir = dir })
	alpha := createStudioAgent(t, h, "Alpha Agent", "MARKER-ALPHA persona.", "ready")
	beta := createStudioAgent(t, h, "Beta Agent", "MARKER-BETA persona.", "ready")

	convAlpha := startStudioConversation(t, h, alpha.ID)
	studioAsk(t, h, convAlpha, "alpha question one")

	// Edit Alpha after the conversation exists; the binding must keep rev 1.
	updateStudioAgent(t, h, alpha.ID, 1, "Alpha Agent", "MARKER-ALPHA-V2 persona.", "ready")

	h.mgr.Close()
	h2, cap2 := newStudioHarness(t, []llm.Response{textTurn("alpha second answer"), textTurn("beta answer")}, func(c *Config) { c.SessionDir = dir })
	if code := studioStatus(t, h2, http.MethodGet, "/api/sessions/"+convAlpha, ""); code != http.StatusOK {
		t.Fatalf("reload bound conversation: %d, want 200", code)
	}
	studioAsk(t, h2, convAlpha, "alpha question two")

	systems, toolCounts, histories, _ := cap2.snapshot()
	if len(systems) != 1 {
		t.Fatalf("model calls after reload = %d, want one", len(systems))
	}
	if !strings.Contains(systems[0], "MARKER-ALPHA") || strings.Contains(systems[0], "MARKER-ALPHA-V2") {
		t.Fatalf("reloaded conversation did not pin its snapshot:\n%s", systems[0])
	}
	if toolCounts[0] == 0 {
		t.Fatalf("reloaded conversation tools = %d, want the Web runtime toolset", toolCounts[0])
	}
	own := studioMessageText(histories[0][0]) + " " + studioMessageText(histories[0][len(histories[0])-1])
	if !strings.Contains(own, "alpha question one") || !strings.Contains(own, "alpha question two") {
		t.Fatalf("reloaded history = %q, want both of this conversation's prompts", own)
	}

	convBeta := startStudioConversation(t, h2, beta.ID)
	studioAsk(t, h2, convBeta, "beta question")
	systems, _, histories, _ = cap2.snapshot()
	if !strings.Contains(systems[1], "MARKER-BETA") || strings.Contains(systems[1], "MARKER-ALPHA") {
		t.Fatalf("second Agent saw the first Agent's instructions:\n%s", systems[1])
	}
	if len(histories[1]) != 1 || studioMessageText(histories[1][0]) != "beta question" {
		t.Fatalf("second Agent history = %+v, want only its own prompt", histories[1])
	}
}

func TestStudioRegistryCorruptionAndInconsistencyFailClosed(t *testing.T) {
	dir := t.TempDir()
	if _, err := openStudioStore(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agent-studio.json")

	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openStudioStore(dir); err == nil || !strings.Contains(err.Error(), "agent studio registry") {
		t.Fatalf("corrupt registry error = %v", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := openStudioStore(dir); err == nil || !strings.Contains(err.Error(), "missing from an initialized") {
		t.Fatalf("missing initialized registry error = %v", err)
	}

	// Structurally valid JSON whose binding points at a definition that is
	// not there must also fail, not silently drop the conversation.
	if err := os.Remove(path + ".initialized"); err != nil {
		t.Fatal(err)
	}
	bad := studioData{
		Version: studioFormatVersion,
		Agents:  map[string]studioAgent{},
		Bindings: map[string]studioBinding{
			"conv_missing": {
				Agent:     studioAgent{studioInput: studioInput{Name: "Ghost", Model: "k3"}, ID: "ag_missing", Revision: 1},
				CreatedAt: "2026-01-01T00:00:00Z",
			},
		},
	}
	raw, err := json.Marshal(bad)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openStudioStore(dir); err == nil || !strings.Contains(err.Error(), "missing definition") {
		t.Fatalf("dangling binding error = %v", err)
	}
}

func TestStudioRuntimeTranscriptWithoutBindingNeverBecomesLegacy(t *testing.T) {
	dir := t.TempDir()
	h, _ := newStudioHarness(t, []llm.Response{textTurn("first studio answer")}, func(c *Config) { c.SessionDir = dir })
	a := createStudioAgent(t, h, "Pinned Bot", "PINNED persona.", "ready")
	sid := startStudioConversation(t, h, a.ID)
	studioAsk(t, h, sid, "first question")

	st, err := session.Open(filepath.Join(dir, sid+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if meta := st.Meta(); meta == nil || meta.Runtime != "agent-studio-web-v1" {
		t.Fatalf("transcript runtime marker = %+v, want agent-studio-web-v1", st.Meta())
	}

	registry := filepath.Join(dir, "agent-studio.json")
	raw, err := os.ReadFile(registry)
	if err != nil {
		t.Fatal(err)
	}
	var data studioData
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	data.Bindings = map[string]studioBinding{}
	out, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registry, out, 0o600); err != nil {
		t.Fatal(err)
	}

	h.mgr.Close()
	h2 := newHarness(t, scriptedTurns(textTurn("legacy answer")), func(c *Config) { c.SessionDir = dir })
	if _, err := h2.mgr.Get(sid); err == nil || !strings.Contains(err.Error(), "binding") {
		t.Fatalf("unbound runtime-marked transcript load = %v, want a binding failure", err)
	}
	if code := studioStatus(t, h2, http.MethodGet, "/api/sessions/"+sid, ""); code != http.StatusInternalServerError {
		t.Fatalf("unbound transcript GET: %d, want 500", code)
	}
	if code := studioStatus(t, h2, http.MethodPost, "/api/sessions/"+sid+"/messages", `{"prompt":"resume"}`); code != http.StatusInternalServerError {
		t.Fatalf("unbound transcript run: %d, want 500", code)
	}
	if h2.calls() != 0 {
		t.Fatalf("legacy model called %d times for an unbound Studio transcript", h2.calls())
	}
	if list := studioListAgents(t, h2); len(list) != 1 || list[0].ID != a.ID {
		t.Fatalf("definitions lost with the binding: %+v", list)
	}
}

// --- authorization and per-user isolation ----------------------------------------

func TestStudioManagementAndConversationsAreUserScoped(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("ok")))
	alice, bob := h.alice(t), h.bob(t)

	res, err := h.srv.Client().Get(h.srv.URL + "/api/manage/agents")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous manage request: %d, want 401", res.StatusCode)
	}

	for _, check := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/manage/agents", studioBody(studioAgentBody("Nope", "nope", "ready"))},
		{http.MethodPatch, "/api/manage/agents/ag_missing", `{"revision":1}`},
		{http.MethodDelete, "/api/manage/agents/ag_missing", ""},
	} {
		resp := h.doAs(alice, check.method, check.path, check.body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("ordinary user %s %s: %d, want 403", check.method, check.path, resp.StatusCode)
		}
	}

	resp := h.doAs(h.token, http.MethodPost, "/api/manage/agents", studioBody(studioAgentBody("Shared Bot", "SHARED persona.", "ready")))
	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("admin create agent: %d (%s)", resp.StatusCode, msg)
	}
	var a studioAgent
	if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	mk := h.doAs(alice, http.MethodPost, "/api/apps/"+a.ID+"/sessions", "")
	if mk.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(mk.Body)
		mk.Body.Close()
		t.Fatalf("alice create conversation: %d (%s)", mk.StatusCode, msg)
	}
	var conv struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(mk.Body).Decode(&conv); err != nil {
		t.Fatal(err)
	}
	mk.Body.Close()

	// Bob must not read, list or cancel alice's conversation.
	for _, check := range []struct{ method, path string }{
		{http.MethodGet, "/api/sessions/" + conv.ID},
		{http.MethodGet, "/api/sessions/" + conv.ID + "/stream"},
		{http.MethodPost, "/api/apps/" + a.ID + "/sessions/" + conv.ID + "/cancel"},
	} {
		resp := h.doAs(bob, check.method, check.path, "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("bob %s %s: %d, want 404", check.method, check.path, resp.StatusCode)
		}
	}
	listIDs := func(bearer string) map[string]bool {
		resp := h.doAs(bearer, http.MethodGet, "/api/apps/"+a.ID+"/sessions", "")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list conversations: %d", resp.StatusCode)
		}
		var out struct {
			Sessions []struct {
				ID string `json:"id"`
			} `json:"sessions"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		ids := map[string]bool{}
		for _, s := range out.Sessions {
			ids[s.ID] = true
		}
		return ids
	}
	if ids := listIDs(bob); ids[conv.ID] {
		t.Fatalf("bob's studio list leaked alice's conversation: %v", ids)
	}
	if ids := listIDs(alice); !ids[conv.ID] {
		t.Fatalf("alice's studio list is missing her conversation: %v", ids)
	}
	cancel := h.doAs(alice, http.MethodPost, "/api/apps/"+a.ID+"/sessions/"+conv.ID+"/cancel", "")
	defer cancel.Body.Close()
	if cancel.StatusCode != http.StatusOK {
		t.Fatalf("alice cancel: %d, want 200", cancel.StatusCode)
	}
}

func TestStudioWebConversationUsesNativeSurfaceAndPinsModel(t *testing.T) {
	h, _ := newStudioHarness(t, []llm.Response{textTurn("ok")})
	addStudioTestModel(t, "glm-5.3")
	a := createStudioAgent(t, h, "Guarded Bot", "GUARDED persona.", "ready")
	sid := startStudioConversation(t, h, a.ID)

	// The native Web conversation surface keeps working.
	for _, check := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/sessions/" + sid, ""},
		{http.MethodGet, "/api/sessions/" + sid + "/stream", ""},
		{http.MethodPost, "/api/sessions/" + sid + "/messages", `{"prompt":"hi"}`},
	} {
		want := http.StatusAccepted
		if check.method == http.MethodGet {
			want = http.StatusOK
		}
		if code := studioStatus(t, h, check.method, check.path, check.body); code != want {
			t.Fatalf("allowed %s %s: %d, want %d", check.method, check.path, code, want)
		}
	}
	// The message above started a run, and SetModel refuses during one (409).
	// Let it finish so the switches below probe the allowlist, not the run.
	waitRunDone(t, h.session(sid))

	// Model identity is part of the bound Agent snapshot: the allowlist of a
	// legacy definition is exactly its default, so a switch to any other
	// model is refused while the default itself may be re-applied.
	if code := studioStatus(t, h, http.MethodPost, "/api/sessions/"+sid+"/control", `{"action":"set_model","model":"glm-5.3"}`); code != http.StatusBadRequest {
		t.Fatalf("out-of-allowlist switch: %d, want 400", code)
	}
	if code := studioStatus(t, h, http.MethodPost, "/api/sessions/"+sid+"/control", `{"action":"set_model","model":"k3"}`); code != http.StatusOK {
		t.Fatalf("in-allowlist switch: %d, want 200", code)
	}
}

func TestStudioDraftOrDeletedAgentCannotResumeRuns(t *testing.T) {
	h, _ := newStudioHarness(t, []llm.Response{textTurn("first ok"), textTurn("second ok")})
	a := createStudioAgent(t, h, "Runner Bot", "RUNNER persona.", "ready")
	sid := startStudioConversation(t, h, a.ID)
	studioAsk(t, h, sid, "first question")

	// A disabled definition stops existing conversations too, not just new ones.
	updateStudioAgent(t, h, a.ID, 1, "Runner Bot", "RUNNER persona.", "draft")
	if code := studioStatus(t, h, http.MethodPost, "/api/sessions/"+sid+"/messages", `{"prompt":"while draft"}`); code < 400 {
		t.Fatalf("draft agent resumed a run: %d, want a refusal", code)
	}

	// Re-enabling unblocks it: the refusal was about availability.
	updateStudioAgent(t, h, a.ID, 2, "Runner Bot", "RUNNER persona.", "ready")
	studioAsk(t, h, sid, "second question")

	deleteStudioAgent(t, h, a.ID)
	if code := studioStatus(t, h, http.MethodPost, "/api/sessions/"+sid+"/messages", `{"prompt":"after delete"}`); code < 400 {
		t.Fatalf("deleted agent resumed a run: %d, want a refusal", code)
	}
	if code := studioStatus(t, h, http.MethodPost, "/api/apps/"+a.ID+"/sessions", ""); code != http.StatusNotFound {
		t.Fatalf("deleted agent conversation: %d, want 404", code)
	}
}

func listStudioTemplates(t *testing.T, h *harness) []studioTemplate {
	t.Helper()
	resp := h.do(http.MethodGet, "/api/manage/templates", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("list templates: %d (%s)", resp.StatusCode, msg)
	}
	var out struct {
		Templates []studioTemplate `json:"templates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Templates
}

// Templates are workspace furniture in their own file: seeded on first boot,
// then grown and pruned through the API, and never touching the agent
// registry's schema.
func TestStudioTemplateLifecycle(t *testing.T) {
	dir := t.TempDir()
	withDir := func(c *Config) { c.SessionDir = dir }
	h, _ := newStudioHarness(t, nil, withDir)

	seed := listStudioTemplates(t, h)
	if len(seed) != 5 {
		t.Fatalf("seeded templates = %d, want 5", len(seed))
	}

	resp := h.do(http.MethodPost, "/api/manage/templates", studioBody(map[string]any{
		"name":         "评审员",
		"description":  "按清单评审。",
		"instructions": "你是评审员。",
		"icon":         "chart",
		"color":        "#2fa37c",
		"category":     "code",
		"starters":     []string{"帮我评审这份方案"},
	}))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("create template: %d (%s)", resp.StatusCode, msg)
	}
	var created studioTemplate
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.ID, "tpl_") || created.Name != "评审员" || len(created.Starters) != 1 {
		t.Fatalf("created template = %+v", created)
	}

	// The templates file carries state across processes, and the agent
	// registry keeps loading unchanged beside it.
	h.mgr.Close()
	h2, _ := newStudioHarness(t, nil, withDir)
	if reloaded := listStudioTemplates(t, h2); len(reloaded) != 6 {
		t.Fatalf("templates after create on second process = %d, want 5", len(reloaded))
	}

	if code := studioStatus(t, h2, http.MethodDelete, "/api/manage/templates/"+created.ID, ""); code != http.StatusNoContent {
		t.Fatalf("delete template: %d, want 204", code)
	}
	if code := studioStatus(t, h2, http.MethodDelete, "/api/manage/templates/"+created.ID, ""); code != http.StatusNotFound {
		t.Fatalf("double delete: %d, want 404", code)
	}
	if left := listStudioTemplates(t, h2); len(left) != 5 {
		t.Fatalf("templates after delete = %d, want 4", len(left))
	}

	// Validation mirrors the agent form's limits; a template without a name
	// never reaches the file.
	if code := studioStatus(t, h2, http.MethodPost, "/api/manage/templates", studioBody(map[string]any{"name": "  "})); code != http.StatusBadRequest {
		t.Fatalf("blank template name: %d, want 400", code)
	}
	if still := listStudioTemplates(t, h2); len(still) != 5 {
		t.Fatalf("templates after rejected create = %d, want 4", len(still))
	}
}
