<script setup lang="ts">
import { useI18n } from "vue-i18n";

const { t } = useI18n();
defineProps<{ controlsId?: string; minimizeTip?: string }>();
defineEmits<{ minimize: []; exit: [] }>();
</script>

<template>
  <div class="immersive-chat-actions" role="group" :aria-label="t('immersiveBrowser.windowControls')">
    <button type="button" class="immersive-chat-action immersive-chat-action--minimize"
      :title="minimizeTip || t('immersiveBrowser.minimizeTip')" :aria-label="t('immersiveBrowser.minimize')" :aria-controls="controlsId" @click="$emit('minimize')">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
        <rect x="3" y="3" width="18" height="18" rx="3" />
        <path d="M3 17h18M12 6v7m-3-3 3 3 3-3" />
      </svg>
      <span>{{ t('immersiveBrowser.minimize') }}</span>
    </button>
    <button type="button" class="immersive-chat-action immersive-chat-action--exit"
      :title="t('immersiveBrowser.exitTip')" :aria-label="t('immersiveBrowser.exit')" @click="$emit('exit')">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
        <rect x="3" y="4" width="18" height="16" rx="2.5" />
        <path d="M9 4v16m4-11 3 3-3 3" />
      </svg>
      <span>{{ t('immersiveBrowser.exit') }}</span>
    </button>
  </div>
</template>

<style scoped>
.immersive-chat-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
  flex-shrink: 0;
}
.immersive-chat-action {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  min-height: 34px;
  padding: 0 10px;
  border: 1px solid var(--el-border-color);
  border-radius: 9px;
  background: var(--el-fill-color);
  color: var(--el-text-color-primary);
  font: inherit;
  font-size: 12px;
  font-weight: 500;
  line-height: 1.2;
  white-space: nowrap;
  cursor: pointer;
  transition: background-color 120ms ease, border-color 120ms ease, transform 100ms ease;
}
.immersive-chat-action--minimize:hover {
  background: var(--el-fill-color-darker);
  border-color: var(--el-text-color-placeholder);
}
.immersive-chat-action--exit {
  border-color: var(--el-color-primary-light-5);
  background: var(--el-color-primary-light-9);
  color: var(--el-text-color-primary);
}
.immersive-chat-action--exit svg { color: var(--el-color-primary-dark-2); }
.immersive-chat-action--exit:hover {
  border-color: var(--el-color-primary);
  background: var(--el-color-primary-light-8);
}
.immersive-chat-action:active { transform: translateY(1px); }
.immersive-chat-action:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 3px; }
.immersive-chat-action svg { flex-shrink: 0; width: 17px; height: 17px; }
@media (pointer: coarse) { .immersive-chat-action { min-height: 44px; } }
@media (prefers-reduced-motion: reduce) {
  .immersive-chat-action { transition: none; }
  .immersive-chat-action:active { transform: none; }
}
</style>
