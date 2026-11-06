import { describe, expect, it } from "vitest";
import { parseWebArtifact, validWebArtifactScope } from "./webArtifact";

describe("managed website artifact identity", () => {
  it("accepts a service identity and optional fallback title", () => {
    expect(parseWebArtifact('{"service_id":"ws_example","title":" Demo "}')).toEqual({ service_id: "ws_example", title: "Demo" });
    expect(parseWebArtifact('{"service_id":"ws_example"}')).toEqual({ service_id: "ws_example", title: undefined });
  });
  it("refuses URLs, model-selected sessions, unknown options and malformed IDs", () => {
    for (const source of ["", "null", "[]", "{}", "not json", '{"service_id":"https://example.invalid/"}', '{"service_id":"../ws_one"}', '{"service_id":"ws_one "}', '{"service_id":"session_one"}', '{"service_id":42}', '{"service_id":"ws_one","url":"https://example.invalid/"}', '{"service_id":"ws_one","preview_url":"https://example.invalid/"}', '{"service_id":"ws_one","session_id":"other"}', '{"service_id":"ws_one","html":"<iframe/>"}', '{"service_id":"ws_one","title":" "}', '{"service_id":"ws_one","title":42}', `{"service_id":"ws_one","title":"${"x".repeat(161)}"}`, " ".repeat(5000)]) {
      expect(parseWebArtifact(source), source).toBeNull();
    }
  });
  it("requires a valid host-provided current conversation scope", () => {
    expect(validWebArtifactScope({ sessionId: "20261106T000000Z-chat_one" })).toBe(true);
    expect(validWebArtifactScope(null)).toBe(false);
    expect(validWebArtifactScope({ sessionId: "" })).toBe(false);
    expect(validWebArtifactScope({ sessionId: "../other" })).toBe(false);
  });
});
