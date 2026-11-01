import katex from "katex";
import type MarkdownIt from "markdown-it";
import type { StateBlock, StateInline } from "markdown-it";

type MarkdownEngine = InstanceType<typeof MarkdownIt>;
const MAX_SOURCE_LENGTH = 16 * 1024;

export function renderMath(source: string, displayMode: boolean): string {
  if (!source.trim() || source.length > MAX_SOURCE_LENGTH) throw new Error("invalid math source");
  return katex.renderToString(source, {
    displayMode,
    throwOnError: true,
    strict: "warn",
    trust: false,
    maxSize: 100,
    maxExpand: 100,
  });
}

function inlineMath(state: StateInline, silent: boolean): boolean {
  if (state.src.charCodeAt(state.pos) !== 0x24) return false;
  let cursor = state.pos + 1;
  let closing = -1;
  while (cursor < state.posMax) {
    const code = state.src.charCodeAt(cursor);
    if (code === 0x0a) return false;
    if (code === 0x24 && state.src.charCodeAt(cursor - 1) !== 0x5c) {
      closing = cursor;
      break;
    }
    if (code === 0x5c) cursor++;
    cursor++;
  }
  if (closing < 0) return false;
  const source = state.src.slice(state.pos + 1, closing);
  if (!source.trim() || /^\s|\s$/.test(source) || /\d/.test(state.src[closing + 1] ?? "")) return false;
  if (source.length > MAX_SOURCE_LENGTH) return false;
  if (!silent) {
    const token = state.push("math_inline", "", 0);
    token.content = source;
    token.markup = "$";
  }
  state.pos = closing + 1;
  return true;
}

function blockMath(state: StateBlock, startLine: number, endLine: number, silent: boolean): boolean {
  const start = state.bMarks[startLine] + state.tShift[startLine];
  const first = state.src.slice(start, state.eMarks[startLine]).trim();
  if (!first.startsWith("$$")) return false;
  if (silent) return true;

  let content = "";
  let nextLine = startLine + 1;
  if (first.length > 2) {
    if (first.length < 4 || !first.endsWith("$$")) return false;
    content = first.slice(2, -2).trim();
  } else {
    let closed = false;
    for (; nextLine < endLine; nextLine++) {
      const lineStart = state.bMarks[nextLine] + state.tShift[nextLine];
      const line = state.src.slice(lineStart, state.eMarks[nextLine]).trim();
      if (line === "$$") {
        closed = true;
        break;
      }
    }
    if (!closed) return false;
    content = state.getLines(startLine + 1, nextLine, state.blkIndent, false).trim();
    nextLine++;
  }
  if (!content) return false;
  const token = state.push("math_block", "", 0);
  token.block = true;
  token.content = content;
  token.map = [startLine, nextLine];
  state.line = nextLine;
  return true;
}

export function installMarkdownMath(md: MarkdownEngine): void {
  const esc = md.utils.escapeHtml;
  md.inline.ruler.after("escape", "math_inline", inlineMath);
  md.block.ruler.before("fence", "math_block", blockMath, { alt: ["paragraph", "reference", "blockquote", "list"] });
  md.renderer.rules.math_inline = (tokens, idx) => {
    const source = tokens[idx].content;
    try {
      return renderMath(source, false);
    } catch {
      return `<code class="math-inline-error">$${esc(source)}$</code>`;
    }
  };
  md.renderer.rules.math_block = (tokens, idx) => {
    const source = tokens[idx].content;
    try {
      return `<div class="math-block">${renderMath(source, true)}</div>`;
    } catch {
      return `<div class="math-block math-block-error"><code>${esc(source)}</code></div>`;
    }
  };
}
