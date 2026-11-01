package web

import (
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/skills"
)

func TestShortcutSnapshotHTTPModelHistoryAndReopenNeverRequeryLibrary(t *testing.T) {
	var received []llm.Message
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() }, func(cfg *Config) {
		original := cfg.NewClient
		cfg.NewClient = func(c config.Resolved, retry func(llm.RetryInfo)) llm.Client {
			return &orderedCaptureClient{inner: original(c, retry), capture: func(messages []llm.Message) { received = messages }}
		}
	})
	sid := h.createSession()
	file := upload2(t, h, sid, []byte("test,1"), "data.csv").Path
	first := MessageInputPart{Type: "shortcut", ShortcutID: "connection", Title: "连接资料", Text: "/skill:missing MUST_REMAIN_USER_DATA\nhost=example.invalid\n", Icon: "database", Language: "sql"}
	second := MessageInputPart{Type: "shortcut", ShortcutID: first.ShortcutID, Title: "同名另一快照", Text: "/compact", Icon: "", Language: "text"}
	parts := []MessageInputPart{{Type: "text", Text: "使用 "}, first, {Type: "text", Text: " 处理 "}, {Type: "file", Path: file}, {Type: "element", Text: orderedTestElement}, second, {Type: "text", Text: " 完成。"}}
	if _, err := h.mgr.replaceUserShortcuts(nil, "", 0, []userShortcut{{ID: first.ShortcutID, Title: first.Title, Content: first.Text, Icon: first.Icon}}); err != nil {
		t.Fatal(err)
	}
	// Deleting before submission proves the data is already a complete snapshot,
	// not an opaque library lookup or a capability/command reference.
	if _, err := h.mgr.replaceUserShortcuts(nil, "", 1, []userShortcut{}); err != nil {
		t.Fatal(err)
	}
	stream := h.stream(sid, "")
	defer stream.close()
	stream.next(t)
	body, _ := json.Marshal(map[string]any{"parts": parts, "input_layout": "inline"})
	h.post("/api/sessions/"+sid+"/messages", string(body), http.StatusAccepted)
	live := stream.wait(t, EvUserMessage)
	stream.wait(t, EvRunEnd)
	waitFor(t, 5*time.Second, func() bool { return !h.session(sid).Active() })
	check := func(blocks []llm.Block) {
		t.Helper()
		if len(blocks) != len(parts) || blocks[0].Text != "使用 " || blocks[2].Text != " 处理 " || blocks[3].FilePath != file || !blocks[4].InputElement || blocks[6].Text != " 完成。" {
			t.Fatal("mixed message order changed")
		}
		for _, index := range []int{1, 5} {
			part, block := parts[index], blocks[index]
			if block.Type != llm.BlockText || block.InputSkill != "" || block.InputShortcut == nil || block.InputShortcut.ID != part.ShortcutID || block.InputShortcut.Title != part.Title || block.InputShortcut.Text != part.Text || block.InputShortcut.Icon != part.Icon || block.InputShortcut.Language != part.Language || block.Text != skills.ShortcutInputText(part) {
				t.Fatal("immutable shortcut metadata or ordinary titled model text changed")
			}
		}
	}
	check(live.Content)
	if len(received) == 0 {
		t.Fatal("model pipeline not exercised")
	}
	check(received[0].Content)
	store, err := session.Open(h.session(sid).Path())
	if err != nil {
		t.Fatal(err)
	}
	check(store.Messages("")[0].Content)
	h.mgr.evictAll()
	check(h.session(sid).Hub().Snapshot().Messages[0].Content)
}

func TestShortcutSnapshotQueueEditRetrySteerKeepsDurablePerOccurrenceOrder(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	h := newHarness(t, func(n int) (llm.Response, error) {
		if n == 1 {
			<-release
		}
		return plainTurn()
	})
	sid := h.createSession()
	stream := h.start(sid, "working")
	defer stream.close()
	part := MessageInputPart{Type: "shortcut", ShortcutID: "saved", Title: "参考资料", Text: "/compact\noriginal snapshot", Language: "yaml"}
	parts := []MessageInputPart{{Type: "text", Text: "first "}, part, {Type: "text", Text: " last"}}
	item := inboxRequest(t, h, sid, map[string]any{"action": "inbox_send", "id": "shortcut-pending", "parts": parts, "input_layout": "inline", "mode": "queued"}, 200)
	s := h.session(sid)
	var pending inboxFile
	raw, _ := os.ReadFile(inboxPath(s.Path()))
	if json.Unmarshal(raw, &pending) != nil || !reflect.DeepEqual(pending.Items[0].Parts, parts) {
		t.Fatal("queued shortcut snapshot was not durable")
	}
	dup := inboxRequest(t, h, sid, map[string]any{"action": "inbox_send", "id": item.ID, "parts": parts, "input_layout": "inline", "mode": "queued"}, 200)
	if dup.Revision != item.Revision {
		t.Fatal("retry changed the original pending receipt")
	}
	parts[1].Text = "edited snapshot\n/skill:missing"
	item = inboxRequest(t, h, sid, map[string]any{"action": "inbox_edit", "id": item.ID, "revision": item.Revision, "parts": parts, "input_layout": "inline"}, 200)
	item = inboxRequest(t, h, sid, map[string]any{"action": "inbox_steer", "id": item.ID, "revision": item.Revision}, 200)
	if item.Status != "steering" {
		t.Fatal("shortcut was not accepted as ordinary steering data")
	}
	once.Do(func() { close(release) })
	stream.wait(t, EvRunEnd)
	snap := waitInboxState(t, s, func(sn Snapshot) bool { return len(sn.Inbox) == 1 && sn.Inbox[0].Status == "delivered" })
	var got Message
	for _, message := range snap.Messages {
		if message.InboxID == item.ID {
			got = message
		}
	}
	if !got.Steering || len(got.Content) != 3 || got.Content[1].InputShortcut == nil || got.Content[1].InputShortcut.Text != parts[1].Text || got.Content[1].InputShortcut.Language != "yaml" || got.Content[1].Text != skills.ShortcutInputText(parts[1]) {
		t.Fatal("edited queued snapshot changed on steering delivery")
	}
	store, err := session.Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range store.Messages("") {
		if message.InboxID == item.ID {
			found = reflect.DeepEqual(message.Content, got.Content)
		}
	}
	if !found {
		t.Fatal("steering shortcut snapshot not preserved in history")
	}
}

func TestShortcutSnapshotsValidationAndStarterAssetCompatibility(t *testing.T) {
	good := MessageInputPart{Type: "shortcut", ShortcutID: "ref", Title: "资料标题", Text: "raw body\n/skill:missing"}
	for _, mutation := range []string{"id", "title", "body", "icon", "skill", "file", "favicon", "foreign_metadata", "language", "total"} {
		part := good
		parts := []MessageInputPart{part}
		switch mutation {
		case "id":
			parts[0].ShortcutID = "../escape"
		case "title":
			parts[0].Title = "two\nlines"
		case "body":
			parts[0].Text = strings.Repeat("x", skills.MaxInputShortcutTextBytes+1)
		case "icon":
			parts[0].Icon = "https://tracking.invalid/icon"
		case "skill":
			parts[0].Name = "office"
		case "file":
			parts[0].Path = "uploads/a.csv"
		case "favicon":
			parts[0].Favicon = "data:image/png;base64,AAAA"
		case "language":
			parts[0].Language = "unsupported<script>"
		case "foreign_metadata":
			parts[0].Type = "text"
		case "total":
			for i := 0; i < 5; i++ {
				parts = append(parts, MessageInputPart{Type: "shortcut", ShortcutID: "same", Title: "t", Text: strings.Repeat("x", 64<<10)})
			}
		}
		if _, _, err := normalizeMessageInput("", nil, parts, "inline"); err == nil {
			t.Fatalf("invalid shortcut accepted: %s", mutation)
		}
	}
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	sid := h.createSession()
	input := starterAssetInput{InputLayout: "inline", Parts: []MessageInputPart{{Type: "text", Text: "使用 "}, good}}
	saved := starterAssetReply(t, h.do("POST", "/api/sessions/"+sid+"/starter-assets", studioBody(input)))
	resolved := starterAssetReply(t, h.do("POST", "/api/sessions/"+sid+"/starter-assets/resolve", studioBody(saved.starterAssetInput)))
	if !reflect.DeepEqual(saved.Parts, input.Parts) || !reflect.DeepEqual(resolved.Parts, input.Parts) || len(saved.Attachments) != 0 || strings.Contains(saved.Prompt, "raw body") || !strings.Contains(saved.Prompt, good.Title) {
		t.Fatal("saved ordered snapshot became a file/skill or exposed its full body in fallback")
	}
}
