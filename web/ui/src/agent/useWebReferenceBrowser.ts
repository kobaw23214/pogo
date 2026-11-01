import { onBeforeUnmount, ref, watch } from "vue";
import { api } from "@/api/client";
import { randomUUID } from "@/utils/uuid";
import { browserControlMemory, browserOperationRetrySame, clearBrowserLease, completeBrowserState, installBrowserLease, observeBrowserControl, retireBrowserControls } from "./browserSheets";
import { nativeViewedTab, type NativeBrowserState } from "./nativeBrowser";

type Transport = Pick<typeof api, "browserAuthoringCreate" | "browserAuthoringClose" | "browserState" | "browserTabs" | "browserControl">;
interface OwnedPage { token: string; epoch?: number; closeOperation: string }
interface BrowserResource {
  source: string; scope: string; session: string; temporary: boolean; context: string; viewed: string;
  pages: Map<string, OwnedPage>; createOperation: string; tabOperation: string; closed: boolean;
  createAttempted: boolean;
  flight?: Promise<void>; cleanup?: Promise<void>;
}

/** Owns only a new tab of an existing conversation, or a disposable browser-only
 * session in Manager. No user page is navigated, taken over, or closed on open.
 * Late POST receipts are retained long enough to clean up their exact resource. */
export function useWebReferenceBrowser(options: {
  active: () => boolean; sourceSession: () => string | null | undefined; scope: () => string;
  transport?: Transport; errorText?: (error: unknown) => string;
}) {
  const transport = options.transport || api;
  const sessionId = ref(""), state = ref<NativeBrowserState | null>(null), loading = ref(false), error = ref("");
  let resource: BrowserResource | undefined, disposed = false, timer: ReturnType<typeof setTimeout> | undefined;
  const current = (value: BrowserResource) => !disposed && !value.closed && resource === value && options.active() && options.scope() === value.scope && (options.sourceSession() || "") === value.source;
  const message = (cause: unknown) => options.errorText?.(cause) || (cause instanceof Error ? cause.message : String(cause));
  const valid = (value: Awaited<ReturnType<Transport["browserState"]>>): NativeBrowserState => {
    if (value.backend !== "native") throw new Error(value.error || "Browser unavailable");
    if (!completeBrowserState(value)) throw new Error("Browser returned an incomplete page binding");
    return value;
  };
  function remember(value: BrowserResource, next: NativeBrowserState) {
    if (next.context_ref !== value.context) return;
    observeBrowserControl(value.session, next);
    // Discover only descendants that the worker binds to our own opener.
    for (let pass = 0; pass < next.tabs.length; pass++) for (const page of next.tabs) {
      if (!value.temporary && !value.pages.has(page.id) && (!page.opener_tab || !value.pages.has(page.opener_tab))) continue;
      const lease = browserControlMemory(value.session, value.context, page.id);
      if (!value.pages.has(page.id)) value.pages.set(page.id, { token: lease.token, epoch: lease.epoch, closeOperation: randomUUID() });
    }
  }
  function publish(value: BrowserResource, next: NativeBrowserState) {
    if (!current(value) || next.context_ref !== value.context || nativeViewedTab(next) !== value.viewed) return;
    remember(value, next);
    const before = state.value;
    if (before && nativeViewedTab(before) === value.viewed && ((next.generation || 0) < (before.generation || 0) || next.generation === before.generation && (next.document_version || 0) < (before.document_version || 0))) return;
    sessionId.value = value.session;
    state.value = { ...next, tabs: next.tabs.filter(page => value.pages.has(page.id)) };
  }
  function ownedReceipt(value: BrowserResource, receipt: Awaited<ReturnType<Transport["browserTabs"]>>) {
    const next = valid(receipt.state);
    if (receipt.context_ref !== value.context || next.context_ref !== value.context || nativeViewedTab(next) !== receipt.tab_id || !next.tabs.some(page => page.id === receipt.tab_id) || !receipt.lease_token || next.control !== "human" || !Number.isSafeInteger(next.control_epoch)) throw new Error("Browser returned an invalid new-page receipt");
    value.pages.set(receipt.tab_id, { token: receipt.lease_token, epoch: next.control_epoch, closeOperation: randomUUID() });
    installBrowserLease(value.session, value.context, receipt.tab_id, next.control_epoch, receipt.lease_token);
    value.viewed = receipt.tab_id; value.tabOperation = "";
    return next;
  }
  async function closePage(value: BrowserResource, tab: string, page: OwnedPage) {
    const live = browserControlMemory(value.session, value.context, tab);
    const token = live.token || page.token, epoch = live.token ? live.epoch : page.epoch;
    if (!token) return;
    const request = { action: "close" as const, context_ref: value.context, tab_id: tab, lease_token: token, op_id: page.closeOperation };
    for (let attempt = 0; attempt < 2; attempt++) {
      try {
        await transport.browserTabs(value.session, request);
        clearBrowserLease(value.session, value.context, tab, token, epoch); return;
      } catch (cause) {
        if (!browserOperationRetrySame(cause)) return;
        // Retry only this exact close operation, never a new action or lease.
      }
    }
  }
  async function clean(value: BrowserResource): Promise<void> {
    if (value.cleanup) return value.cleanup;
    value.closed = true;
    value.cleanup = (async () => {
      // Let a late creation finish so its ID/lease can be cleaned, not leaked.
      await value.flight?.catch(() => {});
      if (value.temporary && !value.session && value.createAttempted) {
        // A failed response does not prove POST failed. Resolve only our same
        // owner-scoped UUID, then delete; never start or publish a browser.
        try {
          const created = await transport.browserAuthoringCreate(value.createOperation);
          if (/^browser-authoring-[a-f0-9]{48}$/.test(created.session_id)) value.session = created.session_id;
        } catch { /* A confirmed expired operation or disconnected client is bounded by server TTL. */ }
      }
      if (!value.session) return;
      if (value.temporary) {
        try { await transport.browserAuthoringClose(value.session); retireBrowserControls(value.session, ""); } catch { /* Backend idle/max TTL also closes disconnected clients. */ }
        return;
      }
      if (value.tabOperation && value.context) {
        try { ownedReceipt(value, await transport.browserTabs(value.session, { action: "new", context_ref: value.context, op_id: value.tabOperation })); } catch { /* Unknown outcomes keep the same operation; never issue a replacement or close a guessed tab. */ }
      }
      const anchor = value.pages.keys().next().value;
      if (anchor) {
        try {
          const latest = valid(await transport.browserState(value.session, undefined, { context_ref: value.context, tab_id: anchor }));
          if (latest.context_ref === value.context && nativeViewedTab(latest) === anchor) remember(value, latest);
        } catch { /* A missing binding cannot authorize guessing other pages. */ }
      }
      for (const [tab, page] of [...value.pages].reverse()) await closePage(value, tab, page);
    })();
    return value.cleanup;
  }
  function stop() {
    clearTimeout(timer); timer = undefined;
    const old = resource; resource = undefined;
    sessionId.value = ""; state.value = null; loading.value = false; error.value = "";
    if (old) void clean(old);
  }
  function heartbeat(value: BrowserResource) {
    clearTimeout(timer);
    timer = setTimeout(async () => {
      if (!current(value) || !value.context || !value.viewed) return;
      try { publish(value, valid(await transport.browserState(value.session, undefined, { context_ref: value.context, tab_id: value.viewed }))); }
      catch (cause) { if (current(value)) error.value = message(cause); }
      if (current(value)) heartbeat(value);
    }, 60_000);
  }
  async function connect(value: BrowserResource) {
    if (value.flight) return value.flight;
    loading.value = true; error.value = "";
    const flight = (async () => {
      try {
        if (!value.session) {
          value.createAttempted = true;
          const created = await transport.browserAuthoringCreate(value.createOperation);
          if (!/^browser-authoring-[a-f0-9]{48}$/.test(created.session_id)) throw new Error("Invalid browser authoring session");
          value.session = created.session_id;
        }
        if (!current(value)) return;
        let next = valid(await transport.browserState(value.session));
        if (!current(value)) return;
        if (value.context && next.context_ref !== value.context) { value.pages.clear(); value.tabOperation = ""; value.viewed = ""; }
        value.context = next.context_ref!;
        if (value.temporary) {
          value.viewed = nativeViewedTab(next);
          if (!value.viewed) {
            value.tabOperation ||= randomUUID();
            next = ownedReceipt(value, await transport.browserTabs(value.session, { action: "new", context_ref: value.context, op_id: value.tabOperation }));
          } else {
            remember(value, next);
            const memory = browserControlMemory(value.session, value.context, value.viewed);
            if (!memory.token && next.control_epoch === 0 && Number.isSafeInteger(next.generation)) {
              // Fresh contexts begin Agent-owned at epoch zero. Adoption is
              // reserved for an unassigned HUMAN page, not that initial state.
              const target = { context_ref: value.context, tab_id: value.viewed };
              const claim = await transport.browserControl(value.session, "human", next.control === "human" ? { ...target, generation: next.generation, control_epoch: 0, adopt_if_unowned: true } : target);
              next = valid(claim);
              if (claim.lease_token) installBrowserLease(value.session, value.context, value.viewed, next.control_epoch, claim.lease_token);
            }
          }
        } else if (!value.viewed) {
          value.tabOperation ||= randomUUID();
          next = ownedReceipt(value, await transport.browserTabs(value.session, { action: "new", context_ref: value.context, op_id: value.tabOperation }));
        } else next = valid(await transport.browserState(value.session, undefined, { context_ref: value.context, tab_id: value.viewed }));
        publish(value, next);
        if (current(value)) heartbeat(value);
      } catch (cause) {
        if (!browserOperationRetrySame(cause)) value.tabOperation = "";
        if (value.temporary && [404, 410].includes((cause as { status?: number }).status || 0)) {
          // Only a confirmed expired/closed handle allows the next explicit
          // retry to create a fresh session. Unknown POST outcomes keep UUID.
          value.session = ""; value.context = ""; value.viewed = ""; value.pages.clear(); value.createOperation = randomUUID();
        }
        if (current(value)) error.value = message(cause);
      } finally { if (current(value)) loading.value = false; }
    })();
    value.flight = flight;
    try { await flight; } finally { if (value.flight === flight) value.flight = undefined; }
  }
  function start() {
    if (disposed || !options.active()) return;
    const source = options.sourceSession() || "";
    const value: BrowserResource = { source, scope: options.scope(), session: source, temporary: !source, context: "", viewed: "", pages: new Map(), createOperation: randomUUID(), tabOperation: "", closed: false, createAttempted: false };
    resource = value; void connect(value);
  }
  function retry() { if (resource && current(resource)) void connect(resource); else start(); }
  function recover() { stop(); start(); }
  async function switchTab(tab: string) {
    const value = resource;
    if (!value || !current(value) || loading.value || !value.pages.has(tab) || value.viewed === tab) return;
    const previous = value.viewed; value.viewed = tab; loading.value = true; error.value = "";
    try { publish(value, valid(await transport.browserState(value.session, undefined, { context_ref: value.context, tab_id: tab }))); }
    catch (cause) { if (current(value)) { value.viewed = previous; error.value = message(cause); } }
    finally { if (current(value)) loading.value = false; }
  }
  function observe(next: NativeBrowserState) {
    const value = resource;
    if (value && current(value)) publish(value, next);
  }
  watch(() => [options.active(), options.sourceSession(), options.scope()], () => { stop(); start(); }, { immediate: true, flush: "sync" });
  onBeforeUnmount(() => { disposed = true; stop(); });
  return { sessionId, state, loading, error, retry, recover, switchTab, observe, close: stop };
}
