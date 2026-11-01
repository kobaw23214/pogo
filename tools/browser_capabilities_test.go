package tools

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

type capabilityBrowserLink struct {
	stubLink
	backend string
}

func (l *capabilityBrowserLink) BrowserBackend() string { return l.backend }

func TestBrowserBackendAcceptsOnlyNativeMetadata(t *testing.T) {
	for _, tc := range []struct {
		link BrowserLink
		want string
	}{
		{nil, ""}, {&stubLink{}, BrowserBackendNative}, {&capabilityBrowserLink{backend: "future-executor"}, ""},
		{&capabilityBrowserLink{backend: BrowserBackendNative}, BrowserBackendNative},
		{&capabilityBrowserLink{backend: "legacy"}, ""},
	} {
		if got := BrowserBackendOf(tc.link); got != tc.want {
			t.Fatalf("backend %q, want %q", got, tc.want)
		}
	}
}

func TestBrowserSchemaAdvertisesOnlyAvailableBackendActions(t *testing.T) {
	for _, tc := range []struct {
		name, backend                       string
		mode                                BrowserImageMode
		eval, screenshot, tabs, coordinates bool
	}{
		{"native direct", BrowserBackendNative, BrowserImageDirect, false, true, true, true},
		{"native helper", BrowserBackendNative, BrowserImageHelper, false, true, true, true},
		{"native DOM only", BrowserBackendNative, BrowserImageNone, false, false, true, false},
		{"native unknown image path", BrowserBackendNative, BrowserImageUnknown, false, true, true, true},
		{"unsupported transport", "legacy", BrowserImageDirect, false, true, true, true},
		{"unknown transport", "", BrowserImageUnknown, false, true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			browser := &Browser{Link: &capabilityBrowserLink{backend: tc.backend}, ImageMode: tc.mode}
			schema := browser.InputSchema()
			props := schema["properties"].(map[string]any)
			batch := props["actions"].(map[string]any)
			child := batch["items"].(map[string]any)
			if batch["minItems"] != 1 || batch["maxItems"] != MaxBrowserBatchSteps {
				t.Fatalf("unbounded batch: %v", batch)
			}
			for _, entry := range []struct {
				schema map[string]any
				nested bool
			}{{schema, false}, {child, true}} {
				if entry.schema["additionalProperties"] != false {
					t.Fatal("operation schema permits arbitrary fields")
				}
				if required := entry.schema["required"].([]string); len(required) != 1 || required[0] != "action" {
					t.Fatalf("missing required action: %v", required)
				}
				fields := entry.schema["properties"].(map[string]any)
				actions := fields["action"].(map[string]any)["enum"].([]string)
				eval := tc.eval && !(tc.backend == BrowserBackendNative && entry.nested)
				for action, want := range map[string]bool{"eval": eval, BrowserActionScreenshot: tc.screenshot, BrowserActionTabs: tc.tabs, BrowserActionTab: tc.tabs, BrowserActionBatch: !entry.nested} {
					if got := slices.Contains(actions, action); got != want {
						t.Errorf("nested=%t: action %s exposed=%t, want %t", entry.nested, action, got, want)
					}
				}
				for name, want := range map[string]bool{"expression": eval, "x": tc.coordinates, "y": tc.coordinates, "version": tc.coordinates, "actions": !entry.nested} {
					if _, got := fields[name]; got != want {
						t.Errorf("nested=%t: field %s exposed=%t, want %t", entry.nested, name, got, want)
					}
				}
			}
			raw, err := json.Marshal(schema)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), `"oneOf"`) || strings.Contains(string(raw), `"anyOf"`) {
				t.Fatal("schema requires conditional-union support")
			}
			t.Logf("schema bytes: %d; description bytes: %d", len(raw), len(browser.Description()))
		})
	}
}

func TestBrowserRejectsUnsupportedWholeBatchBeforeAnyAction(t *testing.T) {
	for _, tc := range []struct {
		name, backend string
		mode          BrowserImageMode
		last          string
	}{
		{"native eval", BrowserBackendNative, BrowserImageDirect, `{"action":"eval","expression":"document.title"}`},
		{"DOM-only screenshot", BrowserBackendNative, BrowserImageNone, `{"action":"screenshot"}`},
		{"DOM-only coordinates", BrowserBackendNative, BrowserImageNone, `{"action":"click","x":1,"y":2,"version":"v1"}`},
		{"missing child action", BrowserBackendNative, BrowserImageDirect, `{"target":"e1","text":"value"}`},
		{"nested batch", BrowserBackendNative, BrowserImageDirect, `{"action":"batch","actions":[{"action":"snapshot"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			link := &capabilityBrowserLink{backend: tc.backend}
			browser := &Browser{Link: link, ImageMode: tc.mode}
			args := json.RawMessage(`{"action":"batch","actions":[{"action":"click","target":"e1"},` + tc.last + `]}`)
			if _, err := browser.Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "step 2") {
				t.Fatalf("expected second-step preflight failure, got %v", err)
			}
			if len(link.ops) != 0 {
				t.Fatalf("batch partly dispatched before rejection: %s", link.ops)
			}
		})
	}
}

func TestBrowserKnownBackendsRejectWrongOrMissingArguments(t *testing.T) {
	for _, args := range []string{
		`{"action":"type","target":"e1"}`, `{"action":"type","target":"e1","text":null}`,
		`{"action":"type","target":"e1","value":"replacement"}`, `{"action":"fill","target":"e1","text":"replacement"}`,
		`{"action":"select","target":"e1"}`, `{"action":"select","target":"e1","text":3}`,
		`{"action":"press","text":"Enter"}`, `{"action":"snapshot","expression":"document.title"}`,
		`{"action":"click","selector":"button"}`, `{"action":"snapshot","full":null}`,
		`{"action":"click","target":"e1","version":"v1"}`, `{"action":"wait","text":"ready","seconds":61}`,
		`{"action":"scroll","amount":-1}`,
		`{"action":"batch","actions":[null]}`, `{"action":"batch","actions":[{}]}`,
	} {
		link := &capabilityBrowserLink{backend: BrowserBackendNative}
		if _, err := (&Browser{Link: link, ImageMode: BrowserImageDirect}).Execute(context.Background(), json.RawMessage(args)); err == nil {
			t.Errorf("bad arguments accepted: %s", args)
		}
		if len(link.ops) != 0 {
			t.Errorf("bad arguments reached executor: %s", args)
		}
	}
	legacy := &capabilityBrowserLink{backend: "legacy"}
	if _, err := (&Browser{Link: legacy}).Execute(context.Background(), json.RawMessage(`{"action":"eval","text":"document.title"}`)); err == nil || len(legacy.ops) != 0 {
		t.Fatal("legacy guessed expression from an undeclared text field")
	}
}

func TestBrowserExplicitEmptyTextSurvivesSingleAndBatchWire(t *testing.T) {
	for _, backend := range []string{BrowserBackendNative} {
		for _, action := range []string{BrowserActionType, BrowserActionSelect} {
			for _, batch := range []bool{false, true} {
				link := &capabilityBrowserLink{backend: backend}
				raw := `{"action":"` + action + `","target":"e1","text":""}`
				if batch {
					raw = `{"action":"batch","actions":[` + raw + `]}`
				}
				if _, err := (&Browser{Link: link}).Execute(context.Background(), json.RawMessage(raw)); err != nil {
					t.Fatalf("%s empty %s batch=%t: %v", backend, action, batch, err)
				}
				if len(link.ops) != 1 || !strings.Contains(string(link.ops[0]), `"text":""`) {
					t.Fatalf("explicit empty text lost: %s", link.ops)
				}
			}
		}
	}
}

func TestBrowserImageModeDescribesActualInspectionPath(t *testing.T) {
	for _, tc := range []struct {
		mode   BrowserImageMode
		hint   string
		images int
	}{
		{BrowserImageDirect, "direct visual inspection", 1},
		{BrowserImageHelper, "Call analyze_image", 1},
		{BrowserImageNone, "not a visual observation", 0},
		{BrowserImageUnknown, "does not establish", 1},
	} {
		link := &capabilityBrowserLink{backend: BrowserBackendNative, stubLink: stubLink{replies: []BrowserReply{{OK: true, Screenshot: &BrowserScreenshot{Data: "private-transport-bytes", ImageRef: "uploads/frame.jpg", Mime: "image/jpeg", Width: 1280, Height: 800}}}}}
		browser := &Browser{Link: link, ImageMode: tc.mode}
		// Even an unsolicited image on a DOM reply must not imply visual access.
		result, err := browser.Execute(context.Background(), json.RawMessage(`{"action":"snapshot"}`))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result.Text, tc.hint) || len(result.Images) != tc.images {
			t.Fatalf("mode %q misrepresented image access: %+v", tc.mode, result)
		}
		encoded, _ := json.Marshal(result)
		if strings.Contains(string(encoded), "private-transport-bytes") {
			t.Fatal("screenshot transport bytes leaked")
		}
	}

}

func TestBrowserCapabilityValidationNeverRetriesUncertainActions(t *testing.T) {
	link := &capabilityBrowserLink{backend: BrowserBackendNative, stubLink: stubLink{err: errors.New("transport lost after dispatch")}}
	_, err := (&Browser{Link: link}).Execute(context.Background(), json.RawMessage(`{"action":"click","target":"e1"}`))
	if err == nil || len(link.ops) != 1 {
		t.Fatalf("uncertain mutation was retried or hidden: calls=%d err=%v", len(link.ops), err)
	}
}
