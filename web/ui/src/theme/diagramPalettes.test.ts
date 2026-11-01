import { describe, expect, it } from "vitest";
import { luminance, parse } from "./color";
import { DIAGRAM_PALETTES, paletteVariables } from "./diagramPalettes";

describe("diagram palettes", () => {
  it("ships eight, with unique ids, and the follow-skin default comes first", () => {
    expect(DIAGRAM_PALETTES.length).toBe(8);
    const ids = DIAGRAM_PALETTES.map((p) => p.id);
    expect(new Set(ids).size).toBe(ids.length);
    expect(ids[0]).toBe("skin");
    expect(DIAGRAM_PALETTES[0].hues).toBeNull();
  });

  it("every hue is a valid hex colour — a typo must fail here, not paint black", () => {
    for (const p of DIAGRAM_PALETTES) {
      for (const hue of p.hues ?? []) {
        expect(() => parse(hue)).not.toThrow();
      }
    }
  });

  it("the follow-skin default opts out of overriding", () => {
    expect(paletteVariables(DIAGRAM_PALETTES[0], false, "#ffffff", "#111827")).toBeNull();
  });

  it("keeps light-mode node fills above the soften pass threshold", () => {
    // MermaidDiagram's soften pass rewrites any fill under 0.55 luminance; a
    // palette whose nodes fell under it would render washed out, which is the
    // palette not surviving its own render.
    for (const p of DIAGRAM_PALETTES.slice(1)) {
      const vars = paletteVariables(p, false, "#ffffff", "#111827");
      expect(vars).not.toBeNull();
      expect(luminance(vars!.primaryColor)).toBeGreaterThan(0.55);
      expect(luminance(vars!.clusterBkg)).toBeGreaterThan(0.55);
    }
  });

  it("keeps text readable in both modes", () => {
    for (const p of DIAGRAM_PALETTES.slice(1)) {
      const light = paletteVariables(p, false, "#ffffff", "#111827");
      const dark = paletteVariables(p, true, "#1e1e2e", "#cdd6f4");
      expect(light!.primaryTextColor).toBe("#111827");
      expect(dark!.primaryTextColor).toBe("#cdd6f4");
      // A dark canvas lifts the fill instead of washing it out.
      expect(luminance(dark!.primaryColor)).toBeLessThan(luminance(light!.primaryColor));
    }
  });
});
