import { afterEach, describe, expect, it, vi } from "vitest";
import { preferredLocale, resolveLocale, SUPPORTED_LOCALES, LOCALE_LABELS } from "./locales";
import { getLocale, i18n, setLocale } from "./index";

afterEach(() => { vi.unstubAllGlobals(); setLocale("en"); });
describe("interface language selection", () => {
  it("ships all eight requested languages with native names", () => {
    expect(SUPPORTED_LOCALES).toEqual(["zh-CN", "zh-TW", "en", "ja", "ko", "de", "fr", "es"]);
    expect(Object.keys(LOCALE_LABELS).sort()).toEqual([...SUPPORTED_LOCALES].sort());
    expect(LOCALE_LABELS["zh-TW"]).toBe("繁體中文");
  });
  it.each([
    ["zh-TW", "zh-TW"], ["zh-Hant", "zh-TW"], ["zh-HK", "zh-TW"], ["zh-MO", "zh-TW"],
    ["zh-Hant-HK", "zh-TW"], ["zh-Hans-SG", "zh-CN"], ["zh-Hans-TW", "zh-CN"], ["zh", "zh-CN"],
    ["en-US", "en"], ["EN_gb", "en"], ["ja-JP", "ja"], ["ko-KR", "ko"], ["de-AT", "de"], ["fr-CA", "fr"], ["es-MX", "es"],
  ])("resolves %s to %s", (input, expected) => { expect(resolveLocale(input)).toBe(expected); });
  it("uses the saved choice, then all preferred browser languages, then English", () => {
    expect(preferredLocale("zh-TW", ["en-US"])).toBe("zh-TW");
    expect(preferredLocale("removed-locale", ["pt-BR", "ko-KR", "en"])).toBe("ko");
    expect(preferredLocale(null, ["it-IT"])).toBe("en");
    expect(resolveLocale("unsupported")).toBeNull();
  });
  it("keeps preference and document lang in sync without accepting unknown values", () => {
    const setItem = vi.fn();
    vi.stubGlobal("localStorage", { setItem });
    vi.stubGlobal("document", { documentElement: { lang: "" } });
    setLocale("ja-JP");
    expect(getLocale()).toBe("ja");
    expect(document.documentElement.lang).toBe("ja");
    expect(setItem).toHaveBeenLastCalledWith("pogo-locale", "ja");
    setLocale("unsupported");
    expect(getLocale()).toBe("ja");
    expect(setItem).toHaveBeenCalledTimes(1);
    expect(i18n.global.fallbackLocale.value).toBe("en");
  });
  it("still changes language when private mode denies persistence", () => {
    vi.stubGlobal("localStorage", { setItem() { throw new Error("denied"); } });
    expect(() => setLocale("fr")).not.toThrow();
    expect(getLocale()).toBe("fr");
  });
});
