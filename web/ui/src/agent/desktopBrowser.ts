import type { NativeBrowserPick, NativeBrowserRect, NativeBrowserState, NativeBrowserTarget } from "./nativeBrowser";
import { nativeElementPick, nativePickMatches } from "./nativeBrowser";

export interface DesktopBrowserBinding extends NativeBrowserTarget { session_id: string }
export interface DesktopBrowserPresentation {
  bounds: NativeBrowserRect;
  visible: boolean;
  blocked: boolean;
  lease_token?: string;
  control_epoch?: number;
  generation?: number;
}
export type DesktopBrowserEvent = DesktopBrowserBinding & { surface_id: string } & (
  | { type: "state"; state: NativeBrowserState }
  | { type: "pick"; pick: NativeBrowserPick }
  | { type: "picker-ended" }
  | { type: "error"; message: string }
);
/** Only a trusted desktop preload exposes this interface to the app shell.
 * Shape detection is capability negotiation, not a security boundary: the host
 * validates the shell sender, binding and authoritative lease on every input.
 * No arbitrary URL, JavaScript, CDP method or file operation is exposed here. */
export interface DesktopBrowserBridge {
  readonly version: 1;
  attach(request: DesktopBrowserBinding & { view_id: string }): Promise<{ surface_id: string }>;
  update(request: DesktopBrowserPresentation & { surface_id: string; revision: number }): Promise<{ interactive: boolean }>;
  detach(request: { surface_id: string }): Promise<void>;
  openDevTools(request: { surface_id: string }): Promise<void>;
  setPicker(request: { surface_id: string; enabled: boolean }): Promise<void>;
  onEvent(listener: (event: DesktopBrowserEvent) => void): () => void;
}
declare global { interface Window { pogoDesktopBrowser?: DesktopBrowserBridge } }

export function desktopBrowserBridge(candidate: unknown): DesktopBrowserBridge | undefined {
  if (!candidate || typeof candidate !== "object") return;
  const bridge = candidate as DesktopBrowserBridge;
  if (bridge.version !== 1 || [bridge.attach, bridge.update, bridge.detach, bridge.openDevTools, bridge.setPicker, bridge.onEvent].some(method => typeof method !== "function")) return;
  return bridge;
}
export function desktopEventMatches(event: DesktopBrowserEvent, binding: DesktopBrowserBinding, surface: string): boolean {
  return !!event && !!surface && event.surface_id === surface && event.session_id === binding.session_id && event.context_ref === binding.context_ref && event.tab_id === binding.tab_id;
}
export function desktopElementPick(pick: NativeBrowserPick, state: NativeBrowserState, binding: DesktopBrowserBinding) {
  if (state.context_ref !== binding.context_ref || state.viewed_tab !== binding.tab_id || !state.version || !nativePickMatches(pick, { version: state.version, tab_id: binding.tab_id, viewport: state.viewport })) return null;
  return nativeElementPick(pick, state);
}
export function desktopBounds(rect: { x: number; y: number; width: number; height: number }): NativeBrowserRect | null {
  if (![rect.x, rect.y, rect.width, rect.height].every(Number.isFinite) || rect.width < 1 || rect.height < 1) return null;
  return { x: rect.x, y: rect.y, width: rect.width, height: rect.height };
}
export interface DesktopBrowserClip extends NativeBrowserRect { clipX?: boolean; clipY?: boolean }
/** Visibility is tested against clipping, but bounds remain the complete page
 * rectangle. Resizing a page to the visible sliver during a dock transition
 * would reflow the document to e.g. 14px wide and invalidate its observations. */
export function desktopSurfaceGeometry(rect: NativeBrowserRect, viewport: { width: number; height: number }, clips: DesktopBrowserClip[] = []) {
  const bounds = desktopBounds(rect);
  if (!bounds || ![viewport.width, viewport.height].every(value => Number.isFinite(value) && value > 0)) return { bounds, visible: false };
  let left = Math.max(0, bounds.x), top = Math.max(0, bounds.y);
  let right = Math.min(viewport.width, bounds.x + bounds.width), bottom = Math.min(viewport.height, bounds.y + bounds.height);
  let insideAncestor = true;
  for (const clip of clips) {
    if (clip.clipX) { left = Math.max(left, clip.x); right = Math.min(right, clip.x + clip.width); }
    if (clip.clipY) { top = Math.max(top, clip.y); bottom = Math.min(bottom, clip.y + clip.height); }
    // The native host clips to its window, not arbitrary DOM ancestors. A
    // partly clipped child must stay hidden until it fits its shell slot.
    if (clip.clipX && (bounds.x < clip.x - 1 || bounds.x + bounds.width > clip.x + clip.width + 1)) insideAncestor = false;
    if (clip.clipY && (bounds.y < clip.y - 1 || bounds.y + bounds.height > clip.y + clip.height + 1)) insideAncestor = false;
  }
  return { bounds, visible: insideAncestor && bounds.width >= 160 && bounds.height >= 100 && right - left >= 160 && bottom - top >= 100 };
}

/** Owns one presentation attachment, never the underlying browser tab.
 * Hides are sent immediately, including while an older update awaits a reply.
 * The host must fence each await with the monotonically increasing revision. */
export function createDesktopSurface(bridge: DesktopBrowserBridge, binding: DesktopBrowserBinding, viewId: string, callbacks: { interactive: (value: boolean) => void; error: (error: unknown) => void }) {
  let surface = "";
  let closed = false;
  let revision = 0;
  // attach creates a hidden native view. Wait for real layout; never resize a
  // page to an invented 1×1 rectangle while Vue is mounting its dock.
  let desired: DesktopBrowserPresentation | undefined;
  let attaching: Promise<void> | undefined;
  let lastKey = "";
  const publish = async () => {
    const presentation = desired;
    if (!surface || closed || !presentation) return;
    const key = JSON.stringify(presentation);
    if (key === lastKey) return;
    lastKey = key;
    const current = ++revision;
    if (!presentation.visible || presentation.blocked) callbacks.interactive(false);
    try {
      const result = await bridge.update({ ...presentation, surface_id: surface, revision: current });
      if (!closed && current === revision) callbacks.interactive(presentation.visible && !presentation.blocked && result.interactive === true);
    } catch (error) {
      if (!closed && current === revision) { lastKey = ""; callbacks.interactive(false); callbacks.error(error); }
    }
  };
  return {
    get id() { return surface; },
    attach() {
      if (attaching || surface || closed) return attaching ?? Promise.resolve();
      attaching = (async () => {
        try {
          const reply = await bridge.attach({ ...binding, view_id: viewId });
          if (!reply || typeof reply.surface_id !== "string" || !reply.surface_id) throw new Error("Invalid desktop browser attachment");
          if (closed) { await bridge.detach({ surface_id: reply.surface_id }); return; }
          surface = reply.surface_id;
          await publish();
        } catch (error) { if (!closed) callbacks.error(error); }
        finally { attaching = undefined; }
      })();
      return attaching;
    },
    update(next: DesktopBrowserPresentation) {
      if (closed) return Promise.resolve();
      desired = { ...next, bounds: { ...next.bounds } };
      return publish();
    },
    async close() {
      if (closed) return;
      closed = true; revision++; callbacks.interactive(false);
      const attached = surface; surface = "";
      if (attached) await bridge.detach({ surface_id: attached });
    },
  };
}
