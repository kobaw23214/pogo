// The citation source glyphs, as SVG inner markup shared two ways: the
// CiteTypeIcon component renders them into its own <svg>, and MarkdownView's
// chip renderer needs the same glyph as a plain string — chips are v-html
// output, so no component can be attached there. One table, both consumers.

const GLYPHS: Record<string, string> = {
  information: '<path d="M4 19.5A2.5 2.5 0 0 1 6.5 17H20M6.5 3H20v19H6.5A2.5 2.5 0 0 1 4 19.5v-14A2.5 2.5 0 0 1 6.5 3Z"/><path d="M8 7h8M8 11h6"/>',
  // command_exec: terminal prompt
  command_exec: '<path d="m4 17 6-6-6-6"/><path d="M12 19h8"/>',
  // file_read: document
  file_read:
    '<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/>' +
    '<path d="M14 2v6h6"/><path d="M8 13h8M8 17h5"/>',
  // file_write: pencil over document
  file_write: '<path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z"/>',
  // search_result: magnifier
  search_result: '<circle cx="11" cy="11" r="7"/><path d="m21 21-4.3-4.3"/>',
  // web_page: globe
  web_page:
    '<circle cx="12" cy="12" r="9"/><path d="M3 12h18"/>' +
    '<path d="M12 3a13.5 13.5 0 0 1 0 18a13.5 13.5 0 0 1 0-18"/>',
  // subagent: branching node
  subagent:
    '<circle cx="6" cy="6" r="2.5"/><circle cx="18" cy="18" r="2.5"/>' +
    '<path d="M8.5 6H15a3 3 0 0 1 3 3v6.5"/>',
};

// mcp_tool / tool / unknown: wrench
const FALLBACK =
  '<path d="M14.7 6.3a4.5 4.5 0 0 0-6 6L3 18l3 3 5.7-5.7a4.5 4.5 0 0 0 6-6L14 13l-3-3z"/>';

/** citeIconInner is the glyph markup without the <svg> wrapper. */
export function citeIconInner(type: string): string {
  return GLYPHS[type] ?? FALLBACK;
}

/**
 * citeIconSvg is the standalone glyph: a full inline <svg> string for v-html
 * output. Stroked with currentColor and sized in em, so the chip's own
 * font-size and theme tokens colour and scale it; the width/height attributes
 * keep it sane even where no chip stylesheet reaches (a file preview, say).
 */
export function citeIconSvg(type: string): string {
  return (
    '<svg viewBox="0 0 24 24" width="1em" height="1em" fill="none" stroke="currentColor" stroke-width="2" ' +
    'stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" class="cite-type-icon">' +
    citeIconInner(type) +
    "</svg>"
  );
}
