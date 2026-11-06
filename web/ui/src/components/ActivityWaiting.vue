<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";

const props = defineProps<{ active: boolean; reserve?: boolean }>();
const { t } = useI18n();
const elapsed = ref(0);
let timer: ReturnType<typeof setInterval> | undefined;
watch(() => props.active, active => {
  if (timer) clearInterval(timer);
  timer = undefined;
  elapsed.value = 0;
  if (active && typeof window !== "undefined") {
    const started = Date.now();
    timer = setInterval(() => { elapsed.value = Math.max(0, Math.floor((Date.now() - started) / 1000)); }, 250);
  }
}, { immediate: true });
onBeforeUnmount(() => { if (timer) clearInterval(timer); });
const announced = computed(() => props.active && elapsed.value >= 10 ? t("workLog.live.waiting") : "");
</script>

<template>
  <div v-if="active || reserve" class="activity-waiting" :class="{ visible: active && elapsed >= 1 }">
    <span v-if="active && elapsed >= 1" class="waiting-dots" aria-hidden="true">···</span>
    <span v-if="active && elapsed >= 10" aria-hidden="true">{{ t("workLog.live.waiting") }} {{ elapsed }}s</span>
    <span class="waiting-announcement" role="status" aria-live="polite">{{ announced }}</span>
  </div>
</template>

<style scoped>
.activity-waiting { min-height: 28px; display: flex; align-items: center; gap: 8px; font-size: 12px; color: var(--pg-activity-text, var(--el-text-color-regular)); font-variant-numeric: tabular-nums; }
.waiting-dots { font-size: 18px; letter-spacing: 3px; }
.waiting-announcement { position: absolute; width: 1px; height: 1px; padding: 0; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
</style>
