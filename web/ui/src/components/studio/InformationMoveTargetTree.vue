<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Icon } from "@iconify/vue";
import { ArrowDown, ArrowRight, Check, Collection, Expand, Fold } from "@element-plus/icons-vue";
import type { InformationEntry } from "@/api/information";
import { folderIcon, folderOpenIcon } from "@/components/fileIcons";
import { informationPath } from "@/agent/informationLibrary";
import { informationCanMoveTo, informationMoveInitialExpansion, informationMoveTargetRows } from "./informationMoveTargets";

const props = withDefaults(defineProps<{ entries: readonly InformationEntry[]; movingId: string; currentParent: string; modelValue: string; disabled?: boolean }>(), { disabled: false });
const emit = defineEmits<{ "update:modelValue": [id: string] }>();
const { t } = useI18n();
const expanded = ref<string[]>([]), focused = ref(""), treeElement = ref<HTMLElement>();
const rows = computed(() => informationMoveTargetRows(props.entries, props.movingId, expanded.value));
const visibleIDs = computed(() => ["", ...rows.value.map(row => row.entry.id)]);
const destinationValid = computed(() => informationCanMoveTo(props.entries, props.movingId, props.modelValue));
const rootValid = computed(() => informationCanMoveTo(props.entries, props.movingId, ""));
function pathLabel(id: string) { return [t("information.root"), ...informationPath(props.entries, id).map(entry => entry.name)].join(" / "); }
watch(() => props.movingId, () => { expanded.value = informationMoveInitialExpansion(props.entries, props.currentParent); focused.value = props.currentParent; }, { immediate: true });
watch(visibleIDs, ids => { if (!ids.includes(focused.value)) focused.value = ids.includes(props.modelValue) ? props.modelValue : ""; });
function select(id: string) {
  if (!props.disabled && informationCanMoveTo(props.entries, props.movingId, id)) emit("update:modelValue", id);
}
function toggle(id: string) {
  if (props.disabled) return;
  expanded.value = expanded.value.includes(id) ? expanded.value.filter(value => value !== id) : [...expanded.value, id];
}
function expandAll() { if (!props.disabled) expanded.value = props.entries.filter(entry => entry.kind === "folder").map(entry => entry.id); }
function collapseAll() { if (!props.disabled) expanded.value = []; }
async function focus(id: string) {
  focused.value = id;
  await nextTick();
  if (!props.disabled) Array.from(treeElement.value?.querySelectorAll<HTMLElement>("[role=treeitem]") || []).find(item => item.dataset.targetId === id)?.focus();
}
function onKey(event: KeyboardEvent, id: string) {
  if (props.disabled) return;
  const index = visibleIDs.value.indexOf(id), row = rows.value.find(item => item.entry.id === id);
  if (event.key === "ArrowDown") void focus(visibleIDs.value[Math.min(index + 1, visibleIDs.value.length - 1)] || "");
  else if (event.key === "ArrowUp") void focus(visibleIDs.value[Math.max(index - 1, 0)] || "");
  else if (event.key === "Home") void focus("");
  else if (event.key === "End") void focus(visibleIDs.value[visibleIDs.value.length - 1] || "");
  else if (event.key === "ArrowRight" && row?.expandable) { if (!row.expanded) toggle(id); else void focus(visibleIDs.value[index + 1] || id); }
  else if (event.key === "ArrowLeft" && row) { if (row.expandable && row.expanded) toggle(id); else void focus(row.entry.parent_id); }
  else if (event.key === "Enter" || event.key === " ") select(id);
  else return;
  event.preventDefault();
}
</script>

<template>
  <div class="move-target-picker">
    <div class="move-target-heading"><strong>{{ t('information.moveTo') }}</strong><div><button type="button" :disabled="disabled" @click="expandAll"><el-icon><Expand /></el-icon>{{ t('information.expandAll') }}</button><button type="button" :disabled="disabled" @click="collapseAll"><el-icon><Fold /></el-icon>{{ t('information.collapseAll') }}</button></div></div>
    <div ref="treeElement" class="move-target-tree" role="tree" :aria-label="t('information.moveTo')" :aria-disabled="disabled" data-testid="information-move-tree">
      <div class="move-target-row root" :class="{ selected: modelValue === '', current: currentParent === '' }" role="treeitem" :aria-level="1" :aria-selected="modelValue === ''" :aria-disabled="disabled || !rootValid" :tabindex="!disabled && focused === '' ? 0 : -1" data-target-id="" :title="t('information.root')" @focus="focused = ''" @click="select('')" @keydown="onKey($event, '')"><span class="move-tree-spacer" /><el-icon class="move-folder-icon"><Collection /></el-icon><span class="move-target-name">{{ t('information.root') }}</span><span v-if="currentParent === ''" class="move-current-label">{{ t('information.currentLocation') }}</span><el-icon v-if="modelValue === ''" class="move-selected-check"><Check /></el-icon></div>
      <div v-for="row in rows" :key="row.entry.id" class="move-target-row" :class="{ selected: modelValue === row.entry.id, current: currentParent === row.entry.id, blocked: row.blocked }" :style="{ paddingLeft: `${12 + Math.min(row.depth + 1, 8) * 20}px` }" role="treeitem" :aria-level="row.depth + 2" :aria-expanded="row.expandable ? row.expanded : undefined" :aria-selected="modelValue === row.entry.id" :aria-disabled="disabled || row.blocked" :tabindex="!disabled && focused === row.entry.id ? 0 : -1" :data-target-id="row.entry.id" :title="pathLabel(row.entry.id)" @focus="focused = row.entry.id" @click="select(row.entry.id)" @keydown="onKey($event, row.entry.id)">
        <button v-if="row.expandable" type="button" class="move-tree-toggle" tabindex="-1" :disabled="disabled" :aria-label="`${t(row.expanded ? 'information.collapse' : 'information.expand')} ${row.entry.name}`" @click.stop="toggle(row.entry.id)"><el-icon><ArrowDown v-if="row.expanded" /><ArrowRight v-else /></el-icon></button><span v-else class="move-tree-spacer" />
        <Icon :icon="row.expanded ? folderOpenIcon : folderIcon" class="move-folder-icon" aria-hidden="true" /><span class="move-target-name">{{ row.entry.name }}</span><span v-if="row.blocked" class="move-blocked-label">{{ t('information.moveUnavailable') }}</span><span v-else-if="currentParent === row.entry.id" class="move-current-label">{{ t('information.currentLocation') }}</span><el-icon v-if="modelValue === row.entry.id" class="move-selected-check"><Check /></el-icon>
      </div>
    </div>
    <p class="move-target-hint">{{ t('information.moveTreeHint') }}</p>
    <dl class="move-location-summary"><div><dt>{{ t('information.currentLocation') }}</dt><dd :title="pathLabel(currentParent)">{{ pathLabel(currentParent) }}</dd></div><div class="move-destination" :class="{ invalid: !destinationValid }"><dt>{{ t('information.moveDestination') }}</dt><dd :title="destinationValid ? pathLabel(modelValue) : ''">{{ destinationValid ? pathLabel(modelValue) : t('information.moveTargetGone') }}</dd></div></dl>
  </div>
</template>

<style scoped>
.move-target-picker{min-width:0}.move-target-heading{display:flex;align-items:center;justify-content:space-between;gap:8px;margin:0 0 10px;font-size:12px}.move-target-heading>div{display:flex;gap:6px}.move-target-heading button{display:inline-flex;align-items:center;gap:4px;padding:5px 7px;border:0;border-radius:6px;background:transparent;color:var(--el-text-color-secondary);font:inherit;font-size:11px;cursor:pointer}.move-target-heading button:hover:not(:disabled){background:var(--el-fill-color);color:var(--el-color-primary)}button:disabled{opacity:.5;cursor:not-allowed}.move-target-tree{max-height:min(42dvh,360px);min-height:160px;overflow:auto;border:1px solid var(--el-border-color-light);border-radius:10px;padding:6px;background:var(--el-fill-color-blank)}.move-target-row{display:flex;align-items:center;gap:7px;min-height:40px;min-width:0;box-sizing:border-box;padding:7px 12px;border-radius:6px;color:var(--el-text-color-regular);font-size:13px;cursor:pointer;outline:none}.move-target-row:hover{background:var(--el-fill-color-light)}.move-target-row.selected{background:var(--el-color-primary-light-9);color:var(--el-color-primary)}.move-target-row.blocked{color:var(--el-text-color-placeholder);cursor:not-allowed}.move-target-row:focus-visible,.move-target-heading button:focus-visible,.move-tree-toggle:focus-visible{outline:2px solid var(--el-color-primary);outline-offset:-2px}.move-target-name{min-width:36px;flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.move-folder-icon{flex:0 0 18px;width:18px;height:18px;font-size:18px}.move-tree-toggle,.move-tree-spacer{display:inline-grid;place-items:center;width:18px;height:22px;flex:0 0 18px;padding:0;border:0;border-radius:4px;background:transparent;color:var(--el-text-color-secondary);font-size:11px}.move-tree-toggle{cursor:pointer}.move-tree-toggle:hover:not(:disabled){background:var(--el-fill-color)}.move-current-label,.move-blocked-label{flex:none;padding:3px 6px;border-radius:4px;background:var(--el-fill-color);color:var(--el-text-color-secondary);font-size:10px;line-height:1.4}.move-blocked-label{background:transparent;color:var(--el-text-color-placeholder)}.move-selected-check{flex:none;margin-left:2px;font-size:16px;color:var(--el-color-primary)}.move-target-hint{margin:9px 2px 16px;color:var(--el-text-color-secondary);font-size:11px;line-height:1.6}.move-location-summary{display:flex;flex-direction:column;gap:8px;margin:0;border-radius:9px;padding:12px 14px;background:var(--el-fill-color-lighter)}.move-location-summary>div{display:flex;gap:12px;align-items:baseline;min-width:0}.move-location-summary dt{flex:0 0 60px;color:var(--el-text-color-secondary);font-size:11px;line-height:1.6}.move-location-summary dd{min-width:0;margin:0;font-size:12px;line-height:1.6;overflow-wrap:anywhere}.move-destination dd{font-weight:550;color:var(--el-color-primary)}.move-destination.invalid dd{color:var(--el-color-danger)}
@media(max-width:520px){.move-target-heading{align-items:flex-start;flex-wrap:wrap}.move-target-tree{max-height:38dvh;min-height:140px}.move-target-row{font-size:12px;padding-right:7px;gap:5px}.move-current-label,.move-blocked-label{font-size:9px;padding-inline:4px}.move-location-summary{padding:10px}.move-location-summary>div{gap:7px}.move-location-summary dt{flex-basis:54px}}
</style>
