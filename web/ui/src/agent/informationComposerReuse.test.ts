import { describe, expect, it } from "vitest";
import { informationComposerParts } from "./informationComposer";
import { normalizeComposerParts } from "./composerParts";
import { composerInputParts, inputPartsBlocks, blocksComposerParts } from "./orderedInput";
import type { AttachmentMeta, MessageInputPart } from "@/api/types";

const path = `uploads/information/inf_${"a".repeat(32)}/report.png`;
const attachment: AttachmentMeta = {
  path,
  name: "report.png",
  kind: "image",
  mime: "image/png",
  size: 16,
  created: "2026-10-18",
};

describe("reused information files keep separate ordered occurrences", () => {
  it("keeps two independently inserted cards with the same physical path across normalization and history", () => {
    const first = informationComposerParts([{ type: "file", path, as_file: true }], [attachment]);
    const second = informationComposerParts([{ type: "file", path, as_file: true }], [attachment]);
    const draft = normalizeComposerParts([
      { type: "text", text: "First " },
      ...first,
      { type: "text", text: " then " },
      ...second,
      { type: "text", text: " end" },
    ]);
    const files = draft.filter((part) => part.type === "file");
    expect(files).toHaveLength(2);
    expect(files[0].id).not.toBe(files[1].id);
    const expected: MessageInputPart[] = [
      { type: "text", text: "First " },
      { type: "file", path, as_file: true },
      { type: "text", text: " then " },
      { type: "file", path, as_file: true },
      { type: "text", text: " end" },
    ];
    expect(composerInputParts(draft, (key) => [key])).toEqual(expected);
    const reloaded = blocksComposerParts(
      inputPartsBlocks(JSON.parse(JSON.stringify(expected)), new Map([[path, { name: "report.png", image: true }]])),
    );
    expect(reloaded.map((part) => part.type)).toEqual(["text", "file", "text", "file", "text"]);
    expect(composerInputParts(reloaded, (key) => [key])).toEqual(expected);
  });

  it("retains per-occurrence as_file choices even when both share one metadata record", () => {
    const parts: MessageInputPart[] = [
      { type: "file", path, as_file: true },
      { type: "text", text: " inspect visually " },
      { type: "file", path, as_file: false },
    ];
    const draft = informationComposerParts(parts, [attachment]);
    expect(draft.filter((part) => part.type === "file")).toHaveLength(2);
    expect(composerInputParts(normalizeComposerParts(draft), (key) => [key])).toEqual(parts);
  });
});
