import { describe, expect, it, vi } from "vitest";
import { useAgentStream } from "./useAgentStream";
import type { WebServiceRecord } from "@/api/webServices";
const record: WebServiceRecord = { service_id: "ws_one", name: "counter", profile: "python-web", project_root: "app", generation: 1, revision: 2, state: "ready", lease_expires_at: "2026-11-05T20:00:00Z" };
describe("managed website state events", () => {
  it("accepts only current live host records, without deriving availability from tool history", () => {
    vi.stubGlobal("fetch", vi.fn(() => new Promise(() => {})));
    const stream = useAgentStream({ attachments: false });
    try {
      void stream.connect("current");
      const event = { type: "web_service_changed" as const, seq: 1, ts: 1, web_service: record };
      stream.apply(event); expect(stream.webServiceChanged.value).toBeNull();
      stream.apply(event, { sessionId: "current", replay: true }); expect(stream.webServiceChanged.value).toBeNull();
      stream.apply(event, { sessionId: "other", replay: false }); expect(stream.webServiceChanged.value).toBeNull();
      stream.apply({ ...event, web_service: { ...record, generation: -1 } }, { sessionId: "current", replay: false }); expect(stream.webServiceChanged.value).toBeNull();
      stream.apply(event, { sessionId: "current", replay: false }); expect(stream.webServiceChanged.value).toEqual({ sessionId: "current", record });
      void stream.connect("next"); expect(stream.webServiceChanged.value).toBeNull();
      stream.apply(event, { sessionId: "current", replay: false }); expect(stream.webServiceChanged.value).toBeNull();
    } finally { stream.disconnect(); vi.unstubAllGlobals(); }
  });
});
