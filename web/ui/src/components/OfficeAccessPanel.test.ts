import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, nextTick, reactive, type Ref } from "vue";
import type { OfficeAccess, OfficeOpen, OfficeSession } from "@/agent/officeTransport";

const host = vi.hoisted(() => ({ request: vi.fn(), confirm: vi.fn(), cleanups: [] as (() => void)[] }));
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), onBeforeUnmount: (fn: () => void) => host.cleanups.push(fn), useSSRContext: () => ({ modules: new Set() }) }));
vi.mock("vue-i18n", async original => { const { ref } = await import("vue"); return { ...await original<typeof import("vue-i18n")>(), useI18n: () => ({ t: (key: string) => key, locale: ref("zh-CN") }) }; });
vi.mock("element-plus", () => ({ ElMessageBox: { confirm: host.confirm } }));
vi.mock("@/api/client", () => ({ officeRequest: host.request, authHeaders: () => ({ Authorization: "Bearer synthetic-only" }) }));
import OfficeAccessPanel from "./OfficeAccessPanel.vue";

function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done; }); return { promise, resolve }; }
const copy = <T>(value: T): T => JSON.parse(JSON.stringify(value)) as T;
const version = "a".repeat(64);
function writer(): OfficeSession {
  return { id: "writer-original", documentId: "document-original", conversationId: "conversation-a", path: "reports/review.xlsx", fileRef: "file-review", bootEpoch: "boot-original", title: "Review", fileType: "xlsx", editor: "cell", generation: 7, ready: true, instanceBound: true, canvasMounted: true, closed: false, dirty: false, dirtyEpoch: 4, persistedEpoch: 4, saveState: "saved", revision: 2, blocked: false, pending: false, canvasVersion: version, diskVersion: version, leaseEpoch: 9, connectionState: "disconnected" };
}
function available(): OfficeAccess { return { ok: true, path: "reports/review.xlsx", fileRef: "file-review", diskVersion: version, writer: null, access: { state: "available", canTakeover: false, canRecover: false, leaseEpoch: 0 }, recoveryCopies: [] }; }
function occupied(): OfficeAccess { return { ...available(), writer: writer(), access: { state: "disconnected", canTakeover: true, canRecover: false, leaseEpoch: 9 } }; }
function claim(): OfficeOpen { return { session: { ...writer(), id: "new-owned-session", documentId: "new-owned-document", generation: 8, ready: false, canvasMounted: false, instanceBound: true, leaseEpoch: 10 }, requestedPath: "reports/review.xlsx", instanceOwned: true, bootstrapToken: "unused-claim-proof", bridgeToken: "synthetic-bridge-capability-123456789", bridgeURL: "/office/bridge/new-owned-session/cap/", browserURL: "/office/editor", config: {} }; }
type Controller = { access: Ref<OfficeAccess | null>; failure: Ref<string>; busy: Ref<boolean>; checking: Ref<boolean>; state: Ref<string>; takeoverUnconfirmed: Ref<boolean>; edit(): Promise<void>; recover(readForgotten?: boolean): Promise<void>; refresh(): Promise<void>; editSavedCopy(): Promise<void>; resetDialogOpen: Ref<boolean>; resetAcknowledged: Ref<boolean>; resetInterruptAcknowledged: Ref<boolean>; resetRequiresOnlineConfirmation: Ref<boolean>; canConfirmReset: Ref<boolean>; resetUnconfirmed: Ref<boolean>; resetTitle: Ref<string>; resetConnection: Ref<string>; resetLastSaved: Ref<string>; showReset(): void; cancelReset(): void; clearOccupancy(): Promise<void> };
const scopes: ReturnType<typeof effectScope>[] = [];
async function flush() { for (let i = 0; i < 15; i++) await Promise.resolve(); await nextTick(); }
function mount() {
  const scope = effectScope(); scopes.push(scope);
  const props = reactive({ path: "reports/review.xlsx", conversationId: "conversation-a" });
  const emit = vi.fn();
  const controller = scope.run(() => (OfficeAccessPanel as unknown as { setup(props: unknown, ctx: unknown): Controller }).setup(props, { emit, expose() {} }))!;
  const cleanup = host.cleanups[host.cleanups.length - 1]!;
  return { controller, props, emit, unmount: cleanup };
}
let current: OfficeAccess;
beforeEach(() => {
  vi.useFakeTimers(); current = available(); host.confirm.mockReset().mockResolvedValue("confirm"); host.request.mockReset().mockImplementation(async (method: string, path: string) => {
    if (method === "GET" && path.startsWith("/api/office/access?")) return copy(current);
    if (method === "POST" && ["/api/office/takeover", "/api/office/reset"].includes(path)) return claim();
    if (method === "POST" && path === "/api/office/saved-copy") return { ok: true, path: "reports/review-saved-copy.xlsx", title: "Saved copy", diskVersion: version };
    if (method === "POST" && path.endsWith("/recover")) return { ok: true, state: "pending" };
    if (method === "POST" && path.endsWith("/release")) return { ok: true };
    throw new Error(`Unexpected access-panel request ${method} ${path}`);
  });
});
afterEach(() => { for (const cleanup of host.cleanups.splice(0)) cleanup(); for (const scope of scopes.splice(0)) scope.stop(); vi.clearAllTimers(); vi.useRealTimers(); });
const posts = () => host.request.mock.calls.filter(call => call[0] === "POST");

describe("Office read-only availability and explicit writer takeover", () => {
  it("checks the requested file and conversation without claiming or closing an editor", async () => {
    const { controller } = mount(); await flush();
    const [method, path, , signal] = host.request.mock.calls[0]!;
    expect(method).toBe("GET"); expect(new URL(path, "https://pogo.test").searchParams.get("path")).toBe("reports/review.xlsx");
    expect(new URL(path, "https://pogo.test").searchParams.get("conversation_id")).toBe("conversation-a");
    expect(signal).toBeInstanceOf(AbortSignal); expect(controller.state.value).toBe("available"); expect(posts()).toHaveLength(0);
  });

  it("requests editing only after an explicit click when there is no existing writer", async () => {
    const { controller, emit } = mount(); await flush(); expect(emit).not.toHaveBeenCalled();
    await controller.edit(); expect(emit).toHaveBeenCalledExactlyOnceWith("edit"); expect(posts()).toHaveLength(0);
  });

  it("keeps an online writer read-only and never closes or steals its instance", async () => {
    current = occupied(); current.access = { ...current.access, state: "online", canTakeover: false };
    const { controller, emit } = mount(); await flush(); await controller.edit();
    expect(controller.state.value).toBe("online"); expect(posts()).toHaveLength(0); expect(emit).not.toHaveBeenCalled();
    expect(host.confirm).not.toHaveBeenCalled();
  });

  it("passes the exact expired lease, file version and writer generation to one CAS takeover", async () => {
    current = occupied(); const { controller, emit } = mount(); await flush(); await controller.edit();
    const calls = posts(); expect(calls).toHaveLength(1);
    expect(calls[0]![1]).toBe("/api/office/takeover");
    expect(calls[0]![2]).toEqual({ conversationId: "conversation-a", path: "reports/review.xlsx", instanceId: expect.any(String), expectedSessionId: "writer-original", expectedGeneration: 7, expectedDirtyEpoch: 4, expectedVersion: version, expectedLeaseEpoch: 9 });
    expect(emit).toHaveBeenCalledWith("claimed", { claim: claim(), instanceId: calls[0]![2].instanceId });
    expect(host.confirm).not.toHaveBeenCalled(); expect(calls.some(call => call[1].endsWith("/close"))).toBe(false);
  });

  it("requires confirmation and the immutable recovered-copy identity for dirty takeover", async () => {
    current = occupied(); current.writer!.dirty = true;
    current.writer!.recoveryCopy = { id: "recovery-ready-a", state: "ready", title: "Recovered review.xlsx", fileType: "xlsx", createdAt: "2026-10-18T00:00:00Z", sourceSessionId: "writer-original", generation: 7, dirtyEpoch: 4, persistedEpoch: 2, baseVersion: version };
    const { controller } = mount(); await flush(); await controller.edit();
    expect(host.confirm).toHaveBeenCalledOnce(); expect(posts()[0]![2].recoveryId).toBe("recovery-ready-a");
    expect(posts().some(call => call[1].endsWith("/close") || call[1].endsWith("/save"))).toBe(false);
  });

  it("never turns an unconfirmed writer into an available editor based on local time", async () => {
    current = occupied(); current.access = { ...current.access, state: "unconfirmed", canTakeover: false, canRecover: true };
    current.writer!.dirty = true;
    const { controller, emit } = mount(); await flush(); await vi.advanceTimersByTimeAsync(60_000); await controller.edit();
    expect(posts()).toHaveLength(0); expect(emit).not.toHaveBeenCalled(); expect(controller.state.value).toBe("unconfirmed");
  });

  it("cancelling recovery consent leaves the previous writer and canvas untouched", async () => {
    current = occupied(); current.writer!.dirty = true; current.access.canRecover = true; current.access.canTakeover = false;
    host.confirm.mockRejectedValueOnce("cancel"); const { controller } = mount(); await flush(); await controller.recover();
    expect(posts()).toHaveLength(0); expect(controller.failure.value).toBe(""); expect(controller.access.value?.writer?.id).toBe("writer-original");
  });

  it("requests isolated recovery with the exact original session, epoch and base version", async () => {
    current = occupied(); current.writer!.dirty = true; current.access.canRecover = true; current.access.canTakeover = false;
    const { controller } = mount(); await flush(); await controller.recover();
    expect(posts()).toEqual([["POST", "/api/office/sessions/writer-original/recover", { expectedGeneration: 7, expectedDirtyEpoch: 4, expectedLeaseEpoch: 9, expectedVersion: version }, undefined]]);
  });

  it("recovers the original canvas version even when the canonical disk file has changed", async () => {
    current = occupied(); current.writer!.dirty = true; current.diskVersion = "b".repeat(64); current.access.canRecover = true;
    const { controller } = mount(); await flush(); await controller.recover();
    expect(posts()[0]![2].expectedVersion).toBe(version);
  });

  it("uses only the explicit retained-snapshot lookup for a sealed unknown recovery", async () => {
    current = occupied(); current.writer!.dirty = true; current.writer!.recoverySealed = true;
    current.access.canTakeover = false; current.access.canRecover = false; current.access.canReadForgotten = true;
    const { controller } = mount(); await flush(); await controller.recover(true);
    expect(host.confirm).not.toHaveBeenCalled();
    expect(posts()).toEqual([["POST", "/api/office/sessions/writer-original/recover", { expectedGeneration: 7, expectedDirtyEpoch: 4, expectedLeaseEpoch: 9, expectedVersion: version, readForgotten: true }, undefined]]);
    await vi.advanceTimersByTimeAsync(60_000); await flush();
    expect(posts()).toHaveLength(1); expect(controller.access.value?.writer?.dirty).toBe(true);
  });

  it("does not infer retained lookup permission from a local dirty flag", async () => {
    current = occupied(); current.writer!.dirty = true; current.writer!.recoverySealed = true;
    current.access.canTakeover = false; current.access.canRecover = true; current.access.canReadForgotten = false;
    const { controller } = mount(); await flush(); await controller.recover(true);
    expect(posts()).toHaveLength(0); expect(host.confirm).not.toHaveBeenCalled();
  });

  it("creates an explicitly confirmed saved-file copy without retiring the unknown dirty writer", async () => {
    current = occupied(); current.writer!.dirty = true; current.access.canTakeover = false;
    const { controller, emit } = mount(); await flush(); await controller.editSavedCopy();
    expect(host.confirm).toHaveBeenCalledOnce(); expect(posts()).toHaveLength(1);
    expect(posts()[0]).toEqual(["POST", "/api/office/saved-copy", { conversationId: "conversation-a", path: "reports/review.xlsx", expectedVersion: version, operationId: expect.any(String) }]);
    expect(emit).toHaveBeenCalledWith("openCopy", "reports/review-saved-copy.xlsx");
    expect(controller.access.value?.writer?.dirty).toBe(true); expect(controller.access.value?.writer?.id).toBe("writer-original");
  });

  it("retries an unknown saved-copy outcome with the same operation and version", async () => {
    current = occupied(); current.writer!.dirty = true; current.access.canTakeover = false;
    const original = host.request.getMockImplementation()!; let first = true;
    host.request.mockImplementation(async (...args) => { if (first && args[1] === "/api/office/saved-copy") { first = false; throw new TypeError("Response lost after copy commit"); } return original(...args); });
    const { controller, emit } = mount(); await flush(); await controller.editSavedCopy();
    current.diskVersion = "b".repeat(64); await controller.refresh(); await controller.editSavedCopy();
    expect(posts()).toHaveLength(2); expect(posts()[1]![2]).toEqual(posts()[0]![2]);
    expect(host.confirm).toHaveBeenCalledOnce(); expect(emit).toHaveBeenCalledExactlyOnceWith("openCopy", "reports/review-saved-copy.xlsx");
  });

  it("retains the exact uncertain takeover identity instead of allocating another writer", async () => {
    current = occupied(); const original = host.request.getMockImplementation()!; let first = true;
    host.request.mockImplementation(async (...args) => {
      if (first && args[1] === "/api/office/takeover") { first = false; current.writer = { ...writer(), id: "issued-but-unconfirmed", generation: 8, leaseEpoch: 10 }; current.access.canTakeover = false; throw new TypeError("Response lost after writer claim"); }
      return original(...args);
    });
    const { controller, emit } = mount(); await flush(); await controller.edit();
    expect(controller.takeoverUnconfirmed.value).toBe(true); expect(emit).not.toHaveBeenCalled();
    await controller.edit(); expect(posts()).toHaveLength(2); expect(posts()[1]![2]).toEqual(posts()[0]![2]);
    expect(controller.takeoverUnconfirmed.value).toBe(false); expect(emit).toHaveBeenCalledOnce();
  });

  it("does not retry or close a writer after a stale CAS takeover is refused", async () => {
    current = occupied(); const original = host.request.getMockImplementation()!;
    host.request.mockImplementation(async (...args) => { if (args[0] === "POST" && args[1] === "/api/office/takeover") throw Object.assign(new Error("Writer lease changed"), { status: 409, code: "OFFICE_ACCESS_CHANGED" }); return original(...args); });
    const { controller, emit } = mount(); await flush(); await controller.edit();
    expect(posts()).toHaveLength(1); expect(emit).not.toHaveBeenCalled();
    expect(host.request.mock.calls.filter(call => call[0] === "GET")).toHaveLength(2);
    expect(controller.access.value?.writer?.id).toBe("writer-original");
  });

  it("releases only its own unused late takeover claim after unmount", async () => {
    current = occupied(); const late = deferred<OfficeOpen>(), original = host.request.getMockImplementation()!;
    host.request.mockImplementation(async (...args) => args[0] === "POST" && args[1] === "/api/office/takeover" ? late.promise : original(...args));
    const { controller, emit, unmount } = mount(); await flush(); const editing = controller.edit(); await flush(); unmount(); late.resolve(claim()); await editing;
    expect(emit).not.toHaveBeenCalled(); const calls = posts(); expect(calls).toHaveLength(2);
    expect(calls[1]![1]).toBe("/api/office/sessions/new-owned-session/release");
    expect(calls[1]![2]).toEqual({ instanceId: calls[0]![2].instanceId, bootstrapToken: "unused-claim-proof", fileRef: "file-review", bootEpoch: "boot-original" });
    expect(calls.some(call => call[1].endsWith("/close"))).toBe(false);
  });

  it("does not assign a late old-file claim after the preview changes to another file", async () => {
    current = occupied(); const late = deferred<OfficeOpen>(), original = host.request.getMockImplementation()!;
    host.request.mockImplementation(async (...args) => args[0] === "POST" && args[1] === "/api/office/takeover" ? late.promise : original(...args));
    const { controller, props, emit } = mount(); await flush(); const editing = controller.edit(); await flush();
    current = { ...available(), path: "reports/another.xlsx", fileRef: "file-another" }; props.path = "reports/another.xlsx"; await flush();
    late.resolve(claim()); await editing;
    expect(emit).not.toHaveBeenCalled(); expect(controller.access.value?.fileRef).toBe("file-another");
    expect(posts()[1]![1]).toBe("/api/office/sessions/new-owned-session/release");
  });

  it("aborts availability reads and ignores their late results on unmount", async () => {
    const late = deferred<OfficeAccess>(); host.request.mockImplementationOnce(() => late.promise);
    const { controller, unmount } = mount(); await flush(); const signal = host.request.mock.calls[0]![3] as AbortSignal;
    unmount(); expect(signal.aborted).toBe(true); late.resolve(occupied()); await flush();
    expect(controller.access.value).toBeNull(); expect(posts()).toHaveLength(0); expect(vi.getTimerCount()).toBe(0);
  });
});


describe("Office explicit clearing of an editing lock", () => {
  function resettable(online = false) {
    const result = occupied(); result.writer!.dirty = true; result.writer!.persistedEpoch = 2;
    result.writer!.savedAt = "2026-10-16T09:11:22Z";
    result.access = { ...result.access, state: online ? "online" : "disconnected", canTakeover: false, canReset: true, resetRequiresOnlineConfirmation: online };
    return result;
  }
  it("requires acknowledgement before clearing and cancels without any mutation", async () => {
    current = resettable(); const { controller } = mount(); await flush(); controller.showReset();
    expect(controller.resetDialogOpen.value).toBe(true); expect(controller.resetTitle.value).toBe("Review");
    expect(controller.resetConnection.value).toBe("officeEditor.access.resetConnection.disconnected");
    expect(controller.resetLastSaved.value).toBe(new Date("2026-10-16T09:11:22Z").toLocaleString("zh-CN"));
    expect(controller.canConfirmReset.value).toBe(false); await controller.clearOccupancy(); expect(posts()).toHaveLength(0);
    controller.resetAcknowledged.value = true; expect(controller.canConfirmReset.value).toBe(true); controller.cancelReset();
    expect(controller.resetDialogOpen.value).toBe(false); expect(controller.resetAcknowledged.value).toBe(false);
    await controller.clearOccupancy(); expect(posts()).toHaveLength(0);
  });
  it("does not infer permission from dirty or disconnected metadata", async () => {
    current = resettable(); current.access.canReset = false;
    const { controller } = mount(); await flush(); controller.showReset(); controller.resetAcknowledged.value = true; await controller.clearOccupancy();
    expect(controller.resetDialogOpen.value).toBe(false); expect(posts()).toHaveLength(0);
  });
  it("clears exactly the old writer and edits the original without making a copy", async () => {
    current = resettable(); const { controller, emit } = mount(); await flush(); controller.showReset(); controller.resetAcknowledged.value = true;
    await controller.clearOccupancy();
    expect(posts()).toEqual([["POST", "/api/office/reset", { conversationId: "conversation-a", path: "reports/review.xlsx", instanceId: expect.any(String), expectedSessionId: "writer-original", expectedGeneration: 7, expectedDirtyEpoch: 4, expectedPersistedEpoch: 2, expectedLeaseEpoch: 9, expectedVersion: version, expectedCanvasVersion: version, confirmDiscardChanges: true, allowInterruptOnline: false }, undefined]]);
    expect(emit).toHaveBeenCalledExactlyOnceWith("claimed", { claim: claim(), instanceId: posts()[0]![2].instanceId });
    expect(host.confirm).not.toHaveBeenCalled();
  });
  it("requires separate consent before interrupting an online or unconfirmed editor", async () => {
    current = resettable(true); const { controller } = mount(); await flush(); controller.showReset();
    expect(controller.resetRequiresOnlineConfirmation.value).toBe(true); controller.resetAcknowledged.value = true;
    expect(controller.canConfirmReset.value).toBe(false); await controller.clearOccupancy(); expect(posts()).toHaveLength(0);
    controller.resetInterruptAcknowledged.value = true; expect(controller.canConfirmReset.value).toBe(true); await controller.clearOccupancy();
    expect(posts()[0]![2].allowInterruptOnline).toBe(true);
  });
  it("freezes the confirmation snapshot while background reads find newer writer details", async () => {
    current = resettable(); const { controller } = mount(); await flush(); controller.showReset(); controller.resetAcknowledged.value = true;
    current.writer!.dirtyEpoch = 8; current.writer!.persistedEpoch = 5; current.writer!.leaseEpoch = 10; current.diskVersion = "b".repeat(64);
    await controller.refresh(); await controller.clearOccupancy();
    expect(posts()[0]![2]).toMatchObject({ expectedDirtyEpoch: 4, expectedPersistedEpoch: 2, expectedLeaseEpoch: 9, expectedVersion: version });
  });
  it("retries an unknown clearing result only explicitly with the identical request and blocks other writer operations", async () => {
    current = resettable(); const original = host.request.getMockImplementation()!; let first = true;
    host.request.mockImplementation(async (...args) => {
      if (first && args[1] === "/api/office/reset") { first = false; current.writer = { ...writer(), id: "issued-after-reset", generation: 8, leaseEpoch: 10 }; current.access.canReset = true; current.access.canTakeover = true; current.access.canRecover = true; throw new TypeError("Response lost after clearing committed"); }
      return original(...args);
    });
    const { controller, emit } = mount(); await flush(); controller.showReset(); controller.resetAcknowledged.value = true; await controller.clearOccupancy();
    expect(controller.resetUnconfirmed.value).toBe(true); expect(emit).not.toHaveBeenCalled();
    controller.showReset(); await controller.edit(); await controller.recover(); await controller.editSavedCopy();
    await vi.advanceTimersByTimeAsync(30_000); expect(posts()).toHaveLength(1); expect(controller.failure.value).toBe("officeEditor.access.resetUnknown");
    await controller.clearOccupancy(); expect(posts()).toHaveLength(2); expect(posts()[1]).toEqual(posts()[0]);
    expect(controller.resetUnconfirmed.value).toBe(false); expect(emit).toHaveBeenCalledOnce();
  });
  it("requires renewed consent after stale CAS refusal without automatic retry or closing the writer", async () => {
    current = resettable(); const original = host.request.getMockImplementation()!; let first = true;
    host.request.mockImplementation(async (...args) => {
      if (first && args[1] === "/api/office/reset") { first = false; current.access.state = "online"; current.access.resetRequiresOnlineConfirmation = true; throw Object.assign(new Error("Now online"), { status: 409, code: "EDITOR_STILL_CONNECTED" }); }
      return original(...args);
    });
    const { controller, emit } = mount(); await flush(); controller.showReset(); controller.resetAcknowledged.value = true; await controller.clearOccupancy();
    expect(controller.failure.value).toBe("officeEditor.access.resetChanged"); expect(controller.resetUnconfirmed.value).toBe(false);
    await controller.clearOccupancy(); await vi.advanceTimersByTimeAsync(20_000); expect(posts()).toHaveLength(1); expect(emit).not.toHaveBeenCalled();
    controller.showReset(); expect(controller.resetAcknowledged.value).toBe(false); expect(controller.resetRequiresOnlineConfirmation.value).toBe(true);
    controller.resetAcknowledged.value = true; await controller.clearOccupancy(); expect(posts()).toHaveLength(1);
    controller.resetInterruptAcknowledged.value = true; await controller.clearOccupancy(); expect(posts()).toHaveLength(2); expect(posts()[1]![2].instanceId).not.toBe(posts()[0]![2].instanceId);
  });
  it("drops confirmation when preview scope changes and never submits a stale dialog", async () => {
    current = resettable(); const { controller, props } = mount(); await flush(); controller.showReset(); controller.resetAcknowledged.value = true;
    current = { ...available(), path: "reports/another.xlsx", fileRef: "file-another" }; props.path = "reports/another.xlsx"; await flush();
    expect(controller.resetDialogOpen.value).toBe(false); expect(controller.resetAcknowledged.value).toBe(false);
    await controller.clearOccupancy(); expect(posts()).toHaveLength(0);
  });
  it.each(["unmount", "file", "conversation"])("releases only the unused late reset claim after %s", async kind => {
    current = resettable(); const late = deferred<OfficeOpen>(), original = host.request.getMockImplementation()!;
    host.request.mockImplementation(async (...args) => args[0] === "POST" && args[1] === "/api/office/reset" ? late.promise : original(...args));
    const { controller, props, emit, unmount } = mount(); await flush(); controller.showReset(); controller.resetAcknowledged.value = true;
    const clearing = controller.clearOccupancy(); await flush();
    if (kind === "unmount") unmount();
    else { current = available(); if (kind === "file") props.path = "reports/another.xlsx"; else props.conversationId = "conversation-b"; await flush(); }
    late.resolve(claim()); await clearing;
    expect(emit).not.toHaveBeenCalled(); expect(posts()).toHaveLength(2);
    expect(posts()[1]![1]).toBe("/api/office/sessions/new-owned-session/release");
    expect(posts()[1]![2]).toMatchObject({ instanceId: posts()[0]![2].instanceId, bootstrapToken: "unused-claim-proof" });
    expect(posts().some(call => call[1].endsWith("/close"))).toBe(false);
  });
});
