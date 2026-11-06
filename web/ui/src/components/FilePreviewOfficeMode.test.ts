import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, nextTick, reactive, type Ref } from "vue";
import type { OfficeOpen, OfficeSession } from "@/agent/officeTransport";

const host = vi.hoisted(() => ({ configuration: vi.fn(), cleanup: [] as (() => void)[] }));
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), onBeforeUnmount: (fn: () => void) => host.cleanup.push(fn), useSSRContext: () => ({ modules: new Set() }) }));
vi.mock("vue-i18n", async original => { const { ref } = await import("vue"); return { ...await original<typeof import("vue-i18n")>(), useI18n: () => ({ t: (key: string) => key, d: () => "", locale: ref("zh-CN") }) }; });
vi.mock("@/api/client", () => ({ api: {}, authHeaders: () => ({}), fileDownloadURL: (path: string) => `/download/${path}`, fileRawURL: (path: string) => `/raw/${path}` }));
vi.mock("@/agent/officeTransport", () => ({ officeApi: { configuration: host.configuration }, officePathMounted: () => false }));
import FilePreview from "./FilePreview.vue";

type Binding = { path: string; conversationId: string; mode: "view" | "edit"; id?: string };
type Controller = {
  officeBinding: Ref<Binding | null>;
  officeEditing: Ref<boolean>;
  officeEditor: Ref<{ prepareClose(): Promise<boolean>; refreshStatus(): Promise<void> } | null>;
  officeModeChanging: Ref<boolean>;
  officeClaim: Ref<{ claim: OfficeOpen; instanceId: string } | null>;
  officeStatus(value: OfficeSession): void;
  enableOfficeEdit(): Promise<void>;
};
const scopes: ReturnType<typeof effectScope>[] = [];
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done; }); return { promise, resolve }; }
async function flush() { for (let i = 0; i < 20; i++) await Promise.resolve(); await nextTick(); }
async function mount() {
  const props = reactive({ path: "reports/review.docx", sessionId: "mode-test-session" });
  const emit = vi.fn(), scope = effectScope(); scopes.push(scope);
  const start = host.cleanup.length;
  const c = scope.run(() => (FilePreview as unknown as { setup(p: unknown, ctx: unknown): Controller }).setup(props, { emit, expose() {} }))!;
  await flush();
  c.officeStatus({ id: "old-view-session", path: props.path, conversationId: props.sessionId, fileRef: "file-ref", canvasVersion: "saved-version", diskVersion: "saved-version" } as OfficeSession);
  const prepareClose = vi.fn().mockResolvedValue(true);
  c.officeEditor.value = { prepareClose, refreshStatus: vi.fn().mockResolvedValue(undefined) };
  return { c, props, emit, prepareClose, close: () => { for (const fn of host.cleanup.slice(start)) fn(); } };
}
beforeEach(() => {
  host.configuration.mockReset().mockResolvedValue({ enabled: true, configured: true, state: "ready" });
  vi.stubGlobal("window", { setTimeout: vi.fn() });
});
afterEach(() => { for (const fn of host.cleanup.splice(0)) fn(); for (const scope of scopes.splice(0)) scope.stop(); vi.unstubAllGlobals(); });

describe("Office view-to-edit lifecycle", () => {
  it("waits for the old session to close before remounting edit without its retired id", async () => {
    const { c, emit, prepareClose } = await mount();
    const closed = deferred<boolean>(); prepareClose.mockReturnValueOnce(closed.promise);
    const pending = c.enableOfficeEdit();
    expect(prepareClose).toHaveBeenCalledOnce();
    expect(c.officeModeChanging.value).toBe(true);
    expect(c.officeBinding.value).toMatchObject({ mode: "view", id: "old-view-session" });
    closed.resolve(true); await pending;
    expect(c.officeBinding.value).toMatchObject({ mode: "edit", path: "reports/review.docx" });
    expect(c.officeBinding.value?.id).toBeUndefined();
    expect(emit).toHaveBeenCalledWith("officeClosed", "old-view-session");
    expect(c.officeModeChanging.value).toBe(false);
  });

  it("retains a dirty or unconfirmed view when its editor refuses to close", async () => {
    const { c, emit, prepareClose } = await mount(); prepareClose.mockResolvedValueOnce(false);
    await c.enableOfficeEdit();
    expect(c.officeBinding.value).toMatchObject({ mode: "view", id: "old-view-session" });
    expect(emit.mock.calls.filter(call => call[0] === "officeClosed")).toEqual([]);
  });

  it("coalesces repeated edit clicks while the close acknowledgement is in flight", async () => {
    const { c, prepareClose } = await mount(); const closed = deferred<boolean>(); prepareClose.mockReturnValueOnce(closed.promise);
    const first = c.enableOfficeEdit(), second = c.enableOfficeEdit();
    expect(first).toBe(second); expect(prepareClose).toHaveBeenCalledOnce();
    closed.resolve(true); await first;
    expect(c.officeBinding.value?.mode).toBe("edit");
  });

  it("does not adopt a late closed view into a switched conversation", async () => {
    const { c, props, emit, prepareClose } = await mount(); const closed = deferred<boolean>(); prepareClose.mockReturnValueOnce(closed.promise);
    const pending = c.enableOfficeEdit(); props.sessionId = "other-conversation"; await flush(); closed.resolve(true); await pending;
    expect(c.officeBinding.value).toMatchObject({ mode: "view", conversationId: "mode-test-session" });
    expect(emit.mock.calls.filter(call => call[0] === "officeClosed")).toEqual([]);
  });

  it("never remounts a closed claim passed from the view as an initial editor", async () => {
    const { c } = await mount();
    c.officeClaim.value = { claim: { session: { id: "old-view-session" } } as OfficeOpen, instanceId: "old-instance" };
    await c.enableOfficeEdit();
    expect(c.officeClaim.value).toBeNull(); expect(c.officeBinding.value?.id).toBeUndefined();
  });
});
