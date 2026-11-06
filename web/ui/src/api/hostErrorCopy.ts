import { getLocale, gt } from "@/i18n";
import templates from "@/i18n/host-error-templates.json";

const escapePattern = (value: string) => value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
const goFormat = /%(?:\[\d+\])?[-+# 0]*(?:\d+|\*)?(?:\.(?:\d+|\*))?[a-zA-Z%]/g;
function formatValuePattern(format: string): string {
  switch (format.charAt(format.length - 1)) {
    case "d": return "[-+]?\\d+";
    case "b": return "[-+]?[01]+";
    case "o": case "O": return "[-+]?(?:0o)?[0-7]+";
    case "x": case "X": return "(?:0[xX])?[0-9a-fA-F]+";
    case "t": return "(?:true|false)";
    case "f": case "F": case "e": case "E": case "g": case "G": return "[-+]?(?:\\d+(?:\\.\\d*)?|\\.\\d+)(?:[eE][-+]?\\d+)?";
    case "q": return '"(?:[^"\\\\]|\\\\.)*"|`[^`]*`';
    default: return "[\\s\\S]*?";
  }
}
const exact = new Map(templates.filter(entry => !entry.source.includes("%")).map(entry => [entry.source, entry.key]));
const patterns = templates.filter(entry => entry.source.includes("%"))
  .sort((a, b) => b.source.length - a.source.length).map(entry => {
    const fields: { name: string; nested: boolean }[] = [];
    let pattern = "^", offset = 0;
    for (const match of entry.source.matchAll(goFormat)) {
      pattern += escapePattern(entry.source.slice(offset, match.index));
      if (match[0] === "%%") pattern += "%";
      else { fields.push({ name: `p${fields.length}`, nested: match[0].endsWith("w") }); pattern += `(${formatValuePattern(match[0])})`; }
      offset = match.index! + match[0].length;
    }
    return { key: entry.key, fields, pattern: new RegExp(pattern + escapePattern(entry.source.slice(offset)) + "$") };
  });

/** Localize only recognized host error templates. Names, paths, external tool
 * messages and other dynamic values are never submitted to a translator. */
export function localizeHostError(message: string, depth = 0): string {
  if (getLocale() === "zh-CN" || !/\p{Script=Han}/u.test(message)) return message;
  const key = exact.get(message);
  if (key) return gt(`hostErrors.${key}`);
  for (const entry of patterns) {
    const match = entry.pattern.exec(message);
    if (!match) continue;
    const params = Object.fromEntries(entry.fields.map((field, index) => [field.name,
      field.nested && depth < 2 ? localizeHostError(match[index + 1], depth + 1) : match[index + 1]]));
    return gt(`hostErrors.${entry.key}`, params);
  }
  return message;
}

/** Keep exact original diagnostics and status/code alongside localized copy. */
export function hostAPIError(value: unknown): Error & { originalMessage: string; code?: string } {
  const nested = value && typeof value === "object" ? value as { message?: unknown; code?: unknown } : undefined;
  const message = typeof value === "string" ? value : typeof nested?.message === "string" ? nested.message : String(value ?? "");
  return Object.assign(new Error(localizeHostError(message)), { originalMessage: message,
    ...(typeof nested?.code === "string" ? { code: nested.code } : {}) });
}
