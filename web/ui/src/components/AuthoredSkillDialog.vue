<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Download, Refresh } from "@element-plus/icons-vue";
import { api } from "@/api/client";
import type { AuthoredSkillSummary, SkillDocumentContent } from "@/api/types";
import { registerStudioNavigationGuard } from "@/agent/studioNavigation";
import { notifyAuthoredSkillChanged } from "@/agent/skillAuthoringReceipt";
import SkillPackageBrowser from "./SkillPackageBrowser.vue";
import { useAuthoredSkillEditor } from "./authoredSkillEditor";
const props = defineProps<{ modelValue: boolean; agentId: string; skill: AuthoredSkillSummary | null }>();
const emit = defineEmits<{ "update:modelValue": [boolean]; saved: [document: SkillDocumentContent] }>();
const { t } = useI18n();
const browser = ref<InstanceType<typeof SkillPackageBrowser> | null>(null);
const exportedURLs = new Set<string>();
const exporting = ref(false), exportError = ref(""), notice = ref("");
let exportGeneration = 0;
const scope = () => props.agentId && props.skill ? `${props.agentId}:${props.skill.id}` : "";
const editor = useAuthoredSkillEditor({
  scope, open: () => props.modelValue,
  content: (path, revision) => api.authoredSkillContent(props.agentId, props.skill!.id, path, revision),
  files: (path, revision) => api.authoredSkillFiles(props.agentId, props.skill!.id, path, revision),
  versions: () => api.authoredSkillVersions(props.agentId, props.skill!.id),
  save: input => api.saveAuthoredSkillContent(props.agentId, props.skill!.id, input.content, input.expected_revision),
  confirmLeave: () => browser.value?.confirmLeave() ?? Promise.resolve(true),
  saved: document => { notice.value = t("skillAuthoring.savedNotice"); emit("saved", document); notifyAuthoredSkillChanged(props.agentId, props.skill!.id, document.revision || ""); },
});
const { rootDocument, versions, selectedRevision, loading, error, historical, newerVersion, scopeKey } = editor;
const title = computed(() => props.skill?.name || t("skillAuthoring.title"));
async function reloadLatest() {
  const requestedScope = scope();
  if (await confirmLeave() && requestedScope === scope() && props.modelValue) await editor.load();
}
async function confirmLeave() { return await (browser.value?.confirmLeave() ?? true); }
async function close(done: () => void) { if (await confirmLeave()) done(); }
const removeGuard = registerStudioNavigationGuard(confirmLeave);
async function versionChange(event: Event) {
  const select = event.target as HTMLSelectElement;
  await editor.selectRevision(select.value);
  select.value = selectedRevision.value;
}
watch([scope, () => props.modelValue], () => { exportGeneration++; exporting.value = false; exportError.value = ""; notice.value = ""; });
async function download() {
  if (!props.skill || !rootDocument.value || exporting.value) return;
  const requestedScope = scope(), request = ++exportGeneration, id = props.skill.id;
  exporting.value = true; exportError.value = "";
  try {
    const blob = await api.exportAuthoredSkill(props.agentId, id, rootDocument.value.revision);
    if (!props.modelValue || scope() !== requestedScope || request !== exportGeneration) return;
    const url = URL.createObjectURL(blob); exportedURLs.add(url);
    const link = document.createElement("a"); link.href = url; link.download = `${title.value}-v${rootDocument.value.version || props.skill.version}.zip`; link.click();
    setTimeout(() => { if (exportedURLs.delete(url)) URL.revokeObjectURL(url); }, 1000);
  } catch (reason) { if (request === exportGeneration && requestedScope === scope()) exportError.value = `${t("skillAuthoring.exportFailed")}：${reason instanceof Error ? reason.message : String(reason)}`; }
  finally { if (request === exportGeneration) exporting.value = false; }
}
onBeforeUnmount(() => { exportGeneration++; removeGuard(); for (const url of exportedURLs) URL.revokeObjectURL(url); exportedURLs.clear(); });
defineExpose({ confirmLeave });
</script>

<template>
  <el-dialog :model-value="modelValue" :title="title" width="min(1240px, calc(100vw - 32px))" top="4vh" append-to-body destroy-on-close :before-close="close" class="authored-skill-dialog" data-testid="authored-skill-dialog" @update:model-value="emit('update:modelValue', $event)">
    <div class="authored-skill-meta"><span class="scope-badge">{{ t('skillAuthoring.private') }}</span><span>{{ t('skillAuthoring.structural') }}</span><span>{{ t('skillAuthoring.behavior') }}</span></div>
    <div v-if="loading" class="skill-state" role="status">{{ t('skillAuthoring.loading') }}</div>
    <div v-else-if="error" class="skill-state bad" role="alert"><span>{{ t('skillAuthoring.failed') }}：{{ error }}</span><el-button text @click="editor.load"><el-icon><Refresh /></el-icon>{{ t('skillAuthoring.retry') }}</el-button></div>
    <template v-else-if="rootDocument && skill">
      <div class="authored-skill-toolbar">
        <label class="version-picker"><span>{{ t('skillAuthoring.version') }}</span><select :value="selectedRevision" :aria-label="t('skillAuthoring.version')" data-testid="authored-skill-version" @change="versionChange"><option value="">{{ t('skillAuthoring.latest') }} · v{{ rootDocument.version || versions.find(v => v.revision === editor.currentRevision.value)?.version || skill.version }}</option><option v-for="version in versions.filter(v => v.revision !== editor.currentRevision.value)" :key="version.revision" :value="version.revision">v{{ version.version }}</option></select></label>
        <el-button :loading="exporting" :disabled="exporting" data-testid="authored-skill-export" @click="download"><el-icon><Download /></el-icon>{{ t(exporting ? 'skillAuthoring.exporting' : 'skillAuthoring.export') }}</el-button>
      </div>
      <p v-if="historical" class="history-note" role="status">{{ t('skillAuthoring.historical') }}</p>
      <p v-if="newerVersion" class="history-note" role="status">{{ t('skillAuthoring.newerVersion') }} <button type="button" class="reload-button" data-testid="authored-skill-reload" @click="reloadLatest">{{ t('skillDocuments.reloadLatest') }}</button></p>
      <p v-if="notice" class="saved-notice" role="status">{{ notice }}</p><p v-if="exportError" class="bad" role="alert">{{ exportError }}</p>
      <SkillPackageBrowser ref="browser" :scope-key="scopeKey" :file-tree-hint="t('skillAuthoring.fileTreeHint')" :root-document="rootDocument" :load-document="editor.loadDocument" :load-files="editor.loadFiles" :save-document="editor.saveDocument" @saved="editor.documentSaved" @reloaded="editor.documentReloaded" />
    </template>
    <p class="skill-verification-note">{{ t('skillAuthoring.neverTested') }}</p>
  </el-dialog>
</template>

<style scoped>
.authored-skill-meta{display:flex;align-items:center;gap:10px;flex-wrap:wrap;font-size:12px;color:var(--el-text-color-secondary);margin-bottom:16px}.scope-badge{display:inline-flex;padding:4px 8px;border-radius:5px;background:var(--el-color-primary-light-9);color:var(--el-color-primary)}.authored-skill-toolbar{display:flex;align-items:center;justify-content:space-between;gap:12px;margin-bottom:12px}.version-picker{display:flex;align-items:center;gap:8px;font-size:12px;color:var(--el-text-color-secondary)}.version-picker select{font:inherit;padding:7px 10px;background:var(--el-bg-color);color:var(--el-text-color-primary);border:1px solid var(--el-border-color);border-radius:6px;min-width:160px}.reload-button{font:inherit;color:var(--el-color-primary);border:0;background:transparent;cursor:pointer;padding:0 4px}.skill-state{display:flex;gap:8px;align-items:center;justify-content:center;min-height:280px;color:var(--el-text-color-secondary)}.bad{color:var(--el-color-danger);overflow-wrap:anywhere}.history-note,.saved-notice{font-size:12px;line-height:1.6;border-radius:6px;padding:8px 10px;background:var(--el-fill-color-light);color:var(--el-text-color-secondary)}.saved-notice{color:var(--el-color-success)}.skill-verification-note{font-size:11px;line-height:1.7;color:var(--el-text-color-placeholder);margin:12px 0 0}@media(max-width:680px){.authored-skill-toolbar{align-items:stretch}.version-picker{min-width:0;flex:1}.version-picker select{min-width:0;width:100%}.authored-skill-meta{gap:7px}}
</style>
<style>.authored-skill-dialog{max-height:92dvh;display:flex;flex-direction:column}.authored-skill-dialog .el-dialog__body{overflow:auto;min-height:0}.authored-skill-dialog .el-dialog__header{padding-bottom:12px}@media(max-width:680px){.authored-skill-dialog{width:calc(100vw - 16px)!important;margin-top:2vh!important;max-height:96dvh}}</style>
