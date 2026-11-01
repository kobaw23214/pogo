import type { InjectionKey } from "vue";
import type { ComposerPart } from "./composerParts";
/** A detached snapshot. Preparing uploads captured local bytes without sending
 * or changing the containing conversation's current draft. */
export interface ShortcutDraftSource {
  sessionId: string | null;
  parts: ComposerPart[];
  prepare?: () => Promise<ComposerPart[]>;
}
export const shortcutDraftSourceKey: InjectionKey<() => ShortcutDraftSource | null> = Symbol("shortcutDraftSource");
