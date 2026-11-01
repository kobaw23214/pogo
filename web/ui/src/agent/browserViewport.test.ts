import { describe, expect, it } from "vitest";
import { browserCaptureMatches, browserCaptureResolution, desktopBrowserViewport } from "./browserViewport";

describe("desktop browser display viewport", () => {
  it("uses the negotiated Wayland grid without repeatedly requesting unrealizable dimensions", () => {
    expect(desktopBrowserViewport({ width: 1433, height: 901 }, 8)).toEqual({ width: 1432, height: 904 });
    expect(desktopBrowserViewport({ width: 2001, height: 1101 }, 8)).toEqual({ width: 2000, height: 1104 });
    expect(desktopBrowserViewport({ width: 600, height: 800 }, 8)).toEqual({ width: 1280, height: 1600 });
    expect(desktopBrowserViewport({ width: 1433, height: 901 })).toEqual({ width: 1433, height: 901 });
  });
  it("preserves a desktop breakpoint when the dock is narrower than a webpage", () => {
    expect(desktopBrowserViewport({ width: 600, height: 800 })).toEqual({ width: 1280, height: 1600 });
    expect(desktopBrowserViewport({ width: 800, height: 600 })).toEqual({ width: 1280, height: 960 });
  });
  it("expands the actual page for an immersive view without retaining a narrow dock width", () => {
    expect(desktopBrowserViewport({ width: 1440, height: 900 })).toEqual({ width: 1440, height: 900 });
    expect(desktopBrowserViewport({ width: 2000, height: 1100 })).toEqual({ width: 2000, height: 1100 });
  });
  it("caps dimensions to the worker display contract and keeps a usable desktop height", () => {
    expect(desktopBrowserViewport({ width: 3200, height: 1800 })).toEqual({ width: 2560, height: 1440 });
    expect(desktopBrowserViewport({ width: 1400, height: 250 })).toEqual({ width: 1400, height: 720 });
  });
  it("ignores hidden/transition slivers and does not manufacture a viewport before layout", () => {
    for (const bounds of [{ width: 0, height: 0 }, { width: 150, height: 800 }, { width: 800, height: 99 }, { width: NaN, height: 800 }, { width: 800, height: Infinity }]) expect(desktopBrowserViewport(bounds)).toBeNull();
    expect(desktopBrowserViewport({ width: 1280.25, height: 800.25 })).toEqual({ width: 1280, height: 800 });
  });
});

describe("native page capture density", () => {
  it("matches a Retina immersive surface while retaining its desktop CSS size", () => {
    expect(browserCaptureResolution({ width: 1424, height: 872 }, { width: 1424, height: 872 }, 2, 2))
      .toEqual({ preferredScale: 2, scale: 2, pixelWidth: 2848, pixelHeight: 1744 });
  });
  it("retains the text density floor for narrow docks and ordinary displays", () => {
    expect(browserCaptureResolution({ width: 535, height: 669 }, { width: 1280, height: 1600 }, 2, 2)?.scale).toBe(1.5);
    expect(browserCaptureResolution({ width: 1280, height: 800 }, { width: 1280, height: 800 }, 1, 2)?.scale).toBe(1.5);
  });
  it("falls back by native pixel budget without silently shrinking the CSS viewport", () => {
    const display = { width: 2560, height: 1600 };
    expect(browserCaptureResolution(display, display, 2, 2)).toEqual({ preferredScale: 2, scale: 1.25, pixelWidth: 3200, pixelHeight: 2000 });
    expect(browserCaptureResolution({ width: 1920, height: 1080 }, { width: 1920, height: 1080 }, 2, 2)?.scale).toBe(1.5);
    expect(browserCaptureResolution({ width: 1920, height: 968 }, { width: 1920, height: 968 }, 2, 2)?.scale).toBe(2);
    expect(browserCaptureResolution({ width: 1920, height: 976 }, { width: 1920, height: 976 }, 2, 2)?.scale).toBe(1.5);
    expect(display).toEqual({ width: 2560, height: 1600 });
  });
  it("respects the configured worker limit and accepts an already sharper shared page", () => {
    const geometry = { width: 1280, height: 960 };
    const desired = browserCaptureResolution(geometry, geometry, 2, 1.5)!;
    expect(desired.scale).toBe(1.5);
    expect(browserCaptureMatches({ pixel_width: 2560, pixel_height: 1920 }, desired)).toBe(true);
    expect(browserCaptureMatches({ pixel_width: 1600, pixel_height: 1200 }, desired)).toBe(false);
    expect(browserCaptureMatches(undefined, desired)).toBe(false);
    expect(browserCaptureMatches(undefined, null)).toBe(true);
  });
  it.each([NaN, Infinity, 0, -1])("rejects invalid display density (%s)", dpr => {
    expect(browserCaptureResolution({ width: 1280, height: 800 }, { width: 1280, height: 800 }, dpr, 2)).toBeNull();
  });
});
