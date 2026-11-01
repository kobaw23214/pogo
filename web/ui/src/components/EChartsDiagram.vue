<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Brush, Check, CopyDocument, Download, FullScreen } from "@element-plus/icons-vue";
import type { EChartsType } from "echarts/core";
import CodeBlock from "./CodeBlock.vue";
import { echartsTheme, getECharts } from "./echartsRuntime";
import { sanitizeEChartsOption } from "./sanitizeEChartsOption";
import {
  DIAGRAM_PALETTES,
  applyDiagramPalette,
  diagramPaletteId,
  diagramPaletteVersion,
  themeVersion,
} from "@/theme";

// The same caps as mermaid: past this the option is a data dump, not a chart,
// and rendering it would freeze the tab instead of informing the user.
const MAX_SOURCE_BYTES = 64 * 1024;
const MAX_SOURCE_LINES = 1_000;

const props = defineProps<{ source: string }>();

const { t } = useI18n();
const error = ref("");
const copied = ref(false);
const notice = ref("");
const fullscreen = ref(false);
const styleOpen = ref(false);
const canvas = ref<HTMLElement | null>(null);
const shell = ref<HTMLElement | null>(null);
let chart: EChartsType | null = null;
let generation = 0;
let copyTimer: number | undefined;
let resizeObserver: ResizeObserver | null = null;

function pickPalette(id: string) {
  styleOpen.value = false;
  if (id !== diagramPaletteId.value) applyDiagramPalette(id);
}

async function render(source: string) {
  const current = ++generation;
  error.value = "";
  notice.value = "";

  const bytes = new TextEncoder().encode(source).byteLength;
  if (bytes > MAX_SOURCE_BYTES || source.split("\n").length > MAX_SOURCE_LINES) {
    destroyChart();
    error.value = t("echartsDiagram.tooLarge");
    return;
  }

  const sanitized = sanitizeEChartsOption(source);
  if (!sanitized.ok) {
    destroyChart();
    error.value =
      sanitized.reason === "json"
        ? t("echartsDiagram.notJSON")
        : sanitized.reason === "shape"
          ? t("echartsDiagram.notObject")
          : t("echartsDiagram.tooLarge");
    return;
  }

  const echarts = await getECharts();
  if (current !== generation) return;
  // A skin or palette change re-inits rather than setOption-merges: the theme
  // is read once at init, so merging into a live chart would keep the old
  // colours for everything the option does not mention.
  destroyChart();
  // The canvas div is v-if'd away while an error is showing, and the watcher
  // runs before the template re-render that brings it back — wait for it, or
  // init would see a null host right after a failed block turns good again.
  await nextTick();
  if (current !== generation) return;
  const host = canvas.value;
  if (!host) return;
  chart = echarts.init(host, echartsTheme(), { renderer: "svg" });
  chart.setOption(sanitized.option, { notMerge: true });
  chart.resize();
}

function destroyChart() {
  chart?.dispose();
  chart = null;
}

async function copySource() {
  try {
    await navigator.clipboard.writeText(props.source);
    copied.value = true;
    window.clearTimeout(copyTimer);
    copyTimer = window.setTimeout(() => (copied.value = false), 1200);
  } catch {
    // Clipboard access can be denied; failing quietly is better than an alert.
  }
}

/** getDataURL with the skin's canvas behind the SVG, so the exported PNG does
 * not carry transparent holes through every light surface it is pasted onto. */
function downloadImage() {
  if (!chart) return;
  try {
    const link = document.createElement("a");
    link.href = chart.getDataURL({ pixelRatio: 2, backgroundColor: "#ffffff" });
    link.download = `echarts-${new Date().toISOString().replace(/[:.]/g, "-")}.png`;
    link.click();
  } catch {
    notice.value = t("echartsDiagram.downloadFailed");
  }
}

/**
 * Native fullscreen when the browser allows it, with an in-page overlay fallback so
 * the button always does something (permissions policy can block the API in frames).
 */
async function toggleFullscreen() {
  const element = shell.value;
  if (!element) return;
  try {
    if (document.fullscreenElement) {
      await document.exitFullscreen();
    } else if (element.requestFullscreen) {
      await element.requestFullscreen();
    } else {
      fullscreen.value = !fullscreen.value;
    }
  } catch {
    fullscreen.value = !fullscreen.value;
  }
}

function syncFullscreen() {
  fullscreen.value = document.fullscreenElement === shell.value;
}

function onKeydown(event: KeyboardEvent) {
  if (event.key === "Escape" && fullscreen.value && !document.fullscreenElement) {
    fullscreen.value = false;
  }
}

// Source, skin and palette are all render inputs, for the same reason as the
// dispose-and-re-init comment in render(): none of them can be styled onto a
// chart that is already on screen.
watch(
  [() => props.source, themeVersion, diagramPaletteVersion],
  ([source]) => {
    void render(source as string);
  },
  { immediate: true },
);
onMounted(() => {
  document.addEventListener("fullscreenchange", syncFullscreen);
  window.addEventListener("keydown", onKeydown);
  // The conversation column and the dock panes both resize; echarts does not
  // follow its container on its own.
  if (typeof ResizeObserver === "function" && canvas.value) {
    resizeObserver = new ResizeObserver(() => chart?.resize());
    resizeObserver.observe(canvas.value);
  }
});
onBeforeUnmount(() => {
  generation++;
  window.clearTimeout(copyTimer);
  resizeObserver?.disconnect();
  destroyChart();
  document.removeEventListener("fullscreenchange", syncFullscreen);
  window.removeEventListener("keydown", onKeydown);
});
</script>

<template>
  <div ref="shell" class="echarts-diagram" :class="{ 'is-fullscreen': fullscreen }">
    <div v-if="!error" class="bar">
      <button class="act" @click="copySource">
        <el-icon><Check v-if="copied" /><CopyDocument v-else /></el-icon>
        <span>{{ copied ? t("common.copied") : t("echartsDiagram.copyCode") }}</span>
      </button>
      <button class="act" @click="downloadImage">
        <el-icon><Download /></el-icon>
        <span>{{ t("echartsDiagram.downloadImage") }}</span>
      </button>
      <!-- Not teleported: the popover must stay inside the shell, because in
           fullscreen only the shell's subtree is painted. -->
      <el-popover
        v-model:visible="styleOpen"
        placement="bottom-end"
        trigger="click"
        :width="220"
        :teleported="false"
        popper-class="diagram-style-pop"
      >
        <template #reference>
          <button class="act">
            <el-icon><Brush /></el-icon>
            <span>{{ t("echartsDiagram.styleColor") }}</span>
          </button>
        </template>
        <div class="palette-list">
          <button
            v-for="p in DIAGRAM_PALETTES"
            :key="p.id"
            class="palette-item"
            :class="{ on: p.id === diagramPaletteId }"
            @click="pickPalette(p.id)"
          >
            <span class="chips" aria-hidden="true">
              <i v-for="chip in p.hues ?? ['var(--pg-diagram-node)', 'var(--pg-diagram-alt)', 'var(--pg-diagram-cluster)']" :key="chip" :style="{ background: chip }" />
            </span>
            <span class="palette-text">
              <span class="palette-name">{{ p.id === "skin" ? t("echartsDiagram.paletteSkin") : p.name }}</span>
              <span v-if="p.origin" class="palette-origin">{{ p.origin }}</span>
            </span>
            <span v-if="p.id === diagramPaletteId" class="tick">✓</span>
          </button>
        </div>
      </el-popover>
      <button class="act" @click="toggleFullscreen">
        <el-icon><FullScreen /></el-icon>
        <span>{{ fullscreen ? t("echartsDiagram.exitFullscreen") : t("echartsDiagram.fullscreen") }}</span>
      </button>
    </div>
    <div v-if="notice" class="note">{{ notice }}</div>
    <!-- The option was parsed as JSON and sanitized before reaching setOption. -->
    <div v-if="!error" ref="canvas" class="canvas" />
    <div v-else class="fallback">
      <div class="note bad">ECharts: {{ error }}</div>
      <CodeBlock :code="source" lang="json" :collapsed-height="240" />
    </div>
  </div>
</template>

<style scoped lang="scss">
.echarts-diagram {
  position: relative;
  margin: 0.7em 0;
  min-width: 0;

  &.is-fullscreen {
    position: fixed;
    inset: 0;
    z-index: 3000;
    margin: 0;
    display: flex;
    flex-direction: column;
    background: var(--el-bg-color);
    padding: 44px 16px 16px;

    .canvas {
      flex: 1;
      min-height: 0;
      border: 0;
    }
  }

  &:fullscreen {
    background: var(--el-bg-color);
    padding: 44px 16px 16px;
  }
}

.bar {
  position: absolute;
  top: 8px;
  right: 10px;
  z-index: 1;
  display: flex;
  gap: 6px;
}

/* Against a white page a white button is invisible: these sit on the grey fill
   ramp, one step darker on hover. */
.act {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  border: 1px solid var(--el-border-color);
  border-radius: 5px;
  background: var(--el-fill-color-dark);
  color: var(--el-text-color-regular);
  font-size: 11px;
  line-height: 1.6;
  padding: 3px 9px;
  cursor: pointer;

  .el-icon {
    font-size: 12px;
  }

  &:hover {
    background: var(--el-fill-color-darker);
    border-color: var(--el-border-color-dark);
    color: var(--el-text-color-primary);
  }

  &:active {
    background: var(--el-border-color-light);
  }
}

/* echarts needs an explicit height to lay out against; the width follows the
   column and the ResizeObserver relays it. */
.canvas {
  height: 340px;
  padding: 6px 10px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 6px;
  background: var(--el-bg-color);
}

.note {
  margin-bottom: 6px;
  font-size: 12px;
  color: var(--el-text-color-secondary);

  &.bad {
    color: var(--el-color-danger);
  }
}

:deep(.diagram-style-pop) {
  padding: 6px !important;
  border-radius: 12px !important;
}

.palette-list {
  display: flex;
  flex-direction: column;
  gap: 1px;
  max-height: 60vh;
  overflow-y: auto;
}

.palette-item {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 6px 8px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: inherit;
  text-align: left;
  cursor: pointer;
  transition: background var(--pg-transition);

  &:hover {
    background: var(--el-fill-color-light);
  }

  &.on {
    background: var(--pg-accent-wash);
  }
}

.chips {
  flex: 0 0 auto;
  display: inline-flex;
  border-radius: 999px;
  overflow: hidden;
  box-shadow: inset 0 0 0 1px var(--el-border-color);

  i {
    display: block;
    width: 8px;
    height: 20px;
  }
}

.palette-text {
  flex: 1 1 auto;
  min-width: 0;
  display: flex;
  flex-direction: column;
}

.palette-name {
  font-size: 12.5px;
  font-weight: 550;
  color: var(--el-text-color-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.palette-origin {
  font-size: 10px;
  color: var(--el-text-color-secondary);
}

.tick {
  flex: 0 0 auto;
  font-size: 11px;
  color: var(--el-color-primary);
}
</style>
