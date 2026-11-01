package tools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yosukeno/pogo/inputref"
	"github.com/yosukeno/pogo/shortcuts"
	"github.com/yosukeno/pogo/skills"
)

const (
	ShortcutListName                    = "shortcut_list"
	ShortcutFolderCreateName            = "shortcut_folder_create"
	ShortcutUpdateName                  = "shortcut_update"
	ShortcutFolderRenameName            = "shortcut_folder_rename"
	ShortcutMoveName                    = "shortcut_move"
	ShortcutDeleteName                  = "shortcut_delete"
	ShortcutMergeName                   = "shortcut_merge"
	shortcutManagementMaxIDs            = 512
	shortcutManagementMaxRevision int64 = 1<<53 - 1
)

// ShortcutManagementBackend is optional and independent of ShortcutBackend.
// The host binds identity, validates current private IDs and commits each
// mutation together with its CAS/idempotency receipt. Sources stay read-only.
type ShortcutManagementBackend interface {
	List(context.Context, shortcuts.ListRequest) (shortcuts.ListResult, error)
	FolderCreate(context.Context, shortcuts.FolderCreateRequest) (shortcuts.MutationResult, error)
	Update(context.Context, shortcuts.UpdateRequest) (shortcuts.MutationResult, error)
	FolderRename(context.Context, shortcuts.FolderRenameRequest) (shortcuts.MutationResult, error)
	Move(context.Context, shortcuts.MoveRequest) (shortcuts.MutationResult, error)
	Delete(context.Context, shortcuts.DeleteRequest) (shortcuts.MutationResult, error)
	Merge(context.Context, shortcuts.MergeRequest) (shortcuts.MutationResult, error)
}

type ShortcutList struct{ Backend ShortcutManagementBackend }
type ShortcutFolderCreate struct{ Backend ShortcutManagementBackend }
type ShortcutUpdate struct{ Backend ShortcutManagementBackend }
type ShortcutFolderRename struct{ Backend ShortcutManagementBackend }
type ShortcutMove struct{ Backend ShortcutManagementBackend }
type ShortcutDelete struct{ Backend ShortcutManagementBackend }
type ShortcutMerge struct{ Backend ShortcutManagementBackend }

func (*ShortcutList) Name() string                         { return ShortcutListName }
func (*ShortcutFolderCreate) Name() string                 { return ShortcutFolderCreateName }
func (*ShortcutUpdate) Name() string                       { return ShortcutUpdateName }
func (*ShortcutFolderRename) Name() string                 { return ShortcutFolderRenameName }
func (*ShortcutMove) Name() string                         { return ShortcutMoveName }
func (*ShortcutDelete) Name() string                       { return ShortcutDeleteName }
func (*ShortcutMerge) Name() string                        { return ShortcutMergeName }
func (*ShortcutList) IsReadOnly() bool                     { return true }
func (*ShortcutFolderCreate) IsReadOnly() bool             { return false }
func (*ShortcutUpdate) IsReadOnly() bool                   { return false }
func (*ShortcutFolderRename) IsReadOnly() bool             { return false }
func (*ShortcutMove) IsReadOnly() bool                     { return false }
func (*ShortcutDelete) IsReadOnly() bool                   { return false }
func (*ShortcutMerge) IsReadOnly() bool                    { return false }
func (*ShortcutList) ExecutionMode() ExecutionMode         { return Parallel }
func (*ShortcutFolderCreate) ExecutionMode() ExecutionMode { return Sequential }
func (*ShortcutUpdate) ExecutionMode() ExecutionMode       { return Sequential }
func (*ShortcutFolderRename) ExecutionMode() ExecutionMode { return Sequential }
func (*ShortcutMove) ExecutionMode() ExecutionMode         { return Sequential }
func (*ShortcutDelete) ExecutionMode() ExecutionMode       { return Sequential }
func (*ShortcutMerge) ExecutionMode() ExecutionMode        { return Sequential }
func (*ShortcutList) Description() string {
	return "List one folder of this Agent's shortcuts with breadcrumbs, read-only state and current whole-library revision. Empty parent_id means the private root. @information and @quick-cards are read-only source folders; @information/<folder-id> browses a source subfolder. Use returned private IDs for management and ref_id for shortcut_query action=read. Sources never become editable by listing or reading."
}
func (*ShortcutFolderCreate) Description() string {
	return "Create a private folder for this Agent. Use the latest library expected_revision from shortcut_query action=list and a stable request_id for an identical retry. parent_id is a private folder ID or omitted for root; the Information Repository and Quick Cards sources cannot be changed."
}
func (*ShortcutUpdate) Description() string {
	return "Update one existing private shortcut without changing its ID. Supply either metadata fields (title/icon/language) to preserve the full ordered content, or a complete graph from shortcut_query action=read to replace content, never both. Preserve opaque reference handles and nesting order. Read-only source cards cannot be edited. Whole-library expected_revision prevents overwriting newer changes; changed requests require new request_id."
}
func (*ShortcutFolderRename) Description() string {
	return "Rename one private folder without moving its contents. Requires current library expected_revision and an idempotent request_id. Read-only source folders cannot be renamed."
}
func (*ShortcutMove) Description() string {
	return "Move 1-512 private shortcuts/folders to one private parent; empty parent_id means root. Moving a folder preserves descendants. The host rejects cycles, mixed invalid targets and read-only sources atomically. Requires expected_revision and a stable request_id for identical retries."
}
func (*ShortcutDelete) Description() string {
	return "Delete 1-512 explicitly selected private shortcuts/folders. Nonempty folders require recursive:true; descendants are then removed together. Source folders, Information Repository originals and Quick Cards remain read-only. Requires current expected_revision and a stable request_id; a conflict or invalid target never partially deletes."
}
func (*ShortcutMerge) Description() string {
	return "Preview exact duplicate private cards in the same folder by default; no changes are made. The host compares the full ordered snapshots, never titles alone. To apply, pass apply:true, the returned plan_id, its expected_revision and a request_id. Stale plans fail without deleting. Preview is read-only; applying removes only the plan's duplicate roots, preserving the chosen survivor and source originals."
}

func managementProps(extra map[string]any) map[string]any {
	p := map[string]any{"request_id": prop("string", "1-160 letters/digits/underscore/hyphen; reuse only for identical retries"), "expected_revision": prop("integer", "Current whole-library revision from shortcut_query action=list; conflicts require rereading")}
	for k, v := range extra {
		p[k] = v
	}
	return p
}
func (*ShortcutList) InputSchema() map[string]any {
	return shortcutObject(nil, map[string]any{"parent_id": prop("string", "Private folder, read-only source folder, or empty for private root"), "offset": prop("integer", "Zero-based offset, default 0"), "limit": prop("integer", "Page size, default 30, maximum 100")})
}
func (*ShortcutFolderCreate) InputSchema() map[string]any {
	return shortcutObject([]string{"request_id", "expected_revision", "name"}, managementProps(map[string]any{"name": prop("string", "Nonempty folder name, maximum 120 characters"), "parent_id": prop("string", "Private folder ID, omit for root")}))
}
func (*ShortcutUpdate) InputSchema() map[string]any {
	return shortcutObject([]string{"request_id", "expected_revision", "id"}, managementProps(map[string]any{"id": prop("string", "Existing private shortcut ID"), "title": prop("string", "Optional replacement title; content remains intact"), "icon": prop("string", "Optional icon name; empty clears it"), "language": prop("string", "Optional supported highlighting language; empty means plain text"), "graph": shortcutGraphSchema()}))
}
func (*ShortcutFolderRename) InputSchema() map[string]any {
	return shortcutObject([]string{"request_id", "expected_revision", "id", "name"}, managementProps(map[string]any{"id": prop("string", "Existing private folder ID"), "name": prop("string", "New folder name, maximum 120 characters")}))
}
func managementIDsSchema() map[string]any {
	return map[string]any{"type": "array", "items": prop("string", "Private shortcut or folder ID"), "minItems": 1, "maxItems": shortcutManagementMaxIDs}
}
func (*ShortcutMove) InputSchema() map[string]any {
	return shortcutObject([]string{"request_id", "expected_revision", "ids", "parent_id"}, managementProps(map[string]any{"ids": managementIDsSchema(), "parent_id": prop("string", "Target private folder ID or empty string for root")}))
}
func (*ShortcutDelete) InputSchema() map[string]any {
	return shortcutObject([]string{"request_id", "expected_revision", "ids"}, managementProps(map[string]any{"ids": managementIDsSchema(), "recursive": prop("boolean", "Default false; true explicitly deletes folder descendants")}))
}
func (*ShortcutMerge) InputSchema() map[string]any {
	return shortcutObject(nil, managementProps(map[string]any{"apply": prop("boolean", "Default false: preview only; true applies the exact host-issued plan"), "plan_id": prop("string", "Required for apply: opaque plan ID returned by preview")}))
}

func shortcutManagementBackendAvailable(b ShortcutManagementBackend) bool {
	if b == nil {
		return false
	}
	v := reflect.ValueOf(b)
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Func, reflect.Map, reflect.Slice, reflect.Chan:
		return !v.IsNil()
	}
	return true
}
func shortcutManagementFor(o Options) ShortcutManagementBackend {
	if shortcutManagementBackendAvailable(o.ShortcutManagement) {
		return o.ShortcutManagement
	}
	b, _ := o.Shortcuts.(ShortcutManagementBackend)
	if shortcutManagementBackendAvailable(b) {
		return b
	}
	return nil
}
func shortcutManagementAvailable(ctx context.Context, b ShortcutManagementBackend) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !shortcutManagementBackendAvailable(b) {
		return errors.New("shortcut management is unavailable in this session")
	}
	return nil
}
func decodeShortcutManagementArgs(schema map[string]any, raw json.RawMessage, target any) error {
	if len(raw) > 2<<20 {
		return errors.New("shortcut management arguments exceed 2 MiB")
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if _, ok := value.(map[string]any); !ok || !managementNoNulls(value) {
		return errors.New("shortcut management arguments must be an object without null values")
	}
	return decodeShortcutArgs(schema, raw, target)
}
func managementNoNulls(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case map[string]any:
		for _, item := range v {
			if !managementNoNulls(item) {
				return false
			}
		}
	case []any:
		for _, item := range v {
			if !managementNoNulls(item) {
				return false
			}
		}
	}
	return true
}
func shortcutManagementID(s string) bool     { return skillOpaqueID(s) }
func shortcutManagementParent(s string) bool { return s == "" || shortcutManagementID(s) }
func shortcutListParent(s string) bool {
	return shortcutManagementParent(s) || s == shortcuts.InformationFolderID || s == shortcuts.QuickCardsFolderID || strings.HasPrefix(s, shortcuts.InformationFolderID+"/") && shortcutManagementID(strings.TrimPrefix(s, shortcuts.InformationFolderID+"/"))
}
func shortcutListedID(s string) bool {
	return shortcutListParent(s) && s != "" || strings.HasPrefix(s, shortcuts.QuickCardsFolderID+"/") && shortcutManagementID(strings.TrimPrefix(s, shortcuts.QuickCardsFolderID+"/"))
}
func shortcutManagementText(s string, max int) bool {
	return utf8.ValidString(s) && strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= max && !strings.ContainsFunc(s, unicode.IsControl)
}
func validateManagementMutation(r shortcuts.MutationRequest) error {
	if !shortcutManagementID(r.RequestID) || r.ExpectedRevision < 0 || r.ExpectedRevision >= shortcutManagementMaxRevision {
		return errors.New("invalid shortcut request_id or expected_revision")
	}
	return nil
}
func validateManagementIDs(ids []string) error {
	if len(ids) < 1 || len(ids) > shortcutManagementMaxIDs {
		return errors.New("expected 1-512 private IDs")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !shortcutManagementID(id) || seen[id] {
			return errors.New("IDs must be unique private shortcut/folder IDs")
		}
		seen[id] = true
	}
	return nil
}
func managementMutationTools(b ShortcutManagementBackend) []Tool {
	return []Tool{&ShortcutFolderCreate{Backend: b}, &ShortcutUpdate{Backend: b}, &ShortcutFolderRename{Backend: b}, &ShortcutMove{Backend: b}, &ShortcutDelete{Backend: b}, &ShortcutMerge{Backend: b}}
}

func (t *ShortcutList) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	if err := shortcutManagementAvailable(ctx, t.Backend); err != nil {
		return Result{}, err
	}
	var a shortcuts.ListRequest
	if err := decodeShortcutManagementArgs(t.InputSchema(), raw, &a); err != nil {
		return Result{}, err
	}
	if !shortcutListParent(a.ParentID) || a.Offset < 0 || a.Offset > 10000 || a.Limit < 0 || a.Limit > 100 {
		return Result{}, errors.New("invalid shortcut listing folder, offset or limit")
	}
	if a.Limit == 0 {
		a.Limit = 30
	}
	v, err := t.Backend.List(ctx, a)
	if err != nil {
		return Result{}, err
	}
	if v.ParentID != a.ParentID || v.Revision < 0 || v.Revision > shortcutManagementMaxRevision || v.Offset != min(a.Offset, v.Total) || v.Total < 0 || len(v.Entries) > a.Limit || len(v.Breadcrumbs) > 64 || v.Offset+len(v.Entries) > v.Total || strings.HasPrefix(a.ParentID, "@") && !v.ReadOnly {
		return Result{}, errors.New("shortcut host returned invalid listing bounds or scope")
	}
	end := v.Offset + len(v.Entries)
	if v.NextOffset != nil {
		if *v.NextOffset != end || end >= v.Total || len(v.Entries) == 0 {
			return Result{}, errors.New("shortcut host returned invalid next page")
		}
	} else if end < v.Total {
		return Result{}, errors.New("shortcut host omitted the remaining result offset")
	}
	for _, f := range v.Breadcrumbs {
		if f.ID != "" && !shortcutListedID(f.ID) || !shortcutManagementText(f.Name, 1024) {
			return Result{}, errors.New("shortcut host returned invalid breadcrumbs")
		}
	}
	for _, e := range v.Entries {
		if !shortcutListedID(e.ID) || !shortcutListParent(e.ParentID) || !shortcutManagementText(e.Title, 1024) || len(e.Snippet) > 4096 || len(e.Source) > 64 || len(e.Kind) > 64 || e.RefID != "" && !shortcutManagementID(e.RefID) || strings.HasPrefix(e.ID, "@") && !e.ReadOnly {
			return Result{}, errors.New("shortcut host returned invalid scoped metadata")
		}
	}
	if v.Entries == nil {
		v.Entries = []shortcuts.ReferenceSummary{}
	}
	if v.Breadcrumbs == nil {
		v.Breadcrumbs = []shortcuts.Folder{}
	}
	return shortcutResult(v, ShortcutDetails{Type: "shortcut", Action: "list"})
}

func (t *ShortcutFolderCreate) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	if err := shortcutManagementAvailable(ctx, t.Backend); err != nil {
		return Result{}, err
	}
	var a shortcuts.FolderCreateRequest
	if err := decodeShortcutManagementArgs(t.InputSchema(), raw, &a); err != nil {
		return Result{}, err
	}
	if err := validateManagementMutation(a.MutationRequest); err != nil {
		return Result{}, err
	}
	if !shortcutManagementText(a.Name, 120) || !shortcutManagementParent(a.ParentID) {
		return Result{}, errors.New("invalid private folder name or parent")
	}
	v, err := t.Backend.FolderCreate(ctx, a)
	if err != nil {
		return Result{}, err
	}
	if v.Shortcut != nil || !v.Reused && (v.Folder == nil || v.Folder.Name != strings.TrimSpace(a.Name) || v.Folder.ParentID != a.ParentID) {
		return Result{}, errors.New("shortcut host returned a different folder")
	}
	var allowed map[string]bool
	if v.Folder != nil {
		allowed = map[string]bool{v.Folder.ID: true}
	}
	return managementResult(v, a.MutationRequest, "folder_create", allowed)
}
func (t *ShortcutFolderRename) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	if err := shortcutManagementAvailable(ctx, t.Backend); err != nil {
		return Result{}, err
	}
	var a shortcuts.FolderRenameRequest
	if err := decodeShortcutManagementArgs(t.InputSchema(), raw, &a); err != nil {
		return Result{}, err
	}
	if err := validateManagementMutation(a.MutationRequest); err != nil {
		return Result{}, err
	}
	if !shortcutManagementID(a.ID) || !shortcutManagementText(a.Name, 120) {
		return Result{}, errors.New("invalid private folder ID or name")
	}
	v, err := t.Backend.FolderRename(ctx, a)
	if err != nil {
		return Result{}, err
	}
	if v.Shortcut != nil || v.Folder != nil && v.Folder.ID != a.ID || !v.Reused && (v.Folder == nil || v.Folder.Name != strings.TrimSpace(a.Name)) {
		return Result{}, errors.New("shortcut host returned a different folder")
	}
	return managementResult(v, a.MutationRequest, "folder_rename", map[string]bool{a.ID: true})
}
func (t *ShortcutUpdate) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	if err := shortcutManagementAvailable(ctx, t.Backend); err != nil {
		return Result{}, err
	}
	var a shortcuts.UpdateRequest
	if err := decodeShortcutManagementArgs(t.InputSchema(), raw, &a); err != nil {
		return Result{}, err
	}
	if err := validateManagementMutation(a.MutationRequest); err != nil {
		return Result{}, err
	}
	if !shortcutManagementID(a.ID) {
		return Result{}, errors.New("id must identify a private shortcut")
	}
	metadata := a.Title != nil || a.Icon != nil || a.Language != nil
	if a.Graph != nil {
		if metadata {
			return Result{}, errors.New("graph and metadata fields are mutually exclusive")
		}
		if err := shortcuts.ValidateGraph(*a.Graph); err != nil {
			return Result{}, err
		}
	} else if !metadata {
		return Result{}, errors.New("provide metadata or a complete graph")
	}
	probe := inputref.Part{Type: "shortcut", ShortcutID: "sc_probe", Title: "valid", Text: "valid"}
	if a.Title != nil {
		probe.Title = strings.TrimSpace(*a.Title)
	}
	if a.Icon != nil {
		probe.Icon = *a.Icon
	}
	if a.Language != nil {
		probe.Language = *a.Language
	}
	if !skills.ValidInputShortcut(probe) {
		return Result{}, errors.New("invalid shortcut title, icon or highlighting language")
	}
	v, err := t.Backend.Update(ctx, a)
	if err != nil {
		return Result{}, err
	}
	if v.Folder != nil || v.Shortcut != nil && v.Shortcut.ID != a.ID {
		return Result{}, errors.New("shortcut host returned a different update")
	}
	// A durable retry acknowledges the original write and returns the current
	// target, which may have since been edited, moved or deleted by the user.
	if !v.Reused {
		if v.Shortcut == nil || a.Title != nil && v.Shortcut.Title != strings.TrimSpace(*a.Title) || a.Icon != nil && v.Shortcut.Icon != *a.Icon || a.Language != nil && v.Shortcut.Language != *a.Language {
			return Result{}, errors.New("shortcut host returned a different update")
		}
		if a.Graph != nil {
			for _, node := range a.Graph.Nodes {
				if node.LocalID == a.Graph.RootID && (v.Shortcut.Title != strings.TrimSpace(node.Title) || v.Shortcut.Icon != node.Icon || v.Shortcut.Language != node.Language) {
					return Result{}, errors.New("shortcut host changed the replacement graph metadata")
				}
			}
		}
	}
	return managementResult(v, a.MutationRequest, "update", map[string]bool{a.ID: true})
}
func (t *ShortcutMove) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	if err := shortcutManagementAvailable(ctx, t.Backend); err != nil {
		return Result{}, err
	}
	var a shortcuts.MoveRequest
	if err := decodeShortcutManagementArgs(t.InputSchema(), raw, &a); err != nil {
		return Result{}, err
	}
	if err := validateManagementMutation(a.MutationRequest); err != nil {
		return Result{}, err
	}
	if err := validateManagementIDs(a.IDs); err != nil {
		return Result{}, err
	}
	if !shortcutManagementParent(a.ParentID) {
		return Result{}, errors.New("target must be a private folder or root")
	}
	v, err := t.Backend.Move(ctx, a)
	if err != nil {
		return Result{}, err
	}
	for _, c := range v.Changes {
		if c.ToParentID != a.ParentID {
			return Result{}, errors.New("shortcut host moved to a different parent")
		}
	}
	return managementResult(v, a.MutationRequest, "move", idSet(a.IDs))
}
func (t *ShortcutDelete) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	if err := shortcutManagementAvailable(ctx, t.Backend); err != nil {
		return Result{}, err
	}
	var a shortcuts.DeleteRequest
	if err := decodeShortcutManagementArgs(t.InputSchema(), raw, &a); err != nil {
		return Result{}, err
	}
	if err := validateManagementMutation(a.MutationRequest); err != nil {
		return Result{}, err
	}
	if err := validateManagementIDs(a.IDs); err != nil {
		return Result{}, err
	}
	v, err := t.Backend.Delete(ctx, a)
	if err != nil {
		return Result{}, err
	}
	var allowed map[string]bool
	if !a.Recursive {
		allowed = idSet(a.IDs)
	}
	return managementResult(v, a.MutationRequest, "delete", allowed)
}
func idSet(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

func decodeMerge(raw json.RawMessage) (shortcuts.MergeRequest, error) {
	var a shortcuts.MergeRequest
	if err := decodeShortcutManagementArgs((&ShortcutMerge{}).InputSchema(), raw, &a); err != nil {
		return a, err
	}
	if a.ExpectedRevision < 0 || a.ExpectedRevision > shortcutManagementMaxRevision {
		return a, errors.New("invalid merge revision")
	}
	if a.Apply {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return a, err
		}
		if _, present := fields["expected_revision"]; !present {
			return a, errors.New("apply requires an explicit expected_revision")
		}
		if err := validateManagementMutation(a.MutationRequest); err != nil {
			return a, err
		}
		if !shortcutManagementID(a.PlanID) {
			return a, errors.New("apply requires the host-issued plan_id")
		}
	} else if a.RequestID != "" || a.PlanID != "" {
		return a, errors.New("preview does not accept mutation request_id or plan_id")
	}
	return a, nil
}

// ShortcutMergeReadOnly is safe for approval classification: malformed or
// hidden mutation fields never earn the read-only exception.
func ShortcutMergeReadOnly(raw json.RawMessage) bool {
	a, err := decodeMerge(raw)
	return err == nil && !a.Apply
}
func (t *ShortcutMerge) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	if err := shortcutManagementAvailable(ctx, t.Backend); err != nil {
		return Result{}, err
	}
	a, err := decodeMerge(raw)
	if err != nil {
		return Result{}, err
	}
	v, err := t.Backend.Merge(ctx, a)
	if err != nil {
		return Result{}, err
	}
	if !a.Apply {
		if v.Action != "merge" || !shortcutManagementID(v.AgentID) || v.Saved || v.Changed || v.Reused || v.RequestID != "" || v.AppliedRevision != 0 || v.Shortcut != nil || v.Folder != nil || len(v.Changes) != 0 || !validMergePlan(v.Plan) || v.Plan.Revision != v.Revision || a.ExpectedRevision != 0 && v.Revision != a.ExpectedRevision {
			return Result{}, errors.New("shortcut host returned an invalid merge preview")
		}
		return managementView(v)
	}
	if v.Reused && v.Plan == nil && len(v.Changes) == 0 {
		return managementResult(v, a.MutationRequest, "merge", nil)
	}
	if !validMergePlan(v.Plan) || v.Plan.ID != a.PlanID || v.Plan.Revision != a.ExpectedRevision {
		return Result{}, errors.New("shortcut host applied a different merge plan")
	}
	dups := map[string]bool{}
	for _, g := range v.Plan.Groups {
		for _, id := range g.DuplicateIDs {
			dups[id] = true
		}
	}
	if len(v.Changes) != len(dups) {
		return Result{}, errors.New("merge result differs from the planned duplicate set")
	}
	return managementResult(v, a.MutationRequest, "merge", dups)
}
func validMergePlan(p *shortcuts.MergePlan) bool {
	if p == nil || !shortcutManagementID(p.ID) || p.Revision < 0 || p.Revision > shortcutManagementMaxRevision || len(p.Groups) > 256 || p.Removals < 0 || p.Removals > 256 {
		return false
	}
	seen := map[string]bool{}
	count := 0
	for _, g := range p.Groups {
		if !shortcutManagementID(g.KeepID) || seen[g.KeepID] || !shortcutManagementParent(g.ParentID) || !shortcutManagementText(g.Title, 120) || len(g.DuplicateIDs) < 1 || len(g.DuplicateIDs) > 256 {
			return false
		}
		seen[g.KeepID] = true
		for _, id := range g.DuplicateIDs {
			if !shortcutManagementID(id) || seen[id] {
				return false
			}
			seen[id] = true
			count++
		}
	}
	return count == p.Removals
}

func managementResult(v shortcuts.MutationResult, r shortcuts.MutationRequest, action string, allowed map[string]bool) (Result, error) {
	if v.Action != action || !v.Saved || !shortcutManagementID(v.AgentID) || v.RequestID != r.RequestID || v.AppliedRevision != r.ExpectedRevision+1 || v.Revision < v.AppliedRevision || v.Revision > shortcutManagementMaxRevision || v.Reused && v.Changed || len(v.Changes) > shortcutManagementMaxIDs || v.Plan != nil && action != "merge" {
		return Result{}, errors.New("shortcut host returned an invalid mutation receipt")
	}
	if v.Folder != nil && (!shortcutManagementID(v.Folder.ID) || v.Folder.ReadOnly || !shortcutManagementText(v.Folder.Name, 120) || !shortcutManagementParent(v.Folder.ParentID)) {
		return Result{}, errors.New("shortcut host returned an invalid private folder")
	}
	if action != "update" && v.Shortcut != nil || action != "folder_create" && action != "folder_rename" && v.Folder != nil {
		return Result{}, errors.New("shortcut host returned unexpected saved objects")
	}
	if v.Shortcut != nil {
		entry := v.Shortcut
		part := inputref.Part{Type: "shortcut", ShortcutID: entry.ID, Title: entry.Title, Text: entry.Content, Icon: entry.Icon, Language: entry.Language, Parts: entry.Parts}
		if !shortcutManagementParent(entry.ParentID) || !skills.ValidInputShortcut(part) || skills.ValidateInputParts([]inputref.Part{part}, entry.InputLayout, "shortcut") != "" {
			return Result{}, errors.New("shortcut host returned an invalid ordered snapshot")
		}
	}
	seen := map[string]bool{}
	for _, c := range v.Changes {
		want := action
		if action == "merge" {
			want = "delete"
		}
		if c.Action != want || c.Kind != "shortcut" && c.Kind != "folder" || !shortcutManagementID(c.ID) || seen[c.ID] || !shortcutManagementText(c.Title, 120) || !shortcutManagementParent(c.FromParentID) || !shortcutManagementParent(c.ToParentID) || allowed != nil && !allowed[c.ID] || (action == "update" || action == "merge") && c.Kind != "shortcut" || (action == "folder_create" || action == "folder_rename") && c.Kind != "folder" {
			return Result{}, errors.New("shortcut host returned unrequested or invalid changes")
		}
		seen[c.ID] = true
	}
	if v.Changed && !v.Reused && len(v.Changes) == 0 {
		return Result{}, errors.New("shortcut host reported a change without a receipt")
	}
	return managementView(v)
}
func managementView(v shortcuts.MutationResult) (Result, error) {
	d := ShortcutDetails{Type: "shortcut", Action: v.Action, Saved: v.Saved, Changed: v.Changed, AgentID: v.AgentID, Revision: v.Revision, AppliedRevision: v.AppliedRevision, RequestID: v.RequestID, Reused: v.Reused, Changes: append([]shortcuts.Change{}, v.Changes...)}
	if v.Shortcut != nil {
		entry := *v.Shortcut
		entry.Parts = inputref.Clone(entry.Parts)
		d.Shortcut = &entry
	}
	if v.Folder != nil {
		f := *v.Folder
		d.Folder = &f
	}
	if v.Plan != nil {
		p := *v.Plan
		p.Groups = append([]shortcuts.MergeGroup{}, v.Plan.Groups...)
		for i := range p.Groups {
			p.Groups[i].DuplicateIDs = append([]string{}, p.Groups[i].DuplicateIDs...)
		}
		d.Plan = &p
	}
	text := map[string]any{"action": v.Action, "saved": v.Saved, "changed": v.Changed, "revision": v.Revision, "applied_revision": v.AppliedRevision, "request_id": v.RequestID, "reused": v.Reused, "changes": d.Changes}
	if v.Reused && (v.Action == "folder_create" || v.Action == "folder_rename" || v.Action == "update") {
		text["target_present"] = d.Shortcut != nil || d.Folder != nil
	}
	if d.Shortcut != nil {
		text["id"] = d.Shortcut.ID
		text["title"] = d.Shortcut.Title
	}
	if d.Folder != nil {
		text["folder"] = d.Folder
	}
	if d.Plan != nil {
		text["plan"] = d.Plan
	}
	return shortcutResult(text, d)
}
