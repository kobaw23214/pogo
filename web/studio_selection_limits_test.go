package web

// Capability selections are catalog membership, not a tool-count budget. These
// tests drive the real create/edit endpoints with valid catalogs beyond the old
// limits, without connecting any MCP server or making a provider request.
import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/mcpclient"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

func patchStudioSelectionAgent(t *testing.T, h *harness, id string, body map[string]any) studioAgent {
	t.Helper()
	resp := h.do(http.MethodPatch, "/api/manage/agents/"+id, studioBody(body))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("edit agent: %d (%s)", resp.StatusCode, msg)
	}
	var a studioAgent
	if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
		t.Fatal(err)
	}
	return a
}

func assertStudioSelectionArrays(t *testing.T, h *harness, id string, want map[string][]string) {
	t.Helper()
	raw := getStudioAppRaw(t, h, id)
	for key, values := range want {
		if got := stringField(t, raw, key); !slices.Equal(got, values) {
			t.Fatalf("%s selection was reordered, truncated or changed: got %d entries, want %d\ngot %v\nwant %v", key, len(got), len(values), got, values)
		}
	}
}

func normalizedStudioToolSelection(ids []string) []string {
	out := []string{}
	for _, id := range ids {
		if tools.SkillBindingForTool(id) == "" {
			out = append(out, id)
		}
	}
	for _, name := range tools.SkillBindingNames() {
		for _, tool := range tools.SkillBindingTools(name) {
			if slices.Contains(ids, tool) {
				out = append(out, tool)
			}
		}
	}
	return out
}

func TestStudioSelectionsBeyondFormerCapsRoundTrip(t *testing.T) {
	const skillCount, serverCount = 205, 70
	dir := t.TempDir()
	allSkills := make([]skills.Skill, 0, skillCount)
	skillIDs := make([]string, 0, skillCount)
	for i := 0; i < skillCount; i++ {
		sk := writeTestSkill(t, dir, fmt.Sprintf("selection-skill-%03d", i), "isolated selection metadata", "# Selection fixture")
		allSkills = append(allSkills, sk)
		skillIDs = append(skillIDs, skillIDOf(sk))
	}
	for _, sk := range bindingTestSkills(t) {
		allSkills = append(allSkills, sk)
		skillIDs = append(skillIDs, skillIDOf(sk))
	}
	servers := make(map[string]*mcpclient.ServerConfig, serverCount)
	serverIDs := make([]string, 0, serverCount)
	for i := 0; i < serverCount; i++ {
		name := fmt.Sprintf("selection-server-%03d", i)
		// Configuration validation is intentional. The store is attached only
		// after construction, so no MCP pool starts or contacts this address.
		servers[name] = &mcpclient.ServerConfig{URL: "http://127.0.0.1:1/not-connected"}
		serverIDs = append(serverIDs, name)
	}
	store := filepath.Join(t.TempDir(), "mcp.json")
	if err := mcpclient.Save(store, mcpclient.Config{Servers: servers}); err != nil {
		t.Fatal(err)
	}
	h, capture := newStudioHarness(t, []llm.Response{textTurn("unused")}, func(c *Config) {
		c.Skills = allSkills
	})
	h.mgr.cfg.MCPClientStore = store
	if h.mgr.mcpPool != nil {
		t.Fatal("selection metadata fixture unexpectedly started an MCP pool")
	}
	toolIDs := make([]string, 0, len(tools.BuiltinCatalog()))
	for _, info := range tools.BuiltinCatalog() {
		toolIDs = append(toolIDs, info.Name)
	}
	if len(toolIDs) <= 16 {
		t.Fatal("this regression requires the real native catalog to exceed the retired limit")
	}
	selected := map[string][]string{"skills": skillIDs, "mcp_servers": serverIDs, "tools": normalizedStudioToolSelection(toolIDs)}
	makeBody := func(name string, values map[string][]string) map[string]any {
		extra := map[string]any{"skill_tool_bindings": tools.SkillBindingNames()}
		for field, ids := range values {
			// Deduplicate only after retaining the first occurrence, including
			// duplicate entries beyond the retired count boundary.
			withDuplicates := append([]string{" " + ids[0] + " "}, ids[1:]...)
			extra[field] = append(withDuplicates, ids[0], ids[len(ids)-1])
		}
		return studioAgentCapsBody(name, "ready", extra)
	}
	a := postStudioAgent(t, h, makeBody("All selections", selected), http.StatusCreated)
	assertStudioSelectionArrays(t, h, a.ID, selected)

	reversed := map[string][]string{}
	for field, ids := range selected {
		reversed[field] = slices.Clone(ids)
		if field != "tools" {
			slices.Reverse(reversed[field])
		}
	}
	edit := makeBody("All selections edited", reversed)
	edit["revision"] = a.Revision
	updated := patchStudioSelectionAgent(t, h, a.ID, edit)
	if updated.ID != a.ID || updated.Revision != a.Revision+1 {
		t.Fatalf("editing replaced the definition or did not save: %+v", updated)
	}
	assertStudioSelectionArrays(t, h, a.ID, reversed)

	// A long valid prefix must not hide an unknown or blank final reference.
	// Rejected edits must leave the entire previous definition unchanged.
	for _, field := range []string{"skills", "mcp_servers", "tools"} {
		for _, bad := range []struct{ name, value, reason string }{
			{"unknown", "selection-does-not-exist", "未知"},
			{"blank", " \t ", "不能为空"},
		} {
			t.Run(field+"/"+bad.name, func(t *testing.T) {
				for _, method := range []string{http.MethodPost, http.MethodPatch} {
					body := makeBody("Invalid final reference", reversed)
					body[field] = append(slices.Clone(reversed[field]), bad.value)
					route := "/api/manage/agents"
					if method == http.MethodPatch {
						route += "/" + a.ID
						body["revision"] = updated.Revision
					}
					resp := h.do(method, route, studioBody(body))
					msg, _ := io.ReadAll(resp.Body)
					resp.Body.Close()
					if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(msg), bad.reason) {
						t.Fatalf("%s: invalid reference was accepted or failed for the wrong reason: %d %s", method, resp.StatusCode, msg)
					}
				}
				assertStudioSelectionArrays(t, h, a.ID, reversed)
				stored, ok := h.mgr.studio.get(a.ID)
				if !ok || stored.Revision != updated.Revision {
					t.Fatal("rejected edit changed the saved definition")
				}
			})
		}
	}
	if agents := h.mgr.studio.list(true); len(agents) != 1 {
		t.Fatal("invalid creates persisted extra definitions")
	}
	if len(capture.clientModels()) != 0 {
		t.Fatal("saving selections unexpectedly called a model")
	}
}

func TestStudioSelectionOmittedAndEmptyRemainDistinctAcrossEdits(t *testing.T) {
	h, capture := newStudioHarness(t, []llm.Response{textTurn("unused")})
	a := postStudioAgent(t, h, studioAgentCapsBody("Omitted selections", "ready", nil), http.StatusCreated)
	a = makeStudioAgentLegacy(t, h, a)
	for _, key := range []string{"skills", "mcp_servers", "tools"} {
		if _, present := getStudioAppRaw(t, h, a.ID)[key]; present {
			t.Fatalf("omitted %s unexpectedly acquired a selection", key)
		}
	}
	empty := map[string][]string{"skills": {}, "mcp_servers": {}, "tools": {}}
	body := studioAgentCapsBody("Explicitly empty selections", "ready", map[string]any{
		"revision": a.Revision, "skills": []string{}, "mcp_servers": []string{}, "tools": []string{},
		"skill_tool_bindings": tools.SkillBindingNames(),
	})
	a = patchStudioSelectionAgent(t, h, a.ID, body)
	assertStudioSelectionArrays(t, h, a.ID, empty)
	stored, ok := h.mgr.studio.get(a.ID)
	if !ok || stored.Skills == nil || stored.MCPServers == nil || stored.Tools == nil {
		t.Fatal("explicitly empty selections became legacy inherited permissions")
	}
	body = studioAgentCapsBody("Omitted again", "ready", map[string]any{"revision": a.Revision})
	a = patchStudioSelectionAgent(t, h, a.ID, body)
	stored, ok = h.mgr.studio.get(a.ID)
	if !ok || stored.Skills != nil || stored.MCPServers != nil || stored.Tools == nil {
		t.Fatal("omitted native tools must be materialized, not confused with an explicit empty selection")
	}
	for _, key := range []string{"skills", "mcp_servers"} {
		if _, present := getStudioAppRaw(t, h, a.ID)[key]; present {
			t.Fatalf("edited omitted %s was serialized as empty instead of absent", key)
		}
	}
	if got := stringField(t, getStudioAppRaw(t, h, a.ID), "tools"); !slices.Contains(got, "read") || slices.Contains(got, tools.BackgroundTaskName) {
		t.Fatalf("omitted native tools must inherit independent tools without linked grants: %v", got)
	}
	if len(capture.clientModels()) != 0 {
		t.Fatal("saving nil/empty selections unexpectedly called a model")
	}
}
