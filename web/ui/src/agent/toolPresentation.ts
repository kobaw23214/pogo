import type { ToolDetails } from "@/api/types";
import { gt } from "@/i18n";
import { shortcutCallTool } from "./shortcutToolNames";
import { formatDuration, isBashDetails, isMCPDetails, isSkillDetails, isSubagentDetails, summarizeArgs, type TimelineCall } from "./timeline";

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

const BUILTIN_GLYPHS: Readonly<Record<string, ToolGlyphKind>> = {
  read: "read", ls: "list", find: "explore", grep: "explore", write: "change", edit: "change", bash: "command", browser: "browser", docs_office: "office",
  shortcut_query: "shortcut", shortcut_manage: "shortcut", shortcut_search: "shortcut", shortcut_read: "shortcut", shortcut_create: "shortcut",
  shortcut_list: "shortcut", shortcut_folder_create: "shortcut", shortcut_update: "shortcut", shortcut_folder_rename: "shortcut",
  shortcut_move: "shortcut", shortcut_delete: "shortcut", shortcut_merge: "shortcut",
  skill_search: "skill", skill_read: "skill", skill_save: "skill", background_task: "background", watch: "background", watch_test: "background", watch_decide: "background",
  workflow_discover: "explore", workflow_read: "read", workflow_validate: "explore", workflow_edit: "change", workflow_test: "background", workflow_run: "background", workflow_publish: "change",
  agent_workflow_read: "read", agent_workflow_edit: "change", agent_workflow_enable: "change", agent_workflow_run: "background",
  subagent: "subagent", subagent_tasks: "subagent", todo: "todo", request_user_input: "todo", request_user_input_async: "todo",
};

export function toolGlyph(name: string, details?: ToolDetails): ToolGlyphKind {
  if (Object.prototype.hasOwnProperty.call(BUILTIN_GLYPHS, name)) return BUILTIN_GLYPHS[name];
  if (isMCPDetails(details)) return "mcp";
  if (isSkillDetails(details)) return "skill";
  if (name.startsWith("mcp_")) return "mcp";
  if (name.startsWith("skill_")) return "skill";
  return "tool";
}

export function toolCallState(call: TimelineCall): ToolCallState {
  if (call.inputInvalid) return "error";
  if (call.gate || call.childGates?.length) return "gate";
  const background = call.name === "subagent" ? call.bgTask : undefined;
  if (background ? background.status === "running" : call.running) return "running";
  if (call.orphaned) return "orphaned";
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

export function presentToolCall(call: TimelineCall): ToolPresentation {
  const normalized = shortcutCallTool(call.name, call.args);
  const details = call.result?.details;
  const builtin = Object.prototype.hasOwnProperty.call(BUILTIN_GLYPHS, normalized);
  let name = builtin ? gt(`toolCall.names.${normalized}`) : call.name;
  let secondary: string | undefined;
  if (!builtin && isMCPDetails(details)) {
    name = typeof details.title === "string" && details.title.trim() ? details.title.trim() : typeof details.tool === "string" && details.tool ? details.tool : call.name;
    secondary = [typeof details.server === "string" ? details.server : undefined, typeof details.tool === "string" && details.tool !== name ? details.tool : undefined].filter(Boolean).join(" · ") || undefined;
  } else if (!builtin && isSkillDetails(details)) {
    name = typeof details.capability === "string" && details.capability ? details.capability : call.name;
    secondary = gt("toolCall.runtimeSkill");
  }
  const state = toolCallState(call);
  const bash = call.name === "bash" && isBashDetails(details) ? details : undefined;
  const backgroundLabel = bash?.background && (!bash.status || bash.status === "running")
    ? gt(bash.status === "running" ? "bashResult.backgroundRunning" : "bashResult.backgroundDispatched") : undefined;
  const statusLabel = call.inputInvalid ? gt("toolCall.notExecuted") : backgroundLabel ?? (state === "running" ? gt(call.name === "request_user_input" ? "userInput.waiting" : "toolCall.running") : state === "gate" ? gt("toolCall.waitingApproval") : state === "orphaned" ? gt("toolCall.orphaned") : state === "error" ? gt("toolCall.failed") : undefined);
  const duration = recordedDuration(call);
  return {
    glyph: toolGlyph(normalized, details), name, secondary,
    target: call.inputInvalid ? gt("toolCall.invalidArguments") : summarizeArgs(call.name, call.args) || undefined,
    meta: duration === undefined ? undefined : formatDuration(duration),
    protocolName: call.name, state, statusLabel,
  };
}
