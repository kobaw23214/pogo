<script setup lang="ts">
import { useI18n } from "vue-i18n";
defineProps<{ pending: boolean; error?: string; syncing?: boolean }>();
const emit = defineEmits<{ retry: []; inspect: [] }>();
const { t } = useI18n();
</script>
<template>
  <div v-if="pending || error" class="studio-tool-notice" role="status" :data-testid="error ? 'studio-tools-sync-error' : 'studio-tools-pending'">
    <span class="notice-mark" aria-hidden="true">{{ error ? '!' : '↻' }}</span>
    <div class="notice-copy"><span>{{ t(error ? 'studioTools.syncFailed' : 'studioTools.pending') }}</span><small v-if="pending && !error">{{ t('studioTools.currentKept') }}</small></div>
    <button v-if="error" type="button" :disabled="syncing" @click="emit('retry')">{{ t('studioTools.retry') }}</button>
    <button v-else type="button" @click="emit('inspect')">{{ t('studioTools.view') }}</button>
  </div>
</template>
<style scoped>
.studio-tool-notice{display:flex;align-items:center;gap:9px;padding:9px 12px;border:1px solid var(--el-border-color-light);border-radius:10px;background:var(--el-fill-color-light);color:var(--el-text-color-regular);font-size:12px;line-height:1.55}.notice-mark{flex:none;color:var(--el-color-primary);font-size:15px}.notice-copy{display:flex;flex:1;min-width:0;flex-direction:column;gap:2px}.notice-copy small{color:var(--el-text-color-secondary);font-size:11px}.studio-tool-notice button{flex:none;border:0;background:none;color:var(--el-color-primary);padding:4px 0;font:inherit;cursor:pointer}.studio-tool-notice button:disabled{opacity:.5;cursor:wait}.studio-tool-notice button:focus-visible{outline:2px solid var(--el-color-primary);outline-offset:3px}@media(max-width:520px){.studio-tool-notice{flex-wrap:wrap}.notice-copy{flex-basis:calc(100% - 30px)}.studio-tool-notice button{margin-left:24px}}
</style>
