package web

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

// A background task's state belongs to the session: the run that dispatched it
// ending must not take the task's card down with it.
func TestBgTaskStateSurvivesRunEnd(t *testing.T) {
	h := NewHub()
	h.Publish(Event{Type: EvRunStart, RunID: "r1"})
	h.Publish(Event{Type: EvSubagentStart, TaskID: "sub1", Name: "explore", Text: "look at x", TS: 1000})
	h.Publish(Event{Type: EvSubagentFrame, TaskID: "sub1", Frame: json.RawMessage(`{"type":"turn_start","turn":1}`)})
	h.Publish(Event{Type: EvRunEnd})

	snap := h.Snapshot()
	if len(snap.BgTasks) != 1 {
		t.Fatalf("the run's end wiped the background task: %+v", snap.BgTasks)
	}
	task := snap.BgTasks[0]
	if task.Status != "running" || len(task.Frames) != 1 {
		t.Errorf("the task did not keep its running state and frames: %+v", task)
	}

	h.Publish(Event{
		Type: EvSubagentEnd, TaskID: "sub1", TS: 5000,
		Details: tools.SubagentDetails{ID: "sub1", Turns: 3},
		Usage:   &llm.Usage{Input: 120, Output: 6},
	})
	snap = h.Snapshot()
	task = snap.BgTasks[0]
	if task.Status != "done" || task.DurationMS != 4000 {
		t.Errorf("the completion did not settle the task: %+v", task)
	}
	d, ok := task.Details.(tools.SubagentDetails)
	if !ok || d.Turns != 3 {
		t.Errorf("the details did not arrive: %+v", task.Details)
	}
	// The snapshot's total is the run-billed spend plus the background spend.
	if snap.Usage.Input != 120 || snap.Usage.Output != 6 {
		t.Errorf("background spend did not enter the total: %+v", snap.Usage)
	}
}

// The session's own pending cards are cleared at a run's end, but a
// subagent's is not: a background task asks whenever it asks, and clearing its
// card would leave nothing for the reviewer to answer.
func TestRunEndKeepsSubagentGates(t *testing.T) {
	h := NewHub()
	h.Publish(Event{Type: EvRunStart, RunID: "r1"})
	h.Publish(Event{Type: EvGateRequest, GateID: "g1", CallID: "c1", Name: "bash"})
	h.Publish(Event{Type: EvGateRequest, GateID: "g2", CallID: "sub1:c2", Name: "bash", Origin: "sub1"})
	h.Publish(Event{Type: EvRunEnd})

	gates := h.Snapshot().Live.PendingGates
	if len(gates) != 1 || gates[0].GateID != "g2" {
		t.Fatalf("run end cleared the wrong gates: %+v", gates)
	}
}

// Finished tasks are capped so a session that delegates all day does not grow
// every snapshot without bound; the oldest finished ones go first.
func TestBgFinishedTasksAreCapped(t *testing.T) {
	h := NewHub()
	for i := 0; i < maxBgTaskHistory+5; i++ {
		id := fmt.Sprintf("sub%02d", i)
		h.Publish(Event{Type: EvSubagentStart, TaskID: id, TS: int64(1000 + i)})
		h.Publish(Event{Type: EvSubagentEnd, TaskID: id, TS: int64(2000 + i)})
	}
	tasks := h.Snapshot().BgTasks
	if len(tasks) != maxBgTaskHistory {
		t.Fatalf("history not capped: %d tasks", len(tasks))
	}
	if tasks[0].TaskID != "sub05" {
		t.Errorf("the newest tasks were not the survivors: first is %s", tasks[0].TaskID)
	}
}

// A client that missed the live frames (they are never in the replay log)
// heals its card from the copy the end event carries; the same copy is what a
// replay of that end event serves.
func TestSubagentEndCarriesTheFrames(t *testing.T) {
	h := NewHub()
	h.Publish(Event{Type: EvSubagentStart, TaskID: "sub1", TS: 1000})
	h.Publish(Event{Type: EvSubagentFrame, TaskID: "sub1", Frame: json.RawMessage(`{"type":"turn_start","turn":1}`)})

	_, ch, cancel := h.Subscribe(0)
	defer cancel()
	h.Publish(Event{Type: EvSubagentEnd, TaskID: "sub1", TS: 5000})

	// The live subscriber gets the frames with the end.
	var got Event
	for e := range ch {
		if e.Type == EvSubagentFrame {
			continue // frames still flow live; they just are not logged
		}
		got = e
		break
	}
	if got.Type != EvSubagentEnd || len(got.Frames) != 1 {
		t.Fatalf("the end event did not carry the frames: %+v", got)
	}

	// And a reconnect replaying the log gets them, which is the whole point:
	// the end is logged, so its frames ride along.
	backlog, _, cancel2 := h.Subscribe(0)
	defer cancel2()
	var end *Event
	for _, e := range backlog {
		if e.Type == EvSubagentFrame {
			t.Fatal("a frame entered the replay log")
		}
		if e.Type == EvSubagentEnd {
			cp := e
			end = &cp
		}
	}
	if end == nil || len(end.Frames) != 1 {
		t.Fatalf("the replayed end lost the frames: %+v", end)
	}
}

// A background completion that arrives while the session is idle must wake it:
// the notification becomes the prompt of a fresh run, and the spend is on disk
// by the time anyone looks.
func TestBgCompletionWakesAnIdleSession(t *testing.T) {
	h := newHarness(t, func(int) (llm.Response, error) { return textTurn("noted"), nil })
	sid := h.createSession()
	sess := h.session(sid)

	task := tools.BgTask{ID: "subdeadbeef", Mode: tools.ModeExplore, Task: "analyse the sample", StartedAt: time.Now()}
	sess.onBgStart(task)
	sess.onBgComplete(task, tools.BgResult{
		Text:    "it is a dropper",
		Details: tools.SubagentDetails{ID: "subdeadbeef", Mode: "explore", Turns: 2, InputTok: 120, OutTok: 30, Session: "/tmp/child.jsonl"},
	})

	deadline := time.Now().Add(10 * time.Second)
	for {
		snap := sess.hub.Snapshot()
		seen := false
		for _, m := range snap.Messages {
			if m.Role == "user" && len(m.Content) > 0 &&
				strings.HasPrefix(m.Content[0].Text, bgNotificationMarker) {
				seen = true
			}
		}
		if seen && !snap.Run.Active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the completion never woke the session")
		}
		time.Sleep(20 * time.Millisecond)
	}

	usage, delegated := sess.store.UsageTotals()
	if usage.Input != 120 || usage.Output != 30 {
		t.Errorf("the background spend is not on disk: %+v", usage)
	}
	if delegated.Input != 120 {
		t.Errorf("the background spend is not attributed as delegated: %+v", delegated)
	}
	// And the model actually answered the notification, which is the point of
	// waking it.
	if h.calls() == 0 {
		t.Error("the wake never reached the model")
	}
}

// A subagent's unanswered card must not hold the main conversation's approvals
// behind it: the two lanes are separate precisely so this cannot hang.
func TestSubagentApprovalDoesNotBlockTheMainLane(t *testing.T) {
	hub := NewHub()
	policy := NewPolicy()
	policy.Set(ModeStrict, 0)
	gate := NewWebGate(hub, policy, 5*time.Second)

	subDone := make(chan agent.GateDecision, 1)
	go func() {
		subDone <- gate.Review(context.Background(), agent.GateRequest{
			CallID: "sub1:c1", ToolName: "bash", Origin: "sub1",
			Args: json.RawMessage(`{"command":"ls"}`),
		})
	}()
	// Wait for the subagent's card to be on screen, and deliberately leave it
	// unanswered.
	var subGateID string
	deadline := time.Now().Add(2 * time.Second)
	for subGateID == "" && time.Now().Before(deadline) {
		for _, g := range hub.Snapshot().Live.PendingGates {
			if g.Origin == "sub1" {
				subGateID = g.GateID
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if subGateID == "" {
		t.Fatal("the subagent's card never appeared")
	}

	mainDone := make(chan agent.GateDecision, 1)
	go func() {
		mainDone <- gate.Review(context.Background(), agent.GateRequest{
			CallID: "c2", ToolName: "bash",
			Args: json.RawMessage(`{"command":"pwd"}`),
		})
	}()
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		answered := false
		for _, g := range hub.Snapshot().Live.PendingGates {
			if g.Origin == "" {
				if err := gate.Decide(g.GateID, Verdict{Allow: true}); err == nil {
					answered = true
				}
			}
		}
		if answered {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case d := <-mainDone:
		if !d.Allow {
			t.Errorf("the main lane's approved call came back denied: %+v", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the main lane waited behind a subagent's unanswered card")
	}
	// Let the abandoned card go home rather than time the test out on it.
	_ = gate.Decide(subGateID, Verdict{Allow: false, Reason: "test over"})
	<-subDone
}

// The envelope's Task line is the rail preview's brief, clipped at 160 bytes.
// CJK text is 3 bytes a character, so the cut lands mid-rune more often than
// not, and the stranded half arrives in the browser as U+FFFD — the garbled
// hover preview. Every cut length must yield whole runes.
func TestClipBgNeverSplitsARune(t *testing.T) {
	task := strings.Repeat("用 mal_similar 工具查询样本的相似样本。", 20)
	for n := 1; n <= len(task); n++ {
		got := clipBg(task, n)
		if !utf8.ValidString(got) || strings.ContainsRune(got, 0xFFFD) {
			t.Fatalf("clipBg(task, %d) = %q: a rune was split", n, got)
		}
	}
}
