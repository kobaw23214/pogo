package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestBrowserContextIsDisplayMetadataOnly(t *testing.T) {
	const contextRef = "ctx_012345678901234567890123"
	link := &stubLink{replies: []BrowserReply{{Backend: BrowserBackendNative, ContextRef: contextRef, ActiveTab: "t2", Version: "1:t2:3:0:revision:0:1280x800", OK: true, URL: "https://fixture.invalid/new", Text: "New page"}}}
	result, err := (&Browser{Link: link}).Execute(context.Background(), json.RawMessage(`{"action":"tab","tab_action":"new","url":"https://fixture.invalid/new"}`))
	if err != nil {
		t.Fatal(err)
	}
	details, ok := result.Details.(map[string]any)
	if !ok || details["context_ref"] != contextRef || details["active_tab"] != "t2" {
		t.Fatalf("display identity was not retained: %#v", result.Details)
	}
	if strings.Contains(result.Text, contextRef) {
		t.Fatal("display identity became model text")
	}
	if len(link.ops) != 1 || strings.Contains(string(link.ops[0]), contextRef) {
		t.Fatal("display metadata changed page execution")
	}
	plain := browserDetails(BrowserActionSnapshot, BrowserReply{OK: true})
	if _, exists := plain["context_ref"]; exists {
		t.Fatal("missing context became invented display identity")
	}
}
