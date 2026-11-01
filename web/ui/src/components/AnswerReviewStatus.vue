<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Check, Warning, Close } from "@element-plus/icons-vue";
import type { AnswerReview } from "@/api/types";

const props = defineProps<{ review: AnswerReview; serverOffset?: number | null }>();
const { t } = useI18n();
const now = ref(Date.now());
let timer: ReturnType<typeof setInterval> | undefined;
let mounted = false;
const running = computed(() => props.review.state === "running");
const label = computed(() => {
  const kind = props.review.kind === "quote" ? "quote" : "citation";
  const state = props.review.state === "fallback" && props.review.reason === "timeout" ? "timeout" : props.review.state;
  return t(`answerReview.${kind}.${state}`);
});
const elapsed = computed(() => {
  const review = props.review;
  const ms = running.value ? now.value + (props.serverOffset ?? 0) - review.started_at :
    review.duration_ms ?? ((review.finished_at ?? review.started_at) - review.started_at);
  return t("answerReview.elapsed", { seconds: Math.floor(Math.max(0, ms) / 1000) });
});
function updateTimer() {
  if (timer !== undefined) clearInterval(timer);
  timer = undefined;
  now.value = Date.now();
  if (mounted && running.value) timer = setInterval(() => { now.value = Date.now(); }, 1000);
}
watch(() => [props.review.state, props.review.started_at], updateTimer);
onMounted(() => { mounted = true; updateTimer(); });
onBeforeUnmount(() => { mounted = false; if (timer !== undefined) clearInterval(timer); });
</script>

<template>
  <div class="answer-review" :class="review.state" role="status" aria-live="polite" :aria-busy="running" :aria-label="label" :data-state="review.state" data-testid="answer-review-status">
    <span v-if="running" class="review-spinner" aria-hidden="true" />
    <Check v-else-if="review.state === 'completed'" class="review-icon" aria-hidden="true" />
    <Close v-else-if="review.state === 'cancelled'" class="review-icon" aria-hidden="true" />
    <Warning v-else class="review-icon" aria-hidden="true" />
    <span class="review-label">{{ label }}</span>
    <span class="review-elapsed" aria-hidden="true">{{ elapsed }}</span>
  </div>
</template>

<style scoped lang="scss">
.answer-review { display: flex; align-items: center; gap: 7px; min-height: 24px; margin: 5px 0 8px; color: var(--el-text-color-secondary); font-size: 12px; line-height: 1.55; }
.answer-review.running { color: var(--el-color-primary); }
.answer-review.fallback { color: var(--el-color-warning-dark-2, var(--el-text-color-secondary)); }
.review-spinner { width: 13px; height: 13px; box-sizing: border-box; border: 2px solid var(--pg-accent-line, var(--el-border-color)); border-top-color: currentColor; border-right-color: currentColor; border-radius: 50%; flex: 0 0 auto; animation: review-spin .85s linear infinite; }
.review-icon { width: 14px; height: 14px; flex: 0 0 auto; }
.review-label { min-width: 0; }
.review-elapsed { font-variant-numeric: tabular-nums; white-space: nowrap; font-size: 11px; opacity: .8; }
@keyframes review-spin { to { transform: rotate(360deg); } }
@media (prefers-reduced-motion: reduce) { .review-spinner { animation: none; border-style: dotted; } }
</style>
