import { computed, ref } from "vue";
import { mix } from "./color";

/**
 * The diagram colour styles, as authored.
 *
 * A palette is three hues and nothing else — node, accent, group. Everything
 * mermaid actually reads (node fills, borders, line colour, cluster surfaces,
 * the secondary/tertiary pair) is derived by paletteVariables below, mixing the
 * hues towards the *current* skin's canvas and ink. Deriving rather than
 * authoring final hexes is what makes one palette serve all fourteen skins:
 * "Ocean" on Paper is a pale blue wash on white, on Mocha the same seed is a
 * blue lifted out of the dark canvas, and neither needed a second colour list.
 *
 * The mix ratios also keep the derived fills above the luminance threshold the
 * diagram's soften pass rewrites below — a palette's nodes survive rendering
 * looking like the palette, which is the entire point of picking one.
 */
export interface DiagramPaletteSeed {
  /** Stable id; goes in localStorage. */
  id: string;
  /** Display name. A proper noun, so it is never translated — the follow-skin
   * default is the exception, its name is descriptive and comes from i18n. */
  name: string;
  /** Where the hues come from, shown under the name in the picker. */
  origin: string;
  /** [node, accent, group]; null reads everything off the live skin instead. */
  hues: [string, string, string] | null;
}

export const DIAGRAM_PALETTES: DiagramPaletteSeed[] = [
  { id: "skin", name: "", origin: "", hues: null },
  { id: "ocean", name: "Ocean", origin: "Tailwind blue/cyan", hues: ["#2563eb", "#0891b2", "#0ea5e9"] },
  { id: "forest", name: "Forest", origin: "Mermaid forest, re-seeded", hues: ["#16a34a", "#65a30d", "#059669"] },
  { id: "sunset", name: "Sunset", origin: "Tailwind orange/amber", hues: ["#ea580c", "#d97706", "#dc2626"] },
  { id: "grape", name: "Grape", origin: "Tailwind violet/fuchsia", hues: ["#7c3aed", "#a855f7", "#c026d3"] },
  { id: "sakura", name: "Sakura", origin: "Tailwind pink/rose", hues: ["#db2777", "#e11d48", "#ec4899"] },
  { id: "mono", name: "Mono", origin: "Print-friendly slate", hues: ["#64748b", "#475569", "#334155"] },
  { id: "okabe-ito", name: "Okabe-Ito", origin: "Colorblind-safe", hues: ["#0072b2", "#e69f00", "#009e73"] },
];

const byId = new Map(DIAGRAM_PALETTES.map((p) => [p.id, p]));

const STORAGE_KEY = "pogo:diagram-palette";

function stored(): string | null {
  try {
    return localStorage.getItem(STORAGE_KEY);
  } catch {
    // Private mode; the choice is then session-only.
    return null;
  }
}

function resolveInitial(): string {
  const saved = stored();
  return saved && byId.has(saved) ? saved : "skin";
}

/** The palette in effect, and the version counter JS-side consumers watch.
 * Mirrors the skin pair (currentThemeId / themeVersion): a rendered diagram
 * carries its colours inline, so a palette change has to re-render, not
 * re-style, and the version is what mounted diagrams key that re-render on. */
export const diagramPaletteId = ref<string>(resolveInitial());
export const currentDiagramPalette = computed(() => byId.get(diagramPaletteId.value) ?? DIAGRAM_PALETTES[0]);
export const diagramPaletteVersion = ref(0);

export function applyDiagramPalette(id: string, persist = true): void {
  diagramPaletteId.value = byId.has(id) ? id : "skin";
  diagramPaletteVersion.value++;
  if (!persist) return;
  try {
    localStorage.setItem(STORAGE_KEY, diagramPaletteId.value);
  } catch {
    // Private mode — session-only, same as the skin.
  }
}

/**
 * paletteVariables turns a seed into the themeVariables mermaid reads.
 *
 * Returns null for the follow-skin default, meaning "keep whatever the runtime
 * already read off the skin's CSS variables" — the default must behave exactly
 * as if no palette existed.
 *
 * The percentages are the design, and they are the same for every palette: node
 * fill a tenth towards the hue, borders halfway, the line colour carried by the
 * node hue (it is what makes a diagram read as *styled* rather than recoloured),
 * clusters quieter than nodes so a group never outshouts its members.
 */
export function paletteVariables(
  palette: DiagramPaletteSeed,
  dark: boolean,
  canvas: string,
  ink: string,
): Record<string, string> | null {
  if (!palette.hues) return null;
  const [node, alt, group] = palette.hues;
  return {
    primaryColor: mix(canvas, node, dark ? 0.24 : 0.12),
    primaryBorderColor: mix(canvas, node, dark ? 0.6 : 0.45),
    primaryTextColor: ink,
    secondaryColor: mix(canvas, alt, dark ? 0.22 : 0.14),
    secondaryBorderColor: mix(canvas, alt, dark ? 0.55 : 0.4),
    secondaryTextColor: ink,
    tertiaryColor: mix(canvas, node, dark ? 0.14 : 0.05),
    tertiaryBorderColor: mix(canvas, node, dark ? 0.35 : 0.25),
    tertiaryTextColor: ink,
    lineColor: mix(canvas, node, dark ? 0.6 : 0.5),
    textColor: ink,
    clusterBkg: mix(canvas, group, dark ? 0.18 : 0.08),
    clusterBorder: mix(canvas, group, dark ? 0.45 : 0.35),
  };
}
