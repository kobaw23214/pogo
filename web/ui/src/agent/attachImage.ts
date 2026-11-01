/**
 * Attachment preparation: what happens between a pasted file and the upload
 * request. The pipeline is client-side downscale + re-encode, because the
 * research consensus (and both providers' guidance) is that the bytes you send
 * should already be the size you mean: a 1568px long edge is where every
 * provider's tokenizer stops giving you more information, and re-encoding on
 * the canvas strips EXIF as a side effect.
 */

/** The long edge every provider's guidance converges on. */
export const MAX_EDGE = 1568;

/** JPEG quality for re-encoded images. */
export const REENCODE_QUALITY = 0.87;

/**
 * Files this small are sent untouched: a small PNG screenshot keeps its text
 * pixel-perfect, and lossy recompression of crisp text is the one case every
 * guide warns about.
 */
export const SMALL_ENOUGH = 400_000;

/**
 * fitScale is the scale factor that brings the long edge to MAX_EDGE without
 * ever upscaling. Pure so the behaviour can be pinned without a canvas.
 */
export function fitScale(w: number, h: number, maxEdge = MAX_EDGE): number {
  if (w <= 0 || h <= 0) return 1;
  return Math.min(maxEdge / Math.max(w, h), 1);
}

/**
 * Whether a file should be re-encoded at all: small files pass through, a
 * large file already at the ceiling only re-encodes to change format, which is
 * not worth the quality loss. force means "the target type is unacceptable
 * regardless of size" (a gif the server will not store, say).
 */
export function shouldReencode(w: number, h: number, bytes: number, force = false): boolean {
  if (force) return true;
  if (bytes <= SMALL_ENOUGH) return false;
  return fitScale(w, h) < 1 || bytes > 1_500_000;
}

/**
 * prepareImage downscale-and-reencodes when that is a win, and hands back the
 * original on any failure — an upload of the untouched file is always better
 * than a failed attachment flow.
 */
export async function prepareImage(
  file: Blob,
  name: string,
  force = false,
): Promise<{ blob: Blob; name: string }> {
  const dims = await dimensions(file);
  if (!dims || !shouldReencode(dims.w, dims.h, file.size, force)) {
    return { blob: file, name };
  }
  try {
    const bitmap = await createImageBitmap(file);
    const scale = fitScale(bitmap.width, bitmap.height);
    const canvas = document.createElement("canvas");
    canvas.width = Math.max(1, Math.round(bitmap.width * scale));
    canvas.height = Math.max(1, Math.round(bitmap.height * scale));
    const ctx = canvas.getContext("2d");
    if (!ctx) throw new Error("no 2d context");
    ctx.drawImage(bitmap, 0, 0, canvas.width, canvas.height);
    bitmap.close();
    const blob = await new Promise<Blob | null>((resolve) =>
      canvas.toBlob(resolve, "image/jpeg", REENCODE_QUALITY),
    );
    if (!blob || blob.size >= file.size) {
      // The re-encode did not pay for itself; keep the original.
      return { blob: file, name };
    }
    const dot = name.lastIndexOf(".");
    const base = dot > 0 ? name.slice(0, dot) : name;
    return { blob, name: `${base}.jpg` };
  } catch {
    return { blob: file, name };
  }
}

/** dimensions decodes just enough of the file to know its size, if it can. */
async function dimensions(file: Blob): Promise<{ w: number; h: number } | null> {
  try {
    const bitmap = await createImageBitmap(file);
    const out = { w: bitmap.width, h: bitmap.height };
    bitmap.close();
    return out;
  } catch {
    return null;
  }
}
