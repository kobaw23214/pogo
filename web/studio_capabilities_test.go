package web

// Per-Agent capability binding tests: the admin catalog, definition
// validation and lossless round-trip, the pinned session contract, model
// switching inside the allowlist, and the actual runtime difference between
// two capability selections (prompt, registry, read endpoints) — including
// the fail-closed cases when a selected capability disappears.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/mcpclient"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

// writeManifestSkill adds one declared tool to a skill written by
// writeTestSkill, so a selection difference is visible in the model's
// schemas, not only in the prompt prose.
func writeManifestSkill(t *testing.T, dir, name, description, toolName string) skills.Skill {
	t.Helper()
	sk := writeTestSkill(t, dir, name, description, "# "+name)
	toolsJSON := fmt.Sprintf(`{"tools":[{"name":%q,"title":"Echo","description":"%s declared tool",`+
		`"inputSchema":{"type":"object","properties":{},"additionalProperties":false},`+
		`"annotations":{"readOnlyHint":true},"x-pogo-exec":{"argv":["echo","hi"]}}]}`, toolName, name)
	if err := os.WriteFile(filepath.Join(sk.Dir, "tools.json"), []byte(toolsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return sk
}

func studioAgentCapsBody(name, status string, extra map[string]any) map[string]any {
	body := studioAgentBody(name, name+" persona.", status)
	for k, v := range extra {
		body[k] = v
	}
	return body
}

func postStudioAgent(t *testing.T, h *harness, body map[string]any, want int) studioAgent {
	t.Helper()
	resp := h.do(http.MethodPost, "/api/manage/agents", studioBody(body))
	defer resp.Body.Close()
	if resp.StatusCode != want {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("create agent: %d, want %d (%s)", resp.StatusCode, want, msg)
	}
	var a studioAgent
	if want == http.StatusCreated {
		if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

func getStudioAppRaw(t *testing.T, h *harness, id string) map[string]any {
	t.Helper()
	resp := h.do(http.MethodGet, "/api/apps/"+id, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET app %s: %d", id, resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func sessionCapabilities(t *testing.T, h *harness, aid, sid string) map[string]any {
	t.Helper()
	resp := h.do(http.MethodGet, "/api/apps/"+aid+"/sessions/"+sid+"/capabilities", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("session capabilities: %d (%s)", resp.StatusCode, msg)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func stringField(t *testing.T, m map[string]any, key string) []string {
	t.Helper()
	raw, ok := m[key]
	if !ok {
		t.Fatalf("response has no %q field: %v", key, m)
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("%q is not an array: %v", key, raw)
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("%q holds a non-string: %v", key, v)
		}
		out = append(out, s)
	}
	return out
}

// --- admin catalog -----------------------------------------------------------

func TestStudioCapabilitiesCatalogRejectsUnreadableMCP(t *testing.T) {
	store := filepath.Join(t.TempDir(), "mcp.json")
	if err := mcpclient.Save(store, mcpclient.Config{}); err != nil {
		t.Fatal(err)
	}
	h, _ := newStudioHarness(t, []llm.Response{textTurn("ok")}, func(c *Config) {
		c.MCPClientStore = store
	})
	if err := os.WriteFile(store, []byte(`{"servers":`), 0o600); err != nil {
		t.Fatal(err)
	}
	resp := h.do(http.MethodGet, "/api/manage/capabilities", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("broken catalog: %d, want 503", resp.StatusCode)
	}
	if err := mcpclient.Save(store, mcpclient.Config{}); err != nil {
		t.Fatal(err)
	}
	resp = h.do(http.MethodGet, "/api/manage/capabilities", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("recovered catalog: %d, want 200", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if got := stringField(t, out, "mcp_servers"); len(got) != 0 {
		t.Fatalf("recovered empty catalog: %v", got)
	}
}

func TestStudioCapabilitiesCatalogServesSkillsAndMCPServers(t *testing.T) {
	dir := t.TempDir()
	skA := writeManifestSkill(t, dir, "alpha", "the alpha capability", "alpha.echo")
	skB := writeTestSkill(t, dir, "beta", "the beta capability", "# Beta")
	// The embedded built-ins ride the same catalog with their own marker.
	skC := writeTestSkill(t, dir, "web-drawing", "draw diagrams", "# Draw")
	skC.Source = "embedded"
	store := filepath.Join(t.TempDir(), "mcp.json")
	err := mcpclient.Save(store, mcpclient.Config{Servers: map[string]*mcpclient.ServerConfig{
		"remote":   {URL: "http://127.0.0.1:1/unreachable", StartupTimeoutMs: 1000},
		"disabled": {URL: "http://127.0.0.1:1/off", Enabled: boolPtr(false)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	h, _ := newStudioHarness(t, []llm.Response{textTurn("ok")}, func(c *Config) {
		c.Skills = []skills.Skill{skA, skB, skC}
		c.MCPClientStore = store
	})

	resp := h.do(http.MethodGet, "/api/manage/capabilities", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET capabilities: %d", resp.StatusCode)
	}
	var out struct {
		Skills []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Hidden      bool   `json:"hidden"`
			ToolCount   int    `json:"tool_count"`
			Builtin     bool   `json:"builtin"`
			BrowserTask bool   `json:"browser_task"`
		} `json:"skills"`
		BuiltinTools []struct {
			Name      string `json:"name"`
			ReadOnly  bool   `json:"read_only"`
			Protected bool   `json:"protected"`
		} `json:"builtin_tools"`
		MCPServers []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Transport string `json:"transport"`
			Enabled   bool   `json:"enabled"`
			Status    string `json:"status"`
			ToolCount int    `json:"tool_count"`
		} `json:"mcp_servers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Skills == nil || out.MCPServers == nil {
		t.Fatalf("catalog arrays must not be null: %+v", out)
	}
	if len(out.Skills) != 3 {
		t.Fatalf("catalog skills = %+v, want alpha, beta and web-drawing", out.Skills)
	}
	byName := map[string]int{}
	builtinByName := map[string]bool{}
	for _, sk := range out.Skills {
		byName[sk.Name] = sk.ToolCount
		builtinByName[sk.Name] = sk.Builtin
		wantID := skillIDOf(map[bool]skills.Skill{true: skA, false: skB}[sk.Name == "alpha"])
		if sk.Name == "web-drawing" {
			wantID = skillIDOf(skC)
		}
		if sk.ID != wantID {
			t.Fatalf("skill %s id = %q, want the deterministic %q", sk.Name, sk.ID, wantID)
		}
		if sk.Hidden {
			t.Fatalf("skill %s wrongly hidden", sk.Name)
		}
	}
	if !builtinByName["web-drawing"] || builtinByName["alpha"] || builtinByName["beta"] {
		t.Fatalf("builtin marker = %v, want only web-drawing", builtinByName)
	}
	if byName["alpha"] != 1 || byName["beta"] != 0 {
		t.Fatalf("tool counts = %v, want alpha:1 beta:0", byName)
	}
	if len(out.MCPServers) != 2 {
		t.Fatalf("catalog mcp servers = %+v, want remote and disabled", out.MCPServers)
	}
	servers := map[string]struct {
		transport string
		enabled   bool
		status    string
	}{}
	for _, srv := range out.MCPServers {
		if srv.ID != srv.Name {
			t.Fatalf("mcp id %q != name %q", srv.ID, srv.Name)
		}
		servers[srv.Name] = struct {
			transport string
			enabled   bool
			status    string
		}{srv.Transport, srv.Enabled, srv.Status}
	}
	if srv := servers["remote"]; !srv.enabled || srv.transport != "streamable" || srv.status == "disabled" {
		t.Fatalf("remote entry = %+v, want an enabled streamable server allowed to be down", srv)
	}
	if srv := servers["disabled"]; srv.enabled || srv.status != "disabled" {
		t.Fatalf("disabled entry = %+v, want enabled:false status:disabled", srv)
	}

	// The native half of the catalog rides the same endpoint: every name the
	// tools package knows, with the protected core marked.
	if len(out.BuiltinTools) == 0 {
		t.Fatalf("builtin_tools missing from capabilities: %+v", out)
	}
	prot := map[string]bool{}
	for _, bt := range out.BuiltinTools {
		prot[bt.Name] = bt.Protected
	}
	for _, name := range []string{"read", "ls", "find", "grep", "todo", "subagent", "analyze_image"} {
		if !prot[name] {
			t.Fatalf("builtin %s missing or unmarked protected: %v", name, prot)
		}
	}
	for _, name := range []string{"write", "edit", "bash", "browser"} {
		if v, ok := prot[name]; !ok || v {
			t.Fatalf("builtin %s must be present and selectable: %v", name, prot)
		}
	}

	// An empty deployment answers empty arrays, never null.
	h2, _ := newStudioHarness(t, []llm.Response{textTurn("ok")})
	resp2 := h2.do(http.MethodGet, "/api/manage/capabilities", "")
	defer resp2.Body.Close()
	var empty struct {
		Skills     []any `json:"skills"`
		MCPServers []any `json:"mcp_servers"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&empty); err != nil {
		t.Fatal(err)
	}
	if empty.Skills == nil || empty.MCPServers == nil {
		t.Fatalf("empty deployment must answer empty arrays: %+v", empty)
	}
}

// --- definition validation and round-trip ------------------------------------

func TestStudioDefinitionSelectionRoundTripsAndValidates(t *testing.T) {
	dir := t.TempDir()
	skA := writeTestSkill(t, dir, "alpha", "the alpha capability", "# Alpha")
	store := filepath.Join(t.TempDir(), "mcp.json")
	err := mcpclient.Save(store, mcpclient.Config{Servers: map[string]*mcpclient.ServerConfig{
		"remote":   {URL: "http://127.0.0.1:1/unreachable", StartupTimeoutMs: 1000},
		"disabled": {URL: "http://127.0.0.1:1/off", Enabled: boolPtr(false)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	h, _ := newStudioHarness(t, []llm.Response{textTurn("ok")}, func(c *Config) {
		c.Skills = []skills.Skill{skA}
		c.MCPClientStore = store
	})
	addStudioTestModel(t, "glm-5.3")
	idA := skillIDOf(skA)

	// Full selection round-trips losslessly, models normalized to canonical IDs.
	a := postStudioAgent(t, h, studioAgentCapsBody("Selected Bot", "ready", map[string]any{
		"models":      []string{"k3", "glm-5.3", "glm-5.3"},
		"skills":      []string{idA},
		"mcp_servers": []string{"remote"},
		"tools":       []string{"read", "bash", "bash"},
	}), http.StatusCreated)
	raw := getStudioAppRaw(t, h, a.ID)
	if got := stringField(t, raw, "models"); !slices.Equal(got, []string{"k3", "glm-5.3"}) {
		t.Fatalf("models = %v, want deduped [k3 glm-5.3]", got)
	}
	if got := stringField(t, raw, "skills"); !slices.Equal(got, []string{idA}) {
		t.Fatalf("skills = %v, want [%s]", got, idA)
	}
	if got := stringField(t, raw, "mcp_servers"); !slices.Equal(got, []string{"remote"}) {
		t.Fatalf("mcp_servers = %v, want [remote]", got)
	}
	if got := stringField(t, raw, "tools"); !slices.Equal(got, []string{"read", "bash"}) {
		t.Fatalf("tools = %v, want deduped [read bash]", got)
	}

	// Omitted selectors on a new definition use the current linked-selection
	// defaults; unrelated model and MCP fields retain their omission semantics.
	defaulted := postStudioAgent(t, h, studioAgentCapsBody("Defaulted Bot", "ready", nil), http.StatusCreated)
	rawDefaulted := getStudioAppRaw(t, h, defaulted.ID)
	for _, key := range []string{"models", "mcp_servers"} {
		if _, present := rawDefaulted[key]; present {
			t.Fatalf("defaulted definition grew a %s field: %v", key, rawDefaulted)
		}
	}
	if got := stringField(t, rawDefaulted, "skills"); len(got) != 0 {
		t.Fatalf("defaulted skills = %v, want []", got)
	}
	if got := stringField(t, rawDefaulted, "tools"); len(got) == 0 {
		t.Fatal("defaulted native tools must be materialized")
	}
	if got := stringField(t, rawDefaulted, "skill_tool_bindings"); !slices.Equal(got, tools.SkillBindingNames()) {
		t.Fatalf("defaulted bindings = %v, want %v", got, tools.SkillBindingNames())
	}

	// Explicit empty survives the round trip as an empty array, not a missing key.
	empty := postStudioAgent(t, h, studioAgentCapsBody("Empty Bot", "ready", map[string]any{
		"skills": []string{}, "mcp_servers": []string{},
	}), http.StatusCreated)
	rawEmpty := getStudioAppRaw(t, h, empty.ID)
	if got := stringField(t, rawEmpty, "skills"); len(got) != 0 {
		t.Fatalf("explicit empty skills = %v, want []", got)
	}
	if got := stringField(t, rawEmpty, "mcp_servers"); len(got) != 0 {
		t.Fatalf("explicit empty mcp_servers = %v, want []", got)
	}

	// Every invalid reference is a clear 400.
	for name, extra := range map[string]map[string]any{
		"unknown model":        {"models": []string{"k3", "no-such-model"}},
		"default not allowed":  {"models": []string{"glm-5.3"}},
		"empty model list":     {"models": []string{}},
		"unknown skill":        {"skills": []string{"0123456789abcdef0123456789abcdef"}},
		"unknown mcp server":   {"mcp_servers": []string{"ghost"}},
		"disabled mcp server":  {"mcp_servers": []string{"disabled"}},
		"blank skill id":       {"skills": []string{" "}},
		"blank mcp server ids": {"mcp_servers": make([]string, 65)},
		"unknown builtin tool": {"tools": []string{"nope"}},
		"blank builtin tool":   {"tools": []string{" "}},
		"blank builtin ids":    {"tools": make([]string, 17)},
	} {
		resp := h.do(http.MethodPost, "/api/manage/agents", studioBody(studioAgentCapsBody("Bad "+name, "ready", extra)))
		msg, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: %d, want 400 (%s)", name, resp.StatusCode, msg)
		}
		if len(msg) == 0 {
			t.Fatalf("%s: rejection must carry a reason", name)
		}
	}
}

// --- the pinned session contract ---------------------------------------------

func TestStudioSessionCapabilitiesReportPinnedSelection(t *testing.T) {
	dir := t.TempDir()
	skA := writeTestSkill(t, dir, "alpha", "the alpha capability", "# Alpha")
	h, _ := newStudioHarness(t, []llm.Response{textTurn("ok")}, func(c *Config) { c.Skills = []skills.Skill{skA} })
	addStudioTestModel(t, "glm-5.3")
	idA := skillIDOf(skA)

	a := postStudioAgent(t, h, studioAgentCapsBody("Pinned Bot", "ready", map[string]any{
		"models": []string{"k3", "glm-5.3"}, "skills": []string{idA},
	}), http.StatusCreated)
	sid := startStudioConversation(t, h, a.ID)

	caps := sessionCapabilities(t, h, a.ID, sid)
	if got := stringField(t, caps, "models"); !slices.Equal(got, []string{"k3", "glm-5.3"}) {
		t.Fatalf("pinned models = %v", got)
	}
	if got := caps["model"]; got != "k3" {
		t.Fatalf("current model = %v, want the default k3", got)
	}
	if got := stringField(t, caps, "skills"); !slices.Equal(got, []string{idA}) {
		t.Fatalf("pinned skills = %v", got)
	}
	if got := stringField(t, caps, "mcp_servers"); len(got) != 0 {
		t.Fatalf("pinned mcp_servers = %v, want [] without an MCP store", got)
	}

	// A switch inside the allowlist moves the reported current model.
	h.post("/api/sessions/"+sid+"/control", `{"action":"set_model","model":"glm-5.3"}`, http.StatusOK)
	if got := sessionCapabilities(t, h, a.ID, sid)["model"]; got != "glm-5.3" {
		t.Fatalf("current model after switch = %v, want glm-5.3", got)
	}

	// Wrong agent id or unknown session answers 404, not another binding's data.
	for _, path := range []string{
		"/api/apps/" + legacyIDForTest(t, h) + "/sessions/" + sid + "/capabilities",
		"/api/apps/" + a.ID + "/sessions/" + sid + "ff/capabilities",
	} {
		resp := h.do(http.MethodGet, path, "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s: %d, want 404", path, resp.StatusCode)
		}
	}

	// A legacy definition reports its single pin and the live global catalog.
	legacy := postStudioAgent(t, h, studioAgentCapsBody("Legacy Caps Bot", "ready", nil), http.StatusCreated)
	legacy = makeStudioAgentLegacy(t, h, legacy)
	legacySid := startStudioConversation(t, h, legacy.ID)
	legacyCaps := sessionCapabilities(t, h, legacy.ID, legacySid)
	if got := stringField(t, legacyCaps, "models"); !slices.Equal(got, []string{"k3"}) {
		t.Fatalf("legacy models = %v, want [k3]", got)
	}
	if got := stringField(t, legacyCaps, "skills"); !slices.Equal(got, []string{idA}) {
		t.Fatalf("legacy skills = %v, want the global [%s]", got, idA)
	}
}

func makeStudioAgentLegacy(t *testing.T, h *harness, a studioAgent) studioAgent {
	t.Helper()
	h.mgr.studio.mu.Lock()
	defer h.mgr.studio.mu.Unlock()
	legacy := h.mgr.studio.data.Agents[a.ID]
	legacy.Tools, legacy.Skills, legacy.SkillToolBindings = nil, nil, nil
	h.mgr.studio.data.Agents[a.ID] = legacy
	if err := h.mgr.studio.save(h.mgr.studio.data); err != nil {
		t.Fatal(err)
	}
	return legacy
}

func legacyIDForTest(t *testing.T, h *harness) string {
	t.Helper()
	a := postStudioAgent(t, h, studioAgentCapsBody("Other Bot", "ready", nil), http.StatusCreated)
	return a.ID
}

// --- capability A vs B: the runtime actually differs --------------------------

func TestStudioSkillSelectionChangesPromptToolsAndReadEndpoints(t *testing.T) {
	dir := t.TempDir()
	skA := writeManifestSkill(t, dir, "alpha", "ALPHA-CAPABILITY-2f91 instructions", "alpha.echo")
	skB := writeTestSkill(t, dir, "beta", "BETA-CAPABILITY-7c04 instructions", "# Beta")
	// Conversations: A (alpha), B (beta), empty, legacy; each asks once; A
	// also expands /skill:alpha.
	turns := []llm.Response{}
	for i := 0; i < 6; i++ {
		turns = append(turns, textTurn("ok"))
	}
	h, cap := newStudioHarness(t, turns, func(c *Config) { c.Skills = []skills.Skill{skA, skB} })
	idA, idB := skillIDOf(skA), skillIDOf(skB)

	agentA := postStudioAgent(t, h, studioAgentCapsBody("Alpha Bot", "ready", map[string]any{"skills": []string{idA}}), http.StatusCreated)
	agentB := postStudioAgent(t, h, studioAgentCapsBody("Beta Bot", "ready", map[string]any{"skills": []string{idB}}), http.StatusCreated)
	agentNone := postStudioAgent(t, h, studioAgentCapsBody("None Bot", "ready", map[string]any{"skills": []string{}}), http.StatusCreated)
	agentLegacy := postStudioAgent(t, h, studioAgentCapsBody("Inherit Bot", "ready", nil), http.StatusCreated)
	agentLegacy = makeStudioAgentLegacy(t, h, agentLegacy)

	sidA := startStudioConversation(t, h, agentA.ID)
	sidB := startStudioConversation(t, h, agentB.ID)
	sidNone := startStudioConversation(t, h, agentNone.ID)
	sidLegacy := startStudioConversation(t, h, agentLegacy.ID)
	for _, sid := range []string{sidA, sidB, sidNone, sidLegacy} {
		studioAsk(t, h, sid, "show your setup")
	}

	systems, _, _, _ := cap.snapshot()
	schemas := cap.toolSchemas()
	if len(systems) != 4 || len(schemas) != 4 {
		t.Fatalf("model calls = %d, want one per conversation", len(systems))
	}
	has := func(s, sub string) bool { return strings.Contains(s, sub) }
	if !has(systems[0], "ALPHA-CAPABILITY-2f91") || has(systems[0], "BETA-CAPABILITY-7c04") {
		t.Fatalf("A prompt must hold alpha only:\n%s", systems[0])
	}
	if !slices.Contains(schemas[0], "alpha_echo") {
		t.Fatalf("A schemas = %v, want alpha_echo", schemas[0])
	}
	if !has(systems[1], "BETA-CAPABILITY-7c04") || has(systems[1], "ALPHA-CAPABILITY-2f91") {
		t.Fatalf("B prompt must hold beta only:\n%s", systems[1])
	}
	if slices.Contains(schemas[1], "alpha_echo") {
		t.Fatalf("B schemas gained alpha's declared tool: %v", schemas[1])
	}
	if has(systems[2], "<available_skills>") || slices.Contains(schemas[2], "alpha_echo") {
		t.Fatalf("explicit-empty prompt/schemas gained skills:\n%s\n%v", systems[2], schemas[2])
	}
	// Legacy keeps the prompt/read inheritance — every skill section, the
	// /skill: expansions, the read endpoints — but no longer registers
	// manifest tools: schema tokens are paid only by explicit selections.
	// The catalog view below still shows the declared tools (the registered
	// half and the catalog half are deliberately different answers).
	if !has(systems[3], "ALPHA-CAPABILITY-2f91") || !has(systems[3], "BETA-CAPABILITY-7c04") || slices.Contains(schemas[3], "alpha_echo") {
		t.Fatalf("legacy prompt lost the global inheritance or schemas gained its tools:\n%s\n%v", systems[3], schemas[3])
	}

	// /skill:name expands inside the selection and refuses outside it.
	studioAsk(t, h, sidA, "/skill:alpha")
	systems, _, histories, _ := cap.snapshot()
	last := histories[len(histories)-1]
	if got := studioMessageText(last[len(last)-1]); !has(got, "# alpha") {
		t.Fatalf("/skill:alpha expansion missing its body: %q", got)
	}
	if code := studioStatus(t, h, http.MethodPost, "/api/sessions/"+sidA+"/messages", `{"prompt":"/skill:beta"}`); code != http.StatusBadRequest {
		t.Fatalf("/skill:beta outside the selection: %d, want 400", code)
	}
	// The legacy conversation still expands any global skill.
	studioAsk(t, h, sidLegacy, "/skill:beta")

	// The read endpoints follow the same boundary.
	decodeSkills := func(path string) []string {
		resp := h.do(http.MethodGet, path, "")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %d", path, resp.StatusCode)
		}
		var out struct {
			Skills []struct {
				Name string `json:"name"`
			} `json:"skills"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		names := []string{}
		for _, sk := range out.Skills {
			names = append(names, sk.Name)
		}
		return names
	}
	if got := decodeSkills("/api/skills?session_id=" + sidA); !slices.Equal(got, []string{"alpha"}) {
		t.Fatalf("A session skills = %v, want [alpha]", got)
	}
	if got := decodeSkills("/api/skills?session_id=" + sidNone); len(got) != 0 {
		t.Fatalf("explicit-empty session skills = %v, want []", got)
	}
	if got := decodeSkills("/api/skills?session_id=" + sidLegacy); !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Fatalf("legacy session skills = %v, want the global set", got)
	}
	if code := studioStatus(t, h, http.MethodGet, "/api/skills?session_id="+sidA+"ff", ""); code != http.StatusNotFound {
		t.Fatalf("unknown session_id: %d, want 404", code)
	}

	contentCode := func(name, sid string) int {
		return studioStatus(t, h, http.MethodGet, "/api/skills/content?name="+url.QueryEscape(name)+"&session_id="+sid, "")
	}
	if code := contentCode("alpha", sidA); code != http.StatusOK {
		t.Fatalf("content inside the selection: %d, want 200", code)
	}
	if code := contentCode("beta", sidA); code != http.StatusNotFound {
		t.Fatalf("content outside the selection: %d, want 404", code)
	}

	toolsFor := func(sid string) []string {
		resp := h.do(http.MethodGet, "/api/tools?session_id="+sid, "")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /api/tools?session_id=%s: %d", sid, resp.StatusCode)
		}
		var out struct {
			Tools []tools.SynthInfo `json:"tools"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		names := []string{}
		for _, ti := range out.Tools {
			names = append(names, ti.WireName)
		}
		return names
	}
	if got := toolsFor(sidA); !slices.Equal(got, []string{"alpha_echo"}) {
		t.Fatalf("A session tools = %v, want [alpha_echo]", got)
	}
	if got := toolsFor(sidNone); len(got) != 0 {
		t.Fatalf("explicit-empty session tools = %v, want []", got)
	}
}

func TestStudioSkillSelectionFailsClosedWhenTheSkillDisappears(t *testing.T) {
	dir := t.TempDir()
	skA := writeManifestSkill(t, dir, "alpha", "the alpha capability", "alpha.echo")
	skB := writeTestSkill(t, dir, "beta", "the beta capability", "# Beta")
	// The embedded built-ins ride the same catalog with their own marker.
	skC := writeTestSkill(t, dir, "web-drawing", "draw diagrams", "# Draw")
	skC.Source = "embedded"
	h, cap := newStudioHarness(t, []llm.Response{textTurn("ok")}, func(c *Config) { c.Skills = []skills.Skill{skA, skB} })
	idA := skillIDOf(skA)

	a := postStudioAgent(t, h, studioAgentCapsBody("Fragile Bot", "ready", map[string]any{"skills": []string{idA}}), http.StatusCreated)

	// The skill vanishes after the definition was saved (its upload removed):
	// the next conversation gets nothing rather than everything.
	h.mgr.SetSkills([]skills.Skill{skB})
	sid := startStudioConversation(t, h, a.ID)
	studioAsk(t, h, sid, "show your setup")

	systems, _, _, _ := cap.snapshot()
	schemas := cap.toolSchemas()
	if len(systems) != 1 || strings.Contains(systems[0], "<available_skills>") {
		t.Fatalf("deleted skill still in the prompt or widened to global:\n%s", systems[0])
	}
	if slices.Contains(schemas[0], "alpha_echo") {
		t.Fatalf("deleted skill's declared tool survived: %v", schemas[0])
	}

	// The pinned definition still names the ID (it is the contract); the
	// session-bound reads resolve it to nothing.
	caps := sessionCapabilities(t, h, a.ID, sid)
	if got := stringField(t, caps, "skills"); !slices.Equal(got, []string{idA}) {
		t.Fatalf("pinned skills = %v, want the preserved [%s]", got, idA)
	}
	resp := h.do(http.MethodGet, "/api/skills?session_id="+sid, "")
	defer resp.Body.Close()
	var out struct {
		Skills []any `json:"skills"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Skills) != 0 {
		t.Fatalf("deleted skill resolved again: %v", out.Skills)
	}
}

// --- native tool selection ------------------------------------------------------

func TestStudioToolSelectionFiltersSessionRegistry(t *testing.T) {
	dir := t.TempDir()
	skBT := writeTestSkill(t, dir, "browse-web", "drive the browser", "# Browse")
	skBT.BrowserTask = true
	turns := []llm.Response{}
	for i := 0; i < 5; i++ {
		turns = append(turns, textTurn("ok"))
	}
	h, cap := newStudioHarness(t, turns, func(c *Config) {
		c.Skills = append(bindingTestSkills(t), skBT)
		c.BrowserURL = "http://127.0.0.1:9"
		c.BrowserToken = "test-only"
	})
	idBT := skillIDOf(skBT)
	browserSkillID := bindingSkillID(t, h.mgr.Skills(), "browser-use")

	// An analyst: the write verbs stay, bash and the browser go.
	analyst := postStudioAgent(t, h, studioAgentCapsBody("Analyst", "ready", map[string]any{
		"skills":              []string{idBT},
		"tools":               []string{"read", "ls", "find", "grep", "todo", "subagent", "write", "edit"},
		"skill_tool_bindings": []string{"browser-use"},
	}), http.StatusCreated)
	legacy := postStudioAgent(t, h, studioAgentCapsBody("Legacy Tools Bot", "ready", map[string]any{
		"skills": []string{idBT},
	}), http.StatusCreated)
	legacy = makeStudioAgentLegacy(t, h, legacy)

	sidAnalyst := startStudioConversation(t, h, analyst.ID)
	sidLegacy := startStudioConversation(t, h, legacy.ID)
	studioAsk(t, h, sidAnalyst, "show your setup")
	studioAsk(t, h, sidLegacy, "show your setup")

	schemas := cap.toolSchemas()
	if len(schemas) != 2 {
		t.Fatalf("model calls = %d, want one per conversation", len(schemas))
	}
	for _, name := range []string{"browser", "bash"} {
		if slices.Contains(schemas[0], name) {
			t.Fatalf("analyst schemas gained %s: %v", name, schemas[0])
		}
		if !slices.Contains(schemas[1], name) {
			t.Fatalf("legacy (nil tools) schemas lost %s: %v", name, schemas[1])
		}
	}
	// The protected core survives any selection; the kept verbs stay.
	for _, name := range []string{"read", "ls", "find", "grep", "todo", "subagent", "write", "edit"} {
		if !slices.Contains(schemas[0], name) {
			t.Fatalf("analyst schemas lost %s: %v", name, schemas[0])
		}
	}

	// The browser gate follows the tool: without it the /skill: arming path is
	// closed, with it (legacy inheritance) the path stays open.
	if h.session(sidAnalyst).browserToolEnabled() {
		t.Fatalf("analyst session reports the browser tool enabled")
	}
	if !h.session(sidLegacy).browserToolEnabled() {
		t.Fatalf("legacy session lost the browser tool")
	}
	studioAsk(t, h, sidAnalyst, "/skill:browse-web look at the page")
	schemas = cap.toolSchemas()
	if slices.Contains(schemas[len(schemas)-1], "browser") {
		t.Fatalf("browser-task invocation registered the withheld tool: %v", schemas[len(schemas)-1])
	}

	// The session endpoint reports actual registered built-ins, including the
	// protected core; legacy inheritance keeps optional capabilities available.
	caps := sessionCapabilities(t, h, analyst.ID, sidAnalyst)
	if got := stringField(t, caps, "tools"); !slices.Contains(got, "write") || !slices.Contains(got, tools.ShortcutQueryName) || slices.Contains(got, "bash") || slices.Contains(got, "browser") {
		t.Fatalf("analyst effective tools = %v", got)
	}
	legacyCaps := sessionCapabilities(t, h, legacy.ID, sidLegacy)
	if got := stringField(t, legacyCaps, "tools"); !slices.Contains(got, "bash") || !slices.Contains(got, "browser") || !slices.Contains(got, tools.ShortcutManageName) {
		t.Fatalf("legacy optional tools unavailable = %v", got)
	}

	// The original definition snapshot stays intact; a separate tool policy
	// updates this idle conversation as well as newly created conversations.
	body := studioAgentCapsBody("Analyst", "ready", map[string]any{
		"skills":              []string{idBT, browserSkillID},
		"tools":               []string{"read", "ls", "find", "grep", "todo", "subagent", "write", "edit", "bash", "browser"},
		"skill_tool_bindings": []string{"browser-use"},
	})
	body["revision"] = analyst.Revision
	resp := h.do(http.MethodPatch, "/api/manage/agents/"+analyst.ID, studioBody(body))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH analyst: %d", resp.StatusCode)
	}
	b, ok := h.mgr.studio.binding(sidAnalyst)
	if !ok || b.Agent.Tools == nil || slices.Contains(*b.Agent.Tools, "browser") {
		t.Fatalf("pinned binding moved with the edit: %+v", b.Agent.Tools)
	}
	if b.ToolPolicy == nil || !toolSelected(b.effectiveTools(), "browser") || !h.session(sidAnalyst).browserToolEnabled() {
		t.Fatal("idle existing conversation did not adopt native tool policy")
	}
	studioAsk(t, h, sidAnalyst, "show your updated tools")
	schemas = cap.toolSchemas()
	if got := schemas[len(schemas)-1]; !slices.Contains(got, "browser") || !slices.Contains(got, "bash") {
		t.Fatalf("existing chat registry stale: %v", got)
	}
	sidNew := startStudioConversation(t, h, analyst.ID)
	studioAsk(t, h, sidNew, "show your setup")
	schemas = cap.toolSchemas()
	if got := schemas[len(schemas)-1]; !slices.Contains(got, "browser") || !slices.Contains(got, "bash") {
		t.Fatalf("new conversation of the edited Agent lacks browser/bash: %v", got)
	}
}

func TestToolsEndpointServesBuiltinCatalog(t *testing.T) {
	h, _ := newStudioHarness(t, []llm.Response{textTurn("ok")})
	resp := h.do(http.MethodGet, "/api/tools", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/tools: %d", resp.StatusCode)
	}
	var out struct {
		Builtin []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			ReadOnly    bool   `json:"read_only"`
			Protected   bool   `json:"protected"`
		} `json:"builtin"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Builtin) != len(tools.BuiltinCatalog()) {
		t.Fatalf("builtin = %v, want the %d-name catalog", out.Builtin, len(tools.BuiltinCatalog()))
	}
	for i, entry := range out.Builtin {
		want := tools.BuiltinCatalog()[i]
		if entry.Name != want.Name || entry.Protected != want.Protected || entry.ReadOnly != want.ReadOnly || entry.Description == "" {
			t.Fatalf("builtin[%d] = %+v, want %+v with a description", i, entry, want)
		}
	}
}

// --- model switching inside the allowlist --------------------------------------

func TestStudioModelSwitchRespectsAllowlistAndSurvivesReload(t *testing.T) {
	dir := t.TempDir()
	h, cap := newStudioHarness(t, []llm.Response{textTurn("ok")}, func(c *Config) { c.SessionDir = dir })
	addStudioTestModel(t, "glm-5.3")
	addStudioTestModel(t, "other-model")

	a := postStudioAgent(t, h, studioAgentCapsBody("Switcher Bot", "ready", map[string]any{
		"models": []string{"k3", "glm-5.3"},
	}), http.StatusCreated)
	sid := startStudioConversation(t, h, a.ID)

	// A resolvable model outside the allowlist is refused; the switch inside
	// applies maximum reasoning like the default pin does.
	h.post("/api/sessions/"+sid+"/control", `{"action":"set_model","model":"other-model"}`, http.StatusBadRequest)
	h.post("/api/sessions/"+sid+"/control", `{"action":"set_model","model":"glm-5.3"}`, http.StatusOK)
	if _, model := sessionCwdAndModel(h.session(sid)); model != "glm-5.3" {
		t.Fatalf("session model = %q, want glm-5.3", model)
	}
	if _, _, _, extras := cap.snapshot(); len(extras) == 0 || extras[len(extras)-1]["reasoning_effort"] != "max" {
		t.Fatalf("switched glm-5.3 lost maximum reasoning: %+v", extras)
	}

	// Model policy alone follows the definition when idle; narrowing away the
	// chosen model returns the conversation to its allowed default on reload.
	body := studioAgentCapsBody("Switcher Bot", "ready", map[string]any{"models": []string{"k3"}})
	body["revision"] = a.Revision
	resp := h.do(http.MethodPatch, "/api/manage/agents/"+a.ID, studioBody(body))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("narrow definition: %d", resp.StatusCode)
	}

	h.mgr.Close()
	h2, _ := newStudioHarness(t, []llm.Response{textTurn("ok")}, func(c *Config) { c.SessionDir = dir })
	addStudioTestModel(t, "glm-5.3")
	addStudioTestModel(t, "other-model")
	if _, err := h2.mgr.Get(sid); err != nil {
		t.Fatal(err)
	}
	if _, model := sessionCwdAndModel(h2.session(sid)); model != "k3" {
		t.Fatalf("reloaded model = %q, want the synchronized k3", model)
	}
	caps := sessionCapabilities(t, h2, a.ID, sid)
	if got := stringField(t, caps, "models"); !slices.Equal(got, []string{"k3"}) {
		t.Fatalf("model allowlist did not follow the edit: %v", got)
	}
	h2.post("/api/sessions/"+sid+"/control", `{"action":"set_model","model":"k3"}`, http.StatusOK)

	narrow := sessionCapabilities(t, h2, a.ID, startStudioConversation(t, h2, a.ID))
	if got := stringField(t, narrow, "models"); !slices.Equal(got, []string{"k3"}) {
		t.Fatalf("new conversation models = %v, want the narrowed [k3]", got)
	}
}

// --- MCP selection at construction and refresh ----------------------------------

func mcpToolPresent(name string) func([]tools.Tool, []mcpclient.Status) bool {
	return func(list []tools.Tool, _ []mcpclient.Status) bool {
		for _, tool := range list {
			if tool.Name() == name {
				return true
			}
		}
		return false
	}
}

func TestStudioMCPSelectionHoldsAtConstructAndRefresh(t *testing.T) {
	alpha := newWebCatalogFixture(t, false, true)
	beta := newWebCatalogFixture(t, false, true)
	gamma := newWebCatalogFixture(t, false, true)
	store := filepath.Join(t.TempDir(), "mcp.json")
	cfgOf := func(withGamma bool) mcpclient.Config {
		servers := map[string]*mcpclient.ServerConfig{
			"alpha": {URL: alpha.url, Trust: true, StartupTimeoutMs: 5000},
			"beta":  {URL: beta.url, Trust: true, StartupTimeoutMs: 5000},
		}
		if withGamma {
			servers["gamma"] = &mcpclient.ServerConfig{URL: gamma.url, Trust: true, StartupTimeoutMs: 5000}
		}
		return mcpclient.Config{Servers: servers}
	}
	if err := mcpclient.Save(store, cfgOf(false)); err != nil {
		t.Fatal(err)
	}
	model := &catalogModel{requests: make(chan catalogModelCall, 16)}
	h := newHarness(t, nil, func(c *Config) {
		c.MCPClientStore = store
		c.NewClient = func(config.Resolved, func(llm.RetryInfo)) llm.Client { return model }
	})
	waitCatalog(t, h.mgr.mcpPool, mcpToolPresent("mcp_alpha_echo"))
	waitCatalog(t, h.mgr.mcpPool, mcpToolPresent("mcp_beta_echo"))

	selected := postStudioAgent(t, h, studioAgentCapsBody("Alpha MCP Bot", "ready", map[string]any{
		"mcp_servers": []string{"alpha"},
	}), http.StatusCreated)
	sess := h.session(startStudioConversation(t, h, selected.ID))

	// Construction: only the selected server's tools are registered.
	first := startCatalogRun(t, sess, model)
	modelSchema(t, first, "mcp_alpha_echo")
	withoutModelTool(t, first, "mcp_beta_echo")
	finishCatalogRun(t, sess, first)

	// The pool gains a server mid-conversation; the next run's refresh must
	// not widen the pinned selection.
	if err := mcpclient.Save(store, cfgOf(true)); err != nil {
		t.Fatal(err)
	}
	h.mgr.ReconcileMCP(context.Background(), cfgOf(true))
	waitCatalog(t, h.mgr.mcpPool, mcpToolPresent("mcp_gamma_echo"))
	second := startCatalogRun(t, sess, model)
	modelSchema(t, second, "mcp_alpha_echo")
	withoutModelTool(t, second, "mcp_beta_echo")
	withoutModelTool(t, second, "mcp_gamma_echo")
	finishCatalogRun(t, sess, second)

	// A legacy definition inherits the whole pool, before and after refresh.
	legacy := postStudioAgent(t, h, studioAgentCapsBody("Legacy MCP Bot", "ready", nil), http.StatusCreated)
	legacySess := h.session(startStudioConversation(t, h, legacy.ID))
	third := startCatalogRun(t, legacySess, model)
	modelSchema(t, third, "mcp_alpha_echo")
	modelSchema(t, third, "mcp_beta_echo")
	modelSchema(t, third, "mcp_gamma_echo")
	finishCatalogRun(t, legacySess, third)

	// An explicit empty selection registers nothing at all.
	none := postStudioAgent(t, h, studioAgentCapsBody("No MCP Bot", "ready", map[string]any{
		"mcp_servers": []string{},
	}), http.StatusCreated)
	noneSess := h.session(startStudioConversation(t, h, none.ID))
	fourth := startCatalogRun(t, noneSess, model)
	withoutModelTool(t, fourth, "mcp_alpha_echo")
	finishCatalogRun(t, noneSess, fourth)

	// The session contract reports the pinned server names.
	caps := sessionCapabilities(t, h, selected.ID, sess.ID)
	if got := stringField(t, caps, "mcp_servers"); !slices.Equal(got, []string{"alpha"}) {
		t.Fatalf("pinned mcp_servers = %v, want [alpha]", got)
	}
	legacyCaps := sessionCapabilities(t, h, legacy.ID, legacySess.ID)
	if got := stringField(t, legacyCaps, "mcp_servers"); !slices.Equal(got, []string{"alpha", "beta", "gamma"}) {
		t.Fatalf("legacy mcp_servers = %v, want the whole pool", got)
	}
}

// --- /api/tools MCP groups ---------------------------------------------------

// The skills panel's by-tool view reads one response: the manifest catalog
// plus the pool's per-server MCP groups, narrowed to the conversation's
// pinned server selection.
func TestToolsEndpointServesMCPGroups(t *testing.T) {
	alpha := newWebCatalogFixture(t, false, true)
	beta := newWebCatalogFixture(t, false, true)
	store := filepath.Join(t.TempDir(), "mcp.json")
	if err := mcpclient.Save(store, mcpclient.Config{Servers: map[string]*mcpclient.ServerConfig{
		"alpha": {URL: alpha.url, Trust: true, StartupTimeoutMs: 5000},
		"beta":  {URL: beta.url, Trust: true, StartupTimeoutMs: 5000},
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

	type group struct {
		Name  string `json:"name"`
		State string `json:"state"`
		Tools []struct {
			Name     string `json:"name"`
			WireName string `json:"wire_name"`
			Server   string `json:"server"`
		} `json:"tools"`
	}
	mcpGroups := func(path string) []group {
		resp := h.do(http.MethodGet, path, "")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %d", path, resp.StatusCode)
		}
		var out struct {
			Tools []tools.SynthInfo `json:"tools"`
			MCP   []group           `json:"mcp"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if out.MCP == nil {
			t.Fatalf("%s: mcp must be an array, got null", path)
		}
		return out.MCP
	}

	// Workspace-wide: every enabled server, its tools carrying provenance.
	groups := mcpGroups("/api/tools")
	if len(groups) != 2 {
		t.Fatalf("workspace mcp groups = %+v, want alpha and beta", groups)
	}
	if groups[0].Name != "alpha" || groups[0].State != "connected" {
		t.Fatalf("first group = %+v, want a connected alpha", groups[0])
	}
	if len(groups[0].Tools) != 1 || groups[0].Tools[0].WireName != "mcp_alpha_echo" ||
		groups[0].Tools[0].Name != "echo" || groups[0].Tools[0].Server != "alpha" {
		t.Fatalf("alpha tools = %+v, want echo with its wire name and server", groups[0].Tools)
	}

	// A pinned conversation sees exactly its selection; an explicit empty one
	// sees an empty (but present) array.
	pinned := postStudioAgent(t, h, studioAgentCapsBody("Pinned MCP Bot", "ready", map[string]any{
		"mcp_servers": []string{"beta"},
	}), http.StatusCreated)
	sidPinned := startStudioConversation(t, h, pinned.ID)
	groups = mcpGroups("/api/tools?session_id=" + sidPinned)
	if len(groups) != 1 || groups[0].Name != "beta" {
		t.Fatalf("pinned session mcp groups = %+v, want beta only", groups)
	}

	none := postStudioAgent(t, h, studioAgentCapsBody("Empty MCP Bot", "ready", map[string]any{
		"mcp_servers": []string{},
	}), http.StatusCreated)
	sidNone := startStudioConversation(t, h, none.ID)
	if groups = mcpGroups("/api/tools?session_id=" + sidNone); len(groups) != 0 {
		t.Fatalf("explicit-empty session mcp groups = %+v, want []", groups)
	}

	if code := studioStatus(t, h, http.MethodGet, "/api/tools?session_id="+sidPinned+"ff", ""); code != http.StatusNotFound {
		t.Fatalf("unknown session_id: %d, want 404", code)
	}
}

// --- ownership on the new reads -------------------------------------------------

func TestStudioSessionCapabilityReadsRequireTheOwner(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("ok")))
	alice, bob := h.alice(t), h.bob(t)
	resp := h.doAs(h.token, http.MethodPost, "/api/manage/agents", studioBody(studioAgentCapsBody("Owned Caps Bot", "ready", nil)))
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
	var conv studioConversation
	if err := json.NewDecoder(mk.Body).Decode(&conv); err != nil {
		t.Fatal(err)
	}
	mk.Body.Close()

	for _, path := range []string{
		"/api/apps/" + a.ID + "/sessions/" + conv.ID + "/capabilities",
		"/api/skills?session_id=" + conv.ID,
		"/api/tools?session_id=" + conv.ID,
		"/api/skills/content?name=x&session_id=" + conv.ID,
	} {
		resp := h.doAs(bob, http.MethodGet, path, "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("bob GET %s: %d, want 404", path, resp.StatusCode)
		}
	}
	resp = h.doAs(alice, http.MethodGet, "/api/apps/"+a.ID+"/sessions/"+conv.ID+"/capabilities", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("alice capabilities: %d, want 200", resp.StatusCode)
	}
	resp = h.doAs(alice, http.MethodGet, "/api/skills?session_id="+conv.ID, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("alice session skills: %d, want 200", resp.StatusCode)
	}

	// The capabilities catalog itself is operator-only.
	for _, token := range []string{alice, bob} {
		resp := h.doAs(token, http.MethodGet, "/api/manage/capabilities", "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("non-admin capabilities catalog: %d, want 403", resp.StatusCode)
		}
	}
}
