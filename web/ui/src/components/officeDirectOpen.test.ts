import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, nextTick, reactive, type Ref } from "vue";

const host = vi.hoisted(() => ({ configuration: vi.fn(), fileContent: vi.fn(), fileRender: vi.fn(), cleanups: [] as (() => void)[] }));
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), onBeforeUnmount: (fn: () => void) => host.cleanups.push(fn), useSSRContext: () => ({ modules: new Set() }) }));
vi.mock("vue-i18n", async original => { const { ref } = await import("vue"); return { ...await original<typeof import("vue-i18n")>(), useI18n: () => ({ t: (key: string) => key, d: () => "", locale: ref("zh-CN") }) }; });
vi.mock("@/api/client", () => ({ api: { fileContent: host.fileContent, fileRender: host.fileRender }, authHeaders: () => ({}), fileDownloadURL: (path: string) => `/download/${path}`, fileRawURL: (path: string) => `/raw/${path}` }));
vi.mock("@/agent/officeTransport", () => ({ officeApi: { configuration: host.configuration }, officePathMounted: () => false }));
vi.mock("./CodeBlock.vue", () => ({ default: {} }));
vi.mock("./MarkdownView.vue", () => ({ default: {} }));
vi.mock("./MermaidDiagram.vue", () => ({ default: {} }));
import FilePreview from "./FilePreview.vue";

type Controller = { officeEditing: Ref<boolean>; officeBinding: Ref<{ path: string; conversationId: string; mode: "view" | "edit" } | null>; readOnlyOffice: Ref<boolean>; renderError: Ref<string>; failed: Ref<string>; officeEditor: Ref<unknown>; officeChecking: Ref<boolean>; officeCheckElapsed: Ref<number>; officeCheckAttempt: Ref<number>; editSavedOffice(): void; prepareClose(): Promise<boolean>; refreshPreview(): Promise<boolean>; load(): Promise<void>; officeOpenFailed(message: string): void; officeFocusExisting(path: string): void };
const scopes: ReturnType<typeof effectScope>[] = [];
async function flush() { for (let i = 0; i < 8; i++) await Promise.resolve(); await nextTick(); }
function mount(path: string) {
  const scope = effectScope(); scopes.push(scope);
  const props = reactive({ path, sessionId: "conversation-a", citation: false, backClose: true });
  const emit = vi.fn();
  const controller = scope.run(() => (FilePreview as unknown as { setup(props: unknown, ctx: unknown): Controller }).setup(props, { emit, expose() {} }))!;
  return { props, emit, controller };
}
beforeEach(() => { host.configuration.mockReset().mockResolvedValue({ enabled: true }); host.fileContent.mockReset().mockResolvedValue({ content: "text", binary: false }); host.fileRender.mockReset(); });
afterEach(() => { for (const cleanup of host.cleanups.splice(0)) cleanup(); for (const scope of scopes.splice(0)) scope.stop(); vi.useRealTimers(); vi.unstubAllGlobals(); });

describe("Workspace Office opens directly and preserves its only editor", () => {
  it("opens docx and xlsx directly without fetching the old preview or file bytes", async () => {
    for (const path of ["uploads/plan.docx", "uploads/data.xlsx"]) {
      const { controller, emit } = mount(path); await flush();
      expect(controller.officeEditing.value).toBe(true);
      expect(controller.officeBinding.value?.path).toBe(path);
      expect(emit).toHaveBeenCalledWith("officeOpened", { path, conversationId: "conversation-a" });
    }
    expect(host.fileContent).not.toHaveBeenCalled(); expect(host.fileRender).not.toHaveBeenCalled();
  });
  it("uses view mode for quick previews and waits for its close acknowledgement before entering edit", async () => {
    const { controller } = mount("uploads/view-only.docx"); await flush();
    expect(controller.officeBinding.value?.mode).toBe("view");
    let acknowledgeClose!: (closed: boolean) => void;
    const closeAcknowledgement = new Promise<boolean>(resolve => { acknowledgeClose = resolve; });
    const prepareClose = vi.fn(() => closeAcknowledgement);
    controller.officeEditor.value = { prepareClose, refreshStatus: vi.fn() };
    controller.editSavedOffice();
    await flush();
    expect(prepareClose).toHaveBeenCalledOnce();
    expect(controller.officeBinding.value?.mode).toBe("view");
    acknowledgeClose(true); await flush();
    expect(controller.officeBinding.value?.mode).toBe("edit");
  });
  it("shows an unavailable error instead of falling back to a stale read-only document", async () => {
    host.configuration.mockResolvedValue({ enabled: false });
    const { controller } = mount("uploads/unavailable.docx"); await flush();
    expect(controller.officeEditing.value).toBe(false);
    expect(controller.failed.value).toBe("officeEditor.notConfigured");
    expect(host.fileContent).not.toHaveBeenCalled(); expect(host.fileRender).not.toHaveBeenCalled();
  });
  it("lets a configured, temporarily unavailable service reach the scoped five-minute open stage", async () => {
    host.configuration.mockResolvedValueOnce({ enabled: true, configured: true, ready: false, state: "unavailable", reason: "Editor is restarting; chat still works." });
    const { controller } = mount("uploads/recover.docx"); await flush();
    expect(controller.officeEditing.value).toBe(true);
    expect(controller.officeBinding.value?.path).toBe("uploads/recover.docx");
    expect(controller.failed.value).toBe("");
    expect(host.configuration).toHaveBeenCalledTimes(1);
    expect(host.fileContent).not.toHaveBeenCalled(); expect(host.fileRender).not.toHaveBeenCalled();
  });
  it("shows configuration errors without reserving an editor or substituting a preview", async () => {
    host.configuration.mockResolvedValueOnce({ enabled: false, configured: true, ready: false, state: "config_error", reason: "Office signing configuration is invalid." });
    const { controller } = mount("uploads/config-error.xlsx"); await flush();
    expect(controller.officeEditing.value).toBe(false);
    expect(controller.officeBinding.value).toBeNull();
    expect(controller.failed.value).toBe("Office signing configuration is invalid.");
    expect(host.fileContent).not.toHaveBeenCalled(); expect(host.fileRender).not.toHaveBeenCalled();
  });
  it("keeps a contradictory enabled config_error terminal instead of opening a broken configuration", async () => {
    host.configuration.mockResolvedValueOnce({ enabled: true, configured: true, ready: false, state: "config_error", reason: "Office signing configuration is invalid." });
    const { controller } = mount("uploads/config-error-enabled.xlsx"); await flush();
    expect(controller.officeEditing.value).toBe(false);
    expect(controller.officeBinding.value).toBeNull();
    expect(controller.failed.value).toBe("Office signing configuration is invalid.");
  });
  it("focuses the existing file when two entrypoints race before the engine is mounted", async () => {
    const first = mount("uploads/race.xlsx"), second = mount("uploads/./race.xlsx"); await flush();
    expect(first.controller.officeEditing.value).toBe(true);
    expect(second.controller.officeEditing.value).toBe(false);
    expect(second.emit).toHaveBeenCalledWith("focusOffice", "uploads/./race.xlsx");
    expect(second.controller.failed.value).toBe("officeEditor.duplicate");
  });
  it("preserves the binding on refresh and keeps the reservation when closing is refused", async () => {
    const { controller } = mount("uploads/manual.docx"); await flush();
    const binding = controller.officeBinding.value;
    controller.officeEditor.value = { prepareClose: vi.fn().mockResolvedValue(false), refreshStatus: vi.fn() };
    expect(await controller.refreshPreview()).toBe(false);
    expect(await controller.prepareClose()).toBe(false);
    expect(controller.officeBinding.value).toBe(binding);
    const duplicate = mount("uploads/manual.docx"); await flush();
    expect(duplicate.controller.officeEditing.value).toBe(false);
    expect(host.configuration).toHaveBeenCalledTimes(2);
  });
  it("does not issue or cancel startup checks when asked to reload a live canvas", async () => {
    const { controller } = mount("uploads/keep-canvas.docx"); await flush();
    const binding = controller.officeBinding.value;
    await controller.load(); await flush();
    expect(controller.officeBinding.value).toBe(binding);
    expect(controller.officeEditing.value).toBe(true);
    expect(host.configuration).toHaveBeenCalledTimes(1);
  });
  it("releases the frontend reservation after a confirmed unused bootstrap failure", async () => {
    const { controller } = mount("uploads/retry-after-failure.docx"); await flush();
    controller.officeOpenFailed("Editor bootstrap failed and was released");
    expect(controller.officeEditing.value).toBe(false); expect(controller.officeBinding.value).toBeNull();
    const retry = mount("uploads/retry-after-failure.docx"); await flush();
    expect(retry.controller.officeEditing.value).toBe(true);
  });
  it("closes only its unused duplicate preview and focuses the validated existing document", async () => {
    const { controller, emit } = mount("uploads/legacy-alias-race.docx"); await flush();
    const closeExisting = vi.fn(); controller.officeEditor.value = { prepareClose: closeExisting };
    controller.officeFocusExisting("agents/agent-a/uploads/legacy-alias-race.docx");
    expect(controller.officeEditing.value).toBe(false); expect(controller.officeBinding.value).toBeNull();
    expect(emit).toHaveBeenCalledWith("focusOffice", "agents/agent-a/uploads/legacy-alias-race.docx"); expect(emit).toHaveBeenCalledWith("back");
    expect(closeExisting).not.toHaveBeenCalled();
  });
  it("ignores a stale config response after switching to a different file", async () => {
    let resolve!: (value: { enabled: boolean }) => void;
    host.configuration.mockImplementationOnce(() => new Promise(yes => { resolve = yes; }));
    const { controller, props } = mount("uploads/old.docx");
    props.path = "uploads/new.xlsx"; await flush();
    resolve({ enabled: true }); await flush();
    expect(controller.officeBinding.value?.path).toBe("uploads/new.xlsx");
  });
  it("aborts only the old availability check on a file switch", async () => {
    let firstSignal!: AbortSignal;
    host.configuration.mockImplementationOnce((signal: AbortSignal) => { firstSignal = signal; return new Promise(() => {}); });
    const { controller, props } = mount("uploads/cancel-old.docx"); await flush();
    expect(firstSignal.aborted).toBe(false);
    props.path = "uploads/replace-new.xlsx"; await flush();
    expect(firstSignal.aborted).toBe(true);
    expect(controller.officeBinding.value?.path).toBe("uploads/replace-new.xlsx");
  });
  it("shows checking progress for a slow service and times out only after five minutes", async () => {
    vi.useFakeTimers();
    host.configuration.mockImplementationOnce(() => new Promise(() => {}));
    const { controller } = mount("uploads/slow-check.xlsx"); await flush();
    expect(controller.officeChecking.value).toBe(true);
    await vi.advanceTimersByTimeAsync(299_000);
    expect(controller.failed.value).toBe("");
    expect(controller.officeCheckElapsed.value).toBe(299);
    await vi.advanceTimersByTimeAsync(1000); await flush();
    expect(controller.failed.value).toBe("officeEditor.startup.timeout");
    expect(controller.officeChecking.value).toBe(false);
    expect(vi.getTimerCount()).toBe(0);
  });
  it("retries a transient checking request without resetting the file or duplicating its reservation", async () => {
    vi.useFakeTimers();
    host.configuration.mockRejectedValueOnce(Object.assign(new Error("temporarily unavailable"), { status: 503 }));
    const { controller } = mount("uploads/check-retry.xlsx"); await flush();
    expect(controller.officeChecking.value).toBe(true);
    await vi.advanceTimersByTimeAsync(500); await flush();
    expect(host.configuration).toHaveBeenCalledTimes(2);
    expect(controller.officeCheckAttempt.value).toBe(2);
    expect(controller.officeBinding.value?.path).toBe("uploads/check-retry.xlsx");
    expect(vi.getTimerCount()).toBe(0);
  });
  it("cancels a pending availability check on unmount and ignores its late success", async () => {
    let signal!: AbortSignal, resolve!: (value: { enabled: boolean }) => void;
    host.configuration.mockImplementationOnce((value: AbortSignal) => { signal = value; return new Promise(done => { resolve = done; }); });
    const { controller, emit } = mount("uploads/unmounted-check.xlsx"); await flush();
    for (const cleanup of host.cleanups.splice(0)) cleanup();
    expect(signal.aborted).toBe(true);
    resolve({ enabled: true }); await flush();
    expect(controller.officeEditing.value).toBe(false);
    expect(emit).not.toHaveBeenCalledWith("officeOpened", expect.anything());
  });
  it("retains the existing text preview for other file types", async () => {
    const { controller } = mount("notes.md"); await flush();
    expect(host.configuration).not.toHaveBeenCalled();
    expect(host.fileContent).toHaveBeenCalledWith("notes.md");
    expect(controller.officeEditing.value).toBe(false);
  });
  it("shows a readable missing-file state when an answer refers to an old path", async () => {
    host.fileContent.mockRejectedValueOnce(Object.assign(new Error("private server path: no such file"), { status: 404 }));
    const { controller } = mount("uploads/removed.txt"); await flush();
    expect(controller.failed.value).toBe("workspaceFiles.notFound");
    expect(controller.failed.value).not.toContain("private server path");
  });
});

describe("Spreadsheet preview loading boundaries", () => {
  it("skips files over 20 MiB without downloading their bytes", async () => {
    const { controller } = mount("uploads/large.xlsm"); await flush();
    controller.readOnlyOffice.value = true;
    host.fileContent.mockResolvedValue({ binary: true, size: 21 * 1024 * 1024 });
    const fetchMock = vi.fn(); vi.stubGlobal("fetch", fetchMock);
    await controller.load();
    expect(controller.renderError.value).toBe("filePreview.tooLarge");
    expect(fetchMock).not.toHaveBeenCalled();
  });
  it("aborts the previous spreadsheet read when the file changes", async () => {
    const { controller, props } = mount("uploads/small.xlsm"); await flush();
    controller.readOnlyOffice.value = true;
    host.fileContent.mockResolvedValue({ binary: true, size: 1024 });
    const signals: AbortSignal[] = [];
    vi.stubGlobal("fetch", vi.fn((_url: string, init: { signal: AbortSignal }) => {
      signals.push(init.signal);
      return new Promise((_resolve, reject) => {
        if (init.signal.aborted) reject(new Error("aborted"));
        else init.signal.addEventListener("abort", () => reject(new Error("aborted")), { once: true });
      });
    }));
    const loading = controller.load();
    await flush();
    controller.officeEditing.value = false;
    props.path = "uploads/replacement.xlsm";
    await flush();
    expect(signals[0].aborted).toBe(true);
    await loading;
  });
});
