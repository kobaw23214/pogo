<script setup lang="ts">
import { computed } from 'vue';
import { useI18n } from 'vue-i18n';

const props = withDefaults(defineProps<{ status: string; count?: number; stale?: boolean }>(), { count: 0, stale: false });
const { t } = useI18n();
const label = computed(() => props.status === 'cancelling' ? t('agentWorkflow.cancelling') : t(`agentWorkflow.status.${props.status}`));
const description = computed(() => `${label.value}${props.count > 1 ? ` · ${props.count}` : ''}${props.stale ? ` · ${t('agentWorkflow.activityReconnecting')}` : ''}`);
</script>

<template>
  <span class="aw-run-badge" :class="[status, { stale }]" :title="description" :aria-label="description" data-testid="workflow-run-badge">
    <span class="aw-run-badge-mark" aria-hidden="true"><svg v-if="status === 'waiting_confirmation'" viewBox="0 0 16 16"><path d="M5 7V3a1 1 0 0 1 2 0v4-5a1 1 0 0 1 2 0v5-4a1 1 0 0 1 2 0v5l1-1a1 1 0 0 1 2 1l-2 4a4 4 0 0 1-7 0L2 8a1 1 0 0 1 2-1l1 1" /></svg><svg v-else-if="status === 'completed'" viewBox="0 0 16 16"><path d="m3 8 3 3 7-7" /></svg><svg v-else-if="status === 'failed' || status === 'interrupted'" viewBox="0 0 16 16"><path d="m4 4 8 8M12 4l-8 8" /></svg><i v-else /></span>
    <span>{{ label }}</span><strong v-if="count > 1">{{ count }}</strong><span v-if="stale" class="aw-run-badge-stale" aria-hidden="true">↻</span>
  </span>
</template>

<style scoped>
.aw-run-badge{--aw-state-color:var(--el-color-info);display:inline-flex;align-items:center;gap:5px;max-width:100%;padding:4px 7px;border:1px solid color-mix(in srgb,var(--aw-state-color) 28%,transparent);border-radius:6px;background:color-mix(in srgb,var(--aw-state-color) 8%,var(--el-bg-color));color:var(--aw-state-color);font-size:11px;line-height:1.4;font-weight:550;white-space:nowrap;vertical-align:middle}
.aw-run-badge.running{--aw-state-color:var(--el-color-primary)}.aw-run-badge.waiting_confirmation{--aw-state-color:var(--el-color-warning)}.aw-run-badge.completed{--aw-state-color:var(--el-color-success)}.aw-run-badge.failed,.aw-run-badge.interrupted{--aw-state-color:var(--el-color-danger)}
.aw-run-badge-mark{display:grid;place-items:center;flex:none;width:12px;height:12px}.aw-run-badge-mark svg{width:12px;height:12px;fill:none;stroke:currentColor;stroke-width:1.6;stroke-linecap:round;stroke-linejoin:round}.aw-run-badge-mark i{width:6px;height:6px;border-radius:50%;background:currentColor}.running:not(.stale) .aw-run-badge-mark i{width:9px;height:9px;box-sizing:border-box;border:1.5px solid currentColor;border-right-color:transparent;background:none;animation:aw-run-badge-spin 1s linear infinite}
.aw-run-badge strong{font-size:10px;line-height:1.2;padding:1px 4px;border-radius:4px;background:color-mix(in srgb,var(--aw-state-color) 12%,transparent);font-variant-numeric:tabular-nums}.aw-run-badge.stale{border-style:dashed}.aw-run-badge-stale{font-size:12px;color:var(--el-color-warning)}
@keyframes aw-run-badge-spin{to{transform:rotate(360deg)}}@media(prefers-reduced-motion:reduce){.running .aw-run-badge-mark i{animation:none!important}}
</style>
