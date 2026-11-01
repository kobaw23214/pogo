/** The real browser keeps one final layout while its shell moves on the compositor. */
export const IMMERSIVE_LAYOUT_EVENT = "pogo:immersive-layout";
export const IMMERSIVE_PRESENTED_EVENT = "pogo:immersive-presented";
export interface ImmersiveLayoutDetail {
  transitionId: string;
  phase: "start" | "commit" | "finish";
  direction: "entering" | "leaving";
  animated: boolean;
  duration: number;
  reason?: "geometry-complete" | "instant" | "cancelled";
}
export interface ImmersivePresentedDetail {
  transitionId: string;
  status: "ready" | "unavailable";
  reason: "decoded-frame" | "local-new-tab" | "desktop" | "unavailable";
  sessionId?: string;
  context_ref?: string;
  tab_id?: string;
  generation?: number;
  viewport?: { width: number; height: number };
  media_epoch?: string;
  frame_id?: number;
}
export function notifyImmersiveLayout(dock: HTMLElement, detail: ImmersiveLayoutDetail) {
  dock.dispatchEvent(new CustomEvent<ImmersiveLayoutDetail>(IMMERSIVE_LAYOUT_EVENT, { bubbles: true, detail }));
}
export function notifyImmersivePresented(dock: HTMLElement, detail: ImmersivePresentedDetail) {
  dock.dispatchEvent(new CustomEvent<ImmersivePresentedDetail>(IMMERSIVE_PRESENTED_EVENT, { bubbles: true, detail }));
}
/** A hidden pane may finish its own motion, but can never begin or resume one.
 * Clearing its active id on hide also fences any late commit from that motion. */
export function acceptsImmersivePaneLayout(visible: boolean, activeId: string | undefined, detail: ImmersiveLayoutDetail): boolean {
  if (!visible) return detail.phase === "finish" && activeId === detail.transitionId;
  return detail.phase === "start" || activeId === detail.transitionId;
}

/** getBoundingClientRect includes the FLIP scale, which is presentation only.
 * Using it as the remote viewport postpones the real resize until animation end. */
export function browserLayoutSize(element: Pick<HTMLElement, "clientWidth" | "clientHeight">) {
  return { width: element.clientWidth, height: element.clientHeight };
}
