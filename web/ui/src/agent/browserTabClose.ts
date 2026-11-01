import type { api } from "@/api/client";
import { randomUUID } from "@/utils/uuid";
import { browserControlMemory, browserOperationRetrySame, clearBrowserLease, installBrowserLease, observeBrowserControl, targetMatches, type BrowserSheetBinding } from "./browserSheets";
import type { NativeBrowserState } from "./nativeBrowser";

type CloseAPI = Pick<typeof api, "browserControl" | "browserTabs"> & Partial<Pick<typeof api, "browserState">>;
type PendingClose = { op_id: string; lease_token?: string; epoch?: number; generation?: number; control_epoch?: number };
const key = (binding: BrowserSheetBinding) => JSON.stringify([binding.sessionId, binding.context_ref, binding.tab_id]);

/** A click on × is the user's explicit close intent. Input ownership remains
 * page-scoped, and an uncertain close keeps its original operation identity. */
export class BrowserTabClosures {
  private pending = new Map<string, PendingClose & { binding: BrowserSheetBinding }>();
  private closed = new Set<string>();
  private flights = new Map<string, Promise<NativeBrowserState | undefined>>();

  isClosed(binding: BrowserSheetBinding): boolean {
    return this.closed.has(key(binding));
  }

  close(binding: BrowserSheetBinding, client: CloseAPI): Promise<NativeBrowserState | undefined> {
    const id = key(binding);
    const existing = this.flights.get(id);
    if (existing) return existing;
    const flight: Promise<NativeBrowserState | undefined> = this.closeOnce(binding, client).finally(() => {
      if (this.flights.get(id) === flight) this.flights.delete(id);
    });
    this.flights.set(id, flight);
    return flight;
  }

  private async closeOnce(binding: BrowserSheetBinding, client: CloseAPI): Promise<NativeBrowserState | undefined> {
    const id = key(binding);
    let pending: PendingClose | undefined = this.pending.get(id);
    let acquired = false;
    try {
      // Check pending first: a lost close receipt can be recovered even after
      // the Page and its lease have disappeared. Never reacquire for that retry.
      if (!pending) {
        // Allocate before any mutation. A close is independent of document
        // readiness and must not wait for an input takeover while loading.
        const op_id = randomUUID();
        const memory = browserControlMemory(binding.sessionId, binding.context_ref, binding.tab_id);
        if (!memory.token && client.browserState) {
          const next = await client.browserState(binding.sessionId, undefined, { context_ref: binding.context_ref, tab_id: binding.tab_id });
          if (next.backend !== "native" || !targetMatches(next, binding) || !Number.isSafeInteger(next.generation) || next.generation! < 1
            || !Number.isSafeInteger(next.control_epoch) || next.control_epoch! < 0 || !["agent", "human"].includes(next.control)) throw new Error("Browser close observation does not match the tab being closed");
          observeBrowserControl(binding.sessionId, next);
          if ((memory.epoch ?? -1) > next.control_epoch!) throw new Error("Browser control changed before the tab could close");
          if (next.control === "human" && next.control_epoch! > 0 && !memory.token) throw new Error("Browser tab is controlled by another human");
          // The server rechecks this exact Page generation and ownership epoch
          // atomically; a competing takeover cannot authorize a stale close.
          pending = { op_id, ...(memory.token ? { lease_token: memory.token, epoch: memory.epoch } : {}), generation: next.generation, control_epoch: next.control_epoch };
        } else if (!memory.token) {
          // Compatibility for older embedded clients without targeted reads.
          // The Web client always uses the nonblocking close path above.
          const next = await client.browserControl(binding.sessionId, "human", { context_ref: binding.context_ref, tab_id: binding.tab_id });
          if (!targetMatches(next, binding) || next.control !== "human" || !next.lease_token) throw new Error("Browser control reply does not match the tab being closed");
          observeBrowserControl(binding.sessionId, next);
          if (!installBrowserLease(binding.sessionId, binding.context_ref, binding.tab_id, next.control_epoch, next.lease_token)) throw new Error("Browser control changed before the tab could close");
          acquired = true;
        }
        pending ??= { op_id, lease_token: memory.token, epoch: memory.epoch };
        this.pending.set(id, { ...pending, binding: { ...binding } });
      }
      const result = await client.browserTabs(binding.sessionId, { action: "close", op_id: pending.op_id, lease_token: pending.lease_token, generation: pending.generation, control_epoch: pending.control_epoch, context_ref: binding.context_ref, tab_id: binding.tab_id });
      if (result.tab_id !== binding.tab_id || result.context_ref !== binding.context_ref || result.state.context_ref !== binding.context_ref || result.state.tabs.some(tab => tab.id === binding.tab_id)) throw new Error("Browser close outcome is not confirmed; retry with the same operation ID");
      this.closed.add(id);
      this.pending.delete(id);
      if (pending.lease_token) clearBrowserLease(binding.sessionId, binding.context_ref, binding.tab_id, pending.lease_token, pending.epoch);
      return result.state;
    } catch (error) {
      const status = (error as { status?: number }).status;
      if (status === 410) {
        this.closed.add(id);
        this.pending.delete(id);
        if (pending?.lease_token) clearBrowserLease(binding.sessionId, binding.context_ref, binding.tab_id, pending.lease_token, pending.epoch);
        return;
      }
      // A takeover has no idempotent operation ID. A lost takeover receipt is
      // surfaced, never force-replayed or applied to a different Page.
      if (pending) {
        if (pending.lease_token && (status === 401 || status === 403)) clearBrowserLease(binding.sessionId, binding.context_ref, binding.tab_id, pending.lease_token, pending.epoch);
        if (!browserOperationRetrySame(error)) {
          this.pending.delete(id);
          // If this click acquired an idle Page but closing was rejected before
          // dispatch, restore its Agent ownership without touching siblings.
          if (acquired && pending.lease_token && status !== 401 && status !== 403) {
            try {
              const next = await client.browserControl(binding.sessionId, "agent", { context_ref: binding.context_ref, tab_id: binding.tab_id, lease_token: pending.lease_token });
              observeBrowserControl(binding.sessionId, next);
              clearBrowserLease(binding.sessionId, binding.context_ref, binding.tab_id, pending.lease_token, pending.epoch);
            } catch { /* A changed lease must not be overridden. */ }
          }
        }
      }
      throw error;
    }
  }

  /** Hidden panes and HTTP discovery can finish with pre-close snapshots.
   * Confirmed retired identities must never become Sheets again. */
  filterState(sessionId: string, state: NativeBrowserState, authoritative = false): NativeBrowserState | undefined {
    if (!Array.isArray(state.tabs) || !state.tabs.every(tab => tab && /^t[1-9]\d*$/.test(tab.id) && typeof tab.url === "string" && typeof tab.title === "string")) return state;
    // A fresh complete discovery can confirm a lost close receipt. A hidden
    // Pane or an old idempotency response cannot prove that a Page disappeared.
    const ids = new Set(state.tabs.map(tab => tab.id));
    if (authoritative && ids.size === state.tabs.length && (!state.active_tab || ids.has(state.active_tab)) && (!state.viewed_tab || ids.has(state.viewed_tab))) {
      for (const [id, pending] of this.pending) {
        const binding = pending.binding;
        if (binding.sessionId !== sessionId || binding.context_ref !== state.context_ref || ids.has(binding.tab_id)) continue;
        this.closed.add(id);
        this.pending.delete(id);
        if (pending.lease_token) clearBrowserLease(binding.sessionId, binding.context_ref, binding.tab_id, pending.lease_token, pending.epoch);
      }
    }
    const retired = (tab: string) => this.closed.has(key({ sessionId, context_ref: state.context_ref!, tab_id: tab }));
    if (state.viewed_tab && retired(state.viewed_tab)) return;
    const tabs = state.tabs.filter(tab => !retired(tab.id));
    return tabs.length === state.tabs.length ? state : { ...state, tabs, active_tab: retired(state.active_tab) ? "" : state.active_tab };
  }
}
