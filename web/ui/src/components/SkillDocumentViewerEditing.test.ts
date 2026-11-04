import { afterEach, describe, expect, it, vi } from "vitest";
import { effectScope, reactive, type Ref } from "vue";
import type { SkillDocumentContent, SkillDocumentUpdate } from "@/api/types";
import type { useSkillDocumentEditing } from "./skillDocumentEditing";
const hooks = vi.hoisted(() => ({ cleanup: [] as (() => void)[] }));
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), onMounted() {}, onBeforeUnmount: (fn: () => void) => hooks.cleanup.push(fn), useSSRContext: () => ({ modules: new Set() }) }));
vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string) => key }) }));
vi.mock("./MarkdownView.vue", () => ({ default: {} }));
vi.mock("./CodeBlock.vue", () => ({ default: {} }));
vi.mock("./ConfirmDialog.vue", () => ({ default: {} }));
import SkillDocumentViewer from "./SkillDocumentViewer.vue";
const raw = "---\nname: demo\ndescription: 已保存说明\n---\n# 已保存说明\n";
const doc = (content = raw, revision = "revision-a"): SkillDocumentContent => ({ content, revision, relative_path: "SKILL.md", editable: true, truncated: false });
interface Controller {
  editor: ReturnType<typeof useSkillDocumentEditing>;
  document: Ref<SkillDocumentContent | null>;
  location: Ref<{ path: string; anchor: string }>;
  source: Ref<string>;
  leaveConfirm: Ref<{ ask(): Promise<boolean> } | null>;
  navigate(action: () => Promise<unknown>): Promise<void>;
  openPath(path: string): Promise<void>;
  openLink(href: string): Promise<void>;
  reloadLatest(): Promise<void>;
}
const scopes: ReturnType<typeof effectScope>[] = [];
function mount(selectedPath = "SKILL.md") {
  const save = vi.fn(async (input: SkillDocumentUpdate) => doc(input.content, "revision-b"));
  const props = reactive({ scopeKey: "skill-a", managed: true, rootDocument: doc(), selectedPath, loadDocument: vi.fn(async (path: string): Promise<SkillDocumentContent> => ({ content: "# flow", relative_path: path, truncated: false })), saveDocument: save });
  const scope = effectScope(); scopes.push(scope); const emit = vi.fn(), discard = vi.fn(async () => false);
  const c = scope.run(() => (SkillDocumentViewer as unknown as { setup(props: unknown, ctx: unknown): Controller }).setup(props, { emit, expose() {} }))!;
  c.leaveConfirm.value = { ask: discard };
  return { c, props, save, emit, discard };
}
afterEach(() => { for (const cleanup of hooks.cleanup.splice(0)) cleanup(); for (const scope of scopes.splice(0)) scope.stop(); });
describe("Skill detail editing UI handlers", () => {
  it("loads a package document selected when the viewer first mounts", async () => {
    const { c, props } = mount("references/flow.md");

    await vi.waitFor(() => expect(c.document.value?.relative_path).toBe("references/flow.md"));

    expect(props.loadDocument).toHaveBeenCalledExactlyOnceWith("references/flow.md");
    expect(c.location.value.path).toBe("references/flow.md");
    expect(c.source.value).toBe("# flow");
  });
  it("reopens the selected package document after the Skill scope changes", async () => {
    const { c, props } = mount("references/flow.md");
    await vi.waitFor(() => expect(c.document.value?.relative_path).toBe("references/flow.md"));
    props.scopeKey = "skill-b";
    props.rootDocument = doc("# Other Skill", "revision-b");

    await vi.waitFor(() => expect(props.loadDocument).toHaveBeenCalledTimes(2));

    expect(props.loadDocument).toHaveBeenLastCalledWith("references/flow.md");
    expect(c.location.value.path).toBe("references/flow.md");
    expect(c.document.value?.relative_path).toBe("references/flow.md");
  });
  it("keeps raw frontmatter in edit mode and renders only the draft body in preview without saving", () => {
    const { c, save } = mount(); c.editor.edit(); expect(c.editor.draft.value).toBe(raw);
    c.editor.draft.value = raw.replace("# 已保存说明", "# 草稿预览"); c.editor.preview();
    expect(c.source.value).toBe("# 草稿预览\n"); expect(c.editor.dirty.value).toBe(true); expect(c.document.value?.content).toBe(raw);
    c.editor.edit(); expect(c.editor.draft.value).toContain("name: demo"); expect(save).not.toHaveBeenCalled();
  });
  it("cancels tree and Markdown navigation without changing the selected document or dropping a dirty draft", async () => {
    const { c, props, emit, discard } = mount(); c.editor.edit(); c.editor.draft.value += "我的草稿";
    await c.navigate(() => c.openPath("references/flow.md"));
    expect(discard).toHaveBeenCalledOnce(); expect(props.loadDocument).not.toHaveBeenCalled(); expect(c.location.value.path).toBe("SKILL.md");
    expect(c.editor.draft.value).toContain("我的草稿"); expect(emit).toHaveBeenCalledWith("pathChange", "SKILL.md");
    discard.mockResolvedValueOnce(true); await c.navigate(() => c.openPath("references/flow.md"));
    expect(c.location.value.path).toBe("references/flow.md"); expect(props.loadDocument).toHaveBeenCalledExactlyOnceWith("references/flow.md"); expect(c.editor.editing.value).toBe(false);
  });
  it("emits the acknowledged root document for metadata refresh after a successful save", async () => {
    const { c, save, emit } = mount(); c.editor.edit(); c.editor.draft.value += "新任务说明";
    await c.editor.save(); expect(save).toHaveBeenCalledExactlyOnceWith({ content: raw + "新任务说明", expected_revision: "revision-a" });
    expect(emit).toHaveBeenCalledWith("saved", doc(raw + "新任务说明", "revision-b")); expect(c.document.value?.revision).toBe("revision-b"); expect(c.location.value.path).toBe("SKILL.md"); expect(c.editor.mode.value).toBe("preview");
  });
  it("scrolls draft-preview anchors and blocks unsafe links without asking to discard the source", async () => {
    const { c, props, discard } = mount(); c.editor.edit(); c.editor.draft.value += "\n# 任务步骤"; c.editor.preview();
    await c.openLink("#任务步骤"); expect(c.location.value.anchor).toBe("任务步骤");
    await c.openLink("../private.md"); expect(c.editor.dirty.value).toBe(true); expect(c.editor.draft.value).toContain("# 任务步骤");
    expect(discard).not.toHaveBeenCalled(); expect(props.loadDocument).not.toHaveBeenCalled();
  });
  it("retains a 409 draft, asks before reloading, and supplies the fresh root revision to the parent", async () => {
    const { c, props, save, emit, discard } = mount(); save.mockRejectedValueOnce(Object.assign(new Error("另一窗口已修改"), { status: 409 }));
    c.editor.edit(); c.editor.draft.value += "我的草稿"; await c.editor.save();
    expect(c.editor.conflict.value).toBe(true); expect(c.editor.draft.value).toContain("我的草稿"); expect(emit).not.toHaveBeenCalledWith("saved", expect.anything());
    await c.reloadLatest(); expect(props.loadDocument).not.toHaveBeenCalled(); expect(c.editor.draft.value).toContain("我的草稿");
    props.loadDocument.mockResolvedValueOnce(doc("# 最新服务器说明", "revision-c")); discard.mockResolvedValueOnce(true); await c.reloadLatest();
    expect(props.loadDocument).toHaveBeenCalledExactlyOnceWith("SKILL.md"); expect(emit).toHaveBeenCalledWith("reloaded", doc("# 最新服务器说明", "revision-c"));
    expect(c.editor.editing.value).toBe(false); c.editor.edit(); expect(c.editor.draft.value).toBe("# 最新服务器说明"); expect(c.editor.revision.value).toBe("revision-c");
  });
});
