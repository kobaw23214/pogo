package web

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/mcpclient"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

const studioStarterGenerateRoute = "/api/manage/agents/generate-starters"

func starterGenerationBody(extra map[string]any) map[string]any {
	body := map[string]any{
		"name": "项目研究助手", "description": "研究项目资料", "instructions": "结论附依据，区分事实与推测。",
		"model": "k3", "models": []string{"k3"}, "skills": []string{}, "mcp_servers": []string{}, "tools": []string{}, "count": 2,
	}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

func generatedStarterJSON(count int) string {
	rows := []map[string]string{}
	labels := []string{"资料分析", "方案对比", "写作"}
	for i := 0; i < count; i++ {
		rows = append(rows, map[string]string{"title": fmt.Sprintf("项目分析 %d", i+1), "label": labels[i%len(labels)], "prompt": fmt.Sprintf("帮我分析项目的第 %d 个方面", i+1), "icon": "search"})
	}
	return studioBody(map[string]any{"starters": rows})
}

func starterGenerationFiles(t *testing.T, dirs ...string) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[path] = sha256.Sum256(data)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestStudioStarterGenerationUsesUnsavedDraftAndSelectedModelWithoutToolsOrWrites(t *testing.T) {
	dir := t.TempDir()
	sk := writeTestSkill(t, dir, "project-research", "项目文档与风险分析", "NEVER READ THIS INSTRUCTION BODY")
	h, cap := newStudioHarness(t, []llm.Response{textTurn(generatedStarterJSON(2))}, func(c *Config) { c.Skills = []skills.Skill{sk} })
	config.SetCatalogForTest([]config.Model{
		{ID: "k3", Provider: "kimi"}, {ID: "selected-model", Provider: "kimi", Aliases: []string{"selected-alias"}, FollowupModel: "k3"},
	})
	before := starterGenerationFiles(t, h.mgr.cfg.Cwd, h.mgr.cfg.SessionDir)
	body := starterGenerationBody(map[string]any{
		"name": "", "description": "只填了简介的未保存草稿", "instructions": "", "model": "selected-alias", "models": []string{"k3", "selected-model"},
		"skills": []string{skillIDOf(sk)}, "tools": []string{"read"},
		"existing_starters": []any{map[string]string{"title": "未完成的标题", "icon": "code"}, "已有问题"},
	})
	resp := h.do(http.MethodPost, studioStarterGenerateRoute, studioBody(body))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("generation status %d: %s", resp.StatusCode, b)
	}
	var out struct {
		Starters []starterEntry `json:"starters"`
		Model    string         `json:"model"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Starters) != 2 || out.Model != "selected-model" || out.Starters[0].Label != "资料分析" || out.Starters[1].Label != "方案对比" {
		t.Fatalf("generated response %#v", out)
	}
	if got := cap.clientModels(); !reflect.DeepEqual(got, []string{"selected-model"}) {
		t.Fatalf("default model switched or multiple calls: %v", got)
	}
	systems, counts, histories, _ := cap.snapshot()
	if len(systems) != 1 || systems[0] != studioStarterSystem || !reflect.DeepEqual(counts, []int{0}) || len(histories) != 1 || len(histories[0]) != 1 {
		t.Fatalf("generation used an Agent prompt, tool schemas or history: %v %v %v", systems, counts, histories)
	}
	prompt := histories[0][0].Text()
	for _, expected := range []string{"只填了简介的未保存草稿", "project-research", "项目文档与风险分析", "读取工作区文件内容", "未完成的标题", "已有问题"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("draft metadata missing %q: %s", expected, prompt)
		}
	}
	if strings.Contains(prompt, "NEVER READ") || strings.Contains(prompt, "test-key") || strings.Contains(prompt, "kimi.example") || strings.Contains(prompt, sk.Path) {
		t.Fatalf("generation leaked skill content, paths or provider configuration: %s", prompt)
	}
	if after := starterGenerationFiles(t, h.mgr.cfg.Cwd, h.mgr.cfg.SessionDir); !reflect.DeepEqual(before, after) {
		t.Fatal("unsaved generation wrote workspace or persisted Agent/session state")
	}
	if len(h.mgr.studio.list(true)) != 0 {
		t.Fatal("unsaved generation created an Agent")
	}
}

func TestStudioStarterGenerationPreservesAllSelectedCapabilitiesAboveFormerLimits(t *testing.T) {
	var selectedSkills []skills.Skill
	var skillIDs, serverIDs, toolIDs []string
	dir := t.TempDir()
	for i := 0; i < 201; i++ {
		sk := writeTestSkill(t, dir, fmt.Sprintf("selected-skill-%03d", i), fmt.Sprintf("selected skill %03d", i), "PRIVATE INSTRUCTIONS MUST NOT ENTER GENERATION")
		selectedSkills = append(selectedSkills, sk)
		skillIDs = append(skillIDs, skillIDOf(sk))
	}
	for _, sk := range bindingTestSkills(t) {
		if sk.Name == "workflow-authoring" {
			continue
		}
		selectedSkills = append(selectedSkills, sk)
		skillIDs = append(skillIDs, skillIDOf(sk))
	}
	servers := map[string]*mcpclient.ServerConfig{}
	for i := 0; i < 65; i++ {
		name := fmt.Sprintf("selected-mcp-%03d", i)
		serverIDs = append(serverIDs, name)
		servers[name] = &mcpclient.ServerConfig{URL: "https://not-contacted.invalid/mcp", BearerToken: "PRIVATE MCP SECRET"}
	}
	for _, entry := range tools.BuiltinCatalog() {
		toolIDs = append(toolIDs, entry.Name)
	}
	if len(toolIDs) <= 16 {
		t.Fatal("regression requires more than the former 16 valid native tools")
	}
	h, cap := newStudioHarness(t, []llm.Response{textTurn(generatedStarterJSON(2))}, func(c *Config) { c.Skills = selectedSkills })
	// Install after manager startup: generation may read this local directory,
	// but must never start connections just to describe selected capabilities.
	mcpPath := filepath.Join(t.TempDir(), "mcp.json")
	if err := mcpclient.Save(mcpPath, mcpclient.Config{Servers: servers}); err != nil {
		t.Fatal(err)
	}
	h.mgr.cfg.MCPClientStore = mcpPath
	body := starterGenerationBody(map[string]any{
		"skills":              append(append([]string{}, skillIDs...), " "+skillIDs[0]+" "),
		"mcp_servers":         append(append([]string{}, serverIDs...), " "+serverIDs[0]+" "),
		"tools":               append(append([]string{}, toolIDs...), " "+toolIDs[0]+" "),
		"skill_tool_bindings": tools.SkillBindingNames(),
	})
	resp := h.do(http.MethodPost, studioStarterGenerateRoute, studioBody(body))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("valid large capability selection refused: %d %s", resp.StatusCode, raw)
	}
	_, toolCounts, histories, _ := cap.snapshot()
	if len(histories) != 1 || len(histories[0]) != 1 || !reflect.DeepEqual(toolCounts, []int{0}) {
		t.Fatal("draft generation must make one text-only request, without executable tools")
	}
	var context studioStarterContext
	if err := json.Unmarshal([]byte(histories[0][0].Text()), &context); err != nil {
		t.Fatal(err)
	}
	if len(context.Skills) != len(skillIDs) || len(context.MCPServers) != len(serverIDs) || len(context.Tools) != len(toolIDs) {
		t.Fatalf("selection was truncated or duplicates retained: skills=%d MCP=%d tools=%d", len(context.Skills), len(context.MCPServers), len(context.Tools))
	}
	for i, entry := range context.Skills {
		if entry.Name != selectedSkills[i].Name {
			t.Fatalf("Skill order or final entries changed at %d: %q", i, entry.Name)
		}
	}
	for i, entry := range context.MCPServers {
		if entry.Name != serverIDs[i] || entry.State != "unknown" || len(entry.Tools) != 0 {
			t.Fatalf("uncached MCP order or state changed at %d: %#v", i, entry)
		}
	}
	normalizedTools := normalizedStudioToolSelection(toolIDs)
	for i, entry := range context.Tools {
		if entry.Name != normalizedTools[i] {
			t.Fatalf("native tool order or final entries changed at %d: %q", i, entry.Name)
		}
	}
	if prompt := histories[0][0].Text(); strings.Contains(prompt, "PRIVATE") || strings.Contains(prompt, "not-contacted.invalid") {
		t.Fatal("large selection leaked instructions or connection settings into generation")
	}
	// Removing selection counts must not remove the separate generated-prompt
	// byte budget. The same valid selection can exceed it through metadata.
	for i := range h.mgr.cfg.Skills {
		h.mgr.cfg.Skills[i].Description = strings.Repeat("界", 500)
	}
	oversized := h.do(http.MethodPost, studioStarterGenerateRoute, studioBody(body))
	oversized.Body.Close()
	if oversized.StatusCode != http.StatusBadRequest || len(cap.clientModels()) != 1 {
		t.Fatal("oversized capability metadata reached the model or bypassed the prompt budget")
	}
	if len(h.mgr.studio.list(true)) != 0 {
		t.Fatal("draft generation created an Agent")
	}
}

func TestStudioStarterGenerationRejectsInvalidDraftBeforeModelCall(t *testing.T) {
	h, cap := newStudioHarness(t, []llm.Response{textTurn(generatedStarterJSON(2))})
	for _, tc := range []struct {
		name  string
		extra map[string]any
	}{
		{"empty draft", map[string]any{"name": "", "description": " ", "instructions": ""}},
		{"missing model selection", map[string]any{"models": nil}},
		{"empty model selection", map[string]any{"models": []string{}}},
		{"empty default model", map[string]any{"model": ""}},
		{"default outside selection", map[string]any{"models": []string{"k3"}, "model": "second"}},
		{"unknown model", map[string]any{"models": []string{"unknown"}}},
		{"zero count", map[string]any{"count": 0}},
		{"too many", map[string]any{"count": 9}},
		{"long name", map[string]any{"name": strings.Repeat("名", 61)}},
		{"long description", map[string]any{"description": strings.Repeat("简", 241)}},
		{"long instruction", map[string]any{"instructions": strings.Repeat("指", 12001)}},
		{"long existing prompt", map[string]any{"existing_starters": []string{strings.Repeat("问", 201)}}},
		{"invalid existing icon", map[string]any{"existing_starters": []any{map[string]string{"title": "draft", "icon": "unknown"}}}},
		{"unknown skill", map[string]any{"skills": []string{"unknown-skill"}}},
		{"unknown MCP", map[string]any{"mcp_servers": []string{"unknown-server"}}},
		{"unknown tool", map[string]any{"tools": []string{"run-anything"}}},
		{"blank ID in large selection", map[string]any{"skills": make([]string, 201)}},
		{"oversized ID", map[string]any{"skills": []string{strings.Repeat("x", 201)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.do(http.MethodPost, studioStarterGenerateRoute, studioBody(starterGenerationBody(tc.extra)))
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				b, _ := io.ReadAll(resp.Body)
				t.Fatalf("got %d: %s", resp.StatusCode, b)
			}
		})
	}
	if models := cap.clientModels(); len(models) > 0 {
		t.Fatalf("invalid draft reached a model: %v", models)
	}
}

func TestStudioStarterGenerationRejectsUnreadableDisabledMCPCatalogAndUnconfiguredModels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	h, cap := newStudioHarness(t, []llm.Response{textTurn(generatedStarterJSON(2))}, func(c *Config) { c.MCPClientStore = path })
	disabled := false
	if err := mcpclient.Save(path, mcpclient.Config{Servers: map[string]*mcpclient.ServerConfig{"remote": {URL: "https://server.example/mcp", Enabled: &disabled, BearerToken: "NEVER SEND THIS SECRET"}}}); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"disabled", "corrupt", "unconfigured"} {
		if state == "corrupt" {
			if err := os.WriteFile(path, []byte(`{"servers":`), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		body := starterGenerationBody(map[string]any{"mcp_servers": []string{"remote"}})
		if state == "unconfigured" {
			t.Setenv("KIMI_API_KEY", "")
			body["mcp_servers"] = []string{}
		}
		resp := h.do(http.MethodPost, studioStarterGenerateRoute, studioBody(body))
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || strings.Contains(string(b), "NEVER SEND") {
			t.Fatalf("%s catalog/model not refused safely: %d %s", state, resp.StatusCode, b)
		}
	}
	if got := cap.clientModels(); len(got) != 0 {
		t.Fatalf("invalid selection called model: %v", got)
	}
}

func TestStudioStarterGenerationMCPCatalogDoesNotStartPrivateAccountPool(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn(generatedStarterJSON(2))))
	h.psrv.auth.mu.Lock()
	u := h.psrv.auth.users["alice"]
	u.Admin = true
	h.psrv.auth.users["alice"] = u
	h.psrv.auth.mu.Unlock()
	// Install a configured server after manager startup. There is no existing
	// private pool, and obtaining metadata must not start one or use its key.
	path := filepath.Join(t.TempDir(), "mcp.json")
	cfg := mcpclient.Config{Servers: map[string]*mcpclient.ServerConfig{
		"remote": {URL: "http://127.0.0.1:1/mcp", BearerToken: "SHARED-MCP-SECRET"},
	}}
	if err := mcpclient.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	h.mgr.cfg.MCPClientStore = path
	h.mgr.mcpPool = mcpclient.NewPool(nil)
	h.mgr.mcpBase = cfg
	h.mgr.mcpTokenFor = func() map[identity.Scope]map[string]string {
		return map[identity.Scope]map[string]string{identity.LocalScope("alice"): {"remote": "PRIVATE-MCP-SECRET"}}
	}
	resp := h.doAs(aliceToken, http.MethodPost, studioStarterGenerateRoute, studioBody(starterGenerationBody(map[string]any{"mcp_servers": []string{"remote"}})))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("cached metadata generation: %d %s", resp.StatusCode, b)
	}
	h.mgr.mcpOwnerMu.Lock()
	pools := len(h.mgr.mcpOwnerPools)
	h.mgr.mcpOwnerMu.Unlock()
	if pools != 0 {
		t.Fatal("draft generation created a private MCP connection pool")
	}
	// The only model request stays under the generator's constant system
	// prompt. Read the helper directly to pin the missing-cache state too.
	req := httptest.NewRequest(http.MethodPost, studioStarterGenerateRoute, nil)
	req = req.WithContext(context.WithValue(req.Context(), requestIdentityKey, &requestIdentity{Scope: identity.LocalScope("alice"), Username: "alice", Local: true, Admin: true}))
	metadata, err := h.psrv.studioStarterMetadata(req, studioGenerateStartersInput{Name: "草稿", MCPServers: []string{"remote"}, Count: 2})
	if err != nil || len(metadata.MCPServers) != 1 || metadata.MCPServers[0].State != "unknown" || len(metadata.MCPServers[0].Tools) != 0 {
		t.Fatalf("missing private cache borrowed capabilities: %#v %v", metadata, err)
	}
	data, _ := json.Marshal(metadata)
	if strings.Contains(string(data), "SECRET") || strings.Contains(string(data), "127.0.0.1") {
		t.Fatalf("MCP credentials/configuration leaked: %s", data)
	}
}

func TestStudioStarterGenerationAuthenticationAndBodyBounds(t *testing.T) {
	h, cap := newStudioHarness(t, []llm.Response{textTurn(generatedStarterJSON(2))})
	resp, err := h.srv.Client().Post(h.srv.URL+studioStarterGenerateRoute, "application/json", strings.NewReader(studioBody(starterGenerationBody(nil))))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated generation: %d", resp.StatusCode)
	}
	for _, body := range []string{studioBody(starterGenerationBody(map[string]any{"unexpected": true})), studioBody(starterGenerationBody(nil)) + " {}", `null`} {
		resp := h.do(http.MethodPost, studioStarterGenerateRoute, body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid JSON body: %d", resp.StatusCode)
		}
	}
	resp = h.do(http.MethodPost, studioStarterGenerateRoute, studioBody(starterGenerationBody(map[string]any{"instructions": strings.Repeat("x", studioGenerateBodyLimit+1)})))
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body: %d", resp.StatusCode)
	}
	if len(cap.clientModels()) != 0 {
		t.Fatal("invalid HTTP request reached model")
	}
	mu := newMultiuserHarness(t, scriptedTurns(textTurn(generatedStarterJSON(2))))
	resp = mu.doAs(aliceToken, http.MethodPost, studioStarterGenerateRoute, studioBody(starterGenerationBody(nil)))
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin generated: %d", resp.StatusCode)
	}
}

func TestParseGeneratedStudioStartersRequiresCompleteUniqueBoundedCards(t *testing.T) {
	for _, text := range []string{generatedStarterJSON(2), "```json\n" + generatedStarterJSON(2) + "\n```", "```\n" + generatedStarterJSON(2) + "\n```"} {
		rows, err := parseGeneratedStudioStarters(text, 2, nil)
		if err != nil || len(rows) != 2 || rows[0].Label != "资料分析" || rows[1].Label != "方案对比" {
			t.Fatalf("valid cards refused: %v", err)
		}
	}
	for name, text := range map[string]string{
		"malformed": `{"starters":[`, "missing count": generatedStarterJSON(1), "trailing prose": generatedStarterJSON(2) + " done",
		"unknown field": `{"starters":[{"title":"标题","label":"资料分析","prompt":"问题","panel":"execute"}]}`,
		"plain prompt":  `{"starters":["问题"]}`, "blank title": `{"starters":[{"title":" ","label":"资料分析","prompt":"问题"}]}`,
		"blank prompt": `{"starters":[{"title":"标题","label":"资料分析","prompt":" "}]}`, "invalid icon": `{"starters":[{"title":"标题","label":"资料分析","prompt":"问题","icon":"unsafe"}]}`,
		"missing label": `{"starters":[{"title":"标题","prompt":"问题"}]}`,
		"blank label":   `{"starters":[{"title":"标题","label":" \t\n ","prompt":"问题"}]}`,
		"long label":    studioBody(map[string]any{"starters": []map[string]string{{"title": "标题", "label": strings.Repeat("类", 41), "prompt": "问题"}}}),
		"long prompt":   studioBody(map[string]any{"starters": []map[string]string{{"title": "标题", "label": "资料分析", "prompt": strings.Repeat("问", 201)}}}),
		"duplicate":     `{"starters":[{"title":"A","label":"Analysis","prompt":"  Help  me "},{"title":"B","label":"Writing","prompt":"help me"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			count := 1
			if name == "duplicate" || name == "missing count" || name == "trailing prose" {
				count = 2
			}
			if _, err := parseGeneratedStudioStarters(text, count, nil); err == nil {
				t.Fatalf("accepted invalid model content: %s", text)
			}
		})
	}
	if _, err := parseGeneratedStudioStarters(generatedStarterJSON(2), 2, []starterEntry{{Prompt: "帮我分析项目的第 1 个方面"}}); err == nil {
		t.Fatal("generated duplicate of existing prompt accepted")
	}
	boundary := studioBody(map[string]any{"starters": []map[string]string{{"title": "标题", "label": strings.Repeat("类", 40), "prompt": "问题"}}})
	if rows, err := parseGeneratedStudioStarters(boundary, 1, []starterEntry{{Title: "未完成的手写卡片", Label: ""}}); err != nil || len(rows) != 1 || rows[0].Label != strings.Repeat("类", 40) {
		t.Fatalf("valid label boundary or unlabeled existing draft rejected: %v %#v", err, rows)
	}
	trimmed := studioBody(map[string]any{"starters": []map[string]string{{"title": " 短标题 ", "label": " \t资料分析\n ", "prompt": " 问题 "}}})
	if rows, err := parseGeneratedStudioStarters(trimmed, 1, nil); err != nil || len(rows) != 1 || rows[0].Label != "资料分析" {
		t.Fatalf("generated label was not trimmed and retained: %v %#v", err, rows)
	}
}

type starterGenerationFailureClient struct {
	err  error
	wait bool
}

func (c *starterGenerationFailureClient) Model() string { return "k3" }
func (c *starterGenerationFailureClient) Stream(ctx context.Context, _ string, _ []llm.Message, _ []llm.ToolSchema, _ func(llm.Delta)) (llm.Response, error) {
	if c.wait {
		<-ctx.Done()
		return llm.Response{}, ctx.Err()
	}
	return llm.Response{}, c.err
}

func TestStudioStarterGenerationErrorsAreFriendlyAndContextIsCancellable(t *testing.T) {
	for _, tc := range []struct {
		name     string
		client   *starterGenerationFailureClient
		want     int
		canceled bool
	}{
		{"provider", &starterGenerationFailureClient{err: errors.New("secret-key=DO-NOT-EXPOSE")}, http.StatusBadGateway, false},
		{"deadline", &starterGenerationFailureClient{wait: true}, http.StatusGatewayTimeout, false},
		{"canceled", &starterGenerationFailureClient{wait: true}, http.StatusRequestTimeout, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, scriptedTurns(), func(c *Config) {
				c.NewClient = func(config.Resolved, func(llm.RetryInfo)) llm.Client { return tc.client }
			})
			req := httptest.NewRequest(http.MethodPost, studioStarterGenerateRoute, strings.NewReader(studioBody(starterGenerationBody(nil))))
			if tc.client.wait {
				ctx, cancel := context.WithTimeout(req.Context(), 10*time.Millisecond)
				defer cancel()
				if tc.canceled {
					cancel()
				}
				req = req.WithContext(ctx)
			}
			w := httptest.NewRecorder()
			(&Server{mgr: h.mgr}).handleStudioGenerateStarters(w, req)
			if w.Code != tc.want || strings.Contains(w.Body.String(), "DO-NOT-EXPOSE") {
				t.Fatalf("bad error: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestStudioStarterGenerationNeverExecutesModelToolCallsOrPartialOutput(t *testing.T) {
	for name, response := range map[string]llm.Response{
		"tool":           {Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, Name: "write", ID: "call", Input: json.RawMessage(`{"path":"created","content":"bad"}`)}}}, StopReason: llm.StopToolUse},
		"truncated":      {Message: textTurn(generatedStarterJSON(2)).Message, StopReason: llm.StopMaxTokens},
		"count mismatch": textTurn(generatedStarterJSON(1)),
	} {
		t.Run(name, func(t *testing.T) {
			h, cap := newStudioHarness(t, []llm.Response{response})
			before := starterGenerationFiles(t, h.mgr.cfg.Cwd, h.mgr.cfg.SessionDir)
			resp := h.do(http.MethodPost, studioStarterGenerateRoute, studioBody(starterGenerationBody(nil)))
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadGateway {
				t.Fatalf("incomplete output accepted: %d", resp.StatusCode)
			}
			if after := starterGenerationFiles(t, h.mgr.cfg.Cwd, h.mgr.cfg.SessionDir); !reflect.DeepEqual(before, after) {
				t.Fatal("generation executed/wrote a returned tool call")
			}
			if len(cap.clientModels()) != 1 {
				t.Fatal("bad model output caused an implicit repair/fallback call")
			}
		})
	}
}
