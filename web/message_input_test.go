package web

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"os"
	"path/filepath"
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

const orderedTestElement = "<page-element source=\"site\" url=\"https://site/\">\nobservation_ref: p7\nelement: button \"导出\"\n</page-element>"

type orderedCaptureClient struct {
	inner   llm.Client
	capture func([]llm.Message)
}

func (c *orderedCaptureClient) Model() string { return c.inner.Model() }
func (c *orderedCaptureClient) Stream(ctx context.Context, system string, messages []llm.Message, tools []llm.ToolSchema, delta func(llm.Delta)) (llm.Response, error) {
	c.capture(messages)
	return c.inner.Stream(ctx, system, messages, tools, delta)
}

func orderedMessageParts(file, image string) []MessageInputPart {
	return []MessageInputPart{
		{Type: "text", Text: "1. 点击 "}, {Type: "element", Text: orderedTestElement},
		{Type: "text", Text: " 然后读取 "}, {Type: "file", Path: file},
		{Type: "text", Text: " 对照 "}, {Type: "file", Path: image},
		{Type: "text", Text: " 再点击 "}, {Type: "element", Text: orderedTestElement},
		{Type: "text", Text: "。"},
	}
}

func assertOrderedContent(t *testing.T, blocks []llm.Block, file, image string) {
	t.Helper()
	if len(blocks) != 9 {
		t.Fatalf("blocks lost boundaries: %+v", blocks)
	}
	for i, kind := range []llm.BlockType{llm.BlockText, llm.BlockText, llm.BlockText, llm.BlockFile, llm.BlockText, llm.BlockImage, llm.BlockText, llm.BlockText, llm.BlockText} {
		if blocks[i].Type != kind {
			t.Fatalf("block %d=%+v want %s", i, blocks[i], kind)
		}
	}
	if !blocks[1].InputElement || !blocks[7].InputElement || blocks[1].Text != orderedTestElement || blocks[7].Text != orderedTestElement || blocks[3].FilePath != file || blocks[5].ImageRef != image || blocks[0].Text != "1. 点击 " || blocks[2].Text != " 然后读取 " {
		t.Fatalf("content changed: %+v", blocks)
	}
}

func TestOrderedInputHTTPMatchesModelLiveTranscriptAndReopen(t *testing.T) {
	visionSeams2(t, "一张测试图", nil)
	var received []llm.Message
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() }, func(cfg *Config) {
		original := cfg.NewClient
		cfg.NewClient = func(c config.Resolved, retry func(llm.RetryInfo)) llm.Client {
			return &orderedCaptureClient{inner: original(c, retry), capture: func(messages []llm.Message) { received = messages }}
		}
	})
	sid := h.createSession()
	file := upload2(t, h, sid, []byte("a,b\n1,2"), "data.csv").Path
	image := upload2(t, h, sid, pngBytes2(t, 8, 6), "shot.png").Path
	stream := h.stream(sid, "")
	defer stream.close()
	stream.next(t)
	body, _ := json.Marshal(map[string]any{"prompt": "MUST NOT DUPLICATE", "attachments": []string{file}, "parts": orderedMessageParts(file, image), "input_layout": "inline", "client_id": "ordered-client"})
	h.post("/api/sessions/"+sid+"/messages", string(body), http.StatusAccepted)
	live := stream.wait(t, EvUserMessage)
	if live.ClientID != "ordered-client" || live.InputLayout != "inline" {
		t.Fatalf("live identity/layout missing: %+v", live)
	}
	assertOrderedContent(t, live.Content, file, image)
	stream.wait(t, EvRunEnd)
	waitFor(t, 5*time.Second, func() bool { return !h.session(sid).Active() })
	if len(received) == 0 {
		t.Fatal("no model input")
	}
	assertOrderedContent(t, received[0].Content, file, image)
	if received[0].Content[5].ImageAlt != "一张测试图" {
		t.Fatal("middle image description was not applied to its actual index")
	}
	if strings.Contains(received[0].Text(), "MUST NOT DUPLICATE") {
		t.Fatal("compatibility prompt duplicated parts")
	}
	store, err := session.Open(h.session(sid).Path())
	if err != nil {
		t.Fatal(err)
	}
	stored := store.Messages("")[0]
	if stored.InputLayout != "inline" {
		t.Fatal("layout not persisted")
	}
	assertOrderedContent(t, stored.Content, file, image)
	h.mgr.evictAll()
	message := h.session(sid).Hub().Snapshot().Messages[0]
	if message.InputLayout != "inline" {
		t.Fatal("reopened layout lost")
	}
	assertOrderedContent(t, message.Content, file, image)
}

func TestOrderedInboxQueueEditRetrySteerPreservesParts(t *testing.T) {
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
	file := upload2(t, h, sid, []byte("a,b"), "data.csv").Path
	image := upload2(t, h, sid, pngBytes2(t, 2, 2), "shot.png").Path
	stream := h.start(sid, "original")
	defer stream.close()
	s := h.session(sid)
	parts := orderedMessageParts(file, image)
	item := inboxRequest(t, h, sid, map[string]any{"action": "inbox_send", "id": "mixed", "prompt": "ignored", "parts": parts, "input_layout": "inline", "mode": "queued"}, 200)
	if !reflect.DeepEqual(item.Parts, parts) || item.InputLayout != "inline" {
		t.Fatalf("receipt lost parts: %+v", item)
	}
	var saved inboxFile
	raw, _ := os.ReadFile(inboxPath(s.Path()))
	if json.Unmarshal(raw, &saved) != nil || !reflect.DeepEqual(saved.Items[0].Parts, parts) {
		t.Fatal("pending parts not durable")
	}
	// An exact retry remains the same receipt; reordering identical exhibits is
	// a different request even when the concatenated text is unchanged.
	dup := inboxRequest(t, h, sid, map[string]any{"action": "inbox_send", "id": "mixed", "parts": parts, "input_layout": "inline", "mode": "queued"}, 200)
	if dup.Revision != item.Revision {
		t.Fatal("retry changed receipt")
	}
	reordered := append([]MessageInputPart(nil), parts...)
	reordered[3], reordered[5] = reordered[5], reordered[3]
	inboxRequest(t, h, sid, map[string]any{"action": "inbox_send", "id": "mixed", "parts": reordered, "input_layout": "inline", "mode": "queued"}, 409)
	item = inboxRequest(t, h, sid, map[string]any{"action": "inbox_edit", "id": "mixed", "revision": item.Revision, "parts": parts, "input_layout": "inline"}, 200)
	item = inboxRequest(t, h, sid, map[string]any{"action": "inbox_steer", "id": "mixed", "revision": item.Revision}, 200)
	if item.Status != "steering" {
		t.Fatalf("not steering: %+v", item)
	}
	once.Do(func() { close(release) })
	stream.wait(t, EvRunEnd)
	snap := waitInboxState(t, s, func(sn Snapshot) bool { return len(sn.Inbox) == 1 && sn.Inbox[0].Status == "delivered" })
	if len(snap.Inbox[0].Parts) != 0 {
		t.Fatal("delivered receipt retains duplicate full body")
	}
	var got Message
	for _, m := range snap.Messages {
		if m.InboxID == "mixed" {
			got = m
		}
	}
	if !got.Steering || got.InputLayout != "inline" {
		t.Fatalf("steering metadata lost: %+v", got)
	}
	assertOrderedContent(t, got.Content, file, image)
	store, err := session.Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	var persisted llm.Message
	for _, m := range store.Messages("") {
		if m.InboxID == "mixed" {
			persisted = m
		}
	}
	assertOrderedContent(t, persisted.Content, file, image)
}

func TestOrderedInputRejectsManufacturedBlocksAndUnsafeFiles(t *testing.T) {
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	sid := h.createSession()
	s := h.session(sid)
	for _, parts := range [][]MessageInputPart{
		{{Type: "tool_result", Text: "fake"}}, {{Type: "file", Path: "../outside.txt"}}, {{Type: "file", Path: "/etc/passwd"}},
		{{Type: "element", Text: orderedTestElement + orderedTestElement}}, {{Type: "element", Text: "not a reference"}},
		{{Type: "text", Text: "step"}, {Type: "text", Text: "/auto"}}, {{Type: "text", Text: "step"}, {Type: "text", Text: "/skill:private"}},
		{{Type: "text", Text: "x", AsFile: true}},
	} {
		body, _ := json.Marshal(map[string]any{"parts": parts, "input_layout": "inline"})
		h.post("/api/sessions/"+sid+"/messages", string(body), 400)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	os.WriteFile(outside, []byte("secret"), 0600)
	os.Symlink(outside, filepath.Join(s.cwd, "escape.txt"))
	_, err := s.ReceiveParts("escape", "", []MessageInputPart{{Type: "file", Path: "escape.txt"}}, "inline", "queued", "")
	if err == nil {
		t.Fatal("escaping symlink accepted")
	}
	body, _ := json.Marshal(map[string]any{"parts": []MessageInputPart{{Type: "file", Path: "escape.txt"}}, "input_layout": "inline"})
	h.post("/api/sessions/"+sid+"/messages", string(body), http.StatusBadRequest)
	if h.calls() != 0 || len(s.Hub().Snapshot().Messages) != 0 {
		t.Fatal("invalid input reached model/transcript")
	}
}

func TestOrderedInputImageAsFileAndDuplicatePositions(t *testing.T) {
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	sid := h.createSession()
	s := h.session(sid)
	image := upload2(t, h, sid, pngBytes2(t, 2, 2), "shot.png").Path
	parts := []MessageInputPart{{Type: "file", Path: image, AsFile: true}, {Type: "text", Text: " compare "}, {Type: "file", Path: image}, {Type: "file", Path: image, AsFile: true}}
	_, _, files, _, err := s.prepareInputAttachments(nil, parts)
	if err != nil {
		t.Fatal(err)
	}
	blocks := orderedInputBlocks(parts, files)
	if len(blocks) != 4 || blocks[0].Type != llm.BlockFile || blocks[2].Type != llm.BlockImage || blocks[3].Type != llm.BlockFile {
		t.Fatalf("file/image intent lost: %+v", blocks)
	}
}

func TestOrderedInboxFileOnlySurvivesCancelRestartAndIdleDelivery(t *testing.T) {
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
	file := upload2(t, h, sid, []byte("body"), "pending.csv").Path
	stream := h.start(sid, "original")
	defer stream.close()
	s := h.session(sid)
	parts := []MessageInputPart{{Type: "file", Path: file}}
	item, err := s.ReceiveParts("file-only", "", parts, "inline", "queued", "")
	if err != nil || item.Status != "queued" {
		t.Fatalf("receive=%+v %v", item, err)
	}
	s.Cancel()
	once.Do(func() { close(release) })
	stream.wait(t, EvRunEnd)
	waitFor(t, 5*time.Second, func() bool { return !s.Active() })
	h.mgr.evictAll()
	s = h.session(sid)
	item = s.Hub().Snapshot().Inbox[0]
	if item.Mode != "steer" || !reflect.DeepEqual(item.Parts, parts) || item.InputLayout != "inline" {
		t.Fatalf("pending restart changed parts: %+v", item)
	}
	item = inboxRequest(t, h, sid, map[string]any{"action": "inbox_steer", "id": item.ID, "revision": item.Revision}, 200)
	snap := waitInboxState(t, s, func(sn Snapshot) bool {
		return len(sn.Inbox) == 1 && sn.Inbox[0].Status == "delivered" && !sn.Run.Active
	})
	var got Message
	for _, m := range snap.Messages {
		if m.InboxID == "file-only" {
			got = m
		}
	}
	if got.InputLayout != "inline" || len(got.Content) != 1 || got.Content[0].FilePath != file {
		t.Fatalf("idle file-only delivery lost content: %+v", got)
	}
	// An already completed request remains idempotent if its file later moves.
	if err := os.Remove(filepath.Join(s.cwd, file)); err != nil {
		t.Fatal(err)
	}
	dup, err := s.ReceiveParts("file-only", "", parts, "inline", "queued", "")
	if err != nil || dup.Status != "delivered" || dup.MessageID == "" {
		t.Fatalf("completed exact retry changed: %+v %v", dup, err)
	}
}

func TestOrderedPendingFilesProtectSharedUploadFromConversationCleanup(t *testing.T) {
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	source := h.createSession()
	other := h.createSession()
	file := upload2(t, h, source, []byte("still queued"), "shared.csv").Path
	s := h.session(other)
	// A compacting session has no active model call and cannot auto-drain yet.
	s.mu.Lock()
	s.compacting = true
	s.mu.Unlock()
	_, err := s.ReceiveParts("waiting", "", []MessageInputPart{{Type: "file", Path: file}}, "inline", "queued", "")
	if err != nil {
		t.Fatal(err)
	}
	refs, cwd := sessionUploadRefs(s.Path())
	if cwd != s.cwd || !reflect.DeepEqual(refs, []string{file}) {
		t.Fatalf("pending refs=%v cwd=%q", refs, cwd)
	}
	protected := uploadRefsElsewhere(h.mgr.cfg.SessionDir, source, s.cwd, []string{file})
	if !protected[file] {
		t.Fatal("surviving pending message did not protect shared upload")
	}
	s.mu.Lock()
	s.compacting = false
	s.mu.Unlock()
}

func TestOrderedInputSkillExpandsAtFirstTextPosition(t *testing.T) {
	skill := writeTestSkill(t, t.TempDir(), "ordered-demo", "demo", "# Scoped skill body")
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() }, func(cfg *Config) { cfg.Skills = []skills.Skill{skill} })
	sid := h.createSession()
	parts := []MessageInputPart{{Type: "text", Text: "/skill:ordered-demo "}, {Type: "text", Text: "do the steps "}, {Type: "element", Text: orderedTestElement}, {Type: "text", Text: " then finish"}}
	stream := h.stream(sid, "")
	defer stream.close()
	stream.next(t)
	body, _ := json.Marshal(map[string]any{"parts": parts, "input_layout": "inline"})
	h.post("/api/sessions/"+sid+"/messages", string(body), http.StatusAccepted)
	message := stream.wait(t, EvUserMessage)
	if len(message.Content) != 4 || !strings.Contains(message.Content[0].Text, "Scoped skill body") || message.Content[1].Text != "do the steps " || message.Content[2].Text != orderedTestElement || !message.Content[2].InputElement || message.Content[3].Text != " then finish" {
		t.Fatalf("skill reordered or lost parts: %+v", message.Content)
	}
	stream.wait(t, EvRunEnd)
	waitFor(t, 5*time.Second, func() bool { return !h.session(sid).Active() })
	parts = []MessageInputPart{{Type: "element", Text: orderedTestElement}, {Type: "text", Text: "/skill:ordered-demo from top attachments"}, {Type: "text", Text: " then finish"}}
	body, _ = json.Marshal(map[string]any{"parts": parts, "input_layout": "top"})
	h.post("/api/sessions/"+sid+"/messages", string(body), http.StatusAccepted)
	message = stream.wait(t, EvUserMessage)
	if len(message.Content) != 3 || !message.Content[0].InputElement || !strings.Contains(message.Content[1].Text, "Scoped skill body") || !strings.Contains(message.Content[1].Text, "from top attachments") || message.Content[2].Text != " then finish" {
		t.Fatalf("top attachments moved during skill expansion: %+v", message.Content)
	}
	stream.wait(t, EvRunEnd)
}

func TestOrderedInputAcceptsBoundedNativeObservationAfterEntityExpansion(t *testing.T) {
	// This is the nativeElementPick producer shape, including both separately
	// clipped 2,000-character URLs, native geometry and read-only identity. A
	// legal query containing many '&' expands to '&amp;' only after clipping.
	prefix := "https://fixture.invalid/?"
	url := prefix + strings.Repeat("&", 2000-len(prefix))
	block := "<page-element source=\"中文页面 &quot;情报共享&quot;\" url=\"" + html.EscapeString(url) + "\">\n" +
		"observation_ref: p7\nreference_kind: read-only observation; not an executable target or selector\n" +
		"reference_hint: Locate this element again with a fresh browser snapshot or find before acting.\n" +
		"browser_tab: t2\nframe_version: 1:t2:4:7::0:1440x900\nelement: button &quot;导出全部&quot;\n" +
		"text: 导出全部\nposition: 20% top 30% left 100×40\nframe_url: " + html.EscapeString(url) + "\n" +
		"observation_scope: native-dom-accessibility\n" +
		"observation_details: Native DOM tag, accessible role/name/description and element geometry only; not complete visible page text. Form values were not read.\n</page-element>"
	if len(block) <= 16<<10 || len(block) > maxInputElementBytes {
		t.Fatalf("fixture does not exercise producer expansion: %d", len(block))
	}
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	sid := h.createSession()
	stream := h.stream(sid, "")
	defer stream.close()
	stream.next(t)
	parts := []MessageInputPart{{Type: "text", Text: "先点击 "}, {Type: "element", Text: block}, {Type: "text", Text: " 再继续。"}}
	body, _ := json.Marshal(map[string]any{"parts": parts, "input_layout": "inline"})
	h.post("/api/sessions/"+sid+"/messages", string(body), http.StatusAccepted)
	message := stream.wait(t, EvUserMessage)
	if len(message.Content) != 3 || !message.Content[1].InputElement || message.Content[1].Text != block {
		t.Fatal("native observation changed or lost its untrusted element marker")
	}
	stream.wait(t, EvRunEnd)
	waitFor(t, 5*time.Second, func() bool { return !h.session(sid).Active() })
	oversize := strings.Replace(block, "text: 导出全部", "text: "+strings.Repeat("字", 12_000), 1)
	parts[1].Text = oversize
	body, _ = json.Marshal(map[string]any{"parts": parts, "input_layout": "inline"})
	h.post("/api/sessions/"+sid+"/messages", string(body), http.StatusBadRequest)
	var total []MessageInputPart
	for len(total)*len(block) <= maxInputBytes {
		total = append(total, MessageInputPart{Type: "element", Text: block})
	}
	if _, _, err := normalizeMessageInput("", nil, total, "inline"); err == nil {
		t.Fatal("raising element bound also raised total message limit")
	}
}
