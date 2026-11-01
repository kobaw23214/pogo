import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, nextTick, reactive, type Ref } from "vue";
import type { StarterCard } from "@/api/types";
import type { ComposerPart } from "@/agent/composerParts";
import type { PrepareStarterDraft, StarterDraft } from "./starterCardDraft";

const host = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), save: vi.fn() }));
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), useSSRContext: () => ({ modules: new Set() }) }));
vi.mock("vue-i18n", async original => ({ ...await original<typeof import("vue-i18n")>(), useI18n: () => ({ t: (key: string, values?: Record<string, unknown>) => key + (values ? JSON.stringify(values) : "") }) }));
vi.mock("element-plus", () => ({ ElMessage: { success: host.success, error: host.error } }));
vi.mock("@/api/client", () => ({ api: { saveStarters: host.save } }));
vi.mock("./InlineComposer.vue", () => ({ default: {} }));
import StarterCards from "./StarterCards.vue";

interface Controller {
  working: Ref<StarterCard[]>;
  page: Ref<number>;
  pageCount: Ref<number>;
  pagedCards: Ref<StarterCard[]>;
  editing: Ref<boolean>;
  saving: Ref<boolean>;
  saveError: Ref<string>;
  shortcutPreview: Ref<{ id: string; title: string; content: string; icon?: string } | null>;
  formError: Ref<string>;
  form: Ref<{ title: string; label: string; icon: string; kind: "prompt" | "panel"; panel: string; parts: ComposerPart[]; inputLayout: "top" | "inline" }>;
  openAdd(draft?: StarterDraft): void;
  openEdit(index: number): void;
  submitForm(): Promise<void>;
  saveSet(cards: StarterCard[]): Promise<boolean>;
  activateReference(part: ComposerPart): void;
}
const scopes: ReturnType<typeof effectScope>[] = [];
const cards = (count: number): StarterCard[] => Array.from({ length: count }, (_, index) => ({ title: `流程 ${index + 1}`, prompt: `任务 ${index + 1}` }));
const textDraft = (text = "新流程"): StarterDraft => ({ parts: [{ type: "text", text }], inputLayout: "inline" });
function mount(list: StarterCard[], onSave = vi.fn<(cards: StarterCard[]) => Promise<void>>().mockResolvedValue(undefined), prepareDraft?: PrepareStarterDraft) {
  const scope = effectScope(); scopes.push(scope);
  const props = reactive({ cards: list, fallback: "选择", readOnly: false, onSave, prepareDraft });
  const emit = vi.fn(); const expose = vi.fn();
  const controller = scope.run(() => (StarterCards as unknown as { setup(props: unknown, ctx: unknown): Controller }).setup(props, { emit, expose }))!;
  return { controller, props, onSave, emit, expose };
}
beforeEach(() => { host.success.mockClear(); host.error.mockClear(); host.save.mockReset(); });
afterEach(() => { for (const scope of scopes.splice(0)) scope.stop(); });

describe("Shortcut save and pagination", () => {
  it("opens an immutable material snapshot from a chip in the starter editor", () => {
    const { controller: c, onSave } = mount([]);
    const reference: ComposerPart = { type: "shortcut", id: "occurrence", shortcutId: "material", label: "资料", title: "资料", text: "原始资料正文" };
    c.openAdd({ inputLayout: "inline", parts: [reference] });
    c.activateReference(reference);
    expect(c.shortcutPreview.value).toEqual({ id: "material", title: "资料", content: "原始资料正文" });
    reference.text = "后续编辑";
    expect(c.shortcutPreview.value?.content).toBe("原始资料正文");
    expect(c.editing.value).toBe(true); expect(onSave).not.toHaveBeenCalled();
  });

  it("exposes draft prefill and retains a detached mixed prompt", () => {
    const { controller: c, expose } = mount([]);
    const draft: StarterDraft = { parts: [{ type: "text", text: "处理 " }, { type: "file", id: "f", key: "k", label: "报告.xlsx" }], inputLayout: "inline" };
    c.openAdd(draft);
    expect(expose).toHaveBeenCalledWith({ openAdd: c.openAdd });
    expect(c.editing.value).toBe(true);
    expect(c.form.value.title).toBe("处理 〔报告.xlsx〕");
    c.form.value.parts[0] = { type: "text", text: "已修改" };
    expect(draft.parts[0]).toEqual({ type: "text", text: "处理 " });
  });
  it("waits for preparation and persistence before closing or showing page 2", async () => {
    let prepared!: (value: Awaited<ReturnType<PrepareStarterDraft>>) => void;
    let persisted!: () => void;
    const prepare = vi.fn<PrepareStarterDraft>().mockImplementation(() => new Promise(resolve => { prepared = resolve; }));
    const onSave = vi.fn<(cards: StarterCard[]) => Promise<void>>().mockImplementation(() => new Promise(resolve => { persisted = resolve; }));
    const { controller: c, props, emit } = mount(cards(9), onSave, prepare);
    c.openAdd(textDraft());
    const saving = c.submitForm();
    expect(c.saving.value).toBe(true); expect(c.editing.value).toBe(true); expect(c.page.value).toBe(1); expect(onSave).not.toHaveBeenCalled();
    prepared({ prompt: "新流程", parts: [{ type: "text", text: "新流程" }], input_layout: "inline" });
    await nextTick();
    expect(onSave).toHaveBeenCalledTimes(1); expect(c.editing.value).toBe(true);
    persisted(); await saving; await nextTick();
    expect(c.page.value).toBe(2); expect(c.pagedCards.value).toHaveLength(1); expect(c.pagedCards.value[0].title).toBe("新流程");
    expect(c.editing.value).toBe(false); expect(emit).toHaveBeenCalledWith("saved");
    props.cards = [...c.working.value]; await nextTick();
    expect(c.page.value).toBe(2);
  });
  it("keeps the prompt after preparation or write failure and retries explicitly", async () => {
    const prepare = vi.fn<PrepareStarterDraft>().mockRejectedValueOnce(new Error("upload interrupted")).mockResolvedValue({ parts: [{ type: "text", text: "新流程" }], input_layout: "inline" });
    const onSave = vi.fn<(cards: StarterCard[]) => Promise<void>>().mockRejectedValueOnce(new Error("write denied")).mockResolvedValue(undefined);
    const { controller: c } = mount(cards(9), onSave, prepare);
    c.openAdd(textDraft());
    await c.submitForm();
    expect(c.editing.value).toBe(true); expect(c.saveError.value).toContain("upload interrupted"); expect(c.form.value.parts).toEqual(textDraft().parts);
    expect(c.working.value).toHaveLength(9); expect(c.page.value).toBe(1);
    await c.submitForm();
    expect(c.editing.value).toBe(true); expect(c.saveError.value).toContain("write denied"); expect(c.working.value).toHaveLength(9);
    await c.submitForm(); await nextTick();
    expect(c.editing.value).toBe(false); expect(c.working.value).toHaveLength(10); expect(c.page.value).toBe(2);
  });
  it("supports unlimited pages and clamps only after the saved list shrinks", async () => {
    const { controller: c } = mount(cards(45));
    c.page.value = 5; expect(c.pagedCards.value[0].title).toBe("流程 37");
    c.openAdd(textDraft("第46张")); expect(c.formError.value).toBe("");
    await c.submitForm(); await nextTick();
    expect(c.pageCount.value).toBe(6); expect(c.page.value).toBe(6); expect(c.pagedCards.value[0].title).toBe("第46张");
    await c.saveSet(cards(8)); await nextTick(); expect(c.page.value).toBe(1);
  });
  it("preserves an edited file card and strips prompt parts when changing to a panel", async () => {
    const original: StarterCard = { title: "文件", prompt: "〔报告.xlsx〕", parts: [{ type: "file", path: "starter-assets/id/报告.xlsx", as_file: true }], input_layout: "inline" };
    const { controller: c, onSave } = mount([original]);
    c.openEdit(0); expect(c.form.value.parts[0]).toMatchObject({ type: "file", label: "报告.xlsx", asFile: true });
    await c.submitForm(); expect(onSave.mock.calls[0][0][0]).toMatchObject(original);
    c.openEdit(0); c.form.value.kind = "panel"; c.form.value.panel = "dashboard";
    await c.submitForm();
    expect(onSave.mock.calls[1][0][0]).toEqual({ title: "文件", panel: "dashboard" });
  });
});
