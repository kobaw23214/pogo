import { describe, expect, it } from "vitest";
import { adjacentStoryStop, buildStoryStops, commitsStorySwipe, nearestStoryStop, storyTransitionDuration, type StoryChapter } from "../../components/studio/landingSnapConfig";

const chapters: StoryChapter[] = [
  { id: "overview", title: "首页", start: 0, end: 1840, cinematic: true, anchors: [
    { key: "gather", top: 0, label: "聚合" }, { key: "unfold", top: 702.4, label: "拆解" }, { key: "enter", top: 1088.72, label: "穿入" },
  ] },
  { id: "capabilities", title: "基座能力", start: 2452, end: 2876, anchors: [{ key: "details", top: 2820, label: "能力详解" }] },
  { id: "workspace", title: "工作空间", start: 3516, end: 4461, anchors: [
    { key: "configure", top: 3800, label: "配置能力" }, { key: "execute", top: 4024, label: "推进任务" }, { key: "result", top: 4248, label: "看见结果" },
  ] },
  { id: "possibilities", title: "应用场景", start: 5101, end: 5554, anchors: [{ key: "directions", top: 5280, label: "探索应用方向" }] },
  { id: "begin", title: "开始使用", start: 6194, end: 6194, anchors: [] },
];
const stops = buildStoryStops(chapters, 620, 6194);

describe("five chapters with internal story stops", () => {
  it("keeps exactly five chapters and three opening poses", () => {
    expect(new Set(stops.map(stop => stop.chapterId)).size).toBe(5);
    expect(stops.filter(stop => stop.chapterId === "overview").map(stop => [stop.label, stop.top])).toEqual([
      ["聚合", 0], ["拆解", 702.4], ["穿入", 1088.72],
    ]);
  });

  it("one forward gesture reaches the next pose before changing chapters", () => {
    expect(adjacentStoryStop(stops, 0, 1)?.id).toBe("overview:unfold");
    expect(adjacentStoryStop(stops, 702.4, 1)?.id).toBe("overview:enter");
    expect(adjacentStoryStop(stops, 1088.72, 1)?.chapterId).toBe("capabilities");
  });

  it("does not park on the opening's empty portal exit", () => {
    expect(stops.some(stop => stop.top > 1088.72 && stop.top < 2452)).toBe(false);
  });

  it("reverses through internal poses and the preceding chapter's final pose", () => {
    expect(adjacentStoryStop(stops, 2452, -1)?.id).toBe("overview:enter");
    expect(adjacentStoryStop(stops, 1088.72, -1)?.id).toBe("overview:unfold");
    expect(adjacentStoryStop(stops, 702.4, -1)?.id).toBe("overview:gather");
  });

  it("an interrupted transition can reverse to its origin", () => {
    expect(adjacentStoryStop(stops, 420, -1)?.top).toBe(0);
    expect(adjacentStoryStop(stops, 420, 1)?.top).toBe(702.4);
  });

  it("retains configuration, execution and result milestones", () => {
    const labels = stops.filter(stop => stop.chapterId === "workspace").map(stop => stop.label);
    expect(labels).toEqual(expect.arrayContaining(["配置能力", "推进任务", "看见结果"]));
  });

  it.each([200, 320, 620, 900])("every part of tall reading content is reachable with overlap at height %i", readable => {
    const long: StoryChapter = { id: "reading", title: "阅读", start: 100, end: 5700, anchors: [
      { key: "a", label: "第一段", top: 360 }, { key: "b", label: "第二段", top: 2400 }, { key: "c", label: "第三段", top: 4200 },
    ] };
    const points = buildStoryStops([long], readable, 6000);
    expect(points[0]!.top).toBe(100);
    expect(points[points.length - 1]!.top).toBe(5700);
    points.slice(1).forEach((point, index) => {
      expect(point.top - points[index]!.top).toBeGreaterThan(0);
      expect(point.top - points[index]!.top).toBeLessThanOrEqual(readable * 0.8 + 0.001);
    });
    expect(new Set(points.map(point => point.id)).size).toBe(points.length);
  });

  it("adds reading stops when accordion content grows", () => {
    const narrow = { ...chapters[1]!, start: 0, end: 2600, anchors: [{ key: "details", top: 600, label: "能力详解" }] };
    const before = buildStoryStops([narrow], 650, 6000);
    const after = buildStoryStops([{ ...narrow, end: 4100 }], 650, 6000);
    expect(after.length).toBeGreaterThan(before.length);
    expect(after[after.length - 1]!.top).toBe(4100);
  });

  it("clamps a short final page to the actual document end", () => {
    expect(stops[stops.length - 1]?.top).toBe(6194);
    expect(adjacentStoryStop(stops, 6194, 1)).toBeUndefined();
    expect(adjacentStoryStop(stops, 0, -1)).toBeUndefined();
    expect(buildStoryStops([{ id: "tiny", title: "短页", start: 0, end: 40, anchors: [] }], 620, 0)).toHaveLength(1);
  });

  it("ignores invalid bounds and missing intent", () => {
    expect(buildStoryStops(chapters, 0, 6000)).toEqual([]);
    expect(buildStoryStops([{ ...chapters[0]!, start: Number.NaN }], 620, 6000)).toEqual([]);
    expect(adjacentStoryStop(stops, Number.NaN, 1)).toBeUndefined();
    expect(adjacentStoryStop(stops, 400, 0)).toBeUndefined();
    expect(nearestStoryStop([], 0)).toBe(-1);
  });

  it("selects a nearby stop after a scrollbar drag without an extra step", () => {
    expect(stops[nearestStoryStop(stops, 710)]?.id).toBe("overview:unfold");
    expect(stops[nearestStoryStop(stops, 2449)]?.chapterId).toBe("capabilities");
  });

  it("uses distinct bounded tempos for internal scenes and chapter changes", () => {
    expect(storyTransitionDuration(700, 800, false)).toBeCloseTo(0.6);
    expect(storyTransitionDuration(700, 800, true)).toBeCloseTo(0.905);
    expect(storyTransitionDuration(6000, 800, true)).toBe(1.15);
    expect(storyTransitionDuration(-700, 800, false)).toBeCloseTo(0.6);
    expect(storyTransitionDuration(700, 800, false, true)).toBeCloseTo(0.39);
    expect(storyTransitionDuration(700, 0, false)).toBe(0);
  });

  it("a deliberate swipe commits while a small hesitant drag returns", () => {
    expect(commitsStorySwipe(60, 400)).toBe(true);
    expect(commitsStorySwipe(-60, 400)).toBe(true);
    expect(commitsStorySwipe(25, 50)).toBe(true);
    expect(commitsStorySwipe(25, 500)).toBe(false);
    expect(commitsStorySwipe(5, 1)).toBe(false);
  });
});
