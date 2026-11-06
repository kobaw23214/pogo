import { describe, expect, it } from "vitest";
import ts from "typescript";
import { parse } from "@vue/compiler-sfc";
import { parse as parseTemplate, NodeTypes, type TemplateChildNode } from "@vue/compiler-dom";

const files = import.meta.glob<string>(["../**/*.ts", "../**/*.vue", "!../i18n/**", "!../**/*.test.ts"], { eager: true, query: "?raw", import: "default" });
const han = /\p{Script=Han}/u;
// These parse source-language data or immutable authored content, not UI copy.
const dataFiles: Record<string, string> = {
  "../agent/citePreview.ts": "Chinese evidence stopwords used to match original sources",
  "../agent/hostStatusCopy.ts": "Exact host reason matching before translation",
  "../agent/hostWorkflowDiagnostic.ts": "Exact host hint matching before translation",
  "../components/starterSample.ts": "Authored prompts exported as a sample document",
};
const protocolStrings: Record<string, readonly string[]> = {
  "../agent/composerParts.ts": ["[资料："],
  "../agent/orderedInput.ts": ["[资料："],
  "../agent/shortcutStructure.ts": ["[Office选区：", "；版本 ", "；来源 信息库 ", "；保存版本 ", "；只读选取；资料快照 ", "；包含未保存编辑", "[资料："],
  "../agent/toolPresentation.ts": ["用户拒绝了这次调用"],
  "../components/SentinelSheet.vue": ["[Pogo 哨兵任务]"],
};
function scriptCopy(source: string, path: string): string[] {
  const found: string[] = [];
  const ast = ts.createSourceFile(path, source, ts.ScriptTarget.Latest, true);
  function visit(node: ts.Node) {
    if (ts.isStringLiteralLike(node) || ts.isTemplateHead(node) || ts.isTemplateMiddle(node) || ts.isTemplateTail(node)) {
      if (han.test(node.text) && !protocolStrings[path]?.includes(node.text)) found.push(node.text);
    }
    ts.forEachChild(node, visit);
  }
  visit(ast);
  return found;
}
function templateCopy(source: string, path: string): string[] {
  const found: string[] = [];
  function visit(node: TemplateChildNode) {
    if (node.type === NodeTypes.TEXT && han.test(node.content)) found.push(node.content.trim());
    if (node.type === NodeTypes.INTERPOLATION) found.push(...scriptCopy(node.content.loc.source, path));
    if (node.type === NodeTypes.ELEMENT) {
      for (const prop of node.props) {
        if (prop.type === NodeTypes.ATTRIBUTE && prop.value && han.test(prop.value.content)) found.push(prop.value.content);
        if (prop.type === NodeTypes.DIRECTIVE && prop.exp) found.push(...scriptCopy(prop.exp.loc.source, path));
      }
      node.children.forEach(visit);
    }
  }
  parseTemplate(source).children.forEach(visit);
  return found;
}
describe("new product copy cannot bypass translation tables", () => {
  it("has no unreviewed Chinese literals in UI templates or script labels", () => {
    const remaining: string[] = [];
    for (const [path, source] of Object.entries(files)) {
      if (dataFiles[path]) continue;
      if (path.endsWith(".vue")) {
        const { descriptor } = parse(source);
        for (const block of [descriptor.script, descriptor.scriptSetup]) if (block) remaining.push(...scriptCopy(block.content, path).map(text => `${path}: ${text}`));
        if (descriptor.template) remaining.push(...templateCopy(descriptor.template.content, path).map(text => `${path}: ${text}`));
      } else remaining.push(...scriptCopy(source, path).map(text => `${path}: ${text}`));
    }
    expect(remaining).toEqual([]);
  });
});
