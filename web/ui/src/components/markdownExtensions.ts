import MarkdownIt from "markdown-it";
import type { StateBlock, StateCore, StateInline, Token } from "markdown-it";

type MarkdownEngine = InstanceType<typeof MarkdownIt>;
interface HeadingEntry {
  level: number;
  slug: string;
  text: string;
}
interface FootnoteState {
  definitions: Map<string, string>;
  references: string[];
  occurrences: Record<string, number>;
}
interface ExtensionEnv {
  headings?: HeadingEntry[];
  footnotes?: FootnoteState;
}
type LocalizedText = string | (() => string);
export interface MarkdownExtensionLabels {
  toc: LocalizedText;
  details: LocalizedText;
  completed: LocalizedText;
  uncompleted: LocalizedText;
  backToText: LocalizedText;
  headingLink: LocalizedText;
  footnote: LocalizedText;
}
type ResolvedMarkdownExtensionLabels = Record<keyof MarkdownExtensionLabels, string>;
function text(value: LocalizedText): string { return typeof value === "function" ? value() : value; }
const defaultLabels: MarkdownExtensionLabels = {
  toc: "Contents",
  details: "Details",
  completed: "Completed",
  uncompleted: "Not completed",
  backToText: "Back to content",
  headingLink: "Link to this heading",
  footnote: "Footnote",
};

function envFootnotes(env: ExtensionEnv, create = true): FootnoteState | undefined {
  if (!env.footnotes && create) {
    env.footnotes = { definitions: new Map(), references: [], occurrences: {} };
  }
  return env.footnotes;
}

function headingText(token: Token): string {
  return (token.children ?? [])
    .filter(child => child.type === "text" || child.type === "code_inline")
    .map(child => child.content)
    .join("")
    .trim();
}

function slugify(value: string): string {
  return value
    .normalize("NFKC")
    .toLowerCase()
    .replace(/[^\p{Letter}\p{Number}\s_-]/gu, "")
    .trim()
    .replace(/\s+/g, "-")
    .replace(/-+/g, "-") || "section";
}

function footnoteDefinition(state: StateBlock, startLine: number, endLine: number, silent: boolean): boolean {
  const start = state.bMarks[startLine] + state.tShift[startLine];
  const line = state.src.slice(start, state.eMarks[startLine]);
  const match = line.match(/^\[\^([A-Za-z0-9_-]+)\]:[ \t]*(.*)$/);
  if (!match) return false;
  if (silent) return true;
  const notes = envFootnotes(state.env as ExtensionEnv)!;
  let content = match[2];
  let nextLine = startLine + 1;
  while (nextLine < endLine) {
    const indent = state.tShift[nextLine] - state.blkIndent;
    if (indent < 2) break;
    const lineStart = state.bMarks[nextLine] + state.tShift[nextLine];
    content += `\n${state.src.slice(lineStart, state.eMarks[nextLine]).trim()}`;
    nextLine++;
  }
  if (!notes.definitions.has(match[1])) notes.definitions.set(match[1], content.trim());
  state.line = nextLine;
  return true;
}

function footnoteReference(state: StateInline, silent: boolean): boolean {
  if (!state.src.startsWith("[^", state.pos)) return false;
  const end = state.src.indexOf("]", state.pos + 2);
  if (end < 0) return false;
  const id = state.src.slice(state.pos + 2, end);
  if (!/^[A-Za-z0-9_-]+$/.test(id)) return false;
  const notes = envFootnotes(state.env as ExtensionEnv)!;
  if (!notes.definitions.has(id)) return false;
  if (!silent) {
    let index = notes.references.indexOf(id);
    if (index < 0) {
      notes.references.push(id);
      index = notes.references.length - 1;
    }
    const occurrence = (notes.occurrences[id] ?? 0) + 1;
    notes.occurrences[id] = occurrence;
    const token = state.push("footnote_ref", "", 0);
    token.meta = { id, index, occurrence };
  }
  state.pos = end + 1;
  return true;
}

function tocBlock(state: StateBlock, startLine: number, _endLine: number, silent: boolean): boolean {
  const start = state.bMarks[startLine] + state.tShift[startLine];
  const line = state.src.slice(start, state.eMarks[startLine]).trim();
  if (!/^\[TOC\]$/i.test(line) && !/^\[\[toc\]\]$/i.test(line)) return false;
  if (silent) return true;
  const token = state.push("toc", "", 0);
  token.block = true;
  token.map = [startLine, startLine + 1];
  state.line = startLine + 1;
  return true;
}

function detailsBlock(state: StateBlock, startLine: number, endLine: number, silent: boolean, labels: ResolvedMarkdownExtensionLabels): boolean {
  const start = state.bMarks[startLine] + state.tShift[startLine];
  const first = state.src.slice(start, state.eMarks[startLine]).trim();
  const match = first.match(/^:::+\s*details(?:\s+(.*))?$/i);
  if (!match) return false;
  let closeLine = startLine + 1;
  let fenceChar = "";
  let fenceLength = 0;
  for (; closeLine < endLine; closeLine++) {
    const lineStart = state.bMarks[closeLine] + state.tShift[closeLine];
    const line = state.src.slice(lineStart, state.eMarks[closeLine]).trim();
    const marker = line.match(/^(`+|~+)/);
    if (marker) {
      const char = marker[1][0];
      if (!fenceChar) {
        fenceChar = char;
        fenceLength = marker[1].length;
      } else if (char === fenceChar && marker[1].length >= fenceLength) {
        fenceChar = "";
        fenceLength = 0;
      }
      continue;
    }
    if (!fenceChar && /^:::+\s*$/.test(line)) break;
  }
  if (closeLine >= endLine) return false;
  if (silent) return true;

  const open = state.push("details_open", "details", 1);
  open.block = true;
  open.attrJoin("class", "markdown-details");
  const summary = state.push("html_block", "", 0);
  summary.block = true;
  summary.content = `<summary>${state.md.utils.escapeHtml(match[1]?.trim() || labels.details)}</summary>\n`;
  state.md.block.tokenize(state, startLine + 1, closeLine);
  const close = state.push("details_close", "details", -1);
  close.block = true;
  state.line = closeLine + 1;
  return true;
}

function enhanceDocument(state: StateCore, labels: ResolvedMarkdownExtensionLabels): void {
  const env = state.env as ExtensionEnv;
  const headings: HeadingEntry[] = [];
  const used = new Map<string, number>();
  for (let i = 0; i < state.tokens.length - 2; i++) {
    const open = state.tokens[i];
    if (open.type !== "heading_open") continue;
    const text = headingText(state.tokens[i + 1]);
    const base = slugify(text);
    const count = (used.get(base) ?? 0) + 1;
    used.set(base, count);
    const slug = count === 1 ? base : `${base}-${count}`;
    open.attrSet("id", slug);
    open.meta = { ...open.meta, slug, text };
    state.tokens[i + 2].meta = { ...state.tokens[i + 2].meta, slug, text };
    const level = Number(open.tag.slice(1));
    if (level >= 2 && level <= 4) headings.push({ level, slug, text });
  }
  env.headings = headings;

  for (let i = 0; i < state.tokens.length - 2; i++) {
    const container = state.tokens[i];
    const paragraph = state.tokens[i + 1];
    const inline = state.tokens[i + 2];
    if (paragraph.type !== "paragraph_open" || inline.type !== "inline") continue;

    if (container.type === "list_item_open") {
      const match = inline.content.match(/^\[([ xX])\][ \t]+/);
      if (!match) continue;
      container.attrJoin("class", "task-list-item");
      inline.content = inline.content.slice(match[0].length);
      if (inline.children?.[0]?.type === "text") {
        inline.children[0].content = inline.children[0].content.slice(match[0].length);
      }
      const marker = new MarkdownIt.Token("html_inline", "", 0);
      const checked = match[1].toLowerCase() === "x";
      marker.content = `<input class="task-list-checkbox" type="checkbox" disabled${checked ? " checked" : ""} aria-label="${checked ? labels.completed : labels.uncompleted}">`;
      inline.children = [marker, ...(inline.children ?? [])];
    }

    if (container.type === "blockquote_open") {
      const match = inline.content.match(/^\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\][ \t]*(?:\n|$)/i);
      if (!match) continue;
      const kind = match[1].toLowerCase();
      container.attrJoin("class", `callout callout-${kind}`);
      inline.content = inline.content.slice(match[0].length);
      if (inline.children?.[0]?.type === "text") {
        inline.children[0].content = inline.children[0].content.slice(match[0].length);
      }
      const title = new MarkdownIt.Token("html_inline", "", 0);
      title.content = `<span class="callout-title">${match[1].toUpperCase()}</span>`;
      inline.children = [title, ...(inline.children ?? [])];
    }
  }

  const notes = envFootnotes(env, false);
  if (notes?.references.length) {
    const items = notes.references.map((id, index) => {
      const number = index + 1;
      const definition = state.md.utils.escapeHtml(notes.definitions.get(id) ?? "");
      return `<li id="fn-${number}"><span>${definition}</span> <a class="footnote-backref" href="#fnref-${number}-1" aria-label="${state.md.utils.escapeHtml(labels.backToText)}">↩</a></li>`;
    }).join("");
    const section = new MarkdownIt.Token("html_block", "", 0);
    section.block = true;
    section.content = `<section class="footnotes"><hr><ol>${items}</ol></section>\n`;
    state.tokens.push(section);
  }
}

export function installMarkdownExtensions(md: MarkdownEngine, provided: Partial<MarkdownExtensionLabels> = {}): void {
  const labels = new Proxy({ ...defaultLabels, ...provided }, {
    get: (target, property: keyof MarkdownExtensionLabels) => text(target[property]),
  }) as unknown as ResolvedMarkdownExtensionLabels;
  md.block.ruler.before("reference", "footnote_definition", footnoteDefinition, { alt: ["paragraph", "reference"] });
  md.inline.ruler.after("escape", "footnote_ref", footnoteReference);
  md.block.ruler.before("fence", "markdown_toc", tocBlock, { alt: ["paragraph", "reference", "blockquote", "list"] });
  md.block.ruler.before("blockquote", "markdown_details", (state, startLine, endLine, silent) => detailsBlock(state, startLine, endLine, silent, labels), { alt: ["paragraph", "reference", "blockquote", "list"] });
  md.core.ruler.push("markdown_document_extensions", state => enhanceDocument(state, labels));

  md.renderer.rules.heading_close = (tokens, idx) => {
    const token = tokens[idx];
    const slug = String(token.meta?.slug ?? "");
    if (!slug) return `</${token.tag}>`;
    return `<a class="heading-anchor" href="#${md.utils.escapeHtml(slug)}" aria-label="${md.utils.escapeHtml(labels.headingLink)}">#</a></${token.tag}>`;
  };
  md.renderer.rules.toc = (_tokens, _idx, _options, env) => {
    const headings = (env as ExtensionEnv).headings ?? [];
    if (!headings.length) return "";
    const items = headings.map(item =>
      `<li class="toc-level-${item.level}"><a href="#${md.utils.escapeHtml(item.slug)}">${md.utils.escapeHtml(item.text)}</a></li>`,
    ).join("");
    const label = md.utils.escapeHtml(labels.toc);
    return `<nav class="markdown-toc" aria-label="${label}"><strong>${label}</strong><ol>${items}</ol></nav>`;
  };
  md.renderer.rules.footnote_ref = (tokens, idx) => {
    const meta = tokens[idx].meta as { index: number; occurrence: number };
    const number = meta.index + 1;
    return `<sup id="fnref-${number}-${meta.occurrence}" class="footnote-ref"><a href="#fn-${number}" aria-label="${md.utils.escapeHtml(labels.footnote)} ${number}">[${number}]</a></sup>`;
  };
}
