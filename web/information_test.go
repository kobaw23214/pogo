package web

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/information"
	"github.com/yosukeno/pogo/inputref"
)

type informationResult struct {
	information.TreeView
	Entry *information.Entry `json:"entry,omitempty"`
}

func infoJSON(t *testing.T, h *muHarness, token, method, target string, body any, want int) []byte {
	t.Helper()
	raw := ""
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			t.Fatal(e)
		}
		raw = string(b)
	}
	r := h.doAs(token, method, target, raw)
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode != want {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, target, r.StatusCode, want, b)
	}
	return b
}
func infoUpload(t *testing.T, h *muHarness, token, target, name, data string, fields map[string]string, want int) []byte {
	t.Helper()
	var body bytes.Buffer
	m := multipart.NewWriter(&body)
	for k, v := range fields {
		if err := m.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	p, e := m.CreateFormFile("file", name)
	if e != nil {
		t.Fatal(e)
	}
	_, _ = p.Write([]byte(data))
	m.Close()
	req, _ := http.NewRequest("POST", h.srv.URL+target, &body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", m.FormDataContentType())
	r, e := h.srv.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode != want {
		t.Fatalf("upload %d want%d: %s", r.StatusCode, want, b)
	}
	return b
}
func TestInformationAPIScopeCASStrictPayloadAndMultipleAgents(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	infoJSON(t, h, "", "GET", "/api/information", nil, 401)
	var alice, bob information.TreeView
	_ = json.Unmarshal(infoJSON(t, h, aliceToken, "GET", "/api/information", nil, 200), &alice)
	_ = json.Unmarshal(infoJSON(t, h, bobToken, "GET", "/api/information", nil, 200), &bob)
	if alice.ScopeID == bob.ScopeID || alice.Revision != 0 || alice.Entries == nil {
		t.Fatal("namespace leak")
	}
	var created informationResult
	_ = json.Unmarshal(infoJSON(t, h, aliceToken, "POST", "/api/information/entries", map[string]any{"revision": 0, "parent_id": "", "kind": "shortcut", "name": "Shared", "shortcut": information.Shortcut{ID: "data", Title: "Data", Content: "private synthetic source"}}, 201), &created)
	if created.Entry == nil || created.Entries[0].Shortcut != nil {
		t.Fatal("metadata contains full content")
	}
	id := created.Entry.ID
	infoJSON(t, h, aliceToken, "POST", "/api/information/entries", map[string]any{"revision": 0, "kind": "folder", "name": "stale"}, 409)
	infoJSON(t, h, aliceToken, "POST", "/api/information/entries", map[string]any{"revision": 1, "kind": "folder", "name": "Shared"}, 409)
	for _, payload := range []string{`null`, `{"revision":1,"kind":"folder","name":"x","owner":"bob"}`, `{"revision":1,"kind":"folder","name":"x"} {}`} {
		r := h.doAs(aliceToken, "POST", "/api/information/entries", payload)
		r.Body.Close()
		if r.StatusCode != 400 {
			t.Fatal("malformed body accepted", r.StatusCode)
		}
	}
	for _, route := range []string{"/api/information/entries/" + id, "/api/information/entries/" + id + "?version=1", "/api/information/entries/" + id + "/content?version=1"} {
		b := infoJSON(t, h, bobToken, "GET", route, nil, 404)
		if bytes.Contains(b, []byte("private synthetic")) {
			t.Fatal("cross-account content leak")
		}
	}
	for i := 0; i < 2; i++ {
		agent, err := h.mgr.studio.put("", studioInput{Name: "App", Instructions: "test", Model: "k3", Status: "ready"}, 0)
		if err != nil {
			t.Fatal(err)
		}
		sid := h.studioConversationAs(aliceToken, agent.ID)
		result := infoJSON(t, h, aliceToken, "POST", "/api/sessions/"+sid+"/information/resolve", map[string]any{"id": id, "version": 1}, 200)
		if !bytes.Contains(result, []byte("private synthetic source")) {
			t.Fatal("Agent did not share current owner's library")
		}
		infoJSON(t, h, bobToken, "POST", "/api/sessions/"+sid+"/information/resolve", map[string]any{"id": id, "version": 1}, 404)
	}
	req, _ := http.NewRequest("POST", h.srv.URL+"/api/information/entries", strings.NewReader(`{"revision":1,"kind":"folder","name":"x"}`))
	req.Header.Set("Authorization", "Bearer "+aliceToken)
	req.Header.Set("Origin", "https://unrelated.test")
	r, err := h.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 403 {
		t.Fatal("cross-origin write accepted", r.StatusCode)
	}
	for _, owner := range []identity.Scope{identity.LocalScope("alice"), identity.LocalScope("bob")} {
		s, err := h.mgr.InformationStore(&owner)
		if err != nil {
			t.Fatal(err)
		}
		if s.ScopeID() == "" {
			t.Fatal("scope missing")
		}
	}
}
func TestInformationAPIFileAssetsResolveOldVersionsAndSafeInline(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	sid := h.createAs(aliceToken)
	var first informationResult
	_ = json.Unmarshal(infoUpload(t, h, aliceToken, "/api/information/files", "report.txt", "original bytes", map[string]string{"revision": "0"}, 201), &first)
	if first.Entry == nil || first.Entry.File == nil {
		t.Fatal("file missing")
	}
	id := first.Entry.ID
	r := h.doAs(aliceToken, "GET", "/api/information/entries/"+id+"/content?version=1", "")
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 200 || string(b) != "original bytes" || !strings.HasPrefix(r.Header.Get("Content-Disposition"), "inline") || r.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("content mismatch")
	}
	var asset struct {
		Part       inputref.Part  `json:"part"`
		Attachment AttachmentMeta `json:"attachment"`
	}
	_ = json.Unmarshal(infoUpload(t, h, aliceToken, "/api/information/assets", "nested.csv", "a,b\n1,2", nil, 201), &asset)
	if !strings.HasPrefix(asset.Part.Path, "information-assets/") {
		t.Fatal("asset not opaque")
	}
	var q informationResult
	_ = json.Unmarshal(infoJSON(t, h, aliceToken, "POST", "/api/information/entries", map[string]any{"revision": 1, "parent_id": "", "kind": "shortcut", "name": "Nested", "shortcut": information.Shortcut{ID: "nested", Title: "Nested", Content: "", Parts: []inputref.Part{{Type: "text", Text: "prefix"}, asset.Part, {Type: "skill", Name: "office"}, {Type: "office", Office: &inputref.OfficeSnapshot{Path: "uploads/source.docx", Title: "Word section", Editor: "word", Text: "frozen unsaved section", CanvasVersion: "canvas1", SourceSession: "source-other-session", Dirty: true}}}}}, 201), &q)
	var resolved struct {
		Parts       []inputref.Part  `json:"parts"`
		Attachments []AttachmentMeta `json:"attachments"`
	}
	_ = json.Unmarshal(infoJSON(t, h, aliceToken, "POST", "/api/sessions/"+sid+"/information/resolve", map[string]any{"id": q.Entry.ID, "version": 1}, 200), &resolved)
	if len(resolved.Parts) != 1 || len(resolved.Attachments) != 1 || resolved.Parts[0].Parts[3].Office.SourceSession != "source-other-session" {
		t.Fatal("reference snapshot/order changed")
	}
	nestedURL := "/api/information/entries/" + q.Entry.ID + "/assets/content?version=1&asset=" + url.QueryEscape(asset.Part.Path)
	r = h.doAs(aliceToken, "GET", nestedURL, "")
	nestedBytes, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 200 || string(nestedBytes) != "a,b\n1,2" {
		t.Fatal("nested member asset unavailable")
	}
	infoJSON(t, h, bobToken, "GET", nestedURL, nil, 404)
	infoJSON(t, h, aliceToken, "GET", "/api/information/entries/"+q.Entry.ID+"/assets/content?version=1&asset="+url.QueryEscape(first.Entry.File.Asset), nil, 404)
	infoJSON(t, h, aliceToken, "GET", "/api/information/entries/"+q.Entry.ID+"/assets/content?asset="+url.QueryEscape(asset.Part.Path), nil, 400)
	p := resolved.Parts[0].Parts[1].Path
	if strings.HasPrefix(p, "information-assets/") || !strings.HasPrefix(p, "uploads/information/") {
		t.Fatal("file not materialized")
	}
	data, err := os.ReadFile(filepath.Join(h.session(sid).cwd, p))
	if err != nil || string(data) != "a,b\n1,2" {
		t.Fatal(err)
	}
	infoJSON(t, h, aliceToken, "DELETE", "/api/information/entries/"+id+"?revision=2", nil, 200)
	infoJSON(t, h, aliceToken, "GET", "/api/information/entries/"+id, nil, 404)
	infoJSON(t, h, aliceToken, "GET", "/api/information/entries/"+id+"?version=1", nil, 200)
	var html informationResult
	_ = json.Unmarshal(infoUpload(t, h, aliceToken, "/api/information/files", "unsafe.html", "<script>bad()</script>", map[string]string{"revision": "3"}, 201), &html)
	r = h.doAs(aliceToken, "GET", "/api/information/entries/"+html.Entry.ID+"/content", "")
	r.Body.Close()
	if !strings.HasPrefix(r.Header.Get("Content-Disposition"), "attachment") || !strings.Contains(r.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatal("active content served inline")
	}
	infoUpload(t, h, bobToken, "/api/information/files", "other.txt", "other private", map[string]string{"revision": "0"}, 201)
	infoJSON(t, h, bobToken, "POST", "/api/information/entries", map[string]any{"revision": 1, "kind": "shortcut", "name": "forged", "shortcut": information.Shortcut{ID: "forged", Title: "Forged", Content: "", Parts: []inputref.Part{asset.Part}}}, 404)
}
func TestInformationImportShortcutCopiesOwnedAssetsAndKeepsPrivateLibrary(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	owner := identity.LocalScope("alice")
	source := userShortcut{ID: "private", Title: "Private material", Content: "original exact content", Icon: "database", Language: "sql"}
	sid := h.createAs(aliceToken)
	session := h.session(sid)
	if err := os.MkdirAll(filepath.Join(session.cwd, "uploads"), 0755); err != nil {
		t.Fatal(err)
	}
	privateSource := filepath.Join(session.cwd, "uploads", "private.csv")
	if err := os.WriteFile(privateSource, []byte("PRIVATE_COPY_BYTES"), 0600); err != nil {
		t.Fatal(err)
	}
	saved := starterAssetReply(t, h.doAs(aliceToken, "POST", "/api/sessions/"+sid+"/shortcuts/assets", studioBody(starterAssetInput{Parts: []inputref.Part{{Type: "text", Text: source.Content}, {Type: "file", Path: "uploads/private.csv", AsFile: true}}})))
	source.Parts = saved.Parts
	if _, err := h.mgr.replaceUserShortcuts(&owner, "", 0, []userShortcut{source}); err != nil {
		t.Fatal(err)
	}
	var imported informationResult
	_ = json.Unmarshal(infoJSON(t, h, aliceToken, "POST", "/api/information/import-shortcut", map[string]any{"revision": 0, "parent_id": "", "source_agent_id": "", "shortcut_id": "private"}, 201), &imported)
	if imported.Entry == nil || imported.Entry.Shortcut.Content != source.Content || imported.Entry.Shortcut.Icon != "database" {
		t.Fatal("not a snapshot")
	}
	if !strings.HasPrefix(imported.Entry.Shortcut.Parts[1].Path, "information-assets/") || imported.Entry.Shortcut.Parts[1].Path == source.Parts[1].Path {
		t.Fatal("private file not copied")
	}
	if err := os.Remove(privateSource); err != nil {
		t.Fatal(err)
	}
	privateRoot, err := h.mgr.shortcutAssetNamespace(&owner, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(privateRoot); err != nil {
		t.Fatal(err)
	}
	infoFile := infoJSON(t, h, aliceToken, "GET", "/api/information/entries/"+imported.Entry.ID+"/assets/content?version=1&asset="+url.QueryEscape(imported.Entry.Shortcut.Parts[1].Path), nil, 200)
	if string(infoFile) != "PRIVATE_COPY_BYTES" {
		t.Fatal("copy depended on private source")
	}
	if _, err := h.mgr.replaceUserShortcuts(&owner, "", 1, []userShortcut{}); err != nil {
		t.Fatal(err)
	}
	got := infoJSON(t, h, aliceToken, "GET", "/api/information/entries/"+imported.Entry.ID+"?version=1", nil, 200)
	if !bytes.Contains(got, []byte(source.Content)) {
		t.Fatal("source deletion changed copied information")
	}
	infoJSON(t, h, bobToken, "POST", "/api/information/import-shortcut", map[string]any{"revision": 0, "parent_id": "", "source_agent_id": "", "shortcut_id": "private"}, 404)
}

func TestInformationShortcutRenameReadAndResolveKeepOldAndNewTitles(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	sid := h.createAs(aliceToken)
	var created informationResult
	_ = json.Unmarshal(infoJSON(t, h, aliceToken, "POST", "/api/information/entries", map[string]any{"revision": 0, "kind": "shortcut", "name": "Original name", "shortcut": information.Shortcut{ID: "record", Title: "Alternate title", Content: "body"}}, 201), &created)
	id := created.Entry.ID
	var renamed informationResult
	_ = json.Unmarshal(infoJSON(t, h, aliceToken, "PATCH", "/api/information/entries/"+id, map[string]any{"revision": 1, "name": "Renamed"}, 200), &renamed)
	if renamed.Entry.Shortcut.Title != "Renamed" || renamed.Entries[0].Name != "Renamed" {
		t.Fatal("rename list and detail disagree")
	}
	for version, title := range map[string]string{"1": "Original name", "2": "Renamed"} {
		var entry information.Entry
		_ = json.Unmarshal(infoJSON(t, h, aliceToken, "GET", "/api/information/entries/"+id+"?version="+version, nil, 200), &entry)
		if entry.Name != title || entry.Shortcut.Title != title {
			t.Fatal("get version title changed", version)
		}
		var resolved struct {
			Parts []inputref.Part `json:"parts"`
		}
		v := 1
		if version == "2" {
			v = 2
		}
		_ = json.Unmarshal(infoJSON(t, h, aliceToken, "POST", "/api/sessions/"+sid+"/information/resolve", map[string]any{"id": id, "version": v}, 200), &resolved)
		if len(resolved.Parts) != 1 || resolved.Parts[0].Title != title || resolved.Parts[0].Text != "body" {
			t.Fatal("resolve version title changed", version)
		}
	}
	var edited informationResult
	_ = json.Unmarshal(infoJSON(t, h, aliceToken, "PATCH", "/api/information/entries/"+id, map[string]any{"revision": 2, "shortcut": information.Shortcut{ID: "record", Title: "Edited title", Content: "updated body"}}, 200), &edited)
	if edited.Entry.Name != "Edited title" || edited.Entry.Shortcut.Title != "Edited title" {
		t.Fatal("title-only patch did not synchronize name")
	}
}
