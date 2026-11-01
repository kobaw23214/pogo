import { browserFavicon } from "./browserFavicon";

/** Persist raster bytes, never an external URL or an active SVG document. */
export function referenceFavicon(value: unknown): string | undefined {
  const icon = browserFavicon(value);
  return icon && !icon.startsWith("data:image/svg+xml;") ? icon : undefined;
}

const tags = new Set(["svg", "g", "path", "rect", "circle", "ellipse", "polygon", "polyline", "line", "defs", "linearGradient", "radialGradient", "stop", "clipPath", "mask", "title", "desc"]);
const attributes = new Set(["viewBox", "width", "height", "x", "y", "x1", "x2", "y1", "y2", "cx", "cy", "r", "rx", "ry", "d", "points", "fill", "fill-rule", "fill-opacity", "stroke", "stroke-width", "stroke-linecap", "stroke-linejoin", "stroke-miterlimit", "stroke-dasharray", "stroke-dashoffset", "stroke-opacity", "opacity", "transform", "id", "offset", "stop-color", "stop-opacity", "gradientTransform", "gradientUnits", "spreadMethod", "clip-path", "clip-rule", "mask", "maskUnits", "maskContentUnits", "preserveAspectRatio"]);

/** Rebuild a static SVG before decoding it as an image. No href, image/use,
 * foreignObject, scripts, event handlers, CSS or external resource survives. */
function staticSVG(value: string): string | undefined {
  if (typeof DOMParser === "undefined" || /<!DOCTYPE|<!ENTITY/i.test(value)) return;
  const doc = new DOMParser().parseFromString(value, "image/svg+xml");
  if (doc.querySelector("parsererror") || doc.documentElement.localName !== "svg") return;
  const clean = document.implementation.createDocument("http://www.w3.org/2000/svg", "svg");
  let count = 0;
  const copy = (source: Element, target: Element): boolean => {
    if (++count > 512) return false;
    for (const attribute of Array.from(source.attributes)) {
      if (!attributes.has(attribute.name) || attribute.namespaceURI || attribute.value.length > 32768) continue;
      const content = attribute.value;
      if (/[\\\u0000-\u001f\u007f]/.test(content)) continue;
      if (/url\s*\(/i.test(content) && !/^url\(#[A-Za-z_][\w.-]*\)$/.test(content)) continue;
      target.setAttribute(attribute.name, content);
    }
    for (const child of Array.from(source.children)) {
      if (!tags.has(child.localName) || child.namespaceURI !== "http://www.w3.org/2000/svg") continue;
      const next = clean.createElementNS("http://www.w3.org/2000/svg", child.localName);
      if (!copy(child, next)) return false;
      target.appendChild(next);
    }
    return true;
  };
  if (!copy(doc.documentElement, clean.documentElement)) return;
  clean.documentElement.setAttribute("width", "64");
  clean.documentElement.setAttribute("height", "64");
  return new XMLSerializer().serializeToString(clean);
}

export async function snapshotReferenceFavicon(value: unknown): Promise<string | undefined> {
  const raster = referenceFavicon(value);
  if (raster) return raster;
  const icon = browserFavicon(value);
  if (!icon?.startsWith("data:image/svg+xml;") || typeof Image === "undefined") return;
  try {
    const decoded = atob(icon.slice(icon.indexOf(",") + 1));
    const svg = staticSVG(new TextDecoder("utf-8", { fatal: true }).decode(Uint8Array.from(decoded, char => char.charCodeAt(0))));
    if (!svg) return;
    const bytes = new TextEncoder().encode(svg);
    const source = "data:image/svg+xml;base64," + btoa(Array.from(bytes, byte => String.fromCharCode(byte)).join(""));
    const image = new Image();
    await new Promise<void>((resolve, reject) => {
      const timer = setTimeout(() => { image.onload = image.onerror = null; image.src = ""; reject(new Error("Icon decode timed out")); }, 1500);
      image.onload = () => { clearTimeout(timer); resolve(); };
      image.onerror = () => { clearTimeout(timer); reject(new Error("Icon decode failed")); };
      image.src = source;
    });
    const canvas = document.createElement("canvas");
    canvas.width = canvas.height = 64;
    const context = canvas.getContext("2d");
    if (!context) return;
    context.drawImage(image, 0, 0, 64, 64);
    return referenceFavicon(canvas.toDataURL("image/png"));
  } catch { return; }
}
