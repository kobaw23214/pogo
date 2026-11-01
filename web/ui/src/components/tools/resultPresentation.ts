export interface ResultTextPreview {
  text: string;
  shortened: boolean;
}

/** A literal prefix, never an inferred summary of an external result. */
export function resultTextPreview(text: string, maxChars = 1200, maxLines = 10): ResultTextPreview {
  // Pretty-printed small results often take more lines than their content merits.
  if (text.length <= Math.min(400, maxChars) && maxLines === 10 && text.split("\n").length <= 24) {
    return { text, shortened: false };
  }
  let end = Math.min(text.length, maxChars);
  let offset = 0;
  for (let line = 0; line < maxLines; line++) {
    const newline = text.indexOf("\n", offset);
    if (newline < 0) break;
    if (line === maxLines - 1) end = Math.min(end, newline);
    offset = newline + 1;
  }
  if (end > 0 && end < text.length && /[\uD800-\uDBFF]/.test(text[end - 1]) && /[\uDC00-\uDFFF]/.test(text[end])) end--;
  return { text: text.slice(0, end), shortened: end < text.length };
}

export function resultValueText(value: unknown): string {
  if (typeof value === "string") return value;
  return structuredResultText(value);
}

export function structuredResultText(value: unknown): string {
  try {
    return JSON.stringify(value, null, 2) ?? String(value);
  } catch {
    return String(value);
  }
}

/** Only elide a byte-identical JSON serialization; parsing can round large IDs. */
export function isExactResultJSON(text: string, value: unknown): boolean {
  try {
    const serialized = JSON.stringify(value);
    return serialized !== undefined && text.trim() === serialized;
  } catch {
    return false;
  }
}

export function hasResultArgs(value: unknown): boolean {
  if (value === null || value === undefined) return false;
  if (typeof value === "object") return Object.keys(value).length > 0;
  return true;
}
