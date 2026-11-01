package agentworkflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"
)

const (
	MaxCodeSourceBytes        = 64 << 10
	MaxCodeDataBytes          = 256 << 10
	DefaultCodeTimeoutSeconds = 10
	MaxCodeTimeoutSeconds     = 60
	MaxCodeFields             = 64
	MaxCodeSchemaDepth        = 8
	MaxRenderedDataBytes      = 1 << 20
)

// CodeSpec is executable version content. It grants no tools, files, network or
// credentials; the host chooses an available isolated language runner.
type CodeSpec struct {
	Language       string                 `json:"language"`
	Source         string                 `json:"source"`
	Inputs         map[string]CodeBinding `json:"inputs"`
	Outputs        map[string]ValueSchema `json:"outputs"`
	TimeoutSeconds int                    `json:"timeout_seconds,omitempty"`
}

type CodeBinding struct {
	Type   string          `json:"type"`
	Source string          `json:"source,omitempty"`
	Value  json.RawMessage `json:"value,omitempty"`
}

// ValueSchema is a bounded JSON data contract, not an expression language.
// Objects with nonempty Properties reject undeclared properties; every
// CodeSpec.Outputs field is required. Empty/omitted Properties are equivalent.
type ValueSchema struct {
	Type       string                 `json:"type"`
	Items      *ValueSchema           `json:"items,omitempty"`
	Properties map[string]ValueSchema `json:"properties,omitempty"`
	Required   []string               `json:"required,omitempty"`
}

type CodeExecution struct {
	Language        string `json:"language"`
	Runtime         string `json:"runtime,omitempty"`
	SourceSHA256    string `json:"source_sha256"`
	Stdout          string `json:"stdout,omitempty"`
	Stderr          string `json:"stderr,omitempty"`
	StdoutTruncated bool   `json:"stdout_truncated,omitempty"`
	StderrTruncated bool   `json:"stderr_truncated,omitempty"`
	ExitCode        *int   `json:"exit_code,omitempty"`
	TimedOut        bool   `json:"timed_out,omitempty"`
	DurationMS      int64  `json:"duration_ms"`
	ErrorCode       string `json:"error_code,omitempty"`
}

// NodeTest records the user's explicit sample arguments with the frozen Run.
// It never borrows upstream values from another Run implicitly.
type NodeTest struct {
	StepID string          `json:"step_id"`
	Inputs json.RawMessage `json:"inputs"`
}

func codeType(kind string) bool {
	switch kind {
	case "string", "number", "integer", "boolean", "object", "array":
		return true
	}
	return false
}

func decodeCodeJSON(raw []byte) (any, error) {
	if len(raw) == 0 || len(raw) > MaxCodeDataBytes {
		return nil, fmt.Errorf("JSON数据需要1–%d字节", MaxCodeDataBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("无效JSON: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("只允许一个JSON值")
	}
	if err := boundedJSON(value, 0, new(int)); err != nil {
		return nil, err
	}
	return value, nil
}

func boundedJSON(value any, depth int, count *int) error {
	(*count)++
	if depth > 32 || *count > 32768 {
		return fmt.Errorf("JSON嵌套或元素数量超出限制")
	}
	switch value := value.(type) {
	case map[string]any:
		for _, child := range value {
			if err := boundedJSON(child, depth+1, count); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range value {
			if err := boundedJSON(child, depth+1, count); err != nil {
				return err
			}
		}
	case json.Number:
		n, err := strconv.ParseFloat(string(value), 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return fmt.Errorf("只支持有限JSON数值")
		}
	}
	return nil
}

func validateValueSchema(schema ValueSchema, depth int, count *int) error {
	(*count)++
	if depth > MaxCodeSchemaDepth || *count > 256 || !codeType(schema.Type) {
		return fmt.Errorf("输出类型无效或结构过深")
	}
	if schema.Type == "array" {
		if schema.Items == nil {
			return fmt.Errorf("数组输出必须声明items类型")
		}
		if err := validateValueSchema(*schema.Items, depth+1, count); err != nil {
			return err
		}
	} else if schema.Items != nil {
		return fmt.Errorf("只有数组类型能声明items")
	}
	if schema.Type != "object" && (len(schema.Properties) > 0 || len(schema.Required) > 0) {
		return fmt.Errorf("只有对象类型能声明properties/required")
	}
	if len(schema.Properties) > MaxCodeFields || len(schema.Required) > MaxCodeFields {
		return fmt.Errorf("对象字段过多")
	}
	for name, child := range schema.Properties {
		if !ValidID(name) {
			return fmt.Errorf("对象字段名无效: %s", name)
		}
		if err := validateValueSchema(child, depth+1, count); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, name := range schema.Required {
		if _, ok := schema.Properties[name]; !ok || seen[name] {
			return fmt.Errorf("required必须引用唯一的已声明字段")
		}
		seen[name] = true
	}
	return nil
}

func validateTypedValue(schema ValueSchema, value any, path string) error {
	valid := false
	switch schema.Type {
	case "string":
		_, valid = value.(string)
	case "boolean":
		_, valid = value.(bool)
	case "number", "integer":
		if number, ok := value.(json.Number); ok {
			n, err := strconv.ParseFloat(string(number), 64)
			valid = err == nil && !math.IsNaN(n) && !math.IsInf(n, 0)
			if schema.Type == "integer" {
				exact, ok := new(big.Rat).SetString(string(number))
				valid = valid && ok && exact.IsInt()
			}
		}
	case "array":
		array, ok := value.([]any)
		valid = ok
		if ok && schema.Items != nil {
			for i, child := range array {
				if err := validateTypedValue(*schema.Items, child, fmt.Sprintf("%s.%d", path, i)); err != nil {
					return err
				}
			}
		}
	case "object":
		object, ok := value.(map[string]any)
		valid = ok
		if ok && len(schema.Properties) > 0 {
			for key, child := range object {
				declared, exists := schema.Properties[key]
				if !exists {
					return fmt.Errorf("%s存在未声明字段%s", path, key)
				}
				if err := validateTypedValue(declared, child, path+"."+key); err != nil {
					return err
				}
			}
			for _, key := range schema.Required {
				if _, exists := object[key]; !exists {
					return fmt.Errorf("%s缺少字段%s", path, key)
				}
			}
		}
	}
	if !valid {
		return fmt.Errorf("%s需要%s类型", path, schema.Type)
	}
	return nil
}

func ValidateCodeSpec(spec *CodeSpec) error {
	if spec == nil {
		return fmt.Errorf("代码节点需要code配置")
	}
	if spec.Language != "python" && spec.Language != "javascript" {
		return fmt.Errorf("代码语言仅支持python或javascript")
	}
	if strings.TrimSpace(spec.Source) == "" || !validText(spec.Source, MaxCodeSourceBytes) {
		return fmt.Errorf("代码必须非空且不超过%d字节", MaxCodeSourceBytes)
	}
	if spec.TimeoutSeconds < 0 || spec.TimeoutSeconds > MaxCodeTimeoutSeconds {
		return fmt.Errorf("代码超时必须为1–60秒，省略时默认10秒")
	}
	if len(spec.Inputs) > MaxCodeFields || len(spec.Outputs) > MaxCodeFields || len(spec.Outputs) == 0 {
		return fmt.Errorf("代码输入最多64项，输出需要1–64项")
	}
	constantBytes := 0
	for key, binding := range spec.Inputs {
		if !ValidID(key) || !codeType(binding.Type) {
			return fmt.Errorf("代码输入名称或类型无效: %s", key)
		}
		if (binding.Source != "") == (len(binding.Value) > 0) {
			return fmt.Errorf("代码输入%s需要且只能填写source或value", key)
		}
		if binding.Source != "" {
			if _, err := parseDataReference(binding.Source); err != nil {
				if _, localErr := parseRepeatReference(binding.Source); localErr != nil {
					return err
				}
			}
		} else {
			constantBytes += len(binding.Value)
			if constantBytes > MaxCodeDataBytes {
				return fmt.Errorf("代码输入常量合计超过256KiB")
			}
			value, err := decodeCodeJSON(binding.Value)
			if err != nil {
				return err
			}
			if err := validateTypedValue(ValueSchema{Type: binding.Type}, value, "inputs."+key); err != nil {
				return err
			}
		}
	}
	count := 0
	for name, schema := range spec.Outputs {
		if !ValidID(name) {
			return fmt.Errorf("输出字段名无效")
		}
		if err := validateValueSchema(schema, 0, &count); err != nil {
			return err
		}
	}
	return nil
}

func ValidateCodeInputs(spec *CodeSpec, raw json.RawMessage) error {
	if err := ValidateCodeSpec(spec); err != nil {
		return err
	}
	value, err := decodeCodeJSON(raw)
	if err != nil {
		return err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("代码输入必须为JSON对象")
	}
	if len(object) != len(spec.Inputs) {
		return fmt.Errorf("代码输入必须与声明字段完全一致")
	}
	for name, binding := range spec.Inputs {
		value, exists := object[name]
		if !exists {
			return fmt.Errorf("缺少代码输入%s", name)
		}
		if err := validateTypedValue(ValueSchema{Type: binding.Type}, value, "inputs."+name); err != nil {
			return err
		}
	}
	return nil
}

func ValidateCodeOutput(spec *CodeSpec, raw json.RawMessage) error {
	if err := ValidateCodeSpec(spec); err != nil {
		return err
	}
	value, err := decodeCodeJSON(raw)
	if err != nil {
		return err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("代码返回值必须为JSON对象")
	}
	if len(object) != len(spec.Outputs) {
		return fmt.Errorf("代码输出必须与声明字段完全一致")
	}
	for name, schema := range spec.Outputs {
		value, exists := object[name]
		if !exists {
			return fmt.Errorf("缺少代码输出%s", name)
		}
		if err := validateTypedValue(schema, value, "output."+name); err != nil {
			return err
		}
	}
	return nil
}

type dataReference struct {
	input, step, field string
	path               []string
}

func parseDataReference(source string) (dataReference, error) {
	bad := func() (dataReference, error) { return dataReference{}, fmt.Errorf("无效数据引用: %s", source) }
	if len(source) > 1024 {
		return bad()
	}
	parts := strings.Split(source, ".")
	r := dataReference{}
	if len(parts) >= 2 && parts[0] == "inputs" && ValidID(parts[1]) {
		r.input = parts[1]
		r.path = parts[2:]
	} else if len(parts) >= 3 && parts[0] == "steps" && ValidID(parts[1]) {
		r.step, r.field = parts[1], parts[2]
		r.path = parts[3:]
		if r.field != "output" && r.field != "summary" && r.field != "artifact" && r.field != "feedback" {
			return bad()
		}
		if r.field != "output" && len(r.path) > 0 {
			return bad()
		}
	} else {
		return bad()
	}
	if len(r.path) > MaxCodeSchemaDepth+1 {
		return bad()
	}
	for _, part := range r.path {
		if !ValidID(part) {
			n, err := strconv.Atoi(part)
			if err != nil || n < 0 || strconv.Itoa(n) != part {
				return bad()
			}
		}
	}
	return r, nil
}

func traverseData(value any, path []string) (any, error) {
	for _, part := range path {
		switch current := value.(type) {
		case map[string]any:
			var exists bool
			value, exists = current[part]
			if !exists {
				return nil, fmt.Errorf("数据字段不存在: %s", part)
			}
		case []any:
			n, err := strconv.Atoi(part)
			if err != nil || n < 0 || n >= len(current) {
				return nil, fmt.Errorf("数组下标不存在: %s", part)
			}
			value = current[n]
		default:
			return nil, fmt.Errorf("数据不能继续读取字段: %s", part)
		}
	}
	return value, nil
}

func TypedInputs(definition Definition, values map[string]string) (map[string]json.RawMessage, error) {
	normalized, err := NormalizeInputs(definition, values)
	if err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	for _, input := range definition.Inputs {
		value, exists := normalized[input.Key]
		if !exists || value == "" && !input.Required && inputType(input) != "text" && inputType(input) != "select" {
			continue
		}
		var encoded []byte
		switch inputType(input) {
		case "boolean":
			encoded, _ = json.Marshal(value == "yes")
		case "number", "integer", "object", "array":
			encoded = []byte(value)
		default:
			encoded, _ = json.Marshal(value)
		}
		out[input.Key] = append(json.RawMessage(nil), encoded...)
	}
	return out, nil
}

func DecodeInputs(definition Definition, raw json.RawMessage) (map[string]string, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return NormalizeInputs(definition, nil)
	}
	value, err := decodeCodeJSON(raw)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("运行输入必须为JSON对象")
	}
	known := map[string]Input{}
	for _, input := range definition.Inputs {
		known[input.Key] = input
	}
	values := map[string]string{}
	for key, value := range object {
		input, exists := known[key]
		if !exists {
			return nil, fmt.Errorf("无效输入参数%s", key)
		}
		if text, ok := value.(string); ok {
			values[key] = text
			continue
		}
		if inputType(input) == "boolean" {
			if yes, ok := value.(bool); ok {
				if yes {
					values[key] = "yes"
				} else {
					values[key] = "no"
				}
				continue
			}
		}
		switch inputType(input) {
		case "number", "integer", "object", "array":
			b, _ := json.Marshal(value)
			values[key] = string(b)
		default:
			return nil, fmt.Errorf("输入参数%s需要字符串", key)
		}
	}
	return NormalizeInputs(definition, values)
}

func ResolveCodeInputs(run Run, stepID string) (json.RawMessage, error) {
	var spec *CodeSpec
	for _, step := range run.Definition.Steps {
		if step.ID == stepID && step.Kind == "code" {
			spec = step.Code
			break
		}
	}
	if err := ValidateCodeSpec(spec); err != nil {
		return nil, err
	}
	if run.NodeTest != nil && run.NodeTest.StepID == stepID {
		if err := ValidateCodeInputs(spec, run.NodeTest.Inputs); err != nil {
			return nil, err
		}
		return append(json.RawMessage(nil), run.NodeTest.Inputs...), nil
	}
	inputs, err := TypedInputs(run.Definition, run.Inputs)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for name, binding := range spec.Inputs {
		if binding.Source == "" {
			value, err := decodeCodeJSON(binding.Value)
			if err != nil {
				return nil, err
			}
			out[name] = value
			continue
		}
		r, err := parseDataReference(binding.Source)
		if err != nil {
			return nil, err
		}
		var value any
		if r.input != "" {
			raw, exists := inputs[r.input]
			if !exists {
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
		out[name] = value
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if err = ValidateCodeInputs(spec, raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func stepReferenceValue(steps []StepRun, r dataReference) (any, error) {
	for _, step := range steps {
		if step.StepID != r.step {
			continue
		}
		if step.Status != Completed {
			return nil, fmt.Errorf("上游节点尚未成功完成: %s", r.step)
		}
		switch r.field {
		case "summary":
			return step.Summary, nil
		case "artifact":
			return step.Artifact, nil
		case "feedback":
			return step.Feedback, nil
		case "output":
			return decodeCodeJSON(step.Output)
		}
	}
	return nil, fmt.Errorf("上游节点不存在: %s", r.step)
}

func cloneCode(spec *CodeSpec) *CodeSpec {
	if spec == nil {
		return nil
	}
	raw, _ := json.Marshal(spec)
	var out CodeSpec
	_ = json.Unmarshal(raw, &out)
	return &out
}

func renderDataValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	raw, _ := json.Marshal(value)
	return string(raw)
}

// validateDataReference enforces the same dependency boundary for templates,
// conditions and code bindings; callers never infer a connection from a ref.
func validateDataReference(definition Definition, self, source string, inputs, guaranteed map[string]bool, expectedType string) error {
	r, err := parseDataReference(source)
	if err != nil {
		return err
	}
	schema := ValueSchema{}
	if r.input != "" {
		if !inputs[r.input] {
			return fmt.Errorf("引用的输入参数未声明: %s", r.input)
		}
		for _, input := range definition.Inputs {
			if input.Key == r.input {
				kind := inputType(input)
				if kind == "text" || kind == "select" {
					kind = "string"
				}
				schema.Type = kind
				break
			}
		}
	} else {
		if r.step == self || !guaranteed[r.step] {
			return fmt.Errorf("只能引用保证已完成的前序步骤: %s", r.step)
		}
		if r.field != "output" {
			schema.Type = "string"
		} else {
			var found *Step
			for i := range definition.Steps {
				if definition.Steps[i].ID == r.step {
					found = &definition.Steps[i]
					break
				}
			}
			if found != nil && (found.Kind == "join" || found.Kind == "iteration" || found.Kind == "loop") {
				// The host aggregates only successful ancestor code results. A
				// skipped branch is absent and is checked again at resolution.
				schema = ValueSchema{Type: "object"}
			} else if found == nil || found.Kind != "code" || found.Code == nil {
				return fmt.Errorf("结构化output来源需要代码或汇聚节点: %s", r.step)
			} else {
				schema = ValueSchema{Type: "object", Properties: found.Code.Outputs}
			}
		}
	}
	for _, part := range r.path {
		switch schema.Type {
		case "object":
			if len(schema.Properties) == 0 {
				schema = ValueSchema{}
				continue
			}
			child, ok := schema.Properties[part]
			if !ok {
				return fmt.Errorf("未声明的数据字段: %s", part)
			}
			schema = child
		case "array":
			if _, err := strconv.Atoi(part); err != nil {
				return fmt.Errorf("数组引用需要数字下标")
			}
			if schema.Items == nil {
				schema = ValueSchema{}
			} else {
				schema = *schema.Items
			}
		case "":
		default:
			return fmt.Errorf("%s数据不能继续读取字段", schema.Type)
		}
	}
	if expectedType != "" && schema.Type != "" && expectedType != schema.Type && !(expectedType == "number" && schema.Type == "integer") {
		return fmt.Errorf("输入绑定类型%s与来源类型%s不匹配", expectedType, schema.Type)
	}
	return nil
}
