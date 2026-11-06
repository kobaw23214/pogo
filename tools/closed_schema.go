package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

func closedObject(required []string, props map[string]any) map[string]any {
	s := object(required, props)
	s["additionalProperties"] = false
	return s
}
func stringArraySchema(description string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": description}
}
func nullableSchema(s map[string]any) map[string]any {
	s["type"] = []string{s["type"].(string), "null"}
	return s
}

// validateClosedValue rejects unknown fields at every schema level. Agent
// workflow mutations must not accept an invented owner, actor or credential.
func validateClosedValue(schema map[string]any, raw json.RawMessage, path string) error {
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
			if err := validateClosedValue(child, fields[key], path+key+"."); err != nil {
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
			if err := validateClosedValue(item, value, fmt.Sprintf("%s[%d].", strings.TrimSuffix(path, "."), i)); err != nil {
				return err
			}
		}
	}
	return nil
}
