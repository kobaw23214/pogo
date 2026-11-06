package tools

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

const claimTestContext = "ctx_012345678901234567890123"

func TestBrowserClaimRequiresOneFreshPageBindingBeforeDispatch(t *testing.T) {
	valid := `{"action":"tab","tab_action":"claim","target":"t2","context_ref":"` + claimTestContext + `","control_epoch":3}`
	for _, raw := range []string{
		strings.Replace(valid, `,"target":"t2"`, "", 1),
		strings.Replace(valid, `,"context_ref":"`+claimTestContext+`"`, "", 1),
		strings.Replace(valid, `,"control_epoch":3`, "", 1),
		strings.Replace(valid, `"t2"`, `"e2"`, 1),
		strings.Replace(valid, claimTestContext, "private-worker-id", 1),
		strings.Replace(valid, `"control_epoch":3`, `"control_epoch":null`, 1),
		strings.Replace(valid, `"control_epoch":3`, `"control_epoch":-1`, 1),
		strings.Replace(valid, `"control_epoch":3`, `"control_epoch":3.5`, 1),
		strings.Replace(valid, `"control_epoch":3`, `"control_epoch":9007199254740992`, 1),
		strings.Replace(valid, `"control_epoch":3`, `"control_epoch":"3"`, 1),
		strings.Replace(valid, `"control_epoch":3`, `"control_epoch":3,"url":""`, 1),
		strings.Replace(valid, `"control_epoch":3`, `"control_epoch":3,"lease_token":"never-request-a-lease"`, 1),
		strings.Replace(valid, `"control_epoch":3`, `"control_epoch":3,"force":true`, 1),
		`{"action":"tab","tab_action":"new","context_ref":""}`,
		`{"action":"tab","target":"t2","control_epoch":0}`,
		`{"action":"snapshot","context_ref":"` + claimTestContext + `","control_epoch":3}`,
		`{"action":"batch","actions":[{"action":"click","target":"e1"},` + valid + `]}`,
	} {
		link := &stubLink{}
		browser := &Browser{Link: link, ImageMode: BrowserImageNone}
		if err := browser.ValidateArgs(json.RawMessage(raw)); err == nil {
			t.Errorf("claim contract accepted %s", raw)
		}
		if _, err := browser.Execute(context.Background(), json.RawMessage(raw)); err == nil || len(link.ops) != 0 {
			t.Errorf("invalid claim partly dispatched: %s; err=%v ops=%d", raw, err, len(link.ops))
		}
	}
	for _, epoch := range []string{"0", "3", "9007199254740991"} {
		link := &stubLink{}
		browser := &Browser{Link: link, ImageMode: BrowserImageNone}
		raw := strings.Replace(valid, `"control_epoch":3`, `"control_epoch":`+epoch, 1)
		if err := browser.ValidateArgs(json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
		if _, err := browser.Execute(context.Background(), json.RawMessage(raw)); err != nil || len(link.ops) != 1 {
			t.Fatalf("valid claim did not dispatch: %v", err)
		}
		var sent map[string]any
		_ = json.Unmarshal(link.ops[0], &sent)
		if len(sent) != 5 || sent["context_ref"] != claimTestContext || sent["target"] != "t2" || sent["tab_action"] != "claim" || !strings.Contains(string(link.ops[0]), `"control_epoch":`+epoch) {
			t.Fatalf("page claim changed the observed binding: %s", link.ops[0])
		}
	}
}

func TestBrowserClaimSchemaIsNativeAndStandalone(t *testing.T) {
	for _, backend := range []string{BrowserBackendNative, "unsupported"} {
		browser := &Browser{Link: &capabilityBrowserLink{backend: backend}, ImageMode: BrowserImageNone}
		properties := browser.InputSchema()["properties"].(map[string]any)
		child := properties["actions"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
		for _, entry := range []struct {
			fields map[string]any
			claim  bool
		}{{properties, backend == BrowserBackendNative}, {child, false}} {
			if got := slices.Contains(entry.fields["tab_action"].(map[string]any)["enum"].([]string), "claim"); got != entry.claim {
				t.Fatalf("claim exposure = %t want %t backend=%s", got, entry.claim, backend)
			}
			for _, field := range []string{"context_ref", "control_epoch"} {
				if _, got := entry.fields[field]; got != entry.claim {
					t.Fatalf("claim fence %s exposure = %t want %t", field, got, entry.claim)
				}
			}
		}
	}
}

func TestBrowserClaimDetailsCannotChangeControlFromUnrelatedReplies(t *testing.T) {
	epoch := int64(4)
	valid := BrowserReply{OK: true, Backend: "native", ActiveTab: "t2", Version: "1:t2:3:0:r:4", ContextRef: claimTestContext, Control: "agent", ControlEpoch: &epoch, Text: "Current page"}
	for _, tc := range []struct {
		name   string
		op     string
		change func(*BrowserReply)
		want   bool
	}{
		{name: "exact explicit claim", want: true},
		{name: "ordinary snapshot", op: `{"action":"snapshot"}`},
		{name: "failed claim", change: func(r *BrowserReply) { r.OK = false }},
		{name: "different page", change: func(r *BrowserReply) { r.ActiveTab = "t3" }},
		{name: "different context", change: func(r *BrowserReply) { r.ContextRef = "ctx_999999999999999999999999" }},
		{name: "unverified context", change: func(r *BrowserReply) { r.ContextRef = "" }},
		{name: "human took control again", change: func(r *BrowserReply) { r.Control = "human" }},
		{name: "epoch missing", change: func(r *BrowserReply) { r.ControlEpoch = nil }},
		{name: "epoch not advanced", change: func(r *BrowserReply) { n := int64(3); r.ControlEpoch = &n }},
		{name: "later unrelated control", change: func(r *BrowserReply) { n := int64(6); r.ControlEpoch = &n }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply := valid
			if tc.change != nil {
				tc.change(&reply)
			}
			raw := tc.op
			if raw == "" {
				raw = `{"action":"tab","tab_action":"claim","target":"t2","context_ref":"` + claimTestContext + `","control_epoch":3}`
			}
			result, err := (&Browser{Link: &stubLink{replies: []BrowserReply{reply}}}).Execute(context.Background(), json.RawMessage(raw))
			if (err != nil) != !reply.OK {
				t.Fatalf("claim execution status was not preserved: %v", err)
			}
			details := result.Details.(map[string]any)
			for _, field := range []string{"tab_action", "control", "control_epoch"} {
				if _, present := details[field]; present != tc.want {
					t.Fatalf("%s changed UI control: %+v", field, details)
				}
			}
			if tc.want && (details["tab_action"] != "claim" || details["control"] != "agent" || details["control_epoch"] != int64(4)) {
				t.Fatalf("verified claim missing final control: %+v", details)
			}
		})
	}
}
