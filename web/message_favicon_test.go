package web

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
)

func TestElementFaviconHTTPTranscriptReopenKeepsPerOccurrenceSnapshot(t *testing.T) {
	var received []llm.Message
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() }, func(cfg *Config) {
		original := cfg.NewClient
		cfg.NewClient = func(c config.Resolved, retry func(llm.RetryInfo)) llm.Client {
			return &orderedCaptureClient{inner: original(c, retry), capture: func(messages []llm.Message) { received = messages }}
		}
	})
	sid := h.createSession()
	first := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes2(t, 4, 4))
	second := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes2(t, 5, 5))
	parts := []MessageInputPart{{Type: "text", Text: "旧图标 "}, {Type: "element", Text: orderedTestElement, Favicon: first}, {Type: "text", Text: " 同URL后来图标 "}, {Type: "element", Text: orderedTestElement, Favicon: second}}
	stream := h.stream(sid, "")
	defer stream.close()
	stream.next(t)
	body, _ := json.Marshal(map[string]any{"parts": parts, "input_layout": "inline"})
	h.post("/api/sessions/"+sid+"/messages", string(body), http.StatusAccepted)
	live := stream.wait(t, EvUserMessage)
	stream.wait(t, EvRunEnd)
	waitFor(t, 5*time.Second, func() bool { return !h.session(sid).Active() })
	assert := func(blocks []llm.Block) {
		t.Helper()
		if len(blocks) != 4 || blocks[1].InputElementFavicon != first || blocks[3].InputElementFavicon != second || blocks[1].Text != orderedTestElement || blocks[3].Text != orderedTestElement || strings.Contains(blocks[1].Text, "base64") {
			t.Fatal("per-occurrence display snapshot or observation changed")
		}
	}
	assert(live.Content)
	if len(received) == 0 {
		t.Fatal("model pipeline not exercised")
	}
	assert(received[0].Content)
	store, err := session.Open(h.session(sid).Path())
	if err != nil {
		t.Fatal(err)
	}
	assert(store.Messages("")[0].Content)
	h.mgr.evictAll()
	assert(h.session(sid).Hub().Snapshot().Messages[0].Content)
}

func TestElementFaviconInputRejectsRemoteSVGAndNonElementMetadata(t *testing.T) {
	for _, part := range []MessageInputPart{{Type: "element", Text: orderedTestElement, Favicon: "https://tracking.test/icon.png"}, {Type: "element", Text: orderedTestElement, Favicon: "data:image/svg+xml;base64,PHN2Zy8+"}, {Type: "text", Text: "x", Favicon: "data:image/png;base64,AAAA"}, {Type: "skill", Name: "office", Favicon: "data:image/png;base64,AAAA"}, {Type: "file", Path: "uploads/a.csv", Favicon: "data:image/png;base64,AAAA"}} {
		if _, _, err := normalizeMessageInput("", nil, []MessageInputPart{part}, "inline"); err == nil {
			t.Fatal("unsafe/non-element favicon accepted")
		}
	}
}
