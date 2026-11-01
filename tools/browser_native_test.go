package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestBrowserFaviconOnlyEntersUIDetails(t *testing.T) {
	icon := "data:image/svg+xml;base64,PHN2Zz48L3N2Zz4="
	reply := BrowserReply{OK: true, Backend: "native", ActiveTab: "t1", Version: "v7", URL: "https://example.test/", Title: "Observed page", Text: "Evidence text", Favicon: icon}
	link := &stubLink{replies: []BrowserReply{reply}}
	result, err := (&Browser{Link: link}).Execute(context.Background(), json.RawMessage(`{"action":"snapshot"}`))
	if err != nil {
		t.Fatal(err)
	}
	details, ok := result.Details.(map[string]any)
	if !ok || details["favicon"] != icon || details["url"] != "https://example.test/" {
		t.Fatalf("UI citation lost page icon: %+v", result.Details)
	}
	if strings.Contains(result.Text, "data:image/") || strings.Contains(result.Text, "favicon") || len(result.Images) != 0 || result.Text != formatBrowserReply("snapshot", reply) {
		t.Fatal("favicon changed model text or image observations")
	}
	for _, format := range []func(string, BrowserReply) string{formatBrowserReply, formatBrowserFailure} {
		if strings.Contains(format("snapshot", reply), icon) {
			t.Fatal("favicon entered model-facing format")
		}
	}
}

func TestBrowserNativeScreenshotCarriesReferenceNotBytes(t *testing.T) {
	link := &stubLink{replies: []BrowserReply{{OK: true, Backend: "native", ActiveTab: "t1", Version: "v7", URL: "https://example.test/", Screenshot: &BrowserScreenshot{
		Data: "never-leak-bytes", ImageRef: "uploads/browser.jpg", Mime: "image/jpeg", Width: 1280, Height: 800,
	}}}}
	result, err := (&Browser{Link: link}).Execute(context.Background(), json.RawMessage(`{"action":"screenshot"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Images) != 1 || result.Images[0].ImageRef != "uploads/browser.jpg" || !strings.Contains(result.Text, "version: v7") {
		t.Fatalf("lost screenshot metadata: %+v", result)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "never-leak-bytes") {
		t.Fatal("runtime bytes entered tool transcript")
	}
}

func TestBrowserNativeCoordinateValidation(t *testing.T) {
	for _, args := range []string{
		`{"action":"click","x":3,"y":4}`,
		`{"action":"click","x":3,"version":"v1"}`,
		`{"action":"click","x":-1,"y":4,"version":"v1"}`,
		`{"action":"click","x":3,"y":4,"version":"v1","target":"e1"}`,
	} {
		link := &stubLink{}
		if _, err := (&Browser{Link: link}).Execute(context.Background(), json.RawMessage(args)); err == nil || len(link.ops) != 0 {
			t.Fatalf("invalid coordinate action executed: %s", args)
		}
	}
	link := &stubLink{}
	for _, args := range []string{`{"action":"click","x":0,"y":4,"version":"v1"}`, `{"action":"tabs"}`, `{"action":"tab","target":"t2"}`} {
		if _, err := (&Browser{Link: link}).Execute(context.Background(), json.RawMessage(args)); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(string(link.ops[0]), `"x":0`) {
		t.Fatal("zero coordinate was dropped")
	}
}

func TestBrowserNewTabPreservesCreationIntent(t *testing.T) {
	link := &stubLink{}
	b := &Browser{Link: link}
	for _, raw := range []string{
		`{"action":"tab","tab_action":"new","url":"https://example.test/projects"}`,
		`{"action":"tab","tab_action":"new"}`,
		`{"action":"tab","target":"t2"}`,
		`{"action":"batch","actions":[{"action":"tab","tab_action":"new","url":"https://example.test/"},{"action":"snapshot"}]}`,
	} {
		if _, err := b.Execute(context.Background(), json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
	}
	var created, blank, switched browserArgs
	_ = json.Unmarshal(link.ops[0], &created)
	_ = json.Unmarshal(link.ops[1], &blank)
	_ = json.Unmarshal(link.ops[2], &switched)
	if created.TabAction != "new" || created.URL != "https://example.test/projects" || created.Target != "" || blank.TabAction != "new" || switched.Target != "t2" {
		t.Fatalf("tab intent was lost: %s / %s / %s", link.ops[0], link.ops[1], link.ops[2])
	}
	var batch browserArgs
	_ = json.Unmarshal(link.ops[3], &batch)
	if len(batch.Actions) != 2 || batch.Actions[0].TabAction != "new" || batch.Actions[0].URL != "https://example.test/" {
		t.Fatalf("nested tab intent was lost: %s", link.ops[3])
	}
	for _, raw := range []string{
		`{"action":"tab","tab_action":"new","target":"t1"}`,
		`{"action":"tab","tab_action":"close","target":"t1"}`,
		`{"action":"tab","url":"https://example.test/","target":"t1"}`,
		`{"action":"tab","tab_action":"switch"}`,
		`{"action":"navigate","url":"https://example.test/","tab_action":"new"}`,
	} {
		before := len(link.ops)
		if _, err := b.Execute(context.Background(), json.RawMessage(raw)); err == nil || len(link.ops) != before {
			t.Fatalf("ambiguous tab operation dispatched: %s", raw)
		}
	}
	props := b.InputSchema()["properties"].(map[string]any)
	if _, ok := props["tab_action"]; !ok || !strings.Contains(b.Description(), "human-controlled tab") {
		t.Fatal("model cannot discover separate tab creation and ownership")
	}
}
