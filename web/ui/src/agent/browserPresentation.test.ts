import { describe, expect, it, vi } from "vitest";
import { browserHeldFrameMatches, browserPixelBridgeTransform, browserPresentedPixelRect, cancelBrowserPixelGeometry, captureBrowserHeldFrame, type BrowserPresentationBinding, type BrowserPresentedVideo } from "./browserPresentation";
const binding: BrowserPresentationBinding = { sessionId: "session-a", context_ref: "ctx-a", tab_id: "t1", generation: 2 };
function fixture() {
  const video = { videoWidth: 2560, videoHeight: 1600, readyState: 4 } as HTMLVideoElement;
  const source = { drawImage: vi.fn() }, destination = { drawImage: vi.fn() };
  const sample = { width: 0, height: 0, getContext: () => source };
  const canvas = { width: 800, height: 600, getContext: () => destination, ownerDocument: { createElement: vi.fn(() => sample) } } as unknown as HTMLCanvasElement;
  const presented: BrowserPresentedVideo = { binding, video, media_epoch: "media-1", frame_id: 1 };
  return { video, source, destination, sample, canvas, presented };
}
describe("local held browser pixels", () => {
  it("copies only an already presented video during teardown and returns no input authority", () => {
    const f = fixture();
    const held = captureBrowserHeldFrame(f.presented, f.video, f.canvas, binding);
    expect(held).toEqual({ binding, width: 2560, height: 1600 });
    expect(f.source.drawImage).toHaveBeenCalledExactlyOnceWith(f.video, 0, 0, 2560, 1600);
    expect(f.destination.drawImage).toHaveBeenCalledExactlyOnceWith(f.sample, 0, 0);
    expect([f.canvas.width, f.canvas.height]).toEqual([2560, 1600]);
    expect(held).not.toHaveProperty("lease_token"); expect(held).not.toHaveProperty("media_epoch");
  });
  it.each([{ sessionId: "session-b" }, { context_ref: "ctx-b" }, { tab_id: "t2" }, { generation: 3 }])("never preserves another binding %j", change => {
    const f = fixture(), next = { ...binding, ...change };
    expect(browserHeldFrameMatches({ binding, width: 1, height: 1 }, next)).toBe(false);
    expect(captureBrowserHeldFrame(f.presented, f.video, f.canvas, next)).toBeNull();
    expect(f.source.drawImage).not.toHaveBeenCalled(); expect([f.canvas.width, f.canvas.height]).toEqual([800, 600]);
  });
  it("cannot use an old presentation callback to bless the next video", () => {
    const f = fixture();
    expect(captureBrowserHeldFrame(f.presented, { ...f.video } as HTMLVideoElement, f.canvas, binding)).toBeNull();
    expect(f.source.drawImage).not.toHaveBeenCalled();
  });
  it("does not manufacture a held image before the first successful presentation", () => {
    const f = fixture();
    expect(captureBrowserHeldFrame(undefined, f.video, f.canvas, binding)).toBeNull();
    expect(captureBrowserHeldFrame(f.presented, f.video, f.canvas, null)).toBeNull();
    expect(f.source.drawImage).not.toHaveBeenCalled();
  });
  it("preserves an existing held canvas if the next video cannot be copied", () => {
    const f = fixture(); f.source.drawImage.mockImplementation(() => { throw new Error("no decoded image"); });
    expect(captureBrowserHeldFrame(f.presented, f.video, f.canvas, binding)).toBeNull();
    expect(f.destination.drawImage).not.toHaveBeenCalled(); expect([f.canvas.width, f.canvas.height]).toEqual([800, 600]);
  });
  it.each([{ readyState: 1 }, { videoWidth: 0 }, { videoHeight: 9000 }, { videoWidth: 8192, videoHeight: 8192 }])("rejects missing or unbounded decoded pixels %j", change => {
    const f = fixture(); Object.assign(f.video, change);
    expect(captureBrowserHeldFrame(f.presented, f.video, f.canvas, binding)).toBeNull(); expect(f.source.drawImage).not.toHaveBeenCalled();
  });
});


describe("temporary browser pixel bridge geometry", () => {
  it("cancels all local geometry without a media handoff and retains the final pixel box", () => {
    const canvas = { style: { left: "0px", top: "148.625px", width: "535px", height: "668.75px", objectFit: "fill" } };
    const page = { style: { transformOrigin: "0 0" }, dataset: { pixelPageBridge: "true" } } as unknown as HTMLElement;
    const animations = [vi.fn(), vi.fn(), vi.fn()].map(cancel => ({ cancel }) as unknown as Animation);
    cancelBrowserPixelGeometry(animations, page, "50% 50%");
    for (const animation of animations) expect(animation.cancel).toHaveBeenCalledOnce();
    expect(page.style.transformOrigin).toBe("50% 50%"); expect(page.dataset.pixelPageBridge).toBeUndefined();
    expect(canvas.style).toEqual({ left: "0px", top: "148.625px", width: "535px", height: "668.75px", objectFit: "fill" });
    expect(() => cancelBrowserPixelGeometry([undefined], undefined, "")).not.toThrow();
  });
  it("locates the real contained pixels before a layout change", () => {
    expect(browserPresentedPixelRect({ x: 853, y: 82, width: 535, height: 966 }, { width: 535, height: 966 }, { width: 1920, height: 2400 }))
      .toEqual({ x: 853, y: 230.625, width: 535, height: 668.75 });
  });
  it("accounts separately for ancestor X/Y scales instead of applying contain twice", () => {
    expect(browserPresentedPixelRect({ x: 100, y: 50, width: 200, height: 900 }, { width: 1000, height: 600 }, { width: 1000, height: 500 }))
      .toEqual({ x: 100, y: 125, width: 200, height: 750 });
  });
  it("inverse-maps screen deltas into local coordinates inside a non-uniform FLIP", () => {
    expect(browserPixelBridgeTransform({ x: 100, y: 160, width: 600, height: 400 }, { x: 50, y: 80, width: 1000, height: 500 }, { width: 2000, height: 1000 }))
      .toBe("translate3d(100px, 160px, 0) scale(0.6, 0.8)");
  });
  it("compensates the page's rail reflow before mapping pixels so no new side gutter appears", () => {
    // Actual former failure: the dock was at its old pose, but hiding its rail
    // made the native-page box 54px wider before the first animation frame.
    const oldPage = { x: 853, y: 82, width: 535, height: 965.5 };
    const reflowedPage = { x: 847.311, y: 94, width: 589.378, height: 953.5 };
    const layout = { width: 1424, height: 954 };
    const parse = (value: string) => value.match(/translate3d\(([-\d.e+]+)px, ([-\d.e+]+)px, 0\) scale\(([-\d.e+]+), ([-\d.e+]+)\)/)!.slice(1).map(Number);
    const [tx, ty, sx, sy] = parse(browserPixelBridgeTransform(oldPage, reflowedPage, layout)!);
    const compensated = { x: reflowedPage.x + tx! * reflowedPage.width / layout.width,
      y: reflowedPage.y + ty! * reflowedPage.height / layout.height,
      width: reflowedPage.width * sx!, height: reflowedPage.height * sy! };
    for (const key of ["x", "y", "width", "height"] as const) expect(compensated[key]).toBeCloseTo(oldPage[key], 8);
    const future = browserPresentedPixelRect({ x: 0, y: 0, ...layout }, layout, { width: 1424, height: 952 })!;
    const targetScreen = { x: compensated.x + future.x * compensated.width / layout.width,
      y: compensated.y + future.y * compensated.height / layout.height,
      width: future.width * compensated.width / layout.width, height: future.height * compensated.height / layout.height };
    const sourcePaint = { x: 853, y: 230.548, width: 535, height: 668.404 };
    const [paintX, , paintScaleX] = parse(browserPixelBridgeTransform(sourcePaint, targetScreen, future)!);
    expect(paintX).toBeCloseTo(0, 8); expect(paintScaleX).toBeCloseTo(1, 8);
  });
  it("uses the new viewport's future contain rectangle as the handoff target", () => {
    expect(browserPresentedPixelRect({ x: 0, y: 0, width: 535, height: 966 }, { width: 535, height: 966 }, { width: 1280, height: 1600 }))
      .toEqual({ x: 0, y: 148.625, width: 535, height: 668.75 });
    expect(browserPresentedPixelRect({ x: 10, y: 20, width: 500, height: 700 }, { width: 500, height: 700 }, { width: 1280, height: 1600 }, "fill"))
      .toEqual({ x: 10, y: 20, width: 500, height: 700 });
  });
  it("does not create geometry from hidden or unready media", () => {
    expect(browserPresentedPixelRect({ x: 0, y: 0, width: 0, height: 100 }, { width: 100, height: 100 }, { width: 100, height: 100 })).toBeNull();
    expect(browserPixelBridgeTransform({ x: 0, y: 0, width: 100, height: 100 }, { x: 0, y: 0, width: 100, height: 100 }, { width: 0, height: 100 })).toBeNull();
  });
});
