import { computed, ref, shallowRef, type Ref } from "vue";
import { api, authHeaders, streamURL } from "@/api/client";
import { normalizeMessageContent, normalizeMessages } from "@/api/messageContent";
import { isDelegatedEmbed, delegatedUpdates } from "@/api/delegated";
import { flushTreeInvalidate, invalidateIndex, scheduleTreeInvalidate } from "@/components/fileTreeStore";
import { bumpWorkspaceChanges } from "@/components/changesStore";
import { parseFrame } from "./sse";
import { createNewlineCounter } from "./argsPreview";
import { browserStreamBacklogThrough, createBrowserReveal, type BrowserEventDelivery } from "./browserView";
import { createBrowserFocusObserver } from "./browserFocus";
import { emptyLive, type AgentEvent, type AttachmentMeta, type BgTask, type InboxItem, type Live, type Message, type OverheadPart, type PolicyState, type RunInfo, type ShellJobRecord, type StarterCard, type ToolResult, type Usage } from "@/api/types";
import { liveAgentWorkflowUpdate, type AgentWorkflowUpdate } from "./agentWorkflowUpdates";
import { mergeInboxItem } from "@/agent/conversationInbox";
import { gt } from "@/i18n";
import { publishOfficeOpen, publishOfficeSync } from "./officeTransport";
import { publishBrowserDownloads } from "./browserDownloads";
import { foldSessionProgress, type SessionProgressEvent } from "./sessionProgress";
import { notifyStarterShortcutsChanged } from "./shortcutSources";
import { validStudioToolConfig } from "./studioToolSync";
import type { StudioToolConfigSync } from "./studioSessionCapabilities";
import type { AnswerReview } from "@/api/types";
import { finishAnswerReview, validAnswerReview } from "./answerReview";
import { shellJobSnapshot, validShellJobRecord } from "./shellTasks";

// liveThrottleMs bounds how often streaming deltas invalidate the timeline.
//
// Without it, every token rebuilds the whole view — the single biggest
// performance mistake in the reference implementation this UI is modelled on.
// Deltas still arrive individually; only the recomputation is batched.
const liveThrottleMs = 80;

// maxPendingOutput mirrors web/hub.go. Both sides cap the live output of a running
// call, and they have to agree: if the client kept more than a snapshot carries,
// reconnecting would visibly shrink what is on screen.
const maxPendingOutput = 32 * 1024;

// maxPendingFrames mirrors web/hub.go for the same reason, and is a count rather
// than a byte budget because a frame is a whole event: dropping the oldest costs one
// line of a delegated run's history, while cutting bytes would leave a fragment that
// cannot be rendered at all.
const maxPendingFrames = 400;

// maxIncomingHead/maxIncomingTail cap the raw argument text kept for a call that
// is still being generated. They mirror the server's caps on the snapshot's
// live.incoming for the same reason as maxPendingOutput: disagree, and a
// reconnect mid-stream would visibly change what the preview card shows.
const maxIncomingHead = 4096;
const maxIncomingTail = 8192;

// fsMutatingTools are the calls whose tool_end can change the workspace
// listing: the file panel refreshes (debounced) when one settles, so a file
// the agent just wrote shows up without a manual refresh. Mirrors the gated
// set in tools/guard.go — the read-only tools never change a byte.
const fsMutatingTools = new Set(["write", "edit", "bash", "subagent"]);

/** clipTail keeps the last n characters, starting at a line boundary when one is near. */
export function clipTail(s: string, n: number): string {
  if (s.length <= n) return s;
  const tail = s.slice(s.length - n);
  const nl = tail.indexOf("\n");
  return nl >= 0 && nl < 200 ? tail.slice(nl + 1) : tail;
}

export interface RetryNotice {
  attempt: number;
  max: number;
  delayMs: number;
  reason: string;
  at: number;
}

/**
 * Outage is the detail behind the unreachable page: how many reconnect
 * attempts have fired, whether auto-retry gave up (a refused request stays
 * refused), and the last error text.
 */
export interface Outage {
  attempts: number;
  gaveUp: boolean;
  message: string;
}

// outageGraceMs is how long the link may flap before the error page takes
// over. A sub-second blip — a reload, a wifi hop — must not flash a
// full-screen error; a browser's error page does not appear during loading
// either.
const outageGraceMs = 2500;

/**
 * One vision-helper description of an attached image — the visible step one
 * of an image turn when the conversation model cannot see images itself.
 */
export interface ImageDescribe {
  ref: string;
  name: string;
  status: "running" | "done" | "failed";
  startedAt: number;
  durationMs?: number;
  /** The description, once the helper answered. Also the image's alt. */
  text?: string;
  /** Why the step failed; the run continues with a placeholder alt. */
  error?: string;
}

/**
 * useAgentStream keeps one session's state in sync with the server.
 *
 * It is a client-side mirror of the server's event fold (web/hub.go), which is
 * why a snapshot and a live stream can never disagree: both are the same state
 * machine, applied in the same order.
 */
export function useAgentStream(options: { attachments?: boolean } = {}) {
  const messages = ref<Message[]>([]);
  // The selected target may change before its snapshot arrives. Components
  // retain the old transcript during that gap, so its UI state needs this id.
  const displaySessionId = ref<string | null>(null);
  const results = ref<Record<string, ToolResult>>({});
  const live = ref<Live>(emptyLive());
  const run = ref<RunInfo>({ active: false });
  const answerReview = shallowRef<AnswerReview | null>(null);
  // Source previews refresh after a file-mutating run settles, rather than
  // reloading between the writes that make up one requested change.
  const workspaceRevision = ref(0);
  const agentWorkflowUpdate = shallowRef<AgentWorkflowUpdate | null>(null);
  let workspaceDirty = false;
  const progressEvents = ref<SessionProgressEvent[]>([]);
  // A policy update can keep the same current model while changing the list
  // of allowed models. Consumers must also observe those model_changed events.
  const modelChangeVersion = ref(0);
  const toolConfig = shallowRef<{ sessionId: string; config: StudioToolConfigSync } | null>(null);
  const policy = ref<PolicyState>({ mode: "standard" });
  const usage = ref<Usage>({ input: 0, output: 0 });
  // contextTokens is the latest turn's prompt size. Kept apart from `usage` on
  // purpose: that one is the running total and says nothing about how full the
  // window is.
  const contextTokens = ref(0);
  // A previous model's tokenizer count is useful only as an approximation.
  // Persisted snapshots carry the same flag, so reconnecting cannot relabel it.
  const contextEstimated = ref(false);
  // overheadTokens is the server's fixed-cost estimate (system prompt + tool
  // schemas), and overheadBreakdown what it is made of. Both arrive with a
  // snapshot, a model change, and a turn_start whose tool view changed them
  // (a Skill that brought tools, an MCP tool selected).
  const overheadTokens = ref(0);
  const overheadBreakdown = ref<OverheadPart[]>([]);
  // clearTrigger is the prompt size at which the server starts dropping old tool
  // results, or 0 when clearing is off. Only the server knows it — "auto" is a
  // fraction of the current model's window — and the gauge needs it because clearing
  // holds occupancy just below it.
  const clearTrigger = ref(0);
  // Backgrounded delegations, keyed by task id. Apart from `live` for the same
  // reason the server keeps them out of Live: they outlive every run, and
  // `live` is wiped at each run's end.
  const bgTasks = ref<Record<string, BgTask>>({});
  // A process may keep running after its dispatching tool and run have ended.
  const shellTasks = ref<Record<string, ShellJobRecord>>({});
  function acceptShellTask(task: ShellJobRecord) {
    if (!validShellJobRecord(task)) return;
    const previous = shellTasks.value[task.task_id];
    if (previous && previous.revision >= task.revision) return;
    // Terminal records cannot be turned back into running jobs by a late reply.
    if (previous && previous.status !== "running" && task.status === "running") return;
    shellTasks.value = { ...shellTasks.value, [task.task_id]: { ...task } };
    if (task.status !== "running" && (!previous || previous.status === "running")) {
      workspaceRevision.value++;
      scheduleTreeInvalidate();
      bumpWorkspaceChanges();
    }
  }
  const seq = ref(0);

  const connected = ref(false);
  const streamError = ref<string | null>(null);
  function runNotice(): string | null {
    if (switching.value || run.value.active) return null;
    if (run.value.end_reason === "max_tokens") return gt("stream.outputLimited");
    if (run.value.end_reason === "transport_error") {
      const notice = gt("stream.responseInterrupted");
      return run.value.error ? `${notice}\n${run.value.error}` : notice;
    }
    return run.value.error || null;
  }
  // unreachable drives the full-page "cannot connect" view; outage carries its
  // detail row. connected=false alone is transient — the page waits out the
  // grace window.
  const unreachable = ref(false);
  const outage = ref<Outage | null>(null);
  const retry = shallowRef<RetryNotice | null>(null);
  const policyReverted = ref<string | null>(null);
  // Steering messages a run accepted but never delivered. The view clears this
  // once it has handed the text back to the user.
  const undelivered = ref<string[]>([]);
  const inbox = shallowRef<InboxItem[]>([]);
  function acceptInboxItem(item: InboxItem) { inbox.value = mergeInboxItem(inbox.value, item); }
  // Server-generated follow-up chips, kept with the id of the assistant message
  // they were made for so the view can ignore a late event for a stale turn.
  // Empty means the skills' rule matching decides what, if anything, to offer.
  const followups = ref<{ messageId: string | null; chips: StarterCard[] }>({ messageId: null, chips: [] });
  // Optimistic user bubbles: drawn the instant send is clicked, retired when
  // the server echoes their client_id on user_message. Held apart from
  // `messages` on purpose — a snapshot can never contain them, so folding one
  // in wholesale would swallow a send still in flight. The view renders
  // messages + pendingUser; everything else reads `messages` alone.
  const pendingUser = ref<Message[]>([]);
  // The session's uploads as the panes know them: seeded by the GET listing on
  // connect, grown by attachment_uploaded events. The session-files popover
  // reads it, which is what lets a file show before any message carries it.
  const uploads = ref<AttachmentMeta[]>([]);
  // Step one of an image turn on a text-only conversation model: one vision-
  // helper description per image, folded from image_describe_start/end.
  // Cleared at run_end — the settled text lives on as the user message's
  // image_alt, which the end event patches in below.
  const describes = ref<ImageDescribe[]>([]);
  // switching is set when a session change begins and cleared by the new
  // session's first snapshot: the view holds the old conversation (faded)
  // instead of blanking it, which is what used to make switching flicker.
  const switching = ref(false);

  // serverOffset is the estimated difference between the server's clock and
  // this browser's (server − browser, ms). The hub stamps every event with
  // the server's own time, so each event that arrives is one sample of the
  // difference, biased low by delivery latency — which is why the estimate is
  // the maximum over a sliding window, never an average. The live "working
  // for" clock runs on this estimated server time, so a skewed server clock
  // (an hour off is a real thing that happens) never shows a run as having
  // started in the past. null until the first event arrives; callers fall
  // back to browser time then, which is the old behaviour.
  const serverOffset = ref<number | null>(null);
  const clockSamples: number[] = [];

  function sampleClock(ts: number) {
    if (!ts) return;
    clockSamples.push(ts - Date.now());
    if (clockSamples.length > 32) clockSamples.shift();
    serverOffset.value = Math.max(...clockSamples);
  }

  // serverNow is the server's clock as this browser can best estimate it.
  const serverNow = () => Date.now() + (serverOffset.value ?? 0);

  // liveVersion is bumped at most every liveThrottleMs so consumers can memoise
  // on it instead of on the delta stream.
  const liveVersion = ref(0);
  // Bare setTimeout rather than window.setTimeout: the fold is exercised by unit
  // tests that replay a recorded stream, and those run without a DOM.
  let liveTimer: ReturnType<typeof setTimeout> | null = null;
  const touchLive = () => {
    if (liveTimer !== null) return;
    liveTimer = setTimeout(() => {
      liveTimer = null;
      liveVersion.value++;
    }, liveThrottleMs);
  };
  const flushLive = () => {
    if (liveTimer !== null) {
      clearTimeout(liveTimer);
      liveTimer = null;
    }
    liveVersion.value++;
  };

  let controller: AbortController | null = null;
  const browserReveal = createBrowserReveal();
  const browserFocus = createBrowserFocusObserver();
  let streamEpoch = 0;
  let streamInstance: string | undefined;
  let authListener = false;
  let sessionId: string | null = null;
  let closedByUs = false;
  let delegatedEpoch = 0;
  let backoff = 500;
  let attempts = 0;
  let outageTimer: ReturnType<typeof setTimeout> | null = null;

  // The liveness watchdog. The server comments on the stream every 20s
  // (keepalive in web/server.go), so a connection that delivers nothing for
  // twice that is dead — whether or not the socket admits it. A silently
  // dropped proxy link never errors and never ends; it just stops delivering,
  // and the page looks alive while every event freezes. Tripping the watchdog
  // aborts the reader, which takes the ordinary outage path: noteOutage, then
  // a reconnect that resumes from the last sequence number.
  const watchdogSilentMs = 45_000;
  const watchdogTickMs = 15_000;
  let lastBytesAt = 0;
  let watchdog: ReturnType<typeof setInterval> | null = null;
  let watchdogTripped = false;

  function startWatchdog() {
    stopWatchdog();
    lastBytesAt = Date.now();
    watchdogTripped = false;
    watchdog = setInterval(() => {
      if (Date.now() - lastBytesAt > watchdogSilentMs) {
        watchdogTripped = true;
        controller?.abort();
      }
    }, watchdogTickMs);
  }

  function stopWatchdog() {
    if (watchdog !== null) {
      clearInterval(watchdog);
      watchdog = null;
    }
  }

  // noteOutage records one failed (or ended) connection attempt. The error
  // page appears immediately when retrying is pointless, otherwise only after
  // the grace window — the timer is shared, so a flapping link arms it once.
  function noteOutage(message: string, gaveUp: boolean) {
    attempts++;
    outage.value = { attempts, gaveUp, message };
    if (gaveUp) {
      unreachable.value = true;
      return;
    }
    if (outageTimer === null && !unreachable.value) {
      outageTimer = setTimeout(() => {
        outageTimer = null;
        unreachable.value = true;
      }, outageGraceMs);
    }
  }

  // clearOutage runs on a successful connect and when the session is dropped:
  // the page closes, the counters reset, the grace timer disarms.
  function clearOutage() {
    attempts = 0;
    outage.value = null;
    unreachable.value = false;
    if (outageTimer !== null) {
      clearTimeout(outageTimer);
      outageTimer = null;
    }
  }

  // One newline counter per still-streaming call. base seeds a counter made
  // for a snapshot-restored entry: the server already counted those lines, so
  // the client counter only adds what arrives after the reconnect.
  const incomingCounters = new Map<string, { counter: ReturnType<typeof createNewlineCounter>; base: number }>();

  // A session switch does NOT reset: the old conversation stays on screen
  // (faded, see `switching`) until the new session's snapshot arrives. The
  // snapshot below overwrites every field wholesale, so nothing stale can
  // survive it — and nothing blank precedes it.
  function apply(e: AgentEvent, delivery?: BrowserEventDelivery) {
    sampleClock(e.ts);
    progressEvents.value = foldSessionProgress(progressEvents.value, e);
    if (e.seq) seq.value = e.seq;
    if (!isDelegatedEmbed) {
      const observed = delivery ?? { sessionId: null, replay: true };
      browserReveal.observe(e, observed);
      browserFocus.observe(e, observed);
    }

    switch (e.type) {
      case "shortcuts_changed": {
        // The event carries no content or permissions. Open panels refetch via
        // their own authenticated Agent scope; an editor draft is untouched.
        if (!delivery || delivery.replay || !e.shortcut_agent_id) return;
        notifyStarterShortcutsChanged(e.shortcut_agent_id);
        return;
      }
      case "office_open": {
        if (!delivery || delivery.replay || delivery.sessionId !== e.conversation_id || !e.document_session_id || !e.path || !["docx", "xlsx"].includes(e.file_type || "")) return;
        publishOfficeOpen({ id: e.document_session_id, conversationId: e.conversation_id!, path: e.path, fileType: e.file_type! });
        return;
      }
      case "office_sync": {
        if (!delivery || delivery.replay || delivery.sessionId !== e.conversation_id || !e.document_session_id || !e.path || !["docx", "xlsx"].includes(e.file_type || "")) return;
        publishOfficeSync({ id: e.document_session_id, conversationId: e.conversation_id!, path: e.path, fileType: e.file_type! });
        return;
      }
      case "snapshot": {
        const s = e.snapshot;
        if (!s) return;
        streamInstance = typeof s.stream_instance === 'string' && /^[A-Za-z0-9_-]{16,128}$/.test(s.stream_instance) ? s.stream_instance : undefined;
        displaySessionId.value = delivery?.sessionId ?? sessionId;
        workspaceDirty = false;
        messages.value = normalizeMessages(s.messages);
        const restoredAnswers = s.user_input_answers ?? {};
        results.value = Object.fromEntries(Object.entries(s.results ?? {}).map(([callID, result]) => [callID,
          restoredAnswers[callID] ? { ...result, details: restoredAnswers[callID] } : result,
        ]));
        live.value = { ...emptyLive(), ...s.live, pending_user_inputs: s.live?.pending_user_inputs ?? [] };
        incomingCounters.clear();
        run.value = s.run ?? { active: false };
        answerReview.value = validAnswerReview(s.answer_review) ? { ...s.answer_review } : null;
        if (answerReview.value?.state === "running" && !run.value.active) {
          answerReview.value = finishAnswerReview(answerReview.value, e.ts);
        }
        policy.value = s.policy ?? { mode: "standard" };
        usage.value = s.usage ?? { input: 0, output: 0 };
        bgTasks.value = Object.fromEntries((s.bg_tasks ?? []).map((t) => [t.task_id, t]));
        shellTasks.value = shellJobSnapshot(s.shell_tasks);
        contextTokens.value = s.context_tokens ?? 0;
        contextEstimated.value = s.context_estimated ?? false;
        overheadTokens.value = s.overhead_tokens ?? 0;
        overheadBreakdown.value = s.overhead_breakdown ?? [];
        clearTrigger.value = s.clear_trigger ?? 0;
        seq.value = s.seq;
        retry.value = null;
        policyReverted.value = null;
        undelivered.value = [];
        inbox.value = s.inbox ?? [];
        // Wholesale like every other field: a snapshot without followups means
        // the session has none, not that the old ones should linger.
        followups.value = { messageId: s.followups?.message_id ?? null, chips: s.followups?.chips ?? [] };
        // A snapshot is the whole truth: whatever describe steps the live
        // stream had in flight are either replayed (logged events) or over.
        describes.value = [];
        switching.value = false;
        streamError.value = runNotice();
        flushLive();
        return;
      }

      case "run_start":
        answerReview.value = null;
        streamError.value = null;
        workspaceDirty = false;
        live.value = { ...emptyLive(), pending_gates: live.value.pending_gates.filter(g => !!g.origin), pending_user_inputs: live.value.pending_user_inputs.filter(item => item.async), run_id: e.run_id, active: true };
        run.value = { ...run.value, active: true, run_id: e.run_id, model: e.model ?? run.value.model, end_reason: undefined, error: undefined };
        retry.value = null;
        flushLive();
        return;

      case "inbox_updated":
        if (e.item) acceptInboxItem(e.item);
        return;

      case "user_message": {
        // Content carries the attachment blocks (images before text, file notes
        // after) exactly like the server's own fold in hub.go — falling back to
        // the compact text-only form only when the event has none. Dropping it
        // here is what used to make chips appear only after a reload.
        const content = normalizeMessageContent(e.content, e.text ?? "");
        // The sender's placeholder retires on the echo of its client_id; every
        // other client (a second tab, an old build) just appends the settled
        // message as before.
        if (e.client_id) {
          pendingUser.value = pendingUser.value.filter((m) => m.id !== e.client_id);
        }
        messages.value = [
          ...messages.value,
          { id: e.message_id ?? `u${messages.value.length}`, role: "user", content, ts: e.ts,
            ...(e.inbox_id ? { inbox_id: e.inbox_id } : {}),
            ...(e.run_id ? { run_id: e.run_id } : {}),
            ...(e.input_layout ? { input_layout: e.input_layout } : {}),
            ...(e.steering ? { steering: true } : {}) },
        ];
        // A new question starts a new turn; the previous turn's chips belong
        // to it and must not resurface once this run ends.
        followups.value = { messageId: null, chips: [] };
        flushLive();
        return;
      }

      case "turn_start":
        live.value = { ...live.value, turn: e.turn, message_id: e.message_id, text: "", thinking: "" };
        if (e.overhead_tokens !== undefined) {
          overheadTokens.value = e.overhead_tokens;
          overheadBreakdown.value = e.overhead_breakdown ?? [];
        }
        flushLive();
        return;

      case "token":
        live.value.text = (live.value.text ?? "") + (e.text ?? "");
        touchLive();
        return;

      case "thinking":
        live.value.thinking = (live.value.thinking ?? "") + (e.text ?? "");
        touchLive();
        return;

      case "answer_review":
        if (!validAnswerReview(e.answer_review)) return;
        answerReview.value = { ...e.answer_review };
        flushLive();
        return;

      case "message": {
        // A message's usage is that turn's own report, so its prompt size is the
        // current context occupancy.
        if (e.usage?.input) {
          contextTokens.value = e.usage.input;
          contextEstimated.value = false;
        }
        // The settled message supersedes whatever was accumulated, so the live
        // copy is dropped rather than rendered twice.
        const settledReview = validAnswerReview(e.answer_review) ? e.answer_review :
          answerReview.value && answerReview.value.message_id === e.message_id ? answerReview.value : null;
        messages.value = [
          ...messages.value,
          { id: e.message_id ?? `m${messages.value.length}`, role: e.role || "assistant", content: normalizeMessageContent(e.content, e.text), ts: e.ts,
            ...(settledReview ? { answer_review: { ...settledReview } } : {}) },
        ];
        live.value = { ...live.value, text: "", thinking: "" };
        flushLive();
        return;
      }

      // Raw argument fragments of a call whose tool_start has not arrived yet:
      // a big `write` streams for minutes and this is the only sign of life in
      // that gap. Accumulated and throttled like tool_partial, and capped like
      // the server caps it, so a snapshot mid-stream matches what is on screen.
      case "tool_args": {
        if (!e.call_id) return;
        const list = (live.value.incoming ??= []);
        let entry = list.find((t) => t.call_id === e.call_id);
        if (!entry) {
          entry = { call_id: e.call_id, name: e.name ?? "", bytes: 0, ts: e.ts };
          list.push(entry);
        } else if (e.name && !entry.name) {
          // Only the first fragment names the tool, but a late join can see the
          // name arrive after the entry already exists.
          entry.name = e.name;
        }
        const text = e.text ?? "";
        entry.bytes += text.length;
        if (text) {
          let slot = incomingCounters.get(e.call_id);
          if (!slot) {
            slot = { counter: createNewlineCounter(), base: entry.lines ?? 0 };
            incomingCounters.set(e.call_id, slot);
          }
          entry.lines = slot.base + slot.counter.push(text);
        }
        const head = entry.head ?? "";
        if (text && head.length < maxIncomingHead) {
          // The head's only job is to carry the path, so it stops growing once
          // full rather than shadowing the tail's budget.
          entry.head = head + text.slice(0, maxIncomingHead - head.length);
        }
        if (text) entry.tail = clipTail((entry.tail ?? "") + text, maxIncomingTail);
        touchLive();
        return;
      }

      case "tool_start":
        live.value.pending_tools = [
          ...live.value.pending_tools,
          { call_id: e.call_id!, name: e.name!, args: e.args, started_at: e.ts },
        ];
        // The arguments are done generating: the pending-tool card takes over
        // from the incoming preview.
        if (live.value.incoming) {
          live.value.incoming = live.value.incoming.filter((t) => t.call_id !== e.call_id);
        }
        incomingCounters.delete(e.call_id!);
        flushLive();
        return;

      // Appended in place and throttled like the token deltas: a command can
      // print far faster than a browser can usefully repaint. Only the tail is
      // kept, matching what the server puts in a snapshot, so a reconnect does not
      // change how much is on screen.
      case "tool_partial": {
        const call = live.value.pending_tools.find((t) => t.call_id === e.call_id);
        if (!call) return; // a fragment for a call that already settled
        if (e.text) call.output = clipTail((call.output ?? "") + e.text, maxPendingOutput);
        // A subagent reports structured events rather than bytes. Bounded by count
        // and mirroring the server's cap, so a reconnect does not change how much
        // history is on screen.
        if (e.frame) {
          const frames = [...(call.frames ?? []), e.frame];
          call.frames = frames.length > maxPendingFrames ? frames.slice(-maxPendingFrames) : frames;
        }
        touchLive();
        return;
      }

      case "tool_end": {
        const workflowUpdate = liveAgentWorkflowUpdate(e, delivery);
        if (workflowUpdate && (!sessionId || workflowUpdate.sessionId === sessionId) &&
          (agentWorkflowUpdate.value?.sessionId !== workflowUpdate.sessionId || workflowUpdate.seq > agentWorkflowUpdate.value.seq)) agentWorkflowUpdate.value = workflowUpdate;
        // A subagent's frames move onto the result before the pending call goes:
        // they are the run's process and the result is its answer, so unlike
        // live output — whose bytes the result replaces — keeping both renders
        // nothing twice, and the card does not empty out the moment it settles.
        const settled = live.value.pending_tools.find((t) => t.call_id === e.call_id);
        live.value.pending_tools = live.value.pending_tools.filter((t) => t.call_id !== e.call_id);
        // Defensive: a tool_end whose tool_start was missed (log truncation)
        // must not leave the incoming preview behind either.
        if (live.value.incoming) {
          live.value.incoming = live.value.incoming.filter((t) => t.call_id !== e.call_id);
        }
        incomingCounters.delete(e.call_id!);
        results.value = {
          ...results.value,
          [e.call_id!]: {
            call_id: e.call_id!,
            name: e.name,
            text: e.text ?? "",
            is_error: e.is_error,
            details: e.details,
            duration_ms: e.duration_ms,
            source: e.source,
            frames: settled?.frames,
          },
        };
        flushLive();
        // A settled mutating call may have added, renamed or deleted entries;
        // the tree hears about it debounced — failures too, since a half-run
        // command can still have written something. The changes panel hears
        // the same news through its own tick so journalled changes refresh.
        if (e.name && fsMutatingTools.has(e.name)) {
          workspaceDirty = true;
          scheduleTreeInvalidate();
          bumpWorkspaceChanges();
        }
        return;
      }

      case "shell_task_start":
      case "shell_task_output":
      case "shell_task_end":
        if (e.shell_task) acceptShellTask(e.shell_task);
        return;

      // A backgrounded subagent belongs to the session, not to any run, so its
      // progress arrives on its own events rather than on tool_partial. Folded
      // into bgTasks, which the timeline joins to the dispatching call's card.
      case "subagent_start": {
        if (!e.task_id) return;
        // A duplicate start — a replay after a reconnect — must not wipe the
        // frames the live stream already accumulated: they are not in the
        // replay log, so resetting here would blank the card for good.
        const existing = bgTasks.value[e.task_id];
        if (!existing) {
          bgTasks.value = {
            ...bgTasks.value,
            [e.task_id]: {
              task_id: e.task_id,
              mode: e.name ?? "",
              task: e.text ?? "",
              status: "running",
              started_at: e.ts,
              frames: [],
            },
          };
        }
        flushLive();
        return;
      }

      case "subagent_frame": {
        const task = e.task_id ? bgTasks.value[e.task_id] : undefined;
        if (!task || !e.frame) return; // a frame for a task that already settled
        // Appended in place and throttled like tool_partial, and capped the same
        // way the server caps it, so a reconnect does not change what is on screen.
        const frames = [...(task.frames ?? []), e.frame];
        task.frames = frames.length > maxPendingFrames ? frames.slice(-maxPendingFrames) : frames;
        touchLive();
        return;
      }

      case "subagent_end": {
        const task = e.task_id ? bgTasks.value[e.task_id] : undefined;
        if (task) {
          // The end carries the hub's capped frame list: a client that missed
          // frames live (they are never replayed) adopts the fuller copy,
          // which is what heals a card frozen mid-run by a dropped connection.
          const frames =
            (e.frames?.length ?? 0) > (task.frames?.length ?? 0) ? e.frames : task.frames;
          bgTasks.value = {
            ...bgTasks.value,
            [task.task_id]: {
              ...task,
              status: e.is_error ? "failed" : "done",
              error: e.error,
              details: e.details as BgTask["details"],
              duration_ms: e.duration_ms,
              frames,
            },
          };
        }
        // The agent's own counters never include background spend; the run_end
        // event carries it summed (see web/hub.go), and here it is added.
        if (e.usage) {
          usage.value = {
            input: usage.value.input + (e.usage.input ?? 0),
            output: usage.value.output + (e.usage.output ?? 0),
            cache_read: (usage.value.cache_read ?? 0) + (e.usage.cache_read ?? 0),
            reasoning: (usage.value.reasoning ?? 0) + (e.usage.reasoning ?? 0),
          };
        }
        // A backgrounded edit child can have changed files without any tool_end
        // in this session to invalidate the tree on.
        workspaceRevision.value++;
        scheduleTreeInvalidate();
        flushLive();
        return;
      }

      case "gate_request":
        if (!e.gate_id || !e.name) return;
        live.value.pending_gates = [
          ...live.value.pending_gates.filter(g => g.gate_id !== e.gate_id),
          {
            gate_id: e.gate_id!,
            call_id: e.call_id ?? "",
            tool: e.name!,
            args: e.args,
            deadline: e.deadline ?? 0,
            danger: e.danger,
            origin: e.origin,
            frozen: e.frozen,
            remaining_ms: e.remaining_ms,
          },
        ];
        flushLive();
        return;

      case "user_input_request":
        if (!e.user_input) return;
        live.value.pending_user_inputs = [
          ...live.value.pending_user_inputs.filter(item => item.request_id !== e.user_input!.request_id),
          e.user_input,
        ];
        flushLive();
        return;

      case "user_input_resolved": {
        const resolution = e.user_input_resolution;
        if (!resolution) return;
        const pending = live.value.pending_user_inputs.find(item => item.request_id === resolution.request_id);
        live.value.pending_user_inputs = live.value.pending_user_inputs.filter(item => item.request_id !== resolution.request_id);
        if (resolution.async && resolution.call_id && results.value[resolution.call_id]) {
          const current = results.value[resolution.call_id];
          const previous = current.details && typeof current.details === "object" && "questions" in current.details ? current.details.questions : [];
          results.value[resolution.call_id] = { ...current, details: {
            request_id: resolution.request_id, call_id: resolution.call_id, status: resolution.status, async: true,
            questions: pending?.questions ?? previous,
            ...(resolution.answers ? { answers: resolution.answers } : {}),
          } };
        }
        flushLive();
        return;
      }

      case "gate_deadline":
        live.value.pending_gates = live.value.pending_gates.map((g) =>
          g.gate_id === e.gate_id ? { ...g, deadline: e.deadline ?? g.deadline,
            frozen: e.frozen ?? g.frozen, remaining_ms: e.remaining_ms ?? g.remaining_ms } : g,
        );
        flushLive();
        return;

      case "gate_resolved":
        live.value.pending_gates = live.value.pending_gates.filter((g) => g.gate_id !== e.gate_id);
        flushLive();
        return;

      case "policy_changed":
        if (e.policy) policy.value = e.policy;
        return;

      case "policy_reverted":
        if (e.policy) policy.value = e.policy;
        policyReverted.value = e.reason ?? "auto mode expired";
        return;

      case "tool_config":
        if (!delivery?.sessionId || delivery.replay || !validStudioToolConfig(e.tool_config)) return;
        if (sessionId && delivery.sessionId !== sessionId) return;
        toolConfig.value = { sessionId: delivery.sessionId, config: { ...e.tool_config, tools: [...e.tool_config.tools] } };
        return;

      case "model_config_pending":
        run.value = { ...run.value, model_config_pending: e.model_config_pending === true };
        return;

      case "model_changed":
        modelChangeVersion.value++;
        if (e.clear_trigger !== undefined) clearTrigger.value = e.clear_trigger;
        if (e.overhead_tokens !== undefined) {
          overheadTokens.value = e.overhead_tokens;
          overheadBreakdown.value = e.overhead_breakdown ?? [];
        }
        if (run.value.model !== e.model || run.value.provider !== e.provider) contextEstimated.value = contextTokens.value > 0;
        run.value = {
          ...run.value,
          model: e.model,
          provider: e.provider,
          context_window: e.context_window ?? 0,
          model_config_pending: false,
        };
        return;

      case "retry":
        retry.value = {
          attempt: e.attempt ?? 0,
          max: e.max ?? 0,
          delayMs: e.delay_ms ?? 0,
          reason: e.reason ?? "",
          at: e.ts,
        };
        return;

      case "run_end":
        if (answerReview.value?.state === "running") {
          answerReview.value = finishAnswerReview(answerReview.value, e.ts, e.stop_reason === "aborted");
        }
        if (workspaceDirty) workspaceRevision.value++;
        workspaceDirty = false;
        if (e.usage) usage.value = e.usage;
        live.value = { ...emptyLive(), pending_gates: live.value.pending_gates.filter(g => !!g.origin), pending_user_inputs: live.value.pending_user_inputs.filter(item => item.async) };
        incomingCounters.clear();
        run.value = { ...run.value, active: false, run_id: undefined, end_reason: e.end_reason || e.stop_reason, error: e.error };
        retry.value = null;
        // The describe step's record retires with the run: what it produced
        // lives on as the user message's image_alt (patched on the end event).
        describes.value = [];
        // A provider output limit can end without an error body. It still
        // leaves work unfinished, even when the only visible output was thought.
        streamError.value = runNotice();
        // A steering message the run accepted but never delivered. Surfacing it is
        // the difference between the user being told and watching what they typed
        // disappear; the view offers the text back.
        if (e.undelivered?.length) undelivered.value = e.undelivered;
        // Final sync: any debounced tree refresh pending from the run's writes
        // fires now, and the quick-open index — the one expensive listing —
        // is busted so the next ⌘P lazily rewalks the workspace.
        flushTreeInvalidate();
        invalidateIndex();
        flushLive();
        return;

      case "followups":
        // Pure assignment, like retry: the view reads the chips once the run is
        // idle, so they take no part in the live-throttle machinery.
        followups.value = { messageId: e.message_id ?? null, chips: e.chips ?? [] };
        return;

      case "error":
        streamError.value = e.error ?? gt("stream.unknownError");
        return;

      case "gate_auto":
        // Audit only: the call went through under a rule and no card was shown.
        return;

      case "browser_downloads":
        if (delivery?.sessionId && e.browser_downloads) publishBrowserDownloads(delivery.sessionId, e.browser_downloads);
        return;

      case "attachment_uploaded":
        // A file landed through the upload endpoint — maybe from another tab.
        // The popover counts it at once (deduped by path; a replay or the
        // seeding fetch may already know it), and the file tree hears about it
        // debounced, the same signal a settled mutating tool sends.
        if (e.attachment) {
          uploads.value = [...uploads.value.filter((u) => u.path !== e.attachment!.path), e.attachment];
        }
        scheduleTreeInvalidate();
        return;

      case "image_describe_start":
        // The vision helper starts on one image: a visible step whose whole
        // point is that it can take tens of seconds. Replaced rather than
        // duplicated if a replay re-announces it.
        if (!e.image_ref) return;
        describes.value = [
          ...describes.value.filter((d) => d.ref !== e.image_ref),
          { ref: e.image_ref, name: e.image_name ?? e.image_ref, status: "running", startedAt: e.ts },
        ];
        flushLive();
        return;

      case "image_describe_end": {
        if (!e.image_ref) return;
        const prior = describes.value.find((d) => d.ref === e.image_ref);
        describes.value = [
          ...describes.value.filter((d) => d.ref !== e.image_ref),
          {
            ref: e.image_ref,
            name: e.image_name ?? prior?.name ?? e.image_ref,
            status: e.error ? "failed" : "done",
            startedAt: prior?.startedAt ?? e.ts,
            durationMs: e.duration_ms,
            text: e.text,
            error: e.error,
          },
        ];
        // The settled description is the image block's alt: patch it into the
        // user message now, so the bubble can offer it without waiting for the
        // next snapshot — the transcript carries it from then on regardless.
        if (e.text) {
          const alt = e.text;
          messages.value = messages.value.map((m) =>
            m.role === "user" && m.content.some((b) => b.type === "image" && b.image_ref === e.image_ref)
              ? {
                  ...m,
                  content: m.content.map((b) =>
                    b.type === "image" && b.image_ref === e.image_ref ? { ...b, image_alt: alt } : b,
                  ),
                }
              : m,
          );
        }
        flushLive();
        return;
      }

      case "rewound":
      case "compacted": {
        // Two different events with one response, because the response is the same
        // one: whatever this tab holds no longer describes the session's history.
        // A rewind abandoned a branch, a compaction replaced the whole history with
        // a summary of it, and in both cases only a fresh snapshot rebuilds the
        // truth. A REST refetch does that without dropping this still-good stream;
        // the seq guard keeps a snapshot that raced live events from regressing them.
        const sid = sessionId;
        if (!sid) return;
        // A rewind can also delete the uploads only its abandoned branch
        // referenced: the session-files list reseeds from scratch (the union
        // in refreshUploads would keep the ghosts), and the tree drops them
        // on the same debounced signal any delete sends.
        uploads.value = [];
        void refreshUploads(sid);
        scheduleTreeInvalidate();
        const requestedInstance = streamInstance;
        void api
          .session(sid)
          .then((res) => {
            if (sessionId !== sid || streamInstance !== requestedInstance || !res.snapshot) return;
            if ((res.snapshot.seq ?? 0) < seq.value) return;
            apply({ seq: 0, type: "snapshot", ts: Date.now(), snapshot: res.snapshot });
          })
          .catch(() => {
            // The stream is intact; the next visit reloads from scratch anyway.
          });
        return;
      }
    }
  }

  // The original event fold is shared by both transports. Embedded mode gets
  // the same Hub events through the parent's authorized, bounded long poll.
  // No browser credential or fallback request to the legacy API is involved.
  async function openDelegated(sid: string, from?: number) {
    controller?.abort();
    stopWatchdog();
    const requestController = new AbortController();
    controller = requestController;
    const epoch = ++delegatedEpoch;
    sessionId = sid;
    closedByUs = false;
    const currentAttempt = () => epoch === delegatedEpoch && sessionId === sid && !closedByUs;
    let cursor = from;
    try {
      while (currentAttempt() && !requestController.signal.aborted) {
        const batch = await delegatedUpdates(sid, cursor, requestController.signal);
        if (!currentAttempt() || requestController.signal.aborted) return;
        connected.value = true;
        streamError.value = runNotice();
        backoff = 500;
        clearOutage();
        lastBytesAt = Date.now(); // an empty long-poll response is a heartbeat
        if (watchdog === null) startWatchdog();
        for (const event of batch.events) {
          if (!currentAttempt() || requestController.signal.aborted) return;
          apply(event as AgentEvent);
        }
        cursor = resumeFrom();
        // The server waits on Hub.Subscribe when no events are available;
        // do not replace its event history with synthetic snapshots or deltas.
      }
    } catch (err) {
      if (!currentAttempt()) return;
      if ((err as Error)?.name === "AbortError" && !watchdogTripped) return;
      const status = (err as { status?: number })?.status;
      const gaveUp = status === 401 || status === 403 || status === 404;
      streamError.value = watchdogTripped
        ? gt("stream.disconnected")
        : status === 401 || status === 403
          ? gt("stream.unauthorized")
          : ((err as Error)?.message ?? gt("stream.interrupted"));
      noteOutage(streamError.value, gaveUp);
      if (gaveUp) switching.value = false;
      else scheduleReconnect();
    } finally {
      // An old poll may settle after a new session has already connected.
      if (currentAttempt()) {
        connected.value = false;
        stopWatchdog();
      }
    }
  }

  async function open(sid: string, from?: number) {
    if (isDelegatedEmbed) return openDelegated(sid, from);
    controller?.abort();
    const requestController = new AbortController();
    controller = requestController;
    const epoch = ++streamEpoch;
    sessionId = sid;
    browserReveal.selectSession(sid);
    browserFocus.selectSession(sid);
    closedByUs = false;
    const currentAttempt = () => epoch === streamEpoch && sessionId === sid && !closedByUs;

    try {
      // fetch + ReadableStream rather than EventSource: the stream needs an
      // Authorization header, and EventSource cannot set one.
      const res = await fetch(streamURL(sid, from, streamInstance), {
        headers: authHeaders(),
        signal: requestController.signal,
      });
      if (!currentAttempt() || requestController.signal.aborted) return;
      if (!res.ok || !res.body) {
        streamError.value = res.status === 401 ? gt("stream.unauthorized") : gt("stream.connectFailed", { status: res.status });
        const gaveUp = res.status === 401 || res.status === 404;
        noteOutage(streamError.value, gaveUp);
        // A rejected token will be rejected again; retrying would only spin.
        // Giving up also ends the hold: there is no snapshot coming.
        if (gaveUp) {
          switching.value = false;
          return;
        }
        scheduleReconnect();
        return;
      }

      connected.value = true;
      streamError.value = runNotice();
      backoff = 500;
      clearOutage();
      startWatchdog();
      const backlogThrough = browserStreamBacklogThrough(res.headers?.get("X-Pogo-Stream-Backlog-Through") ?? null);

      const reader = res.body.getReader();
      const decoder = new TextDecoder("utf-8");
      let buffer = "";
      for (;;) {
        const { done, value } = await reader.read();
        if (!currentAttempt() || requestController.signal.aborted) return;
        if (done) break;
        // Keepalives count: the watchdog watches bytes, not events.
        lastBytesAt = Date.now();
        buffer += decoder.decode(value, { stream: true });
        let sep: number;
        while ((sep = buffer.indexOf("\n\n")) !== -1) {
          const frame = buffer.slice(0, sep);
          buffer = buffer.slice(sep + 2);
          const e = parseFrame(frame);
          if (e) apply(e, { sessionId: sid, replay: e.seq <= backlogThrough });
        }
      }
    } catch (err) {
      if (!currentAttempt()) return;
      // A deliberate close aborts too; only a watchdog trip takes the outage
      // path from here.
      if ((err as Error)?.name === "AbortError" && !watchdogTripped) return;
      streamError.value = watchdogTripped
        ? gt("stream.disconnected")
        : ((err as Error)?.message ?? gt("stream.interrupted"));
      noteOutage(streamError.value, false);
      scheduleReconnect();
      return;
    } finally {
      if (currentAttempt()) {
        connected.value = false;
        stopWatchdog();
      }
    }

    // A clean EOF mid-session is the server going away — same outage path as
    // a refused connection.
    if (currentAttempt()) {
      noteOutage(gt("stream.disconnected"), false);
      scheduleReconnect();
    }
  }

  // scheduleReconnect resumes from the last sequence number seen, so only the
  // events missed in the gap are replayed. The server degrades to a snapshot on
  // its own if its log no longer reaches back that far.
  function scheduleReconnect() {
    if (closedByUs || !sessionId) return;
    const sid = sessionId;
    const from = resumeFrom();
    const epoch = isDelegatedEmbed ? delegatedEpoch : streamEpoch;
    const delay = backoff;
    backoff = Math.min(backoff * 2, 5000);
    setTimeout(() => {
      if (!closedByUs && sessionId === sid && epoch === (isDelegatedEmbed ? delegatedEpoch : streamEpoch)) void open(sid, from);
    }, delay);
  }

  // resumeFrom is undefined while a session switch is still waiting for its
  // first snapshot: the seq held then belongs to the PREVIOUS session, and
  // replaying the new one from it would apply its events onto stale state.
  // No `from` asks for a snapshot, which is always correct.
  function resumeFrom(): number | undefined {
    return switching.value ? undefined : seq.value;
  }

  function connect(sid: string) {
    closedByUs = false;
    if (typeof window !== 'undefined' && !authListener) {
      window.addEventListener('pogo:auth-changed', clearStreamInstance);
      authListener = true;
    }
    if (sessionId !== sid) {
      streamInstance = undefined;
      workspaceDirty = false;
      streamError.value = null;
      // Hold the old session on screen until the new one's snapshot lands;
      // the view fades it meanwhile. See the comment above apply().
      switching.value = true;
      // A new session gets a fresh link: the old one's error page goes away
      // now, not when the fetch happens to succeed.
      clearOutage();
      // Per-session state that must not bleed across: another conversation's
      // placeholders, upload list and describe steps describe files this one
      // never saw.
      pendingUser.value = [];
      uploads.value = [];
      describes.value = [];
      progressEvents.value = [];
      toolConfig.value = null;
      answerReview.value = null;
    }
    const p = open(sid);
    // Seed the uploads list alongside the stream; attachment_uploaded events
    // keep it current from here. open() sets sessionId synchronously, so the
    // fetch's stale-session guard has the new id already.
    if (!isDelegatedEmbed && options.attachments !== false) void refreshUploads(sid);
    return p;
  }

  // refreshUploads seeds the session-files list from the server's attribution
  // (transcript blocks ∪ upload ledger). Unioned rather than assigned, so an
  // attachment_uploaded event that landed while the fetch flew survives it.
  async function refreshUploads(sid: string) {
    try {
      const list = await api.sessionAttachments(sid);
      if (sessionId !== sid) return; // switched away while the fetch flew
      const byPath = new Map<string, AttachmentMeta>();
      for (const u of list) byPath.set(u.path, u);
      for (const u of uploads.value) byPath.set(u.path, u);
      uploads.value = [...byPath.values()];
    } catch {
      // The popover still has its timeline-derived entries to fall back on.
    }
  }

  // sendOptimistic draws the user's bubble the instant send is clicked —
  // attachment chips included, since its blocks are built from the same paths
  // the POST carries. The user_message fold retires it on the echoed id.
  function sendOptimistic(m: Message) {
    pendingUser.value = [...pendingUser.value, ...normalizeMessages([m])];
  }

  // dropOptimistic is the send-failed path: the placeholder leaves rather
  // than sit on the timeline claiming a message the server never accepted.
  function dropOptimistic(id: string) {
    pendingUser.value = pendingUser.value.filter((m) => m.id !== id);
  }

  // updateOptimistic swaps a placeholder's content in place — the attachment
  // upload the bubble opened ahead of has finished, so the real file blocks
  // replace the bare text it opened with.
  function updateOptimistic(id: string, m: Message) {
    const replacement = normalizeMessages([m])[0];
    if (replacement) pendingUser.value = pendingUser.value.map((x) => (x.id === id ? replacement : x));
  }

  // retryNow is the error page's button: one immediate attempt, outside the
  // backoff schedule. The page stays up until an attempt actually succeeds —
  // hiding it on the click would flash it straight back on failure.
  function retryNow() {
    if (!sessionId) return Promise.resolve();
    backoff = 500;
    return open(sessionId, resumeFrom());
  }

  function disconnect() {
    workspaceDirty = false;
    clearStreamInstance();
    if (typeof window !== 'undefined' && authListener) {
      window.removeEventListener('pogo:auth-changed', clearStreamInstance);
      authListener = false;
    }
    closedByUs = true;
    streamEpoch++;
    browserReveal.selectSession(null);
    browserFocus.selectSession(null);
    sessionId = null;
    clearOutage();
    connected.value = false;
    stopWatchdog();
    if (isDelegatedEmbed) {
      delegatedEpoch++;
      switching.value = false;
    }
    controller?.abort();
    controller = null;
  }

  function clearStreamInstance() { streamInstance = undefined; }

  // A run that has already finished is reported by the snapshot's run.active,
  // not by a replayed run_end. Anything waiting on "the run is over" has to look
  // at this, not only at the event.
  const busy = computed(() => run.value.active);

  return {
    messages: messages as Ref<Message[]>,
    displaySessionId,
    results,
    live,
    run,
    answerReview,
    workspaceRevision,
    agentWorkflowUpdate,
    progressEvents,
    modelChangeVersion,
    toolConfig,
    policy,
    usage,
    bgTasks,
    shellTasks,
    acceptShellTask,
    contextTokens,
    contextEstimated,
    overheadTokens,
    overheadBreakdown,
    clearTrigger,
    seq,
    busy,
    connected,
    streamError,
    unreachable,
    outage,
    retry,
    undelivered,
    inbox,
    acceptInboxItem,
    followups,
    policyReverted,
    liveVersion,
    switching,
    serverOffset,
    serverNow,
    pendingUser: pendingUser as Ref<Message[]>,
    uploads: uploads as Ref<AttachmentMeta[]>,
    describes: describes as Ref<ImageDescribe[]>,
    sendOptimistic,
    dropOptimistic,
    updateOptimistic,
    connect,
    disconnect,
    retryNow,
    apply,
  };
}
