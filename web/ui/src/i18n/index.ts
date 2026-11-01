import { createI18n } from "vue-i18n";
import type { Composer } from "vue-i18n";

// Locales the UI ships. zh-CN is the fallback because the codebase's source
// strings were originally written in Chinese; everything else reads English
// rather than a translation nobody maintains.
export const SUPPORTED_LOCALES = ["zh-CN", "en"] as const;
export type Locale = (typeof SUPPORTED_LOCALES)[number];

export const LOCALE_LABELS: Record<Locale, string> = {
  "zh-CN": "中文",
  en: "English",
};

interface MessageModule {
  namespace: string;
  // Message tables are nested key/text maps; vue-i18n validates them at
  // runtime, so keeping the value type loose here avoids fighting generics.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  messages: Partial<Record<Locale, Record<string, any>>>;
}

// Each file under ./messages owns one namespace and carries both locales,
// so per-component copy lives in a single file and namespaces never collide.
const modules = import.meta.glob<{ default: MessageModule }>("./messages/*.ts", {
  eager: true,
});

// eslint-disable-next-line @typescript-eslint/no-explicit-any
const messages: Record<Locale, Record<string, any>> = {
  "zh-CN": {},
  en: {},
};
for (const mod of Object.values(modules)) {
  const { namespace, messages: nsMessages } = mod.default;
  for (const locale of SUPPORTED_LOCALES) {
    const table = nsMessages[locale];
    if (table) messages[locale][namespace] = table;
  }
}

const STORAGE_KEY = "pogo-locale";

function detectLocale(): Locale {
  try {
    const saved =
      typeof localStorage !== "undefined" ? localStorage.getItem(STORAGE_KEY) : null;
    if (saved && (SUPPORTED_LOCALES as readonly string[]).includes(saved)) {
      return saved as Locale;
    }
  } catch {
    // private mode etc. — fall through to navigator
  }
  const nav = typeof navigator !== "undefined" ? navigator.language : "";
  if (nav.startsWith("zh")) return "zh-CN";
  return "en";
}

const shortDateTime: Intl.DateTimeFormatOptions = {
  year: "numeric",
  month: "numeric",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  hour12: false,
};
const datetimeFormats = {
  "zh-CN": { short: shortDateTime },
  en: {
    short: {
      month: "short",
      day: "numeric",
      year: "numeric",
      hour: "2-digit",
      minute: "2-digit",
      hour12: false,
    } satisfies Intl.DateTimeFormatOptions,
  },
};

export const i18n = createI18n({
  legacy: false,
  locale: detectLocale(),
  fallbackLocale: "zh-CN",
  messages,
  datetimeFormats,
});

// createI18n's generics type `global` loosely; with legacy:false it is always
// a Composer at runtime, whose locale is a writable ref.
const globalComposer = () => i18n.global as unknown as Composer;

export function setLocale(locale: Locale) {
  globalComposer().locale.value = locale;
  try {
    localStorage.setItem(STORAGE_KEY, locale);
  } catch {
    // private mode — session-only locale is fine
  }
}

// For non-component modules (agent/timeline.ts, agent/useAgentStream.ts, ...)
// that can't call useI18n(). Components should prefer useI18n() instead.
export function gt(key: string, params?: Record<string, unknown>): string {
  const t = globalComposer().t;
  return params ? t(key, params) : t(key);
}
