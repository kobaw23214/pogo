import { reactive } from "vue";
import type { DockTab } from "@/components/dockSheets";
import { randomUUID } from "@/utils/uuid";
import type { NativeBrowserState, NativeBrowserTarget } from "./nativeBrowser";
import { browserFavicon } from "./browserFavicon";

export interface BrowserSheetBinding extends NativeBrowserTarget { sessionId: string }
export function validBrowserBinding(value: unknown): value is BrowserSheetBinding {
  if (!value || typeof value !== "object") return false;
  const v = value as BrowserSheetBinding;
  return typeof v.sessionId === "string" && !!v.sessionId && /^ctx_[a-f0-9]{24}$/.test(v.context_ref) && /^t[1-9]\d*$/.test(v.tab_id);
}
export function browserSheetVisible(tab: DockTab, sessionId: string | null): boolean {
  if (tab.kind === "html" || tab.kind === "skill") return tab.parentSession === sessionId;
  if (tab.kind === "sentinel") return !tab.parentSession || tab.parentSession === sessionId;
  return tab.kind !== "browser" || (!tab.browserBinding ? !!sessionId : tab.browserBinding.sessionId === sessionId);
}
export function browserSheetID() { return `browser:${randomUUID()}`; }
export function targetMatches(state: NativeBrowserState, target: NativeBrowserTarget): boolean {
  return state.context_ref === target.context_ref && state.viewed_tab === target.tab_id;
}
export function completeBrowserState(state: NativeBrowserState): boolean {
  if (!/^ctx_[a-f0-9]{24}$/.test(state.context_ref ?? "") || !Array.isArray(state.tabs) || !state.tabs.every(page => page && /^t[1-9]\d*$/.test(page.id) && typeof page.url === "string" && typeof page.title === "string")) return false;
  const ids = new Set(state.tabs.map(page => page.id));
  return ids.size === state.tabs.length && (!state.viewed_tab || ids.has(state.viewed_tab)) && (!state.active_tab || ids.has(state.active_tab));
}
/** Reconcile a complete, ordered discovery snapshot for one context. Pane
 * observations can arrive out of order and must pass removeMissing=false.
 * Old contexts become local recovery records, never executable bindings.
 * Only complete discovery can retire them; saved URLs are never replayed. */
export function reconcileBrowserSheets(tabs: DockTab[], sid: string, state: NativeBrowserState, newTabLabel = "New tab", removeMissing = true): DockTab[] {
  if (!completeBrowserState(state)) return tabs;
  const liveIds = new Set(state.tabs.map(page => page.id));
  // A contradictory/truncated snapshot cannot prove a tab was closed.
  if (liveIds.size !== state.tabs.length || (state.viewed_tab && !liveIds.has(state.viewed_tab)) || (state.active_tab && !liveIds.has(state.active_tab))) return tabs;
  const next = tabs.filter(tab => !removeMissing || tab.kind !== "browser" || tab.browserBinding?.sessionId !== sid
    || (tab.browserBinding.context_ref === state.context_ref ? liveIds.has(tab.browserBinding.tab_id) : tab.url !== "about:blank"))
    .map(tab => removeMissing && tab.kind === "browser" && tab.browserBinding?.sessionId === sid && tab.browserBinding.context_ref !== state.context_ref
      ? { ...tab, browserUnavailable: true } : tab);
  const unbound = next.findIndex(t => t.kind === "browser" && !t.browserBinding && !t.browserUnavailable);
  for (const page of state.tabs) {
    const binding: BrowserSheetBinding = { sessionId: sid, context_ref: state.context_ref!, tab_id: page.id };
    const at = next.findIndex(t => t.kind === "browser" && t.browserBinding?.sessionId === sid && t.browserBinding.context_ref === binding.context_ref && t.browserBinding.tab_id === page.id);
    const existing = at >= 0 ? next[at] : undefined;
    const favicon = page.url === "about:blank" ? undefined : browserFavicon(page.favicon);
    const tab: DockTab = { id: existing?.id ?? browserSheetID(), kind: "browser", label: page.url === "about:blank" ? newTabLabel : page.title || page.url || "Browser", url: page.url, ...(favicon ? { favicon } : {}), ...(existing?.browserDraft ? { browserDraft: existing.browserDraft } : {}), browserBinding: binding, closable: true };
    if (at >= 0) next[at] = tab;
    else if (unbound >= 0 && !next[unbound].browserBinding && page.id === (state.viewed_tab || state.active_tab || state.tabs[0]?.id)) next[unbound] = { ...tab, id: next[unbound].id };
    else next.push(tab);
  }
  return next;
}

/** Keep a user's surviving selection, even when Agent focus or popup tabs
 * change. Only a removed selection needs a replacement from live pages. */
export function reconciledBrowserSelection(tabs: DockTab[], active: string | null, sid: string, state: NativeBrowserState, manuallySelectedRecovery = false): string | null {
  const visible = tabs.filter(tab => browserSheetVisible(tab, sid));
  const selected = visible.find(tab => tab.id === active);
  if (selected && (selected.kind !== "browser" || (selected.browserUnavailable && manuallySelectedRecovery) || (!selected.browserUnavailable && selected.browserBinding?.context_ref === state.context_ref))) return active;
  const liveIds = new Set(state.tabs.map(page => page.id));
  const pages = visible.filter(tab => tab.kind === "browser" && tab.browserBinding?.sessionId === sid && tab.browserBinding.context_ref === state.context_ref && liveIds.has(tab.browserBinding.tab_id));
  return pages.find(tab => tab.browserBinding?.tab_id === state.viewed_tab)?.id
    ?? pages.find(tab => tab.browserBinding?.tab_id === state.active_tab)?.id
    ?? pages[0]?.id ?? selected?.id ?? visible[0]?.id ?? null;
}

// Credentials are window-local and bound to one session/context/Page. Two
// Sheets viewing the same Page share its lease. An actual child popup may use
// its opener's lease only while the worker proves that exact inheritance.
interface BrowserControlMemory { token: string; epoch?: number; mode?: "agent" | "human"; context: string; tab: string }
const controls = new Map<string, BrowserControlMemory>();
// A popup can be discovered after its opener was handed to Agent. Preserve a
// small private history so that transition cannot strand the already-created
// child. The worker's current lineage/epoch must still authorize the child.
const previousLeases = new Map<string, Map<number, string>>();
const controlKey = (sessionId: string, context: string, tab: string) => JSON.stringify([sessionId, context, tab]);
function rememberLease(sessionId: string, control: BrowserControlMemory) {
  if (!control.token || !Number.isSafeInteger(control.epoch) || control.epoch! < 0) return;
  const key = controlKey(sessionId, control.context, control.tab);
  const history = previousLeases.get(key) ?? new Map<number, string>();
  history.set(control.epoch!, control.token);
  while (history.size > 8) history.delete(history.keys().next().value!);
  previousLeases.set(key, history);
}
/** A new authoritative context cannot inherit a local lease from an old one. */
export function retireBrowserControls(sessionId: string, currentContext: string) {
  for (const [key, control] of controls) {
    const [sid] = JSON.parse(key) as string[];
    if (sid === sessionId && control.context !== currentContext) { control.token = ""; controls.delete(key); }
  }
  for (const key of previousLeases.keys()) {
    const [sid, context] = JSON.parse(key) as string[];
    if (sid === sessionId && context !== currentContext) previousLeases.delete(key);
  }
}
export function browserControlMemory(sessionId: string, context: string, tab: string): BrowserControlMemory {
  const key = controlKey(sessionId, context, tab);
  let control = controls.get(key);
  if (!control) { control = reactive({ token: "", context, tab }); controls.set(key, control); }
  return control;
}
export function observeBrowserControl(sessionId: string, state: NativeBrowserState) {
  if (!state.context_ref) return;
  const observe = (tab: string, mode: "agent" | "human", epoch: number | undefined) => {
    const control = browserControlMemory(sessionId, state.context_ref!, tab);
    if ((epoch ?? -1) < (control.epoch ?? -1) || epoch === control.epoch && control.mode === "agent" && mode === "human") return;
    if (mode !== "human" || epoch !== control.epoch || state.reset) {
      if (!state.reset) rememberLease(sessionId, control);
      control.token = "";
      if (state.reset) previousLeases.delete(controlKey(sessionId, state.context_ref!, tab));
    }
    if (Number.isInteger(epoch)) { control.epoch = epoch; control.mode = mode; }
  };
  for (const page of state.tabs) if (page.control && Number.isInteger(page.control_epoch)) observe(page.id, page.control, page.control_epoch);
  const viewed = state.viewed_tab ?? state.active_tab;
  if (viewed) observe(viewed, state.control, state.control_epoch);
  if (state.reset) return;
  // A popup is registered after its opener and can itself open another popup.
  // Resolve a finite chain without trusting array order or granting siblings.
  const pages = new Map(state.tabs.map(page => [page.id, page]));
  for (let pass = 0; pass < state.tabs.length; pass++) {
    let inherited = false;
    for (const child of state.tabs) {
      const epoch = child.inherited_control_epoch;
      if (child.control !== "human" || !Number.isSafeInteger(epoch) || epoch! < 0
        || child.control_epoch !== epoch || !child.opener_tab || !/^t[1-9]\d*$/.test(child.opener_tab) || child.opener_tab === child.id) continue;
      const parent = pages.get(child.opener_tab);
      if (parent && (!Number.isSafeInteger(parent.control_epoch) || parent.control_epoch! < epoch!)) continue;
      // The opener may already have closed. Worker-proven child lineage still
      // binds its inherited lease to this window's exact former opener.
      const source = browserControlMemory(sessionId, state.context_ref, child.opener_tab);
      const target = browserControlMemory(sessionId, state.context_ref, child.id);
      const token = source.mode === "human" && source.epoch === epoch && source.token
        || previousLeases.get(controlKey(sessionId, state.context_ref, child.opener_tab))?.get(epoch!);
      if (!token || target.token) continue;
      inherited = installBrowserLease(sessionId, state.context_ref, child.id, epoch, token) || inherited;
    }
    if (!inherited) break;
  }
}

/** Unknown outcomes retain the exact operation identity; only confirmed
 * pre-dispatch rejection permits a later explicit request with a new ID. */
export function browserOperationRetrySame(error: unknown): boolean {
  const err = error as { code?: string; message?: string; status?: number };
  const code = err?.code || err?.message?.match(/TAB_OPERATION_[A-Z_]+/)?.[0];
  if (code === "TAB_OPERATION_REJECTED") return false;
  if (code && ["TAB_OPERATION_PENDING", "TAB_OPERATION_OUTCOME_UNKNOWN", "TAB_OPERATION_ID_CONFLICT"].includes(code)) return true;
  return ![400, 401, 403, 404, 410, 429].includes(err?.status ?? 0);
}

export function installBrowserLease(sessionId: string, context: string, tab: string, epoch: number | undefined, token: string): boolean {
  const control = browserControlMemory(sessionId, context, tab);
  if (!token || !Number.isInteger(epoch) || (control.epoch ?? -1) > epoch!
    || control.epoch === epoch && control.mode === "agent") return false;
  if (control.epoch === epoch && control.token && control.token !== token) return false;
  if (control.epoch !== epoch) rememberLease(sessionId, control);
  Object.assign(control, { context, epoch, token, mode: "human" });
  return true;
}
export function clearBrowserLease(sessionId: string, context: string, tab: string, token: string, epoch?: number): boolean {
  const control = browserControlMemory(sessionId, context, tab);
  if (control.context !== context || control.token !== token || control.epoch !== epoch) return false;
  rememberLease(sessionId, control);
  control.token = "";
  return true;
}
/** Operation identities survive same-window reloads; credentials never do. */
export class BrowserNewOperations {
  readonly key = "pogo:browser-new-operations:v1";
  private pending: Record<string, string> = {};
  constructor(private storage: Pick<Storage, "getItem" | "setItem">) {
    const raw = storage.getItem(this.key);
    if (raw) {
      const value: unknown = JSON.parse(raw);
      if (value && typeof value === "object" && !Array.isArray(value)) for (const [key, id] of Object.entries(value)) if (typeof id === "string" && /^[a-zA-Z0-9-]{1,128}$/.test(id)) this.pending[key] = id;
    }
  }
  private index(sid: string, context: string) { return JSON.stringify([sid, context]); }
  get(sid: string, context: string): string {
    const key = this.index(sid, context);
    const id = this.pending[key] ?? randomUUID();
    this.pending[key] = id;
    this.storage.setItem(this.key, JSON.stringify(this.pending));
    return id;
  }
  complete(sid: string, context: string) {
    delete this.pending[this.index(sid, context)];
    this.storage.setItem(this.key, JSON.stringify(this.pending));
  }
}
