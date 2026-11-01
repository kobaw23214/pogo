package agentworkflow

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const (
	MaxRepeatItems        = 100
	MaxRepeatConcurrency  = 10
	MaxCodeRetries        = 2
	MaxRepeatItemBytes    = 64 << 10
	MaxRepeatReceiptBytes = 2 << 20
)

type IterationSpec struct {
	Items       CodeBinding `json:"items"`
	Body        *Step       `json:"body"`
	Concurrency int         `json:"concurrency,omitempty"`
	MaxItems    int         `json:"max_items,omitempty"`
	OnItemError string      `json:"on_item_error,omitempty"`
}

type LoopSpec struct {
	Initial       CodeBinding `json:"initial"`
	Body          *Step       `json:"body"`
	Until         Condition   `json:"until"`
	MaxIterations int         `json:"max_iterations"`
}

// Policies are restricted to isolated pure-data code. They never authorize
// automatic repetition of an Agent's possibly external side effects.
type CodePolicy struct {
	MaxRetries   int             `json:"max_retries,omitempty"`
	RetryDelayMS int             `json:"retry_delay_ms,omitempty"`
	OnFailure    string          `json:"on_failure,omitempty"`
	Fallback     json.RawMessage `json:"fallback,omitempty"`
}

type CodeAttempt struct {
	Number     int             `json:"number"`
	StartedAt  int64           `json:"started_at"`
	FinishedAt int64           `json:"finished_at,omitempty"`
	Execution  *CodeExecution  `json:"execution,omitempty"`
	Output     json.RawMessage `json:"output,omitempty"`
	Error      string          `json:"error,omitempty"`
}

type RepeatRun struct {
	Kind         string          `json:"kind"`
	Total        int             `json:"total"`
	Items        []RepeatItem    `json:"items"`
	State        json.RawMessage `json:"state,omitempty"`
	LimitReached bool            `json:"limit_reached,omitempty"`
}

type RepeatItem struct {
	Index          int             `json:"index"`
	Status         string          `json:"status"`
	Input          json.RawMessage `json:"input"`
	Output         json.RawMessage `json:"output,omitempty"`
	Summary        string          `json:"summary,omitempty"`
	Error          string          `json:"error,omitempty"`
	SessionID      string          `json:"session_id,omitempty"`
	ActiveRunID    string          `json:"active_run_id,omitempty"`
	CodeInputs     json.RawMessage `json:"code_inputs,omitempty"`
	CodeExecution  *CodeExecution  `json:"code_execution,omitempty"`
	Attempts       []CodeAttempt   `json:"attempts,omitempty"`
	FallbackUsed   bool            `json:"fallback_used,omitempty"`
	Artifact       string          `json:"artifact,omitempty"`
	ArtifactSHA256 string          `json:"artifact_sha256,omitempty"`
	StartedAt      int64           `json:"started_at,omitempty"`
	FinishedAt     int64           `json:"finished_at,omitempty"`
}

// RecoverableCodeExecution requires a real exited process and an explicit
// script/data/resource failure. Infrastructure failures before execution,
// missing isolation, revoked authorization and cancellation never qualify.
func RecoverableCodeExecution(receipt *CodeExecution) bool {
	if receipt == nil || receipt.ExitCode == nil {
		return false
	}
	switch receipt.ErrorCode {
	case "code_error", "invalid_output", "output_schema_invalid", "timeout", "memory_limit", "output_limit":
		return true
	default:
		return false
	}
}

func ValidateCodePolicy(spec *CodeSpec, policy *CodePolicy) error {
	if policy == nil {
		return nil
	}
	if err := ValidateCodeSpec(spec); err != nil {
		return err
	}
	if policy.MaxRetries < 0 || policy.MaxRetries > MaxCodeRetries || policy.RetryDelayMS < 0 || policy.RetryDelayMS > 5000 {
		return fmt.Errorf("代码最多重试2次，重试间隔需要0–5000毫秒")
	}
	if policy.OnFailure != "" && policy.OnFailure != "stop" && policy.OnFailure != "fallback" {
		return fmt.Errorf("代码失败策略仅支持stop或fallback")
	}
	if policy.OnFailure == "fallback" {
		return ValidateCodeOutput(spec, policy.Fallback)
	}
	if len(policy.Fallback) != 0 {
		return fmt.Errorf("只有fallback策略可以填写备用输出")
	}
	return nil
}

type repeatReference struct {
	mode, field string
	path        []string
}

func parseRepeatReference(source string) (repeatReference, error) {
	parts := strings.Split(source, ".")
	if len(source) > 1024 || len(parts) < 2 || len(parts) > MaxCodeSchemaDepth+3 {
		return repeatReference{}, fmt.Errorf("无效局部数据引用: %s", source)
	}
	r := repeatReference{mode: parts[0], field: parts[1], path: parts[2:]}
	if !(r.mode == "iteration" && (r.field == "item" || r.field == "index") || r.mode == "loop" && (r.field == "state" || r.field == "index")) || r.field == "index" && len(r.path) > 0 {
		return r, fmt.Errorf("无效局部数据引用: %s", source)
	}
	for _, part := range r.path {
		if !ValidID(part) {
			n, err := strconv.Atoi(part)
			if err != nil || n < 0 || strconv.Itoa(n) != part {
				return r, fmt.Errorf("无效局部数据字段: %s", part)
			}
		}
	}
	return r, nil
}

func validateRepeatBinding(binding CodeBinding, kind string) error {
	if binding.Type != kind {
		return fmt.Errorf("此数据来源需要%s类型", kind)
	}
	if (binding.Source != "") == (len(binding.Value) > 0) {
		return fmt.Errorf("数据来源需要且只能填写source或value")
	}
	if binding.Source != "" {
		_, err := parseDataReference(binding.Source)
		return err
	}
	value, err := decodeCodeJSON(binding.Value)
	if err != nil {
		return err
	}
	return validateTypedValue(ValueSchema{Type: kind}, value, "value")
}

func repeatBody(step Step) *Step {
	if step.Kind == "iteration" && step.Iteration != nil {
		return step.Iteration.Body
	}
	if step.Kind == "loop" && step.Loop != nil {
		return step.Loop.Body
	}
	return nil
}

func ValidateRepeatSpec(step Step) error {
	var body *Step
	mode := step.Kind
	switch mode {
	case "iteration":
		spec := step.Iteration
		if spec == nil || step.Loop != nil {
			return fmt.Errorf("集合迭代需要iteration配置")
		}
		if err := validateRepeatBinding(spec.Items, "array"); err != nil {
			return err
		}
		if spec.Concurrency < 0 || spec.Concurrency > MaxRepeatConcurrency || spec.MaxItems < 0 || spec.MaxItems > MaxRepeatItems {
			return fmt.Errorf("迭代并发需要1–10，项数上限需要1–100；省略分别为1和100")
		}
		if spec.OnItemError != "" && spec.OnItemError != "stop" && spec.OnItemError != "continue" {
			return fmt.Errorf("逐项错误处理仅支持stop或continue")
		}
		body = spec.Body
	case "loop":
		spec := step.Loop
		if spec == nil || step.Iteration != nil {
			return fmt.Errorf("有界循环需要loop配置")
		}
		if err := validateRepeatBinding(spec.Initial, "object"); err != nil {
			return err
		}
		if spec.MaxIterations < 1 || spec.MaxIterations > MaxRepeatItems {
			return fmt.Errorf("循环必须明确填写1–100次的执行上限")
		}
		if err := ValidateLoopUntil(spec); err != nil {
			return err
		}
		body = spec.Body
	default:
		return fmt.Errorf("不是迭代或循环节点")
	}
	if step.Task != "" || step.Prompt != "" || step.OutputFile != "" || step.Code != nil || step.Condition != nil || step.Policy != nil || len(step.Skills)+len(step.Shortcuts) > 0 {
		return fmt.Errorf("迭代容器不能包含任务或能力；请在body中配置")
	}
	if body == nil || body.Kind != "agent" && body.Kind != "code" || mode == "loop" && body.Kind != "code" {
		return fmt.Errorf("迭代体需要单个Agent或代码节点；循环体当前仅支持代码")
	}
	if body.Iteration != nil || body.Loop != nil || body.Condition != nil || body.ID != "" && !ValidID(body.ID) || !validText(body.Title, 256) || !validText(body.Task, 16384) || !validText(body.Prompt, 16384) {
		return fmt.Errorf("迭代体不能嵌套控制节点且ID/指令必须有效")
	}
	if body.Kind == "agent" {
		if strings.TrimSpace(body.Task) == "" || body.Prompt != "" || body.Code != nil || body.Policy != nil {
			return fmt.Errorf("Agent迭代体需要task，不能配置代码策略")
		}
		if body.OutputFile != "" && (!SafePath(body.OutputFile) || strings.EqualFold(strings.SplitN(body.OutputFile, "/", 2)[0], "inputs") || strings.EqualFold(strings.SplitN(body.OutputFile, "/", 2)[0], "resources") || strings.EqualFold(strings.SplitN(body.OutputFile, "/", 2)[0], "iteration-results")) {
			return fmt.Errorf("Agent迭代产物需要安全且不使用保留目录的相对路径")
		}
		if err := ValidateResourceIDs(body.Skills); err != nil {
			return err
		}
		if err := ValidateResourceIDs(body.Shortcuts); err != nil {
			return err
		}
	} else {
		if body.Task != "" || body.Prompt != "" || body.OutputFile != "" || len(body.Skills)+len(body.Shortcuts) > 0 {
			return fmt.Errorf("代码迭代体只处理结构化输入输出，不能声明Agent任务或文件产物")
		}
		if err := ValidateCodeSpec(body.Code); err != nil {
			return err
		}
		if err := ValidateCodePolicy(body.Code, body.Policy); err != nil {
			return err
		}
	}
	for _, text := range []string{body.Task, body.Prompt} {
		remaining := placeholders.ReplaceAllString(text, "")
		if strings.Contains(remaining, "{{") || strings.Contains(remaining, "}}") {
			return fmt.Errorf("迭代任务模板括号不完整")
		}
		for _, token := range placeholders.FindAllStringSubmatch(text, -1) {
			source := strings.TrimSpace(token[1])
			if local, err := parseRepeatReference(source); err == nil {
				if local.mode != mode {
					return fmt.Errorf("迭代局部变量作用域不匹配")
				}
			} else if _, err := parseDataReference(source); err != nil {
				return err
			}
		}
	}
	if body.Code != nil {
		for _, binding := range body.Code.Inputs {
			if binding.Source != "" {
				if local, err := parseRepeatReference(binding.Source); err == nil && local.mode != mode {
					return fmt.Errorf("代码迭代局部变量作用域不匹配")
				}
			}
		}
	}
	return nil
}

func validateRepeatReferences(d Definition, step Step, inputs, guaranteed map[string]bool) error {
	if err := ValidateRepeatSpec(step); err != nil {
		return err
	}
	binding := CodeBinding{}
	if step.Iteration != nil {
		binding = step.Iteration.Items
	} else {
		binding = step.Loop.Initial
	}
	if binding.Source != "" {
		if err := validateDataReference(d, step.ID, binding.Source, inputs, guaranteed, binding.Type); err != nil {
			return err
		}
	}
	body := repeatBody(step)
	if step.Loop != nil {
		local, _ := parseRepeatReference(step.Loop.Until.Source)
		if local.field == "state" {
			source := "steps.body_output.output"
			if len(local.path) > 0 {
				source += "." + strings.Join(local.path, ".")
			}
			expected := ""
			if op := step.Loop.Until.Operator; op == "gt" || op == "gte" || op == "lt" || op == "lte" {
				expected = "number"
			}
			if err := validateDataReference(Definition{Steps: []Step{{ID: "body_output", Kind: "code", Code: body.Code}}}, "until_predicate", source, nil, map[string]bool{"body_output": true}, expected); err != nil {
				return fmt.Errorf("循环退出条件: %w", err)
			}
		}
	}
	check := func(source, expected string) error {
		if local, err := parseRepeatReference(source); err == nil {
			if local.mode != step.Kind {
				return fmt.Errorf("局部变量不属于当前迭代体")
			}
			if local.field == "index" && expected != "" && expected != "integer" && expected != "number" {
				return fmt.Errorf("局部index需要integer或number输入")
			}
			if local.field == "state" && len(local.path) == 0 && expected != "" && expected != "object" {
				return fmt.Errorf("loop.state需要object输入")
			}
			return nil
		}
		return validateDataReference(d, step.ID, source, inputs, guaranteed, expected)
	}
	if body.Code != nil {
		for _, input := range body.Code.Inputs {
			if input.Source != "" {
				if err := check(input.Source, input.Type); err != nil {
					return err
				}
			}
		}
	}
	for _, text := range []string{body.Task, body.Prompt} {
		for _, token := range placeholders.FindAllStringSubmatch(text, -1) {
			if err := check(strings.TrimSpace(token[1]), ""); err != nil {
				return err
			}
		}
	}
	return nil
}

func ValidateLoopUntil(spec *LoopSpec) error {
	if spec == nil {
		return fmt.Errorf("循环配置缺失")
	}
	r, err := parseRepeatReference(spec.Until.Source)
	if err != nil || r.mode != "loop" {
		return fmt.Errorf("循环退出条件只能引用loop.state或loop.index")
	}
	c := spec.Until
	c.Source = "inputs.value"
	ds := validateCondition(Step{ID: "until", Kind: "condition", Condition: &c}, map[string]bool{"value": true}, map[string]bool{})
	if len(ds) > 0 {
		return fmt.Errorf("%s", ds[0].Message)
	}
	return nil
}

func EvaluateLoopUntil(spec *LoopSpec, state json.RawMessage, index int) (bool, error) {
	if err := ValidateLoopUntil(spec); err != nil {
		return false, err
	}
	value, err := resolveRepeatReference(spec.Until.Source, "loop", state, index)
	if err != nil {
		return false, err
	}
	c := spec.Until
	c.Source = "inputs.value"
	return EvaluateCondition(Step{Kind: "condition", Condition: &c}, map[string]string{"value": renderDataValue(value)}, nil)
}

func resolveRepeatReference(source, mode string, value json.RawMessage, index int) (any, error) {
	r, err := parseRepeatReference(source)
	if err != nil {
		return nil, err
	}
	if r.mode != mode || index < 0 || index >= MaxRepeatItems {
		return nil, fmt.Errorf("局部数据作用域或下标无效")
	}
	if r.field == "index" {
		return json.Number(strconv.Itoa(index)), nil
	}
	decoded, err := decodeCodeJSON(value)
	if err != nil {
		return nil, err
	}
	return traverseData(decoded, r.path)
}

func resolveOuterBinding(run Run, binding CodeBinding) (json.RawMessage, error) {
	if binding.Source == "" {
		return append(json.RawMessage(nil), binding.Value...), nil
	}
	r, err := parseDataReference(binding.Source)
	if err != nil {
		return nil, err
	}
	var value any
	if r.input != "" {
		inputs, e := TypedInputs(run.Definition, run.Inputs)
		if e != nil {
			return nil, e
		}
		raw, ok := inputs[r.input]
		if !ok {
			return nil, fmt.Errorf("缺少输入%s", r.input)
		}
		value, err = decodeCodeJSON(raw)
	} else {
		value, err = stepReferenceValue(run.Steps, r)
	}
	if err != nil {
		return nil, err
	}
	value, err = traverseData(value, r.path)
	if err != nil {
		return nil, err
	}
	if err = validateTypedValue(ValueSchema{Type: binding.Type}, value, "binding"); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func findRepeatStep(run Run, id string) (Step, error) {
	for _, step := range run.Definition.Steps {
		if step.ID == id {
			if err := ValidateRepeatSpec(step); err != nil {
				return step, err
			}
			return step, nil
		}
	}
	return Step{}, fmt.Errorf("迭代节点不存在")
}

func ResolveIterationItems(run Run, parentID string) ([]json.RawMessage, error) {
	step, err := findRepeatStep(run, parentID)
	if err != nil {
		return nil, err
	}
	if step.Iteration == nil {
		return nil, fmt.Errorf("不是集合迭代")
	}
	raw, err := resolveOuterBinding(run, step.Iteration.Items)
	if err != nil {
		return nil, err
	}
	value, err := decodeCodeJSON(raw)
	if err != nil {
		return nil, err
	}
	array, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("迭代来源必须为数组")
	}
	limit := step.Iteration.MaxItems
	if limit == 0 {
		limit = MaxRepeatItems
	}
	if len(array) > limit {
		return nil, fmt.Errorf("迭代项数超过上限%d", limit)
	}
	out := make([]json.RawMessage, len(array))
	for i, item := range array {
		out[i], err = json.Marshal(item)
		if err != nil {
			return nil, err
		}
		if len(out[i]) > MaxRepeatItemBytes {
			return nil, fmt.Errorf("第%d项输入超过64KiB", i)
		}
	}
	return out, nil
}

func ResolveLoopInitial(run Run, parentID string) (json.RawMessage, error) {
	step, err := findRepeatStep(run, parentID)
	if err != nil {
		return nil, err
	}
	if step.Loop == nil {
		return nil, fmt.Errorf("不是有界循环")
	}
	raw, err := resolveOuterBinding(run, step.Loop.Initial)
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxRepeatItemBytes {
		return nil, fmt.Errorf("循环初始状态超过64KiB")
	}
	value, err := decodeCodeJSON(raw)
	if err != nil {
		return nil, err
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("循环状态需要JSON对象")
	}
	return raw, nil
}

// ResolveRepeatBody replaces only original template tokens. Data containing
// template-like text is never scanned or executed a second time.
func ResolveRepeatBody(run Run, parentID string, value json.RawMessage, index int) (Step, error) {
	parent, err := findRepeatStep(run, parentID)
	if err != nil {
		return Step{}, err
	}
	if index < 0 || index >= MaxRepeatItems || len(value) > MaxRepeatItemBytes {
		return Step{}, fmt.Errorf("逐项输入或执行下标超过限制")
	}
	decoded, err := decodeCodeJSON(value)
	if err != nil {
		return Step{}, err
	}
	if parent.Kind == "loop" {
		if _, ok := decoded.(map[string]any); !ok {
			return Step{}, fmt.Errorf("循环状态需要JSON对象")
		}
	}
	body := cloneStep(*repeatBody(parent))
	body.ID = parentID
	if body.Code != nil {
		for name, binding := range body.Code.Inputs {
			if binding.Source != "" {
				var raw json.RawMessage
				if _, e := parseRepeatReference(binding.Source); e == nil {
					decoded, e := resolveRepeatReference(binding.Source, parent.Kind, value, index)
					if e != nil {
						return Step{}, e
					}
					if e = validateTypedValue(ValueSchema{Type: binding.Type}, decoded, "inputs."+name); e != nil {
						return Step{}, e
					}
					raw, err = json.Marshal(decoded)
				} else {
					raw, err = resolveOuterBinding(run, binding)
				}
				if err != nil {
					return Step{}, err
				}
				binding.Source = ""
				binding.Value = raw
				body.Code.Inputs[name] = binding
			}
		}
	}
	for field, text := range map[string]string{"task": body.Task, "prompt": body.Prompt} {
		var builder strings.Builder
		cursor := 0
		appendText := func(s string) error {
			if len(s) > MaxRenderedDataBytes-builder.Len() {
				return fmt.Errorf("迭代任务模板展开超过1MiB")
			}
			builder.WriteString(s)
			return nil
		}
		for _, at := range placeholders.FindAllStringIndex(text, -1) {
			if err = appendText(text[cursor:at[0]]); err != nil {
				return Step{}, err
			}
			token := text[at[0]:at[1]]
			source := strings.TrimSpace(placeholders.FindStringSubmatch(token)[1])
			var rendered string
			if _, e := parseRepeatReference(source); e == nil {
				decoded, e := resolveRepeatReference(source, parent.Kind, value, index)
				if e != nil {
					return Step{}, e
				}
				rendered = renderDataValue(decoded)
			} else {
				rendered, err = Render(token, run.Inputs, run.Steps)
				if err != nil {
					return Step{}, err
				}
			}
			if err = appendText(rendered); err != nil {
				return Step{}, err
			}
			cursor = at[1]
		}
		if err = appendText(text[cursor:]); err != nil {
			return Step{}, err
		}
		if field == "task" {
			body.Task = builder.String()
		} else {
			body.Prompt = builder.String()
		}
	}
	return body, nil
}

// ResourceSteps projects one executable resource owner per outer node. The
// same frozen Agent-body resources are copied separately for each item.
func ResourceSteps(d Definition) []Step {
	steps := make([]Step, 0, len(d.Steps))
	for _, step := range d.Steps {
		if body := repeatBody(step); body != nil {
			bodyCopy := cloneStep(*body)
			bodyCopy.ID = step.ID
			steps = append(steps, bodyCopy)
		} else {
			steps = append(steps, step)
		}
	}
	return steps
}

func cloneIteration(spec *IterationSpec) *IterationSpec {
	if spec == nil {
		return nil
	}
	raw, _ := json.Marshal(spec)
	var out IterationSpec
	_ = json.Unmarshal(raw, &out)
	return &out
}
func cloneLoop(spec *LoopSpec) *LoopSpec {
	if spec == nil {
		return nil
	}
	raw, _ := json.Marshal(spec)
	var out LoopSpec
	_ = json.Unmarshal(raw, &out)
	return &out
}
func clonePolicy(policy *CodePolicy) *CodePolicy {
	if policy == nil {
		return nil
	}
	next := *policy
	next.Fallback = append(json.RawMessage(nil), policy.Fallback...)
	return &next
}
