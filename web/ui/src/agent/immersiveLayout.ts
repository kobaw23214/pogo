export interface FloatingRect { x: number; y: number; width: number; height: number }
export interface ViewportSize { width: number; height: number }

/** Keep the controls reachable, including after zoom, rotation and a soft keyboard. */
export function fitFloatingChat(rect: FloatingRect | null, viewport: ViewportSize): FloatingRect {
  const vw = Math.max(1, viewport.width);
  const vh = Math.max(1, viewport.height);
  const gap = Math.min(12, vw / 20, vh / 20);
  const top = Math.min(88, vh * .2);
  const maxW = Math.max(1, vw - gap * 2);
  const maxH = Math.max(1, vh - top - gap);
  if (vw <= 720) {
    const height = Math.min(maxH, Math.max(240, vh * .58));
    return { x: gap, y: vh - gap - height, width: maxW, height };
  }
  const width = Math.min(maxW, Math.max(360, rect?.width ?? 440));
  const height = Math.min(maxH, Math.max(300, rect?.height ?? Math.min(660, vh * .76)));
  return {
    x: Math.min(vw - gap - width, Math.max(gap, rect?.x ?? vw - gap - width)),
    y: Math.min(vh - gap - height, Math.max(top, rect?.y ?? vh - gap - height)),
    width, height,
  };
}

export function adjustFloatingChat(rect: FloatingRect, dx: number, dy: number, resize: boolean, viewport: ViewportSize): FloatingRect {
  return fitFloatingChat(resize
    ? { ...rect, width: rect.width + dx, height: rect.height + dy }
    : { ...rect, x: rect.x + dx, y: rect.y + dy }, viewport);
}
