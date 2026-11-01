// Package agentworkflow defines private, Agent-bound reusable procedures.
// It deliberately has no platform workflow, HTTP, credential or queue dependency.
package agentworkflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

var ErrInvalid = errors.New("agentworkflow: invalid definition")
var ErrConflict = errors.New("agentworkflow: conflicting revision")

const (
	Queued              = "queued"
	Running             = "running"
	WaitingConfirmation = "waiting_confirmation"
	Completed           = "completed"
	Failed              = "failed"
	Cancelled           = "cancelled"
	Interrupted         = "interrupted"
	Skipped             = "skipped"
	MaxSteps            = 200
	MaxPatchOperations  = 2*MaxSteps + 2
	MaxRequestBytes     = 4 << 20
)

type Input struct {
	Key      string        `json:"key"`
	Label    string        `json:"label,omitempty"`
	Required bool          `json:"required,omitempty"`
	Type     string        `json:"type,omitempty"`
	Options  []InputOption `json:"options,omitempty"`
	Default  string        `json:"default,omitempty"`
}
type InputOption struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
}
type Step struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	Title      string         `json:"title,omitempty"`
	Task       string         `json:"task,omitempty"`
	Prompt     string         `json:"prompt,omitempty"`
	OutputFile string         `json:"output_file,omitempty"`
	Condition  *Condition     `json:"condition,omitempty"`
	Skills     []string       `json:"skills,omitempty"`
	Shortcuts  []string       `json:"shortcuts,omitempty"`
	Code       *CodeSpec      `json:"code,omitempty"`
	Iteration  *IterationSpec `json:"iteration,omitempty"`
	Loop       *LoopSpec      `json:"loop,omitempty"`
	Policy     *CodePolicy    `json:"policy,omitempty"`
}

const (
	ToolApprovalStandard = "standard"
	ToolApprovalStrict   = "strict"
	ToolApprovalAuto     = "auto"
)

// EffectiveToolApprovalMode preserves the legacy default without writing it
// into retained definitions or changing their original hashes.
func (d Definition) EffectiveToolApprovalMode() string {
	if d.ToolApprovalMode == "" {
		return ToolApprovalStandard
	}
	return d.ToolApprovalMode
}

func ValidToolApprovalMode(mode string) bool {
	return mode == "" || mode == ToolApprovalStandard || mode == ToolApprovalStrict || mode == ToolApprovalAuto
}

type Definition struct {
	ID               string  `json:"id"`
	OwnerID          string  `json:"owner_id"`
	AgentID          string  `json:"agent_id"`
	Name             string  `json:"name"`
	Description      string  `json:"description,omitempty"`
	Revision         int     `json:"revision"`
	Inputs           []Input `json:"inputs"`
	Steps            []Step  `json:"steps"`
	Graph            *Graph  `json:"graph,omitempty"`
	ToolApprovalMode string  `json:"tool_approval_mode,omitempty"`
	EnabledVersion   string  `json:"enabled_version,omitempty"`
	DefinitionHash   string  `json:"definition_hash"`
	Archived         bool    `json:"archived,omitempty"`
	Deleted          bool    `json:"deleted,omitempty"`
	CreatedAt        int64   `json:"created_at"`
	UpdatedAt        int64   `json:"updated_at"`
}
type Operation struct {
	Op               string          `json:"op"`
	StepID           string          `json:"step_id,omitempty"`
	Step             *Step           `json:"step,omitempty"`
	Patch            json.RawMessage `json:"patch,omitempty"`
	Index            *int            `json:"index,omitempty"`
	Name             *string         `json:"name,omitempty"`
	Description      *string         `json:"description,omitempty"`
	Inputs           *[]Input        `json:"inputs,omitempty"`
	Graph            *Graph          `json:"graph,omitempty"`
	ToolApprovalMode *string         `json:"tool_approval_mode,omitempty"`
}
type EditRequest struct {
	Action           string      `json:"action"`
	WorkflowID       string      `json:"workflow_id,omitempty"`
	RequestID        string      `json:"request_id"`
	Name             string      `json:"name,omitempty"`
	Description      string      `json:"description,omitempty"`
	ExpectedRevision int         `json:"expected_revision,omitempty"`
	Inputs           []Input     `json:"inputs,omitempty"`
	Steps            []Step      `json:"steps,omitempty"`
	Graph            *Graph      `json:"graph,omitempty"`
	ToolApprovalMode string      `json:"tool_approval_mode,omitempty"`
	Operations       []Operation `json:"operations,omitempty"`
	VersionID        string      `json:"version_id,omitempty"`
	TargetRevision   int         `json:"target_revision,omitempty"`
}

// VersionSummary describes an actual retained definition. A content hash may
// appear at several save revisions; callers identify a snapshot by both.
type VersionSummary struct {
	VersionID      string   `json:"version_id"`
	Revision       int      `json:"revision"`
	Name           string   `json:"name"`
	UpdatedAt      int64    `json:"updated_at"`
	StepCount      int      `json:"step_count"`
	IsCurrentDraft bool     `json:"is_current_draft"`
	IsEnabled      bool     `json:"is_enabled"`
	Sources        []string `json:"sources"`
}

type VersionPage struct {
	Versions           []VersionSummary `json:"versions"`
	HasMore            bool             `json:"has_more"`
	NextBeforeRevision int              `json:"next_before_revision,omitempty"`
	EnabledVersion     *VersionSummary  `json:"enabled_version,omitempty"`
}
type InputFile struct {
	Source string `json:"source"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type StepRun struct {
	StepID         string          `json:"step_id"`
	Status         string          `json:"status"`
	Summary        string          `json:"summary,omitempty"`
	Artifact       string          `json:"artifact,omitempty"`
	ArtifactSHA256 string          `json:"artifact_sha256,omitempty"`
	Feedback       string          `json:"feedback,omitempty"`
	SessionID      string          `json:"session_id,omitempty"`
	ActiveRunID    string          `json:"active_run_id,omitempty"`
	StartedAt      int64           `json:"started_at,omitempty"`
	FinishedAt     int64           `json:"finished_at,omitempty"`
	Branch         string          `json:"branch,omitempty"`
	Output         json.RawMessage `json:"output,omitempty"`
	CodeInputs     json.RawMessage `json:"code_inputs,omitempty"`
	CodeExecution  *CodeExecution  `json:"code_execution,omitempty"`
	Attempts       []CodeAttempt   `json:"attempts,omitempty"`
	FallbackUsed   bool            `json:"fallback_used,omitempty"`
	Repeat         *RepeatRun      `json:"repeat,omitempty"`
}
type Run struct {
	ID                 string            `json:"id"`
	OwnerID            string            `json:"owner_id"`
	AgentID            string            `json:"agent_id"`
	WorkflowID         string            `json:"workflow_id"`
	VersionID          string            `json:"version_id"`
	AgentFingerprint   string            `json:"agent_fingerprint"`
	SourceSessionID    string            `json:"source_session_id,omitempty"`
	ExecutionSessionID string            `json:"execution_session_id,omitempty"`
	Workspace          string            `json:"workspace,omitempty"`
	Status             string            `json:"status"`
	Error              string            `json:"error,omitempty"`
	Summary            string            `json:"summary,omitempty"`
	RetryOf            string            `json:"retry_of,omitempty"`
	Definition         Definition        `json:"definition"`
	AgentSnapshot      json.RawMessage   `json:"agent_snapshot,omitempty"`
	Resources          *RunResources     `json:"resources,omitempty"`
	NodeTest           *NodeTest         `json:"node_test,omitempty"`
	Inputs             map[string]string `json:"inputs"`
	Files              []InputFile       `json:"files,omitempty"`
	Steps              []StepRun         `json:"steps"`
	CurrentStep        int               `json:"current_step"`
	Trial              bool              `json:"trial"`
	CancelRequested    bool              `json:"cancel_requested,omitempty"`
	Revision           int64             `json:"revision"`
	CreatedAt          int64             `json:"created_at"`
	UpdatedAt          int64             `json:"updated_at"`
}
type Diagnostic struct {
	Code      string `json:"code"`
	Severity  string `json:"severity"`
	StepID    string `json:"step_id,omitempty"`
	FieldPath string `json:"field_path,omitempty"`
	Message   string `json:"message"`
	FixHint   string `json:"fix_hint,omitempty"`
}

var identifier = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)
var placeholders = regexp.MustCompile(`\{\{\s*([^{}]*?)\s*\}\}`)

func SafePath(value string) bool {
	return value != "" && len(value) <= 1024 && utf8.ValidString(value) && !strings.ContainsAny(value, "\\\x00\r\n") && !strings.HasPrefix(value, "/") && !strings.Contains(value, ":") && path.Clean(value) == value && value != "." && value != ".." && !strings.HasPrefix(value, "../")
}
func ValidID(value string) bool   { return identifier.MatchString(value) }
func ValidHash(value string) bool { return sha256Hex.MatchString(value) }
func validText(value string, limit int) bool {
	return len(value) <= limit && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

// Hash covers exactly the reviewed content and revision, never private scope,
// timestamps, archived/deleted status, or the current enabled-version pointer.
func Hash(d Definition) string {
	v := struct {
		Name, Description string
		Revision          int
		Inputs            []Input
		Steps             []Step
		ToolApprovalMode  string `json:",omitempty"`
	}{d.Name, d.Description, d.Revision, d.Inputs, d.Steps, d.ToolApprovalMode}
	// Preserve the legacy wire representation exactly, including its field
	// names and order. Merely reading an old definition must not re-version it.
	b, _ := json.Marshal(v)
	if d.Graph != nil {
		b, _ = json.Marshal(struct {
			Name, Description string
			Revision          int
			Inputs            []Input
			Steps             []Step
			Graph             *Graph
			ToolApprovalMode  string `json:",omitempty"`
		}{d.Name, d.Description, d.Revision, d.Inputs, d.Steps, d.Graph, d.ToolApprovalMode})
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func Validate(d Definition) []Diagnostic {
	out := []Diagnostic{}
	add := func(code, step, field, message string) {
		out = append(out, Diagnostic{Code: code, Severity: "error", StepID: step, FieldPath: field, Message: message})
	}
	if strings.TrimSpace(d.Name) == "" || !validText(d.Name, 256) {
		add("invalid_name", "", "name", "需要不超过256字节的流程名称")
	}
	if !validText(d.Description, 8192) {
		add("invalid_description", "", "description", "流程说明过长或包含无效字符")
	}
	if !ValidToolApprovalMode(d.ToolApprovalMode) {
		add("invalid_tool_approval_mode", "", "tool_approval_mode", "默认工具审批仅支持standard、strict或auto")
	}
	if len(d.Inputs) > 32 {
		add("too_many_inputs", "", "inputs", "最多支持32个输入参数")
	}
	inputs := map[string]bool{}
	for index, i := range d.Inputs {
		if !ValidID(i.Key) || inputs[i.Key] || !validText(i.Label, 256) {
			add("invalid_input", "", "inputs", "参数名必须唯一且为有效标识符")
		}
		if err := validateInputDefinition(i); err != nil {
			add("invalid_input", "", fmt.Sprintf("inputs[%d]", index), err.Error())
		}
		inputs[i.Key] = true
	}
	stepCount := len(d.Steps)
	for _, step := range d.Steps {
		if repeatBody(step) != nil {
			stepCount++
		}
	}
	if len(d.Steps) == 0 || stepCount > MaxSteps {
		add("invalid_step_count", "", "steps", fmt.Sprintf("流程需要1–%d个节点", MaxSteps))
	}
	var graph *GraphAnalysis
	dependenciesKnown := true
	if d.Graph != nil {
		var diagnostics []Diagnostic
		graph, diagnostics = AnalyzeGraph(d)
		out = append(out, diagnostics...)
		dependenciesKnown = len(diagnostics) == 0
	}
	declaredSteps := map[string]bool{}
	resourceNamespaceReserved := false
	iterationNamespaceReserved := false
	for _, step := range d.Steps {
		declaredSteps[step.ID] = true
		iterationNamespaceReserved = iterationNamespaceReserved || step.Kind == "iteration"
	}
	for _, step := range ResourceSteps(d) {
		resourceNamespaceReserved = resourceNamespaceReserved || len(step.Skills)+len(step.Shortcuts) > 0
	}
	seen := map[string]bool{}
	files := map[string]bool{}
	for _, s := range d.Steps {
		if !ValidID(s.ID) || seen[s.ID] {
			add("invalid_step_id", s.ID, "id", "步骤ID必须唯一且为稳定标识符")
		}
		if !validText(s.Title, 256) {
			add("invalid_title", s.ID, "title", "步骤标题过长或包含无效字符")
		}
		if s.Kind != "agent" && s.Kind != "confirm" && s.Kind != "code" && s.Kind != "iteration" && s.Kind != "loop" && (d.Graph == nil || s.Kind != "condition" && s.Kind != "parallel" && s.Kind != "join") {
			add("invalid_kind", s.ID, "kind", "顺序流程支持agent/confirm/code/iteration/loop；图流程还支持condition/parallel/join")
		}
		if s.Iteration != nil && s.Kind != "iteration" || s.Loop != nil && s.Kind != "loop" {
			add("unexpected_repeat", s.ID, "kind", "只有对应的迭代或循环节点可以配置iteration/loop")
		}
		if s.Kind == "iteration" || s.Kind == "loop" {
			if err := ValidateRepeatSpec(s); err != nil {
				add("invalid_repeat", s.ID, s.Kind, err.Error())
			}
		}
		if s.Policy != nil && s.Kind != "code" {
			add("unexpected_code_policy", s.ID, "policy", "仅代码节点允许自动重试或备用输出策略")
		}
		if s.Kind != "code" && s.Code != nil {
			add("unexpected_code", s.ID, "code", "仅代码节点可以配置code")
		}
		if s.Kind == "code" {
			if err := ValidateCodeSpec(s.Code); err != nil {
				add("invalid_code", s.ID, "code", err.Error())
			}
			if s.Task != "" || s.Prompt != "" || s.OutputFile != "" {
				add("invalid_code_step", s.ID, "code", "代码节点不能包含Agent任务、确认提示或文件产物路径；结构化结果保存在output")
			}
			if err := ValidateCodePolicy(s.Code, s.Policy); err != nil {
				add("invalid_code_policy", s.ID, "policy", err.Error())
			}
		}
		if s.Kind != "condition" && s.Condition != nil {
			add("unexpected_condition", s.ID, "condition", "仅条件节点可以配置条件")
		}
		for _, resource := range []struct {
			field string
			ids   []string
		}{{"skills", s.Skills}, {"shortcuts", s.Shortcuts}} {
			if len(resource.ids) > 0 && s.Kind != "agent" {
				add("invalid_resources", s.ID, resource.field, "仅Agent节点可以绑定Skill或快捷方式")
			} else if err := ValidateResourceIDs(resource.ids); err != nil {
				add("invalid_resources", s.ID, resource.field, err.Error())
			}
		}
		if s.Kind == "condition" || s.Kind == "parallel" || s.Kind == "join" {
			if s.Task != "" || s.Prompt != "" || s.OutputFile != "" {
				add("invalid_control_step", s.ID, "kind", "控制节点不能包含执行任务、确认内容或产物路径")
			}
		}
		guaranteed := seen
		if d.Graph != nil {
			// A malformed graph has no complete dominator analysis. Still
			// reject missing nodes and self-references, but do not mislabel
			// declared upstream values until its connections can be checked.
			guaranteed = declaredSteps
			if dependenciesKnown && graph != nil {
				guaranteed = graph.Guaranteed[s.ID]
			}
		}
		if s.Kind == "condition" {
			for _, diagnostic := range validateCondition(s, inputs, guaranteed) {
				out = append(out, diagnostic)
			}
			if s.Condition != nil {
				if err := validateDataReference(d, s.ID, s.Condition.Source, inputs, guaranteed, ""); err != nil {
					add("invalid_condition_source", s.ID, "condition.source", err.Error())
				}
			}
		}
		if s.Kind == "code" && s.Code != nil {
			for key, binding := range s.Code.Inputs {
				if binding.Source != "" {
					if err := validateDataReference(d, s.ID, binding.Source, inputs, guaranteed, binding.Type); err != nil {
						add("invalid_code_binding", s.ID, "code.inputs."+key, err.Error())
					}
				}
			}
		}
		if s.Kind == "iteration" || s.Kind == "loop" {
			if err := validateRepeatReferences(d, s, inputs, guaranteed); err != nil {
				add("invalid_repeat_binding", s.ID, s.Kind, err.Error())
			}
		}
		if s.Kind == "agent" && (strings.TrimSpace(s.Task) == "" || s.Prompt != "") {
			add("invalid_agent_step", s.ID, "task", "Agent步骤需要task，不能填写确认prompt")
		}
		if s.Kind == "confirm" && (strings.TrimSpace(s.Prompt) == "" || s.Task != "" || s.OutputFile != "") {
			add("invalid_confirm_step", s.ID, "prompt", "确认步骤需要prompt，不能包含执行任务或产物文件")
		}
		if !validText(s.Task, 16384) || !validText(s.Prompt, 16384) {
			add("invalid_instruction", s.ID, "task", "步骤指令过长或包含无效字符")
		}
		if s.OutputFile != "" {
			if !SafePath(s.OutputFile) || files[s.OutputFile] {
				add("invalid_output_file", s.ID, "output_file", "产物必须是唯一、安全的相对路径")
			}
			// Graph node workspaces receive frozen input files and upstream
			// outputs together. Reserve their input directory, including on
			// case-insensitive development filesystems. Legacy hashes and
			// historical sequential definitions keep their original contract.
			if d.Graph != nil && strings.EqualFold(strings.SplitN(s.OutputFile, "/", 2)[0], "inputs") {
				add("reserved_output_path", s.ID, "output_file", "图流程产物不能使用保留的inputs资料目录")
			}
			if resourceNamespaceReserved && strings.EqualFold(strings.SplitN(s.OutputFile, "/", 2)[0], "resources") {
				add("reserved_resource_output", s.ID, "output_file", "resources是节点冻结资源的保留目录，绑定资源的流程请使用其它产物路径")
			}
			if iterationNamespaceReserved && strings.EqualFold(strings.SplitN(s.OutputFile, "/", 2)[0], "iteration-results") {
				add("reserved_iteration_output", s.ID, "output_file", "iteration-results是逐项产物的保留输入目录，请使用其它产物路径")
			}
			files[s.OutputFile] = true
		}
		for field, text := range map[string]string{"task": s.Task, "prompt": s.Prompt} {
			remaining := placeholders.ReplaceAllString(text, "")
			if strings.Contains(remaining, "{{") || strings.Contains(remaining, "}}") {
				add("invalid_template", s.ID, field, "模板括号不完整")
			}
			for _, m := range placeholders.FindAllStringSubmatch(text, -1) {
				source := strings.TrimSpace(m[1])
				r, parseErr := parseDataReference(source)
				if parseErr == nil && r.input != "" && !inputs[r.input] {
					add("unknown_input", s.ID, field, "引用的输入参数未声明: "+r.input)
				} else if parseErr == nil && r.step != "" && (r.step == s.ID || !guaranteed[r.step]) {
					add("unknown_step", s.ID, field, "只能引用保证已完成的前序步骤；分支结果请经汇聚节点summary引用："+r.step)
				} else if err := validateDataReference(d, s.ID, source, inputs, guaranteed, ""); err != nil {
					add("unsupported_template", s.ID, field, err.Error())
				}
			}
		}
		seen[s.ID] = true
	}
	return out
}

// Render performs data substitution only: no expression evaluation, arbitrary
// executable paths or control flow are available to workflow templates. Nested
// JSON field traversal is limited to explicitly named data values.
func Render(text string, inputs map[string]string, steps []StepRun) (string, error) {
	remaining := placeholders.ReplaceAllString(text, "")
	if strings.Contains(remaining, "{{") || strings.Contains(remaining, "}}") {
		return "", fmt.Errorf("%w: malformed template", ErrInvalid)
	}
	results := map[string]StepRun{}
	for _, s := range steps {
		results[s.StepID] = s
	}
	var failure error
	renderToken := func(token string) string {
		m := placeholders.FindStringSubmatch(token)
		r, err := parseDataReference(strings.TrimSpace(m[1]))
		if err != nil {
			failure = fmt.Errorf("%w: unsupported template %s", ErrInvalid, token)
			return ""
		}
		if r.input != "" {
			v, ok := inputs[r.input]
			if !ok {
				failure = fmt.Errorf("%w: missing input %s", ErrInvalid, r.input)
			}
			if len(r.path) > 0 {
				value, err := decodeCodeJSON([]byte(v))
				if err == nil {
					value, err = traverseData(value, r.path)
				}
				if err != nil {
					failure = err
					return ""
				}
				return renderDataValue(value)
			}
			return v
		}
		s, ok := results[r.step]
		if !ok || s.Status != Completed {
			failure = fmt.Errorf("%w: previous step is unavailable %s", ErrInvalid, r.step)
			return ""
		}
		switch r.field {
		case "summary":
			return s.Summary
		case "artifact":
			return s.Artifact
		case "output":
			value, err := decodeCodeJSON(s.Output)
			if err == nil {
				value, err = traverseData(value, r.path)
			}
			if err != nil {
				failure = err
				return ""
			}
			return renderDataValue(value)
		default:
			return s.Feedback
		}
	}
	// Preserve legacy substitution semantics. New typed output references can
	// each carry 256KiB, so guard their combined expansion before writing into
	// the builder rather than allocating the complete expanded prompt first.
	bounded := false
	for _, match := range placeholders.FindAllStringSubmatch(text, -1) {
		if r, err := parseDataReference(strings.TrimSpace(match[1])); err == nil && r.field == "output" {
			bounded = true
			break
		}
	}
	if bounded {
		var builder strings.Builder
		write := func(value string) error {
			if len(value) > MaxRenderedDataBytes-builder.Len() {
				return fmt.Errorf("%w: 结构化模板展开结果超过1MiB，请精简引用数据", ErrInvalid)
			}
			builder.WriteString(value)
			return nil
		}
		cursor := 0
		for _, index := range placeholders.FindAllStringIndex(text, -1) {
			if err := write(text[cursor:index[0]]); err != nil {
				return "", err
			}
			value := renderToken(text[index[0]:index[1]])
			if failure != nil {
				return "", failure
			}
			if err := write(value); err != nil {
				return "", err
			}
			cursor = index[1]
		}
		if err := write(text[cursor:]); err != nil {
			return "", err
		}
		return builder.String(), nil
	}
	out := placeholders.ReplaceAllStringFunc(text, renderToken)
	return out, failure
}

// Patch modifies a private copy. Any invalid operation or invalid resulting
// definition rejects the complete batch without retaining a partial edit.
func Patch(d Definition, ops []Operation) (Definition, error) {
	b, _ := json.Marshal(d)
	var next Definition
	_ = json.Unmarshal(b, &next)
	if len(ops) == 0 || len(ops) > MaxPatchOperations {
		return d, fmt.Errorf("%w: need 1–%d patch operations", ErrInvalid, MaxPatchOperations)
	}
	find := func(id string) int {
		for i, s := range next.Steps {
			if s.ID == id {
				return i
			}
		}
		return -1
	}
	for _, o := range ops {
		i := find(o.StepID)
		switch o.Op {
		case "set_metadata":
			if o.ToolApprovalMode != nil {
				if *o.ToolApprovalMode == "" || !ValidToolApprovalMode(*o.ToolApprovalMode) {
					return d, fmt.Errorf("%w: invalid tool_approval_mode", ErrInvalid)
				}
				next.ToolApprovalMode = *o.ToolApprovalMode
			}
			if o.Name != nil {
				next.Name = *o.Name
			}
			if o.Description != nil {
				next.Description = *o.Description
			}
			if o.Inputs != nil {
				next.Inputs = cloneInputs(*o.Inputs)
			}
		case "set_graph":
			if o.Graph == nil {
				return d, fmt.Errorf("%w: set_graph needs a graph", ErrInvalid)
			}
			graph := *o.Graph
			graph.Edges = append([]Edge(nil), o.Graph.Edges...)
			next.Graph = &graph
		case "add_step":
			if o.Step == nil || find(o.Step.ID) >= 0 {
				return d, fmt.Errorf("%w: step missing or exists", ErrInvalid)
			}
			idx := len(next.Steps)
			if o.Index != nil {
				idx = *o.Index
			}
			if idx < 0 || idx > len(next.Steps) {
				return d, ErrInvalid
			}
			next.Steps = append(next.Steps, Step{})
			copy(next.Steps[idx+1:], next.Steps[idx:])
			next.Steps[idx] = cloneStep(*o.Step)
		case "update_step":
			if i < 0 || len(o.Patch) == 0 {
				return d, fmt.Errorf("%w: update_step needs step_id and patch", ErrInvalid)
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(o.Patch, &fields) != nil || len(fields) == 0 {
				return d, ErrInvalid
			}
			raw, _ := json.Marshal(next.Steps[i])
			var full map[string]json.RawMessage
			_ = json.Unmarshal(raw, &full)
			for k, v := range fields {
				switch k {
				case "kind", "title", "task", "prompt", "output_file", "condition", "skills", "shortcuts", "code", "iteration", "loop", "policy":
					full[k] = v
				default:
					return d, fmt.Errorf("%w: unsupported step patch field %s", ErrInvalid, k)
				}
			}
			raw, _ = json.Marshal(full)
			// Code supports partial scalar updates, but explicitly supplied
			// binding/schema maps replace their prior contents. Unmarshal would
			// otherwise retain removed map keys and silently change the contract.
			if codePatch, changed := fields["code"]; changed && next.Steps[i].Code != nil {
				var codeFields map[string]json.RawMessage
				if json.Unmarshal(codePatch, &codeFields) != nil {
					return d, ErrInvalid
				}
				if _, changed := codeFields["inputs"]; changed {
					next.Steps[i].Code.Inputs = nil
				}
				if _, changed := codeFields["outputs"]; changed {
					next.Steps[i].Code.Outputs = nil
				}
			}
			// These new compound fields are complete reviewed values. Decode
			// replacements into fresh pointers so omitted nested keys cannot
			// survive from the previous container/policy configuration.
			if _, changed := fields["iteration"]; changed {
				next.Steps[i].Iteration = nil
			}
			if _, changed := fields["loop"]; changed {
				next.Steps[i].Loop = nil
			}
			if _, changed := fields["policy"]; changed {
				next.Steps[i].Policy = nil
			}
			if json.Unmarshal(raw, &next.Steps[i]) != nil {
				return d, ErrInvalid
			}
		case "remove_step":
			if i < 0 {
				return d, ErrInvalid
			}
			next.Steps = append(next.Steps[:i], next.Steps[i+1:]...)
		case "move_step":
			if i < 0 || o.Index == nil || *o.Index < 0 || *o.Index >= len(next.Steps) {
				return d, ErrInvalid
			}
			s := next.Steps[i]
			next.Steps = append(next.Steps[:i], next.Steps[i+1:]...)
			idx := *o.Index
			next.Steps = append(next.Steps, Step{})
			copy(next.Steps[idx+1:], next.Steps[idx:])
			next.Steps[idx] = s
		default:
			return d, fmt.Errorf("%w: unsupported operation %s", ErrInvalid, o.Op)
		}
	}
	if ds := Validate(next); len(ds) > 0 {
		return d, fmt.Errorf("%w: %s", ErrInvalid, ds[0].Message)
	}
	return next, nil
}
