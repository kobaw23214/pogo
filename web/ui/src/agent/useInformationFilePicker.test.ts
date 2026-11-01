import { afterEach, describe, expect, it, vi } from "vitest";
import { createRenderer, h, ref } from "vue";
import type { InformationEntry } from "@/api/information";
import type { ComposerPart } from "./composerParts";
import { useInformationFilePicker } from "./useInformationFilePicker";

interface HostNode { text: string; children: HostNode[]; parent: HostNode | null }
const node = (text = ""): HostNode => ({ text, children: [], parent: null });
// A real Vue component lifecycle without browser globals or mocked composable code.
const renderer = createRenderer<HostNode, HostNode>({
  createElement: type => node(type), createText: node, createComment: node,
  patchProp() {}, setText(target, text) { target.text = text; },
  setElementText(target, text) { target.text = text; target.children = []; },
  insert(target, parent, anchor) {
    if (target.parent) target.parent.children.splice(target.parent.children.indexOf(target), 1);
    const index = anchor ? parent.children.indexOf(anchor) : -1;
    parent.children.splice(index < 0 ? parent.children.length : index, 0, target); target.parent = parent;
  },
  remove(target) { if (target.parent) target.parent.children.splice(target.parent.children.indexOf(target), 1); target.parent = null; },
  parentNode: target => target.parent,
  nextSibling: target => target.parent?.children[target.parent.children.indexOf(target) + 1] || null,
});
function deferred<T>() {
  let resolve!: (value: T) => void, reject!: (error: unknown) => void;
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}
const file = (n = 1): InformationEntry => ({
  id: `inf_${String(n).padStart(32, "0")}`, parent_id: "", kind: "file", name: `source-${n}.csv`, version: n,
  created_at: "2026-10-18T05:00:00Z", updated_at: "2026-10-18T05:00:00Z",
  file: { name: `source-${n}.csv`, mime: "text/csv", size: 12, sha256: "a".repeat(64), asset: `information-assets/${"a".repeat(64)}/source-${n}.csv` },
});
const parts = (name: string): ComposerPart[] => [{ type: "file", id: name, key: name, path: `uploads/${name}.csv`, label: `${name}.csv`, status: "ready", asFile: true }];
const cleanups: (() => void)[] = [];
function mount(allowedKinds?: readonly InformationEntry["kind"][]) {
  const scope = ref("agent-a/session-a/draft-a");
  const resolve = vi.fn<(entry: InformationEntry) => Promise<ComposerPart[]>>();
  let picker!: ReturnType<typeof useInformationFilePicker>;
  const app = renderer.createApp({ setup() {
    picker = useInformationFilePicker({ scope: () => scope.value, allowedKinds, resolve, errorText: error => `File import failed: ${String(error)}` });
    return () => h("div");
  } });
  app.mount(node());
  let unmounted = false;
  const unmount = () => { if (!unmounted) { unmounted = true; app.unmount(); } };
  cleanups.push(unmount);
  return { picker, scope, resolve, unmount };
}
afterEach(() => { for (const cleanup of cleanups.splice(0)) cleanup(); });

describe("Information file picker lifecycle", () => {
  it("resolves library shortcuts at the selected version when the caller permits all reusable entries", async () => {
    const { picker, resolve } = mount(["file", "shortcut", "folder"]);
    const selected = { ...file(3), kind: "shortcut" as const, shortcut: { id: "inf-nested", title: "数据库资料", content: "saved content" }, file: undefined };
    const snapshot: ComposerPart[] = [{ type: "shortcut", id: "snapshot", shortcutId: "inf-nested", title: "数据库资料", label: "数据库资料", text: "saved content" }];
    resolve.mockResolvedValue(snapshot);
    const choice = picker.choose();
    await picker.select({ ...file(), kind: "folder" });
    expect(resolve).not.toHaveBeenCalled();
    await picker.select(selected);
    expect(resolve).toHaveBeenCalledWith(selected);
    await expect(choice).resolves.toEqual(snapshot);
    expect(picker.open.value).toBe(false);
  });

  it("keeps a failed choice open for an explicit retry and settles only the successful result", async () => {
    const { picker, resolve } = mount(), first = deferred<ComposerPart[]>(), second = deferred<ComposerPart[]>();
    resolve.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    const choice = picker.choose(); let settled = false; void choice.then(() => { settled = true; });
    expect(picker.open.value).toBe(true); expect(picker.busy.value).toBe(false);
    const failure = picker.select(file()); expect(picker.busy.value).toBe(true);
    first.reject(new Error("network interrupted")); await failure;
    expect(settled).toBe(false); expect(picker.open.value).toBe(true); expect(picker.busy.value).toBe(false);
    expect(picker.error.value).toContain("network interrupted"); expect(resolve).toHaveBeenCalledTimes(1);
    const retry = picker.select(file()); expect(picker.error.value).toBe(""); expect(resolve).toHaveBeenCalledTimes(2);
    const selected = parts("retry"); second.resolve(selected); await retry;
    await expect(choice).resolves.toBe(selected);
    expect(picker.open.value).toBe(false); expect(picker.busy.value).toBe(false); expect(picker.error.value).toBe("");
  });

  it("settles a cancelled open picker with null without copying any file", async () => {
    const { picker, resolve } = mount(); const choice = picker.choose();
    picker.cancel(); await expect(choice).resolves.toBeNull();
    expect(picker.open.value).toBe(false); expect(picker.busy.value).toBe(false); expect(resolve).not.toHaveBeenCalled();
    picker.cancel();
    const next = picker.choose(); expect(picker.open.value).toBe(true); picker.cancel(); await expect(next).resolves.toBeNull();
  });

  it.each(["resolve", "reject"] as const)("ignores a late %s after cancel while a new selection remains in progress", async outcome => {
    const { picker, resolve } = mount(), old = deferred<ComposerPart[]>(), fresh = deferred<ComposerPart[]>();
    resolve.mockReturnValueOnce(old.promise).mockReturnValueOnce(fresh.promise);
    const abandoned = picker.choose(), oldRequest = picker.select(file()); picker.cancel();
    await expect(abandoned).resolves.toBeNull();
    const next = picker.choose(), nextRequest = picker.select(file(2));
    if (outcome === "resolve") old.resolve(parts("abandoned")); else old.reject(new Error("late failure"));
    await oldRequest;
    expect(picker.open.value).toBe(true); expect(picker.busy.value).toBe(true); expect(picker.error.value).toBe("");
    const selected = parts("fresh"); fresh.resolve(selected); await nextRequest; await expect(next).resolves.toBe(selected);
  });

  it("synchronously cancels a changed scope and allows a new scope to select without accepting the old response", async () => {
    const { picker, scope, resolve } = mount(), old = deferred<ComposerPart[]>(), fresh = deferred<ComposerPart[]>();
    resolve.mockReturnValueOnce(old.promise).mockReturnValueOnce(fresh.promise);
    const first = picker.choose(), oldRequest = picker.select(file());
    scope.value = "agent-b/session-b/draft-b";
    expect(picker.open.value).toBe(false); expect(picker.busy.value).toBe(false); await expect(first).resolves.toBeNull();
    const next = picker.choose(), freshRequest = picker.select(file(2));
    old.resolve(parts("old-scope")); await oldRequest;
    expect(picker.open.value).toBe(true); expect(picker.busy.value).toBe(true);
    const selected = parts("new-scope"); fresh.resolve(selected); await freshRequest; await expect(next).resolves.toBe(selected);
    expect(resolve.mock.calls.map(([entry]) => [entry.id, entry.version])).toEqual([[file().id, 1], [file(2).id, 2]]);
  });

  it.each([false, true])("settles pending selection on actual Vue unmount with request started=%s", async started => {
    const { picker, resolve, unmount } = mount(), request = deferred<ComposerPart[]>(); resolve.mockReturnValue(request.promise);
    const choice = picker.choose(), inFlight = started ? picker.select(file()) : undefined;
    unmount(); await expect(choice).resolves.toBeNull();
    request.resolve(parts("unmounted")); await inFlight;
    expect(picker.open.value).toBe(false); expect(picker.busy.value).toBe(false); expect(picker.error.value).toBe("");
    expect(resolve).toHaveBeenCalledTimes(started ? 1 : 0);
  });

  it("does not replace the pending promise or duplicate an in-flight file resolution", async () => {
    const { picker, resolve } = mount(), request = deferred<ComposerPart[]>(); resolve.mockReturnValue(request.promise);
    const choice = picker.choose(); await expect(picker.choose()).resolves.toBeNull(); expect(picker.open.value).toBe(true);
    const first = picker.select(file()); await picker.select(file(2));
    expect(resolve).toHaveBeenCalledTimes(1); expect(resolve).toHaveBeenCalledWith(file());
    const selected = parts("single"); request.resolve(selected); await first; await expect(choice).resolves.toBe(selected);
  });

  it("ignores non-files, incomplete file entries and selections made without an open picker", async () => {
    const { picker, resolve } = mount(); await picker.select(file()); expect(resolve).not.toHaveBeenCalled();
    const choice = picker.choose(), { file: _file, ...metadata } = file();
    await picker.select({ ...metadata, kind: "folder" });
    await picker.select({ ...metadata, kind: "shortcut", shortcut: { id: "nested", title: "Nested", content: "body" } });
    await picker.select(metadata);
    expect(resolve).not.toHaveBeenCalled(); expect(picker.open.value).toBe(true); expect(picker.busy.value).toBe(false);
    picker.cancel(); await expect(choice).resolves.toBeNull();
  });
});
