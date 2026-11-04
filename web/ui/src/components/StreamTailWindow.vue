<script setup lang="ts">
import { computed, ref } from "vue";
import { useResizeObserver } from "@vueuse/core";
import { recentTailLines } from "@/agent/textTail";

/** A recent fragment of the stream, never a delayed playback of every line. */
const props = withDefaults(
  defineProps<{
    text: string;
    streaming?: boolean;
    lines?: number;
    maxUnits?: number;
    /** Accepted for older callers; recent previews no longer queue playback. */
    paceMs?: number;
  }>(),
  { streaming: true, lines: 3, paceMs: 420 },
);
const root = ref<HTMLElement | null>(null);
const measuredMaxUnits = ref<number>();
let measureContext: CanvasRenderingContext2D | null | undefined;

function updateMaxUnits(width: number) {
  const el = root.value;
  if (!el || width <= 0) { measuredMaxUnits.value = undefined; return; }
  if (measureContext === undefined) measureContext = document.createElement("canvas").getContext("2d");
  if (!measureContext) return;
  const style = getComputedStyle(el);
  measureContext.font = `${style.fontStyle} ${style.fontWeight} ${style.fontSize} ${style.fontFamily}`;
  const unitWidth = measureContext.measureText("0").width;
  if (!Number.isFinite(unitWidth) || unitWidth <= 0) return;
  measuredMaxUnits.value = Math.max(1, Math.floor(width / unitWidth) - 1);
}

useResizeObserver(root, entries => {
  const entry = entries[0];
  if (entry) updateMaxUnits(entry.contentRect.width);
});
const effectiveMaxUnits = computed(() => props.maxUnits ?? measuredMaxUnits.value ?? 72);
const visible = computed(() => recentTailLines(props.text, effectiveMaxUnits.value, props.lines));
</script>

<template>
  <!-- Fixed height preserves the surrounding layout as the latest rows change. -->
  <div ref="root" class="stw" :style="{ '--stw-lines': lines }" :data-streaming="streaming">
    <TransitionGroup name="stwroll">
      <p v-for="l in visible" :key="l.key" class="stw-line">{{ l.text }}</p>
    </TransitionGroup>
  </div>
</template>

<style scoped lang="scss">
.stw {
  position: relative;
  display: flex;
  flex-direction: column;
  justify-content: flex-end;
  height: calc(var(--stw-lines, 3) * 1.7em);
  overflow: hidden;
  min-width: 0;
}

.stw-line {
  margin: 0;
  line-height: 1.7;
  white-space: pre-wrap;
  word-break: break-word;
}

/* The roll: the newest line slides up into place from below, and the rows
   above it glide up one row apiece (TransitionGroup's FLIP move). The
   leaving top line is taken out at once rather than faded out — fading it
   over the row stepping into its place puts two texts in one row, which
   reads as a ghost (残影). The move carries the upward motion; the top line
   just goes. */
.stwroll-move,
.stwroll-enter-active {
  transition:
    transform 0.14s ease-out,
    opacity 0.14s;
}

.stwroll-enter-from {
  transform: translateY(0.9em);
  opacity: 0;
}

.stwroll-leave-active {
  display: none;
}

@media (prefers-reduced-motion: reduce) {
  .stwroll-move,
  .stwroll-enter-active {
    transition: none;
  }

  .stwroll-enter-from {
    transform: none;
  }
}
</style>
