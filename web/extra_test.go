package web

// Tests for the three runtime-management surfaces: skill upload, session
// import/export, and the provider configuration endpoints. All drive the real
// Server through the same harness the run-lifecycle tests use.

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/skills"
)

func idleRespond(int) (llm.Response, error) { return llm.Response{}, nil }

// buildSkillZip packs the two accepted layouts into a zip in memory: SKILL.md
// at the archive root, or one wrapper directory around it.
func buildSkillZip(t *testing.T, wrapped bool) []byte {
	t.Helper()
	prefix := ""
	if wrapped {
		prefix = "sample-skill/"
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		prefix + "SKILL.md":         "---\nname: sample-skill\ndescription: a test skill\n---\n\nBody.\n",
		prefix + "scripts/run.sh":   "#!/bin/sh\necho hi\n",
		prefix + "references/md.md": "# note\n",
	} {
		fw, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSkillUploadInstallsAndReloads(t *testing.T) {
	userDir := t.TempDir()
	h := newHarness(t, idleRespond, func(c *Config) {
		c.SkillOptions = skills.Options{UserDir: userDir}
	})

	for _, wrapped := range []bool{false, true} {
		// h.do takes a string body; a zip is binary, so build the request here.
		req, err := http.NewRequest(http.MethodPost, h.srv.URL+"/api/skills/upload",
			bytes.NewReader(buildSkillZip(t, wrapped)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+h.token)
		req.Header.Set("Content-Type", "application/zip")
		res, err := h.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("wrapped=%v: status %d: %s", wrapped, res.StatusCode, body)
		}
		var out struct {
			Name string `json:"name"`
			Path string `json:"path"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		if out.Name != "sample-skill" {
			t.Fatalf("installed as %q", out.Name)
		}
	}

	// The bundle landed with SKILL.md at the install root, not nested twice.
	if _, err := os.Stat(filepath.Join(userDir, "sample-skill", "SKILL.md")); err != nil {
		t.Fatalf("installed skill missing: %v", err)
	}
	// The reload made it live for the listing.
	var list struct {
		Skills []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"skills"`
	}
	if err := decodeJSONBody(h.do(http.MethodGet, "/api/skills", ""), &list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, sk := range list.Skills {
		if sk.Name == "sample-skill" {
			found = true
		}
	}
	if !found {
		t.Fatalf("uploaded skill not in listing: %+v", list.Skills)
	}

	// A zip-slip entry is refused.
	var evil bytes.Buffer
	zw := zip.NewWriter(&evil)
	fw, _ := zw.Create("../escape.txt")
	_, _ = fw.Write([]byte("no"))
	_ = zw.Close()
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/api/skills/upload", bytes.NewReader(evil.Bytes()))
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Content-Type", "application/zip")
	res, err := h.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("zip-slip accepted: %d", res.StatusCode)
	}
}

func TestSkillSampleDownloads(t *testing.T) {
	h := newHarness(t, idleRespond)
	res := h.do(http.MethodGet, "/api/skills/sample", "")
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("content-type %q", ct)
	}
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("sample is not a zip: %v", err)
	}
	for _, want := range []string{"sample-skill/SKILL.md", "sample-skill/scripts/hello.sh"} {
		found := false
		for _, zf := range zr.File {
			if zf.Name == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("sample zip lacks %s", want)
		}
	}
}

// writeDirectSession creates a session file without driving a run, the way an
// import consumer cares about it: records on disk, listable, exportable.
func writeDirectSession(t *testing.T, dir, prompt string) string {
	t.Helper()
	store, err := session.Create(dir, t.TempDir(), "k3")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(llm.UserText(prompt)); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(filepath.Base(store.Path()), ".jsonl")
}

func TestSessionExportImportRoundTrip(t *testing.T) {
	h := newHarness(t, idleRespond)
	sid := writeDirectSession(t, h.mgr.sessionDir(), "hello export")

	var ex exportedSession
	if err := decodeJSONBody(h.do(http.MethodGet, "/api/sessions/"+sid+"/export", ""), &ex); err != nil {
		t.Fatal(err)
	}
	if ex.Kind != sessionExportKind || len(ex.Records) == 0 {
		t.Fatalf("export shape: %+v", ex)
	}
	if !strings.Contains(ex.Title, "hello export") {
		t.Fatalf("export title: %q", ex.Title)
	}
	if cd := exportFilename(sid, ex.Title, ".json"); !strings.Contains(cd, ".json") {
		t.Fatalf("download name: %q", cd)
	}

	// Import the export back in: one new session with the same message count.
	var imp struct {
		Imported int `json:"imported"`
	}
	body, _ := json.Marshal(ex)
	if err := decodeJSONBody(h.do(http.MethodPost, "/api/sessions/import", string(body)), &imp); err != nil {
		t.Fatal(err)
	}
	if imp.Imported != 1 {
		t.Fatalf("imported %d", imp.Imported)
	}
	var list struct {
		Sessions []struct {
			ID       string `json:"id"`
			Messages int    `json:"messages"`
		} `json:"sessions"`
	}
	if err := decodeJSONBody(h.do(http.MethodGet, "/api/sessions", ""), &list); err != nil {
		t.Fatal(err)
	}
	copies := 0
	for _, s := range list.Sessions {
		if s.Messages == 1 {
			copies++
		}
	}
	if copies < 2 {
		t.Fatalf("imported copy missing from listing: %+v", list.Sessions)
	}

	// The sample endpoint serves something the import endpoint accepts.
	var sample exportedSession
	if err := decodeJSONBody(h.do(http.MethodGet, "/api/sessions/sample", ""), &sample); err != nil {
		t.Fatal(err)
	}
	sampleBody, _ := json.Marshal(sample)
	var imp2 struct {
		Imported int `json:"imported"`
	}
	if err := decodeJSONBody(h.do(http.MethodPost, "/api/sessions/import", string(sampleBody)), &imp2); err != nil {
		t.Fatal(err)
	}
	if imp2.Imported != 1 {
		t.Fatalf("sample not importable: %+v", imp2)
	}

	// Foreign JSON is refused with an explanation.
	res := h.do(http.MethodPost, "/api/sessions/import", `{"kind":"other","records":[]}`)
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("garbage accepted: %d", res.StatusCode)
	}
}

// TestSessionExportMarkdown pins the reading copy: the markdown endpoint
// serves the live branch with every block kind labelled, not just the prose.
func TestSessionExportMarkdown(t *testing.T) {
	h := newHarness(t, idleRespond)
	store, err := session.Create(h.mgr.sessionDir(), t.TempDir(), "k3")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(llm.UserText("look at this")); err != nil {
		t.Fatal(err)
	}
	assistant := llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
		{Type: llm.BlockThinking, Text: "plan first"},
		{Type: llm.BlockText, Text: "checking"},
		{Type: llm.BlockToolUse, ID: "t1", Name: "bash", Input: json.RawMessage(`{"command":"ls"}`)},
	}}
	if err := store.Append(assistant); err != nil {
		t.Fatal(err)
	}
	results := llm.Message{Role: llm.RoleUser, Content: []llm.Block{
		{Type: llm.BlockToolResult, ToolUseID: "t1", Text: "file list", DurationMS: 45},
	}}
	if err := store.Append(results); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendStats(session.Stats{
		Usage: &session.UsageStats{Input: 1200, Output: 80},
		Tools: &session.ToolStats{CallCount: 1, Duration: 45},
	}); err != nil {
		t.Fatal(err)
	}
	sid := strings.TrimSuffix(filepath.Base(store.Path()), ".jsonl")

	res := h.do(http.MethodGet, "/api/sessions/"+sid+"/export.md", "")
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "text/markdown; charset=utf-8" {
		t.Fatalf("content-type %q", ct)
	}
	if cd := res.Header.Get("Content-Disposition"); !strings.Contains(cd, ".md") {
		t.Fatalf("disposition %q", cd)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(body)
	for _, want := range []string{
		"👤 用户", "look at this",
		"🤖 助手", "思考过程", "plan first", "checking",
		"工具调用 `bash`", `"command": "ls"`,
		"`bash` 结果", "file list", "45ms",
		"> 📊 本轮", "输入 1,200 tok",
		"## 会话统计", "工具调用 1 次", "Token（直接）",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("markdown lacks %q\n---\n%s", want, doc)
		}
	}

	// A missing session is a 404, not an empty document.
	if res := h.do(http.MethodGet, "/api/sessions/nope-123/export.md", ""); res.StatusCode != http.StatusNotFound {
		t.Fatalf("missing session: %d", res.StatusCode)
	}
}

func TestConfigGetPutReloads(t *testing.T) {
	// A real file behind POGO_CONFIG; Save writes it and Reload applies it.
	cfgPath := filepath.Join(t.TempDir(), "providers.json")
	t.Setenv("POGO_CONFIG", cfgPath)
	savedM, savedP := config.Catalog(), config.Providers()
	minimal := `{"providers":{"kimi":{"base_url":"https://kimi.example/v1","key_env":"KIMI_API_KEY"}},"models":[{"id":"k3","provider":"kimi","context_window":200000}]}`
	if err := os.WriteFile(cfgPath, []byte(minimal), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		config.SetCatalogForTest(savedM)
		config.SetProvidersForTest(savedP)
	})

	h := newHarness(t, idleRespond)

	var got struct {
		Path      string `json:"path"`
		Raw       string `json:"raw"`
		Providers []struct {
			Name       string  `json:"name"`
			Configured bool    `json:"configured"`
			KeyValue   *string `json:"key_value"`
		} `json:"providers"`
	}
	if err := decodeJSONBody(h.do(http.MethodGet, "/api/config", ""), &got); err != nil {
		t.Fatal(err)
	}
	if got.Path != cfgPath || len(got.Providers) != 1 || !got.Providers[0].Configured {
		t.Fatalf("get config: %+v", got)
	}
	// The live value behind key_env rides along ("test-key" is what newHarness
	// installs): the dialog's curl preview reproduces the real request.
	if got.Providers[0].KeyValue == nil || *got.Providers[0].KeyValue != "test-key" {
		t.Fatalf("live key value not reported: %+v", got.Providers[0])
	}

	// PUT via the raw editor: move the base URL, rotate the key through env.
	moved := `{"providers":{"kimi":{"base_url":"https://moved.example/v1","key_env":"KIMI_API_KEY"}},"models":[{"id":"k3","provider":"kimi","context_window":200000}]}`
	var saved2 struct {
		OK      bool   `json:"ok"`
		Default string `json:"default"`
	}
	putBody, _ := json.Marshal(map[string]any{
		"raw": moved,
		"env": map[string]string{"KIMI_API_KEY": "rotated"},
	})
	if err := decodeJSONBody(h.do(http.MethodPut, "/api/config", string(putBody)), &saved2); err != nil {
		t.Fatal(err)
	}
	if !saved2.OK || saved2.Default != "k3" {
		t.Fatalf("save result: %+v", saved2)
	}
	onDisk, err := os.ReadFile(cfgPath)
	if err != nil || !strings.Contains(string(onDisk), "moved.example") {
		t.Fatalf("file not rewritten: %v %s", err, onDisk)
	}
	if cfg, err := config.Resolve("k3"); err != nil || cfg.BaseURL != "https://moved.example/v1" || cfg.APIKey != "rotated" {
		t.Fatalf("resolve after save: %+v %v", cfg, err)
	}

	// A model naming an unknown provider is refused, and the file survives.
	invalid := `{"providers":{},"models":[{"id":"x","provider":"ghost"}]}`
	res := h.do(http.MethodPut, "/api/config", fmt.Sprintf(`{"raw":%q}`, invalid))
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid config accepted: %d", res.StatusCode)
	}
	if onDisk, _ := os.ReadFile(cfgPath); !strings.Contains(string(onDisk), "moved.example") {
		t.Fatal("invalid save rewrote the file")
	}
}

// The probe endpoint forges the dialog's curl from the server side. A fake
// gateway captures what actually arrived, so the assertions cover the whole
// shape: the URL join, the Bearer key resolved from key_env, custom headers,
// and the extra_body merge over the wire request.
func TestConfigProbeSendsThePreviewedRequest(t *testing.T) {
	var gotReq struct {
		Path    string            `json:"path"`
		Auth    string            `json:"auth"`
		Headers map[string]string `json:"headers"`
		Body    map[string]any    `json:"body"`
	}
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		headers := map[string]string{}
		for k := range r.Header {
			headers[k] = r.Header.Get(k)
		}
		gotReq = struct {
			Path    string            `json:"path"`
			Auth    string            `json:"auth"`
			Headers map[string]string `json:"headers"`
			Body    map[string]any    `json:"body"`
		}{r.URL.Path, r.Header.Get("Authorization"), headers, parsed}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"pong"}}]}`))
	}))
	defer gw.Close()
	t.Setenv("PROBE_API_KEY", "sk-from-env")

	h := newHarness(t, idleRespond)
	// The probe's env-resolved key may only ride to a host the config already
	// names, so the test gateway joins the provider table for these calls to
	// be allowed at all (the harness's own cleanup restores the original).
	providers := config.Providers()
	providers["probe"] = config.Provider{BaseURL: gw.URL}
	config.SetProvidersForTest(providers)
	payload, _ := json.Marshal(map[string]any{
		"base_url":   gw.URL + "/compatible-mode/v1/",
		"key_env":    "PROBE_API_KEY",
		"model":      "qwen-max",
		"headers":    map[string]string{"X-DashScope-SSE": "enable"},
		"extra_body": map[string]any{"parameters": map[string]any{}},
	})
	var got struct {
		OK      bool   `json:"ok"`
		Status  int    `json:"status"`
		Body    string `json:"body"`
		Elapsed int64  `json:"elapsed_ms"`
	}
	if err := decodeJSONBody(h.do(http.MethodPost, "/api/config/probe", string(payload)), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Status != 200 || !strings.Contains(got.Body, "pong") || got.Elapsed < 0 {
		t.Fatalf("probe result: %+v", got)
	}
	if gotReq.Path != "/compatible-mode/v1/chat/completions" {
		t.Fatalf("url join: %s", gotReq.Path)
	}
	if gotReq.Auth != "Bearer sk-from-env" {
		t.Fatalf("auth resolved from key_env: %q", gotReq.Auth)
	}
	if gotReq.Headers["X-Dashscope-Sse"] != "enable" {
		t.Fatalf("custom header missing: %v", gotReq.Headers)
	}
	if _, ok := gotReq.Body["parameters"]; !ok {
		t.Fatalf("extra_body not merged: %v", gotReq.Body)
	}

	// A base_url that is not an absolute http(s) URL is refused outright.
	res := h.do(http.MethodPost, "/api/config/probe", `{"base_url":"not a url"}`)
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("garbage base_url accepted: %d", res.StatusCode)
	}

	// The conventional fallback: key_env empty, name kimi → KIMI_API_KEY (what
	// newHarness installs) rides the Authorization header instead of nothing.
	payload2, _ := json.Marshal(map[string]any{
		"name":     "kimi",
		"base_url": gw.URL,
		"model":    "k3",
	})
	if err := decodeJSONBody(h.do(http.MethodPost, "/api/config/probe", string(payload2)), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Status != 200 {
		t.Fatalf("conventional probe: %+v", got)
	}
	if gotReq.Auth != "Bearer test-key" {
		t.Fatalf("conventional key not resolved: %q", gotReq.Auth)
	}
}

// The probe used to send a server-resolved key to any host the request named —
// the endpoint launders the credential out to whoever drives it. Now an
// env-resolved key only rides to a host providers.json already names; a new
// host takes a key typed by the user.
func TestConfigProbeBindsEnvKeysToConfiguredHosts(t *testing.T) {
	t.Setenv("PROBE_API_KEY", "sk-from-env")
	prev := config.Providers()
	config.SetProvidersForTest(map[string]config.Provider{"kimi": {BaseURL: "https://api.kimi.example/v1"}})
	t.Cleanup(func() { config.SetProvidersForTest(prev) })

	h := newHarness(t, idleRespond)

	// Env-resolved key to a host the config does not name: refused before any
	// request leaves the process.
	res := h.do(http.MethodPost, "/api/config/probe",
		`{"base_url":"https://attacker.example/v1","key_env":"PROBE_API_KEY","model":"x"}`)
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("env key to an unconfigured host must be refused, got %d", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "providers.json") {
		t.Errorf("refusal does not say what bound the key: %s", body)
	}

	// The same host with the key typed into the dialog is the user handing it
	// over deliberately — the request is attempted (and fails here only
	// because the host does not exist, which is the probe's job to report).
	res2 := h.do(http.MethodPost, "/api/config/probe",
		`{"base_url":"https://attacker.example/v1","key":"sk-typed-by-user","model":"x"}`)
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("a typed key to any host must reach the probe itself, got %d", res2.StatusCode)
	}
}

// decodeJSONBody closes the response and decodes it, failing the test on a
// non-2xx status with the body as the message.
func decodeJSONBody(res *http.Response, out any) error {
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %d: %s", res.Request.Method, res.Request.URL.Path, res.StatusCode,
			bytes.TrimSpace(body))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

func TestStarterEditorRoundTrip(t *testing.T) {
	store := filepath.Join(t.TempDir(), "starters.json")
	h := newHarness(t, idleRespond, func(c *Config) {})
	// A second server on the same manager with the store configured: the
	// harness does not expose its ServerOptions.
	srv2, err := NewServer(h.mgr, ServerOptions{Token: h.token, StartersStore: store})
	if err != nil {
		t.Fatal(err)
	}
	ts2 := httptest.NewServer(srv2.Handler())
	t.Cleanup(ts2.Close)
	do2 := func(method, path, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, ts2.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+h.token)
		res, err := ts2.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	decode2 := func(res *http.Response, out any) {
		t.Helper()
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		if res.StatusCode >= 300 {
			t.Fatalf("%s: %d: %s", res.Request.URL.Path, res.StatusCode, data)
		}
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatal(err)
		}
	}

	// No override yet: skill defaults, custom flag absent.
	var got struct {
		Starters struct {
			Cards []struct {
				Title string `json:"title"`
			} `json:"cards"`
			Custom bool `json:"custom"`
		} `json:"starters"`
	}
	decode2(do2(http.MethodGet, "/api/starters", ""), &got)
	if got.Starters.Custom {
		t.Fatal("custom flag set with no override file")
	}

	// Save a user set; GET now serves it.
	set := `{"heading":"自定义开场","cards":[{"title":"新卡片","prompt":"hello"}]}`
	var saved struct {
		OK       bool     `json:"ok"`
		Cards    int      `json:"cards"`
		Warnings []string `json:"warnings"`
	}
	decode2(do2(http.MethodPut, "/api/starters", set), &saved)
	if !saved.OK || saved.Cards != 1 {
		t.Fatalf("save: %+v", saved)
	}
	decode2(do2(http.MethodGet, "/api/starters", ""), &got)
	if !got.Starters.Custom || len(got.Starters.Cards) != 1 || got.Starters.Cards[0].Title != "新卡片" {
		t.Fatalf("after save: %+v", got.Starters)
	}

	// A card with no action is refused, file unchanged.
	bad := `{"cards":[{"title":"空动作"}]}`
	res := do2(http.MethodPut, "/api/starters", bad)
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid set accepted: %d", res.StatusCode)
	}
	onDisk, _ := os.ReadFile(store)
	if !strings.Contains(string(onDisk), "新卡片") {
		t.Fatal("invalid save rewrote the store")
	}
}
