package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestBrowserPagesRejectInvalidPlansBeforeDispatch(t *testing.T) {
	urls := make([]string, 9)
	for i := range urls {
		urls[i] = fmt.Sprintf("https://fixture.invalid/%d", i)
	}
	tooMany, _ := json.Marshal(map[string]any{"action": "open_pages", "urls": urls})
	for _, raw := range []string{
		string(tooMany),
		`{"action":"open_pages","urls":["https://fixture.invalid/"],"concurrency":4}`,
		`{"action":"open_pages","urls":["https://fixture.invalid","https://fixture.invalid/"]}`,
		`{"action":"observe_pages","pages":[{"tab_id":"t2","context_ref":"` + claimTestContext + `","control_epoch":0,"lease_token":"must-not-pass"}]}`,
		`{"action":"snapshot","tab_id":"t2","context_ref":"` + claimTestContext + `"}`,
		`{"action":"batch","actions":[{"action":"click","target":"e1"},{"action":"open_pages","urls":["https://fixture.invalid/"]}]}`,
	} {
		link := &stubLink{}
		b := &Browser{Link: link, ImageMode: BrowserImageNone}
		if _, err := b.Execute(context.Background(), json.RawMessage(raw)); err == nil || len(link.ops) != 0 {
			t.Errorf("invalid plan reached browser: %s; err=%v ops=%d", raw, err, len(link.ops))
		}
	}
}

func TestBrowserPageBindingPreservesZeroEpochAndDoesNotClaim(t *testing.T) {
	link := &stubLink{}
	raw := json.RawMessage(`{"action":"snapshot","tab_id":"t2","context_ref":"` + claimTestContext + `","control_epoch":0}`)
	if _, err := (&Browser{Link: link, ImageMode: BrowserImageNone}).Execute(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if len(link.ops) != 1 {
		t.Fatalf("binding dispatched %d times", len(link.ops))
	}
	var sent map[string]any
	if err := json.Unmarshal(link.ops[0], &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 4 || sent["action"] != "snapshot" || sent["tab_id"] != "t2" || sent["context_ref"] != claimTestContext || sent["control_epoch"] != float64(0) {
		t.Fatalf("binding lost its observed identity or added authority: %s", link.ops[0])
	}
}

func TestBrowserPagesPartialResultRemainsACollectionWithIncompletePayload(t *testing.T) {
	epoch := int64(0)
	collection := BrowserPagesResult{Kind: "browser-pages-v1", Operation: BrowserActionOpenPages, Concurrency: 2,
		Coverage: BrowserPagesCoverage{Requested: 2, Observed: 1, Failed: 1},
		Pages: []BrowserPageObservation{
			{Index: 0, TabID: "t2", ContextRef: claimTestContext, ControlEpoch: &epoch, Generation: 1, Version: "1:t2:1:0", URL: "https://fixture.invalid/one", OK: true, Status: "observed", Text: "One observed page"},
			{Index: 1, TabID: "t3", Status: "failed", Error: "Navigation refused"},
		}}
	data, err := json.Marshal(collection)
	if err != nil {
		t.Fatal(err)
	}
	link := &stubLink{replies: []BrowserReply{{OK: true, Backend: "native", URL: "https://unrelated.invalid/active", Data: data}}}
	result, err := (&Browser{Link: link, ImageMode: BrowserImageNone}).Execute(context.Background(), json.RawMessage(`{"action":"open_pages","urls":["https://fixture.invalid/one","https://fixture.invalid/two"]}`))
	if err != nil {
		t.Fatal(err)
	}
	details := result.Details.(map[string]any)
	if details["content_source"] != "pogo_browser_pages" || details["url"] != "" || details["title"] != "" || details["has_readable_content"] != true {
		t.Fatalf("collection was attributed to the active page: %+v", details)
	}
	if result.Payload == nil || !result.Payload.Incomplete || string(result.Payload.Structured) != string(data) {
		t.Fatalf("partial coverage or retained page data was lost: %+v", result.Payload)
	}
	if !strings.Contains(result.Text, "1 observed; 1 failed") || !strings.Contains(result.Text, "One observed page") || !strings.Contains(result.Text, "never replay") || len(link.ops) != 1 {
		t.Fatalf("partial result lost recovery guidance or was retried: %s", result.Text)
	}
}
