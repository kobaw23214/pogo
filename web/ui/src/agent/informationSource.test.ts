import { describe, expect, it } from "vitest";
import type { CiteSource } from "@/api/types";
import { sourceTypeName } from "./cite";
import { citeIconInner } from "./citeIcons";
import { safeSourceURL, sourceIdentity, sourcePreview } from "./citePreview";

const stamp = { entry_id: "inf_" + "a".repeat(32), version: 3, snapshot_sha256: "b".repeat(64) };
const source = (over: Partial<CiteSource> = {}): CiteSource => ({
  label: "S7", type: "information", title: "业务口径", locator: `information://${stamp.entry_id}?version=3#${stamp.snapshot_sha256}`,
  call_id: "library-read", excerpt: "原始资料备用摘录", hash: "result-hash", ts: 1, information: stamp, ...over,
});

describe("information source identity", () => {
  it("keeps a library version internal and presents a dedicated source glyph", () => {
    const src = source();
    expect(sourceIdentity(src)).toEqual({ title: "业务口径", detail: "v3", kind: "library" });
    expect(safeSourceURL(src)).toBeUndefined();
    expect(sourceTypeName(src.type)).toBe("信息库");
    expect(citeIconInner(src.type)).not.toBe(citeIconInner("tool"));
  });
  it("previews only the matching host read body, never the envelope metadata", () => {
    const body = "报表指标按交易完成时间统计，不包含已取消订单。";
    const content = JSON.stringify({ entry: { id: stamp.entry_id }, version: 3, snapshot_sha256: stamp.snapshot_sha256, reference_data_only: true, content: body });
    const preview = sourcePreview(source({ content }), "指标按交易完成时间统计。");
    expect(preview.text).toBe(body);
    expect(preview.text).not.toContain("snapshot_sha256");
  });
  it("does not substitute a changed version or malformed/truncated body", () => {
    for (const content of [JSON.stringify({ entry: { id: stamp.entry_id }, version: 4, snapshot_sha256: stamp.snapshot_sha256, content: "wrong version" }), "{truncated"]) {
      expect(sourcePreview(source({ content }), "wrong version").text).toBe("原始资料备用摘录");
    }
    expect(sourcePreview(source({ information: undefined, content: JSON.stringify({ content: "unstamped" }) }), "").text).toBe("原始资料备用摘录");
  });
});
