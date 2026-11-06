import { describe, expect, it, vi } from "vitest";
import { studioCapabilityFingerprint, syncStudioMcpCapabilities } from "./studioCapabilitySync";
import type { StudioMcpConfigSync, StudioSessionCapabilities } from "./studioSessionCapabilities";

const caps = (extra: Partial<StudioSessionCapabilities> = {}): StudioSessionCapabilities => ({
  model: "m", models: ["m"], skills: ["office"], mcp_servers: ["old"],
  mcp_sync: true, mcp_revision: 1, mcp_desired_revision: 1, skills_revision: 1, ...extra,
});
const receipt = (extra: Partial<StudioMcpConfigSync> = {}): StudioMcpConfigSync => ({
  changed: false, deferred: false, revision: 1, desired_revision: 1, mcp_servers: ["old"], ...extra,
});

describe("effective conversation capabilities", () => {
  it("never probes an MCP sync endpoint on an older server", async () => {
    const before = caps({ mcp_sync: undefined }), request = vi.fn();
    expect(await syncStudioMcpCapabilities(before, request)).toBe(before);
    expect(request).not.toHaveBeenCalled();
  });

  it("keeps the effective servers while busy and applies revocations after the host commit", async () => {
    const request = vi.fn().mockResolvedValueOnce(receipt({ deferred: true, desired_revision: 2 }))
      .mockResolvedValueOnce(receipt({ changed: true, revision: 2, desired_revision: 2, mcp_servers: [] }));
    const before = caps(), waiting = await syncStudioMcpCapabilities(before, request);
    expect(waiting).toMatchObject({ mcp_servers: ["old"], mcp_pending: true, mcp_revision: 1, mcp_desired_revision: 2 });
    expect(studioCapabilityFingerprint(waiting)).toBe(studioCapabilityFingerprint(before));
    const applied = await syncStudioMcpCapabilities(waiting, request);
    expect(applied).toMatchObject({ mcp_servers: [], mcp_pending: false, mcp_revision: 2 });
    expect(studioCapabilityFingerprint(applied)).not.toBe(studioCapabilityFingerprint(before));
  });

  it("does not let an old sync receipt replace a newer effective or pending revision", async () => {
    const before = caps({ mcp_revision: 3, mcp_desired_revision: 5, mcp_pending: true });
    expect(await syncStudioMcpCapabilities(before, async () => receipt({ revision: 2, desired_revision: 9 }))).toBe(before);
    expect(await syncStudioMcpCapabilities(before, async () => receipt({ revision: 3, desired_revision: 4 }))).toBe(before);
  });

  it("can retry a failed sync without discarding the last confirmed selection", async () => {
    const before = caps(), request = vi.fn().mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValueOnce(receipt({ changed: true, revision: 2, desired_revision: 2, mcp_servers: ["new"] }));
    await expect(syncStudioMcpCapabilities(before, request)).rejects.toThrow("offline");
    expect(before.mcp_servers).toEqual(["old"]);
    expect((await syncStudioMcpCapabilities(before, request)).mcp_servers).toEqual(["new"]);
  });

  it("rejects malformed receipts and invalidates for Skill changes even when native tools stay fixed", async () => {
    const before = caps({ tools: ["read"] });
    await expect(syncStudioMcpCapabilities(before, async () => receipt({ mcp_servers: [42] as never }))).rejects.toThrow("Invalid MCP");
    expect(studioCapabilityFingerprint(caps({ skills: [], skills_revision: 2, tools: ["read"] }))).not.toBe(studioCapabilityFingerprint(before));
  });
});
