import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createSSRApp, defineComponent, effectScope, h, nextTick, reactive, type ComputedRef, type Ref } from "vue";
import { renderToString } from "vue/server-renderer";
import type { InformationEntry } from "@/api/information";
import type { InformationPreviewKind } from "./informationFilePreview";

const host = vi.hoisted(() => ({ entry: vi.fn(), content: vi.fn(), cleanups: [] as (() => void)[] }));
vi.mock("vue", async original => ({
  ...await original<typeof import("vue")>(),
  useSSRContext: () => ({ modules: new Set() }),
  provide: () => {},
  onBeforeUnmount: (cleanup: () => void) => host.cleanups.push(cleanup),
}));
vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string, values?: { message?: string }) => values?.message ? `${key}: ${values.message}` : key }) }));
vi.mock("@/api/information", () => ({ informationApi: { entry: host.entry, content: host.content } }));
vi.mock("./InformationOfficeViewer.vue", () => ({ default: defineComponent({
  props: ["entryId", "version", "mode"],
  setup: props => () => h("section", { "data-testid": "native-office-preview", "data-entry": props.entryId, "data-version": props.version, "data-mode": props.mode }),
}) }));
vi.mock("./InformationShortcutPreview.vue", () => ({ default: defineComponent({ setup: () => () => h("div", { "data-testid": "shortcut-preview" }) }) }));
vi.mock("./CodeBlock.vue", () => ({ default: defineComponent({ props: ["code", "lang"], setup: props => () => h("pre", { "data-language": props.lang }, props.code) }) }));
vi.mock("./fileIcons", () => ({ fileIcon: () => "test", folderIcon: "folder" }));
vi.mock("@iconify/vue", () => ({ Icon: defineComponent({ setup: () => () => h("span") }) }));

import InformationEntryDialog from "./InformationEntryDialog.vue";

const mib = 1024 ** 2;
const id = (letter: string) => `inf_${letter.repeat(32)}`;
const file = (name: string, size = 100, version = 7, letter = "a"): InformationEntry => ({
  id: id(letter), parent_id: "", name, kind: "file", version, created_at: "now", updated_at: "now",
  file: { name, mime: "application/octet-stream", size, sha256: "b".repeat(64), asset: `information-assets/${"b".repeat(64)}/${name}` },
});
interface Props { modelValue: boolean; entryId: string; version?: number }
interface Controller {
  entry: Ref<InformationEntry | null>; loading: Ref<boolean>; error: Ref<string>; text: Ref<string>; blobURL: Ref<string>;
  kind: Ref<InformationPreviewKind | "">; officeViewer: Ref<{ cleanup(): Promise<void> } | null>;
  officeError: Ref<string>; officeRetrying: Ref<boolean>; officeActive: ComputedRef<boolean>; officeKey: ComputedRef<string>;
  officePreview: ComputedRef<{ key: string; entryId: string; version: number; onError(message: string): void } | null>;
  load(): Promise<void>; officeFailed(message: string, key: string): void; retryOffice(): Promise<void>;
}
const scopes: ReturnType<typeof effectScope>[] = [];
const flush = async () => { for (let i = 0; i < 5; i++) { await Promise.resolve(); await nextTick(); } };
function deferred<T>() {
  let resolve!: (value: T) => void, reject!: (reason: unknown) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
function mount(extra: Partial<Props> = {}) {
  const props = reactive<Props>({ modelValue: true, entryId: id("a"), ...extra });
  const emit = vi.fn(), scope = effectScope(); scopes.push(scope);
  const c = scope.run(() => (InformationEntryDialog as unknown as { setup(p: unknown, context: unknown): Controller }).setup(props, { emit, expose() {} }))!;
  return { c, props, emit };
}
async function render(c: Controller, props: Props) {
  // Keep the real compiled template, with the same already-loaded setup state.
  // The Office stub records the mounted native-viewer contract, not a fake Blob.
  const component = { ...InformationEntryDialog, setup: () => c };
  const app = createSSRApp({ render: () => h(component as Parameters<typeof h>[0], props) });
  app.component("el-dialog", defineComponent({ setup: (_, { slots }) => () => h("article", [slots.header?.(), slots.default?.(), slots.footer?.()]) }));
  app.component("el-button", defineComponent({ setup: (_, { slots }) => () => h("button", slots.default?.()) }));
  app.component("el-icon", defineComponent({ setup: (_, { slots }) => () => h("span", slots.default?.()) }));
  return renderToString(app);
}

beforeEach(() => {
  host.entry.mockReset().mockResolvedValue(file("员工名单.xlsx"));
  host.content.mockReset().mockResolvedValue(new Blob(["source"]));
  vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:information-preview");
  vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
});
afterEach(() => {
  for (const cleanup of host.cleanups.splice(0)) cleanup();
  for (const scope of scopes.splice(0)) scope.stop();
  vi.restoreAllMocks();
});

describe("Information library file preview dialog", () => {
  it.each(["员工名单.xlsx", "部门说明.DOCX"])("mounts a large %s in the read-only Office viewer without fetching the package as a Blob", async name => {
    host.entry.mockResolvedValue(file(name, 94 * mib));
    const { c, props } = mount(); await flush();
    expect(c.kind.value).toBe("office"); expect(c.officeActive.value).toBe(true);
    expect(c.loading.value).toBe(false); expect(c.error.value).toBe("");
    expect(host.content).not.toHaveBeenCalled();
    expect(c.officePreview.value).toMatchObject({ entryId: id("a"), version: 7 });
    const html = await render(c, props);
    expect(html).toContain('data-testid="native-office-preview"');
    expect(html).toContain('data-mode="preview"'); expect(html).toContain('data-version="7"');
    expect(html).not.toContain("information.fileUnsupported");
    expect(html).not.toContain("information.previewTooLarge");
    expect(html).not.toContain("blob:information-preview");
  });

  it("keeps an explicitly requested historical source version instead of opening the latest version", async () => {
    host.entry.mockResolvedValue(file("旧版.xlsx", 94 * mib, 3));
    const { c } = mount({ version: 3 }); await flush();
    expect(host.entry).toHaveBeenCalledWith(id("a"), 3, expect.any(AbortSignal));
    expect(c.officePreview.value).toMatchObject({ entryId: id("a"), version: 3 });
    expect(c.officeKey.value).toContain(`${id("a")}:3:`);
    expect(host.content).not.toHaveBeenCalled();
  });

  it("aborts a pending metadata request when closed and never mounts a late Office result", async () => {
    const result = deferred<InformationEntry>(); host.entry.mockReturnValue(result.promise);
    const { c, props } = mount(); const signal = host.entry.mock.calls[0]![2] as AbortSignal;
    props.modelValue = false; expect(signal.aborted).toBe(true);
    result.resolve(file("late.xlsx")); await flush();
    expect(c.entry.value).toBeNull(); expect(c.officeActive.value).toBe(false); expect(c.loading.value).toBe(false);
    expect(await render(c, props)).not.toContain('data-testid="native-office-preview"');
    expect(host.content).not.toHaveBeenCalled();
  });

  it("ignores a previous file's delayed response after switching to a different source", async () => {
    const old = deferred<InformationEntry>(); host.entry.mockReturnValueOnce(old.promise).mockResolvedValueOnce(file("new.docx", 100, 9, "d"));
    const { c, props } = mount(); const signal = host.entry.mock.calls[0]![2] as AbortSignal;
    props.entryId = id("d"); await flush(); old.resolve(file("old.xlsx")); await flush();
    expect(signal.aborted).toBe(true); expect(c.entry.value?.name).toBe("new.docx");
    expect(c.officePreview.value).toMatchObject({ entryId: id("d"), version: 9 });
    expect(host.content).not.toHaveBeenCalled();
  });

  it("clears the old viewer on close, file switch and version change and rejects its delayed errors", async () => {
    const { c, props } = mount(); await flush(); const first = c.officePreview.value!;
    first.onError("first failed"); expect(c.officeError.value).toBe("first failed");
    host.entry.mockResolvedValueOnce(file("员工名单.xlsx", 100, 8)); props.version = 8;
    expect(c.officeActive.value).toBe(false); expect(c.officeError.value).toBe("");
    first.onError("late failure"); await flush();
    expect(c.officeError.value).toBe(""); expect(c.officePreview.value?.version).toBe(8);
    const second = c.officePreview.value!;
    host.entry.mockResolvedValueOnce(file("说明.docx", 100, 1, "d")); props.entryId = id("d");
    second.onError("wrong file"); await flush(); expect(c.officeError.value).toBe("");
    const last = c.officePreview.value!; props.modelValue = false; last.onError("closed viewer");
    expect(c.officeError.value).toBe(""); expect(c.officePreview.value).toBeNull();
    expect(await render(c, props)).not.toContain('data-testid="native-office-preview"');
  });

  it("waits for the exact old viewer to close before retrying the same immutable source", async () => {
    const { c, props } = mount(); await flush(); const first = c.officePreview.value!, closing = deferred<void>();
    const cleanup = vi.fn(() => closing.promise); c.officeViewer.value = { cleanup };
    first.onError("connection interrupted"); const retrying = c.retryOffice();
    expect(c.officeActive.value).toBe(false); expect(c.officeRetrying.value).toBe(true); expect(c.officeError.value).toBe("");
    await flush(); expect(cleanup).toHaveBeenCalledOnce();
    expect(await render(c, props)).not.toContain('data-testid="native-office-preview"');
    await c.retryOffice(); expect(cleanup).toHaveBeenCalledOnce();
    first.onError("old callback during close"); expect(c.officeError.value).toBe("");
    closing.resolve(); await retrying; await flush();
    expect(c.officeActive.value).toBe(true); expect(c.officeRetrying.value).toBe(false);
    expect(c.officePreview.value).toMatchObject({ entryId: id("a"), version: 7 });
    expect(c.officeKey.value).not.toBe(first.key);
    first.onError("old callback after replacement"); expect(c.officeError.value).toBe("");
    expect(host.entry).toHaveBeenCalledOnce(); expect(host.content).not.toHaveBeenCalled();
  });

  it("does not remount the old source when the dialog closes during retry cleanup", async () => {
    const { c, props } = mount(); await flush(); const closing = deferred<void>();
    c.officeViewer.value = { cleanup: vi.fn(() => closing.promise) };
    const retrying = c.retryOffice(); await flush(); props.modelValue = false;
    closing.resolve(); await retrying; await flush();
    expect(c.officeActive.value).toBe(false); expect(c.officeRetrying.value).toBe(false); expect(c.entry.value).toBeNull();
    expect(await render(c, props)).not.toContain('data-testid="native-office-preview"');
  });

  it("keeps source-code highlighting and fetches the pinned text version once", async () => {
    host.entry.mockResolvedValue(file("release_test.py", 50, 4)); host.content.mockResolvedValue(new Blob(["def run():\n    return True\n"]));
    const { c, props } = mount(); await flush();
    expect(c.kind.value).toBe("text"); expect(c.officeActive.value).toBe(false);
    expect(host.content).toHaveBeenCalledWith(id("a"), 4, false, expect.any(AbortSignal));
    expect(c.text.value).toContain("return True");
    const html = await render(c, props); expect(html).toContain('data-language="py"'); expect(html).toContain("def run()");
    expect(html).not.toContain('data-testid="native-office-preview"');
  });

  it("retains PDF Blob previews and revokes the object URL on close", async () => {
    host.entry.mockResolvedValue(file("report.pdf")); host.content.mockResolvedValue(new Blob(["%PDF fixture"], { type: "application/pdf" }));
    const { c, props } = mount(); await flush();
    expect(c.kind.value).toBe("pdf"); expect(c.blobURL.value).toBe("blob:information-preview");
    expect(await render(c, props)).toContain('<iframe src="blob:information-preview"');
    props.modelValue = false; expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:information-preview");
    expect(c.blobURL.value).toBe(""); expect(c.officeActive.value).toBe(false);
  });

  it.each([["big.py", 2 * mib + 1, "large"], ["legacy.doc", 100, "unsupported"]])("does not download unsupported or oversized ordinary preview %s", async (name, size, kind) => {
    host.entry.mockResolvedValue(file(String(name), Number(size)));
    const { c } = mount(); await flush();
    expect(c.kind.value).toBe(kind); expect(c.officeActive.value).toBe(false); expect(host.content).not.toHaveBeenCalled();
  });

  it("hides a stale ordinary-file error and releases its request when switching to Office", async () => {
    const content = deferred<Blob>(); host.entry.mockResolvedValueOnce(file("before.py")).mockResolvedValueOnce(file("after.xlsx"));
    host.content.mockReturnValueOnce(content.promise); const { c, props } = mount(); await flush();
    const signal = host.content.mock.calls[0]![3] as AbortSignal;
    props.version = 8; await flush(); content.reject(new Error("old read failed")); await flush();
    expect(signal.aborted).toBe(true); expect(c.error.value).toBe(""); expect(c.kind.value).toBe("office");
  });
});
