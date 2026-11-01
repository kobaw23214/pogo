import { afterEach, describe, expect, it, vi } from "vitest";
import { createSSRApp, defineComponent, effectScope, h, reactive, type Ref } from "vue";
import { renderToString } from "vue/server-renderer";
import type { SkillDocumentContent, SkillPackageEntry, SkillPackageFiles } from "@/api/types";
const hooks = vi.hoisted(() => ({ cleanup: [] as (() => void)[] }));
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), onBeforeUnmount: (fn: () => void) => hooks.cleanup.push(fn), useSSRContext: () => ({ modules: new Set() }) }));
vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string) => key }) }));
vi.mock("./SkillDocumentViewer.vue", () => ({ default: defineComponent({ props: ["selectedPath", "fileInfo"], setup: props => () => h("div", { "data-viewer-path": props.selectedPath, "data-file-previewable": String(props.fileInfo?.previewable) }) }) }));
import SkillPackageBrowser from "./SkillPackageBrowser.vue";
const binary: SkillPackageEntry = { name: "huge.zip", path: "huge.zip", kind: "file", size_bytes: 1024 ** 3, previewable: false, reason: "Binary file" };
const source: SkillPackageEntry = { name: "helper.py", path: "helper.py", kind: "file", size_bytes: 12, previewable: true, language: "python" };
const listing = (path: string, entries: SkillPackageEntry[] = [binary, source]): SkillPackageFiles => ({ relative_path: path, entries, truncated: false, limits: { max_entries: 1000, max_depth: 16 } });
const flush = async () => { for (let i = 0; i < 12; i++) await Promise.resolve(); };
const scopes: ReturnType<typeof effectScope>[] = [];
interface Controller { selectedPath: Ref<string>; treeOpen: Ref<boolean>; select(entry: SkillPackageEntry): Promise<void>; loadPreview(path: string): Promise<SkillDocumentContent>; }
function mount() {
  const props = reactive({ scopeKey: "skill-a", rootDocument: { content: "root", truncated: false }, loadFiles: vi.fn(async (path: string) => listing(path)), loadDocument: vi.fn(async (path: string) => ({ content: "source", truncated: false, relative_path: path })) });
  const scope = effectScope(); scopes.push(scope); let c!: Controller;
  scope.run(() => (SkillPackageBrowser as unknown as { setup(props: unknown, context: unknown): unknown }).setup(props, { expose(exposed: Controller) { c = exposed; } })); return { c, props };
}
afterEach(() => { for (const cleanup of hooks.cleanup.splice(0)) cleanup(); for (const scope of scopes.splice(0)) scope.stop(); vi.unstubAllGlobals(); });
describe("Skill package browser", () => {
  it("selects the root instructions by default and keeps selection when collapsing the tree", async () => {
    const { c } = mount(); await flush(); expect(c.selectedPath.value).toBe("SKILL.md"); await c.select(source); expect(c.selectedPath.value).toBe("helper.py"); c.treeOpen.value = false; c.treeOpen.value = true; expect(c.selectedPath.value).toBe("helper.py");
  });
  it("uses metadata for binary files without reading, downloading or executing them", async () => {
    const { c, props } = mount(); await flush(); await c.select(binary); const result = await c.loadPreview(binary.path); expect(result).toMatchObject({ content: "", relative_path: "huge.zip" }); expect(props.loadDocument).not.toHaveBeenCalled();
  });
  it("previews a script as static text only through the authorized content endpoint", async () => {
    const { c, props } = mount(); await flush(); await c.loadPreview(source.path); expect(props.loadDocument).toHaveBeenCalledExactlyOnceWith("helper.py");
  });
  it("resets selection for a different Skill and starts with the tree hidden on narrow screens", async () => {
    vi.stubGlobal("window", { matchMedia: () => ({ matches: true }) }); const { c, props } = mount(); await flush(); expect(c.treeOpen.value).toBe(false); await c.select(source); props.scopeKey = "skill-b"; expect(c.selectedPath.value).toBe("SKILL.md"); expect(c.treeOpen.value).toBe(false);
  });
  it("renders a separate tree and preview workspace without exposing a host path or destructive actions", async () => {
    const app = createSSRApp({ render: () => h(SkillPackageBrowser, { scopeKey: "demo", rootDocument: { content: "root", truncated: false }, loadFiles: async (path: string) => listing(path), loadDocument: async () => ({ content: "", truncated: false }) }) });
    app.component("el-icon", defineComponent({ setup: (_, context) => () => h("span", context.slots.default?.()) })); const html = await renderToString(app);
    expect(html).toContain('data-testid="skill-package-browser"'); expect(html).toContain('role="tree"'); expect(html).toContain('class="package-preview"'); expect(html).toContain('data-viewer-path="SKILL.md"'); expect(html).not.toContain("/home/"); expect(html).not.toContain("download="); expect(html).not.toContain("Delete");
  });
});
