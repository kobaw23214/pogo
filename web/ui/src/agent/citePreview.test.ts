import { describe, expect, it } from "vitest";
import type { CiteSource } from "@/api/types";
import { safeSourceURL, sourceIdentity, sourcePreview } from "./citePreview";

const source = (over: Partial<CiteSource> & { content?: string } = {}): CiteSource & { content?: string } => ({
  label: "S3",
  type: "command_exec",
  title: "pdf-text report.pdf",
  locator: "cmd://pdf-text report.pdf",
  call_id: "call_3",
  excerpt: "报告封面",
  hash: "abc",
  ts: 1,
  ...over,
});

describe("sourcePreview", () => {
  it("finds the cited subject beyond the initial 300-character source stamp", () => {
    const content = `${"Introduction and background. ".repeat(35)}\n\nSNOWLIGHT loads the payload directly into memory.\nIt then connects to its controller.`;
    const preview = sourcePreview(source({ content }), "SNOWLIGHT loads its payload in memory.");
    expect(preview.matched).toBe(true);
    expect(preview.kind).toBe("output");
    expect(preview.text).toContain("SNOWLIGHT loads the payload");
    expect(content).toContain(preview.text);
  });

  it("matches Chinese prose while ignoring generic source/report words", () => {
    const content = "报告说明：文档来源记录。\n\n天擎和天眼等产品已支持精确检测。";
    const preview = sourcePreview(source({ content }), "报告称天擎和天眼支持精确检测。[1](#cite:S3)");
    expect(preview.matched).toBe(true);
    expect(preview.text).toBe("天擎和天眼等产品已支持精确检测。");
  });

  it("prioritizes the exact IP over shared prose and preserves explicit page markers", () => {
    const content = "# 测试1.pdf · 第 1-6 页 / 共 6 页\n===== Page 1 =====\n该报告记录了攻击工具与攻击者的通信情况。\n===== Page 6 =====\n连接地址：18.179.119.184:80\n这是提取工具观察到的文本。";
    const preview = sourcePreview(source({ content }), "报告记录了攻击工具与攻击者通信，C2 为 18.179.119.184:80。");
    expect(preview.matched).toBe(true);
    expect(preview.page).toBe(6);
    expect(preview.location).toContain("输出标记");
    expect(preview.text).toContain("18.179.119.184:80");
    expect(preview.text).not.toContain("Page 1");
    expect(content).toContain(preview.text);
  });

  it("selects identifiers and Windows paths without changing the quoted text", () => {
    const path = "D:\\project\\payload\\SharpWeb-main.exe";
    const hash = "df3bd55c46adbf13ae68b5a9b1da19a0";
    const content = `环境：Windows\n\nPDB: ${path}\nMD5: ${hash}`;
    const preview = sourcePreview(source({ content }), `文件 ${path} 的 MD5 为 ${hash}`);
    expect(preview.text).toBe(`PDB: ${path}\nMD5: ${hash}`);
    expect(preview.matched).toBe(true);
  });

  it("uses the saved excerpt honestly when full output is unavailable", () => {
    const preview = sourcePreview(source({ excerpt: "Retry timeout is 2500 milliseconds." }), "The retry timeout is 2500 milliseconds.");
    expect(preview).toMatchObject({ text: "Retry timeout is 2500 milliseconds.", matched: true, kind: "excerpt" });
    expect(preview.page).toBeUndefined();
  });

  it("returns a clearly unmatched fallback when the context has no overlap", () => {
    const preview = sourcePreview(source({ content: "Alpha documentation.\n\nUnrelated installation notes." }), "量子纠缠的测量结果");
    expect(preview.matched).toBe(false);
    expect(preview.text).toBe("Alpha documentation.\n\nUnrelated installation notes.");
  });

  it("does not manufacture a page from prose, a file heading or the answer", () => {
    const preview = sourcePreview(source({ content: "# 测试1.pdf · 第 1-6 页 / 共 6 页\nAnalysis says Page 6 contains SNOWLIGHT." }), "第 6 页列出 SNOWLIGHT");
    expect(preview.matched).toBe(true);
    expect(preview.page).toBeUndefined();
  });

  it("does not combine matching fragments across page boundaries", () => {
    const content = "===== Page 5 =====\r\nORCHID only\r\n===== Page 6 =====\r\nLOTUS only";
    const preview = sourcePreview(source({ content }), "ORCHID and LOTUS");
    expect(preview.page).toBe(5);
    expect(preview.text).toBe("ORCHID only");
  });

  it.each(["第 6 页", "page 6"])("uses %s only to break a tie between actual matching page excerpts", (hint) => {
    const content = "===== Page 4 =====\n地址：18.179.119.184:80\n===== Page 6 =====\n地址：18.179.119.184:80";
    const preview = sourcePreview(source({ content }), `18.179.119.184:80，${hint}`);
    expect(preview).toMatchObject({ matched: true, page: 6, text: "地址：18.179.119.184:80" });
  });

  it("does not let a page hint override a stronger lexical match elsewhere", () => {
    const content = "===== Page 4 =====\nSNOWLIGHT C2: 18.179.119.184:80\n===== Page 6 =====\nAddress: 18.179.119.184:80";
    const preview = sourcePreview(source({ content }), "SNOWLIGHT C2: 18.179.119.184:80，第 6 页");
    expect(preview).toMatchObject({ matched: true, page: 4 });
  });

  it("ignores nonexistent or unrelated hinted pages instead of fabricating a location", () => {
    const content = "===== Page 4 =====\n18.179.119.184:80\n===== Page 6 =====\nUnrelated notes";
    expect(sourcePreview(source({ content }), "18.179.119.184:80，第 99 页")).toMatchObject({ matched: true, page: 4 });
    expect(sourcePreview(source({ content }), "18.179.119.184:80，第 6 页")).toMatchObject({ matched: true, page: 4 });
    expect(sourcePreview(source({ content: "18.179.119.184:80" }), "18.179.119.184:80，第 6 页").page).toBeUndefined();
  });

  it("never counts a page hint alone as a lexical match", () => {
    const content = "===== Page 4 =====\nFirst source passage\n===== Page 6 =====\nSecond source passage";
    const preview = sourcePreview(source({ content }), "第 6 页 / page 6");
    expect(preview).toMatchObject({ matched: false, page: 4, text: "First source passage" });
  });

  it("matches short alphanumeric identifiers such as C2 as whole tokens", () => {
    const content = "ABC2DEF\n\nC2 observation";
    expect(sourcePreview(source({ content }), "C2")).toMatchObject({ matched: true, text: "C2 observation" });
  });

  it("puts a late C2 address and its preceding label within the first four lines", () => {
    const hashes = Array.from({ length: 5 }, (_, i) => `${i}: df3bd55c46adbf13ae68b5a9b1da19a0`).join("\n");
    const content = `===== Page 6 =====\n${hashes}\nC2:\n18.179.119.184:80\nReferences and remaining notes.`;
    const preview = sourcePreview(source({ content }), "C2 为 18.179.119.184:80，第 6 页");
    const firstLines = preview.text.split("\n").slice(0, 4).join("\n");
    expect(preview).toMatchObject({ matched: true, page: 6 });
    expect(firstLines).toContain("C2:");
    expect(firstLines).toContain("18.179.119.184:80");
    expect(preview.text).toContain("References and remaining notes.");
    expect(content).toContain(preview.text);
  });

  it("aligns late critical prose at a word boundary and retains contiguous following context", () => {
    const content = `${"Background material is provided here. ".repeat(7)}The controller address is 18.179.119.184:80 and the connection remains active. Additional context follows.`;
    const preview = sourcePreview(source({ content }), "Controller address: 18.179.119.184:80");
    expect(preview.matched).toBe(true);
    expect(preview.text.indexOf("18.179.119.184:80")).toBeLessThan(70);
    expect(preview.text).toMatch(/^(?:Background|The|material|is|provided|here|controller|address|18\.)/);
    expect(preview.text).toContain("Additional context follows.");
    expect(content).toContain(preview.text);
  });

  it("keeps original offsets when non-ASCII case folding would change string length", () => {
    const content = `${"İ😀".repeat(90)} Controller: 18.179.119.184:80. Following context.`;
    const preview = sourcePreview(source({ content }), "18.179.119.184:80");
    expect(preview.matched).toBe(true);
    expect(preview.text).toContain("18.179.119.184:80");
    expect(preview.text).not.toMatch(/^[\udc00-\udfff]|[\ud800-\udbff]$/);
    expect(content).toContain(preview.text);
  });

  it("does not treat citation IDs, page labels or partial English words as evidence", () => {
    expect(sourcePreview(source({ content: "S3 Page 6 capital" }), "[1](#cite:S3) 第 6 页 API").matched).toBe(false);
    expect(sourcePreview(source({ content: "报告内容" }), "报告").matched).toBe(false);
  });

  it("limits preview length without splitting Unicode surrogate pairs", () => {
    const content = `${"😀".repeat(300)} SNOWLIGHT ${"😀".repeat(300)}`;
    const preview = sourcePreview(source({ content }), "SNOWLIGHT");
    expect(preview.matched).toBe(true);
    expect(Array.from(preview.text).length).toBeLessThanOrEqual(420);
    expect(preview.text).not.toMatch(/^[\udc00-\udfff]|[\ud800-\udbff]$/);
    expect(content).toContain(preview.text);
  });

  it("bounds work on large outputs and does not pretend unseen text matched", () => {
    const preview = sourcePreview(source({ content: `${"a".repeat(200_000)}\n\nNEEDLE_END` }), "NEEDLE_END");
    expect(preview.matched).toBe(false);
    expect(preview.text.length).toBeLessThanOrEqual(420);
  });

  it("returns an empty unavailable excerpt without inventing fallback evidence", () => {
    expect(sourcePreview(source({ excerpt: "", content: "  " }), "anything")).toEqual({ text: "", matched: false, kind: "excerpt" });
  });
});

describe("browser recorded page excerpts", () => {
  const url = "https://example.test/plan";
  const paragraph = "青禾方案 A 的交付周期是 7 天，每月费用为 218 元，支持 4 个协作席位，不包含离线导出。";
  const plain = `青禾方案 A\n\n${paragraph}\n\n数据仅属于合成评测材料。`;
  const context = "青禾方案 A 每月 218 元，4 个协作席位。";
  const evidence = { schema: "dom-evidence-v1", status: "observed", frames: [{ url, text: paragraph, tables: [], paragraphs: [{ text: paragraph }] }] };
  const header = `runtime: native; tab: t1; version: v1\npage: ${url} · 青禾方案 A\n`;
  const envelope = (value: unknown, tail = plain) => `${header}visible DOM evidence / as-observed calculation:\n${JSON.stringify(value, null, 2)}\n${tail}`;
  const web = (content: string, extra: Partial<CiteSource> = {}) => source({ type: "web_page", title: "青禾方案 A", locator: url, content, ...extra });

  it("prefers original trailing page prose over duplicate JSON text fields", () => {
    const content = envelope(evidence);
    const preview = sourcePreview(web(content), context);
    expect(preview).toMatchObject({ text: paragraph, matched: true, browserContent: "page", kind: "output" });
    expect(preview.text).not.toMatch(/"(?:text|tables|schema)"|dom-evidence-v1/);
    expect(content).toContain(preview.text);
  });

  it("handles quoted braces and escapes without treating them as JSON boundaries", () => {
    const content = envelope({ ...evidence, note: 'quoted } ] { [ and \\ slash with "quote"' });
    expect(sourcePreview(web(content), context)).toMatchObject({ text: paragraph, browserContent: "page" });
    const crlf = envelope(evidence).replace(/\n/g, "\r\n");
    expect(sourcePreview(web(crlf), context)).toMatchObject({ text: paragraph, browserContent: "page" });
  });

  it("accepts a recorded stamp header but does not create a source position", () => {
    const preview = sourcePreview(web(`[cite:S3|web_page|青禾方案 A]\n${envelope(evidence)}`), "第 7 页，每月 218 元");
    expect(preview.browserContent).toBe("page");
    expect(preview.page).toBeUndefined();
    expect(preview.location).toBeUndefined();
  });

  it("does not extract or combine text from different frame observations", () => {
    const content = envelope({ ...evidence, frames: [
      { url, text: "ROOT_ONLY" },
      { url: "https://frame.example.test/", text: "CHILD_ONLY" },
    ] }, "ROOT_ONLY was present in the captured page body.");
    const preview = sourcePreview(web(content), "CHILD_ONLY");
    expect(preview).toMatchObject({ text: "ROOT_ONLY was present in the captured page body.", matched: false, browserContent: "page" });
    expect(preview.text).not.toContain("CHILD_ONLY");
  });

  it.each([
    ["unknown schema", envelope({ ...evidence, schema: "future-schema" })],
    ["missing schema", envelope({ text: paragraph })],
    ["malformed JSON", `${header}visible DOM evidence / as-observed calculation:\n{not JSON}\n${plain}`],
    ["truncated JSON", envelope(evidence).slice(0, 230)],
    ["no page body", envelope(evidence, "")],
    ["API response only", envelope(evidence, 'data: {"text":"API_ONLY"}')],
    ["screenshot only", envelope(evidence, "viewport screenshot: page.png (1280x720 pixels)")],
    ["different recorded URL", envelope(evidence).replace(`page: ${url}`, "page: https://elsewhere.test/page")],
    ["missing separator", envelope(evidence).replace(/}\n青禾方案 A/, "}青禾方案 A")],
  ])("keeps %s as honestly labelled browser output", (_name, content) => {
    const preview = sourcePreview(web(content), context);
    expect(preview.browserContent).toBe("output");
    expect(content).toContain(preview.text);
  });

  it("keeps errors and browser protocol replies as tool output", () => {
    expect(sourcePreview(web(envelope(evidence), { error: true }), context).browserContent).toBe("output");
    const protocol = `${header}data: {"result":{"value":"API_ONLY"}}`;
    expect(sourcePreview(web(protocol), "API_ONLY").browserContent).toBe("output");
  });

  it("does not unwrap ordinary JSON web pages, files or commands", () => {
    const json = JSON.stringify({ text: paragraph, tables: [] });
    const preview = sourcePreview(web(json), context);
    expect(preview.browserContent).toBeUndefined();
    expect(preview.text).toBe(json);
    for (const type of ["file_read", "command_exec", "mcp_tool"]) {
      expect(sourcePreview(web(envelope(evidence), { type }), context).browserContent).toBeUndefined();
    }
  });

  it("bounds JSON inspection and retains truncated output without inventing page text", () => {
    const content = envelope({ ...evidence, padding: "a".repeat(200_000) });
    const preview = sourcePreview(web(content), context);
    expect(preview.browserContent).toBe("output");
    expect(Array.from(preview.text).length).toBeLessThanOrEqual(420);
    expect(content).toContain(preview.text);
  });
});

describe("safeSourceURL", () => {
  it("allows absolute HTTP(S) URLs, preserving their path, query and fragment", () => {
    expect(safeSourceURL({ locator: "https://example.com/article?q=1#section" })).toBe("https://example.com/article?q=1#section");
    expect(safeSourceURL({ locator: " HTTP://example.com " })).toBe("http://example.com/");
  });

  it.each([
    "javascript:alert(1)", "data:text/html,test", "file:///etc/passwd", "cmd://echo test",
    "//example.com", "/local/path", "https://", "https://user:secret@example.com/",
    "https://exam\nple.com", "blob:https://example.com/a",
  ])("rejects non-web, malformed or credential-bearing locators: %s", (locator) => {
    expect(safeSourceURL({ locator })).toBeUndefined();
  });
});

describe("sourceIdentity", () => {
  it("shows the web publisher without inventing a title", () => {
    expect(sourceIdentity(source({ type: "web_page", title: "", locator: "https://www.example.com/article" }))).toEqual({
      title: "example.com", detail: "example.com", kind: "web", url: "https://www.example.com/article",
    });
  });

  it("preserves recorded file paths and keeps commands labeled as tools", () => {
    expect(sourceIdentity(source({ type: "file_read", title: "agent/source.go", locator: "file://agent/source.go#L1-40" }))).toMatchObject({
      title: "agent/source.go", detail: "agent/source.go#L1-40", kind: "file",
    });
    expect(sourceIdentity(source())).toMatchObject({ title: "pdf-text report.pdf", kind: "tool" });
  });

  it("shows the PDF filename from the known extractor envelope without inventing a target", () => {
    const src = source({
      content: "# 测试1.pdf · 第 1-6 页 / 共 6 页\n===== Page 1 =====\n报告摘要\n===== Page 6 =====\n18.179.119.184:80",
    });
    const identity = sourceIdentity(src);
    expect(identity).toEqual({ title: "测试1.pdf", detail: "PDF", kind: "file" });
    expect(identity.url).toBeUndefined();
    expect(safeSourceURL(src)).toBeUndefined();
    expect(src.type).toBe("command_exec");
    expect(src.locator).toBe("cmd://pdf-text report.pdf");
  });

  it("recognizes an intact saved PDF header and a later first nonempty page", () => {
    const identity = sourceIdentity(source({ excerpt: "# Quarterly Report.PDF · 第 1-6 页 / 共 8 页\r\n===== Page 3 =====\r\nResults" }));
    expect(identity).toEqual({ title: "Quarterly Report.PDF", detail: "PDF", kind: "file" });
  });

  it.each([
    "The document 测试1.pdf is on page 6.",
    "Summary:\n# 测试1.pdf · 第 1-6 页 / 共 6 页\n===== Page 1 =====\nContent",
    "# 测试1.pdf · 第 1-6 页 / 共 6 页",
    "# 测试1.pdf · 第 1-6 页 / 共\n===== Page 1 =====\nContent",
    "# 测试1.pdf · 第 1-6 页 / 共 6 页\nPage 1\nContent",
    "# 测试1.pdf · 第 6-1 页 / 共 6 页\n===== Page 1 =====\nContent",
    "# 测试1.pdf · 第 1-6 页 / 共 5 页\n===== Page 1 =====\nContent",
    "# 测试1.pdf · 第 1-6 页 / 共 6 页\n===== Page 7 =====\nContent",
    "# ../测试1.pdf · 第 1-6 页 / 共 6 页\n===== Page 1 =====\nContent",
  ])("does not infer a PDF identity from incomplete or inconsistent output: %s", (content) => {
    expect(sourceIdentity(source({ content })).kind).toBe("tool");
  });
});
