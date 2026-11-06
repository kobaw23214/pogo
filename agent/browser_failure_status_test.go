package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

type failureStatusBrowserLink struct {
	reply tools.BrowserReply
	calls int
}

func (*failureStatusBrowserLink) BrowserBackend() string { return tools.BrowserBackendNative }
func (b *failureStatusBrowserLink) Execute(context.Context, json.RawMessage) (tools.BrowserReply, error) {
	b.calls++
	return b.reply, nil
}

func TestBrowserExecutorStatusReachesHistoryUIAndStoredResult(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   string
		reply  tools.BrowserReply
		failed bool
	}{
		{"navigation-timeout", `{"action":"navigate","url":"https://site.invalid/"}`, tools.BrowserReply{OK: false, Error: "page.goto: Timeout 60000ms exceeded", ActiveTab: "t2"}, true},
		{"page-error-text-is-data", `{"action":"snapshot"}`, tools.BrowserReply{OK: true, Text: "Error reports: 4 failed transactions", URL: "https://site.invalid/"}, false},
		{"partial-batch", `{"action":"batch","actions":[{"action":"type","target":"e1","text":"example"},{"action":"click","target":"e2"},{"action":"snapshot"}]}`, tools.BrowserReply{OK: false, Error: "batch stopped at step 2/3", Steps: []tools.BrowserStep{{Action: "type", OK: true}, {Action: "click", OK: false, Error: "stale ref e2"}}, Batch: &tools.BrowserBatch{Requested: 3, Executed: 2, Skipped: 1, StopReason: "step_failed"}, Screenshot: &tools.BrowserScreenshot{ImageRef: "uploads/browser-failure.png", Mime: "image/png", Width: 100, Height: 80}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			link := &failureStatusBrowserLink{reply: tc.reply}
			registry := tools.New(tools.Options{Cwd: dir, SpillDir: filepath.Join(dir, "spill"), ResultScope: t.Name(), Browser: link, BrowserImageMode: tools.BrowserImageDirect})
			browser, ok := registry.Get("browser")
			if !ok {
				t.Fatal("browser fixture absent")
			}
			a := New(Config{Registry: registry})
			events := make(chan Event, 8)
			args := json.RawMessage(tc.args)
			call := llm.Block{Type: llm.BlockToolUse, ID: "browser-call", Name: "browser", Input: args}
			block := a.runToolCall(ctx, 0, reviewedCall{call: call, tool: browser, args: args}, nil, events)
			if block.IsError != tc.failed || link.calls != 1 || block.ResultID == "" {
				t.Fatalf("history status or retained identity lost: %+v calls=%d", block, link.calls)
			}
			found := false
			for len(events) > 0 {
				event := <-events
				if event.Kind == EventToolEnd {
					found = true
					if event.IsError != tc.failed || event.ToolOutput == "" {
						t.Fatalf("UI status or observation lost: %+v", event)
					}
				}
			}
			if !found {
				t.Fatal("tool completion event missing")
			}
			reader, ok := registry.Get(tools.ToolResultReadName)
			if !ok {
				t.Fatal("result reader absent")
			}
			input, _ := json.Marshal(map[string]any{"result_id": block.ResultID, "view": "text", "limit": 8192})
			saved, err := reader.Execute(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(saved.Details)
			var details map[string]any
			if json.Unmarshal(encoded, &details) != nil || details["original_is_error"] != tc.failed {
				t.Fatalf("saved result erased original failure: %s", encoded)
			}
			if tc.name == "partial-batch" {
				if !strings.Contains(block.Text, "1. type — ok") || !strings.Contains(block.Text, "2. click — failed") || !strings.Contains(block.Text, "1 not executed") || len(block.Images) != 1 {
					t.Fatalf("partial progress or screenshot lost: %+v", block)
				}
			}
		})
	}
}
