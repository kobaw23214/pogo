import { afterEach, describe, expect, it, vi } from "vitest";
import { effectScope, nextTick, reactive, type Ref } from "vue";
import type { InformationEntry } from "@/api/information";
import type { ShortcutSnapshot, ShortcutsView } from "@/api/shortcuts";
const host = vi.hoisted(() => ({ cleanups: [] as (() => void)[] }));
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), useSSRContext: () => ({ modules: new Set() }), onBeforeUnmount: (fn: () => void) => host.cleanups.push(fn) }));
vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string) => key }) }));
vi.mock("./ShortcutsPanel.vue", () => ({ default: {} }));
vi.mock("./PrivateShortcutTree.vue", () => ({ default: {} }));
vi.mock("./studio/InformationLibrary.vue", () => ({ default: {} }));
import ShortcutWorkspace from "./ShortcutWorkspace.vue";
type Section = "root" | "information" | "cards" | "private";
interface Controller {
  section: Ref<Section>; folderId: Ref<string>; visitedInformation: Ref<boolean>; visitedCards: Ref<boolean>; privateView: Ref<ShortcutsView | null>; folders: Ref<{ id: string }[]>;
  information: Ref<{ confirmLeave(): Promise<boolean> } | null>; privatePanel: Ref<{ confirmLeave(): Promise<boolean> } | null>;
  navigate(next: Section, folder?: string): Promise<void>; back(): void; close(): Promise<void>; receivedView(view: ShortcutsView): void; insertInformation(entry: InformationEntry): void; insertShortcut(value: ShortcutSnapshot): void;
}
const scopes: ReturnType<typeof effectScope>[] = [];
const view = (agent = "agent-a"): ShortcutsView => ({ scope_id: "scope-" + agent, agent_id: agent, revision: 1, shortcuts: [{ id: "legacy", title: "原有快捷方式", content: "保留根内容" }], folders: [{ id: "project", name: "项目" }, { id: "child", name: "流程", parent_id: "project" }] });
function mount() {
  const props = reactive({ agentId: "agent-a", sessionId: "session-a", disabled: false, active: true, informationBusy: false });
  const scope = effectScope(); scopes.push(scope); const emit = vi.fn();
  const c = scope.run(() => (ShortcutWorkspace as unknown as { setup(props: unknown, context: unknown): Controller }).setup(props, { emit, expose() {} }))!;
  const confirm = vi.fn<() => Promise<boolean>>().mockResolvedValue(true); c.privatePanel.value = { confirmLeave: confirm }; c.receivedView(view());
  return { c, props, emit, confirm };
}
function deferred() { let done!: (value: boolean) => void; return { promise: new Promise<boolean>(resolve => { done = resolve; }), resolve: (value: boolean) => done(value) }; }
const entry = Object.freeze<InformationEntry>({ id: "inf_" + "a".repeat(32), parent_id: "", kind: "file", name: "资料.csv", version: 7, created_at: "2026-10-18", updated_at: "2026-10-18", file: Object.freeze({ name: "资料.csv", mime: "text/csv", size: 3, sha256: "b".repeat(64), asset: "information-assets/" + "b".repeat(64) + "/资料.csv" }) });
afterEach(() => { for (const cleanup of host.cleanups.splice(0)) cleanup(); for (const scope of scopes.splice(0)) scope.stop(); });
describe("Agent shortcut workspace navigation", () => {
  it("starts at the Agent private root and does not load either preset before visiting it", () => { const { c } = mount(); expect(c.section.value).toBe("root"); expect(c.folderId.value).toBe(""); expect(c.visitedInformation.value).toBe(false); expect(c.visitedCards.value).toBe(false); expect(c.folders.value.map(folder => folder.id)).toEqual(["project", "child"]); });
  it("navigates nested private folders and back through their real ancestry", async () => { const { c } = mount(); await c.navigate("private", "child"); expect(c.folderId.value).toBe("child"); c.back(); await nextTick(); for (let i = 0; i < 5; i++) await Promise.resolve(); expect(c.folderId.value).toBe("project"); c.back(); for (let i = 0; i < 5; i++) await Promise.resolve(); expect(c.section.value).toBe("root"); });
  it("rejects imaginary private directories without leaving or creating a source", async () => { const { c, confirm } = mount(); await c.navigate("private", "@information"); expect(c.section.value).toBe("root"); expect(confirm).not.toHaveBeenCalled(); });
  it("lazily visits sources without discarding the private tree or reclassifying its old root entries", async () => { const { c } = mount(); await c.navigate("information"); expect(c.visitedInformation.value).toBe(true); expect(c.visitedCards.value).toBe(false); await c.navigate("cards"); expect(c.visitedCards.value).toBe(true); await c.navigate("root"); expect(c.privateView.value?.shortcuts[0]).not.toHaveProperty("parent_id"); });
  it("ignores a view emitted by the previous Agent and clears visible folders on Agent switch", async () => { const { c, props } = mount(); props.agentId = "agent-b"; await nextTick(); c.receivedView(view()); expect(c.privateView.value).toBeNull(); expect(c.folders.value).toEqual([]); c.receivedView(view("agent-b")); expect(c.folders.value).toHaveLength(2); });
  it("returns to root when a selected private folder disappears elsewhere", async () => { const { c } = mount(); await c.navigate("private", "child"); c.receivedView({ ...view(), folders: [] }); await nextTick(); expect(c.section.value).toBe("root"); expect(c.folderId.value).toBe(""); });
  it.each(["disabled", "informationBusy"] as const)("rejects navigation and close while %s", async flag => { const { c, props, emit, confirm } = mount(); props[flag] = true; await c.navigate("cards"); await c.close(); expect(confirm).not.toHaveBeenCalled(); expect(emit).not.toHaveBeenCalled(); expect(c.visitedCards.value).toBe(false); });
});
describe("Workspace protects private rich drafts and read-only viewers", () => {
  it.each(["information", "cards", "private", "close"] as const)("a cancelled private editor leave keeps %s from proceeding", async next => { const { c, emit, confirm } = mount(); confirm.mockResolvedValueOnce(false); if (next === "close") await c.close(); else await c.navigate(next, "project"); expect(c.section.value).toBe("root"); expect(emit).not.toHaveBeenCalled(); expect(c.visitedCards.value).toBe(false); expect(c.visitedInformation.value).toBe(false); });
  it("also protects an active information preview before switching back to private content", async () => { const { c } = mount(); await c.navigate("information"); const viewer = vi.fn<() => Promise<boolean>>().mockResolvedValue(false); c.information.value = { confirmLeave: viewer }; await c.navigate("private", "project"); expect(viewer).toHaveBeenCalledOnce(); expect(c.section.value).toBe("information"); });
  it.each(["navigate", "close"] as const)("serializes all commands while %s is waiting", async action => { const { c, confirm, emit } = mount(), wait = deferred(); confirm.mockReturnValue(wait.promise); const pending = action === "close" ? c.close() : c.navigate("cards"); await c.navigate("information"); await c.navigate("private", "project"); await c.close(); expect(confirm).toHaveBeenCalledOnce(); wait.resolve(true); await pending; if (action === "close") expect(emit).toHaveBeenCalledExactlyOnceWith("close"); else { expect(c.section.value).toBe("cards"); expect(emit).not.toHaveBeenCalled(); } });
  for (const action of ["navigate", "close"] as const) it.each(["disabled", "informationBusy", "agent", "session", "unmount"] as const)(`rejects a late ${action} after %s changes`, async change => { const { c, props, confirm, emit } = mount(), wait = deferred(); confirm.mockReturnValueOnce(wait.promise); const pending = action === "close" ? c.close() : c.navigate("cards"); if (change === "disabled") props.disabled = true; if (change === "informationBusy") props.informationBusy = true; if (change === "agent") { props.agentId = "agent-b"; await nextTick(); } if (change === "session") props.sessionId = "session-b"; if (change === "unmount") for (const cleanup of host.cleanups.splice(0)) cleanup(); wait.resolve(true); await pending; expect(c.section.value).toBe("root"); expect(c.visitedCards.value).toBe(false); expect(emit).not.toHaveBeenCalled(); });
});
describe("Workspace references never execute preset panels", () => {
  it("forwards a complete immutable information file only in its active preset folder", async () => { const { c, emit } = mount(); await c.navigate("information"); c.insertInformation(entry); expect(emit).toHaveBeenCalledExactlyOnceWith("insertInformation", entry); expect(entry.file?.sha256).toBe("b".repeat(64)); });
  it.each(["folder", "disabled", "informationBusy", "inactive", "root", "cards", "private"] as const)("does not forward information selections in %s state", async state => { const { c, props, emit } = mount(); await c.navigate("information"); if (state === "disabled") props.disabled = true; if (state === "informationBusy") props.informationBusy = true; if (state === "inactive") props.active = false; if (["root", "cards", "private"].includes(state)) await c.navigate(state as Section, "project"); c.insertInformation(state === "folder" ? { ...entry, kind: "folder", file: undefined } : entry); expect(emit).not.toHaveBeenCalled(); });
  it("forwards prompt snapshots as references while cards are active without emitting openStarter or management", async () => { const { c, emit } = mount(); await c.navigate("cards"); const snapshot: ShortcutSnapshot = { id: "starter-hash", title: "首页提示", content: "固定说明", source: "starter" }; c.insertShortcut(snapshot); expect(emit).toHaveBeenCalledExactlyOnceWith("insert", snapshot); });
  it("does not forward references while a private leave decision is pending", async () => { const { c, confirm, emit } = mount(), wait = deferred(); confirm.mockReturnValue(wait.promise); const pending = c.navigate("cards"); c.insertShortcut({ id: "legacy", title: "旧资料", content: "不能晚到" }); expect(emit).not.toHaveBeenCalled(); wait.resolve(false); await pending; });
});
