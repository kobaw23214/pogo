package web

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/information"
	"github.com/yosukeno/pogo/shortcuts"
)

func TestShortcutListDirectoryPaginationAndFrozenRead(t *testing.T) {
	h, s, b := shortcutToolFixture(t)
	ctx := context.Background()
	folders := []shortcutFolder{{ID: "folder", Name: "数据库"}, {ID: "child", Name: "子目录", ParentID: "folder"}}
	entries := []userShortcut{{ID: "root-card", Title: "根卡片", Content: "root"}, {ID: "nested-card", Title: "目录里的卡片", Content: "nested exact", ParentID: "folder"}, {ID: "deep-card", Title: "深层卡片", Content: "deep", ParentID: "child"}}
	if _, err := h.mgr.replaceUserShortcuts(s.owner, s.studioID, 0, entries, folders); err != nil {
		t.Fatal(err)
	}
	root, err := b.List(ctx, shortcuts.ListRequest{})
	if err != nil || root.Revision != 1 || root.Total != 4 || root.ReadOnly || len(root.Breadcrumbs) != 0 {
		t.Fatalf("unexpected root: %+v %v", root, err)
	}
	ids := map[string]bool{}
	for _, entry := range root.Entries {
		ids[entry.ID] = true
		if entry.ID == shortcuts.InformationFolderID || entry.ID == shortcuts.QuickCardsFolderID {
			if !entry.ReadOnly || entry.Kind != "folder" {
				t.Fatal("preset directory did not remain a read-only source")
			}
		}
	}
	if !ids["folder"] || !ids["root-card"] || ids["nested-card"] || ids["child"] {
		t.Fatal("root listing included descendants")
	}
	var paged []shortcuts.ReferenceSummary
	for offset := 0; ; {
		page, e := b.List(ctx, shortcuts.ListRequest{Offset: offset, Limit: 1})
		if e != nil || page.Total != root.Total || len(page.Entries) != 1 {
			t.Fatal("bounded pagination failed", page, e)
		}
		paged = append(paged, page.Entries...)
		if page.NextOffset == nil {
			break
		}
		offset = *page.NextOffset
	}
	if !reflect.DeepEqual(paged, root.Entries) {
		t.Fatal("pagination omitted or reordered entries")
	}
	nested, err := b.List(ctx, shortcuts.ListRequest{ParentID: "folder"})
	if err != nil || nested.Total != 2 || len(nested.Breadcrumbs) != 1 || nested.Breadcrumbs[0].ID != "folder" {
		t.Fatalf("folder view: %+v %v", nested, err)
	}
	var ref string
	for _, entry := range nested.Entries {
		if entry.ID == "nested-card" {
			ref = entry.RefID
		}
	}
	read, err := b.Read(ctx, shortcuts.ReadRequest{RefID: ref})
	if err != nil || read.Reference.ID != "nested-card" || read.Parts[0].Text != "nested exact" || read.Graph == nil {
		t.Fatal("listed card was not readable as an editable frozen snapshot", read, err)
	}
	deep, err := b.List(ctx, shortcuts.ListRequest{ParentID: "child"})
	if err != nil || len(deep.Breadcrumbs) != 2 || deep.Breadcrumbs[0].ID != "folder" || deep.Breadcrumbs[1].ID != "child" || deep.Total != 1 {
		t.Fatal("breadcrumb ancestry differs", deep, err)
	}
	for _, bad := range []shortcuts.ListRequest{{ParentID: "missing"}, {ParentID: "root-card"}, {ParentID: "../other"}, {ParentID: "@unknown"}, {Offset: -1}, {Limit: 101}} {
		if _, err = b.List(ctx, bad); err == nil {
			t.Fatal("invalid directory request accepted", bad)
		}
	}
	current, _ := h.mgr.userShortcuts(s.owner, s.studioID)
	if current.Revision != 1 || !reflect.DeepEqual(current.Shortcuts, entries) || !reflect.DeepEqual(current.Folders, folders) {
		t.Fatal("browsing changed the private tree")
	}
}

func TestShortcutListReadOnlySourcesAndPrivateIsolation(t *testing.T) {
	h, s, b := shortcutToolFixture(t)
	ctx := context.Background()
	store, err := h.mgr.InformationStore(s.owner)
	if err != nil {
		t.Fatal(err)
	}
	v, folder, err := store.Create(0, information.CreateInput{Kind: "folder", Name: "参考文档"})
	if err != nil {
		t.Fatal(err)
	}
	asset, err := store.PutAsset("reference.txt", "", strings.NewReader("immutable list reference\n"), information.MaxAssetBytes)
	if err != nil {
		t.Fatal(err)
	}
	_, file, err := store.Create(v.Revision, information.CreateInput{ParentID: folder.ID, Kind: "file", Name: "参考文件", File: &asset})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := store.List()
	info, err := b.List(ctx, shortcuts.ListRequest{ParentID: shortcuts.InformationFolderID})
	if err != nil || !info.ReadOnly || info.Total != 1 || info.Entries[0].ID != shortcuts.InformationFolderID+"/"+folder.ID || !info.Entries[0].ReadOnly {
		t.Fatal("information source root differs", info, err)
	}
	child, err := b.List(ctx, shortcuts.ListRequest{ParentID: info.Entries[0].ID})
	if err != nil || !child.ReadOnly || child.Total != 1 || child.Entries[0].ID != shortcuts.InformationFolderID+"/"+file.ID || !child.Entries[0].ReadOnly || len(child.Breadcrumbs) != 2 {
		t.Fatal("information nested directory differs", child, err)
	}
	read, err := b.Read(ctx, shortcuts.ReadRequest{RefID: child.Entries[0].RefID})
	if err != nil || read.Parts[0].Path != asset.Asset || read.Reference.ReadOnly != true {
		t.Fatal("listed source must remain a readable frozen reference", read, err)
	}
	if _, err = b.List(ctx, shortcuts.ListRequest{ParentID: child.Entries[0].ID}); err == nil {
		t.Fatal("file accepted as a directory")
	}
	cards, err := b.List(ctx, shortcuts.ListRequest{ParentID: shortcuts.QuickCardsFolderID})
	if err != nil || !cards.ReadOnly || cards.Total == 0 {
		t.Fatal("quick cards read-only source absent", cards, err)
	}
	for _, card := range cards.Entries {
		if !card.ReadOnly || !strings.HasPrefix(card.ID, shortcuts.QuickCardsFolderID+"/") || card.RefID == "" {
			t.Fatal("quick card confused with a mutable private entry", card)
		}
	}
	after, _ := store.List()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("source listing changed the Information Library")
	}
	private, _ := h.mgr.userShortcuts(s.owner, s.studioID)
	if private.Revision != 0 || len(private.Shortcuts) != 0 || len(private.Folders) != 0 {
		t.Fatal("source listing copied entries into the private tree")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = b.List(cancelled, shortcuts.ListRequest{}); err == nil {
		t.Fatal("cancelled listing proceeded")
	}
	s.mu.Lock()
	s.studioID = "different-Agent"
	s.mu.Unlock()
	if _, err = b.List(ctx, shortcuts.ListRequest{}); err == nil {
		t.Fatal("listing escaped its bound Agent")
	}
}
