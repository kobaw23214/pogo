package agent

import (
	"encoding/json"
	"strings"
)

// EvidenceQuoteRepairStatus records a bounded rewrite of explicitly declared
// verbatim quotations. It is host metadata, not evidence of semantic support.
// Failed or unverifiable repairs retain the original answer.
type EvidenceQuoteRepairStatus struct {
	Attempted bool   `json:"attempted"`
	Accepted  bool   `json:"accepted"`
	Reason    string `json:"reason,omitempty"`
}

// An optional coverage checkpoint must not replace a complete structured
// answer with a diagnostic acknowledgement. Values can be revised after new
// evidence, but the declared answer schema and raw/fenced representation stay.
// This guard says nothing about factual or semantic correctness.
func evidenceCheckpointFormatMatches(original, replacement string) bool {
	before, after := parseCitationJSONObject(original), parseCitationJSONObject(replacement)
	if before == nil {
		return true
	}
	if after == nil || before.fence != after.fence || len(before.fields) != len(after.fields) || len(before.sources) > 0 && len(after.sources) == 0 {
		return false
	}
	for key, raw := range before.fields {
		other, ok := after.fields[key]
		if !ok {
			return false
		}
		if key == "sources" {
			continue // A necessary new saved read can add its actual citation.
		}
		decode := func(value json.RawMessage) (any, bool) {
			d := json.NewDecoder(strings.NewReader(string(value)))
			if !evidenceQuoteUniqueValue(d, 0) {
				return nil, false
			}
			d = json.NewDecoder(strings.NewReader(string(value)))
			d.UseNumber()
			var decoded any
			if d.Decode(&decoded) != nil {
				return nil, false
			}
			return decoded, true
		}
		left, lok := decode(raw)
		right, rok := decode(other)
		if !lok || !rok || !sameEvidenceJSONTypes(left, right) {
			return false
		}
	}
	return true
}

func sameEvidenceJSONTypes(left, right any) bool {
	switch x := left.(type) {
	case nil:
		return right == nil
	case string:
		_, ok := right.(string)
		return ok
	case bool:
		_, ok := right.(bool)
		return ok
	case json.Number:
		_, ok := right.(json.Number)
		return ok
	case []any:
		y, ok := right.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !sameEvidenceJSONTypes(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := right.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for key, value := range x {
			other, ok := y[key]
			if !ok || !sameEvidenceJSONTypes(value, other) {
				return false
			}
		}
		return true
	}
	return false
}
