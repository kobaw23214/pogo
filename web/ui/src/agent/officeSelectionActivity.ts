/** Invalidate an asynchronous read when the user changes an Office selection.
 * Only same-origin renderer frames are observed; host toolbar clicks do not
 * count, and no event is cancelled, no text is read, and no state is written. */
export function observeOfficeSelectionActivity(root: HTMLElement, changed: () => void): () => void {
  const documents = new Map<Document, () => void>(), frames = new Map<HTMLIFrameElement, () => void>();
  let stopped = false;
  const events = ["pointerdown", "keydown", "selectionchange", "beforeinput"] as const;
  function scan() {
    if (stopped) return;
    const seenDocuments = new Set<Document>(), seenFrames = new Set<HTMLIFrameElement>();
    const visit = (scope: ParentNode) => {
      for (const frame of scope.querySelectorAll<HTMLIFrameElement>("iframe")) {
        seenFrames.add(frame);
        if (!frames.has(frame)) { frame.addEventListener("load", scan); frames.set(frame, () => frame.removeEventListener("load", scan)); }
        try {
          const doc = frame.contentDocument;
          if (!doc || seenDocuments.has(doc)) continue;
          seenDocuments.add(doc);
          if (!documents.has(doc)) {
            for (const event of events) doc.addEventListener(event, changed, { capture: true });
            documents.set(doc, () => { for (const event of events) doc.removeEventListener(event, changed, { capture: true }); });
          }
          visit(doc);
        } catch { /* A foreign document has no authority over this selection. */ }
      }
    };
    visit(root);
    for (const [doc, remove] of documents) if (!seenDocuments.has(doc)) { remove(); documents.delete(doc); }
    for (const [frame, remove] of frames) if (!seenFrames.has(frame)) { remove(); frames.delete(frame); }
  }
  scan();
  const timer = setInterval(scan, 1000);
  return () => { stopped = true; clearInterval(timer); for (const remove of documents.values()) remove(); for (const remove of frames.values()) remove(); documents.clear(); frames.clear(); };
}
