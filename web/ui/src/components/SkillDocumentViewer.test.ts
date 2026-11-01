import { describe, expect, it, vi } from "vitest";
import { createSSRApp, defineComponent, h, ref } from "vue";
import { renderToString } from "vue/server-renderer";
vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string) => key }) }));
vi.mock("@/i18n", () => ({ gt: (key: string) => key }));
vi.mock("@/agent/useCitePopover", () => ({ useCitePopover: () => ({ bindRoot() {}, card: ref(null), cardId: "skill-citation", dismissCard() {}, cancelHide() {}, scheduleHide() {}, onCardBlur() {}, onLocate() {}, canLocate: false, canOpenFile: false, onOpenFile() {}, positionCard() {} }) }));
vi.mock("@/agent/useInlineCiteFavicons", () => ({ useInlineCiteFavicons() {} }));
vi.mock("./CodeBlock.vue", () => ({ default: defineComponent({ props: ["code", "lang"], setup: props => () => h("pre", { "data-language": props.lang }, props.code) }) }));
vi.mock("./MermaidDiagram.vue", () => ({ default: defineComponent({ setup: () => () => h("div", { "data-diagram": "mermaid" }) }) }));
vi.mock("./EChartsDiagram.vue", () => ({ default: defineComponent({ setup: () => () => h("div", { "data-diagram": "echarts" }) }) }));
import SkillDocumentViewer from "./SkillDocumentViewer.vue";
import MarkdownView from "./MarkdownView.vue";
const render = (component: object, props: Record<string, unknown>) => { const app = createSSRApp({ render: () => h(component, props) }); app.component("el-icon", defineComponent({ setup: (_, context) => () => h("span", context.slots.default?.()) })); return renderToString(app); };
describe("Skill document preview rendering", () => {
  it("renders relative links as local controls rather than malformed application URLs", async () => {
    const html = await render(SkillDocumentViewer, { scopeKey: "yx", rootDocument: { content: '---\nname: yx\n---\n# 流程\n\n[SQL](references/sql/workflow.md) [字段](../contract.json) [小节](#步骤) [外链](https://example.test/doc)', truncated: false }, loadDocument: vi.fn() });
    expect(html).toContain('<h1 id="流程">流程'); expect(html).not.toContain("name: yx"); expect(html).toContain('data-document-href="references/sql/workflow.md"'); expect(html).toContain('data-document-href="#%E6%AD%A5%E9%AA%A4"');
    expect(html).not.toMatch(/<a[^>]*\shref="references\//); expect(html).not.toContain("/manage/references/");
    expect(html).toContain('href="https://example.test/doc" target="_blank" rel="noopener noreferrer"'); expect(html).toContain("SKILL.md");
  });
  it("shows JSON through the shared code viewer, never as HTML", async () => {
    const html = await render(SkillDocumentViewer, { scopeKey: "json", rootDocument: { relative_path: "contract.json", language: "json", content: '{"example":"<img onerror=alert(1)>"}', truncated: true }, loadDocument: vi.fn() });
    expect(html).toContain('data-language="json"'); expect(html).toContain("&lt;img onerror=alert(1)&gt;"); expect(html).not.toContain("<img"); expect(html).toContain("skillDocuments.truncated");
  });
  it("does not execute source HTML, fetch remote images or render active diagrams", async () => {
    const html = await render(SkillDocumentViewer, { scopeKey: "safe", rootDocument: { content: '<script>alert(1)</script>\n\n![x](https://example.test/private.png)\n\n```mermaid\ngraph TD; A-->B\n```', truncated: false }, loadDocument: vi.fn() });
    expect(html).not.toContain("<script>"); expect(html).toContain("&lt;script&gt;"); expect(html).not.toContain("<img"); expect(html).not.toContain("data-diagram");
  });
  it("leaves normal Markdown link behavior unchanged outside the Skill viewer", async () => {
    const html = await render(MarkdownView, { source: "[普通链接](references/workflow.md)" });
    expect(html).toContain('href="references/workflow.md"'); expect(html).not.toContain("data-document-href");
  });
  it("shows unsupported-file metadata explicitly without embedding or downloading the file", async () => {
    const html = await render(SkillDocumentViewer, { scopeKey: "binary", rootDocument: { relative_path: "archive.zip", content: "", truncated: false }, fileInfo: { name: "archive.zip", path: "archive.zip", kind: "file", size_bytes: 104857600, previewable: false, media_type: "application/zip", reason: "二进制文件无法预览" }, loadDocument: vi.fn() });
    expect(html).toContain('data-testid="skill-file-unavailable"'); expect(html).toContain("archive.zip"); expect(html).toContain("100.0 MiB"); expect(html).toContain("application/zip"); expect(html).toContain("二进制文件无法预览"); expect(html).not.toContain("<iframe"); expect(html).not.toContain("download=");
  });
  it("offers a source editing mode only in managed detail while keeping the default rendered preview", async () => {
    const save = vi.fn();
    const html = await render(SkillDocumentViewer, { scopeKey: "editable", managed: true, rootDocument: { content: "---\nname: demo\n---\n# 已保存说明", editable: true, revision: "rev-a", truncated: false }, loadDocument: vi.fn(), saveDocument: save });
    expect(html).toContain('data-testid="skill-document-preview"'); expect(html).toContain('data-testid="skill-document-edit"'); expect(html).toContain('<h1 id="已保存说明">已保存说明');
    expect(html).not.toContain('data-testid="skill-document-source"'); expect(html).not.toContain("name: demo"); expect(save).not.toHaveBeenCalled();
  });
  it("shows the reason for a managed read-only bundle and disables editing", async () => {
    const html = await render(SkillDocumentViewer, { scopeKey: "runtime", managed: true, rootDocument: { content: "# 工具说明", editable: false, revision: "rev-a", edit_disabled_reason: "运行时包不能直接修改", truncated: false }, loadDocument: vi.fn(), saveDocument: vi.fn() });
    expect(html).toContain('data-testid="skill-document-read-only"'); expect(html).toContain("运行时包不能直接修改");
    expect(html).toMatch(/<button[^>]*disabled[^>]*data-testid="skill-document-edit"/); expect(html).not.toContain('data-testid="skill-document-source"');
  });
  it("keeps Agent and session instruction viewers read-only even if content metadata says editable", async () => {
    const html = await render(SkillDocumentViewer, { scopeKey: "agent-session", rootDocument: { content: "# 说明", editable: true, revision: "rev-a", truncated: false }, loadDocument: vi.fn() });
    expect(html).not.toContain('data-testid="skill-document-edit"'); expect(html).not.toContain('data-testid="skill-document-save"'); expect(html).not.toContain("<textarea");
  });
});
