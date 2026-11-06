import { createI18n } from "vue-i18n";
import type { Composer } from "vue-i18n";
import { watch } from "vue";
import { SUPPORTED_LOCALES, preferredLocale, resolveLocale, type Locale } from "./locales";
export { SUPPORTED_LOCALES, LOCALE_LABELS, preferredLocale, resolveLocale, type Locale } from "./locales";

export interface MessageTree { [key: string]: string | MessageTree }
interface MessageModule { namespace: string; messages: Partial<Record<Locale, MessageTree>> }
const modules = import.meta.glob<{ default: MessageModule }>("./messages/*.ts", { eager: true });
// Language packs are bundled locally: changing a preference never sends user
// content to a translation service and works without an external network.
const packs = import.meta.glob<{ default: MessageTree }>("./locales/*.json", { eager: true });
export const messageCatalogs = Object.fromEntries(SUPPORTED_LOCALES.map(locale => [locale, {}])) as Record<Locale, MessageTree>;
for (const { default: mod } of Object.values(modules)) {
  for (const locale of SUPPORTED_LOCALES) {
    const table = mod.messages[locale];
    if (table) messageCatalogs[locale][mod.namespace] = table;
  }
}
for (const [path, mod] of Object.entries(packs)) {
  const locale = path.slice(path.lastIndexOf("/") + 1, -5);
  if ((SUPPORTED_LOCALES as readonly string[]).includes(locale)) Object.assign(messageCatalogs[locale as Locale], mod.default);
}

const STORAGE_KEY = "pogo-locale";
function detectLocale(): Locale {
  let saved: string | null = null;
  try { saved = typeof localStorage !== "undefined" ? localStorage.getItem(STORAGE_KEY) : null; } catch { /* Private mode. */ }
  const languages = typeof navigator === "undefined" ? [] : navigator.languages?.length ? navigator.languages : [navigator.language];
  return preferredLocale(saved, languages);
}
const short: Intl.DateTimeFormatOptions = { year: "numeric", month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false };
const datetimeFormats = Object.fromEntries(SUPPORTED_LOCALES.map(locale => [locale, { short: locale === "en" ? { ...short, month: "short" as const } : short }]));
export const i18n = createI18n({ legacy: false, locale: detectLocale(), fallbackLocale: "en", messages: messageCatalogs, datetimeFormats });
const globalComposer = () => i18n.global as unknown as Composer;
export function getLocale(): Locale { return resolveLocale(globalComposer().locale.value) ?? "en"; }
export function setLocale(locale: Locale | string): void {
  const resolved = resolveLocale(locale);
  if (!resolved) return;
  globalComposer().locale.value = resolved;
  try { localStorage.setItem(STORAGE_KEY, resolved); } catch { /* Session-only preference. */ }
}
watch(() => globalComposer().locale.value, locale => {
  if (typeof document !== "undefined" && document.documentElement) document.documentElement.lang = resolveLocale(locale) ?? "en";
}, { immediate: true, flush: "sync" });
if (typeof window !== "undefined" && typeof window.addEventListener === "function") window.addEventListener("storage", event => {
  if (event.key !== STORAGE_KEY) return;
  const locale = resolveLocale(event.newValue);
  if (locale) globalComposer().locale.value = locale;
});

/** Shared translation entry for presenters outside Vue component setup. */
export function gt(key: string, params?: Record<string, unknown>): string {
  const t = globalComposer().t;
  return params ? t(key, params) : t(key);
}
