import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createSSRApp, defineComponent, effectScope, h, nextTick, reactive, type ComputedRef, type Ref } from "vue";
import { renderToString } from "vue/server-renderer";
import type { SpillEntries, SpillEntry, SpillFile } from "@/api/types";
const host = vi.hoisted(() => ({ entries: vi.fn(), file: vi.fn(), cleanup: [] as (() => void)[] }));
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), onBeforeUnmount: (cleanup: () => void) => host.cleanup.push(cleanup), useSSRContext: () => ({ modules: new Set() }) }));
vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string) => key }) }));
vi.mock("@/api/client", () => ({ api: { spillEntries: host.entries, spillFile: host.file } }));
import SpillBrowser from "./SpillBrowser.vue";

const root = "/home/pogo/.pogo/spill";
const folder: SpillEntry = { name: "scopes", path: "scopes", type: "directory", size: 0, modified_at: "" };
const entry: SpillEntry = { name: "data.html", path: "scopes/data.html", type: "file", size: 80000, modified_at: "" };
const listing = (path = "", extra: Partial<SpillEntries> = {}): SpillEntries => ({ root, path, parent: "", read_only: true, exists: true, entries: path ? [entry] : [folder], offset: 0, limit: 100, next_offset: null, complete: true, truncated: false, skipped: 0, scan_limit_reached: false, ...extra });
const contents = (extra: Partial<SpillFile> = {}): SpillFile => ({ root, path: entry.path, name: entry.name, read_only: true, size: 80000, modified_at: "", offset: 0, bytes: 32768, next_offset: 32768, complete: false, truncated: true, binary: false, encoding: "utf-8", content: "<script>alert('never run')</script>", ...extra });
type Controller = { directory: Ref<SpillEntries | null>; file: Ref<SpillFile | null>; directoryLoading: Ref<boolean>; fileLoading: Ref<boolean>; directoryError: Ref<string>; fileError: Ref<string>; directoryHistory: Ref<number[]>; selectedPath: Ref<string>; filePosition: Ref<{ offset: number; startLine: number; continued: boolean }>; fileLines: ComputedRef<{ number: number; text: string; continued: boolean }[]>; loadDirectory(path?: string, offset?: number, history?: number[]): Promise<void>; loadFile(path: string): Promise<void>; select(entry: SpillEntry): Promise<void>; directoryPage(next: boolean): Promise<void> | undefined; filePage(next: boolean): Promise<void> | undefined; retryDirectory(): Promise<void> };
const scopes: ReturnType<typeof effectScope>[] = [];
const flush = async () => { for (let i = 0; i < 8; i++) await Promise.resolve(); await nextTick(); };
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done; }); return { promise, resolve }; }
function mount(open = true) {
  const props = reactive({ modelValue: open, root }); const scope = effectScope(); scopes.push(scope);
  const c = scope.run(() => (SpillBrowser as unknown as { setup(props: unknown, context: unknown): Controller }).setup(props, { emit: vi.fn(), expose() {} }))!;
  return { c, props };
}
async function render(c: Controller, props: { modelValue: boolean; root: string }) {
  const component = { ...SpillBrowser, setup: () => c };
  const app = createSSRApp({ render: () => h(component as Parameters<typeof h>[0], props) });
  app.component("el-dialog", defineComponent({ setup: (_, { slots }) => () => h("article", [slots.default?.(), slots.footer?.()]) }));
  app.component("el-button", defineComponent({ setup: (_, { slots }) => () => h("button", slots.default?.()) }));
  app.component("el-icon", defineComponent({ setup: (_, { slots }) => () => h("span", slots.default?.()) }));
  return renderToString(app);
}
beforeEach(() => { host.entries.mockReset().mockImplementation(async (path = "", offset = 0) => listing(path, { offset })); host.file.mockReset().mockImplementation(async () => contents()); });
afterEach(() => { for (const cleanup of host.cleanup.splice(0)) cleanup(); for (const scope of scopes.splice(0)) scope.stop(); });

describe("Spill read-only browser", () => {
  it("does not fetch while closed, then starts at the configured root", async () => {
    const { c, props } = mount(false); await flush(); expect(host.entries).not.toHaveBeenCalled();
    props.modelValue = true; await flush(); expect(host.entries).toHaveBeenCalledExactlyOnceWith("", 0, expect.any(AbortSignal));
    expect(c.directory.value?.root).toBe(root); expect(host.file).not.toHaveBeenCalled();
  });
  it("navigates directories and uses server offsets for forward and back pages", async () => {
    const { c } = mount(); await flush(); await c.select(folder);
    expect(c.directory.value?.path).toBe("scopes");
    c.directory.value = listing("scopes", { next_offset: 137, complete: false, truncated: true });
    await c.directoryPage(true); expect(host.entries).toHaveBeenLastCalledWith("scopes", 137, expect.any(AbortSignal));
    expect(c.directoryHistory.value).toEqual([0]);
    await c.directoryPage(false); expect(host.entries).toHaveBeenLastCalledWith("scopes", 0, expect.any(AbortSignal));
    expect(c.directoryHistory.value).toEqual([]);
  });
  it("renders executable file formats only as escaped text, with no writer or download controls", async () => {
    const { c, props } = mount(); await flush(); await c.select(entry);
    expect(host.file).toHaveBeenCalledExactlyOnceWith(entry.path, 0, expect.any(AbortSignal));
    const html = await render(c, props);
    expect(html).toContain('&lt;script&gt;alert('); expect(html).not.toContain("<script>");
    expect(html).toContain('data-testid="spill-file-content"'); expect(html).toContain(root); expect(html).toContain("spillBrowser.filePartial");
    expect(html).not.toContain("contenteditable"); expect(html).not.toContain("<iframe"); expect(html).not.toContain("download="); expect(html).not.toContain('type="file"');
  });
  it("pages through bounded file chunks without accumulating the entire file", async () => {
    const { c } = mount(); await flush(); await c.select(entry);
    host.file.mockResolvedValueOnce(contents({ offset: 32768, bytes: 32768, next_offset: 65536, content: "second" }));
    await c.filePage(true); expect(host.file).toHaveBeenLastCalledWith(entry.path, 32768, expect.any(AbortSignal));
    expect(c.file.value?.content).toBe("second"); expect(c.file.value?.content).not.toContain("script");
    await c.filePage(false); expect(host.file).toHaveBeenLastCalledWith(entry.path, 0, expect.any(AbortSignal));
  });
  it("keeps real source line numbers across continued Chinese lines, trailing newlines and backward paging", async () => {
    const first = "第一行\n第二行的前半", second = "后半\n第三行\n";
    const offset = new TextEncoder().encode(first).length;
    const size = offset + new TextEncoder().encode(second).length;
    host.file.mockResolvedValueOnce(contents({ content: first, bytes: offset, size, next_offset: offset, modified_at: "version-a" }));
    const { c, props } = mount(); await flush(); await c.select(entry);
    expect(c.fileLines.value).toEqual([{ number: 1, text: "第一行", continued: false }, { number: 2, text: "第二行的前半", continued: false }]);
    host.file.mockResolvedValueOnce(contents({ content: second, offset, bytes: size - offset, size, next_offset: null, complete: true, truncated: false, modified_at: "version-a" }));
    await c.filePage(true);
    expect(c.fileLines.value).toEqual([{ number: 2, text: "后半", continued: true }, { number: 3, text: "第三行", continued: false }, { number: 4, text: "", continued: false }]);
    const html = await render(c, props); expect(html).toContain("spillBrowser.continuedLine"); expect(html).toContain('class="spill-line-number" aria-hidden="true"'); expect(html).toContain('data-line="2"');
    host.file.mockResolvedValueOnce(contents({ content: first, bytes: offset, size, next_offset: offset, modified_at: "version-a" }));
    await c.filePage(false); expect(c.filePosition.value).toEqual({ offset: 0, startLine: 1, continued: false });
    expect(c.fileLines.value.map(line => line.number)).toEqual([1, 2]);
  });
  it("starts a new line after a page-ending LF and keeps one number for an arbitrarily long wrapped line", async () => {
    host.file.mockResolvedValueOnce(contents({ content: "one\n", next_offset: 4, bytes: 4 }));
    const { c } = mount(); await flush(); await c.select(entry);
    expect(c.fileLines.value).toEqual([{ number: 1, text: "one", continued: false }]);
    host.file.mockResolvedValueOnce(contents({ content: "长行".repeat(1000), offset: 4, bytes: 6000, next_offset: 6004 }));
    await c.filePage(true); expect(c.fileLines.value).toHaveLength(1); expect(c.fileLines.value[0]).toMatchObject({ number: 2, continued: false });
    host.file.mockResolvedValueOnce(contents({ content: "续段", offset: 6004, bytes: 6, next_offset: null }));
    await c.filePage(true); expect(c.fileLines.value).toEqual([{ number: 2, text: "续段", continued: true }]);
  });
  it("resets numbering on file switch and close and renders the empty file as one logical empty line", async () => {
    const { c, props } = mount(); await flush(); await c.select(entry);
    host.file.mockResolvedValueOnce(contents({ content: "", size: 0, bytes: 0, next_offset: null, complete: true, truncated: false }));
    await c.loadFile("empty.txt"); expect(c.filePosition.value).toEqual({ offset: 0, startLine: 1, continued: false });
    expect(c.fileLines.value).toEqual([{ number: 1, text: "", continued: false }]);
    props.modelValue = false; await flush(); expect(c.fileLines.value).toEqual([]); expect(c.filePosition.value).toEqual({ offset: 0, startLine: 1, continued: false });
  });
  it.each([{ size: 90000 }, { modified_at: "new-version" }])("refuses to reuse line numbers after a file change: %o", async change => {
    const { c } = mount(); await flush(); await c.select(entry);
    host.file.mockResolvedValueOnce(contents({ offset: 32768, ...change }));
    await c.filePage(true); expect(c.file.value).toBeNull(); expect(c.fileLines.value).toEqual([]); expect(c.fileError.value).toBe("spillBrowser.fileChanged");
    await c.loadFile(c.selectedPath.value); expect(host.file).toHaveBeenLastCalledWith(entry.path, 0, expect.any(AbortSignal));
    expect(c.filePosition.value).toEqual({ offset: 0, startLine: 1, continued: false });
  });
  it("cancels superseded reads and never lets a late response replace the selected file", async () => {
    const { c, props } = mount(); await flush();
    const late = deferred<SpillFile>(); host.file.mockReturnValueOnce(late.promise);
    const first = c.loadFile("old.txt"), oldSignal = host.file.mock.calls[0]![2] as AbortSignal;
    await c.loadFile("new.txt"); expect(oldSignal.aborted).toBe(true);
    late.resolve(contents({ path: "old.txt", content: "stale" })); await first;
    expect(c.file.value?.content).not.toBe("stale");
    const pending = deferred<SpillFile>(); host.file.mockReturnValueOnce(pending.promise);
    const request = c.loadFile("pending.txt"), signal = host.file.mock.calls[host.file.mock.calls.length - 1]![2] as AbortSignal;
    props.modelValue = false; await flush(); expect(signal.aborted).toBe(true);
    pending.resolve(contents()); await request; expect(c.file.value).toBeNull(); expect(c.fileLoading.value).toBe(false);
  });
  it("retries the failed navigation instead of silently returning to the old directory", async () => {
    const { c } = mount(); await flush(); host.entries.mockRejectedValueOnce(new Error("Unreadable directory"));
    await c.loadDirectory("scopes/unreadable"); expect(c.directoryError.value).toBe("Unreadable directory");
    await c.retryDirectory(); expect(host.entries).toHaveBeenLastCalledWith("scopes/unreadable", 0, expect.any(AbortSignal));
    expect(c.directoryError.value).toBe("");
  });
  it("explains binary files and incomplete listings without inventing a complete view", async () => {
    host.entries.mockResolvedValueOnce(listing("", { skipped: 2, scan_limit_reached: true, truncated: true, complete: false }));
    const { c, props } = mount(); await flush(); host.file.mockResolvedValueOnce(contents({ binary: true, content: "", next_offset: null })); await c.select(entry);
    const html = await render(c, props); expect(html).toContain("spillBrowser.binary"); expect(html).toContain("spillBrowser.scanLimit"); expect(html).toContain("spillBrowser.skipped"); expect(html).not.toContain('data-testid="spill-file-content"');
  });
});
