import { describe, expect, it } from "vitest";
import { normalizeUrl } from "./browserUrl";
const BASE = "http://localhost:17777/panels/样本库/";
describe("normalizeUrl", () => {
  it("resolves same-origin paths and hashes against the pane's base", () => {
    expect(normalizeUrl("/panels/shell/", BASE)).toBe("http://localhost:17777/panels/shell/");
    // href percent-encodes the base's CJK segment; same URL, canonical spelling.
    expect(normalizeUrl("#/sample/abc", BASE)).toBe(
      "http://localhost:17777/panels/%E6%A0%B7%E6%9C%AC%E5%BA%93/#/sample/abc",
    );
  });

  it("adds http:// to scheme-less input", () => {
    expect(normalizeUrl("example.com", BASE)).toBe("http://example.com/");
    expect(normalizeUrl("example.com/x?y=1", BASE)).toBe("http://example.com/x?y=1");
  });

  it("treats an unknown 'scheme' as host:port, not a scheme", () => {
    expect(normalizeUrl("localhost:8080", BASE)).toBe("http://localhost:8080/");
  });

  it("keeps http and https as given", () => {
    expect(normalizeUrl("https://example.com", BASE)).toBe("https://example.com/");
  });

  it("refuses script-capable and non-http schemes — they must never reach a frame src", () => {
    expect(normalizeUrl("javascript:alert(1)", BASE)).toBeNull();
    expect(normalizeUrl("data:text/html,<b>", BASE)).toBeNull();
    expect(normalizeUrl("file:///etc/passwd", BASE)).toBeNull();
    expect(normalizeUrl("about:blank", BASE)).toBeNull();
  });

  it("refuses empty input", () => {
    expect(normalizeUrl("   ", BASE)).toBeNull();
  });
});


describe("hostname validation shared by browser entrypoints", () => {
  it("rejects ASCII, full-width and percent-encoded commas and other invalid DNS labels", () => {
    for (const value of ["http://www,example.com/", "https://www，example.com/", "www,example.com", "http://www%2cexample.com/", "http://www%EF%BC%8Cexample.com/", "https://foo_bar.example/", "https://a..example/", "https://-a.example/", "https://a-.example/", `https://${"a".repeat(64)}.example/`]) expect(normalizeUrl(value, BASE), value).toBeNull();
  });
  it("preserves explicit protocols, DNS/IDNA names, local hosts, IP literals and ports", () => {
    for (const [value, expected] of [
      ["http://www.example.com/", "http://www.example.com/"],
      ["https://www.example.com/", "https://www.example.com/"],
      ["http://localhost:8080/", "http://localhost:8080/"],
      ["http://sample-lib:17777/app", "http://sample-lib:17777/app"],
      ["http://127.0.0.1:17777/", "http://127.0.0.1:17777/"],
      ["http://192.168.1.10:3000/", "http://192.168.1.10:3000/"],
      ["http://[::1]:8080/", "http://[::1]:8080/"],
      ["http://[2001:db8::1]/", "http://[2001:db8::1]/"],
      ["https://例子.测试/", "https://xn--fsqu00a.xn--0zwm56d/"],
      ["http://example.com./", "http://example.com./"],
    ]) expect(normalizeUrl(value, BASE)).toBe(expected);
  });
  it("validates the hostname only, keeping legitimate commas in paths, queries and fragments", () => {
    expect(normalizeUrl("http://example.com/a,b?tags=x,y#part,2", BASE)).toBe("http://example.com/a,b?tags=x,y#part,2");
  });
});
