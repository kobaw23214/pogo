package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/information"
	"github.com/yosukeno/pogo/inputref"
	"github.com/yosukeno/pogo/skills"
)

func projectedShortcutView(t *testing.T, h *muHarness, token, agent string) shortcutView {
	t.Helper()
	r := h.doAs(token, "GET", "/api/shortcuts?agent_id="+agent, "")
	defer r.Body.Close()
	var view shortcutView
	if r.StatusCode != 200 || json.NewDecoder(r.Body).Decode(&view) != nil {
		t.Fatalf("projection read failed %d", r.StatusCode)
	}
	return view
}

func TestStarterShortcutProjectionHasOneSourcePreservesOrderAndDoesNotConsumePrivateLimit(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	parts := []inputref.Part{{Type: "text", Text: "before "}, {Type: "element", Text: "<page-element source=\"native\" url=\"https://example.invalid/\">\nbutton Test\n</page-element>"}, {Type: "skill", Name: "office"}, {Type: "office", Office: &inputref.OfficeSnapshot{Path: "uploads/a.xlsx", Title: "A", Editor: "cell", Text: "frozen selection", CanvasVersion: "v1", Dirty: true}}, {Type: "text", Text: " after"}}
	entries := []starterEntry{{Title: "Ordered", Icon: "database", Prompt: "Ordered references", Parts: parts, InputLayout: "inline"}, {Prompt: strings.Repeat("长", 200)}}
	for index := 0; index < 270; index++ {
		entries = append(entries, starterEntry{Title: fmt.Sprintf("Card %d", index), Prompt: fmt.Sprintf("Prompt %d", index)})
	}
	a, err := h.mgr.studio.put("", studioInput{Name: "Source", Instructions: "test", Model: "k3", Status: "ready", Starters: entries}, 0)
	if err != nil {
		t.Fatal(err)
	}
	owner := identity.LocalScope("alice")
	private := make([]userShortcut, shortcutsMaxCount)
	for i := range private {
		private[i] = userShortcut{ID: fmt.Sprintf("private_%d", i), Title: "Private", Content: "data"}
	}
	if _, err = h.mgr.replaceUserShortcuts(&owner, a.ID, 0, private); err != nil {
		t.Fatal(err)
	}
	view := projectedShortcutView(t, h, aliceToken, a.ID)
	if len(view.Shortcuts) != 256 || len(view.StarterShortcuts) != 272 || view.CanManageStarters {
		t.Fatal("projection limit, source grant or private data changed")
	}
	first := view.StarterShortcuts[0]
	if !reflect.DeepEqual(first.Parts, parts) || first.Icon != "database" || first.InputLayout != "inline" || len([]rune(view.StarterShortcuts[1].Title)) != 120 || view.StarterShortcuts[1].Content != entries[1].Prompt {
		t.Fatal("ordered data or legacy full prompt changed")
	}
	input := a.studioInput
	input.Starters = []starterEntry{entries[1], entries[0]}
	a, err = h.mgr.studio.put(a.ID, input, a.Revision)
	if err != nil {
		t.Fatal(err)
	}
	changed := projectedShortcutView(t, h, aliceToken, a.ID)
	if len(changed.StarterShortcuts) != 2 || changed.StarterShortcuts[1].ID != first.ID {
		t.Fatal("source reorder duplicated or lost stable content identity")
	}
	input = a.studioInput
	input.Starters = []starterEntry{}
	if _, err = h.mgr.studio.put(a.ID, input, a.Revision); err != nil {
		t.Fatal(err)
	}
	after := projectedShortcutView(t, h, aliceToken, a.ID)
	if len(after.StarterShortcuts) != 0 || after.Revision != view.Revision || len(after.Shortcuts) != 256 {
		t.Fatal("deleted source resurrected or projection wrote private store")
	}
	bob := projectedShortcutView(t, h, bobToken, a.ID)
	if len(bob.Shortcuts) != 0 {
		t.Fatal("projection exposed another owner's private shortcuts")
	}
}

func TestStarterShortcutResolutionRequiresExactCurrentAgentSourceAndCopiesFilesBeforeNesting(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	h.armUsers(map[string]userRecord{"alice": {Username: "alice", Token: aliceToken, Admin: true}, "bob": {Username: "bob", Token: bobToken}})
	a, err := h.mgr.studio.put("", studioInput{Name: "Files", Instructions: "test", Model: "k3", Status: "ready"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	sid := h.studioConversationAs(aliceToken, a.ID)
	sess := h.session(sid)
	if err = os.MkdirAll(filepath.Join(sess.cwd, "uploads"), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(sess.cwd, "uploads", "test.csv"), []byte("EXACT_SOURCE_BYTES"), 0600); err != nil {
		t.Fatal(err)
	}
	saved := starterAssetReply(t, h.doAs(aliceToken, "POST", "/api/sessions/"+sid+"/starter-assets", studioBody(starterAssetInput{InputLayout: "inline", Parts: []inputref.Part{{Type: "text", Text: "Read "}, {Type: "file", Path: "uploads/test.csv", AsFile: true}, {Type: "text", Text: " then report"}}})))
	input := a.studioInput
	input.Starters = []starterEntry{{Title: "CSV procedure", Prompt: saved.Prompt, Parts: saved.Parts, InputLayout: "inline"}}
	a, err = h.mgr.studio.put(a.ID, input, a.Revision)
	if err != nil {
		t.Fatal(err)
	}
	view := projectedShortcutView(t, h, aliceToken, a.ID)
	item := view.StarterShortcuts[0]
	var imported informationResult
	_ = json.Unmarshal(infoJSON(t, h, aliceToken, "POST", "/api/information/import-shortcut", map[string]any{"revision": 0, "source_agent_id": a.ID, "shortcut_id": item.ID}, 201), &imported)
	if imported.Entry == nil || !strings.HasPrefix(imported.Entry.Shortcut.Parts[1].Path, "information-assets/") {
		t.Fatal("published source asset not imported into library snapshot")
	}
	importedData := infoJSON(t, h, aliceToken, "GET", "/api/information/entries/"+imported.Entry.ID+"/assets/content?version=1&asset="+url.QueryEscape(imported.Entry.Shortcut.Parts[1].Path), nil, 200)
	if string(importedData) != "EXACT_SOURCE_BYTES" {
		t.Fatal("import read wrong source namespace")
	}
	url := "/api/sessions/" + sid + "/shortcuts/starters/" + item.ID + "/resolve"
	resolved := starterAssetReply(t, h.doAs(aliceToken, "POST", url, ""))
	if len(resolved.Parts) != 3 || resolved.Parts[0].Text != "Read " || resolved.Parts[2].Text != " then report" || len(resolved.Attachments) != 1 || resolved.Parts[1].Path == saved.Parts[1].Path {
		t.Fatal("source materialization changed ordered content")
	}
	data, err := os.ReadFile(filepath.Join(sess.cwd, filepath.FromSlash(resolved.Parts[1].Path)))
	if err != nil || string(data) != "EXACT_SOURCE_BYTES" {
		t.Fatal("snapshot did not materialize exact source bytes")
	}
	nested := starterAssetReply(t, h.doAs(aliceToken, "POST", "/api/sessions/"+sid+"/shortcuts/assets", studioBody(starterAssetInput{Parts: []inputref.Part{{Type: "shortcut", ShortcutID: item.ID, Title: item.Title, Text: item.Content, Parts: resolved.Parts}}})))
	if !strings.HasPrefix(nested.Parts[0].Parts[1].Path, "shortcut-assets/") {
		t.Fatal("nested source files were not made owner-private")
	}
	for _, request := range []struct {
		token, url, body string
		status           int
	}{{bobToken, url, "", 404}, {aliceToken, url, `{"parts":[]}`, 400}, {aliceToken, "/api/sessions/" + h.createAs(aliceToken) + "/shortcuts/starters/" + item.ID + "/resolve", "", 409}} {
		r := h.doAs(request.token, "POST", request.url, request.body)
		r.Body.Close()
		if r.StatusCode != request.status {
			t.Fatalf("source grant accepted or wrong status: got %d want %d", r.StatusCode, request.status)
		}
	}
	input = a.studioInput
	input.Starters = []starterEntry{}
	if _, err = h.mgr.studio.put(a.ID, input, a.Revision); err != nil {
		t.Fatal(err)
	}
	r := h.doAs(aliceToken, "POST", url, "")
	r.Body.Close()
	if r.StatusCode != 409 {
		t.Fatal("deleted source selected by old content hash")
	}
}

func TestStarterShortcutProjectionImportedIntoInformationLibraryAsIndependentSnapshot(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	a, err := h.mgr.studio.put("", studioInput{Name: "Published", Instructions: "test", Model: "k3", Status: "ready", Starters: []starterEntry{{Title: "Source", Prompt: "original", Parts: []inputref.Part{{Type: "text", Text: "first"}, {Type: "skill", Name: "office"}, {Type: "text", Text: "last"}}, InputLayout: "inline"}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	item := projectedShortcutView(t, h, aliceToken, a.ID).StarterShortcuts[0]
	var imported informationResult
	_ = json.Unmarshal(infoJSON(t, h, aliceToken, "POST", "/api/information/import-shortcut", map[string]any{"revision": 0, "source_agent_id": a.ID, "shortcut_id": item.ID}, 201), &imported)
	if imported.Entry == nil || imported.Entry.Shortcut.Title != "Source" || !reflect.DeepEqual(imported.Entry.Shortcut.Parts, item.Parts) {
		t.Fatal("information library lost projection snapshot")
	}
	input := a.studioInput
	input.Starters = []starterEntry{}
	if _, err = h.mgr.studio.put(a.ID, input, a.Revision); err != nil {
		t.Fatal(err)
	}
	var retained information.Entry
	_ = json.Unmarshal(infoJSON(t, h, aliceToken, "GET", "/api/information/entries/"+imported.Entry.ID, nil, 200), &retained)
	if retained.Shortcut == nil || retained.Shortcut.Content != imported.Entry.Shortcut.Content || !reflect.DeepEqual(retained.Shortcut.Parts, imported.Entry.Shortcut.Parts) {
		t.Fatal("import was a live alias instead of a deliberate independent snapshot")
	}
}

func TestGlobalStarterProjectionSeparatesPanelActionsAndVersionGuardsSourceSave(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	h.psrv.opts.StartersStore = filepath.Join(t.TempDir(), "starters.json")
	h.psrv.opts.Panels = []Panel{{Name: "docs"}}
	set := skills.Starters{Heading: "Homepage", Cards: []skills.StarterCard{{Title: "Prompt", Prompt: "exact prompt"}, {Title: "Open docs", Panel: "docs", At: "#/list"}}, Followups: []skills.FollowupGroup{}}
	raw, _ := json.Marshal(set)
	if err := os.WriteFile(h.psrv.opts.StartersStore, raw, 0600); err != nil {
		t.Fatal(err)
	}
	view := projectedShortcutView(t, h, aliceToken, "")
	if len(view.StarterShortcuts) != 1 || len(view.StarterActions) != 1 || view.StarterActions[0].At != "#/list" {
		t.Fatal("panel action was dropped or became model content")
	}
	before := h.psrv.effectiveStarters().Revision
	set.Cards[0].Prompt = "updated elsewhere"
	raw, _ = json.Marshal(set)
	_ = os.WriteFile(h.psrv.opts.StartersStore, raw, 0600)
	r := httptestRecorderForStarters(t, h.psrv, studioBody(map[string]any{"expected_revision": before, "cards": []skills.StarterCard{{Title: "Old edit", Prompt: "must not overwrite"}}}))
	if r.Code != http.StatusConflict {
		t.Fatal("old homepage editor overwrote updated source")
	}
	data, _ := os.ReadFile(h.psrv.opts.StartersStore)
	if strings.Contains(string(data), "must not overwrite") {
		t.Fatal("source conflict changed disk")
	}
	current := h.psrv.effectiveStarters().Revision
	r = httptestRecorderForStarters(t, h.psrv, studioBody(map[string]any{"expected_revision": current, "heading": "Updated homepage", "cards": []skills.StarterCard{{Title: "Fresh edit", Prompt: "saved with current revision"}}}))
	if r.Code != http.StatusOK {
		t.Fatalf("current source revision rejected: %d %s", r.Code, r.Body.String())
	}
	data, _ = os.ReadFile(h.psrv.opts.StartersStore)
	if !strings.Contains(string(data), "saved with current revision") || strings.Contains(string(data), "expected_revision") {
		t.Fatal("current write was not saved as pure starter data")
	}
	r = httptestRecorderForStarters(t, h.psrv, studioBody(map[string]any{"expected_revision": current, "cards": []skills.StarterCard{{Prompt: "stale"}}}))
	if r.Code != http.StatusConflict {
		t.Fatal("successful write did not advance source revision")
	}
	r = httptestRecorderForStarters(t, h.psrv, studioBody(map[string]any{"expected_revision": h.psrv.effectiveStarters().Revision, "cards": []skills.StarterCard{}, "unknown_field": true}))
	if r.Code != http.StatusBadRequest {
		t.Fatal("request envelope removal bypassed strict starter validation")
	}
	after, _ := os.ReadFile(h.psrv.opts.StartersStore)
	if string(after) != string(data) {
		t.Fatal("rejected source writes changed disk")
	}
}

// Direct handler invocation keeps this source-CAS unit independent of the
// separate authentication route tests; no real server data is involved.
func httptestRecorderForStarters(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("PUT", "/api/starters", io.NopCloser(strings.NewReader(body)))
	w := httptest.NewRecorder()
	s.handleSaveStarters(w, r)
	return w
}
