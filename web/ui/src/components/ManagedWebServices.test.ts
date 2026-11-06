import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { App } from "vue";
import type { WebServiceRecord, WebsiteQuota } from "@/api/webServices";
import { button, click, deferred, find, flat, flush, input, mountComponent, submit, text } from "./websiteComponentTestHarness";
const host = vi.hoisted(() => ({ list: vi.fn(), status: vi.fn(), rename: vi.fn(), action: vi.fn(), start: vi.fn(), logs: vi.fn(), remove: vi.fn() }));
vi.mock("@/api/client", () => ({ api: { webServices: host.list, webService: host.status, renameWebService: host.rename, webServiceAction: host.action, startWebService: host.start, webServiceLogs: host.logs, deleteWebService: host.remove } }));
vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string, values?: unknown) => key + (values ? ":" + JSON.stringify(values) : "") }) }));
vi.mock("@/i18n", () => ({ gt: (key: string) => key }));
import ManagedWebServices from "./ManagedWebServices.vue";
const apps: App[] = [];
let current: WebServiceRecord, quota: WebsiteQuota;
function mount() { const opened = vi.fn(), deleted = vi.fn(); const { app, root } = mountComponent(ManagedWebServices, "./ManagedWebServices.vue", { sessionId: "session_one", displaySessionId: "session_one", connected: true, onOpen: opened, onDeleted: deleted }); apps.push(app); return { root, opened, deleted }; }
async function show() { const mounted = mount(); await flush(); await click(find(mounted.root, n => n.props["data-testid"] === "website-access")); return mounted; }
beforeEach(() => {
  vi.useFakeTimers(); vi.setSystemTime(new Date("2026-11-05T12:00:00Z")); vi.stubGlobal("Document", class {}); vi.stubGlobal("ShadowRoot", class {});
  current = { service_id: "ws_one", generation: 1, revision: 1, state: "ready", name: "Counter", slot_name: "counter", lease_id: "lease_one", project_root: "app", profile: "node-web", lease_expires_at: "2026-11-06T12:00:00Z" };
  quota = { session: { current: 1, limit: 2 }, user: { current: 1, limit: 4 }, total: { current: 1, limit: 16 } };
  host.list.mockReset().mockImplementation(() => Promise.resolve({ services: [{ ...current }], quota, default_lease_seconds: 21600 }));
  host.status.mockReset().mockImplementation(() => Promise.resolve({ ...current })); host.start.mockReset(); host.logs.mockReset();
  host.rename.mockReset().mockImplementation((_sid, _id, displayName) => { current = { ...current, name: displayName, revision: current.revision + 1 }; return Promise.resolve({ ...current, operation_status: "completed" }); });
  host.action.mockReset().mockImplementation((_sid, _id, action, _operation, _signal, seconds) => { current = { ...current, revision: current.revision + 1, ...(action === "renew" ? { lease_expires_at: new Date(Date.now() + seconds * 1000).toISOString() } : {}) }; return Promise.resolve({ ...current, operation_status: "completed" }); });
  host.remove.mockReset().mockImplementation(() => { host.list.mockResolvedValue({ services: [], quota, default_lease_seconds: 21600 }); return Promise.resolve(); });
});
afterEach(() => { for (const app of apps.splice(0)) app.unmount(); vi.useRealTimers(); vi.unstubAllGlobals(); });
describe("conversation website controls", () => {
  it("keeps a ready website accessible while a storage check is being retried", async () => {
    current = { ...current, storage_warning: { code: "STORAGE_CHECK_UNAVAILABLE", message: "managed storage usage could not be verified", retryable: true } };
    const { root, opened } = await show();
    const warning = find(root, n => n.props["data-testid"] === "website-storage-warning");
    expect(warning.props.role).toBe("status"); expect(text(warning)).toBe("webServices.storageCheckRetrying");
    expect(text(root)).toContain("webServices.state_ready"); expect(text(root)).not.toContain("webServices.state_failed");
    expect(text(root)).not.toContain("managed storage usage could not be verified");
    expect(button(root, "webServices.open").props.disabled).toBe(false);
    await click(button(root, "webServices.open"));
    expect(opened).toHaveBeenCalledExactlyOnceWith({ sessionId: "session_one", serviceId: "ws_one", label: "Counter", open: true });
    expect(host.action).not.toHaveBeenCalled(); expect(host.start).not.toHaveBeenCalled();
  });
  it("clears a recovered storage warning on status refresh without restarting the website", async () => {
    current = { ...current, storage_warning: { code: "STORAGE_CHECK_UNAVAILABLE", message: "temporary" } };
    const { root } = await show();
    expect(flat(root).some(n => n.props["data-testid"] === "website-storage-warning")).toBe(true);
    current = { ...current, storage_warning: undefined, revision: current.revision + 1 };
    await click(button(root, "webServices.refresh"));
    expect(flat(root).some(n => n.props["data-testid"] === "website-storage-warning")).toBe(false);
    expect(button(root, "webServices.open").props.disabled).toBe(false);
    expect(host.action).not.toHaveBeenCalled(); expect(host.start).not.toHaveBeenCalled();
  });
  it("asks for inline confirmation before deleting and cancels without a request", async () => {
    const { root } = await show(); await click(button(root, "webServices.delete"));
    expect(text(root)).toContain('webServices.deleteTitle:{"name":"Counter"}'); expect(text(root)).toContain("webServices.deleteHint"); expect(host.remove).not.toHaveBeenCalled();
    await click(button(root, "websiteSettings.cancel")); expect(flat(root).some(n => n.props["data-testid"] === "website-delete-confirmation")).toBe(false); expect(host.remove).not.toHaveBeenCalled();
  });
  it("deletes the confirmed service, removes its card and reports its scoped identity", async () => {
    const { root, deleted } = await show(); await click(button(root, "webServices.delete")); await submit(find(root, n => n.props["data-testid"] === "website-delete-confirmation"));
    expect(host.remove).toHaveBeenCalledExactlyOnceWith("session_one", "ws_one", expect.any(AbortSignal));
    expect(flat(root).some(n => n.props["data-service-id"] === "ws_one")).toBe(false); expect(deleted).toHaveBeenCalledExactlyOnceWith({ sessionId: "session_one", serviceId: "ws_one" }); expect(host.action).not.toHaveBeenCalled();
  });
  it("disables repeated confirmations and competing controls while deletion is running", async () => {
    const pending = deferred<void>(); host.remove.mockReturnValueOnce(pending.promise); const { root } = await show(); await click(button(root, "webServices.delete"));
    const form = find(root, n => n.props["data-testid"] === "website-delete-confirmation"); const removing = submit(form); await flush();
    expect(find(root, n => n.props["data-testid"] === "website-delete-confirm").props.disabled).toBe(true);
    expect(button(root, "webServices.open").props.disabled).toBe(true); expect(button(root, "webServices.renew").props.disabled).toBe(true); expect(button(root, "webServices.delete").props.disabled).toBe(true);
    pending.resolve(); await removing; expect(host.remove).toHaveBeenCalledOnce();
  });
  it("keeps an uncertain deletion visible with retry and prevents restarting it", async () => {
    const { root, deleted } = await show(); host.remove.mockRejectedValueOnce(new Error("lost")); await click(button(root, "webServices.delete"));
    await submit(find(root, n => n.props["data-testid"] === "website-delete-confirmation"));
    expect(text(root)).toContain("webServices.deleteUnknown"); expect(button(root, "webServices.restart").props.disabled).toBe(true); expect(deleted).not.toHaveBeenCalled();
    expect(button(root, "webServices.closeDelete").props.disabled).toBe(false);
    await submit(find(root, n => n.props["data-testid"] === "website-delete-confirmation")); expect(host.remove).toHaveBeenCalledTimes(2); expect(deleted).toHaveBeenCalledOnce();
  });
  it.each(["stopped", "expired", "failed"] as const)("allows deletion of a %s website", async state => {
    current = { ...current, state }; const { root } = await show(); expect(button(root, "webServices.delete").props.disabled).toBe(false);
    await click(button(root, "webServices.delete")); await submit(find(root, n => n.props["data-testid"] === "website-delete-confirmation")); expect(host.remove).toHaveBeenCalledOnce();
  });
  it("shows a durable deletion in progress and permits its same-service cleanup retry", async () => {
    current = { ...current, state: "stopping", phase: "deleting" }; const { root } = await show();
    expect(text(find(root, n => n.props.role === "status" && text(n) === "webServices.deleting"))).toBe("webServices.deleting");
    expect(button(root, "webServices.open").props.disabled).toBe(true); expect(button(root, "webServices.restart").props.disabled).toBe(true); expect(button(root, "webServices.delete").props.disabled).toBe(false);
    await click(button(root, "webServices.delete")); await submit(find(root, n => n.props["data-testid"] === "website-delete-confirmation")); expect(host.remove).toHaveBeenCalledOnce();
  });
  it("renames a website with Unicode text inside the conversation popover and keeps its identity", async () => {
    const { root, opened } = await show(), deadline = current.lease_expires_at; await click(button(root, "websiteSettings.rename"));
    const name = find(root, n => n.tag === "input" && n.props.id === "website-name-ws_one"); await input(name, "  海上日落计划 🌅  ");
    await submit(find(root, n => n.tag === "form"));
    expect(host.rename).toHaveBeenCalledExactlyOnceWith("session_one", "ws_one", "海上日落计划 🌅", expect.any(String), expect.any(AbortSignal));
    expect(text(root)).toContain("海上日落计划 🌅"); expect(current.slot_name).toBe("counter"); expect(current.lease_id).toBe("lease_one"); expect(current.lease_expires_at).toBe(deadline);
    expect(flat(root).some(n => n.tag === "form")).toBe(false); await click(button(root, "webServices.open"));
    expect(opened).toHaveBeenCalledWith({ sessionId: "session_one", serviceId: "ws_one", label: "海上日落计划 🌅", open: true }); expect(host.action).not.toHaveBeenCalled();
  });
  it("keeps the rename editor and draft visible when saving fails", async () => {
    const { root } = await show(); await click(button(root, "websiteSettings.rename"));
    await input(find(root, n => n.tag === "input" && n.props.id === "website-name-ws_one"), "新网站名称");
    host.rename.mockRejectedValueOnce(Object.assign(new Error("Name could not be saved"), { status: 400 })); await submit(find(root, n => n.tag === "form"));
    expect(text(root)).toContain("Name could not be saved"); expect(find(root, n => n.tag === "input" && n.props.id === "website-name-ws_one").value).toBe("新网站名称");
    expect(button(root, "websiteSettings.saveName").props.disabled).toBe(false);
  });
  it("starts renewal from the platform default and submits the selected multi-day duration", async () => {
    const { root } = await show(); await click(button(root, "webServices.renew"));
    const amount = () => find(root, n => n.tag === "input" && n.props["aria-label"] === "websiteSettings.duration"); expect(amount().value).toBe(6);
    await input(find(root, n => n.tag === "select"), 86400, true); await input(amount(), 3); await submit(find(root, n => n.tag === "form"));
    expect(host.action).toHaveBeenCalledExactlyOnceWith("session_one", "ws_one", "renew", expect.any(String), expect.any(AbortSignal), 259200);
    expect(current.lease_expires_at).toBe("2026-11-08T12:00:00.000Z"); expect(current.lease_id).toBe("lease_one"); expect(flat(root).some(n => n.tag === "form")).toBe(false);
  });
  it.each(["session", "user", "total"] as const)("shows an actionable %s capacity reminder", async scope => {
    quota[scope] = { current: 4, limit: 4 }; const { root } = await show();
    const notice = find(root, n => String(n.props.class || "").includes("website-quota-notice"));
    expect(text(notice)).toBe(`websiteSettings.quota_${scope}:` + JSON.stringify({ current: 4, limit: 4 })); expect(notice.props.role).toBe("status");
    expect(host.start).not.toHaveBeenCalled();
  });
});
