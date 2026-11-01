import { describe, expect, it } from "vitest";
import { browserFavicon } from "./browserFavicon";

describe("browser favicon display sources", () => {
  it("accepts bounded image bytes while refusing URL fetches and documents", () => {
    const icon = "data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=";
    expect(browserFavicon(icon)).toBe(icon);
    for (const value of [undefined, "https://site.test/favicon.ico", "http://192.168.1.1/icon.svg", "file:///private/icon.png", "javascript:alert(1)", "data:text/html;base64,PGgxPmJhZDwvaDE+", "data:image/svg+xml,<svg/>", "data:image/png;base64,", "data:image/png;base64,abc!", "data:image/png;base64,YQ=", "data:image/png;base64," + "A".repeat(87384)]) expect(browserFavicon(value)).toBeUndefined();
  });
});
