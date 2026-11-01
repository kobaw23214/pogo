import { describe, expect, it, vi } from "vitest";
import { createSSRApp, defineComponent, h, ref } from "vue";
import { renderToString } from "vue/server-renderer";
import type { MessageInputPart } from "@/api/types";
import { inputPartsText } from "@/agent/shortcutStructure";
vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string) => key }) }));
vi.mock("@/i18n", () => ({ gt: (key: string) => key }));
vi.mock("@/agent/useCitePopover", () => ({ useCitePopover: () => ({ bindRoot() {}, card: ref(null), cardId: "test-citation", dismissCard() {}, cancelHide() {}, scheduleHide() {}, onCardBlur() {}, onLocate() {}, canLocate: false, canOpenFile: false, onOpenFile() {}, positionCard() {} }) }));
vi.mock("@/agent/useInlineCiteFavicons", () => ({ useInlineCiteFavicons() {} }));
vi.mock("./CodeBlock.vue", () => ({ default: defineComponent({ props: ["code", "lang", "uncollapsed", "lineNumbers"], setup: props => () => h("pre", { "data-language": props.lang, "data-uncollapsed": String(!!props.uncollapsed), "data-line-numbers": String(!!props.lineNumbers) }, props.code) }) }));
vi.mock("./MermaidDiagram.vue", () => ({ default: defineComponent({ setup: () => () => h("div", { "data-diagram": "mermaid" }) }) }));
vi.mock("./EChartsDiagram.vue", () => ({ default: defineComponent({ setup: () => () => h("div", { "data-diagram": "echarts" }) }) }));
vi.mock("./ShortcutLanguageSelect.vue", () => ({ default: defineComponent({ setup: () => () => h("span") }) }));
vi.mock("./OrderedMessageContent.vue", () => ({ default: defineComponent({ props: ["blocks", "parseSkill"], setup: props => () => h("span", { "data-reference": true, "data-parse-skill": String(props.parseSkill) }, JSON.stringify(props.blocks)) }) }));
import MarkdownView from "./MarkdownView.vue";
import AnswerText from "./AnswerText.vue";
import ShortcutContent from "./ShortcutContent.vue";
import ShortcutDetails from "./ShortcutDetails.vue";
async function render(component: object, props: Record<string, unknown>) { return renderToString(createSSRApp({ render: () => h(component, props) })); }

describe("Markdown shortcut display", () => {
  it("renders actual headings, formatting, lists and tables through the shared safe Markdown renderer", async () => {
    const source = "## 数据说明\n\n**重点**与`字段`\n\n- 第一项\n- 第二项\n\n| 名称 | 值 |\n| --- | --- |\n| 数据库 | example |";
    const html = await render(ShortcutContent, { content: source, language: "markdown" });
    expect(html).toContain('<h2 id="数据说明">数据说明'); expect(html).toContain('class="heading-anchor"'); expect(html).toContain("<strong>重点</strong>");
    expect(html).toContain("<code>字段</code>"); expect(html).toContain("<ul>"); expect(html).toContain("<table>");
    expect(html).not.toContain('data-language="markdown"');
    expect(source).toContain("## 数据说明");
  });
  it("keeps text and other source formats as literal code without converting stored text", async () => {
    for (const language of ["text", "yml", "bash", "properties"]) {
      const html = await render(ShortcutContent, { content: "# 保留原文\nkey=value", language });
      expect(html).toContain(`data-language="${language}"`); expect(html).toContain("# 保留原文"); expect(html).not.toContain("<h1>");
    }
  });
  it("uses the same Markdown rule in card/information details and preserves ordered nested reference contents", async () => {
    const parts: MessageInputPart[] = [
      { type: "text", text: "## 前文\n\n**开始**" },
      { type: "file", path: "uploads/资料.csv" },
      { type: "text", text: "## 后文" },
      { type: "skill", name: "office" },
      { type: "shortcut", shortcut_id: "child", title: "嵌套资料", text: "### 嵌套标题\n\n**内容**", language: "markdown" },
    ];
    const before = JSON.stringify(parts), raw = inputPartsText(parts);
    const html = await render(ShortcutDetails, { title: "有序资料", content: raw, language: "markdown", parts });
    const markers = ['<h2 id="前文">前文', "资料.csv", '<h2 id="后文">后文', "office", '<h3 id="嵌套标题">嵌套标题'];
    let previous = -1; for (const marker of markers) { const index = html.indexOf(marker); expect(index).toBeGreaterThan(previous); previous = index; }
    expect(html).toContain('data-parse-skill="false"'); expect(html).toContain("<strong>内容</strong>");
    expect(JSON.stringify(parts)).toBe(before); expect(inputPartsText(parts)).toBe(raw);
  });
});

describe("Passive Markdown boundary and default compatibility", () => {
  const source = '![remote](https://example.invalid/private.png)\n\n[网站](https://example.invalid/) [1](#cite:S1)\n\n<script>alert(1)</script>\n\n```mermaid\ngraph TD; A-->B\n```\n\n```echarts\n{"series":[]}\n```\n\n```bash\necho preserved\n```';
  it("does not mount diagrams, load images, create citation actions or interpret raw HTML in passive materials", async () => {
    const html = await render(MarkdownView, { source, passive: true, fileLinks: true });
    expect(html).not.toContain("<img"); expect(html).not.toContain("<script>"); expect(html).toContain("&lt;script&gt;");
    expect(html).toContain('class="markdown-image-alt">remote</span>');
    expect(html).not.toContain("data-diagram"); expect(html).not.toContain("data-cite-anchor"); expect(html).not.toContain("data-workspace-file");
    expect(html).toContain('target="_blank" rel="noopener noreferrer"');
    for (const language of ["mermaid", "echarts", "bash"]) expect(html).toContain(`data-language="${language}" data-uncollapsed="true" data-line-numbers="false"`);
    expect(html).toContain("echo preserved");
  });
  it("preserves ordinary answer images, diagrams, citation anchors and code behavior when passive is omitted", async () => {
    const html = await render(MarkdownView, { source });
    expect(html).toContain('<img src="https://example.invalid/private.png"');
    expect(html).toContain('data-diagram="mermaid"'); expect(html).toContain('data-diagram="echarts"');
    expect(html).toContain('data-cite-anchor="0"');
    expect(html).toContain('data-language="bash" data-uncollapsed="false" data-line-numbers="true"');
  });
  it("reuses the answer typography wrapper while forwarding passive safety only for saved materials", async () => {
    const passive = await render(AnswerText, { source, passive: true });
    expect(passive).toContain('class="markdown-view answer"');
    expect(passive).not.toContain("<img"); expect(passive).not.toContain("data-diagram"); expect(passive).not.toContain("data-cite-anchor");
    const answer = await render(AnswerText, { source });
    expect(answer).toContain('<img src="https://example.invalid/private.png"');
    expect(answer).toContain('data-diagram="mermaid"'); expect(answer).toContain('data-cite-anchor="0"');
  });
  it("keeps unsafe markdown URI schemes inert and escapes image-alt HTML", async () => {
    const html = await render(MarkdownView, { source: '[x](javascript:alert(1))\n\n![<img onerror=alert(1)>](https://example.invalid/a)', passive: true });
    expect(html).not.toContain('href="javascript:'); expect(html).not.toContain("<img"); expect(html).toContain("&lt;img onerror=alert(1)&gt;");
  });
});
