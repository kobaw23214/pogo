package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

const (
	ShortcutQueryName  = "shortcut_query"
	ShortcutManageName = "shortcut_manage"
)

var shortcutManageVerbs = []struct{ action, legacy string }{
	{"create", ShortcutCreateName},
	{"update", ShortcutUpdateName},
	{"folder_create", ShortcutFolderCreateName},
	{"folder_rename", ShortcutFolderRenameName},
	{"move", ShortcutMoveName},
}

// ShortcutManageActions resolves both the current grouped grant and older
// per-operation grants without widening an existing Agent's permissions.
// Delete and merge are deliberately independent capabilities.
func ShortcutManageActions(enabled *[]string) []string {
	actions := []string{}
	all := enabled == nil || slices.Contains(*enabled, ShortcutManageName)
	for _, verb := range shortcutManageVerbs {
		if all || slices.Contains(*enabled, verb.legacy) {
			actions = append(actions, verb.action)
		}
	}
	return actions
}

// IsLegacyShortcutBuiltin accepts persisted configuration names without
// advertising those older verbs as separate model-facing tools.
func IsLegacyShortcutBuiltin(name string) bool {
	switch name {
	case ShortcutSearchName, ShortcutReadName, ShortcutListName:
		return true
	}
	for _, verb := range shortcutManageVerbs {
		if name == verb.legacy {
			return true
		}
	}
	return false
}

type ShortcutQuery struct {
	Backend    ShortcutBackend
	Management ShortcutManagementBackend
}

type ShortcutManage struct {
	Backend    ShortcutBackend
	Management ShortcutManagementBackend
	// nil is the host's full available action set, not a model-controlled grant.
	Actions []string
}

func (*ShortcutQuery) Name() string                  { return ShortcutQueryName }
func (*ShortcutQuery) IsReadOnly() bool              { return true }
func (*ShortcutQuery) ExecutionMode() ExecutionMode  { return Parallel }
func (*ShortcutManage) Name() string                 { return ShortcutManageName }
func (*ShortcutManage) IsReadOnly() bool             { return false }
func (*ShortcutManage) ExecutionMode() ExecutionMode { return Sequential }

func (t *ShortcutQuery) actionTools() map[string]Tool {
	out := map[string]Tool{}
	if shortcutBackendAvailable(t.Backend) {
		out["search"] = &ShortcutSearch{Backend: t.Backend}
		out["read"] = &ShortcutRead{Backend: t.Backend}
	}
	if shortcutManagementBackendAvailable(t.Management) {
		out["list"] = &ShortcutList{Backend: t.Management}
	}
	return out
}

func (t *ShortcutManage) actionTools() map[string]Tool {
	out := map[string]Tool{}
	allowed := func(action string) bool { return t.Actions == nil || slices.Contains(t.Actions, action) }
	if allowed("create") && shortcutBackendAvailable(t.Backend) {
		out["create"] = &ShortcutCreate{Backend: t.Backend}
	}
	if shortcutManagementBackendAvailable(t.Management) {
		for action, tool := range map[string]Tool{
			"update":        &ShortcutUpdate{Backend: t.Management},
			"folder_create": &ShortcutFolderCreate{Backend: t.Management},
			"folder_rename": &ShortcutFolderRename{Backend: t.Management},
			"move":          &ShortcutMove{Backend: t.Management},
		} {
			if allowed(action) {
				out[action] = tool
			}
		}
	}
	return out
}

func (t *ShortcutManage) withSelection(enabled *[]string) *ShortcutManage {
	copy := *t
	copy.Actions = ShortcutManageActions(enabled)
	return &copy
}

func (*ShortcutQuery) Description() string {
	return "Query this Agent's shortcuts and authorized reference sources. action=search finds scoped metadata and opaque ref_id handles across shortcuts, quick cards, information, current message, browser, Office, workspace files and enabled Skills; paginate with offset. action=read reads one ref_id as a frozen ordered snapshot, including an editable graph for authorized management. action=list browses one folder with breadcrumbs and current whole-library revision; empty parent_id means private root, @information and @quick-cards are read-only sources. No query changes sources, enables Skills or grants permissions."
}

func (t *ShortcutManage) Description() string {
	var descriptions []string
	actions := t.actionTools()
	for _, verb := range shortcutManageVerbs {
		if tool, ok := actions[verb.action]; ok {
			descriptions = append(descriptions, "action="+verb.action+": "+shortcutGroupedDescription(tool.Description()))
		}
	}
	return "Manage this Agent's private shortcuts using only the advertised actions. Deletion and duplicate merging require their separate tools and grants. " + strings.Join(descriptions, " ")
}

func shortcutGroupedDescription(s string) string {
	return strings.NewReplacer(
		ShortcutSearchName, "shortcut_query(action=search)",
		ShortcutReadName, "shortcut_query(action=read)",
		ShortcutListName, "shortcut_query(action=list)",
		ShortcutCreateName, "shortcut_manage(action=create)",
		ShortcutUpdateName, "shortcut_manage(action=update)",
	).Replace(s)
}

func shortcutGroupedSchema(order []string, actions map[string]Tool, mutation bool) map[string]any {
	props := map[string]any{}
	verbs := []string{}
	for _, action := range order {
		tool, ok := actions[action]
		if !ok {
			continue
		}
		verbs = append(verbs, action)
		for name, property := range tool.InputSchema()["properties"].(map[string]any) {
			if _, exists := props[name]; !exists {
				props[name] = property
			}
		}
	}
	props["action"] = map[string]any{"type": "string", "enum": verbs, "description": "Choose one advertised action; send only that action's arguments"}
	required := []string{"action"}
	if mutation {
		required = append(required, "request_id")
	}
	return shortcutObject(required, props)
}

func (t *ShortcutQuery) InputSchema() map[string]any {
	return shortcutGroupedSchema([]string{"search", "read", "list"}, t.actionTools(), false)
}
func (t *ShortcutManage) InputSchema() map[string]any {
	return shortcutGroupedSchema([]string{"create", "update", "folder_create", "folder_rename", "move"}, t.actionTools(), true)
}

// Dispatch validates the exact action schema before approval or execution.
// In particular, the union of grouped fields cannot relax an individual
// operation's required fields or accept another action's private arguments.
func shortcutGroupedDispatch(raw json.RawMessage, actions map[string]Tool) (Tool, json.RawMessage, error) {
	if len(raw) > 2<<20 {
		return nil, nil, fmt.Errorf("shortcut arguments exceed 2 MiB")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, nil, fmt.Errorf("shortcut arguments must be an object")
	}
	var action string
	if json.Unmarshal(fields["action"], &action) != nil || action == "" {
		return nil, nil, fmt.Errorf("shortcut action is required")
	}
	tool, ok := actions[action]
	if !ok {
		return nil, nil, fmt.Errorf("shortcut action %q is unavailable or not authorized", action)
	}
	delete(fields, "action")
	props := tool.InputSchema()["properties"].(map[string]any)
	for name := range fields {
		if _, ok := props[name]; !ok {
			return nil, nil, fmt.Errorf("field %q is not valid for shortcut action %q", name, action)
		}
	}
	payload, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, err
	}
	if err := ValidateArgs(tool, payload); err != nil {
		return nil, nil, err
	}
	return tool, payload, nil
}

func (t *ShortcutQuery) ValidateArgs(raw json.RawMessage) error {
	_, _, err := shortcutGroupedDispatch(raw, t.actionTools())
	return err
}
func (t *ShortcutManage) ValidateArgs(raw json.RawMessage) error {
	_, _, err := shortcutGroupedDispatch(raw, t.actionTools())
	return err
}
func (t *ShortcutQuery) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	tool, payload, err := shortcutGroupedDispatch(raw, t.actionTools())
	if err != nil {
		return Result{}, err
	}
	return tool.Execute(ctx, payload)
}
func (t *ShortcutManage) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	tool, payload, err := shortcutGroupedDispatch(raw, t.actionTools())
	if err != nil {
		return Result{}, err
	}
	return tool.Execute(ctx, payload)
}
