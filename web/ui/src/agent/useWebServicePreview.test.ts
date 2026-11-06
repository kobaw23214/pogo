import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, nextTick, reactive } from "vue";
import type { WebServiceGrant, WebServiceRecord } from "@/api/webServices";
const host = vi.hoisted(() => ({ status: vi.fn(), open: vi.fn(), revoke: vi.fn() }));
vi.mock("@/api/client", () => ({ api: { webService: host.status, openWebService: host.open, revokeWebService: host.revoke } }));
vi.mock("@/i18n", () => ({ gt: (key: string) => key }));
import { useWebServicePreview } from "./useWebServicePreview";
const scopes: ReturnType<typeof effectScope>[] = [];
const record = (id = "ws_first", generation = 1): WebServiceRecord => ({ service_id: id, generation, revision: generation, state: "ready", name: id, slot_name: id, lease_id: `lease_${id}`, profile: "python-web", project_root: "app", lease_expires_at: new Date(Date.now() + 7200000).toISOString() });
const grant = (id = "grant_one", service = "ws_first", generation = 1): WebServiceGrant => ({ service_id: service, generation, grant_id: id, preview_url: `http://${"a".repeat(48)}.preview.localhost:12345/`, grant_expires_at: record().lease_expires_at, lease_expires_at: record().lease_expires_at, browser_url: null, limitations: [] });
const pathGrant = (): WebServiceGrant => ({ ...grant(), preview_url: `http://192.168.1.10:19770/_pogo/web/${"a".repeat(48)}/` });
function deferred<T>() { let resolve!: (value: T) => void, reject!: (reason: unknown) => void; const promise = new Promise<T>((r, j) => { resolve = r; reject = j; }); return { promise, resolve, reject }; }
async function flush() { for (let i = 0; i < 16; i++) await Promise.resolve(); await nextTick(); }
function mount(openRequest?: number) {
  const scope = effectScope(); scopes.push(scope);
  const context = reactive({ sessionId: "session_one", serviceId: "ws_first", active: true, openRequest });
  const p = scope.run(() => useWebServicePreview(() => context))!;
  return { p, context, scope };
}
beforeEach(() => {
  vi.useFakeTimers(); vi.setSystemTime(new Date("2026-11-05T12:00:00Z"));
  const initial = record();
  host.status.mockReset().mockImplementation((_sid, id) => Promise.resolve({ ...initial, service_id: id, name: id, slot_name: id, lease_id: `lease_${id}` }));
  host.open.mockReset().mockResolvedValue(grant()); host.revoke.mockReset().mockResolvedValue({});
});
afterEach(async () => { for (const scope of scopes.splice(0)) scope.stop(); await flush(); vi.useRealTimers(); vi.unstubAllGlobals(); });
describe("managed website lifecycle views", () => {
  it("opens the canonical website from an explicitly trusted localhost workbench", async () => {
    vi.stubGlobal("location", { origin: "http://localhost:19770" });
    host.open.mockResolvedValue({ ...pathGrant(), canonical_origin: "http://192.168.1.10:19770", allowed_parent_origins: ["http://192.168.1.10:19770", "http://localhost:19770"] });
    const { p } = mount(); await flush(); await p.open();
    expect(p.issue.value).toBe(""); expect(p.grant.value?.preview_url).toBe(pathGrant().preview_url);
    expect(host.revoke).not.toHaveBeenCalled();
  });
  it("confirms a restart that completed after the initial status read", async () => {
    host.open.mockImplementationOnce(async () => { host.status.mockResolvedValue(record("ws_first", 2)); return grant("grant_one", "ws_first", 2); });
    const { p } = mount(); await flush(); await p.open();
    expect(p.issue.value).toBe(""); expect(p.record.value?.generation).toBe(2); expect(p.grant.value?.generation).toBe(2);
  });
  it("reports an unconfirmed newer generation as a transition and never installs it", async () => {
    host.open.mockResolvedValue(grant("grant_one", "ws_first", 2));
    const { p } = mount(); await flush(); await p.open();
    expect(p.issue.value).toBe("webServices.previewChanged"); expect(p.grant.value).toBeNull();
  });
  it("opens a same-origin IP website explicitly and closes only the local view", async () => {
    vi.stubGlobal("location", { origin: "http://192.168.1.10:19770" }); host.open.mockResolvedValue(pathGrant());
    const { p } = mount(); await flush(); expect(host.open).not.toHaveBeenCalled();
    await p.open(); expect(p.grant.value?.preview_url).toBe(pathGrant().preview_url);
    expect(await p.prepareClose()).toBe(true); expect(p.grant.value).toBeNull();
    expect(host.revoke).not.toHaveBeenCalled();
  });
  it("keeps the website open past an obsolete shorter grant deadline until the website lease ends", async () => {
    vi.stubGlobal("location", { origin: "http://192.168.1.10:19770" });
    host.open.mockResolvedValue({ ...pathGrant(), grant_expires_at: new Date(Date.now() + 60000).toISOString() });
    const { p } = mount(); await flush(); await p.open(); await vi.advanceTimersByTimeAsync(61000);
    expect(p.grant.value?.preview_url).toBe(pathGrant().preview_url); expect(host.open).toHaveBeenCalledOnce();
    expect(host.revoke).not.toHaveBeenCalled();
  });
  it("rejects a returned workbench API URL without revoking the shared website address", async () => {
    vi.stubGlobal("location", { origin: "http://192.168.1.10:19770" });
    host.open.mockResolvedValue({ ...pathGrant(), preview_url: "http://192.168.1.10:19770/api/sessions" });
    const { p } = mount(); await flush(); await p.open(); await flush();
    expect(p.grant.value).toBeNull(); expect(p.issue.value).toBe("webServices.invalidResponse"); expect(host.revoke).not.toHaveBeenCalled();
  });
  it("restores only the service reference until an explicit open request arrives", async () => {
    const { p } = mount(); await flush();
    expect(p.record.value?.service_id).toBe("ws_first"); expect(host.open).not.toHaveBeenCalled(); expect(p.grant.value).toBeNull();
    await p.open(); expect(host.open).toHaveBeenCalledOnce(); expect(p.grant.value?.grant_id).toBe("grant_one");
  });
  it("honors an explicit open click and preserves the shared address when closed", async () => {
    const { p } = mount(1); await flush();
    expect(host.open).toHaveBeenCalledOnce(); expect(p.grant.value).not.toBeNull();
    expect(await p.prepareClose()).toBe(true); expect(p.grant.value).toBeNull();
    expect(host.revoke).not.toHaveBeenCalled();
  });
  it("can close while offline because closing never requires a remote revocation", async () => {
    const { p } = mount(); await flush(); await p.open(); host.revoke.mockRejectedValue(new Error("offline"));
    expect(await p.prepareClose()).toBe(true); expect(p.closeIssue.value).toBe(""); expect(p.grant.value).toBeNull();
    expect(host.revoke).not.toHaveBeenCalled();
  });
  it("discards a late open response after unmount without revoking its website", async () => {
    const late = deferred<WebServiceGrant>(); host.open.mockReturnValueOnce(late.promise);
    const { p, scope } = mount(); await flush(); const opening = p.open(); await flush();
    scope.stop(); late.resolve(grant()); await opening; await flush();
    expect(p.grant.value).toBeNull(); expect(host.revoke).not.toHaveBeenCalled();
  });
  it("discards a late old-session receipt while the new session is displayed", async () => {
    const late = deferred<WebServiceGrant>();
    host.open.mockReturnValueOnce(late.promise).mockResolvedValueOnce(grant("grant_two", "ws_second"));
    const { p, context } = mount(); await flush(); const first = p.open(); await flush();
    context.sessionId = "session_two"; context.serviceId = "ws_second"; await flush();
    await p.open(); expect(p.grant.value?.grant_id).toBe("grant_two");
    late.resolve(grant()); await first; await flush();
    expect(p.grant.value?.service_id).toBe("ws_second"); expect(p.grant.value?.grant_id).toBe("grant_two");
    expect(host.revoke).not.toHaveBeenCalled();
  });
  it("does not install an old-generation receipt after a newer status has landed", async () => {
    const late = deferred<WebServiceGrant>(); host.open.mockReturnValueOnce(late.promise);
    const { p } = mount(); await flush(); const opening = p.open(); await flush();
    host.status.mockResolvedValue(record("ws_first", 2)); await p.refresh();
    late.resolve(grant()); await opening; await flush();
    expect(p.grant.value).toBeNull(); expect(host.revoke).not.toHaveBeenCalled(); expect(p.issue.value).toBe("webServices.previewChanged");
  });
  it("ends the local view when the website lease expires without reopening or renewing", async () => {
    const ending = { ...record(), lease_expires_at: new Date(Date.now() + 60000).toISOString() };
    host.status.mockResolvedValue(ending); host.open.mockResolvedValue({ ...grant(), grant_expires_at: ending.lease_expires_at, lease_expires_at: ending.lease_expires_at });
    const { p } = mount(); await flush(); await p.open(); await vi.advanceTimersByTimeAsync(61000);
    expect(p.grant.value).toBeNull(); expect(p.issue.value).toBe("webServices.previewExpired");
    expect(host.open).toHaveBeenCalledOnce(); expect(host.revoke).not.toHaveBeenCalled();
  });
  it("reuses an uncertain open operation, and uses a fresh one after GRANT_EXPIRED", async () => {
    const { p } = mount(); await flush();
    host.open.mockRejectedValueOnce(new Error("lost response")); await p.open();
    host.open.mockRejectedValueOnce(Object.assign(new Error("expired"), { code: "GRANT_EXPIRED" })); await p.open(); await p.open();
    expect(host.open.mock.calls[0]![2]).toBe(host.open.mock.calls[1]![2]);
    expect(host.open.mock.calls[2]![2]).not.toBe(host.open.mock.calls[1]![2]);
  });
  it("rejects an executable URL without remotely mutating the website", async () => {
    host.open.mockResolvedValue({ ...grant(), preview_url: "javascript:alert(1)" });
    const { p } = mount(); await flush(); await p.open(); await flush();
    expect(p.grant.value).toBeNull(); expect(p.issue.value).toBe("webServices.invalidResponse"); expect(host.revoke).not.toHaveBeenCalled();
  });
  it("clears the local page on pagehide and restores only its reference on pageshow", async () => {
    const events = new EventTarget(); vi.stubGlobal("window", events);
    const { p } = mount(); await flush(); await p.open();
    events.dispatchEvent(new Event("pagehide")); await flush(); expect(p.grant.value).toBeNull();
    events.dispatchEvent(new Event("pageshow")); await flush(); expect(host.open).toHaveBeenCalledOnce();
    expect(p.record.value?.service_id).toBe("ws_first"); expect(host.revoke).not.toHaveBeenCalled();
  });
  it("discards a late pre-pagehide receipt even when pageshow already occurred", async () => {
    const events = new EventTarget(); vi.stubGlobal("window", events);
    const late = deferred<WebServiceGrant>(); host.open.mockReturnValueOnce(late.promise);
    const { p } = mount(); await flush(); const opening = p.open(); await flush();
    events.dispatchEvent(new Event("pagehide")); events.dispatchEvent(new Event("pageshow"));
    late.resolve(grant()); await opening; await flush();
    expect(p.grant.value).toBeNull(); expect(host.revoke).not.toHaveBeenCalled(); expect(host.open).toHaveBeenCalledOnce();
  });
  it("does not let late receipt A replace newer displayed receipt B after pageshow", async () => {
    const events = new EventTarget(); vi.stubGlobal("window", events);
    const lateA = deferred<WebServiceGrant>(); host.open.mockReturnValueOnce(lateA.promise).mockResolvedValueOnce(grant("grant_B"));
    const { p } = mount(); await flush(); const openingA = p.open(); await flush();
    events.dispatchEvent(new Event("pagehide")); events.dispatchEvent(new Event("pageshow")); await flush();
    await p.open(); expect(p.grant.value?.grant_id).toBe("grant_B"); lateA.resolve(grant("grant_A")); await openingA; await flush();
    expect(p.grant.value?.grant_id).toBe("grant_B"); expect(host.revoke).not.toHaveBeenCalled();
  });
  it("preserves a shared URL returned by both stale and current open requests", async () => {
    const events = new EventTarget(); vi.stubGlobal("window", events);
    const lateA = deferred<WebServiceGrant>(); host.open.mockReturnValueOnce(lateA.promise).mockResolvedValueOnce(grant("grant_shared"));
    const { p } = mount(); await flush(); const openingA = p.open(); await flush();
    events.dispatchEvent(new Event("pagehide")); events.dispatchEvent(new Event("pageshow")); await flush();
    await p.open(); lateA.resolve(grant("grant_shared")); await openingA; await flush();
    expect(p.grant.value?.grant_id).toBe("grant_shared"); expect(host.revoke).not.toHaveBeenCalled();
    expect(await p.prepareClose()).toBe(true); expect(host.revoke).not.toHaveBeenCalled();
  });
  it("keeps the original operation through a running receipt and explicit retries", async () => {
    host.open.mockRejectedValueOnce(new Error("lost first response")).mockImplementationOnce((_sid, _service, operationId) => Promise.resolve({ ...record(), operation_id: operationId, operation_status: "running", accepted: true }));
    const { p } = mount(); await flush(); await p.open(); const original = host.open.mock.calls[0]![2];
    await p.open(); expect(p.grant.value).toBeNull(); expect(p.issue.value).toBe("webServices.previewPending"); await p.open();
    expect(host.open.mock.calls.map(call => call[2])).toEqual([original, original, original]); expect(p.grant.value?.grant_id).toBe("grant_one"); expect(host.revoke).not.toHaveBeenCalled();
  });
  it.each(["other-operation", "other-generation"])("rejects a pending receipt bound to %s", async mismatch => {
    host.open.mockImplementationOnce((_sid, _service, operationId) => Promise.resolve({ ...record(), operation_id: mismatch === "other-operation" ? "another-request" : operationId,
      generation: mismatch === "other-generation" ? 2 : 1, operation_status: "accepted", accepted: true }));
    const { p } = mount(); await flush(); await p.open();
    expect(p.issue.value).toBe("webServices.invalidResponse"); expect(p.grant.value).toBeNull(); await p.open();
    expect(host.open.mock.calls[0]![2]).not.toBe(host.open.mock.calls[1]![2]);
  });
  it("changes the view reload version only after a successful explicit open", async () => {
    const { p } = mount(); await flush(); expect(p.viewVersion.value).toBe(0);
    await p.open(); expect(p.viewVersion.value).toBe(1); const address = p.grant.value?.preview_url;
    await p.refresh(); expect(p.viewVersion.value).toBe(1);
    await p.open(); expect(p.viewVersion.value).toBe(2); expect(p.grant.value?.preview_url).toBe(address);
    host.open.mockRejectedValueOnce(new Error("offline")); await p.open(); expect(p.viewVersion.value).toBe(2);
  });
  it("keeps the same URL and synchronizes its deadline on renewal", async () => {
    const { p } = mount(); await flush(); await p.open(); const address = p.grant.value!.preview_url;
    const renewed = { ...record(), revision: 2, lease_expires_at: new Date(Date.now() + 86400000).toISOString() };
    host.status.mockResolvedValue(renewed); await p.refresh();
    expect(p.grant.value?.preview_url).toBe(address); expect(p.grant.value?.lease_expires_at).toBe(renewed.lease_expires_at);
    expect(p.grant.value?.grant_expires_at).toBe(renewed.lease_expires_at); expect(host.open).toHaveBeenCalledOnce();
  });
  it("retains the address during a resource restart and uses the recovered generation", async () => {
    const { p } = mount(); await flush(); await p.open(); const original = p.grant.value!;
    host.status.mockResolvedValue({ ...record(), revision: 2, state: "stopping" }); await p.refresh();
    expect(p.grant.value?.preview_url).toBe(original.preview_url);
    host.status.mockResolvedValue({ ...record("ws_first", 2), state: "starting" }); await p.refresh();
    expect(p.grant.value?.preview_url).toBe(original.preview_url); expect(p.grant.value?.generation).toBe(2);
    host.status.mockResolvedValue({ ...record("ws_first", 2), revision: 3 }); await p.refresh();
    expect(p.grant.value?.preview_url).toBe(original.preview_url); expect(p.issue.value).toBe(""); expect(host.open).toHaveBeenCalledOnce();
  });
  it("invalidates the displayed address when the same slot starts a new lease", async () => {
    const { p } = mount(); await flush(); await p.open();
    host.status.mockResolvedValue({ ...record("ws_first", 2), lease_id: "lease_replacement" }); await p.refresh();
    expect(p.grant.value).toBeNull(); expect(p.issue.value).toBe("webServices.previewUnavailable"); expect(host.open).toHaveBeenCalledOnce(); expect(host.revoke).not.toHaveBeenCalled();
  });
  it("retires a deleted website and stops automatic polling after an authoritative 404", async () => {
    const { p } = mount(); await flush(); await p.open();
    host.status.mockRejectedValue(Object.assign(new Error("gone"), { status: 404, code: "SERVICE_NOT_FOUND" }));
    await p.refresh(); const calls = host.status.mock.calls.length;
    expect(p.grant.value).toBeNull(); expect(p.record.value).toBeNull(); expect(p.stale.value).toBe(false);
    expect(p.issue.value).toBe("webServices.notFound");
    await vi.advanceTimersByTimeAsync(60000); expect(host.status).toHaveBeenCalledTimes(calls);
    await p.open(); expect(host.open).toHaveBeenCalledOnce(); expect(host.revoke).not.toHaveBeenCalled();
  });
  it("keeps an existing view during a transient status failure", async () => {
    const { p } = mount(); await flush(); await p.open(); const original = p.grant.value;
    host.status.mockRejectedValueOnce(Object.assign(new Error("temporarily unavailable"), { status: 503 }));
    await p.refresh();
    expect(p.grant.value).toEqual(original); expect(p.record.value).not.toBeNull(); expect(p.stale.value).toBe(true);
  });
  it.each(["deleting", "missing"] as const)("ignores an open receipt that arrives after the website is %s", async state => {
    const late = deferred<WebServiceGrant>(); host.open.mockReturnValueOnce(late.promise);
    const { p } = mount(); await flush(); const opening = p.open(); await flush();
    expect(p.loading.value).toBe(true);
    if (state === "deleting") host.status.mockResolvedValue({ ...record(), revision: 2, state: "stopping", phase: "deleting" });
    else host.status.mockRejectedValue(Object.assign(new Error("gone"), { status: 404 }));
    await p.refresh(); late.resolve(grant()); await opening;
    expect(p.grant.value).toBeNull(); expect(p.loading.value).toBe(false); expect(p.stale.value).toBe(false);
    expect(p.issue.value).toBe(state === "deleting" ? "webServices.deleting" : "webServices.notFound");
  });
  it("does not let an obsolete missing response retire a newer confirmed view", async () => {
    const { p } = mount(); await flush(); await p.open();
    const late = deferred<WebServiceRecord>(); host.status.mockReturnValueOnce(late.promise);
    const earlier = p.refresh(); host.status.mockResolvedValueOnce({ ...record(), revision: 3 }); await p.refresh();
    late.reject(Object.assign(new Error("old missing reply"), { status: 404 })); await earlier;
    expect(p.record.value?.revision).toBe(3); expect(p.grant.value).not.toBeNull(); expect(p.issue.value).toBe("");
  });
  it("resumes normal status polling for a different website after the previous one was deleted", async () => {
    const { p, context } = mount(); await flush();
    host.status.mockRejectedValueOnce(Object.assign(new Error("gone"), { status: 404 })); await p.refresh();
    context.serviceId = "ws_second"; await flush(); const calls = host.status.mock.calls.length;
    expect(p.record.value?.service_id).toBe("ws_second"); expect(p.issue.value).toBe("");
    await vi.advanceTimersByTimeAsync(12000); expect(host.status.mock.calls.length).toBeGreaterThan(calls);
  });
  it.each(["stopped", "expired", "failed", "interrupted"] as const)("removes the view for terminal state %s", async state => {
    const { p } = mount(); await flush(); await p.open();
    host.status.mockResolvedValue({ ...record(), revision: 2, state }); await p.refresh();
    expect(p.grant.value).toBeNull(); expect(host.revoke).not.toHaveBeenCalled(); expect(host.open).toHaveBeenCalledOnce();
  });
  it("does not let an older failed status request overwrite a newer successful observation", async () => {
    const { p } = mount(); await flush(); const earlier = deferred<WebServiceRecord>(); host.status.mockReturnValueOnce(earlier.promise);
    const oldRefresh = p.refresh(); const newest = { ...record(), revision: 3, name: "Updated website" };
    host.status.mockResolvedValueOnce(newest); await p.refresh(); earlier.reject(new Error("obsolete network error")); await oldRefresh;
    expect(p.record.value?.name).toBe("Updated website"); expect(p.stale.value).toBe(false); expect(p.issue.value).toBe("");
  });
  it("does not install an older status response after a newer successful observation", async () => {
    const { p } = mount(); await flush(); const earlier = deferred<WebServiceRecord>(); host.status.mockReturnValueOnce(earlier.promise);
    const oldRefresh = p.refresh(); host.status.mockResolvedValueOnce({ ...record(), revision: 3, name: "Newest" }); await p.refresh();
    earlier.resolve({ ...record(), revision: 2, name: "Old" }); await oldRefresh; expect(p.record.value?.name).toBe("Newest");
  });
  it("uses the current lease deadline if renewal completes while open is in flight", async () => {
    const late = deferred<WebServiceGrant>(); host.open.mockReturnValueOnce(late.promise); const original = grant();
    const { p } = mount(); await flush(); const opening = p.open(); await flush();
    const renewed = { ...record(), revision: 2, lease_expires_at: new Date(Date.now() + 86400000).toISOString() };
    host.status.mockResolvedValue(renewed); await p.refresh(); late.resolve(original); await opening;
    expect(p.grant.value?.preview_url).toBe(original.preview_url); expect(p.grant.value?.lease_expires_at).toBe(renewed.lease_expires_at); expect(p.grant.value?.grant_expires_at).toBe(renewed.lease_expires_at);
  });
  it("reports service expiration when the deadline passes before a terminal status arrives", async () => {
    const ending = { ...record(), lease_expires_at: new Date(Date.now() + 60000).toISOString() };
    host.status.mockResolvedValue(ending); host.open.mockResolvedValue({ ...grant(), lease_expires_at: ending.lease_expires_at, grant_expires_at: ending.lease_expires_at });
    const { p } = mount(); await flush(); await p.open(); vi.setSystemTime(Date.now() + 61000); await p.refresh();
    expect(p.grant.value).toBeNull(); expect(p.issue.value).toBe("webServices.previewExpired");
  });
});
