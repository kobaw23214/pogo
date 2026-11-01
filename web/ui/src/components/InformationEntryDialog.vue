<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, provide, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Download } from "@element-plus/icons-vue";
import { Icon } from "@iconify/vue";
import { informationApi, type InformationEntry } from "@/api/information";
import InformationShortcutPreview from "./InformationShortcutPreview.vue";
import InformationOfficeViewer from "./InformationOfficeViewer.vue";
import { workspaceFileNavigationKey } from "@/agent/workspaceFileNavigation";
import CodeBlock from "./CodeBlock.vue";
import { fileIcon, folderIcon } from "./fileIcons";
import { informationFileLanguage, informationFilePreview, type InformationPreviewKind } from "./informationFilePreview";
const props = defineProps<{ modelValue: boolean; entryId: string; version?: number }>();
const emit = defineEmits<{ "update:modelValue": [value: boolean] }>();
const { t } = useI18n();
provide(workspaceFileNavigationKey, { scope: () => null, open: () => {} });
const entry = ref<InformationEntry | null>(null), loading = ref(false), downloading = ref(false), error = ref("");
const blobURL = ref(""), text = ref(""), kind = ref<InformationPreviewKind | "">("");
const officeViewer = ref<InstanceType<typeof InformationOfficeViewer> | null>(null);
const officeMounted = ref(false), officeError = ref(""), officeRetrying = ref(false), officeAttempt = ref(0);
const officeKey = computed(() => entry.value ? `${entry.value.id}:${entry.value.version}:${officeAttempt.value}` : "");
const officeActive = computed(() => props.modelValue && kind.value === "office" && !!entry.value?.file && officeMounted.value);
const officePreview = computed(() => {
  if (!officeActive.value || !entry.value) return null;
  const key = officeKey.value;
  return { key, entryId: entry.value.id, version: entry.value.version, onError: (message: string) => officeFailed(message, key) };
});
const language = computed(() => informationFileLanguage(entry.value?.file?.name || ""));
const previewIcon = computed(() => entry.value?.kind === "folder" ? folderIcon : fileIcon(entry.value?.file?.name || entry.value?.name || ""));
let sequence = 0, disposed = false, controller: AbortController | undefined, downloadController: AbortController | undefined;
const downloadURLs = new Set<string>();
function clear() { if (blobURL.value) URL.revokeObjectURL(blobURL.value); blobURL.value = ""; text.value = ""; kind.value = ""; officeMounted.value = false; officeError.value = ""; officeRetrying.value = false; officeAttempt.value++; }
async function load() {
  const ticket = ++sequence; controller?.abort(); downloadController?.abort(); downloading.value = false; loading.value = false; clear(); entry.value = null; error.value = ""; if (!props.modelValue) return;
  const id = props.entryId, version = props.version, current = new AbortController(); controller = current; loading.value = true;
  const valid = () => !disposed && ticket === sequence && props.modelValue && props.entryId === id && props.version === version;
  try {
    const result = await informationApi.entry(id, version, current.signal); if (!valid()) return; entry.value = result; if (!result.file) return;
    const format = informationFilePreview(result.file.name, result.file.mime, result.file.size, true);
    if (format === "office") { kind.value = format; officeMounted.value = true; return; }
    if (format === "unsupported" || format === "large") { kind.value = format; return; }
    const blob = await informationApi.content(id, result.version, false, current.signal); if (!valid()) return;
    if (informationFilePreview(result.file.name, result.file.mime, blob.size) === "large") { kind.value = "large"; return; }
    if (format === "text") { const content = await blob.text(); if (!valid()) return; text.value = content; } else blobURL.value = URL.createObjectURL(format === "pdf" ? new Blob([blob], { type: "application/pdf" }) : blob); kind.value = format;
  } catch (value) { if (valid() && !current.signal.aborted) error.value = t("information.previewError", { message: value instanceof Error ? value.message : String(value) }); }
  finally { if (!disposed && ticket === sequence) loading.value = false; }
}
watch(() => [props.modelValue, props.entryId, props.version], () => { void load(); }, { immediate: true, flush: "sync" });
function officeFailed(message: string, key: string) { if (!disposed && officeActive.value && key === officeKey.value) officeError.value = message; }
async function retryOffice() {
  if (!officeActive.value || officeRetrying.value) return;
  const ticket = sequence, key = officeKey.value, viewer = officeViewer.value;
  const valid = () => !disposed && props.modelValue && ticket === sequence && key === officeKey.value && kind.value === "office";
  officeMounted.value = false; officeRetrying.value = true; officeError.value = "";
  try {
    // Unmount first to stop the bridge and heartbeat, then wait for this exact
    // viewer's idempotent close before allocating a replacement capability.
    await nextTick(); await viewer?.cleanup();
    if (valid()) { officeAttempt.value++; officeMounted.value = true; }
  } finally { if (!disposed && ticket === sequence) officeRetrying.value = false; }
}
async function download() { const item = entry.value; if (!item?.file || downloading.value) return; downloading.value = true; error.value = "";
  const ticket = sequence, current = new AbortController(); downloadController = current;
  const valid = () => !disposed && ticket === sequence && props.modelValue && entry.value?.id === item.id && entry.value?.version === item.version;
  try { const blob = await informationApi.content(item.id, item.version, true, current.signal); if (!valid()) return; const url = URL.createObjectURL(blob), link = document.createElement("a"); downloadURLs.add(url); link.href = url; link.download = item.file.name; link.click(); setTimeout(() => { if (downloadURLs.delete(url)) URL.revokeObjectURL(url); }, 1000); }
  catch (value) { if (valid() && !current.signal.aborted) error.value = value instanceof Error ? value.message : String(value); } finally { if (valid()) downloading.value = false; }
}
onBeforeUnmount(() => { disposed = true; sequence++; controller?.abort(); downloadController?.abort(); clear(); for (const url of downloadURLs) URL.revokeObjectURL(url); downloadURLs.clear(); });
</script>
<template>
  <el-dialog :model-value="modelValue" :title="entry?.name || t('information.preview')" :width="kind === 'office' ? 'calc(100vw - 32px)' : 'min(1120px, calc(100vw - 32px))'" class="information-entry-dialog" :class="{ 'information-entry-office-dialog': kind === 'office' }" :top="kind === 'office' ? '16px' : '5dvh'" append-to-body :close-on-click-modal="false" @update:model-value="emit('update:modelValue', $event)">
    <template #header><div class="entry-title"><Icon :icon="previewIcon" aria-hidden="true" /><span>{{ entry?.name || t('information.preview') }}</span></div></template>
    <div v-if="entry" class="entry-meta"><span>{{ t('information.shared') }}</span><span>{{ t('information.version', { n: entry.version }) }}</span><span>{{ t(`information.${entry.kind}`) }}</span></div>
    <p v-if="loading" class="entry-state" role="status">{{ t('information.loading') }}</p><p v-if="error" class="entry-error" role="alert">{{ error }} <el-button text @click="load">{{ t('information.retry') }}</el-button></p>
    <div v-if="entry" class="entry-body" :class="{ 'entry-office-body': kind === 'office' }" data-testid="information-entry-preview">
      <InformationShortcutPreview v-if="entry.shortcut" :entry="entry" />
      <template v-else-if="entry.file">
        <template v-if="kind === 'office'">
          <InformationOfficeViewer v-if="officePreview" :key="officePreview.key" ref="officeViewer" :entry-id="officePreview.entryId" :version="officePreview.version" mode="preview" @error="officePreview.onError">
            <template #error><el-button @click="retryOffice">{{ t('information.retry') }}</el-button></template>
          </InformationOfficeViewer>
          <p v-else-if="officeRetrying" class="entry-state" role="status">{{ t('informationOffice.loading') }}</p>
        </template>
        <img v-else-if="kind === 'image' && blobURL" :src="blobURL" :alt="entry.name" />
        <iframe v-else-if="kind === 'pdf' && blobURL" :src="blobURL" :title="t('information.pdfPreview')" />
        <CodeBlock v-else-if="kind === 'text'" :code="text" :lang="language" line-numbers uncollapsed preserve-trailing-line />
        <p v-else-if="kind === 'large'">{{ t('information.previewTooLarge') }}</p>
        <template v-else-if="kind === 'unsupported'"><p>{{ t('information.fileUnsupported') }}</p><p v-if="/\.(?:docx?|xlsx?|pptx?)$/i.test(entry.file.name)">{{ t('information.officeHint') }}</p></template>
      </template>
      <p v-else>{{ t('information.folderHint') }}</p>
    </div>
    <template #footer><el-button v-if="entry?.file" :loading="downloading" @click="download"><el-icon><Download /></el-icon>{{ t('information.download') }}</el-button><el-button @click="emit('update:modelValue', false)">{{ t('information.close') }}</el-button></template>
  </el-dialog>
</template>
<style scoped>.entry-title{display:flex;align-items:center;gap:10px;padding-right:24px;min-width:0;font-size:17px;font-weight:600;line-height:1.5}.entry-title>svg{width:26px;height:26px;flex:none}.entry-title>span{overflow-wrap:anywhere;min-width:0}.entry-meta{display:flex;gap:8px;flex-wrap:wrap;margin-bottom:20px;color:var(--el-text-color-secondary);font-size:11px}.entry-meta>span{padding:3px 7px;background:var(--el-fill-color-light);border-radius:5px}.entry-state{padding:30px;text-align:center;font-size:13px;color:var(--el-text-color-secondary)}.entry-error{color:var(--el-color-danger);font-size:12px;overflow-wrap:anywhere}.entry-body{max-height:68dvh;overflow:auto;font-size:13px;line-height:1.8}.entry-body>p{color:var(--el-text-color-secondary)}.entry-body>img{max-width:100%;height:auto}.entry-body>iframe{height:67dvh;width:100%;border:0}.entry-body :deep(.shortcut-details-body){max-height:60dvh}</style>
<style scoped>
.entry-body.entry-office-body{display:flex;flex:1;min-height:0;max-height:none;overflow:hidden;border:1px solid var(--el-border-color-lighter);border-radius:8px}
.entry-office-body>.entry-state{margin:auto}
</style>
<style>
.information-entry-dialog{max-height:90dvh}.information-entry-dialog .el-dialog__body{min-height:0;overflow:auto}
.information-entry-office-dialog{height:calc(100dvh - 32px);max-height:calc(100dvh - 32px);display:flex;flex-direction:column;margin-bottom:16px}
.information-entry-office-dialog .el-dialog__header,.information-entry-office-dialog .el-dialog__footer{flex:none}
.information-entry-office-dialog .el-dialog__body{flex:1;display:flex;flex-direction:column;overflow:hidden}
.information-entry-office-dialog .entry-meta{flex:none;margin-bottom:10px}
@media(max-width:650px){.information-entry-office-dialog{padding:12px}.information-entry-office-dialog .entry-title{font-size:14px}}
</style>
