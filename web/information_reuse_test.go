package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/information"
	"github.com/yosukeno/pogo/inputref"
)

type resolvedInformationReuse struct {
	Parts       []inputref.Part   `json:"parts"`
	Attachments []AttachmentMeta  `json:"attachments"`
	Entry       information.Entry `json:"entry"`
}

func informationReuseStore(t *testing.T, h *muHarness, sid string) *information.Store {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("platform preserves the independent-copy fallback")
	}
	s, err := h.mgr.InformationStore(h.session(sid).owner)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func informationReuseAsset(t *testing.T, s *information.Store, name, text string) information.Asset {
	t.Helper()
	a, err := s.PutAsset(name, "", strings.NewReader(text), 4096)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func informationReuseEntry(t *testing.T, s *information.Store, in information.CreateInput) information.Entry {
	t.Helper()
	v, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	_, e, err := s.Create(v.Revision, in)
	if err != nil {
		t.Fatal(err)
	}
	return *e
}
func informationReuseResolve(t *testing.T, h *muHarness, sid string, e information.Entry) resolvedInformationReuse {
	t.Helper()
	var result resolvedInformationReuse
	if err := json.Unmarshal(infoJSON(t, h, aliceToken, "POST", "/api/sessions/"+sid+"/information/resolve", map[string]any{"id": e.ID, "version": e.Version}, 200), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestInformationResolveReusePreservesOrderAndEditedCopies(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	sid := h.createAs(aliceToken)
	s := informationReuseStore(t, h, sid)
	a := informationReuseAsset(t, s, "report.txt", "source")
	e := informationReuseEntry(t, s, information.CreateInput{Kind: "shortcut", Name: "ordered", Shortcut: &information.Shortcut{ID: "ordered", Title: "ordered", InputLayout: "inline", Parts: []inputref.Part{{Type: "text", Text: "before"}, {Type: "file", Path: a.Asset, AsFile: true}, {Type: "skill", Name: "office"}, {Type: "shortcut", ShortcutID: "nested", Title: "nested", Text: "", Parts: []inputref.Part{{Type: "file", Path: a.Asset, AsFile: true}}}}}})
	first := informationReuseResolve(t, h, sid, e)
	second := informationReuseResolve(t, h, sid, e)
	if len(first.Attachments) != 1 || len(second.Attachments) != 1 || first.Attachments[0].Path != second.Attachments[0].Path {
		t.Fatal("duplicate file exports", first, second)
	}
	parts := first.Parts[0].Parts
	if parts[0].Text != "before" || parts[2].Type != "skill" || parts[1].Path != parts[3].Parts[0].Path {
		t.Fatal("nested order or deduplication changed")
	}
	old := first.Attachments[0].Path
	cwd := h.session(sid).cwd
	if err := os.WriteFile(filepath.Join(cwd, old), []byte("edited"), 0600); err != nil {
		t.Fatal(err)
	}
	third := informationReuseResolve(t, h, sid, e)
	fourth := informationReuseResolve(t, h, sid, e)
	fresh := third.Attachments[0].Path
	if fresh == old || fourth.Attachments[0].Path != fresh {
		t.Fatal("modified copy reused or replacement duplicated")
	}
	data, _ := os.ReadFile(filepath.Join(cwd, old))
	if string(data) != "edited" {
		t.Fatal("user edit overwritten")
	}
	infoJSON(t, h, bobToken, "POST", "/api/sessions/"+h.createAs(bobToken)+"/information/resolve", map[string]any{"id": e.ID, "version": 1}, 404)
}

func TestInformationResolveReuseFailedLaterAssetCannotDeleteBorrowedFile(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	sid := h.createAs(aliceToken)
	s := informationReuseStore(t, h, sid)
	a := informationReuseAsset(t, s, "safe.txt", "safe")
	b := informationReuseAsset(t, s, "broken.txt", "original")
	single := informationReuseEntry(t, s, information.CreateInput{Kind: "file", Name: a.Name, File: &a})
	first := informationReuseResolve(t, h, sid, single)
	p := first.Attachments[0].Path
	combined := informationReuseEntry(t, s, information.CreateInput{Kind: "shortcut", Name: "combined", Shortcut: &information.Shortcut{ID: "combined", Title: "combined", Parts: []inputref.Part{{Type: "file", Path: a.Asset, AsFile: true}, {Type: "file", Path: b.Asset, AsFile: true}}}})
	if err := os.WriteFile(filepath.Join(s.Root(), "assets", b.SHA256, "data"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	infoJSON(t, h, aliceToken, "POST", "/api/sessions/"+sid+"/information/resolve", map[string]any{"id": combined.ID, "version": 1}, 400)
	data, err := os.ReadFile(filepath.Join(h.session(sid).cwd, p))
	if err != nil || string(data) != "safe" {
		t.Fatal("failed reference deleted borrowed copy", err)
	}
	again := informationReuseResolve(t, h, sid, single)
	if again.Attachments[0].Path != p {
		t.Fatal("successful earlier reference lost")
	}
	if pending, _ := filepath.Glob(filepath.Join(h.session(sid).cwd, "uploads/information/.pending-*")); len(pending) != 0 {
		t.Fatal("failed reference leaked partial stages", pending)
	}
}

func TestInformationResolveReuseLegacyWitnessIsCurrentOwnedSessionLedger(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	sid := h.createAs(aliceToken)
	s := informationReuseStore(t, h, sid)
	a := informationReuseAsset(t, s, "legacy.txt", "source")
	e := informationReuseEntry(t, s, information.CreateInput{Kind: "file", Name: a.Name, File: &a})
	cwd := h.session(sid).cwd
	legacy := "uploads/information/inf_" + strings.Repeat("a", 32) + "/" + a.Name
	if err := os.MkdirAll(filepath.Dir(filepath.Join(cwd, legacy)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, legacy), []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	noteSessionUpload(cwd, sid, legacy)
	result := informationReuseResolve(t, h, sid, e)
	if result.Attachments[0].Path != legacy {
		t.Fatal("authenticated persisted legacy witness not reused")
	}
}

func TestInformationResolveReuseWorkspaceCopySurvivesOriginalSessionDeletion(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	sid := h.createAs(aliceToken)
	other := h.createAs(aliceToken)
	s := informationReuseStore(t, h, sid)
	a := informationReuseAsset(t, s, "shared.txt", "shared")
	e := informationReuseEntry(t, s, information.CreateInput{Kind: "file", Name: a.Name, File: &a})
	first := informationReuseResolve(t, h, sid, e)
	second := informationReuseResolve(t, h, other, e)
	p := first.Attachments[0].Path
	if second.Attachments[0].Path != p {
		t.Fatal("same workspace did not share pristine copy")
	}
	cwd := h.session(sid).cwd
	noteSessionUpload(cwd, sid, p)
	ordinary := "uploads/ordinary.txt"
	if err := os.WriteFile(filepath.Join(cwd, ordinary), []byte("ordinary"), 0600); err != nil {
		t.Fatal(err)
	}
	noteSessionUpload(cwd, sid, ordinary)
	// The second composer has not posted a message or recorded an upload. Its
	// successful library resolve must nevertheless survive the old session's GC.
	response := h.doAs(aliceToken, "DELETE", "/api/sessions/"+sid, "")
	response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatal("session delete failed", response.StatusCode)
	}
	data, err := os.ReadFile(filepath.Join(cwd, p))
	if err != nil || string(data) != "shared" {
		t.Fatal("shared unsent composer reference deleted with first session", err)
	}
	if _, err = os.Stat(filepath.Join(cwd, ordinary)); !os.IsNotExist(err) {
		t.Fatal("ordinary session-upload cleanup changed", err)
	}
	again := informationReuseResolve(t, h, other, e)
	if again.Attachments[0].Path != p {
		t.Fatal("workspace cache lost after original session deletion")
	}
}
