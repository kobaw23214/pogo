import type { ToolDetails } from "@/api/types";
import { resolveToolIdentity, type ToolIdentityCatalog } from "./toolIdentity";
import { gt } from "@/i18n";
import { formatDuration, isBashDetails, isMCPDetails, isSubagentDetails, type TimelineCall } from "./timeline";

export type ToolGlyphKind = "thinking" | "explore" | "command" | "change" | "read" | "list" | "browser" | "office" | "mcp" | "skill" | "subagent" | "todo" | "shortcut" | "background" | "tool";
export type ToolCallState = "gate" | "running" | "orphaned" | "error" | "done";

export interface ToolPresentation {
  glyph: ToolGlyphKind;
  name: string;
  target?: string;
  secondary?: string;
  meta?: string;
  protocolName: string;
  state: ToolCallState;
  statusLabel?: string;
}

export function toolGlyph(name: string, details?: ToolDetails): ToolGlyphKind {
  return resolveToolIdentity(name, undefined, details).glyph;
}

export type ToolControlOutcome = "denied" | "approval-timeout" | "human-control" | "cancelled";

/** Compatibility for exact host-generated receipts. Arbitrary error prose is
 * not a trustworthy permission or recovery signal. */
export function toolControlOutcome(call: TimelineCall): ToolControlOutcome | undefined {
  if (!call.result?.is_error || call.inputInvalid) return undefined;
  const text = call.result.text.trim();
  if (text === "用户拒绝了这次调用" || text === "The user rejected this call" || text === "Tool execution was blocked") return "denied";
  if (/^The user did not approve this call within [0-9a-z.µ]+$/.test(text)) return "approval-timeout";
  if (text === "Run was cancelled" || text === "Operation aborted") return "cancelled";
  if (call.name === "browser") {
    const details = call.result.details as unknown as Record<string, unknown> | undefined;
    if (details?.error_code === "TAB_HUMAN_CONTROL" || details?.code === "TAB_HUMAN_CONTROL") return "human-control";
    // formatBrowserError prefixes native error codes with this exact envelope.
    const nativeError = text.replace(/^browser [a-z_]+ failed: /, "");
    if (/^TAB_HUMAN_CONTROL(?=[:\s]|$)/.test(nativeError)) return "human-control";
    try {
      const receipt = JSON.parse(text);
      if (receipt && typeof receipt === "object" && receipt.error && typeof receipt.error === "object" && receipt.error.code === "TAB_HUMAN_CONTROL") return "human-control";
    } catch { /* Missing structured metadata keeps the ordinary error. */ }
  }
  return undefined;
}

export function toolCallState(call: TimelineCall): ToolCallState {
  if (call.inputInvalid) return "error";
  if (call.gate || call.childGates?.length) return "gate";
  const background = call.name === "subagent" ? call.bgTask : undefined;
  if (background ? background.status === "running" : call.running) return "running";
  if (call.orphaned) return "orphaned";
  if (toolControlOutcome(call)) return "done";
  if (background) return background.status === "failed" ? "error" : "done";
  const details = call.result?.details;
  if (call.name === "bash" && isBashDetails(details) && details.background) {
    if (!details.status) return "orphaned";
    if (details.status === "running") return "running";
    if (details.status && details.status !== "completed") return "error";
  }
  if (call.name === "subagent" && isSubagentDetails(details) && details.background) {
    return call.taskNotice ? call.taskNotice.failed ? "error" : "done" : "orphaned";
  }
  if (call.name === "browser" && details && "action" in details && "ok" in details && details.ok === false) return "error";
  return call.result?.is_error || (call.name === "bash" && isBashDetails(details) && typeof details.exit_code === "number" && details.exit_code !== 0) ? "error" : "done";
}

function recordedDuration(call: TimelineCall): number | undefined {
  const details = call.result?.details;
  if (call.name === "bash" && isBashDetails(details) && details.background) {
    return details.status && details.status !== "running" && typeof details.duration_ms === "number" && Number.isFinite(details.duration_ms) && details.duration_ms >= 0 ? details.duration_ms : undefined;
  }
  if (call.name === "subagent" && isSubagentDetails(details) && details.background && !call.bgTask) return undefined;
  const ms = call.name === "subagent" && call.bgTask ? call.bgTask.duration_ms
    : (isBashDetails(details) || isMCPDetails(details)) ? details.duration_ms ?? call.result?.duration_ms : call.result?.duration_ms;
  return typeof ms === "number" && Number.isFinite(ms) && ms >= 0 ? ms : undefined;
}

function recordedResultLabel(call: TimelineCall): string | undefined {
  if (!call.result || call.inputInvalid || toolControlOutcome(call)) return undefined;
  const details = call.result.details as unknown as Record<string, unknown> | undefined;
  if (!details || typeof details !== "object") return undefined;
  const count = (value: unknown) => typeof value === "number" && Number.isFinite(value) && value >= 0 ? value : undefined;
  if (call.name === "bash" && isBashDetails(call.result.details)) {
    if (call.result.details.background && call.result.details.status === "running") return undefined;
    const code = call.result.details.exit_code;
    return typeof code === "number" && code !== 0 ? `exit ${code}` : undefined;
  }
  if (call.name === "browser" && Array.isArray(details.step_results)) {
    const steps = details.step_results as Array<{ ok?: boolean }>;
    return gt("toolCall.stepsSucceeded", { n: steps.filter(step => step.ok === true).length, total: steps.length });
  }
  if (call.result.is_error) return undefined;
  if (call.name === "read" && count(details.shown_lines) !== undefined) return gt("toolCall.lines", { n: details.shown_lines });
  if (call.name === "read" && Array.isArray(details.files)) return gt("toolCall.files", { n: details.files.length });
  if ((call.name === "find" || call.name === "grep") && count(details.matches) !== undefined) return gt("toolCall.matches", { n: details.matches });
  if (call.name === "ls" && count(details.entries) !== undefined) return gt("toolCall.items", { n: details.entries });
  if ((call.name === "write" || call.name === "edit") && count(details.added) !== undefined && count(details.removed) !== undefined) return `+${details.added} −${details.removed}`;
  if (details.structured_content) {
    const value = details.structured_content;
    if (Array.isArray(value)) return gt("toolCall.items", { n: value.length });
    if (typeof value === "object") {
      const entries = Object.values(value);
      if (entries.length === 1 && Array.isArray(entries[0])) return gt("toolCall.items", { n: entries[0].length });
    }
  }
  return undefined;
}

export function presentToolCall(call: TimelineCall, catalog?: ToolIdentityCatalog): ToolPresentation {
  const identity = resolveToolIdentity(call.name, call.args, call.result?.details, catalog);
  const details = call.result?.details;
  const state = toolCallState(call);
  const bash = call.name === "bash" && isBashDetails(details) ? details : undefined;
  const backgroundLabel = bash?.background && (!bash.status || bash.status === "running")
    ? gt(bash.status === "running" ? "bashResult.backgroundRunning" : "bashResult.backgroundDispatched") : undefined;
  const outcome = toolControlOutcome(call);
  const statusLabel = call.inputInvalid ? gt("toolCall.notExecuted") : outcome ? gt(`toolCall.outcomes.${outcome}`) : backgroundLabel ?? (state === "running" ? gt(call.name === "request_user_input" || call.name === "request_user_input_async" ? "userInput.waiting" : "toolCall.running") : state === "gate" ? gt("toolCall.waitingApproval") : state === "orphaned" ? gt("toolCall.orphaned") : state === "error" ? gt("toolCall.failed") : undefined);
  const duration = recordedDuration(call);
  const resultLabel = recordedResultLabel(call);
  return {
    glyph: identity.glyph, name: identity.name, secondary: identity.secondary,
    target: call.inputInvalid ? gt("toolCall.invalidArguments") : identity.target,
    meta: [resultLabel, duration === undefined ? undefined : formatDuration(duration)].filter(Boolean).join(" · ") || undefined,
    protocolName: call.name, state, statusLabel,
  };
}
