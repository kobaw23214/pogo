import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, reactive, type Ref } from "vue";
import type { PlatformSkill, SkillLibraryCatalog } from "@/agent/platformTypes";
import type { SkillDocumentContent } from "@/api/types";
import { setLocale } from "@/i18n";
const host = vi.hoisted(() => ({ library: vi.fn(), content: vi.fn(), dispatch: vi.fn(), cleanup: [] as (() => void)[], guards: [] as (() => Promise<boolean>)[] }));
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), onMounted() {}, onBeforeUnmount: (fn: () => void) => host.cleanup.push(fn), useSSRContext: () => ({ modules: new Set() }) }));
vi.mock("vue-i18n", async original => ({ ...await original<typeof import("vue-i18n")>(), useI18n: () => ({ t: (key: string) => key }) }));
vi.mock("@/api/client", () => ({ api: { skillLibrary: host.library, managedSkillContent: host.content } }));
vi.mock("@/agent/studioNavigation", () => ({ registerStudioNavigationGuard: (fn: () => Promise<boolean>) => { host.guards.push(fn); return () => {}; } }));
vi.mock("@/components/SkillsPanel.vue", () => ({ default: {} }));
vi.mock("./SkillUploadDialog.vue", () => ({ default: {} }));
vi.mock("@/components/SkillExecutionNotice.vue", () => ({ default: {} }));
vi.mock("@/components/SkillDocumentViewer.vue", () => ({ default: {} }));
vi.mock("@/components/SkillPackageBrowser.vue", () => ({ default: {} }));
import SkillLibrary from "./SkillLibrary.vue";
const first: PlatformSkill = { id: "skill-a", name: "demo", description: "旧说明", hidden: false, tool_count: 0, removable: true };
const second: PlatformSkill = { ...first, id: "skill-b", name: "other" };
const catalog = (skills = [first, second]): SkillLibraryCatalog => ({ skills, tools: [], execution: null, max_upload_bytes: 1024 });
const doc: SkillDocumentContent = { content: "# 原说明", revision: "rev-a", editable: true, truncated: false };
interface Controller {
  skills: Ref<PlatformSkill[]>;
  selected: Ref<PlatformSkill | null>; detailOpen: Ref<boolean>; instructions: Ref<SkillDocumentContent | null>; action: Ref<string | null>;
  packageBrowser: Ref<{ confirmLeave(): Promise<boolean> } | null>;
  reload(): Promise<void>; show(skill: PlatformSkill): Promise<void>; create(skill: PlatformSkill): Promise<void>; askAction(kind: string, skill: PlatformSkill): Promise<void>;
  closeDetail(done: () => void): Promise<void>; documentSaved(doc: SkillDocumentContent): void;
}
const scopes: ReturnType<typeof effectScope>[] = [];
async function mount() {
  const scope = effectScope(); scopes.push(scope); const emit = vi.fn();
  const c = scope.run(() => (SkillLibrary as unknown as { setup(props: unknown, context: unknown): Controller }).setup(reactive({ canManage: true }), { emit, expose() {} }))!;
  for (let i = 0; i < 8; i++) await Promise.resolve();
  return { c, emit };
}
beforeEach(() => {
  host.library.mockReset().mockResolvedValue(catalog()); host.content.mockReset().mockResolvedValue(doc); host.dispatch.mockClear(); host.guards.length = 0;
  vi.stubGlobal("window", { addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: host.dispatch });
  vi.stubGlobal("CustomEvent", class { constructor(readonly type: string) {} });
});
afterEach(() => { for (const cleanup of host.cleanup.splice(0)) cleanup(); for (const scope of scopes.splice(0)) scope.stop(); vi.unstubAllGlobals(); setLocale("zh-CN"); });
describe("managed Skill detail parent leave guards", () => {
  it("re-sorts Skill names with the active language without translating user-defined names", async () => {
    const values = ["Zulu", "中", "Alice"].map((name, index) => ({ ...first, id: `skill-${index}`, name }));
    host.library.mockResolvedValueOnce(catalog(values));
    const { c } = await mount();
    setLocale("zh-CN");
    const chinese = c.skills.value.map(skill => skill.name);
    expect(chinese).toEqual(values.map(skill => skill.name).sort(new Intl.Collator("zh-CN").compare));
    setLocale("en");
    const english = c.skills.value.map(skill => skill.name);
    expect(english).toEqual(values.map(skill => skill.name).sort(new Intl.Collator("en").compare));
    expect(new Set(english)).toEqual(new Set(values.map(skill => skill.name)));
  });
  it("routes drawer close, Skill switch, Agent creation, management actions and Studio navigation through the editor guard", async () => {
    const { c, emit } = await mount(); await c.show(first); const guard = vi.fn(async () => false); c.packageBrowser.value = { confirmLeave: guard };
    const done = vi.fn(); await c.closeDetail(done); await c.show(second); await c.create(first); await c.askAction("delete", first);
    expect(done).not.toHaveBeenCalled(); expect(c.selected.value?.id).toBe(first.id); expect(c.detailOpen.value).toBe(true); expect(c.action.value).toBeNull(); expect(emit).not.toHaveBeenCalled(); expect(host.content).toHaveBeenCalledTimes(1);
    expect(await host.guards[0]!()).toBe(false);
    guard.mockResolvedValue(true); await c.closeDetail(done); expect(done).toHaveBeenCalledOnce();
  });
  it("adopts only saved document contents and refreshes catalog descriptions through the standard change event", async () => {
    const { c } = await mount(); await c.show(first); const saved: SkillDocumentContent = { ...doc, content: "# 新说明", revision: "rev-b" };
    c.documentSaved(saved); expect(c.instructions.value).toEqual(saved); expect(host.dispatch).toHaveBeenCalledWith(expect.objectContaining({ type: "pogo-skills-changed" }));
    host.library.mockResolvedValueOnce(catalog([{ ...first, description: "新说明" }, second])); await c.reload();
    expect(c.selected.value?.description).toBe("新说明"); expect(c.instructions.value?.revision).toBe("rev-b");
  });
  it("does not destroy a dirty editor when an external catalog refresh removes its Skill", async () => {
    const { c } = await mount(); await c.show(first); c.packageBrowser.value = { confirmLeave: vi.fn(async () => false) };
    host.library.mockResolvedValueOnce(catalog([second])); await c.reload();
    expect(c.selected.value?.id).toBe(first.id); expect(c.detailOpen.value).toBe(true); expect(c.instructions.value).toEqual(doc);
  });
});
