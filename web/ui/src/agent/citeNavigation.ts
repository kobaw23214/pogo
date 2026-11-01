import type { InjectionKey } from "vue";
import type { CiteSource } from "@/api/types";

export interface CiteNavigation {
  canOpen(src: CiteSource): boolean;
  open(src: CiteSource): void;
}

/** The containing session supplies its own workspace-aware file opener. */
export const citeNavigationKey: InjectionKey<CiteNavigation> = Symbol("citeNavigation");

/** Only host-stamped metadata can open a fixed library version, never a model URL. */
export function informationCitationTarget(src: CiteSource): { id: string; version: number } | null {
  const stamp = src.information;
  if (src.type !== "information" || !stamp || !/^inf_[a-f0-9]{32}$/.test(stamp.entry_id)
    || !Number.isSafeInteger(stamp.version) || stamp.version < 1 || !/^[a-f0-9]{64}$/.test(stamp.snapshot_sha256)) return null;
  if (src.locator !== `information://${stamp.entry_id}?version=${stamp.version}#${stamp.snapshot_sha256}`) return null;
  return { id: stamp.entry_id, version: stamp.version };
}

export interface CitationFileTarget {
  /** Path relative to the originating session workspace, never an OS path. */
  path: string;
  name: string;
  /** One-based, inclusive line range recorded by the file-read tool. */
  lineStart?: number;
  lineEnd?: number;
}

/**
 * File stamps use `file://` followed by a raw workspace-relative path, not a
 * browser file URL. Do not URL-parse or decode them: a space, # or literal %20
 * belongs to the filename. The file API performs its own permission checks.
 */
export function citationFileTarget(src: CiteSource): CitationFileTarget | null {
  if (src.type !== "file_read" && src.type !== "file_write") return null;
  if (!src.locator.startsWith("file://")) return null;
  let path = src.locator.slice("file://".length);
  let lineStart: number | undefined;
  let lineEnd: number | undefined;

  // source.go stores the plain path as the title and adds a line suffix only
  // to a read locator. Matching both fields disambiguates a real range from
  // a filename that itself ends in #L10-14. An ambiguous # remains literal.
  if (src.type === "file_read") {
    const range = /#L([1-9]\d*)(?:-([1-9]\d*))?$/.exec(path);
    if (range) {
      const first = Number(range[1]);
      const last = Number(range[2] ?? range[1]);
      const candidate = path.slice(0, range.index);
      if (Number.isSafeInteger(first) && Number.isSafeInteger(last) && last >= first && src.title === candidate) {
        path = candidate;
        lineStart = first;
        lineEnd = last;
      }
    }
  }

  // Normalize separators only after the locator has been read literally.
  // Absolute/UNC paths, drive paths, URI/ADS syntax, traversal and controls
  // are deliberately not offered as clickable local file targets.
  path = path.replace(/\\/g, "/");
  if (!path || path.startsWith("/") || /[:\u0000-\u001f\u007f]/.test(path)) return null;
  // No supported stamp URI-encodes separators or traversal. Reject their
  // encoded forms as well instead of relying on a downstream decoder count.
  if (/%(?:0[0-9a-f]|1[0-9a-f]|25|2e|2f|3a|5c|7f)/i.test(path)) return null;
  const parts = path.split("/");
  if (parts.some((part) => !part || /^[. ]+$/.test(part))) return null;
  return { path, name: parts[parts.length - 1], ...(lineStart === undefined ? {} : { lineStart, lineEnd }) };
}
