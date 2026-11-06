import { getLocale } from "@/i18n";
// Relative time for the sidebar's session rows: "5 分钟前", "3 小时前".
// Intl.RelativeTimeFormat does the language work; this file only picks the
// unit — and keeps that choice testable without a clock.

const UNITS: [Intl.RelativeTimeFormatUnit, number][] = [
  ["year", 365 * 24 * 3600],
  ["month", 30 * 24 * 3600],
  ["week", 7 * 24 * 3600],
  ["day", 24 * 3600],
  ["hour", 3600],
  ["minute", 60],
];

/**
 * fmtRel renders epoch-ms as a compact relative label. The absolute time is
 * the caller's to attach as a tooltip — the list shows relative, the hover
 * shows exact, the pattern every researched product settled on.
 */
export function fmtRel(ms: number, now = Date.now(), locale: string = getLocale()): string {
  const rtf = new Intl.RelativeTimeFormat(locale, { style: "narrow" });
  const diff = Math.max(0, Math.floor((now - ms) / 1000));
  for (const [unit, secs] of UNITS) {
    if (diff >= secs) {
      return rtf.format(-Math.floor(diff / secs), unit);
    }
  }
  return rtf.format(0, "minute"); // "0 分钟前" — this minute
}

/** baseName is a project group's label: the directory's own name, with the
 * full path left to the tooltip — the switcher research was unanimous that
 * users identify projects by basename and disambiguate only on demand. */
export function baseName(path: string): string {
  const p = path.replace(/\/+$/, "");
  const at = p.lastIndexOf("/");
  return at >= 0 ? p.slice(at + 1) : p || "/";
}
