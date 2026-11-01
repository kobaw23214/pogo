import { normalizeMessageContent, normalizeMessages } from "@/api/messageContent";
import { emptyLive, type AgentEvent, type BgTask, type Snapshot } from "@/api/types";

const outputLimit = 32 * 1024, frameLimit = 400;
const tail = (value: string, limit: number) => value.length > limit ? value.slice(-limit) : value;

/** View-only metadata; older shared sessions do not retain per-node readings. */
export type TaskContextSnapshot = Snapshot & { task_context_unavailable?: boolean };

export function normalizeTaskSnapshot(value: Snapshot): Snapshot {
  return { ...value, seq: value.seq || 0, messages: normalizeMessages(value.messages), results: value.results || {},
    live: { ...emptyLive(), ...value.live, pending_tools: value.live?.pending_tools || [], pending_gates: value.live?.pending_gates || [], pending_user_inputs: value.live?.pending_user_inputs || [] },
    run: value.run || { active: false }, policy: value.policy || { mode: "standard" }, usage: value.usage || { input: 0, output: 0 } };
}

/** A view-only fold: it deliberately has no chat, browser, Office or file side effects. */
export function reduceTaskSessionEvent(previous: Snapshot | null, event: AgentEvent): Snapshot | null {
  if (event.type === "snapshot") return event.snapshot ? normalizeTaskSnapshot(event.snapshot) : previous;
  if (!previous || event.seq && event.seq <= previous.seq) return previous;
  const next = { ...previous, seq: event.seq || previous.seq, live: { ...previous.live }, run: { ...previous.run } };
  const live = next.live;
  switch (event.type) {
    case "run_start":
      next.answer_review = undefined;
      next.context_tokens = 0; next.context_estimated = false;
      next.live = { ...emptyLive(), active: true, run_id: event.run_id, pending_gates: live.pending_gates.filter(gate => !!gate.origin), pending_user_inputs: live.pending_user_inputs.filter(item => item.async) };
      next.run = { ...next.run, active: true, run_id: event.run_id, model: event.model || next.run.model, provider: event.provider || next.run.provider, end_reason: undefined, error: undefined };
      break;
    case "user_message":
    case "message": {
      const id = event.message_id || `${event.type}:${event.seq}`;
      const review = event.answer_review || (next.answer_review?.message_id === id ? next.answer_review : undefined);
      const message = { id, role: event.type === "user_message" ? "user" as const : event.role || "assistant" as const,
        content: normalizeMessageContent(event.content, event.text), ts: event.ts,
        ...(event.run_id ? { run_id: event.run_id } : {}), ...(event.inbox_id ? { inbox_id: event.inbox_id } : {}),
        ...(event.input_layout ? { input_layout: event.input_layout } : {}), ...(event.steering ? { steering: true } : {}), ...(review ? { answer_review: review } : {}) };
      next.messages = [...next.messages.filter(item => item.id !== id), message];
      if (event.type === "message") { live.text = ""; live.thinking = ""; if (event.usage?.input) { next.context_tokens = event.usage.input; next.context_estimated = false; } }
      else next.followups = undefined;
      break;
    }
    case "turn_start": Object.assign(live, { turn: event.turn, message_id: event.message_id, text: "", thinking: "" }); break;
    case "token": live.text = (live.text || "") + (event.text || ""); break;
    case "thinking": live.thinking = (live.thinking || "") + (event.text || ""); break;
    case "answer_review": if (event.answer_review) next.answer_review = { ...event.answer_review }; break;
    case "tool_args": {
      if (!event.call_id) break;
      const found = live.incoming?.find(item => item.call_id === event.call_id), text = event.text || "";
      const incoming = { ...found, call_id: event.call_id, name: found?.name || event.name || "", ts: found?.ts || event.ts,
        bytes: (found?.bytes || 0) + text.length, lines: (found?.lines || 0) + (text.match(/\n/g)?.length || 0),
        head: ((found?.head || "") + text).slice(0, 4096), tail: tail((found?.tail || "") + text, 8192) };
      live.incoming = [...(live.incoming || []).filter(item => item.call_id !== event.call_id), incoming]; break;
    }
    case "tool_start":
      if (!event.call_id || !event.name) break;
      live.incoming = live.incoming?.filter(item => item.call_id !== event.call_id);
      live.pending_tools = [...live.pending_tools.filter(item => item.call_id !== event.call_id), { call_id: event.call_id, name: event.name, args: event.args, started_at: event.ts }]; break;
    case "tool_partial":
      live.pending_tools = live.pending_tools.map(item => item.call_id === event.call_id ? { ...item,
        ...(event.text ? { output: tail((item.output || "") + event.text, outputLimit) } : {}),
        ...(event.frame ? { frames: [...(item.frames || []), event.frame].slice(-frameLimit) } : {}) } : item); break;
    case "tool_end": {
      if (!event.call_id) break;
      const pending = live.pending_tools.find(item => item.call_id === event.call_id);
      live.pending_tools = live.pending_tools.filter(item => item.call_id !== event.call_id);
      live.incoming = live.incoming?.filter(item => item.call_id !== event.call_id);
      next.results = { ...next.results, [event.call_id]: { call_id: event.call_id, name: event.name, text: event.text || "", is_error: event.is_error,
        details: event.details, duration_ms: event.duration_ms, source: event.source, frames: pending?.frames } }; break;
    }
    case "gate_request":
      if (!event.gate_id || !event.name) break;
      live.pending_gates = [...live.pending_gates.filter(item => item.gate_id !== event.gate_id), { gate_id: event.gate_id, call_id: event.call_id || "", tool: event.name,
        args: event.args, deadline: event.deadline || 0, danger: event.danger, origin: event.origin, frozen: event.frozen, remaining_ms: event.remaining_ms }]; break;
    case "user_input_request":
      if (event.user_input) live.pending_user_inputs = [...live.pending_user_inputs.filter(item => item.request_id !== event.user_input!.request_id), event.user_input];
      break;
    case "user_input_resolved": {
      const resolution = event.user_input_resolution;
      if (!resolution) break;
      const pending = live.pending_user_inputs.find(item => item.request_id === resolution.request_id);
      live.pending_user_inputs = live.pending_user_inputs.filter(item => item.request_id !== resolution.request_id);
      if (resolution.async && resolution.call_id && next.results[resolution.call_id]) {
        const current = next.results[resolution.call_id];
        const previous = current.details && typeof current.details === "object" && "questions" in current.details ? current.details.questions : [];
        const details = { request_id: resolution.request_id, call_id: resolution.call_id, status: resolution.status, async: true,
          questions: pending?.questions ?? previous, ...(resolution.answers ? { answers: resolution.answers } : {}) };
        next.results[resolution.call_id] = { ...current, details };
        next.user_input_answers = { ...next.user_input_answers, [resolution.call_id]: details };
      }
      break;
    }
    case "gate_deadline": live.pending_gates = live.pending_gates.map(item => item.gate_id === event.gate_id ? { ...item, deadline: event.deadline ?? item.deadline, frozen: event.frozen ?? item.frozen, remaining_ms: event.remaining_ms ?? item.remaining_ms } : item); break;
    case "gate_resolved": live.pending_gates = live.pending_gates.filter(item => item.gate_id !== event.gate_id); break;
    case "policy_changed": case "policy_reverted": if (event.policy) next.policy = event.policy; break;
    case "model_changed":
      if (next.run.model !== event.model || next.run.provider !== event.provider) next.context_estimated = next.context_tokens > 0;
      next.run = { ...next.run, model: event.model, provider: event.provider, context_window: event.context_window, model_config_pending: false };
      if (event.clear_trigger !== undefined) next.clear_trigger = event.clear_trigger;
      if (event.overhead_tokens !== undefined) next.overhead_tokens = event.overhead_tokens; break;
    case "model_config_pending": next.run.model_config_pending = event.model_config_pending; break;
    case "inbox_updated": if (event.item) next.inbox = [...(next.inbox || []).filter(item => item.id !== event.item!.id), event.item]; break;
    case "followups": next.followups = { message_id: event.message_id, chips: event.chips || [] }; break;
    case "subagent_start":
      if (event.task_id && !next.bg_tasks?.some(item => item.task_id === event.task_id)) next.bg_tasks = [...(next.bg_tasks || []), { task_id: event.task_id, mode: event.name || "", task: event.text || "", status: "running", started_at: event.ts, frames: [] }]; break;
    case "subagent_frame":
      next.bg_tasks = next.bg_tasks?.map(item => item.task_id === event.task_id && event.frame ? { ...item, frames: [...(item.frames || []), event.frame].slice(-frameLimit) } : item); break;
    case "subagent_end":
      next.bg_tasks = next.bg_tasks?.map(item => item.task_id === event.task_id ? { ...item, status: event.is_error ? "failed" : "done", error: event.error,
        details: event.details as BgTask["details"], duration_ms: event.duration_ms, frames: (event.frames?.length || 0) > (item.frames?.length || 0) ? event.frames : item.frames } : item);
      if (event.usage) next.usage = { input: next.usage.input + event.usage.input, output: next.usage.output + event.usage.output,
        cache_read: (next.usage.cache_read || 0) + (event.usage.cache_read || 0), reasoning: (next.usage.reasoning || 0) + (event.usage.reasoning || 0) }; break;
    case "image_describe_end":
      if (event.text) next.messages = next.messages.map(message => ({ ...message, content: message.content.map(block => block.type === "image" && block.image_ref === event.image_ref ? { ...block, image_alt: event.text } : block) })); break;
    case "run_end":
      if (next.answer_review?.state === "running") next.answer_review = { ...next.answer_review, state: event.stop_reason === "aborted" || event.end_reason === "aborted" ? "cancelled" : "fallback", reason: "interrupted", finished_at: event.ts, duration_ms: Math.max(0, event.ts - next.answer_review.started_at) };
      if (event.usage) next.usage = event.usage;
      next.live = { ...emptyLive(), pending_gates: live.pending_gates.filter(gate => !!gate.origin), pending_user_inputs: live.pending_user_inputs.filter(item => item.async) };
      next.run = { ...next.run, active: false, run_id: undefined, end_reason: event.end_reason || event.stop_reason, error: event.error }; break;
    case "error": next.run.error = event.error || event.text; break;
  }
  return next;
}

/** Legacy sequential workflows share a session; user-message run markers delimit each node's transcript. */
export function projectTaskSessionSnapshot(value: Snapshot | null, activeRunId?: string): { snapshot: TaskContextSnapshot | null; isolationMissing: boolean } {
  if (!value || !activeRunId) return { snapshot: value, isolationMissing: false };
  let owner: string | undefined, legacy = false;
  const messages = value.messages.filter(message => {
    if (message.role === "user") { owner = message.run_id; if (!owner) legacy = true; }
    return (message.run_id || owner) === activeRunId;
  });
  const ownLive = value.live.run_id === activeRunId || value.run.active && value.run.run_id === activeRunId;
  const latestUser = [...value.messages].reverse().find(message => message.role === "user");
  const liveRunId = value.run.active ? value.run.run_id || value.live.run_id : value.live.active ? value.live.run_id : undefined;
  // A shared execution session's scalar prompt reading belongs only to its
  // latest marked node. Never attach the next node's reading/model limit to an
  // earlier step, even if that earlier transcript is still readable.
  const ownContext = latestUser?.run_id === activeRunId && (!liveRunId || liveRunId === activeRunId);
  const calls = new Set(messages.flatMap(message => message.content.flatMap(block => block.type === "tool_use" ? [block.id] : [])));
  if (ownLive) for (const pending of value.live.pending_tools) calls.add(pending.call_id);
  const results = Object.fromEntries(Object.entries(value.results).filter(([id]) => calls.has(id)));
  const children = new Set(Object.values(results).flatMap(result => {
    const details = result.details as { type?: string; id?: string; run_id?: string } | undefined;
    return details?.type === "subagent" ? [details.id, details.run_id].filter((id): id is string => !!id) : [];
  }));
  const gates = value.live.pending_gates.filter(gate => gate.origin ? children.has(gate.origin) : ownLive);
  const live = ownLive ? { ...value.live, pending_gates: gates } : { ...emptyLive(), pending_gates: gates };
  const review = ownLive || value.answer_review && messages.some(message => message.id === value.answer_review?.message_id) ? value.answer_review : undefined;
  return { snapshot: { ...value, messages, results, live, run: ownLive ? value.run : ownContext ? { ...value.run, active: false, run_id: undefined } : { active: false },
      context_tokens: ownContext ? value.context_tokens : 0, context_estimated: ownContext ? value.context_estimated : undefined,
      overhead_tokens: ownContext ? value.overhead_tokens : undefined, clear_trigger: ownContext ? value.clear_trigger : undefined,
      task_context_unavailable: !ownContext, bg_tasks: value.bg_tasks?.filter(task => children.has(task.task_id)), answer_review: review },
    isolationMissing: !messages.length && !ownLive && legacy };
}
