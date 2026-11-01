package web

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/shortcuts"
	"github.com/yosukeno/pogo/tools"
)

// Tool receipt validation and the real store must agree, especially after an
// intervening human edit. A backend-only idempotency test cannot detect this.
func TestShortcutManagementRealRegistryReplaysPreserveLaterEdits(t *testing.T) {
	h, s, b := shortcutToolFixture(t)
	ctx := context.Background()
	registry := tools.New(tools.Options{Cwd: s.cwd, SpillDir: t.TempDir(), Shortcuts: b})
	execute := func(name string, request any) tools.ShortcutDetails {
		t.Helper()
		raw, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		// The model-facing registry now groups these operations; the typed
		// backend requests and their replay receipts retain their contracts.
		action := map[string]string{tools.ShortcutFolderCreateName: "folder_create", tools.ShortcutFolderRenameName: "folder_rename", tools.ShortcutUpdateName: "update"}[name]
		toolName := name
		if action != "" {
			toolName = tools.ShortcutManageName
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			fields["action"], _ = json.Marshal(action)
			raw, err = json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
		}
		tool, exists := registry.Get(toolName)
		if !exists {
			t.Fatal("host tool missing", toolName)
		}
		result, err := tool.Execute(ctx, raw)
		if err != nil {
			t.Fatalf("real backend/tool contract failed for %s: %v", name, err)
		}
		details, ok := result.Details.(tools.ShortcutDetails)
		if !ok || !details.Saved || details.AgentID != s.studioID {
			t.Fatal("missing genuine saved receipt", result.Details)
		}
		return details
	}
	view := func() shortcutView {
		t.Helper()
		v, err := h.mgr.userShortcuts(s.owner, s.studioID)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	create := shortcuts.FolderCreateRequest{MutationRequest: shortcuts.MutationRequest{RequestID: "registry-folder-create", ExpectedRevision: 0}, Name: "Original folder"}
	first := execute(tools.ShortcutFolderCreateName, create)
	id := first.Folder.ID
	rename := shortcuts.FolderRenameRequest{MutationRequest: shortcuts.MutationRequest{RequestID: "registry-folder-rename", ExpectedRevision: view().Revision}, ID: id, Name: "First rename"}
	execute(tools.ShortcutFolderRenameName, rename)
	if _, err := b.FolderRename(ctx, shortcuts.FolderRenameRequest{MutationRequest: shortcuts.MutationRequest{RequestID: "human-folder-edit", ExpectedRevision: view().Revision}, ID: id, Name: "Human latest name"}); err != nil {
		t.Fatal(err)
	}
	current := view()
	for _, request := range []struct {
		name string
		args any
	}{{tools.ShortcutFolderCreateName, create}, {tools.ShortcutFolderRenameName, rename}} {
		replayed := execute(request.name, request.args)
		if !replayed.Reused || replayed.Changed || replayed.Folder == nil || replayed.Folder.ID != id || replayed.Folder.Name != "Human latest name" || view().Revision != current.Revision {
			t.Fatal("replayed folder operation overwrote or misrepresented the later human edit", replayed)
		}
	}
	if _, err := b.Delete(ctx, shortcuts.DeleteRequest{MutationRequest: shortcuts.MutationRequest{RequestID: "human-folder-delete", ExpectedRevision: current.Revision}, IDs: []string{id}}); err != nil {
		t.Fatal(err)
	}
	current = view()
	for _, request := range []struct {
		name string
		args any
	}{{tools.ShortcutFolderCreateName, create}, {tools.ShortcutFolderRenameName, rename}} {
		replayed := execute(request.name, request.args)
		if !replayed.Reused || replayed.Changed || replayed.Folder != nil || len(view().Folders) != 0 || view().Revision != current.Revision {
			t.Fatal("replayed folder operation resurrected a deleted target", replayed)
		}
	}
	card, err := b.Create(ctx, shortcutToolSimpleRequest("registry-card", "Original card"))
	if err != nil {
		t.Fatal(err)
	}
	title := "Agent edited title"
	update := shortcuts.UpdateRequest{MutationRequest: shortcuts.MutationRequest{RequestID: "registry-card-update", ExpectedRevision: view().Revision}, ID: card.Shortcut.ID, Title: &title}
	execute(tools.ShortcutUpdateName, update)
	current = view()
	current.Shortcuts[0].Title, current.Shortcuts[0].Content, current.Shortcuts[0].Parts = "Human card title", "Human body must survive", nil
	if _, err := h.mgr.replaceUserShortcuts(s.owner, s.studioID, current.Revision, current.Shortcuts); err != nil {
		t.Fatal(err)
	}
	current = view()
	replayed := execute(tools.ShortcutUpdateName, update)
	if !replayed.Reused || replayed.Changed || replayed.Shortcut == nil || replayed.Shortcut.Title != "Human card title" || replayed.Shortcut.Content != "Human body must survive" || view().Revision != current.Revision {
		t.Fatal("update retry failed to preserve the current human content", replayed)
	}
	if _, err := b.Delete(ctx, shortcuts.DeleteRequest{MutationRequest: shortcuts.MutationRequest{RequestID: "human-card-delete", ExpectedRevision: current.Revision}, IDs: []string{card.Shortcut.ID}}); err != nil {
		t.Fatal(err)
	}
	current = view()
	replayed = execute(tools.ShortcutUpdateName, update)
	if !replayed.Reused || replayed.Changed || replayed.Shortcut != nil || len(view().Shortcuts) != 0 || view().Revision != current.Revision {
		t.Fatal("update retry resurrected the removed card", replayed)
	}
	list, _ := registry.Get(tools.ShortcutQueryName)
	result, err := list.Execute(ctx, json.RawMessage(`{"action":"list","offset":100,"limit":1}`))
	if err != nil || !strings.Contains(result.Text, `"entries":[]`) {
		t.Fatal("real directory end-of-pagination receipt rejected", result, err)
	}
}
