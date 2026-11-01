package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestBrowserDevToolsCapabilityIsNativeAndStandalone(t *testing.T) {
	for _, backend := range []string{BrowserBackendNative, "legacy", "", "future-executor"} {
		browser := &Browser{Link: &capabilityBrowserLink{backend: backend}, ImageMode: BrowserImageNone}
		props := browser.InputSchema()["properties"].(map[string]any)
		child := props["actions"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
		for _, tc := range []struct {
			fields map[string]any
			want   bool
		}{{props, backend == BrowserBackendNative}, {child, false}} {
			actions := tc.fields["action"].(map[string]any)["enum"].([]string)
			if slices.Contains(actions, BrowserActionDevTools) != tc.want {
				t.Fatalf("wrong devtools declaration for %q: %v", backend, actions)
			}
			for _, field := range []string{"method", "params"} {
				if _, exists := tc.fields[field]; exists != tc.want {
					t.Fatalf("unexpected %s availability for %q", field, backend)
				}
			}
		}
	}
	if BrowserReadOnly(BrowserActionDevTools) || BrowserReadOnly("eval") {
		t.Fatal("protocol methods and JavaScript must not be classified as read-only")
	}
}

func TestBrowserDevToolsPreservesProtocolParametersWithoutReinterpretingThem(t *testing.T) {
	// Do not decode numbers through float64 or filter nested protocol arguments.
	params := json.RawMessage(`{"nodeId":9007199254740993,"ratio":1.000e-7,"nested":{"flag":false,"value":null,"items":[1,"x",{}]},"expression":"document.querySelector('main').textContent"}`)
	for _, method := range []string{"help", "targets.list", "DOM.getDocument", "CSS.getComputedStyleForNode", "Runtime.evaluate", "Debugger.resume", "Page.navigate", "Network.getResponseBody", "network.list", "network.read", "console.list", "events.list", "FutureDomain.newMethod"} {
		t.Run(method, func(t *testing.T) {
			link := &capabilityBrowserLink{backend: BrowserBackendNative}
			browser := &Browser{Link: link, ImageMode: BrowserImageNone}
			args := op(t, map[string]any{"action": BrowserActionDevTools, "method": method, "params": params})
			if err := ValidateArgs(browser, args); err != nil {
				t.Fatal(err)
			}
			prepared, _, err := browser.prepare(args)
			if err != nil || !bytes.Equal(prepared.Params, params) {
				t.Fatalf("raw parameters changed before transport: %s / %v", prepared.Params, err)
			}
			if _, err := browser.Execute(context.Background(), args); err != nil || len(link.ops) != 1 {
				t.Fatalf("diagnostic call was not dispatched once: %v / %s", err, link.ops)
			}
			var wire map[string]json.RawMessage
			if err := json.Unmarshal(link.ops[0], &wire); err != nil {
				t.Fatal(err)
			}
			if len(wire) != 3 || !bytes.Equal(wire["params"], params) || string(wire["method"]) != `"`+method+`"` {
				t.Fatalf("method or nested parameters lost: %s", link.ops[0])
			}
		})
	}
	for _, raw := range []string{`{"action":"devtools","method":"help"}`, `{"action":"devtools","method":"help","params":{}}`} {
		link := &capabilityBrowserLink{backend: BrowserBackendNative}
		browser := &Browser{Link: link}
		if err := ValidateArgs(browser, json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
		if _, err := browser.Execute(context.Background(), json.RawMessage(raw)); err != nil || string(link.ops[0]) != raw {
			t.Fatalf("optional/empty params changed: %v / %s", err, link.ops)
		}
	}
}

func TestBrowserDevToolsPreservesOpaqueTargetSelection(t *testing.T) {
	target := "dt_" + strings.Repeat("a", 24) + "_12"
	link := &capabilityBrowserLink{backend: BrowserBackendNative}
	browser := &Browser{Link: link}
	raw := op(t, map[string]any{"action": BrowserActionDevTools, "method": "Runtime.evaluate", "target": target,
		"params": map[string]any{"expression": "globalThis.workerName", "returnByValue": true}})
	if err := ValidateArgs(browser, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := browser.Execute(context.Background(), raw); err != nil || len(link.ops) != 1 {
		t.Fatalf("target call was not dispatched once: %s %v", link.ops, err)
	}
	var sent browserArgs
	if err := json.Unmarshal(link.ops[0], &sent); err != nil || sent.Target != target || sent.Method != "Runtime.evaluate" {
		t.Fatalf("opaque target lost in bridge: %s %v", link.ops[0], err)
	}
	for _, invalid := range []any{"", "t1", "e12", "foreign-target-id", "dt_" + strings.Repeat("a", 24) + "_0", target + "\n", strings.Repeat("a", 161), 1, nil} {
		before := len(link.ops)
		bad := op(t, map[string]any{"action": BrowserActionDevTools, "method": "Runtime.evaluate", "target": invalid})
		if err := ValidateArgs(browser, bad); err == nil {
			t.Errorf("invalid target accepted before dispatch: %v", invalid)
		}
		if _, err := browser.Execute(context.Background(), bad); err == nil || len(link.ops) != before {
			t.Fatalf("invalid target reached runtime: %v / %v", invalid, err)
		}
	}
}

func TestBrowserDevToolsRejectsMalformedAndOversizedArgumentsBeforeDispatch(t *testing.T) {
	for _, raw := range []string{
		`{"action":"devtools"}`, `{"action":"devtools","method":""}`, `{"action":"devtools","method":"  "}`,
		`{"action":"devtools","method":3}`, `{"action":"devtools","method":null}`,
		`{"action":"devtools","method":"help","params":null}`, `{"action":"devtools","method":"help","params":[]}`,
		`{"action":"devtools","method":"help","params":"{}"}`, `{"action":"devtools","method":"help","params":true}`,
		`{"action":"devtools","method":"help","params":5}`, `{"action":"devtools","method":"help","target":"other-tab"}`,
		`{"action":"devtools","method":"help","sessionId":"foreign-session"}`,
		`{"action":"snapshot","method":"help"}`, `{"action":"eval","expression":"1","params":{}}`,
		`{"action":"devtools","method":"help","params":{"x":"` + strings.Repeat("x", MaxBrowserDevToolsParamsBytes-7) + `"}}`,
	} {
		link := &capabilityBrowserLink{backend: BrowserBackendNative}
		browser := &Browser{Link: link}
		if err := ValidateArgs(browser, json.RawMessage(raw)); err == nil {
			t.Errorf("pre-approval validation accepted malformed diagnostics: %.200s", raw)
		}
		if _, err := browser.Execute(context.Background(), json.RawMessage(raw)); err == nil || len(link.ops) != 0 {
			t.Fatalf("malformed diagnostics reached executor: %.200s / %v", raw, err)
		}
	}
	params := json.RawMessage(`{"x":"` + strings.Repeat("x", MaxBrowserDevToolsParamsBytes-8) + `"}`)
	if len(params) != MaxBrowserDevToolsParamsBytes {
		t.Fatal("boundary fixture is wrong")
	}
	browser := &Browser{Link: &capabilityBrowserLink{backend: BrowserBackendNative}}
	if err := ValidateArgs(browser, op(t, map[string]any{"action": BrowserActionDevTools, "method": "help", "params": params})); err != nil {
		t.Fatalf("exactly 64 KiB parameters refused: %v", err)
	}
}

func TestBrowserDevToolsRejectsOtherBackendsAndWholeBatches(t *testing.T) {
	for _, backend := range []string{"legacy", "", "future-executor", BrowserBackendNative} {
		for _, batch := range []bool{false, true} {
			if backend == BrowserBackendNative && !batch {
				continue
			}
			link := &capabilityBrowserLink{backend: backend}
			browser := &Browser{Link: link}
			raw := `{"action":"devtools","method":"help"}`
			if batch {
				raw = `{"action":"batch","actions":[{"action":"click","target":"e1"},` + raw + `]}`
			}
			if err := ValidateArgs(browser, json.RawMessage(raw)); err == nil {
				t.Errorf("unsupported diagnostics accepted for %q batch=%v", backend, batch)
			}
			if _, err := browser.Execute(context.Background(), json.RawMessage(raw)); err == nil || len(link.ops) != 0 {
				t.Fatalf("unsupported diagnostics partly dispatched for %q batch=%v: %v", backend, batch, err)
			}
		}
	}
}

func TestBrowserDevToolsDataSurvivesSuccessFailureAndSpill(t *testing.T) {
	for _, ok := range []bool{true, false} {
		for _, large := range []bool{false, true} {
			payload := "diagnostic-tail"
			if large {
				payload = strings.Repeat("x", MaxBytes+100) + payload
			}
			data := op(t, map[string]any{"requestId": "request-7", "body": payload, "exceptionDetails": map[string]any{"text": "evaluation failed"}})
			link := &capabilityBrowserLink{backend: BrowserBackendNative, stubLink: stubLink{replies: []BrowserReply{{OK: ok, Error: "diagnostic failed", Data: data}}}}
			browser := &Browser{Link: link, SpillDir: t.TempDir()}
			result, err := browser.Execute(context.Background(), json.RawMessage(`{"action":"devtools","method":"network.read","params":{"id":"request-7"}}`))
			if err != nil || result.Details.(map[string]any)["ok"] != ok {
				t.Fatalf("diagnostic result failed: %v / %+v", err, result)
			}
			text := result.Text
			if large {
				path, _ := result.Details.(map[string]any)["full_output_path"].(string)
				full, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("large diagnostic data not recoverable: %v", err)
				}
				text = string(full)
			}
			if !strings.Contains(text, string(data)) {
				t.Fatalf("structured diagnostic data lost: ok=%v large=%v", ok, large)
			}
		}
	}
}

func TestBrowserDevToolsMaxParametersSurviveDelegationPipe(t *testing.T) {
	data := json.RawMessage(`{"requestId":"r1","bytes":9007199254740993}`)
	link := &recordingLink{reply: BrowserReply{OK: true, Data: data}}
	child, cleanup := pipePair(t, &Subagent{Browser: link}, "devtools-child")
	defer cleanup()
	child.backend = BrowserBackendNative
	// The JSON transport escapes '<' as six bytes; even a valid 64 KiB params
	// object can exceed the ordinary tool-output ceiling on the private pipe.
	value := strings.Repeat("<", MaxBrowserDevToolsParamsBytes-8)
	target := "dt_" + strings.Repeat("b", 24) + "_2"
	raw := json.RawMessage(`{"action":"devtools","method":"Runtime.evaluate","target":"` + target + `","params":{"x":"` + value + `"}}`)
	browser := &Browser{Link: child}
	if err := ValidateArgs(browser, raw); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		result, err := browser.Execute(context.Background(), raw)
		if err == nil && !strings.Contains(result.Text, string(data)) {
			err = errors.New("structured result was lost in the child channel")
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("valid diagnostic request stalled on the private browser channel")
	}
	seen := link.seen()
	if len(seen) != 1 {
		t.Fatalf("expected one delegated operation, got %d", len(seen))
	}
	var arrived struct {
		Params struct{ X string } `json:"params"`
		Target string             `json:"target"`
	}
	if err := json.Unmarshal([]byte(seen[0]), &arrived); err != nil || arrived.Params.X != value || arrived.Target != target {
		t.Fatalf("delegated diagnostic parameters changed: %v", err)
	}
}
