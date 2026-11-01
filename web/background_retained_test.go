package web

import (
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/tools"
)

func TestRetainedBgCompletionBillsAndNotifiesOnce(t *testing.T) {
	h := newHarness(t, func(int) (llm.Response, error) { return textTurn("noted"), nil })
	s := h.session(h.createSession())
	task := tools.BgTask{ID: "run-retained", AgentID: "agent-retained", Mode: tools.ModeExplore, Task: "inspect", StartedAt: time.Now()}
	result := tools.BgResult{Text: "done", Details: tools.SubagentDetails{ID: task.ID, RunID: task.ID, AgentID: task.AgentID, Mode: task.Mode, Status: "idle", InputTok: 27, OutTok: 5}}
	s.onBgStart(task)
	s.onBgComplete(task, result)
	s.onBgComplete(task, result)
	s.mu.Lock()
	if s.bgWake != nil {
		s.bgWake.Stop()
		s.bgWake = nil
	}
	inbox := append([]string(nil), s.bgInbox...)
	u, d := s.store.UsageTotals()
	path := s.store.Path()
	s.mu.Unlock()
	if len(inbox) != 1 || !strings.Contains(inbox[0], task.AgentID) {
		t.Fatalf("duplicate/missing notification: %+v", inbox)
	}
	if u.Input != 27 || u.Output != 5 || d.Input != 27 {
		t.Fatalf("duplicate charge: %+v %+v", u, d)
	}
	// A retry after reopening the durable store is also suppressed, including
	// hub totals, without resetting the child's identity or old token baseline.
	reopened, err := session.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.store = reopened
	s.mu.Unlock()
	s.onBgComplete(task, result)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.bgInbox) != 1 {
		t.Fatal("reopened completion notified twice")
	}
	if snap := s.hub.Snapshot(); snap.Usage.Input != 27 || snap.Usage.Output != 5 {
		t.Fatalf("duplicate hub charge: %+v", snap.Usage)
	}
}
