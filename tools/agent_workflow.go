package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	af "github.com/yosukeno/pogo/agentworkflow"
)

const (
	AgentWorkflowReadName   = "agent_workflow_read"
	AgentWorkflowEditName   = "agent_workflow_edit"
	AgentWorkflowEnableName = "agent_workflow_enable"
	AgentWorkflowRunName    = "agent_workflow_run"
)

// AgentWorkflowBackend is issued for one authenticated user's bound Agent
// conversation. The host supplies identity and rechecks live capability grants.
// It is intentionally independent of the platform WorkflowBackend.
type AgentWorkflowBackend interface {
	AgentWorkflowCall(context.Context, string, json.RawMessage) (json.RawMessage, error)
}

type agentWorkflowTool struct {
	backend AgentWorkflowBackend
	group   string
}

func NewAgentWorkflowTools(backend AgentWorkflowBackend) []Tool {
	if !agentWorkflowBackendAvailable(backend) {
		return nil
	}
	result := make([]Tool, 0, 4)
	for _, group := range []string{"read", "edit", "enable", "run"} {
		result = append(result, &agentWorkflowTool{backend: backend, group: group})
	}
	return result
}

func agentWorkflowBackendAvailable(backend AgentWorkflowBackend) bool {
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

func (t *agentWorkflowTool) Name() string     { return "agent_workflow_" + t.group }
func (t *agentWorkflowTool) IsReadOnly() bool { return t.group == "read" }
func (t *agentWorkflowTool) ExecutionMode() ExecutionMode {
	if t.IsReadOnly() {
		return Parallel
	}
	return Sequential
}

func (t *agentWorkflowTool) Description() string {
	switch t.group {
	case "read":
		return "Read private internal workflows belonging to the current user and this Agent, independently of platform workflows. action=capabilities returns actual available capabilities and limits (max_steps, max_parallel_nodes), supported tool_approval_modes and default_tool_approval_mode. action=resources discovers this Agent's authorized Skill, shortcut and source-file catalog; optional source_session_id narrows source-chat materials. Bind returned stable resource IDs to agent-step skills/shortcuts; never invent IDs or treat a name in task prose as a resource binding. Resource binding does not grant tools or external access. list/get read definitions and exact revision/hash; list with include_deleted=true additionally returns deleted/archived workflows for read-only history; validate checks a saved workflow or proposed definition without executing or saving; runs/run read persistent history; artifact reads a recorded step output, never an arbitrary path. get, runs and saved validate require workflow_id. run requires BOTH workflow_id and run_id; run_id alone is invalid. artifact additionally requires step_id; for an iteration Agent item add its zero-based item_index to read that exact recorded artifact. Use workflow.id or run.workflow_id for workflow_id, run.id for run_id, and definition step.id or run.steps[].step_id for step_id; these IDs are not interchangeable. capabilities/list/resources need no workflow IDs, and an unsaved candidate validate uses definition. Workflows are reusable across this Agent's conversations. Use real returned IDs; the host supplies user and Agent identity. Queued, waiting_confirmation and interrupted are not successful completion."
	case "edit":
		return "Create, locally patch, archive or delete this Agent's private internal workflow: at most 200 nodes, at most 10 Agent nodes executing concurrently per Run. create needs name, steps and request_id; patch needs workflow_id, expected_revision, request_id and operations. Keep step IDs stable: add_step(step,index?), update_step(step_id,patch), remove_step(step_id), move_step(step_id,index), set_metadata(name?/description?/inputs?/tool_approval_mode?), set_graph(graph). Omitted code fields remain unchanged, but a supplied code.inputs or code.outputs replaces that entire map, so include every field to retain. Inputs: text (legacy default), boolean (yes/no strings; typed bool at code boundaries), number/integer/object/array JSON values, select with options [{value,label}]; prefer choices over typed yes/no. Only agent steps, including an iteration Agent body, accept skills/shortcuts stable IDs from read resources; empty lists clear bindings, and binding a Skill never grants tools. Without graph, steps execute sequentially. Opt-in graph.version=1 uses explicit edges with __input/__output, condition (branch=true/false only on condition edges), parallel and join, and rejects cycles; graph connections control execution and move_step does not rewire them, so change steps and graph in one atomic patch. Read only guaranteed completed predecessors; reference join outputs for paths that may be skipped. iteration maps an array through one bounded Agent/code body; loop repeats a code body until a literal condition holds, at most 100 rounds; bodies cannot nest control nodes and count toward the node limit. A code step runs saved Python/JavaScript directly without an LLM and never receives Agent tools or credentials; only pure-data code accepts policy (at most two retries or schema-checked fallback), and cancelled, revoked or uncertain external Agent actions are never retried automatically. An agent step uses this Agent's capabilities; a confirm step waits for the actual user. The same request_id retries one unchanged intent; conflicting revisions never overwrite newer drafts. Saving does not enable or run. tool_approval_mode standard/strict/auto is a versioned default inherited by every Agent node (omitted keeps legacy standard; Runs and retries keep their frozen mode); set it only for an explicit user choice, never to bypass a pending review; it never skips confirm nodes or grants missing tools, files or external access. archive retains versions and history. delete requires workflow_id, exact expected_revision and request_id; it hides and disables the workflow but retains all versions, Runs and artifacts, is refused while any Run or execution is unfinished (end the Run explicitly first), and deleted workflows cannot be edited, restored, enabled or executed. Graph, code, iteration and loop details: the agent-workflows Skill."
	case "enable":
		return "Enable or disable the exact reviewed internal workflow version for this Agent. Supply workflow_id, expected_revision and the 64-character expected_hash from read/validate; action=enable freezes/selects that saved version, action=disable prevents new normal starts without cancelling existing Runs. This is internal activation, not platform publication, external embedding or a permission grant. If the draft changed, read and review it again before enabling. Report validation and actual trial results separately."
	case "run":
		return "Trial, test one code node, start, cancel or explicitly retry this Agent's private internal workflow. test_node needs exact expected_revision/expected_hash, step_id and explicit node_inputs matching code.inputs; it records an independent trial Run, executes only that code node, skips all others, and does not activate or alter the draft. Code tests run real isolated code, not the model; inspect output/code_inputs/code_execution logs with read run. trial needs exact expected_revision/expected_hash and executes a frozen draft without changing activation; start uses the enabled version and optionally version_id. Supply workflow_id, request_id, optional typed inputs matching the declared schema and files relative to the authorized Agent/source-chat workspace; the host copies selected files into the Run. This is real model and tool execution, not a side-effect-free simulation. cancel/retry need run_id. Start returns a durable Run receipt, not completion; use agent_workflow_read to follow it. Retry creates a linked new Run and may repeat previous effects. Only the actual user can handle human confirmation in the workflow panel; this tool cannot confirm, approve, alter identities or recursively launch workflows from an execution session."
	}
	return ""
}

func agentWorkflowInputSchema() map[string]any {
	return workflowObject([]string{"key"}, map[string]any{
		"key": prop("string", "Stable input parameter key"), "label": prop("string", "User-facing label"), "required": prop("boolean", "Whether a nonempty runtime value is required"),
		"type":    map[string]any{"type": "string", "enum": []string{"text", "boolean", "select", "number", "integer", "object", "array"}, "description": "Omit for legacy text. boolean form values remain yes/no and become JSON booleans at code input boundaries. number/integer/object/array are typed JSON data; select requires options."},
		"options": map[string]any{"type": "array", "maxItems": af.MaxInputOptions, "items": workflowObject([]string{"value"}, map[string]any{"value": prop("string", "Stable nonempty choice value, not its display label"), "label": prop("string", "User-facing choice label")}), "description": "Select choices; boolean may provide exactly yes/no with custom labels. Text inputs cannot have choices."},
		"default": prop("string", "Optional default runtime string; boolean must be yes/no and select must match an option value"),
	})
}

func agentWorkflowJSONValueSchema() map[string]any {
	return map[string]any{"type": []string{"string", "number", "integer", "boolean", "object", "array", "null"}, "description": "Literal JSON data, never executable expressions. Runtime types are checked against the saved workflow/code declarations."}
}

func agentWorkflowCodeSchema(patch bool) map[string]any {
	kinds := []string{"string", "number", "integer", "boolean", "object", "array"}
	valueSchema := workflowObject([]string{"type"}, map[string]any{
		"type":       map[string]any{"type": "string", "enum": kinds},
		"items":      map[string]any{"type": "object", "description": "Required for array schemas; another ValueSchema with type, optional items/properties/required. Maximum schema depth is 8."},
		"properties": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "object"}, "description": "Object property names to nested ValueSchema declarations. Undeclared object fields are rejected when properties is declared."},
		"required":   workflowStrings("Required nested object field names; every top-level Code outputs field is always required"),
	})
	schema := workflowObject([]string{"language", "source", "inputs", "outputs"}, map[string]any{
		"language": map[string]any{"type": "string", "enum": []string{"python", "javascript"}},
		"source":   prop("string", "Versioned UTF-8 source, at most 64 KiB. Define main(inputs) returning a JSON object, with outputs exactly matching the declared fields. No implicit Agent tools, Skills, shortcuts, filesystem or network access."),
		"inputs": map[string]any{"type": "object", "additionalProperties": workflowObject([]string{"type"}, map[string]any{
			"type":   map[string]any{"type": "string", "enum": kinds},
			"source": prop("string", "Exactly one source or value. Source is inputs.KEY(.field/index) or guaranteed upstream steps.ID.output(.field/index), or summary/artifact/feedback. A join output is keyed by successful ancestor code step ID."),
			"value":  agentWorkflowJSONValueSchema(),
		}), "description": "Up to 64 named inputs; bind existing typed data or explicit literal samples, never interpolate values into code."},
		"outputs":         map[string]any{"type": "object", "minProperties": 1, "additionalProperties": valueSchema, "description": "1–64 output fields and their ValueSchema. Each is required; return no extra top-level fields. Complete input/output JSON is at most 256 KiB."},
		"timeout_seconds": prop("integer", "Host-bounded execution timeout, 1–60 seconds; omit/0 uses 10 seconds"),
	})
	if patch {
		schema["type"] = []string{"object", "null"}
		delete(schema, "required")
	}
	return schema
}

// The model-facing declaration and the validation schema share these builders.
// ValidateArgs always checks the complete closed schema (compact=false). The
// declaration (compact=true) states each nested shape once and refers back to
// it elsewhere: a 17 KB step schema used to be declared four times across the
// tools, and the code schema three times inside every step.
func agentWorkflowShapeRef(description string) map[string]any {
	return map[string]any{"type": "object", "description": description}
}

func agentWorkflowStepSchema(patch, compact bool) map[string]any {
	condition := workflowObject([]string{"source", "operator"}, map[string]any{
		"source":   prop("string", "Exact data reference: inputs.key(.field/index), a guaranteed predecessor steps.id.summary/artifact/feedback, or steps.id.output(.field/index) from code/join; no expressions or braces"),
		"operator": map[string]any{"type": "string", "enum": []string{"equals", "not_equals", "contains", "not_contains", "is_empty", "not_empty", "gt", "gte", "lt", "lte"}},
		"value":    prop("string", "Literal comparison value; numeric operators require a finite number; omit for is_empty/not_empty"),
	})
	if patch {
		condition["type"] = []string{"object", "null"}
	}
	properties := map[string]any{
		"kind":        map[string]any{"type": "string", "enum": []string{"agent", "confirm", "code", "condition", "parallel", "join", "iteration", "loop"}},
		"title":       prop("string", "Short user-facing step title"),
		"task":        prop("string", "Task for the owning Agent; inputs and prior step outputs may be referenced"),
		"prompt":      prop("string", "Question shown to the actual user at a confirm step"),
		"output_file": prop("string", "Agent nodes only: optional expected output file relative to the Run workspace; no absolute path or traversal. Code nodes return typed output data and cannot declare output_file. Graph workflows reserve inputs/ for frozen materials. Definitions containing iteration also reserve iteration-results/ for copied per-item artifact inputs."),
		"condition":   condition,
		"code":        agentWorkflowCodeSchema(patch),
		"iteration":   agentWorkflowRepeatSchema("iteration", patch, compact),
		"loop":        agentWorkflowRepeatSchema("loop", patch, compact),
		"policy":      agentWorkflowCodePolicySchema(patch),
		"skills":      map[string]any{"type": "array", "maxItems": af.MaxStepResources, "items": prop("string", "Authorized stable Skill ID returned by read resources"), "description": "Agent nodes only. Explicit Skill bindings; no new tool permission. Empty list removes bindings."},
		"shortcuts":   map[string]any{"type": "array", "maxItems": af.MaxStepResources, "items": prop("string", "Authorized stable shortcut ID returned by read resources"), "description": "Agent nodes only. Explicit reusable material bindings; never arbitrary paths or invented names."},
	}
	if patch {
		return workflowObject(nil, properties)
	}
	properties["id"] = prop("string", "Stable step ID; retain it during local edits")
	return workflowObject([]string{"id", "kind"}, properties)
}

func agentWorkflowCodePolicySchema(nullable bool) map[string]any {
	schema := workflowObject(nil, map[string]any{
		"max_retries":    map[string]any{"type": "integer", "minimum": 0, "maximum": 2, "description": "Pure-data code only. Retry actual script failures at most twice; never retry cancellation, revoked permission or storage errors."},
		"retry_delay_ms": map[string]any{"type": "integer", "minimum": 0, "maximum": 5000},
		"on_failure":     map[string]any{"type": "string", "enum": []string{"stop", "fallback"}},
		"fallback":       map[string]any{"type": "object", "additionalProperties": agentWorkflowJSONValueSchema(), "description": "Required only with on_failure=fallback; must exactly match this code node's declared outputs. The original failed attempt remains visible."},
	})
	if nullable {
		schema["type"] = []string{"object", "null"}
	}
	return schema
}

func agentWorkflowRepeatSchema(kind string, nullable, compact bool) map[string]any {
	bindingKind := "array"
	if kind == "loop" {
		bindingKind = "object"
	}
	binding := workflowObject([]string{"type"}, map[string]any{"type": map[string]any{"type": "string", "enum": []string{bindingKind}}, "source": prop("string", "Exactly one source or value: declared inputs.KEY or guaranteed upstream steps.ID.output.FIELD; not a local iteration/loop variable."), "value": agentWorkflowJSONValueSchema()})
	bodyKinds := []string{"agent", "code"}
	if kind == "loop" {
		bodyKinds = []string{"code"}
	}
	code, policy := agentWorkflowCodeSchema(false), agentWorkflowCodePolicySchema(false)
	if compact {
		code = agentWorkflowShapeRef("Same fields as step.code; required for a code body")
		policy = agentWorkflowShapeRef("Same fields as step.policy; code bodies only")
	}
	body := workflowObject([]string{"kind"}, map[string]any{
		"id": prop("string", "Optional local body ID; the host attributes execution/resources to the outer container node."), "kind": map[string]any{"type": "string", "enum": bodyKinds}, "title": prop("string", "Body title"),
		"task":        prop("string", "Agent iteration task. May refer to iteration.item(.field/index), iteration.index and guaranteed outer input/predecessor data; values are substituted once as data."),
		"output_file": prop("string", "Optional Agent iteration artifact relative to that item's isolated workspace; no inputs/, resources/ or iteration-results/ path."),
		"code":        code, "policy": policy,
		"skills": workflowStrings("Authorized stable Skill IDs discovered with read resources; Agent iteration bodies only; no new permission."), "shortcuts": workflowStrings("Authorized stable shortcut IDs; Agent iteration bodies only."),
	})
	properties := map[string]any{"body": body}
	required := []string{"body"}
	if kind == "iteration" {
		properties["items"] = binding
		required = append(required, "items")
		properties["concurrency"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 10, "description": "Defaults to 1; shares the Run's ten execution slots with all other nodes."}
		properties["max_items"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "Defaults to 100; input above the limit is rejected, not truncated."}
		properties["on_item_error"] = map[string]any{"type": "string", "enum": []string{"stop", "continue"}, "description": "stop is default. continue records a failed item and places null at its original result index; it does not rerun Agent effects."}
	} else {
		properties["initial"] = binding
		required = append(required, "initial", "until", "max_iterations")
		properties["max_iterations"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "Required hard bound. Reaching it without satisfying until fails and retains the final state."}
		properties["until"] = workflowObject([]string{"source", "operator"}, map[string]any{"source": prop("string", "Only loop.state(.field/index) or loop.index. Checked after each successful body execution; index is zero-based."), "operator": map[string]any{"type": "string", "enum": []string{"equals", "not_equals", "contains", "not_contains", "is_empty", "not_empty", "gt", "gte", "lt", "lte"}}, "value": prop("string", "Literal comparison; finite decimal for numeric operators. No scripts or expressions.")})
	}
	schema := workflowObject(required, properties)
	schema["description"] = "A bounded container with a single Agent/code body for iteration, and code-only body for loop; no nested control nodes. Body code input sources additionally support iteration.item/index or loop.state/index. Iteration output is {items:[actual output or null],errors:[{index,error}],total,completed,failed}; loop output is {state,iterations,limit_reached}. Each successful iteration item retains the complete body output JSON object; fields are not flattened into numbers. Skip null failed items and read the declared body output field when aggregating; failed is a count and errors contains details. Aggregate JSON <=256KiB."
	if compact && kind == "iteration" {
		schema["description"] = "Bounded container mapping an array through one agent or code body; no nested control nodes; body inputs may also use iteration.item/index. Output {items:[complete body output object or null],errors:[{index,error}],total,completed,failed}: skip null items and read the body's declared field when aggregating. Aggregate JSON <=256KiB."
	} else if compact {
		schema["description"] = "Bounded container repeating one code body; body inputs may also use loop.state/index, and each successful round's object replaces the state. Output {state,iterations,limit_reached}."
	}
	if nullable {
		schema["type"] = []string{"object", "null"}
	}
	return schema
}

func agentWorkflowGraphSchema() map[string]any {
	return workflowObject([]string{"version", "edges"}, map[string]any{
		"version": prop("integer", "Graph schema version; must be 1"),
		"edges": map[string]any{"type": "array", "minItems": 2, "maxItems": af.MaxSteps * 3, "items": workflowObject([]string{"source", "target"}, map[string]any{
			"source": prop("string", "Stable step ID or __input"),
			"target": prop("string", "Stable step ID or __output"),
			"branch": map[string]any{"type": "string", "enum": []string{"true", "false"}, "description": "Required only for condition outputs; omit on all other edges"},
		})},
	})
}

func agentWorkflowToolApprovalModeSchema() map[string]any {
	return map[string]any{"type": "string", "enum": []string{af.ToolApprovalStandard, af.ToolApprovalStrict, af.ToolApprovalAuto}, "description": "Default tool review behavior inherited by every Agent node: standard reviews Bash and other sensitive tools; strict reviews mutations; auto skips ordinary tool reviews. Omitted keeps legacy standard. This never skips human-confirmation nodes or grants missing capabilities. Only set an explicitly requested user choice."}
}

func agentWorkflowDefinitionSchema() map[string]any {
	return workflowObject([]string{"name", "steps"}, map[string]any{
		"name": prop("string", "Internal workflow name"), "description": prop("string", "Reusable purpose"),
		"inputs":             map[string]any{"type": "array", "items": agentWorkflowInputSchema()},
		"steps":              map[string]any{"type": "array", "minItems": 1, "maxItems": af.MaxSteps, "items": agentWorkflowStepSchema(false, false)},
		"graph":              agentWorkflowGraphSchema(),
		"tool_approval_mode": agentWorkflowToolApprovalModeSchema(),
	})
}

// InputSchema is the declaration the model and MCP clients see. Every call is
// still checked against validationSchema, so a compact reference never widens
// what the host accepts.
func (t *agentWorkflowTool) InputSchema() map[string]any { return agentWorkflowSchema(t.group, true) }

func (t *agentWorkflowTool) validationSchema() map[string]any {
	return agentWorkflowSchema(t.group, false)
}

func agentWorkflowSchema(group string, compact bool) map[string]any {
	id := prop("string", "Workflow ID returned by this Agent's private workflow service")
	revision := prop("integer", "Exact saved draft revision returned by read/validate")
	hash := prop("string", "Exact 64-character lowercase definition hash returned by read/validate")
	inputs := map[string]any{"type": "object", "additionalProperties": agentWorkflowJSONValueSchema(), "description": "Explicit values matching declared inputs. Legacy text/select accept strings; boolean accepts yes/no (or a JSON boolean); numeric/object/array inputs accept native JSON values or JSON text. Source conversations are not copied automatically."}
	props := map[string]any{"workflow_id": id}
	switch group {
	case "read":
		props["action"] = map[string]any{"type": "string", "enum": []string{"capabilities", "resources", "list", "get", "validate", "runs", "run", "artifact"}}
		props["include_deleted"] = prop("boolean", "Only for list: include deleted/archived workflows for read-only history; false or omitted hides them")
		props["source_session_id"] = prop("string", "Optional authorized source-chat session for resources discovery; must belong to the current Agent and user")
		props["run_id"], props["step_id"] = prop("string", "Logical Run ID"), prop("string", "Step whose recorded output to read")
		props["item_index"] = map[string]any{"type": "integer", "minimum": 0, "maximum": af.MaxRepeatItems - 1, "description": "Optional zero-based iteration item for artifact; host verifies the exact recorded item snapshot, never an arbitrary path."}
		props["expected_revision"], props["definition"], props["inputs"] = revision, agentWorkflowDefinitionSchema(), inputs
		if compact {
			props["definition"] = agentWorkflowShapeRef("Unsaved candidate for validate: the same name/description/inputs/steps/graph/tool_approval_mode fields agent_workflow_edit create accepts")
		}
		return workflowObject([]string{"action"}, props)
	case "edit":
		props["action"] = map[string]any{"type": "string", "enum": []string{"create", "patch", "archive", "delete"}}
		props["request_id"], props["expected_revision"] = prop("string", "Unique 1-128 character key for one unchanged edit intent"), revision
		props["name"], props["description"] = prop("string", "Workflow name"), prop("string", "Reusable purpose")
		props["inputs"] = map[string]any{"type": "array", "items": agentWorkflowInputSchema()}
		props["steps"] = map[string]any{"type": "array", "minItems": 1, "maxItems": af.MaxSteps, "items": agentWorkflowStepSchema(false, compact)}
		props["graph"] = agentWorkflowGraphSchema()
		props["tool_approval_mode"] = agentWorkflowToolApprovalModeSchema()
		opInputs := map[string]any{"type": "array", "items": agentWorkflowInputSchema()}
		step, patch, graph := agentWorkflowStepSchema(false, false), agentWorkflowStepSchema(true, false), agentWorkflowGraphSchema()
		if compact {
			opInputs = map[string]any{"type": "array", "items": agentWorkflowShapeRef("Same fields as top-level inputs items"), "description": "Replacement inputs for set_metadata"}
			step = agentWorkflowShapeRef("add_step: a complete new step with the same fields as steps[] items")
			patch = agentWorkflowShapeRef("update_step: only the step fields to change, with the same shapes as steps[] items; id cannot change. code is patched field by field, but a supplied code.inputs or code.outputs replaces that whole map")
			graph = agentWorkflowShapeRef("set_graph: the complete replacement graph, same fields as top-level graph")
		}
		operation := workflowObject([]string{"op"}, map[string]any{
			"op":   map[string]any{"type": "string", "enum": []string{"set_metadata", "add_step", "update_step", "remove_step", "move_step", "set_graph"}},
			"name": prop("string", "Replacement name for set_metadata"), "description": prop("string", "Replacement description for set_metadata"),
			"tool_approval_mode": agentWorkflowToolApprovalModeSchema(),
			"inputs":             opInputs,
			"step":               step, "step_id": prop("string", "Existing stable step ID"),
			"patch": patch, "index": prop("integer", "Zero-based insertion or destination index"),
			"graph": graph,
		})
		props["operations"] = map[string]any{"type": "array", "minItems": 1, "maxItems": af.MaxPatchOperations, "items": operation}
		return workflowObject([]string{"action", "request_id"}, props)
	case "enable":
		props["action"] = map[string]any{"type": "string", "enum": []string{"enable", "disable"}}
		props["expected_revision"], props["expected_hash"] = revision, hash
		return workflowObject([]string{"action", "workflow_id", "expected_revision", "expected_hash"}, props)
	case "run":
		props["action"] = map[string]any{"type": "string", "enum": []string{"trial", "test_node", "start", "cancel", "retry"}}
		props["request_id"], props["run_id"] = prop("string", "Unique 1-128 character key for this unchanged execution/control intent"), prop("string", "Original logical Run ID for cancel or retry")
		props["expected_revision"], props["expected_hash"], props["version_id"], props["inputs"] = revision, hash, hash, inputs
		props["files"] = workflowStrings("Explicit selected files relative to the authorized Agent/source-chat workspace; host validates and snapshots them")
		props["step_id"] = prop("string", "Exact saved code step ID to test independently; only for test_node")
		props["node_inputs"] = map[string]any{"type": "object", "additionalProperties": agentWorkflowJSONValueSchema(), "description": "Explicit code function sample arguments for test_node, not workflow inputs. Must exactly match code.inputs names/types; upstream references are not executed or copied from other Runs."}
		return workflowObject([]string{"action", "workflow_id", "request_id"}, props)
	}
	return workflowObject(nil, nil)
}

func (t *agentWorkflowTool) ValidateArgs(raw json.RawMessage) error {
	if len(raw) > af.MaxRequestBytes {
		return errors.New("internal workflow arguments exceed 4 MiB")
	}
	if err := validateWorkflowValue(t.validationSchema(), raw, ""); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	get := func(key string) string { var v string; _ = json.Unmarshal(fields[key], &v); return v }
	allowFields := func(names ...string) error {
		allowed := map[string]bool{"action": true}
		for _, name := range names {
			allowed[name] = true
		}
		for name := range fields {
			if !allowed[name] {
				return fmt.Errorf("%s does not accept %s", get("action"), name)
			}
		}
		return nil
	}
	require := func(keys ...string) error {
		for _, key := range keys {
			v, exists := fields[key]
			if !exists || jsonKind(v) == "null" || jsonKind(v) == "string" && strings.TrimSpace(get(key)) == "" {
				return fmt.Errorf("missing required field %q", key)
			}
		}
		return nil
	}
	for _, key := range []string{"expected_hash", "version_id"} {
		if _, exists := fields[key]; exists && (len(get(key)) != 64 || strings.Trim(get(key), "0123456789abcdef") != "") {
			return fmt.Errorf("%s must be an exact 64-character lowercase definition hash", key)
		}
	}
	if value, exists := fields["expected_revision"]; exists {
		var n int
		if json.Unmarshal(value, &n) != nil || n < 1 {
			return errors.New("expected_revision must be a positive saved revision")
		}
	}
	if value, exists := fields["item_index"]; exists {
		var index int
		if json.Unmarshal(value, &index) != nil || index < 0 || index >= af.MaxRepeatItems {
			return errors.New("item_index must be a zero-based item index from 0 to 99")
		}
	}
	if _, exists := fields["request_id"]; exists && !skillRequestID(get("request_id")) {
		return errors.New("request_id must be 1-128 letters/digits/underscore/dot/colon/hyphen and start with a letter or digit")
	}
	if _, exists := fields["workflow_id"]; exists {
		if err := require("workflow_id"); err != nil {
			return err
		}
	}
	switch t.group {
	case "read":
		switch get("action") {
		case "capabilities":
			return allowFields()
		case "list":
			return allowFields("include_deleted")
		case "resources":
			if err := allowFields("source_session_id", "workflow_id"); err != nil {
				return err
			}
			if _, exists := fields["source_session_id"]; exists {
				return require("source_session_id")
			}
			return nil
		case "get", "runs":
			if err := allowFields("workflow_id"); err != nil {
				return err
			}
			return require("workflow_id")
		case "run":
			if err := allowFields("workflow_id", "run_id"); err != nil {
				return err
			}
			return require("workflow_id", "run_id")
		case "artifact":
			if err := allowFields("workflow_id", "run_id", "step_id", "item_index"); err != nil {
				return err
			}
			return require("workflow_id", "run_id", "step_id")
		case "validate":
			if err := allowFields("workflow_id", "definition", "expected_revision", "inputs"); err != nil {
				return err
			}
			if _, exists := fields["expected_revision"]; exists && get("workflow_id") == "" {
				return errors.New("expected_revision requires a saved workflow_id")
			}
			if _, exists := fields["definition"]; !exists && get("workflow_id") == "" {
				return errors.New("validate needs a workflow_id or a proposed definition")
			}
			if _, exists := fields["definition"]; exists && get("workflow_id") != "" {
				return require("expected_revision")
			}
		}
	case "edit":
		if mode, supplied := fields["tool_approval_mode"]; supplied {
			var value string
			if json.Unmarshal(mode, &value) != nil || value == "" || !af.ValidToolApprovalMode(value) {
				return errors.New("tool_approval_mode needs standard, strict or auto")
			}
		}
		action := get("action")
		if action == "create" {
			if err := require("name", "steps"); err != nil {
				return err
			}
			for _, key := range []string{"workflow_id", "expected_revision", "operations"} {
				if _, exists := fields[key]; exists {
					return fmt.Errorf("create does not accept %s", key)
				}
			}
			return nil
		}
		if err := require("workflow_id", "expected_revision"); err != nil {
			return err
		}
		for _, key := range []string{"name", "description", "steps", "inputs", "graph", "tool_approval_mode"} {
			if _, exists := fields[key]; exists {
				return fmt.Errorf("%s uses operations, not top-level %s", action, key)
			}
		}
		if action == "archive" || action == "delete" {
			if _, exists := fields["operations"]; exists {
				return fmt.Errorf("%s does not accept operations", action)
			}
			return nil
		}
		var ops []map[string]json.RawMessage
		if err := json.Unmarshal(fields["operations"], &ops); err != nil || len(ops) < 1 || len(ops) > af.MaxPatchOperations {
			return fmt.Errorf("patch needs 1-%d local operations", af.MaxPatchOperations)
		}
		for _, op := range ops {
			if err := validateAgentWorkflowOperation(op); err != nil {
				return err
			}
		}
	case "run":
		switch get("action") {
		case "test_node":
			if err := allowFields("workflow_id", "request_id", "expected_revision", "expected_hash", "step_id", "node_inputs"); err != nil {
				return err
			}
			return require("expected_revision", "expected_hash", "step_id", "node_inputs")
		case "trial":
			if err := allowFields("workflow_id", "request_id", "expected_revision", "expected_hash", "inputs", "files"); err != nil {
				return err
			}
			return require("expected_revision", "expected_hash")
		case "start":
			return allowFields("workflow_id", "request_id", "version_id", "inputs", "files")
		case "cancel", "retry":
			if err := require("run_id"); err != nil {
				return err
			}
			for _, key := range []string{"inputs", "files", "expected_revision", "expected_hash", "version_id", "step_id", "node_inputs"} {
				if _, exists := fields[key]; exists {
					return fmt.Errorf("%s retains the original Run inputs; it does not accept %s", get("action"), key)
				}
			}
		}
	}
	return nil
}

func validateAgentWorkflowOperation(op map[string]json.RawMessage) error {
	var name string
	_ = json.Unmarshal(op["op"], &name)
	allowed := map[string]bool{"op": true}
	required := []string{}
	switch name {
	case "set_metadata":
		if mode, supplied := op["tool_approval_mode"]; supplied {
			var value string
			if json.Unmarshal(mode, &value) != nil || value == "" || !af.ValidToolApprovalMode(value) {
				return errors.New("tool_approval_mode needs standard, strict or auto")
			}
		}
		allowed["name"], allowed["description"], allowed["inputs"], allowed["tool_approval_mode"] = true, true, true, true
		if len(op) == 1 {
			return errors.New("set_metadata needs name, description, inputs or tool_approval_mode")
		}
	case "add_step":
		allowed["step"], allowed["index"] = true, true
		required = []string{"step"}
	case "update_step":
		allowed["step_id"], allowed["patch"] = true, true
		required = []string{"step_id", "patch"}
		var patch map[string]json.RawMessage
		if json.Unmarshal(op["patch"], &patch) != nil || len(patch) == 0 {
			return errors.New("update_step needs a nonempty patch without id")
		}
	case "remove_step":
		allowed["step_id"] = true
		required = []string{"step_id"}
	case "move_step":
		allowed["step_id"], allowed["index"] = true, true
		required = []string{"step_id", "index"}
	case "set_graph":
		allowed["graph"] = true
		required = []string{"graph"}
	}
	for key := range op {
		if !allowed[key] {
			return fmt.Errorf("%s does not accept %s", name, key)
		}
	}
	for _, key := range required {
		v, exists := op[key]
		var text string
		if !exists || jsonKind(v) == "null" || jsonKind(v) == "string" && (json.Unmarshal(v, &text) != nil || strings.TrimSpace(text) == "") {
			return fmt.Errorf("%s needs %s", name, key)
		}
	}
	if value, exists := op["index"]; exists {
		var n int
		if json.Unmarshal(value, &n) != nil || n < 0 {
			return errors.New("step index must be a zero-based nonnegative integer")
		}
	}
	return nil
}

func (t *agentWorkflowTool) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if !agentWorkflowBackendAvailable(t.backend) {
		return Result{}, errors.New("internal workflow tools are unavailable in this conversation")
	}
	if err := t.ValidateArgs(raw); err != nil {
		return Result{}, err
	}
	out, err := t.backend.AgentWorkflowCall(ctx, t.group, raw)
	if err != nil {
		return Result{}, err
	}
	if len(out) > 2<<20 {
		return Result{}, errors.New("internal workflow host response exceeds 2 MiB")
	}
	var value map[string]any
	if err := json.Unmarshal(out, &value); err != nil || value == nil {
		return Result{}, errors.New("internal workflow host returned an invalid JSON object")
	}
	var request struct {
		Action string `json:"action"`
	}
	_ = json.Unmarshal(raw, &request)
	result := Result{Text: string(out), Payload: &ResultPayload{Text: string(out), Structured: append(json.RawMessage(nil), out...), Format: "json"}, Details: map[string]any{"type": "agent_workflow", "group": t.group, "action": request.Action, "result": value}}
	if ok, exists := value["ok"].(bool); exists && !ok {
		message := "Internal workflow operation failed; read structured diagnostics"
		if failure, yes := value["error"].(map[string]any); yes {
			if text, yes := failure["message"].(string); yes && text != "" {
				message = text
			}
		}
		return result, errors.New(message)
	}
	return result, nil
}
