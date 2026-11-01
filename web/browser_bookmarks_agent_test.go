package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func bookmarkAgentFixture(t *testing.T, h *muHarness, name, status string) studioAgent {
	t.Helper()
	a, err := h.mgr.studio.put("", studioInput{Name: name, Model: "k3", Status: status}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestAgentBookmarksAPIIsolatesOwnerAgentAndLegacyImport(t *testing.T) {
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	h.armUsers(map[string]userRecord{"alice": {Username: "alice", Token: aliceToken, Admin: true}, "bob": {Username: "bob", Token: bobToken}})
	a, b := bookmarkAgentFixture(t, h, "A", "ready"), bookmarkAgentFixture(t, h, "B", "ready")
	path := func(aid string) string {
		p := "/api/browser/bookmarks"
		if aid != "" {
			p += "?agent_id=" + aid
		}
		return p
	}
	read := func(token, aid string) browserBookmarkView {
		t.Helper()
		r := h.doAs(token, "GET", path(aid), "")
		defer r.Body.Close()
		var v browserBookmarkView
		if r.StatusCode != 200 || json.NewDecoder(r.Body).Decode(&v) != nil {
			t.Fatalf("read %s status=%d", aid, r.StatusCode)
		}
		return v
	}
	save := func(token, aid string, v browserBookmarkView, entries []browserBookmark) *http.Response {
		return h.doAs(token, "PUT", path(aid), studioBody(map[string]any{"account_id": v.AccountID, "revision": v.Revision, "bookmarks": entries}))
	}
	legacy := read(aliceToken, "")
	entry := []browserBookmark{{ID: "old", Title: "Legacy address", URL: "https://legacy.example/"}}
	r := save(aliceToken, "", legacy, entry)
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatal(r.Status)
	}
	alice := identity.LocalScope("alice")
	oldPath, _ := browserBookmarkPath(h.mgr.cfg.SessionDir, &alice)
	before, _ := os.ReadFile(oldPath)
	va, vb, bob := read(aliceToken, a.ID), read(aliceToken, b.ID), read(bobToken, a.ID)
	if va.Scope != "agent" || va.AgentID != a.ID || va.ScopeID == "" || va.ScopeID != va.AccountID || va.ScopeID == vb.ScopeID || va.ScopeID == bob.ScopeID || va.ScopeID == legacy.AccountID || va.Total != 0 || vb.Total != 0 || va.OwnerAccountID != legacy.AccountID || legacy.OwnerAccountID != legacy.AccountID || bob.OwnerAccountID == legacy.OwnerAccountID {
		t.Fatal("Agent scope fell back to account or another owner/Agent")
	}
	// Import is an explicit caller write after reading the old account library.
	// Merely visiting two Agents above created no copies or revisions.
	r = save(aliceToken, a.ID, va, read(aliceToken, "").Bookmarks)
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatal(r.Status)
	}
	imported := read(aliceToken, a.ID)
	if imported.Revision != 1 || imported.Total != 1 || read(aliceToken, b.ID).Total != 0 || read(bobToken, a.ID).Total != 0 {
		t.Fatal("import affected another scope")
	}
	after, _ := os.ReadFile(oldPath)
	if string(before) != string(after) {
		t.Fatal("Agent import rewrote legacy account data")
	}
	for _, tc := range []struct {
		token, aid string
		view       browserBookmarkView
	}{{aliceToken, b.ID, imported}, {bobToken, a.ID, imported}, {aliceToken, a.ID, va}} {
		r = save(tc.token, tc.aid, tc.view, []browserBookmark{})
		raw, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != 409 {
			t.Fatalf("wrong scope or stale CAS accepted: %d", r.StatusCode)
		}
		if tc.token == bobToken && strings.Contains(string(raw), "Legacy address") {
			t.Fatal("conflict response leaked another owner's data")
		}
	}
	restart := &Manager{cfg: h.mgr.cfg, studio: h.mgr.studio}
	restored, err := restart.browserBookmarks(&alice, "", a.ID)
	if err != nil || restored.Revision != 1 || restored.AgentID != a.ID || len(restored.Bookmarks) != 1 {
		t.Fatal("Agent data did not survive manager restart")
	}
	draft := bookmarkAgentFixture(t, h, "Draft", "draft")
	for _, tc := range []struct {
		token, query string
		want         int
	}{
		{aliceToken, "?agent_id=", 400}, {aliceToken, "?agent_id=" + a.ID + "&agent_id=" + b.ID, 400}, {aliceToken, "?agent_id=../../account", 400}, {aliceToken, "?agent_id=%zz", 400},
		{aliceToken, "?agent_id=ag_" + strings.Repeat("f", 32), 404}, {bobToken, "?agent_id=" + draft.ID, 404}, {aliceToken, "?agent_id=" + draft.ID, 200},
	} {
		r = h.doAs(tc.token, "GET", "/api/browser/bookmarks"+tc.query, "")
		r.Body.Close()
		if r.StatusCode != tc.want {
			t.Fatalf("scope query %s =%d want%d", tc.query, r.StatusCode, tc.want)
		}
	}
	if err := h.mgr.studio.remove(a.ID); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "PUT"} {
		r = h.doAs(aliceToken, method, path(a.ID), studioBody(map[string]any{"account_id": imported.AccountID, "revision": 1, "bookmarks": []browserBookmark{}}))
		r.Body.Close()
		if r.StatusCode != 404 {
			t.Fatalf("deleted Agent %s=%d", method, r.StatusCode)
		}
	}
	if _, err := h.mgr.browserBookmarks(&alice, "", a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("trusted metadata read bypassed deleted Agent fence")
	}
}

func TestAgentBookmarkToolUsesTrustedBindingAndSubagentScope(t *testing.T) {
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { return plainTurn() }, func(c *Config) { c.BrowserURL, c.BrowserToken = "http://127.0.0.1:9", "unused" })
	a, b := bookmarkAgentFixture(t, h, "A", "ready"), bookmarkAgentFixture(t, h, "B", "ready")
	alice, bob := identity.LocalScope("alice"), identity.LocalScope("bob")
	for _, row := range []struct {
		owner        *identity.Scope
		agent, title string
	}{{&alice, "", "LEGACY"}, {&alice, a.ID, "ALICE-A"}, {&alice, b.ID, "ALICE-B"}, {&bob, a.ID, "BOB-A"}} {
		_, err := h.mgr.replaceBrowserBookmarks(row.owner, 0, []browserBookmark{{ID: "one", Title: row.title, URL: "https://saved.example/"}}, row.agent)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct{ token, aid, title string }{{aliceToken, a.ID, "ALICE-A"}, {aliceToken, b.ID, "ALICE-B"}, {bobToken, a.ID, "BOB-A"}} {
		sess := h.session(h.studioConversationAs(row.token, row.aid))
		if sess.browser.bookmarks == nil {
			t.Fatal("Agent bookmark metadata unavailable")
		}
		for _, link := range []tools.BrowserLink{sess.browser, subPane{sess.browser}} {
			reply, err := link.Execute(context.Background(), json.RawMessage(`{"action":"bookmarks"}`))
			var view browserBookmarkView
			if err != nil || json.Unmarshal(reply.Data, &view) != nil || view.Scope != "agent" || view.AgentID != row.aid || view.Total != 1 || view.Bookmarks[0].Title != row.title || reply.URL != "" || sess.browser.Armed() {
				t.Fatalf("tool read wrong trusted scope %+v %v", view, err)
			}
			if _, err := link.Execute(context.Background(), json.RawMessage(`{"action":"bookmarks","agent_id":"`+b.ID+`"}`)); err == nil {
				t.Fatal("model-controlled Agent override accepted")
			}
		}
	}
	sess := h.session(h.studioConversationAs(aliceToken, a.ID))
	if err := h.mgr.studio.remove(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.browser.Execute(context.Background(), json.RawMessage(`{"action":"bookmarks"}`)); !errors.Is(err, ErrNotFound) {
		t.Fatal("existing conversation fell back after Agent deletion")
	}
}

func TestAgentBookmarkCASSerializesWithinAgentOnly(t *testing.T) {
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	a := bookmarkAgentFixture(t, h, "A", "ready")
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, name := range []string{"one", "two"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			_, err := h.mgr.replaceBrowserBookmarks(nil, 0, []browserBookmark{{ID: name, Title: name, URL: "https://saved.example/"}}, a.ID)
			results <- err
		}(name)
	}
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for err := range results {
		if err == nil {
			wins++
		} else if errors.Is(err, errBrowserBookmarksConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("wins%d conflicts%d", wins, conflicts)
	}
}
