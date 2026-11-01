<script setup lang="ts">
import { useI18n } from "vue-i18n";
defineProps<{ canExport: boolean; compact?: boolean }>();
const emit = defineEmits<{ import: []; export: [] }>();
const { t } = useI18n();
</script>

<template>
  <div class="bookmark-transfer" :class="{ compact }" role="group" :aria-label="t('nativeBrowser.bookmarkTransfer')">
    <button type="button" :title="t('nativeBrowser.bookmarkImport')" :aria-label="t('nativeBrowser.bookmarkImport')" @click="emit('import')">
      <svg viewBox="0 0 20 20" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M10 2v10m-4-4 4 4 4-4M3 12v5h14v-5" /></svg><span v-if="!compact">{{ t('nativeBrowser.bookmarkImport') }}</span>
    </button>
    <button type="button" :disabled="!canExport" :title="t('nativeBrowser.bookmarkExport')" :aria-label="t('nativeBrowser.bookmarkExport')" @click="emit('export')">
      <svg viewBox="0 0 20 20" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M10 12V2m-4 4 4-4 4 4M3 12v5h14v-5" /></svg><span v-if="!compact">{{ t('nativeBrowser.bookmarkExport') }}</span>
    </button>
  </div>
</template>

<style scoped>
.bookmark-transfer { display:inline-flex; justify-content:center; align-items:center; gap:8px; flex-shrink:0; }
button { display:inline-flex; align-items:center; justify-content:center; gap:5px; padding:6px 10px; border:0; border-radius:6px; background:transparent; color:var(--el-text-color-secondary); font:inherit; font-size:12px; cursor:pointer; }
button:hover:not(:disabled) { color:var(--el-color-primary); background:var(--el-fill-color-light); }
button:disabled { opacity:.45; cursor:default; }
button:focus-visible { outline:2px solid var(--el-color-primary); outline-offset:2px; }
.compact { gap:2px; }.compact button { width:28px; height:28px; padding:5px; }
</style>
