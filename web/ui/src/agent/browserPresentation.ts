/** Local pixels may bridge a media transition, but never authorize input. */
export interface BrowserPresentationBinding {
  sessionId: string; context_ref: string; tab_id: string; generation: number;
}
export interface BrowserPresentedVideo {
  binding: BrowserPresentationBinding;
  video: HTMLVideoElement;
  media_epoch: string;
  frame_id: number;
}
export interface BrowserHeldFrame {
  binding: BrowserPresentationBinding;
  width: number;
  height: number;
}
export interface BrowserPixelRect { x: number; y: number; width: number; height: number }
function usablePixelRect(value: BrowserPixelRect): boolean {
  return [value.x, value.y, value.width, value.height].every(Number.isFinite) && value.width > 0 && value.height > 0;
}
/** Locate actually painted pixels, accounting for a non-uniform ancestor FLIP.
 * Applying contain to the already transformed screen box would letterbox twice. */
export function browserPresentedPixelRect(screen: BrowserPixelRect, layout: { width: number; height: number }, intrinsic: { width: number; height: number }, fit: "contain" | "fill" = "contain"): BrowserPixelRect | null {
  if (!usablePixelRect(screen) || ![layout.width, layout.height, intrinsic.width, intrinsic.height].every(value => Number.isFinite(value) && value > 0)) return null;
  if (fit === "fill") return { ...screen };
  const scale = Math.min(layout.width / intrinsic.width, layout.height / intrinsic.height);
  const width = intrinsic.width * scale, height = intrinsic.height * scale;
  const sx = screen.width / layout.width, sy = screen.height / layout.height;
  return { x: screen.x + (layout.width - width) * .5 * sx, y: screen.y + (layout.height - height) * .5 * sy,
    width: width * sx, height: height * sy };
}
/** Inverse-map old screen pixels into a bridge canvas inside the moving dock.
 * The target's screen/layout ratio includes the ancestor's current scale. */
export function browserPixelBridgeTransform(source: BrowserPixelRect, targetScreen: BrowserPixelRect, targetLayout: { width: number; height: number }): string | null {
  if (!usablePixelRect(source) || !usablePixelRect(targetScreen) || ![targetLayout.width, targetLayout.height].every(value => Number.isFinite(value) && value > 0)) return null;
  const sx = targetScreen.width / targetLayout.width, sy = targetScreen.height / targetLayout.height;
  return `translate3d(${(source.x - targetScreen.x) / sx}px, ${(source.y - targetScreen.y) / sy}px, 0) scale(${source.width / targetScreen.width}, ${source.height / targetScreen.height})`;
}
/** Cancel only compositor geometry. Keep the frame's explicit target box so
 * interruption cannot re-contain a stale portrait image in the new aspect. */
export function cancelBrowserPixelGeometry(animations: readonly (Animation | undefined)[], page: HTMLElement | undefined, origin: string) {
  for (const animation of animations) animation?.cancel();
  if (page) { page.style.transformOrigin = origin; delete page.dataset.pixelPageBridge; }
}
export function browserHeldFrameMatches(frame: BrowserHeldFrame | undefined, current: BrowserPresentationBinding | null): boolean {
  return !!frame && !!current && frame.binding.sessionId === current.sessionId && frame.binding.context_ref === current.context_ref &&
    frame.binding.tab_id === current.tab_id && frame.binding.generation === current.generation;
}
/** Called when suspending or retiring a presented video, before stopping it.
 * Capturing once at the transition avoids a per-frame canvas readback at 60fps. */
export function captureBrowserHeldFrame(presented: BrowserPresentedVideo | undefined, currentVideo: HTMLVideoElement | null, canvas: HTMLCanvasElement | null, current: BrowserPresentationBinding | null): BrowserHeldFrame | null {
  if (!presented || !currentVideo || !canvas || presented.video !== currentVideo || !browserHeldFrameMatches({ binding: presented.binding, width: 1, height: 1 }, current)) return null;
  const width = currentVideo.videoWidth, height = currentVideo.videoHeight;
  if (currentVideo.readyState < 2 || ![width, height].every(n => Number.isSafeInteger(n) && n > 0 && n <= 8192) || width * height > 8_294_400) return null;
  try {
    // Do not overwrite a previous held image unless the current video can be
    // copied. This does not encode pixels or request another network frame.
    const sample = canvas.ownerDocument.createElement("canvas");
    sample.width = width; sample.height = height;
    const source = sample.getContext("2d"), destination = canvas.getContext("2d");
    if (!source || !destination) return null;
    source.drawImage(currentVideo, 0, 0, width, height);
    canvas.width = width; canvas.height = height;
    destination.drawImage(sample, 0, 0);
    return { binding: { ...presented.binding }, width, height };
  } catch { return null; }
}
