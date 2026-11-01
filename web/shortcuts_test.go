package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yosukeno/pogo/identity"
)

func TestUserShortcutsPrivateScopeDurabilityAndCAS(t *testing.T) {
	m := bookmarkManager(t)
	alice, bob := identity.LocalScope("alice"), identity.LocalScope("bob")
	first, err := m.userShortcuts(&alice, "ag_first")
	if err != nil || first.ScopeID == "" || first.Revision != 0 || first.Shortcuts == nil {
		t.Fatal("new private scope must be an explicit empty list")
	}
	body := "host=example.invalid\nuser=reader\npassword=synthetic secret\n"
	saved, err := m.replaceUserShortcuts(&alice, "ag_first", 0, []userShortcut{{ID: "db", Title: "  数据库  ", Content: body}})
	if err != nil || saved.Revision != 1 || saved.Shortcuts[0].Title != "数据库" || saved.Shortcuts[0].Content != body || saved.Shortcuts[0].Icon != "" {
		t.Fatal("body whitespace or optional icon changed")
	}
	otherProject := alice
	otherProject.ProjectID = "another-project"
	for _, scope := range []struct {
		owner *identity.Scope
		agent string
	}{{&alice, "ag_second"}, {&alice, ""}, {&bob, "ag_first"}, {&otherProject, "ag_first"}, {nil, "ag_first"}} {
		view, err := m.userShortcuts(scope.owner, scope.agent)
		if err != nil || view.ScopeID == first.ScopeID || view.Revision != 0 || len(view.Shortcuts) != 0 {
			t.Fatal("private library crossed an ownership or Agent boundary")
		}
	}
	path, _ := shortcutPath(m.cfg.SessionDir, &alice, "ag_first")
	for file, mode := range map[string]os.FileMode{path: 0600, filepath.Dir(path): 0700} {
		info, err := os.Stat(file)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatal("private runtime permissions are not restrictive")
		}
	}
	reloaded := &Manager{cfg: m.cfg}
	view, err := reloaded.userShortcuts(&alice, "ag_first")
	if err != nil || view.Revision != 1 || view.Shortcuts[0].Content != body {
		t.Fatal("saved private content did not survive reload")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{"first", "second"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, err := m.replaceUserShortcuts(&alice, "ag_first", 1, []userShortcut{{ID: id, Title: id, Content: id}})
			results <- err
		}(id)
	}
	wg.Wait()
	close(results)
	ok, conflicts := 0, 0
	for err := range results {
		if err == nil {
			ok++
		} else if errors.Is(err, errShortcutsConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if ok != 1 || conflicts != 1 {
		t.Fatal("concurrent windows overwrote the same revision")
	}
}

func TestUserShortcutValidationAndCorruptionDoNotOverwriteOrEchoSecrets(t *testing.T) {
	good := userShortcut{ID: "db", Title: "数据库", Content: "synthetic private body"}
	for _, entries := range [][]userShortcut{nil, {good, good}, {{ID: "../escape", Title: "Title", Content: "text"}}, {{ID: "db", Title: "Title", Content: ""}}, {{ID: "db", Title: "Title", Content: strings.Repeat("x", 64<<10+1)}}, {{ID: "db", Title: "Title", Content: "text", Icon: "https://track.invalid/icon"}}, make([]userShortcut, 257)} {
		if _, err := validateUserShortcuts(entries); err == nil {
			t.Fatal("malformed user library accepted")
		}
	}
	m := bookmarkManager(t)
	if _, err := m.userShortcuts(&identity.Scope{}, ""); err == nil {
		t.Fatal("invalid identity fell back to single-user ownership")
	}
	if _, err := m.replaceUserShortcuts(nil, "", 0, []userShortcut{good}); err != nil {
		t.Fatal(err)
	}
	path, _ := shortcutPath(m.cfg.SessionDir, nil, "")
	bad := []byte(`{"version":1,"revision":1,"shortcuts":"synthetic-secret-broken-file"}`)
	if err := os.WriteFile(path, bad, 0600); err != nil {
		t.Fatal(err)
	}
	for _, op := range []func() error{func() error { _, err := m.userShortcuts(nil, ""); return err }, func() error { _, err := m.replaceUserShortcuts(nil, "", 1, []userShortcut{}); return err }} {
		if err := op(); err == nil || strings.Contains(err.Error(), "synthetic-secret") {
			t.Fatal("corrupt private file error was missing or leaked content")
		}
	}
	current, _ := os.ReadFile(path)
	if string(current) != string(bad) {
		t.Fatal("corrupt library silently overwritten")
	}
}

func TestUserShortcutsAPIIsolationAgentVisibilityCASAndStrictPayload(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	ready, err := h.mgr.studio.put("", studioInput{Name: "Shared app", Instructions: "test", Model: "k3", Status: "ready"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := h.mgr.studio.put("", studioInput{Name: "Draft app", Instructions: "test", Model: "k3", Status: "draft"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/shortcuts?agent_id=" + ready.ID
	res := h.doAs("", http.MethodGet, path, "")
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatal("anonymous private library read accepted")
	}
	read := func(token, path string) shortcutView {
		t.Helper()
		res := h.doAs(token, http.MethodGet, path, "")
		defer res.Body.Close()
		var v shortcutView
		if res.StatusCode != 200 || json.NewDecoder(res.Body).Decode(&v) != nil || res.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("private library read failed: %d", res.StatusCode)
		}
		return v
	}
	alice, bob := read(aliceToken, path), read(bobToken, path)
	if alice.ScopeID == bob.ScopeID || alice.ScopeID == read(aliceToken, "/api/shortcuts").ScopeID {
		t.Fatal("owner and Agent namespaces not distinct")
	}
	input := map[string]any{"scope_id": alice.ScopeID, "revision": 0, "shortcuts": []userShortcut{{ID: "private", Title: "数据库", Content: "synthetic-private-body"}}}
	payload, _ := json.Marshal(input)
	res = h.doAs(aliceToken, http.MethodPut, path, string(payload))
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("save failed: %d", res.StatusCode)
	}
	for _, token := range []string{aliceToken, bobToken} {
		res = h.doAs(token, http.MethodPut, path, string(payload))
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 409 || token == bobToken && strings.Contains(string(raw), "synthetic-private-body") {
			t.Fatal("stale write accepted or identity conflict leaked another user's content")
		}
	}
	for _, target := range []string{"/api/shortcuts?agent_id=ag_missing", "/api/shortcuts?agent_id=" + draft.ID, "/api/shortcuts?agent_id=../escape"} {
		res = h.doAs(aliceToken, http.MethodGet, target, "")
		res.Body.Close()
		if res.StatusCode != 404 {
			t.Fatalf("non-visible Agent library accepted: %d", res.StatusCode)
		}
	}
	for _, body := range []string{`null`, `{"revision":1,"shortcuts":[]}`, `{"scope_id":"x","shortcuts":[]}`, `{"scope_id":"x","revision":1,"shortcuts":[],"owner":"bob"}`, `{"scope_id":"x","revision":1,"shortcuts":[]} {}`} {
		res = h.doAs(aliceToken, http.MethodPut, path, body)
		res.Body.Close()
		if res.StatusCode != 400 {
			t.Fatalf("malformed payload accepted: %d", res.StatusCode)
		}
	}
	input["revision"], input["shortcuts"] = 1, nil
	payload, _ = json.Marshal(input)
	res = h.doAs(aliceToken, http.MethodPut, path, string(payload))
	res.Body.Close()
	if res.StatusCode != 400 || read(aliceToken, path).Revision != 1 || len(read(bobToken, path).Shortcuts) != 0 {
		t.Fatal("invalid update changed a private library")
	}
}

func TestUserShortcutLanguagePreservesOldPlainTextAndNewSyntaxAcrossReload(t *testing.T) {
	m := bookmarkManager(t)
	entries := []userShortcut{{ID: "old", Title: "旧版资料", Content: "old plain text"}, {ID: "new", Title: "SQL 资料", Content: "SELECT * FROM data;\n", Language: "sql"}}
	saved, err := m.replaceUserShortcuts(nil, "", 0, entries)
	if err != nil || saved.Shortcuts[0].Language != "" || saved.Shortcuts[1].Language != "sql" {
		t.Fatal("language metadata or legacy text default changed", err)
	}
	reopened := &Manager{cfg: m.cfg}
	got, err := reopened.userShortcuts(nil, "")
	if err != nil || got.Shortcuts[0].Language != "" || got.Shortcuts[1].Language != "sql" || got.Shortcuts[1].Content != entries[1].Content {
		t.Fatal("language or exact text lost on reopen", err)
	}
	for _, language := range []string{"javascript-fake", "<script>", "SQL", "sql\n"} {
		bad := entries[1]
		bad.Language = language
		if _, err := m.replaceUserShortcuts(nil, "", 1, []userShortcut{bad}); err == nil {
			t.Fatalf("unsupported language accepted: %q", language)
		}
	}
	after, err := reopened.userShortcuts(nil, "")
	if err != nil || after.Revision != 1 || len(after.Shortcuts) != 2 {
		t.Fatal("invalid syntax changed saved library")
	}
}
