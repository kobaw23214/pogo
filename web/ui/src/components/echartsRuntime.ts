import type { EChartsType } from "echarts/core";
import { cssVar } from "@/theme";
import { currentDiagramPalette } from "@/theme/diagramPalettes";
import { mix } from "@/theme/color";

import { healStaleChunk } from "@/staleChunk";

let echartsPromise: Promise<typeof import("echarts")> | undefined;

/**
 * The echarts module, loaded on first chart and kept. Unlike mermaid there is
 * no global initialize: the theme is an argument to init, so a skin change is
 * the caller disposing and re-initing with a fresh theme object.
 */
export async function getECharts(): Promise<typeof import("echarts")> {
  echartsPromise ??= import("echarts").catch(healStaleChunk);
  return echartsPromise;
}

/**
 * A categorical series palette from the live skin and the chosen diagram
 * palette, mirroring how mermaid is themed.
 *
 * A named diagram palette has three hues, which is fewer than a chart with
 * many series needs — the rest are the same hues mixed towards the canvas, so
 * series four to six read as quieter siblings of one to three rather than as
 * colours from somewhere else. The follow-skin default has no hues of its own
 * and uses the ContextMeter's skin-read chart tokens instead.
 */
export function echartsColors(): string[] {
  const canvas = cssVar("--el-bg-color", "#ffffff");
  const palette = currentDiagramPalette.value;
  if (palette.hues) {
    const quiet = (hue: string) => mix(canvas, hue, 0.55);
    return [...palette.hues, ...palette.hues.map(quiet)];
  }
  const tokens = ["--pg-chart-user", "--pg-chart-assistant", "--pg-chart-tools", "--pg-chart-overhead"];
  const hues = tokens.map((token) => cssVar(token, "#2563eb"));
  return [...hues, ...hues.map((hue) => mix(canvas, hue, 0.55))];
}

/** The theme object handed to echarts.init: everything text- and axis-shaped
 * reads off the skin, the series colours off the palette, and the canvas stays
 * transparent so the chart sits on the conversation surface. */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function echartsTheme(): any {
  const ink = cssVar("--el-text-color-primary", "#111827");
  const secondary = cssVar("--el-text-color-secondary", "#6b7280");
  const border = cssVar("--el-border-color-lighter", "#e5e7eb");
  const fontFamily =
    cssVar("--el-font-family", "-apple-system, BlinkMacSystemFont, Segoe UI, PingFang SC, sans-serif");
  const axis = {
    axisLine: { lineStyle: { color: border } },
    axisTick: { lineStyle: { color: border } },
    axisLabel: { color: secondary },
    splitLine: { lineStyle: { color: border } },
    nameTextStyle: { color: secondary },
  };
  return {
    color: echartsColors(),
    backgroundColor: "transparent",
    textStyle: { color: ink, fontFamily },
    title: { textStyle: { color: ink }, subtextStyle: { color: secondary } },
    legend: { textStyle: { color: secondary } },
    categoryAxis: axis,
    valueAxis: axis,
    timeAxis: axis,
    logAxis: axis,
    tooltip: {
      backgroundColor: cssVar("--el-bg-color-overlay", "#ffffff"),
      borderColor: border,
      textStyle: { color: ink },
    },
  };
}

export type { EChartsType };
