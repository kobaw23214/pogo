package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skills"
)

func TestBrowserDesktopOwnerConfiguration(t *testing.T) {
	for _, name := range []string{" ", "alice ", "*", "../alice", "ali\nce", strings.Repeat("a", 65), string([]byte{0xff})} {
		if _, err := NewManager(Config{BrowserURL: "http://127.0.0.1:9", BrowserToken: "test", BrowserDesktopOwner: name}); err == nil || !strings.Contains(err.Error(), "POGO_BROWSER_DESKTOP_OWNER") {
			t.Fatalf("invalid owner accepted or wrong error: %q %v", name, err)
		}
	}
	if _, err := NewManager(Config{BrowserDesktopOwner: "alice"}); err == nil || !strings.Contains(err.Error(), "requires POGO_BROWSER_URL") {
		t.Fatalf("owner without worker must fail startup: %v", err)
	}
}

func TestBrowserDesktopOwnerRequiresCompleteScope(t *testing.T) {
	alice := identity.LocalScope("alice")
	m := &Manager{cfg: Config{browserDesktopOwner: &alice}}
	if !m.browserAllowed(&alice) || m.browserAllowed(nil) {
		t.Fatal("exact owner must be accepted and missing owner rejected")
	}
	for _, change := range []func(*identity.Scope){
		func(s *identity.Scope) { s.SubjectID = identity.LocalScope("bob").SubjectID },
		func(s *identity.Scope) { s.AppID = "other" },
		func(s *identity.Scope) { s.TenantID = "other" },
		func(s *identity.Scope) { s.ProjectID = "other" },
	} {
		other := alice
		change(&other)
		if m.browserAllowed(&other) {
			t.Fatalf("partial matching scope was authorized: %+v", other)
		}
	}
	m.cfg.browserDesktopOwner = nil
	if !m.browserAllowed(nil) || !m.browserAllowed(&alice) {
		t.Fatal("headless deployment without owner binding must remain compatible")
	}
}

func TestBrowserDesktopOwnerMultiuserAPIsAndCapabilities(t *testing.T) {
	var calls atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if strings.HasSuffix(r.URL.Path, "/state") {
			writeJSON(w, http.StatusOK, map[string]any{"display_mode": "desktop", "control": "agent", "tabs": []any{}, "device_owner_bound": false})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
	t.Cleanup(worker.Close)
	sk := writeTestSkill(t, t.TempDir(), "browse-device", "Browser task", "# Browse")
	sk.BrowserTask = true
	cap := &studioCapture{}
	h := newMultiuserHarness(t, scriptedTurns(textTurn("ok")), func(c *Config) {
		c.BrowserURL, c.BrowserToken, c.BrowserDesktopOwner = worker.URL, "test-worker-token", "alice"
		c.Skills = append([]skills.Skill{sk}, bindingTestSkills(t)...)
		c.NewClient = func(rc config.Resolved, _ func(llm.RetryInfo)) llm.Client {
			return &studioClient{cap: cap, next: &scriptedClient{model: rc.Model, respond: func() (llm.Response, error) { return textTurn("ok"), nil }}}
		}
	})
	aliceID, bobID := h.createAs(aliceToken), h.createAs(bobToken)
	if h.session(aliceID).nativeBrowser == nil || !h.session(aliceID).browserToolEnabled() {
		t.Fatal("bound owner lost native executor or browser capability")
	}
	assertDeniedSession := func(s *Session) {
		t.Helper()
		if s.nativeBrowser != nil || s.browser.executor != nil || s.browserToolEnabled() || browserLinkIf(s.browserToolEnabled(), s.browser) != nil || subPaneIf(s.browserToolEnabled(), s.browser) != nil {
			t.Fatal("unbound account retained browser executor/tool/child loan")
		}
	}
	assertDeniedSession(h.session(bobID))
	// A shared token resolves to bootstrap admin, not to the configured owner.
	assertDeniedSession(h.session(h.createAs(h.token)))
	read := func(res *http.Response) string {
		t.Helper()
		defer res.Body.Close()
		raw, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		res := h.doAs(bobToken, method, "/api/sessions/"+bobID+"/browser", "")
		raw := read(res)
		if res.StatusCode != http.StatusOK || !strings.Contains(raw, `"backend":"unavailable"`) || !strings.Contains(raw, "BROWSER_DEVICE_NOT_BOUND") || strings.Contains(raw, "alice") {
			t.Fatalf("unbound state: %d %s", res.StatusCode, raw)
		}
	}
	for _, endpoint := range []string{"frame", "control", "input", "pick", "tabs", "auth"} {
		method := http.MethodPost
		if endpoint == "frame" {
			method = http.MethodGet
		}
		res := h.doAs(bobToken, method, "/api/sessions/"+bobID+"/browser/"+endpoint, "{}")
		raw := read(res)
		if res.StatusCode != http.StatusForbidden || !strings.Contains(raw, "BROWSER_DEVICE_NOT_BOUND") {
			t.Fatalf("unbound %s: %d %s", endpoint, res.StatusCode, raw)
		}
	}
	res := h.doAs(bobToken, http.MethodPost, "/api/sessions/"+bobID+"/messages", `{"prompt":"/skill:browse-device inspect the page"}`)
	if raw := read(res); res.StatusCode != http.StatusForbidden || !strings.Contains(raw, "BROWSER_DEVICE_NOT_BOUND") || h.session(bobID).Browser().Armed() {
		t.Fatalf("browser skill armed an unbound account: %d %s", res.StatusCode, raw)
	}
	res = h.doAs(bobToken, http.MethodGet, "/api/skills?session_id="+bobID, "")
	if raw := read(res); strings.Contains(raw, "browse-device") {
		t.Fatalf("unbound skill catalog advertises browser task: %s", raw)
	}
	if calls.Load() != 0 {
		t.Fatalf("denied requests contacted worker %d times", calls.Load())
	}
	res = h.doAs(aliceToken, http.MethodGet, "/api/sessions/"+aliceID+"/browser", "")
	if raw := read(res); res.StatusCode != http.StatusOK || !strings.Contains(raw, `"device_owner_bound":true`) || !strings.Contains(raw, `"backend":"native"`) {
		t.Fatalf("owner state did not carry server authorization fact: %d %s", res.StatusCode, raw)
	}
	for i, pair := range [][2]string{{aliceToken, aliceID}, {bobToken, bobID}} {
		res := h.doAs(pair[0], http.MethodPost, "/api/sessions/"+pair[1]+"/messages", `{"prompt":"hello"}`)
		if raw := read(res); res.StatusCode != http.StatusAccepted {
			t.Fatalf("normal chat failed: %d %s", res.StatusCode, raw)
		}
		deadline := time.Now().Add(5 * time.Second)
		for len(cap.toolSchemas()) <= i && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		got := cap.toolSchemas()
		if len(got) <= i || slices.Contains(got[i], "browser") != (i == 0) {
			t.Fatalf("actual model browser capability did not follow owner: %v", got)
		}
	}
	browserSkillID := bindingSkillID(t, h.mgr.Skills(), "browser-use")
	res = h.doAs(h.token, http.MethodPost, "/api/manage/agents", studioBody(studioAgentCapsBody("Owner Gate Bot", "ready", map[string]any{
		"skills": []string{browserSkillID}, "tools": []string{"browser"},
		"skill_tool_bindings": []string{"browser-use"},
	})))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create fixture agent: %s", read(res))
	}
	var app studioAgent
	if err := json.Unmarshal([]byte(read(res)), &app); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{aliceToken, "true"}, {bobToken, "false"}} {
		sid := h.studioConversationAs(pair[0], app.ID)
		res := h.doAs(pair[0], http.MethodGet, "/api/apps/"+app.ID+"/sessions/"+sid+"/capabilities", "")
		var out struct {
			Tools  []string `json:"tools"`
			Skills []string `json:"skills"`
		}
		if err := json.Unmarshal([]byte(read(res)), &out); err != nil {
			t.Fatal(err)
		}
		want := pair[1] == "true"
		if slices.Contains(out.Tools, "browser") != want || slices.Contains(out.Skills, browserSkillID) != want {
			t.Fatalf("session capability API disagrees with owner: %+v", out)
		}
	}
	// Reloading a persisted session recomputes the binding, never revives an
	// executor from an old transcript or another user's browser profile.
	h.mgr.evictAll()
	assertDeniedSession(h.session(bobID))
}

func TestBrowserDesktopOwnerSharedTokenRetainsBootstrapIdentity(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("ok")), func(c *Config) {
		c.BrowserURL, c.BrowserToken, c.BrowserDesktopOwner = "http://127.0.0.1:9", "test", bootstrapUser
	})
	s := h.session(h.createAs(h.token))
	admin := identity.LocalScope(bootstrapUser)
	if s.owner == nil || *s.owner != admin || s.nativeBrowser == nil || !s.browserToolEnabled() {
		t.Fatal("shared web token did not retain the bootstrap account's explicit device binding")
	}
}
