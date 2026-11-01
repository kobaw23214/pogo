package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The calls models got wrong in the GLM-5.3 runs: a batch sent without
// action, and a wait given only seconds. Each must be refused before approval
// with one correction the model can apply, and must never reach the executor.
func TestBrowserValidateArgsGivesCorrectableFeedback(t *testing.T) {
	for _, backend := range []string{BrowserBackendNative, ""} {
		link := &capabilityBrowserLink{backend: backend}
		b := &Browser{Link: link, ImageMode: BrowserImageNone}
		for _, tc := range []struct{ name, args, want string }{
			{"batch without action", `{"actions":[{"action":"type","target":"e1","text":"Nova"},{"action":"click","target":"e5"}]}`, `{"action":"batch","actions":[...]}`},
			{"no action at all", `{"target":"e1"}`, "set it to one of snapshot"},
			{"batch step without action", `{"action":"batch","actions":[{"action":"click","target":"e1"},{"target":"e2"}]}`, `step 2 has no "action"`},
			{"wait with only seconds", `{"action":"wait","seconds":3}`, "seconds only limits the wait and is not a sleep"},
			{"wait in a batch", `{"action":"batch","actions":[{"action":"click","target":"e1"},{"action":"wait","seconds":3}]}`, "step 2: browser wait: needs a condition"},
			{"wrong action name keeps the enum message", `{"action":"fill","target":"e1","text":"x"}`, "must be one of"},
		} {
			t.Run(backend+"/"+tc.name, func(t *testing.T) {
				err := ValidateArgs(b, json.RawMessage(tc.args))
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("ValidateArgs(%s) = %v, want it to contain %q", tc.args, err, tc.want)
				}
			})
		}
		for _, valid := range []string{
			`{"action":"batch","actions":[{"action":"type","target":"e1","text":""},{"action":"click","target":"e5"}]}`,
			`{"action":"wait","gone_text":"No ticket submitted.","seconds":10}`,
			`{"action":"snapshot"}`,
		} {
			if err := ValidateArgs(b, json.RawMessage(valid)); err != nil {
				t.Errorf("%q backend refused a valid call %s: %v", backend, valid, err)
			}
		}
		if len(link.ops) != 0 {
			t.Fatalf("validation reached the executor %d time(s)", len(link.ops))
		}
	}
}

func TestBrowserReplyReportsOpenedTabsAndUnfinishedWork(t *testing.T) {
	// Decoded from the worker's JSON, as both the Web bridge and the evaluation
	// adapter do, so the field names are part of the contract under test.
	var reply BrowserReply
	worker := `{"ok":true,"backend":"native","active_tab":"t2","version":"1:t2:1:1:x","url":"http://site.test/quote","title":"Quote",
		"text":"Quote Q-7","opened_tabs":[{"id":"t2","url":"http://site.test/quote"}],"from_tab":"t1","pending_requests":2,"settle_ms":2000}`
	if err := json.Unmarshal([]byte(worker), &reply); err != nil {
		t.Fatal(err)
	}
	link := &capabilityBrowserLink{backend: BrowserBackendNative, stubLink: stubLink{replies: []BrowserReply{reply}}}
	res, err := (&Browser{Link: link, ImageMode: BrowserImageNone}).Execute(context.Background(), json.RawMessage(`{"action":"click","target":"e1"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"opened tab t2 during the action; this reply observes active tab t2",
		"action began in tab t1 (its current state is not verified)",
		"network pending: 2 request(s) observed on related pages during the action remained pending after 2000 ms",
		"do not repeat the action",
		"Quote Q-7",
	} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("reply text lacks %q:\n%s", want, res.Text)
		}
	}
	quiet, err := (&Browser{Link: &capabilityBrowserLink{backend: BrowserBackendNative}}).Execute(context.Background(), json.RawMessage(`{"action":"click","target":"e1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(quiet.Text, "opened tab") || strings.Contains(quiet.Text, "network pending") {
		t.Errorf("a settled reply in place must not carry notes:\n%s", quiet.Text)
	}
}

func TestBrowserOpenedTabReceiptDoesNotInferSourceState(t *testing.T) {
	// FromTab is historical metadata: the source could have closed or changed.
	// Neither state can be inferred, nor requires an unconditional extra read.
	reply := BrowserReply{
		OK: true, Backend: BrowserBackendNative, ActiveTab: "t2", FromTab: "t1",
		OpenedTabs: []BrowserTabRef{{ID: "t2", URL: "https://site.test/receipt"}},
		URL:        "https://site.test/receipt", Text: "Receipt R-42",
	}
	link := &capabilityBrowserLink{backend: BrowserBackendNative, stubLink: stubLink{replies: []BrowserReply{reply}}}
	result, err := (&Browser{Link: link}).Execute(context.Background(), json.RawMessage(`{"action":"click","target":"e3"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, unsupported := range []string{"unchanged", "return with", "tab target t1", "still exists", "Check tabs", "observe before acting"} {
		if strings.Contains(result.Text, unsupported) {
			t.Errorf("receipt inferred a source state or required an extra read %q: %s", unsupported, result.Text)
		}
	}
	for _, observed := range []string{"action began in tab t1 (its current state is not verified)", "observes active tab t2", "Receipt R-42"} {
		if !strings.Contains(result.Text, observed) {
			t.Errorf("missing observation %q: %s", observed, result.Text)
		}
	}
	if len(link.ops) != 1 {
		t.Fatalf("formatting replayed the action: %d calls", len(link.ops))
	}
}

func TestBrowserPendingTrafficIsIndependentOfBusinessOutcome(t *testing.T) {
	for _, pageText := range []string{"Order accepted: R-42", "Order is processing", "Order rejected"} {
		reply := BrowserReply{OK: true, Backend: BrowserBackendNative, Text: pageText, PendingRequests: 1, SettleMS: 800}
		link := &capabilityBrowserLink{backend: BrowserBackendNative, stubLink: stubLink{replies: []BrowserReply{reply}}}
		result, err := (&Browser{Link: link}).Execute(context.Background(), json.RawMessage(`{"action":"click","target":"e3"}`))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result.Text, pageText) || !strings.Contains(result.Text, "network pending: 1 request(s) observed on related pages during the action remained pending after 800 ms") {
			t.Fatalf("network or page observation lost: %s", result.Text)
		}
		for _, unsupported := range []string{"started by this action", "still loading", "business completed"} {
			if strings.Contains(result.Text, unsupported) {
				t.Fatalf("network observation inferred %q: %s", unsupported, result.Text)
			}
		}
		if !strings.Contains(result.Text, "condition-based wait") || !strings.Contains(result.Text, "do not repeat the action") || len(link.ops) != 1 {
			t.Fatalf("missing safe observation guidance or action was replayed: %s", result.Text)
		}
	}
	if note := formatBrowserSettle(BrowserReply{OK: true, SettleMS: 800}); note != "" {
		t.Fatalf("quiet network invented a completion claim or extra warning: %s", note)
	}
}
