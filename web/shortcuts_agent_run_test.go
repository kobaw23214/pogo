package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

// Complements provider-model tests of the actual tool backend: this drives the
// ordinary HTTP message/agent-loop/SSE path with deterministic model responses.
func TestShortcutAgentRunPublishesSavedReceiptAndScopedChange(t *testing.T) {
	h, capture := newStudioHarness(t, []llm.Response{
		toolTurn(tools.ShortcutManageName, `{"action":"create","request_id":"loop-save-one","root_id":"root","nodes":[{"local_id":"root","title":"完整流程","parts":[{"kind":"text","text":"开始|"},{"kind":"node","node_id":"child"},{"kind":"text","text":"|结束"}]},{"local_id":"child","title":"子卡","parts":[{"kind":"text","text":"子卡原文"}]}]}`),
		textTurn("快捷方式已保存。"),
	})
	a := postStudioAgent(t, h, studioAgentCapsBody("Shortcut run", "ready", map[string]any{"tools": []string{tools.ShortcutCreateName}}), http.StatusCreated)
	sid := createStudioConversationBody(t, h, a.ID, "{}").ID
	stream := h.stream(sid, "")
	defer stream.close()
	if event := stream.next(t); event.Type != EvSnapshot {
		t.Fatalf("first frame: %s", event.Type)
	}
	h.post("/api/sessions/"+sid+"/messages", `{"prompt":"把流程保存为快捷方式"}`, http.StatusAccepted)
	change := stream.wait(t, EvShortcutsChanged)
	if change.ShortcutAgentID != a.ID || change.ShortcutID == "" || change.ShortcutRevision != 1 {
		t.Fatalf("invalid scoped SSE invalidation: %+v", change)
	}
	end := stream.wait(t, EvToolEnd)
	if end.IsError || end.Name != tools.ShortcutManageName {
		t.Fatalf("create failed: %+v", end)
	}
	var receipt tools.ShortcutDetails
	raw, err := json.Marshal(end.Details)
	if err != nil || json.Unmarshal(raw, &receipt) != nil || !receipt.Saved || receipt.AgentID != a.ID || receipt.Shortcut == nil || receipt.Shortcut.ID != change.ShortcutID {
		t.Fatalf("invalid tool-end SSE receipt: %s", raw)
	}
	if event := stream.wait(t, EvRunEnd); event.Error != "" {
		t.Fatal(event.Error)
	}
	view, err := h.mgr.userShortcuts(h.session(sid).owner, a.ID)
	if err != nil || len(view.Shortcuts) != 1 || view.Shortcuts[0].ID != change.ShortcutID || len(view.Shortcuts[0].Parts) != 3 || view.Shortcuts[0].Parts[1].Title != "子卡" {
		t.Fatalf("root-only persistence failed: %+v %v", view, err)
	}
	_, _, histories, _ := capture.snapshot()
	if len(histories) != 2 {
		t.Fatalf("model turns=%d, want two", len(histories))
	}
	found := false
	for _, message := range histories[1] {
		for _, block := range message.Content {
			if block.Type == llm.BlockToolResult && strings.Contains(block.Text, change.ShortcutID) {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("saved root receipt did not reach the next model turn")
	}
}
