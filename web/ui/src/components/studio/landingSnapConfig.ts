import { gt as tr } from "@/i18n";
export type ScrollDirection = -1 | 0 | 1;
export type StoryAnchor = { key: string; top: number; label: string };
export type StoryChapter = {
  id: string; title: string; start: number; end: number;
  anchors: StoryAnchor[]; cinematic?: boolean;
};
export type StoryStop = StoryAnchor & { id: string; chapterId: string; chapterTitle: string };

/** Reading windows overlap by at least 20%; cinematic anchors seek a timeline. */
export function buildStoryStops(chapters: readonly StoryChapter[], readableHeight: number, maxScroll: number): StoryStop[] {
  if (!Number.isFinite(readableHeight) || readableHeight <= 0 || !Number.isFinite(maxScroll)) return [];
  const clamp = (value: number) => Math.max(0, Math.min(Math.max(0, maxScroll), value));
  const maxStep = readableHeight * 0.8;
  const minStep = Math.min(160, readableHeight * 0.22);
  const result: StoryStop[] = [];
  for (const chapter of chapters) {
    if (![chapter.start, chapter.end].every(Number.isFinite)) continue;
    const start = clamp(chapter.start);
    const end = Math.max(start, clamp(chapter.end));
    const candidates: StoryAnchor[] = [
      { key: "start", top: start, label: chapter.title },
      ...chapter.anchors.filter(anchor => Number.isFinite(anchor.top))
        .map(anchor => ({ ...anchor, top: Math.max(start, Math.min(end, clamp(anchor.top))) })),
    ];
    if (!chapter.cinematic) candidates.push({ key: "end", top: end, label: chapter.anchors[chapter.anchors.length - 1]?.label ?? chapter.title });
    candidates.sort((a, b) => a.top - b.top);
    const points: StoryAnchor[] = [];
    for (const point of candidates) {
      const previous = points[points.length - 1];
      if (previous && point.top - previous.top < 2) {
        if (point.key !== "end") points[points.length - 1] = point;
      } else if (previous && !chapter.cinematic && point.top - previous.top < minStep && points.length > 1) {
        points[points.length - 1] = point;
      } else points.push(point);
    }
    const append = (point: StoryAnchor) => result.push({ ...point, id: `${chapter.id}:${point.key}`, chapterId: chapter.id, chapterTitle: chapter.title });
    points.forEach((point, index) => {
      const previous = points[index - 1];
      if (previous && !chapter.cinematic) {
        const steps = Math.ceil((point.top - previous.top) / maxStep);
        for (let part = 1; part < steps; part++) {
          append({ key: `${previous.key}-${point.key}-${part}`, top: previous.top + (point.top - previous.top) * part / steps, label: tr('studioManagement.text509', { value0: previous.label }) });
        }
      }
      append(point);
    });
  }
  return result.sort((a, b) => a.top - b.top);
}

export function nearestStoryStop(stops: readonly StoryStop[], position: number): number {
  if (!stops.length || !Number.isFinite(position)) return -1;
  let nearest = 0;
  for (let i = 1; i < stops.length; i++) {
    if (Math.abs(stops[i]!.top - position) < Math.abs(stops[nearest]!.top - position)) nearest = i;
  }
  return nearest;
}

export function adjacentStoryStop(stops: readonly StoryStop[], position: number, direction: ScrollDirection): StoryStop | undefined {
  if (!Number.isFinite(position) || !direction) return undefined;
  return direction > 0 ? stops.find(stop => stop.top > position + 3)
    : [...stops].reverse().find(stop => stop.top < position - 3);
}

export function storyTransitionDuration(distance: number, viewport: number, chapterChange: boolean, reduced = false): number {
  if (![distance, viewport].every(Number.isFinite) || viewport <= 0) return 0;
  const duration = chapterChange ? Math.min(1.15, 0.8 + Math.abs(distance) / viewport * 0.12)
    : Math.min(0.72, 0.46 + Math.abs(distance) / viewport * 0.16);
  return reduced ? duration * 0.65 : duration;
}

export function commitsStorySwipe(distance: number, elapsed: number): boolean {
  const travel = Math.abs(distance);
  return travel >= 44 || (travel >= 18 && travel / Math.max(1, elapsed) >= 0.4);
}
