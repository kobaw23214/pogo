import { afterEach, describe, expect, it, vi } from "vitest";
import { effectScope, nextTick, reactive, type Ref } from "vue";
import { readFileSync } from "node:fs";
import { nativeElementPick, type NativeBrowserState } from "@/agent/nativeBrowser";
import type { ComposerElementPart } from "@/agent/composerParts";
import type { ElementPick } from "@/agent/elementPicker";
import { createWebReference } from "@/agent/webReference";

vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), useSSRContext: () => ({ modules: new Set() }) }));
vi.mock("vue-i18n", async original => { const { ref } = await import("vue"); return { ...await original<typeof import("vue-i18n")>(), useI18n: () => ({ t: (key: string) => key, locale: ref("zh-CN") }) }; });
vi.mock("./NativeBrowserPane.vue", () => ({ default: {} }));
vi.mock("./ElementPageIcon.vue", () => ({ default: {} }));
vi.mock("./ElementCard.vue", () => ({ default: {} }));
import WebReferenceCreateDialog from "./WebReferenceCreateDialog.vue";

const context = "ctx_" + "a".repeat(24);
const binding = (session = "authoring-a", tab = "t1") => `${session}:${context}:${tab}`;
const state = (): NativeBrowserState => ({
  backend: "native", control: "human", url: "https://test.invalid/orders", title: "订单", context_ref: context,
  version: "1:t1:1:2:0:1200x800", viewed_tab: "t1", active_tab: "t1", generation: 1,
  tabs: [{ id: "t1", url: "https://test.invalid/orders", title: "订单", favicon: "data:image/png;base64,AAAA" }, { id: "t2", url: "https://test.invalid/report", title: "报告" }], viewport: { width: 1200, height: 800 },
});
function pick(label = "导出"): ElementPick {
  const snapshot = state();
  return { ...nativeElementPick({ tab_id: "t1", version: snapshot.version!, url: snapshot.url, title: snapshot.title,
    viewport: snapshot.viewport, observation_scope: "native-dom-accessibility", element: { ref: "p1", tag: "button", role: "button", name: label, text: label, rect: { x: 20, y: 30, width: 90, height: 35 } } }, snapshot)!, icon: "data:image/png;base64,AAAA" };
}
const scopes: ReturnType<typeof effectScope>[] = [];
interface Controller {
  selected: Ref<ComposerElementPart | undefined>; selectedCard: Ref<unknown>; selectionError: Ref<string>;
  existingReferences: Ref<ComposerElementPart[]>; active: Ref<boolean>;
  pageBinding: Ref<{ key: string; session: string; target: { context_ref: string; tab_id: string } } | undefined>;
  panes: Ref<{ key: string }[]>;
  acceptPick(pick: ElementPick, bindingKey: string): void;
  updateState(state: NativeBrowserState, bindingKey: string): void;
  create(): void; close(): void; chooseExisting(id: string): void;
  clearSelection(): void; switchTab(id: string): void; retry(): void; recover(key: string): void;
}
function mount(extra: Record<string, unknown> = {}) {
  const props = reactive({ modelValue: true, disabled: false, sessionId: "conversation-a", observedReferences: [] as ComposerElementPart[], error: "",
    browserSessionId: "authoring-a", browserState: state() as NativeBrowserState | null, browserLoading: false, browserError: "", ...extra });
  const scope = effectScope(); scopes.push(scope); const emit = vi.fn();
  const c = scope.run(() => (WebReferenceCreateDialog as unknown as { setup(props: unknown, context: unknown): Controller }).setup(props, { emit, expose() {} }))!;
  return { c, props, emit };
}
afterEach(() => { for (const scope of scopes.splice(0)) scope.stop(); });

describe("Browser-based web reference authoring", () => {
  it("keeps the native observed snapshot and frozen favicon, inserting only on explicit confirmation", () => {
    const { c, emit } = mount(), captured = pick();
    c.acceptPick(captured, binding());
    expect(emit).not.toHaveBeenCalled(); expect(c.selected.value).toMatchObject({ type: "element", text: captured.block, favicon: captured.icon, title: "订单" });
    expect(c.selectedCard.value).toMatchObject({ title: "订单", favicon: captured.icon, url: "https://test.invalid/orders" });
    c.create(); expect(emit).toHaveBeenCalledTimes(1); expect(emit.mock.calls[0][0]).toBe("create");
    const result = emit.mock.calls[0][1] as ComposerElementPart;
    expect(result.id).not.toBe(c.selected.value?.id); expect(result.text).toBe(captured.block);
    expect(result.text).toContain("observation_scope: native-dom-accessibility");
    expect(result.text).not.toContain("user-authored-web-reference");
    expect(emit).not.toHaveBeenCalledWith("update:modelValue", false);
  });
  it("replaces a staged element and supports clearing without touching any composer", () => {
    const { c, emit } = mount(); c.acceptPick(pick("导出"), binding()); c.acceptPick(pick("下载"), binding());
    expect(c.selected.value?.label).toContain("下载"); c.clearSelection(); c.create();
    expect(c.selected.value).toBeUndefined(); expect(emit).not.toHaveBeenCalled();
  });
  it("keeps the native page instance while staging, replacing, and clearing the footer reference", async () => {
    const { c } = mount(), page = c.panes.value[0];
    c.acceptPick(pick("导出"), binding()); await nextTick();
    expect(c.panes.value[0]).toBe(page);
    c.acceptPick(pick("下载"), binding()); await nextTick();
    expect(c.panes.value[0]).toBe(page);
    c.clearSelection(); await nextTick();
    expect(c.panes.value[0]).toBe(page);
  });
  it.each(["modelValue", "disabled", "browserLoading", "browserError"])("rejects browser events when %s prevents active interaction", (name) => {
    const { c, props, emit } = mount();
    if (name === "modelValue") props.modelValue = false;
    if (name === "disabled") props.disabled = true;
    if (name === "browserLoading") props.browserLoading = true;
    if (name === "browserError") props.browserError = "connection unavailable";
    c.acceptPick(pick(), binding()); c.switchTab("t2");
    expect(c.selected.value).toBeUndefined(); expect(emit).not.toHaveBeenCalled();
  });
  it("rejects a delayed pick from an old browser session or a replaced page", () => {
    const { c, props } = mount(); c.acceptPick(pick(), binding("retired-session")); expect(c.selected.value).toBeUndefined();
    c.acceptPick(pick(), binding("authoring-a", "t2")); expect(c.selected.value).toBeUndefined();
    props.browserSessionId = "authoring-b"; c.acceptPick(pick(), binding()); expect(c.selected.value).toBeUndefined();
  });
  it.each(["internalURL", "wrongURL", "manualBlock", "nonNativeKey"])("does not manufacture a snapshot for %s", kind => {
    const { c } = mount(), captured = pick();
    if (kind === "internalURL") captured.pageURL = "about:blank";
    if (kind === "wrongURL") captured.pageURL = "https://other.invalid/";
    if (kind === "manualBlock") captured.block = createWebReference({ url: "https://test.invalid/orders", title: "订单", text: "用户手写", context: "", selector: "" })!.text;
    if (kind === "nonNativeKey") captured.key = "authored:manual";
    c.acceptPick(captured, binding()); expect(c.selected.value).toBeUndefined(); expect(c.selectionError.value).toBe("webReference.invalidSelection");
  });
  it("retains the selected snapshot through a reconnect, page switch, or parent insertion error", async () => {
    const { c, props, emit } = mount(); c.acceptPick(pick(), binding()); const before = JSON.stringify(c.selected.value);
    props.error = "aggregate size too large"; props.browserLoading = true; props.browserError = "disconnected"; await nextTick();
    expect(JSON.stringify(c.selected.value)).toBe(before);
    props.browserLoading = false; c.retry(); expect(emit).toHaveBeenCalledWith("retry");
    props.browserError = ""; props.browserState = { ...state(), viewed_tab: "t2", url: "https://test.invalid/report" }; await nextTick();
    expect(JSON.stringify(c.selected.value)).toBe(before); c.create();
    expect(emit).toHaveBeenCalledWith("create", expect.objectContaining({ text: pick().block }));
  });
  it.each(["sourceSession", "authoringSession", "close"])("clears staged data when %s changes", async change => {
    const { c, props } = mount(); c.acceptPick(pick(), binding()); expect(c.selected.value).toBeDefined();
    if (change === "sourceSession") props.sessionId = "conversation-b";
    if (change === "authoringSession") props.browserSessionId = "authoring-b";
    if (change === "close") props.modelValue = false;
    await nextTick(); expect(c.selected.value).toBeUndefined();
  });
  it("passes only matching current page observations to its owner", () => {
    const { c, emit } = mount(); c.updateState(state(), binding("retired"));
    c.updateState({ ...state(), context_ref: "ctx_" + "b".repeat(24) }, binding());
    c.updateState({ ...state(), viewed_tab: "t2" }, binding()); expect(emit).not.toHaveBeenCalled();
    c.updateState(state(), binding()); expect(emit).toHaveBeenCalledWith("state", state());
  });
  it("requests explicit known-page switching without sending navigation or switching to arbitrary ids", () => {
    const { c, emit } = mount(); c.switchTab("t1"); c.switchTab("unknown"); expect(emit).not.toHaveBeenCalled();
    c.switchTab("t2"); expect(emit.mock.calls).toEqual([["switchTab", "t2"]]);
  });
  it("keeps visited native pane keys alive across tab switches and retires them with the authoring session", async () => {
    const { c, props } = mount(); expect(c.panes.value.map(page => page.key)).toEqual([binding()]);
    props.browserState = { ...state(), viewed_tab: "t2", url: "https://test.invalid/report" }; await nextTick();
    expect(c.panes.value.map(page => page.key)).toEqual([binding(), binding("authoring-a", "t2")]);
    props.browserState = state(); await nextTick(); expect(c.panes.value).toHaveLength(2);
    props.browserSessionId = "authoring-b"; await nextTick(); expect(c.panes.value.map(page => page.key)).toEqual([binding("authoring-b")]);
    props.modelValue = false; await nextTick(); expect(c.panes.value).toEqual([]);
  });
  it("gates recovery, retry, close and confirmation while the parent is saving", () => {
    const { c, props, emit } = mount(); c.acceptPick(pick(), binding());
    props.disabled = true; c.recover(binding()); c.retry(); c.close(); c.create(); c.clearSelection();
    expect(c.selected.value).toBeDefined(); expect(emit).not.toHaveBeenCalled();
    props.disabled = false; c.recover(binding("old")); expect(emit).not.toHaveBeenCalled();
    c.recover(binding()); expect(emit).toHaveBeenCalledWith("recover"); c.close(); expect(emit).toHaveBeenCalledWith("update:modelValue", false);
  });
  it("stages reused historical references without rewriting them or immediately inserting them", () => {
    const original = createWebReference({ url: "https://test.invalid/", title: "旧来源", text: "历史引用", context: "", selector: "" })!;
    const { c, emit } = mount({ observedReferences: [original] }); expect(c.existingReferences.value).toHaveLength(1);
    c.chooseExisting(original.id); expect(emit).not.toHaveBeenCalled(); expect(c.selected.value?.text).toBe(original.text); expect(c.selected.value?.id).not.toBe(original.id);
    c.create(); expect(emit).toHaveBeenCalledWith("create", expect.objectContaining({ text: original.text }));
  });
  it("keeps the pane bound to trusted discovered page identity", () => {
    const { c, props } = mount(); expect(c.pageBinding.value).toMatchObject({ session: "authoring-a", target: { context_ref: context, tab_id: "t1" } });
    props.browserState = { ...state(), context_ref: "untrusted" }; expect(c.pageBinding.value).toBeUndefined();
    props.browserState = { ...state(), viewed_tab: "t9" }; expect(c.pageBinding.value).toBeUndefined();
    props.browserState = null; expect(c.pageBinding.value).toBeUndefined();
  });
  it("has one native renderer and no manual-quotation or iframe authoring path", () => {
    const source = readFileSync(new URL("./WebReferenceCreateDialog.vue", import.meta.url), "utf8");
    expect(source).toContain("<NativeBrowserPane"); expect(source).toContain("<ElementCard");
    expect(source).not.toContain("<iframe"); expect(source).not.toContain("<textarea");
    expect(source).not.toContain("web-reference-url"); expect(source).not.toContain("web-reference-title"); expect(source).not.toContain("web-reference-text");
  });
});
