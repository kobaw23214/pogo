package web

// Multi-user isolation tests: the same full-featured server, driven by two
// users.json accounts (fixed tokens are the programmatic spelling of a
// password login), must give each user a private session space while the
// shared token channel stays closed.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/catalog"
	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/memory"
	"github.com/yosukeno/pogo/session"
)

// recordingClient captures the system prompts the agent sends, so a test can
// assert whose memory and workspace a run actually saw.
type recordingClient struct {
	inner llm.Client
	mu    sync.Mutex
	sys   []string
}

func (c *recordingClient) Model() string { return c.inner.Model() }

func (c *recordingClient) Stream(ctx context.Context, system string, history []llm.Message, tools []llm.ToolSchema, onDelta func(llm.Delta)) (llm.Response, error) {
	c.mu.Lock()
	c.sys = append(c.sys, system)
	c.mu.Unlock()
	return c.inner.Stream(ctx, system, history, tools, onDelta)
}

func (c *recordingClient) systems() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.sys...)
}

// --- the multi-user harness ---------------------------------------------------

type muHarness struct {
	t       *testing.T
	mgr     *Manager
	psrv    *Server
	srv     *httptest.Server
	token   string // the shared web token, which must stop working as a shared door
	rec     *recordingClient
	memRoot string // the server-wide memory root, which owned sessions must not see
}

// The fixed per-account tokens the harness's users carry. Long, opaque, the
// shape operators paste into users.json.
const (
	aliceToken = "atk-alice-fixed-32-chars-example00"
	bobToken   = "atk-bob-fixed-32-chars-example00000"
)

func newMultiuserHarness(t *testing.T, respond func(int) (llm.Response, error), tweaks ...func(*Config)) *muHarness {
	t.Helper()
	t.Setenv("KIMI_API_KEY", "test-key")
	installTestCatalog(t)

	cat, err := catalog.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })

	h := &muHarness{t: t, token: "shared-token"}
	// A server-wide memory store whose directory must never reach an owned
	// session's prompt: per-user memory replaces it, it does not add to it.
	h.memRoot = filepath.Join(t.TempDir(), "global-memory")
	if err := os.MkdirAll(filepath.Join(h.memRoot, "notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.memRoot, "notes", "secret.md"), []byte("global secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	globalMem, _ := memory.Load(memory.Options{Project: true, Cwd: h.memRoot})

	cfg := Config{
		Cwd:         t.TempDir(),
		SessionDir:  t.TempDir(),
		Catalog:     cat,
		Model:       "k3",
		MaxTurns:    5,
		GateTimeout: 5 * time.Second,
		RunTimeout:  time.Minute,
		IdleTimeout: time.Hour,
		Memory:      globalMem,
		NewClient: func(c config.Resolved, onRetry func(llm.RetryInfo)) llm.Client {
			inner := &scriptedClient{model: c.Model, respond: func() (llm.Response, error) {
				h.rec.mu.Lock()
				n := len(h.rec.sys) + 1
				h.rec.mu.Unlock()
				return respond(n)
			}}
			h.rec.inner = inner
			return h.rec
		},
	}
	h.rec = &recordingClient{}
	for _, tweak := range tweaks {
		tweak(&cfg)
	}
	mgr, err := NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.mgr = mgr
	t.Cleanup(mgr.Close)

	srv, err := NewServer(mgr, ServerOptions{Token: h.token})
	if err != nil {
		t.Fatal(err)
	}
	h.psrv = srv
	h.armUsers(map[string]userRecord{
		"alice": {Username: "alice", Token: aliceToken},
		"bob":   {Username: "bob", Token: bobToken},
	})
	h.srv = httptest.NewServer(srv.Handler())
	t.Cleanup(h.srv.Close)
	return h
}

// armUsers installs a users table on the harness's server Ã¢ÂÂ the in-memory
// half of what newAuthStore loads from users.json Ã¢ÂÂ so fixed-token
// resolution can be exercised without rebuilding the server.
func (h *muHarness) armUsers(users map[string]userRecord) {
	h.t.Helper()
	h.psrv.auth.mu.Lock()
	defer h.psrv.auth.mu.Unlock()
	h.psrv.auth.users = users
	h.psrv.auth.path = "test-users.json"
}

// doAs issues a request with one user's credential.
func (h *muHarness) doAs(bearer, method, path, body string) *http.Response {
	h.t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, h.srv.URL+path, r)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	return resp
}

func (h *muHarness) alice(t *testing.T) string { t.Helper(); return aliceToken }
func (h *muHarness) bob(t *testing.T) string   { t.Helper(); return bobToken }

func (h *muHarness) session(sid string) *Session {
	h.t.Helper()
	s, err := h.mgr.Get(sid)
	if err != nil {
		h.t.Fatal(err)
	}
	return s
}

func (h *muHarness) createAs(bearer string) string {
	h.t.Helper()
	resp := h.doAs(bearer, http.MethodPost, "/api/sessions", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(resp.Body)
		h.t.Fatalf("create session: %d (%s)", resp.StatusCode, msg)
	}
	var out struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		h.t.Fatal(err)
	}
	return out.SessionID
}

func (h *muHarness) listIDs(bearer string) map[string]bool {
	h.t.Helper()
	resp := h.doAs(bearer, http.MethodGet, "/api/sessions", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("list sessions: %d", resp.StatusCode)
	}
	var out struct {
		Sessions []struct {
			ID string `json:"id"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		h.t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, s := range out.Sessions {
		ids[s.ID] = true
	}
	return ids
}

// --- the tests ----------------------------------------------------------------

// The core property: two users of the same server see disjoint session sets,
// and each can only operate on their own.
func TestMultiuserSessionsAreIsolatedPerUser(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("ok")))
	alice, bob := h.alice(t), h.bob(t)

	aliceID := h.createAs(alice)
	bobID := h.createAs(bob)

	if ids := h.listIDs(alice); !ids[aliceID] || ids[bobID] {
		t.Fatalf("alice's list = %v, want only her own session", ids)
	}
	if ids := h.listIDs(bob); !ids[bobID] || ids[aliceID] {
		t.Fatalf("bob's list = %v, want only his own session", ids)
	}

	// Reading, patching and deleting across users is a 404, indistinguishable
	// from a missing id Ã¢ÂÂ the answer leaks nothing about the session's
	// existence.
	for _, check := range []struct {
		name, method, path string
		want               int
	}{
		{"get", http.MethodGet, "/api/sessions/" + aliceID, http.StatusNotFound},
		{"transcript", http.MethodGet, "/api/sessions/" + aliceID + "/transcript", http.StatusNotFound},
		{"export", http.MethodGet, "/api/sessions/" + aliceID + "/export", http.StatusNotFound},
		{"patch", http.MethodPatch, "/api/sessions/" + aliceID, http.StatusNotFound},
		{"delete", http.MethodDelete, "/api/sessions/" + aliceID, http.StatusNotFound},
		{"messages", http.MethodPost, "/api/sessions/" + aliceID + "/messages", http.StatusNotFound},
	} {
		resp := h.doAs(bob, check.method, check.path, `{"prompt":"hi","title":"x"}`)
		resp.Body.Close()
		if resp.StatusCode != check.want {
			t.Fatalf("bob %s alice's session: %d, want %d", check.name, resp.StatusCode, check.want)
		}
	}

	// Alice's own access still works, and after she deletes it, it is gone for
	// her too Ã¢ÂÂ the tombstone ended ownership.
	if resp := h.doAs(alice, http.MethodGet, "/api/sessions/"+aliceID, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("alice get own: %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	if resp := h.doAs(alice, http.MethodDelete, "/api/sessions/"+aliceID, ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("alice delete own: %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	if ids := h.listIDs(alice); ids[aliceID] {
		t.Fatal("alice still lists her deleted session")
	}
}

// The token channel in multi-user mode: unknown values stay 401, and the
// deployment's shared token maps to the default admin account Ã¢ÂÂ
// authenticated AND holding the admin grant.
func TestMultiuserTokenChannels(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("ok")))

	resp := h.doAs("not-a-token", http.MethodGet, "/api/sessions", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("garbage bearer: %d, want 401", resp.StatusCode)
	}
	if resp := h.doAs(h.alice(t), http.MethodGet, "/api/sessions", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("valid credential: %d, want 200", resp.StatusCode)
	} else {
		resp.Body.Close()
	}

	// The shared env token: admin identity, admin grant. ?token= spelling too.
	if resp := h.doAs(h.token, http.MethodGet, "/api/sessions", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("admin-bound shared token: %d, want 200", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	req, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/api/config?token="+h.token, nil)
	resp2, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("admin token via ?token= on an operator endpoint: %d, want 200", resp2.StatusCode)
	}
}

// Operator surfaces demand the admin grant; a plain account is 403 before the
// handler runs.
func TestMultiuserAdminGateOnOperatorEndpoints(t *testing.T) {
	scheduleEnv(t)
	h := newMultiuserHarness(t, scriptedTurns(textTurn("ok")))
	alice := h.alice(t)
	admin := h.token // the shared token binds to the default admin account

	for _, check := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/config", ""},
		{http.MethodPut, "/api/config", "{}"},
		{http.MethodGet, "/api/mcp", ""},
		{http.MethodPost, "/api/skills/upload", ""},
		{http.MethodPut, "/api/sandbox", "{}"},
	} {
		resp := h.doAs(alice, check.method, check.path, check.body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("user without admin: %s %s: %d, want 403", check.method, check.path, resp.StatusCode)
		}
		// The admin grant reaches the handler; anything but the gate's 403
		// proves it did (the specific status is the handler's business).
		resp = h.doAs(admin, check.method, check.path, check.body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusForbidden {
			t.Fatalf("admin credential: %s %s: still 403", check.method, check.path)
		}
	}

	// Conversation-facing reads stay open to every authenticated user.
	// Scheduled-Agent jobs are an account-scoped conversation capability.
	// Cross-owner visibility and legacy-global access are checked by the
	// schedule binding safety tests rather than this operator-only gate.
	for _, path := range []string{"/api/models", "/api/skills", "/api/tools", "/api/attachments/policy", "/api/schedule/jobs"} {
		resp := h.doAs(alice, http.MethodGet, path, "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("plain user %s: %d, want 200", path, resp.StatusCode)
		}
	}
}

// A run must see the owner's workspace and memory: the system prompt carries
// the owner's memory directory, never another user's and never the server's
// shared one.
func TestMultiuserRunSeesOwnersMemoryAndWorkspace(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("ok")))
	alice := h.alice(t)

	// A note in alice's memory directory, planted before her first session
	// loads it: an empty memory store contributes nothing to a prompt, so the
	// assertion needs a directory with something in it.
	aliceMem := filepath.Join(h.mgr.UserRoot(identity.LocalScope("alice")), ".pogo", "memory")
	if err := os.MkdirAll(aliceMem, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(aliceMem, "note.md"), []byte("alice note"), 0o600); err != nil {
		t.Fatal(err)
	}

	aliceID := h.createAs(alice)
	h.doAs(alice, http.MethodPost, "/api/sessions/"+aliceID+"/messages", `{"prompt":"hello"}`).Body.Close()

	deadline := time.Now().Add(10 * time.Second)
	for len(h.rec.systems()) == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	systems := h.rec.systems()
	if len(systems) == 0 {
		t.Fatal("the run never reached the model")
	}

	// The roots come from real sessions: a default workspace IS its owner's
	// root, so no test re-implements the subject derivation.
	sessRoot := func(sid string) string {
		s := h.session(sid)
		s.mu.Lock()
		cwd := s.cwd
		s.mu.Unlock()
		return cwd
	}
	aliceRoot := sessRoot(aliceID)
	bobRoot := sessRoot(h.createAs(h.bob(t)))
	if aliceRoot == bobRoot || filepath.Base(filepath.Dir(aliceRoot)) != "users" {
		t.Fatalf("roots %q / %q are not distinct per-user roots", aliceRoot, bobRoot)
	}
	if !strings.Contains(systems[0], aliceMem) {
		t.Fatalf("system prompt does not carry alice's memory root %s", aliceMem)
	}
	if strings.Contains(systems[0], h.memRoot) {
		t.Fatal("system prompt carries the server-wide memory root")
	}
	if strings.Contains(systems[0], bobRoot) {
		t.Fatal("system prompt carries another user's memory root")
	}

	// A workspace request cannot escape the owner's root.
	if _, err := h.mgr.resolveWorkspaceUnder(aliceRoot, relBetweenRoots(aliceRoot, bobRoot)); err == nil {
		t.Fatal("workspace resolution escaped alice's root into another user's")
	}
}

// The file panel resolves against the caller's root: alice's marker file is
// visible to her and not to bob.
func TestMultiuserFilesPanelIsScopedToTheUserRoot(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("ok")))
	alice, bob := h.alice(t), h.bob(t)

	aliceRoot := func() string {
		s := h.session(h.createAs(alice))
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.cwd
	}()
	if err := os.WriteFile(filepath.Join(aliceRoot, "alices-file.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp := h.doAs(alice, http.MethodGet, "/api/files?path=.", "")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "alices-file.txt") {
		t.Fatalf("alice's panel: %d %s", resp.StatusCode, body)
	}
	resp = h.doAs(bob, http.MethodGet, "/api/files?path=.", "")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || strings.Contains(string(body), "alices-file.txt") {
		t.Fatalf("bob's panel must not see alice's file: %d %s", resp.StatusCode, body)
	}
	// And a direct path into alice's tree is not reachable through the panel's
	// root-relative resolution.
	resp = h.doAs(bob, http.MethodGet, "/api/files?path=../users", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bob escaping root: %d, want 400/404", resp.StatusCode)
	}
}

// Sessions without a catalog row Ã¢ÂÂ a CLI-era transcript, or a file copied in Ã¢ÂÂ
// are invisible in multi-user mode. Ownership is never inferred.
func TestMultiuserUnownedSessionsAreInvisible(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("ok")))

	if _, err := session.Create(h.mgr.cfg.SessionDir, h.mgr.cfg.Cwd, "k3"); err != nil {
		t.Fatal(err)
	}
	ids := h.listIDs(h.alice(t))
	if len(ids) != 0 {
		t.Fatalf("unowned session leaked into the list: %v", ids)
	}
}

// Local subjects never collide: same names, different accounts, and a local
// account stays distinct from any differently derived subject.
func TestLocalSubjectsAreDistinct(t *testing.T) {
	if identity.LocalScope("alice").SubjectID == identity.LocalScope("bob").SubjectID {
		t.Fatal("two local users share a subject id")
	}
	// A differently derived "alice" (another app/tenant axis) cannot collide
	// with the local alice of the same name.
	otherAlice := identity.DeriveSubject("other-app", "local", "local", "alice")
	if identity.LocalScope("alice").SubjectID == otherAlice {
		t.Fatal("local alice collides with a differently derived alice")
	}
	if !identity.LocalScope("alice").Valid() {
		t.Fatal("a local scope must be a valid ownership scope")
	}
}

// relBetweenRoots turns two absolute roots into the relative path from the
// first to the second ("../<key>"), the escape a hostile workspace request
// would spell.
func relBetweenRoots(from, to string) string {
	rel, err := filepath.Rel(filepath.Dir(from), to)
	if err != nil {
		return "../../" + filepath.Base(to)
	}
	return rel
}

// Fixed per-user tokens (users.json "token"): no expiry, bound to one
// account, entering exactly that account's session space Ã¢ÂÂ the simple
// integration shape for scripts and external systems.
func TestMultiuserFixedTokensBindToAccounts(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("ok")))

	// Bearer spelling and ?token= spelling both resolve to the account.
	sid := h.createAs(aliceToken)
	if ids := h.listIDs(aliceToken); !ids[sid] {
		t.Fatalf("alice's fixed token does not list her session: %v", ids)
	}
	req, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/api/sessions?token="+bobToken, nil)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bob via ?token=: %d, want 200", resp.StatusCode)
	}
	var out struct {
		Sessions []struct {
			ID string `json:"id"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	for _, s := range out.Sessions {
		if s.ID == sid {
			t.Fatal("bob's fixed token sees alice's session")
		}
	}

	// A fixed token is NOT the admin grant: operator endpoints stay 403.
	if resp := h.doAs(aliceToken, http.MethodGet, "/api/config", ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("fixed token on operator endpoint: %d, want 403", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
}

// The files panel sends session-cwd-relative paths ("users/<key>/Ã¢ÂÂ¦") because
// that is what the workspace field carries Ã¢ÂÂ computed against the SERVER
// root. requestRoot for an owned scope is already the user root, so Resolve
// must strip the scope root's own prefix or the path doubles and every panel
// listing 404s (the exact bug seen live: stat /home/x/users/k/users/k).
func TestMultiuserFilesPanelPathIsNotDoubled(t *testing.T) {
	h := newMultiuserHarness(t, nil)
	bearer := h.alice(t)

	probe := h.doAs(bearer, "GET", "/api/files", "")
	if probe.StatusCode != 200 {
		t.Fatalf("root listing: %d", probe.StatusCode)
	}
	// The user root exists (UserRoot mkdirs); its cwd-relative form is what
	// the panel would send. Find it by listing the server root... which is
	// not visible per-scope, so derive from an owned session's cwd instead.
	sid := h.createAs(bearer)
	cwd := h.session(sid).cwd
	// cwd is absolute; the panel's wire form is it relativized to the server
	// cwd. Reconstruct both spellings and exercise the strip.
	rel := strings.TrimPrefix(filepath.ToSlash(cwd), filepath.ToSlash(h.mgr.Cwd())+"/")
	for _, tc := range []struct{ name, path string }{
		{"root itself", rel},
		{"with trailing slash", rel + "/"},
		{"subdir under it", rel + "/sub"},
		{"plain relative untouched", "sub/inner"},
	} {
		resp := h.doAs(bearer, "GET", "/api/files?path="+url.QueryEscape(tc.path), "")
		// "sub/inner" does not exist under the user root: the assertion is
		// NOT-doubling, so a 404 is fine there Ã¢ÂÂ what must never happen is a
		// 404 whose stat path contains the key twice.
		body, _ := io.ReadAll(resp.Body)
		if strings.Count(string(body), rel) > 1 {
			t.Fatalf("%s: doubled prefix in %s", tc.name, string(body))
		}
		if tc.path == rel || tc.path == rel+"/" {
			if resp.StatusCode != 200 {
				t.Fatalf("%s: expected the user root to list, got %d: %s", tc.name, resp.StatusCode, string(body))
			}
		}
	}
}

// 每个 Agent 的默认工作区按 (用户, Agent) 分片：alice 与 bob 对同一个
// Agent 各得一棵 agents/<id> 树；清单端点回报的 cwd 是请求者自己的根，
// 侧栏据此算出的相对路径不会再拼出 "users/<key>/users/<key>/…"。
func TestStudioAgentWorkspaceIsPerUserPerAgent(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("ok")))

	resp := h.doAs(h.token, http.MethodPost, "/api/manage/agents", studioBody(studioAgentBody("Desk Bot", "per-user desk.", "ready")))
	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("create agent: %d (%s)", resp.StatusCode, msg)
	}
	var a struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()

	want := map[string]struct{ root, agentDir string }{
		h.alice(t): {h.mgr.UserRoot(identity.LocalScope("alice")), filepath.Join(h.mgr.UserRoot(identity.LocalScope("alice")), "agents", a.ID)},
		h.bob(t):   {h.mgr.UserRoot(identity.LocalScope("bob")), filepath.Join(h.mgr.UserRoot(identity.LocalScope("bob")), "agents", a.ID)},
	}
	for bearer, w := range want {
		sid := h.studioConversationAs(bearer, a.ID)
		if cwd, _ := sessionCwdAndModel(h.session(sid)); cwd != w.agentDir {
			t.Fatalf("session cwd = %q, want the caller's own %q", cwd, w.agentDir)
		}
		if _, err := os.Stat(w.agentDir); err != nil {
			t.Fatalf("per-user agent workspace not created: %v", err)
		}

		list := h.doAs(bearer, http.MethodGet, "/api/apps/"+a.ID+"/sessions", "")
		var out struct {
			Cwd string `json:"cwd"`
		}
		if err := json.NewDecoder(list.Body).Decode(&out); err != nil {
			list.Body.Close()
			t.Fatal(err)
		}
		list.Body.Close()
		if out.Cwd != w.root {
			t.Fatalf("list cwd = %q, want the caller's root %q", out.Cwd, w.root)
		}
	}
}

// studioConversationAs opens a conversation with an Agent under one bearer.
func (h *muHarness) studioConversationAs(bearer, agentID string) string {
	h.t.Helper()
	resp := h.doAs(bearer, http.MethodPost, "/api/apps/"+agentID+"/sessions", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(resp.Body)
		h.t.Fatalf("create conversation: %d (%s)", resp.StatusCode, msg)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		h.t.Fatal(err)
	}
	return out.ID
}
