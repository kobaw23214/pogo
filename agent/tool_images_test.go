package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func TestToolScreenshotReachesNextModelRequestByReference(t *testing.T) {
	tool := &fakeTool{name: "browser", run: func(context.Context, json.RawMessage) (tools.Result, error) {
		return tools.Result{Text: "page version v1", Images: []llm.Block{{Type: llm.BlockImage, ImageRef: "uploads/browser.jpg", ImageMime: "image/jpeg", DataURL: "must-not-persist"}}}, nil
	}}
	client := &fakeClient{responses: []llm.Response{toolCalls("browser")}}
	a := New(Config{Client: client, Registry: tools.NewRegistry(tool), ImageResolver: func(ref string) (string, bool) {
		return "data:image/jpeg;base64,wire-only", ref == "uploads/browser.jpg"
	}})
	for range drain(a.Run(context.Background(), "observe page")) {
	}
	var seen, stored *llm.Block
	for _, m := range client.lastSeen() {
		for _, b := range m.Content {
			if len(b.Images) > 0 {
				copy := b
				seen = &copy
			}
		}
	}
	for _, m := range a.Messages() {
		for _, b := range m.Content {
			if len(b.Images) > 0 {
				copy := b
				stored = &copy
			}
		}
	}
	if seen == nil || seen.Images[0].DataURL != "data:image/jpeg;base64,wire-only" {
		t.Fatalf("model never received screenshot: %+v", seen)
	}
	if stored == nil || stored.Images[0].DataURL != "" || stored.Images[0].ImageRef != "uploads/browser.jpg" {
		t.Fatalf("persisted bytes or lost reference: %+v", stored)
	}
	raw, _ := json.Marshal(a.Messages())
	if strings.Contains(string(raw), "base64") || strings.Contains(string(raw), "must-not-persist") {
		t.Fatal("image bytes leaked into history")
	}
	// Prompt editing must drop the visual observation along with the old text,
	// while leaving the original transcript available for replay.
	history := a.Messages()
	cands, _ := clearable(history, nil)
	if len(cands) != 1 {
		t.Fatalf("expected screenshot result candidate, got %d", len(cands))
	}
	edited, _ := apply(history, cands, map[string]bool{stored.ToolUseID: true})
	for _, m := range edited {
		for _, b := range m.Content {
			if len(b.Images) > 0 {
				t.Fatal("cleared screenshot still sent to model")
			}
		}
	}
	if len(stored.Images) != 1 {
		t.Fatal("context editing mutated original image reference")
	}
}
