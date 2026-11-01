<script setup lang="ts">
import { ref } from "vue";
import { useI18n } from "vue-i18n";
import { Check, ArrowRight } from "@element-plus/icons-vue";
import type { SavedShortcutDetails } from "@/agent/shortcutReceipt";
import type { ShortcutSnapshot } from "@/api/shortcuts";
import ShortcutReferenceDialog from "../ShortcutReferenceDialog.vue";
const props = defineProps<{ details: SavedShortcutDetails }>();
const preview = ref<ShortcutSnapshot | null>(null);
const { t } = useI18n();
function open() { preview.value = JSON.parse(JSON.stringify(props.details.shortcut)); }
</script>

<template>
  <button class="saved-shortcut" type="button" data-testid="shortcut-created-result" @click="open">
    <span class="saved-shortcut-check"><Check /></span>
    <span class="saved-shortcut-text"><strong>{{ details.shortcut.title }}</strong><small>{{ t(details.reused ? 'shortcutTools.reused' : details.action === 'update' ? details.changed === false ? 'shortcutTools.unchanged' : 'shortcutTools.updated' : 'shortcutTools.saved') }}</small></span>
    <span class="saved-shortcut-open">{{ t('shortcutTools.view') }} <ArrowRight /></span>
  </button>
  <ShortcutReferenceDialog v-model="preview" />
</template>

<style scoped>
.saved-shortcut{width:100%;max-width:560px;display:flex;align-items:center;gap:12px;padding:13px 15px;text-align:left;border:1px solid var(--el-border-color);border-radius:12px;background:var(--el-bg-color);color:var(--el-text-color-primary);cursor:pointer;font:inherit}.saved-shortcut:hover{border-color:var(--el-color-primary);background:var(--el-fill-color-light)}.saved-shortcut:focus-visible{outline:2px solid var(--el-color-primary);outline-offset:2px}.saved-shortcut-check{display:grid;place-items:center;width:30px;height:30px;flex:none;border-radius:9px;background:var(--el-color-success-light-9);color:var(--el-color-success)}.saved-shortcut-check svg{width:17px}.saved-shortcut-text{display:flex;flex:1;min-width:0;flex-direction:column;gap:4px}.saved-shortcut-text strong{font-size:14px;overflow-wrap:anywhere}.saved-shortcut-text small{font-size:12px;color:var(--el-text-color-secondary)}.saved-shortcut-open{display:flex;align-items:center;gap:4px;flex:none;font-size:12px;color:var(--el-color-primary)}.saved-shortcut-open svg{width:13px}@media(max-width:480px){.saved-shortcut-open{font-size:0}.saved-shortcut{padding:11px}}
</style>
