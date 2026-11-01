package tools

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestBrowserCalculateIsNativeOnlyAndObservedReferencesOnly(t *testing.T) {
	valid := `{"action":"calculate","operation":"difference","quantity_refs":["q_1:abc","q_2:def"]}`
	for _, backend := range []string{"", "legacy", BrowserBackendNative} {
		link := &capabilityBrowserLink{backend: backend}
		browser := &Browser{Link: link, ImageMode: BrowserImageNone}
		props := browser.InputSchema()["properties"].(map[string]any)
		actions := props["action"].(map[string]any)["enum"].([]string)
		if slices.Contains(actions, BrowserActionCalculate) != (backend == BrowserBackendNative) {
			t.Fatalf("calculate capability mismatch for %q", backend)
		}
		_, hasRefs := props["quantity_refs"]
		if hasRefs != (backend == BrowserBackendNative) {
			t.Fatalf("quantity reference schema mismatch for %q", backend)
		}
		_, err := browser.Execute(context.Background(), json.RawMessage(valid))
		if backend == BrowserBackendNative {
			if err != nil || len(link.ops) != 1 || string(link.ops[0]) != valid {
				t.Fatalf("observed references lost on native wire: %v / %s", err, link.ops)
			}
		} else if err == nil || len(link.ops) != 0 {
			t.Fatal("non-native calculation reached executor")
		}
	}
	if !BrowserReadOnly(BrowserActionCalculate) {
		t.Fatal("source-backed calculation must be read-only")
	}
}

func TestBrowserCalculateRejectsInventedValuesBeforeWholeBatchDispatch(t *testing.T) {
	for _, args := range []string{
		`{"action":"calculate","operation":"sum","values":[0.1,0.2],"quantity_refs":["q1","q2"]}`,
		`{"action":"calculate","operation":"eval","quantity_refs":["q1"]}`,
		`{"action":"calculate","operation":"compare","quantity_refs":["q1"]}`,
		`{"action":"calculate","operation":"difference","quantity_refs":["q1","q2","q3"]}`,
		`{"action":"calculate","operation":"sort","quantity_refs":[]}`,
		`{"action":"calculate","operation":"sum","quantity_refs":["q1","q1"]}`,
		`{"action":"calculate","operation":"sum","quantity_refs":["q1",4]}`,
		`{"action":"calculate","operation":"sort","quantity_refs":null}`,
		`{"action":"calculate","operation":"compare","quantity_refs":["q1","https://source.test/?value=3"]}`,
		`{"action":"calculate","operation":"compare","quantity_refs":["q1","q2"],"unit":"kg"}`,
	} {
		link := &capabilityBrowserLink{backend: BrowserBackendNative}
		browser := &Browser{Link: link, ImageMode: BrowserImageNone}
		batch := `{"action":"batch","actions":[{"action":"click","target":"e1"},` + args + `]}`
		if _, err := browser.Execute(context.Background(), json.RawMessage(batch)); err == nil || len(link.ops) != 0 {
			t.Fatalf("invalid calculation partly executed: %s / %v", args, err)
		}
	}
}

func TestBrowserVisibleEvidenceAndCalculationReachModelWithoutLegacyDataHook(t *testing.T) {
	evidence := json.RawMessage(`{"schema":"dom-evidence-v1","quantities":[{"quantity_ref":"q1","raw":"24天","source_id":"row2-cell3"}],"pagination":{"status":"unknown"}}`)
	link := &capabilityBrowserLink{backend: BrowserBackendNative, stubLink: stubLink{replies: []BrowserReply{{OK: true, Backend: "native", URL: "https://source.test", Text: "Page content", Evidence: evidence}}}}
	result, err := (&Browser{Link: link, ImageMode: BrowserImageNone}).Execute(context.Background(), json.RawMessage(`{"action":"snapshot"}`))
	if err != nil || !strings.Contains(result.Text, `"quantity_ref": "q1"`) || !strings.Contains(result.Text, "Page content") || strings.Contains(result.Text, "\ndata:") {
		t.Fatalf("native evidence lost or confused with pick-data: %v / %s", err, result.Text)
	}
	var reply BrowserReply
	if err := json.Unmarshal([]byte(`{"ok":true,"evidence":{"scope":"as_observed","current_state_verified":false,"result":{"value":"0.3"}}}`), &reply); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(formatBrowserReply(BrowserActionCalculate, reply), `"current_state_verified": false`) {
		t.Fatal("calculation scope lost on worker-to-model boundary")
	}
}

func TestBrowserUnsupportedCalculationRetainsItsEvidenceAndLimit(t *testing.T) {
	link := &capabilityBrowserLink{backend: BrowserBackendNative, stubLink: stubLink{replies: []BrowserReply{{OK: false, Error: "incompatible_units", Evidence: json.RawMessage(`{"status":"unsupported","reason":"incompatible_units","scope":"as_observed","current_state_verified":false,"sources":[{"raw":"4年"},{"raw":"24天"}]}`)}}}}
	result, err := (&Browser{Link: link, ImageMode: BrowserImageNone}).Execute(context.Background(), json.RawMessage(`{"action":"calculate","operation":"compare","quantity_refs":["q1","q2"]}`))
	if err != nil || result.Details.(map[string]any)["ok"] != false || !strings.Contains(result.Text, `"status": "unsupported"`) || !strings.Contains(result.Text, "4年") || !strings.Contains(result.Text, `"current_state_verified": false`) {
		t.Fatalf("unsupported quantities lost their source or scope: %v / %+v", err, result)
	}
}
