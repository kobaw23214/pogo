/** Observe input in same-origin Office frames without reading text or moving
 * focus. Save shortcuts use the same confirmed workspace-save path as the UI. */
export function observeOfficeInput(root: HTMLElement, callbacks: { activity(): void; composition(active: boolean): void; save(): void }): () => void {
  if (typeof root.querySelectorAll !== "function") return () => {};
  const bindings = new Map<Document, () => void>();
  const frameBindings = new Map<HTMLIFrameElement, () => void>();
  const composing = new Set<Document>();
  let stopped = false;
  const bind = (doc: Document) => {
    if (bindings.has(doc) || typeof doc.addEventListener !== "function") return;
    const activity = () => callbacks.activity();
    const start = () => { composing.add(doc); callbacks.composition(true); activity(); };
    const end = () => { composing.delete(doc); callbacks.composition(composing.size > 0); activity(); };
    const key = (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "s" && !event.altKey && !event.shiftKey && !event.isComposing && !composing.size) {
        event.preventDefault(); event.stopPropagation(); callbacks.save();
      } else if (!event.ctrlKey && !event.metaKey && !event.altKey) activity();
    };
    doc.addEventListener("keydown", key, { capture: true });
    doc.addEventListener("beforeinput", activity, { capture: true });
    doc.addEventListener("compositionstart", start, { capture: true });
    doc.addEventListener("compositionend", end, { capture: true });
    bindings.set(doc, () => { doc.removeEventListener("keydown", key, { capture: true }); doc.removeEventListener("beforeinput", activity, { capture: true }); doc.removeEventListener("compositionstart", start, { capture: true }); doc.removeEventListener("compositionend", end, { capture: true }); if (composing.delete(doc)) callbacks.composition(composing.size > 0); });
  };
  const scan = () => {
    if (stopped) return;
    const documents = new Set<Document>(), frames = new Set<HTMLIFrameElement>();
    const visit = (scope: ParentNode) => {
      if (typeof scope.querySelectorAll !== "function") return;
      for (const frame of scope.querySelectorAll<HTMLIFrameElement>("iframe")) {
        frames.add(frame);
        if (!frameBindings.has(frame)) { frame.addEventListener("load", scan); frameBindings.set(frame, () => frame.removeEventListener("load", scan)); }
        try {
          const doc = frame.contentDocument;
          if (doc && !documents.has(doc)) { documents.add(doc); bind(doc); visit(doc); }
        } catch { /* Foreign embedded pages have no workspace-save authority. */ }
      }
    };
    visit(root);
    for (const [doc, remove] of bindings) if (!documents.has(doc)) { remove(); bindings.delete(doc); }
    for (const [frame, remove] of frameBindings) if (!frames.has(frame)) { remove(); frameBindings.delete(frame); }
  };
  scan();
  // Nested frames may be added within their own document after initial load.
  const timer = setInterval(scan, 1000);
  return () => { stopped = true; clearInterval(timer); for (const remove of bindings.values()) remove(); for (const remove of frameBindings.values()) remove(); bindings.clear(); frameBindings.clear(); composing.clear(); };
}
