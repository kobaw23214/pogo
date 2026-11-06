import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { validWebServiceGrant, type WebServiceGrant, type WebServiceRecord } from "./webServices";

const origin = "http://192.168.1.10:19770", token = "a".repeat(48);
const pathURL = `${origin}/_pogo/web/${token}/`;
const record: WebServiceRecord = { service_id: "ws_one", generation: 1, revision: 1, state: "ready", name: "counter", profile: "python-web", project_root: "site", lease_expires_at: "2026-11-05T14:00:00Z" };
const receipt = (preview_url = pathURL): WebServiceGrant => ({ service_id: "ws_one", generation: 1, grant_id: "grant_one", preview_url, grant_expires_at: "2026-11-05T12:30:00Z", lease_expires_at: record.lease_expires_at, browser_url: null, limitations: [] });

beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(new Date("2026-11-05T12:00:00Z")); vi.stubGlobal("location", { origin }); });
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); });

describe("managed preview grant URL boundary", () => {
  it("accepts the exact same-origin IP grant root and preserves domain mode", () => {
    expect(validWebServiceGrant(receipt(), record)).toBe(true);
    expect(validWebServiceGrant(receipt(`https://${token}.preview.example.test/`), record)).toBe(true);
    vi.stubGlobal("location", { origin: "http://[fd00::1]:19770" });
    expect(validWebServiceGrant(receipt(`http://[fd00::1]:19770/_pogo/web/${token}/`), record)).toBe(true);
  });
  it.each([
    `${origin}/`, `${origin}/api/sessions`, `${origin}/_pogo/web/`,
    `${origin}/_pogo/web/${"a".repeat(47)}/`, `${origin}/_pogo/web/${"A".repeat(48)}/`,
    `${origin}/_pogo/web/${token}`, `${pathURL}api/counter`,
    `${origin}/_pogo/web/${token}/extra/../`, `${origin}/_pogo/web/%61${token.slice(1)}/`,
    `${origin}/other/../_pogo/web/${token}/`, `${origin}/_pogo/web/${token}\\`,
    `${pathURL}?token=another`, `${pathURL}#fragment`, ` ${pathURL}`,
    `http://user:secret@192.168.1.10:19770/_pogo/web/${token}/`,
    `http://192.168.1.11:19770/_pogo/web/${token}/`, `http://192.168.1.10:19771/_pogo/web/${token}/`,
    `https://192.168.1.10:19770/_pogo/web/${token}/`, `//192.168.1.10:19770/_pogo/web/${token}/`,
    `javascript:alert(1)`, `http://${token}.preview.example.test/api/counter`,
  ])("rejects a preview URL outside the exact grant root: %s", previewURL => {
    expect(validWebServiceGrant(receipt(previewURL), record)).toBe(false);
  });
  it("fails closed without a current origin and retains grant ownership and expiry checks", () => {
    vi.stubGlobal("location", undefined);
    expect(validWebServiceGrant(receipt(), record)).toBe(false);
    vi.stubGlobal("location", { origin });
    for (const change of [{ service_id: "ws_other" }, { generation: 2 }, { grant_expires_at: "2026-11-05T12:00:00Z" }, { grant_expires_at: "2026-11-05T15:00:00Z" }]) {
      expect(validWebServiceGrant({ ...receipt(), ...change }, record)).toBe(false);
    }
  });
});

describe("configured main-entry aliases", () => {
  const localhost = "http://localhost:19770", loopback = "http://127.0.0.1:19770";
  const trustedReceipt = (overrides: Record<string, unknown> = {}) => ({
    ...receipt(), canonical_origin: origin, allowed_parent_origins: [origin, localhost, loopback], ...overrides,
  });

  it.each([origin, localhost, loopback])("accepts configured parent %s without rewriting the canonical website address", parent => {
    vi.stubGlobal("location", { origin: parent });
    const value = trustedReceipt();
    expect(validWebServiceGrant(value, record)).toBe(true);
    expect(value.preview_url).toBe(pathURL);
  });

  it("keeps old receipts same-origin only when alias metadata is absent", () => {
    expect(validWebServiceGrant(receipt(), record)).toBe(true);
    vi.stubGlobal("location", { origin: localhost });
    expect(validWebServiceGrant(receipt(), record)).toBe(false);
    expect(validWebServiceGrant({ ...receipt(), canonical_origin: origin }, record)).toBe(false);
    expect(validWebServiceGrant({ ...receipt(), allowed_parent_origins: [origin, localhost] }, record)).toBe(false);
  });

  it.each([
    "http://evil.example:19770", "http://localhost:19771", "https://localhost:19770", "http://192.168.1.11:19770",
  ])("rejects a parent not listed by the gateway: %s", parent => {
    vi.stubGlobal("location", { origin: parent });
    expect(validWebServiceGrant(trustedReceipt(), record)).toBe(false);
  });

  it.each([
    undefined, null, "", `${origin}/`, `${origin}/api`, `${origin}?x=1`, `${origin}#x`, ` ${origin}`,
    "http://user:secret@192.168.1.10:19770", "http://192.168.1.11:19770", "javascript:alert(1)",
  ])("rejects a false or noncanonical declared canonical origin: %s", canonical_origin => {
    vi.stubGlobal("location", { origin: localhost });
    expect(validWebServiceGrant(trustedReceipt({ canonical_origin }), record)).toBe(false);
  });

  it.each([
    undefined, null, "http://localhost:19770", [], [origin],
    [origin, localhost, "*"], [origin, localhost, "null"],
    [origin, localhost, `${loopback}/`], [origin, localhost, `${loopback}/api`],
    [origin, localhost, `${loopback}?x=1`], [origin, localhost, `${loopback}#x`],
    [origin, localhost, ` ${loopback}`], [origin, localhost, `${loopback} `],
    [origin, localhost, "http://user:secret@127.0.0.1:19770"],
    [origin, localhost, "http://LOCALHOST:19770"], [origin, localhost, "http://127.1:19770"],
    [origin, localhost, 42],
  ].map(allowed_parent_origins => ({ allowed_parent_origins })))("rejects malformed or nonmatching parent-origin metadata: %j", ({ allowed_parent_origins }) => {
    vi.stubGlobal("location", { origin: localhost });
    expect(validWebServiceGrant(trustedReceipt({ allowed_parent_origins }), record)).toBe(false);
  });

  it("retains exact grant-root checks even for an allowed parent", () => {
    vi.stubGlobal("location", { origin: localhost });
    for (const preview_url of [
      `${pathURL}api/counter`, `${pathURL}?other=1`, `${pathURL}#x`, ` ${pathURL}`,
      `${origin}/other/../_pogo/web/${token}/`, `${origin}/_pogo/web/%61${token.slice(1)}/`,
      `${origin}/_pogo/web/${token}/extra/../`, `${origin}/_pogo/web/${token}\\`,
      `http://user:secret@192.168.1.10:19770/_pogo/web/${token}/`,
      `http://192.168.1.11:19770/_pogo/web/${token}/`,
    ]) expect(validWebServiceGrant(trustedReceipt({ preview_url }), record)).toBe(false);
  });

  it("retains ownership, generation and expiry checks for an allowed parent", () => {
    vi.stubGlobal("location", { origin: localhost });
    for (const change of [
      { service_id: "ws_other" }, { generation: 2 }, { grant_id: "../bad" },
      { grant_expires_at: "2026-11-05T12:00:00Z" }, { grant_expires_at: "2026-11-05T15:00:00Z" },
    ]) expect(validWebServiceGrant(trustedReceipt(change), record)).toBe(false);
    vi.stubGlobal("location", undefined);
    expect(validWebServiceGrant(trustedReceipt(), record)).toBe(false);
  });

  it("accepts equal lease deadlines serialized by Go with nanosecond precision", () => {
    vi.stubGlobal("location", { origin: localhost });
    const deadline = "2026-11-05T14:00:00.392396169Z";
    expect(validWebServiceGrant(trustedReceipt({ grant_expires_at: deadline, lease_expires_at: deadline }), { ...record, lease_expires_at: deadline })).toBe(true);
  });
});
