import { inject, nextTick, onBeforeUnmount, onMounted, ref, useId, watch, type ComputedRef } from "vue";
import type { CiteSource } from "@/api/types";
import type { CiteCardItem } from "./cite";
import { sourcePreview, type SourcePreview } from "./citePreview";
import { citeNavigationKey } from "./citeNavigation";
import { canJumpToCite, jumpToCite } from "./citeJump";

interface CitationProps { source: string; sources?: CiteSource[]; streaming?: boolean }
interface CardState { items: CiteCardItem[]; left: number; top: number; above: boolean; pinned: boolean }
interface AnchorState { index: string; label: string; labels: string[]; key: string }
const OPEN_EVENT = "pogo:citation-open";

/** Resolve anchors after each Markdown render instead of retaining detached nodes. */
export function useCitePopover(props: CitationProps, numbers: ComputedRef<Map<string, number>>) {
  const root = ref<HTMLElement | null>(null);
  const card = ref<CardState | null>(null);
  const cardId = "citation-" + useId();
  const navigation = inject(citeNavigationKey, null);
  let active: AnchorState | null = null;
  let showTimer: number | undefined;
  let hideTimer: number | undefined;
  let suppressFocus = false;
  let hoverCapable = false;
  let restoreTriggerFocus = false;
  const previews = new Map<string, { content?: string; excerpt: string; context: string; value: SourcePreview }>();
  function bindRoot(element: unknown) { root.value = element instanceof HTMLElement ? element : null; }

  function eventAnchor(e: Event): HTMLAnchorElement | null {
    const a = (e.target as Element | null)?.closest?.(".cite a[data-cite-anchor]");
    return a instanceof HTMLAnchorElement && root.value?.contains(a) ? a : null;
  }
  function describeAnchor(a: HTMLAnchorElement): AnchorState {
    const label = (a.getAttribute("href") ?? "").slice("#cite:".length);
    const labels = [...new Set((a.closest(".cite")?.getAttribute("data-cite-group") ?? label).split(" "))];
    const index = a.dataset.citeAnchor ?? "";
    return { index, label, labels, key: index + ":" + label };
  }
  function findAnchor(): HTMLAnchorElement | null {
    if (!active) return null;
    const candidates = root.value?.querySelectorAll<HTMLAnchorElement>("a[data-cite-anchor]") ?? [];
    for (const a of candidates) {
      if ((a.dataset.citeMembers ?? a.dataset.citeAnchor ?? "").split(" ").includes(active.index)
        && describeAnchor(a).labels.includes(active.label)) return a;
    }
    return null;
  }
  function cardElement(): HTMLElement | null { return document.getElementById(cardId); }
  function inCard(target: EventTarget | null): boolean { return target instanceof Node && !!cardElement()?.contains(target); }
  function scope(): ParentNode { return root.value?.closest(".conv-inner, .subsheet, .share-page") ?? document; }

  function contextFor(a: HTMLAnchorElement): string {
    const block = a.closest("tr, p, li, blockquote") ?? a.parentElement;
    if (!block) return "";
    if (block.tagName === "TR") {
      const copy = block.cloneNode(true) as Element;
      copy.querySelectorAll(".cite").forEach((c) => c.remove());
      return copy.textContent?.trim() ?? "";
    }
    const range = document.createRange();
    range.selectNodeContents(block);
    const chip = a.closest(".cite")!;
    const previous = [...block.querySelectorAll(".cite")].filter((c) => c !== chip && !!(c.compareDocumentPosition(chip) & Node.DOCUMENT_POSITION_FOLLOWING)).pop();
    if (previous) range.setStartAfter(previous);
    range.setEndBefore(chip);
    return range.toString().trim();
  }
  function resolve(a: HTMLAnchorElement): CiteCardItem[] {
    const context = contextFor(a);
    return describeAnchor(a).labels.map((label) => {
      const src = props.sources?.find((s) => s.label === label);
      if (!src) return { label, num: numbers.value.get(label), state: props.streaming ? "pending" : "unavailable" };
      let cached = previews.get(label);
      if (!cached || cached.content !== src.content || cached.excerpt !== src.excerpt || cached.context !== context) {
        cached = { content: src.content, excerpt: src.excerpt, context, value: sourcePreview(src, context) };
        previews.set(label, cached);
      }
      return { label, src, num: numbers.value.get(label), state: "ready", preview: cached.value };
    });
  }
  function markAnchor(a: HTMLAnchorElement | null) {
    root.value?.querySelectorAll('.cite a[aria-expanded="true"]').forEach((other) => other.setAttribute("aria-expanded", "false"));
    a?.setAttribute("aria-expanded", "true");
  }
  function closeCard(restoreFocus = false) {
    const anchor = restoreFocus ? findAnchor() : null;
    window.clearTimeout(showTimer);
    window.clearTimeout(hideTimer);
    showTimer = undefined;
    hideTimer = undefined;
    markAnchor(null);
    card.value = null;
    active = null;
    previews.clear();
    if (anchor) {
      suppressFocus = true;
      anchor.focus({ preventScroll: true });
      suppressFocus = false;
    }
  }
  function positionCard() {
    if (!card.value) return;
    const anchor = findAnchor();
    if (!anchor) { closeCard(); return; }
    const rect = anchor.getBoundingClientRect();
    if (!card.value.pinned && (rect.bottom < 0 || rect.top > window.innerHeight)) { closeCard(); return; }
    const width = Math.min(360, window.innerWidth - 24);
    const height = Math.min(cardElement()?.getBoundingClientRect().height || 290, window.innerHeight - 24);
    const below = window.innerHeight - rect.bottom;
    const above = below < height + 8 && rect.top > below;
    card.value.left = Math.max(12, Math.min(rect.left, window.innerWidth - width - 12));
    card.value.top = Math.max(12, Math.min(above ? rect.top - height - 8 : rect.bottom + 8, window.innerHeight - height - 12));
    card.value.above = above;
  }
  function show(pinned: boolean) {
    const anchor = findAnchor();
    if (!anchor) { closeCard(); return; }
    window.clearTimeout(showTimer);
    window.clearTimeout(hideTimer);
    window.dispatchEvent(new CustomEvent(OPEN_EVENT, { detail: cardId }));
    card.value = { items: resolve(anchor), left: 12, top: 12, above: false, pinned };
    markAnchor(anchor);
    positionCard();
    void nextTick(positionCard);
  }
  function scheduleShow(a: HTMLAnchorElement, delay = 240) {
    const target = describeAnchor(a);
    cancelHide();
    if (active && a === findAnchor() && (card.value || showTimer !== undefined)) return;
    closeCard();
    active = target;
    showTimer = window.setTimeout(() => { showTimer = undefined; show(false); }, delay);
  }
  function cancelHide() { window.clearTimeout(hideTimer); }
  function scheduleHide() {
    if (card.value?.pinned) return;
    if (!card.value) { closeCard(); return; }
    window.clearTimeout(hideTimer);
    hideTimer = window.setTimeout(() => {
      if (document.activeElement === findAnchor() || inCard(document.activeElement)) return;
      closeCard();
    }, 300);
  }
  function onMouseOver(e: MouseEvent) {
    const a = eventAnchor(e);
    if (!a) return;
    if (e.relatedTarget instanceof Node && a.contains(e.relatedTarget)) return;
    scheduleShow(a);
  }
  function onMouseOut(e: MouseEvent) {
    const a = eventAnchor(e);
    if (!a || inCard(e.relatedTarget) || (e.relatedTarget instanceof Node && a.contains(e.relatedTarget))) return;
    scheduleHide();
  }
  function onFocusIn(e: FocusEvent) {
    if (suppressFocus) return;
    const a = eventAnchor(e);
    if (a) scheduleShow(a, 0);
  }
  function onFocusOut(e: FocusEvent) {
    if (!inCard(e.relatedTarget)) scheduleHide();
  }
  function onCardBlur(e: FocusEvent) {
    if (!inCard(e.relatedTarget) && !(e.relatedTarget instanceof Node && findAnchor()?.contains(e.relatedTarget))) scheduleHide();
  }
  function onClick(e: MouseEvent) {
    const a = eventAnchor(e);
    if (!a) return;
    e.preventDefault();
    active = describeAnchor(a);
    // A single local file is an actionable destination. Hover/focus still
    // previews the recorded excerpt; activating the link opens its file and
    // recorded range. Groups need the chooser, and unavailable destinations
    // retain the same source-details fallback (including shared answers).
    const src = active.labels.length === 1 ? props.sources?.find((s) => s.label === active!.label) : undefined;
    if (src && canOpenFile(src)) {
      onOpenFile(src);
      return;
    }
    show(true);
    if (e.detail === 0) void nextTick(() => cardElement()?.querySelector<HTMLElement>("button:not(:disabled), a[href]")?.focus());
  }
  function onKeyDown(e: KeyboardEvent) {
    const a = eventAnchor(e);
    if (!a || e.key !== "ArrowDown") return;
    // Enter follows an available file destination. ArrowDown keeps the saved
    // evidence and its actions reachable without a pointer or a trip through
    // the rest of the document to the teleported dialog.
    e.preventDefault();
    active = describeAnchor(a);
    show(true);
    void nextTick(() => cardElement()?.querySelector<HTMLElement>("button:not(:disabled), a[href]")?.focus());
  }
  function onDocumentClick(e: MouseEvent) {
    if (!active || inCard(e.target)) return;
    const a = eventAnchor(e);
    if (a && a === findAnchor()) return;
    closeCard();
  }
  function onDocumentKey(e: KeyboardEvent) {
    if (e.key === "Escape" && active) { e.preventDefault(); closeCard(true); }
  }
  function onOtherCard(e: Event) { if ((e as CustomEvent).detail !== cardId) closeCard(); }
  function onScroll(e: Event) { if (!inCard(e.target)) positionCard(); }
  function canLocate(label: string) { return canJumpToCite(label, scope()); }
  function onLocate(label: string) { const targetScope = scope(); if (jumpToCite(label, targetScope)) closeCard(); }
  function canOpenFile(src: CiteSource) {
    return !!root.value?.closest(".conv-inner") && !root.value.closest(".subsheet, .share-page") && !!navigation?.canOpen(src);
  }
  function onOpenFile(src: CiteSource) { if (canOpenFile(src)) { closeCard(); navigation?.open(src); } }
  function dismissCard() { closeCard(true); }

  watch([() => props.source, () => props.sources], () => {
    restoreTriggerFocus = !!active && document.activeElement === findAnchor();
  }, { flush: "pre" });
  watch([() => props.source, () => props.sources, () => props.streaming], ([text], [previous]) => {
    if (!active) return;
    if (previous && !text.startsWith(previous)) { closeCard(); return; }
    const anchor = findAnchor();
    if (!anchor) { closeCard(); return; }
    if (card.value) { card.value.items = resolve(anchor); markAnchor(anchor); positionCard(); }
    if (restoreTriggerFocus) {
      suppressFocus = true;
      anchor.focus({ preventScroll: true });
      suppressFocus = false;
      restoreTriggerFocus = false;
    }
  }, { flush: "post" });

  onMounted(() => {
    hoverCapable = window.matchMedia("(hover: hover) and (pointer: fine)").matches;
    const el = root.value;
    if (!el) return;
    el.addEventListener("click", onClick);
    el.addEventListener("keydown", onKeyDown);
    el.addEventListener("focusin", onFocusIn);
    el.addEventListener("focusout", onFocusOut);
    if (hoverCapable) { el.addEventListener("mouseover", onMouseOver); el.addEventListener("mouseout", onMouseOut); }
    document.addEventListener("click", onDocumentClick, true);
    document.addEventListener("keydown", onDocumentKey);
    window.addEventListener(OPEN_EVENT, onOtherCard);
    window.addEventListener("scroll", onScroll, true);
    window.addEventListener("resize", positionCard);
  });
  onBeforeUnmount(() => {
    closeCard();
    const el = root.value;
    el?.removeEventListener("click", onClick);
    el?.removeEventListener("keydown", onKeyDown);
    el?.removeEventListener("focusin", onFocusIn);
    el?.removeEventListener("focusout", onFocusOut);
    el?.removeEventListener("mouseover", onMouseOver);
    el?.removeEventListener("mouseout", onMouseOut);
    document.removeEventListener("click", onDocumentClick, true);
    document.removeEventListener("keydown", onDocumentKey);
    window.removeEventListener(OPEN_EVENT, onOtherCard);
    window.removeEventListener("scroll", onScroll, true);
    window.removeEventListener("resize", positionCard);
  });
  return { bindRoot, card, cardId, closeCard, dismissCard, cancelHide, scheduleHide, onCardBlur, onLocate, canLocate, canOpenFile, onOpenFile, positionCard };
}
