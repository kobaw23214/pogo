import { describe, expect, it } from "vitest";
import { browserErrorDetails, browserRecoveryReason, browserReopenURL } from "./browserRecovery";

describe("browser identity recovery", () => {
  it("accepts dedicated HTTP and WebSocket identity codes", () => {
    expect(browserRecoveryReason({ code: "BROWSER_CONTEXT_CHANGED", status: 409 })).toBe("context_changed");
    expect(browserRecoveryReason({ code: "BROWSER_TAB_CLOSED", status: 410 })).toBe("tab_closed");
    expect(browserRecoveryReason({ code: "BROWSER_SESSION_EXPIRED", status: 404 })).toBe("session_expired");
    expect(browserRecoveryReason({ code: "BROWSER_SESSION_EXPIRED", status: 410 })).toBe("session_expired");
    expect(browserRecoveryReason({ code: "BROWSER_CONTEXT_CHANGED" })).toBe("context_changed");
    expect(browserRecoveryReason({ code: "BROWSER_CONTEXT_CHANGED", status: 403 })).toBeUndefined();
    expect(browserRecoveryReason({ code: "BROWSER_CONTEXT_CHANGED", status: "409" })).toBeUndefined();
  });
  it("supports old workers only for exact known messages and matching status", () => {
    const message = "browser context changed; this saved tab binding is no longer current";
    expect(browserRecoveryReason({ status: 409, message })).toBe("context_changed");
    expect(browserRecoveryReason({ status: 409, message: `native browser service returned HTTP 409: ${message}` })).toBe("context_changed");
    expect(browserRecoveryReason({ status: 410, message: "bound browser tab is closed" })).toBe("tab_closed");
    expect(browserRecoveryReason({ status: 404, message: "browser session not found" })).toBe("session_expired");
    for (const issue of [{ status: 403, message }, { status: 409, message: `${message}; cancel first` }, { status: 409, message: `Unexpected: ${message}` }, { message }, { status: 404, message: "route not found" }]) expect(browserRecoveryReason(issue)).toBeUndefined();
  });
  it("does not recover pending operations, debugger fences, human ownership or generic failures", () => {
    for (const issue of [
      { status: 409, code: "TAB_OPERATION_PENDING", message: "previous browser action is still pending" },
      { status: 409, code: "TAB_OPERATION_OUTCOME_UNKNOWN" },
      { status: 409, code: "PICK_OBSERVATION_PENDING" },
      { status: 409, message: "debugger paused; resume debugging before capturing a fresh frame" },
      { status: 409, message: "this tab is already controlled by a human" },
      { code: "LIVE_FENCED", message: "browser context changed; this saved tab binding is no longer current" },
      { status: 502, message: "service unreachable" }, new TypeError("Failed to fetch"), null,
    ]) expect(browserRecoveryReason(issue)).toBeUndefined();
  });
});
it("reopens only an absolute safe HTTP URL and preserves the recorded path/query", () => {
  expect(browserReopenURL("https://example.test/path?a=1#section")).toBe("https://example.test/path?a=1#section");
  expect(browserReopenURL("http://127.0.0.1:8929/project")).toBe("http://127.0.0.1:8929/project");
  for (const value of [undefined, "about:blank", "file:///tmp/x", "javascript:alert(1)", "example.test", "/path", "https://user:secret@example.test/", "https://example.test/\nprivate", "https://www,example.com/"]) expect(browserReopenURL(value)).toBeUndefined();
});
it("bounds optional diagnostics without turning unknown objects into product copy", () => {
  expect(browserErrorDetails(new Error("raw details"))).toBe("raw details");
  expect(browserErrorDetails("x".repeat(5000))).toHaveLength(4096);
  expect(browserErrorDetails({ secret: "do not stringify" })).toBe("");
});
