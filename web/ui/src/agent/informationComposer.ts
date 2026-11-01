import type { AttachmentMeta, MessageInputPart } from "@/api/types";
import { blocksComposerParts, inputPartsBlocks } from "./orderedInput";
import { validateInputParts } from "./shortcutStructure";
import type { ComposerPart } from "./composerParts";

/** The server resolves library assets into this session before they enter a draft. */
export function informationComposerParts(parts: MessageInputPart[], attachments: AttachmentMeta[]): ComposerPart[] {
  if (validateInputParts(parts)) throw new Error("Invalid information reference");
  const files = new Map(attachments.map(file => [file.path, { name: file.name, image: file.kind === "image" }]));
  const result = blocksComposerParts(inputPartsBlocks(parts, files));
  const wireFiles = parts.filter(part => part.type === "file");
  let index = 0;
  for (const part of result) if (part.type === "file") part.asFile = wireFiles[index++]?.as_file;
  return result;
}

/** A late file copy must not insert into a different session or newer draft. */
export function informationInsertionCurrent(origin: { session: string; draft: string }, session: string | null, draft: string, closed: boolean): boolean {
  return !closed && !!session && origin.session === session && origin.draft === draft;
}
