import type { NativeBrowserViewport } from "./nativeBrowser";

/** The pane is a view onto a desktop page, not a request to emulate a tablet.
 * Keep desktop breakpoints in a narrow dock; a wide immersive view expands
 * the actual browser viewport. CSS pixels never include devicePixelRatio. */
export function desktopBrowserViewport(bounds: NativeBrowserViewport, quantum: 1 | 8 = 1): NativeBrowserViewport | null {
  if (![bounds.width, bounds.height].every(Number.isFinite) || bounds.width < 160 || bounds.height < 100) return null;
  const quantize = (value: number) => Math.round(value / quantum) * quantum;
  const width = Math.max(1280, Math.min(2560, quantize(bounds.width)));
  const height = Math.max(720, Math.min(1600, quantize(width * bounds.height / bounds.width)));
  return { width, height };
}

export interface BrowserCaptureResolution {
  preferredScale: number;
  scale: number;
  pixelWidth: number;
  pixelHeight: number;
}

/** Match the displayed pixels without changing desktop layout or coordinates.
 * The worker advertises its density limit and makes the authoritative choice. */
export function browserCaptureResolution(bounds: NativeBrowserViewport, viewport: NativeBrowserViewport, dpr: number, limit: number): BrowserCaptureResolution | null {
  if (![bounds.width, bounds.height, viewport.width, viewport.height, dpr, limit].every(Number.isFinite)
    || bounds.width <= 0 || bounds.height <= 0 || viewport.width <= 0 || viewport.height <= 0 || dpr <= 0
    || ![1, 1.25, 1.5, 2].includes(limit)) return null;
  const displayScale = Math.min(bounds.width / viewport.width, bounds.height / viewport.height);
  const preferredScale = Math.min(limit, Math.max(Math.min(1.5, limit), dpr * displayScale));
  const valid = [1, 1.25, 1.5, 2].filter(scale => {
    const width = viewport.width * scale, height = viewport.height * scale;
    return scale <= limit && Number.isInteger(width) && Number.isInteger(height) && width % 2 === 0 && height % 2 === 0
      && width <= 3840 && height <= 2400 && width * height <= 7464960;
  });
  const scale = valid.find(value => value >= preferredScale) ?? valid[valid.length - 1];
  return scale === undefined ? null : { preferredScale, scale, pixelWidth: viewport.width * scale, pixelHeight: viewport.height * scale };
}

/** A second lower-DPI viewer must accept an already sharper page; requesting
 * the lower density again would continuously interrupt both viewers. */
export function browserCaptureMatches(pixels: { pixel_width: number; pixel_height: number } | undefined, desired: BrowserCaptureResolution | null): boolean {
  return !desired || !!pixels && pixels.pixel_width >= desired.pixelWidth && pixels.pixel_height >= desired.pixelHeight;
}
