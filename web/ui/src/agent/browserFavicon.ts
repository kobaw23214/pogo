/** Icons are bounded image bytes supplied by the session's own Chromium.
 * Never turn page metadata or saved storage into another frontend URL fetch. */
export function browserFavicon(value: unknown): string | undefined {
  if (typeof value !== "string" || value.length > Math.ceil(65536 * 4 / 3) + 64) return;
  const match = /^data:image\/(?:png|jpeg|gif|webp|x-icon|vnd\.microsoft\.icon|svg\+xml);base64,([A-Za-z0-9+/]+={0,2})$/.exec(value);
  if (!match || match[1].length % 4 !== 0) return;
  const bytes = match[1].length * 3 / 4 - (match[1].endsWith("==") ? 2 : match[1].endsWith("=") ? 1 : 0);
  if (bytes > 0 && bytes <= 65536) return value;
}
