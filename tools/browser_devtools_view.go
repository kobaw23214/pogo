package tools

import (
	"bytes"
	"encoding/json"
	"io"
)

// This is a presentation view of trusted worker metadata, not a rewrite of CDP
// results. Raw protocol results and unknown fields remain opaque RawMessages so
// object IDs, large numbers, exception details and future fields survive intact.
// The worker's bounded, redacted original is retained in tool Details separately.
func browserDevToolsModelData(method string, raw json.RawMessage) json.RawMessage {
	data, ok := browserJSONObject(raw)
	if !ok {
		return raw
	}
	if _, exists := data["model_view"]; exists {
		return raw // An unknown producer already owns this field.
	}
	changes := map[string]any{"version": "devtools-compact-v1"}
	changed := false
	if provenance, ok := browserJSONObject(data["provenance"]); ok && redundantBrowserCoverage(provenance) {
		delete(provenance, "coverage")
		data["provenance"], _ = json.Marshal(provenance)
		changes["coverage"] = "See provenance.target_coverage; logs start at attachment."
		changed = true
	}
	if method == "network.list" {
		var items []json.RawMessage
		if json.Unmarshal(data["items"], &items) != nil || items == nil {
			return raw
		}
		removed := 0
		for i, rawItem := range items {
			item, ok := browserJSONObject(rawItem)
			if !ok {
				return raw
			}
			var requestID string
			if json.Unmarshal(item["request_id"], &requestID) != nil || requestID == "" {
				continue // Do not hide fields that cannot be addressed by network.read.
			}
			itemChanged := false
			if ordinaryBrowserHeaders(item["headers"]) {
				delete(item, "headers")
				removed++
				itemChanged = true
			}
			if response, ok := browserJSONObject(item["response"]); ok && ordinaryBrowserHeaders(response["headers"]) {
				delete(response, "headers")
				item["response"], _ = json.Marshal(response)
				removed++
				itemChanged = true
			}
			if itemChanged {
				items[i], _ = json.Marshal(item)
			}
		}
		if removed > 0 {
			data["items"], _ = json.Marshal(items)
			changes["network_headers"] = "Ordinary header maps omitted; diagnostic header states are unchanged. Use network.read with request_id for retained request/response headers and body; no request replay. Records may be evicted."
			changed = true
		}
	}
	if !changed {
		return raw
	}
	data["model_view"], _ = json.Marshal(changes)
	view, err := json.Marshal(data)
	if err != nil {
		return raw
	}
	return view
}

func browserJSONObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, false
	}
	value := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		key, isString := token.(string)
		if err != nil || !isString {
			return nil, false
		}
		if _, duplicate := value[key]; duplicate {
			return nil, false
		}
		var field json.RawMessage
		if decoder.Decode(&field) != nil {
			return nil, false
		}
		value[key] = field
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return nil, false
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, false
	}
	return value, true
}

func ordinaryBrowserHeaders(raw json.RawMessage) bool {
	headers, ok := browserJSONObject(raw)
	if !ok || len(headers) == 0 {
		return false
	}
	// Worker bounds may return {state:"truncated", text:...}. Preserve that
	// diagnostic, including its text; an omitted map must never imply completeness.
	if _, diagnostic := headers["state"]; diagnostic {
		return false
	}
	for _, value := range headers {
		var text string
		if json.Unmarshal(value, &text) != nil || string(value) == "null" {
			return false
		}
	}
	return true
}

const browserCoverageText = "Current page plus attached OOPIF and dedicated worker targets; shared/service workers and other tabs are excluded. Logs start at attachment."

func redundantBrowserCoverage(provenance map[string]json.RawMessage) bool {
	var text, scope string
	if json.Unmarshal(provenance["coverage"], &text) != nil || text != browserCoverageText ||
		json.Unmarshal(provenance["scope"], &scope) != nil || scope != "tab_family" {
		return false
	}
	coverage, ok := browserJSONObject(provenance["target_coverage"])
	if !ok {
		return false
	}
	var supported, excluded []string
	if json.Unmarshal(coverage["supported_types"], &supported) != nil ||
		json.Unmarshal(coverage["excluded_types"], &excluded) != nil ||
		!exactBrowserStrings(supported, []string{"page", "iframe", "worker"}) ||
		!exactBrowserStrings(excluded, []string{"service_worker", "shared_worker", "other_tabs"}) {
		return false
	}
	var state, prior string
	if json.Unmarshal(coverage["state"], &state) != nil ||
		(state != "attached_targets_only" && state != "partial" && state != "unavailable") ||
		json.Unmarshal(coverage["prior_activity"], &prior) != nil || prior != "not_reconstructed" {
		return false
	}
	for _, field := range []string{"attached", "initializing"} {
		var count uint64
		if json.Unmarshal(coverage[field], &count) != nil || string(coverage[field]) == "null" {
			return false
		}
	}
	_, ok = browserJSONObject(coverage["rejected"])
	return ok
}

func exactBrowserStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
