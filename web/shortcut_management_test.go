package web

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/inputref"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/shortcuts"
)

func managementView(t *testing.T, b *shortcutToolBackend) shortcutView {
	t.Helper()
	v, e := b.m.userShortcuts(b.owner, b.agentID)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func managementBase(key string, revision int64) shortcuts.MutationRequest {
	return shortcuts.MutationRequest{RequestID: key, ExpectedRevision: revision}
}
func managementRaw(t *testing.T, b *shortcutToolBackend) []byte {
	t.Helper()
	p, e := shortcutDocPath(b)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(p)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		t.Fatal(e)
	}
	return raw
}
func managementCard(t *testing.T, b *shortcutToolBackend, id string) userShortcut {
	t.Helper()
	for _, e := range managementView(t, b).Shortcuts {
		if e.ID == id {
			return e
		}
	}
	t.Fatal("private card missing", id)
	return userShortcut{}
}
func managementFolder(t *testing.T, b *shortcutToolBackend, key, name, parent string) shortcuts.MutationResult {
	t.Helper()
	r, e := b.FolderCreate(context.Background(), shortcuts.FolderCreateRequest{MutationRequest: managementBase(key, managementView(t, b).Revision), Name: name, ParentID: parent})
	if e != nil {
		t.Fatal(e)
	}
	return r
}

func TestShortcutManagementTreeMoveMetadataAndAtomicCAS(t *testing.T) {
	_, _, b := shortcutToolFixture(t)
	ctx := context.Background()
	req := shortcutToolSimpleRequest("nested-seed", "Original")
	req.Nodes[0].Parts = []shortcuts.Part{{Kind: "text", Text: "before"}, {Kind: "node", NodeID: "child"}, {Kind: "text", Text: "after"}}
	req.Nodes = append(req.Nodes, shortcuts.Node{LocalID: "child", Title: "Frozen child", Parts: []shortcuts.Part{{Kind: "text", Text: "child text"}}})
	created, e := b.Create(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	original := managementCard(t, b, created.Shortcut.ID)
	folder := managementFolder(t, b, "folder-root", "Projects", "")
	child := managementFolder(t, b, "folder-child", "Nested", folder.Folder.ID)
	v := managementView(t, b)
	moved, e := b.Move(ctx, shortcuts.MoveRequest{MutationRequest: managementBase("move-card", v.Revision), IDs: []string{original.ID}, ParentID: child.Folder.ID})
	if e != nil || !moved.Changed || len(moved.Changes) != 1 {
		t.Fatal(moved, e)
	}
	after := managementCard(t, b, original.ID)
	if !reflect.DeepEqual(original.Parts, after.Parts) || original.Content != after.Content || after.ParentID != child.Folder.ID {
		t.Fatal("move modified card contents")
	}
	v = managementView(t, b)
	before := managementRaw(t, b)
	for _, request := range []shortcuts.MoveRequest{
		{MutationRequest: managementBase("cycle", v.Revision), IDs: []string{folder.Folder.ID}, ParentID: child.Folder.ID},
		{MutationRequest: managementBase("ancestor-child", v.Revision), IDs: []string{folder.Folder.ID, original.ID}, ParentID: ""},
		{MutationRequest: managementBase("foreign", v.Revision), IDs: []string{original.ID, "foreign_card"}, ParentID: ""},
		{MutationRequest: managementBase("duplicate", v.Revision), IDs: []string{original.ID, original.ID}, ParentID: ""},
	} {
		if _, e = b.Move(ctx, request); e == nil {
			t.Fatal("invalid batch move accepted", request.RequestID)
		}
		if !bytes.Equal(before, managementRaw(t, b)) {
			t.Fatal("failed batch move changed library")
		}
	}
	renamed, e := b.FolderRename(ctx, shortcuts.FolderRenameRequest{MutationRequest: managementBase("rename-folder", v.Revision), ID: child.Folder.ID, Name: "Renamed"})
	if e != nil || renamed.Folder == nil || renamed.Folder.ID != child.Folder.ID {
		t.Fatal(renamed, e)
	}
	title := "Updated title"
	updated, e := b.Update(ctx, shortcuts.UpdateRequest{MutationRequest: managementBase("metadata", renamed.Revision), ID: original.ID, Title: &title})
	if e != nil || updated.Shortcut == nil || updated.Shortcut.ID != original.ID {
		t.Fatal(updated, e)
	}
	after = managementCard(t, b, original.ID)
	if after.Title != title || !reflect.DeepEqual(original.Parts, after.Parts) || after.Content != original.Content || after.InputLayout != original.InputLayout {
		t.Fatal("metadata update changed mixed snapshot")
	}
	if _, e = b.Update(ctx, shortcuts.UpdateRequest{MutationRequest: managementBase("stale", renamed.Revision), ID: original.ID, Title: &title}); !errors.Is(e, errShortcutsConflict) {
		t.Fatal(e)
	}
	other := managementFolder(t, b, "other-folder", "Other", "")
	occupied := managementFolder(t, b, "occupied-folder", "Projects", other.Folder.ID)
	beforeCollision := managementRaw(t, b)
	if _, e = b.Move(ctx, shortcuts.MoveRequest{MutationRequest: managementBase("name-collision", occupied.Revision), IDs: []string{folder.Folder.ID}, ParentID: other.Folder.ID}); e == nil || !strings.Contains(e.Error(), "同名") {
		t.Fatal("same-name destination folder accepted", e)
	}
	if !bytes.Equal(beforeCollision, managementRaw(t, b)) {
		t.Fatal("folder name collision changed tree")
	}
	cleared, e := b.FolderRename(ctx, shortcuts.FolderRenameRequest{MutationRequest: managementBase("clear-name", occupied.Revision), ID: occupied.Folder.ID, Name: "Different"})
	if e != nil {
		t.Fatal(e)
	}
	whole, e := b.Move(ctx, shortcuts.MoveRequest{MutationRequest: managementBase("whole-folder", cleared.Revision), IDs: []string{folder.Folder.ID}, ParentID: other.Folder.ID})
	if e != nil || !whole.Changed {
		t.Fatal(whole, e)
	}
	if managementCard(t, b, original.ID).ParentID != child.Folder.ID {
		t.Fatal("moving parent rewrote descendant")
	}
}

func TestShortcutManagementDurableReplayNoopAndNoResurrection(t *testing.T) {
	h, s, b := shortcutToolFixture(t)
	ctx := context.Background()
	request := shortcuts.FolderCreateRequest{MutationRequest: managementBase("first-request", 0), Name: "Initial"}
	first, e := b.FolderCreate(ctx, request)
	if e != nil {
		t.Fatal(e)
	}
	renamed, e := b.FolderRename(ctx, shortcuts.FolderRenameRequest{MutationRequest: managementBase("rename", first.Revision), ID: first.Folder.ID, Name: "Human changed"})
	if e != nil {
		t.Fatal(e)
	}
	replayed, e := h.mgr.shortcutToolBackend(s).FolderCreate(ctx, request)
	if e != nil || !replayed.Reused || replayed.Changed || replayed.AppliedRevision != first.Revision || replayed.Revision != renamed.Revision || replayed.Folder.Name != "Human changed" {
		t.Fatal(replayed, e)
	}
	noop := shortcuts.FolderRenameRequest{MutationRequest: managementBase("noop", renamed.Revision), ID: first.Folder.ID, Name: "Human changed"}
	unchanged, e := b.FolderRename(ctx, noop)
	if e != nil || unchanged.Changed || unchanged.Revision != renamed.Revision+1 {
		t.Fatal(unchanged, e)
	}
	bad := noop
	bad.Name = "Wrong reuse"
	if _, e = b.FolderRename(ctx, bad); e == nil {
		t.Fatal("no-op request allowed changed payload")
	}
	deletedReq := shortcuts.DeleteRequest{MutationRequest: managementBase("delete-empty", unchanged.Revision), IDs: []string{first.Folder.ID}}
	deleted, e := b.Delete(ctx, deletedReq)
	if e != nil {
		t.Fatal(e)
	}
	for _, backend := range []*shortcutToolBackend{b, h.mgr.shortcutToolBackend(s)} {
		again, e := backend.Delete(ctx, deletedReq)
		if e != nil || !again.Reused || again.Changed || again.Revision != deleted.Revision {
			t.Fatal(again, e)
		}
		again, e = backend.FolderCreate(ctx, request)
		if e != nil || !again.Reused || again.Folder != nil {
			t.Fatal("deleted folder resurrected or retry failed", again, e)
		}
	}
	view := managementView(t, b)
	if len(view.Folders) != 0 || view.Revision != deleted.Revision {
		t.Fatal(view)
	}
	if _, e = b.FolderRename(ctx, shortcuts.FolderRenameRequest{MutationRequest: managementBase("first-request", view.Revision), ID: first.Folder.ID, Name: "cross-action"}); e == nil {
		t.Fatal("request ID reused across actions")
	}
}

func TestShortcutManagementDeleteRecursiveAndReadonlyIsolation(t *testing.T) {
	h, s, b := shortcutToolFixture(t)
	ctx := context.Background()
	f := managementFolder(t, b, "root", "Private", "")
	sub := managementFolder(t, b, "sub", "Sub", f.Folder.ID)
	cardReq := shortcutToolSimpleRequest("card", "Private card")
	cardReq.ParentID = sub.Folder.ID
	card, e := b.Create(ctx, cardReq)
	if e != nil {
		t.Fatal(e)
	}
	view := managementView(t, b)
	before := managementRaw(t, b)
	if _, e = b.Delete(ctx, shortcuts.DeleteRequest{MutationRequest: managementBase("nonrecursive", view.Revision), IDs: []string{f.Folder.ID}}); e == nil {
		t.Fatal("nonempty directory deleted without explicit recursion")
	}
	if !bytes.Equal(before, managementRaw(t, b)) {
		t.Fatal("failed delete changed library")
	}
	for _, id := range []string{shortcuts.InformationFolderID, shortcuts.QuickCardsFolderID, "@information/inf_private", "@quick-cards/starter"} {
		if _, e = b.Delete(ctx, shortcuts.DeleteRequest{MutationRequest: managementBase("readonly-delete", view.Revision), IDs: []string{card.Shortcut.ID, id}, Recursive: true}); e == nil {
			t.Fatal("readonly source batch accepted", id)
		}
		if _, e = b.Move(ctx, shortcuts.MoveRequest{MutationRequest: managementBase("readonly-move", view.Revision), IDs: []string{card.Shortcut.ID}, ParentID: id}); e == nil {
			t.Fatal("readonly parent accepted", id)
		}
		if _, e = b.FolderCreate(ctx, shortcuts.FolderCreateRequest{MutationRequest: managementBase("readonly-parent", view.Revision), Name: "Forbidden", ParentID: id}); e == nil {
			t.Fatal("source child created", id)
		}
	}
	other := createStudioAgent(t, h, "Other private Agent", "", "ready")
	otherSID := createStudioConversationBody(t, h, other.ID, "{}").ID
	foreign := h.mgr.shortcutToolBackend(h.session(otherSID))
	foreignCard, e := foreign.Create(ctx, shortcutToolSimpleRequest("foreign-root", "Foreign"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = b.Delete(ctx, shortcuts.DeleteRequest{MutationRequest: managementBase("foreign-delete", view.Revision), IDs: []string{card.Shortcut.ID, foreignCard.Shortcut.ID}, Recursive: true}); e == nil {
		t.Fatal("foreign ID batch accepted")
	}
	if !bytes.Equal(before, managementRaw(t, b)) {
		t.Fatal("invalid source mutation changed private library")
	}
	removed, e := b.Delete(ctx, shortcuts.DeleteRequest{MutationRequest: managementBase("recursive", view.Revision), IDs: []string{f.Folder.ID}, Recursive: true})
	if e != nil || len(removed.Changes) != 3 {
		t.Fatal(removed, e)
	}
	if len(managementView(t, b).Shortcuts) != 0 || len(managementView(t, b).Folders) != 0 {
		t.Fatal("recursive delete left descendants")
	}
	if len(managementView(t, foreign).Shortcuts) != 1 {
		t.Fatal("other Agent changed")
	}
	_ = s
}

func TestShortcutManagementGraphAssetsRollbackAndFrozenHistory(t *testing.T) {
	h, s, b := shortcutToolFixture(t)
	ctx := context.Background()
	if e := os.MkdirAll(filepath.Join(s.cwd, "uploads"), 0700); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"first.txt", "second.txt"} {
		if e := os.WriteFile(filepath.Join(s.cwd, "uploads", name), []byte("source "+name), 0600); e != nil {
			t.Fatal(e)
		}
	}
	s.hub.Publish(Event{Type: EvUserMessage, Content: []llm.Block{{Type: llm.BlockText, FilePath: "uploads/first.txt"}, {Type: llm.BlockText, FilePath: "uploads/second.txt"}}})
	firstRef := shortcutToolFind(t, b, "file", "first.txt")
	req := shortcutToolSimpleRequest("initial-asset", "Original")
	req.Nodes[0].Parts = []shortcuts.Part{{Kind: "ref", RefID: firstRef.RefID}}
	first, e := b.Create(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	snapshotRef := shortcutToolFind(t, b, "shortcut", "Original")
	outerReq := shortcutToolSimpleRequest("frozen-parent", "Frozen parent")
	outerReq.Nodes[0].Parts = []shortcuts.Part{{Kind: "ref", RefID: snapshotRef.RefID}}
	outer, e := b.Create(ctx, outerReq)
	if e != nil {
		t.Fatal(e)
	}
	frozen := inputref.Clone(outer.Shortcut.Parts)
	secondRef := shortcutToolFind(t, b, "file", "second.txt")
	graph := shortcuts.Graph{RootID: "root", Nodes: []shortcuts.Node{{LocalID: "root", Title: "Revised", Parts: []shortcuts.Part{{Kind: "text", Text: "before"}, {Kind: "ref", RefID: secondRef.RefID}, {Kind: "node", NodeID: "child"}, {Kind: "text", Text: "after"}}}, {LocalID: "child", Title: "Child", Parts: []shortcuts.Part{{Kind: "text", Text: "nested text"}}}}}
	namespace, _ := h.mgr.shortcutAssetNamespace(s.owner, s.studioID)
	dirsBefore, e := os.ReadDir(namespace)
	if e != nil {
		t.Fatal(e)
	}
	rawBefore := managementRaw(t, b)
	v := managementView(t, b)
	if _, e = b.Update(ctx, shortcuts.UpdateRequest{MutationRequest: managementBase("missing-after-stage", v.Revision), ID: "private_missing", Graph: &graph}); e == nil {
		t.Fatal("missing graph target accepted")
	}
	dirsAfter, _ := os.ReadDir(namespace)
	if len(dirsBefore) != len(dirsAfter) || !bytes.Equal(rawBefore, managementRaw(t, b)) {
		t.Fatal("failed graph update leaked assets or changed library")
	}
	update, e := b.Update(ctx, shortcuts.UpdateRequest{MutationRequest: managementBase("graph-update", v.Revision), ID: first.Shortcut.ID, Graph: &graph})
	if e != nil || update.Shortcut == nil || update.Shortcut.ID != first.Shortcut.ID || len(update.Shortcut.Parts) != 4 {
		t.Fatal(update, e)
	}
	if update.Shortcut.Parts[0].Text != "before" || update.Shortcut.Parts[2].Parts[0].Text != "nested text" || update.Shortcut.Parts[3].Text != "after" {
		t.Fatal("graph order lost")
	}
	if !reflect.DeepEqual(managementCard(t, b, outer.Shortcut.ID).Parts, frozen) {
		t.Fatal("updating source rewrote another saved snapshot")
	}
	if _, e = b.Update(ctx, shortcuts.UpdateRequest{MutationRequest: managementBase("old-cas", v.Revision), ID: first.Shortcut.ID, Graph: &graph}); !errors.Is(e, errShortcutsConflict) {
		t.Fatal(e)
	}
	selfGraph := shortcuts.Graph{RootID: "root", Nodes: []shortcuts.Node{{LocalID: "root", Title: "Self", Parts: []shortcuts.Part{{Kind: "ref", RefID: snapshotRef.RefID}}}}}
	if _, e = b.Update(ctx, shortcuts.UpdateRequest{MutationRequest: managementBase("self-cycle", update.Revision), ID: first.Shortcut.ID, Graph: &selfGraph}); e == nil || !strings.Contains(e.Error(), "shortcut_query") || !strings.Contains(e.Error(), "read") {
		t.Fatal("self snapshot update did not explain safe editing", e)
	}
	removed, e := b.Delete(ctx, shortcuts.DeleteRequest{MutationRequest: managementBase("delete-source", update.Revision), IDs: []string{first.Shortcut.ID}})
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(managementCard(t, b, outer.Shortcut.ID).Parts, frozen) {
		t.Fatal("delete damaged historical snapshot")
	}
	dirsAfter, _ = os.ReadDir(namespace)
	if len(dirsAfter) != 2 {
		t.Fatal("delete removed owned referenced assets", len(dirsAfter))
	}
	if removed.Changes[0].Action != "delete" {
		t.Fatal(removed)
	}
	for _, name := range []string{"first.txt", "second.txt"} {
		raw, e := os.ReadFile(filepath.Join(s.cwd, "uploads", name))
		if e != nil || string(raw) != "source "+name {
			t.Fatal("source file changed")
		}
	}
}

func TestShortcutManagementConcurrentCASAndIdempotency(t *testing.T) {
	for _, same := range []bool{false, true} {
		t.Run(map[bool]string{true: "same-key", false: "different-key"}[same], func(t *testing.T) {
			h, s, b := shortcutToolFixture(t)
			other := h.mgr.shortcutToolBackend(s)
			var wg sync.WaitGroup
			type outcome struct {
				result shortcuts.MutationResult
				err    error
			}
			results := make(chan outcome, 2)
			for i, backend := range []*shortcutToolBackend{b, other} {
				wg.Add(1)
				go func(i int, backend *shortcutToolBackend) {
					defer wg.Done()
					key := []string{"key-a", "key-b"}[i]
					name := []string{"A", "B"}[i]
					if same {
						key = "same-key"
						name = "Same"
					}
					r, e := backend.FolderCreate(context.Background(), shortcuts.FolderCreateRequest{MutationRequest: managementBase(key, 0), Name: name})
					results <- outcome{r, e}
				}(i, backend)
			}
			wg.Wait()
			success, conflicts, reused := 0, 0, 0
			for range 2 {
				item := <-results
				r, e := item.result, item.err
				if e == nil {
					success++
					if r.Reused {
						reused++
					}
				} else if errors.Is(e, errShortcutsConflict) {
					conflicts++
				} else {
					t.Fatal(e)
				}
			}
			view := managementView(t, b)
			if len(view.Folders) != 1 || view.Revision != 1 {
				t.Fatal(view)
			}
			if same {
				if success != 2 || reused != 1 {
					t.Fatal(success, reused)
				}
			} else if success != 1 || conflicts != 1 {
				t.Fatal(success, conflicts)
			}
		})
	}
}

func TestShortcutManagementMergeExactPreviewPlanCASAndReplay(t *testing.T) {
	h, s, b := shortcutToolFixture(t)
	ctx := context.Background()
	part := []inputref.Part{{Type: "text", Text: "same body"}}
	base := userShortcut{ID: "card_a", Title: "Duplicate", Content: "same body", Parts: part, InputLayout: "inline"}
	second := base
	second.ID = "card_b"
	different := base
	different.ID = "card_different"
	different.Content = "different body"
	different.Parts = []inputref.Part{{Type: "text", Text: "different body"}}
	otherParent := base
	otherParent.ID = "card_other_parent"
	otherParent.ParentID = "folder_other"
	differentIcon := base
	differentIcon.ID = "card_icon"
	differentIcon.Icon = "star"
	differentLayout := base
	differentLayout.ID = "card_layout"
	differentLayout.InputLayout = "top"
	firstNested := base
	firstNested.ID = "card_nested_a"
	firstNested.Parts = []inputref.Part{{Type: "shortcut", ShortcutID: "snapshot_a", Title: "Child", Text: "same body", Parts: part}}
	secondNested := firstNested
	secondNested.ID = "card_nested_b"
	secondNested.Parts = inputref.Clone(firstNested.Parts)
	secondNested.Parts[0].ShortcutID = "snapshot_b"
	outer := userShortcut{ID: "history_parent", Title: "History", Content: "snapshot", InputLayout: "inline", Parts: shortcutToolParts(second)}
	entries := []userShortcut{second, base, different, otherParent, differentIcon, differentLayout, firstNested, secondNested, outer}
	view, e := h.mgr.replaceUserShortcuts(s.owner, s.studioID, 0, entries, []shortcutFolder{{ID: "folder_other", Name: "Other"}})
	if e != nil {
		t.Fatal(e)
	}
	rawBefore := managementRaw(t, b)
	preview, e := b.Merge(ctx, shortcuts.MergeRequest{})
	if e != nil || preview.Saved || preview.Plan == nil || len(preview.Plan.Groups) != 1 || preview.Plan.Removals != 1 || preview.Plan.Groups[0].KeepID != "card_a" || !slices.Equal(preview.Plan.Groups[0].DuplicateIDs, []string{"card_b"}) {
		t.Fatal(preview, e)
	}
	if !bytes.Equal(rawBefore, managementRaw(t, b)) {
		t.Fatal("preview wrote library")
	}
	apply := shortcuts.MergeRequest{MutationRequest: managementBase("merge-apply", view.Revision), Apply: true, PlanID: preview.Plan.ID}
	merged, e := b.Merge(ctx, apply)
	if e != nil || !merged.Saved || !merged.Changed || len(merged.Changes) != 1 || merged.Changes[0].ID != "card_b" || merged.Plan.Revision != view.Revision {
		t.Fatal(merged, e)
	}
	if len(managementView(t, b).Shortcuts) != len(entries)-1 || !reflect.DeepEqual(managementCard(t, b, outer.ID).Parts, outer.Parts) {
		t.Fatal("merge removed nonidentical card or changed historical snapshot")
	}
	b.managementPlans = nil
	retry, e := h.mgr.shortcutToolBackend(s).Merge(ctx, apply)
	if e != nil || !retry.Reused || retry.Changed || retry.AppliedRevision != merged.Revision || retry.Revision != merged.Revision {
		t.Fatal(retry, e)
	}
	preview, e = b.Merge(ctx, shortcuts.MergeRequest{})
	if e != nil || preview.Plan.Removals != 0 {
		t.Fatal(preview, e)
	}
	noOp, e := b.Merge(ctx, shortcuts.MergeRequest{MutationRequest: managementBase("merge-noop", merged.Revision), Apply: true, PlanID: preview.Plan.ID})
	if e != nil || noOp.Changed || noOp.Revision != merged.Revision+1 || len(noOp.Changes) != 0 {
		t.Fatal(noOp, e)
	}
	if _, e = b.Merge(ctx, shortcuts.MergeRequest{MutationRequest: managementBase("forged-plan", noOp.Revision), Apply: true, PlanID: "smp_forged"}); e == nil {
		t.Fatal("unissued plan accepted")
	}
	stale, e := b.Merge(ctx, shortcuts.MergeRequest{})
	if e != nil {
		t.Fatal(e)
	}
	managementFolder(t, b, "new-directory", "New", "")
	if _, e = b.Merge(ctx, shortcuts.MergeRequest{MutationRequest: managementBase("stale-plan", stale.Revision), Apply: true, PlanID: stale.Plan.ID}); !errors.Is(e, errShortcutsConflict) {
		t.Fatal(e)
	}
	fresh, e := b.Merge(ctx, shortcuts.MergeRequest{})
	if e != nil {
		t.Fatal(e)
	}
	cached := b.managementPlans[fresh.Plan.ID]
	cached.Expires = time.Now().Add(-time.Second)
	b.managementPlans[fresh.Plan.ID] = cached
	if _, e = b.Merge(ctx, shortcuts.MergeRequest{MutationRequest: managementBase("expired-plan", fresh.Revision), Apply: true, PlanID: fresh.Plan.ID}); e == nil {
		t.Fatal("expired plan accepted")
	}
}

func TestShortcutManagementWriteFailureRetainsLibraryAndRequest(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-based write refusal cannot be induced as root")
	}
	_, _, b := shortcutToolFixture(t)
	created := managementFolder(t, b, "create", "Original", "")
	before := managementRaw(t, b)
	p, err := shortcutDocPath(b)
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(p)
	st, err := os.Stat(parent)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(parent, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, st.Mode().Perm()) })
	request := shortcuts.FolderRenameRequest{MutationRequest: managementBase("write-refusal", created.Revision), ID: created.Folder.ID, Name: "Later"}
	seq := b.s.hub.Snapshot().Seq
	if result, e := b.FolderRename(context.Background(), request); e == nil || result.Saved {
		t.Fatal("write failure was reported as saved", result, e)
	}
	if !bytes.Equal(before, managementRaw(t, b)) || seq != b.s.hub.Snapshot().Seq {
		t.Fatal("failed write changed library or published success")
	}
	if err = os.Chmod(parent, st.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	retried, err := b.FolderRename(context.Background(), request)
	if err != nil || retried.Reused || retried.Revision != created.Revision+1 {
		t.Fatal("failed request was consumed or could not be retried", retried, err)
	}
}
