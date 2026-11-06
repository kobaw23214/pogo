package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func devToolsViewObject(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	object, ok := browserJSONObject(raw)
	if !ok {
		t.Fatalf("expected JSON object: %s", raw)
	}
	return object
}

func devToolsViewData(t *testing.T, result Result) map[string]json.RawMessage {
	t.Helper()
	_, data, found := strings.Cut(result.Text, "\ndata: ")
	if !found {
		t.Fatalf("result has no data: %s", result.Text)
	}
	return devToolsViewObject(t, json.RawMessage(data))
}

func TestBrowserDevToolsNetworkViewPreservesEvidenceAndRawDetails(t *testing.T) {
	data := json.RawMessage(`{"items":[{"seq":9007199254740993,"id":"n1","request_id":"n1","cdp_request_id":"7.1","tab_id":"t1","target_ref":"dt_first","target_type":"iframe","frame_id":"f1","loader_id":"l1","navigation_id":"l1","timestamp":1900000000001,"finished_at":1900000000002,"url":"https://arbitrary.test/a","method":"POST","resource_type":"Fetch","headers":{"Content-Type":"application/json","x-custom":"request header value"},"request_headers_complete":false,"response_headers_complete":true,"post_data":"{\"amount\":9007199254740993}","redirect_index":1,"redirect_from":"n0","status":503,"response":{"status":503,"mime_type":"application/json","protocol":"h2","from_service_worker":true,"headers":{"x-error-reason":"busy"},"future":1.000e-7},"body":{"state":"truncated","bytes":9007199254740993,"stored_bytes":65536,"limit":65536,"reason":"limit"},"future":{"important":9007199254740993}},{"seq":12,"request_id":"n2","target_ref":"dt_second","cdp_request_id":"7.1","headers":{"x-other":"another"},"body":{"state":"unavailable","reason":"loading_failed","error":"disconnected"}}],"cursor":12,"next_cursor":13,"oldest_cursor":8,"evicted_before":7,"cursor_lost":true,"limit":2,"paused":true,"unknown":{"value":1.000e-7}}`)
	before := append(json.RawMessage(nil), data...)
	fullRead := json.RawMessage(`{"request":{"request_id":"n1","headers":{"Content-Type":"application/json","x-custom":"request header value"},"response":{"status":503,"headers":{"x-error-reason":"busy"}},"body":{"state":"truncated","text":"{\"error\":\"busy\"}"}}}`)
	link := &capabilityBrowserLink{backend: BrowserBackendNative, stubLink: stubLink{replies: []BrowserReply{{OK: true, Data: data}, {OK: true, Data: fullRead}}}}
	browser := &Browser{Link: link, SpillDir: t.TempDir()}
	result, err := browser.Execute(context.Background(), json.RawMessage(`{"action":"devtools","method":"network.list"}`))
	if err != nil {
		t.Fatal(err)
	}
	view := devToolsViewData(t, result)
	var items, original []json.RawMessage
	if json.Unmarshal(view["items"], &items) != nil || len(items) != 2 {
		t.Fatal("items changed")
	}
	json.Unmarshal(devToolsViewObject(t, data)["items"], &original)
	for i := range items {
		got := devToolsViewObject(t, items[i])
		want := devToolsViewObject(t, original[i])
		if _, exists := got["headers"]; exists {
			t.Fatal("ordinary request headers were not projected")
		}
		delete(want, "headers")
		if response, exists := want["response"]; exists {
			wantResponse := devToolsViewObject(t, response)
			gotResponse := devToolsViewObject(t, got["response"])
			if _, exists := gotResponse["headers"]; exists {
				t.Fatal("ordinary response headers were not projected")
			}
			delete(wantResponse, "headers")
			want["response"], _ = json.Marshal(wantResponse)
			got["response"], _ = json.Marshal(gotResponse)
		}
		wantJSON, _ := json.Marshal(want)
		gotJSON, _ := json.Marshal(got)
		if !bytes.Equal(gotJSON, wantJSON) {
			t.Fatalf("evidence field changed:\ngot %s\nwant %s", gotJSON, wantJSON)
		}
	}
	for key, raw := range devToolsViewObject(t, data) {
		if key != "items" && !bytes.Equal(view[key], raw) {
			t.Fatalf("list metadata %s changed: %s", key, view[key])
		}
	}
	if !strings.Contains(string(view["model_view"]), "network.read") || !strings.Contains(string(view["model_view"]), "no request replay") {
		t.Fatal("projected list does not describe detail retrieval")
	}
	details := result.Details.(map[string]any)
	if !bytes.Equal(details["devtools_data"].(json.RawMessage), before) || !bytes.Equal(data, before) {
		t.Fatal("model view changed the original diagnostic payload")
	}
	// The retained UI/persistence copy must not alias the transport buffer.
	details["devtools_data"].(json.RawMessage)[0] = '['
	if !bytes.Equal(data, before) {
		t.Fatal("raw details alias transport data")
	}
	read, err := browser.Execute(context.Background(), json.RawMessage(`{"action":"devtools","method":"network.read","params":{"request_id":"n1"}}`))
	if err != nil || !strings.Contains(read.Text, string(fullRead)) || len(link.ops) != 2 {
		t.Fatalf("full request detail no longer available without hidden calls: %v %s ops=%d", err, read.Text, len(link.ops))
	}
}

func TestBrowserDevToolsNetworkViewKeepsHeaderDiagnosticsAndUnknownShapes(t *testing.T) {
	for _, headers := range []string{
		`{"state":"truncated","text":"{\"x-long\":\"partial"}`,
		`{"state":"unavailable","reason":"capture_failed"}`,
		`{"x-future":{"opaque":9007199254740993}}`,
		`{"x-null":null}`, `null`, `[]`, `{}`,
	} {
		raw := json.RawMessage(`{"items":[{"request_id":"r1","headers":` + headers + `,"response":{"headers":` + headers + `}}]}`)
		if got := browserDevToolsModelData("network.list", raw); !bytes.Equal(got, raw) {
			t.Fatalf("header diagnostic or unknown shape changed: %s -> %s", raw, got)
		}
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"items":[{"headers":{"x":"retained without request_id"}}]}`),
		json.RawMessage(`{"items":[{"request_id":"r1","headers":{"x":"a","x":"b"}}]}`),
		json.RawMessage(`{"items":[{"request_id":"r1","request_id":"r2","headers":{"x":"a"}}]}`),
		json.RawMessage(`{"items":null}`), json.RawMessage(`{"items":{}}`),
		json.RawMessage(`{"items":[null]}`), json.RawMessage(`{"items":[]`),
		json.RawMessage(`{"items":[],"items":[]}`),
		json.RawMessage(`{"items":[],"model_view":{"future":true}}`),
	} {
		if got := browserDevToolsModelData("network.list", raw); !bytes.Equal(got, raw) {
			t.Fatalf("unknown/malformed payload did not fall back unchanged: %s -> %s", raw, got)
		}
	}
}

func devToolsCoverageData(t *testing.T) json.RawMessage {
	t.Helper()
	return op(t, map[string]any{
		"provenance": map[string]any{"generation": 7, "tab_id": "t1", "target_ref": "dt_ref", "target_type": "worker", "scope": "tab_family", "url": "https://arbitrary.test/worker.js", "captured_at": 1900000000001, "coverage": browserCoverageText,
			"target_coverage": map[string]any{"state": "partial", "supported_types": []string{"page", "iframe", "worker"}, "excluded_types": []string{"service_worker", "shared_worker", "other_tabs"}, "attached": 2, "initializing": 1, "rejected": map[string]any{"capacity": 3}, "prior_activity": "not_reconstructed", "future": "preserved"}},
		"paused": true, "errors": []any{map[string]any{"state": "unavailable", "error": "attachment refused"}},
		"result":           json.RawMessage(`{"provenance":{"coverage":"page-owned-value"},"items":[{"headers":{"application":"data"}}],"value":9007199254740993}`),
		"exceptionDetails": json.RawMessage(`{"text":"evaluation failed","exception":{"objectId":"opaque.1.1"}}`),
	})
}

func TestBrowserDevToolsCoverageViewOnlyRemovesProvenRedundancy(t *testing.T) {
	raw := devToolsCoverageData(t)
	view := devToolsViewObject(t, browserDevToolsModelData("Runtime.evaluate", raw))
	before := devToolsViewObject(t, raw)
	provenance := devToolsViewObject(t, view["provenance"])
	if _, exists := provenance["coverage"]; exists {
		t.Fatal("known coverage prose was not projected")
	}
	for key, value := range before {
		if key != "provenance" && !bytes.Equal(view[key], value) {
			t.Fatalf("opaque CDP or error field %s changed", key)
		}
	}
	for key, value := range devToolsViewObject(t, before["provenance"]) {
		if key != "coverage" && !bytes.Equal(provenance[key], value) {
			t.Fatalf("provenance field %s changed", key)
		}
	}
	for _, changed := range []string{
		strings.Replace(string(raw), browserCoverageText, "new coverage contract", 1),
		strings.Replace(string(raw), `"initializing":1`, `"initializing":null`, 1),
		strings.Replace(string(raw), `"attached":2,`, "", 1),
		strings.Replace(string(raw), `"scope":"tab_family"`, `"scope":"other_scope"`, 1),
		strings.Replace(string(raw), `"supported_types":["page","iframe","worker"]`, `"supported_types":["page","iframe","new_target"]`, 1),
	} {
		if changed == string(raw) {
			t.Fatal("test did not change input")
		}
		if got := browserDevToolsModelData("Runtime.evaluate", json.RawMessage(changed)); string(got) != changed {
			t.Fatalf("unproven coverage semantics changed: %s", got)
		}
	}
}

func TestBrowserDevToolsViewDoesNotProjectFailureOrOtherAction(t *testing.T) {
	raw := devToolsCoverageData(t)
	for _, tc := range []struct {
		backend, action string
		ok              bool
	}{
		{BrowserBackendNative, BrowserActionDevTools, false},
		{BrowserBackendNative, BrowserActionSnapshot, true},
	} {
		link := &capabilityBrowserLink{backend: tc.backend, stubLink: stubLink{replies: []BrowserReply{{OK: tc.ok, Error: "unavailable", Data: raw}}}}
		browser := &Browser{Link: link, SpillDir: t.TempDir()}
		args := json.RawMessage(`{"action":"snapshot"}`)
		if tc.action == BrowserActionDevTools {
			args = json.RawMessage(`{"action":"devtools","method":"Runtime.evaluate"}`)
		}
		result, err := browser.Execute(context.Background(), args)
		if (err != nil) != !tc.ok || !strings.Contains(result.Text, string(raw)) || len(link.ops) != 1 {
			t.Fatalf("failure or non-devtools output changed: %+v %v %s", tc, err, result.Text)
		}
		_, retained := result.Details.(map[string]any)["devtools_data"]
		if retained != (tc.action == BrowserActionDevTools) {
			t.Fatalf("incorrect raw diagnostic retention scope: %+v", tc)
		}
	}
}
