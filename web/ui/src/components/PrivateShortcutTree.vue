<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { ArrowRight, Folder, FolderOpened } from "@element-plus/icons-vue";
import type { ShortcutFolder } from "@/api/shortcuts";
import { shortcutFolderChildren, shortcutFolderPath } from "@/agent/shortcutFolderTree";
const props = withDefaults(defineProps<{ folders: ShortcutFolder[]; selected: string; disabled?: boolean }>(), { disabled: false });
const emit = defineEmits<{ open: [id: string] }>();
const { t } = useI18n();
const expanded = ref<Set<string>>(new Set());
watch(() => [props.selected, props.folders] as const, () => {
  const next = new Set(expanded.value);
  for (const folder of shortcutFolderPath(props.folders, props.selected)) next.add(folder.id);
  expanded.value = next;
}, { immediate: true });
const rows = computed(() => {
  const result: { folder: ShortcutFolder; level: number; children: boolean }[] = [], seen = new Set<string>();
  function walk(parent: string, level: number) {
    if (level > 16) return;
    for (const folder of shortcutFolderChildren(props.folders, parent)) {
      if (seen.has(folder.id)) continue; seen.add(folder.id);
      const children = props.folders.some(value => value.parent_id === folder.id);
      result.push({ folder, level, children });
      if (expanded.value.has(folder.id)) walk(folder.id, level + 1);
    }
  }
  walk("", 1); return result;
});
function toggle(id: string) { const next = new Set(expanded.value); if (next.has(id)) next.delete(id); else next.add(id); expanded.value = next; }
</script>
<template><div class="private-shortcut-tree" role="group" :aria-label="t('shortcutWorkspace.privateFolders')"><div v-for="row in rows" :key="row.folder.id" class="private-tree-row" :class="{ selected: selected === row.folder.id }" :style="{ '--tree-depth': row.level - 1 }"><button type="button" class="private-tree-expand" :disabled="disabled || !row.children" :aria-label="t(expanded.has(row.folder.id) ? 'shortcutWorkspace.collapseFolder' : 'shortcutWorkspace.expandFolder', { name: row.folder.name })" :aria-expanded="row.children ? expanded.has(row.folder.id) : undefined" @click="toggle(row.folder.id)"><el-icon v-if="row.children" :class="{ expanded: expanded.has(row.folder.id) }"><ArrowRight /></el-icon></button><button type="button" role="treeitem" :aria-level="row.level + 1" :aria-selected="selected === row.folder.id" :aria-expanded="row.children ? expanded.has(row.folder.id) : undefined" :disabled="disabled" :title="row.folder.name" :data-folder-id="row.folder.id" @click="emit('open', row.folder.id)"><el-icon><FolderOpened v-if="selected === row.folder.id" /><Folder v-else /></el-icon><span>{{ row.folder.name }}</span></button></div><p v-if="!folders.length" class="private-tree-empty">{{ t('shortcutWorkspace.noPrivateFolders') }}</p></div></template>
<style scoped>.private-tree-row{display:flex;align-items:center;min-width:0;padding-left:calc(var(--tree-depth)*12px);border-radius:7px}.private-tree-row.selected{background:var(--el-color-primary-light-9);color:var(--el-color-primary)}button{font:inherit;cursor:pointer;color:inherit;background:none;border:0}button:disabled{opacity:.5;cursor:not-allowed}.private-tree-expand{display:grid;place-items:center;flex:none;width:20px;height:34px;padding:0}.private-tree-expand .el-icon{font-size:10px;color:var(--el-text-color-placeholder);transition:transform .15s}.private-tree-expand .expanded{transform:rotate(90deg)}button[role=treeitem]{display:flex;align-items:center;gap:7px;min-width:0;flex:1;padding:8px 7px 8px 0;text-align:left;font-size:12px}button[role=treeitem]>.el-icon{flex:none;color:var(--el-text-color-secondary)}button[role=treeitem]>span{min-width:0;overflow:hidden;white-space:nowrap;text-overflow:ellipsis}.private-tree-row:hover{background:var(--el-fill-color-light)}button:focus-visible{outline:2px solid var(--el-color-primary);outline-offset:-2px;border-radius:6px}.private-tree-empty{margin:8px 10px;font-size:10px;line-height:1.8;color:var(--el-text-color-placeholder)}</style>
