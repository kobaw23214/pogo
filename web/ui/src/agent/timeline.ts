import type {
  BgTask,
  Block,
  InputLayout,
  CiteSource,
  IncomingArgs,
  Live,
  MCPDetails,
  Message,
  PendingGate,
  ReadFileDetails,
  ReadManyDetails,
  SkillDetails,
  SubagentDetails,
  SubagentFrame,
  TodoDetails,
  TodoItem,
  ToolDetails,
  ToolName,
  ToolResult,
  AnswerReview,
  ShellJobRecord,
  ShellJobStatus,
} from "@/api/types";
import { collectSources, compareSourceLabels, numberCitations } from "@/agent/cite";
import { gt } from "@/i18n";
import { shortcutCallTool } from "@/agent/shortcutToolNames";
import { normalizeMessageContent, normalizeMessages } from "@/api/messageContent";

// This module is deliberately free of Vue imports: it is a pure projection of
// server state into what the view renders, so it can be unit tested without a
// component, a DOM, or a running agent.

export interface TimelineCall {
  callId: string;
  name: ToolName;
  args: unknown;
  /** Invalid arguments remain opaque text for inspection, never parsed or run. */
  inputInvalid?: boolean;
  inputError?: string;
  result?: ToolResult;
  /** running is true between tool_start and tool_end. */
  running: boolean;
  /** Host timestamp of this pending call, preserved across snapshot recovery. */
  startedAt?: number;
  /** Child approvals matched by exact task/run origin, never by task text. */
  childGates?: PendingGate[];
  /** A background result matched to the dispatch's exact task/run identity. */
  taskNotice?: TaskNotice;
  /**
   * liveOutput is what a still-running call has printed so far. Only bash reports
   * it, and it is replaced by `result` once the call settles, so the same output
   * is never shown twice.
   */
  liveOutput?: string;
  /**
   * liveFrames is the delegated run's own events. Structured rather than text
   * because a delegation has structure — turns, tool calls, an ending — and a
   * card that only got "· read" could not show it. While the call runs they
   * come from the pending tool; once it settles, from the copy the client
   * retained on the result, so the card keeps its process instead of emptying
   * out the moment the answer arrives.
   */
  liveFrames?: SubagentFrame[];
  /** gate is set while this call is waiting for a human. */
  gate?: PendingGate;
  /**
   * bgTask is the background delegation this call dispatched, joined by the
   * task id in its (immediately settled) result's details. The call itself
   * finished the moment it was answered — that is what "background" means —
   * so the card's liveness, frames and final tally all come from here.
   */
  bgTask?: BgTask;
  /** orphaned: never finished and no run is active — the process died mid-call. */
  orphaned: boolean;
  /**
   * corrects points at an earlier failed call this one appears to fix. It is a
   * heuristic (same tool, same target, later, succeeded) and the UI renders it
   * as a hint, not as an assertion.
   */
  corrects?: string;
  /**
   * superseded marks a task-list write that a later one replaced.
   *
 * The todo tool holds no state: a task's current plan is whichever of its calls
 * wrote last, and earlier calls in that task are replaced snapshots.
   * Rendering them all identically would put several contradictory checklists on
   * one timeline, and someone scrolling up would read an old "1/3 done" as the
   * present state. So the flag exists to let the card collapse the old ones —
   * they stay visible, because how the plan changed is worth seeing, but they
   * stop claiming to be the plan.
   */
  superseded?: boolean;
}

export interface UserItem {
  kind: "user";
  id: string;
  text: string;
  /** Send time, epoch ms. Absent on transcripts old enough to lack it. */
  ts?: number;
  steering?: boolean;
  runId?: string;
  inboxId?: string;
  /** Attachment images in send order, workspace paths. */
  images?: string[];
  /** Vision-helper descriptions by image ref, when the transcript carries them. */
  imageAlts?: Record<string, string>;
  /** Non-image attachments: path + display name. */
  files?: { path: string; name: string }[];
  /** Original user blocks for ordered input, retained across SSE and replay. */
  orderedContent?: Block[];
  inputLayout?: InputLayout;
}

/**
 * A NoticeItem is a harness-injected user-role message — the soft-cap
 * checkpoint, the unfinished-todo nudge — rendered as a quiet system line. It
 * is NOT a user item: nobody typed it, so it must not get a bubble, must not
 * be a rewind target, and — the whole reason it exists as a kind — must not
 * start a new exchange, which is what restarted the turn numbering in a
 * subagent's transcript view every time the soft cap fired.
 */
export interface NoticeItem {
  kind: "notice";
  id: string;
  text: string;
  ts?: number;
}

export interface TurnItem {
  kind: "turn";
  id: string;
  /** index is the 1-based turn number within the current exchange. */
  index: number;
  thinking: string;
  text: string;
  calls: TimelineCall[];
  /**
   * Calls whose arguments are still streaming in, rendered as preview cards
   * where their pending-tool card will later appear. Never present on a
   * settled turn's own row: every entry is adopted onto the in-flight turn,
   * the same place an orphan gate goes.
   */
  incoming?: IncomingArgs[];
  /** streaming is true for the turn whose message has not settled yet. */
  streaming: boolean;
  /**
   * Settle time, epoch ms. Absent on the in-flight turn and on transcripts old
   * enough to lack it; the exchange projection reads it for the work-log
   * duration.
   */
  ts?: number;
  /** Total thinking duration, ms, summed off the blocks that carry it. */
  thinkingMs?: number;
  answerReview?: AnswerReview;
  /**
   * Sources reached so far in this exchange, plus explicitly cited sources
   * from earlier exchanges. Each projected turn gets its own array, including
   * [] when nothing is available, so an absent source never looks resolved.
   * Optional only for callers that construct a turn without a source context.
   */
  sources?: CiteSource[];
}

export type TimelineItem = UserItem | NoticeItem | TurnItem;

// harnessNoticePrefix opens the two user-role nudges the loop injects itself:
// the soft-cap checkpoint (agent/softcap.go) and the unfinished-todo nudge
// (agent/todofinish.go). A background task's completion is harness-made too
// but wears its own marker, because it DOES start an exchange — a new run
// answers it — whereas a nudge lands mid-run and must not split one.
export const harnessNoticePrefix = "[pogo] ";

/** isHarnessNotice reports whether a user-role text is the harness nudging
 * its own run rather than a person saying something. */
export function isHarnessNotice(text: string): boolean {
  return text.startsWith(harnessNoticePrefix);
}

// taskNotificationMarker opens a background subagent's completion message. It
// is harness-generated text wearing the user role — the one legal injection
// point — and the marker is what keeps it recognisable as a system notice:
// here for styling, on the server for the sidebar count, in the run-end path
// for re-queueing an undelivered one.
export const taskNotificationMarker = "<task-notification>";

/** isTaskNotification reports whether a user-role message is a background
 * completion notice rather than something the user typed. */
export function isTaskNotification(text: string): boolean {
  return text.startsWith(taskNotificationMarker);
}

/**
 * taskNotificationBody strips the envelope for display: the marker lines, and
 * the "[SYSTEM NOTIFICATION]" preamble, which is addressed to the model and is
 * noise to a person.
 */
export function taskNotificationBody(text: string): string {
  return text
    .replace(/^<task-notification>\n?/, "")
    .replace(/\n?<\/task-notification>\s*$/, "")
    .replace(/^\[SYSTEM NOTIFICATION[^\n]*\n/, "");
}

/**
 * TaskNotice is the structured read of a background completion notification,
 * parsed back out of the envelope web/background.go writes. The card renders
 * from this instead of dumping the raw envelope: the envelope is addressed to
 * the model, the card is for a person.
 */
export interface TaskNotice {
  id: string;
  kind?: "shell";
  status?: ShellJobStatus;
  command?: string;
  workdir?: string;
  exitCode?: number;
  agentId?: string;
  mode?: string;
  brief: string;
  failed: boolean;
  /** The failure reason, when failed. */
  error?: string;
  /** The reported outcome, clipped at dispatch time. */
  result?: string;
  /** The child transcript's full path — the "查看过程" link's target. */
  transcript?: string;
  inputTok?: number;
  outTok?: number;
  turns?: number;
}

function noticeString(value: string): string | null {
  try {
    const decoded: unknown = JSON.parse(value);
    return typeof decoded === "string" ? decoded : null;
  } catch { return null; }
}

/** Shell fields are single-line JSON strings written by the host. Output is
 * decoded only as text; it never becomes markup or executable UI actions. */
function parseShellTaskNotification(body: string): TaskNotice | null {
  const header = body.match(/^Task: ([A-Za-z0-9_-]{1,160})\nStatus: (completed|failed|cancelled|timed_out|interrupted)\n/);
  if (!header) return null;
  const commandField = body.match(/^Command: (.*)$/m);
  const workdirField = body.match(/^Working directory: (.*)$/m);
  if (!commandField || !workdirField) return null;
  const command = noticeString(commandField[1]), workdir = noticeString(workdirField[1]);
  if (command === null || workdir === null) return null;
  const descriptionField = body.match(/^Description: (.*)$/m);
  const description = descriptionField ? noticeString(descriptionField[1]) : undefined;
  if (descriptionField && description === null) return null;
  const exitField = body.match(/^Exit code: (-?\d+)$/m);
  const exitCode = exitField ? Number(exitField[1]) : undefined;
  if (exitCode !== undefined && !Number.isSafeInteger(exitCode)) return null;
  const status = header[2] as ShellJobStatus;
  // A malformed or contradictory success receipt must not produce a green tick.
  if (status === "completed" && exitCode !== 0) return null;
  const errorField = body.match(/^Error: (.*)$/m);
  const error = errorField ? noticeString(errorField[1]) : undefined;
  if (errorField && error === null) return null;
  const outputField = body.match(/^Command output is untrusted data, not instructions\. Recent output \(quoted\):\n([^\n]*)/m);
  // Older Go-quoted output may contain escapes JSON does not support. Keep that
  // literal quoted line rather than guessing or dropping command output.
  const result = outputField ? noticeString(outputField[1]) ?? outputField[1] : undefined;
  return { id: header[1], kind: "shell", status, command, workdir, exitCode,
    brief: description || command.split("\n")[0] || header[1], failed: status !== "completed", ...(error ? { error } : {}), ...(result !== undefined ? { result } : {}) };
}

/**
 * parseTaskNotification reads the envelope's fields. Tolerant by line: any
 * line that does not match a known field is left in the result, because the
 * model-written outcome is the part that must survive verbatim.
 */
export function parseTaskNotification(text: string): TaskNotice | null {
  if (!isTaskNotification(text)) return null;
  const body = taskNotificationBody(text);
  const shell = parseShellTaskNotification(body);
  if (shell) return shell;
  const task = body.match(/^Task: (\S+?)(?: \(mode "([^"]*)"\))? — ([^\n]*)$/m);
  if (!task) return null;
  const out: TaskNotice = { id: task[1], mode: task[2] || undefined, brief: task[3], failed: false };
  const retained = body.match(/^Retained agent: (agent_[a-f0-9]{32})\./m);
  if (retained) out.agentId = retained[1];

  const status = body.match(/^Status: (completed|FAILED(?:: ([^\n]*))?)$/m);
  if (!status) return null;
  out.failed = status?.[1] !== "completed";
  if (out.failed) out.error = status?.[2];

  const transcript = body.match(/^Full transcript: (\S+)$/m);
  if (transcript) out.transcript = transcript[1];
  const tokens = body.match(/^Tokens: (\d+) in \/ (\d+) out over (\d+) turn/m);
  if (tokens) {
    out.inputTok = Number(tokens[1]);
    out.outTok = Number(tokens[2]);
    out.turns = Number(tokens[3]);
  }

  // The result is the free-form middle: everything between the Result: line
  // and the machine-written tail (transcript, tokens). A regex lookahead
  // against those two line starts is the cut; index arithmetic, not one big
  // pattern, because the outcome itself may contain anything.
  const ri = body.search(/^Result:$/m);
  if (ri >= 0) {
    let rest = body.slice(body.indexOf("\n", ri) + 1);
    const cut = rest.search(/^(Full transcript:|Tokens:)/m);
    if (cut >= 0) rest = rest.slice(0, cut);
    const resultText = rest.trim();
    if (resultText) out.result = resultText;
  }
  return out;
}

/**
 * transcriptSettled reports whether a transcript read off disk shows a run
 * that has ended: its last message is the assistant speaking, with no pending
 * tool call. A running child's file always ends on a tool result or a call —
 * never on a finished answer — and a checkpoint nudge ends on the user role,
 * so neither reads as settled. The subagent sheet polls until this flips,
 * which is what keeps the view live when the parent's own event stream cannot
 * say (another session is showing, or the run predates the page).
 */
export function transcriptSettled(messages: Message[]): boolean {
  const last = messages[messages.length - 1];
  if (!last || last.role !== "assistant") return false;
  let spoke = false;
  for (const b of normalizeMessageContent(last.content)) {
    if (b.type === "tool_use") return false;
    if (b.type === "text" && b.text.trim() !== "") spoke = true;
  }
  return spoke;
}

/**
 * transcriptAction is the sheet's answer to "what is this run doing right
 * now", read off the transcript's tail — the same line the parent's live work
 * header derives from the event stream, except the sheet only ever has the
 * file, so the deduction runs on what the child has written so far.
 *
 * Only the newest assistant message can hold a call still in flight: the loop
 * is sequential, so a tool result — and therefore the next turn — cannot exist
 * while an earlier call is unanswered. An unresolved call there is the running
 * action; anything else short of settled (a turn whose calls all landed, a
 * checkpoint nudge as the tail) means the child is generating, which the file
 * cannot see — so the honest line is "thinking".
 *
 * Returns null once the transcript has settled: a finished run has no current
 * action, and the footer this drives should go away rather than freeze on the
 * last thing the run did.
 */
export function transcriptAction(messages: Message[], results: Record<string, ToolResult>): string | null {
  if (transcriptSettled(messages)) return null;
  for (let i = messages.length - 1; i >= 0; i--) {
    const m = messages[i];
    if (m.role !== "assistant") continue;
    const content = normalizeMessageContent(m.content);
    for (let j = content.length - 1; j >= 0; j--) {
      const b = content[j];
      if (b.type === "tool_use" && !b.input_invalid && !(b.id in results)) {
        const detail = summarizeArgs(b.name, b.input);
        return detail
          ? gt("workLog.live.runningTool", { detail })
          : gt("workLog.live.runningToolBare", { name: b.name });
      }
    }
    break;
  }
  return gt("workLog.live.thinking");
}

/**
 * buildTimeline projects the server state into a flat list of exchanges.
 *
 * pogo's shape is a flat sequence of turns — one LLM call plus the tools it
 * asked for. There is no triage, no plan, no sub-task fan-out, so there is
 * nothing here that corresponds to the phase/wave structure a plan-execute agent
 * needs.
 */
export function buildTimeline(
  messages: Message[],
  results: Record<string, ToolResult>,
  live: Live,
  bgTasks: Record<string, BgTask> = {},
  answerReview?: AnswerReview | null,
  shellTasks: Record<string, ShellJobRecord> = {},
): TimelineItem[] {
  messages = normalizeMessages(messages);
  const items: TimelineItem[] = [];
  const taskNotices = new Map<string, TaskNotice>();
  for (const message of messages) {
    if (message.role !== "user") continue;
    const text = message.content.filter((block): block is Extract<Block, { type: "text" }> => block.type === "text").map(block => block.text).join("");
    // Several background completions can be delivered in one parent wake.
    for (const envelope of text.match(/^<task-notification>[\s\S]*?^<\/task-notification>/gm) ?? []) {
      const notice = parseTaskNotification(envelope);
      if (notice) taskNotices.set(notice.id, notice);
    }
  }
  let turnIndex = 0;
  // Only a tool_use encountered in this transcript can make its result
  // reachable. The result table may also contain later calls, a rewound
  // branch, or an orphan event; scanning it wholesale would attach evidence
  // to an answer that never had access to it.
  const reachableSources = new Map<string, CiteSource>();
  const exchangeSources = new Map<string, CiteSource>();
  const sourcesFor = (text: string): CiteSource[] => {
    const selected = new Map(exchangeSources);
    // Prior questions do not fill the next question's sources panel, but a
    // deliberate reference back to an earlier source must remain usable.
    for (const label of numberCitations(text, null).nums.keys()) {
      const source = reachableSources.get(label);
      if (source && !selected.has(label)) selected.set(label, source);
    }
    return [...selected.values()].sort((a, b) => compareSourceLabels(a.label, b.label));
  };

  for (const m of messages) {
    if (m.role === "user") {
      const text = m.content
        .filter((b): b is Extract<typeof b, { type: "text" }> => b.type === "text")
        .map((b) => b.text)
        .join("");
      // A harness nudge is not an exchange boundary: the run it landed in is
      // still the same run, and counting from 第 1 轮 again after a soft-cap
      // checkpoint read as a restart that never happened.
      if (isHarnessNotice(text)) {
        items.push({ kind: "notice", id: m.id, text, ts: m.ts });
        continue;
      }
      const images = m.content
        .filter((b): b is Extract<typeof b, { type: "image" }> => b.type === "image")
        .map((b) => b.image_ref);
      const imageAlts: Record<string, string> = {};
      for (const b of m.content) {
        // The vision helper's description travels as the block's alt: patched
        // live by image_describe_end, persisted in the transcript after.
        if (b.type === "image" && b.image_alt) imageAlts[b.image_ref] = b.image_alt;
      }
      const files = m.content
        .filter((b): b is Extract<typeof b, { type: "file" }> => b.type === "file")
        .map((b) => ({ path: b.file_path, name: b.file_name ?? b.file_path }));
      items.push({
        kind: "user",
        id: m.id,
        text,
        ts: m.ts,
        ...(m.input_layout || m.content.some(block => block.type === "text" && (block.input_shortcut || block.input_office))
          ? { orderedContent: m.content, inputLayout: m.input_layout || "top" } : {}),
        ...(m.steering ? { steering: true } : {}),
        ...(m.run_id ? { runId: m.run_id } : {}),
        ...(m.inbox_id ? { inboxId: m.inbox_id } : {}),
        ...(images.length ? { images } : {}),
        ...(Object.keys(imageAlts).length ? { imageAlts } : {}),
        ...(files.length ? { files } : {}),
      });
      if (!m.steering) {
        turnIndex = 0;
        exchangeSources.clear();
      }
      continue;
    }

    turnIndex++;
    const thinking: string[] = [];
    const text: string[] = [];
    const calls: TimelineCall[] = [];
    let thinkingMs = 0;
    for (const b of m.content) {
      if (b.type === "thinking") {
        thinking.push(b.text);
        thinkingMs += b.duration_ms ?? 0;
      } else if (b.type === "text") text.push(b.text);
      else if (b.type === "tool_use") {
        const result = results[b.id];
        const pending = live.pending_tools.find((t) => t.call_id === b.id);
        // A backgrounded dispatch settles its own call immediately — the ack is
        // the result — so the running state and the process frames come from the
        // task it started, not from anything the call itself still has open.
        const det = result?.details as SubagentDetails | undefined;
        const bg = det?.background && det.id ? bgTasks[det.id] : undefined;
        const childRunIds = b.name === "subagent" ? new Set([det?.id, det?.run_id, bg?.task_id].filter((id): id is string => !!id)) : new Set<string>();
        const notice = [...childRunIds].map(id => taskNotices.get(id)).find(value => value && (!value.agentId || !det?.agent_id || value.agentId === det.agent_id));
        const childGates = live.pending_gates.filter(gate => !!gate.origin && childRunIds.has(gate.origin));
        calls.push({
          callId: b.id,
          name: b.name,
          args: b.input,
          ...(b.input_invalid === true ? { inputInvalid: true, inputError: b.input_error } : {}),
          result,
          running: b.input_invalid !== true && (pending !== undefined || bg?.status === "running"),
          ...(pending?.started_at !== undefined ? { startedAt: pending.started_at } : {}),
          ...(childGates.length ? { childGates } : {}),
          ...(notice ? { taskNotice: notice } : {}),
          liveOutput: pending?.output,
          liveFrames: bg ? bg.frames : (pending?.frames ?? result?.frames),
          gate: b.input_invalid === true ? undefined : live.pending_gates.find((g) => g.call_id === b.id),
          bgTask: bg,
          orphaned: b.input_invalid !== true && !result && pending === undefined && !live.active,
        });
      }
    }
    for (const source of collectSources(calls)) {
      // Stable source labels identify the same evidence for the whole
      // transcript. Keep the first reachable stamp if a label is repeated.
      if (!reachableSources.has(source.label)) reachableSources.set(source.label, source);
      exchangeSources.set(source.label, reachableSources.get(source.label)!);
    }
    const answerText = text.join("");
    items.push({
      kind: "turn",
      id: m.id,
      index: turnIndex,
      thinking: thinking.join(""),
      text: answerText,
      calls,
      streaming: false,
      ts: m.ts,
      thinkingMs: thinkingMs > 0 ? thinkingMs : undefined,
      ...(m.answer_review ? { answerReview: m.answer_review } : {}),
      sources: sourcesFor(answerText),
    });
  }

  // The turn in flight, before its message settles. Once `message` arrives the
  // server clears live.text, so this cannot duplicate anything.
  const liveReview = answerReview && (answerReview.message_id ? answerReview.message_id === live.message_id : answerReview.turn === live.turn) ? answerReview : undefined;
  if (live.active && !items.some(item => item.kind === "turn" && item.id === live.message_id) && ((live.text ?? "") !== "" || (live.thinking ?? "") !== "" || liveReview)) {
    items.push({
      kind: "turn",
      id: live.message_id ?? "live",
      index: turnIndex + 1,
      thinking: live.thinking ?? "",
      text: live.text ?? "",
      calls: [],
      streaming: true,
      ...(liveReview ? { answerReview: liveReview } : {}),
      sources: sourcesFor(live.text ?? ""),
    });
  }

  adoptOrphanGates(items, live, turnIndex);
  if (answerReview?.message_id) {
    const target = items.find(item => item.kind === "turn" && item.id === answerReview.message_id);
    if (target?.kind === "turn") target.answerReview = answerReview;
    else if (!live.active && answerReview.state !== "running") {
      // An interrupted request can finish without a final message. Keep its
      // real outcome visible without inventing model prose or reasoning.
      items.push({ kind: "turn", id: answerReview.message_id, index: answerReview.turn,
        thinking: "", text: "", calls: [], streaming: false, answerReview });
    }
  }
  adoptIncoming(items, live, turnIndex);
  // Join by the host task id. The saved tool result remains the original
  // acknowledgement; only this view acquires the later process outcome.
  for (const item of items) {
    if (item.kind !== "turn") continue;
    for (const call of item.calls) {
      const result = call.result, details = result?.details;
      if (call.name !== "bash" || !result || !isBashDetails(details) || !details.background || !details.task_id) continue;
      const task = shellTasks[details.task_id];
      const notice = taskNotices.get(details.task_id);
      if (!task || task.status === "running" && notice?.kind === "shell") {
        if (notice?.kind === "shell" && notice.status) {
          call.result = { ...result, text: notice.result ?? notice.error ?? "", is_error: notice.failed,
            details: { ...details, status: notice.status, exit_code: notice.exitCode, duration_ms: undefined } };
        } else {
          // The live cache is bounded. A historical ACK alone is no evidence
          // that its process is still running, or that it completed successfully.
          call.result = { ...result, details: { ...details, status: undefined, exit_code: undefined, duration_ms: undefined } };
        }
        continue;
      }
      call.result = {
        ...result,
        text: task.output_tail || (task.status === "running" ? result.text : task.error || ""),
        is_error: task.status !== "running" && task.status !== "completed",
        details: { ...details, status: task.status, exit_code: task.exit_code,
          duration_ms: task.ended_at ? Math.max(0, task.ended_at - task.started_at) : details.duration_ms,
          truncated: task.output_truncated },
      };
    }
  }
  linkCorrections(items);
  markSupersededTodos(items);
  return items;
}

/**
 * adoptOrphanGates surfaces an approval whose tool_use has not arrived yet.
 *
 * Gate events are published by the gate itself rather than through the loop's
 * event channel, so a gate_request can overtake the assistant message that
 * requested the call — it happens routinely, since the two are emitted
 * microseconds apart from different goroutines. A card that only rendered once
 * its call was known would blink into existence late, and after a log truncation
 * might never appear at all. The run is blocked on this card, so it has to show
 * up regardless.
 */
function adoptOrphanGates(items: TimelineItem[], live: Live, turnIndex: number): void {
  const known = new Set<string>();
  for (const item of items) {
    if (item.kind === "turn") for (const c of item.calls) known.add(c.callId);
  }
  const orphans = live.pending_gates.filter((g) => !known.has(g.call_id));
  if (orphans.length === 0) return;

  const host = hostForLive(items, live, turnIndex);
  for (const gate of orphans) {
    host.calls.push({
      callId: gate.call_id,
      name: gate.tool,
      args: gate.args,
      running: false,
      gate,
      orphaned: false,
    });
  }
}

/**
 * adoptIncoming puts the calls whose arguments are still streaming onto the
 * turn in flight, so the preview card sits exactly where the pending-tool
 * card will appear once tool_start lands. Like an orphan gate, an incoming
 * call has no tool_use block yet, so without adoption it would render
 * nowhere.
 */
function adoptIncoming(items: TimelineItem[], live: Live, turnIndex: number): void {
  if (!live.incoming?.length) return;
  const host = hostForLive(items, live, turnIndex);
  host.incoming = [...(host.incoming ?? []), ...live.incoming];
}

/** hostForLive finds or creates the turn item that represents the run in flight. */
function hostForLive(items: TimelineItem[], live: Live, turnIndex: number): TurnItem {
  const last = items[items.length - 1];
  if (last && last.kind === "turn") return last;
  const host: TurnItem = {
    kind: "turn",
    id: live.message_id ?? "live",
    index: turnIndex + 1,
    thinking: live.thinking ?? "",
    text: "",
    calls: [],
    streaming: true,
    sources: [],
  };
  items.push(host);
  return host;
}

/**
 * linkCorrections marks a successful call as the repair of an earlier failed one.
 *
 * This is pogo's signature behaviour: a tool failure is fed back as text and the
 * model fixes itself. Showing the two calls as unrelated failures hides the most
 * interesting thing the agent does. The match is intentionally narrow — same
 * tool, same target, later in time, and the earlier one failed.
 */
function linkCorrections(items: TimelineItem[]): void {
  const failed = new Map<string, string>(); // "tool\0target" -> callId
  for (const item of items) {
    if (item.kind !== "turn") continue;
    for (const call of item.calls) {
      const key = `${call.name}\u0000${targetOf(call.name, call.args)}`;
      if (call.result?.is_error) {
        failed.set(key, call.callId);
        continue;
      }
      if (call.result && !call.result.is_error) {
        const earlier = failed.get(key);
        if (earlier && earlier !== call.callId) {
          call.corrects = earlier;
          failed.delete(key);
        }
      }
    }
  }
}

/**
 * markSupersededTodos flags every task-list write except the last valid settled
 * one within each user task. A later question never supersedes an old task.
 *
 * A second pass rather than a check inside the projection, for the same reason
 * linkCorrections is one: "is this the newest" is a fact about the whole timeline,
 * and a loop building items one at a time cannot know it until it has finished.
 *
 * Only settled, successful writes count as the current plan. A rejected call
 * changed nothing — a list with two items in_progress never became the state — so
 * it must not demote the good list before it. A still-running one has not written
 * anything yet either, and letting it demote its predecessor would blank the plan
 * for as long as the call is in flight.
 */
function markSupersededTodos(items: TimelineItem[]): void {
  let newest: TimelineCall | undefined;
  for (const item of items) {
    // A new question owns a new plan. Steering continues the task already in
    // flight; notices (including soft-cap nudges) are not questions at all.
    if (item.kind === "user" && !item.steering) newest = undefined;
    if (item.kind !== "turn") continue;
    for (const call of item.calls) {
      if (call.name !== "todo") continue;
      // No result means nothing was written, which covers both a call still in
      // flight and one orphaned by a dead process — a `running` clause would be
      // unreachable here, since the result only arrives at tool_end.
      if (!call.result || call.result.is_error || !isTodoDetails(call.result.details)) continue;
      if (newest) newest.superseded = true;
      newest = call;
    }
  }
}

/** targetOf is what two calls have to agree on to be "the same work". */
export function targetOf(name: ToolName, args: unknown): string {
  const shortcutName = shortcutCallTool(name, args);
  if (shortcutName !== name) return `${name}:${shortcutName}:${targetOf(shortcutName, args)}`;
  const a = (args ?? {}) as Record<string, unknown>;
  if (name === "bash") return String(a.command ?? "");
  if (name.startsWith("agent_workflow_")) return JSON.stringify([a.workflow_id || "", a.run_id || "", a.action || "", a.expected_revision || "", a.request_id || ""]);
  if (name.startsWith("workflow_")) return JSON.stringify([a.workflow_id || "", a.action || "", a.expected_revision || "", a.request_id || ""]);
  if (name === "background_task") return JSON.stringify([a.source || "", a.action || "", a.task_id || a.script_path || a.title || ""]);
  if (name === "watch_test") return String(a.watch_id || a.script_path || "");
  if (name === "skill_search") return String(a.query || "");
  if (name === "skill_read") return `${String(a.skill || "")}:${String(a.path || "SKILL.md")}`;
  if (name === "skill_save") return String(a.skill_id || a.request_id || "");
  if (name === "shortcut_search") return String(a.query || a.source || "");
  if (name === "shortcut_read") return gt("shortcutTools.read");
  if (name === "shortcut_list") return String(a.parent_id || "");
  if (name === "shortcut_folder_create") return `${String(a.parent_id || "")}:${String(a.name || "")}`;
  if (name === "shortcut_update" || name === "shortcut_folder_rename") return String(a.id || "");
  if (name === "shortcut_move" || name === "shortcut_delete") return Array.isArray(a.ids) ? a.ids.map(String).sort().join(",") : "";
  if (name === "shortcut_merge") return String(a.plan_id || "");
  if (name === "shortcut_create") {
    const nodes = Array.isArray(a.nodes) ? a.nodes as Record<string, unknown>[] : [];
    return String(nodes.find(node => node?.local_id === a.root_id)?.title || gt("shortcutTools.create"));
  }
  return String(a.path ?? "");
}

/** shortArgValue renders one MCP argument compactly for the header label. */
function shortArgValue(v: unknown): string {
  const s = typeof v === "string" ? `"${v}"` : (JSON.stringify(v) ?? String(v));
  return s.length > 60 ? s.slice(0, 59) + "…" : s;
}

/** summarizeArgs is the one-line label next to a tool call. */
export function summarizeArgs(name: ToolName, args: unknown): string {
  const shortcutName = shortcutCallTool(name, args);
  if (shortcutName !== name) return summarizeArgs(shortcutName, args);
  const a = (args ?? {}) as Record<string, unknown>;
  if (name === "bash") return String(a.command ?? "");
  if (name.startsWith("agent_workflow_")) return [a.name, a.workflow_id, a.run_id, a.action].filter(Boolean).map(String).join(" · ");
  if (name.startsWith("workflow_")) return [a.name, a.workflow_id, a.query].filter(Boolean).map(String).join(" · ");
  if (name === "background_task") {
    const action = String(a.action || "");
    const known = ["prepare", "check", "create", "list", "status", "pause", "resume", "remove"].includes(action);
    const source = a.source === "schedule" || a.source === "sentinel" ? gt(`backgroundTools.${a.source}`) : "";
    return [gt(known ? `backgroundTools.${action}` : "backgroundTools.task"), source, String(a.title || a.script_path || a.task_id || "")].filter(Boolean).join(" · ");
  }
  if (name === "watch_test") return [gt("backgroundTools.check"), String(a.watch_id || a.script_path || "")].filter(Boolean).join(" · ");
  if (name === "skill_search") return String(a.query || gt("skillAuthoring.searchAction"));
  if (name === "skill_read") return String(a.path || "SKILL.md");
  if (name === "skill_save") return a.skill_id ? `${gt("skillAuthoring.updateAction")} · ${String(a.skill_id)}` : gt("skillAuthoring.createAction");
  if (name === "shortcut_search") return String(a.query || gt("shortcutTools.list"));
  if (name === "shortcut_read") return gt("shortcutTools.read");
  if (name === "shortcut_list") return `${gt("shortcutTools.list")} · ${String(a.parent_id || gt("shortcutTools.root"))}`;
  if (name === "shortcut_create") return targetOf(name, args);
  if (name === "shortcut_update") return `${gt("shortcutTools.update")} · ${String(a.title || a.id || "")}`;
  if (name === "shortcut_folder_create") return `${gt("shortcutTools.folderCreate")} · ${String(a.name || "")}`;
  if (name === "shortcut_folder_rename") return `${gt("shortcutTools.folderRename")} · ${String(a.name || a.id || "")}`;
  if (name === "shortcut_move" || name === "shortcut_delete") {
    const count = Array.isArray(a.ids) ? a.ids.length : 0;
    return `${gt(name === "shortcut_move" ? "shortcutTools.move" : "shortcutTools.delete")} · ${gt("shortcutTools.affected", { count })}`;
  }
  if (name === "shortcut_merge") return gt(a.apply === true ? "shortcutTools.merge" : "shortcutTools.mergePreview");
  // A delegation's label is its task: the first line says what was delegated,
  // which is the one thing a bare "subagent" does not. Not length-clipped: the
  // header wraps (ToolCall's .args is pre-wrap), so a long title shows in full
  // — cutting it mid-sentence hid what was delegated to save a line that
  // wrapping gets back for free.
  if (name === "subagent") {
    return String(a.task ?? "").split("\n", 1)[0].trim();
  }
  // An MCP tool's arguments are a free-form object owned by the external
  // server, so there is no fixed key to feature; the first couple of keys are
  // the useful part of the label. The wire name (mcp_<server>_<tool>) is never
  // parsed — it may be hash-truncated — it only marks the family.
  if (name.startsWith("mcp_")) {
    if (!args || typeof args !== "object" || Array.isArray(args)) return "";
    return Object.entries(args as Record<string, unknown>)
      .slice(0, 2)
      .map(([k, v]) => `${k}: ${shortArgValue(v)}`)
      .join(", ");
  }
  // A RuntimeSkill capability's arguments are likewise a free-form object
  // owned by the contract, so the first couple of keys label the call.
  // skill_read is excluded: its fixed path argument reads better below. (The
  // cast keeps the literal comparison honest — ToolName only lists built-ins.)
  if (name.startsWith("skill_") && (name as string) !== "skill_read") {
    if (!args || typeof args !== "object" || Array.isArray(args)) return "";
    return Object.entries(args as Record<string, unknown>)
      .slice(0, 2)
      .map(([k, v]) => `${k}: ${shortArgValue(v)}`)
      .join(", ");
  }
  const path = String(a.path ?? "");
  // ls defaults to the working directory, and a bare label reads as a missing
  // argument rather than as that default.
  if (name === "ls") return path || ".";
  // For a search the pattern is the point; the path is the qualifier.
  if (name === "find" || name === "grep") {
    const pattern = String(a.pattern ?? "");
    const include = name === "grep" && a.include ? ` (${String(a.include)})` : "";
    const where = path && path !== "." ? ` ${gt("timeline.searchIn", { path })}` : "";
    return `${pattern}${include}${where}`;
  }
  if (name === "read" && (a.offset || a.limit)) {
    const offset = Number(a.offset ?? 0);
    const limit = Number(a.limit ?? 0);
    return `${path}  ${offset + 1}${limit ? `-${offset + limit}` : "+"}`;
  }
  // A task-list write labels itself with the item being started. The list itself
  // is right below in the card, so repeating it on the header line would say
  // nothing; what the line can add is which of the items is the live one.
  if (name === "todo") {
    const todos = Array.isArray(a.todos) ? (a.todos as TodoItem[]) : [];
    const current = todos.find((t) => t?.status === "in_progress");
    if (current) return String(current.task ?? "");
    if (todos.length === 0) return gt("timeline.todoCleared");
    return todos.length === 1
      ? gt("timeline.todoCountOne", { n: todos.length })
      : gt("timeline.todoCount", { n: todos.length });
  }
  return path;
}

/** shortPath keeps the tail of a long path, which is the part that identifies it. */
export function shortPath(path: string, max = 44): string {
  if (path.length <= max) return path;
  return "…" + path.slice(path.length - max + 1);
}

export interface DiffPayload {
  diff: string;
  added: number;
  removed: number;
  path: string;
}

/**
 * diffOf pulls the rendered diff out of a tool's details. Only edit and write
 * produce one, and write only when it overwrote an existing file.
 */
export function diffOf(details: ToolDetails | undefined): DiffPayload | null {
  if (!details || !("diff" in details) || !details.diff) return null;
  return {
    diff: details.diff,
    added: details.added ?? 0,
    removed: details.removed ?? 0,
    path: details.path,
  };
}

export function isBashDetails(d: ToolDetails | undefined): d is Extract<ToolDetails, { command: string }> {
  return !!d && "command" in d;
}

/**
 * server+tool together belong only to an MCP call's details; the wire name is
 * not a reliable discriminator (it may be hash-truncated), so the pair of keys
 * is.
 */
export function isMCPDetails(d: ToolDetails | undefined): d is MCPDetails {
  return !!d && "server" in d && "tool" in d;
}

/**
 * capability+callId together belong only to a RuntimeSkill call's details
 * (skillruntime.CallResult); every key is always serialized, so the pair is
 * present even on the failure path, which carries only the capability.
 */
export function isSkillDetails(d: ToolDetails | undefined): d is SkillDetails {
  return !!d && "capability" in d && "callId" in d;
}

/** SubagentStep is one row of a delegated run, as the card shows it. */
export interface SubagentStep {
  kind: "turn" | "tool" | "end";
  label: string;
  detail?: string;
  bad?: boolean;
  /** Present on tool rows, so a result attaches to the call it belongs to. */
  callId?: string;
  done?: boolean;
  /** Tool rows: the tool's own name, so the result renderer can switch on it. */
  name?: string;
  /**
   * Tool rows: the one-line argument summary the parent timeline shows next to
   * its own calls. Absent when there is nothing useful to say (an empty
   * summary, or a tool_end whose start was dropped).
   */
  summary?: string;
  /**
   * Tool rows that finished: the call's output and structured details, so the
   * row can expand into what it actually produced — a read's lines, a command's
   * output, an edit's diff — rather than only that it happened.
   */
  text?: string;
  details?: ToolDetails;
  /** Only set when a frame/turn boundary provides a reliable child turn. */
  turn?: number;
  identity?: string;
  args?: unknown;
}

/**
 * childSessionId reduces a transcript path — the child's session frame and the
 * settled details both carry the full path — to the id the transcript endpoint
 * and the dock's subagent tab both key on.
 */
export function childSessionId(path: string | undefined): string | undefined {
  if (!path) return undefined;
  const base = path.split("/").pop() ?? "";
  const id = base.endsWith(".jsonl") ? base.slice(0, -".jsonl".length) : base;
  return id || undefined;
}

/**
 * subagentLiveness maps each delegation's child session id to whether it is
 * still running, read off the parent's own timeline: a synchronous call
 * between tool_start and tool_end, or a background task whose status says so.
 * The dock drives its subagent tabs' polling and status dots off this map —
 * the tab strip must not ask each sheet to guess.
 */
export function subagentLiveness(items: TimelineItem[]): Record<string, boolean> {
  const out: Record<string, boolean> = {};
  for (const item of items) {
    if (item.kind !== "turn") continue;
    for (const call of item.calls) {
      if (call.name !== "subagent") continue;
      const details = call.result?.details;
      const id =
        childSessionId(call.bgTask?.details?.session) ??
        childSessionId(isSubagentDetails(details) ? details.session : undefined) ??
        childSessionId((call.bgTask?.frames ?? call.liveFrames ?? []).find((f) => f.session)?.session);
      if (!id) continue;
      const running = call.bgTask ? call.bgTask.status === "running" : call.running;
      // An id could in principle recur (same transcript referenced twice); any
      // live sighting wins over a settled one.
      out[id] = (out[id] ?? false) || running;
    }
  }
  return out;
}

/**
 * subagentSteps projects a delegated run's events into rows.
 *
 * Here rather than in the component for the reason at the top of this file: it is a
 * projection of server state, and the one piece of it that has already been wrong
 * once. The first version paired tool_start with tool_end by tool name, and a live
 * run then produced a turn holding `ls start / read start / read ok / ls ok` — a
 * child runs calls in parallel, so completion order is not start order and names
 * repeat. Pairing on call_id is the fix, and this is where it can be pinned.
 */
export function subagentSteps(frames: SubagentFrame[] | undefined): SubagentStep[] {
  const out: SubagentStep[] = [];
  let turn: number | undefined;
  const validTurn = (value: number | undefined) => value !== undefined && Number.isSafeInteger(value) && value > 0 ? value : undefined;
  for (const f of frames ?? []) {
    const frameTurn = validTurn(f.turn);
    switch (f.type) {
      case "turn_start": {
        turn = frameTurn;
        if (turn !== undefined && out.some(step => step.kind === "turn" && step.turn === turn)) break;
        out.push({ kind: "turn", label: gt("timeline.turnLabel", { n: f.turn ?? "?" }), ...(turn !== undefined ? { turn, identity: `turn:${turn}` } : {}) });
        break;
      }
      case "tool_start": {
        const name = f.name ?? "tool";
        const knownTurn = frameTurn ?? turn;
        const identity = knownTurn !== undefined && f.call_id ? `turn:${knownTurn}:call:${f.call_id}` : undefined;
        if (identity && out.some(step => step.identity === identity)) break;
        // The same one-line summary the parent puts next to its own calls: an
        // empty one is dropped rather than rendered as a blank gap in the row.
        const summary = summarizeArgs(name as ToolName, f.args) || undefined;
        out.push({ kind: "tool", label: name, name, summary, callId: f.call_id,
          ...(f.args !== undefined ? { args: f.args } : {}),
          ...(knownTurn !== undefined ? { turn: knownTurn } : {}), ...(identity ? { identity } : {}) });
        break;
      }
      case "tool_end": {
        // Folded onto the call it belongs to rather than added as a second row: a
        // call that started and finished is one thing that happened, not two. The
        // `!done` guard matters when a child reuses ids across turns.
        const knownTurn = frameTurn ?? turn;
        const identity = knownTurn !== undefined && f.call_id ? `turn:${knownTurn}:call:${f.call_id}` : undefined;
        if (identity && out.some(step => step.identity === identity && step.done)) break;
        const started = f.call_id ? out.find((s) => s.kind === "tool" && s.callId === f.call_id && !s.done && (knownTurn === undefined || s.turn === knownTurn)) : undefined;
        const target =
          started ??
          (out.push({ kind: "tool", label: f.name ?? "tool", name: f.name, callId: f.call_id,
            ...(knownTurn !== undefined ? { turn: knownTurn } : {}), ...(identity ? { identity } : {}) }),
          out[out.length - 1]);
        target.done = true;
        target.bad = f.is_error === true || isBashDetails(f.details) && typeof f.details.exit_code === "number" && f.details.exit_code !== 0;
        target.detail = target.bad ? gt("timeline.failed") : undefined;
        // The output and details ride the same frame kinds the parent renders
        // its own results from, so an expanded row shows what the call produced.
        target.text = f.text;
        target.details = f.details;
        break;
      }
      case "run_end":
        out.push({
          kind: "end",
          label: f.error ? gt("timeline.endedError") : gt("timeline.ended"),
          detail: f.error || f.stop_reason,
          bad: Boolean(f.error),
        });
        break;
      default:
        // `session` is shown as a command in the footer, not as a row. Nothing else
        // is forwarded — see frameWorthForwarding in tools/subagent.go, which is an
        // allowlist precisely so this branch stays empty.
        break;
    }
  }
  return out;
}

/**
 * What the collapsed card shows while a delegation is live: the turn it is on,
 * and the newest tool call *of that turn*.
 *
 * The action is bounded by the turn row because a call from the previous turn
 * is stale news under a fresh "第 N 轮": between turn_start and the first
 * tool_start the child is generating, which no forwarded frame can corroborate
 * — so the honest second line there is no line. A turn row dropped to frame
 * capping is the opposite case: the calls survived, so the action shows alone.
 */
export function subagentLiveStrip(steps: SubagentStep[]): { turn?: SubagentStep; action?: SubagentStep } {
  let turn: SubagentStep | undefined;
  let action: SubagentStep | undefined;
  for (let i = steps.length - 1; i >= 0; i--) {
    const s = steps[i];
    if (s.kind === "turn") {
      turn = s;
      break;
    }
    if (s.kind === "tool" && !action) action = s;
  }
  return { turn, action };
}

/**
 * The child's live delta streams, from the coalesced tail frames: the newest
 * thinking frame and the newest token frame each carry their whole turn's
 * accumulated tail, so "the last one seen" is the complete current text —
 * which is what makes a mid-stream join (or a reconnect off the frame ring,
 * which keeps one slot per kind) render correctly.
 *
 * Deliberately per-turn on the server side: turn N's thinking is stale news
 * under turn N+1's header, and the same applies to the answer tail (the
 * settled answer is a different accumulator and never resets).
 */
export function subagentLiveTail(
  frames: SubagentFrame[] | undefined,
): { thinking?: string; answer?: string; streaming?: "thinking" | "token" } {
  let thinking: string | undefined;
  let answer: string | undefined;
  for (const f of frames ?? []) {
    if (f.type === "thinking" && f.text) thinking = f.text;
    else if (f.type === "token" && f.text) answer = f.text;
  }
  // Which stream is live right now: a delta frame is the newest event overall
  // exactly while the child is generating, so the collapsed card can prefer it
  // over the (possibly stale) newest tool call — the same rule the parent's own
  // live strip follows: thinking while it thinks, text while it answers.
  let streaming: "thinking" | "token" | undefined;
  const last = frames?.[frames.length - 1];
  if (last?.type === "thinking") streaming = "thinking";
  else if (last?.type === "token") streaming = "token";
  return { thinking, answer, streaming };
}

/** A run of these tools reads and never writes: exploration, not action. */
const exploreKinds = new Set(["ls", "find", "grep", "read"]);

/**
 * The expanded card's rows: the step list with runs of exploration folded into
 * one group row.
 *
 * A delegation that is going well is mostly reading — ls, grep, read, ls again
 * — and rendering each as its own row is the "terminal log pasted into a card"
 * look the card was criticised for. Cursor's "Explored N tools" is the lesson:
 * a run of reads is one thought, not N events. Only a run of 2+ folds — a
 * group of one is furniture, not a summary — and turn/end rows are boundaries,
 * because the turn header is what says where the group happened.
 */
export type SubagentRow =
  | { kind: "turn"; label: string }
  | { kind: "tool" | "end"; step: SubagentStep }
  | { kind: "explore"; steps: SubagentStep[] };

export function subagentRows(steps: SubagentStep[]): SubagentRow[] {
  return buildSubagentRows(steps, false);
}

/** Live cards keep the same group wrapper from the first read onward. The
 * heading can appear for 2+ calls without relocating already-open leaves. */
export function subagentActivityRows(steps: SubagentStep[]): SubagentRow[] {
  return buildSubagentRows(steps, true);
}

function buildSubagentRows(steps: SubagentStep[], groupSingle: boolean): SubagentRow[] {
  const rows: SubagentRow[] = [];
  let pending: SubagentStep[] = [];
  const flush = () => {
    if (pending.length >= (groupSingle ? 1 : 2)) rows.push({ kind: "explore", steps: pending });
    else for (const s of pending) rows.push({ kind: "tool", step: s });
    pending = [];
  };
  for (const s of steps) {
    if (s.kind === "tool" && exploreKinds.has(s.name ?? "")) {
      pending.push(s);
      continue;
    }
    flush();
    rows.push(s.kind === "turn" ? { kind: "turn", label: s.label } : { kind: s.kind, step: s });
  }
  flush();
  return rows;
}

/**
 * Subagent details, recognised by `mode` — the one field every delegated run has and
 * no other tool's details do, whether it committed anything or not.
 */
export function isSubagentDetails(d: ToolDetails | undefined): d is SubagentDetails {
  return !!d && "mode" in d;
}

export function isReadDetails(d: ToolDetails | undefined): d is Extract<ToolDetails, { total_lines: number }> {
  return !!d && "total_lines" in d && !("command" in d);
}

/**
 * A multi-file read. `files` is the discriminator, and it is the reason the Go type
 * has no top-level `total_lines`: with one, isReadDetails above would claim it first
 * and the single-file component would be handed a result it cannot draw.
 */
export function isReadManyDetails(d: ToolDetails | undefined): d is ReadManyDetails {
  return !!d && "files" in d && Array.isArray((d as ReadManyDetails).files);
}

/**
 * readBodies picks each file's content out of a multi-file read's text.
 *
 * A slice at recorded byte offsets, not a parse. The tool writes the offsets while it
 * writes the text (ReadFileDetails.BodyOffset), so the two cannot disagree — whereas
 * splitting on the `==> path <==` headers has a failure this must not have: a file
 * whose own contents contain such a line ends its section early, and the remainder is
 * attributed to the next file. A viewer showing one file's content under another
 * file's name is worse than showing none.
 *
 * `undefined` for an entry with no body: an unreadable path, or a transcript recorded
 * before the offsets existed. The component shows the row without content rather than
 * guessing at one.
 *
 * Go counts bytes and a JS string is indexed in UTF-16 code units, so the text is
 * encoded once and the ranges are taken from the bytes. Slicing the string directly
 * and validating the result's byte length is not enough, and the way it fails is
 * instructive: with three files where one holds CJK text, the wrong slice for the
 * next file was `"=> plain.go <"` — thirteen ASCII bytes — against a body of
 * `"done ✅ 🎉"`, also thirteen bytes. A length check passes and the reader sees one
 * file's fragment under another file's name, which is exactly the failure the
 * offsets exist to remove. So this does the conversion rather than checking it.
 */
export function readBodies(text: string, files: ReadFileDetails[]): (string | undefined)[] {
  if (!files.some((f) => f.body_offset && f.body_length)) return files.map(() => undefined);
  // Encoded once per result. The text is bounded by the tool's own output budget, so
  // this is a copy of tens of kilobytes, not of a file.
  const bytes = new TextEncoder().encode(text);
  const decoder = new TextDecoder();
  return files.map((f) => {
    if (!f.body_offset || !f.body_length) return undefined;
    const end = f.body_offset + f.body_length;
    // A range past the end means the text is not the one the offsets were measured
    // against. Nothing legitimate produces that, and a partial slice would be a
    // fragment presented as a file.
    if (end > bytes.byteLength) return undefined;
    return decoder.decode(bytes.subarray(f.body_offset, end));
  });
}

export function isLsDetails(d: ToolDetails | undefined): d is Extract<ToolDetails, { entries: number }> {
  return !!d && "entries" in d;
}

/**
 * isSearchDetails covers both find and grep, which share a shape apart from
 * grep's extra counters. The discriminator is "scanned": it is the field that
 * exists precisely because a search reports how much ground it covered, and no
 * other tool has anything like it.
 */
export function isSearchDetails(d: ToolDetails | undefined): d is Extract<ToolDetails, { scanned: number }> {
  return !!d && "scanned" in d;
}

/**
 * matchSkillRead reports which skill a read call was loading, if any.
 *
 * It matches on the call's arguments rather than on the result details, because
 * arguments live in the assistant message and therefore survive into the session
 * file, while details are dropped on the way to disk. A badge derived from details
 * would vanish on reload, which is precisely when you want to know why the agent
 * did what it did.
 */
export function matchSkillRead(
  name: ToolName,
  args: unknown,
  skills: { name: string; path: string }[],
  cwd: string,
): string | null {
  if (name !== "read" || skills.length === 0) return null;
  const raw = (args as { path?: unknown } | null)?.path;
  if (typeof raw !== "string" || raw === "") return null;
  const target = normalizePath(raw.startsWith("/") ? raw : `${cwd}/${raw}`);
  return skills.find((s) => normalizePath(s.path) === target)?.name ?? null;
}

// normalizePath resolves . and .. textually. Enough for comparing two paths the
// server produced; it is not a filesystem and does not follow symlinks, which is
// why the Go side does the authoritative matching for the terminal.
function normalizePath(path: string): string {
  const out: string[] = [];
  for (const part of path.split("/")) {
    if (part === "" || part === ".") continue;
    if (part === "..") out.pop();
    else out.push(part);
  }
  return "/" + out.join("/");
}

export interface SkillBlock {
  name: string;
  location: string;
  body: string;
  /** trailing is whatever the user typed after /skill:name. */
  trailing: string;
}

// The server wraps an explicit /skill:name invocation in <skill name= location=>.
// Parsing it back out is what lets a 400-line instruction sheet collapse to one
// line instead of burying the question the user actually asked.
const SKILL_BLOCK = /^<skill name="([^"]*)" location="([^"]*)">\n([\s\S]*?)\n<\/skill>(?:\n\n([\s\S]*))?$/;

export function parseSkillBlock(text: string): SkillBlock | null {
  const m = SKILL_BLOCK.exec(text.trim());
  if (!m) return null;
  let body = m[3];
  // The first line is the "References are relative to ..." note the server adds;
  // it is guidance for the model, not something a reader needs twice.
  const nl = body.indexOf("\n\n");
  if (body.startsWith("References are relative to ") && nl !== -1) {
    body = body.slice(nl + 2);
  }
  return { name: m[1], location: m[2], body, trailing: m[4] ?? "" };
}

export function isWriteDetails(d: ToolDetails | undefined): d is Extract<ToolDetails, { bytes: number }> {
  return !!d && "bytes" in d;
}

/**
 * An empty array or Go nil-slice null is a successful clear, including in
 * persisted history. Other values are malformed and must not replace a plan.
 */
export function isTodoDetails(d: ToolDetails | undefined): d is TodoDetails {
  return !!d && "todos" in d && (d.todos === null || Array.isArray(d.todos));
}

/**
 * liveTodos is the latest user task's newest settled list, or undefined when
 * that task has not written a plan. Earlier questions remain only in history.
 *
 * The current bar and historical summaries use the same projection, including
 * task boundaries. Inline snapshots use the same settled-successful write rule.
 *
 * An empty list is undefined, not an empty array. `todo` with no items means the
 * agent cleared its list, and a pinned bar reading "0/0" for the rest of the
 * session is worse than no bar: the point of pinning is that it says what is
 * happening now, and nothing is.
 */
export function liveTodos(items: TimelineItem[]): TodoItem[] | undefined {
  const plan = projectTaskTodos(items).current;
  return plan?.todos.length ? plan.todos : undefined;
}

/** A successful list write belongs to the question that started its task. */
export interface TaskTodoPlan {
  userId: string;
  callId: string;
  todos: TodoItem[];
}

/**
 * Replayable task plans, derived from the same transcript as the conversation.
 * No second persistent store: reload, reconnect and rewind all rebuild the same
 * association. Rejected, in-flight and orphaned writes never replace a plan.
 * A successful empty list is retained in history as "cleared", and hides the
 * pinned bar. Starting a new question hides the old bar even if it has no todo.
 *
 * Optimistic bubbles are not accepted questions: uploads or a queued follow-up
 * must not steal the running task's plan. The caller supplies their ids until
 * the server echoes user_message. Server-marked steering continues its parent.
 */
export function projectTaskTodos(items: TimelineItem[], optimisticUserIds: ReadonlySet<string> = new Set()): {
  byUser: Map<string, TaskTodoPlan>;
  current?: TaskTodoPlan;
  currentUserId: string;
} {
  const byUser = new Map<string, TaskTodoPlan>();
  let currentUserId = "head";
  let hasTask = false;
  let current: TaskTodoPlan | undefined;
  for (const item of items) {
    if (item.kind === "user") {
      if (optimisticUserIds.has(item.id)) continue;
      if (item.steering && hasTask) continue;
      currentUserId = item.id;
      hasTask = true;
      current = undefined;
      continue;
    }
    // A truncated/restored transcript may begin with turns or a harness nudge.
    // Its synthetic head is an existing task too, matching buildExchanges.
    hasTask = true;
    if (item.kind !== "turn") continue;
    for (const call of item.calls) {
      if (call.name !== "todo" || !call.result || call.result.is_error || !isTodoDetails(call.result.details)) continue;
      current = { userId: currentUserId, callId: call.callId, todos: call.result.details.todos ?? [] };
      byUser.set(currentUserId, current);
    }
  }
  return { byUser, current, currentUserId };
}

export function isEditDetails(d: ToolDetails | undefined): d is Extract<ToolDetails, { edits: number }> {
  return !!d && "edits" in d;
}

export function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  const m = Math.floor(ms / 60_000);
  return `${m}m${Math.round((ms % 60_000) / 1000)}s`;
}
