<script setup lang="ts">
import { computed } from 'vue';
import { useI18n } from 'vue-i18n';
import { contextRingPercent } from '@/agent/contextUsage';
import { contextLevel } from '@/agent/contextEstimate';
import type { TaskContextReading } from '@/agent/taskContextUsage';

const props = defineProps<{ reading?: TaskContextReading | null }>();
const { t } = useI18n();
const reading = computed(() => props.reading);
const percent = computed(() => contextRingPercent(reading.value?.percent));
const label = computed(() => reading.value?.percent == null ? '—' : `${reading.value.source === 'estimated' ? '~' : ''}${Math.round(reading.value.percent)}%`);
const level = computed(() => reading.value?.source === 'unknown' ? '' : contextLevel(reading.value?.used || 0, reading.value?.window || 0, reading.value?.clearTrigger));
const tokens = (value: number) => value.toLocaleString('en-US');
const description = computed(() => {
  const value = reading.value;
  const usage = !value || value.source === 'unknown' ? t('taskContextRing.unknown')
    : t('taskContextRing.usage', { used: `${value.source === 'estimated' ? '~' : ''}${tokens(value.used)}`, limit: value.window > 0 ? tokens(value.window) : t('taskContextRing.unknownLimit'), percent: value.percent == null ? '—' : `${Math.round(value.percent)}%` });
  return [t('taskContextRing.title'), usage, value ? t(`contextMeter.sources.${value.source}`) : '', value?.model ? t('taskContextRing.model', { model: value.model }) : '', value?.stale ? t('taskContextRing.stale') : ''].filter(Boolean).join('\n');
});
</script>

<template>
  <span class="task-context-ring" :class="[level, { estimated: reading?.source === 'estimated', stale: reading?.stale }]" role="img" tabindex="0" :title="description" :aria-label="description" data-testid="task-context-ring" :data-source="reading?.source || 'unknown'">
    <svg viewBox="0 0 36 36" aria-hidden="true"><circle class="tcr-track" cx="18" cy="18" r="14" pathLength="100" /><circle v-if="percent !== null" class="tcr-value" cx="18" cy="18" r="14" pathLength="100" :stroke-dasharray="`${percent} 100`" /><text x="18" y="18">{{ label }}</text></svg>
  </span>
</template>

<style scoped>
.task-context-ring{display:inline-flex;align-items:center;justify-content:center;flex:none;width:34px;height:34px;border-radius:50%;color:var(--el-color-success);vertical-align:middle;cursor:help}.task-context-ring svg{display:block;width:32px;height:32px;overflow:visible}.task-context-ring circle{fill:none;stroke-width:2.5;transform:rotate(-90deg);transform-origin:center}.tcr-track{stroke:var(--el-text-color-placeholder);opacity:.24}.tcr-value{stroke:currentColor;stroke-linecap:round}.task-context-ring text{fill:var(--el-text-color-regular);font-size:7.5px;font-weight:600;text-anchor:middle;dominant-baseline:central;font-variant-numeric:tabular-nums}.task-context-ring.mid{color:var(--el-color-warning)}.task-context-ring.high{color:var(--el-color-danger)}.task-context-ring[data-source=unknown]{color:var(--el-text-color-placeholder)}.task-context-ring.stale .tcr-value{opacity:.5}.task-context-ring:focus-visible{outline:2px solid var(--el-color-primary);outline-offset:2px}.task-context-ring.estimated .tcr-track{stroke-dasharray:2 2}
</style>
