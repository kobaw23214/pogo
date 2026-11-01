import { describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { browserMediaRelayURL, readBrowserMediaRelayTicket } from "./browserMediaRelay";

const binding = { context_ref: "ctx_aaaaaaaaaaaaaaaaaaaaaaaa", tab_id: "t1", generation: 2, media_epoch: "media-1" };
const grant = { ...binding, ice_transport_policy: "relay", expires_in: 60, ice_servers: [{ urls: "turns:pogo.example:443?transport=tcp", username: "60:view-fixture", credential: "synthetic-password" }] };
const read = (value: unknown, origin = "https://pogo.example", requestedAt = 1000, now = 1001) => readBrowserMediaRelayTicket(value, binding, origin, requestedAt, now);

describe("same Web-port authenticated media relay", () => {
  it("requires the exact Web authority, including TLS, TCP and IPv6 spelling", () => {
    expect(browserMediaRelayURL("https://pogo.example")).toBe("turns:pogo.example:443?transport=tcp");
    expect(browserMediaRelayURL("http://127.0.0.1:19777")).toBe("turn:127.0.0.1:19777?transport=tcp");
    expect(browserMediaRelayURL("https://[2001:db8::1]:19777")).toBe("turns:[2001:db8::1]:19777?transport=tcp");
    for (const origin of ["", "file:///pogo", "https://pogo.example/path", "https://pogo.example/", "https://user:secret@pogo.example", "ws://pogo.example", "https://pogo.example?host=other"]) expect(browserMediaRelayURL(origin)).toBeNull();
    expect(read(grant)).toEqual({ iceServers: grant.ice_servers, iceTransportPolicy: "relay" });
    expect(read({ ...grant, ice_servers: [{ ...grant.ice_servers[0], urls: "turn:127.0.0.1:19777?transport=tcp" }] }, "http://127.0.0.1:19777")).toMatchObject({ iceTransportPolicy: "relay" });
  });
  it.each([
    "stun:pogo.example:443", "turns:other.example:443?transport=tcp", "turns:pogo.example:5349?transport=tcp",
    "turn:pogo.example:443?transport=tcp", "turns:pogo.example:443?transport=udp", "turns:pogo.example:443",
    "turns:pogo.example:443?transport=tcp&other=1", "turns:pogo.example:443?transport=tcp\n",
  ])("rejects a media endpoint outside the Web-port contract (%s)", urls => {
    expect(read({ ...grant, ice_servers: [{ ...grant.ice_servers[0], urls }] })).toBeNull();
  });
  it.each([
    { context_ref: "ctx_other" }, { tab_id: "t2" }, { generation: 3 }, { media_epoch: "media-2" },
    { ice_transport_policy: "all" }, { ice_transport_policy: undefined }, { expires_in: 0 }, { expires_in: 301 }, { expires_in: "60" },
    { ice_servers: [] }, { ice_servers: [grant.ice_servers[0], grant.ice_servers[0]] }, { unexpected: true },
  ])("rejects mismatched or malformed grants without acquiring any direct authority (%j)", patch => {
    expect(read({ ...grant, ...patch })).toBeNull();
  });
  it("requires a fresh finite credential and checks elapsed time before the peer exists", () => {
    for (const patch of [{ username: "" }, { credential: "" }, { credential: "x".repeat(1025) }, { credential: "secret\n" }, { username: "space user" }, { credential: {} }, { urls: [grant.ice_servers[0].urls] }, { unexpected: true }]) {
      expect(read({ ...grant, ice_servers: [{ ...grant.ice_servers[0], ...patch }] })).toBeNull();
    }
    expect(read(grant, undefined, 1000, 60999)).not.toBeNull();
    expect(read(grant, undefined, 1000, 61000)).toBeNull();
    expect(read(grant, undefined, 1000, 999)).toBeNull();
    expect(read(grant, undefined, NaN)).toBeNull();
    expect(read(grant, undefined, 1000, Infinity)).toBeNull();
  });
  it("sends the consumed live nonce only to Pogo's authenticated, page-scoped API", async () => {
    const fetch = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify(grant), { headers: { "Content-Type": "application/json" } }));
    const controller = new AbortController();
    try {
      const input = { ...binding, live_ticket: "a".repeat(64), sessionId: "not-the-page-scope", worker_url: "http://internal-worker" };
      await api.browserMediaRelayTicket("session/a", input, controller.signal);
      expect(fetch.mock.calls[0]?.[0]).toBe("/api/sessions/session%2Fa/browser/media-relay-ticket");
      expect(fetch.mock.calls[0]?.[1]).toMatchObject({ method: "POST", signal: controller.signal });
      expect(JSON.parse(fetch.mock.calls[0]![1]!.body as string)).toEqual({ ...binding, live_ticket: "a".repeat(64) });
    } finally { fetch.mockRestore(); }
  });
});
