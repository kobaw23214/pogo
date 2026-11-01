package web

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

// subBuffer is how far one subscriber may fall behind before it is dropped.
//
// Dropping is safe because of the log: the client notices the stream ended,
// reconnects with ?from=<lastSeq>, and is caught up from there. That turns the
// classic slow-consumer problem into something that needs no policy at all.
const subBuffer = 256

// maxLog bounds the replay log. Overflowing it costs a reconnecting client the
// incremental path, not its data: it falls back to a snapshot, which is rebuilt
// from state that is already correct.
const maxLog = 2000

// maxPendingOutput bounds the live output kept per running call.
//
// It has to be bounded here as well as at the source: the tool stops forwarding
// after its own budget, but this copy goes into every snapshot, so an unbounded
// one would make reconnecting more expensive the longer a command has been
// running.
const maxPendingOutput = 32 << 10

// maxPendingFrames bounds the structured progress events kept per running call.
//
// A count, not a byte budget: these are whole events, so dropping the oldest costs
// one line of history while cutting bytes would hand a client half a frame. Sized
// for what a delegated run actually produces — a few events per turn, and a
// subagent that has produced hundreds is looping, in which case the recent ones are
// the interesting ones anyway.
const maxPendingFrames = 400

// maxBgTaskHistory bounds how many background tasks the hub keeps for cards and
// snapshots. Running tasks are never dropped; among finished ones the oldest go
// first.
const maxBgTaskHistory = 32

// maxIncomingHead and maxIncomingTail bound the raw argument preview kept per
// incoming call: the head is where the path field lives, the tail is the line
// preview. Like maxPendingOutput this copy goes into every snapshot, so it must
// not grow with how much the model streams.
const (
	maxIncomingHead = 4096
	maxIncomingTail = 8192
)

// tailOf keeps the last n bytes, cutting on a line boundary when there is one
// nearby so the first visible line is not half a line.
func tailOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	// The byte window can open inside a multi-byte rune; drop the stranded
	// leading bytes so the tail's first character is whole, not a U+FFFD.
	s = strings.ToValidUTF8(s, "")
	if i := strings.IndexByte(s, '\n'); i >= 0 && i < 200 {
		s = s[i+1:]
	}
	return s
}

// appendLiveFrame appends one progress frame to a bounded ring, with one
// special case: a coalesced tail frame (a subagent's live thinking or answer,
// each frame carrying the accumulated tail rather than a delta) replaces a
// trailing frame of the same kind instead of taking a new slot. The rings feed
// every snapshot, so a two-minute thinking burst must not cost two hundred
// slots of near-identical text — it costs one, and the tool and turn frames
// around it stay in the window a reconnect actually wants.
func appendLiveFrame(f []json.RawMessage, frame json.RawMessage) []json.RawMessage {
	if kind := liveFrameKind(frame); kind != "" {
		if n := len(f); n > 0 && liveFrameKind(f[n-1]) == kind {
			f[n-1] = frame
			return f
		}
	}
	f = append(f, frame)
	if len(f) > maxPendingFrames {
		// Copied rather than resliced: keeping the tail of the old array would
		// pin the whole backing store for the life of the call.
		f = append([]json.RawMessage(nil), f[len(f)-maxPendingFrames:]...)
	}
	return f
}

// liveFrameKind reports the frame type when it is one of the coalesced kinds,
// "" otherwise. Parsed shallowly and defensively: frames arrive from child
// processes, and a malformed one must degrade to an ordinary append.
func liveFrameKind(frame json.RawMessage) string {
	var e struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(frame, &e) != nil {
		return ""
	}
	switch e.Type {
	case "thinking", "token":
		return e.Type
	}
	return ""
}

// countContentNewlines counts the `\n` escape pairs of a raw JSON-arguments
// fragment — the newlines of the file being written. A `\\` pair (an escaped
// backslash) makes the following n literal text, not a newline, so escapes
// are tracked properly; carry holds a fragment's trailing lone backslash for
// the next fragment, because `\n` can be split across chunks.
func countContentNewlines(carry *bool, text string) int {
	lines := 0
	i := 0
	if *carry {
		if len(text) == 0 {
			return 0
		}
		if text[0] == 'n' {
			lines++
		}
		*carry = false
		i = 1
	}
	for ; i < len(text); i++ {
		if text[i] != '\\' {
			continue
		}
		if i+1 >= len(text) {
			*carry = true
			break
		}
		if text[i+1] == 'n' {
			lines++
		}
		i++ // the pair is consumed whatever it escapes
	}
	return lines
}

// Hub is one session's event log, live state, and fan-out.
//
// Everything a client can observe is derived here by folding published events,
// so a snapshot and a live stream cannot disagree: they are the same state
// machine read at different times.
type Hub struct {
	mu         sync.Mutex
	instanceID string
	seq        int64
	log        []Event
	subs       map[int64]chan Event
	nextSub    int64
	closed     bool

	msgs    []Message
	results map[string]ToolResult
	live    Live
	usage   llm.Usage
	// bgTasks and bgUsage are the session-scoped state of backgrounded
	// delegations. They sit outside live precisely because live is wiped at every
	// run's end, and outside usage because the run-end event overwrites usage
	// with the agent's counters, which background spend never enters — the
	// snapshot's total is the sum of the two.
	bgTasks    map[string]*BgTaskLive
	shellTasks map[string]tools.ShellJobRecord
	bgUsage    llm.Usage
	policy     PolicyState
	run        RunInfo
	// context is the latest turn's prompt size, which is what fills the window.
	context int64
	// A model change retains the last count only as an approximation until its
	// own provider reports a prompt size. Snapshots must preserve that distinction.
	contextEstimated bool
	// overhead is the fixed per-request cost (system prompt + tool schemas),
	// and overheadParts itemizes it (agent.OverheadBreakdown). Updated at
	// session creation, history replacement, catalog or Skill refresh and each
	// model turn as the tool view changes. They feed the context meter's
	// breakdown display and nothing else.
	overhead      int64
	overheadParts []agent.OverheadPart
	// streamedOverhead is the last figure an event carried to subscribers
	// (overheadStreamed once there is one), which is what decides whether a
	// turn_start has to carry the current one.
	streamedOverhead      int64
	streamedOverheadParts []agent.OverheadPart
	overheadStreamed      bool
	// clearTrigger is the prompt size at which context clearing starts, or 0 when it
	// is off. Sent to clients because the gauge's warning bands are meaningless
	// without it: clearing holds occupancy just below this, so fixed percentages of
	// the window would leave the gauge permanently amber.
	clearTrigger int64
	// followups are the latest generated suggestion chips, keyed to the assistant
	// message they build on. Session-scoped rather than run-scoped: the generating
	// call lands after the run that prompted it, so Live — cleared at every run's
	// end — could not hold them.
	followups          *Followups
	inbox              []InboxItem
	resolvedUserInputs map[string]tools.UserInputDetails
	answerReview       *llm.AnswerReviewStatus

	msgSeq int
}

func NewHub() *Hub {
	return &Hub{
		instanceID:         rand.Text(),
		subs:               make(map[int64]chan Event),
		results:            make(map[string]ToolResult),
		bgTasks:            make(map[string]*BgTaskLive),
		shellTasks:         make(map[string]tools.ShellJobRecord),
		resolvedUserInputs: make(map[string]tools.UserInputDetails),
		policy:             PolicyState{Mode: string(ModeStandard)},
	}
}

// Publish assigns a sequence number, folds the event into the session state, and
// fans it out. It is the only writer of Hub state.
func (h *Hub) Publish(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}

	h.seq++
	e.Seq = h.seq
	if e.TS == 0 {
		e.TS = nowMS()
	}
	h.apply(&e)

	if logged(e.Type) {
		h.log = append(h.log, e)
		if len(h.log) > maxLog {
			h.log = h.log[len(h.log)-maxLog:]
		}
	}

	for id, ch := range h.subs {
		select {
		case ch <- e:
		default:
			// Too far behind. Close and forget it; it will come back with ?from=.
			close(ch)
			delete(h.subs, id)
		}
	}
}

// apply folds one event into the derived state, and fills in the fields the Hub
// owns rather than the publisher (message ids).
func (h *Hub) apply(e *Event) {
	switch e.Type {
	case EvInboxUpdated:
		if e.Item != nil {
			updated := false
			for i := range h.inbox {
				if h.inbox[i].ID == e.Item.ID {
					h.inbox[i] = *e.Item
					updated = true
					break
				}
			}
			if !updated {
				h.inbox = append(h.inbox, *e.Item)
			}
		}
	case EvShellTaskStart, EvShellTaskOutput, EvShellTaskEnd:
		if e.ShellTask != nil {
			h.foldShellTaskLocked(*e.ShellTask)
		}
	case EvRunStart:
		h.answerReview = nil
		// Background approvals belong to the session, not the main run that
		// happened to be active when the child asked. Keep their visible handles
		// until gate_resolved, including across a new question in this session.
		var gates []PendingGate
		for _, gate := range h.live.PendingGates {
			if gate.Origin != "" {
				gates = append(gates, gate)
			}
		}
		h.live = Live{RunID: e.RunID, Active: true, PendingGates: gates}
		h.run.Active = true
		h.run.RunID = e.RunID
		h.run.EndReason, h.run.Error = "", ""
		if e.Model != "" {
			h.run.Model, h.run.Provider = e.Model, e.Provider
		}

	case EvUserMessage:
		h.msgSeq++
		e.MessageID = fmt.Sprintf("u%d", h.msgSeq)
		e.Role = string(llm.RoleUser)
		// Content carries the full first message — image blocks included —
		// whenever the turn had attachments; text-only turns keep the compact
		// form they always had.
		content := e.Content
		if content == nil {
			content = []llm.Block{{Type: llm.BlockText, Text: e.Text}}
		}
		h.msgs = append(h.msgs, Message{
			ID:      e.MessageID,
			Role:    string(llm.RoleUser),
			Content: content,
			TS:      e.TS,
			InboxID: e.InboxID, RunID: e.RunID, Steering: e.Steering, InputLayout: e.InputLayout,
		})

	case EvImageDescribeEnd:
		// The settled description is the image block's alt from here on: the
		// persister writes it into the transcript, and the fold patches it into
		// the user message the same way, so a snapshot and the live stream show
		// the describe step identically after a refresh. A failed describe has
		// no text to settle — the fallback note lives only in the transcript.
		// Copy-on-write: the message's Content slice is shared with the
		// user_message event already fanned out, and a slow subscriber may
		// still be marshaling it.
		if e.Text == "" {
			break
		}
		for i := range h.msgs {
			if h.msgs[i].Role != string(llm.RoleUser) {
				continue
			}
			var patched []llm.Block
			for j, b := range h.msgs[i].Content {
				if b.Type == llm.BlockImage && b.ImageRef == e.ImageRef && b.ImageAlt != e.Text {
					if patched == nil {
						patched = append([]llm.Block(nil), h.msgs[i].Content...)
					}
					patched[j].ImageAlt = e.Text
				}
			}
			if patched != nil {
				h.msgs[i].Content = patched
			}
		}

	case EvTurnStart:
		// One turn produces exactly one assistant message, so this is where its
		// id is minted. Deltas inherit it, which is how the browser knows which
		// bubble to append to.
		h.msgSeq++
		h.live.Turn = e.Turn
		h.live.MessageID = fmt.Sprintf("m%d", h.msgSeq)
		h.live.Text, h.live.Thinking = "", ""
		e.MessageID = h.live.MessageID
		// The fixed cost rides along only when subscribers may not have the
		// current figure (OverheadUnstreamed).
		if e.OverheadTokens != nil {
			h.foldOverheadLocked(*e.OverheadTokens, e.OverheadBreakdown)
		}

	case EvToken:
		e.MessageID = h.live.MessageID
		h.live.Text += e.Text

	case EvThinking:
		e.MessageID = h.live.MessageID
		h.live.Thinking += e.Text

	case EvAnswerReview:
		if e.AnswerReview != nil {
			e.AnswerReview = e.AnswerReview.Clone()
			if e.AnswerReview.MessageID == "" {
				e.AnswerReview.MessageID = h.live.MessageID
			}
			e.MessageID = e.AnswerReview.MessageID
			h.answerReview = e.AnswerReview.Clone()
		}

	case EvMessage:
		// A cancelled or empty provider response can settle without blocks or
		// a role. Keep the UI's settled-message shape stable; SSE may omit the
		// empty content field, but snapshots always carry an array.
		if e.Content == nil {
			e.Content = []llm.Block{}
		}
		if e.Role == "" {
			e.Role = string(llm.RoleAssistant)
		}
		// Input is prompt_tokens, which already includes the cached prefix
		// (cached_tokens is a subset of it in this protocol), so it is the whole
		// context size on its own.
		if e.Usage != nil && e.Usage.Input > 0 {
			h.context = e.Usage.Input
			h.contextEstimated = false
		}
		if h.live.MessageID == "" {
			h.msgSeq++
			h.live.MessageID = fmt.Sprintf("m%d", h.msgSeq)
		}
		e.MessageID = h.live.MessageID
		if e.AnswerReview != nil {
			e.AnswerReview = e.AnswerReview.Clone()
			e.AnswerReview.MessageID = e.MessageID
			h.answerReview = e.AnswerReview.Clone()
		}
		h.msgs = append(h.msgs, Message{
			ID: e.MessageID, Role: e.Role, Content: e.Content, TS: e.TS,
			AnswerReview: e.AnswerReview.Clone(),
		})
		// The settled message supersedes the accumulated text, so the live copy
		// is dropped rather than left to be rendered twice.
		h.live.Text, h.live.Thinking = "", ""

	case EvToolStart:
		// The arguments are settled and the pending-tool card takes over, so the
		// incoming preview goes away — otherwise the same call would render twice.
		h.live.Incoming = dropIncoming(h.live.Incoming, e.CallID)
		h.live.PendingTools = append(h.live.PendingTools, PendingTool{
			CallID: e.CallID, Name: e.Name, Args: e.Args, StartedAt: e.TS,
		})

	case EvToolArgs:
		// Folded into the incoming list rather than logged, so a client that
		// reconnects mid-generation still sees the preview. Entries are ordered
		// by arrival: the first event for a call creates its entry.
		i := -1
		for j := range h.live.Incoming {
			if h.live.Incoming[j].CallID == e.CallID {
				i = j
				break
			}
		}
		if i < 0 {
			h.live.Incoming = append(h.live.Incoming, IncomingCall{CallID: e.CallID, TS: e.TS})
			i = len(h.live.Incoming) - 1
		}
		inc := &h.live.Incoming[i]
		// The name-bearing event normally arrives first; this covers a provider
		// that names the call only after some arguments.
		if inc.Name == "" {
			inc.Name = e.Name
		}
		inc.Bytes += len(e.Text)
		inc.Lines += countContentNewlines(&inc.escPending, e.Text)
		if len(inc.Head) < maxIncomingHead {
			// The head is display-only, never parsed — but a byte cut can split
			// a multi-byte rune, and the partial halves would show as U+FFFD in
			// the live preview. ToValidUTF8 drops them (the stream is valid
			// UTF-8, so invalid bytes exist only at the cut). If a rune was
			// straddling this event's boundary, its continuation bytes in the
			// next event arrive as invalid after a valid tail and get dropped
			// the same way.
			inc.Head = strings.ToValidUTF8(
				inc.Head+e.Text[:min(maxIncomingHead-len(inc.Head), len(e.Text))], "")
		}
		inc.Tail = tailOf(inc.Tail+e.Text, maxIncomingTail)

	case EvToolPartial:
		// Folded into the pending call rather than logged, so a client that
		// reconnects mid-command still sees the output so far. Keeping the tail
		// matches what the settled result will contain: the last lines are the ones
		// that say how it is going.
		for i := range h.live.PendingTools {
			if h.live.PendingTools[i].CallID != e.CallID {
				continue
			}
			h.live.PendingTools[i].Output = tailOf(h.live.PendingTools[i].Output+e.Text, maxPendingOutput)
			if len(e.Frame) > 0 {
				h.live.PendingTools[i].Frames = appendLiveFrame(h.live.PendingTools[i].Frames, e.Frame)
			}
		}

	case EvToolEnd:
		// Defensive for the incoming preview: every settled call passed through
		// tool_start, which already dropped it, but a stray entry must not linger.
		h.live.Incoming = dropIncoming(h.live.Incoming, e.CallID)
		// The duration is read off the pending entry before it goes: tool_start's
		// stamp is the only record of when the call began.
		var dur int64
		for _, p := range h.live.PendingTools {
			if p.CallID == e.CallID && p.StartedAt > 0 && e.TS > p.StartedAt {
				dur = e.TS - p.StartedAt
			}
		}
		h.live.PendingTools = dropTool(h.live.PendingTools, e.CallID)
		e.DurationMS = dur
		h.results[e.CallID] = ToolResult{
			CallID: e.CallID, Name: e.Name, Text: e.Text,
			IsError: e.IsError, Details: e.Details, Source: e.Source, DurationMS: dur,
		}

	case EvGateRequest:
		h.live.PendingGates = append(h.live.PendingGates, PendingGate{
			GateID: e.GateID, CallID: e.CallID, Tool: e.Name,
			Args: e.Args, Deadline: e.Deadline, Danger: e.Danger, Origin: e.Origin,
		})

	case EvGateDeadline:
		for i := range h.live.PendingGates {
			if h.live.PendingGates[i].GateID == e.GateID {
				h.live.PendingGates[i].Deadline = e.Deadline
				if e.Frozen != nil {
					h.live.PendingGates[i].Frozen = *e.Frozen
					h.live.PendingGates[i].RemainingMS = e.RemainingMS
				}
			}
		}

	case EvGateResolved:
		h.live.PendingGates = dropGate(h.live.PendingGates, e.GateID)

	case EvUserInputRequest:
		if e.UserInput != nil {
			h.live.PendingUserInputs = append(dropUserInput(h.live.PendingUserInputs, e.UserInput.RequestID), *clonePendingUserInputPtr(*e.UserInput))
		}

	case EvUserInputResolved:
		if e.Resolution != nil {
			if e.Resolution.Async && e.Resolution.CallID != "" {
				questions := []tools.UserInputQuestion(nil)
				for _, request := range h.live.PendingUserInputs {
					if request.RequestID == e.Resolution.RequestID {
						questions = request.Questions
					}
				}
				if previous, ok := h.resolvedUserInputs[e.Resolution.CallID]; ok && len(questions) == 0 {
					questions = previous.Questions
				}
				h.resolvedUserInputs[e.Resolution.CallID] = cloneUserInputDetails(tools.UserInputDetails{
					RequestID: e.Resolution.RequestID, CallID: e.Resolution.CallID, Status: e.Resolution.Status, Async: true,
					Questions: questions, Answers: e.Resolution.Answers,
				})
			}
			h.live.PendingUserInputs = dropUserInput(h.live.PendingUserInputs, e.Resolution.RequestID)
		}

	case EvPolicyChanged, EvPolicyReverted:
		if e.Policy != nil {
			h.policy = *e.Policy
		}

	case EvModelChanged:
		if h.run.Model != e.Model || h.run.Provider != e.Provider {
			h.contextEstimated = h.context > 0
		}
		h.run.Model, h.run.Provider = e.Model, e.Provider
		h.run.ContextWindow = e.ContextWindow
		h.run.ModelConfigPending = false
		if e.ClearTrigger != nil {
			h.clearTrigger = *e.ClearTrigger
		}
		if e.OverheadTokens != nil {
			h.foldOverheadLocked(*e.OverheadTokens, e.OverheadBreakdown)
		}

	case EvModelConfigPending:
		h.run.ModelConfigPending = e.ModelConfigPending

	case EvRunEnd:
		if h.answerReview != nil && h.answerReview.State == "running" {
			status := h.answerReview.Clone()
			status.State, status.Reason = "fallback", "interrupted"
			if e.StopReason == "aborted" || e.EndReason == "aborted" {
				status.State, status.Reason = "cancelled", "cancelled"
			}
			status.FinishedAt = e.TS
			status.DurationMS = max(int64(0), e.TS-status.StartedAt)
			h.answerReview = status
		}
		if e.Usage != nil {
			h.usage = *e.Usage
			// The agent's counters never see background spend — it is recorded
			// when the task completes, mid-run or long after — so the total the
			// stream ends on would quietly drop it. Publish the same sum the
			// snapshot serves. Mutating the event here reaches the fan-out, the
			// same way tool_end's duration does.
			sum := *e.Usage
			sum.Input += h.bgUsage.Input
			sum.Output += h.bgUsage.Output
			sum.CacheRead += h.bgUsage.CacheRead
			sum.Reasoning += h.bgUsage.Reasoning
			e.Usage = &sum
		}
		// A subagent's pending approval survives the run's end: a background task
		// asks whenever it asks, and clearing its card here would leave the
		// reviewer nothing to answer while the child waits out its timeout.
		var gates []PendingGate
		for _, g := range h.live.PendingGates {
			if g.Origin != "" {
				gates = append(gates, g)
			}
		}
		var inputs []PendingUserInput
		for _, input := range h.live.PendingUserInputs {
			if input.Async {
				inputs = append(inputs, input)
			}
		}
		h.live = Live{PendingGates: gates, PendingUserInputs: inputs}
		h.run.Active = false
		h.run.RunID = ""
		h.run.EndReason, h.run.Error = e.EndReason, e.Error
		if h.run.EndReason == "" {
			h.run.EndReason = e.StopReason
		}

	case EvFollowups:
		// A generation that came back empty is not published at all, so an event
		// that arrives always carries chips — the state moves wholesale rather
		// than merging with what a previous run suggested.
		if len(e.Chips) > 0 {
			h.followups = &Followups{
				MessageID: e.MessageID,
				Chips:     append([]skills.StarterCard(nil), e.Chips...),
			}
		}

	case EvSubagentStart:
		h.bgTasks[e.TaskID] = &BgTaskLive{
			TaskID: e.TaskID, Mode: e.Name, Task: e.Text,
			Status: "running", StartedAt: e.TS,
		}

	case EvSubagentFrame:
		// Bounded the same way as a pending tool's frames: this copy goes into
		// every snapshot, and a task that has produced hundreds is looping, in
		// which case the recent ones are the interesting ones anyway.
		if t, ok := h.bgTasks[e.TaskID]; ok && len(e.Frame) > 0 {
			t.Frames = appendLiveFrame(t.Frames, e.Frame)
		}

	case EvSubagentEnd:
		if t, ok := h.bgTasks[e.TaskID]; ok {
			t.Status = "done"
			if e.IsError {
				t.Status = "failed"
			}
			t.Error = e.Error
			t.Details = e.Details
			// The frames go out with the end: they are never logged, so a client
			// that reconnects past them has no other way to see the whole run.
			// Same mutate-before-fanout trick as DurationMS, one line down.
			e.Frames = t.Frames
			if e.TS > t.StartedAt {
				t.DurationMS = e.TS - t.StartedAt
				// Same trick as tool_end: the duration is computed from state, and
				// mutating the event here carries it to the fan-out.
				e.DurationMS = t.DurationMS
			}
		}
		if e.Usage != nil {
			h.bgUsage.Input += e.Usage.Input
			h.bgUsage.Output += e.Usage.Output
			h.bgUsage.CacheRead += e.Usage.CacheRead
			h.bgUsage.Reasoning += e.Usage.Reasoning
		}
		// Finished tasks accumulate without bound otherwise: a session that
		// delegates all day would grow every snapshot by a task per delegation.
		for len(h.bgTasks) > maxBgTaskHistory {
			oldest, oldestTS := "", int64(0)
			for id, t := range h.bgTasks {
				if t.Status == "running" {
					continue
				}
				if oldest == "" || t.StartedAt < oldestTS {
					oldest, oldestTS = id, t.StartedAt
				}
			}
			if oldest == "" {
				break // everything is running; the cap yields to reality
			}
			delete(h.bgTasks, oldest)
		}
	}
}

// Seed loads a session's persisted history into the derived state. It must be
// called before the first run, while nothing else can be publishing.
//
// tool_result blocks are lifted out of their user messages into the results
// table so that a resumed session has exactly the shape a live one does. Their
// tool name is recovered from the assistant message that requested them, since
// the result block itself does not carry it.
func (h *Hub) Seed(history []session.Timed) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seedLocked(history)
}

func (h *Hub) seedLocked(history []session.Timed) {
	names := make(map[string]string)
	for _, m := range history {
		var kept []llm.Block
		for _, b := range m.Message.Content {
			switch b.Type {
			case llm.BlockToolUse:
				names[b.ID] = b.Name
				kept = append(kept, b)
			case llm.BlockToolResult:
				h.results[b.ToolUseID] = ToolResult{
					CallID: b.ToolUseID, Name: names[b.ToolUseID],
					// The persisted text carries the citation header the model
					// read; the card shows the raw output, the same shape the
					// live stream's ToolOutput has.
					Text: agent.StripCiteHeader(b.Text), IsError: b.IsError,
					// Passed through as raw JSON rather than decoded into a
					// concrete type: the Hub has no reason to know which tool
					// produced which shape, and json.RawMessage re-marshals
					// verbatim. This is what makes a reloaded diff identical to a
					// live one instead of missing.
					Details: detailsOrNil(b.Details),
					Source:  sourceOrNil(b.Source),
					// The duration persisted on the block, so a reloaded result
					// says how long it took just like a live one.
					DurationMS: b.DurationMS,
				}
			default:
				kept = append(kept, b)
			}
		}
		if len(kept) == 0 {
			continue
		}
		h.msgSeq++
		prefix := "m"
		if m.Message.Role == llm.RoleUser {
			prefix = "u"
		}
		h.msgs = append(h.msgs, Message{
			ID: fmt.Sprintf("%s%d", prefix, h.msgSeq), Role: string(m.Message.Role), Content: kept,
			TS: m.Time, InboxID: m.Message.InboxID, RunID: m.Message.RunID, Steering: m.Message.Steering, InputLayout: m.Message.InputLayout,
			AnswerReview: m.Message.AnswerReview.Clone(),
		})
		if m.Message.Role == llm.RoleUser {
			h.answerReview = nil
		}
		if review := m.Message.AnswerReview.Clone(); review != nil {
			review.MessageID = fmt.Sprintf("%s%d", prefix, h.msgSeq)
			h.msgs[len(h.msgs)-1].AnswerReview = review.Clone()
			h.answerReview = review
		}
	}
}

func (h *Hub) setInbox(items []InboxItem) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.inbox = append([]InboxItem{}, items...)
}

func (h *Hub) inboxMessageID(id string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, m := range h.msgs {
		if m.InboxID == id {
			return m.ID
		}
	}
	return ""
}

func (h *Hub) restoreUserInputAnswers(details tools.UserInputDetails) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if details.CallID == "" {
		return
	}
	h.resolvedUserInputs[details.CallID] = cloneUserInputDetails(details)
}

// Reset rebuilds the derived state from a rewound branch. Everything the
// timeline shows is replaced: messages, results, live state, and the context
// occupancy. The replay log goes with them — its events describe the
// abandoned branch, so a reconnecting client must fall back to a snapshot
// (canReplay now fails), which carries exactly this state. Subscriptions
// survive: they belong to the session, not to the branch. The sequence
// counter keeps climbing, since SSE resume semantics depend on it never
// going backwards.
func (h *Hub) Reset(history []session.Timed, usage llm.Usage) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.msgs = nil
	h.results = make(map[string]ToolResult)
	h.resolvedUserInputs = make(map[string]tools.UserInputDetails)
	h.live = Live{}
	h.run.EndReason, h.run.Error = "", ""
	h.context = 0
	h.contextEstimated = false
	h.usage = usage
	// Chips reference a message on the branch being abandoned, so they go with
	// it; the next run generates a fresh set against history that still exists.
	h.followups = nil
	h.answerReview = nil
	// The usage argument is read back from the transcript's stats records, which
	// already include every background completion appended straight to the file —
	// so the background increment starts over rather than doubling them.
	// bgTasks themselves survive: a rewind abandons a conversation branch, and
	// the tasks were never part of it.
	h.bgUsage = llm.Usage{}
	h.msgSeq = 0
	h.log = nil
	h.seedLocked(history)
}

// UserMessageOrdinal reports which visible user message id is, counting only
// user messages and starting at 1. The store counts records by the same rule
// (session.Store.RewindPoint), which is what lets a rewind line the two up
// without either side knowing the other's ids.
func (h *Hub) UserMessageOrdinal(id string) (int, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, m := range h.msgs {
		if m.Role != string(llm.RoleUser) {
			continue
		}
		n++
		if m.ID == id {
			return n, true
		}
	}
	return 0, false
}

// LastAssistantID reports the timeline id of the most recent assistant message,
// which is the message a follow-up suggestion run builds on. The hub owns the
// numbering, so it is the only place that can answer this.
func (h *Hub) LastAssistantID() (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.msgs) - 1; i >= 0; i-- {
		if h.msgs[i].Role == string(llm.RoleAssistant) {
			return h.msgs[i].ID, true
		}
	}
	return "", false
}

// detailsOrNil keeps an absent payload absent. Handing an empty json.RawMessage
// to an `any` field would serialise as the invalid value “ rather than being
// omitted, which breaks the whole response and not just this one field.
func detailsOrNil(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

// sourceOrNil decodes a persisted citation stamp. Unlike Details it has one
// shape — agent.Source — so it is decoded rather than passed through raw: a
// hand-edited or older transcript's stamp must not crash a reload, it just
// fails to resolve.
func sourceOrNil(raw json.RawMessage) *agent.Source {
	if len(raw) == 0 {
		return nil
	}
	var src agent.Source
	if err := json.Unmarshal(raw, &src); err != nil {
		return nil
	}
	return &src
}

// SetRunInfo records the model a session will use, so a client that connects
// before the first run still knows what it is talking to.
func (h *Hub) SetRunInfo(model, provider string, contextWindow int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.run.Model, h.run.Provider, h.run.ContextWindow = model, provider, contextWindow
}

// FirstUserTitle returns the sidebar line of the session's first genuine user
// message — its text, or when the send carried only attachments, the first
// layer's names (session.AttachmentTitle, the same derivation summarise
// applies on disk). "" when there is none yet. The first prompt only reaches
// disk once its run settles it — which the describe step can delay by tens of
// seconds — so for that window the live fold is the only place the title
// exists.
func (h *Hub) FirstUserTitle() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, m := range h.msgs {
		if m.Role != string(llm.RoleUser) {
			continue
		}
		msg := llm.Message{Content: m.Content, InputLayout: m.InputLayout}
		text := msg.Text()
		// Harness notices wear the user role but are not sends; the
		// transcript's title derivation skips them too. Tool-result-only
		// messages need no guard here: no text and no attachments means
		// they fall through on their own.
		if session.IsHarnessNotice(text) {
			continue
		}
		if at := session.UserInputTitle(msg); at != "" {
			return at
		}
	}
	return ""
}

// SetOverhead records the fixed prompt overhead and its itemized parts, so a
// snapshot can split the context occupancy into overhead and conversation and
// say what the overhead is made of. Connected subscribers learn the change
// from their next snapshot, or from the next turn_start (OverheadUnstreamed).
func (h *Hub) SetOverhead(tokens int64, parts []agent.OverheadPart) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.overhead = tokens
	h.overheadParts = append([]agent.OverheadPart(nil), parts...)
}

// OverheadUnstreamed records the figure like SetOverhead and reports whether
// connected subscribers may not have it: it differs from the last figure an
// event carried. Session/catalog refreshes update the hub silently, so
// comparing against the hub's own state would miss them. The caller then
// attaches the figure to the turn_start it publishes, and folding that event
// marks it carried.
func (h *Hub) OverheadUnstreamed(tokens int64, parts []agent.OverheadPart) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.overhead = tokens
	h.overheadParts = append([]agent.OverheadPart(nil), parts...)
	return !h.overheadStreamed || h.streamedOverhead != tokens || !slices.Equal(h.streamedOverheadParts, parts)
}

// foldOverheadLocked applies a figure an event carries to the hub's state and
// to its record of what the stream has delivered. h.mu is held.
func (h *Hub) foldOverheadLocked(tokens int64, parts []agent.OverheadPart) {
	h.overhead = tokens
	h.overheadParts = append([]agent.OverheadPart(nil), parts...)
	h.streamedOverhead, h.overheadStreamed = tokens, true
	h.streamedOverheadParts = append([]agent.OverheadPart(nil), parts...)
}

// SetClearTrigger records the prompt size at which context clearing begins, so a
// snapshot can tell the gauge what to measure its warning bands against. Zero means
// clearing is off.
//
// The "auto" trigger is a fraction of the model's
// window, so switching models moves it. SetModel updates it for that reason.
func (h *Hub) SetClearTrigger(tokens int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clearTrigger = tokens
}

// SetUsage seeds the running cost total recovered from the transcript's stats
// records on reopen. Set once, before the first subscriber — same rule as
// SetOverhead.
func (h *Hub) SetUsage(u llm.Usage) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.usage = u
	// Same reasoning as Reset: the seeded total already contains everything
	// background tasks recorded, so the increment starts at zero.
	h.bgUsage = llm.Usage{}
}

// RecordSpend folds spend the agent's counters never saw into the total the
// snapshot serves: follow-up generation runs after its run is over, so the
// run's usage report cannot carry it. It joins the background bucket, which is
// the channel keeping the live total equal to the one a reload recomputes from
// the transcript's stats records.
func (h *Hub) RecordSpend(u llm.Usage) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.bgUsage.Input += u.Input
	h.bgUsage.Output += u.Output
	h.bgUsage.CacheRead += u.CacheRead
	h.bgUsage.Reasoning += u.Reasoning
}

// Seq is the last assigned sequence number.
func (h *Hub) Seq() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.seq
}

// Snapshot returns the current state without subscribing.
func (h *Hub) Snapshot() Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.snapshotLocked()
}

func (h *Hub) snapshotLocked() Snapshot {
	results := make(map[string]ToolResult, len(h.results))
	for k, v := range h.results {
		results[k] = v
	}
	msgs := append([]Message(nil), h.msgs...)
	for i := range msgs {
		// Also cover an already accumulated message from before normalization.
		if msgs[i].Content == nil {
			msgs[i].Content = []llm.Block{}
		}
		if msgs[i].Role == "" {
			msgs[i].Role = string(llm.RoleAssistant)
		}
		msgs[i].AnswerReview = msgs[i].AnswerReview.Clone()
	}
	if msgs == nil {
		msgs = []Message{}
	}
	// The total a client shows is the run-billed spend plus whatever background
	// tasks have reported since the counters were last (re)based — see the
	// struct note on why the two never live in one field.
	usage := h.usage
	usage.Input += h.bgUsage.Input
	usage.Output += h.bgUsage.Output
	usage.CacheRead += h.bgUsage.CacheRead
	usage.Reasoning += h.bgUsage.Reasoning
	tasks := make([]BgTaskLive, 0, len(h.bgTasks))
	for _, t := range h.bgTasks {
		cp := *t
		// Deep-copied: appending to a shared backing array later would race the
		// marshal of this snapshot.
		cp.Frames = append([]json.RawMessage(nil), t.Frames...)
		tasks = append(tasks, cp)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].StartedAt < tasks[j].StartedAt })
	shellTasks := make([]tools.ShellJobRecord, 0, len(h.shellTasks))
	for _, record := range h.shellTasks {
		shellTasks = append(shellTasks, cloneShellJobRecord(record))
	}
	sort.Slice(shellTasks, func(i, j int) bool {
		if shellTasks[i].StartedAt == shellTasks[j].StartedAt {
			return shellTasks[i].TaskID < shellTasks[j].TaskID
		}
		return shellTasks[i].StartedAt < shellTasks[j].StartedAt
	})
	var followups *Followups
	if h.followups != nil {
		// Copied like the task frames: a client mutating the snapshot's slice must
		// not reach the hub's copy.
		followups = &Followups{
			MessageID: h.followups.MessageID,
			Chips:     append([]skills.StarterCard(nil), h.followups.Chips...),
		}
	}
	resolvedInputs := make(map[string]tools.UserInputDetails, len(h.resolvedUserInputs))
	for callID, details := range h.resolvedUserInputs {
		resolvedInputs[callID] = cloneUserInputDetails(details)
	}
	return Snapshot{
		StreamInstance:    h.instanceID,
		Inbox:             append([]InboxItem{}, h.inbox...),
		Seq:               h.seq,
		Messages:          msgs,
		Results:           results,
		UserInputAnswers:  resolvedInputs,
		Live:              h.live.clone(),
		Run:               h.run,
		Policy:            h.policy,
		Usage:             usage,
		BgTasks:           tasks,
		ShellTasks:        shellTasks,
		ContextTokens:     h.context,
		ContextEstimated:  h.contextEstimated,
		OverheadTokens:    h.overhead,
		OverheadBreakdown: append([]agent.OverheadPart(nil), h.overheadParts...),
		ClearTrigger:      h.clearTrigger,
		Followups:         followups,
		AnswerReview:      h.answerReview.Clone(),
	}
}

// Subscribe registers a listener and returns whatever it needs to catch up
// first. Backlog and registration happen under one lock, which is what makes the
// handoff lossless in both directions: no event can slip through between the two.
//
// A negative from asks for a snapshot. A non-negative one asks for the events
// after that sequence number, and silently degrades to a snapshot when the log no
// longer reaches back that far.
func (h *Hub) Subscribe(from int64, instance ...string) (backlog []Event, ch <-chan Event, cancel func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	// Remote readers supply their snapshot's instance. A missing/old instance
	// cannot establish that a numeric cursor refers to this Hub, even when the
	// sequence has already caught up after restart. Internal readers may omit
	// it because they retain this exact Hub pointer throughout their lifetime.
	if len(instance) > 0 && (len(instance) != 1 || instance[0] != h.instanceID) {
		from = -1
	}

	if from >= 0 && h.canReplay(from) {
		for _, e := range h.log {
			if e.Seq > from {
				backlog = append(backlog, e)
			}
		}
	} else {
		snap := h.snapshotLocked()
		backlog = []Event{{Seq: snap.Seq, Type: EvSnapshot, TS: nowMS(), Snapshot: &snap}}
	}

	if h.closed {
		// Nothing more will arrive; hand back a closed channel so the caller can
		// still deliver the backlog and then finish.
		dead := make(chan Event)
		close(dead)
		return backlog, dead, func() {}
	}

	h.nextSub++
	id := h.nextSub
	sub := make(chan Event, subBuffer)
	h.subs[id] = sub
	return backlog, sub, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if c, ok := h.subs[id]; ok {
			delete(h.subs, id)
			close(c)
		}
	}
}

// canReplay reports whether the log still covers everything after from.
func (h *Hub) canReplay(from int64) bool {
	if from >= h.seq {
		// A cursor ahead of this Hub belongs to a previous process/session
		// incarnation. Its policy and pending gates need a fresh snapshot.
		return from == h.seq
	}
	if len(h.log) == 0 {
		// No logged events at all. Only safe if the client has seen everything
		// that could have been logged.
		return from >= h.seq
	}
	return h.log[0].Seq <= from+1
}

// Subscribers is the current listener count, which is what idle eviction keys
// off.
func (h *Hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// Closed reports whether Close has run: nothing more will ever be published,
// and a subscriber whose channel ended can use this to tell "I fell behind"
// (recoverable, resubscribe with ?from) from "the session is gone" (terminal).
// It exists for the MCP watcher, which has no other way to make that
// distinction — a closed channel means both.
func (h *Hub) Closed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closed
}

// Close ends every subscription. Called when a session is evicted from memory.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for id, ch := range h.subs {
		close(ch)
		delete(h.subs, id)
	}
}

func dropTool(list []PendingTool, callID string) []PendingTool {
	for i := range list {
		if list[i].CallID == callID {
			return append(list[:i:i], list[i+1:]...)
		}
	}
	return list
}

func dropIncoming(list []IncomingCall, callID string) []IncomingCall {
	for i := range list {
		if list[i].CallID == callID {
			return append(list[:i:i], list[i+1:]...)
		}
	}
	return list
}

func dropGate(list []PendingGate, gateID string) []PendingGate {
	for i := range list {
		if list[i].GateID == gateID {
			return append(list[:i:i], list[i+1:]...)
		}
	}
	return list
}

func dropUserInput(list []PendingUserInput, requestID string) []PendingUserInput {
	for i := range list {
		if list[i].RequestID == requestID {
			return append(list[:i:i], list[i+1:]...)
		}
	}
	return list
}
