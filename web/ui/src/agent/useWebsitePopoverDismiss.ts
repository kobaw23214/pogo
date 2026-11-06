import { onScopeDispose, type Ref } from "vue";

/** Deliberate page input dismisses the card; restoring an Office iframe's
 * focus is not an outside click and must not cancel an owner-link reveal. */
export function useWebsitePopoverDismiss(expanded: Ref<boolean>, trigger: Ref<HTMLElement | null>, content: Ref<HTMLElement | null>) {
  if (typeof document === "undefined") return;
  const page = document, capture = { capture: true };
  const outside = (event: Event) => {
    if (!expanded.value) return;
    const path = event.composedPath();
    if (trigger.value && path.includes(trigger.value) || content.value && path.includes(content.value)) return;
    expanded.value = false;
  };
  const keyboard = (event: KeyboardEvent) => { if (expanded.value && event.key === "Escape") expanded.value = false; };
  page.addEventListener("pointerdown", outside, capture);
  page.addEventListener("click", outside, capture);
  page.addEventListener("keydown", keyboard, capture);
  onScopeDispose(() => {
    page.removeEventListener("pointerdown", outside, capture);
    page.removeEventListener("click", outside, capture);
    page.removeEventListener("keydown", keyboard, capture);
  });
}
