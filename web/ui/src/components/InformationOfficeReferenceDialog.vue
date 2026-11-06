<script setup lang="ts">
import { getLocale } from "@/i18n";
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Search, Document, Lock, Close, Fold, Expand, Refresh } from "@element-plus/icons-vue";
import { Icon } from "@iconify/vue";
import { informationApi, type InformationEntry } from "@/api/information";
import type { OfficeReferenceSnapshot } from "@/api/types";
import { fileIcon } from "./fileIcons";
import InformationOfficeViewer from "./InformationOfficeViewer.vue";
import OfficeReference from "./OfficeReference.vue";

const props = withDefaults(defineProps<{ modelValue: boolean; disabled?: boolean; error?: string }>(), { disabled: false, error: "" });
const emit = defineEmits<{ "update:modelValue": [value: boolean]; select: [value: OfficeReferenceSnapshot] }>();
const { t } = useI18n();
const entries = ref<InformationEntry[]>([]), query = ref(""), listLoading = ref(false), listError = ref("");
const chosen = ref<InformationEntry | null>(null), selected = ref<OfficeReferenceSnapshot | null>(null), viewerError = ref("");
const filesCollapsed = ref(false), viewerVersion = ref(0), detailsOpen = ref(false);
let listSequence = 0, disposed = false, listController: AbortController | undefined;
const files = computed(() => entries.value.filter(entry => entry.kind === "file" && entry.file && /\.(?:docx|xlsx)$/i.test(entry.file.name)).sort((a, b) => a.name.localeCompare(b.name, getLocale())));
const visibleFiles = computed(() => files.value.filter(entry => entry.name.toLocaleLowerCase().includes(query.value.trim().toLocaleLowerCase())));
const viewerKey = computed(() => chosen.value ? `${chosen.value.id}:${chosen.value.version}:${viewerVersion.value}` : "");
const canInsert = computed(() => !props.disabled && !!selected.value);
function folderPath(entry: InformationEntry) {
  const names: string[] = [], seen = new Set<string>(); let parent = entry.parent_id;
  while (parent && !seen.has(parent) && seen.size < 32) { seen.add(parent); const folder = entries.value.find(value => value.id === parent); if (!folder) break; names.unshift(folder.name); parent = folder.parent_id; }
  return names.join(" / ");
}
async function loadList() {
  const ticket = ++listSequence; listController?.abort(); const controller = new AbortController(); listController = controller; listLoading.value = true; listError.value = "";
  try { const result = await informationApi.tree(controller.signal); if (!disposed && ticket === listSequence && props.modelValue) entries.value = result.entries; }
  catch (value) { if (!disposed && ticket === listSequence && !controller.signal.aborted) listError.value = value instanceof Error ? value.message : t("informationOffice.unavailable"); }
  finally { if (!disposed && ticket === listSequence) listLoading.value = false; }
}
function choose(entry: InformationEntry) {
  if (props.disabled || !props.modelValue || !files.value.some(value => value.id === entry.id && value.version === entry.version)) return;
  if (chosen.value?.id === entry.id && chosen.value.version === entry.version && !viewerError.value) return;
  chosen.value = entry; selected.value = null; detailsOpen.value = false; viewerError.value = ""; viewerVersion.value++;
  if (typeof window !== "undefined" && window.innerWidth < 760) filesCollapsed.value = true;
}
function acceptSelection(snapshot: OfficeReferenceSnapshot, key: string) {
  if (!props.modelValue || props.disabled || key !== viewerKey.value || !chosen.value || snapshot.information?.entry_id !== chosen.value.id || snapshot.information.version !== chosen.value.version || !snapshot.text.trim() || snapshot.dirty) return;
  selected.value = JSON.parse(JSON.stringify(snapshot)) as OfficeReferenceSnapshot;
  detailsOpen.value = false;
}
function failed(message: string, key: string) { if (props.modelValue && key === viewerKey.value) viewerError.value = message; }
function retry() { if (props.disabled || !chosen.value) return; viewerError.value = ""; selected.value = null; viewerVersion.value++; }
function insert() { if (canInsert.value && selected.value) emit("select", JSON.parse(JSON.stringify(selected.value)) as OfficeReferenceSnapshot); }
function close(value: boolean) { if (!props.disabled) emit("update:modelValue", value); }
watch(() => props.modelValue, open => {
  chosen.value = null; selected.value = null; viewerError.value = ""; detailsOpen.value = false; viewerVersion.value++;
  if (open) { query.value = ""; filesCollapsed.value = false; void loadList(); }
  else { ++listSequence; listController?.abort(); }
}, { immediate: true, flush: "sync" });
onBeforeUnmount(() => { disposed = true; ++listSequence; listController?.abort(); });
</script>

<template>
  <el-dialog :model-value="modelValue" :title="t('informationOffice.title')" width="calc(100vw - 24px)" top="12px" class="information-office-dialog" append-to-body :close-on-click-modal="false" :close-on-press-escape="!disabled" :show-close="!disabled" @update:model-value="close">
    <div class="office-workspace" :class="{ 'files-collapsed': filesCollapsed }" data-testid="information-office-workspace">
      <aside v-show="!filesCollapsed" class="office-files"><header><strong>{{ t('informationOffice.files') }}</strong><span>{{ files.length }}</span><button type="button" :disabled="disabled" data-testid="office-files-collapse" :title="t('informationOffice.collapseFiles')" :aria-label="t('informationOffice.collapseFiles')" @click="filesCollapsed = true"><el-icon><Fold /></el-icon></button></header><el-input v-model="query" clearable :placeholder="t('informationOffice.search')" :prefix-icon="Search" :disabled="disabled" />
        <div class="office-file-list"><p v-if="listLoading" class="office-state" role="status">{{ t('information.loading') }}</p><div v-else-if="listError" class="office-state office-error" role="alert">{{ listError }}<el-button text @click="loadList">{{ t('informationOffice.retry') }}</el-button></div><div v-else-if="!files.length" class="office-state"><el-icon><Document /></el-icon><strong>{{ t('informationOffice.empty') }}</strong><span>{{ t('informationOffice.emptyHint') }}</span></div><p v-else-if="!visibleFiles.length" class="office-state">{{ t('informationOffice.noResults') }}</p>
          <button v-for="entry in visibleFiles" :key="entry.id" type="button" class="office-file" :data-testid="`office-file-${entry.id}`" :class="{ active: chosen?.id === entry.id }" :disabled="disabled" :title="entry.name" @click="choose(entry)"><Icon :icon="fileIcon(entry.file?.name || entry.name)" aria-hidden="true" /><span><strong>{{ entry.name }}</strong><small>{{ folderPath(entry) || t('information.root') }} · {{ t('informationOffice.saved', { n: entry.version }) }}</small></span></button>
        </div><p class="office-library-note"><el-icon><Lock /></el-icon>{{ t('informationOffice.savedHint') }}</p>
      </aside>
      <section class="office-main"><header class="office-preview-header"><button v-if="filesCollapsed" type="button" class="office-files-toggle" data-testid="office-files-expand" :disabled="disabled" @click="filesCollapsed = false"><el-icon><Expand /></el-icon>{{ t('informationOffice.files') }}</button><div><strong>{{ chosen?.name || t('informationOffice.choose') }}</strong><span v-if="chosen">{{ t('informationOffice.saved', { n: chosen.version }) }}</span></div><el-button v-if="viewerError && chosen" text :disabled="disabled" @click="retry"><el-icon><Refresh /></el-icon>{{ t('informationOffice.retry') }}</el-button></header>
        <InformationOfficeViewer v-if="modelValue && chosen" :key="viewerKey" :entry-id="chosen.id" :version="chosen.version" :disabled="disabled" @reference="acceptSelection($event, viewerKey)" @error="failed($event, viewerKey)" />
        <div v-else class="office-state office-main-state"><el-icon><Document /></el-icon><strong>{{ t('informationOffice.choose') }}</strong><span>{{ t('informationOffice.chooseHint') }}</span><span>{{ t('informationOffice.limitsHint') }}</span></div>
      </section>
    </div>
    <section v-if="selected" class="office-selection" data-testid="office-selected-card" :class="{ expanded: detailsOpen }" aria-live="polite"><div class="office-selection-heading"><span class="office-selection-status">{{ t('informationOffice.selected') }}</span><small>{{ t('informationOffice.selectionReplace') }}</small><button type="button" :disabled="disabled" :aria-label="t('informationOffice.clearSelection')" @click="selected = null; detailsOpen = false"><el-icon><Close /></el-icon></button></div><div class="office-selection-card"><OfficeReference :office="selected" /><button type="button" class="office-details-toggle" :aria-expanded="detailsOpen" @click="detailsOpen = !detailsOpen">{{ t(detailsOpen ? 'informationOffice.hideDetails' : 'informationOffice.showDetails') }}</button></div><pre v-if="detailsOpen">{{ selected.text }}</pre></section>
    <p v-if="props.error" class="office-parent-error office-error" role="alert">{{ props.error }}</p>
    <template #footer><div class="office-footer"><span><el-icon><Lock /></el-icon>{{ t('informationOffice.savedHint') }}</span><div><el-button :disabled="disabled" @click="close(false)">{{ t('informationOffice.cancel') }}</el-button><el-button type="primary" data-testid="office-insert-selection" :disabled="!canInsert" @click="insert">{{ t('informationOffice.insert') }}</el-button></div></div></template>
  </el-dialog>
</template>

<style scoped>
.office-workspace{display:grid;grid-template-columns:250px minmax(0,1fr);flex:1;min-height:0;border:1px solid var(--el-border-color-lighter);border-radius:10px;overflow:hidden;background:var(--el-bg-color);transition:grid-template-columns .2s ease}.office-workspace.files-collapsed{grid-template-columns:minmax(0,1fr)}.office-files{display:flex;flex-direction:column;gap:12px;padding:14px 12px;background:var(--el-fill-color-extra-light);border-right:1px solid var(--el-border-color-lighter);min-height:0}.office-files>header{display:flex;align-items:center;gap:8px;font-size:12px}.office-files>header>strong{font-weight:600}.office-files>header>span{font-size:10px;color:var(--el-text-color-secondary);background:var(--el-fill-color);padding:2px 6px;border-radius:5px}.office-files>header>button{margin-left:auto;display:flex;align-items:center;justify-content:center;width:26px;height:26px;border:0;border-radius:6px;background:transparent;color:var(--el-text-color-secondary);cursor:pointer}.office-files>header>button:hover{background:var(--el-fill-color)}.office-files :deep(.el-input__inner){font-size:12px}.office-file-list{overflow:auto;min-height:0;flex:1;margin:0 -5px;padding:0 5px}.office-file{width:100%;display:flex;align-items:flex-start;gap:9px;border:1px solid transparent;border-radius:8px;background:transparent;text-align:left;padding:11px 9px;cursor:pointer;color:var(--el-text-color-primary);margin-bottom:5px;transition:background .16s,border-color .16s}.office-file:hover{background:var(--el-fill-color-light)}.office-file.active{border-color:var(--el-color-primary-light-7);background:var(--el-color-primary-light-9)}.office-file:disabled{cursor:default;opacity:.65}.office-file>svg{width:22px;height:22px;flex:none;margin-top:1px}.office-file>span{min-width:0}.office-file strong{display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden;word-break:break-word;font-size:12px;font-weight:550;line-height:1.5}.office-file small{display:block;font-size:10px;color:var(--el-text-color-secondary);margin-top:4px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.office-library-note{display:flex;align-items:flex-start;gap:5px;margin:0;padding-top:8px;border-top:1px solid var(--el-border-color-lighter);font-size:10px;line-height:1.6;color:var(--el-text-color-secondary)}.office-library-note>.el-icon{flex:none;margin-top:3px}.office-main{min-width:0;min-height:0;display:flex;flex-direction:column}.office-preview-header{display:flex;align-items:center;gap:12px;min-height:44px;padding:0 12px;border-bottom:1px solid var(--el-border-color-lighter);flex:none}.office-preview-header>div{display:flex;gap:10px;align-items:center;min-width:0;flex:1}.office-preview-header strong{font-size:12px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;font-weight:550}.office-preview-header>div>span{font-size:10px;color:var(--el-text-color-secondary);white-space:nowrap}.office-files-toggle{display:flex;align-items:center;gap:5px;border:1px solid var(--el-border-color-light);border-radius:6px;padding:5px 8px;background:var(--el-bg-color);color:var(--el-text-color-regular);font:inherit;font-size:11px;cursor:pointer;flex:none}.office-state{display:flex;flex-direction:column;align-items:center;justify-content:center;gap:12px;padding:28px 8px;text-align:center;font-size:12px;line-height:1.7;color:var(--el-text-color-secondary)}.office-state>.el-icon{font-size:30px;opacity:.5}.office-state>strong{font-weight:500;color:var(--el-text-color-regular)}.office-state>span{font-size:11px}.office-main-state{flex:1;min-height:0}.office-error{color:var(--el-color-danger)}.office-selection{flex:none;padding:8px 4px 0;max-height:26dvh;overflow:auto}.office-selection-heading{display:flex;align-items:center;gap:12px;font-size:11px;margin-bottom:5px}.office-selection-status{color:var(--el-color-success)}.office-selection-heading>small{font-size:10px;color:var(--el-text-color-secondary)}.office-selection-heading>button{margin-left:auto;display:flex;align-items:center;justify-content:center;width:24px;height:24px;border:0;border-radius:5px;background:transparent;color:var(--el-text-color-secondary);cursor:pointer}.office-selection-card{display:flex;align-items:center;gap:12px;min-width:0;border:1px solid var(--el-border-color-light);border-radius:7px;padding:6px 9px;background:var(--el-bg-color)}.office-selection-card :deep(.office-reference){border:0;background:transparent;max-width:min(100%,700px);margin:0;padding:0;font-size:12px}.office-details-toggle{margin-left:auto;font-size:11px;flex:none;padding:3px 6px;border:0;background:transparent;color:var(--el-text-color-secondary);cursor:pointer}.office-selection pre{font:11px/1.6 var(--el-font-family-monospace,monospace);white-space:pre-wrap;overflow-wrap:anywhere;margin:8px 0 0;max-height:15dvh;overflow:auto;padding:8px 10px;border-radius:6px;background:var(--el-fill-color-extra-light)}.office-parent-error{margin:6px 0 0;font-size:12px}.office-footer{display:flex;align-items:center;justify-content:space-between;gap:20px}.office-footer>span{display:flex;align-items:center;gap:6px;font-size:11px;color:var(--el-text-color-secondary);text-align:left;line-height:1.6}.office-footer>div{display:flex;align-items:center;flex:none}.office-footer .el-button{min-width:88px}@media(max-width:760px){.office-workspace{grid-template-columns:190px minmax(0,1fr)}.office-files{padding:10px 8px}.office-preview-header>div{display:block}.office-preview-header>div>span{display:block;margin-top:2px}.office-library-note{display:none}}@media(max-width:600px){.office-workspace:not(.files-collapsed){grid-template-columns:minmax(0,1fr)}.office-workspace:not(.files-collapsed)>.office-main{display:none}.office-footer>span{display:none}.office-footer{justify-content:flex-end}.office-selection-heading>small{display:none}.office-files-toggle{font-size:10px}.office-preview-header{gap:8px;padding:0 8px}}@media(prefers-reduced-motion:reduce){.office-workspace{transition:none}}
</style>
<style>
.information-office-dialog.el-dialog{height:calc(100dvh - 24px);margin:12px auto;display:flex;flex-direction:column;padding:14px 16px 12px;max-width:none;border-radius:12px}.information-office-dialog>.el-dialog__header{flex:none;margin-right:0;padding:0 36px 10px 2px}.information-office-dialog>.el-dialog__header .el-dialog__title{font-size:15px;font-weight:550}.information-office-dialog>.el-dialog__body{flex:1;min-height:0;display:flex;flex-direction:column;padding:0}.information-office-dialog>.el-dialog__footer{flex:none;padding:10px 0 0}@media(max-width:600px){.information-office-dialog.el-dialog{width:100vw!important;height:100dvh;margin:0!important;border-radius:0;padding:10px}.information-office-dialog>.el-dialog__header{padding-bottom:8px}.information-office-dialog>.el-dialog__footer{padding-top:8px}}
</style>
