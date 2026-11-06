<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useWebServices } from "@/agent/useWebServices";
import { useWebsitePopoverDismiss } from "@/agent/useWebsitePopoverDismiss";
import { MAX_WEBSITE_SECONDS, webServiceRecoveryIssue, webServiceRunning, webServiceStorageWarningKey, type DeletedWebService, type OpenWebServiceRequest, type WebServiceChange, type WebServiceRecord } from "@/api/webServices";
import WebsiteDurationInput from "./WebsiteDurationInput.vue";

const props = defineProps<{ sessionId: string | null; displaySessionId: string | null; disabled?: boolean; connected: boolean; change?: WebServiceChange | null }>();
const emit = defineEmits<{ open: [request: OpenWebServiceRequest]; deleted: [request: DeletedWebService] }>();
const { t } = useI18n();
const editingName = ref<string | null>(null), nameDraft = ref(""), renewing = ref<string | null>(null), duration = ref(7200), confirmingDelete = ref<string | null>(null);
const state = useWebServices(() => ({ sessionId: props.sessionId, enabled: !!props.sessionId && props.sessionId === props.displaySessionId && !props.disabled, connected: props.connected, change: props.change }), request => {
  if (editingName.value === request.serviceId) editingName.value = null;
  if (renewing.value === request.serviceId) renewing.value = null;
  if (confirmingDelete.value === request.serviceId) confirmingDelete.value = null;
  emit("deleted", request);
});
const { records, quota, defaultLeaseSeconds, error, loading, stale, expanded, selected, log, logError, logLoading, busy, operations, actionErrors, deleting, deleteErrors, now, visible } = state;
watch(() => props.sessionId, () => { editingName.value = null; renewing.value = null; confirmingDelete.value = null; });
const durationValid = computed(() => Number.isSafeInteger(duration.value) && duration.value > 0 && duration.value <= MAX_WEBSITE_SECONDS);
const newDeadline = computed(() => new Date(now.value + duration.value * 1000).toLocaleString());
const quotaNotice = computed(() => {
  for (const scope of ["session", "user", "total"] as const) {
    const value = quota.value?.[scope];
    if (value && value.current >= value.limit) return t(`websiteSettings.quota_${scope}`, value);
  }
  return "";
});
const isDeleting = (record: WebServiceRecord) => !!deleting.value[record.service_id] || record.phase === "deleting";
function beginRename(record: WebServiceRecord) { confirmingDelete.value = null; editingName.value = record.service_id; nameDraft.value = operations.value[record.service_id]?.displayName || record.name; }
function beginRenew(record: WebServiceRecord) { confirmingDelete.value = null; renewing.value = record.service_id; duration.value = operations.value[record.service_id]?.leaseSeconds || defaultLeaseSeconds.value; }
function beginDelete(record: WebServiceRecord) { editingName.value = null; renewing.value = null; confirmingDelete.value = record.service_id; }
async function saveName(record: WebServiceRecord) { await state.action(record, "rename", nameDraft.value); if (!actionErrors.value[record.service_id]) editingName.value = null; }
async function renew(record: WebServiceRecord) { if (durationValid.value) { await state.action(record, "renew", duration.value); if (!actionErrors.value[record.service_id]) renewing.value = null; } }
const accessButton = ref<HTMLElement | null>(null), cardsPanel = ref<HTMLElement | null>(null);
useWebsitePopoverDismiss(expanded, accessButton, cardsPanel);
const ready = computed(() => records.value.filter(r => r.state === "ready" && Date.parse(r.lease_expires_at) > now.value).length);
const ordered = computed(() => [...records.value].sort((a, b) => Number(webServiceRunning(b)) - Number(webServiceRunning(a)) || a.name.localeCompare(b.name)));
const alive = (r: WebServiceRecord) => Date.parse(r.lease_expires_at) > now.value;
const remaining = (value: string) => {
  const clock = Math.max(now.value, Date.now());
  return Date.parse(value) > clock ? t("webServices.remaining", { minutes: Math.ceil((Date.parse(value) - clock) / 60000) }) : t("webServices.expired");
};
function open(record: WebServiceRecord) {
  if (!props.sessionId || record.state !== "ready" || !alive(record) || stale.value || isDeleting(record)) return;
  expanded.value = false;
  emit("open", { sessionId: props.sessionId, serviceId: record.service_id, label: record.name, open: true });
}
defineExpose({ reveal: state.reveal, readyFor: state.readyFor });
</script>

<template>
      <button v-if="visible" ref="accessButton" type="button" class="website-access" :title="t('webServices.view')" :aria-label="t('webServices.view')" :aria-expanded="expanded" aria-haspopup="dialog" data-testid="website-access" @click="expanded = !expanded">
        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true"><circle cx="8" cy="8" r="6" stroke="currentColor"/><path d="M2 8h12M8 2c3 3 3 9 0 12C5 11 5 5 8 2Z" stroke="currentColor"/></svg>
        <span>{{ t('webServices.label') }}</span><span>{{ records.length }}</span><span v-if="ready" class="website-ready-count">· {{ t('webServices.readyCount', { count: ready }) }}</span>
      </button>
  <el-popover v-if="visible" :visible="expanded" virtual-triggering :virtual-ref="accessButton" role="dialog" placement="top-end" :width="420" popper-class="managed-web-services-popover" @show="state.refresh">
    <section ref="cardsPanel" class="website-list" data-testid="website-cards">
      <header class="website-list-head"><strong>{{ t('webServices.heading') }}</strong><button type="button" class="website-action" :disabled="loading" @click="state.refresh">{{ t('webServices.refresh') }}</button></header>
      <p v-if="error" class="website-error" role="alert">{{ stale ? t('webServices.stale') : error }}</p>
      <p v-if="quotaNotice" class="website-quota-notice" role="status">{{ quotaNotice }}</p>
      <p v-if="!records.length" class="website-hint">{{ t(loading ? 'webServices.loading' : 'webServices.empty') }}</p>
      <div class="website-card-stack">
        <article v-for="record in ordered" :key="record.service_id" class="website-card" :class="{ selected: selected === record.service_id }" :data-service-id="record.service_id">
          <div class="website-card-heading"><strong :title="record.name">{{ record.name }}</strong><button type="button" class="website-rename" :disabled="!!busy[record.service_id] || isDeleting(record)" :aria-label="t('websiteSettings.renameTitle')" @click="beginRename(record)">{{ t('websiteSettings.rename') }}</button><span class="website-generation">{{ t('webServices.generation', { generation: record.generation }) }}</span><span class="website-state" :class="record.state" role="status">{{ t(isDeleting(record) ? 'webServices.deleting' : 'webServices.state_' + record.state) }}</span></div>
          <form v-if="editingName === record.service_id && !isDeleting(record)" class="website-inline-form" @submit.prevent="saveName(record)">
            <label :for="`website-name-${record.service_id}`">{{ t('websiteSettings.renameTitle') }}</label>
            <input :id="`website-name-${record.service_id}`" v-model="nameDraft" :disabled="!!busy[record.service_id] || operations[record.service_id]?.action === 'rename'" :placeholder="t('websiteSettings.namePlaceholder')" required />
            <p class="website-hint">{{ t('websiteSettings.nameHint') }}</p><div class="website-actions"><button class="website-primary" type="submit" :disabled="!nameDraft.trim() || [...nameDraft.trim()].length > 80 || !!busy[record.service_id]">{{ t('websiteSettings.saveName') }}</button><button class="website-action" type="button" @click="editingName = null">{{ t('websiteSettings.cancel') }}</button></div>
          </form>
          <p class="website-project" :title="record.project_root">{{ record.project_root }} · {{ record.profile }}</p>
          <p class="website-lease" :title="record.lease_expires_at">{{ t('webServices.serviceLease') }} · {{ remaining(record.lease_expires_at) }}</p>
          <p v-if="record.last_error" class="website-error">{{ record.last_error.message }}</p>
          <p v-if="record.storage_warning" class="website-storage-warning" role="status" data-testid="website-storage-warning">{{ t(webServiceStorageWarningKey(record)) }}</p>
          <div class="website-actions">
            <button type="button" class="website-primary" :disabled="record.state !== 'ready' || !alive(record) || stale || !!busy[record.service_id] || isDeleting(record)" @click="open(record)">{{ t('webServices.open') }}</button>
            <button type="button" class="website-action" :disabled="!!busy[record.service_id] || isDeleting(record) || !alive(record) || ['stopping', 'reconciling'].includes(record.state) || !webServiceRunning(record)" @click="beginRenew(record)">{{ t('webServices.renew') }}</button>
            <button v-if="alive(record)" type="button" class="website-action" :disabled="!!busy[record.service_id] || isDeleting(record) || ['preparing', 'starting', 'stopping', 'reconciling'].includes(record.state)" @click="state.action(record, 'restart')">{{ t('webServices.restart') }}</button>
            <button v-else type="button" class="website-action" :disabled="!!busy[record.service_id] || isDeleting(record) || stale || !!webServiceRecoveryIssue(record)" @click="state.action(record, 'start')">{{ t('webServices.startAgain') }}</button>
            <button type="button" class="website-action website-stop" :disabled="!!busy[record.service_id] || isDeleting(record) || !webServiceRunning(record) || record.state === 'stopping'" @click="state.action(record, 'stop')">{{ t('webServices.stop') }}</button>
            <button type="button" class="website-action" :disabled="isDeleting(record) || (logLoading && selected === record.service_id)" :aria-expanded="selected === record.service_id && !!log" @click="state.readLog(record)">{{ t('webServices.logs') }}</button>
            <button type="button" class="website-action website-delete" :disabled="!!busy[record.service_id]" data-testid="website-delete" @click="beginDelete(record)">{{ t('webServices.delete') }}</button>
          </div>
          <form v-if="confirmingDelete === record.service_id" class="website-delete-confirmation" data-testid="website-delete-confirmation" :aria-label="t('webServices.deleteTitle', { name: record.name })" @submit.prevent="state.remove(record)">
            <strong>{{ t('webServices.deleteTitle', { name: record.name }) }}</strong>
            <p>{{ t('webServices.deleteHint') }}</p>
            <p v-if="deleteErrors[record.service_id]" class="website-error" role="alert">{{ deleteErrors[record.service_id] }}</p>
            <div class="website-actions"><button class="website-action website-delete" type="submit" :disabled="!!busy[record.service_id]" data-testid="website-delete-confirm">{{ t(busy[record.service_id] === 'delete' ? 'webServices.deleting' : 'webServices.confirmDelete') }}</button><button class="website-action" type="button" :disabled="!!busy[record.service_id]" @click="confirmingDelete = null">{{ t(isDeleting(record) ? 'webServices.closeDelete' : 'websiteSettings.cancel') }}</button></div>
          </form>
          <p v-else-if="deleteErrors[record.service_id]" class="website-error" role="alert">{{ deleteErrors[record.service_id] }}</p>
          <form v-if="renewing === record.service_id && !isDeleting(record)" class="website-inline-form" @submit.prevent="renew(record)">
            <label>{{ t('websiteSettings.renewTitle') }}</label><WebsiteDurationInput v-model="duration" :disabled="!!busy[record.service_id] || operations[record.service_id]?.action === 'renew'" />
            <p class="website-hint">{{ t('websiteSettings.renewHint') }}</p><p v-if="durationValid" class="website-hint">{{ t('websiteSettings.deadline', { time: newDeadline }) }}</p>
            <div class="website-actions"><button class="website-primary" type="submit" :disabled="!durationValid || !!busy[record.service_id]">{{ t('websiteSettings.renewSubmit') }}</button><button class="website-action" type="button" @click="renewing = null">{{ t('websiteSettings.cancel') }}</button></div>
          </form>
          <p v-if="!alive(record) && webServiceRecoveryIssue(record)" class="website-hint" role="status">{{ t(webServiceRecoveryIssue(record)) }}</p>
          <p v-if="operations[record.service_id] && !actionErrors[record.service_id] && !isDeleting(record)" class="website-hint" role="status">{{ t('webServices.pending') }}</p>
          <p v-if="actionErrors[record.service_id] && !isDeleting(record)" class="website-error" role="alert">{{ actionErrors[record.service_id] }} <button v-if="operations[record.service_id]" type="button" class="website-action" :disabled="!!busy[record.service_id]" @click="state.action(record, operations[record.service_id]!.action)">{{ t('webServices.retryRequest') }}</button></p>
          <section v-if="selected === record.service_id && (log || logError || logLoading)" class="website-log" :aria-label="t('webServices.logs')">
            <p v-if="logError" class="website-error" role="alert">{{ logError }}</p>
            <pre tabindex="0">{{ logLoading ? t('webServices.loading') : log?.text || t('webServices.noOutput') }}</pre>
            <p v-if="log?.truncated" class="website-hint">{{ t('webServices.logTruncated') }}</p>
            <button v-if="log && !log.eof && log.next_cursor" type="button" class="website-action" :disabled="logLoading" @click="state.readLog(record, true)">{{ t('webServices.nextPage') }}</button>
          </section>
        </article>
      </div>
      <p class="website-hint">{{ t('webServices.sourceHint') }}</p><p class="website-hint">{{ t('webServices.previewHint') }} {{ t('webServices.grantsIndependent') }}</p>
    </section>
  </el-popover>
</template>

<style scoped>
.website-access{display:inline-flex;align-items:center;gap:5px;height:30px;padding:0 9px;border:1px solid var(--el-border-color-lighter);border-radius:8px;background:var(--el-bg-color);color:var(--el-text-color-secondary);cursor:pointer;font:inherit;font-size:12px;white-space:nowrap;box-shadow:0 2px 8px rgb(0 0 0 / 8%)}
.website-list{color:var(--el-text-color-regular);font-size:12px;min-width:0}.website-list-head,.website-card-heading{display:flex;align-items:center;justify-content:space-between;gap:8px}.website-card-heading strong{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:13px}.website-card-stack{max-height:min(56vh,540px);overflow:auto;overscroll-behavior:contain;margin:10px 0;display:grid;gap:8px}.website-card{padding:12px;border:1px solid var(--el-border-color-light);border-radius:10px;background:var(--el-fill-color-extra-light)}.website-card.selected{border-color:var(--el-color-primary)}.website-project{color:var(--el-text-color-secondary);overflow:hidden;text-overflow:ellipsis;white-space:nowrap;margin:6px 0}.website-lease{font-variant-numeric:tabular-nums;margin:8px 0}.website-state{flex-shrink:0;color:var(--el-text-color-secondary);font-size:11px}.website-state.ready{color:var(--el-color-success)}.website-state.failed,.website-state.degraded{color:var(--el-color-danger)}.website-actions{display:flex;flex-wrap:wrap;gap:6px}.website-action,.website-primary{border:1px solid var(--el-border-color-light);border-radius:6px;padding:4px 7px;background:var(--el-bg-color);color:var(--el-text-color-regular);font:inherit;cursor:pointer}.website-primary{color:var(--el-color-primary);border-color:var(--el-color-primary-light-5)}.website-stop{color:var(--el-color-danger)}button:disabled{opacity:.45;cursor:default}.website-hint{font-size:11px;line-height:1.5;color:var(--el-text-color-secondary);margin:8px 0 0}.website-error{color:var(--el-color-danger);overflow-wrap:anywhere;line-height:1.5;margin:8px 0}.website-log{margin-top:10px;padding:8px;border:1px solid var(--el-border-color-light);border-radius:7px;background:var(--el-fill-color-light)}.website-log pre{margin:0;max-height:220px;overflow:auto;white-space:pre-wrap;overflow-wrap:anywhere;font:11px/1.5 var(--font-mono,monospace)}
.website-card-heading strong{flex:1;min-width:0}.website-generation{flex-shrink:0;font-size:11px;color:var(--el-text-color-secondary)}
.website-storage-warning{margin:8px 0;padding:8px 9px;border:1px solid var(--el-color-warning-light-5);border-radius:7px;background:var(--el-color-warning-light-9);color:var(--el-color-warning-dark-2);font-size:11px;line-height:1.6;overflow-wrap:anywhere}
.website-rename{border:0;background:transparent;color:var(--el-color-primary);font:inherit;font-size:11px;cursor:pointer;flex-shrink:0}.website-inline-form{display:grid;gap:8px;margin:10px 0;padding:10px;border:1px solid var(--el-border-color-light);border-radius:7px;background:var(--el-bg-color)}.website-inline-form input{box-sizing:border-box;width:100%;border:1px solid var(--el-border-color);border-radius:5px;padding:7px;background:var(--el-bg-color);color:var(--el-text-color-primary);font:inherit}.website-inline-form label{font-weight:600}.website-quota-notice{padding:9px;border:1px solid var(--el-color-warning-light-5);background:var(--el-color-warning-light-9);border-radius:7px;line-height:1.7}
.website-delete{color:var(--el-color-danger);border-color:var(--el-color-danger-light-5)}.website-delete-confirmation{display:grid;gap:8px;margin:10px 0 0;padding:10px;border:1px solid var(--el-color-danger-light-5);border-radius:7px;background:var(--el-color-danger-light-9);line-height:1.6;overflow-wrap:anywhere}.website-delete-confirmation p{margin:0}.website-delete-confirmation .website-error{margin:0}
</style>
<style>.managed-web-services-popover{max-width:calc(100vw - 24px);box-sizing:border-box}@media(max-width:600px){.website-ready-count{display:none}}</style>
