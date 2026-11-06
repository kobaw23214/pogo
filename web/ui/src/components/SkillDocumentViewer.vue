<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { ArrowLeft, Document, EditPen, Refresh, View } from "@element-plus/icons-vue";
import type { SkillDocumentContent, SkillDocumentUpdate, SkillPackageEntry } from "@/api/types";
import MarkdownView from "./MarkdownView.vue";
import CodeBlock from "./CodeBlock.vue";
import { resolveSkillDocumentLink, skillDocumentLanguage, useSkillDocumentNavigation } from "./skillDocumentNavigation";
import { useSkillDocumentEditing } from "./skillDocumentEditing";
import ConfirmDialog from "./ConfirmDialog.vue";
import { knownHostReason } from "@/agent/hostStatusCopy";

const props = defineProps<{
  scopeKey: string;
  rootDocument: SkillDocumentContent;
  loadDocument: (path: string) => Promise<SkillDocumentContent>;
  selectedPath?: string;
  fileInfo?: SkillPackageEntry;
  /** Manager-only controls. Session/Agent viewers never provide a writer. */
  managed?: boolean;
  saveDocument?: (input: SkillDocumentUpdate) => Promise<SkillDocumentContent>;
}>();
const emit = defineEmits<{ pathChange: [path: string]; saved: [document: SkillDocumentContent]; reloaded: [document: SkillDocumentContent] }>();
const { t } = useI18n();
const { document, location, history, loading, error, blocked, atRoot, open, openPath, back, home, retry } = useSkillDocumentNavigation({
  scope: () => props.scopeKey, root: () => props.rootDocument, load: path => props.loadDocument(path),
});
const leaveConfirm = ref<InstanceType<typeof ConfirmDialog> | null>(null);
const editor = useSkillDocumentEditing({
  scope: () => props.scopeKey, document: () => document.value, path: () => location.value.path,
  enabled: () => !!props.managed && !!props.saveDocument,
  save: input => props.saveDocument!(input),
  saved: next => { document.value = next; emit("saved", next); },
  confirmDiscard: async () => await leaveConfirm.value?.ask({ title: t("skillDocuments.discardTitle"), body: t("skillDocuments.discardBody"), confirmText: t("skillDocuments.discard"), cancelText: t("skillDocuments.keepEditing"), danger: true }) ?? false,
});
const { editing, mode, draft, dirty, canEdit, saving, confirming, error: saveError, conflict } = editor;
async function navigate(action: () => Promise<unknown>) {
  if (await editor.confirmLeave()) await action();
  else emit("pathChange", location.value.path);
}
async function openLink(href: string) {
  const target = resolveSkillDocumentLink(location.value.path, href);
  // Scrolling within a draft preview, or rejecting an unsafe link, does not
  // leave its document and must not discard the editor's source.
  if (target.kind === "blocked" || target.path === location.value.path) await open(href);
  else await navigate(() => open(href));
}
watch([() => props.scopeKey, () => props.rootDocument, () => props.selectedPath], ([, , path]) => {
  if (path && path !== location.value.path) void navigate(() => openPath(path));
}, { immediate: true });
watch(() => location.value.path, path => emit("pathChange", path), { flush: "sync" });
const unavailable = computed(() => props.fileInfo?.path === location.value.path && !props.fileInfo.previewable ? props.fileInfo : null);
function bytes(size: number) { return size < 1024 ? `${size} B` : size < 1048576 ? `${(size / 1024).toFixed(1)} KiB` : `${(size / 1048576).toFixed(1)} MiB`; }
const contentRoot = ref<HTMLElement | null>(null);
const language = computed(() => skillDocumentLanguage(document.value, location.value.path));
const markdown = computed(() => ["md", "markdown"].includes(language.value));
const source = computed(() => {
  const text = editing.value ? draft.value : document.value?.content || "";
  return atRoot.value && markdown.value ? text.replace(/^---\r?\n[\s\S]*?\r?\n---\r?\n?/, "") : text;
});
const readOnlyReason = computed(() => props.managed && atRoot.value && document.value && !canEdit.value
  ? knownHostReason(document.value.edit_disabled_reason) || t("skillDocuments.readOnly") : "");
async function reloadLatest() {
  if (!(await editor.confirmLeave())) return;
  await retry();
  if (atRoot.value && document.value && !error.value) emit("reloaded", document.value);
}
function beforeUnload(event: BeforeUnloadEvent) { if (dirty.value || saving.value) { event.preventDefault(); event.returnValue = ""; } }
onMounted(() => window.addEventListener("beforeunload", beforeUnload));
onBeforeUnmount(() => { if (typeof window !== "undefined") window.removeEventListener("beforeunload", beforeUnload); });
defineExpose({ confirmLeave: editor.confirmLeave, dirty, saving });
const headingSlug = (text: string) => text.toLowerCase().trim().replace(/[^\p{L}\p{N}\p{M}_\s-]/gu, "").replace(/\s/g, "-");
watch([document, () => location.value.anchor], async () => {
  await nextTick();
  const root = contentRoot.value;
  if (!root) return;
  if (!location.value.anchor) { root.scrollTop = 0; return; }
  const used = new Map<string, number>();
  const target = Array.from(root.querySelectorAll<HTMLElement>("h1,h2,h3,h4,h5,h6,[id],a[name]")).find(element => {
    if (element.id === location.value.anchor || element.getAttribute("name") === location.value.anchor) return true;
    if (!/^H[1-6]$/.test(element.tagName)) return false;
    const base = headingSlug(element.textContent || ""), number = used.get(base) || 0;
    used.set(base, number + 1);
    return (number ? `${base}-${number}` : base) === location.value.anchor;
  });
  target?.scrollIntoView?.({ block: "start", behavior: "smooth" });
}, { flush: "post" });
</script>

<template>
  <section class="skill-document-viewer" data-testid="skill-document-viewer" :aria-label="t('skillDocuments.title')">
    <nav class="document-bar" :aria-label="t('skillDocuments.navigation')">
      <button v-if="history.length" type="button" class="document-button" data-testid="skill-document-back" :disabled="saving || confirming" @click="navigate(back)"><el-icon><ArrowLeft /></el-icon>{{ t('skillDocuments.back') }}</button>
      <button v-if="!atRoot" type="button" class="document-button" data-testid="skill-document-home" :disabled="saving || confirming" @click="navigate(home)"><el-icon><Document /></el-icon>{{ t('skillDocuments.home') }}</button>
      <span class="document-path" data-testid="skill-document-path" :title="location.path">{{ location.path }}</span>
      <span v-if="dirty" class="dirty-label" role="status" data-testid="skill-document-dirty">{{ t('skillDocuments.unsaved') }}</span>
      <div v-if="managed && atRoot && document && !loading && !error" class="document-modes" role="group" :aria-label="t('skillDocuments.displayMode')">
        <button type="button" class="mode-button" :class="{ active: mode === 'preview' }" :aria-pressed="mode === 'preview'" :disabled="saving || confirming" data-testid="skill-document-preview" @click="editor.preview"><el-icon><View /></el-icon>{{ t('skillDocuments.preview') }}</button>
        <button type="button" class="mode-button" :class="{ active: mode === 'edit' }" :aria-pressed="mode === 'edit'" :disabled="!canEdit || saving || confirming" :title="readOnlyReason" data-testid="skill-document-edit" @click="editor.edit"><el-icon><EditPen /></el-icon>{{ t('skillDocuments.edit') }}</button>
      </div>
    </nav>
    <div v-if="editing" class="document-edit-bar">
      <span>{{ t('skillDocuments.sourceHint') }}</span>
      <div><button type="button" class="document-button" :disabled="saving || confirming" data-testid="skill-document-cancel" @click="editor.confirmLeave">{{ t('skillDocuments.cancel') }}</button><button type="button" class="document-button save-button" :disabled="!dirty || saving || confirming || conflict || !canEdit" data-testid="skill-document-save" @click="editor.save">{{ t(saving ? 'skillDocuments.saving' : 'skillDocuments.save') }}</button></div>
    </div>
    <p v-if="readOnlyReason && !loading" class="read-only-notice" data-testid="skill-document-read-only">{{ readOnlyReason }}</p>
    <div v-if="saveError" class="document-save-error" role="alert" data-testid="skill-document-save-error"><strong>{{ t(conflict ? 'skillDocuments.conflict' : 'skillDocuments.saveFailed') }}</strong><p>{{ saveError }}</p><p v-if="conflict">{{ t('skillDocuments.conflictHint') }}</p><button v-if="conflict" type="button" class="document-button" :disabled="saving || confirming" data-testid="skill-document-reload" @click="reloadLatest"><el-icon><Refresh /></el-icon>{{ t('skillDocuments.reloadLatest') }}</button></div>
    <p v-if="blocked" class="document-notice" role="alert">{{ t('skillDocuments.blocked') }}</p>
    <div ref="contentRoot" class="document-content" :class="{ 'editing-source': editing && mode === 'edit' && !loading && !error }" :aria-busy="loading">
      <div v-if="unavailable && !loading" class="unavailable-file" data-testid="skill-file-unavailable">
        <el-icon class="unavailable-icon"><Document /></el-icon><h3>{{ unavailable.name }}</h3>
        <p class="file-facts"><span>{{ unavailable.media_type || unavailable.kind }}</span><span>{{ bytes(unavailable.size_bytes) }}</span></p>
        <p>{{ t('skillDocuments.notPreviewable') }}</p><p v-if="unavailable.reason" class="file-reason">{{ knownHostReason(unavailable.reason) }}</p>
        <small>{{ t('skillDocuments.noExecution') }}</small>
      </div>
      <p v-else-if="loading" class="document-state" role="status">{{ t('skillDocuments.loading') }}</p>
      <div v-else-if="error" class="document-state document-error" role="alert"><strong>{{ t('skillDocuments.failed') }}</strong><p>{{ error }}</p><button type="button" class="document-button" data-testid="skill-document-retry" @click="retry"><el-icon><Refresh /></el-icon>{{ t('skillDocuments.retry') }}</button></div>
      <textarea v-else-if="editing && mode === 'edit'" v-model="draft" class="document-source" data-testid="skill-document-source" :aria-label="t('skillDocuments.sourceLabel')" :disabled="saving || confirming" spellcheck="false" autocapitalize="off" autocomplete="off" @keydown.ctrl.enter.prevent="editor.save" @keydown.meta.enter.prevent="editor.save" />
      <template v-else-if="document">
        <p v-if="document.truncated" class="document-notice" role="status">{{ t('skillDocuments.truncated') }}</p>
        <p v-if="!source" class="document-state">{{ t('skillDocuments.empty') }}</p>
        <MarkdownView v-else-if="markdown" class="document-markdown answer" :source="source" passive document-links @document-link="openLink" />
        <CodeBlock v-else :code="source" :lang="language" line-numbers uncollapsed />
      </template>
    </div>
    <ConfirmDialog ref="leaveConfirm" />
  </section>
</template>

<style scoped>
.skill-document-viewer{min-width:0;border:1px solid var(--el-border-color-lighter);border-radius:10px;overflow:hidden;background:var(--el-bg-color)}
.document-bar{display:flex;align-items:center;gap:8px;min-height:42px;padding:8px 12px;border-bottom:1px solid var(--el-border-color-lighter);background:var(--el-fill-color-lighter);flex-wrap:wrap}
.document-button{display:inline-flex;align-items:center;justify-content:center;gap:5px;border:1px solid var(--el-border-color-lighter);border-radius:6px;background:var(--el-bg-color);color:var(--el-text-color-regular);padding:5px 8px;cursor:pointer;font:inherit;font-size:12px;line-height:1.5;white-space:nowrap}
.document-button:hover{color:var(--el-color-primary);border-color:var(--el-color-primary)}
.document-button:disabled,.mode-button:disabled{opacity:.5;cursor:not-allowed}.document-button:disabled:hover{color:var(--el-text-color-regular);border-color:var(--el-border-color-lighter)}
.document-modes{display:inline-flex;padding:2px;border:1px solid var(--el-border-color-lighter);border-radius:7px;background:var(--el-fill-color-light);gap:2px}.mode-button{display:inline-flex;align-items:center;gap:5px;border:0;border-radius:5px;background:none;color:var(--el-text-color-secondary);padding:5px 9px;font:inherit;font-size:12px;cursor:pointer}.mode-button.active{background:var(--el-bg-color);box-shadow:0 1px 3px #00000012;color:var(--el-color-primary)}.dirty-label{font-size:11px;color:var(--el-color-warning-dark-2);white-space:nowrap}
.document-edit-bar{display:flex;justify-content:space-between;align-items:center;gap:12px;padding:9px 12px;border-bottom:1px solid var(--el-border-color-lighter);font-size:12px;line-height:1.6;color:var(--el-text-color-secondary)}.document-edit-bar>div{display:flex;gap:8px;flex-shrink:0}.save-button{background:var(--el-color-primary);color:var(--el-color-white);border-color:var(--el-color-primary)}.save-button:hover{color:var(--el-color-white)}.read-only-notice{padding:9px 12px;margin:0;font-size:12px;line-height:1.7;color:var(--el-text-color-secondary);background:var(--el-fill-color-lighter);border-bottom:1px solid var(--el-border-color-lighter)}
.document-save-error{padding:10px 12px;color:var(--el-color-danger);background:var(--el-color-danger-light-9);border-bottom:1px solid var(--el-border-color-lighter);font-size:12px;line-height:1.6}.document-save-error p{margin:5px 0;white-space:pre-wrap;overflow-wrap:anywhere}.document-save-error strong{font-weight:550}
.document-content.editing-source{padding:0;display:flex;min-height:320px}.document-source{box-sizing:border-box;width:100%;flex:1;min-height:0;resize:vertical;padding:18px 20px;border:0;outline:none;background:var(--el-bg-color);color:var(--el-text-color-primary);font:13px/1.8 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;tab-size:2;white-space:pre;overflow-wrap:normal}.document-source:focus-visible{box-shadow:inset 0 0 0 2px var(--pg-accent-line,var(--el-color-primary-light-7))}.document-source:disabled{color:var(--el-text-color-secondary)}
.document-path{flex:1;min-width:80px;overflow-wrap:anywhere;color:var(--el-text-color-secondary);font:12px/1.6 ui-monospace,SFMono-Regular,Menlo,monospace}
.document-content{max-height:65vh;min-height:130px;overflow:auto;padding:16px 20px;overscroll-behavior:contain;overflow-wrap:anywhere}
.document-state{color:var(--el-text-color-secondary);font-size:13px;line-height:1.8;margin:14px 0}.document-error{color:var(--el-color-danger)}.document-error p{white-space:pre-wrap;overflow-wrap:anywhere}
.document-notice{font-size:12px;line-height:1.7;color:var(--el-color-warning-dark-2);background:var(--el-color-warning-light-9);padding:8px 12px;margin:0}
.document-content>.document-notice{margin:-16px -20px 14px}.document-markdown{font-size:14px;line-height:1.8}
.document-markdown :deep(table){display:block;max-width:100%;overflow:auto}.document-markdown :deep(pre){max-width:100%;overflow:auto}
.unavailable-file{max-width:500px;margin:55px auto;text-align:center;color:var(--el-text-color-secondary);font-size:13px;line-height:1.8}.unavailable-file h3{font-weight:550;color:var(--el-text-color-primary);overflow-wrap:anywhere;margin:12px 0}.unavailable-icon{font-size:34px;color:var(--el-text-color-placeholder)}.file-facts{display:flex;gap:12px;justify-content:center;font-size:12px;flex-wrap:wrap}.file-reason{background:var(--el-fill-color-lighter);padding:10px 14px;border-radius:6px}.unavailable-file small{font-size:12px}
@media(max-width:600px){.document-content{padding:12px}.document-content>.document-notice{margin:-12px -12px 12px}.document-bar{padding:8px}.document-edit-bar{align-items:flex-start;flex-wrap:wrap}.document-edit-bar>div{margin-left:auto}.document-source{padding:12px}}
</style>
