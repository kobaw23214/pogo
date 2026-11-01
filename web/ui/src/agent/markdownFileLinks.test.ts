import { describe, expect, it } from "vitest";
import MarkdownIt from "markdown-it";
import { installMarkdownFileLinks, workspaceFileRenderScope, workspaceFileTargetFromButtonData } from "./markdownFileLinks";
import type { WorkspaceFileScope } from "./workspaceFileNavigation";

const scope: WorkspaceFileScope = { conversationId: "chat-a", cwd: "/home/pogo/users/alice/project", root: "/home/pogo", workspace: "users/alice/project" };
function engine(current: WorkspaceFileScope | null = scope) {
  const md = new MarkdownIt({ html: false, linkify: true });
  installMarkdownFileLinks(md, { scope: () => current, openLabel: name => `Open ${name}`, tooltip: path => `Preview ${path}` });
  return { md, render: (source: string) => md.render(source, { [workspaceFileRenderScope]: current }) };
}

describe("Answer file paths share Markdown's token boundaries", () => {
  it("turns the historical Chinese download path into one compact preview action", () => {
    const path = "uploads/browser/6dec270ccb560b45115af050/a1a921e2-043d-43e5-8937-cd0b35b0bfbd/报送1014.xlsx";
    const html = engine().render(`最终文件：\`${path}\`（已保存，14 KB）`);
    expect(html).toContain(`data-workspace-file="${path}"`);
    expect(html).toContain('data-file-conversation="chat-a"');
    expect(html).toContain(`title="Preview ${path}"`);
    expect(html).toContain('<span class="workspace-file-name">报送1014.xlsx</span>');
    expect(html.match(/<button /g)).toHaveLength(1);
    expect(html).not.toContain("<code>");
  });
  it("marks only original bare filenames for a later scoped name lookup", () => {
    for (const source of ["`sample-a.txt`", "[打开文件](sample-a.txt)", "“sample-a.txt”"]) {
      const html = engine().render(source);
      expect(html, source).toContain('data-workspace-file="sample-a.txt"');
      expect(html, source).toContain('data-file-lookup-by-name="1"');
    }
    for (const source of ["`./sample-a.txt`", "`testdir/sample-a.txt`", "`/home/pogo/users/alice/project/sample-a.txt`", "`users/alice/project/sample-a.txt`", "[文件](file:///home/pogo/users/alice/project/sample-a.txt)", "[文件](./sample-a.txt)"]) {
      const html = engine().render(source);
      expect(html, source).toContain('data-workspace-file=');
      expect(html, source).not.toContain('data-file-lookup-by-name');
    }
  });
  it("keeps decoded bare href names and line ranges in lookup metadata", () => {
    const html = engine().render("[定位](%E4%B8%AD%E6%96%87.ts#L2-L8)");
    expect(html).toContain('data-workspace-file="中文.ts"');
    expect(html).toContain('data-file-lookup-by-name="1"');
    expect(html).toContain('data-file-line-start="2"');
    expect(html).toContain('data-file-line-end="8"');
  });
  it("handles explicit relative Markdown links without a nested file button", () => {
    const html = engine().render("[打开 `报送1014.xlsx`](uploads/browser/报送1014.xlsx)");
    expect(html.match(/<button /g)).toHaveLength(1);
    expect(html).toContain("打开 <code>报送1014.xlsx</code>");
    expect(html).not.toContain("href=\"uploads/");
    expect(html).toContain("</button>");
  });
  it("allows a scoped file URI and preserves URL escapes in an explicit link", () => {
    const html = engine().render("[表格](file:///home/pogo/users/alice/project/资料/报送%201014.xlsx)");
    expect(html).toContain('data-workspace-file="资料/报送 1014.xlsx"');
    expect(html).not.toContain('href="file:');
    const literal = engine().render("`资料/报送%20记录.xlsx`");
    expect(literal).toContain('data-workspace-file="资料/报送%20记录.xlsx"');
  });
  it("recognizes prose paths while leaving ordinary basename mentions alone", () => {
    const html = engine().render("已保存 uploads/browser/结果.xlsx。另一个结果.xlsx尚未生成。");
    expect(html.match(/<button /g)).toHaveLength(1);
    expect(html).toContain("。另一个结果.xlsx尚未生成。");
  });
  it("keeps fenced and indented code literal, including nested fences", () => {
    for (const source of ["```bash\ncat uploads/结果.xlsx\n```", "    uploads/结果.xlsx", "> ```python\n> p = 'uploads/结果.xlsx'\n> ```"]) {
      const html = engine().render(source);
      expect(html).not.toContain("data-workspace-file");
      expect(html).toContain("<code");
    }
  });
  it("never links a shell command or truncated output path", () => {
    const html = engine().render("`cd uploads/结果.xlsx`\n`uploads/.../结果.xlsx`\n`uploads/…/结果.xlsx`\n`cat uploads/结果.xlsx && rm x`");
    expect(html).not.toContain("data-workspace-file");
  });
  it("does not link files without an originating workspace", () => {
    const html = engine(null).render("`uploads/结果.xlsx` [文件](file:///home/pogo/users/alice/project/结果.xlsx)");
    expect(html).not.toContain("data-workspace-file");
    expect(html).toContain("<code>uploads/结果.xlsx</code>");
  });
  it("preserves external URL links and their labels", () => {
    const html = engine().render("[uploads/结果.xlsx](https://example.com/结果.xlsx) https://example.com/a.xlsx");
    expect(html).not.toContain("data-workspace-file");
    expect(html.match(/<a /g)).toHaveLength(2);
    expect(html).toContain("uploads/结果.xlsx</a>");
  });
  it("leaves citation rendering and the following file action independent", () => {
    const md = new MarkdownIt();
    let pending = false;
    md.renderer.rules.link_open = (tokens, idx, options, _env, self) => {
      if (String(tokens[idx].attrGet("href") ?? "").startsWith("#cite:")) { pending = true; return '<span class="cite"><a data-cite="S1">已核查来源'; }
      return self.renderToken(tokens, idx, options);
    };
    md.renderer.rules.text = (tokens, idx) => { if (pending) { pending = false; return ""; } return md.utils.escapeHtml(tokens[idx].content); };
    md.renderer.rules.link_close = () => "</a></span>";
    installMarkdownFileLinks(md, { scope: () => scope, openLabel: name => name, tooltip: path => path });
    const html = md.render("[1](#cite:S1) 后续 `uploads/结果.xlsx`", { [workspaceFileRenderScope]: scope });
    expect(html).toContain('data-cite="S1">已核查来源</a></span>');
    expect(html.match(/<button /g)).toHaveLength(1);
    expect(html).toContain("后续");
  });
  it("escapes path and scope metadata rather than enabling HTML", () => {
    const { render } = engine({ ...scope, conversationId: 'a" onclick="evil()' });
    const html = render("<img src=x onerror=evil()> `uploads/结果.xlsx`");
    expect(html).toContain("&lt;img");
    expect(html).toContain('data-file-conversation="a&quot; onclick=&quot;evil()"');
    expect(html).not.toContain('<img src=x');
    expect(engine().render("[坏链接](javascript:alert(1))")).not.toContain("<button");
  });
  it("carries a code file's line range to the existing preview", () => {
    const html = engine().render("`src/main.go:12-18`");
    expect(html).toContain('data-workspace-file="src/main.go"');
    expect(html).toContain('data-file-line-start="12"');
    expect(html).toContain('data-file-line-end="18"');
  });
  it("upgrades standalone media links to cards without changing navigation metadata", () => {
    for (const [extension, kind] of [["pdf", "pdf"], ["docx", "word"], ["xlsx", "excel"], ["png", "image"], ["mp3", "audio"], ["mp4", "video"]] as const) {
      const html = engine().render(`[打开](uploads/report.${extension})`);
      expect(html).toContain(`workspace-file-card workspace-file-card-${kind}`);
      expect(html).toContain(`data-workspace-file="uploads/report.${extension}"`);
      expect(html).toContain('data-file-conversation="chat-a"');
      expect(html).toContain('data-file-cwd="/home/pogo/users/alice/project"');
      expect(html).toContain('aria-label="Open report.' + extension + '"');
    }
  });
  it("keeps inline media references compact instead of turning them into cards", () => {
    const html = engine().render("请查看 [报告](uploads/report.pdf) 和 [音频](uploads/demo.mp3)");
    expect(html).not.toContain("workspace-file-card");
    expect(html.match(/class="workspace-file-link"/g)).toHaveLength(2);
  });
});

describe("Rendered file-button navigation metadata", () => {
  it("preserves a validated basename's lookup hint and positive line range", () => {
    expect(workspaceFileTargetFromButtonData({ workspaceFile: "中文.ts", fileLookupByName: "1", fileLineStart: "2", fileLineEnd: "8" }, scope)).toEqual({ path: "中文.ts", name: "中文.ts", lookupByName: true, lineStart: 2, lineEnd: 8 });
    // Decoded #L text is a literal filename here, never a second locator parse.
    expect(workspaceFileTargetFromButtonData({ workspaceFile: "notes.txt#L12", fileLookupByName: "1" }, scope)).toEqual({ path: "notes.txt#L12", name: "notes.txt#L12", lookupByName: true });
  });
  it("does not promote explicit nested paths or truthy attribute spellings into a name lookup", () => {
    expect(workspaceFileTargetFromButtonData({ workspaceFile: "testdir/sample-a.txt", fileLookupByName: "1" }, scope)).toEqual({ path: "testdir/sample-a.txt", name: "sample-a.txt" });
    for (const flag of [undefined, "0", "true", "yes"]) {
      expect(workspaceFileTargetFromButtonData({ workspaceFile: "sample-a.txt", fileLookupByName: flag }, scope)).toEqual({ path: "sample-a.txt", name: "sample-a.txt" });
    }
  });
  it("refuses noncanonical paths and invalid location metadata instead of opening another target", () => {
    for (const path of ["../sample-a.txt", "./sample-a.txt", "a/../sample-a.txt", "/home/pogo/users/alice/project/sample-a.txt", "sample-a.txt:12", "sample-a.txt\u0000"]) {
      expect(workspaceFileTargetFromButtonData({ workspaceFile: path, fileLookupByName: "1" }, scope), path).toBeNull();
    }
    for (const location of [{ fileLineStart: "0" }, { fileLineStart: "2.5" }, { fileLineStart: "2", fileLineEnd: "1" }, { fileLineEnd: "8" }, { fileLineStart: "9007199254740992" }]) {
      expect(workspaceFileTargetFromButtonData({ workspaceFile: "sample-a.txt", ...location }, scope)).toBeNull();
    }
    expect(workspaceFileTargetFromButtonData({ workspaceFile: "sample-a.txt" }, { cwd: "relative" })).toBeNull();
  });
});
