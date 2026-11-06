<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { ElMessage } from "element-plus";
import { useWebServicePreview } from "@/agent/useWebServicePreview";
import { webServiceStorageWarningKey } from "@/api/webServices";
const props = defineProps<{ sessionId: string; serviceId: string; active: boolean; openRequest?: number; toolbarTarget?: string }>();
const emit = defineEmits<{ openBrowser: [request: { url: string; title: string; sessionId: string; serviceId: string }] }>();
const { t } = useI18n();
const { record, grant, viewVersion, issue, closeIssue, loading, closing, stale, now, refresh, open, prepareClose } = useWebServicePreview(() => props);
const canOpen = computed(() => record.value?.state === "ready" && Date.parse(record.value.lease_expires_at) > now.value && !loading.value && !closing.value && !stale.value);
const failureMessage = computed(() => closeIssue.value || (record.value && ["failed", "interrupted", "degraded"].includes(record.value.state) ? record.value.last_error?.message : "") || issue.value);
const remaining = (stamp: string) => {
  const clock = Math.max(now.value, Date.now());
  return Date.parse(stamp) > clock ? t("webServices.remaining", { minutes: Math.ceil((Date.parse(stamp) - clock) / 60000) }) : t("webServices.expired");
};
async function copyAddress() {
  try { if (grant.value) { await navigator.clipboard.writeText(grant.value.preview_url); ElMessage.success(t("websiteSettings.copied")); } }
  catch { ElMessage.error(t("websiteSettings.copyFailed")); }
}
async function reload() {
  if (canOpen.value) await open(); else await refresh();
}
async function openBrowser() {
  const owner = props.sessionId, service = props.serviceId;
  if (!grant.value) await open();
  if (owner !== props.sessionId || service !== props.serviceId || !props.active || !grant.value || !canOpen.value) return;
  emit("openBrowser", { url: grant.value.preview_url, title: record.value?.name || t("webServices.previewTitle"), sessionId: owner, serviceId: service });
}
defineExpose({ prepareClose });
</script>
<template>
  <section class="website-pane" data-testid="website-preview">
    <Teleport defer :to="toolbarTarget || 'body'" :disabled="!toolbarTarget">
      <header v-if="!toolbarTarget || active" class="website-toolbar" :class="{ 'in-tabstrip': toolbarTarget }">
        <strong v-if="!toolbarTarget" class="website-title">{{ record?.name || t('webServices.previewTitle') }}</strong>
        <span v-if="record && record.state !== 'ready'" class="website-status" :class="record.state" role="status">{{ t('webServices.state_' + record.state) }}</span>
        <span v-if="record" class="website-deadline" :title="`${t('webServices.serviceLease')} · ${record.lease_expires_at}\n${t('webServices.closeHint')}`" :aria-label="`${t('webServices.serviceLease')} · ${remaining(record.lease_expires_at)}`" tabindex="0">
          <svg width="14" height="14" viewBox="0 0 20 20" fill="none" aria-hidden="true"><circle cx="10" cy="10" r="7" stroke="currentColor" stroke-width="1.4"/><path d="M10 6v4l3 2" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/></svg>
          <span class="deadline-label">{{ remaining(record.lease_expires_at) }}</span>
        </span>
        <button type="button" :disabled="loading || closing" :title="t(canOpen ? (grant ? 'webServices.reopen' : 'webServices.open') : 'webServices.refresh')" :aria-label="t(canOpen ? (grant ? 'webServices.reopen' : 'webServices.open') : 'webServices.refresh')" @click="reload">
          <svg width="16" height="16" viewBox="0 0 20 20" fill="none" aria-hidden="true"><path d="M16 8a6.2 6.2 0 1 0 0 5M16 3v5h-5" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"/></svg>
        </button>
        <a v-if="grant" class="website-link" :href="grant.preview_url" target="_blank" rel="noopener noreferrer" :title="grant.preview_url" :aria-label="t('webServices.open')">
          <svg width="16" height="16" viewBox="0 0 20 20" fill="none" aria-hidden="true"><path d="M11 3h6v6m-1-5-7 7M8 4H5a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h9a2 2 0 0 0 2-2v-3" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"/></svg>
        </a>
        <button v-if="grant" type="button" :title="t('websiteSettings.copyAddress')" :aria-label="t('websiteSettings.copyAddress')" @click="copyAddress">
          <svg width="15" height="15" viewBox="0 0 20 20" fill="none" aria-hidden="true"><rect x="6" y="6" width="11" height="11" rx="2" stroke="currentColor" stroke-width="1.4"/><path d="M13 6V4a1 1 0 0 0-1-1H4a1 1 0 0 0-1 1v8a1 1 0 0 0 1 1h2" stroke="currentColor" stroke-width="1.4"/></svg>
        </button>
        <button type="button" class="native-open" data-testid="website-open-browser" :disabled="!canOpen" :title="t('filePreview.htmlOpenBrowser')" :aria-label="t('filePreview.htmlOpenBrowser')" @click="openBrowser">
          <svg width="16" height="16" viewBox="0 0 20 20" fill="none" aria-hidden="true"><rect x="2" y="3" width="16" height="12" rx="2" stroke="currentColor" stroke-width="1.4"/><path d="M7 18h6m-3-3v3M2 7h16" stroke="currentColor" stroke-width="1.4"/></svg>
          <span class="control-label">{{ t('filePreview.htmlOpenBrowser') }}</span>
        </button>
        <slot name="actions" :record="record" />
      </header>
    </Teleport>
    <p v-if="failureMessage" class="website-preview-error" role="alert">{{ failureMessage }}</p>
    <p v-if="record?.storage_warning" class="website-storage-warning" role="status" data-testid="website-storage-warning">{{ t(webServiceStorageWarningKey(record)) }}</p>
    <p v-if="closing" class="website-preview-notice" role="status">{{ t('webServices.closing') }}</p>
    <p v-if="!grant && !failureMessage" class="website-preview-empty">{{ t(loading ? 'webServices.loading' : record && record.state !== 'ready' ? 'webServices.previewUnavailable' : 'webServices.previewIdle') }}</p>
    <p v-if="grant && record?.state !== 'ready'" class="website-preview-notice" role="status">{{ t('websiteSettings.updating') }}</p>
    <iframe v-if="grant && record?.state === 'ready'" :key="`${grant.grant_id}:${record.generation}:${viewVersion}`" :src="grant.preview_url" :title="record?.name || t('webServices.previewTitle')" class="website-frame" sandbox="allow-scripts allow-forms" referrerpolicy="no-referrer" />
  </section>
</template>
<style scoped>
.website-pane{display:flex;flex-direction:column;height:100%;min-height:0;width:100%;background:var(--el-bg-color);color:var(--el-text-color-regular)}
.website-toolbar{display:flex;align-items:center;gap:4px;padding:6px 10px;min-width:0;flex:0 0 auto;border-bottom:1px solid var(--el-border-color-light);background:var(--el-bg-color-page)}
.website-toolbar.in-tabstrip{padding:0 2px;border:0;background:transparent;min-height:34px}
.website-status{flex-shrink:0;font-size:11px;color:var(--el-text-color-secondary)}.website-status.failed,.website-status.interrupted,.website-status.degraded{color:var(--el-color-danger)}
.website-storage-warning{margin:0;padding:8px 12px;border-bottom:1px solid var(--el-color-warning-light-5);background:var(--el-color-warning-light-9);color:var(--el-color-warning-dark-2);font-size:11px;line-height:1.6;overflow-wrap:anywhere}
.website-title{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:13px}.website-deadline{display:inline-flex;align-items:center;gap:5px;font-size:11px;color:var(--el-text-color-secondary);white-space:nowrap;padding:0 4px}
.website-toolbar button,.website-link{display:inline-flex;align-items:center;justify-content:center;gap:6px;flex-shrink:0;min-width:28px;height:28px;padding:4px;border:0;border-radius:5px;background:transparent;color:var(--el-text-color-regular);cursor:pointer;font:inherit;text-decoration:none;box-sizing:border-box}
.website-toolbar button:hover,.website-link:hover{background:var(--el-fill-color-light)}.website-toolbar button:disabled{opacity:.45;cursor:default}.website-toolbar .native-open{padding:4px 7px;color:var(--el-color-primary);font-size:11px}.control-label{white-space:nowrap}
.website-preview-notice,.website-preview-error{padding:6px 12px;margin:0}.website-preview-notice{font-size:11px;color:var(--el-text-color-secondary);line-height:1.5}.website-preview-error{font-size:12px;color:var(--el-color-danger);overflow-wrap:anywhere}.website-preview-empty{padding:24px;font-size:13px;color:var(--el-text-color-secondary);line-height:1.7}.website-frame{display:block;border:0;width:100%;min-height:0;flex:1;background:white}
@container preview-toolbar (max-width:700px){.in-tabstrip .control-label,.in-tabstrip .deadline-label{display:none}.website-toolbar.in-tabstrip{gap:1px}.website-toolbar.in-tabstrip .native-open{padding:4px}.in-tabstrip .website-deadline{padding:0 3px}}
</style>
