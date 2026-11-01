import type { Message } from "@/api/types";
import { normalizeMessageContent } from "@/api/messageContent";

/**
 * Conversation-wide search highlighting, built on the CSS Custom Highlight
 * API.
 *
 * The sidebar search already quotes matches; this is the in-conversation
 * half: while the search box holds a query, every occurrence of its terms in
 * the open conversation stays painted, and clearing the box ends it. Two
 * deliberate choices:
 *
 *   - Ranges, not DOM edits. Assistant text renders through markdown-it into
 *     v-html, so wrapping matches in <mark> would mean re-implementing the
 *     renderer or mutating nodes under Vue's feet. A Highlight paints ranges
 *     over the live DOM without touching it — streaming, folding and virtual
 *     re-renders stay undisturbed, and ending the highlight is one delete.
 *   - Feature-detected. CSS.highlights exists on Chrome/Edge 105+, Safari
 *     17.2+, Firefox 140+. Where it is missing the conversation simply shows
 *     no paint; locating and the sidebar marks are unaffected.
 *
 * The paint is reapplied by the caller whenever the conversation changes
 * (ranges do not survive node replacement), debounced there.
 */

const HIGHLIGHT_NAME = "search";

// A runaway ceiling: searching "a" in a long session matches everywhere, and
// past a few thousand painted ranges the paint itself becomes the slowdown.
const MAX_RANGES = 2000;

function highlightRegistry(): HighlightRegistry | undefined {
  return typeof CSS !== "undefined" && "highlights" in CSS ? CSS.highlights : undefined;
}

/** messageText joins a message's text blocks, mirroring llm.Message.Text() —
 * the exact string the session index quoted its anchors from. */
export function messageText(m: Message): string {
  let s = "";
  for (const b of normalizeMessageContent(m.content)) if (b.type === "text") s += b.text;
  return s;
}

/** findAnchorMessage returns the id of the first message whose text contains
 * the anchor verbatim, or "" when none does — the anchor is a backend-cut
 * window of one message's text, so a substring test is the whole lookup. */
export function findAnchorMessage(messages: Message[], anchor: string): string {
  return findAnchorMessages(messages, anchor)[0] ?? "";
}

/** findAnchorMessages is the plural form: every message whose text holds the
 * anchor, in conversation order. The same words can appear twice — said
 * mid-work and repeated in the conclusion — so the locate walks this list to
 * land on a *visible* occurrence rather than the first (possibly folded) one. */
export function findAnchorMessages(messages: Message[], anchor: string): string[] {
  if (!anchor) return [];
  const out: string[] = [];
  for (const m of messages) {
    if (messageText(m).includes(anchor)) out.push(m.id);
  }
  return out;
}

/** applySearchHighlight paints every case-insensitive occurrence of the
 * query's whitespace-separated terms inside root. An empty query (or a
 * missing API) clears instead — clearing the search box is how the reader
 * ends the highlight. */
export function applySearchHighlight(root: HTMLElement | null, query: string): void {
  const registry = highlightRegistry();
  if (!registry) return;
  registry.delete(HIGHLIGHT_NAME);
  if (!root) return;
  const terms = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
  if (!terms.length) return;

  const ranges: Range[] = [];
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  for (let node = walker.nextNode(); node && ranges.length < MAX_RANGES; node = walker.nextNode()) {
    const text = node.textContent ?? "";
    const low = text.toLowerCase();
    for (const term of terms) {
      for (let i = low.indexOf(term); i >= 0 && ranges.length < MAX_RANGES; i = low.indexOf(term, i + term.length)) {
        const r = new Range();
        r.setStart(node, i);
        r.setEnd(node, i + term.length);
        ranges.push(r);
      }
    }
  }
  if (ranges.length) registry.set(HIGHLIGHT_NAME, new Highlight(...ranges));
}

/** clearSearchHighlight ends the paint explicitly (view switch, unmount). */
export function clearSearchHighlight(): void {
  highlightRegistry()?.delete(HIGHLIGHT_NAME);
}

/** firstHighlightRangeIn returns the first painted search range inside root,
 * in document order — the keyword's own position on screen. When the located
 * element is taller than the viewport (a long conclusion, a long prose row),
 * scrolling the element's top to the top of the scroller leaves the keyword
 * itself below the fold — found, but not on screen — so the locate lands on
 * this range instead of the element's top edge. */
export function firstHighlightRangeIn(root: HTMLElement): Range | null {
  const hl = highlightRegistry()?.get(HIGHLIGHT_NAME);
  if (!hl) return null;
  for (const r of hl) {
    if (r instanceof Range && root.contains(r.commonAncestorContainer) && [...r.getClientRects()].some(rect => rect.width > 0 && rect.height > 0)) return r;
  }
  return null;
}
