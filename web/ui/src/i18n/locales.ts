/** Stable locale IDs are also used in saved preferences and HTTP headers. */
export const SUPPORTED_LOCALES = ["zh-CN", "zh-TW", "en", "ja", "ko", "de", "fr", "es"] as const;
export type Locale = (typeof SUPPORTED_LOCALES)[number];
export const LOCALE_LABELS: Record<Locale, string> = {
  "zh-CN": "简体中文", "zh-TW": "繁體中文", en: "English", ja: "日本語",
  ko: "한국어", de: "Deutsch", fr: "Français", es: "Español",
};

/** Match regional/script variants without misclassifying Traditional Chinese. */
export function resolveLocale(value: unknown): Locale | null {
  if (typeof value !== "string") return null;
  const tag = value.trim().replace(/_/g, "-").toLowerCase();
  if (/^zh(?:-|$)/.test(tag)) {
    if (/(?:^|-)hans(?:-|$)/.test(tag)) return "zh-CN";
    if (/(?:^|-)(hant|tw|hk|mo)(?:-|$)/.test(tag)) return "zh-TW";
    return "zh-CN";
  }
  const language = tag.split("-")[0];
  return ["en", "ja", "ko", "de", "fr", "es"].includes(language) ? language as Locale : null;
}

export function preferredLocale(saved: unknown, browserLanguages: readonly string[]): Locale {
  const explicit = resolveLocale(saved);
  if (explicit) return explicit;
  for (const language of browserLanguages) {
    const match = resolveLocale(language);
    if (match) return match;
  }
  return "en";
}
