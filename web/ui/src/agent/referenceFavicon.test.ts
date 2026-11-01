import { describe, expect, it, vi } from "vitest";
import { referenceFavicon, snapshotReferenceFavicon } from "./referenceFavicon";
import { blocksComposerParts, composerInputParts, inputPartsBlocks, snapshotComposerElementIcons } from "./orderedInput";
import { createCiteFavicons } from "./citeFavicon";
import { elementComposerPart, readComposerReference } from "./composerParts";
import { prepareStableStarterDraft, starterCardComposerParts } from "@/components/starterCardDraft";

const iconA = "data:image/png;base64,AAAA", iconB = "data:image/png;base64,AQID";
const url = "https://site.test/report?account=one";
const text = `<page-element source="系统" url="${url}">\nelement: button "导出"\n</page-element>`;
const pick = { key: "p1", block: text, label: 'button "导出"', pageTitle: "系统", pageURL: url, icon: iconA };

describe("frozen element favicon identity", () => {
  it("preserves independent picked bytes through send/history/edit/shortcut JSON import and clipboard", async () => {
    const original = elementComposerPart(pick);
    const input = composerInputParts([original], () => []);
    expect(input).toEqual([{ type: "element", text, favicon: iconA }]);
    const history = inputPartsBlocks(input);
    expect(history[0]).toMatchObject({ input_element_favicon: iconA });
    const edited = blocksComposerParts(history);
    expect(edited[0]).toMatchObject({ favicon: iconA, pick: { icon: iconA } });
    const prepared = prepareStableStarterDraft({ inputLayout: "inline", parts: edited });
    const imported = JSON.parse(JSON.stringify({ title: "流程", prompt: prepared.prompt, parts: prepared.parts, input_layout: "inline" }));
    expect(starterCardComposerParts(imported)[0]).toMatchObject({ favicon: iconA });
    expect(readComposerReference(original)).toMatchObject({ favicon: iconA, pick: { icon: iconA } });
    expect(input[0]).not.toHaveProperty("pick");
    expect(text).not.toContain("base64");
  });
  it("never replaces an occurrence snapshot when the same URL later has another favicon", async () => {
    const archive = createCiteFavicons();
    archive.observe([{ url, favicon: iconB }]);
    expect((await snapshotComposerElementIcons([elementComposerPart(pick)], archive))[0]).toMatchObject({ favicon: iconA });
    const old = { ...elementComposerPart({ ...pick, icon: undefined }) };
    expect((await snapshotComposerElementIcons([old], archive))[0]).toMatchObject({ favicon: iconB, pick: { icon: iconB } });
    const unrelated = { ...old, text: text.replace("account=one", "account=two"), pick: { ...old.pick!, pageURL: url.replace("account=one", "account=two") } };
    expect((await snapshotComposerElementIcons([unrelated], archive))[0]).not.toHaveProperty("favicon");
  });
  it("does not persist or fetch remote SVG or unsafe resource references", async () => {
    const fetch = vi.spyOn(globalThis, "fetch");
    for (const value of ["https://tracking.test/icon.png", "file:///tmp/icon.png", "data:image/svg+xml;base64,PHN2Zy8+", "data:text/html;base64,AAAA", "data:image/png;base64," + "A".repeat(90000)]) {
      expect(referenceFavicon(value)).toBeUndefined();
      expect(await snapshotReferenceFavicon(value)).toBeUndefined();
      expect(composerInputParts([{ ...elementComposerPart(pick), favicon: value, pick: undefined }], () => [])[0]).not.toHaveProperty("favicon");
    }
    expect(fetch).not.toHaveBeenCalled(); fetch.mockRestore();
  });
});
