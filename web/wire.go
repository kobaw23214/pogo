// Package web serves the browser interface. It is a second consumer of
// agent.Event, alongside the terminal renderer in main.go.
//
// The one structural decision everything else follows from: a run's lifetime
// belongs to the session, not to an HTTP connection. Starting a turn and
// subscribing to its events are two different requests, so closing the browser
// cannot cancel the work. See hub.go for the replay that makes reconnecting
// cheap, and manager.go for the timeouts that replace "the client hung up" as
// the way runs eventually end.
package web

import (
	"encoding/json"
	"time"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
	"github.com/yosukeno/pogo/wire"
	"github.com/yosukeno/pogo/workflow"
)

// EventType is the SSE `event:` name.
//
// An alias for wire.Type, not a separate type: the loop-derived names below are
// the shared contract, and `pogo -mode json` emits the same ones. Aliasing keeps
// the two from drifting while leaving every existing use site in this package
// unchanged.
type EventType = wire.Type

const (
	// The loop-derived names live in package wire, which is the single definition
	// shared with the JSON mode emitter. Referencing them here rather than
	// redeclaring the strings is what makes "one definition, two consumers" true
	// instead of aspirational.
	EvUserMessage  = wire.UserMessage
	EvTurnStart    = wire.TurnStart
	EvThinking     = wire.Thinking
	EvToken        = wire.Token
	EvMessage      = wire.Message
	EvAnswerReview = wire.AnswerReview
	EvToolArgs     = wire.ToolArgs
	EvToolStart    = wire.ToolStart
	EvToolPartial  = wire.ToolPartial
	EvToolEnd      = wire.ToolEnd
	EvRunEnd       = wire.RunEnd
)

// The rest are web's own: they describe the browser session rather than the loop,
// so they deliberately do not travel in the JSON mode contract.
const (
	// EvSnapshot is always the first frame of a subscription that did not ask to
	// resume from a sequence number.
	EvSnapshot EventType = "snapshot"

	EvRunStart     EventType = "run_start"
	EvRetry        EventType = "retry"
	EvError        EventType = "error"
	EvInboxUpdated EventType = "inbox_updated"

	// The gate publishes these itself rather than through the loop's event
	// channel, because the loop only consumes a verdict and knows nothing about
	// approval as a concept.
	//
	// One consequence clients must handle: a gate_request can arrive *before* the
	// assistant `message` that requested the call. The loop emits that message and
	// then enters the gate from its own goroutine, so the two publishes race over
	// the Hub lock. A snapshot is always self-consistent; a live stream is not
	// ordered across those two paths.
	EvGateRequest  EventType = "gate_request"
	EvGateDeadline EventType = "gate_deadline"
	EvGateResolved EventType = "gate_resolved"
	EvGateAuto     EventType = "gate_auto"

	EvUserInputRequest  EventType = "user_input_request"
	EvUserInputResolved EventType = "user_input_resolved"

	EvPolicyChanged      EventType = "policy_changed"
	EvPolicyReverted     EventType = "policy_reverted"
	EvModelChanged       EventType = "model_changed"
	EvModelConfigPending EventType = "model_config_pending"
	EvToolConfig         EventType = "tool_config"

	// EvRewound tells subscribers the session was rewound to an earlier
	// message: everything they hold past that point describes a branch that
	// no longer exists, and only a fresh snapshot rebuilds the truth.
	EvRewound EventType = "rewound"

	// EvSubagentStart, EvSubagentFrame and EvSubagentEnd track one backgrounded
	// delegation. They are web's own rather than the loop's because a background
	// task belongs to the session, not to any run: the run that dispatched it is
	// usually long gone by the time it finishes, so its progress cannot travel a
	// run's event channel the way a synchronous subagent's does.
	EvSubagentStart EventType = "subagent_start"
	EvSubagentFrame EventType = "subagent_frame"
	EvSubagentEnd   EventType = "subagent_end"

	EvShellTaskStart  EventType = "shell_task_start"
	EvShellTaskOutput EventType = "shell_task_output"
	EvShellTaskEnd    EventType = "shell_task_end"

	// EvCompacted tells subscribers the conversation was replaced by a summary of
	// itself. Structurally the same news as EvRewound — everything held is stale and
	// only a fresh snapshot rebuilds it — and deliberately not the same name: one is
	// a branch the user abandoned and the other is the whole history condensed, and a
	// client that wanted to say so could not if they shared a type.
	EvCompacted EventType = "compacted"

	// EvFollowups carries the generated follow-up suggestion chips for the run
	// that just ended. It is web's own for the same reason the subagent_* events
	// are: the generation happens off the run's event channel, after the run is
	// over, from a goroutine the loop knows nothing about. It can also arrive
	// well after run_end — it is produced by a model call that starts only once
	// the answer is complete, which is the whole point: suggesting a next step
	// must never delay the answer it builds on.
	EvFollowups EventType = "followups"

	// EvAttachmentUploaded announces a file landing in the session's uploads/
	// through the upload endpoint — before and regardless of any message ever
	// carrying it. It is what makes an upload visible to every attached pane
	// the moment it happens: the file tree refreshes, the session-files list
	// counts it. Logged, so a stream resuming with ?from= still hears of it;
	// a fresh snapshot never replays it, which is what the GET endpoint is for.
	EvAttachmentUploaded EventType = "attachment_uploaded"
	EvBrowserDownloads   EventType = "browser_downloads"
	EvShortcutsChanged   EventType = "shortcuts_changed"

	// EvImageDescribeStart and EvImageDescribeEnd track the vision helper's
	// bootstrap description of one attached image, for conversation models
	// that cannot see images. Web's own on two counts: the helper is not the
	// loop, and the work runs before the first turn — dressing it as a tool
	// call would forge a tool_use block the transcript never had. The pair is
	// logged like subagent start/end: a reconnecting pane replays the start
	// and shows the step still in flight.
	EvImageDescribeStart EventType = "image_describe_start"
	EvImageDescribeEnd   EventType = "image_describe_end"

	// EvWorkflowRunStart, EvWorkflowNodeStart, EvWorkflowNodeEnd and
	// EvWorkflowRunEnd track one workflow run (web/workflowrun.go). Web's own
	// for the same reason the subagent_* events are: a workflow run belongs to
	// no session — it has its own hub and its own record file — so its
	// lifecycle cannot travel a session's event channel. All four are logged,
	// so a stream resuming with ?from= replays the transitions; the record
	// each carries is the state authority, so start plus live events always
	// converges.
	EvWorkflowRunStart  EventType = "workflow_run_start"
	EvWorkflowNodeStart EventType = "workflow_node_start"
	EvWorkflowNodeEnd   EventType = "workflow_node_end"
	EvWorkflowRunEnd    EventType = "workflow_run_end"

	// EvWorkflowNodeFrame is one translated loop event of the agent executing
	// a node, fanned out live for the run-observation view. Not logged — same
	// class as tool_partial: a reconnecting client heals from the record
	// snapshot, not from a replay of the typing.
	EvWorkflowNodeFrame EventType = "workflow_node_frame"

	// EvWorkflowDispatchStart and EvWorkflowDispatchEnd track one orchestrator
	// delegation (web/workflowrun.go). Both are logged: a dispatch is a state
	// transition of the node, so a stream resuming with ?from= must replay it
	// to keep the delegation list whole.
	EvWorkflowDispatchStart EventType = "workflow_dispatch_start"
	EvWorkflowDispatchEnd   EventType = "workflow_dispatch_end"
)

// logged reports whether an event goes into the replay log.
//
// Only semantic events do. The incremental kinds are fanned out live so the
// typing effect is not lost, but they are folded into the live state instead of
// being stored: a reconnecting browser wants the current text, not a replay of
// the typing. That is what keeps memory proportional to the transcript rather
// than to the token count — and, for tool_args and tool_partial, to the
// transcript rather than to how much a model streamed or a command printed.
func logged(t EventType) bool {
	switch t {
	case EvToken, EvThinking, EvToolArgs, EvToolPartial, EvSnapshot, EvRewound, EvCompacted, EvSubagentFrame, EvWorkflowNodeFrame, EvShellTaskOutput:
		// EvRewound joins the unlogged kinds: it is a control signal, not
		// history, and a reconnecting client already gets the post-rewind
		// truth from the snapshot the cleared log forces it to take.
		//
		// EvSubagentFrame is progress, same class as tool_partial: folded into
		// the hub's background-task state, which the snapshot serves. The start
		// and end of a task stay logged — they are state transitions a replaying
		// client must not miss. EvWorkflowNodeFrame is the same class again:
		// live progress of a workflow node, with the run record as the state
		// authority a reconnecting client heals from.
		return false
	}
	return true
}

// Event is one SSE frame. It is a flat union rather than a nested `data` object
// so that each field has a name and a type in exactly one place; the cost is a
// wide struct, the benefit is that no consumer has to guess a payload shape.
type Event struct {
	ToolConfig *studioToolSync `json:"tool_config,omitempty"`
	// A scoped invalidation only: clients reread the authenticated shortcut API.
	ShortcutAgentID  string `json:"shortcut_agent_id,omitempty"`
	ShortcutRevision int64  `json:"shortcut_revision,omitempty"`
	ShortcutID       string `json:"shortcut_id,omitempty"`
	// Office reveal events contain public document identity only. Editor
	// capabilities and configuration are fetched by the authenticated UI.
	OfficeDocumentSessionID string    `json:"document_session_id,omitempty"`
	OfficeConversationID    string    `json:"conversation_id,omitempty"`
	OfficePath              string    `json:"path,omitempty"`
	OfficeFileType          string    `json:"file_type,omitempty"`
	Seq                     int64     `json:"seq"`
	Type                    EventType `json:"type"`
	TS                      int64     `json:"ts"`

	// Run lifecycle.
	RunID              string `json:"run_id,omitempty"`
	Model              string `json:"model,omitempty"`
	Provider           string `json:"provider,omitempty"`
	ContextWindow      int    `json:"context_window,omitempty"`
	ModelConfigPending bool   `json:"model_config_pending,omitempty"`
	// Model commits carry explicit zero values to clear disabled thresholds.
	ClearTrigger   *int64 `json:"clear_trigger,omitempty"`
	OverheadTokens *int64 `json:"overhead_tokens,omitempty"`
	// OverheadBreakdown itemizes OverheadTokens and travels with it: on
	// model_changed, and on turn_start when the tool view changed the figure.
	OverheadBreakdown []agent.OverheadPart `json:"overhead_breakdown,omitempty"`
	Turn              int                  `json:"turn,omitempty"`
	// ContextEdit rides on turn_start when this turn's prompt had old tool results
	// cleared from it. Present only on the turns that cleared something, so it is the
	// explanation for a context gauge that just dropped rather than a per-turn field.
	ContextEdit  *agent.ContextEdit      `json:"context_edit,omitempty"`
	AnswerReview *llm.AnswerReviewStatus `json:"answer_review,omitempty"`

	// Messages and streaming. MessageID is filled in by the Hub, which owns the
	// numbering, so publishers never have to track it.
	MessageID string      `json:"message_id,omitempty"`
	Role      string      `json:"role,omitempty"`
	Text      string      `json:"text,omitempty"`
	Content   []llm.Block `json:"content,omitempty"`
	// ClientID echoes the client-generated id a POST /messages body may carry,
	// on EvUserMessage only: an optimistic client matches its placeholder
	// bubble to the settled event by it, instead of waiting out a reload.
	// Deliberately absent from the transcript — it reconciles a live fold,
	// nothing more.
	ClientID    string     `json:"client_id,omitempty"`
	InboxID     string     `json:"inbox_id,omitempty"`
	InputLayout string     `json:"input_layout,omitempty"`
	Steering    bool       `json:"steering,omitempty"`
	Item        *InboxItem `json:"item,omitempty"`
	// Chips ride on EvFollowups, where MessageID names the assistant message the
	// suggestions build on. Only Title and Prompt are ever set — a chip's one
	// action is putting its prompt in the composer. The cards share
	// skills.StarterCard's wire shape so client and server speak of one type.
	Chips []skills.StarterCard `json:"chips,omitempty"`

	// Tool calls. CallID is the identity that ties tool_start, tool_end and any
	// gate traffic together; the browser files everything by it, which is why
	// out-of-order completion in a parallel batch needs no special handling.
	CallID  string          `json:"call_id,omitempty"`
	Name    string          `json:"name,omitempty"`
	Args    json.RawMessage `json:"args,omitempty"`
	IsError bool            `json:"is_error,omitempty"`
	// Details is a tool's structured payload (tools.EditDetails and friends). It
	// never enters the conversation, which is why the diff can be full length
	// here while the model only ever sees the truncated text.
	Details any `json:"details,omitempty"`
	// Source is the citation stamp on EvToolEnd, absent when citations are off.
	// The client resolves an answer's [n](#cite:Sx) anchors against these.
	Source *agent.Source `json:"source,omitempty"`
	// DurationMS is how long the call took, on EvToolEnd. The Hub fills it in
	// from the pending tool's start stamp — publishers don't track time.
	DurationMS int64 `json:"duration_ms,omitempty"`

	// Frame is one structured progress event from a tool that reports them, on
	// EvToolPartial. Today only a subagent, whose child emits this same contract one
	// level down, so the browser renders a delegated run with the code it already has.
	Frame json.RawMessage `json:"frame,omitempty"`

	// TaskID identifies a background delegation on the subagent_* events. It is the
	// child spec's id — the same value the dispatching call's Details carry — so the
	// browser can join a running task to the card the call opened.
	TaskID string `json:"task_id,omitempty"`
	// ShellTask carries a bounded snapshot, including its output tail. Output
	// events are live-only; the Hub retains their latest state for reconnects.
	ShellTask *tools.ShellJobRecord `json:"shell_task,omitempty"`

	// Frames rides on EvSubagentEnd: the task's capped frame list, filled in by
	// the hub at completion. A client that missed frames (they are never in the
	// replay log) heals its card from this copy instead of showing a truncated
	// run forever; a client that saw them all ignores it.
	Frames []json.RawMessage `json:"frames,omitempty"`

	// Approval gate. Deadline is absolute epoch milliseconds, never a duration:
	// a client that reloads has to be able to recompute the remaining time, and
	// a countdown that started on the server cannot survive that.
	GateID      string   `json:"gate_id,omitempty"`
	Deadline    int64    `json:"deadline,omitempty"`
	Frozen      *bool    `json:"frozen,omitempty"`
	RemainingMS int64    `json:"remaining_ms,omitempty"`
	Danger      []string `json:"danger,omitempty"`
	Allow       bool     `json:"allow,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	// Origin names the subagent a gated call came from, empty for the session's
	// own calls. Published because a background subagent's approval can outlive
	// every run: the hub needs to know which pending gates a run's end may not
	// clear, and the card needs to say whose command it is.
	Origin string `json:"origin,omitempty"`
	// By records who decided: user, timeout, cancel, or rule:<name>.
	By   string `json:"by,omitempty"`
	Rule string `json:"rule,omitempty"`

	UserInput  *PendingUserInput    `json:"user_input,omitempty"`
	Resolution *UserInputResolution `json:"user_input_resolution,omitempty"`

	// Policy.
	Policy *PolicyState `json:"policy,omitempty"`
	From   string       `json:"from,omitempty"`
	To     string       `json:"to,omitempty"`

	// Retry, from the llm.OnRetry hook.
	Attempt int   `json:"attempt,omitempty"`
	Max     int   `json:"max,omitempty"`
	DelayMS int64 `json:"delay_ms,omitempty"`

	// Run end.
	StopReason string `json:"stop_reason,omitempty"`
	// EndReason is why the run ended, in the harness's own vocabulary rather than
	// the provider's — the only field that can say "turn_limit" or "stagnation".
	// Defined in agent/endreason.go and carried here unchanged, so the browser and a
	// script reading the JSONL stream cannot disagree about what ended a run.
	EndReason string     `json:"end_reason,omitempty"`
	Usage     *llm.Usage `json:"usage,omitempty"`
	Error     string     `json:"error,omitempty"`
	// Undelivered lists steering messages the run accepted but never passed to the
	// model. A client should offer the text back rather than let it disappear.
	Undelivered []string `json:"undelivered,omitempty"`

	Snapshot *Snapshot `json:"snapshot,omitempty"`

	// Attachment rides on EvAttachmentUploaded: the saved upload's metadata, in
	// the same shape the upload endpoint returned, so a pane files it without a
	// second request.
	Attachment       *AttachmentMeta            `json:"attachment,omitempty"`
	BrowserDownloads *nativeBrowserDownloadList `json:"browser_downloads,omitempty"`

	// ImageRef/ImageName name the image on the image_describe_* events.
	// ImageRef doubles as the identity tying a start to its end — one run
	// never has two in-flight descriptions for the same ref. The end carries
	// the description in Text, or the failure in Error, and DurationMS filled
	// in by the publisher (the Hub only computes durations for tool calls).
	ImageRef  string `json:"image_ref,omitempty"`
	ImageName string `json:"image_name,omitempty"`

	// Workflow run observation. RunID above doubles as the workflow run id on
	// these events — the two never share a hub, so one field names both.
	// NodeID/NodeStatus/Artifact are the per-node convenience fields; the
	// whole record rides WorkflowRun, because it is the state authority the
	// client heals from. On EvWorkflowNodeFrame, Frame above carries the
	// translated loop event of the node's agent.
	WorkflowID  string              `json:"workflow_id,omitempty"`
	NodeID      string              `json:"node_id,omitempty"`
	NodeStatus  string              `json:"node_status,omitempty"`
	Artifact    string              `json:"artifact,omitempty"`
	WorkflowRun *workflow.RunRecord `json:"workflow_run,omitempty"`
	// Dispatch rides the workflow_dispatch_* events: one orchestrator
	// delegation, in the same shape the run record's dispatches list keeps.
	Dispatch *workflow.DispatchRecord `json:"dispatch,omitempty"`
}

// Message is a settled conversation entry as the browser sees it.
//
// tool_result blocks are deliberately absent: they are keyed out into
// Snapshot.Results so that live rendering and replayed history have the same
// shape. Without that split, a session loaded from JSONL would arrive with
// results inline while a live one gets them as events.
type Message struct {
	AnswerReview *llm.AnswerReviewStatus `json:"answer_review,omitempty"`
	ID           string                  `json:"id"`
	Role         string                  `json:"role"`
	Content      []llm.Block             `json:"content"`
	InboxID      string                  `json:"inbox_id,omitempty"`
	RunID        string                  `json:"run_id,omitempty"`
	InputLayout  string                  `json:"input_layout,omitempty"`
	Steering     bool                    `json:"steering,omitempty"`
	// TS is when the message was recorded, epoch ms. Zero for a message from a
	// session file old enough to predate it being threaded through.
	TS int64 `json:"ts,omitempty"`
}

// ToolResult is one finished call, filed by call id.
type ToolResult struct {
	CallID  string `json:"call_id"`
	Name    string `json:"name,omitempty"`
	Text    string `json:"text"`
	IsError bool   `json:"is_error,omitempty"`
	Details any    `json:"details,omitempty"`
	// Source is the call's citation stamp, so a reloaded session resolves the
	// labels its answers cite exactly the way the live stream does.
	Source *agent.Source `json:"source,omitempty"`
	// DurationMS is how long the call took. Live, it is computed from the
	// pending tool's start; replayed, it comes off the persisted block.
	DurationMS int64 `json:"duration_ms,omitempty"`
}

// PendingTool is a call that has started and not yet finished.
type PendingTool struct {
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Args      json.RawMessage `json:"args,omitempty"`
	StartedAt int64           `json:"started_at"`
	// Output is what the call has printed so far, for the tools that report it.
	//
	// Accumulated here rather than left to the client because it is the only way a
	// browser that reconnects mid-command sees anything: the fragments are not
	// logged, so there is nothing to replay. Bounded by maxPendingOutput.
	Output string `json:"output,omitempty"`
	// Frames are the structured progress events of a call that reports them — today
	// a subagent, whose child produces this same contract one level down. Kept here
	// for the same reason Output is: the fragments are not logged, so a browser that
	// reconnects mid-delegation would otherwise see an empty card.
	//
	// Bounded by maxPendingFrames, and by count rather than by bytes because these
	// are whole events: dropping the oldest loses one line of history, while cutting
	// bytes would hand the client half a frame it cannot parse.
	Frames []json.RawMessage `json:"frames,omitempty"`
}

// IncomingCall is a tool call whose arguments the model is still streaming in —
// before review, before tool_start. It is what lets the browser show a
// progressive preview card while a large write is being generated.
type IncomingCall struct {
	CallID string `json:"call_id"`
	Name   string `json:"name"`
	// Head and Tail are raw — still JSON-escaped — argument text: the path field
	// lives in the head, a line preview in the tail. The client does the lenient
	// display-unescaping; the server stays dumb. Both are bounded because this
	// copy goes into every snapshot.
	Head string `json:"head,omitempty"`
	Tail string `json:"tail,omitempty"`
	// Bytes is the total fragment bytes seen, so the card can report progress
	// beyond what Head and Tail keep.
	Bytes int `json:"bytes"`
	// Lines counts content newlines seen so far (the two-character `\n` escape
	// in the raw arguments), so the UI can show a live +N next to the path.
	Lines int `json:"lines"`
	// escPending is the cross-fragment carry: a fragment may end with the lone
	// backslash of an escape whose second char arrives in the next one. It is
	// process-local state, never serialized.
	escPending bool
	TS         int64 `json:"ts,omitempty"`
}

// PendingGate is an approval still waiting on a human.
type PendingGate struct {
	GateID      string          `json:"gate_id"`
	CallID      string          `json:"call_id"`
	Tool        string          `json:"tool"`
	Args        json.RawMessage `json:"args,omitempty"`
	Deadline    int64           `json:"deadline"`
	Frozen      bool            `json:"frozen,omitempty"`
	RemainingMS int64           `json:"remaining_ms,omitempty"`
	Danger      []string        `json:"danger,omitempty"`
	// Origin names the subagent the call came from. Such a gate outlives runs —
	// a background task asks whenever it asks — so EvRunEnd keeps these while
	// clearing the session's own.
	Origin string `json:"origin,omitempty"`
}

// BgTaskLive is one backgrounded delegation as the hub knows it: what it is,
// how far it has got, and — once finished — how it went. Kept outside Live
// precisely because Live is cleared at every run's end and a background task
// is defined by outliving that.
type BgTaskLive struct {
	TaskID string `json:"task_id"`
	Mode   string `json:"mode"`
	// Task is the delegated brief, clipped at publish time. It is what a card
	// or task list shows instead of making the client dig out the dispatching
	// call's arguments.
	Task      string            `json:"task"`
	Status    string            `json:"status"` // "running", "done" or "failed"
	StartedAt int64             `json:"started_at"`
	Frames    []json.RawMessage `json:"frames,omitempty"`
	// Details is the settled SubagentDetails — turns, tokens, transcript,
	// commit — present once Status leaves "running".
	Details    any    `json:"details,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

// Live is everything in flight: partial text, unfinished calls, undecided
// gates. It is the part of a snapshot that cannot be read back from the session
// file.
type Live struct {
	RunID     string `json:"run_id,omitempty"`
	Active    bool   `json:"active"`
	Turn      int    `json:"turn,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	Thinking  string `json:"thinking,omitempty"`
	Text      string `json:"text,omitempty"`
	// The slices are non-nil in JSON so clients can index them without a guard.
	PendingTools      []PendingTool      `json:"pending_tools"`
	PendingGates      []PendingGate      `json:"pending_gates"`
	PendingUserInputs []PendingUserInput `json:"pending_user_inputs"`
	// Incoming holds calls whose arguments are still streaming in, ordered by
	// arrival. Entries leave when their tool_start (or tool_end) arrives.
	Incoming []IncomingCall `json:"incoming"`
}

func (l Live) clone() Live {
	out := l
	out.PendingTools = append([]PendingTool(nil), l.PendingTools...)
	out.PendingGates = append([]PendingGate(nil), l.PendingGates...)
	out.PendingUserInputs = make([]PendingUserInput, len(l.PendingUserInputs))
	for i, request := range l.PendingUserInputs {
		out.PendingUserInputs[i] = *clonePendingUserInputPtr(request)
	}
	out.Incoming = append([]IncomingCall(nil), l.Incoming...)
	if out.PendingTools == nil {
		out.PendingTools = []PendingTool{}
	}
	if out.PendingGates == nil {
		out.PendingGates = []PendingGate{}
	}
	if out.PendingUserInputs == nil {
		out.PendingUserInputs = []PendingUserInput{}
	}
	if out.Incoming == nil {
		out.Incoming = []IncomingCall{}
	}
	return out
}

// RunInfo tells a fresh client whether it may send a prompt, and what it is
// talking to.
type RunInfo struct {
	Active   bool   `json:"active"`
	RunID    string `json:"run_id,omitempty"`
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
	// Latest terminal status in this live Hub. Snapshots must retain an
	// unfinished-run notice when the client reconnects after run_end.
	EndReason string `json:"end_reason,omitempty"`
	Error     string `json:"error,omitempty"`
	// ContextWindow comes from the model catalog. It travels with the session so
	// the client does not have to join a model id against /api/models, and so an
	// unknown model degrades to "no meter" rather than to a wrong one.
	ContextWindow      int  `json:"context_window,omitempty"`
	ModelConfigPending bool `json:"model_config_pending,omitempty"`
}

// PolicyState is the gate configuration. It has to travel in the snapshot:
// policy lives on the server, so a reload must not make the auto-mode banner
// disappear while the gate is in fact still open.
type PolicyState struct {
	Mode string `json:"mode"`
	// RemainingTurns is only set for a turn-limited auto mode; zero means "no
	// limit applies".
	RemainingTurns int `json:"remaining_turns,omitempty"`
}

// Followups are the generated suggestion chips for the most recent assistant
// message that produced any, carried in the snapshot so a full-page reload
// recovers them without waiting for a replay of the event itself.
type Followups struct {
	MessageID string               `json:"message_id"`
	Chips     []skills.StarterCard `json:"chips"`
}

// Snapshot is the whole session state as of Seq.
type Snapshot struct {
	// StreamInstance distinguishes sequence counters across process/session
	// reconstructions. It is a replay cursor hint, never an access credential.
	StreamInstance string `json:"stream_instance"`

	AnswerReview *llm.AnswerReviewStatus `json:"answer_review,omitempty"`
	Inbox        []InboxItem             `json:"inbox"`
	Seq          int64                   `json:"seq"`
	Messages     []Message               `json:"messages"`
	Results      map[string]ToolResult   `json:"results"`
	// Async question answers are session history, not a live run's pending call.
	UserInputAnswers map[string]tools.UserInputDetails `json:"user_input_answers,omitempty"`
	Live             Live                              `json:"live"`
	Run              RunInfo                           `json:"run"`
	Policy           PolicyState                       `json:"policy"`
	// Usage is the session's running total, for cost.
	Usage llm.Usage `json:"usage"`
	// BgTasks are the session's backgrounded delegations, running and recently
	// finished alike. Session-scoped rather than run-scoped, which is the whole
	// point of running in the background.
	BgTasks []BgTaskLive `json:"bg_tasks"`
	// ShellTasks outlive individual runs and are recovered from host records.
	ShellTasks []tools.ShellJobRecord `json:"shell_tasks"`
	// ContextTokens is the most recent turn's prompt size, for occupancy. It is
	// deliberately not derived from Usage: that one accumulates across turns and
	// would read many times the window on a long session.
	ContextTokens int64 `json:"context_tokens"`
	// ContextEstimated marks a retained prompt count from a previous model. The
	// current provider has not measured it yet; do not present it as an exact count.
	ContextEstimated bool `json:"context_estimated,omitempty"`
	// OverheadTokens is the fixed cost of every request: the system prompt plus the
	// tool schemas, both resent every turn.
	//
	// The byte counts are exact — the schemas go through llm.ToolsWireJSON, the same
	// renderer the request uses — but the conversion to tokens is still
	// llm.BytesPerToken, so this is a measured size divided by an approximate rate.
	// Accurate enough for the meter's breakdown, and still not something to decide
	// from: the only authoritative token count is ContextTokens, which the provider
	// reported.
	OverheadTokens int64 `json:"overhead_tokens,omitempty"`
	// OverheadBreakdown itemizes OverheadTokens: labeled system-prompt
	// sections, then tool declarations grouped by Skill or MCP server, in the
	// same units (agent.OverheadBreakdown). Keys are stable; clients localize.
	OverheadBreakdown []agent.OverheadPart `json:"overhead_breakdown,omitempty"`
	// ClearTrigger is the prompt size at which context clearing begins, or absent
	// when it is off. The gauge needs it because clearing holds occupancy just below
	// it: bands drawn at fixed fractions of the window would sit permanently in the
	// warning colour and stop carrying information.
	ClearTrigger int64 `json:"clear_trigger,omitempty"`
	// Followups are the latest generated suggestion chips, keyed to the assistant
	// message they build on. Absent until a run produces any; the client's
	// rule-based matching is the fallback for every message without them.
	Followups *Followups `json:"followups,omitempty"`
}

func nowMS() int64 { return time.Now().UnixMilli() }

// fromAgentEvent translates a loop event into a wire event. The second return
// value is false for events the web layer publishes itself with more context
// than the loop has: agent_start becomes run_start, which also carries the run
// id and the model.
// The mapping itself lives in wire.FromAgent, shared with the JSON mode emitter.
// This function only widens the result into the browser's frame, so a new event
// kind is wired up in one place rather than two — and the browser and a script
// cannot disagree about what `tool_end` means.
//
// Steering arrives as an ordinary user message, because that is what it is: the
// client already knows how to draw one, and the timeline should show it at the
// point it entered the conversation rather than when it was typed. That decision
// is wire's now (wire.UserMessage), which is why it is not repeated here.
func fromAgentEvent(e agent.Event) (Event, bool) {
	w, ok := wire.FromAgent(e)
	if !ok {
		return Event{}, false
	}
	// TS and Seq stay the Hub's job: it owns the numbering, so a publisher never
	// has to track it. wire's timestamp is discarded for the same reason.
	return Event{
		Type:         w.Type,
		Turn:         w.Turn,
		ContextEdit:  w.ContextEdit,
		Role:         w.Role,
		Text:         w.Text,
		Content:      w.Content,
		CallID:       w.CallID,
		Name:         w.Name,
		Args:         w.Args,
		IsError:      w.IsError,
		Details:      w.Details,
		Source:       w.Source,
		Frame:        w.Frame,
		StopReason:   w.StopReason,
		EndReason:    w.EndReason,
		Usage:        w.Usage,
		Error:        w.Error,
		Undelivered:  w.Undelivered,
		InboxID:      w.InboxID,
		RunID:        w.RunID,
		Steering:     w.Steering,
		InputLayout:  w.InputLayout,
		AnswerReview: w.AnswerReview.Clone(),
	}, true
}
