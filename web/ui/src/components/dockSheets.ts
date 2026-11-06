import type { PanelInfo } from "@/api/types";
import { randomUUID } from "@/utils/uuid";
import { validBrowserBinding, type BrowserSheetBinding } from "@/agent/browserSheets";
import { BROWSER_VIEW_ID } from "@/agent/browserBookmarks";
import { browserFavicon } from "@/agent/browserFavicon";

// The dock's rail is chrome: it carries the
// runtime's own affordances, never domain content. Growing it by one icon per
// registered -web-panel put a data application next to "files" and "shell",
// which are properties of the running agent — the mismatch is what read as
// out of place.
//
// So: "files" keeps its own button (high traffic, and it is workspace state
// read alongside the conversation), and everything else becomes a tab of the
// "hub". The hub is a browser-style tab strip rather than a dropdown: the
// shell, each -web-panel and every opened subagent transcript are peers there,
// and a strip is the control that shows all of them — which one is up, which
// are still running — at a glance. A dropdown hides exactly that; see the
// opencode and Claude Code tab requests for how the complaint reads when it
// does.
export type SheetId = "files" | "hub" | "shortcuts" | "tasks" | "skills" | "workflows";

export const PANEL_PREFIX = "panel:";
export const SUBAGENT_PREFIX = "subagent:";
export const BROWSER_PREFIX = "browser:";
export const PREVIEW_PREFIX = "preview:";
export const SKILL_PREFIX = "skill:";
export const HTML_PREFIX = "html:";
export const WEBSITE_PREFIX = "website:";
export const SENTINEL_PREFIX = "sentinel:";
export const TASK_PREFIX = "task:";

export type TabKind = "shell" | "panel" | "subagent" | "browser" | "preview" | "skill" | "html" | "website" | "sentinel" | "task";

export interface OpenFileTarget {
  path: string;
  name: string;
  skill?: string;
  skillPath?: string;
}

export interface DockTab {
  /** "shell", "panel:<name>", "subagent:<child session id>", "browser:<n>", or
   * "preview:<root-relative file path>". */
  id: string;
  label: string;
  kind: TabKind;
  /** Reverse-proxied path (/panels/<name>/) for panel tabs; the file the
   * preview tab shows (root-relative, workspace included) for preview tabs.
   * Skill tabs instead carry a package-relative document path. */
  path?: string;
  /** A Skill document is read through its selected session, never the workspace. */
  skill?: string;
  /** The current file opened from a citation; not a historical snapshot. */
  citation?: boolean;
  lineStart?: number;
  lineEnd?: number;
  /** Repeated file or citation clicks reveal their location in an open tab. */
  revealVersion?: number;
  /** The child transcript's session id; subagent tabs only. */
  childSession?: string;
  /** Retained child identity; parent is captured from the open conversation. */
  agentId?: string;
  parentSession?: string;
  /** Host-authored Sentinel identity, independent of whether it has a worker yet. */
  sentinelId?: string;
  /** Host task identity. A selected result is a hint, revalidated by its sheet. */
  taskId?: string;
  taskSource?: "schedule";
  taskSourceId?: string;
  taskRunSession?: string;
  /** The URL a free browser tab opens at; browser tabs only. */
  url?: string;
  /** Transient capability for a client-rendered HTML tab; never persisted. */
  htmlPreviewId?: string;
  /** Stable website reference only; a pane owns its transient grant URL. */
  webServiceId?: string;
  /** Explicit open click, deliberately omitted from persisted tabs. */
  websiteOpenRequest?: number;
  /** Validated image data from the current browser page, never a remote URL. */
  favicon?: string;
  browserBinding?: BrowserSheetBinding;
  browserDraft?: string;
  browserUnavailable?: boolean;
  /** Every sheet in the hub is a chosen occupant — shell, panels and browser
   * tabs included — so every one of them is closable. Closing the last one
   * leaves the workspace open on its empty state (the closeEmptyGroups:false
   * behaviour): an empty workspace is a place to add from, not a shut dock. */
  closable: boolean;
}

export const SHEET_KEY = "pogo:active-sheet";
export const TABS_KEY = "pogo:hub-tabs";
/** Pre-tab-strip builds remembered one tenant here; read once for migration. */
export const TENANT_KEY = "pogo:hub-tenant";

/** What a subagent card's anchor asks the dock to open. */
export interface OpenSheetRequest {
  /** The child transcript's session id, reduced off its path by childSessionId. */
  childSession: string;
  /** Server-issued retained identity; the parent is never accepted from tool data. */
  agentId?: string;
  /** The delegated brief, clipped — the tab's label. */
  label: string;
}

export interface StoredSheet {
  /** SHEET_KEY. Pre-hub builds wrote "shell" or "panel:<name>" here. */
  sheet: string | null;
  /** Pre-sheet builds: "pogo:files-open". */
  legacyFiles?: boolean;
  /** Pre-sheet builds: "pogo:shell-open". */
  legacyShell?: boolean;
}

export interface SheetState {
  sheet: SheetId | null;
  /** Set only when the stored id carried a tab id; null means "keep TABS_KEY". */
  tab: string | null;
}

/**
 * migrateSheet folds every id previous builds could have persisted onto the
 * current sheet rail. It runs on every boot rather than once, which is what makes
 * it safe: the mapping is idempotent, so a downgrade-then-upgrade cannot strand
 * someone on a sheet that no longer exists.
 *
 * An id from a *newer* build is the one case that cannot be mapped, and it
 * opens nothing — a closed dock is recoverable from the rail, whereas guessing
 * could open a sheet the user never chose.
 */
export function migrateSheet(stored: StoredSheet): SheetState {
  const v = stored.sheet;
  // Memory management moved to platform settings. Do not restore a removed
  // sheet as an empty open dock for users with an older saved layout.
  if (v === "memory") return { sheet: null, tab: null };
  if (v === "files" || v === "hub" || v === "shortcuts" || v === "tasks" || v === "skills" || v === "workflows") return { sheet: v, tab: null };
  if (v === "shell") return { sheet: "hub", tab: "shell" };
  if (v && v.startsWith(PANEL_PREFIX)) return { sheet: "hub", tab: v };
  if (v) return { sheet: null, tab: null };
  if (stored.legacyFiles) return { sheet: "files", tab: null };
  if (stored.legacyShell) return { sheet: "hub", tab: "shell" };
  return { sheet: null, tab: null };
}

/**
 * openTab appends a tab unless its id is already there: clicking the same
 * subagent anchor twice focuses the one tab, it does not stack duplicates.
 * Previews opened at a line also update their location and reveal it again.
 */
export function openTab(tabs: DockTab[], tab: DockTab): DockTab[] {
  if (tab.kind === "website" && tabs.some(t => t.id === tab.id)) return tabs.map(t => t.id === tab.id ? { ...t, websiteOpenRequest: tab.websiteOpenRequest, label: tab.label } : t);
  if (tab.kind === "preview" && (tab.citation || tab.lineStart !== undefined)) {
    const existing = tabs.find((t) => t.id === tab.id);
    const next = { ...tab, revealVersion: (existing?.revealVersion ?? 0) + 1 };
    return existing ? tabs.map((t) => t.id === tab.id ? next : t) : [...tabs, next];
  }
  if (tab.kind === "sentinel" && tab.agentId && tabs.some(t => t.id === tab.id)) {
    return tabs.map(t => t.id === tab.id ? { ...t, label: tab.label, agentId: tab.agentId, parentSession: tab.parentSession, sentinelId: tab.sentinelId } : t);
  }
  if (tab.kind === "task") {
    const next = normalizeStoredTab(tab);
    if (!next) return tabs;
    return tabs.some(t => t.id === next.id) ? tabs.map(t => t.id === next.id ? { ...next, taskRunSession: next.taskRunSession ?? normalizeStoredTab(t)?.taskRunSession } : t) : [...tabs, next];
  }
  if (tab.kind === "subagent" && tab.agentId && tab.parentSession && tabs.some(t => t.id === tab.id)) {
    return tabs.map(t => t.id === tab.id ? { ...t, agentId: tab.agentId, parentSession: tab.parentSession } : t);
  }
  return tabs.some((t) => t.id === tab.id) ? tabs : [...tabs, tab];
}

/**
 * closeTab removes a tab and settles the active one: closing the tab that was
 * showing activates its left neighbour, which is where a browser puts you.
 */
export function closeTab(
  tabs: DockTab[],
  active: string | null,
  id: string,
): { tabs: DockTab[]; active: string | null } {
  const at = tabs.findIndex((t) => t.id === id);
  const next = tabs.filter((t) => t.id !== id);
  if (active !== id) return { tabs: next, active };
  const fallback = next[Math.max(0, Math.min(at, next.length - 1))];
  return { tabs: next, active: fallback?.id ?? null };
}

/**
 * resolveTab picks the tab to render. A remembered id that no longer exists —
 * a -web-panel dropped from the command line since last visit — falls back to
 * the first one instead of leaving the hub blank, because an empty container
 * with no explanation reads as a broken tab.
 */
export function resolveTab(tabs: DockTab[], wanted: string | null): DockTab | null {
  if (tabs.length === 0) return null;
  return tabs.find((t) => t.id === wanted) ?? tabs[0];
}

export interface StoredTabs {
  /** Subagent tabs only; the pinned set is rebuilt from the server each boot. */
  open?: DockTab[];
  active?: string | null;
  /** TENANT_KEY, the pre-strip single-tenant memory. */
  legacyTenant?: string | null;
  browserActive?: Record<string, string | null>;
}

/** Restore the workspace after migration. The browser is one session view;
 * actual page tabs live in Chromium. Old entrypoints are preserved separately
 * by migrateBrowserStorage before their dock wrappers are collapsed. */
export function restoreTabs(stored: StoredTabs, _panels: PanelInfo[]): { tabs: DockTab[]; active: string | null } {
  const tabs: DockTab[] = [];
  const wanted = stored.active ?? stored.legacyTenant;
  let active: string | null = null;
  for (const item of stored.open ?? []) {
    const restored = normalizeStoredTab(item);
    if (restored && !tabs.some(tab => tab.id === restored.id)) tabs.push(restored);
    if (restored && item.id === wanted) active = restored.id;
  }
  return { tabs, active: active ?? tabs[0]?.id ?? null };
}

function normalizeStoredTab(t: DockTab): DockTab | null {
  if (!t || typeof t !== "object") return null;
  if (t.kind === "website") {
    const valid = (value: unknown): value is string => typeof value === "string" && /^[A-Za-z0-9_-]{1,128}$/.test(value);
    if (!valid(t.parentSession) || !valid(t.webServiceId) || t.id !== `${WEBSITE_PREFIX}${t.parentSession}:${t.webServiceId}`) return null;
    return { id: t.id, label: typeof t.label === "string" && t.label ? t.label : t.webServiceId, kind: "website", parentSession: t.parentSession, webServiceId: t.webServiceId, closable: true };
  }
  if (t.kind === "task") {
    const valid = (value: unknown): value is string => typeof value === "string" && /^[A-Za-z0-9_.-]{1,128}$/.test(value) && !value.includes("..");
    const source = (value: unknown): value is string => typeof value === "string" && /^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$/.test(value);
    if (t.taskSource !== "schedule" || !valid(t.agentId) || !source(t.taskSourceId) || t.taskId !== `schedule:${t.taskSourceId}` ||
        t.taskRunSession !== undefined && !valid(t.taskRunSession)) return null;
    const id = `${TASK_PREFIX}${t.agentId}:${t.taskId}`;
    if (t.id !== id) return null;
    return { id, kind: "task", taskId: t.taskId, taskSource: "schedule", taskSourceId: t.taskSourceId, agentId: t.agentId,
      ...(t.taskRunSession ? { taskRunSession: t.taskRunSession } : {}), label: typeof t.label === "string" && t.label ? t.label : t.taskId, closable: true };
  }
  if (t.kind === "sentinel") {
    const valid = (value: unknown): value is string => typeof value === "string" && /^[A-Za-z0-9_-]{1,160}$/.test(value);
    if (!valid(t.sentinelId) || !valid(t.agentId) || t.parentSession !== undefined && !valid(t.parentSession)) return null;
    return { id: SENTINEL_PREFIX + t.sentinelId, kind: "sentinel", sentinelId: t.sentinelId, agentId: t.agentId,
      ...(t.parentSession ? { parentSession: t.parentSession } : {}), label: typeof t.label === "string" && t.label ? t.label : "Sentinel", closable: true };
  }
  if (t.kind === "shell") return { id: "shell", label: t.label || "Shell", kind: "shell", closable: true };
  if (t.kind === "html" && typeof t.path === "string" && typeof t.parentSession === "string" && t.path && /^[A-Za-z0-9_.-]{1,128}$/.test(t.parentSession)) {
    return { id: HTML_PREFIX + t.parentSession + ":" + t.path, label: t.label || t.path.split("/").pop() || t.path, kind: "html", path: t.path, parentSession: t.parentSession, closable: true };
  }
  if (t.kind === "subagent" && t.childSession) {
    const identity = (value: unknown) => typeof value === "string" && /^[A-Za-z0-9_.-]{1,256}$/.test(value) ? value : undefined;
    const agentId = identity(t.agentId), parentSession = identity(t.parentSession);
    return { id: SUBAGENT_PREFIX + t.childSession, label: t.label, kind: "subagent", childSession: t.childSession, closable: true,
      ...(agentId ? { agentId } : {}), ...(parentSession ? { parentSession } : {}) };
  }
  if (t.kind === "browser" && validBrowserBinding(t.browserBinding)) return { id: t.id || `browser:${randomUUID()}`, label: t.label || "Browser", kind: "browser", url: t.url, ...(t.browserUnavailable ? { browserUnavailable: true } : {}), ...(t.url !== "about:blank" && browserFavicon(t.favicon) ? { favicon: browserFavicon(t.favicon) } : {}), ...(t.browserDraft ? { browserDraft: t.browserDraft } : {}), browserBinding: { ...t.browserBinding }, closable: true };
  if (t.kind === "browser" && (t.browserUnavailable || "browserBinding" in t)) return { ...t, id: t.id || `browser:${randomUUID()}`, browserUnavailable: true, closable: true };
  if (t.kind === "browser" || t.kind === "panel") return { id: BROWSER_VIEW_ID, label: t.label || "Browser", kind: "browser", url: t.url || t.path, closable: true };
  if (t.kind === "skill" && typeof t.skill === "string" && t.skill && typeof t.path === "string" && t.path &&
      typeof t.parentSession === "string" && /^[A-Za-z0-9_.-]{1,128}$/.test(t.parentSession)) {
    return { id: `${SKILL_PREFIX}${t.parentSession}:${t.skill}:${t.path}`, label: t.label || t.path.split("/").pop() || t.path,
      kind: "skill", skill: t.skill, path: t.path, parentSession: t.parentSession, closable: true };
  }
  if (t.kind === "preview" && t.path) {
    const base = t.path.split("/").pop() ?? t.path;
    return { id: PREVIEW_PREFIX + t.path, label: t.label || base, kind: "preview", path: t.path, closable: true };
  }
  return null;
}
