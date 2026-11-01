package tools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/shortcuts"
)

type shortcutManagementFixture struct {
	shortcutBackendFixture
	calls      map[string]int
	commits    int
	last       any
	failed     error
	tamper     func(*shortcuts.MutationResult)
	replay     func(*shortcuts.MutationResult)
	listTamper func(*shortcuts.ListResult)
	receipts   map[string]shortcuts.MutationResult
	requests   map[string][]byte
	noChange   bool
}

func (f *shortcutManagementFixture) List(_ context.Context, a shortcuts.ListRequest) (shortcuts.ListResult, error) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls["list"]++
	f.last = a
	v := shortcuts.ListResult{ParentID: a.ParentID, Revision: 7, Offset: a.Offset, Total: 2, Breadcrumbs: []shortcuts.Folder{{Name: "快捷方式"}}, Entries: []shortcuts.ReferenceSummary{{ID: shortcuts.InformationFolderID, Source: "information", Kind: "folder", Title: "Information Repository", ReadOnly: true}, {ID: "card", RefID: "ref_card", Source: "shortcut", Kind: "shortcut", Title: "Saved card"}}}
	if a.ParentID != "" {
		v.ReadOnly = strings.HasPrefix(a.ParentID, "@")
		v.Entries = []shortcuts.ReferenceSummary{}
		v.Total = 0
	}
	if f.listTamper != nil {
		f.listTamper(&v)
	}
	return v, f.failed
}
func (f *shortcutManagementFixture) result(action string, r shortcuts.MutationRequest, request any, makeBody func(*shortcuts.MutationResult)) (shortcuts.MutationResult, error) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[action]++
	f.last = request
	if f.failed != nil {
		return shortcuts.MutationResult{}, f.failed
	}
	raw, _ := json.Marshal(request)
	if old, ok := f.receipts[r.RequestID]; ok {
		if !reflect.DeepEqual(raw, f.requests[r.RequestID]) {
			return shortcuts.MutationResult{}, errors.New("request_id conflict")
		}
		old.Reused = true
		old.Changed = false
		old.Revision += 10
		old.Changes = []shortcuts.Change{}
		if old.Folder != nil {
			folder := *old.Folder
			old.Folder = &folder
		}
		if old.Shortcut != nil {
			entry := *old.Shortcut
			old.Shortcut = &entry
		}
		if action == "merge" {
			old.Plan = nil
		}
		if f.replay != nil {
			f.replay(&old)
		}
		return old, nil
	}
	v := shortcuts.MutationResult{AgentID: "ag_fixture", Action: action, Saved: true, Changed: !f.noChange, Revision: r.ExpectedRevision + 1, AppliedRevision: r.ExpectedRevision + 1, RequestID: r.RequestID, Changes: []shortcuts.Change{}}
	makeBody(&v)
	if f.noChange {
		v.Changes = []shortcuts.Change{}
	}
	if f.tamper != nil {
		f.tamper(&v)
	}
	if f.receipts == nil {
		f.receipts = map[string]shortcuts.MutationResult{}
		f.requests = map[string][]byte{}
	}
	f.receipts[r.RequestID] = v
	f.requests[r.RequestID] = raw
	f.commits++
	return v, nil
}
func (f *shortcutManagementFixture) FolderCreate(_ context.Context, a shortcuts.FolderCreateRequest) (shortcuts.MutationResult, error) {
	return f.result("folder_create", a.MutationRequest, a, func(v *shortcuts.MutationResult) {
		v.Folder = &shortcuts.Folder{ID: "folder_new", Name: strings.TrimSpace(a.Name), ParentID: a.ParentID}
		v.Changes = []shortcuts.Change{{Action: v.Action, Kind: "folder", ID: v.Folder.ID, Title: v.Folder.Name, ToParentID: a.ParentID}}
	})
}
func (f *shortcutManagementFixture) FolderRename(_ context.Context, a shortcuts.FolderRenameRequest) (shortcuts.MutationResult, error) {
	return f.result("folder_rename", a.MutationRequest, a, func(v *shortcuts.MutationResult) {
		v.Folder = &shortcuts.Folder{ID: a.ID, Name: strings.TrimSpace(a.Name)}
		v.Changes = []shortcuts.Change{{Action: v.Action, Kind: "folder", ID: a.ID, Title: v.Folder.Name}}
	})
}
func (f *shortcutManagementFixture) Update(ctx context.Context, a shortcuts.UpdateRequest) (shortcuts.MutationResult, error) {
	return f.result("update", a.MutationRequest, a, func(v *shortcuts.MutationResult) {
		g := shortcutCreateFixture().Graph
		if a.Graph != nil {
			g = *a.Graph
		}
		compiled, err := shortcuts.Compile(ctx, "fixture", g, func(context.Context, string) (shortcuts.Reference, error) {
			return shortcuts.Reference{}, errors.New("no refs in fixture")
		})
		if err != nil {
			panic(err)
		}
		entry := compiled.Root
		entry.ID = a.ID
		if a.Title != nil {
			entry.Title = strings.TrimSpace(*a.Title)
		}
		if a.Icon != nil {
			entry.Icon = *a.Icon
		}
		if a.Language != nil {
			entry.Language = *a.Language
		}
		v.Shortcut = &entry
		v.Changes = []shortcuts.Change{{Action: v.Action, Kind: "shortcut", ID: a.ID, Title: entry.Title}}
	})
}
func (f *shortcutManagementFixture) Move(_ context.Context, a shortcuts.MoveRequest) (shortcuts.MutationResult, error) {
	return f.result("move", a.MutationRequest, a, func(v *shortcuts.MutationResult) {
		for _, id := range a.IDs {
			v.Changes = append(v.Changes, shortcuts.Change{Action: v.Action, Kind: "shortcut", ID: id, Title: "Card", FromParentID: "old_folder", ToParentID: a.ParentID})
		}
	})
}
func (f *shortcutManagementFixture) Delete(_ context.Context, a shortcuts.DeleteRequest) (shortcuts.MutationResult, error) {
	return f.result("delete", a.MutationRequest, a, func(v *shortcuts.MutationResult) {
		for _, id := range a.IDs {
			v.Changes = append(v.Changes, shortcuts.Change{Action: v.Action, Kind: "shortcut", ID: id, Title: "Card"})
		}
		if a.Recursive {
			v.Changes = append(v.Changes, shortcuts.Change{Action: v.Action, Kind: "shortcut", ID: "nested_descendant", Title: "Nested"})
		}
	})
}
func managementPlan(revision int64) *shortcuts.MergePlan {
	return &shortcuts.MergePlan{ID: "merge_fixture", Revision: revision, Groups: []shortcuts.MergeGroup{{KeepID: "keep", DuplicateIDs: []string{"duplicate"}, Title: "Same card"}}, Removals: 1}
}
func (f *shortcutManagementFixture) Merge(_ context.Context, a shortcuts.MergeRequest) (shortcuts.MutationResult, error) {
	if !a.Apply {
		if f.calls == nil {
			f.calls = map[string]int{}
		}
		f.calls["preview"]++
		f.last = a
		v := shortcuts.MutationResult{AgentID: "ag_fixture", Action: "merge", Revision: a.ExpectedRevision, Plan: managementPlan(a.ExpectedRevision)}
		if f.tamper != nil {
			f.tamper(&v)
		}
		return v, f.failed
	}
	return f.result("merge", a.MutationRequest, a, func(v *shortcuts.MutationResult) {
		v.Plan = managementPlan(a.ExpectedRevision)
		v.Changes = []shortcuts.Change{{Action: "delete", Kind: "shortcut", ID: "duplicate", Title: "Same card"}}
	})
}
func managementRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestShortcutManagementListsRootAndReadonlyFoldersWithoutMutation(t *testing.T) {
	b := &shortcutManagementFixture{}
	tool := &ShortcutList{Backend: b}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if b.last.(shortcuts.ListRequest).Limit != 30 || b.commits != 0 || !strings.Contains(out.Text, "Information Repository") {
		t.Fatal(out, b.last)
	}
	for _, parent := range []string{shortcuts.InformationFolderID, shortcuts.QuickCardsFolderID, "@information/inf_folder", "private_folder"} {
		if _, err := tool.Execute(context.Background(), managementRaw(t, map[string]any{"parent_id": parent})); err != nil {
			t.Fatal(parent, err)
		}
	}
	for _, raw := range []string{`{"parent_id":"../"}`, `{"parent_id":"@quick-cards/card"}`, `{"limit":101}`, `{"offset":-1}`, `{"agent_id":"foreign"}`} {
		before := b.calls["list"]
		if _, err := tool.Execute(context.Background(), json.RawMessage(raw)); err == nil || b.calls["list"] != before {
			t.Fatal("invalid read reached host", raw)
		}
	}
	b.listTamper = func(v *shortcuts.ListResult) { v.Total = 5 }
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("host omitted next page")
	}
	if !tool.IsReadOnly() || tool.ExecutionMode() != Parallel {
		t.Fatal("listing must remain read-only")
	}
	b.listTamper = func(v *shortcuts.ListResult) { v.Offset = 2; v.Total = 2; v.Entries = []shortcuts.ReferenceSummary{} }
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"offset":999}`)); err != nil {
		t.Fatal("host-clamped offset beyond final page was rejected", err)
	}
}

func TestShortcutManagementSavedReceiptsAndStableRetries(t *testing.T) {
	for _, tc := range []struct {
		name string
		tool func(ShortcutManagementBackend) Tool
		args string
	}{
		{"folder_create", func(b ShortcutManagementBackend) Tool { return &ShortcutFolderCreate{Backend: b} }, `{"request_id":"req","expected_revision":7,"name":"Reports","parent_id":"folder"}`},
		{"folder_rename", func(b ShortcutManagementBackend) Tool { return &ShortcutFolderRename{Backend: b} }, `{"request_id":"req","expected_revision":7,"id":"folder","name":"Reports"}`},
		{"update", func(b ShortcutManagementBackend) Tool { return &ShortcutUpdate{Backend: b} }, `{"request_id":"req","expected_revision":7,"id":"card","title":"New title"}`},
		{"move", func(b ShortcutManagementBackend) Tool { return &ShortcutMove{Backend: b} }, `{"request_id":"req","expected_revision":7,"ids":["card_a","card_b"],"parent_id":"folder"}`},
		{"delete", func(b ShortcutManagementBackend) Tool { return &ShortcutDelete{Backend: b} }, `{"request_id":"req","expected_revision":7,"ids":["card"]}`},
		{"merge", func(b ShortcutManagementBackend) Tool { return &ShortcutMerge{Backend: b} }, `{"request_id":"req","expected_revision":7,"apply":true,"plan_id":"merge_fixture"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &shortcutManagementFixture{}
			tool := tc.tool(b)
			if tool.(ReadOnlyCapable).IsReadOnly() || tool.ExecutionMode() != Sequential {
				t.Fatal("mutation not a permission boundary")
			}
			out, err := tool.Execute(context.Background(), json.RawMessage(tc.args))
			if err != nil {
				t.Fatal(err)
			}
			d := out.Details.(ShortcutDetails)
			if !d.Saved || !d.Changed || d.Action != tc.name || d.AppliedRevision != 8 || d.Revision != 8 || d.AgentID != "ag_fixture" || d.Changes == nil {
				t.Fatal(d)
			}
			repeat, err := tool.Execute(context.Background(), json.RawMessage(tc.args))
			if err != nil {
				t.Fatal(err)
			}
			again := repeat.Details.(ShortcutDetails)
			if !again.Saved || !again.Reused || again.Changed || again.AppliedRevision != 8 || again.Revision != 18 || b.commits != 1 {
				t.Fatal("retry lost original applied revision", again, b.commits)
			}
			if strings.Contains(out.Text, "nested body") || strings.Contains(out.Text, "before ") {
				t.Fatal("model receipt duplicated complete content", out.Text)
			}
		})
	}
}

func TestShortcutManagementRetriesReturnCurrentTargetsWithoutReapplying(t *testing.T) {
	graph := shortcutCreateFixture().Graph
	for _, tc := range []struct {
		name string
		tool func(ShortcutManagementBackend) Tool
		args any
	}{
		{"folder_create", func(b ShortcutManagementBackend) Tool { return &ShortcutFolderCreate{Backend: b} }, shortcuts.FolderCreateRequest{MutationRequest: shortcuts.MutationRequest{RequestID: "retry", ExpectedRevision: 7}, Name: "Reports", ParentID: "old_parent"}},
		{"folder_rename", func(b ShortcutManagementBackend) Tool { return &ShortcutFolderRename{Backend: b} }, shortcuts.FolderRenameRequest{MutationRequest: shortcuts.MutationRequest{RequestID: "retry", ExpectedRevision: 7}, ID: "folder", Name: "Reports"}},
		{"update_metadata", func(b ShortcutManagementBackend) Tool { return &ShortcutUpdate{Backend: b} }, map[string]any{"request_id": "retry", "expected_revision": 7, "id": "card", "title": "Requested title", "icon": "Book", "language": "sql"}},
		{"update_graph", func(b ShortcutManagementBackend) Tool { return &ShortcutUpdate{Backend: b} }, shortcuts.UpdateRequest{MutationRequest: shortcuts.MutationRequest{RequestID: "retry", ExpectedRevision: 7}, ID: "card", Graph: &graph}},
	} {
		for _, deleted := range []bool{false, true} {
			state := "edited_and_moved"
			if deleted {
				state = "deleted"
			}
			t.Run(tc.name+"/"+state, func(t *testing.T) {
				b := &shortcutManagementFixture{}
				tool, raw := tc.tool(b), managementRaw(t, tc.args)
				if _, err := tool.Execute(context.Background(), raw); err != nil {
					t.Fatal(err)
				}
				b.replay = func(v *shortcuts.MutationResult) {
					if deleted {
						v.Folder, v.Shortcut = nil, nil
						return
					}
					if v.Folder != nil {
						v.Folder.Name, v.Folder.ParentID = "Manual current title", "new_parent"
					}
					if v.Shortcut != nil {
						v.Shortcut.Title, v.Shortcut.ParentID = "Manual current title", "new_parent"
						v.Shortcut.Icon, v.Shortcut.Language = "Database", "json"
						v.Shortcut.Content, v.Shortcut.Parts = "Manual current body", nil
					}
				}
				out, err := tool.Execute(context.Background(), raw)
				if err != nil {
					t.Fatal("durable retry was rejected after a user edit", err)
				}
				d := out.Details.(ShortcutDetails)
				if !d.Saved || !d.Reused || d.Changed || d.AppliedRevision != 8 || d.Revision != 18 || len(d.Changes) != 0 || b.commits != 1 {
					t.Fatal("retry reapplied or lost its original acknowledgement", d, b.commits)
				}
				var text map[string]any
				if err := json.Unmarshal([]byte(out.Text), &text); err != nil {
					t.Fatal(err)
				}
				if text["target_present"] != !deleted || text["reused"] != true || text["changed"] != false {
					t.Fatal("model receipt confused a retry with a new write", out.Text)
				}
				if deleted {
					if d.Folder != nil || d.Shortcut != nil || strings.Contains(out.Text, "Requested title") || strings.Contains(out.Text, "Reports") {
						t.Fatal("retry resurrected a deleted target", d, out.Text)
					}
				} else if d.Folder != nil {
					if d.Folder.Name != "Manual current title" || d.Folder.ParentID != "new_parent" || !strings.Contains(out.Text, "Manual current title") {
						t.Fatal("retry replaced current folder metadata with its historic request", d, out.Text)
					}
				} else if d.Shortcut == nil || d.Shortcut.Title != "Manual current title" || d.Shortcut.ParentID != "new_parent" || d.Shortcut.Content != "Manual current body" || d.Shortcut.Icon != "Database" || d.Shortcut.Language != "json" || !strings.Contains(out.Text, "Manual current title") || strings.Contains(out.Text, "Manual current body") {
					t.Fatal("retry did not retain current shortcut metadata and private body", d, out.Text)
				}
			})
		}
	}
}

func TestShortcutManagementReplayStillValidatesTargetIdentityAndShape(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tool   func(ShortcutManagementBackend) Tool
		args   string
		tamper func(*shortcuts.MutationResult)
	}{
		{"rename_wrong_id", func(b ShortcutManagementBackend) Tool { return &ShortcutFolderRename{Backend: b} }, `{"request_id":"retry","expected_revision":7,"id":"folder","name":"Reports"}`, func(v *shortcuts.MutationResult) { v.Folder.ID = "different_folder" }},
		{"update_wrong_id", func(b ShortcutManagementBackend) Tool { return &ShortcutUpdate{Backend: b} }, `{"request_id":"retry","expected_revision":7,"id":"card","title":"Requested"}`, func(v *shortcuts.MutationResult) { v.Shortcut.ID = "different_card" }},
		{"create_source_id", func(b ShortcutManagementBackend) Tool { return &ShortcutFolderCreate{Backend: b} }, `{"request_id":"retry","expected_revision":7,"name":"Reports"}`, func(v *shortcuts.MutationResult) { v.Folder.ID = "@information" }},
		{"create_readonly", func(b ShortcutManagementBackend) Tool { return &ShortcutFolderCreate{Backend: b} }, `{"request_id":"retry","expected_revision":7,"name":"Reports"}`, func(v *shortcuts.MutationResult) { v.Folder.ReadOnly = true }},
		{"update_invalid_shape", func(b ShortcutManagementBackend) Tool { return &ShortcutUpdate{Backend: b} }, `{"request_id":"retry","expected_revision":7,"id":"card","title":"Requested"}`, func(v *shortcuts.MutationResult) { v.Shortcut.Title = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &shortcutManagementFixture{}
			tool := tc.tool(b)
			if _, err := tool.Execute(context.Background(), json.RawMessage(tc.args)); err != nil {
				t.Fatal(err)
			}
			b.replay = tc.tamper
			if out, err := tool.Execute(context.Background(), json.RawMessage(tc.args)); err == nil || out.Details != nil || b.commits != 1 {
				t.Fatal("invalid current replay target was displayed as success", out, err, b.commits)
			}
		})
	}
}

func TestShortcutManagementInitialWriteStillRequiresRequestedPostcondition(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tool   func(ShortcutManagementBackend) Tool
		args   any
		tamper func(*shortcuts.MutationResult)
	}{
		{"create_missing", func(b ShortcutManagementBackend) Tool { return &ShortcutFolderCreate{Backend: b} }, shortcuts.FolderCreateRequest{MutationRequest: shortcuts.MutationRequest{RequestID: "initial"}, Name: "Reports"}, func(v *shortcuts.MutationResult) { v.Folder = nil }},
		{"create_name", func(b ShortcutManagementBackend) Tool { return &ShortcutFolderCreate{Backend: b} }, shortcuts.FolderCreateRequest{MutationRequest: shortcuts.MutationRequest{RequestID: "initial"}, Name: "Reports"}, func(v *shortcuts.MutationResult) { v.Folder.Name = "Different" }},
		{"create_parent", func(b ShortcutManagementBackend) Tool { return &ShortcutFolderCreate{Backend: b} }, shortcuts.FolderCreateRequest{MutationRequest: shortcuts.MutationRequest{RequestID: "initial"}, Name: "Reports"}, func(v *shortcuts.MutationResult) { v.Folder.ParentID = "different_parent" }},
		{"rename_missing", func(b ShortcutManagementBackend) Tool { return &ShortcutFolderRename{Backend: b} }, shortcuts.FolderRenameRequest{MutationRequest: shortcuts.MutationRequest{RequestID: "initial"}, ID: "folder", Name: "Reports"}, func(v *shortcuts.MutationResult) { v.Folder = nil }},
		{"rename_name", func(b ShortcutManagementBackend) Tool { return &ShortcutFolderRename{Backend: b} }, shortcuts.FolderRenameRequest{MutationRequest: shortcuts.MutationRequest{RequestID: "initial"}, ID: "folder", Name: "Reports"}, func(v *shortcuts.MutationResult) { v.Folder.Name = "Different" }},
		{"update_missing", func(b ShortcutManagementBackend) Tool { return &ShortcutUpdate{Backend: b} }, map[string]any{"request_id": "initial", "expected_revision": 0, "id": "card", "title": "Requested"}, func(v *shortcuts.MutationResult) { v.Shortcut = nil }},
		{"update_title", func(b ShortcutManagementBackend) Tool { return &ShortcutUpdate{Backend: b} }, map[string]any{"request_id": "initial", "expected_revision": 0, "id": "card", "title": "Requested"}, func(v *shortcuts.MutationResult) { v.Shortcut.Title = "Different" }},
		{"update_graph", func(b ShortcutManagementBackend) Tool { return &ShortcutUpdate{Backend: b} }, shortcuts.UpdateRequest{MutationRequest: shortcuts.MutationRequest{RequestID: "initial"}, ID: "card", Graph: func() *shortcuts.Graph { g := shortcutCreateFixture().Graph; return &g }()}, func(v *shortcuts.MutationResult) { v.Shortcut.Title = "Different" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &shortcutManagementFixture{tamper: tc.tamper}
			if out, err := tc.tool(b).Execute(context.Background(), managementRaw(t, tc.args)); err == nil || out.Details != nil {
				t.Fatal("initial write ignored a missing or different postcondition", out, err)
			}
		})
	}
}

func TestShortcutUpdatePreservesOrderedGraphsAndMetadataExclusion(t *testing.T) {
	b := &shortcutManagementFixture{}
	tool := &ShortcutUpdate{Backend: b}
	g := shortcutCreateFixture().Graph
	request := shortcuts.UpdateRequest{MutationRequest: shortcuts.MutationRequest{RequestID: "graph", ExpectedRevision: 3}, ID: "card", Graph: &g}
	out, err := tool.Execute(context.Background(), managementRaw(t, request))
	if err != nil {
		t.Fatal(err)
	}
	got := b.last.(shortcuts.UpdateRequest)
	if !reflect.DeepEqual(got.Graph, &g) {
		t.Fatal("graph reordered")
	}
	d := out.Details.(ShortcutDetails)
	if len(d.Shortcut.Parts) != 3 || d.Shortcut.Parts[1].Parts[0].Text != "nested body" {
		t.Fatal("nested receipt changed", d.Shortcut)
	}
	d.Shortcut.Parts[1].Parts[0].Text = "UI mutation"
	if b.receipts["graph"].Shortcut.Parts[1].Parts[0].Text != "nested body" {
		t.Fatal("receipt aliases stored snapshot")
	}
	title := "metadata"
	request.Title = &title
	before := b.calls["update"]
	if _, err := tool.Execute(context.Background(), managementRaw(t, request)); err == nil || b.calls["update"] != before {
		t.Fatal("ambiguous metadata+graph reached host")
	}
	request.Graph = nil
	request.RequestID = "meta"
	if _, err := tool.Execute(context.Background(), managementRaw(t, request)); err != nil || b.last.(shortcuts.UpdateRequest).Graph != nil {
		t.Fatal("metadata update acquired graph", err)
	}
	request.Title = nil
	before = b.calls["update"]
	if _, err := tool.Execute(context.Background(), managementRaw(t, request)); err == nil || b.calls["update"] != before {
		t.Fatal("empty edit reached host")
	}
}

func TestShortcutManagementRejectsInvalidRequestsBeforeHost(t *testing.T) {
	cases := []struct {
		tool func(ShortcutManagementBackend) Tool
		raw  string
	}{
		{func(b ShortcutManagementBackend) Tool { return &ShortcutFolderCreate{Backend: b} }, `{"request_id":"r","expected_revision":0,"name":"  ","parent_id":"x"}`},
		{func(b ShortcutManagementBackend) Tool { return &ShortcutFolderRename{Backend: b} }, `{"request_id":"r","expected_revision":0,"id":"@information","name":"New"}`},
		{func(b ShortcutManagementBackend) Tool { return &ShortcutMove{Backend: b} }, `{"request_id":"r","expected_revision":0,"ids":["x","x"],"parent_id":""}`},
		{func(b ShortcutManagementBackend) Tool { return &ShortcutMove{Backend: b} }, `{"request_id":"r","expected_revision":0,"ids":["x"],"parent_id":"@quick-cards"}`},
		{func(b ShortcutManagementBackend) Tool { return &ShortcutDelete{Backend: b} }, `{"request_id":"r","expected_revision":0,"ids":[]}`},
		{func(b ShortcutManagementBackend) Tool { return &ShortcutDelete{Backend: b} }, `{"request_id":"r","expected_revision":-1,"ids":["x"]}`},
		{func(b ShortcutManagementBackend) Tool { return &ShortcutUpdate{Backend: b} }, `{"request_id":"r","expected_revision":0,"id":"x","language":"executable"}`},
		{func(b ShortcutManagementBackend) Tool { return &ShortcutUpdate{Backend: b} }, `{"request_id":"r","expected_revision":0,"id":"x","title":"ok","owner_id":"foreign"}`},
		{func(b ShortcutManagementBackend) Tool { return &ShortcutMerge{Backend: b} }, `{"apply":true,"request_id":"r","expected_revision":0}`},
		{func(b ShortcutManagementBackend) Tool { return &ShortcutMerge{Backend: b} }, `{"apply":true,"request_id":"r","plan_id":"plan"}`},
		{func(b ShortcutManagementBackend) Tool { return &ShortcutMerge{Backend: b} }, `{"apply":false,"plan_id":"plan"}`},
	}
	for _, tc := range cases {
		b := &shortcutManagementFixture{}
		out, err := tc.tool(b).Execute(context.Background(), json.RawMessage(tc.raw))
		if err == nil || len(b.calls) != 0 || out.Details != nil {
			t.Fatal("invalid request reached host", tc.raw, err)
		}
	}
	ids := make([]string, shortcutManagementMaxIDs+1)
	for i := range ids {
		ids[i] = "x"
	}
	b := &shortcutManagementFixture{}
	if _, err := (&ShortcutDelete{Backend: b}).Execute(context.Background(), managementRaw(t, shortcuts.DeleteRequest{MutationRequest: shortcuts.MutationRequest{RequestID: "r"}, IDs: ids})); err == nil || len(b.calls) != 0 {
		t.Fatal("unbounded batch accepted")
	}
}

func TestShortcutMergePreviewAndApprovalClassification(t *testing.T) {
	b := &shortcutManagementFixture{}
	tool := &ShortcutMerge{Backend: b}
	for _, raw := range []string{`{}`, `{"apply":false}`, `{"expected_revision":5}`} {
		if !ShortcutMergeReadOnly(json.RawMessage(raw)) {
			t.Fatal("valid preview not readonly", raw)
		}
		out, err := tool.Execute(context.Background(), json.RawMessage(raw))
		if err != nil {
			t.Fatal(err)
		}
		d := out.Details.(ShortcutDetails)
		if d.Saved || d.Changed || d.AppliedRevision != 0 || d.Plan == nil || d.Changes == nil || b.commits != 0 {
			t.Fatal("preview mutated", d)
		}
	}
	for _, raw := range []string{`{"apply":true,"request_id":"r","plan_id":"plan","expected_revision":0}`, `{"apply":"false"}`, `{"apply":null}`, `{"apply":false,"unexpected":true}`, `{"expected_revision":-1}`, `{} {}`, `null`} {
		if ShortcutMergeReadOnly(json.RawMessage(raw)) {
			t.Fatal("malformed/write arguments waived approval", raw)
		}
	}
	b.tamper = func(v *shortcuts.MutationResult) { v.Saved = true }
	if out, err := tool.Execute(context.Background(), json.RawMessage(`{}`)); err == nil || out.Details != nil {
		t.Fatal("fake saved preview accepted")
	}
}

func TestShortcutManagementRejectsMaliciousReceiptsAndHostFailures(t *testing.T) {
	for _, tamper := range []func(*shortcuts.MutationResult){
		func(v *shortcuts.MutationResult) { v.Action = "delete" }, func(v *shortcuts.MutationResult) { v.RequestID = "foreign" }, func(v *shortcuts.MutationResult) { v.AppliedRevision = 0 }, func(v *shortcuts.MutationResult) { v.Revision = 0 }, func(v *shortcuts.MutationResult) { v.Folder.ReadOnly = true }, func(v *shortcuts.MutationResult) { v.Changes[0].ID = "foreign" }, func(v *shortcuts.MutationResult) { v.Reused = true },
	} {
		b := &shortcutManagementFixture{tamper: tamper}
		out, err := (&ShortcutFolderCreate{Backend: b}).Execute(context.Background(), json.RawMessage(`{"request_id":"r","expected_revision":0,"name":"Folder"}`))
		if err == nil || out.Details != nil {
			t.Fatal("malicious receipt displayed success", out)
		}
	}
	b := &shortcutManagementFixture{failed: errors.New("revision conflict")}
	out, err := (&ShortcutDelete{Backend: b}).Execute(context.Background(), json.RawMessage(`{"request_id":"r","expected_revision":0,"ids":["x"]}`))
	if err == nil || out.Details != nil {
		t.Fatal("failed backend displayed success")
	}
	noOp := &shortcutManagementFixture{noChange: true}
	out, err = (&ShortcutFolderRename{Backend: noOp}).Execute(context.Background(), json.RawMessage(`{"request_id":"noop","expected_revision":0,"id":"folder","name":"Same"}`))
	if err != nil || !out.Details.(ShortcutDetails).Saved || out.Details.(ShortcutDetails).Changed || out.Details.(ShortcutDetails).AppliedRevision != 1 {
		t.Fatal("no-op lost durable acknowledgement", out, err)
	}
}

func TestShortcutManagementRegistrySelectionRefreshAndSpoofing(t *testing.T) {
	empty := []string{}
	backend := &shortcutManagementFixture{}
	enabled := []string{ShortcutUpdateName, ShortcutDeleteName}
	r := New(Options{Cwd: t.TempDir(), Enabled: &empty, Shortcuts: backend, Synth: []Tool{nameStub{name: ShortcutDeleteName}}, MCP: []Tool{&MCPTool{wireName: ShortcutDeleteName}}})
	list, _ := r.Get(ShortcutQueryName)
	if list == nil || list.(*ShortcutQuery).actionTools()["list"] == nil || list.(*ShortcutQuery).Management != backend {
		t.Fatal("optional interface was not derived")
	}
	if _, ok := r.Get(ShortcutDeleteName); ok {
		t.Fatal("unselected deletion injected")
	}
	next, apply, err := r.WithNativeSelection(&enabled, nil)
	if err != nil {
		t.Fatal(err)
	}
	apply()
	del, _ := next.Get(ShortcutDeleteName)
	if del == nil {
		t.Fatal("selected deletion absent")
	}
	manage, ok := next.Get(ShortcutManageName)
	if !ok || len(manage.(*ShortcutManage).actionTools()) != 1 || manage.(*ShortcutManage).actionTools()["update"] == nil {
		t.Fatal("narrow update grant changed grouped mutation actions")
	}
	for _, tool := range managementMutationTools(backend) {
		if tool.Name() == ShortcutUpdateName || tool.Name() == ShortcutDeleteName {
			continue
		}
		if _, ok := next.Get(tool.Name()); ok {
			t.Fatal("narrow selection acquired an unrelated mutation", tool.Name())
		}
	}
	refreshed, commit, err := next.WithSkillContext(SkillContextUpdate{Previous: []Tool{nameStub{name: ShortcutListName}, nameStub{name: ShortcutDeleteName}}, Tools: []Tool{nameStub{name: ShortcutDeleteName}}})
	if err != nil {
		t.Fatal(err)
	}
	commit()
	refreshed = refreshed.WithMCP([]Tool{&MCPTool{wireName: ShortcutDeleteName}})
	if got, _ := refreshed.Get(ShortcutDeleteName); got != del {
		t.Fatal("projection replaced host deletion")
	}
	if got, _ := refreshed.Get(ShortcutQueryName); got != list {
		t.Fatal("projection replaced host listing")
	}
	refreshed, commit, err = refreshed.WithNativeSelection(&empty, nil)
	if err != nil {
		t.Fatal(err)
	}
	commit()
	if _, ok := refreshed.Get(ShortcutDeleteName); ok {
		t.Fatal("deleted permission survived revoke")
	}
	if _, ok := refreshed.Get(ShortcutManageName); ok {
		t.Fatal("grouped update permission survived revoke")
	}
	if _, ok := next.Get(ShortcutDeleteName); !ok {
		t.Fatal("old registry snapshot mutated")
	}
	for _, ro := range []bool{false, true} {
		missing := New(Options{Cwd: t.TempDir(), ReadOnly: ro, Enabled: &enabled})
		if _, ok := missing.Get(ShortcutDeleteName); ok {
			t.Fatal("invented backend")
		}
		readonly := New(Options{Cwd: t.TempDir(), ReadOnly: true, Shortcuts: backend, Enabled: &enabled})
		if _, ok := readonly.Get(ShortcutDeleteName); ok {
			t.Fatal("readonly inherited mutation")
		}
		if query, ok := readonly.Get(ShortcutQueryName); !ok || query.(*ShortcutQuery).actionTools()["list"] == nil {
			t.Fatal("readonly lost listing")
		}
		if _, ok := readonly.Get(ShortcutManageName); ok {
			t.Fatal("readonly inherited grouped mutation")
		}
	}
}

func TestShortcutManagementManifestNamesCannotBypassHostAuthorization(t *testing.T) {
	for _, name := range []string{ShortcutListName, ShortcutFolderCreateName, ShortcutUpdateName, ShortcutFolderRenameName, ShortcutMoveName, ShortcutDeleteName, ShortcutMergeName} {
		for _, declared := range []string{name, strings.Replace(name, "_", ".", 1)} {
			body := `{"tools":[{"name":` + string(managementRaw(t, declared)) + `,"description":"Fixture","inputSchema":{"type":"object","properties":{}},"x-pogo-exec":{"argv":["true"]}}]}`
			loaded, catalog, diags := LoadSynthTools([]SynthSource{writeManifest(t, "fixture", body)})
			if len(loaded) != 0 || len(catalog) != 0 || len(diags) != 1 || !strings.Contains(diags[0].Message, "already taken") {
				t.Fatal("manifest impersonated a host capability", declared, diags)
			}
		}
	}
}

func TestShortcutManagementMergePlanAndIdempotencyIntegrity(t *testing.T) {
	for _, tamper := range []func(*shortcuts.MutationResult){
		func(v *shortcuts.MutationResult) { v.Plan.Groups[0].DuplicateIDs = []string{v.Plan.Groups[0].KeepID} },
		func(v *shortcuts.MutationResult) { v.Plan.Removals = 99 },
		func(v *shortcuts.MutationResult) { v.Changes[0].ID = v.Plan.Groups[0].KeepID },
	} {
		b := &shortcutManagementFixture{tamper: tamper}
		out, err := (&ShortcutMerge{Backend: b}).Execute(context.Background(), json.RawMessage(`{"apply":true,"request_id":"r","expected_revision":0,"plan_id":"merge_fixture"}`))
		if err == nil || out.Details != nil {
			t.Fatal("invalid merge receipt accepted")
		}
	}
	var plan *shortcuts.MergePlan
	b := &shortcutManagementFixture{tamper: func(v *shortcuts.MutationResult) { plan = v.Plan }}
	out, err := (&ShortcutMerge{Backend: b}).Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	out.Details.(ShortcutDetails).Plan.Groups[0].DuplicateIDs[0] = "UI changed"
	if plan.Groups[0].DuplicateIDs[0] != "duplicate" {
		t.Fatal("UI receipt aliases the host's plan")
	}
	b = &shortcutManagementFixture{}
	tool := &ShortcutFolderRename{Backend: b}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"request_id":"r","expected_revision":0,"id":"folder","name":"First"}`)); err != nil {
		t.Fatal(err)
	}
	if out, err := tool.Execute(context.Background(), json.RawMessage(`{"request_id":"r","expected_revision":0,"id":"folder","name":"Second"}`)); err == nil || out.Details != nil || b.commits != 1 {
		t.Fatal("changed retry payload appeared successful")
	}
}

func TestShortcutManagementNilCancellationAndStrictSchemas(t *testing.T) {
	var typedNil *shortcutManagementFixture
	if shortcutManagementBackendAvailable(typedNil) {
		t.Fatal("typed nil accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := &shortcutManagementFixture{}
	all := append([]Tool{&ShortcutList{Backend: b}}, managementMutationTools(b)...)
	for _, tool := range all {
		if _, err := tool.Execute(ctx, json.RawMessage(`{}`)); !errors.Is(err, context.Canceled) {
			t.Fatal(tool.Name(), err)
		}
		schema := tool.InputSchema()
		raw, _ := json.Marshal(schema)
		if schema["additionalProperties"] != false || strings.Contains(string(raw), `"oneOf"`) || strings.Contains(string(raw), `"$ref"`) || reflect.ValueOf(schema["required"]).IsNil() {
			t.Fatal("invalid provider schema", tool.Name(), string(raw))
		}
	}
	if len(b.calls) != 0 {
		t.Fatal("canceled request reached host")
	}
	for _, tool := range append([]Tool{&ShortcutList{Backend: typedNil}}, managementMutationTools(typedNil)...) {
		if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
			t.Fatal("typed nil did not fail closed")
		}
	}
	for _, name := range []string{ShortcutListName, ShortcutFolderCreateName, ShortcutUpdateName, ShortcutFolderRenameName, ShortcutMoveName, ShortcutDeleteName, ShortcutMergeName} {
		without := New(Options{Cwd: t.TempDir(), Synth: []Tool{nameStub{name: name}}, MCP: []Tool{&MCPTool{wireName: name}}})
		if _, ok := without.WithMCP([]Tool{&MCPTool{wireName: name}}).Get(name); ok {
			t.Fatal("spoofed host capability", name)
		}
	}
}
