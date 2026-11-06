import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, nextTick, reactive, type Ref } from "vue";
import { retireBrowserControls } from "@/agent/browserSheets";
import type { NativeBrowserState } from "@/agent/nativeBrowser";
import type { ComposerPart, ComposerReferencePart } from "@/agent/composerParts";

const host = vi.hoisted(() => ({ cleanups: [] as (() => void)[], create: vi.fn(), close: vi.fn(), state: vi.fn(), tabs: vi.fn(), control: vi.fn() }));
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), inject: () => null, useId: () => "browser-lifetime", useSSRContext: () => ({ modules: new Set() }), onBeforeUnmount: (fn: () => void) => host.cleanups.push(fn) }));
vi.mock("vue-i18n", async original => { const { ref } = await import("vue"); return { ...await original<typeof import("vue-i18n")>(), useI18n: () => ({ t: (key: string) => key, locale: ref("zh-CN") }) }; });
vi.mock("@/api/client", () => ({ api: { browserAuthoringCreate: host.create, browserAuthoringClose: host.close, browserState: host.state, browserTabs: host.tabs, browserControl: host.control } }));
vi.mock("@/agent/studioNavigation", () => ({ registerStudioNavigationGuard: () => () => {} }));
vi.mock("./InlineComposer.vue", () => ({ default: {} }));
vi.mock("./OrderedMessageContent.vue", () => ({ default: {} }));
vi.mock("./CodeBlock.vue", () => ({ default: {} }));
vi.mock("./ShortcutContent.vue", () => ({ default: {} }));
vi.mock("./ShortcutLanguageSelect.vue", () => ({ default: {} }));
vi.mock("./studio/StarterIconPicker.vue", () => ({ default: {} }));
vi.mock("./OfficeReferenceDialog.vue", () => ({ default: {} }));
vi.mock("./ShortcutReferenceDialog.vue", () => ({ default: {} }));
vi.mock("./ConfirmDialog.vue", () => ({ default: {} }));
vi.mock("./ShortcutSourcesPanel.vue", () => ({ default: {} }));
vi.mock("./WebReferenceCreateDialog.vue", () => ({ default: {} }));
vi.mock("./InformationOfficeReferenceDialog.vue", () => ({ default: {} }));
import ShortcutEditorDialog from "./ShortcutEditorDialog.vue";

const ctx = `ctx_${"a".repeat(24)}`, session = `browser-authoring-${"b".repeat(48)}`;
const browserState = (): NativeBrowserState => ({ backend: "native", context_ref: ctx, viewed_tab: "t1", active_tab: "t1", control: "agent", control_epoch: 0, generation: 1,
  url: "https://example.test/signed-in", title: "已登录页面", version: "1:t1:1", tabs: [{ id: "t1", url: "https://example.test/signed-in", title: "已登录页面" }], viewport: { width: 1280, height: 720 } });
const web = (): ComposerReferencePart => ({ type: "element", id: "web-ref", label: "导出", text: '<page-element source="系统" url="https://example.test/signed-in">\nelement: button "导出"\n</page-element>' });
interface Controller {
  webCreateOpen: Ref<boolean>; officeCreateOpen: Ref<boolean>; saving: Ref<boolean>; uploading: Ref<boolean>; draftParts: Ref<ComposerPart[]>;
  webBrowser: { sessionId: Ref<string>; state: Ref<NativeBrowserState | null> };
  openReferenceCreator(kind: "web" | "office"): void; acceptCreatedReference(part: ComposerReferencePart, kind: "web" | "office"): void;
  saveDraft(): Promise<void>;
}
const scopes: ReturnType<typeof effectScope>[] = [];
const settle = async () => { await nextTick(); for (let i = 0; i < 25; i++) await Promise.resolve(); };
function mount(extra: Record<string, unknown> = {}) {
  const props = reactive({ modelValue: true, item: { id: "draft-a", title: "批量流程", content: "先完成第一步" }, existing: true, library: [], skills: [], sessionId: "", scopeKey: "workspace-a", disabled: false, save: vi.fn().mockResolvedValue(undefined), ...extra });
  const scope = effectScope(); scopes.push(scope); const emit = vi.fn();
  const c = scope.run(() => (ShortcutEditorDialog as unknown as { setup(props: unknown, context: unknown): Controller }).setup(props, { emit, expose() {} }))!;
  return { c, props, emit };
}
beforeEach(() => {
  vi.useFakeTimers(); vi.stubGlobal("window", { addEventListener: vi.fn(), removeEventListener: vi.fn() });
  host.create.mockReset().mockResolvedValue({ session_id: session, expires_in: 1200 }); host.close.mockReset().mockResolvedValue(undefined);
  host.state.mockReset().mockResolvedValue(browserState()); host.tabs.mockReset();
  host.control.mockReset().mockResolvedValue({ ...browserState(), control: "human", control_epoch: 1, lease_token: "owned-authoring-lease" });
});
afterEach(async () => { for (const fn of host.cleanups.splice(0)) fn(); for (const scope of scopes.splice(0)) scope.stop(); await settle(); retireBrowserControls(session, ""); vi.unstubAllGlobals(); vi.useRealTimers(); });

describe("Shortcut editor owns one lazily started browser per editing lifetime", () => {
  it("does not create a browser merely by opening the editor or an Office selection", async () => {
    const { c } = mount(); await settle(); expect(host.create).not.toHaveBeenCalled();
    c.openReferenceCreator("office"); await settle(); expect(host.create).not.toHaveBeenCalled();
  });
  it("retains the same signed-in page across insertion, maker cancellation and reopening", async () => {
    const { c, props } = mount(); c.openReferenceCreator("web"); await settle();
    expect(host.create).toHaveBeenCalledOnce(); const before = c.webBrowser.state.value;
    c.acceptCreatedReference(web(), "web"); await settle(); expect(c.webCreateOpen.value).toBe(false);
    expect(host.close).not.toHaveBeenCalled(); expect(c.webBrowser.sessionId.value).toBe(session); expect(c.webBrowser.state.value).toEqual(before);
    c.openReferenceCreator("web"); await settle(); expect(host.create).toHaveBeenCalledOnce(); expect(c.webBrowser.state.value?.url).toBe("https://example.test/signed-in");
    c.webCreateOpen.value = false; await settle(); expect(host.close).not.toHaveBeenCalled();
    c.openReferenceCreator("web"); await settle(); expect(host.create).toHaveBeenCalledOnce();
    props.modelValue = false; await settle(); expect(host.close).toHaveBeenCalledExactlyOnceWith(session);
  });
  it("does not destroy owned resources on temporary saving, uploading or disabled state", async () => {
    const { c, props } = mount(); c.openReferenceCreator("web"); await settle(); c.webCreateOpen.value = false;
    for (const field of [c.saving, c.uploading]) { field.value = true; await settle(); field.value = false; await settle(); }
    props.disabled = true; await settle(); props.disabled = false; await settle();
    expect(host.create).toHaveBeenCalledOnce(); expect(host.close).not.toHaveBeenCalled(); expect(c.webBrowser.sessionId.value).toBe(session);
  });
  it("retains the browser after a failed save and cleans it after the successful parent close", async () => {
    const { c, props, emit } = mount({ save: vi.fn().mockRejectedValueOnce(new Error("save conflict")).mockResolvedValue(undefined) });
    c.openReferenceCreator("web"); await settle(); c.acceptCreatedReference(web(), "web"); await settle();
    await c.saveDraft(); expect(emit).not.toHaveBeenCalled(); expect(host.close).not.toHaveBeenCalled(); expect(host.create).toHaveBeenCalledOnce();
    await c.saveDraft(); expect(emit).toHaveBeenCalledWith("update:modelValue", false); expect(host.close).not.toHaveBeenCalled();
    props.modelValue = false; await settle(); expect(host.close).toHaveBeenCalledExactlyOnceWith(session);
  });
  it.each(["scope", "session", "unmount"])("cleans up on %s change without creating a replacement for the stale editor", async change => {
    const { c, props } = mount(); c.openReferenceCreator("web"); await settle();
    if (change === "scope") props.scopeKey = "workspace-b";
    if (change === "session") props.sessionId = "other-conversation";
    if (change === "unmount") for (const cleanup of host.cleanups.splice(0)) cleanup();
    await settle(); expect(host.close).toHaveBeenCalledExactlyOnceWith(session); expect(host.create).toHaveBeenCalledOnce();
    expect(c.webBrowser.sessionId.value).toBe("");
  });
});
