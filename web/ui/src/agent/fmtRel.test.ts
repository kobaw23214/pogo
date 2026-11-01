import { describe, expect, it } from "vitest";
import { baseName, fmtRel } from "./fmtRel";

const NOW = Date.UTC(2026, 7, 19, 12, 0, 0);

describe("fmtRel", () => {
  it("picks the largest unit that fits", () => {
    expect(fmtRel(NOW - 30_000, NOW)).toBe("0分钟后");
    expect(fmtRel(NOW - 5 * 60_000, NOW)).toContain("5");
    expect(fmtRel(NOW - 3 * 3600_000, NOW)).toContain("3");
    expect(fmtRel(NOW - 2 * 24 * 3600_000, NOW)).toContain("2");
  });

  it("renders in the requested locale", () => {
    expect(fmtRel(NOW - 3 * 3600_000, NOW, "en")).toContain("3");
  });

  it("clock skew from the future reads as this minute, not a negative age", () => {
    expect(fmtRel(NOW + 60_000, NOW)).toBe("0分钟后");
  });
});

describe("baseName", () => {
  it("takes the last path segment", () => {
    expect(baseName("/home/agent")).toBe("agent");
    expect(baseName("/home/agent/")).toBe("agent");
  });

  it("the root itself labels as root, never as an empty string", () => {
    expect(baseName("/")).toBe("/");
    expect(baseName("")).toBe("/");
  });
});
