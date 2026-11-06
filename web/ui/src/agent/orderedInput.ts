import type { Block, MessageInputPart, OfficeReferenceSnapshot } from "@/api/types";
import { parsePageElementBlocks, type ElementCardData, type ElementPick } from "./elementPicker";
import { elementComposerPart, fileComposerPart, skillComposerPart, mcpComposerPart, shortcutComposerPart, officeComposerPart, normalizeComposerParts, type ComposerPart } from "./composerParts";
import { cloneInputParts, officeSnapshotText } from "./shortcutStructure";
import type { CiteFavicons } from "./citeFavicon";
import { elementPageFavicon } from "./elementPageIcon";
import { referenceFavicon, snapshotReferenceFavicon } from "./referenceFavicon";
import { mcpInvocationText } from "./mcpInvocation";

export type OrderedDisplayPart =
  | { type: "text"; text: string }
  | { type: "skill"; name: string }
  | { type: "mcp"; server: string; name: string }
  | { type: "shortcut"; id: string; title: string; content: string; icon?: string; language?: string; parts?: MessageInputPart[] }
  | { type: "office"; office: OfficeReferenceSnapshot }
  | { type: "element"; card: ElementCardData; block: string }
  | { type: "file" | "image"; path: string; name: string };

/** Keep the offsets around observations instead of extracting all cards. */
export function orderedTextParts(text: string, icon?: string): OrderedDisplayPart[] {
  const parts: OrderedDisplayPart[] = [];
  const re = /<page-element source="[^"]*" url="[^"]*">\n[\s\S]*?\n<\/page-element>/g;
  let offset = 0;
  const matches = Array.from(text.matchAll(re));
  const favicon = matches.length === 1 ? referenceFavicon(icon) : undefined;
  for (const match of matches) {
    if (match.index! > offset) parts.push({ type: "text", text: text.slice(offset, match.index) });
    const card = parsePageElementBlocks(match[0]).cards[0];
    if (card) parts.push({ type: "element", card: favicon ? { ...card, favicon } : card, block: match[0] });
    else parts.push({ type: "text", text: match[0] });
    offset = match.index! + match[0].length;
  }
  if (offset < text.length) parts.push({ type: "text", text: text.slice(offset) });
  return parts;
}

export function orderedDisplayParts(blocks: readonly Block[]): OrderedDisplayPart[] {
  const result: OrderedDisplayPart[] = [];
  for (let index = 0; index < blocks.length; index++) {
    const block = blocks[index];
    if (block.type === "text" && block.input_shortcut) {
      const value = block.input_shortcut;
      result.push({ type: "shortcut", id: value.id, title: value.title, content: value.text, ...(value.icon ? { icon: value.icon } : {}), ...(value.language ? { language: value.language } : {}), ...(value.parts ? { parts: cloneInputParts(value.parts) } : {}) });
      if (value.parts?.length && Number.isInteger(value.block_count) && value.block_count! > 0) index += Math.min(value.block_count!, blocks.length - index - 1);
    } else if (block.type === "text" && block.input_office) result.push({ type: "office", office: block.input_office });
    else if (block.type === "text" && block.input_mcp) result.push({ type: "mcp", server: block.input_mcp.server, name: block.input_mcp.name });
    else if (block.type === "text") result.push(...(block.input_skill ? [{ type: "skill" as const, name: block.input_skill }] : orderedTextParts(block.text, block.input_element ? block.input_element_favicon : undefined)));
    else if (block.type === "file") result.push({ type: "file", path: block.file_path, name: block.file_name || baseName(block.file_path) });
    else if (block.type === "image") result.push({ type: "image", path: block.image_ref, name: block.image_name || baseName(block.image_ref) });
  }
  return result;
}

function baseName(path: string): string { return path.split("/").pop() || path; }

export function inputPartsBlocks(parts: readonly MessageInputPart[], files: ReadonlyMap<string, { name: string; image: boolean }> = new Map()): Block[] {
  return parts.flatMap((part): Block[] => {
    if (part.type === "element") { const favicon = referenceFavicon(part.favicon); return [{ type: "text", text: part.text, input_element: true, ...(favicon ? { input_element_favicon: favicon } : {}) }]; }
    if (part.type === "skill") return [{ type: "text", text: `/skill:${part.name} `, input_skill: part.name }];
    if (part.type === "mcp") return [{ type: "text", text: `${mcpInvocationText(part.server, part.name)} `, input_mcp: { server: part.server, name: part.name } }];
    if (part.type === "office") return [{ type: "text", text: officeSnapshotText(part.office), input_office: JSON.parse(JSON.stringify(part.office)) }];
    if (part.type === "shortcut") {
      const children = part.parts?.length ? inputPartsBlocks(part.parts, files) : [];
      return [{ type: "text", text: `[资料：${part.title}]\n${children.length ? "" : part.text}`, input_shortcut: { id: part.shortcut_id, title: part.title, text: part.text, ...(part.icon ? { icon: part.icon } : {}), ...(part.language ? { language: part.language } : {}), ...(children.length ? { parts: cloneInputParts(part.parts!), block_count: children.length } : {}) } }, ...children];
    }
    if (part.type === "text") return [{ type: "text", text: part.text }];
    const file = files.get(part.path);
    return file?.image && !part.as_file ? [{ type: "image", image_ref: part.path, image_name: file.name }] : [{ type: "file", file_path: part.path, file_name: file?.name || baseName(part.path) }];
  });
}

export function pickFromDisplay(part: Extract<OrderedDisplayPart, { type: "element" }>): ElementPick {
  const card = part.card;
  return { key: part.block, label: card.element || card.selector || card.title, block: part.block,
    pageTitle: card.title, pageURL: card.url, preview: card.text || card.element, hasData: !!card.data, ...(card.favicon ? { icon: card.favicon } : {}) };
}

/** Freeze display identity once. A later icon at the same URL never replaces a
 * reference's own snapshot. Legacy references may use the exact session archive. */
export async function snapshotComposerElementIcons(parts: readonly ComposerPart[], archive?: CiteFavicons | null): Promise<ComposerPart[]> {
  return Promise.all(parts.map(async part => {
    if (part.type !== "element") return { ...part };
    const card = parsePageElementBlocks(part.text).cards[0];
    const url = part.pick?.pageURL || card?.url;
    const existing = referenceFavicon(part.favicon) || referenceFavicon(part.pick?.icon);
    const source = elementPageFavicon(url, existing || part.pick?.icon, archive);
    const favicon = existing || await snapshotReferenceFavicon(source);
    if (!favicon) return { ...part };
    return { ...part, favicon, ...(part.pick ? { pick: { ...part.pick, icon: favicon } } : {}) };
  }));
}

/** Restore actual blocks, including repeated references and file positions. */
export function blocksComposerParts(blocks: readonly Block[], keyForPath: (path: string) => string = path => path): ComposerPart[] {
  return normalizeComposerParts(orderedDisplayParts(blocks).map((part): ComposerPart => {
    if (part.type === "text") return { type: "text", text: part.text };
    if (part.type === "skill") return skillComposerPart(part.name);
    if (part.type === "mcp") return mcpComposerPart(part.server, part.name);
    if (part.type === "shortcut") return shortcutComposerPart(part);
    if (part.type === "office") return officeComposerPart(part.office);
    if (part.type === "element") return elementComposerPart(pickFromDisplay(part));
    return fileComposerPart({ key: keyForPath(part.path), path: part.path, label: part.name, status: "ready", ...(part.type === "file" ? { asFile: true } : {}) });
  }));
}

/** Only the parent resolves pending uploads; a missing file must never vanish. */
export function composerInputParts(parts: readonly ComposerPart[], pathsForKey: (key: string) => (string | { path: string; as_file?: boolean })[], prefix = ""): MessageInputPart[] {
  const result: MessageInputPart[] = prefix ? [{ type: "text", text: prefix }] : [];
  for (const part of parts) {
    if (part.type === "text") result.push({ type: "text", text: part.text });
    else if (part.type === "element") {
      const favicon = referenceFavicon(part.favicon) || referenceFavicon(part.pick?.icon);
      result.push({ type: "element", text: part.text, ...(favicon ? { favicon } : {}) });
    }
    else if (part.type === "skill") result.push({ type: "skill", name: part.name });
    else if (part.type === "mcp") result.push({ type: "mcp", server: part.server, name: part.name });
    else if (part.type === "office") result.push({ type: "office", office: JSON.parse(JSON.stringify(part.office)) });
    else if (part.type === "shortcut") result.push({ type: "shortcut", shortcut_id: part.shortcutId, title: part.title, text: part.text, ...(part.parts?.length ? { parts: cloneInputParts(part.parts) } : {}), ...(part.icon ? { icon: part.icon } : {}), ...(part.language ? { language: part.language } : {}) });
    else {
      const paths = pathsForKey(part.key);
      if (!paths.length) throw new Error(`File is not ready: ${part.label}`);
      for (const path of paths) result.push(typeof path === "string"
        ? { type: "file", path, ...(part.asFile !== undefined ? { as_file: part.asFile } : {}) }
        : { type: "file", ...path, ...(path.as_file === undefined && part.asFile !== undefined ? { as_file: part.asFile } : {}) });
    }
  }
  return result.filter(part => part.type === "file" || part.type === "skill" || part.type === "mcp" || part.type === "office" || part.text !== "" || part.type === "shortcut" && !!part.parts?.length);
}
