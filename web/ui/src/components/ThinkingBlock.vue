<script setup lang="ts">
import { computed, nextTick, ref, useId, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useCardDisclosure } from "@/agent/cardDisclosure";
import { formatDuration } from "@/agent/timeline";
import type { ToolPresentation } from "@/agent/toolPresentation";
import StreamTailWindow from "./StreamTailWindow.vue";
import ToolCallHeader from "./ToolCallHeader.vue";

const props = defineProps<{ id: string; text: string; streaming: boolean; durationMs?: number; compact?: boolean }>();
const { t } = useI18n();
const bodyId = useId();
const expanded = useCardDisclosure(() => props.id && props.id !== "live" ? `${props.id}:thinking` : undefined, false);
const fullOpen = computed(() => expanded.value && !props.compact);
const presentation = computed<ToolPresentation>(() => ({
  glyph: "thinking",
  name: t("thinkingBlock.title"),
  secondary: fullOpen.value ? t("thinkingBlock.full") : t("thinkingBlock.recent"),
  meta: props.durationMs !== undefined && Number.isFinite(props.durationMs) && props.durationMs >= 0 ? formatDuration(props.durationMs) : undefined,
  protocolName: t("thinkingBlock.title"),
  state: props.streaming ? "running" : "done",
  statusLabel: props.streaming ? t("thinkingBlock.streaming") : undefined,
}));
const body = ref<HTMLElement | null>(null);
let followTail = true;
let interactionVersion = 0;
let savedScrollTop: number | undefined;
const nearBottom = (el: HTMLElement) => el.scrollHeight - el.scrollTop - el.clientHeight <= 24;

function onScroll() {
  if (!body.value) return;
  savedScrollTop = body.value.scrollTop;
  followTail = nearBottom(body.value);
  interactionVersion++;
}
function onWheel(event: WheelEvent) {
  if (event.deltaY < 0) { followTail = false; interactionVersion++; }
}
function onKeydown(event: KeyboardEvent) {
  if (["ArrowUp", "PageUp", "Home"].includes(event.key)) { followTail = false; interactionVersion++; }
}
function toggle() { if (!props.compact) expanded.value = !expanded.value; }

watch(fullOpen, async value => {
  if (!value) {
    if (body.value) savedScrollTop = body.value.scrollTop;
    return;
  }
  const id = props.id;
  const version = interactionVersion;
  await nextTick();
  if (!fullOpen.value || props.id !== id || !body.value || version !== interactionVersion) return;
  const el = body.value;
  el.scrollTop = savedScrollTop === undefined ? 0 : followTail ? el.scrollHeight : savedScrollTop;
  followTail = nearBottom(el);
});
watch(() => props.id, () => {
  savedScrollTop = undefined;
  followTail = true;
  interactionVersion++;
  if (body.value) body.value.scrollTop = 0;
});
watch(() => props.text, async (value, previous) => {
  if (!value.startsWith(previous)) {
    savedScrollTop = undefined;
    followTail = true;
    interactionVersion++;
    if (body.value) body.value.scrollTop = 0;
  }
  if (!fullOpen.value || !body.value) return;
  const el = body.value;
  const id = props.id;
  const version = interactionVersion;
  const shouldFollow = followTail && nearBottom(el);
  await nextTick();
  if (shouldFollow && fullOpen.value && props.id === id && body.value === el && version === interactionVersion && followTail) {
    el.scrollTop = el.scrollHeight;
    savedScrollTop = el.scrollTop;
  }
}, { flush: "pre" });
</script>

<template>
  <section v-if="text" class="thinking-block" :class="{ compact }">
    <ToolCallHeader :presentation="presentation" :expandable="!compact" :open="fullOpen" :controls="bodyId" @toggle="toggle" />
    <div v-if="fullOpen" :id="bodyId" ref="body" class="thinking-full" tabindex="0" role="region" :aria-label="t('thinkingBlock.full')"
      @scroll.passive="onScroll" @wheel.passive="onWheel" @keydown="onKeydown">{{ text }}</div>
    <StreamTailWindow v-else class="thinking-recent" :text="text" :streaming="streaming" :lines="3" />
  </section>
</template>

<style scoped>
.thinking-block { margin: 5px 0; min-width: 0; }
.thinking-recent { margin: 3px 0 0 23px; color: var(--el-text-color-secondary); font-size: 12px; line-height: 1.7; }
.thinking-full { margin: 4px 0 0 23px; padding: 0 8px 0 10px; max-height: 40vh; overflow: auto; scrollbar-gutter: stable; overscroll-behavior: contain; border-left: 1px solid var(--el-border-color-lighter); color: var(--el-text-color-secondary); font-size: 12px; line-height: 1.7; white-space: pre-wrap; overflow-wrap: anywhere; }
.thinking-full:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 2px; }
</style>
