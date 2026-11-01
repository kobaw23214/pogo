package web

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/yosukeno/pogo/llm"
)

// Exercise real message receipt/delivery, transcript persistence, sharing, and
// the public read endpoint. A hand-authored transcript would miss metadata
// getting lost anywhere between those boundaries.
func TestShareCreateReadKeepsSteeringAnswerAndStopsAtNextTask(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	h := newHarness(t, func(n int) (llm.Response, error) {
		switch n {
		case 1:
			<-release
			return textTurn("Working on the original task."), nil
		case 2:
			return textTurn("Original task completed with the supplementary requirement."), nil
		default:
			return textTurn("Separate task answer."), nil
		}
	})
	sid := h.createSession()
	stream := h.start(sid, "Original task")
	defer stream.close()
	inboxRequest(t, h, sid, map[string]any{"action": "inbox_send", "id": "share-steer", "prompt": "Supplement the original task", "mode": "steer"}, http.StatusOK)
	inboxRequest(t, h, sid, map[string]any{"action": "inbox_send", "id": "share-next", "prompt": "Separate next task", "mode": "queued"}, http.StatusOK)
	once.Do(func() { close(release) })
	s := h.session(sid)
	waitInboxState(t, s, func(sn Snapshot) bool {
		if sn.Run.Active || len(sn.Inbox) != 2 {
			return false
		}
		for _, item := range sn.Inbox {
			if item.Status != "delivered" {
				return false
			}
		}
		for _, m := range sn.Messages {
			if m.Role == "assistant" && len(m.Content) > 0 && m.Content[0].Text == "Separate task answer." {
				return true
			}
		}
		return false
	})
	if h.calls() != 3 {
		t.Fatalf("calls=%d want3", h.calls())
	}
	publish := func(question string) shareEntry {
		qid := questionID(t, h, sid, question)
		var created struct {
			ID string `json:"id"`
		}
		if err := decodeJSONBody(h.do(http.MethodPost, "/api/sessions/"+sid+"/share", fmt.Sprintf(`{"message_id":%q}`, qid)), &created); err != nil {
			t.Fatal(err)
		}
		var view shareEntry
		if err := decodeJSONBody(h.get("/api/share/"+created.ID), &view); err != nil {
			t.Fatal(err)
		}
		return view
	}
	original := publish("Original task")
	if original.Question != "Original task" || original.Answer != "Working on the original task.\n\nOriginal task completed with the supplementary requirement." {
		t.Fatalf("original exchange truncated: %+v", original)
	}
	steering := 0
	for _, m := range original.Messages {
		if m.Steering {
			steering++
			if m.InboxID != "share-steer" {
				t.Fatal("wrong supplementary message")
			}
		}
		for _, b := range m.Content {
			if strings.Contains(b.Text, "Separate") {
				t.Fatal("next task leaked into original share")
			}
		}
	}
	if steering != 1 {
		t.Fatalf("share lost steering provenance: %d", steering)
	}
	next := publish("Separate next task")
	if next.Question != "Separate next task" || next.Answer != "Separate task answer." || len(next.Messages) != 2 {
		t.Fatalf("visible user ordinal drifted after steering: %+v", next)
	}
}
