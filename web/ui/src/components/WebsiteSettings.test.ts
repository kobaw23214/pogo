import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { reactive, type App } from "vue";
import type { WebsitePolicy, WebsiteRestart, WebsiteSettingsResponse, WebsiteSettingsUpdate } from "@/api/webServices";
import { button, click, deferred, find, flat, flush, input, mountComponent, text } from "./websiteComponentTestHarness";
const host = vi.hoisted(() => ({ get: vi.fn(), save: vi.fn() }));
vi.mock("@/api/client", () => ({ api: { websiteSettings: host.get, saveWebsiteSettings: host.save } }));
vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string, values?: unknown) => key + (values ? ":" + JSON.stringify(values) : "") }) }));
vi.mock("@/i18n", () => ({ gt: (key: string) => key }));
import WebsiteSettings from "./WebsiteSettings.vue";
const apps: App[] = [];
const initial = (): WebsitePolicy => ({ default_lease_seconds: 43200, max_per_session: 2, max_per_user: 4, max_total: 16, internal_port: 8080, cpus: 1, memory_mib: 512 });
let policy: WebsitePolicy, restarting: WebsiteRestart[];
const response = (): WebsiteSettingsResponse => ({ state: { settings: { ...policy }, profiles: ["python-web", "node-web"], usage: { total: 1, users: { owner: 1 }, sessions: { chat: 1 } }, restarting: [...restarting] }, available_profiles: ["python-web", "node-web"] });
function mount() { const dirty = vi.fn(), props = reactive({ active: true, onDirty: dirty }); const { app, root, exposed } = mountComponent(WebsiteSettings, "./WebsiteSettings.vue", props); apps.push(app); return { root, dirty, props, discard: () => (exposed() as { discard(): void }).discard() }; }
const field = (root: ReturnType<typeof mount>["root"], label: string) => find(root, n => n.tag === "input" && n.props["aria-label"] === label);
beforeEach(() => {
  vi.useFakeTimers(); vi.setSystemTime(new Date("2026-11-05T12:00:00Z")); vi.stubGlobal("Document", class {}); vi.stubGlobal("ShadowRoot", class {});
  policy = initial(); restarting = [];
  host.get.mockReset().mockImplementation(() => Promise.resolve(response()));
  host.save.mockReset().mockImplementation((next: WebsitePolicy) => { policy = { ...next }; return Promise.resolve({ settings: policy, effects: { immediate: [], restart_required: [] }, restarted: restarting }); });
});
afterEach(() => { for (const app of apps.splice(0)) app.unmount(); vi.useRealTimers(); vi.unstubAllGlobals(); });
describe("platform website settings", () => {
  it("shows the administrator restriction instead of an editable form on a forbidden response", async () => {
    host.get.mockRejectedValueOnce(Object.assign(new Error("forbidden"), { status: 403 })); const { root } = mount(); await flush();
    expect(text(root)).toContain("websiteSettings.adminOnly"); expect(flat(root).some(n => n.tag === "input")).toBe(false); expect(host.save).not.toHaveBeenCalled();
  });
  it("saves the chosen default duration and immediate limit without substituting two hours", async () => {
    const { root, dirty } = mount(); await flush(); expect(text(root)).toContain("Python / Node.js");
    expect(field(root, "websiteSettings.duration").value).toBe(12);
    await input(find(root, n => n.tag === "select"), 86400, true); await input(field(root, "websiteSettings.duration"), 1.5);
    await input(field(root, "websiteSettings.maxSession"), 3); expect(dirty).toHaveBeenCalledWith(true);
    await click(button(root, "websiteSettings.save"));
    expect(host.save).toHaveBeenCalledExactlyOnceWith({ ...initial(), default_lease_seconds: 129600, max_per_session: 3 });
    expect(text(root)).toContain("websiteSettings.saved"); expect(dirty).toHaveBeenLastCalledWith(false);
  });
  it("offers save-and-restart for resource changes and follows the returned progress", async () => {
    const { root } = mount(); await flush(); await input(field(root, "websiteSettings.cpu"), 2);
    host.save.mockImplementationOnce((next: WebsitePolicy) => {
      policy = { ...next }; restarting = [{ service_id: "ws_one", name: "工作台网站", state: "accepted" }];
      return Promise.resolve({ settings: policy, effects: { immediate: [], restart_required: ["cpus"] }, restarted: restarting });
    });
    await click(button(root, "websiteSettings.saveRestart")); expect(host.save.mock.calls[0]![0].cpus).toBe(2);
    expect(text(root)).toContain("websiteSettings.restart_accepted"); expect(text(root)).toContain("工作台网站");
    restarting = [{ service_id: "ws_one", name: "工作台网站", state: "completed" }]; await vi.advanceTimersByTimeAsync(1500); await flush();
    expect(text(root)).toContain("websiteSettings.restart_completed"); expect(text(root)).not.toContain("websiteSettings.restart_accepted");
  });
  it("retains the unsaved draft and dirty state after an asynchronous save failure", async () => {
    const { root, dirty } = mount(); await flush(); await input(field(root, "websiteSettings.maxUser"), 8);
    host.save.mockRejectedValueOnce(new Error("Settings storage is unavailable")); await click(button(root, "websiteSettings.save"));
    expect(text(root)).toContain("Settings storage is unavailable"); expect(field(root, "websiteSettings.maxUser").value).toBe(8);
    expect(dirty).toHaveBeenLastCalledWith(true); expect(button(root, "websiteSettings.save").props.disabled).toBe(false);
  });
  it("freezes the submitted settings and disables further editing while the save is pending", async () => {
    const { root } = mount(); await flush(); await input(field(root, "websiteSettings.memory"), 1024);
    const pending = deferred<WebsiteSettingsUpdate>(); host.save.mockReturnValueOnce(pending.promise); const saving = click(button(root, "websiteSettings.saveRestart")); await flush();
    expect(field(root, "websiteSettings.memory").props.disabled).toBe(true); expect(button(root, "websiteSettings.saveRestart").props.disabled).toBe(true);
    expect(host.save.mock.calls[0]![0]).toEqual({ ...initial(), memory_mib: 1024 });
    pending.resolve({ settings: { ...initial(), memory_mib: 1024 }, effects: { immediate: [], restart_required: ["memory_mib"] }, restarted: [] }); await saving;
    expect(field(root, "websiteSettings.memory").props.disabled).toBe(false);
  });
  it("keeps an unsaved draft while polling restart status", async () => {
    restarting = [{ service_id: "ws_one", name: "Website", state: "running" }]; const { root } = mount(); await flush();
    await input(field(root, "websiteSettings.maxSession"), 5); await vi.advanceTimersByTimeAsync(1500); await flush();
    expect(field(root, "websiteSettings.maxSession").value).toBe(5); expect(host.save).not.toHaveBeenCalled(); expect(button(root, "websiteSettings.save").props.disabled).toBe(false);
  });
  it("discards an unsaved draft through the settings container contract", async () => {
    const { root, dirty, discard } = mount(); await flush(); await input(field(root, "websiteSettings.maxUser"), 8);
    expect(dirty).toHaveBeenLastCalledWith(true); discard(); await flush();
    expect(field(root, "websiteSettings.maxUser").value).toBe(4); expect(dirty).toHaveBeenLastCalledWith(false); expect(button(root, "websiteSettings.save").props.disabled).toBe(true);
    expect(host.save).not.toHaveBeenCalled();
  });
  it("does not let an older status poll erase a newer failed save message", async () => {
    restarting = [{ service_id: "ws_one", name: "Website", state: "running" }]; const { root } = mount(); await flush();
    await input(field(root, "websiteSettings.maxUser"), 8);
    const late = deferred<WebsiteSettingsResponse>(); host.get.mockReturnValueOnce(late.promise); await vi.advanceTimersByTimeAsync(1500);
    host.save.mockRejectedValueOnce(new Error("Save failed: disk full")); await click(button(root, "websiteSettings.save")); expect(text(root)).toContain("Save failed: disk full");
    late.resolve(response()); await flush(); expect(text(root)).toContain("Save failed: disk full"); expect(field(root, "websiteSettings.maxUser").value).toBe(8);
  });

});
