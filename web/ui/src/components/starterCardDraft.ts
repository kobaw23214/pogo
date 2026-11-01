import type { InputLayout, MessageInputPart, StarterCard } from "@/api/types";
import { composerDisplayText, normalizeComposerParts, type ComposerPart } from "@/agent/composerParts";
import { blocksComposerParts, composerInputParts, inputPartsBlocks } from "@/agent/orderedInput";

/** A detached copy: saving a shortcut never owns or clears the live composer. */
export interface StarterDraft {
  parts: ComposerPart[];
  inputLayout: InputLayout;
}

export interface PreparedStarterDraft {
  parts: MessageInputPart[];
  input_layout: InputLayout;
  prompt?: string;
}

export type PrepareStarterDraft = (draft: StarterDraft) => Promise<PreparedStarterDraft>;

export function cloneStarterDraft(draft: StarterDraft): StarterDraft {
  return { inputLayout: draft.inputLayout, parts: normalizeComposerParts(JSON.parse(JSON.stringify(draft.parts))) };
}

/** Reuse the message parser, so editing and filling share the same chip labels. */
export function starterCardComposerParts(card: StarterCard): ComposerPart[] {
  const restored = blocksComposerParts(card.parts?.length ? inputPartsBlocks(card.parts) : [{ type: "text", text: card.prompt ?? "" }]);
  // File blocks alone cannot distinguish an image attachment from an explicit
  // image-as-file choice. Keep the wire flag per occurrence, including repeated
  // paths whose two occurrences intentionally make different choices.
  const files = card.parts?.filter((part): part is Extract<MessageInputPart, { type: "file" }> => part.type === "file") ?? [];
  let fileIndex = 0;
  return restored.map(part => {
    if (part.type !== "file") return part;
    const original = files[fileIndex++];
    const { asFile: _asFile, ...file } = part;
    return original?.as_file ? { ...file, asFile: true } : file;
  });
}

export function starterDraftTitle(draft: StarterDraft): string {
  return composerDisplayText(draft.parts).replace(/\s+/g, " ").trim().slice(0, 40);
}

/** Existing stable files can be edited without an upload callback. */
export function prepareStableStarterDraft(draft: StarterDraft): PreparedStarterDraft {
  const parts = composerInputParts(draft.parts, key => {
    const file = draft.parts.find(part => part.type === "file" && part.key === key);
    if (!file || file.type !== "file" || !file.path || file.status === "uploading" || file.status === "error") return [];
    // The caller supplies the occurrence's image-as-file choice. A shared upload
    // identity must not copy the first occurrence's choice onto later ones.
    return [file.path];
  });
  return { parts, input_layout: draft.inputLayout, prompt: composerDisplayText(draft.parts) };
}

export function starterDraftHasContent(draft: StarterDraft): boolean {
  return draft.parts.some(part => part.type !== "text" || part.text.trim().length > 0);
}
