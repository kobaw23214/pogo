package information

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/inputref"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	scope, err := Namespace(nil, root)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(filepath.Join(root, "private"), scope)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func create(t *testing.T, s *Store, rev int64, in CreateInput) (TreeView, *Entry) {
	t.Helper()
	v, e, err := s.Create(rev, in)
	if err != nil {
		t.Fatal(err)
	}
	return v, e
}
func str(s string) *string { return &s }
func TestInformationTreeCASMoveVersionsAndRecursiveDelete(t *testing.T) {
	s := testStore(t)
	v, a := create(t, s, 0, CreateInput{Kind: "folder", Name: "A"})
	v, b := create(t, s, v.Revision, CreateInput{Kind: "folder", Name: "B"})
	v, c := create(t, s, v.Revision, CreateInput{Kind: "folder", ParentID: a.ID, Name: "C"})
	_, _, err := s.Create(0, CreateInput{Kind: "folder", Name: "stale"})
	if !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	_, _, err = s.Create(v.Revision, CreateInput{Kind: "folder", Name: "A"})
	if !errors.Is(err, ErrNameConflict) {
		t.Fatal(err)
	}
	for _, in := range []UpdateInput{{ParentID: &a.ID}, {ParentID: &c.ID}} {
		if _, _, err = s.Update(v.Revision, a.ID, in); err == nil {
			t.Fatal("cycle accepted")
		}
	}
	v, e, err := s.Update(v.Revision, c.ID, UpdateInput{Name: str("renamed"), ParentID: &b.ID})
	if err != nil || e.Version != 2 {
		t.Fatal(err, e)
	}
	old, err := s.Get(c.ID, 1)
	if err != nil || old.Name != "C" || old.ParentID != a.ID {
		t.Fatal("old version changed", err)
	}
	if _, _, err = s.Delete(v.Revision, b.ID, false); !errors.Is(err, ErrNotEmpty) {
		t.Fatal(err)
	}
	v, _, err = s.Delete(v.Revision, b.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(c.ID, 0); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if old, err = s.Get(c.ID, 2); err != nil || old.Name != "renamed" {
		t.Fatal("deleted snapshot lost", err)
	}
	reopened, err := Open(s.Root(), s.ScopeID())
	if err != nil {
		t.Fatal(err)
	}
	again, err := reopened.List()
	if err != nil || again.Revision != v.Revision || len(again.Entries) != 1 {
		t.Fatal(again, err)
	}
}
func TestInformationParentsNamesAndDepth(t *testing.T) {
	s := testStore(t)
	v, folder := create(t, s, 0, CreateInput{Kind: "folder", Name: "same"})
	v, _ = create(t, s, v.Revision, CreateInput{Kind: "folder", ParentID: folder.ID, Name: "same"})
	asset, err := s.PutAsset("f.txt", "", strings.NewReader("x"), 100)
	if err != nil {
		t.Fatal(err)
	}
	v, file := create(t, s, v.Revision, CreateInput{Kind: "file", Name: "file", File: &asset})
	for _, in := range []CreateInput{{Kind: "folder", ParentID: file.ID, Name: "nested"}, {Kind: "folder", ParentID: "inf_" + strings.Repeat("0", 32), Name: "missing"}, {Kind: "folder", Name: "../x"}, {Kind: "folder", Name: "x\x00"}} {
		if _, _, err = s.Create(v.Revision, in); err == nil {
			t.Fatal("invalid tree accepted")
		}
	}
	parent := ""
	for i := 0; i <= MaxDepth; i++ {
		var e *Entry
		v, e = create(t, s, v.Revision, CreateInput{Kind: "folder", ParentID: parent, Name: "depth"})
		parent = e.ID
	}
	if _, _, err = s.Create(v.Revision, CreateInput{Kind: "folder", ParentID: parent, Name: "too deep"}); err == nil {
		t.Fatal("depth limit lost")
	}
}
func TestInformationImmutableAssetIntegrityAndSafeMaterialization(t *testing.T) {
	s := testStore(t)
	payload := bytes.Repeat([]byte("A"), 26<<20)
	asset, err := s.PutAsset("../报告.xlsx", "", bytes.NewReader(payload), MaxAssetBytes)
	if err != nil || asset.Size != int64(len(payload)) {
		t.Fatal("large file capped at 25MB", err)
	}
	_, e := create(t, s, 0, CreateInput{Kind: "file", Name: asset.Name, File: &asset})
	_ = e
	f, a, err := s.AssetRead(asset.Asset)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(f)
	f.Close()
	if !bytes.Equal(got, payload) || a.Name != "报告.xlsx" {
		t.Fatal("bytes changed")
	}
	workspace := t.TempDir()
	p, _, err := s.MaterializeAsset(asset.Asset, workspace)
	if err != nil || !strings.HasPrefix(p, "uploads/information/") {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(workspace, p))
	if !bytes.Equal(data, payload) {
		t.Fatal("materialization differs")
	}
	for _, handle := range []string{"../escape", strings.Replace(asset.Asset, "报告.xlsx", "../escape", 1), strings.Replace(asset.Asset, "information-assets/", "shortcut-assets/", 1)} {
		if _, _, err = s.AssetRead(handle); err == nil {
			t.Fatal("path accepted", handle)
		}
	}
	other := testStore(t)
	if _, _, err = other.AssetRead(asset.Asset); err == nil {
		t.Fatal("cross-owner asset accepted")
	}
	if _, err = s.PutAsset("too-big", "", strings.NewReader("1234"), 3); err == nil {
		t.Fatal("upload limit ignored")
	}
	if err = os.WriteFile(filepath.Join(s.Root(), "assets", asset.SHA256, "data"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.AssetRead(asset.Asset); err == nil {
		t.Fatal("corruption hidden")
	}
}
func TestInformationShortcutSnapshotsMetadataAndSearch(t *testing.T) {
	s := testStore(t)
	asset, err := s.PutAsset("input.txt", "", strings.NewReader("saved bytes"), 100)
	if err != nil {
		t.Fatal(err)
	}
	q := &Shortcut{ID: "db", Title: "数据库", Content: "", Parts: []inputref.Part{{Type: "text", Text: "original query unique-match"}, {Type: "file", Path: asset.Asset}, {Type: "skill", Name: "office"}, {Type: "shortcut", ShortcutID: "nested", Title: "Nested", Text: "frozen"}}}
	v, e := create(t, s, 0, CreateInput{Kind: "shortcut", Name: q.Title, Shortcut: q})
	if v.Entries[0].Shortcut != nil {
		t.Fatal("list leaked full body")
	}
	q.Parts[0].Text = "mutated caller"
	full, err := s.Get(e.ID, 1)
	if err != nil || full.Shortcut.Parts[0].Text != "original query unique-match" {
		t.Fatal("not snapshot", err)
	}
	hits, err := s.Search("unique-match", 20)
	if err != nil || len(hits) != 1 || hits[0].Entry.Shortcut != nil {
		t.Fatal(hits, err)
	}
	hits, err = s.Search("数据库", 20)
	if err != nil || len(hits) != 1 {
		t.Fatal(hits, err)
	}
	bad := *full.Shortcut
	bad.Parts = inputref.Clone(bad.Parts)
	bad.Parts[1].Path = "uploads/private.txt"
	if _, _, err = s.Create(v.Revision, CreateInput{Kind: "shortcut", Name: "bad", Shortcut: &bad}); err == nil {
		t.Fatal("raw private file persisted")
	}
	bad.Parts[1].Path = "information-assets/" + strings.Repeat("0", 64) + "/private.txt"
	if _, _, err = s.Create(v.Revision, CreateInput{Kind: "shortcut", Name: "bad", Shortcut: &bad}); err == nil {
		t.Fatal("unowned library asset persisted")
	}
}
func TestInformationNamespaceIncludesEveryOwnerDimensionAndWorkspace(t *testing.T) {
	a := identity.LocalScope("alice")
	base, err := Namespace(&a, "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	for _, dimension := range []string{"app", "tenant", "subject", "project", "workspace", "nil"} {
		b := a
		workspace := "/workspace"
		owner := &b
		switch dimension {
		case "app":
			b.AppID = "other"
		case "tenant":
			b.TenantID = "other"
		case "subject":
			b.SubjectID = "other"
		case "project":
			b.ProjectID = "other"
		case "workspace":
			workspace = "/other"
		case "nil":
			owner = nil
		}
		scope, err := Namespace(owner, workspace)
		if err != nil || scope == base {
			t.Fatal("owner dimension omitted", dimension, err)
		}
	}
	if _, err = Namespace(&identity.Scope{}, "/workspace"); err == nil {
		t.Fatal("invalid owner fell back")
	}
}
func TestInformationConcurrentCASAndSymlinks(t *testing.T) {
	s := testStore(t)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, name := range []string{"a", "b"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			other, e := Open(s.Root(), s.ScopeID())
			if e == nil {
				_, _, e = other.Create(0, CreateInput{Kind: "folder", Name: name})
			}
			results <- e
		}(name)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal(success, conflict)
	}
	outside := t.TempDir()
	if err := os.RemoveAll(filepath.Join(s.Root(), "assets")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(s.Root(), "assets")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutAsset("escape.txt", "", strings.NewReader("do not escape"), 100); err == nil {
		t.Fatal("asset symlink accepted")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("symlink wrote outside")
	}
	workspace := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspace, "uploads")); err != nil {
		t.Fatal(err)
	}
	clean := testStore(t)
	a, err := clean.PutAsset("x", "", strings.NewReader("x"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = clean.MaterializeAsset(a.Asset, workspace); err == nil {
		t.Fatal("workspace symlink accepted")
	}
}

func TestInformationOrphanVersionNeverOverwritesOrWedgesRetry(t *testing.T) {
	s := testStore(t)
	v, e := create(t, s, 0, CreateInput{Kind: "folder", Name: "before"})
	r, err := os.OpenRoot(s.Root())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	orphan := *e
	orphan.Version = 2
	orphan.Name = "unpublished"
	if err = atomicJSON(r, entryPath(e.ID, 2), orphan, true); err != nil {
		t.Fatal(err)
	}
	_, updated, err := s.Update(v.Revision, e.ID, UpdateInput{Name: str("after")})
	if err != nil || updated.Version != 3 {
		t.Fatal("crash orphan wedged edit", err)
	}
	old, err := s.Get(e.ID, 2)
	if err != nil || old.Name != "unpublished" {
		t.Fatal("immutable orphan was overwritten", err)
	}
}

func TestInformationScopeBoundToDiskIncludesEmptyAndHistoricalReads(t *testing.T) {
	s := testStore(t)
	wrong := strings.Repeat("0", 64)
	if wrong == s.ScopeID() {
		wrong = strings.Repeat("1", 64)
	}
	if _, err := Open(s.Root(), wrong); err == nil {
		t.Fatal("empty root rebound to different scope")
	}
	a, err := s.PutAsset("file.txt", "", strings.NewReader("private"), 100)
	if err != nil {
		t.Fatal(err)
	}
	_, entry := create(t, s, 0, CreateInput{Kind: "file", Name: "file", File: &a})
	if _, err = Open(s.Root(), wrong); err == nil {
		t.Fatal("populated root rebound")
	}
	forged := &Store{root: s.Root(), scope: wrong, mu: s.mu}
	if _, err = forged.Get(entry.ID, 1); err == nil {
		t.Fatal("historical read skipped scope binding")
	}
	if f, _, err := forged.AssetRead(a.Asset); err == nil {
		f.Close()
		t.Fatal("asset read skipped scope binding")
	}
}

func TestInformationShortcutNameAndTitleAreOneVersionedValue(t *testing.T) {
	s := testStore(t)
	v, created := create(t, s, 0, CreateInput{Kind: "shortcut", Name: "Created name", Shortcut: &Shortcut{ID: "snapshot", Title: "Ignored alternate title", Content: "frozen body"}})
	if created.Shortcut.Title != created.Name {
		t.Fatal("create split name/title")
	}
	v, renamed, err := s.Update(v.Revision, created.ID, UpdateInput{Name: str("Renamed")})
	if err != nil || renamed.Shortcut.Title != "Renamed" || renamed.Name != "Renamed" {
		t.Fatal("rename split name/title", err)
	}
	before, err := s.Get(created.ID, 1)
	if err != nil || before.Name != "Created name" || before.Shortcut.Title != "Created name" {
		t.Fatal("old snapshot renamed", err)
	}
	changed := *renamed.Shortcut
	changed.Title = "Edited title"
	v, edited, err := s.Update(v.Revision, created.ID, UpdateInput{Shortcut: &changed})
	if err != nil || edited.Name != "Edited title" || edited.Shortcut.Title != "Edited title" {
		t.Fatal("title-only edit did not rename", err)
	}
	_, explicit, err := s.Update(v.Revision, created.ID, UpdateInput{Name: str("Explicit name"), Shortcut: &changed})
	if err != nil || explicit.Name != "Explicit name" || explicit.Shortcut.Title != "Explicit name" {
		t.Fatal("explicit name not authoritative", err)
	}
	if changed.Title != "Edited title" {
		t.Fatal("mutated caller's draft")
	}
}
