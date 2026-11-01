import type { BgTask, PendingGate, SubagentDetails, SubagentFrame, ToolName, ToolResult } from "@/api/types";
import { childSessionId, isSubagentDetails, subagentLiveTail, subagentSteps, type SubagentStep, type TaskNotice } from "./timeline";
import { presentToolCall } from "./toolPresentation";
import { gt } from "@/i18n";

export type SubagentSummaryState = "starting" | "running" | "awaitingApproval" | "completed" | "failed" | "stopped" | "syncing" | "unavailable";
export interface SubagentSummaryInput {
  args?: unknown;
  details?: Partial<SubagentDetails>;
  result?: ToolResult;
  running: boolean;
  frames?: SubagentFrame[];
  bgTask?: BgTask;
  startedAt?: number;
  gates?: PendingGate[];
  notice?: TaskNotice;
  orphaned?: boolean;
}
export interface SubagentSummary {
  task: string;
  state: SubagentSummaryState;
  live: boolean;
  background: boolean;
  action?: string;
  activeCount?: number;
  recent?: { kind: "thinking" | "answer"; text: string };
  resultText?: string;
  error?: string;
  childSession?: string;
  agentId?: string;
  runId?: string;
  model?: string;
  mode?: string;
  startedAt?: number;
  durationMs?: number;
  recordedCalls: number;
  recordedFailures: number;
  turns?: number;
  inputTokens?: number;
  outputTokens?: number;
  retained: boolean;
}

const nonempty = (value: unknown): string | undefined => typeof value === "string" && value.trim() ? value : undefined;
const nonnegative = (value: unknown): number | undefined => typeof value === "number" && Number.isFinite(value) && value >= 0 ? value : undefined;

/** A factual projection, never an AI-generated status or completion estimate.
 * Frames are a bounded recent window, so their counts are explicitly recorded
 * calls; a dispatch acknowledgement is never used as a background answer. */
export function subagentSummary(input: SubagentSummaryInput): SubagentSummary {
  const args = input.args && typeof input.args === "object" ? input.args as Record<string, unknown> : {};
  const dispatchDetails = input.details ?? (isSubagentDetails(input.result?.details) ? input.result.details : undefined);
  const details = input.bgTask?.details ?? dispatchDetails;
  const frames = input.bgTask?.frames ?? input.frames;
  const background = !!input.bgTask || dispatchDetails?.background === true;
  const runIds = [input.bgTask?.task_id, dispatchDetails?.id, dispatchDetails?.run_id, details?.run_id, details?.id].filter((id): id is string => !!id);
  const notice = input.notice && runIds.includes(input.notice.id) && (!input.notice.agentId || !details?.agent_id || input.notice.agentId === details.agent_id) ? input.notice : undefined;
  const steps = subagentSteps(frames);
  const end = [...(frames ?? [])].reverse().find(frame => frame.type === "run_end");
  const stopped = end?.stop_reason === "aborted" || end?.stop_reason === "max_tokens";
  const failed = input.bgTask?.status === "failed" || (!background && input.result?.is_error === true) || notice?.failed === true || !!end?.error;
  const inFlight = input.bgTask ? input.bgTask.status === "running" : !background && input.running;
  const live = inFlight && !end && !notice;
  let state: SubagentSummaryState;
  if (input.gates?.length) state = "awaitingApproval";
  else if (stopped) state = "stopped";
  else if (failed) state = "failed";
  else if (live) state = "running";
  else if (background) state = notice ? "completed" : input.bgTask?.status === "done" || end ? "syncing" : "unavailable";
  else if (input.orphaned) state = "unavailable";
  else state = input.result ? "completed" : end ? "syncing" : "starting";

  const toolSteps = steps.filter(step => step.kind === "tool");
  const latestTurn = [...steps].reverse().find(step => step.kind === "turn")?.turn;
  const current = toolSteps.filter(step => !step.done && (latestTurn === undefined || step.turn === latestTurn));
  const reliablePending = latestTurn !== undefined && current.every(step => !!step.identity);
  const activeCount = live && reliablePending ? current.length : undefined;
  const latest: SubagentStep | undefined = current[current.length - 1];
  let action: string | undefined;
  if ((live || state === "awaitingApproval") && latest?.name) {
    const presentation = presentToolCall({ callId: latest.callId ?? "", name: latest.name as ToolName, args: latest.args, running: !latest.done, orphaned: false });
    action = [presentation.name, presentation.target].filter(Boolean).join(" · ");
  }
  const tail = subagentLiveTail(frames);
  const recent = live && tail.streaming === "thinking" && tail.thinking ? { kind: "thinking" as const, text: tail.thinking }
    : live && tail.streaming === "token" && tail.answer ? { kind: "answer" as const, text: tail.answer } : undefined;
  const resultText = background ? nonempty(notice?.result) : nonempty(input.result?.text);
  const error = nonempty(input.bgTask?.error) ?? (notice?.failed ? nonempty(notice.error) : undefined) ?? nonempty(end?.error)
    ?? (!background && input.result?.is_error ? nonempty(input.result.text) : undefined);
  const session = nonempty(details?.session) ?? nonempty((frames ?? []).find(frame => frame.session)?.session) ?? nonempty(notice?.transcript);
  return {
    task: nonempty(args.task) ?? nonempty(input.bgTask?.task) ?? nonempty(notice?.brief) ?? gt("subagentResult.taskFallback"),
    state, live, background, action, activeCount, recent, resultText, error,
    childSession: childSessionId(session), agentId: details?.agent_id ?? notice?.agentId,
    runId: details?.run_id ?? details?.id ?? input.bgTask?.task_id,
    model: nonempty(details?.model) ?? nonempty((frames ?? []).find(frame => frame.model)?.model),
    mode: nonempty(details?.mode) ?? nonempty(args.mode) ?? nonempty(input.bgTask?.mode) ?? notice?.mode,
    startedAt: nonnegative(input.bgTask?.started_at ?? input.startedAt),
    durationMs: nonnegative(background ? input.bgTask?.duration_ms : input.result?.duration_ms),
    recordedCalls: new Set(toolSteps.flatMap(step => step.identity ? [step.identity] : step.callId ? [`call:${step.callId}`] : [])).size,
    recordedFailures: toolSteps.filter(step => step.bad).length,
    turns: nonnegative(details?.turns ?? notice?.turns), inputTokens: nonnegative(details?.input_tokens ?? notice?.inputTok),
    outputTokens: nonnegative(details?.output_tokens ?? notice?.outTok),
    retained: !live && !failed && details?.status === "idle" && !!details.agent_id,
  };
}
