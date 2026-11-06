package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestBrowserFailuresRetainObservationsAndMarkExecutionError(t *testing.T) {
	for _, tc := range []struct {
		name      string
		reply     BrowserReply
		wantError bool
	}{
		{"navigation-timeout", BrowserReply{OK: false, Error: "page.goto: Timeout 60000ms exceeded", ActiveTab: "t2", Text: "partial page"}, true},
		{"ordinary-error-word", BrowserReply{OK: true, Text: "This page documents error codes and failed transactions."}, false},
		{"pending-observation", BrowserReply{OK: true, ObservationPending: true, Text: "The action dispatched; observation is pending."}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			link := &capabilityBrowserLink{backend: BrowserBackendNative, stubLink: stubLink{replies: []BrowserReply{tc.reply}}}
			result, err := (&Browser{Link: link}).Execute(context.Background(), json.RawMessage(`{"action":"snapshot"}`))
			if (err != nil) != tc.wantError || result.Details.(map[string]any)["ok"] != tc.reply.OK || len(link.ops) != 1 {
				t.Fatalf("wrong status or execution count: %+v %v", result, err)
			}
			if tc.reply.OK && !strings.Contains(result.Text, tc.reply.Text) {
				t.Fatal("successful observed text lost")
			}
			if !tc.reply.OK && (!strings.Contains(result.Text, "Timeout 60000ms") || !strings.Contains(result.Text, "partial page") || !strings.Contains(result.Text, "tab: t2") || strings.Contains(err.Error(), "60000")) {
				t.Fatal("executor diagnostic was not retained separately from safe error status")
			}
		})
	}
}

func TestBrowserFailureRecoveryExplainsSafeNextObservation(t *testing.T) {
	for _, tc := range []struct{ action, issue, want string }{
		{"click", "TAB_CONTROL_CHANGED: bound page control changed", "Do not drop the binding"},
		{"navigate", "BROWSER_TAB_CLOSED: bound tab closed", "stop using the closed page's IDs"},
		{"navigate", "page.goto: net::ERR_CONNECTION_TIMED_OUT", "do not repair the route"},
		{"wait", "page.waitForURL: Timeout 30000ms exceeded", "wait condition was not observed"},
		{"click", "Stale ref e5: page version changed", "fresh ref/version"},
		{"batch", "stale screenshot version", "Do not reuse stale coordinates"},
		{"click", "div intercepts pointer events", "Do not automatically force-click"},
	} {
		t.Run(tc.issue, func(t *testing.T) {
			text := formatBrowserFailure(tc.action, BrowserReply{OK: false, Error: tc.issue})
			if !strings.Contains(text, tc.issue) || !strings.Contains(text, tc.want) {
				t.Fatalf("missing bounded recovery: %s", text)
			}
		})
	}
	link := &capabilityBrowserLink{backend: BrowserBackendNative}
	cause := errors.New("TAB_CONTROL_CHANGED")
	link.err = cause
	_, err := (&Browser{Link: link}).Execute(context.Background(), json.RawMessage(`{"action":"snapshot"}`))
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "same fresh row") {
		t.Fatalf("lost transport cause or safe correction: %v", err)
	}
}

func TestBrowserInvalidLocatorCorrectionNeverDispatches(t *testing.T) {
	link := &capabilityBrowserLink{backend: BrowserBackendNative}
	browser := &Browser{Link: link}
	for _, raw := range []string{`{"action":"click","name":"保存"}`, `{"action":"click","role":"button","target":"e1"}`} {
		err := browser.ValidateArgs(json.RawMessage(raw))
		if err == nil || !strings.Contains(err.Error(), "use find") || !strings.Contains(err.Error(), "returned target ref") {
			t.Fatalf("unactionable locator refusal: %v", err)
		}
	}
	if len(link.ops) != 0 {
		t.Fatal("invalid locator reached executor")
	}
}
