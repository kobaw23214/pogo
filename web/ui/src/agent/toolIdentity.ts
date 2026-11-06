import { computed, inject, onBeforeUnmount, provide, shallowRef, watch, type InjectionKey, type Ref } from "vue";
import { api } from "@/api/client";
import type { BuiltinToolEntry, McpServerGroup, ToolCatalogEntry, ToolDetails } from "@/api/types";
import { gt } from "@/i18n";
import { shortcutCallTool } from "./shortcutToolNames";
import { isMCPDetails, isSkillDetails, summarizeArgs } from "./timeline";
import type { ToolGlyphKind } from "./toolPresentation";

export interface ToolIdentityEntry {
  wireName: string;
  name: string;
  title?: string;
  server?: string;
  skill?: string;
  renderer?: string;
  family: "mcp" | "skill" | "builtin";
}
export type ToolIdentityCatalog = ReadonlyMap<string, ToolIdentityEntry>;
export interface ToolCatalogResponse { tools: ToolCatalogEntry[]; mcp: McpServerGroup[]; builtin?: BuiltinToolEntry[]; }

const EMPTY_CATALOG: ToolIdentityCatalog = new Map();
const emptyCatalog = shallowRef<ToolIdentityCatalog>(EMPTY_CATALOG);
const catalogKey: InjectionKey<Readonly<Ref<ToolIdentityCatalog>>> = Symbol("tool-identity-catalog");

/** Only an explicit wire mapping identifies an external tool. In particular,
 * hashed wire names are not split back into a guessed server or skill. */
export function buildToolIdentityCatalog(response: ToolCatalogResponse): ToolIdentityCatalog {
  const index = new Map<string, ToolIdentityEntry>();
  for (const entry of response.builtin ?? []) {
    index.set(entry.name, { wireName: entry.name, name: entry.name, family: "builtin" });
  }
  for (const entry of response.tools ?? []) {
    if (!entry.wire_name) continue;
    index.set(entry.wire_name, { wireName: entry.wire_name, name: entry.name, title: entry.title, skill: entry.skill, renderer: entry.renderer, family: "skill" });
  }
  for (const server of response.mcp ?? []) {
    for (const entry of server.tools ?? []) {
      if (!entry.wire_name) continue;
      index.set(entry.wire_name, { wireName: entry.wire_name, name: entry.name, title: entry.title, server: entry.server || server.name, family: "mcp" });
    }
  }
  return index;
}

/** A request token also separates refreshes of the same session. A late reply
 * may never relabel another conversation, including A → B → A transitions. */
export function createToolIdentityCatalogSession(load: (session: string) => Promise<ToolCatalogResponse> = api.tools) {
  const catalog = shallowRef<ToolIdentityCatalog>(EMPTY_CATALOG);
  let session: string | undefined;
  let request = 0;
  let disposed = false;
  async function refresh() {
    const target = session;
    const token = ++request;
    if (!target || disposed) return;
    try {
      const result = await load(target);
      if (!disposed && token === request && target === session) catalog.value = buildToolIdentityCatalog(result);
    } catch {
      // Display metadata is optional. Keep only this session's last good
      // catalog; a session change always clears it before loading.
    }
  }
  async function setSession(next: string | undefined) {
    if (next !== session) {
      session = next;
      ++request;
      catalog.value = EMPTY_CATALOG;
    }
    await refresh();
  }
  function dispose() { disposed = true; ++request; catalog.value = EMPTY_CATALOG; }
  return { catalog, refresh, setSession, dispose };
}

export function provideToolIdentityCatalog(session: () => string | undefined, revision?: () => unknown) {
  const controller = createToolIdentityCatalogSession();
  const stop = watch(() => [session(), revision?.()] as const, ([id]) => { void controller.setSession(id); }, { immediate: true });
  provide(catalogKey, computed(() => controller.catalog.value));
  onBeforeUnmount(() => { stop(); controller.dispose(); });
  return controller;
}

export function useToolIdentityCatalog(): Readonly<Ref<ToolIdentityCatalog>> {
  return inject(catalogKey, emptyCatalog);
}

export const BUILTIN_TOOL_GLYPHS: Readonly<Record<string, ToolGlyphKind>> = {
  read: "read", ls: "list", find: "explore", grep: "explore", write: "change", edit: "change", bash: "command", browser: "browser", analyze_image: "read", docs_office: "office",
  information: "read", tool_result: "read", mcp_describe: "mcp", shell_tasks: "background", shell_output: "command", shell_stop: "background",
  shortcut_query: "shortcut", shortcut_manage: "shortcut", shortcut_search: "shortcut", shortcut_read: "shortcut", shortcut_create: "shortcut",
  shortcut_list: "shortcut", shortcut_folder_create: "shortcut", shortcut_update: "shortcut", shortcut_folder_rename: "shortcut",
  shortcut_move: "shortcut", shortcut_delete: "shortcut", shortcut_merge: "shortcut",
  skill_search: "skill", skill_read: "skill", skill_save: "skill", background_task: "background", watch: "background", watch_test: "background", watch_decide: "background",
  agent_workflow_read: "read", agent_workflow_edit: "change", agent_workflow_enable: "change", agent_workflow_run: "background",
  subagent: "subagent", subagent_tasks: "subagent", todo: "todo", request_user_input: "todo", request_user_input_async: "todo",
};

const browserActions = new Set(["navigate", "back", "snapshot", "extract", "find", "click", "hover", "type", "fill", "select", "press", "scroll", "screenshot", "wait", "batch", "tabs", "tab", "download", "downloads", "upload", "bookmarks", "devtools", "calculate", "open_pages", "observe_pages"]);
const object = (value: unknown): Record<string, unknown> => value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
const string = (value: unknown): string | undefined => typeof value === "string" && value.trim() ? value.trim() : undefined;
const first = (...values: unknown[]) => values.map(string).find(Boolean);
const id = (value: unknown): string | undefined => typeof value === "number" && Number.isFinite(value) ? String(value) : string(value);

/** Deliberately limited to a literal single command. Shell chains, substitutions,
 * units and helper commands remain commands, not guessed waiting periods. */
export function pureSleepSeconds(args: unknown): number | undefined {
  const command = string(object(args).command);
  const match = command?.match(/^sleep\s+(\d+(?:\.\d+)?)\s*$/);
  if (!match) return undefined;
  const seconds = Number(match[1]);
  return Number.isFinite(seconds) && seconds >= 0 ? seconds : undefined;
}

function browserTarget(args: unknown, details?: ToolDetails): string | undefined {
  const a = object(args), d = object(details), target = object(a.target);
  const action = string(a.action) || string(d.action);
  if (action === "open_pages") {
    const urls = Array.isArray(a.urls) ? a.urls.filter((url): url is string => typeof url === "string" && !!url.trim()) : [];
    return urls.length ? urls.join(" · ") : undefined;
  }
  if (action === "observe_pages") {
    const pages = Array.isArray(a.pages) ? a.pages.map(page => object(page)) : [];
    const labels = pages.map(page => first(page.url, page.title) || id(page.tab_id)).filter(Boolean);
    return labels.length ? labels.join(" · ") : undefined;
  }
  // A recorded result may supply the page title, but never an unrelated live tab.
  const page = first(d.title, d.url, a.url) || id(a.tab_id) || id(d.active_tab);
  if (["click", "hover", "type", "fill", "select", "press", "find"].includes(action ?? "")) {
    return first(target.text, target.label, target.name, a.target, a.selector, target.selector, a.query, a.key, a.name, a.label, a.placeholder, a.testid, action === "find" ? a.text : undefined) || id(a.ref) || page;
  }
  if (action === "batch") return Array.isArray(a.actions) ? gt("toolCall.steps", { n: a.actions.length }) : page;
  if (action === "tabs") return first(a.context_ref, d.context_ref) || gt("toolCall.browserSession");
  if (action === "tab") return first(a.url, a.target) || id(a.tab_id) || first(a.tab_action, a.operation, a.op) || page;
  if (action === "wait") return first(a.target, a.url, a.text, a.gone_text) || page;
  if (action === "scroll") return first(a.target, a.direction) || page;
  if (action === "download" || action === "downloads") return first(a.download_id, a.url) || page;
  if (action === "upload") return first(a.path) || (Array.isArray(a.files) ? a.files.map(id).filter(Boolean).join(" · ") || undefined : undefined) || page;
  if (action === "devtools") return first(a.method) || page;
  if (action === "calculate") return first(a.operation) || page;
  if (action === "bookmarks") return first(a.query, a.url) || page;
  return page;
}

export interface ToolIdentity {
  glyph: ToolGlyphKind;
  name: string;
  target?: string;
  secondary?: string;
  protocolName: string;
  server?: string;
  renderer?: string;
}

export function resolveToolIdentity(name: string, args?: unknown, details?: ToolDetails, catalog: ToolIdentityCatalog = EMPTY_CATALOG): ToolIdentity {
  const normalized = shortcutCallTool(name, args);
  const entry = catalog.get(name);
  const builtin = Object.prototype.hasOwnProperty.call(BUILTIN_TOOL_GLYPHS, normalized);
  const a = object(args), d = object(details);
  let label = builtin ? gt(`toolCall.names.${normalized}`) : name;
  let glyph: ToolGlyphKind = builtin ? BUILTIN_TOOL_GLYPHS[normalized] : name.startsWith("mcp_") ? "mcp" : name.startsWith("skill_") ? "skill" : "tool";
  let secondary: string | undefined;
  let server: string | undefined;
  if (!builtin && isMCPDetails(details)) {
    label = first(details.title, details.tool) || name;
    server = string(details.server);
    secondary = [server, details.tool !== label ? string(details.tool) : undefined].filter(Boolean).join(" · ") || undefined;
    glyph = "mcp";
  } else if (!builtin && isSkillDetails(details)) {
    label = first(details.capability) || name;
    secondary = gt("toolCall.runtimeSkill");
    glyph = "skill";
  } else if (!builtin && entry) {
    label = first(entry.title, entry.name) || name;
    server = entry.server;
    secondary = first(entry.server, entry.skill);
    glyph = entry.family === "mcp" ? "mcp" : entry.family === "skill" ? "skill" : "tool";
  }
  let target: string | undefined;
  if (normalized === "browser") {
    const action = first(a.action, d.action);
    if (action && browserActions.has(action)) label = gt(`toolCall.browserActions.${action}`);
    target = browserTarget(args, details);
  } else {
    target = summarizeArgs(normalized, args) || undefined;
    if (normalized === "docs_office") target = first(d.path, a.path, a.document_id, a.file_id) || target;
    if (normalized === "information") target = first(a.query, a.topic, a.title, a.url) || target;
    if (normalized === "tool_result") target = first(a.call_id, a.result_id, a.path) || target;
    if (normalized === "mcp_describe") target = first(a.server, a.tool, a.name) || target;
    if (normalized.startsWith("shell_")) target = first(a.task_id, a.job_id) || target;
    if (normalized === "analyze_image") target = first(d.path, a.image) || target;
    if (normalized.startsWith("docs_")) target = first(d.path, a.path, a.file, a.file_path) || target;
    if (normalized.startsWith("dispatch_to_")) {
      label = gt("toolCall.delegate"); glyph = "subagent";
      target = first(a.task, a.prompt) || target;
    }
    const sleep = normalized === "bash" ? pureSleepSeconds(args) : undefined;
    if (sleep !== undefined) { label = gt("toolCall.wait"); target = gt("toolCall.seconds", { n: sleep }); }
  }
  return { glyph, name: label || gt("toolCall.unknownTool"), target, secondary, protocolName: name, server, renderer: entry?.renderer };
}
