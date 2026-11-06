package web

import (
	"bytes"
	"encoding/json"
)

// Redact strings structurally so escaped URLs in raw tool JSON are covered.
// UseNumber keeps large identifiers exact; unchanged snapshots retain their
// original bytes and are never re-projected from a generic object.
func redactSharedServiceSnapshot(data []byte, redact func(string) string) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	changed := false
	var visit func(any) any
	visit = func(v any) any {
		switch item := v.(type) {
		case string:
			next := redact(item)
			if next != item {
				changed = true
			}
			return next
		case []any:
			for i, child := range item {
				item[i] = visit(child)
			}
		case map[string]any:
			for key, child := range item {
				next := redact(key)
				if next != key {
					changed = true
					delete(item, key)
				}
				item[next] = visit(child)
			}
		}
		return v
	}
	value = visit(value)
	if !changed {
		return data, nil
	}
	return json.Marshal(value)
}
