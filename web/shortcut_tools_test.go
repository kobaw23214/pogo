package web

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/information"
	"github.com/yosukeno/pogo/inputref"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/shortcuts"
	"github.com/yosukeno/pogo/skills"
)

func shortcutToolFixture(t *testing.T) (*harness, *Session, *shortcutToolBackend) {
	t.Helper()
	sk := writeTestSkill(t, t.TempDir(), "office", "office", "SYNTHETIC OFFICE IDENTITY")
	h := newHarness(t, func(int) (llm.Response, error) { t.Fatal("shortcut host must not invoke a model"); return plainTurn() }, func(c *Config) { c.Skills = []skills.Skill{sk} })
	a := postStudioAgent(t, h, studioAgentCapsBody("Shortcut host", "ready", map[string]any{"skills": []string{skillIDOf(sk)}}), 201)
	sid := createStudioConversationBody(t, h, a.ID, "{}").ID
	s := h.session(sid)
	return h, s, h.mgr.shortcutToolBackend(s)
}

func shortcutToolSimpleRequest(key, title string) shortcuts.CreateRequest {
	return shortcuts.CreateRequest{RequestID: key, Graph: shortcuts.Graph{RootID: "root", Nodes: []shortcuts.Node{{LocalID: "root", Title: title, Parts: []shortcuts.Part{{Kind: "text", Text: "keep exact text\n"}}}}}}
}

func shortcutToolFind(t *testing.T, b *shortcutToolBackend, source, title string) shortcuts.ReferenceSummary {
	t.Helper()
	result, err := b.Search(context.Background(), shortcuts.SearchRequest{Source: source, Query: title, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range result.Entries {
		if strings.Contains(entry.Title, title) && entry.RefID != "" {
			return entry
		}
	}
	t.Fatalf("reference %s/%s not found: %+v", source, title, result)
	return shortcuts.ReferenceSummary{}
}

func TestShortcutToolHostSixKindsNestedSnapshotsAndSourceAssets(t *testing.T) {
	h, s, b := shortcutToolFixture(t)
	ctx := context.Background()
	old := userShortcut{ID: "existing", Title: "Existing", Content: "original exact body", Parts: []inputref.Part{{Type: "text", Text: "original exact body"}}, InputLayout: "inline"}
	if _, err := h.mgr.replaceUserShortcuts(s.owner, s.studioID, 0, []userShortcut{old}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(s.cwd, "uploads"), 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("synthetic file bytes\n")
	if err := os.WriteFile(filepath.Join(s.cwd, "uploads", "same.csv"), data, 0600); err != nil {
		t.Fatal(err)
	}
	office := &inputref.OfficeSnapshot{Path: "uploads/synthetic.xlsx", Title: "Synthetic selection", Editor: "cell", Sheet: "Data", Range: "B2:C3", Text: "甲\t3\n乙\t5", CanvasVersion: "frozen-v4", CapturedAt: "2026-01-02T03:04:05Z", Dirty: true, DirtyEpoch: 2, SourceSession: s.ID}
	s.hub.Publish(Event{Type: EvUserMessage, Content: []llm.Block{{Type: llm.BlockText, Text: orderedTestElement, InputElement: true, InputElementFavicon: ""}, {Type: llm.BlockText, FilePath: "uploads/same.csv"}, {Type: llm.BlockText, InputOffice: office}, {Type: llm.BlockText, InputSkill: "office"}}})
	current, err := b.Search(ctx, shortcuts.SearchRequest{Source: "message", Limit: 100})
	if err != nil || len(current.Entries) != 4 {
		t.Fatalf("current message omitted its structured references: %+v %v", current, err)
	}
	web := shortcutToolFind(t, b, "browser", "网页")
	file := shortcutToolFind(t, b, "file", "same.csv")
	selected := shortcutToolFind(t, b, "office", "Synthetic selection")
	skill := shortcutToolFind(t, b, "skill", "office")
	existing := shortcutToolFind(t, b, "shortcut", "Existing")
	request := shortcuts.CreateRequest{RequestID: "six-kinds", Graph: shortcuts.Graph{RootID: "root", Nodes: []shortcuts.Node{{LocalID: "root", Title: "Mixed", Parts: []shortcuts.Part{{Kind: "text", Text: "before"}, {Kind: "ref", RefID: web.RefID}, {Kind: "ref", RefID: file.RefID}, {Kind: "ref", RefID: skill.RefID}, {Kind: "ref", RefID: selected.RefID}, {Kind: "node", NodeID: "child"}, {Kind: "ref", RefID: existing.RefID}, {Kind: "ref", RefID: file.RefID}, {Kind: "text", Text: "after"}}}, {LocalID: "child", Title: "Inline only", Parts: []shortcuts.Part{{Kind: "text", Text: "child exact"}}}}}}
	result, err := b.Create(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Reused || result.AgentID != s.studioID || result.Shortcut.InputLayout != "inline" {
		t.Fatalf("invalid receipt: %+v", result)
	}
	parts := result.Shortcut.Parts
	if len(parts) != 9 || parts[0].Text != "before" || parts[8].Text != "after" || parts[1].Text != orderedTestElement || parts[3].Name != "office" || !reflect.DeepEqual(parts[4].Office, office) || parts[5].Type != "shortcut" || parts[5].Parts[0].Text != "child exact" || parts[6].ShortcutID != "existing" || parts[6].Text != "original exact body" {
		t.Fatalf("mixed order or frozen data changed: %+v", parts)
	}
	if !strings.HasPrefix(parts[2].Path, "shortcut-assets/") || parts[2].Path != parts[7].Path {
		t.Fatal("repeated file was not deduplicated")
	}
	namespace, _ := h.mgr.shortcutAssetNamespace(s.owner, s.studioID)
	root, err := os.OpenRoot(namespace)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	asset, meta, err := readStarterAsset(root, parts[2].Path)
	if err != nil {
		t.Fatal(err)
	}
	defer asset.Close()
	raw := new(bytes.Buffer)
	_, _ = raw.ReadFrom(asset)
	if !bytes.Equal(raw.Bytes(), data) || meta.SHA256 == "" {
		t.Fatal("durable private asset bytes differ")
	}
	if raw, err := os.ReadFile(filepath.Join(s.cwd, "uploads", "same.csv")); err != nil || !bytes.Equal(raw, data) {
		t.Fatal("workspace source was changed")
	}
	view, err := h.mgr.userShortcuts(s.owner, s.studioID)
	if err != nil || len(view.Shortcuts) != 2 || view.Shortcuts[0].ID != old.ID {
		t.Fatalf("root-only append replaced existing shortcuts: %+v %v", view, err)
	}
	if len(view.Folders) != 0 {
		t.Fatal("unexpected folder writes")
	}
	seen := false
	for _, event := range s.hub.log {
		if event.Type == EvShortcutsChanged {
			seen = event.ShortcutID == result.Shortcut.ID && event.ShortcutAgentID == s.studioID && event.ShortcutRevision == 2
		}
	}
	if !seen {
		t.Fatal("no scoped save notification")
	}
}

func TestShortcutToolHostInformationAndQuickCardsAreReadOnlySources(t *testing.T) {
	h, s, b := shortcutToolFixture(t)
	ctx := context.Background()
	store, err := h.mgr.InformationStore(s.owner)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := store.PutAsset("library.txt", "", strings.NewReader("immutable library bytes"), information.MaxAssetBytes)
	if err != nil {
		t.Fatal(err)
	}
	_, entry, err := store.Create(0, information.CreateInput{Kind: "file", Name: "Library material", File: &asset})
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	info := shortcutToolFind(t, b, "information", "Library material")
	card := shortcutToolFind(t, b, "quick_card", "hello")
	request := shortcuts.CreateRequest{RequestID: "readonly-sources", Graph: shortcuts.Graph{RootID: "r", Nodes: []shortcuts.Node{{LocalID: "r", Title: "引用源", Parts: []shortcuts.Part{{Kind: "ref", RefID: info.RefID}, {Kind: "ref", RefID: card.RefID}}}}}}
	saved, err := b.Create(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(saved.Shortcut.Parts[0].Path, "shortcut-assets/") || saved.Shortcut.Parts[1].Type != "shortcut" {
		t.Fatal("read-only sources not resolved")
	}
	after, _ := store.List()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("information source mutated")
	}
	if _, err = store.Get(entry.ID, entry.Version); err != nil {
		t.Fatal("source entry disappeared")
	}
	request.RequestID = "invalid-system-parent"
	request.ParentID = "@information"
	if _, err = b.Create(ctx, request); err == nil {
		t.Fatal("system source accepted as writable parent")
	}
}

func TestShortcutToolHostDurableIdempotencyAfterUIEditDeleteAndRestart(t *testing.T) {
	h, s, b := shortcutToolFixture(t)
	ctx := context.Background()
	request := shortcutToolSimpleRequest("stable-key", "Original")
	first, err := b.Create(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	view, _ := h.mgr.userShortcuts(s.owner, s.studioID)
	// A normal UI edit must preserve both current content and durable receipt.
	changed := view.Shortcuts[0]
	changed.Title = "User-edited current title"
	changed.Content = "User-edited current content"
	changed.Parts = []inputref.Part{{Type: "text", Text: changed.Content}}
	if _, err = h.mgr.replaceUserShortcuts(s.owner, s.studioID, view.Revision, []userShortcut{changed}); err != nil {
		t.Fatal(err)
	}
	reopened := h.mgr.shortcutToolBackend(s)
	again, err := reopened.Create(ctx, request)
	if err != nil || !again.Reused || again.Shortcut.ID != first.Shortcut.ID || again.Shortcut.Title != changed.Title || again.Shortcut.Content != changed.Content || again.Revision != 2 {
		t.Fatalf("retry did not preserve current user edits: %+v %v", again, err)
	}
	view, _ = h.mgr.userShortcuts(s.owner, s.studioID)
	if len(view.Shortcuts) != 1 || view.Revision != 2 {
		t.Fatal("retry duplicated existing shortcut")
	}
	if _, err = h.mgr.replaceUserShortcuts(s.owner, s.studioID, view.Revision, []userShortcut{}); err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.Create(ctx, request); err == nil || !strings.Contains(err.Error(), "deleted") {
		t.Fatal("deleted shortcut produced a ghost success receipt")
	}
	view, _ = h.mgr.userShortcuts(s.owner, s.studioID)
	if len(view.Shortcuts) != 0 || view.Revision != 3 {
		t.Fatal("retry resurrected a deleted shortcut")
	}
	request.Nodes[0].Title = "Changed"
	if _, err = reopened.Create(ctx, request); err == nil {
		t.Fatal("idempotency key accepted a changed payload")
	}
}

func TestShortcutToolHostConcurrentAppendDoesNotReplaceOtherRoots(t *testing.T) {
	h, s, _ := shortcutToolFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	fail := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			request := shortcutToolSimpleRequest("append-"+string(rune('a'+i)), "Root")
			_, err := h.mgr.shortcutToolBackend(s).Create(ctx, request)
			fail <- err
		}(i)
	}
	wg.Wait()
	close(fail)
	for err := range fail {
		if err != nil {
			t.Fatal(err)
		}
	}
	view, err := h.mgr.userShortcuts(s.owner, s.studioID)
	if err != nil || len(view.Shortcuts) != 12 || view.Revision != 12 {
		t.Fatalf("concurrent roots lost: %+v %v", view, err)
	}
	wg = sync.WaitGroup{}
	fail = make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := h.mgr.shortcutToolBackend(s).Create(ctx, shortcutToolSimpleRequest("same-key", "Only once"))
			fail <- err
		}()
	}
	wg.Wait()
	close(fail)
	for err := range fail {
		if err != nil {
			t.Fatal(err)
		}
	}
	view, err = h.mgr.userShortcuts(s.owner, s.studioID)
	if err != nil || len(view.Shortcuts) != 13 || view.Revision != 13 {
		t.Fatal("concurrent identical retry created duplicates")
	}
}

func TestShortcutToolHostRejectsForeignHandlesChangedBindingAndDisabledSkill(t *testing.T) {
	h, s, b := shortcutToolFixture(t)
	ctx := context.Background()
	ref := shortcutToolFind(t, b, "skill", "office")
	a := createStudioAgent(t, h, "Other Agent", "", "ready")
	other := h.session(createStudioConversationBody(t, h, a.ID, "{}").ID)
	foreign := h.mgr.shortcutToolBackend(other)
	if _, err := foreign.Read(ctx, shortcuts.ReadRequest{RefID: ref.RefID}); err == nil {
		t.Fatal("foreign Agent read a handle")
	}
	r := shortcutToolSimpleRequest("forged", "Forbidden")
	r.Nodes[0].Parts = []shortcuts.Part{{Kind: "ref", RefID: ref.RefID}}
	if _, err := foreign.Create(ctx, r); err == nil {
		t.Fatal("foreign Agent resolved a handle")
	}
	s.mu.Lock()
	old := s.invocationSkills
	s.invocationSkills = nil
	s.mu.Unlock()
	if _, err := b.Create(ctx, r); err == nil {
		t.Fatal("Skill identity introduced a withdrawn capability")
	}
	s.mu.Lock()
	s.invocationSkills = old
	original := s.owner
	changed := identity.LocalScope("another-account")
	s.owner = &changed
	s.mu.Unlock()
	if _, err := b.Read(ctx, shortcuts.ReadRequest{RefID: ref.RefID}); err == nil {
		t.Fatal("closure followed a changed owner")
	}
	s.mu.Lock()
	s.owner = original
	s.mu.Unlock()
	if _, err := foreign.Search(ctx, shortcuts.SearchRequest{Source: "information", ParentID: "private"}); err == nil {
		t.Fatal("cross-source parent allowed")
	}
}

func TestShortcutToolHostFailureCleansStagedAssetsAndKeepsDocument(t *testing.T) {
	h, s, b := shortcutToolFixture(t)
	ctx := context.Background()
	entries := make([]userShortcut, shortcutsMaxCount)
	for i := range entries {
		entries[i] = userShortcut{ID: "old_" + string(rune(0x41+i/26)) + string(rune('a'+i%26)), Title: "Existing", Content: "keep"}
	}
	if _, err := h.mgr.replaceUserShortcuts(s.owner, s.studioID, 0, entries); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(s.cwd, "uploads"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.cwd, "uploads", "staged.txt"), []byte("keep source"), 0600); err != nil {
		t.Fatal(err)
	}
	s.hub.Publish(Event{Type: EvUserMessage, Content: []llm.Block{{Type: llm.BlockText, FilePath: "uploads/staged.txt"}}})
	ref := shortcutToolFind(t, b, "file", "staged.txt")
	r := shortcutToolSimpleRequest("over-count", "Cannot save")
	r.Nodes[0].Parts = []shortcuts.Part{{Kind: "ref", RefID: ref.RefID}}
	p, _ := shortcutPath(h.mgr.cfg.SessionDir, s.owner, s.studioID)
	before, _ := os.ReadFile(p)
	if _, err := b.Create(ctx, r); err == nil {
		t.Fatal("overfull store accepted root")
	}
	after, _ := os.ReadFile(p)
	if !bytes.Equal(before, after) {
		t.Fatal("failed save changed document")
	}
	namespace, _ := h.mgr.shortcutAssetNamespace(s.owner, s.studioID)
	dirs, err := os.ReadDir(namespace)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(dirs) != 0 {
		t.Fatal("failed save leaked partial private assets")
	}
	for _, event := range s.hub.log {
		if event.Type == EvShortcutsChanged {
			t.Fatal("failed save emitted success notification")
		}
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(after, &doc) != nil || doc["tool_creates"] != nil {
		t.Fatal("failed save wrote a receipt")
	}
}

func TestShortcutToolHostReadGraphLocalCorrectionPreservesFrozenOriginal(t *testing.T) {
	h, s, b := shortcutToolFixture(t)
	ctx := context.Background()
	original := userShortcut{ID: "saved_tree", Title: "Saved tree", Content: "original fallback", InputLayout: "inline", Parts: []inputref.Part{{Type: "text", Text: strings.Repeat("prefix ", 50) + "SEARCH_AT_END"}, {Type: "shortcut", ShortcutID: "nested", Title: "Change leaf", Text: "nested fallback", Parts: []inputref.Part{{Type: "text", Text: "old exact leaf"}, {Type: "element", Text: orderedTestElement}}}}}
	if _, err := h.mgr.replaceUserShortcuts(s.owner, s.studioID, 0, []userShortcut{original}); err != nil {
		t.Fatal(err)
	}
	ref := shortcutToolFind(t, b, "shortcut", "Saved tree")
	search, err := b.Search(ctx, shortcuts.SearchRequest{Source: "shortcut", Query: "SEARCH_AT_END"})
	if err != nil || len(search.Entries) != 1 {
		t.Fatal("full content search incorrectly limited to snippet")
	}
	read, err := b.Read(ctx, shortcuts.ReadRequest{RefID: ref.RefID})
	if err != nil || read.Graph == nil || len(read.Graph.Nodes) != 2 {
		t.Fatalf("no editable flat copy: %+v %v", read, err)
	}
	graph := *read.Graph
	graph.Nodes[1].Parts[0].Text = "new exact leaf"
	saved, err := b.Create(ctx, shortcuts.CreateRequest{RequestID: "correct-copy", Graph: graph})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Shortcut.Parts[0].Text != original.Parts[0].Text || saved.Shortcut.Parts[1].Parts[0].Text != "new exact leaf" || !reflect.DeepEqual(saved.Shortcut.Parts[1].Parts[1], original.Parts[1].Parts[1]) {
		t.Fatal("local correction lost other ordered content")
	}
	view, _ := h.mgr.userShortcuts(s.owner, s.studioID)
	if len(view.Shortcuts) != 2 || !reflect.DeepEqual(view.Shortcuts[0], original) {
		t.Fatal("copy edit modified original private shortcut")
	}
	// Returned mutable values cannot poison the host-issued frozen handle.
	read.Parts[0].Parts[0].Text = "tampered reader"
	readAgain, err := b.Read(ctx, shortcuts.ReadRequest{RefID: ref.RefID})
	if err != nil || readAgain.Parts[0].Parts[0].Text != original.Parts[0].Text {
		t.Fatal("read response aliased frozen registry")
	}
}

func TestShortcutToolHostFileWitnessReuseAndPreserveModifiedCopy(t *testing.T) {
	h, s, b := shortcutToolFixture(t)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(s.cwd, "uploads"), 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(s.cwd, "uploads", "reusable.txt")
	original := []byte("ORIGINAL_FILE_BYTES")
	if err := os.WriteFile(source, original, 0600); err != nil {
		t.Fatal(err)
	}
	s.hub.Publish(Event{Type: EvUserMessage, Content: []llm.Block{{Type: llm.BlockText, FilePath: "uploads/reusable.txt"}}})
	ref := shortcutToolFind(t, b, "file", "reusable.txt")
	request := shortcutToolSimpleRequest("reuse-1", "First")
	request.Nodes[0].Parts = []shortcuts.Part{{Kind: "ref", RefID: ref.RefID}}
	first, err := b.Create(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	request.RequestID = "reuse-2"
	request.Nodes[0].Title = "Second"
	second, err := b.Create(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Shortcut.Parts[0].Path != second.Shortcut.Parts[0].Path {
		t.Fatal("unchanged repeated source created another private file")
	}
	namespace, _ := h.mgr.shortcutAssetNamespace(s.owner, s.studioID)
	handle := strings.Split(first.Shortcut.Parts[0].Path, "/")
	modified := []byte("USER_MODIFIED_COPY_")
	privateData := filepath.Join(namespace, handle[1], "data")
	if err := os.WriteFile(privateData, modified, 0600); err != nil {
		t.Fatal(err)
	}
	request.RequestID = "reuse-3"
	request.Nodes[0].Title = "Third"
	third, err := b.Create(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if third.Shortcut.Parts[0].Path == first.Shortcut.Parts[0].Path {
		t.Fatal("modified private copy reused")
	}
	if data, err := os.ReadFile(privateData); err != nil || !bytes.Equal(data, modified) {
		t.Fatal("modified private copy overwritten or deleted")
	}
	// Same-size source changes must invalidate an earlier opaque handle.
	changed := append([]byte{}, original...)
	changed[0] = 'X'
	if err := os.WriteFile(source, changed, 0600); err != nil {
		t.Fatal(err)
	}
	request.RequestID = "stale-source"
	request.Nodes[0].Title = "Must fail"
	if _, err = b.Create(ctx, request); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("old file handle silently saved newer bytes: %v", err)
	}
	fresh := shortcutToolFind(t, b, "file", "reusable.txt")
	if fresh.RefID == ref.RefID {
		t.Fatal("changed file returned stale reference identity")
	}
	request.RequestID = "new-source"
	request.Nodes[0].Parts[0].RefID = fresh.RefID
	last, err := b.Create(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if last.Shortcut.Parts[0].Path == third.Shortcut.Parts[0].Path {
		t.Fatal("changed source reused old snapshot")
	}
}

func TestShortcutToolHostReceiptHistorySurvivesMoreThan256DeletedRoots(t *testing.T) {
	h, s, b := shortcutToolFixture(t)
	ctx := context.Background()
	p, _ := shortcutPath(h.mgr.cfg.SessionDir, s.owner, s.studioID)
	doc := shortcutDocument{Version: 1, shortcutView: shortcutView{ScopeID: b.scopeID, AgentID: b.agentID, Revision: 300, Shortcuts: []userShortcut{}, Folders: []shortcutFolder{}}, ToolCreates: map[string]shortcutCreateReceipt{}}
	for i := 0; i < 300; i++ {
		key := "old-key-" + string(rune(0x100+i))
		doc.ToolCreates[shortcutToolDigest(key)] = shortcutCreateReceipt{Digest: shortcutToolDigest(key), Revision: 1, ShortcutID: "deleted_" + string(rune('a'+i/26)) + string(rune('a'+i%26))}
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err = writeWorkflowFileAtomic(p, raw); err != nil {
		t.Fatal(err)
	}
	created, err := b.Create(ctx, shortcutToolSimpleRequest("after-deletions", "Still possible"))
	if err != nil || created.Revision != 301 {
		t.Fatalf("256 deleted roots permanently blocked new saves: %v", err)
	}
	raw, err = os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"shortcut":`)) {
		t.Fatal("receipt duplicated a full root snapshot")
	}
}
