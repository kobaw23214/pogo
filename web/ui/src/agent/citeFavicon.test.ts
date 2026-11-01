import { afterEach, describe, expect, it, vi } from "vitest";
import type { CiteSource } from "@/api/types";
import { collectSources } from "./cite";
import { citeFaviconURL, citeSourceFavicon, createCiteFavicons } from "./citeFavicon";

const iconA = "data:image/png;base64,AAAA";
const iconB = "data:image/png;base64,AQID";
function source(locator = "https://example.test/page", extra: Partial<CiteSource> = {}): CiteSource {
  return { label: "S1", type: "web_page", title: "Observed page", locator, call_id: "call-source", excerpt: "Observed evidence", hash: "evidence-hash", ts: 1, ...extra };
}
afterEach(() => vi.restoreAllMocks());

describe("citation favicon document identity", () => {
  it("normalizes an observed HTTP URL while retaining its path and query", () => {
    expect(citeFaviconURL(source("HTTPS://Example.test:443/account?a=1#section"))).toBe("https://example.test/account?a=1");
    expect(citeFaviconURL(source("http://example.test:8080/"))).toBe("http://example.test:8080/");
  });

  it("rejects unsafe or non-web locators even when a valid image is present", () => {
    for (const locator of ["javascript:alert(1)", "file:///tmp/page.html", "about:blank", "https://user:secret@example.test/", "https://example.test/\nprivate", "/relative", "not a url"]) {
      const stamp = source(locator, { favicon: iconA });
      expect(citeFaviconURL(stamp)).toBeUndefined();
      expect(citeSourceFavicon(stamp)).toBeUndefined();
    }
  });

  it("never substitutes another path, account query, protocol, port or hostname", () => {
    const cache = createCiteFavicons();
    cache.observe([{ url: "https://example.test/app?account=alice", favicon: iconA }]);
    expect(cache.get(source("https://example.test/app?account=alice#record"))).toBe(iconA);
    for (const locator of ["https://example.test/other?account=alice", "https://example.test/app?account=bob", "http://example.test/app?account=alice", "https://example.test:8443/app?account=alice", "https://other.test/app?account=alice"]) expect(cache.get(source(locator))).toBeUndefined();
  });
});

describe("view-owned citation favicon observations", () => {
  it("does not share private icons across view caches and synchronously drops them on clear", () => {
    const first = createCiteFavicons(), next = createCiteFavicons();
    first.observe([{ url: "https://example.test/page", favicon: iconA }]);
    expect(citeSourceFavicon(source(), first)).toBe(iconA);
    expect(citeSourceFavicon(source(), next)).toBeUndefined();
    first.clear();
    expect(citeSourceFavicon(source(), first)).toBeUndefined();
    next.observe([{ url: "https://example.test/page", favicon: iconB }]);
    expect(citeSourceFavicon(source(), next)).toBe(iconB);
    expect(citeSourceFavicon(source(), first)).toBeUndefined();
  });

  it("prefers the source's recorded icon over later live metadata for the same URL", () => {
    const cache = createCiteFavicons();
    cache.observe([{ url: "https://example.test/page", favicon: iconB }]);
    expect(citeSourceFavicon(source(undefined, { favicon: iconA }), cache)).toBe(iconA);
    cache.clear();
    expect(citeSourceFavicon(source(undefined, { favicon: iconA }), cache)).toBe(iconA);
  });

  it("keeps recorded source identity when a newer live observation has no usable icon", () => {
    const cache = createCiteFavicons();
    cache.observe([{ url: "https://example.test/page", favicon: iconA }]);
    cache.observe([{ url: "https://example.test/page" }]);
    expect(cache.get(source())).toBe(iconA);
    cache.observe([{ url: "https://example.test/page", favicon: iconA }]);
    cache.observe([{ url: "https://example.test/page", favicon: "https://tracking.test/icon.png" }]);
    expect(cache.get(source())).toBe(iconA);
    // Historical evidence keeps its own recorded icon even if the live page
    // has changed its account, removed its icon or failed to load.
    expect(citeSourceFavicon(source(undefined, { favicon: iconA }), cache)).toBe(iconA);
  });

  it("keeps an observed icon when another tab of the same URL has not loaded its icon yet", () => {
    const cache = createCiteFavicons();
    const url = "https://example.test/page";
    for (const pages of [
      [{ url, favicon: iconA }, { url }],
      [{ url }, { url, favicon: iconA }],
      [{ url, favicon: "https://tracking.test/icon.png" }, { url, favicon: iconA }, { url }],
    ]) {
      cache.clear();
      cache.observe(pages);
      expect(cache.get(source())).toBe(iconA);
    }
    cache.observe([{ url }, { url, favicon: "data:text/html;base64,AAAA" }]);
    expect(cache.get(source())).toBe(iconA);
  });

  it("restores saved session icons without any live browser or changing a newer observation", () => {
    const cache = createCiteFavicons();
    cache.hydrate([{ url: "https://example.test/page#previous", favicon: iconA }]);
    expect(cache.get(source())).toBe(iconA);
    cache.observe([{ url: "https://example.test/page", favicon: iconB }]);
    cache.hydrate([{ url: "https://example.test/page", favicon: iconA }]);
    expect(cache.get(source())).toBe(iconB);
    cache.observe([]);
    cache.observe([{ url: "about:blank" }]);
    expect(cache.get(source())).toBe(iconB);
    cache.clear();
    expect(cache.get(source())).toBeUndefined();
  });

  it("does not fall back to a remote image URL or make any network request", () => {
    const fetch = vi.spyOn(globalThis, "fetch");
    const cache = createCiteFavicons();
    cache.observe([{ url: "https://example.test/page", favicon: iconA }]);
    expect(citeSourceFavicon(source(undefined, { favicon: "https://tracking.test/icon.png" }), cache)).toBe(iconA);
    expect(citeSourceFavicon(source("https://unobserved.test/", { favicon: "https://tracking.test/icon.png" }), cache)).toBeUndefined();
    expect(fetch).not.toHaveBeenCalled();
  });

  it("bounds fallback retention without evicting independent recorded evidence", () => {
    const cache = createCiteFavicons();
    cache.observe(Array.from({ length: 65 }, (_, index) => ({ url: `https://example.test/page/${index}`, favicon: iconA })));
    expect(cache.get(source("https://example.test/page/0"))).toBeUndefined();
    expect(cache.get(source("https://example.test/page/1"))).toBe(iconA);
    expect(cache.get(source("https://example.test/page/64"))).toBe(iconA);
    expect(citeSourceFavicon(source("https://example.test/page/0", { favicon: iconB }), cache)).toBe(iconB);
  });
});

describe("favicon evidence from original tool details", () => {
  it("copies bounded image data only when result and source URLs agree exactly", () => {
    const stamp = source();
    const [matching] = collectSources([{ result: { source: stamp, details: { url: stamp.locator, favicon: iconA }, text: "Evidence" } }]);
    expect(matching.favicon).toBe(iconA);
    expect(matching.content).toBe("Evidence");
    for (const url of [undefined, "https://example.test/other", "https://example.test/page?account=bob", "https://example.test/page#another"]) {
      const [mismatched] = collectSources([{ result: { source: stamp, details: { url, favicon: iconB } } }]);
      expect(mismatched.favicon).toBeUndefined();
    }
  });

  it("rejects unbounded or invalid detail bytes while preserving ordinary source collection", () => {
    const stamp = source();
    for (const favicon of ["https://tracking.test/icon.png", "data:text/html;base64,AAAA", `data:image/png;base64,${"A".repeat(90000)}`, 123]) {
      const [collected] = collectSources([{ result: { source: stamp, details: { url: stamp.locator, favicon } } }]);
      expect(collected.label).toBe(stamp.label);
      expect(collected.favicon).toBeUndefined();
    }
  });
});
