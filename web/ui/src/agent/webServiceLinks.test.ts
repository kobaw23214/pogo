import { describe, expect, it } from "vitest";
import { webServiceLink } from "./webServiceLinks";
describe("owner website link", () => {
  it("extracts only stable conversation/service references", () => {
    expect(webServiceLink("?c=session_one&web_service=ws_one&token=host-token")).toEqual({ present: true, sessionId: "session_one", serviceId: "ws_one" });
    expect(webServiceLink("?c=session_one")).toEqual({ present: false, sessionId: null, serviceId: null });
  });
  it("does not substitute a default conversation for invalid, absent or ambiguous scope", () => {
    expect(webServiceLink("?web_service=ws_one").sessionId).toBeNull();
    expect(webServiceLink("?c=one&c=other&web_service=ws_one").sessionId).toBeNull();
    expect(webServiceLink("?c=one&web_service=ws_one&web_service=ws_other").serviceId).toBeNull();
    expect(webServiceLink("?c=one&web_service=../../admin").serviceId).toBeNull();
  });
});
