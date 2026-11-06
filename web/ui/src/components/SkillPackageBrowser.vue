<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Icon } from "@iconify/vue";
import { ArrowRight, Files, Fold, Refresh } from "@element-plus/icons-vue";
import type { SkillDocumentContent, SkillDocumentUpdate, SkillPackageEntry, SkillPackageFiles } from "@/api/types";
import SkillDocumentViewer from "./SkillDocumentViewer.vue";
import { knownHostReason } from "@/agent/hostStatusCopy";
import { fileIcon, folderIcon, folderOpenIcon } from "./fileIcons";
import { skillFileBytes, useSkillPackageFiles } from "./skillPackageFiles";

const props = defineProps<{
  scopeKey: string;
  fileTreeHint?: string;
  rootDocument: SkillDocumentContent;
  loadDocument: (path: string) => Promise<SkillDocumentContent>;
  loadFiles: (path: string) => Promise<SkillPackageFiles>;
  saveDocument?: (input: SkillDocumentUpdate) => Promise<SkillDocumentContent>;
}>();
const emit = defineEmits<{ saved: [document: SkillDocumentContent]; reloaded: [document: SkillDocumentContent] }>();
const { t } = useI18n();
const viewer = ref<InstanceType<typeof SkillDocumentViewer> | null>(null);
const defaultTreeOpen = () => typeof window === "undefined" || !window.matchMedia?.("(max-width: 680px)").matches;
const treeOpen = ref(defaultTreeOpen()), selectedPath = ref("SKILL.md");
const { expanded, index, rows, ensure, toggle, reveal } = useSkillPackageFiles({ scope: () => props.scopeKey, load: path => props.loadFiles(path) });
const selectedFile = computed(() => index.value.get(selectedPath.value));
const rowIcon = (entry: SkillPackageEntry) => entry.kind === "directory" ? expanded.value.has(entry.path) ? folderOpenIcon : folderIcon : fileIcon(entry.name);
async function select(entry: SkillPackageEntry) {
  if (entry.kind === "directory") { void toggle(entry); return; }
  if (entry.path !== selectedPath.value && !(await confirmLeave())) return;
  selectedPath.value = entry.path;
}
function confirmLeave() { return viewer.value?.confirmLeave() ?? Promise.resolve(true); }
async function loadPreview(path: string): Promise<SkillDocumentContent> {
  const scope = props.scopeKey;
  const entry = index.value.get(path) || await reveal(path);
  if (scope !== props.scopeKey) throw new Error("Skill scope changed");
  // Listing metadata is enough to explain binaries, links and guarded runtime
  // files. Never open/read these through the text endpoint or run/download them.
  if (entry && !entry.previewable) return { content: "", truncated: false, relative_path: path, language: "text" };
  return props.loadDocument(path);
}
watch(() => props.scopeKey, () => { selectedPath.value = props.rootDocument.relative_path || "SKILL.md"; treeOpen.value = defaultTreeOpen(); }, { immediate: true, flush: "sync" });
watch(selectedPath, path => { void reveal(path); });
// A successful root edit creates a new revision without remounting the editor.
// Refresh only listing metadata so the file size follows the saved content.
watch(() => props.rootDocument.revision, (next, previous) => {
  if (next && previous && next !== previous) void ensure("", true);
});
defineExpose({ selectedPath, treeOpen, select, loadPreview, confirmLeave });
</script>

<template>
  <section class="skill-package-browser" :class="{ 'tree-hidden': !treeOpen }" data-testid="skill-package-browser">
    <aside v-show="treeOpen" class="package-sidebar" :aria-label="t('skillDocuments.packageFiles')">
      <header class="tree-head"><span><el-icon><Files /></el-icon>{{ t('skillDocuments.packageFiles') }}</span><button type="button" class="tree-toggle" data-testid="skill-files-hide" :aria-label="t('skillDocuments.hideFiles')" @click="treeOpen = false"><el-icon><Fold /></el-icon></button></header>
      <div class="package-tree" role="tree" :aria-label="t('skillDocuments.packageFiles')">
        <template v-for="row in rows" :key="row.key">
          <button v-if="row.entry" type="button" role="treeitem" :aria-level="row.depth + 1" :aria-selected="selectedPath === row.entry.path" :aria-expanded="row.entry.kind === 'directory' ? expanded.has(row.entry.path) && row.entry.expandable !== false : undefined" class="file-row" :class="{ selected: selectedPath === row.entry.path, guarded: !row.entry.previewable && row.entry.kind !== 'directory' }" :style="{ paddingLeft: `${10 + row.depth * 16}px` }" :data-skill-file="row.entry.path" :title="row.entry.reason ? `${row.entry.path}\n${knownHostReason(row.entry.reason)}` : row.entry.path" @click="select(row.entry)">
            <el-icon class="tree-caret" :class="{ expanded: expanded.has(row.entry.path), invisible: row.entry.kind !== 'directory' || row.entry.expandable === false }"><ArrowRight /></el-icon>
            <Icon class="file-icon" :icon="rowIcon(row.entry)" /><span class="file-name">{{ row.entry.name }}</span><span v-if="row.entry.kind !== 'directory'" class="file-size">{{ skillFileBytes(row.entry.size_bytes) }}</span>
          </button>
          <div v-else class="tree-state" :class="{ bad: row.state === 'error', caution: row.state === 'truncated' }" :style="{ paddingLeft: `${18 + row.depth * 16}px` }" :role="row.state === 'error' ? 'alert' : 'status'">
            <template v-if="row.state === 'error'"><span>{{ row.message }}</span><button type="button" class="tree-toggle" :aria-label="t('skillDocuments.retry')" @click="ensure(row.directory, true)"><el-icon><Refresh /></el-icon></button></template>
            <template v-else>{{ t(`skillDocuments.tree${row.state === 'loading' ? 'Loading' : row.state === 'truncated' ? 'Truncated' : 'Empty'}`) }}</template>
          </div>
        </template>
      </div>
      <footer class="tree-footer">{{ fileTreeHint || t('skillDocuments.lazyFiles') }}</footer>
    </aside>
    <main class="package-preview">
      <button v-if="!treeOpen" type="button" class="show-files" data-testid="skill-files-show" @click="treeOpen = true"><el-icon><Files /></el-icon>{{ t('skillDocuments.showFiles') }}</button>
      <SkillDocumentViewer ref="viewer" :scope-key="scopeKey" :root-document="rootDocument" :load-document="loadPreview" :selected-path="selectedPath" :file-info="selectedFile" managed :save-document="saveDocument" @path-change="selectedPath = $event" @saved="emit('saved', $event)" @reloaded="emit('reloaded', $event)" />
    </main>
  </section>
</template>

<style scoped>
.skill-package-browser{display:grid;grid-template-columns:260px minmax(0,1fr);align-items:stretch;min-width:0;border:1px solid var(--el-border-color-lighter);border-radius:10px;overflow:hidden;background:var(--el-bg-color)}
.skill-package-browser.tree-hidden{grid-template-columns:minmax(0,1fr)}.package-sidebar{min-width:0;border-right:1px solid var(--el-border-color-lighter);display:flex;flex-direction:column;background:var(--el-fill-color-lighter)}
.tree-head{display:flex;align-items:center;justify-content:space-between;gap:8px;padding:10px 12px;min-height:43px;border-bottom:1px solid var(--el-border-color-lighter);font-size:12px;font-weight:550}.tree-head>span{display:flex;align-items:center;gap:7px}
.tree-toggle{display:inline-flex;align-items:center;justify-content:center;padding:5px;border:0;background:none;border-radius:4px;color:var(--el-text-color-secondary);cursor:pointer}.tree-toggle:hover{background:var(--el-fill-color)}
.package-tree{padding:8px 5px;flex:1;overflow:auto;max-height:65vh;min-height:160px}.file-row{width:100%;display:flex;align-items:center;gap:5px;min-height:34px;padding:6px 8px;border:0;border-radius:5px;background:none;color:var(--el-text-color-regular);cursor:pointer;text-align:left;font:inherit;font-size:12px;line-height:1.5}.file-row:hover{background:var(--el-fill-color)}.file-row.selected{background:var(--pg-accent-wash,var(--el-color-primary-light-9));color:var(--el-color-primary);box-shadow:inset 0 0 0 1px var(--pg-accent-line,var(--el-color-primary-light-7))}.file-row:focus-visible{outline:2px solid var(--el-color-primary);outline-offset:-2px}
.tree-caret{font-size:10px;flex:0 0 10px;transition:transform .15s}.tree-caret.expanded{transform:rotate(90deg)}.tree-caret.invisible{visibility:hidden}.file-icon{height:18px;width:18px;flex:0 0 18px}.file-name{overflow:hidden;white-space:nowrap;text-overflow:ellipsis;flex:1;min-width:0}.file-size{font-size:10px;color:var(--el-text-color-placeholder);white-space:nowrap;margin-left:4px}.guarded .file-name{color:var(--el-text-color-secondary)}
.tree-state{display:flex;align-items:center;gap:5px;padding:6px 8px;overflow-wrap:anywhere;color:var(--el-text-color-placeholder);font-size:11px;line-height:1.6}.tree-state.bad{color:var(--el-color-danger)}.tree-state.caution{color:var(--el-color-warning-dark-2)}.tree-state>span{min-width:0;flex:1}.tree-footer{padding:9px 12px;font-size:11px;line-height:1.6;color:var(--el-text-color-placeholder);border-top:1px solid var(--el-border-color-lighter)}
.package-preview{min-width:0;min-height:0;position:relative;background:var(--el-bg-color);display:flex;flex-direction:column}.package-preview :deep(.skill-document-viewer){border:0;border-radius:0;min-height:0;flex:1}.package-preview :deep(.document-content){min-height:380px;max-height:65vh}.show-files{display:inline-flex;align-items:center;gap:5px;font:inherit;font-size:12px;cursor:pointer;background:var(--el-fill-color-lighter);color:var(--el-text-color-regular);border:1px solid var(--el-border-color-lighter);border-radius:5px;padding:5px 9px;margin:8px 12px}
@media(max-width:1000px){.skill-package-browser{grid-template-columns:220px minmax(0,1fr)}}
@media(max-width:680px){.skill-package-browser{grid-template-columns:minmax(0,1fr)}.package-sidebar{border-right:0;border-bottom:1px solid var(--el-border-color-lighter)}.package-tree{max-height:220px;min-height:80px}.tree-footer{display:none}.package-preview :deep(.document-content){min-height:220px;max-height:60vh}}
</style>
