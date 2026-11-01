import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Ref } from "vue";
import { reactive } from "vue";
import type { OfficeActivitySnapshot, OfficeBridgeMessage, OfficeCommand, OfficeOpen, OfficeSession } from "@/agent/officeTransport";
import type { OfficeActivityItem } from "@/agent/officeActivityView";

// These tests run the actual component's controller without mounting a fake
// Office canvas. Engine, messages and HTTP are bounded host doubles only.
const hooks = vi.hoisted(() => ({ mounted: [] as (() => unknown)[], unmounted: [] as (() => void)[], request: vi.fn(), confirm: vi.fn(), nextId: 0 }));
vi.mock("element-plus", () => ({ ElMessageBox: { confirm: hooks.confirm } }));
vi.mock("vue", async (original) => ({ ...await original<typeof import("vue")>(), onMounted: (callback: () => unknown) => { hooks.mounted.push(callback); }, onBeforeUnmount: (callback: () => void) => { hooks.unmounted.push(callback); }, useId: () => `unit-${++hooks.nextId}`, useSSRContext: () => ({ modules: new Set() }) }));
vi.mock("vue-i18n", () => ({ useI18n: () => ({ locale: { value: "zh-CN" }, t: (key: string, params?: { message?: string }) => params?.message ? `${key}: ${params.message}` : key }) }));
vi.mock("@element-plus/icons-vue", () => ({ ArrowDown: {}, Document: {}, RefreshLeft: {}, Loading: {}, DocumentChecked: {} }));
vi.mock("@/api/client", () => ({ officeRequest: hooks.request }));
let OfficeEditor: typeof import("./OfficeEditor.vue")["default"];
let confirmStudioNavigation: typeof import("@/agent/studioNavigation")["confirmStudioNavigation"];
let registerOfficeView: typeof import("@/agent/officeTransport")["registerOfficeView"];

function deferred<T>() { let resolve!: (value: T) => void, reject!: (error: unknown) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; }
const versionA = "a".repeat(64), versionB = "b".repeat(64);
const freshSession = (): OfficeSession => ({ id: "office-session-a", documentId: "document-a", conversationId: "conversation-a", path: "test.docx", fileRef: "file-identity-a", bootEpoch: "boot-a", title: "Test", fileType: "docx", editor: "word", generation: 1, ready: false, closed: false, dirty: false, dirtyEpoch: 0, persistedEpoch: 0, saveState: "saved", revision: 0, blocked: false, pending: false, canvasVersion: versionA, diskVersion: versionA, syncRevision: 1 });
const copy = <T>(value: T): T => JSON.parse(JSON.stringify(value)) as T;
type EngineEvents = { onDocumentReady(): void; onDocumentStateChange(event: { data: boolean }): void; onDownloadAs(event: { data: { url: string; fileType: string } }): void; onError(event: { data?: { errorDescription?: string } }): void };
type Controller = { root: Ref<unknown>; ready: Ref<boolean>; canQueue: Ref<boolean>; dirty: Ref<boolean>; loading: Ref<boolean>; failed: Ref<string>; stateLabel: Ref<string>; saving: Ref<boolean>; inputLocked: Ref<boolean>; syncFrozen: Ref<boolean>; syncState: Ref<string>; syncError: Ref<string>; retainedCanvas: Ref<unknown>; session: Ref<OfficeSession | null>; activity: Ref<OfficeActivitySnapshot>; activityItems: Ref<OfficeActivityItem[]>; canRecover: Ref<boolean>; recoverySession: Ref<OfficeSession | null>; leasePaused: Ref<boolean>; leaseFenced: Ref<boolean>; prepareClose(): Promise<boolean>; refreshStatus(): Promise<void>; save(): Promise<boolean>; recoverCleanSession(): Promise<boolean>; run(operation: "history.undo" | "history.redo"): Promise<void>; cancelBatch(): Promise<void>; discardAndReload(): Promise<void>; retrySynchronization(): Promise<void>; };
let host: ReturnType<typeof createHost>;

type StartupController = Controller & {
  startupPhase: Ref<string>; startupElapsed: Ref<number>; startupUnconfirmed: Ref<boolean>;
  bootstrapReleasePending: Ref<boolean>; editorReady: Ref<boolean>; autoSaveEnabled: Ref<boolean>; retryStartup(): Promise<void>;
};
function createHost() {
  const live = freshSession();
  const opened: OfficeOpen = { requestedPath: "test.docx", bootstrapToken: "unused-bootstrap-a", session: copy(live), config: {}, browserURL: "/office/editor", bridgeURL: "/office/bridge/office-session-a/test-cap/", bridgeToken: "synthetic-capability-aaaaaaaaaaaaaaaa", instanceOwned: true };
  const listeners = new Map<string, Set<(event: unknown) => void>>();
  const calls: { method: string; path: string; body?: Record<string, unknown> }[] = [];
  const queue: OfficeCommand[] = [];
  const sent: Record<string, unknown>[] = [];
  const readyAcks: Record<string, unknown>[] = [];
  const downloads: string[] = [];
  const beacons: { url: string; payload: Blob }[] = [];
  const lifetime: string[] = [];
  const input = { inert: false, isConnected: true };
  const inputListeners = new Map<string, Set<(event: unknown) => void>>();
  const inputDocument = {
    querySelectorAll: () => [],
    addEventListener: (name: string, listener: (event: unknown) => void) => { if (!inputListeners.has(name)) inputListeners.set(name, new Set()); inputListeners.get(name)!.add(listener); },
    removeEventListener: (name: string, listener: (event: unknown) => void) => { inputListeners.get(name)?.delete(listener); },
  };
  const inputFrame = { contentDocument: inputDocument, addEventListener() {}, removeEventListener() {} };
  let events!: EngineEvents;
  let constructors = 0;
  let claimInstance = "";
  let closeGate: ReturnType<typeof deferred<boolean>> | null = null;
  let openGate: ReturnType<typeof deferred<boolean>> | null = null;
  let statusOverride: OfficeSession | null = null;
  let readyOverride: OfficeSession | null = null;
  let resultGate: ReturnType<typeof deferred<{ ok: boolean; ignored?: boolean }>> | null = null;
  let nextStateError: Error | null = null;
  let lostOpenResponses = 0;
  let releaseGate: ReturnType<typeof deferred<boolean>> | null = null;
  let nextPollError: Error | null = null;
  let requireRecoveryRead = false;
  const recoveryReadCommands = new Set<string>();
  const finishedFlights = new Map<string, { operationId: string; finished: boolean; readOnly: boolean; reconciled?: boolean; known?: boolean; succeeded?: boolean }>();
  let nextSaveError: Error | null = null;
  let nextConfigError: Error | null = null;
  let nextStatusError: Error | null = null;
  let nextHeartbeatError: Error | null = null;
  let nextHeartbeatSession: OfficeSession | null = null;
  let nextReleaseError: Error | null = null;
  let nextEngineError: Error | null = null;
  let requestedOverride: string | null = null;
  let nextFileRef: string | null = null;
  let autoReady = true;
  let versionReplacement = false;
  let replacementVersion = versionB;
  let retainAbortedState = false;
  let rejectFreeze = false;
  let rejectReloadReady = false;
  let unconfirmedReloadReady = false;
  const reloadReply = deferred<unknown>();
  const readReplies = new Map<string, ReturnType<typeof deferred<unknown>>>();
  let saveReceipt = deferred<unknown>(), exportReceipt = deferred<unknown>();
  const uiCommandReceipt = deferred<unknown>();
  const source = { postMessage: (value: Record<string, unknown>) => { if (value.type === "ready-ack") readyAcks.push(value); else sent.push(value); } };
  const windowHost = {
    DocsAPI: undefined as unknown,
    addEventListener: (name: string, callback: (event: unknown) => void) => { if (!listeners.has(name)) listeners.set(name, new Set()); listeners.get(name)!.add(callback); },
    removeEventListener: (name: string, callback: (event: unknown) => void) => { listeners.get(name)?.delete(callback); },
  };
  vi.stubGlobal("location", { origin: "https://pogo.test", href: "https://pogo.test/" });
  vi.stubGlobal("window", windowHost);
  vi.stubGlobal("navigator", { sendBeacon: (url: string, payload: Blob) => { beacons.push({ url, payload }); return true; } });
  vi.stubGlobal("document", {
    createElement: () => ({ src: "", onload: undefined as undefined | (() => void), onerror: undefined as undefined | (() => void), remove() {} }),
    head: { append: (script: { onload?: () => void; onerror?: () => void }) => {
      if (nextEngineError) { nextEngineError = null; script.onerror?.(); return; }
      windowHost.DocsAPI = { DocEditor: class {
        constructor(_id: string, config: { events: EngineEvents }) { constructors += 1; events = config.events; if (autoReady) events.onDocumentReady(); }
        downloadAs(format: string) { downloads.push(format); }
        destroyEditor() { lifetime.push("destroy"); }
      } };
      script.onload?.();
    } },
  });
  hooks.request.mockImplementation(async (method: string, path: string, body?: Record<string, unknown>) => {
    calls.push({ method, path, body });
    if (method === "GET" && path.startsWith("/api/office/sessions?")) return { sessions: live.closed ? [] : [copy(live)], requestedPath: new URL(path, "https://pogo.test").searchParams.get("path"), fileRef: live.fileRef };
    if ((method === "POST" && path === "/api/office/sessions") || path.includes("/config?")) {
      if (path.includes("/config?") && nextConfigError) { const error = nextConfigError; nextConfigError = null; throw error; }
      const instance = String(body?.instanceId || new URL(path, "https://pogo.test").searchParams.get("instanceId") || "");
      if (live.instanceBound && claimInstance !== instance && !live.closed) throw Object.assign(new Error("already connected"), { status: 409, code: "EDITOR_ALREADY_CONNECTED" });
      if (live.closed) {
        Object.assign(live, freshSession(), { id: "office-session-b", documentId: "document-b", generation: 2, bootEpoch: "boot-b" });
        if (versionReplacement) Object.assign(live, { canvasVersion: replacementVersion, diskVersion: replacementVersion, externalChange: false, batch: null, replacedBy: undefined, replacementPending: true, syncRevision: 1 });
        opened.bridgeURL = "/office/bridge/office-session-b/new-cap/"; opened.bridgeToken = "synthetic-capability-bbbbbbbbbbbbbbbb";
      }
      claimInstance = instance; live.instanceBound = true;
      if (lostOpenResponses > 0) { lostOpenResponses--; throw new TypeError("open response lost after claim"); }
      if (nextFileRef) { live.fileRef = nextFileRef; nextFileRef = null; }
      if (openGate) await openGate.promise;
      return { ...opened, requestedPath: requestedOverride ?? String(body?.path || new URL(path, "https://pogo.test").searchParams.get("path") || live.path), session: copy(live) };
    }
    if (path.endsWith("/status")) { if (nextStatusError) { const error = nextStatusError; nextStatusError = null; throw error; } return { session: copy(statusOverride || live) }; }
    if (path.endsWith("/heartbeat")) { if (nextHeartbeatError) { const error = nextHeartbeatError; nextHeartbeatError = null; throw error; } return { ok: true, session: copy(nextHeartbeatSession || live) }; }
    if (path.endsWith("/mounted")) return { ok: true };
    if (path.endsWith("/release")) { if (releaseGate) await releaseGate.promise; if (nextReleaseError) { const error = nextReleaseError; nextReleaseError = null; throw error; } live.closed = true; lifetime.push("release-confirmed"); return { ok: true }; }
    if (path.endsWith("/ready")) { live.ready = true; live.instanceBound = true; return { ok: true, session: copy(readyOverride || live) }; }
    if (path.includes("/next?")) {
      if (nextPollError) { const error = nextPollError; nextPollError = null; throw error; }
      const inFlightId = new URL(path, "https://pogo.test").searchParams.get("inFlightId");
      return { command: inFlightId ? null : queue.shift() || null, ...(inFlightId && finishedFlights.has(inFlightId) ? { flight: finishedFlights.get(inFlightId) } : {}), ...(live.recovery ? { recovery: copy(live.recovery) } : {}), ...(new URL(path, "https://pogo.test").searchParams.get("syncRevision") !== String(live.syncRevision) ? { session: copy(live) } : {}), ...(live.activity && new URL(path, "https://pogo.test").searchParams.get("activityVersion") !== String(live.activity.version) ? { activity: copy(live.activity) } : {}) };
    }
    if (path.endsWith("/reconcile")) {
      for (const item of body?.operations as { operationId: string }[] || []) finishedFlights.set(item.operationId, { operationId: item.operationId, finished: true, readOnly: false, reconciled: true });
      live.blocked = requireRecoveryRead;
      live.recovery = { state: requireRecoveryRead ? "checking" : "ready", operationIds: [], revision: (live.recovery?.revision || 1) + 1, ...(requireRecoveryRead ? { needsRead: true, readTarget: { operation: "word.read", args: {} } } : {}) }; live.syncRevision!++;
      return { ok: true, session: copy(live) };
    }
    if (path.endsWith("/results")) {
      if (recoveryReadCommands.has(String(body?.operationId)) && body?.ok === true && live.recovery) { live.blocked = false; live.recovery = { ...live.recovery, state: "ready", needsRead: false }; live.syncRevision!++; }
      readReplies.get(String(body?.operationId))?.resolve({ ok: body?.ok, result: body?.result, error: body?.error });
      return resultGate ? resultGate.promise : { ok: true };
    }
    if (path.endsWith("/commands")) {
      queue.push({ operationId: String(body?.operationId), operation: String(body?.operation), args: body?.args as Record<string, unknown> });
      if (live.recovery?.needsRead) { recoveryReadCommands.add(String(body?.operationId)); const reply = deferred<unknown>(); readReplies.set(String(body?.operationId), reply); return reply.promise; }
      if (live.replacementPending) { const reply = deferred<unknown>(); readReplies.set(String(body?.operationId), reply); return reply.promise; }
      return uiCommandReceipt.promise;
    }
    if (path.endsWith("/batch-freeze")) {
      if (rejectFreeze) throw new Error("freeze acknowledgement unknown");
      if (body?.frozen && live.batch) live.batch.phase = "prepared";
      else live.batch = null;
      live.syncRevision!++;
      return { ok: true, session: copy(live) };
    }
    if (path.endsWith("/reload")) {
      live.batch = { id: "batch-reload", purpose: "reload", phase: "freeze_requested", ...(body?.discardChanges ? { discardChanges: true, confirmedDirtyEpoch: Number(body.confirmedDirtyEpoch), confirmedCanvasVersion: String(body.confirmedCanvasVersion) } : {}) };
      live.syncRevision!++; return reloadReply.promise;
    }
    if (path.endsWith("/reload-ready")) {
      if (rejectReloadReady) throw new Error("readback not confirmed");
      if (!unconfirmedReloadReady) { live.replacementPending = false; live.reloadConfirmed = true; }
      live.syncRevision!++; return { ok: true, session: copy(live) };
    }
    if (path.startsWith("/api/office/batches/") && path.endsWith("/abort")) {
      const id = live.batch?.id;
      if (!retainAbortedState) { live.batch = null; live.syncRevision!++; }
      return { ok: true, state: "aborted", batch_id: id };
    }
    if (path.endsWith("/state")) { if (nextStateError) { const error = nextStateError; nextStateError = null; throw error; } live.dirty = true; live.dirtyEpoch += 1; live.saveState = "dirty"; return { ok: true }; }
    if (path.endsWith("/save")) { if (nextSaveError) { const error = nextSaveError; nextSaveError = null; throw error; } live.saveState = "saving"; queue.push({ operationId: String(body?.operationId), operation: "host.export", args: { fileType: "docx" } }); return saveReceipt.promise; }
    if (path.endsWith("/export")) return exportReceipt.promise;
    if (path.endsWith("/close")) { if (closeGate) await closeGate.promise; live.closed = true; lifetime.push("close-confirmed"); return { ok: true }; }
    throw new Error(`Unexpected test request ${method} ${path}`);
  });
  function message(update: Partial<OfficeBridgeMessage> = {}, origin = "https://pogo.test", peer: unknown = source) {
    const data = { protocol: "pogo-office-lab/v1", type: "ready", sessionId: live.id, documentId: live.documentId, token: opened.bridgeToken, capabilities: { read: true, nativeUndo: true, nativeRedo: true }, ...update };
    for (const callback of [...listeners.get("message") || []]) callback({ origin, source: peer, data });
  }
  return {
    live, opened, calls, queue, sent, readyAcks, downloads, beacons, lifetime, input, inputFrame, source, message,
    inputEvent: (name: string, event: unknown = {}) => { for (const callback of [...inputListeners.get(name) || []]) callback(event); },
    events: () => events, constructors: () => constructors,
    remoteBound: () => { live.ready = true; live.instanceBound = true; claimInstance = "another-page"; },
    setStatus: (value: OfficeSession | null) => { statusOverride = value; },
    setReady: (value: OfficeSession | null) => { readyOverride = value; },
    delayClose: () => { closeGate = deferred<boolean>(); },
    confirmClose: () => { closeGate?.resolve(true); },
    delayOpen: () => { openGate = deferred<boolean>(); },
    confirmOpen: () => { openGate?.resolve(true); },
    delayResult: () => { resultGate = deferred<{ ok: boolean; ignored?: boolean }>(); },
    confirmResult: (receipt = { ok: true } as { ok: boolean; ignored?: boolean }) => { const gate = resultGate; resultGate = null; gate?.resolve(receipt); },
    requireFreshRecoveryRead: () => { requireRecoveryRead = true; },
    failPoll: (error: Error) => { nextPollError = error; },
    finishFlight: (operationId: string, readOnly: boolean, reconciled = false, known = false, succeeded = known) => { finishedFlights.set(operationId, { operationId, finished: true, readOnly, reconciled, known, succeeded }); },
    failNextState: (error: Error) => { nextStateError = error; },
    failNextSave: (error: Error) => { nextSaveError = error; },
    failConfig: (error: Error) => { nextConfigError = error; },
    failStatus: (error: Error) => { nextStatusError = error; },
    failHeartbeat: (error: Error) => { nextHeartbeatError = error; },
    heartbeatSession: (value: OfficeSession | null) => { nextHeartbeatSession = value; },
    failRelease: (error: Error) => { nextReleaseError = error; },
    loseOpenResponses: (count: number) => { lostOpenResponses = count; },
    delayRelease: () => { releaseGate = deferred<boolean>(); },
    confirmRelease: () => { releaseGate?.resolve(true); },
    failEngine: () => { nextEngineError = new Error("script unavailable"); },
    setRequested: (path: string) => { requestedOverride = path; },
    changeNextFileRef: (fileRef: string) => { nextFileRef = fileRef; },
    noCanvasReady: () => { autoReady = false; },
    requestBatch: (purpose: "batch" | "reload" = "batch", extra: Partial<NonNullable<OfficeSession["batch"]>> = {}) => { live.batch = { id: "batch-a", purpose, phase: "freeze_requested", ...extra }; live.syncRevision!++; },
    cancelStateLater: () => { retainAbortedState = true; },
    clearBatch: () => { live.batch = null; live.syncRevision!++; },
    refuseFreeze: () => { rejectFreeze = true; },
    refuseReloadReady: () => { rejectReloadReady = true; },
    unconfirmedReloadReady: () => { unconfirmedReloadReady = true; },
    confirmReloadHTTP: () => { reloadReply.resolve({ ok: true, session: copy(live) }); },
    externalVersion: (version = versionB) => { live.diskVersion = version; live.externalChange = true; live.reloadAvailable = true; live.syncRevision!++; },
    replaceVersion: (version = versionB) => { versionReplacement = true; replacementVersion = version; live.closed = true; live.replacedBy = "office-session-b"; live.diskVersion = version; live.batch = null; live.syncRevision!++; },
    restart: () => { live.closed = true; nextStatusError = Object.assign(new Error("old session absent"), { code: "DOCUMENT_NOT_FOUND", status: 404 }); },
    confirmUICommand: () => { uiCommandReceipt.resolve({ ok: true }); },
    vetoUnload: () => { listeners.get("beforeunload")?.add((value: unknown) => { (value as { preventDefault(): void }).preventDefault(); }); },
    unload: () => { const event = { defaultPrevented: false, preventDefault: vi.fn(() => { event.defaultPrevented = true; }), returnValue: undefined as undefined | string }; for (const callback of [...listeners.get("beforeunload") || []]) callback(event); return event; },
    finishSaveHTTP: () => saveReceipt.resolve({ ok: true, session: { ...copy(live), dirty: false, saveState: "saved" } }),
    confirmExport: () => { live.dirty = false; live.persistedEpoch = live.dirtyEpoch; live.saveState = "saved"; live.revision += 1; live.savedAt = "2026-10-13T00:00:00Z"; exportReceipt.resolve({ ok: true, session: copy(live) }); },
    confirmExportAs: (value: OfficeSession) => exportReceipt.resolve({ ok: true, session: value }),
    rejectExport: () => exportReceipt.reject(new Error("export write rejected")),
    rejectExportAs: (error: Error) => { live.saveState = "error"; exportReceipt.reject(error); saveReceipt.reject(error); },
    newSave: () => { saveReceipt = deferred<unknown>(); exportReceipt = deferred<unknown>(); },
  };
}
async function flush() { for (let i = 0; i < 12; i++) await Promise.resolve(); await vi.advanceTimersByTimeAsync(0); for (let i = 0; i < 12; i++) await Promise.resolve(); }
async function setup(officeSessionId?: string, path = "test.docx") {
  const setup = (OfficeEditor as unknown as { setup(props: unknown, context: unknown): Controller }).setup;
  const emitted = vi.fn();
  const props = reactive({ path, conversationId: "conversation-a", officeSessionId, immersive: false });
  const controller = setup(props, { expose() {}, emit: emitted }) as StartupController;
  controller.root.value = { querySelector: () => host.input, querySelectorAll: () => [host.inputFrame] };
  const mounting = hooks.mounted[hooks.mounted.length - 1]!();
  await flush();
  await mounting;
  await flush();
  return { controller, props, emitted, unmount: hooks.unmounted[hooks.unmounted.length - 1]! };
}
async function bound() { const mounted = await setup(); host.message(); await flush(); expect(mounted.controller.ready.value).toBe(true); return mounted; }
async function pump() { await vi.advanceTimersByTimeAsync(150); await flush(); }
const count = (suffix: string) => host.calls.filter(call => call.path.endsWith(suffix)).length;
async function commandReceipt(operationId: string, operation: string, response: Partial<OfficeBridgeMessage>) {
  host.queue.push({ operationId, operation, args: {} }); await pump();
  host.message({ type: "response", operationId, ...response }); await flush();
}
beforeEach(async () => {
  vi.useFakeTimers(); hooks.mounted.length = 0; hooks.unmounted.length = 0; hooks.request.mockReset(); hooks.confirm.mockReset(); hooks.confirm.mockResolvedValue("confirm"); host = createHost();
  // A new host is a new browser window. Reset module-owned SDK/view/navigation
  // state together; reusing an old DocEditor constructor would target a prior
  // test's closed-over host. SDK sharing within each browser is still real.
  vi.resetModules();
  OfficeEditor = (await import("./OfficeEditor.vue")).default;
  ({ confirmStudioNavigation } = await import("@/agent/studioNavigation"));
  ({ registerOfficeView } = await import("@/agent/officeTransport"));
});
afterEach(async () => { for (const cleanup of [...hooks.unmounted].reverse()) cleanup(); await flush(); vi.useRealTimers(); vi.unstubAllGlobals(); });

describe("Office controller identity and lifetime", () => {
  it("opens a relative Office bridge through the current SSH forwarded origin", async () => {
    vi.stubGlobal("location", { origin: "http://localhost:8080", href: "http://localhost:8080/" });
    const { controller } = await setup();
    host.message({}, "http://localhost:8080"); await flush();
    expect(controller.ready.value).toBe(true); expect(host.constructors()).toBe(1);
    expect(count("/ready")).toBe(1); expect(count("/release")).toBe(0);
    expect(host.calls.filter(call => call.path.includes("/bridge/")).every(call => call.path.startsWith("/office/bridge/"))).toBe(true);
  });
  it("releases an unused claim when a legacy absolute bridge points to another port", async () => {
    vi.stubGlobal("location", { origin: "http://localhost:8080", href: "http://localhost:8080/" });
    host.opened.bridgeURL = "http://192.0.2.99:19770/office/bridge/office-session-a/test-cap/";
    const { controller } = await setup();
    expect(controller.ready.value).toBe(false); expect(host.constructors()).toBe(0);
    expect(count("/release")).toBe(1);
    expect(controller.failed.value).toContain("Office bridge origin is not authorized");
  });
  it("releases an unused claim when the editor is directed to a tool-container origin", async () => {
    host.opened.browserURL = "http://127.0.0.1:19778";
    const { controller } = await setup();
    expect(controller.ready.value).toBe(false); expect(host.constructors()).toBe(0);
    expect(count("/release")).toBe(1);
    expect(controller.failed.value).toContain("Pogo Web gateway");
  });
  it("accepts an authenticated canonical path without treating owner-prefixed spelling as another file", async () => {
    host.live.path = "agents/agent-a/uploads/test.docx";
    const { controller } = await setup(undefined, "users/user-a/agents/agent-a/uploads/test.docx");
    host.message(); await flush();
    expect(controller.ready.value).toBe(true); expect(host.constructors()).toBe(1);
    expect(count("/release")).toBe(0);
  });
  it("releases its own unused claim on mismatched requested identity before loading editor scripts", async () => {
    host.setRequested("users/another-account/test.docx");
    const { controller, emitted } = await setup();
    expect(controller.ready.value).toBe(false); expect(host.constructors()).toBe(0);
    expect(count("/release")).toBe(1); expect(emitted).toHaveBeenCalledWith("openFailed", "officeEditor.failed: officeEditor.unavailable");
  });
  it("retries a transient engine load without releasing its claim or creating a second editor", async () => {
    host.failEngine(); const mounting = setup(); await flush();
    await vi.advanceTimersByTimeAsync(500); const first = await mounting; host.message(); await flush();
    expect(first.controller.ready.value).toBe(true); expect(host.constructors()).toBe(1);
    expect(count("/release")).toBe(0); expect(first.emitted).not.toHaveBeenCalledWith("openFailed", expect.anything());
  });
  it("keeps an uncertain bootstrap release mounted without retrying or opening a second editor", async () => {
    host.opened.browserURL = "http://127.0.0.1:19778"; host.failRelease(new Error("release response lost"));
    const { controller, emitted } = await setup();
    expect(controller.failed.value).toBe("officeEditor.releaseUnknown");
    expect(emitted).not.toHaveBeenCalledWith("openFailed", expect.anything());
    await vi.advanceTimersByTimeAsync(30000);
    expect(host.constructors()).toBe(0); expect(count("/release")).toBe(1);
    expect(host.calls.filter(call => call.path === "/api/office/sessions")).toHaveLength(1);
  });
  it("retains the unopened canvas until its bootstrap release is confirmed", async () => {
    host.noCanvasReady(); const { emitted } = await setup();
    host.events().onError({ data: { errorDescription: "canvas failed" } }); await flush();
    expect(host.lifetime.slice(0, 2)).toEqual(["release-confirmed", "destroy"]);
    expect(emitted).toHaveBeenCalledWith("openFailed", "officeEditor.failed: canvas failed");
  });
  it("does not release a native mounted canvas even when plugin readiness fails", async () => {
    const { controller, emitted } = await setup();
    host.events().onError({ data: { errorDescription: "plugin unavailable" } }); await flush();
    expect(count("/mounted")).toBe(1); expect(count("/release")).toBe(0);
    expect(host.lifetime).toEqual([]); expect(controller.loading.value).toBe(false);
    expect(emitted).not.toHaveBeenCalledWith("openFailed", expect.anything());
  });
  it("opens a vanished old session by verified file identity once after restart", async () => {
    host.failConfig(Object.assign(new Error("not found"), { status: 404, code: "DOCUMENT_NOT_FOUND" }));
    const { controller, emitted } = await setup("vanished-id"); host.message(); await flush();
    expect(controller.ready.value).toBe(true);
    expect(host.calls.filter(call => call.path.includes("/config?"))).toHaveLength(1);
    expect(host.calls.filter(call => call.path === "/api/office/sessions")).toHaveLength(1);
    expect(emitted).toHaveBeenCalledWith("closedPrevious", "vanished-id");
  });
  it("never interprets another 404 as authority to create a replacement editor", async () => {
    host.failConfig(Object.assign(new Error("account unavailable"), { status: 404, code: "UNAUTHORIZED" }));
    const { controller } = await setup("old-id");
    expect(controller.ready.value).toBe(false); expect(host.constructors()).toBe(0);
    expect(host.calls.filter(call => call.path === "/api/office/sessions")).toHaveLength(0);
  });
  it("recreates a confirmed clean canvas after the Office service loses its session", async () => {
    const { controller, emitted } = await bound(); host.restart();
    await controller.refreshStatus(); await flush(); host.message(); await flush();
    expect(host.constructors()).toBe(2); expect(host.lifetime).toEqual(["destroy"]);
    expect(controller.ready.value).toBe(true); expect(controller.dirty.value).toBe(false);
    expect(emitted).toHaveBeenCalledWith("closedPrevious", "office-session-a");
  });
  it("stops recovery and releases the new unused claim when the same path resolves to a different file identity", async () => {
    const { controller, emitted } = await bound(); host.restart(); host.changeNextFileRef("other-authorized-file");
    await controller.refreshStatus(); await flush();
    expect(host.constructors()).toBe(1); expect(host.lifetime).toEqual(["destroy", "release-confirmed"]);
    expect(controller.ready.value).toBe(false); expect(count("/release")).toBe(1);
    expect(emitted).toHaveBeenCalledWith("openFailed", "officeEditor.failed: officeEditor.identityChanged");
  });
  it("preserves manual input on restart and stops reconnect polling without destroying the canvas", async () => {
    const { controller } = await bound(); host.events().onDocumentStateChange({ data: true }); await flush();
    host.restart(); await controller.refreshStatus(); await flush();
    expect(controller.dirty.value).toBe(true); expect(controller.failed.value).toBe("officeEditor.connectionPreserved");
    const requests = host.calls.length; host.events().onDocumentStateChange({ data: true }); await flush(); await vi.advanceTimersByTimeAsync(12000);
    expect(host.constructors()).toBe(1); expect(host.lifetime).toEqual([]); expect(host.calls).toHaveLength(requests);
    expect(count("/release")).toBe(0); expect(count("/close")).toBe(0); expect(host.input.inert).toBe(false);
  });
  it("preserves an in-flight agent mutation on restart even before a dirty acknowledgement", async () => {
    const { controller } = await bound(); host.queue.push({ operationId: "edit-in-progress", operation: "word.replace", args: {} }); await pump();
    host.restart(); await controller.refreshStatus(); await flush();
    expect(controller.failed.value).toBe("officeEditor.connectionPreserved"); expect(host.constructors()).toBe(1); expect(host.lifetime).toEqual([]);
  });
  it("cannot treat a changed process epoch from another document as permission to replace this canvas", async () => {
    const { controller } = await bound();
    host.setStatus({ ...copy(host.live), documentId: "other-document", bootEpoch: "boot-foreign" });
    await controller.refreshStatus(); await flush();
    expect(controller.ready.value).toBe(true); expect(host.constructors()).toBe(1); expect(host.lifetime).toEqual([]);
  });
  it("opens a legacy owner path as saved preview when another window owns the editor", async () => {
    host.live.path = "agents/agent-a/uploads/orphan.docx"; host.remoteBound();
    const { controller, emitted } = await setup(undefined, "users/user-a/agents/agent-a/uploads/orphan.docx");
    expect(controller.canRecover.value).toBe(false); expect(controller.recoverySession.value).toBeNull();
    expect(emitted).toHaveBeenCalledWith("viewSaved");
    expect(count("/close")).toBe(0); expect(count("/release")).toBe(0); expect(host.constructors()).toBe(0);
  });
  it("keeps another locally mounted writer when an unknown alias falls back to saved preview", async () => {
    host.live.path = "agents/agent-a/uploads/alias-race-fresh.docx"; host.remoteBound();
    const release = registerOfficeView(copy(host.live));
    try {
      const { controller, emitted } = await setup(undefined, "users/user-a/agents/agent-a/uploads/alias-race-fresh.docx");
      expect(emitted).toHaveBeenCalledWith("viewSaved");
      expect(controller.recoverySession.value).toBeNull(); expect(controller.canRecover.value).toBe(false);
      expect(host.constructors()).toBe(0); expect(count("/close")).toBe(0); expect(count("/release")).toBe(0);
    } finally { release(); }
  });
  it("retains a fast confirmed edit from host receipts without another polling path or iframe recreation", async () => {
    const { controller } = await bound();
    const item = { sequence: 1, operationId: "visible-edit", operation: "word.replace", actor: "agent" as const, phase: "writing" as const,
      status: "running" as const, target: { kind: "word" as const }, queuedAt: "2026-10-13T00:00:00Z" };
    host.live.activity = { version: 1, items: [item] };
    host.queue.push({ operationId: item.operationId, operation: item.operation, args: {} }); await pump();
    expect(controller.activityItems.value[0]?.status).toBe("running");
    host.live.activity = { version: 2, items: [{ ...item, status: "succeeded", verified: true, changeCount: 1, changes: [{ before: "待评审", after: "评审中" }] }] };
    host.message({ type: "response", operationId: item.operationId, ok: true, result: {} }); await flush();
    expect(controller.activityItems.value[0]).toMatchObject({ status: "succeeded", changeCount: 1, changes: [{ before: "待评审", after: "评审中" }] });
    expect(host.constructors()).toBe(1); expect(host.downloads).toEqual([]);
    const nextCalls = host.calls.filter(call => call.path.includes("/next?"));
    expect(nextCalls.every(call => call.path.includes("activityVersion="))).toBe(true);
    expect(host.calls.some(call => call.path.includes("/activity"))).toBe(false);
  });
  it("cannot replace an uncertain host result with a stale successful status", async () => {
    const { controller } = await bound();
    const item = { sequence: 1, operationId: "uncertain-edit", operation: "word.replace", actor: "agent" as const, phase: "writing" as const,
      status: "unknown" as const, target: { kind: "word" as const }, queuedAt: "2026-10-13T00:00:00Z" };
    host.live.activity = { version: 3, items: [item] }; await controller.refreshStatus();
    host.setStatus({ ...copy(host.live), activity: { version: 2, items: [{ ...item, status: "succeeded", verified: true, changeCount: 1 }] } });
    await controller.refreshStatus();
    expect(controller.activity.value.version).toBe(3);
    expect(controller.activityItems.value[0]?.status).toBe("unknown");
    expect(controller.activityItems.value[0]?.changeCount).toBeUndefined();
  });
  it("preserves the dirty shared editor when immersive layout changes", async () => {
    const { controller, props } = await bound();
    host.events().onDocumentStateChange({ data: true }); await flush();
    props.immersive = true; await flush();
    props.immersive = false; await flush();
    expect(controller.ready.value).toBe(true); expect(controller.dirty.value).toBe(true);
    expect(controller.stateLabel.value).toBe("officeEditor.unsavedShort");
    expect(host.constructors()).toBe(1); expect(host.lifetime).toEqual([]);
    expect(host.calls.filter(call => call.path === "/api/office/sessions")).toHaveLength(1);
    expect(count("/close")).toBe(0); expect(count("/save")).toBe(0);
  });
  it("keeps editor failures visible in the compact toolbar state", async () => {
    const { controller } = await bound();
    host.events().onError({ data: { errorDescription: "The editor connection failed." } }); await flush();
    expect(controller.stateLabel.value).toBe("officeEditor.failedShort");
    expect(controller.failed.value).toContain("The editor connection failed.");
  });
  it("does not mark the editor dirty when worksheet discovery succeeds", async () => {
    const { controller } = await bound();
    await commandReceipt("worksheet-discovery", "sheet.list", { ok: true, result: { sheets: [{ name: "明细" }, { name: "汇总" }] } });
    expect(controller.dirty.value).toBe(false);
    expect(count("/state")).toBe(0); expect(count("/save")).toBe(0);
    expect(count("/results")).toBe(1);
  });
  it("refuses origin, session, document, token and protocol mismatches before ready", async () => {
    const { controller } = await setup();
    host.message({}, "https://other.test");
    for (const update of [{ sessionId: "other" }, { documentId: "other" }, { token: "other" }, { protocol: "other" }]) host.message(update as Partial<OfficeBridgeMessage>);
    await flush(); expect(count("/ready")).toBe(0); expect(controller.ready.value).toBe(false);
    host.message(); await flush(); expect(controller.ready.value).toBe(true); expect(count("/ready")).toBe(1);
  });
  it("does not re-claim on duplicate ready or another message source", async () => {
    await bound(); const instance = host.calls.find(call => call.path.endsWith("/ready"))!.body!.instanceId;
    host.message(); host.message({}, "https://pogo.test", { postMessage() {} }); await flush();
    expect(count("/ready")).toBe(1); expect(host.calls.filter(call => call.path === "/api/office/sessions")).toHaveLength(1);
    await pump(); expect(host.calls.find(call => call.path.includes("/next?"))!.path).toContain(`instanceId=${instance}`);
  });
  it("rejects a mismatched source and stale operation receipt", async () => {
    await bound(); host.queue.push({ operationId: "read-operation-1", operation: "word.read", args: {} }); await pump();
    expect(host.sent).toHaveLength(1);
    host.message({ type: "response", operationId: "read-operation-1", ok: true }, "https://pogo.test", { postMessage() {} });
    host.message({ type: "response", operationId: "old-operation", ok: true }); await flush(); expect(count("/results")).toBe(0);
    host.message({ type: "response", operationId: "read-operation-1", ok: true, result: { text: "live" } }); await flush(); expect(count("/results")).toBe(1);
  });
  it("ignores a late receipt after teardown and never claims a replacement", async () => {
    const mounted = await bound(); host.queue.push({ operationId: "read-operation-1", operation: "word.read", args: {} }); await pump();
    mounted.unmount(); host.message({ type: "response", operationId: "read-operation-1", ok: true }); await flush();
    expect(count("/results")).toBe(0); expect(count("/ready")).toBe(1); expect(host.constructors()).toBe(1);
  });
  it("keeps the original canvas when another view requests the same document", async () => {
    await bound(); const second = await setup();
    expect(second.controller.failed.value).toContain("officeEditor.failed"); expect(host.constructors()).toBe(1);
    expect(host.calls.filter(call => call.path === "/api/office/sessions")).toHaveLength(1); expect(count("/ready")).toBe(1);
  });
  it("accepts its atomically claimed bootstrap and uses that nonce through ready", async () => {
    const { controller } = await setup("office-session-a");
    expect(host.live.instanceBound).toBe(true); expect(host.constructors()).toBe(1);
    const configRequest = host.calls.find(call => call.path.includes("/config?"))!;
    const instance = new URL(configRequest.path, "https://pogo.test").searchParams.get("instanceId");
    host.message(); await flush();
    expect(controller.ready.value).toBe(true); expect(host.calls.find(call => call.path.endsWith("/ready"))!.body!.instanceId).toBe(instance);
    expect(host.calls.filter(call => call.path.includes("/config?"))).toHaveLength(1);
  });
  it("releases a clean late bootstrap without creating a canvas after teardown", async () => {
    host.delayOpen(); const mounting = setup(); await flush();
    hooks.unmounted[hooks.unmounted.length - 1]!(); host.confirmOpen(); await mounting; await flush();
    expect(host.constructors()).toBe(0); expect(host.beacons).toHaveLength(0);
    expect(count("/release")).toBe(1);
    expect(host.calls.find(call => call.path.endsWith("/release"))?.body).toMatchObject({ instanceId: host.calls[0]!.body!.instanceId, bootstrapToken: "unused-bootstrap-a", fileRef: "file-identity-a", bootEpoch: "boot-a" });
    expect(count("/ready")).toBe(0);
  });
  it("refuses a ready acknowledgement for a different document generation", async () => {
    const { controller } = await setup(); host.setReady({ ...copy(host.live), generation: 99, ready: true });
    host.message(); await flush(); expect(controller.ready.value).toBe(false); expect(controller.failed.value).toBe("officeEditor.unverified");
    await pump(); expect(host.calls.filter(call => call.path.includes("/next?"))).toHaveLength(0);
  });
  it("does not tear down the current canvas on a mismatched closed status", async () => {
    const { controller } = await bound(); host.setStatus({ ...copy(host.live), id: "old-session", closed: true });
    await vi.advanceTimersByTimeAsync(3000);
    expect(controller.ready.value).toBe(true); expect(host.lifetime).toEqual([]);
  });
});

describe("Office cleanup and explicit recovery", () => {
  it("awaits confirmed clean close before destroying the editor on soft navigation", async () => {
    await bound(); host.delayClose();
    const completed = vi.fn(); const leaving = confirmStudioNavigation().then(value => { completed(value); return value; });
    await flush(); expect(count("/close")).toBe(1); expect(host.lifetime).toEqual([]); expect(completed).not.toHaveBeenCalled();
    host.confirmClose(); await flush(); expect(await leaving).toBe(true);
    expect(host.lifetime).toEqual(["close-confirmed", "destroy"]); expect(host.beacons).toHaveLength(0);
  });
  it("uses an instance-scoped unload beacon for clean reload rather than claiming again", async () => {
    await bound(); const event = host.unload();
    await flush();
    expect(event.preventDefault).not.toHaveBeenCalled(); expect(host.beacons).toHaveLength(1);
    expect(host.beacons[0]!.url).toBe("/office/bridge/office-session-a/test-cap/detach");
    expect(JSON.parse(await host.beacons[0]!.payload.text())).toEqual({ instanceId: host.calls.find(call => call.path.endsWith("/ready"))!.body!.instanceId });
    expect(count("/close")).toBe(0); expect(host.constructors()).toBe(1);
  });
  it("does not detach a clean document when another editor vetoes the same reload", async () => {
    await bound(); host.vetoUnload(); const event = host.unload(); await flush();
    expect(event.defaultPrevented).toBe(true); expect(host.beacons).toHaveLength(0); expect(count("/close")).toBe(0);
  });
  it("records pre-handshake human input and prevents dirty detach", async () => {
    const { controller, unmount } = await setup();
    host.events().onDocumentStateChange({ data: true }); await flush();
    expect(count("/ready")).toBe(0); expect(count("/state")).toBe(1); expect(controller.dirty.value).toBe(true);
    expect(host.calls.find(call => call.path.endsWith("/state"))!.body!.instanceId).toBe(host.calls.find(call => call.path === "/api/office/sessions")!.body!.instanceId);
    const event = host.unload(); expect(event.preventDefault).toHaveBeenCalledTimes(1);
    expect(await controller.prepareClose()).toBe(false); unmount(); await flush();
    expect(host.beacons).toHaveLength(0); expect(count("/close")).toBe(0); expect(count("/save")).toBe(0);
  });
  it("preserves a server-side pending operation when no local result has arrived", async () => {
    const { controller } = await bound(); host.live.pending = true;
    await vi.advanceTimersByTimeAsync(3000);
    const event = host.unload(); expect(event.preventDefault).toHaveBeenCalledTimes(1);
    expect(await controller.prepareClose()).toBe(false); expect(host.beacons).toHaveLength(0); expect(count("/close")).toBe(0);
  });
  it("never uses the legacy orphan recovery button to close another saved writer", async () => {
    host.remoteBound(); const { controller, emitted } = await setup();
    expect(controller.canRecover.value).toBe(false); expect(host.constructors()).toBe(0); expect(count("/close")).toBe(0);
    expect(await controller.recoverCleanSession()).toBe(false); expect(host.live.id).toBe("office-session-a");
    expect(emitted).toHaveBeenCalledWith("viewSaved"); expect(emitted).not.toHaveBeenCalledWith("closedPrevious", expect.anything());
    expect(count("/close")).toBe(0); expect(host.constructors()).toBe(0);
  });
  it.each(["dirty", "pending", "blocked"] as const)("never closes another writer with %s state when showing a saved preview", async (field) => {
    host.remoteBound(); const { controller } = await setup();
    expect(controller.canRecover.value).toBe(false); host.live[field] = true;
    expect(await controller.recoverCleanSession()).toBe(false); expect(controller.canRecover.value).toBe(false);
    expect(count("/close")).toBe(0); expect(host.constructors()).toBe(0);
  });
  it("rejects stale document identity instead of using a mismatched recovery authority", async () => {
    host.remoteBound(); const { controller } = await setup();
    host.setStatus({ ...copy(host.live), id: "other-session", documentId: "other-document" });
    expect(await controller.recoverCleanSession()).toBe(false); expect(controller.recoverySession.value).toBeNull();
    expect(count("/close")).toBe(0); expect(host.constructors()).toBe(0);
  });
});

describe("Office dirty guards and canonical export", () => {
  it("cannot confirm saving from an export receipt for another file or a rejected fresh status", async () => {
    const { controller, emitted } = await bound(); host.events().onDocumentStateChange({ data: true }); await flush();
    const saving = controller.save(); await pump(); host.finishSaveHTTP(); await flush();
    host.events().onDownloadAs({ data: { fileType: "docx", url: "https://engine.test/current.docx" } }); await flush();
    host.confirmExportAs({ ...copy(host.live), dirty: false, saveState: "saved", fileRef: "another-file" }); await flush();
    expect(await saving).toBe(false); expect(controller.dirty.value).toBe(true);
    expect(emitted).not.toHaveBeenCalledWith("saved"); expect(host.input.inert).toBe(false);
    expect(await controller.prepareClose()).toBe(false);
  });
  it("queues one shared save after an in-flight agent edit without locking manual input while waiting", async () => {
    const { controller } = await bound();
    host.events().onDocumentStateChange({ data: true }); await flush();
    host.queue.push({ operationId: "earlier-agent-edit", operation: "word.replace", args: {} }); await pump();
    const pendingSave = controller.save(); expect(controller.save()).toBe(pendingSave);
    await flush();
    expect(count("/save")).toBe(1); expect(host.downloads).toEqual([]); expect(host.input.inert).toBe(false);
    expect(controller.stateLabel.value).toBe("officeEditor.queuedShort");
    host.message({ type: "response", operationId: "earlier-agent-edit", ok: true, result: {} }); await flush(); await pump();
    expect(host.downloads).toEqual(["docx"]); expect(host.input.inert).toBe(true);
    host.finishSaveHTTP(); host.events().onDownloadAs({ data: { fileType: "docx", url: "https://engine.test/saved.docx" } }); await flush();
    host.confirmExport(); await flush(); expect(await pendingSave).toBe(true);
    expect(host.input.inert).toBe(false); expect(host.constructors()).toBe(1);
  });
  it("blocks close and unmount cleanup from silently saving or closing a dirty canvas", async () => {
    const mounted = await bound(); host.events().onDocumentStateChange({ data: true }); await flush();
    expect(await mounted.controller.prepareClose()).toBe(false); expect(count("/close")).toBe(0); expect(count("/save")).toBe(0);
    mounted.unmount(); await flush(); expect(count("/close")).toBe(0); expect(count("/save")).toBe(0);
  });
  it("blocks close while a command result is still in flight", async () => {
    const { controller } = await bound(); host.queue.push({ operationId: "read-operation-1", operation: "word.read", args: {} }); await pump();
    expect(await controller.prepareClose()).toBe(false); expect(count("/close")).toBe(0);
  });
  it("clears a transient dirty-close notice only after an external Agent save is confirmed", async () => {
    const { controller } = await bound(); host.events().onDocumentStateChange({ data: true }); await flush();
    expect(await controller.prepareClose()).toBe(false); expect(controller.failed.value).toBe("officeEditor.saveToClose");
    host.queue.push({ operationId: "agent-save-operation", operation: "host.export", args: { fileType: "docx" } }); await pump();
    expect(controller.saving.value).toBe(true); expect(host.input.inert).toBe(true);
    expect(controller.failed.value).toBe("officeEditor.saveToClose");
    host.events().onDownloadAs({ data: { fileType: "docx", url: "https://engine.test/current.docx" } }); await flush();
    expect(controller.failed.value).toBe("officeEditor.saveToClose"); host.confirmExport(); await flush();
    expect(controller.dirty.value).toBe(false); expect(controller.failed.value).toBe(""); expect(count("/save")).toBe(0);
    expect(controller.saving.value).toBe(false); expect(host.input.inert).toBe(false);
    expect(await controller.prepareClose()).toBe(true);
  });
  it("waits for export persistence when the save HTTP response returns first", async () => {
    const { controller } = await bound(); host.events().onDocumentStateChange({ data: true }); await flush();
    const complete = vi.fn(); const requested = controller.save(); const saving = requested.then(value => { complete(value); return value; });
    await flush();
    expect(controller.saving.value).toBe(true); expect(controller.stateLabel.value).toBe("officeEditor.queuedShort");
    expect(controller.save()).toBe(requested); expect(count("/save")).toBe(1);
    await pump(); expect(host.downloads).toEqual(["docx"]); expect(host.input.inert).toBe(true);
    host.finishSaveHTTP(); await flush(); expect(complete).not.toHaveBeenCalled();
    host.events().onDownloadAs({ data: { fileType: "docx", url: "https://engine.test/current.docx" } }); await flush();
    expect(host.input.inert).toBe(true); host.confirmExport(); await flush();
    expect(await saving).toBe(true); expect(controller.dirty.value).toBe(false); expect(host.input.inert).toBe(false); expect(count("/results")).toBe(0);
    expect(controller.saving.value).toBe(false); expect(controller.stateLabel.value).toBe("officeEditor.savedShort");
    expect(controller.ready.value).toBe(true); expect(host.constructors()).toBe(1); expect(host.lifetime).toEqual([]);
    expect(host.calls.find(call => call.path.endsWith("/export"))!.body!.operationId).toBe(host.calls.find(call => call.path.endsWith("/save"))!.body!.operationId);
  });
  it("restores the editor stage on export failure without issuing another export", async () => {
    const { controller } = await bound(); host.events().onDocumentStateChange({ data: true }); await flush();
    const saving = controller.save(); await pump(); host.finishSaveHTTP(); await flush();
    host.events().onDownloadAs({ data: { fileType: "docx", url: "https://engine.test/current.docx" } }); await flush(); host.rejectExport(); await flush();
    expect(await saving).toBe(false); expect(controller.saving.value).toBe(false); expect(host.input.inert).toBe(false); expect(controller.dirty.value).toBe(true);
    await vi.advanceTimersByTimeAsync(40000); expect(host.downloads).toEqual(["docx"]); expect(count("/export")).toBe(1);
  });
  it("unlocks after an unknown export timeout and ignores its late callback", async () => {
    const { controller } = await bound(); host.events().onDocumentStateChange({ data: true }); await flush();
    const saving = controller.save(); await pump(); host.finishSaveHTTP(); await flush();
    await vi.advanceTimersByTimeAsync(35000); expect(await saving).toBe(false); expect(host.input.inert).toBe(false);
    host.events().onDownloadAs({ data: { fileType: "docx", url: "https://engine.test/current.docx" } }); await flush(); expect(count("/export")).toBe(0); expect(host.downloads).toHaveLength(1);
  });
});

describe("Office determinate command notices", () => {
  it.each(["PRECONDITION_FAILED", "INVALID_ARGUMENT", "TARGET_NOT_UNIQUE"])("clears %s only after a later successful write is accepted by the host", async (code) => {
    const { controller } = await bound();
    await commandReceipt("rejected-write", "word.replace", { ok: false, error: { code, message: "Write rejected without a change." } });
    const rejected = controller.failed.value; expect(rejected).toContain("Write rejected without a change.");
    await commandReceipt("fresh-read", "word.read", { ok: true, result: { text: "current" } });
    expect(controller.failed.value).toBe(rejected);
    host.delayResult(); await commandReceipt("accepted-write", "word.replace", { ok: true, result: { before: {}, after: {} } });
    expect(controller.failed.value).toBe(rejected);
    host.confirmResult(); await flush(); expect(controller.failed.value).toBe("");
  });
  it("clears NO_HISTORY after a later accepted native history operation", async () => {
    const { controller } = await bound();
    await commandReceipt("no-native-undo", "history.undo", { ok: false, error: { code: "NO_HISTORY", message: "There is nothing to undo." } });
    expect(controller.failed.value).toBe("officeEditor.noUndo");
    await commandReceipt("native-redo", "history.redo", { ok: true, result: { native: true, snapshot: null } });
    expect(controller.failed.value).toBe("");
  });
  it("keeps the notice when a toolbar history request starts and until its bridge receipt is accepted", async () => {
    const { controller } = await bound();
    await commandReceipt("rejected-write", "word.replace", { ok: false, error: { code: "PRECONDITION_FAILED", message: "Expected text differs." } });
    const rejected = controller.failed.value;
    const running = controller.run("history.undo"); await flush(); expect(controller.failed.value).toBe(rejected);
    host.delayResult(); await pump();
    const operationId = String(host.sent[host.sent.length - 1]!.operationId);
    host.message({ type: "response", operationId, ok: true, result: { native: true, snapshot: null } }); await flush();
    host.confirmUICommand(); await running; expect(controller.failed.value).toBe(rejected);
    host.confirmResult(); await flush(); expect(controller.failed.value).toBe("");
  });
  it.each(["OUTCOME_UNKNOWN", "EDITOR_OUTCOME_UNKNOWN"])("retains %s even if a later write appears successful", async (code) => {
    const { controller } = await bound();
    await commandReceipt("unknown-write", "word.replace", { ok: false, error: { code, message: "The outcome is not known." } });
    const unknown = controller.failed.value;
    await commandReceipt("later-write", "word.replace", { ok: true, result: { before: {}, after: {} } });
    expect(controller.failed.value).toBe(unknown); expect(unknown).toContain("The outcome is not known.");
  });
  it("does not erase a newer engine error that replaced a rejected-write notice", async () => {
    const { controller } = await bound();
    await commandReceipt("rejected-write", "word.replace", { ok: false, error: { code: "PRECONDITION_FAILED", message: "Expected text differs." } });
    host.events().onError({ data: { errorDescription: "The editor connection failed." } });
    const connection = controller.failed.value;
    await commandReceipt("accepted-write", "word.replace", { ok: true, result: { before: {}, after: {} } });
    expect(controller.failed.value).toBe(connection); expect(connection).toContain("The editor connection failed.");
  });
  it("retains a newer network error even when its display text equals the previous command notice", async () => {
    const { controller } = await bound();
    await commandReceipt("rejected-write", "word.replace", { ok: false, error: { code: "PRECONDITION_FAILED", message: "Current text differs." } });
    const rejected = controller.failed.value;
    host.failNextState(new Error("Current text differs.")); host.events().onDocumentStateChange({ data: true }); await flush();
    expect(controller.failed.value).toBe(rejected);
    await commandReceipt("accepted-write", "word.replace", { ok: true, result: { before: {}, after: {} } });
    expect(controller.failed.value).toBe(rejected);
  });
  it("does not clear a failed save when a later command succeeds", async () => {
    const { controller } = await bound();
    await commandReceipt("rejected-write", "word.replace", { ok: false, error: { code: "PRECONDITION_FAILED", message: "Expected text differs." } });
    host.failNextSave(new Error("Saving the exported file failed.")); expect(await controller.save()).toBe(false);
    const failedSave = controller.failed.value;
    await commandReceipt("accepted-write", "word.replace", { ok: true, result: { before: {}, after: {} } });
    expect(controller.failed.value).toBe(failedSave); expect(failedSave).toContain("Saving the exported file failed.");
  });
  it("does not clear a notice when the host ignores a late successful receipt", async () => {
    const { controller } = await bound();
    await commandReceipt("rejected-write", "word.replace", { ok: false, error: { code: "PRECONDITION_FAILED", message: "Expected text differs." } });
    const rejected = controller.failed.value;
    host.delayResult(); await commandReceipt("late-write", "word.replace", { ok: true, result: { before: {}, after: {} } });
    host.confirmResult({ ok: true, ignored: true }); await flush();
    expect(controller.failed.value).toBe(rejected);
  });
  it("retains the notice when the confirmed session is blocked", async () => {
    const { controller } = await bound();
    await commandReceipt("rejected-write", "word.replace", { ok: false, error: { code: "PRECONDITION_FAILED", message: "Expected text differs." } });
    const rejected = controller.failed.value;
    host.delayResult(); await commandReceipt("blocked-write", "word.replace", { ok: true, result: { before: {}, after: {} } });
    host.live.blocked = true; host.confirmResult(); await flush();
    expect(controller.failed.value).toBe(rejected); expect(controller.ready.value).toBe(true); expect(controller.canQueue.value).toBe(false);
  });
});

async function finishFrozenSave(controller: Controller) {
  await vi.advanceTimersByTimeAsync(1); await flush();
  expect(count("/save")).toBe(1);
  await pump(); expect(host.downloads).toEqual(["docx"]);
  host.events().onDownloadAs({ data: { url: "https://pogo.test/office/editor/cache/export.docx", fileType: "docx" } });
  host.confirmExport(); host.finishSaveHTTP(); await flush();
  await vi.advanceTimersByTimeAsync(1); await flush();
  expect(controller.syncFrozen.value).toBe(true);
}
async function readyReplacement(controller: Controller, version = versionB) {
  host.replaceVersion(version); await controller.refreshStatus(); await flush();
  expect(host.constructors()).toBe(2);
  host.message(); await flush(); await pump();
  const request = [...host.sent].reverse().find(value => value.operation === "word.read");
  expect(request).toBeDefined(); return String(request!.operationId);
}

describe("Office file version and batch synchronization", () => {
  it("freezes before saving human edits and retains the batch lock after export finishes", async () => {
    const { controller } = await bound();
    host.events().onDocumentStateChange({ data: true }); await flush();
    host.requestBatch(); await controller.refreshStatus();
    expect(controller.syncFrozen.value).toBe(true); expect(host.input.inert).toBe(true);
    expect(count("/batch-freeze")).toBe(0);
    await finishFrozenSave(controller);
    const ack = host.calls.find(call => call.path.endsWith("/batch-freeze"));
    expect(ack?.body).toMatchObject({ batchId: "batch-a", frozen: true, dirtyEpoch: 1, persistedEpoch: 1, canvasVersion: versionA });
    expect(controller.syncState.value).toBe("batch"); expect(host.input.inert).toBe(true);
    await controller.refreshStatus(); await controller.refreshStatus();
    expect(count("/save")).toBe(1); expect(count("/batch-freeze")).toBe(1);
    expect(await controller.prepareClose()).toBe(false); expect(host.unload().defaultPrevented).toBe(true);
  });

  it("only unlocks cancellation after a newer authoritative no-batch status", async () => {
    const { controller } = await bound();
    host.live.batch = { id: "batch-a", phase: "prepared", purpose: "batch" }; host.live.syncRevision!++;
    await controller.refreshStatus(); host.cancelStateLater();
    await controller.cancelBatch();
    expect(controller.syncFrozen.value).toBe(true);
    host.setStatus({ ...copy(host.live), batch: null, syncRevision: 1 }); await controller.refreshStatus();
    expect(controller.syncFrozen.value).toBe(true);
    host.setStatus(null); host.clearBatch(); await controller.refreshStatus();
    expect(controller.syncFrozen.value).toBe(false); expect(host.input.inert).toBe(false);
    expect(host.lifetime).toEqual([]);
  });

  it("does not repeat a failed freeze or unlock when its negative acknowledgement is unknown", async () => {
    const { controller } = await bound();
    host.failNextSave(new Error("save not confirmed")); host.refuseFreeze(); host.requestBatch();
    await controller.refreshStatus(); await vi.advanceTimersByTimeAsync(1); await flush();
    expect(controller.syncFrozen.value).toBe(true); expect(controller.syncState.value).toBe("error");
    await controller.refreshStatus(); await pump();
    expect(count("/save")).toBe(1); expect(count("/batch-freeze")).toBe(1); expect(host.lifetime).toEqual([]);
  });

  it("rejects reload freezing of dirty content without exporting over the external version", async () => {
    const { controller } = await bound(); host.events().onDocumentStateChange({ data: true }); await flush();
    host.externalVersion(); host.requestBatch("reload"); await controller.refreshStatus();
    await vi.advanceTimersByTimeAsync(1); await flush();
    expect(count("/save")).toBe(0); expect(host.downloads).toEqual([]);
    expect(host.calls.find(call => call.path.endsWith("/batch-freeze"))?.body).toMatchObject({ frozen: false });
    expect(controller.dirty.value).toBe(true); expect(host.lifetime).toEqual([]);
  });

  it("automatically requests a clean external reload but never marks it synchronized on request or ready alone", async () => {
    const { controller, emitted } = await bound();
    host.externalVersion(); await controller.refreshStatus();
    expect(count("/reload")).toBe(1); expect(controller.syncState.value).not.toBe("synced");
    await controller.refreshStatus(); await vi.advanceTimersByTimeAsync(1); await flush();
    expect(count("/save")).toBe(0); expect(controller.syncFrozen.value).toBe(true);
    const id = await readyReplacement(controller);
    expect(controller.retainedCanvas.value).not.toBeNull(); expect(host.lifetime).toEqual([]);
    expect(controller.syncState.value).toBe("syncing");
    host.message({ type: "response", operationId: id, ok: true, result: { kind: "word", text: "new disk contents" } }); await flush();
    expect(count("/reload-ready")).toBe(1);
    expect(controller.syncState.value).toBe("synced"); expect(controller.syncFrozen.value).toBe(false);
    expect(controller.retainedCanvas.value).toBeNull(); expect(host.lifetime).toEqual(["destroy"]);
    expect(emitted).not.toHaveBeenCalledWith("closed");
  });

  it("keeps the old frozen pane on native read failure or unconfirmed reload-ready acknowledgement", async () => {
    const { controller } = await bound(); host.refuseReloadReady();
    const id = await readyReplacement(controller);
    host.message({ type: "response", operationId: id, ok: true, result: { kind: "word", text: "new" } }); await flush();
    expect(controller.syncState.value).toBe("error"); expect(controller.syncFrozen.value).toBe(true);
    expect(controller.retainedCanvas.value).not.toBeNull(); expect(host.lifetime).toEqual([]);
    expect(count("/release")).toBe(0);
  });
  it("does not treat HTTP ok as synchronization when the replacement is still pending", async () => {
    const { controller } = await bound(); host.unconfirmedReloadReady();
    const id = await readyReplacement(controller);
    host.message({ type: "response", operationId: id, ok: true, result: { kind: "word", text: "new" } }); await flush();
    expect(controller.syncState.value).toBe("error"); expect(controller.syncFrozen.value).toBe(true);
    expect(controller.retainedCanvas.value).not.toBeNull(); expect(host.lifetime).toEqual([]);
  });

  it("rejects a replacement that changes file identity without destroying the previous canvas", async () => {
    const { controller, emitted } = await bound(); host.changeNextFileRef("a-different-file");
    host.replaceVersion(); await controller.refreshStatus(); await flush();
    expect(host.constructors()).toBe(1); expect(host.lifetime).toEqual([]);
    expect(controller.syncState.value).toBe("error"); expect(controller.retainedCanvas.value).not.toBeNull();
    expect(emitted).not.toHaveBeenCalledWith("closed"); expect(emitted).not.toHaveBeenCalledWith("openFailed", expect.anything());
  });

  it("keeps a committed but unbound closed session visible for reconciliation", async () => {
    const { controller, emitted } = await bound();
    host.live.batch = { id: "batch-a", purpose: "batch", phase: "prepared" }; host.live.syncRevision!++;
    await controller.refreshStatus(); host.live.closed = true; host.live.batch = null; host.live.syncRevision!++;
    await controller.refreshStatus();
    expect(controller.syncState.value).toBe("error"); expect(controller.syncFrozen.value).toBe(true);
    expect(host.lifetime).toEqual([]); expect(emitted).not.toHaveBeenCalledWith("closed");
  });

  it("does not discard when the user cancels the dialog or edits while the dialog is pending", async () => {
    const { controller } = await bound(); host.events().onDocumentStateChange({ data: true }); await flush();
    host.externalVersion(); await controller.refreshStatus();
    hooks.confirm.mockRejectedValueOnce("cancel"); await controller.discardAndReload();
    expect(count("/reload")).toBe(0); expect(controller.dirty.value).toBe(true);
    const confirmation = deferred<string>(); hooks.confirm.mockReturnValueOnce(confirmation.promise);
    const request = controller.discardAndReload(); await flush();
    host.events().onDocumentStateChange({ data: true }); await flush(); confirmation.resolve("confirm"); await request;
    expect(count("/reload")).toBe(0); expect(controller.syncError.value).toBe("officeEditor.sync.changedWhileConfirming");
    expect(host.lifetime).toEqual([]);
  });

  it("does not accept a server discard flag without an exact local user confirmation", async () => {
    const { controller } = await bound(); host.events().onDocumentStateChange({ data: true }); await flush();
    host.externalVersion(); host.requestBatch("reload", { discardChanges: true, confirmedDirtyEpoch: 1, confirmedCanvasVersion: versionA });
    await controller.refreshStatus(); await vi.advanceTimersByTimeAsync(1); await flush();
    expect(host.calls.find(call => call.path.endsWith("/batch-freeze"))?.body?.frozen).toBe(false);
    expect(controller.dirty.value).toBe(true); expect(host.lifetime).toEqual([]);
  });

  it("only discards the confirmed dirty generation after freeze, replacement and verified native readback", async () => {
    const { controller } = await bound(); host.events().onDocumentStateChange({ data: true }); await flush();
    host.externalVersion(); await controller.refreshStatus();
    const action = controller.discardAndReload(); await flush();
    expect(hooks.confirm).toHaveBeenCalledTimes(1);
    expect(host.calls.find(call => call.path.endsWith("/reload"))?.body).toMatchObject({ discardChanges: true, confirmedDirtyEpoch: 1, confirmedCanvasVersion: versionA, expectedVersion: versionB });
    await controller.refreshStatus(); await vi.advanceTimersByTimeAsync(1); await flush();
    expect(host.calls.find(call => call.path.endsWith("/batch-freeze"))?.body).toMatchObject({ frozen: true, dirtyEpoch: 1, persistedEpoch: 0 });
    expect(count("/save")).toBe(0); expect(host.lifetime).toEqual([]);
    const id = await readyReplacement(controller); host.confirmReloadHTTP(); await action;
    expect(controller.retainedCanvas.value).not.toBeNull(); expect(host.lifetime).toEqual([]);
    host.message({ type: "response", operationId: id, ok: true, result: { kind: "word", text: "disk version" } }); await flush();
    expect(controller.syncState.value).toBe("synced"); expect(host.lifetime).toEqual(["destroy"]);
  });

  it.each(["FILE_CHANGED", "FILE_IDENTITY_CHANGED"])("ends a definitely refused %s export while keeping dirty edits available for explicit conflict resolution", async code => {
    const { controller } = await bound(); host.events().onDocumentStateChange({ data: true }); await flush();
    const saving = controller.save(); await pump();
    host.events().onDownloadAs({ data: { url: "https://pogo.test/office/editor/cache/export.docx", fileType: "docx" } }); await flush();
    host.rejectExportAs(Object.assign(new Error("disk changed; write refused"), { status: 409, code })); await flush(); expect(await saving).toBe(false);
    host.externalVersion(); await controller.refreshStatus();
    expect(controller.dirty.value).toBe(true); expect(host.lifetime).toEqual([]);
    const action = controller.discardAndReload(); await flush();
    expect(hooks.confirm).toHaveBeenCalledTimes(1); expect(count("/reload")).toBe(1);
    host.confirmReloadHTTP(); await action;
  });

  it("keeps an unknown export protected and refuses even a new discard dialog until its outcome is reconciled", async () => {
    const { controller } = await bound(); host.events().onDocumentStateChange({ data: true }); await flush();
    const saving = controller.save(); await pump();
    host.events().onDownloadAs({ data: { url: "https://pogo.test/office/editor/cache/export.docx", fileType: "docx" } }); await flush();
    host.rejectExportAs(Object.assign(new Error("transport lost after send"), { status: 504, code: "OUTCOME_UNKNOWN" })); await flush(); expect(await saving).toBe(false);
    host.externalVersion(); await controller.refreshStatus(); await controller.discardAndReload();
    expect(hooks.confirm).not.toHaveBeenCalled(); expect(count("/reload")).toBe(0); expect(controller.dirty.value).toBe(true);
    expect(host.lifetime).toEqual([]); expect(await controller.prepareClose()).toBe(false);
  });

  it("ignores a late previous-generation status and native response during replacement", async () => {
    const { controller } = await bound(); const old = copy(host.live);
    const id = await readyReplacement(controller);
    host.setStatus({ ...old, closed: true, syncRevision: 999 }); await controller.refreshStatus();
    expect(controller.session.value?.id).toBe("office-session-b");
    host.message({ type: "response", sessionId: old.id, documentId: old.documentId, operationId: id, ok: true, result: { kind: "word", text: "stale" } }); await flush();
    expect(count("/reload-ready")).toBe(0); expect(host.lifetime).toEqual([]);
    host.setStatus(null); host.message({ type: "response", operationId: id, ok: true, result: { kind: "word", text: "current" } }); await flush();
    expect(controller.syncState.value).toBe("synced");
  });
  it("reloads a clean same-bytes atomic replacement into a new generation and verifies it", async () => {
    const { controller } = await bound(); host.externalVersion(versionA); await controller.refreshStatus();
    expect(count("/reload")).toBe(1);
    expect(host.calls.find(call => call.path.endsWith("/reload"))?.body?.expectedVersion).toBe(versionA);
    await controller.refreshStatus(); await vi.advanceTimersByTimeAsync(1); await flush();
    const id = await readyReplacement(controller, versionA);
    expect(controller.session.value?.id).toBe("office-session-b"); expect(controller.session.value?.generation).toBe(2);
    expect(host.lifetime).toEqual([]);
    host.message({ type: "response", operationId: id, ok: true, result: { kind: "word", text: "same bytes, newly bound inode" } }); await flush();
    expect(controller.syncState.value).toBe("synced"); expect(controller.syncFrozen.value).toBe(false); expect(host.lifetime).toEqual(["destroy"]);
  });
  it("preserves dirty edits and exposes conflict state for a same-bytes replacement", async () => {
    const { controller } = await bound(); host.events().onDocumentStateChange({ data: true }); await flush();
    host.externalVersion(versionA); await controller.refreshStatus();
    expect(controller.syncState.value).toBe("conflict"); expect(controller.dirty.value).toBe(true);
    expect(controller.syncFrozen.value).toBe(false); expect(count("/reload")).toBe(0);
    expect(host.constructors()).toBe(1); expect(host.lifetime).toEqual([]);
  });
});


describe("Office recovery keeps a live canvas available", () => {
  it("continues reading while a mutation awaits settlement and does not replay it", async () => {
    const { controller } = await bound();
    host.events().onDocumentStateChange({ data: true }); await flush();
    host.queue.push({ operationId: "unknown-mutation", operation: "word.replace", args: { search: "old", replacement: "new" } }); await pump();
    host.live.blocked = true;
    host.live.recovery = { state: "checking", operationIds: ["unknown-mutation"], revision: 1 };
    host.live.syncRevision!++; await pump();
    expect(controller.ready.value).toBe(true); expect(controller.canQueue.value).toBe(false); expect(controller.dirty.value).toBe(true);
    expect(host.lifetime).not.toContain("destroy"); expect(host.input.inert).toBe(false);
    const barrier = host.sent.find(value => value.type === "reconcile")!;
    expect(barrier.operationIds).toEqual(["unknown-mutation"]);
    host.message({ type: "reconciled", barrierId: String(barrier.barrierId), recoveryRevision: 1, settled: false, operations: [] }); await flush();
    expect(count("/reconcile")).toBe(0);
    host.message({ type: "reconciled", barrierId: String(barrier.barrierId), recoveryRevision: 1, settled: true, operations: [{ operationId: "unknown-mutation", operation: "word.replace", ok: true, result: { before: "old", after: "new" } }] }); await flush();
    expect(count("/reconcile")).toBe(1); expect(controller.canQueue.value).toBe(true); expect(controller.dirty.value).toBe(true);
    expect(host.sent.filter(value => value.operation === "word.replace")).toHaveLength(1);
    expect(host.constructors()).toBe(1);
  });
  it("releases a finished read after timeout and dispatches the next read without resetting the editor", async () => {
    const { controller } = await bound();
    host.events().onDocumentStateChange({ data: true }); await flush();
    host.queue.push({ operationId: "timed-read", operation: "word.read", args: {} }); await pump();
    host.finishFlight("timed-read", true); await pump();
    host.queue.push({ operationId: "next-read", operation: "word.read", args: {} }); await pump();
    expect(host.sent.filter(value => value.operation === "word.read").map(value => value.operationId)).toEqual(["timed-read", "next-read"]);
    host.message({ type: "response", operationId: "timed-read", ok: true, result: { text: "stale" } }); await flush();
    expect(count("/results")).toBe(0);
    host.message({ type: "response", operationId: "next-read", ok: true, result: { text: "manual input" } }); await flush();
    expect(count("/results")).toBe(1); expect(controller.dirty.value).toBe(true); expect(controller.ready.value).toBe(true); expect(host.constructors()).toBe(1);
  });
  it("retries a brief transport conflict on the existing polling loop with manual edits intact", async () => {
    const { controller } = await bound();
    host.events().onDocumentStateChange({ data: true }); await flush();
    host.failPoll(Object.assign(new Error("recovery in progress"), { status: 409, code: "EDITOR_RECOVERING" })); await pump();
    expect(controller.ready.value).toBe(false); expect(controller.failed.value).toBe("officeEditor.reconnecting");
    await vi.advanceTimersByTimeAsync(350); await flush();
    expect(controller.ready.value).toBe(true); expect(controller.failed.value).toBe(""); expect(controller.dirty.value).toBe(true); expect(host.constructors()).toBe(1);
  });
  it("rejects another plugin source and incomplete settlement lists", async () => {
    const { controller } = await bound();
    host.live.blocked = true; host.live.recovery = { state: "checking", operationIds: ["write-one", "write-two"], revision: 7 }; host.live.syncRevision!++; await pump();
    const barrier = host.sent.find(value => value.type === "reconcile")!;
    const receipt: Partial<OfficeBridgeMessage> = { type: "reconciled", barrierId: String(barrier.barrierId), recoveryRevision: 7, settled: true, operations: [{ operationId: "write-one", operation: "word.replace", ok: true }] };
    host.message(receipt, "https://pogo.test", {}); host.message(receipt); await flush();
    expect(count("/reconcile")).toBe(0); expect(controller.canQueue.value).toBe(false); expect(host.lifetime).not.toContain("destroy");
  });
});

it("waits for the original native export callback before clearing an unknown save", async () => {
  const { controller } = await bound();
  host.events().onDocumentStateChange({ data: true }); await flush();
  const save = controller.save(); await pump();
  const operationId = String(host.calls.find(call => call.path.endsWith('/save'))?.body?.operationId);
  expect(host.downloads).toEqual(["docx"]);
  await vi.advanceTimersByTimeAsync(35001); await flush();
  host.live.blocked = true;
  host.live.recovery = { state: "checking", operationIds: [], exportIds: [operationId], revision: 1 }; host.live.syncRevision!++;
  await pump();
  let barrier = [...host.sent].reverse().find(value => value.type === "reconcile")!;
  host.message({ type: "reconciled", barrierId: String(barrier.barrierId), recoveryRevision: 1, settled: true, operations: [] }); await flush();
  expect(count("/reconcile")).toBe(0); expect(controller.canQueue.value).toBe(false);
  host.events().onDownloadAs({ data: { url: "https://pogo.test/office/editor/cache/original.docx", fileType: "docx" } }); await flush();
  expect(count("/export")).toBe(0);
  await vi.advanceTimersByTimeAsync(3100); await flush();
  barrier = [...host.sent].reverse().find(value => value.type === "reconcile")!;
  host.message({ type: "reconciled", barrierId: String(barrier.barrierId), recoveryRevision: 1, settled: true, operations: [] }); await flush();
  expect(count("/reconcile")).toBe(1);
  expect(host.calls.find(call => call.path.endsWith("/reconcile"))?.body?.exports).toEqual([{ operationId, settled: true }]);
  expect(controller.dirty.value).toBe(true); expect(host.constructors()).toBe(1);
  host.finishSaveHTTP(); await save;
});


it("confirms an uncertain native settlement with a fresh read before enabling writes", async () => {
  const { controller } = await bound();
  host.events().onDocumentStateChange({ data: true }); await flush();
  host.queue.push({ operationId: "native-unknown", operation: "word.replace", args: {} }); await pump();
  host.live.blocked = true; host.live.recovery = { state: "checking", operationIds: ["native-unknown"], revision: 3 }; host.live.syncRevision!++;
  host.requireFreshRecoveryRead(); await pump();
  const barrier = host.sent.find(value => value.type === "reconcile")!;
  host.message({ type: "reconciled", barrierId: String(barrier.barrierId), recoveryRevision: 3, settled: true, operations: [{ operationId: "native-unknown", operation: "word.replace", ok: false, error: { code: "EDITOR_OUTCOME_UNKNOWN", message: "Native verification incomplete" } }] }); await flush();
  expect(controller.canQueue.value).toBe(false);
  await pump(); await pump(); await pump();
  const read = [...host.sent].reverse().find(value => value.operation === "word.read")!;
  expect(read).toBeDefined(); expect(host.sent.filter(value => value.operation === "word.replace")).toHaveLength(1);
  host.message({ type: "response", operationId: String(read.operationId), ok: true, result: { kind: "word", text: "current manual content" } }); await flush();
  expect(controller.canQueue.value).toBe(true); expect(controller.dirty.value).toBe(true); expect(host.constructors()).toBe(1);
});

it("clears a transient failed-read notice after a successful live read", async () => {
  const { controller } = await bound();
  await commandReceipt("failed-read", "word.read", { ok: false, error: { code: "READ_TIMEOUT", message: "Read timeout" } });
  expect(controller.failed.value).toContain("Read timeout");
  await commandReceipt("fresh-read", "word.read", { ok: true, result: { kind: "word", text: "manual content" } });
  expect(controller.failed.value).toBe(""); expect(controller.ready.value).toBe(true); expect(host.constructors()).toBe(1);
});


it("releases a confirmed write if its receipt acknowledgement was lost", async () => {
  const { controller } = await bound();
  host.events().onDocumentStateChange({ data: true }); await flush();
  host.queue.push({ operationId: "write-with-lost-ack", operation: "word.replace", args: {} }); await pump();
  host.finishFlight("write-with-lost-ack", false, false, true); await pump();
  host.queue.push({ operationId: "read-after-lost-ack", operation: "word.read", args: {} }); await pump();
  expect(host.sent.filter(value => value.operationId === "write-with-lost-ack")).toHaveLength(1);
  expect(host.sent.some(value => value.operationId === "read-after-lost-ack")).toBe(true);
  expect(controller.dirty.value).toBe(true); expect(host.constructors()).toBe(1);
});


it("resolves a confirmed save when its export acknowledgement arrives after queue polling", async () => {
  const { controller, emitted } = await bound();
  host.events().onDocumentStateChange({ data: true }); await flush();
  const save = controller.save(); await pump();
  const operationId = String(host.calls.find(call => call.path.endsWith("/save"))?.body?.operationId);
  host.events().onDownloadAs({ data: { url: "https://pogo.test/office/editor/cache/confirmed.docx", fileType: "docx" } }); await flush();
  host.live.dirty = false; host.live.persistedEpoch = host.live.dirtyEpoch; host.live.saveState = "saved"; host.live.revision++; host.live.savedAt = "2026-10-14T00:00:00Z"; host.live.syncRevision!++;
  host.finishSaveHTTP(); await flush(); // performSave is now waiting on flight.completion.
  host.finishFlight(operationId, false, false, true, true); await pump();
  expect(await save).toBe(true); expect(controller.dirty.value).toBe(false); expect(controller.failed.value).toBe("");
  expect(emitted.mock.calls.some(call => call[0] === "saved")).toBe(true);
  host.confirmExport(); await flush(); // Delayed acknowledgement is harmless.
  expect(controller.failed.value).toBe(""); expect(host.downloads).toHaveLength(1);
});

it("keeps newer manual input dirty when a known saved version completes", async () => {
  const { controller } = await bound();
  host.events().onDocumentStateChange({ data: true }); await flush();
  const save = controller.save(); await pump();
  const operationId = String(host.calls.find(call => call.path.endsWith("/save"))?.body?.operationId);
  host.events().onDownloadAs({ data: { url: "https://pogo.test/office/editor/cache/prior-version.docx", fileType: "docx" } }); await flush();
  host.live.dirty = false; host.live.persistedEpoch = host.live.dirtyEpoch; host.live.saveState = "saved"; host.live.revision++; host.live.savedAt = "2026-10-14T00:00:00Z";
  host.finishSaveHTTP(); await flush();
  host.events().onDocumentStateChange({ data: true }); await flush();
  host.finishFlight(operationId, false, false, true, true); await pump();
  expect(await save).toBe(false); expect(controller.dirty.value).toBe(true); expect(controller.failed.value).toContain("officeEditor.newerChanges");
  host.confirmExportAs({ ...copy(host.live), dirty: false, dirtyEpoch: 1, persistedEpoch: 1, saveState: "saved" }); await flush();
  expect(controller.dirty.value).toBe(true); expect(host.constructors()).toBe(1);
});

describe("Office five-minute startup and claim recovery", () => {
  it("keeps a slow document alive beyond the old cutoff and accepts ready before five minutes", async () => {
    host.noCanvasReady(); const { controller } = await setup();
    expect(controller.startupPhase.value).toBe("document");
    await vi.advanceTimersByTimeAsync(299_999);
    expect(controller.loading.value).toBe(true); expect(host.lifetime).toEqual([]); expect(count("/release")).toBe(0);
    host.events().onDocumentReady(); host.message(); await flush();
    expect(controller.ready.value).toBe(true); expect(controller.startupPhase.value).toBe("bridge");
    await vi.advanceTimersByTimeAsync(1000);
    expect(controller.failed.value).toBe(""); expect(host.constructors()).toBe(1);
  });
  it("releases only an unused canvas when its complete five-minute document budget expires", async () => {
    host.noCanvasReady(); const { controller, emitted } = await setup();
    await vi.advanceTimersByTimeAsync(300_001); await flush();
    expect(controller.failed.value).toContain("officeEditor.startup.timeout");
    expect(host.lifetime).toEqual(["release-confirmed", "destroy"]);
    expect(emitted).toHaveBeenCalledWith("openFailed", expect.any(String));
  });
  it("keeps manual input and the native frame after a bridge timeout, then recovers the same frame", async () => {
    const { controller } = await setup();
    host.events().onDocumentStateChange({ data: true }); await flush();
    await vi.advanceTimersByTimeAsync(300_000); await flush();
    expect(controller.dirty.value).toBe(true); expect(count("/release")).toBe(0); expect(host.lifetime).toEqual([]);
    expect(controller.failed.value).toContain("officeEditor.startup.timeout");
    host.message(); await flush();
    expect(controller.ready.value).toBe(true); expect(controller.dirty.value).toBe(true);
    expect(host.constructors()).toBe(1); expect(controller.failed.value).toBe("");
  });
  it("recovers a lost POST response with the same instance nonce", async () => {
    host.loseOpenResponses(1); const mounting = setup(); await flush();
    await vi.advanceTimersByTimeAsync(500); const { controller } = await mounting; host.message(); await flush();
    const requests = host.calls.filter(call => call.path === "/api/office/sessions");
    expect(requests).toHaveLength(2); expect(requests[0]!.body!.instanceId).toBe(requests[1]!.body!.instanceId);
    expect(controller.ready.value).toBe(true); expect(host.constructors()).toBe(1); expect(count("/release")).toBe(0);
  });
  it("retains an unknown claim across retry and prevents navigation from discarding its nonce", async () => {
    host.loseOpenResponses(3); const mounting = setup(); await flush();
    await vi.advanceTimersByTimeAsync(1500); const { controller, emitted } = await mounting;
    expect(controller.startupUnconfirmed.value).toBe(true); expect(await controller.prepareClose()).toBe(false);
    expect(host.unload().preventDefault).toHaveBeenCalled(); expect(emitted).not.toHaveBeenCalledWith("openFailed", expect.anything());
    const original = host.calls.find(call => call.path === "/api/office/sessions")!.body!.instanceId;
    await controller.retryStartup(); host.message(); await flush();
    expect(controller.ready.value).toBe(true); expect(controller.startupUnconfirmed.value).toBe(false);
    expect(host.calls.filter(call => call.path === "/api/office/sessions").every(call => call.body!.instanceId === original)).toBe(true);
  });
  it("resumes a late ready after an uncertain release without leaving callbacks permanently blocked", async () => {
    host.noCanvasReady(); const { controller } = await setup(); host.failRelease(new Error("release result unknown"));
    host.events().onError({ data: { errorDescription: "initial load failed" } }); await flush();
    expect(controller.bootstrapReleasePending.value).toBe(true); expect(host.lifetime).toEqual([]);
    host.events().onDocumentReady(); host.message(); await flush();
    expect(controller.ready.value).toBe(true); expect(controller.bootstrapReleasePending.value).toBe(false); expect(host.constructors()).toBe(1);
  });
  it("never reopens input on a retired claim when ready arrives during release", async () => {
    host.noCanvasReady(); const { controller, emitted } = await setup(); host.delayRelease();
    host.events().onError({ data: { errorDescription: "initial load failed" } }); await flush();
    expect(count("/release")).toBe(1); expect(host.lifetime).toEqual([]); expect(host.input.inert).toBe(true);
    host.events().onDocumentReady(); host.confirmRelease(); await flush();
    expect(controller.editorReady.value).toBe(false); expect(controller.canQueue.value).toBe(false); expect(host.input.inert).toBe(true);
    expect(host.lifetime).toEqual(["release-confirmed", "destroy"]); expect(emitted).toHaveBeenCalledWith("openFailed", expect.any(String));
  });
  it("releases its own uncertain unused claim and reopens safely from one explicit retry", async () => {
    host.opened.browserURL = "http://127.0.0.1:19778"; host.failRelease(new Error("release response lost"));
    const { controller, emitted } = await setup();
    expect(controller.bootstrapReleasePending.value).toBe(true);
    host.opened.browserURL = "/office/editor";
    await controller.retryStartup(); host.message(); await flush();
    expect(controller.ready.value).toBe(true); expect(controller.bootstrapReleasePending.value).toBe(false);
    expect(host.lifetime).toEqual(["release-confirmed"]); expect(host.constructors()).toBe(1);
    expect(emitted).not.toHaveBeenCalledWith("openFailed", expect.anything());
  });
  it("cancels unused-claim release when queued human input appears at the input barrier", async () => {
    host.noCanvasReady(); const { controller } = await setup();
    host.events().onError({ data: { errorDescription: "initial load failed" } });
    host.events().onDocumentStateChange({ data: true }); await flush();
    expect(controller.dirty.value).toBe(true); expect(count("/state")).toBe(1); expect(count("/release")).toBe(0); expect(host.lifetime).toEqual([]);
    expect(await controller.prepareClose()).toBe(false);
  });
  it("acknowledges duplicate plugin readiness without replacing live capabilities or editing state", async () => {
    const { controller } = await bound(); host.events().onDocumentStateChange({ data: true }); await flush();
    host.message({ capabilities: { read: false, blocked: false } }); await flush();
    expect(host.readyAcks).toHaveLength(2); expect(count("/ready")).toBe(1); expect(controller.dirty.value).toBe(true); expect(host.constructors()).toBe(1);
  });
});

describe("Office writer lease and canonical autosave integration", () => {
  it("disabling automatic workspace saves retains dirty edits while explicit save still works", async () => {
    const { controller } = await bound(); expect(controller.autoSaveEnabled.value).toBe(true);
    controller.autoSaveEnabled.value = false; host.events().onDocumentStateChange({ data: true }); await flush();
    host.events().onDocumentStateChange({ data: false }); await flush(); await vi.advanceTimersByTimeAsync(20_000);
    expect(count("/save")).toBe(0); expect(controller.dirty.value).toBe(true); expect(host.input.inert).toBe(false);
    void controller.save(); await flush(); expect(count("/save")).toBe(1);
  });

  it("does not treat the engine clean event as a canonical saved receipt", async () => {
    const { controller, emitted } = await bound();
    host.events().onDocumentStateChange({ data: true }); await flush();
    host.events().onDocumentStateChange({ data: false }); await flush();
    expect(controller.dirty.value).toBe(true); expect(controller.session.value?.persistedEpoch).toBe(0);
    expect(emitted).not.toHaveBeenCalledWith("saved"); expect(count("/save")).toBe(1);
    await pump(); expect(host.downloads).toEqual(["docx"]);
    expect(controller.dirty.value).toBe(true); expect(emitted).not.toHaveBeenCalledWith("saved");
  });

  it("retains active Chinese composition without input freezing until its real idle period", async () => {
    const { controller } = await bound();
    host.inputEvent("compositionstart"); host.events().onDocumentStateChange({ data: true }); await flush();
    await vi.advanceTimersByTimeAsync(20_000); await flush();
    expect(count("/save")).toBe(0); expect(host.input.inert).toBe(false); expect(controller.dirty.value).toBe(true);
    host.inputEvent("compositionend"); host.events().onDocumentStateChange({ data: false }); await flush();
    await vi.advanceTimersByTimeAsync(2_999); expect(count("/save")).toBe(0); expect(host.input.inert).toBe(false);
    await vi.advanceTimersByTimeAsync(1); await flush(); expect(count("/save")).toBe(1);
  });

  it("routes the editor frame save shortcut into one canonical host export", async () => {
    const { controller } = await bound(); host.events().onDocumentStateChange({ data: true }); await flush();
    const event = { ctrlKey: true, metaKey: false, altKey: false, shiftKey: false, key: "s", isComposing: false, preventDefault: vi.fn(), stopPropagation: vi.fn() };
    host.inputEvent("keydown", event); await flush(); host.inputEvent("keydown", event); await flush();
    expect(event.preventDefault).toHaveBeenCalledTimes(2); expect(event.stopPropagation).toHaveBeenCalledTimes(2);
    expect(count("/save")).toBe(1); expect(controller.dirty.value).toBe(true);
    await pump(); expect(host.downloads).toEqual(["docx"]);
  });

  it("a rejected writer heartbeat fences input while preserving the unsaved native canvas", async () => {
    host.live.leaseEpoch = 2; const { controller } = await bound();
    host.inputEvent("compositionstart"); host.events().onDocumentStateChange({ data: true }); await flush();
    host.failHeartbeat(Object.assign(new Error("Another writer has the lease"), { status: 409, code: "EDITOR_FENCED" }));
    await vi.advanceTimersByTimeAsync(10_000); await flush();
    expect(controller.leaseFenced.value).toBe(true); expect(controller.ready.value).toBe(false); expect(controller.canQueue.value).toBe(false);
    expect(controller.dirty.value).toBe(true); expect(host.input.inert).toBe(true); expect(host.lifetime).toEqual([]); expect(host.constructors()).toBe(1);
    expect(count("/close")).toBe(0); expect(count("/release")).toBe(0); expect(count("/save")).toBe(0);
    const dirtyEpoch = host.live.dirtyEpoch; host.events().onDocumentStateChange({ data: true }); await flush();
    expect(host.live.dirtyEpoch).toBe(dirtyEpoch);
  });

  it("never installs a heartbeat receipt for a different editor generation", async () => {
    host.live.leaseEpoch = 2; const { controller } = await bound();
    host.heartbeatSession({ ...copy(host.live), generation: host.live.generation + 1 });
    await vi.advanceTimersByTimeAsync(10_000); await flush();
    expect(controller.leaseFenced.value).toBe(true); expect(controller.session.value?.generation).toBe(1);
    expect(host.input.inert).toBe(true); expect(host.constructors()).toBe(1); expect(host.lifetime).toEqual([]);
  });

  it("does not clear newer local edits when an earlier clean heartbeat arrives late", async () => {
    host.live.leaseEpoch = 2; const { controller, emitted } = await bound();
    const earlier = copy(host.live), delayed = deferred<{ ok: boolean; session: OfficeSession }>();
    const original = hooks.request.getMockImplementation()!;
    hooks.request.mockImplementation((method: string, path: string, body?: Record<string, unknown>) => path.endsWith("/heartbeat") ? delayed.promise : original(method, path, body));
    await vi.advanceTimersByTimeAsync(10_000); await flush();
    host.inputEvent("compositionstart"); host.events().onDocumentStateChange({ data: true }); await flush();
    expect(controller.dirty.value).toBe(true); expect(host.live.dirty).toBe(true);
    delayed.resolve({ ok: true, session: earlier }); await flush();
    expect(controller.dirty.value).toBe(true); expect(emitted).not.toHaveBeenCalledWith("saved");
    host.inputEvent("compositionend"); await vi.advanceTimersByTimeAsync(3000); await flush();
    expect(count("/save")).toBe(1);
  });

  it("retains a clean leased canvas when a remote takeover closes its former writer before heartbeat replies", async () => {
    host.live.leaseEpoch = 2; const { controller, emitted } = await bound();
    host.live.closed = true; await controller.refreshStatus(); await flush();
    expect(controller.leaseFenced.value).toBe(true); expect(controller.ready.value).toBe(false);
    expect(host.input.inert).toBe(true); expect(host.lifetime).toEqual([]); expect(host.constructors()).toBe(1);
    expect(emitted).not.toHaveBeenCalledWith("closed"); expect(count("/close")).toBe(0);
  });

  it("uses the exact claimed lease epoch for every heartbeat rather than adopting another writer", async () => {
    host.live.leaseEpoch = 12; const { controller } = await bound(); await vi.advanceTimersByTimeAsync(10_000); await flush();
    const heartbeatCalls = host.calls.filter(call => call.path.endsWith("/heartbeat"));
    expect(heartbeatCalls).toHaveLength(2);
    const instanceId = host.calls.find(call => call.path === "/api/office/sessions")!.body!.instanceId;
    expect(heartbeatCalls.every(call => call.body?.leaseEpoch === 12 && call.body?.instanceId === instanceId)).toBe(true);
    expect(controller.leaseFenced.value).toBe(false); expect(controller.ready.value).toBe(true);
  });
});
