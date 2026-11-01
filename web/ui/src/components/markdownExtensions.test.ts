import { describe, expect, it } from "vitest";
import MarkdownIt from "markdown-it";
import { installMarkdownExtensions } from "./markdownExtensions";

function engine() {
  const md = new MarkdownIt({ html: false });
  installMarkdownExtensions(md);
  return md;
}

describe("Markdown document extensions", () => {
  it("renders read-only task lists without persisting interaction", () => {
    const html = engine().render("- [x] 已完成\n- [ ] 待处理");
    expect(html).toContain("task-list-item");
    expect(html).toContain("disabled checked");
    expect(html).toContain('aria-label="Not completed"');
    expect(html).not.toContain("[x]");
  });

  it("renders callouts with a title and removes the marker", () => {
    const html = engine().render("> [!WARNING]\n> 注意数据完整性。");
    expect(html).toContain("callout-warning");
    expect(html).toContain("callout-title");
    expect(html).toContain("WARNING");
    expect(html).not.toContain("[!WARNING]");
  });

  it("adds unique heading anchors and a linked table of contents", () => {
    const html = engine().render("[TOC]\n\n## 相同标题\n\n### 相同标题\n\n#### 下级");
    expect(html).toContain('id="相同标题"');
    expect(html).toContain('id="相同标题-2"');
    expect(html).toContain("markdown-toc");
    expect(html).toContain("toc-level-3");
    expect(html).toContain("heading-anchor");
  });

  it("links footnotes in both directions and escapes definition HTML", () => {
    const html = engine().render("正文[^source]\n\n[^source]: 说明 <img src=x onerror=alert(1)>");
    expect(html).toContain('href="#fn-1"');
    expect(html).toContain('href="#fnref-1-1"');
    expect(html).toContain("footnotes");
    expect(html).not.toContain("<img");
  });

  it("renders collapsible details with nested Markdown", () => {
    const html = engine().render(":::details 查看细节\n**折叠内容**\n:::");
    expect(html).toContain("<details");
    expect(html).toContain("<summary>查看细节</summary>");
    expect(html).toContain("<strong>折叠内容</strong>");
  });

  it("keeps the first footnote definition and gives repeated references unique anchors", () => {
    const html = engine().render("一[^a]二[^a]\n\n[^a]: 第一\n[^a]: 第二");
    expect(html).toContain("fnref-1-1");
    expect(html).toContain("fnref-1-2");
    expect(html).toContain("第一");
    expect(html).not.toContain("第二");
  });

  it("does not close a details block on fence markers inside code", () => {
    const html = engine().render(":::details 代码\n```text\n:::\n```\n\n正文\n:::");
    expect(html.match(/<details/g)).toHaveLength(1);
    expect(html).toContain("正文");
    expect(html).toContain("<summary>代码</summary>");
  });

  it("resolves localized labels at render time", () => {
    let english = false;
    const md = new MarkdownIt({ html: false });
    installMarkdownExtensions(md, { toc: () => english ? "Contents" : "目录" });
    expect(md.render("[TOC]\n\n## 标题")).toContain("<strong>目录</strong>");
    english = true;
    expect(md.render("[TOC]\n\n## 标题")).toContain("<strong>Contents</strong>");
  });
});
