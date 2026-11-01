package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/skills"
)

func TestShortcutFoldersVersionOneReloadLegacyUpdateAndPrivateScope(t *testing.T) {
	m := bookmarkManager(t)
	owner := identity.LocalScope("alice")
	entry := userShortcut{ID: "entry", Title: "Private", Content: "synthetic-private-text"}
	initial, err := m.replaceUserShortcuts(&owner, "ag_first", 0, []userShortcut{entry})
	if err != nil {
		t.Fatal(err)
	}
	path, _ := shortcutPath(m.cfg.SessionDir, &owner, "ag_first")
	raw, _ := os.ReadFile(path)
	var legacy map[string]json.RawMessage
	_ = json.Unmarshal(raw, &legacy)
	delete(legacy, "folders")
	raw, _ = json.Marshal(legacy)
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	old, err := m.userShortcuts(&owner, "ag_first")
	if err != nil || old.Folders == nil || len(old.Folders) != 0 || old.Revision != initial.Revision || old.Shortcuts[0].Content != entry.Content {
		t.Fatal("legacy Version1 without folders must retain its exact private text and explicit empty tree", err)
	}
	entry.ParentID = "child"
	folders := []shortcutFolder{{ID: "root", Name: "  我的目录  "}, {ID: "child", Name: "Nested", ParentID: "root"}}
	saved, err := m.replaceUserShortcuts(&owner, "ag_first", old.Revision, []userShortcut{entry}, folders)
	if err != nil || saved.Folders[0].Name != "我的目录" || saved.Shortcuts[0].ParentID != "child" {
		t.Fatal("folder order, trimmed name or entry parent lost", err)
	}
	reopened := &Manager{cfg: m.cfg}
	got, err := reopened.userShortcuts(&owner, "ag_first")
	if err != nil || !reflect.DeepEqual(got, saved) {
		t.Fatal("private folder tree did not survive Manager reopen", err)
	}
	got.Shortcuts[0].Content = "legacy caller updates content"
	updated, err := reopened.replaceUserShortcuts(&owner, "ag_first", got.Revision, got.Shortcuts)
	if err != nil || !reflect.DeepEqual(updated.Folders, saved.Folders) || updated.Shortcuts[0].ParentID != "child" {
		t.Fatal("old save without folders silently deleted the current folder tree", err)
	}
	bob := identity.LocalScope("bob")
	for _, scope := range []struct {
		owner *identity.Scope
		agent string
	}{{&bob, "ag_first"}, {&owner, "ag_other"}, {&owner, ""}, {nil, "ag_first"}} {
		other, err := reopened.userShortcuts(scope.owner, scope.agent)
		if err != nil || len(other.Folders) != 0 || len(other.Shortcuts) != 0 || other.ScopeID == saved.ScopeID {
			t.Fatal("folders or shortcuts crossed owner/current-Agent boundaries", err)
		}
	}
}

func TestShortcutFoldersRejectReservedParentsCyclesDepthAndDuplicateIDs(t *testing.T) {
	entry := userShortcut{ID: "entry", Title: "Private", Content: "text"}
	cases := []struct {
		name    string
		folders []shortcutFolder
		entries []userShortcut
	}{
		{"system-folder", []shortcutFolder{{ID: "@information", Name: "forbidden"}}, nil},
		{"system-parent", []shortcutFolder{{ID: "private", Name: "private", ParentID: "@quick-cards"}}, nil},
		{"missing-folder-parent", []shortcutFolder{{ID: "private", Name: "private", ParentID: "missing"}}, nil},
		{"self-cycle", []shortcutFolder{{ID: "private", Name: "private", ParentID: "private"}}, nil},
		{"two-node-cycle", []shortcutFolder{{ID: "a", Name: "A", ParentID: "b"}, {ID: "b", Name: "B", ParentID: "a"}}, nil},
		{"duplicate-folders", []shortcutFolder{{ID: "a", Name: "A"}, {ID: "a", Name: "B"}}, nil},
		{"folder-item-collision", []shortcutFolder{{ID: "entry", Name: "Folder"}}, []userShortcut{entry}},
		{"blank-name", []shortcutFolder{{ID: "a", Name: "  "}}, nil},
		{"control-name", []shortcutFolder{{ID: "a", Name: "bad\x00name"}}, nil},
		{"long-name", []shortcutFolder{{ID: "a", Name: strings.Repeat("中", 121)}}, nil},
		{"item-missing-parent", nil, []userShortcut{{ID: "entry", Title: "Private", Content: "text", ParentID: "missing"}}},
		{"item-system-parent", nil, []userShortcut{{ID: "entry", Title: "Private", Content: "text", ParentID: "@information"}}},
		{"too-many", make([]shortcutFolder, shortcutsMaxFolders+1), nil},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := validateShortcutFolders(tt.folders, tt.entries); !errors.Is(err, errShortcutTreeInvalid) {
				t.Fatal("invalid private tree was accepted", err)
			}
		})
	}
	deep := []shortcutFolder{}
	for i := 0; i < shortcutsMaxFolderDepth; i++ {
		parent := ""
		if i > 0 {
			parent = fmt.Sprintf("folder_%d", i-1)
		}
		deep = append(deep, shortcutFolder{ID: fmt.Sprintf("folder_%d", i), Name: "Folder", ParentID: parent})
	}
	if _, err := validateShortcutFolders(deep, nil); err != nil {
		t.Fatal("the documented 16-level boundary was rejected", err)
	}
	deep = append(deep, shortcutFolder{ID: "too_deep", Name: "Too deep", ParentID: deep[len(deep)-1].ID})
	if _, err := validateShortcutFolders(deep, nil); !errors.Is(err, errShortcutTreeInvalid) {
		t.Fatal("the 17th level was accepted", err)
	}
}

func TestShortcutFoldersWholeTreeDeletionRejectsDanglingChildrenWithoutWriting(t *testing.T) {
	m := bookmarkManager(t)
	entry := userShortcut{ID: "entry", Title: "private-title", Content: "synthetic-secret-body", ParentID: "child"}
	folders := []shortcutFolder{{ID: "root", Name: "Root"}, {ID: "child", Name: "Child", ParentID: "root"}}
	if _, err := m.replaceUserShortcuts(nil, "", 0, []userShortcut{entry}, folders); err != nil {
		t.Fatal(err)
	}
	path, _ := shortcutPath(m.cfg.SessionDir, nil, "")
	before, _ := os.ReadFile(path)
	for _, replacement := range [][]shortcutFolder{nil, {}, folders[:1], folders[1:]} {
		if _, err := m.replaceUserShortcuts(nil, "", 1, []userShortcut{entry}, replacement); !errors.Is(err, errShortcutTreeInvalid) || strings.Contains(err.Error(), "synthetic-secret") {
			t.Fatal("folder deletion implicitly moved/dropped children or leaked private content", err)
		}
		after, _ := os.ReadFile(path)
		if string(after) != string(before) {
			t.Fatal("rejected partial tree changed the saved private document")
		}
	}
	deleted, err := m.replaceUserShortcuts(nil, "", 1, []userShortcut{}, []shortcutFolder{})
	if err != nil || deleted.Revision != 2 || len(deleted.Shortcuts) != 0 || len(deleted.Folders) != 0 {
		t.Fatal("an explicitly supplied valid whole-tree removal was rejected", err)
	}
}

func TestShortcutFoldersAndShortcutsShareOneCASRevision(t *testing.T) {
	m := bookmarkManager(t)
	if _, err := m.replaceUserShortcuts(nil, "", 0, []userShortcut{}, []shortcutFolder{}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{"first", "second"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, err := m.replaceUserShortcuts(nil, "", 1, []userShortcut{{ID: "item_" + id, Title: id, Content: id, ParentID: id}}, []shortcutFolder{{ID: id, Name: id}})
			results <- err
		}(id)
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, errShortcutsConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	view, err := m.userShortcuts(nil, "")
	if err != nil || successes != 1 || conflicts != 1 || view.Revision != 2 || len(view.Folders) != 1 || len(view.Shortcuts) != 1 || view.Shortcuts[0].ParentID != view.Folders[0].ID {
		t.Fatal("concurrent writes combined or overwrote two inconsistent folder/item revisions", err)
	}
}

func TestShortcutFoldersAPICompatibilityValidationAndConflictIsolation(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	agent, err := h.mgr.studio.put("", studioInput{Name: "Agent", Instructions: "test", Model: "k3", Status: "ready"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	route := "/api/shortcuts?agent_id=" + agent.ID
	first := projectedShortcutView(t, h, aliceToken, agent.ID)
	if first.Folders == nil {
		t.Fatal("new API must expose folders as an explicit array")
	}
	entry := userShortcut{ID: "entry", Title: "Private", Content: "private-folder-body", ParentID: "private"}
	folder := shortcutFolder{ID: "private", Name: "private-folder-name"}
	put := func(token string, body map[string]any, expected int) []byte {
		t.Helper()
		encoded, _ := json.Marshal(body)
		r := h.doAs(token, http.MethodPut, route, string(encoded))
		defer r.Body.Close()
		raw, _ := io.ReadAll(r.Body)
		if r.StatusCode != expected {
			t.Fatalf("unexpected save status %d, expected %d: %s", r.StatusCode, expected, raw)
		}
		return raw
	}
	body := map[string]any{"scope_id": first.ScopeID, "revision": 0, "folders": []shortcutFolder{folder}, "shortcuts": []userShortcut{entry}}
	put(aliceToken, body, 200)
	delete(body, "folders")
	body["revision"] = 1
	put(aliceToken, body, 200)
	got := projectedShortcutView(t, h, aliceToken, agent.ID)
	if len(got.Folders) != 1 || got.Folders[0] != folder || got.Shortcuts[0].ParentID != "private" {
		t.Fatal("legacy PUT without folders dropped the current private hierarchy")
	}
	body["revision"] = 2
	for _, folders := range []any{nil, []shortcutFolder{}, []shortcutFolder{{ID: "@information", Name: "System"}}, []map[string]any{{"id": "private", "name": "Private", "readonly": false}}} {
		body["folders"] = folders
		put(aliceToken, body, 400)
	}
	body["folders"] = []shortcutFolder{folder}
	body["revision"] = 1
	conflict := put(aliceToken, body, 409)
	var failure struct {
		Current shortcutView `json:"current"`
	}
	_ = json.Unmarshal(conflict, &failure)
	if failure.Current.Revision != 2 || len(failure.Current.Folders) != 1 || failure.Current.Folders[0] != folder {
		t.Fatal("conflict did not return the whole latest tree")
	}
	other := put(bobToken, body, 409)
	if strings.Contains(string(other), folder.Name) || strings.Contains(string(other), entry.Content) {
		t.Fatal("scope conflict disclosed another owner's private tree")
	}
	if current := projectedShortcutView(t, h, aliceToken, agent.ID); current.Revision != 2 || len(current.Folders) != 1 {
		t.Fatal("invalid/CAS requests changed the existing document")
	}
}

func TestShortcutSystemProjectionIsReadOnlyEvenForAdminWithoutChangingHomepageManager(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	h.armUsers(map[string]userRecord{"alice": {Username: "alice", Token: aliceToken, Admin: true}})
	h.psrv.opts.StartersStore = filepath.Join(t.TempDir(), "starters.json")
	view := projectedShortcutView(t, h, aliceToken, "")
	if view.CanManageStarters {
		t.Fatal("the private shortcut browser granted system source management")
	}
	set := h.psrv.effectiveStarters()
	r := h.doAs(aliceToken, "PUT", "/api/starters", studioBody(map[string]any{"expected_revision": set.Revision, "cards": []skills.StarterCard{{Title: "Homepage", Prompt: "still managed by the existing homepage manager"}}}))
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(r.Body)
		t.Fatalf("existing authorized homepage management was blocked: %d %s", r.StatusCode, raw)
	}
}
