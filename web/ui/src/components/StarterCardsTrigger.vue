<script setup lang="ts">
import { Grid, Plus, Loading } from "@element-plus/icons-vue";
import { useI18n } from "vue-i18n";

defineProps<{ count: number; open: boolean; canSaveDraft?: boolean; savingDraft?: boolean }>();
const emit = defineEmits<{ click: []; saveDraft: [] }>();
const { t } = useI18n();
</script>

<template>
  <div
    class="starter-edge"
    :class="{ open }"
  >
    <div class="edge-tab">
      <button
        type="button"
        class="edge-open"
        data-testid="starter-trigger"
        :aria-label="t('starterCards.open')"
        aria-haspopup="dialog"
        :aria-expanded="open"
        @click="emit('click')"
      >
        <el-icon class="edge-icon" aria-hidden="true"><Grid /></el-icon>
        <span class="edge-label">{{ t("starterCards.shortcuts") }}</span>
        <span v-if="count" class="edge-count">{{ count }}</span>
      </button>
      <button
        v-if="canSaveDraft !== undefined"
        type="button"
        class="edge-save"
        data-testid="starter-save-draft"
        :disabled="!canSaveDraft || savingDraft"
        :aria-label="t('starterCards.saveDraft')"
        :title="t(canSaveDraft ? 'starterCards.saveDraft' : 'starterCards.saveDraftEmpty')"
        @click.stop="emit('saveDraft')"
      >
        <el-icon aria-hidden="true" :class="{ 'is-loading': savingDraft }"><Loading v-if="savingDraft" /><Plus v-else /></el-icon>
      </button>
    </div>
  </div>
</template>

<style scoped lang="scss">
// The hit area is wider than the visible tab. Only the tab expands, so the
// conversation and sidebar never move when the pointer approaches the edge.
.starter-edge {
  position: absolute;
  z-index: 8;
  top: 50%;
  left: 0;
  transform: translateY(-50%);
  display: flex;
  align-items: center;
  width: 28px;
  height: 72px;
  padding: 0;
  border: 0;
  background: transparent;
  -webkit-tap-highlight-color: transparent;

}

.edge-tab {
  display: flex;
  align-items: center;
  flex: 0 0 auto;
  width: 14px;
  height: 56px;
  overflow: hidden;
  border: 1px solid var(--el-border-color-light);
  border-left: 0;
  border-radius: 0 12px 12px 0;
  background: var(--el-fill-color-light);
  color: var(--el-text-color-placeholder);
  transition: width 180ms ease, height 180ms ease, color 180ms ease,
    border-color 180ms ease, background-color 180ms ease, box-shadow 180ms ease;
}

.edge-open {
  display: flex;
  align-items: center;
  flex: 1 0 auto;
  gap: 8px;
  height: 100%;
  padding: 0 8px 0 0;
  border: 0;
  background: transparent;
  color: inherit;
  cursor: pointer;
  font: inherit;
  min-width: 0;
}

.edge-save {
  flex: 0 0 32px;
  display: inline-grid;
  place-items: center;
  width: 32px;
  height: 32px;
  margin-right: 7px;
  padding: 0;
  border: 1px solid var(--pg-accent-line);
  border-radius: 8px;
  background: var(--el-color-primary-light-9);
  color: var(--el-color-primary);
  cursor: pointer;
  &:disabled { opacity: .45; cursor: not-allowed; }
  &:hover:not(:disabled) { background: var(--el-color-primary-light-8); }
}
.edge-open:focus-visible, .edge-save:focus-visible {
  outline: 2px solid var(--el-color-primary);
  outline-offset: -3px;
}

.edge-icon {
  flex: 0 0 16px;
  margin-left: -7px;
  font-size: 16px;
  transition: margin-left 180ms ease;
}

.edge-label,
.edge-count {
  flex: 0 0 auto;
  opacity: 0;
  white-space: nowrap;
  transition: opacity 120ms ease;
}

.edge-label { font-size: 12px; font-weight: 500; }
.edge-count {
  min-width: 17px;
  padding: 2px 4px;
  border-radius: 5px;
  background: var(--el-fill-color);
  font-size: 10px;
  font-variant-numeric: tabular-nums;
}

@mixin reveal {
  width: max-content;

  .edge-tab {
    width: max-content;
    min-width: 144px;
    height: 48px;
    color: var(--el-color-primary);
    border-color: var(--pg-accent-line);
    background: var(--el-bg-color-overlay);
    box-shadow: 4px 4px 16px rgb(0 0 0 / 10%);
  }
  .edge-icon { margin-left: 12px; }
  .edge-label, .edge-count { opacity: 1; }
}

@media (hover: hover) and (pointer: fine) {
  .starter-edge:hover { @include reveal; }
}

.starter-edge:focus-within,
.starter-edge.open { @include reveal; }

.starter-edge:focus-within .edge-tab {
  outline: 2px solid var(--el-color-primary);
  outline-offset: -3px;
}

// Touch has no hover: expose the full icon and keep a generous tap target.
@media (hover: none), (pointer: coarse) {
  .starter-edge { @include reveal; }
}

@media (prefers-reduced-motion: reduce) {
  .edge-tab, .edge-icon, .edge-label, .edge-count { transition: none; }
}
</style>
