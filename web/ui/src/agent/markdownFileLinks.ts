import MarkdownIt from "markdown-it";
import { fileRawURL } from "@/api/client";
import { attachmentWorkspacePath } from "./officeFiles";
import { resolvedWorkspaceFileTarget, workspaceFileMatches, workspaceFileTarget, type WorkspaceFileContext, type WorkspaceFileTarget } from "./workspaceFileLinks";
import type { WorkspaceFileScope } from "./workspaceFileNavigation";

type MarkdownEngine = InstanceType<typeof MarkdownIt>;

/** Kept on the render environment so streamed segments share one link stack. */
export const workspaceFileRenderScope = Symbol("workspaceFileRenderScope");
const stackKey = Symbol("workspaceFileLinkStack");
const cardPendingKey = Symbol("workspaceFileCardPending");
type Environment = Record<string | symbol, unknown>;

interface FileLinkLabels {
  scope(): WorkspaceFileScope | null;
  openLabel(name: string): string;
  tooltip(path: string): string;
  cardKind?(kind: WorkspaceFileCardKind): string;
  cardAction?: string;
}

const icon = '<svg class="workspace-file-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M14 3H6a1 1 0 0 0-1 1v16a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8z"/><path d="M14 3v5h5M8 12h8M8 16h6"/></svg>';

/** Revalidate rendered navigation metadata without interpreting the path twice. */
export function workspaceFileTargetFromButtonData(data: Readonly<Record<string, string | undefined>>, context: WorkspaceFileContext): WorkspaceFileTarget | null {
  const target = resolvedWorkspaceFileTarget(data.workspaceFile || "", context);
  if (!target) return null;
  const line = (value: string | undefined): number | undefined => {
    if (value === undefined) return undefined;
    const number = Number(value);
    return /^\d+$/.test(value) && Number.isSafeInteger(number) && number > 0 ? number : NaN;
  };
  const start = line(data.fileLineStart), end = line(data.fileLineEnd);
  if (Number.isNaN(start) || Number.isNaN(end) || (end !== undefined && (start === undefined || end < start))) return null;
  if (start !== undefined) target.lineStart = start;
  if (end !== undefined) target.lineEnd = end;
  if (data.fileLookupByName === "1" && !target.path.includes("/")) target.lookupByName = true;
  return target;
}

export type WorkspaceFileCardKind = "pdf" | "word" | "excel" | "image" | "audio" | "video";
const cardKinds: Record<WorkspaceFileCardKind, string[]> = {
  pdf: ["pdf"],
  word: ["doc", "docx", "odt", "rtf"],
  excel: ["xls", "xlsx", "xlsm", "xlsb", "csv", "tsv"],
  image: ["png", "jpg", "jpeg", "gif", "webp", "svg", "ico", "bmp", "tiff", "tif", "avif", "heic"],
  audio: ["mp3", "wav", "ogg", "m4a", "flac", "aac", "opus"],
  video: ["mp4", "webm", "mov", "mkv", "avi", "m4v"],
};
export function workspaceFileCardKind(path: string): WorkspaceFileCardKind | null {
  const name = path.slice(path.lastIndexOf("/") + 1);
  const extension = name.slice(name.lastIndexOf(".") + 1).toLowerCase();
  for (const [kind, extensions] of Object.entries(cardKinds) as [WorkspaceFileCardKind, string[]][]) {
    if (extensions.includes(extension)) return kind;
  }
  return null;
}

export function workspaceMarkdownImageURL(href: string, origin: WorkspaceFileScope | null): string | null {
  if (!origin) return null;
  const target = workspaceFileTarget(href, origin, "href");
  if (!target || workspaceFileCardKind(target.path) !== "image") return null;
  return fileRawURL(attachmentWorkspacePath(origin.workspace, target.path));
}

/** Extend tokens rather than rewriting Markdown or scanning fenced code. */
export function installMarkdownFileLinks(md: MarkdownEngine, labels: FileLinkLabels): void {
  const esc = md.utils.escapeHtml;
  const text = md.renderer.rules.text;
  const code = md.renderer.rules.code_inline;
  const open = md.renderer.rules.link_open;
  const close = md.renderer.rules.link_close;
  const validate = md.validateLink.bind(md);

  const scope = (env: Environment): WorkspaceFileScope | null => env?.[workspaceFileRenderScope] as WorkspaceFileScope | null ?? null;
  const stack = (env: Environment): boolean[] => {
    if (!Array.isArray(env[stackKey])) env[stackKey] = [];
    return env[stackKey] as boolean[];
  };
  const location = (target: WorkspaceFileTarget) => target.path + (target.lineStart ? `:${target.lineStart}${target.lineEnd && target.lineEnd !== target.lineStart ? `-${target.lineEnd}` : ""}` : "");
  function button(target: WorkspaceFileTarget, origin: WorkspaceFileScope, content?: string, card?: WorkspaceFileCardKind): string {
    const attributes = `type="button" class="${card ? `workspace-file-card workspace-file-card-${card}` : "workspace-file-link"}" data-workspace-file="${esc(target.path)}" data-file-conversation="${esc(origin.conversationId)}" data-file-cwd="${esc(origin.cwd)}"` +
      (target.lookupByName ? ' data-file-lookup-by-name="1"' : "") +
      (target.lineStart ? ` data-file-line-start="${target.lineStart}"` : "") +
      (target.lineEnd ? ` data-file-line-end="${target.lineEnd}"` : "") +
      ` title="${esc(labels.tooltip(location(target)))}" aria-label="${esc(labels.openLabel(target.name))}"`;
    if (card) {
      const kind = labels.cardKind?.(card) ?? card;
      const action = labels.cardAction ?? "Open";
      return `<button ${attributes}>${icon}<span class="workspace-file-card-text"><strong>${esc(target.name)}</strong><span class="workspace-file-card-kind">${esc(kind)}</span></span><span class="workspace-file-card-action">${esc(action)}</span></button>`;
    }
    return `<button ${attributes}>${content ?? icon + `<span class="workspace-file-name">${esc(target.name)}</span>`}`;
  }

  // Markdown-it rejects file: by default. Accept only a destination already
  // resolved inside this conversation; it will render as a preview button.
  md.validateLink = href => validate(href) || !!(labels.scope() && workspaceFileTarget(href, labels.scope()!, "href"));

  md.renderer.rules.link_open = (tokens, idx, options, env, self) => {
    const e = env as Environment;
    const origin = scope(e);
    const href = String(tokens[idx].attrGet("href") ?? "");
    const target = origin && !href.startsWith("#cite:") ? workspaceFileTarget(href, origin, "href") : null;
    stack(e).push(!!target);
    if (target && origin) {
      const card = tokens[idx].meta?.workspaceFileCard as WorkspaceFileCardKind | undefined;
      if (card) e[cardPendingKey] = true;
      return button(target, origin, "", card);
    }
    return open ? open(tokens, idx, options, env, self) : self.renderToken(tokens, idx, options);
  };
  md.renderer.rules.link_close = (tokens, idx, options, env, self) => {
    const e = env as Environment;
    const isFile = stack(e).pop();
    delete e[cardPendingKey];
    if (isFile) return "</button>";
    return close ? close(tokens, idx, options, env, self) : self.renderToken(tokens, idx, options);
  };
  md.renderer.rules.code_inline = (tokens, idx, options, env, self) => {
    const e = env as Environment;
    if (e[cardPendingKey]) return "";
    const origin = scope(e);
    const target = origin && !stack(e).length ? workspaceFileTarget(tokens[idx].content, origin) : null;
    if (target && origin) return button(target, origin) + "</button>";
    return code ? code(tokens, idx, options, env, self) : `<code>${esc(tokens[idx].content)}</code>`;
  };
  md.renderer.rules.text = (tokens, idx, options, env, self) => {
    const e = env as Environment;
    if (e[cardPendingKey]) return "";
    const origin = scope(e);
    const matches = origin && !stack(e).length ? workspaceFileMatches(tokens[idx].content, origin) : [];
    if (!origin || !matches.length) return text ? text(tokens, idx, options, env, self) : esc(tokens[idx].content);
    let end = 0;
    let html = "";
    for (const match of matches) {
      html += esc(tokens[idx].content.slice(end, match.start)) + button(match.target, origin) + "</button>";
      end = match.end;
    }
    return html + esc(tokens[idx].content.slice(end));
  };

  md.core.ruler.push("workspace_file_cards", state => {
    for (let i = 0; i < state.tokens.length; i++) {
      const inline = state.tokens[i];
      if (inline.type !== "inline" || state.tokens[i - 1]?.type !== "paragraph_open" || state.tokens[i + 1]?.type !== "paragraph_close") continue;
      const children = inline.children ?? [];
      if (children.length !== 3 || children[0].type !== "link_open" || children[2].type !== "link_close") continue;
      const origin = scope(state.env as Environment);
      if (!origin) continue;
      const href = String(children[0].attrGet("href") ?? "");
      const target = workspaceFileTarget(href, origin, "href");
      const kind = target ? workspaceFileCardKind(target.path) : null;
      if (kind) children[0].meta = { ...children[0].meta, workspaceFileCard: kind };
    }
  });
}
