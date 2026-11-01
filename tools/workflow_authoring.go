package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

const (
	WorkflowDiscoverName = "workflow_discover"
	WorkflowReadName     = "workflow_read"
	WorkflowValidateName = "workflow_validate"
	WorkflowEditName     = "workflow_edit"
	WorkflowTestName     = "workflow_test"
	WorkflowRunName      = "workflow_run"
	WorkflowPublishName  = "workflow_publish"
)

// WorkflowBackend is issued by the host for the current authenticated user,
// Agent and conversation. Neither tool arguments nor a Skill select identity,
// credentials, private stores or permissions. Every host call rechecks policy.
type WorkflowBackend interface {
	WorkflowCall(context.Context, string, json.RawMessage) (json.RawMessage, error)
}

type workflowAuthoringTool struct {
	backend WorkflowBackend
	action  string
}

// NewWorkflowTools supplies separate grants and review boundaries for reads,
// draft changes, real executions and publication. Registry selection remains
// independent of the current user's workflow action permissions.
func NewWorkflowTools(backend WorkflowBackend) []Tool {
	if !workflowBackendAvailable(backend) {
		return nil
	}
	var out []Tool
	for _, action := range []string{"discover", "read", "validate", "edit", "test", "run", "publish"} {
		out = append(out, &workflowAuthoringTool{backend: backend, action: action})
	}
	return out
}

func workflowBackendAvailable(backend WorkflowBackend) bool {
	if backend == nil {
		return false
	}
	v := reflect.ValueOf(backend)
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return !v.IsNil()
	}
	return true
}

func (t *workflowAuthoringTool) Name() string { return "workflow_" + t.action }
func (t *workflowAuthoringTool) IsReadOnly() bool {
	return t.action == "discover" || t.action == "read" || t.action == "validate"
}
func (t *workflowAuthoringTool) ExecutionMode() ExecutionMode {
	if t.IsReadOnly() {
		return Parallel
	}
	return Sequential
}
func (t *workflowAuthoringTool) Description() string {
	switch t.action {
	case "discover":
		return "Discover workflow resources and capabilities visible to the current user: reusable Agents, configured models, native tools and available approval accounts. Use real returned IDs; inspect effective capabilities and unsupported runtime reasons before selecting a node. Finding an Agent, Skill or MCP connection grants no permission. query optionally narrows resources; workflow_id scopes approval/editor information to an existing workflow."
	case "read":
		return "Read workflows and their existing unified Run records under the current user's permissions. action=list lists visible workflows; get returns one saved draft, revision, definition_hash and publication; runs lists its history; run reads one Run; artifact reads an authorized node artifact. Use returned IDs. A waiting approval is pending, not failure; this tool never approves, cancels or retries a Run."
	case "validate":
		return "Check a saved workflow or proposed definition without saving, publishing, executing tools or creating a Run. Returns structured diagnostics, input keys, effective capabilities and the canonical definition_hash. Supply workflow_id and optionally expected_revision to check an existing draft, or definition={name,description,nodes} for a new proposal. To preview a replacement under an existing workflow's edit permission, supply workflow_id, expected_revision and definition together. Optional inputs are sample string values. Structural validity is separate from runtime support and a passed real trial."
	case "edit":
		return "Create a workflow draft or atomically apply local node/edge changes to a saved draft. action=create needs request_id, name and optional nodes/description. action=patch needs workflow_id, exact expected_revision, request_id and operations. Keep stable node IDs; update_node.patch changes only named fields, remove_node and disconnect are explicit. Node tools narrows native tools only; Skill/MCP selection comes from the referenced Agent. An identical retry reuses request_id; a changed request needs a new key. Conflicts never overwrite a newer draft. Saving does not publish or execute it."
	case "test":
		return "Start a real trial of the exact frozen workflow draft without publishing it or moving the publication pointer. Use workflow_id, expected_revision, expected_hash from workflow_validate/read, request_id and optional string inputs. This is actual model/tool execution, not a side-effect-free simulation; only host-supported runtime capabilities may pass preflight. The receipt returns a test Run in the existing history; use workflow_read to inspect it and the ordinary approval UI for human decisions. Waiting/queued is not completed; do not blindly submit again."
	case "run":
		return "Start an already published workflow with optional string inputs through the existing permissions and durable Run queue. request_id makes an identical intent idempotent. Optional version_id must match the current published version. A queued receipt is admission, not successful completion; use workflow_read for status and results. This tool cannot change publication, approval grants, origin, business scope or callback targets."
	case "publish":
		return "Publish only the exact saved draft the user has reviewed. Supply workflow_id, expected_revision and expected_hash from workflow_validate/read; optional name/desc describe the version. If another edit changes the draft, read and preview the new changes before publishing again. Existing workflow.publish permission is required; draft edit permission alone is insufficient. Publication is separate from creating an external embedding entry or activating a schedule."
	}
	return ""
}

func workflowObject(required []string, props map[string]any) map[string]any {
	s := object(required, props)
	s["additionalProperties"] = false
	return s
}
func workflowStringMap(description string) map[string]any {
	return map[string]any{"type": "object", "description": description, "additionalProperties": map[string]any{"type": "string"}}
}
func workflowStrings(description string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": description}
}
func workflowNullable(s map[string]any) map[string]any {
	s["type"] = []string{s["type"].(string), "null"}
	return s
}
func workflowNodeSchema(patch bool) map[string]any {
	dep := workflowObject([]string{"node"}, map[string]any{
		"node": prop("string", "Stable upstream node ID"),
		"when": map[string]any{"type": "string", "enum": []string{"success", "failed", "rejected", "always"}},
	})
	// The persisted v1 form uses plain strings for success edges. A type union
	// keeps those readable/reusable while exposing the explicit dependency form.
	dep["type"] = []string{"string", "object"}
	policy := workflowObject([]string{"prevent_self_approval", "assignment"}, map[string]any{
		"prevent_self_approval": prop("boolean", "Exclude original owner and actual initiator from approval"),
		"assignment":            map[string]any{"type": "string", "enum": []string{"fixed", "business"}},
		"approvers":             workflowStrings("Fixed local usernames returned by resource discovery; business mode receives its trusted list from the business backend"),
	})
	field := workflowObject([]string{"key"}, map[string]any{
		"key": prop("string", "Stable human form field key"), "label": prop("string", "Display label"),
		"type": map[string]any{"type": "string", "enum": []string{"text", "select"}}, "options": workflowStrings("Allowed select values"),
		"required": prop("boolean", "Require an input before a human decision"),
	})
	candidate := workflowObject([]string{"agent"}, map[string]any{"agent": prop("string", "Discovered candidate Agent ID"), "hint": prop("string", "When to dispatch this Agent")})
	props := map[string]any{
		"kind":  map[string]any{"type": "string", "enum": []string{"agent", "human"}},
		"agent": prop("string", "Existing authorized Agent ID for an Agent node"), "task": prop("string", "This node's task; supports inputs.key and upstream summary/artifact templates"),
		"expect": workflowObject(nil, map[string]any{"path": prop("string", "Expected artifact relative to this Run workspace")}),
		"after":  map[string]any{"type": "array", "items": dep, "description": "Dependencies: upstream node strings or {node,when}; no cycles"},
		"tools":  workflowStrings("Optional native-tool narrowing only; [] does not disable the referenced Agent's Skill/MCP tools"),
		"model":  prop("string", "Optional configured model override"), "max_turns": prop("integer", "Optional per-node turn limit, at most 100"),
		"approval": prop("boolean", "An Agent result waits for human approval"), "approval_policy": policy,
		"orchestrate": workflowObject([]string{"candidates"}, map[string]any{
			"candidates": map[string]any{"type": "array", "items": candidate}, "max_dispatches": prop("integer", "Maximum dispatch calls, at most 20"), "max_parallel": prop("integer", "Concurrent dispatches, at most 4"),
		}),
		"prompt": prop("string", "Human review instructions; supports the same templates"),
		"fields": map[string]any{"type": "array", "items": field}, "actions": workflowStrings("Human decision actions, defaults to approve/reject"),
		"timeout": prop("string", "Human wait duration such as 72h"), "on_timeout": map[string]any{"type": "string", "enum": []string{"reject", "fail"}},
	}
	if patch {
		for _, key := range []string{"expect", "tools", "approval_policy", "orchestrate"} {
			props[key] = workflowNullable(props[key].(map[string]any))
		}
		return workflowObject(nil, props)
	}
	props["id"] = prop("string", "Stable unique node ID; keep it unchanged when editing")
	return workflowObject([]string{"id"}, props)
}

func (t *workflowAuthoringTool) InputSchema() map[string]any {
	id := prop("string", "Workflow ID returned by this tool")
	inputs := workflowStringMap("Optional external input keys with string values; identity and permissions never come from inputs")
	revision := prop("integer", "Exact saved draft revision returned by workflow_read or workflow_validate")
	hash := prop("string", "Exact 12-character lowercase definition_hash returned by workflow_read or workflow_validate")
	requestID := prop("string", "Stable 1-128 character request ID; reuse only for the same intended operation")
	switch t.action {
	case "discover":
		return workflowObject(nil, map[string]any{"query": prop("string", "Optional resource name/description query"), "workflow_id": id})
	case "read":
		return workflowObject([]string{"action"}, map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"list", "get", "runs", "run", "artifact"}}, "workflow_id": id,
			"run_id": prop("string", "Existing Run ID"), "node_id": prop("string", "Authorized node artifact ID"),
		})
	case "validate":
		return workflowObject(nil, map[string]any{
			"workflow_id": id, "expected_revision": revision, "inputs": inputs,
			"definition": workflowObject([]string{"name", "nodes"}, map[string]any{"name": prop("string", "Workflow name"), "description": prop("string", "Business purpose"), "nodes": map[string]any{"type": "array", "items": workflowNodeSchema(false)}}),
		})
	case "edit":
		operation := workflowObject([]string{"op"}, map[string]any{
			"op":      map[string]any{"type": "string", "enum": []string{"add_node", "update_node", "remove_node", "connect", "disconnect", "set_metadata"}},
			"node_id": prop("string", "Existing stable node ID for update/remove"), "node": workflowNodeSchema(false), "patch": workflowNodeSchema(true),
			"from": prop("string", "Upstream node ID"), "to": prop("string", "Dependent node ID"),
			"when": map[string]any{"type": "string", "enum": []string{"success", "failed", "rejected", "always"}},
			"name": prop("string", "set_metadata: new workflow name"), "description": prop("string", "set_metadata: new description"),
		})
		return workflowObject([]string{"action", "request_id"}, map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"create", "patch"}}, "workflow_id": id,
			"expected_revision": revision, "request_id": requestID, "name": prop("string", "Create: workflow name"), "description": prop("string", "Create: workflow purpose"),
			"nodes": map[string]any{"type": "array", "items": workflowNodeSchema(false)}, "operations": map[string]any{"type": "array", "items": operation},
		})
	case "test":
		return workflowObject([]string{"workflow_id", "expected_revision", "expected_hash", "request_id"}, map[string]any{"workflow_id": id, "expected_revision": revision, "expected_hash": hash, "request_id": requestID, "inputs": inputs})
	case "run":
		return workflowObject([]string{"workflow_id", "request_id"}, map[string]any{"workflow_id": id, "request_id": requestID, "inputs": inputs, "version_id": prop("string", "Optional exact currently published 12-character version ID")})
	case "publish":
		return workflowObject([]string{"workflow_id", "expected_revision", "expected_hash"}, map[string]any{"workflow_id": id, "expected_revision": revision, "expected_hash": hash, "name": prop("string", "Optional version label"), "desc": prop("string", "Optional version description")})
	}
	return workflowObject(nil, nil)
}

// validateWorkflowValue deliberately closes unknown fields at every schema
// level. The common validator tolerates extras for legacy tools; workflow
// mutations must not accept an invented owner, approval actor or credential.
func validateWorkflowValue(schema map[string]any, raw json.RawMessage, path string) error {
	kind := jsonKind(raw)
	want, _ := schema["type"].(string)
	if union, ok := schema["type"].([]string); ok {
		if !contains(union, kind) {
			return fmt.Errorf("field %q must be %s, got %s", path, strings.Join(union, " or "), kind)
		}
		want = kind
	}
	if want == "null" {
		return nil
	}
	if want != "" && !typeMatches(want, raw) {
		return fmt.Errorf("field %q must be %s, got %s", path, want, kind)
	}
	if allowed := enumValues(schema); len(allowed) > 0 && kind == "string" {
		var value string
		_ = json.Unmarshal(raw, &value)
		if !contains(allowed, value) {
			return fmt.Errorf("field %q must be one of %s", path, strings.Join(allowed, ", "))
		}
	}
	if want == "object" {
		fields, err := objectFields(raw)
		if err != nil || fields == nil {
			return fmt.Errorf("field %q must be an object", path)
		}
		props, _ := schema["properties"].(map[string]any)
		for _, key := range requiredNames(schema) {
			if value, ok := fields[key]; !ok || jsonKind(value) == "null" {
				return fmt.Errorf("missing required field %q", path+key)
			}
		}
		keys := make([]string, 0, len(fields))
		for key := range fields {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child, ok := props[key].(map[string]any)
			if !ok {
				if schema["additionalProperties"] == false {
					return fmt.Errorf("unknown workflow field %q", path+key)
				}
				child, ok = schema["additionalProperties"].(map[string]any)
				if !ok {
					continue
				}
			}
			if err := validateWorkflowValue(child, fields[key], path+key+"."); err != nil {
				return err
			}
		}
	}
	if want == "array" {
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		item, _ := schema["items"].(map[string]any)
		for i, value := range values {
			if err := validateWorkflowValue(item, value, fmt.Sprintf("%s[%d].", strings.TrimSuffix(path, "."), i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *workflowAuthoringTool) ValidateArgs(raw json.RawMessage) error {
	if len(raw) > 512<<10 {
		return errors.New("workflow arguments exceed 512 KiB")
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := validateWorkflowValue(t.InputSchema(), raw, ""); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	get := func(key string) string { var v string; _ = json.Unmarshal(fields[key], &v); return v }
	require := func(names ...string) error {
		for _, name := range names {
			if value, ok := fields[name]; !ok || jsonKind(value) == "null" || jsonKind(value) == "string" && strings.TrimSpace(get(name)) == "" {
				return fmt.Errorf("missing required field %q", name)
			}
		}
		return nil
	}
	if value, ok := fields["request_id"]; ok {
		var id string
		_ = json.Unmarshal(value, &id)
		if !skillRequestID(id) {
			return errors.New("request_id must be 1-128 letters/digits/underscore/dot/colon/hyphen and start with a letter or digit")
		}
	}
	if value, ok := fields["expected_revision"]; ok {
		var revision int
		if json.Unmarshal(value, &revision) != nil || revision < 1 {
			return errors.New("expected_revision must be a positive saved revision")
		}
	}
	for _, key := range []string{"expected_hash", "version_id"} {
		if _, ok := fields[key]; ok {
			v := get(key)
			if len(v) != 12 || strings.Trim(v, "0123456789abcdef") != "" {
				return fmt.Errorf("%s must be an exact 12-character lowercase definition hash", key)
			}
		}
	}
	switch t.action {
	case "discover":
		if len(get("query")) > 1024 {
			return errors.New("query exceeds 1024 bytes")
		}
	case "read":
		action := get("action")
		if action != "list" {
			if err := require("workflow_id"); err != nil {
				return err
			}
		}
		if action == "run" || action == "artifact" {
			if err := require("run_id"); err != nil {
				return err
			}
		}
		if action == "artifact" {
			return require("node_id")
		}
	case "validate":
		_, hasDefinition := fields["definition"]
		if !hasDefinition && get("workflow_id") == "" {
			return errors.New("supply workflow_id or definition to validate")
		}
		if hasDefinition && get("workflow_id") != "" {
			if err := require("expected_revision"); err != nil {
				return err
			}
		}
		if _, ok := fields["expected_revision"]; ok && get("workflow_id") == "" {
			return errors.New("expected_revision requires an existing workflow_id")
		}
	case "edit":
		if get("action") == "create" {
			if err := require("name"); err != nil {
				return err
			}
			for _, key := range []string{"workflow_id", "expected_revision", "operations"} {
				if _, ok := fields[key]; ok {
					return fmt.Errorf("create does not accept %s", key)
				}
			}
		} else {
			if err := require("workflow_id", "expected_revision", "operations"); err != nil {
				return err
			}
			for _, key := range []string{"name", "description", "nodes"} {
				if _, ok := fields[key]; ok {
					return fmt.Errorf("patch uses operations, not %s", key)
				}
			}
			var operations []json.RawMessage
			_ = json.Unmarshal(fields["operations"], &operations)
			if len(operations) < 1 || len(operations) > 100 {
				return errors.New("patch needs 1-100 operations")
			}
		}
	case "test", "run", "publish":
		return require("workflow_id")
	}
	return nil
}

func (t *workflowAuthoringTool) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if !workflowBackendAvailable(t.backend) {
		return Result{}, errors.New("workflow tools are unavailable in this session")
	}
	if err := t.ValidateArgs(raw); err != nil {
		return Result{}, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		raw = json.RawMessage(`{}`)
	}
	out, err := t.backend.WorkflowCall(ctx, t.action, raw)
	if err != nil {
		return Result{}, err
	}
	if len(out) > 2<<20 {
		return Result{}, errors.New("workflow host response exceeds 2 MiB")
	}
	var value map[string]any
	if err := json.Unmarshal(out, &value); err != nil || value == nil {
		return Result{}, errors.New("workflow host returned an invalid JSON object")
	}
	result := Result{Text: string(out), Payload: &ResultPayload{Text: string(out), Structured: append(json.RawMessage(nil), out...), Format: "json"}, Details: map[string]any{"type": "workflow_authoring", "action": t.action, "result": value}}
	if ok, present := value["ok"].(bool); present && !ok {
		message := "Workflow operation failed; inspect the structured error and diagnostics"
		if failure, ok := value["error"].(map[string]any); ok {
			if text, ok := failure["message"].(string); ok && text != "" {
				message = text
			}
		}
		return result, errors.New(message)
	}
	return result, nil
}
