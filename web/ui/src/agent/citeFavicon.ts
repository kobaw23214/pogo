import { shallowReactive, type InjectionKey } from "vue";
import type { CiteSource } from "@/api/types";
import { browserFavicon } from "./browserFavicon";
import { safeSourceURL } from "./citePreview";

/** Match the observed document, not a domain guessed from a title. Queries can
 * select different apps/accounts; fragments do not change the document icon. */
export function citeFaviconURL(source: Pick<CiteSource, "locator">): string | undefined {
  const value = safeSourceURL(source);
  if (!value) return;
  const url = new URL(value);
  url.hash = "";
  return url.href;
}

export interface CiteFavicons {
  get(source: Pick<CiteSource, "locator" | "favicon">): string | undefined;
  observe(pages: readonly { url: string; favicon?: string }[]): void;
  /** Load saved metadata without overwriting a newer live observation. */
  hydrate(pages: readonly { url: string; favicon?: string }[]): void;
  clear(): void;
}

/** A view owns this archive and clears it when changing conversations. The
 * server persists the same metadata independently of browser connections.
 * Never fetch a website URL or share icons with another session/share page. */
export const citeFaviconKey: InjectionKey<CiteFavicons> = Symbol("citeFavicons");
export function createCiteFavicons(): CiteFavicons {
  const icons = shallowReactive(new Map<string, string>());
  return {
    get(source) {
      const url = citeFaviconURL(source);
      if (!url) return;
      return browserFavicon(source.favicon) ?? icons.get(url);
    },
    observe(pages) {
      const observed = new Map<string, string | undefined>();
      for (const page of pages) {
        const url = citeFaviconURL({ locator: page.url });
        const icon = browserFavicon(page.favicon);
        if (!url) continue;
        // A duplicate tab may still be loading its icon while another tab
        // already has it. One batch must not depend on tab ordering.
        if (!observed.has(url) || icon) observed.set(url, icon);
      }
      for (const [url, icon] of observed) {
        // This is historical source identity, not the live tab indicator.
        // An unavailable/loading page must not erase a previously read icon.
        if (!icon) continue;
        if (icons.get(url) === icon) continue;
        icons.delete(url);
        icons.set(url, icon);
        if (icons.size > 64) icons.delete(icons.keys().next().value!);
      }
    },
    hydrate(pages) {
      for (const page of pages) {
        const url = citeFaviconURL({ locator: page.url });
        const icon = browserFavicon(page.favicon);
        if (!url || !icon || icons.has(url)) continue;
        if (icons.size >= 64) break;
        icons.set(url, icon);
      }
    },
    clear() { icons.clear(); },
  };
}

export function citeSourceFavicon(source: CiteSource, cache?: CiteFavicons | null): string | undefined {
  if (!citeFaviconURL(source)) return;
  return browserFavicon(source.favicon) ?? cache?.get(source);
}
