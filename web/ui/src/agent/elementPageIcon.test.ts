import { describe, expect, it, vi } from "vitest";
import { createCiteFavicons } from "./citeFavicon";
import { elementPageFavicon } from "./elementPageIcon";
import { formatElementBlock, parsePageElementBlocks } from "./elementPicker";

const iconA = "data:image/png;base64,AAAA";
const iconB = "data:image/png;base64,AQID";
const url = "http://private.test:8929/project?account=a&view=one";

describe("element reference website icons", () => {
  it("uses bounded picked bytes and restores persisted exact-document history", () => {
    const archive = createCiteFavicons();
    archive.hydrate([{ url, favicon: iconA }]);
    expect(elementPageFavicon(url, iconB, archive)).toBe(iconB);
    expect(elementPageFavicon(`${url}#section`, undefined, archive)).toBe(iconA);
    const block = formatElementBlock({ tag: "button", role: "button", name: "Save", text: "Save", selector: "", position: "", html: "" }, { title: "A & B", url });
    const card = parsePageElementBlocks(block).cards[0];
    expect(elementPageFavicon(card.url, undefined, archive)).toBe(iconA);
    expect(block).not.toContain(iconA);
  });

  it("never borrows another page, account, session, protocol, port or host", () => {
    const archive = createCiteFavicons();
    archive.hydrate([{ url, favicon: iconA }]);
    for (const value of [url.replace("project", "other"), url.replace("account=a", "account=b"), url.replace("http:", "https:"), url.replace(":8929", ":9999"), url.replace("private.test", "public.test")]) expect(elementPageFavicon(value, undefined, archive)).toBeUndefined();
    expect(elementPageFavicon(url, undefined, createCiteFavicons())).toBeUndefined();
    archive.clear();
    expect(elementPageFavicon(url, undefined, archive)).toBeUndefined();
  });

  it("does not fetch remote icons or accept unsafe document URLs", () => {
    const fetch = vi.spyOn(globalThis, "fetch");
    const archive = createCiteFavicons();
    archive.hydrate([{ url, favicon: iconA }]);
    expect(elementPageFavicon(url, "http://private.test/favicon.ico", archive)).toBe(iconA);
    expect(elementPageFavicon("https://new.test/", "https://tracking.test/icon.ico", archive)).toBeUndefined();
    for (const value of [undefined, "javascript:alert(1)", "file:///tmp/page", "about:blank", "https://user:secret@site.test/", "https://site.test/\nsecret"]) expect(elementPageFavicon(value, iconB, archive)).toBeUndefined();
    expect(fetch).not.toHaveBeenCalled();
    fetch.mockRestore();
  });
});
