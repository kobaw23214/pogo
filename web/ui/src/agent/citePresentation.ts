import type { CiteSource } from "@/api/types";
import { gt } from "@/i18n";
import { citationFileTarget } from "./citeNavigation";
import { sourceIdentity } from "./citePreview";

export interface CitePresentation {
  /** Complete source identity, retained in the tooltip and accessible label. */
  title: string;
  /** Human-readable inline identity; CSS may truncate it, never the location. */
  label: string;
  /** A recorded, validated position. Never inferred from the answer or output. */
  location?: string;
  /** Shared CiteTypeIcon / citeIconSvg glyph key. */
  iconType: string;
}

function fallbackTitle(type: string): string {
  if (type === "information") return gt("cite.informationType");
  const keys: Record<string, string> = {
    file_read: "fileType",
    file_write: "fileType",
    command_exec: "commandType",
    search_result: "searchType",
    mcp_tool: "connectedToolType",
    subagent: "agentType",
  };
  const key = keys[type] ?? "toolType";
  return gt(`cite.${key}`);
}

/**
 * A citation identifies the material first, while its number still belongs to
 * the answer's citation plan. Display metadata does not grant navigation: only
 * citationFileTarget and safeSourceURL validate destinations.
 */
export function citePresentation(src: CiteSource): CitePresentation {
  const file = citationFileTarget(src);
  if (file) {
    const location = file.lineStart === undefined ? undefined
      : file.lineEnd !== undefined && file.lineEnd !== file.lineStart
        ? `:${file.lineStart}–${file.lineEnd}` : `:${file.lineStart}`;
    return { title: file.path, label: file.name, ...(location ? { location } : {}), iconType: src.type };
  }

  const identity = sourceIdentity(src);
  if (identity.kind === "web" && identity.url) {
    const host = new URL(identity.url).host.replace(/^www\./i, "");
    const title = src.title.trim();
    // Browser stamps sometimes use the URL as their fallback title. Repeating
    // it after the host adds no identity, and can hide a useful host on mobile.
    const named = title && title !== src.locator.trim() && title !== identity.url && title !== host;
    return {
      title: named ? `${title} · ${identity.url}` : identity.url,
      // Keep the sentence readable. Page titles belong to the detail card;
      // the real host identifies the publisher without guessing a brand from
      // untrusted page titles (or hiding meaningful subdomains).
      label: host,
      iconType: "web_page",
    };
  }

  // The PDF extractor's intact envelope can identify a document. Its page
  // markers remain output text, so they must not become a native page target.
  if (src.type === "command_exec" && identity.kind === "file" && identity.detail === "PDF") {
    return { title: identity.title, label: identity.title, iconType: "file_read" };
  }

  const title = src.title.trim()
    || (src.locator.startsWith("file://") ? src.locator.slice("file://".length) : "")
    || fallbackTitle(src.type);
  let label = title;
  if (src.type === "file_read" || src.type === "file_write") {
    // Unsafe/ambiguous paths may still be identified, but do not acquire a line
    // target. In particular, leave a literal filename ending in #L10 intact.
    label = title.split(/[\\/]/).filter(Boolean).pop() || title;
  } else if (src.type === "command_exec") {
    // Older persisted stamps prefixed their title with execution status. It
    // stays in the full identity; the short name foregrounds the command.
    label = title.replace(/^exit\s+-?\d+\s*[·|]\s*/i, "") || title;
  }
  return { title, label, iconType: src.type };
}
