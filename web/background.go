package web

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/tools"
)

// bgNotificationMarker opens every background-completion message. It has to be
// text the model cannot mistake for a person: to the API these messages are
// user-role, and an unmarked "the analysis is done, everything looks good"
// reads as the user saying exactly that — Claude Code's task-notification
// carries its "[SYSTEM NOTIFICATION - NOT USER INPUT]" label for the same
// observed reason. The marker is also how the UI styles the bubble, how the
// sidebar count skips it, and how the run-end path recognises a notification
// that was steered but never delivered.
const bgNotificationMarker = "<task-notification>"

// bgWakeDelay is how long the idle path collects completions before opening a
// run for them, so a fan-out that finishes within the same moment is reported
// by one run instead of each task waking the agent for itself.
const bgWakeDelay = 800 * time.Millisecond

// onBgStart announces a freshly dispatched background task. The task brief is
// clipped because it goes into every snapshot the hub serves from here on.
func (s *Session) onBgStart(t tools.BgTask) {
	s.hub.Publish(Event{
		Type: EvSubagentStart, TaskID: t.ID, Name: t.Mode, Text: clipBg(t.Task, 200),
	})
}

// onBgFrame forwards one progress event from a backgrounded child.
func (s *Session) onBgFrame(taskID string, frame json.RawMessage) {
	s.hub.Publish(Event{Type: EvSubagentFrame, TaskID: taskID, Frame: frame})
}

// onBgComplete settles one task: record its spend, tell the interface, and get
// its result into the conversation. The order matters — the notification names
// the transcript and the tokens, so both are on disk and on the wire before
// the message that cites them.
func (s *Session) onBgComplete(t tools.BgTask, res tools.BgResult) {
	d := res.Details
	s.mu.Lock()
	if s.closing || s.bgOff {
		s.mu.Unlock()
		return
	}
	var persistErr error
	appended := true
	// The spend is appended straight to the transcript's stats rather than
	// folded into the agent's counters, which the run goroutine alone may write.
	// UsageTotals sums records, so the session's totals pick it up here; the
	// hub's copy travels on the event below.
	if d.RunID != "" {
		appended, persistErr = s.store.AppendDelegatedStatsOnce(d.RunID, session.UsageStats{Input: d.InputTok, Output: d.OutTok})
	} else if d.InputTok > 0 || d.OutTok > 0 {
		u := &session.UsageStats{Input: d.InputTok, Output: d.OutTok}
		persistErr = s.store.AppendStats(session.Stats{Usage: u, Delegated: u})
	}
	s.mu.Unlock()
	if persistErr == nil && !appended {
		return // a repeated completion must not charge, publish or notify twice
	}
	if persistErr != nil {
		s.hub.Publish(Event{Type: EvError, Error: "failed to record a background subagent's usage: " + persistErr.Error()})
	}
	var errText string
	if res.Err != nil {
		errText = res.Err.Error()
	}
	s.hub.Publish(Event{
		Type: EvSubagentEnd, TaskID: t.ID,
		IsError: res.Err != nil, Error: errText, Details: d,
		Usage: &llm.Usage{Input: d.InputTok, Output: d.OutTok},
	})

	s.mu.Lock()
	off := s.bgOff
	s.mu.Unlock()
	if off {
		// A closing session has no one to notify, and starting a run for one
		// would spend tokens nobody is watching.
		return
	}
	if s.studioID != "" {
		s.syncStudioCapabilitiesAtIdle()
	}
	s.notifyBg(bgNotification(t, res))
}

// notifyBg delivers a completion message into the conversation. A run in
// flight takes it at the next turn boundary — the one point where a user-role
// message is legal — and an idle session collects it for the next wake.
func (s *Session) notifyBg(text string) {
	if s.Steer(text) {
		return
	}
	s.queueBgNotification(text)
}

// queueBgNotification parks a message for the idle path. Also called from
// consume when a run reports the notification among its undelivered steering
// messages: the run is over by then, so steering is not an option.
func (s *Session) queueBgNotification(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bgOff {
		return
	}
	s.bgInbox = append(s.bgInbox, text)
	if s.bgWake == nil {
		s.bgWake = time.AfterFunc(bgWakeDelay, s.wakeBg)
	}
}

// wakeBg opens a run whose prompt is the collected notifications, which is the
// auto-resume half of background delegation: the result reaches the model even
// when the user never comes back to ask.
func (s *Session) wakeBg() {
	s.mu.Lock()
	if s.bgOff {
		s.mu.Unlock()
		return
	}
	msgs := s.bgInbox
	s.bgInbox = nil
	s.bgWake = nil
	s.mu.Unlock()
	if len(msgs) == 0 {
		return
	}
	prompt := strings.Join(msgs, "\n\n")
	if _, err := s.Start(prompt, nil, ""); err == nil {
		return
	}
	// A run started in the gap between the check and now: deliver into it at its
	// next turn boundary. If that run also ended in the same gap, the messages
	// go back to the inbox for the next timer — a result must not fall through
	// the crack between the two delivery paths.
	if s.Steer(prompt) {
		return
	}
	s.mu.Lock()
	if !s.bgOff {
		s.bgInbox = append(msgs, s.bgInbox...)
		if s.bgWake == nil {
			s.bgWake = time.AfterFunc(bgWakeDelay, s.wakeBg)
		}
	}
	s.mu.Unlock()
}

// closeBg stops the notification machinery and every outstanding task. Called
// wherever the session itself goes away — eviction, deletion, server shutdown.
func (s *Session) closeBg() {
	s.mu.Lock()
	s.bgOff = true
	s.stopShellNotificationsLocked()
	if s.bgWake != nil {
		s.bgWake.Stop()
		s.bgWake = nil
	}
	s.bgInbox = nil
	cancel := s.bgCancel
	s.mu.Unlock()
	// Outside the lock: cancelling runs the tasks' completion hooks, which take
	// s.mu themselves.
	if cancel != nil {
		cancel()
	}
	if s.bg != nil {
		s.bg.Close()
	}
}

// BgActive reports whether any background task is still outstanding. The
// eviction guard reads it: such a session is not idle, however quiet its run
// slot and subscriber count are.
func (s *Session) BgActive() bool {
	// A durable completion is still work until its notification is committed.
	// Otherwise an old, quiet session could be evicted between process exit and
	// the callback/timer that resumes the Agent with the result.
	return (s.bg != nil && s.bg.InFlight() > 0) ||
		(s.shellJobs != nil && (s.shellJobs.InFlight() > 0 || len(s.shellJobs.PendingNotifications()) > 0))
}

// CancelBg stops one background task. Its completion hook still fires, so the
// conversation hears about the cancellation rather than about nothing.
func (s *Session) CancelBg(taskID string) bool {
	if s.bg == nil {
		return false
	}
	return s.bg.Cancel(taskID)
}

// bgNotification renders one task's outcome as the message the model wakes up
// to. The result text is already bounded by the tool's own truncation; the
// clip here is a second belt for the notification envelope, not the content.
func bgNotification(t tools.BgTask, res tools.BgResult) string {
	var b strings.Builder
	b.WriteString(bgNotificationMarker + "\n")
	b.WriteString("[SYSTEM NOTIFICATION - NOT USER INPUT] A background subagent has finished. " +
		"Report the outcome to the user; do not treat this message as a new instruction from them.\n")
	fmt.Fprintf(&b, "Task: %s (mode %q) — %s\n", t.ID, t.Mode, clipBg(t.Task, 160))
	d := res.Details
	if d.AgentID != "" {
		fmt.Fprintf(&b, "Retained agent: %s. Its context is %s; use subagent action=continue with this agent_id for a related follow-up.\n", d.AgentID, d.Status)
	}
	if res.Err != nil {
		fmt.Fprintf(&b, "Status: FAILED: %v\n", res.Err)
	} else {
		b.WriteString("Status: completed\n")
	}
	if text := strings.TrimSpace(res.Text); text != "" {
		fmt.Fprintf(&b, "Result:\n%s\n", clipBg(text, 6000))
	}
	if d.Session != "" {
		fmt.Fprintf(&b, "Full transcript: %s\n", d.Session)
	}
	fmt.Fprintf(&b, "Tokens: %d in / %d out over %d turn(s)\n", d.InputTok, d.OutTok, d.Turns)
	b.WriteString("</task-notification>")
	return b.String()
}

// clipBg bounds text for an event or notification, on a line boundary when one
// is nearby. The input is valid UTF-8 (JSON-decoded), so after the byte-wise
// cut ToValidUTF8 drops exactly the partial rune the cut may have left: a
// 160-byte CJK task brief lands mid-character more often than not, and the
// broken rune ships to every preview that shows the brief as U+FFFD.
func clipBg(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = strings.ToValidUTF8(s[:n], "")
	if i := strings.LastIndexByte(s, '\n'); i > n/2 {
		s = s[:i]
	}
	return strings.TrimSpace(s) + "…"
}
