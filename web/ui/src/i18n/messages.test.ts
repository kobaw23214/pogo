// vue-i18n compiles message strings on first use, and its message grammar
// treats {...} as interpolation. A literal brace in a message — a JSON example
// like {"parameters":{}} in a hint, say — therefore throws SyntaxError the
// moment the message is first rendered, which for a dialog means the moment it
// opens; that is a production breakage no type checker sees.
//
// The guard translates every message of every namespace through a real
// vue-i18n instance, because the lower-level baseCompile merely collects
// errors instead of throwing — the throw lives in the compiler options
// createI18n wires up, so that is the path that must be exercised.
import { describe, expect, it } from "vitest";
import { createI18n } from "vue-i18n";

const modules = import.meta.glob<{ default: { namespace: string; messages: Record<string, Record<string, string>> } }>(
  "./messages/*.ts",
  { eager: true },
);

describe("i18n messages compile", () => {
  for (const [path, mod] of Object.entries(modules)) {
    const { namespace, messages } = mod.default;
    for (const [locale, dict] of Object.entries(messages)) {
      for (const [key, text] of Object.entries(dict)) {
        it(`${namespace}.${key} (${locale})`, () => {
          const i18n = createI18n({ legacy: false, locale, messages: { [locale]: { [namespace]: dict } } });
          expect(() =>
            // eslint-disable-next-line @typescript-eslint/no-explicit-any
            (i18n.global as any).t(`${namespace}.${key}`),
            `${path}: ${text}`,
          ).not.toThrow();
        });
      }
    }
  }
});
