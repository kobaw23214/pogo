import type { BrowserDetails, BrowserPageSummary } from "@/api/types";
import type { TimelineCall } from "./timeline";
import { isBashDetails, isMCPDetails, isReadManyDetails, isSkillDetails } from "./timeline";
import { toolCallState, toolControlOutcome } from "./toolPresentation";
import { shortcutCallTool } from "./shortcutToolNames";
import type { ActivityRun } from "./activityRuns";

export type SummaryClauseKind = "read" | "readOperations" | "list" | "search" | "write" | "command" | "wait"
  | "browserOpen" | "browserRead" | "browserOpenPages" | "browserReadPages"
  | "browserClick" | "browserType" | "browserOther" | "browserTabs"
  | "office" | "source" | "mcp" | "skill" | "workflow" | "background" | "other";
export interface SummaryClause { kind: SummaryClauseKind; count: number; object?: string }
export interface ActivitySummary {
  clauses: SummaryClause[];
  total: number;
  successful: number;
  failuresObserved: number;
  running: number;
  gated: number;
  incomplete: number;
  notExecuted: number;
  controlled: number;
  denied: number;
  timedOut: number;
  cancelled: number;
  awaitingInput: number;
  unknown: number;
  /** Only codes recorded by a failed call, not the server's current status. */
  environmentErrors: string[];
}
const record = (value: unknown): Record<string, unknown> => value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
const nonempty = (value: unknown): value is string => typeof value === "string" && value.trim().length > 0;
const ENVIRONMENT_CODES = new Set(["BROWSER_RESOURCE_CAPACITY", "BROWSER_SESSION_EXPIRED", "BROWSER_CONTEXT_CHANGED", "BROWSER_TAB_CLOSED"]);

/** Narrow compatibility with known error-code prefixes; ordinary result prose
 * is never used to infer recovery, execution success or a relationship. */
export function recordedActivityErrorCode(call: TimelineCall): string | undefined {
  if (!call.result?.is_error) return undefined;
  const details = record(call.result.details);
  const explicit = details.error_code ?? details.code;
  if (typeof explicit === "string") return explicit;
  const text = call.result.text.trim();
  const nativeError = call.name === "browser" ? text.replace(/^browser [a-z_]+ failed: /, "") : text;
  const prefix = /^(BROWSER_[A-Z_]+|TAB_HUMAN_CONTROL)(?=[:\s]|$)/.exec(nativeError)?.[1];
  if (prefix) return prefix;
  if (call.name === "browser") {
    try { const code = record(record(JSON.parse(text)).error).code; return nonempty(code) ? code : undefined; }
    catch { /* No structured code: ordinary output stays opaque. */ }
  }
  return undefined;
}

export function pureSleepSeconds(call: Pick<TimelineCall, "name" | "args">): number | undefined {
  if (call.name !== "bash") return;
  const command = record(call.args).command;
  if (typeof command !== "string") return;
  // No shell parsing by substring: a pipeline, chained command or substitution
  // stays a command. The value is a requested duration, never measured time.
  const match = /^\s*sleep\s+(\d+(?:\.\d+)?)([smh]?)\s*$/.exec(command);
  if (!match) return;
  const seconds = Number(match[1]) * (match[2] === "h" ? 3600 : match[2] === "m" ? 60 : 1);
  return Number.isFinite(seconds) ? seconds : undefined;
}

function browserDetails(call: TimelineCall): BrowserDetails | undefined {
  const value = record(call.result?.details);
  return call.name === "browser" && typeof value.action === "string" && typeof value.ok === "boolean" ? value as unknown as BrowserDetails : undefined;
}

/** Document identity, not action identity: control-lease epoch and URL/title
 * changes are not used to manufacture distinct pages. */
export function browserSummaryPageKey(page: BrowserPageSummary): string | undefined {
  if (!nonempty(page.context_ref) || !nonempty(page.tab_id) || !Number.isSafeInteger(page.generation)
    || !Number.isSafeInteger(page.document_version) || page.generation! < 0 || page.document_version! < 0) return;
  return JSON.stringify([page.context_ref, page.tab_id, page.generation, page.document_version]);
}

export function summarizeActivityCalls(calls: readonly TimelineCall[]): ActivitySummary {
  const out: ActivitySummary = { clauses: [], total: 0, successful: 0, failuresObserved: 0,
    running: 0, gated: 0, incomplete: 0, notExecuted: 0, controlled: 0, denied: 0,
    timedOut: 0, cancelled: 0, awaitingInput: 0, unknown: 0, environmentErrors: [] };
  const clauses = new Map<string, SummaryClause>();
  const distinct = new Map<string, Set<string>>();
  const pages = { browserOpen: { valid: true, ids: new Set<string>() }, browserRead: { valid: true, ids: new Set<string>() } };
  const seen = new Set<string>();
  const add = (kind: SummaryClauseKind, count = 1, object?: string, identity?: string) => {
    const key = `${kind}\0${object ?? ""}`;
    if (identity !== undefined) {
      let identities = distinct.get(key); if (!identities) distinct.set(key, identities = new Set());
      if (identities.has(identity)) return;
      identities.add(identity);
    }
    const clause = clauses.get(key);
    if (clause) clause.count += count;
    else { const next = { kind, count, ...(object ? { object } : {}) }; clauses.set(key, next); out.clauses.push(next); }
  };

  for (const call of calls) {
    if (seen.has(call.callId)) continue;
    seen.add(call.callId); out.total++;
    if (call.inputInvalid) { out.notExecuted++; continue; }
    const state = toolCallState(call);
    if (state === "gate") { out.gated++; continue; }
    if (state === "running") { out.running++; continue; }
    const result = call.result;
    if (!result) { if (call.orphaned) out.incomplete++; else out.unknown++; continue; }
    if (["request_user_input", "request_user_input_async"].includes(call.name) && record(result.details).status === "pending") { out.awaitingInput++; continue; }
    const reason = result.text.trim();
    const errorCode = recordedActivityErrorCode(call);
    const control = toolControlOutcome(call);
    if (control === "human-control") { out.controlled++; continue; }
    if (control === "denied") { out.denied++; continue; }
    if (control === "approval-timeout") { out.timedOut++; continue; }
    if (control === "cancelled") { out.cancelled++; continue; }
    if (result.is_error && /^Tool(?: call)? "[^"\n]+" was not executed:/.test(reason)) { out.notExecuted++; continue; }
    const bash = isBashDetails(result.details) ? result.details : undefined;
    if (bash?.background && (bash.status === "cancelled" || bash.status === "interrupted")) { out.cancelled++; continue; }
    if (state === "orphaned") { out.incomplete++; continue; }
    const browser = browserDetails(call);
    // A batch is one call; parent and child failure counters are not added.
    if (state === "error" || browser?.ok === false) {
      out.failuresObserved++;
      if (call.name === "browser" && errorCode && ENVIRONMENT_CODES.has(errorCode) && !out.environmentErrors.includes(errorCode)) out.environmentErrors.push(errorCode);
      continue;
    }
    if (browser?.observation_pending || browser?.batch && browser.batch.stop_reason !== "completed") { out.unknown++; continue; }
    out.successful++;
    const name = shortcutCallTool(call.name, call.args), args = record(call.args), path = nonempty(args.path) ? args.path : undefined;
    if (name === "read") {
      if (isReadManyDetails(result.details)) {
        result.details.files.filter(file => !file.error && nonempty(file.path)).forEach(file => add("read", 1, undefined, file.path));
      } else if (path) add("read", 1, undefined, path);
      else add("readOperations");
    } else if (name === "ls") add("list", 1, undefined, path);
    else if (name === "find" || name === "grep") add("search");
    else if (name === "write" || name === "edit") add("write", 1, undefined, path);
    else if (name === "bash") add(pureSleepSeconds(call) === undefined ? "command" : "wait");
    else if (name.startsWith("shell_")) add("command");
    else if (name === "browser") {
      const action = typeof args.action === "string" ? args.action : browser?.action;
      const tab = action === "tab" ? args.tab_action ?? args.operation ?? args.op : undefined;
      const kind = action === "tabs" || action === "tab" && ["list", "switch", "claim"].includes(String(tab)) ? "browserTabs"
        : ["navigate", "open_pages"].includes(action ?? "") || action === "tab" && tab === "new" ? "browserOpen"
        : ["snapshot", "extract", "observe_pages", "find"].includes(action ?? "") ? "browserRead"
        : action === "click" ? "browserClick" : ["type", "fill"].includes(action ?? "") ? "browserType" : "browserOther";
      add(kind);
      if (kind === "browserOpen" || kind === "browserRead") {
        const recorded = browser?.pages;
        if (!recorded?.length || recorded.some(page => !page.ok || page.status !== "observed" || !browserSummaryPageKey(page))) pages[kind].valid = false;
        else recorded.forEach(page => pages[kind].ids.add(browserSummaryPageKey(page)!));
      }
    } else if (name === "docs_office" || name.startsWith("docs_")) add("office");
    else if (["information", "tool_result", "mcp_describe", "shortcut_read", "shortcut_query"].includes(name)) add("source");
    else if (isMCPDetails(result.details)) add("mcp", 1, result.details.server || undefined);
    else if (name.startsWith("mcp_")) add("mcp");
    else if (isSkillDetails(result.details) || name.startsWith("skill_")) add("skill");
    else if (name.startsWith("agent_workflow_")) add("workflow");
    else if (name === "background_task" || name.startsWith("watch")) add("background");
    else add("other");
  }
  for (const kind of ["browserOpen", "browserRead"] as const) {
    const clause = clauses.get(`${kind}\0`), evidence = pages[kind];
    if (clause && evidence.valid && evidence.ids.size) { clause.kind = kind === "browserOpen" ? "browserOpenPages" : "browserReadPages"; clause.count = evidence.ids.size; }
  }
  // Tab management/waits remain inspectable rows, but do not crowd out real
  // work. A run containing only these still gets a truthful fallback clause.
  if (out.clauses.some(clause => clause.kind !== "browserTabs" && clause.kind !== "wait")) {
    out.clauses = out.clauses.filter(clause => clause.kind !== "browserTabs" && clause.kind !== "wait");
  }
  return out;
}

export interface RetryIdentity { sessionId: string; exchangeId: string; runId: string }
export interface HistoricalFailureGroup { id: string; calls: TimelineCall[]; errorCode: string }
function canonical(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(canonical).join(",")}]`;
  if (value && typeof value === "object") return `{${Object.keys(value).sort().map(key => `${JSON.stringify(key)}:${canonical(record(value)[key])}`).join(",")}}`;
  return JSON.stringify(value) ?? "null";
}

/** Candidate groups only. The view must additionally enforce reading/selection
 * protection before displaying a group; source items are never rewritten. No
 * current TimelineCall field carries a run id, so callers without an explicit
 * origin map correctly receive no groups, even for identical error prose. */
export function historicalFailureGroups(run: ActivityRun, origins: ReadonlyMap<string, RetryIdentity> = new Map()): HistoricalFailureGroup[] {
  if (!run.closed) return [];
  const groups: HistoricalFailureGroup[] = [];
  let previousKey: string | undefined, previous: HistoricalFailureGroup | undefined;
  for (const item of run.items) {
    if (item.kind === "thinking") continue;
    const call = item.call, origin = call && origins.get(call.callId), args = record(call?.args);
    const code = call && recordedActivityErrorCode(call);
    let resource: string | undefined;
    if (call?.name === "browser" && nonempty(args.action) && nonempty(args.context_ref) && nonempty(args.tab_id) && Number.isSafeInteger(args.control_epoch)) resource = canonical(args);
    else if (call && isMCPDetails(call.result?.details) && nonempty(call.result.details.server) && Object.keys(args).length) resource = `${call.result.details.server}\0${canonical(args)}`;
    else if (call && nonempty(args.path)) resource = canonical(args);
    if (!call || call.running || call.gate || call.childGates?.length || call.inputInvalid || !call.result?.is_error
      || !code || code === "TAB_HUMAN_CONTROL" || !resource || !origin || !origin.sessionId || !origin.exchangeId || !origin.runId) {
      previous = undefined; previousKey = undefined; continue;
    }
    const key = JSON.stringify([origin.sessionId, origin.exchangeId, origin.runId, call.name, resource, code]);
    if (previous && previousKey === key) previous.calls.push(call);
    else { previous = { id: `retry:${call.callId}`, calls: [call], errorCode: code }; groups.push(previous); previousKey = key; }
  }
  return groups.filter(group => group.calls.length > 1);
}
