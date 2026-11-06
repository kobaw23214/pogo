import { nextTick, ref, watch } from "vue";
import type { WebServiceRecord } from "@/api/webServices";
import type { WebServiceLink } from "./webServiceLinks";

export interface WebServiceRevealHandle {
  readyFor(sessionId: string): boolean;
  reveal(serviceId: string, sessionId?: string): Promise<WebServiceRecord | null>;
}
interface Context { sessionId: string | null; displaySessionId: string | null; switching: boolean; widget: WebServiceRevealHandle | null; surfaceReady?: boolean; restoreSurface?: () => Promise<void> }

/** Parent stream readiness precedes a child's prop/context update. Consume the
 * URL intent only after that child has revealed the same owner-bound scope. */
export function useWebServiceLinkReveal(link: WebServiceLink, context: () => Context) {
  const revealed = ref(false);
  watch([
    () => context().sessionId,
    () => context().displaySessionId,
    () => context().switching,
    () => context().widget,
    () => { const state = context(); return !!state.sessionId && !!state.widget?.readyFor(state.sessionId); },
    () => context().surfaceReady ?? true,
  ] as const, async ([sid, display, switching, widget, ready, surfaceReady], _previous, onCleanup) => {
    if (revealed.value || !link.present || !link.serviceId || !sid || sid !== link.sessionId || sid !== display || switching || !widget || !ready) return;
    let cancelled = false;
    onCleanup(() => { cancelled = true; });
    const current = () => {
      const state = context();
      return !cancelled && state.sessionId === sid && state.displaySessionId === sid && !state.switching && state.widget === widget && widget.readyFor(sid);
    };
    if (!surfaceReady) {
      await context().restoreSurface?.();
      if (!current() || context().surfaceReady === false) return;
    }
    await nextTick();
    if (!current() || context().surfaceReady === false) return;
    await widget.reveal(link.serviceId, sid);
    if (current() && context().surfaceReady !== false) revealed.value = true;
  }, { flush: "post", immediate: true });
  return revealed;
}
