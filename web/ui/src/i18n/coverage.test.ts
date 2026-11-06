import { describe, expect, it } from "vitest";
import { createI18n } from "vue-i18n";
import { messageCatalogs, type MessageTree } from "./index";
import { SUPPORTED_LOCALES } from "./locales";

function leaves(tree: MessageTree, prefix = "", result: Record<string, string> = {}): Record<string, string> {
  for (const [key, value] of Object.entries(tree)) {
    if (typeof value === "string") result[prefix + key] = value;
    else leaves(value, prefix + key + ".", result);
  }
  return result;
}
const tables = Object.fromEntries(SUPPORTED_LOCALES.map(locale => [locale, leaves(messageCatalogs[locale])]));
const english = tables.en;
const parameters = (message: string) => [...new Set([...message.matchAll(/\{([A-Za-z_]\w*)\}/g)].map(match => match[1]))].sort();

describe("complete local language packs", () => {
  it("has no Chinese product copy left in the English source catalog", () => {
    expect(Object.entries(english).filter(([, message]) => /\p{Script=Han}/u.test(message))).toEqual([]);
    expect(Object.keys(english).length).toBeGreaterThan(6000);
  });
  for (const locale of SUPPORTED_LOCALES) {
    it(`${locale}: covers every key and preserves interpolation arguments`, () => {
      expect(Object.keys(tables[locale]).sort()).toEqual(Object.keys(english).sort());
      const invalid = Object.entries(english).flatMap(([key, source]) => {
        const translated = tables[locale][key];
        return !translated?.trim() || parameters(source).join() !== parameters(translated).join() || /__POGO_\d+__/.test(translated) ? [key] : [];
      });
      expect(invalid).toEqual([]);
    });
    it(`${locale}: compiles and renders every message through vue-i18n`, () => {
      const instance = createI18n({ legacy: false, locale, fallbackLocale: false, messages: { [locale]: messageCatalogs[locale] }, missingWarn: false });
      for (const [key, message] of Object.entries(tables[locale])) {
        const params = Object.fromEntries(parameters(message).map(name => [name, name === "count" || name === "n" ? 2 : "VALUE"]));
        expect(() => instance.global.t(key, params), `${locale}:${key}`).not.toThrow();
      }
      expect(instance.global.t("agentView.nav.importBadFile")).toContain('{"shortcuts":[{"title":…,"url":…}]}');
    });
  }
});
