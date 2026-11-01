package agentworkflow

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxInputOptions    = 64
	MaxResourceIDBytes = 256
	MaxStepResources   = 32
	maxInputValueBytes = 16384
	maxInputTotalBytes = 65536
)

func inputType(input Input) string {
	if input.Type == "" {
		return "text"
	}
	return input.Type
}

func validateInputDefinition(input Input) error {
	kind := inputType(input)
	if kind != "text" && kind != "boolean" && kind != "select" && kind != "number" && kind != "integer" && kind != "object" && kind != "array" {
		return fmt.Errorf("参数 %s 的类型必须为text、boolean、select、number、integer、object或array", input.Key)
	}
	if kind != "boolean" && kind != "select" && len(input.Options) > 0 {
		return fmt.Errorf("参数 %s 的类型不能配置选项", input.Key)
	}
	if kind == "select" && len(input.Options) == 0 {
		return fmt.Errorf("选择参数 %s 必须配置选项", input.Key)
	}
	if len(input.Options) > MaxInputOptions {
		return fmt.Errorf("参数 %s 最多支持%d个选项", input.Key, MaxInputOptions)
	}
	seen := map[string]bool{}
	for _, option := range input.Options {
		if strings.TrimSpace(option.Value) == "" || !validText(option.Value, 256) || !validText(option.Label, 256) || seen[option.Value] {
			return fmt.Errorf("参数 %s 的选项值必须非空、唯一且不超过256字节", input.Key)
		}
		seen[option.Value] = true
	}
	if kind == "boolean" && len(input.Options) > 0 && (len(input.Options) != 2 || !seen["yes"] || !seen["no"]) {
		return fmt.Errorf("布尔参数 %s 的选项值必须为yes和no", input.Key)
	}
	if !validText(input.Default, maxInputValueBytes) {
		return fmt.Errorf("参数 %s 的默认值无效或过长", input.Key)
	}
	if input.Default != "" {
		if err := validateInputValue(input, input.Default); err != nil {
			return fmt.Errorf("参数 %s 的默认值无效: %v", input.Key, err)
		}
	}
	return nil
}

func validateInputValue(input Input, value string) error {
	if len(value) > maxInputValueBytes || !utf8.ValidString(value) {
		return fmt.Errorf("无效输入参数 %s", input.Key)
	}
	if input.Required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("缺少输入参数 %s", input.Key)
	}
	if value == "" {
		// An optional unanswered field is distinct from a selected false value.
		return nil
	}
	switch inputType(input) {
	case "boolean":
		if value != "yes" && value != "no" {
			return fmt.Errorf("输入参数 %s 必须为yes或no", input.Key)
		}
	case "select":
		for _, option := range input.Options {
			if value == option.Value {
				return nil
			}
		}
		return fmt.Errorf("输入参数 %s 不在允许的选项中", input.Key)
	case "number", "integer", "object", "array":
		decoded, err := decodeCodeJSON([]byte(value))
		if err != nil {
			return fmt.Errorf("输入参数 %s 需要合法JSON: %v", input.Key, err)
		}
		return validateTypedValue(ValueSchema{Type: inputType(input)}, decoded, "inputs."+input.Key)
	}
	return nil
}

// NormalizeInputs copies submitted values, fills defaults only for omitted keys,
// and validates the complete runtime input. Values remain strings for historical
// templates, conditions, API clients and Run snapshots; boolean means yes/no.
func NormalizeInputs(definition Definition, values map[string]string) (map[string]string, error) {
	if len(definition.Inputs) > 32 {
		return nil, fmt.Errorf("%w: 最多支持32个输入参数", ErrInvalid)
	}
	known := make(map[string]Input, len(definition.Inputs))
	for _, input := range definition.Inputs {
		if _, exists := known[input.Key]; exists || !ValidID(input.Key) || !validText(input.Label, 256) {
			return nil, fmt.Errorf("%w: 参数名必须唯一且为有效标识符", ErrInvalid)
		}
		if err := validateInputDefinition(input); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		known[input.Key] = input
	}
	normalized := make(map[string]string, len(values)+len(known))
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, exists := known[key]; !exists {
			return nil, fmt.Errorf("%w: 无效输入参数 %s", ErrInvalid, key)
		}
		normalized[key] = values[key]
	}
	total := 0
	for _, input := range definition.Inputs {
		value, supplied := normalized[input.Key]
		if !supplied && input.Default != "" {
			value = input.Default
			normalized[input.Key] = value
		}
		if err := validateInputValue(input, value); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		total += len(value)
	}
	if total > maxInputTotalBytes {
		return nil, fmt.Errorf("%w: 输入参数过大", ErrInvalid)
	}
	return normalized, nil
}

// ValidateInputs shares normalization's definition, choice and size checks. It
// never mutates submitted values; callers starting a Run use NormalizeInputs so
// the effective defaults are retained in that Run's immutable input snapshot.
func ValidateInputs(definition Definition, values map[string]string) error {
	_, err := NormalizeInputs(definition, values)
	return err
}

// Resource IDs are authenticated catalog identities, never filesystem paths or
// a way to grant access. Colon, slash and version separators remain compatible
// with embedded, authored and runtime Skill IDs.
func ValidResourceID(value string) bool {
	if value == "" || len(value) > MaxResourceIDBytes || !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if unicode.IsSpace(char) || unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func ValidateResourceIDs(ids []string) error {
	if len(ids) > MaxStepResources {
		return fmt.Errorf("每个节点每类最多绑定%d个资源", MaxStepResources)
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !ValidResourceID(id) || seen[id] {
			return fmt.Errorf("资源ID必须有效、唯一且不超过%d字节", MaxResourceIDBytes)
		}
		seen[id] = true
	}
	return nil
}

func cloneInputs(inputs []Input) []Input {
	if inputs == nil {
		return nil
	}
	next := make([]Input, len(inputs))
	copy(next, inputs)
	for index := range next {
		next[index].Options = append([]InputOption(nil), inputs[index].Options...)
	}
	return next
}

func cloneStep(step Step) Step {
	step.Code = cloneCode(step.Code)
	step.Iteration = cloneIteration(step.Iteration)
	step.Loop = cloneLoop(step.Loop)
	step.Policy = clonePolicy(step.Policy)
	step.Skills = append([]string(nil), step.Skills...)
	step.Shortcuts = append([]string(nil), step.Shortcuts...)
	if step.Condition != nil {
		condition := *step.Condition
		step.Condition = &condition
	}
	return step
}
