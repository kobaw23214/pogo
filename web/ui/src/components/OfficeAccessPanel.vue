<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { ElMessageBox } from "element-plus";
import { officeApi, type OfficeAccess, type OfficeOpen, type OfficeRecoveryCopy } from "@/agent/officeTransport";
import { authHeaders } from "@/api/client";
import { randomUUID } from "@/utils/uuid";
const props = defineProps<{ path: string; conversationId: string }>();
const emit = defineEmits<{ edit: []; claimed: [value: { claim: OfficeOpen; instanceId: string }]; refresh: []; openCopy: [path: string] }>();
const { t } = useI18n();
const access = ref<OfficeAccess | null>(null);
const failure = ref("");
const busy = ref(false);
const checking = ref(false);
const takeoverUnconfirmed = ref(false);
const resetDialogOpen = ref(false);
const resetAcknowledged = ref(false);
const resetInterruptAcknowledged = ref(false);
const resetDialogAccess = ref<OfficeAccess | null>(null);
const resetUnconfirmed = ref(false);
let resetAttempt: { access: OfficeAccess; instanceId: string; generation: number; path: string; conversationId: string; allowInterruptOnline: boolean } | null = null;
const resetRequiresOnlineConfirmation = computed(() => !!resetDialogAccess.value?.access.resetRequiresOnlineConfirmation);
const canConfirmReset = computed(() => resetAcknowledged.value && (!resetRequiresOnlineConfirmation.value || resetInterruptAcknowledged.value));
const resetTitle = computed(() => resetDialogAccess.value?.writer?.title || props.path.split("/").pop() || props.path);
const resetConnection = computed(() => t(`officeEditor.access.resetConnection.${resetDialogAccess.value?.access.state || "unconfirmed"}`));
const resetLastSaved = computed(() => {
  const value = resetDialogAccess.value?.writer?.savedAt;
  return value ? new Date(value).toLocaleString() : t("officeEditor.access.resetSaveUnknown");
});
let takeoverAttempt: { access: OfficeAccess; instanceId: string; generation: number; path: string; conversationId: string } | null = null;
let savedCopyAttempt: { version: string; operationId: string } | null = null;
let generation = 0, stopped = false;
let controller: AbortController | null = null;
let timer: ReturnType<typeof setTimeout> | undefined;
const state = computed(() => access.value?.access.state || "unconfirmed");
const copies = computed(() => access.value?.recoveryCopies.filter(copy => copy.state === "ready") || []);
const pending = computed(() => access.value?.writer?.recoveryCopy?.state === "pending");
const description = computed(() => t(`officeEditor.access.${access.value?.writer?.recoverySealed ? pending.value ? "recovering" : "sealed" : access.value?.writer?.dirty && state.value !== "online" ? "unsavedOffline" : state.value}`));
function cancelReset() {
  if (busy.value) return;
  resetDialogOpen.value = false;
  resetDialogAccess.value = null;
  resetAcknowledged.value = false;
  resetInterruptAcknowledged.value = false;
}
function showReset() {
  const current = access.value;
  if (busy.value || checking.value || !current?.writer || !current.access.canReset || resetUnconfirmed.value || takeoverUnconfirmed.value) return;
  resetDialogAccess.value = JSON.parse(JSON.stringify(current)) as OfficeAccess;
  resetAcknowledged.value = false;
  resetInterruptAcknowledged.value = false;
  resetDialogOpen.value = true;
}
async function clearOccupancy() {
  if (busy.value || takeoverUnconfirmed.value) return;
  const current = resetDialogAccess.value;
  if (!resetAttempt && (!resetDialogOpen.value || !canConfirmReset.value || !current?.writer || !current.access.canReset)) return;
  const epoch = generation;
  const attempt = resetAttempt || { access: current!, instanceId: randomUUID(), generation: epoch, path: props.path, conversationId: props.conversationId, allowInterruptOnline: resetRequiresOnlineConfirmation.value && resetInterruptAcknowledged.value };
  if (attempt.generation !== epoch) return;
  resetAttempt = attempt;
  busy.value = true; failure.value = ""; resetDialogOpen.value = false;
  try {
    const claim = await officeApi.reset(attempt.path, attempt.conversationId, attempt.instanceId, attempt.access, attempt.allowInterruptOnline);
    if (stopped || epoch !== generation) { await officeApi.release(claim, attempt.instanceId).catch(() => {}); return; }
    resetAttempt = null; resetUnconfirmed.value = false; resetDialogAccess.value = null;
    resetAcknowledged.value = false; resetInterruptAcknowledged.value = false;
    emit("claimed", { claim, instanceId: attempt.instanceId });
  } catch (error) {
    if (!stopped && epoch === generation) {
      const status = (error as { status?: number })?.status;
      resetUnconfirmed.value = !status || [408, 429, 500, 502, 503, 504].includes(status);
      if (!resetUnconfirmed.value) { resetAttempt = null; resetDialogAccess.value = null; resetAcknowledged.value = false; resetInterruptAcknowledged.value = false; }
      const message = resetUnconfirmed.value ? t("officeEditor.access.resetUnknown") : status === 409 ? t("officeEditor.access.resetChanged") : error instanceof Error ? error.message : String(error);
      await refresh(); if (!stopped && epoch === generation) failure.value = message;
    }
  } finally { if (!stopped && epoch === generation) busy.value = false; }
}
function schedule() { clearTimeout(timer); if (!stopped) timer = setTimeout(() => void refresh(), pending.value ? 2000 : 10_000); }
async function refresh() {
  if (stopped || checking.value) return;
  checking.value = true;
  const epoch = generation, path = props.path, conversationId = props.conversationId;
  const abort = new AbortController(); controller = abort;
  try {
    const value = await officeApi.access(path, conversationId, abort.signal);
    if (stopped || generation !== epoch) return;
    if (!value.ok || !value.fileRef || !value.path || !value.access) throw new Error(t("officeEditor.access.checkFailed"));
    access.value = value; failure.value = resetUnconfirmed.value ? t("officeEditor.access.resetUnknown") : "";
  } catch (error) { if (!stopped && generation === epoch && !abort.signal.aborted) failure.value = error instanceof Error ? error.message : String(error); }
  finally { if (!stopped && generation === epoch) { checking.value = false; controller = null; schedule(); } }
}
async function recover(readForgotten = false) {
  if (busy.value || resetUnconfirmed.value || !access.value?.writer || !(readForgotten ? access.value.access.canReadForgotten : access.value.access.canRecover)) return;
  busy.value = true; failure.value = "";
  const epoch = generation, current = access.value;
  try {
    if (!readForgotten) await ElMessageBox.confirm(t("officeEditor.access.recoverConfirm"), t("officeEditor.access.recover"), { confirmButtonText: t("officeEditor.access.recover"), cancelButtonText: t("common.cancel"), type: "warning" });
    if (stopped || epoch !== generation) return;
    await officeApi.recover(current.writer!, current.writer!.canvasVersion || "", undefined, readForgotten);
    if (!stopped && epoch === generation) await refresh();
  } catch (error) { if (!stopped && epoch === generation && error !== "cancel" && error !== "close") failure.value = error instanceof Error ? error.message : String(error); }
  finally { if (!stopped && epoch === generation) busy.value = false; }
}
async function edit() {
  const current = access.value;
  if (busy.value || resetUnconfirmed.value || !current) return;
  if (!takeoverAttempt && !current.writer) { emit("edit"); return; }
  if (!takeoverAttempt && !current.access.canTakeover) return;
  busy.value = true; failure.value = "";
  const epoch = generation, attempt = takeoverAttempt || { access: current, instanceId: randomUUID(), generation: epoch, path: props.path, conversationId: props.conversationId };
  const { path, conversationId, instanceId } = attempt;
  let claim: OfficeOpen | null = null;
  try {
    if (!takeoverAttempt && current.writer?.dirty) {
      await ElMessageBox.confirm(t("officeEditor.access.takeoverConfirm"), t("officeEditor.access.takeover"), { confirmButtonText: t("officeEditor.access.takeover"), cancelButtonText: t("common.cancel"), type: "warning" });
      if (stopped || epoch !== generation) return;
    }
    takeoverAttempt = attempt;
    claim = await officeApi.takeover(path, conversationId, instanceId, attempt.access);
    if (stopped || epoch !== generation) { await officeApi.release(claim, instanceId).catch(() => {}); return; }
    takeoverAttempt = null; takeoverUnconfirmed.value = false;
    emit("claimed", { claim, instanceId });
  } catch (error) {
    if (!stopped && epoch === generation && error !== "cancel" && error !== "close") {
      const status = (error as { status?: number })?.status;
      takeoverUnconfirmed.value = !!takeoverAttempt && (!status || [408, 429, 500, 502, 503, 504].includes(status));
      if (!takeoverUnconfirmed.value) takeoverAttempt = null;
      const message = takeoverUnconfirmed.value ? t("officeEditor.access.takeoverUnknown") : error instanceof Error ? error.message : String(error);
      await refresh(); if (!stopped && epoch === generation) failure.value = message;
    }
  } finally { if (!stopped && epoch === generation) busy.value = false; }
}
async function download(copy: OfficeRecoveryCopy) {
  if (!copy.downloadURL || busy.value) return;
  const url = new URL(copy.downloadURL, location.origin);
  if (url.origin !== location.origin || !/^\/api\/office\/recovery-copies\/[a-zA-Z0-9_-]+$/.test(url.pathname)) return;
  busy.value = true; const epoch = generation;
  try {
    const response = await fetch(`${url.pathname}${url.search}`, { headers: authHeaders() });
    if (!response.ok) throw new Error(t("officeEditor.access.downloadFailed"));
    const bytes = await response.blob();
    if (stopped || epoch !== generation) return;
    const objectURL = URL.createObjectURL(bytes), anchor = document.createElement("a");
    anchor.href = objectURL; anchor.download = copy.title; anchor.click();
    setTimeout(() => URL.revokeObjectURL(objectURL), 30_000);
  } catch (error) { if (!stopped && epoch === generation) failure.value = error instanceof Error ? error.message : String(error); }
  finally { if (!stopped && epoch === generation) busy.value = false; }
}
async function editSavedCopy() {
  const current = access.value;
  if (!current || busy.value || resetUnconfirmed.value || checking.value) return;
  busy.value = true; failure.value = "";
  const epoch = generation, path = props.path, conversationId = props.conversationId;
  try {
    if (!savedCopyAttempt) await ElMessageBox.confirm(t("officeEditor.access.savedCopyConfirm"), t("officeEditor.access.savedCopy"), { confirmButtonText: t("officeEditor.access.savedCopy"), cancelButtonText: t("common.cancel") });
    if (stopped || epoch !== generation) return;
    savedCopyAttempt ||= { version: current.diskVersion, operationId: randomUUID() };
    const result = await officeApi.savedCopy(path, conversationId, savedCopyAttempt.version, savedCopyAttempt.operationId);
    if (stopped || epoch !== generation) return;
    if (!result.ok || !result.path || result.path === path) throw new Error(t("officeEditor.access.checkFailed"));
    savedCopyAttempt = null; emit("openCopy", result.path);
  } catch (error) {
    if (!stopped && epoch === generation && error !== "cancel" && error !== "close") {
      const status = (error as { status?: number })?.status;
      if (status && status >= 400 && status < 500 && status !== 408 && status !== 429) savedCopyAttempt = null;
      failure.value = error instanceof Error ? error.message : String(error);
    }
  } finally { if (!stopped && epoch === generation) busy.value = false; }
}
watch([() => props.path, () => props.conversationId], () => { ++generation; controller?.abort(); clearTimeout(timer); checking.value = false; busy.value = false; access.value = null; resetDialogOpen.value = false; resetDialogAccess.value = null; resetAcknowledged.value = false; resetInterruptAcknowledged.value = false; resetUnconfirmed.value = false; resetAttempt = null; takeoverAttempt = null; savedCopyAttempt = null; takeoverUnconfirmed.value = false; void refresh(); }, { immediate: true });
onBeforeUnmount(() => { resetDialogOpen.value = false; resetDialogAccess.value = null; resetAcknowledged.value = false; resetInterruptAcknowledged.value = false; resetAttempt = null; stopped = true; ++generation; controller?.abort(); clearTimeout(timer); });
</script>

<template>
  <aside class="office-access-panel" :data-access-state="state" aria-live="polite">
    <div class="access-copy"><strong>{{ t('officeEditor.access.viewing') }}</strong><span>{{ description }}</span><small v-if="access?.writer?.savedAt">{{ t('officeEditor.access.savedAt', { time: new Date(access.writer.savedAt).toLocaleString() }) }}</small></div>
    <div class="access-actions">
      <button type="button" class="access-button" :disabled="busy || checking" @click="refresh(); emit('refresh')">{{ t('officeEditor.access.refresh') }}</button>
      <button v-if="access?.access.canRecover" type="button" class="access-button" :disabled="busy || checking || resetUnconfirmed" @click="recover(false)">{{ t('officeEditor.access.recover') }}</button>
      <button v-if="access?.access.canReadForgotten" type="button" class="access-button" :disabled="busy || checking || resetUnconfirmed" @click="recover(true)">{{ t('officeEditor.access.findRecovery') }}</button>
      <button v-if="access?.writer" type="button" class="access-button" :disabled="busy || checking || resetUnconfirmed || takeoverUnconfirmed" @click="editSavedCopy">{{ t('officeEditor.access.savedCopy') }}</button>
      <button v-if="access?.writer || resetUnconfirmed" type="button" class="access-button caution" data-testid="office-clear-occupancy" :title="!resetUnconfirmed && !access?.access.canReset ? t('officeEditor.access.resetBusy') : t('officeEditor.access.reset')" :disabled="busy || checking || takeoverUnconfirmed || (!resetUnconfirmed && !access?.access.canReset)" @click="resetUnconfirmed ? clearOccupancy() : showReset()">{{ t(resetUnconfirmed ? 'officeEditor.access.resetRetry' : 'officeEditor.access.reset') }}</button>
      <button type="button" class="access-button primary" :disabled="busy || checking || resetUnconfirmed || !access || (!takeoverUnconfirmed && !!access.writer && !access.access.canTakeover)" @click="edit">{{ t(takeoverUnconfirmed ? 'officeEditor.startup.retryAction' : access?.writer ? 'officeEditor.access.takeover' : 'officeEditor.edit') }}</button>
    </div>
    <p v-if="failure" class="access-error" role="alert">{{ failure }}</p>
    <p v-if="access?.writer?.recoveryCopy?.error" class="access-error">{{ access.writer.recoveryCopy.error }}</p>
    <details v-if="copies.length" class="recovery-copies"><summary>{{ t('officeEditor.access.copies', { count: copies.length }) }}</summary><p>{{ t('officeEditor.access.copyHelp') }}</p><button v-for="copy in copies" :key="copy.id" type="button" class="copy-download" :disabled="busy" @click="download(copy)">{{ copy.title }} · {{ new Date(copy.createdAt).toLocaleString() }}<span v-if="copy.source === 'forgotten'"> · {{ t('officeEditor.access.retainedSnapshot') }}</span> ↓</button></details>
  </aside>
  <el-dialog :model-value="resetDialogOpen" :title="t('officeEditor.access.resetTitle')" width="min(500px, calc(100vw - 32px))" append-to-body :close-on-click-modal="false" :close-on-press-escape="!busy" :show-close="!busy" @update:model-value="(open: boolean) => { if (!open) cancelReset(); }" data-testid="office-clear-occupancy-dialog">
    <dl class="reset-document">
      <div><dt>{{ t('officeEditor.access.resetDocument') }}</dt><dd>{{ resetTitle }}</dd></div>
      <div><dt>{{ t('officeEditor.access.resetStatus') }}</dt><dd>{{ resetConnection }}</dd></div>
      <div><dt>{{ t('officeEditor.access.resetSavedVersion') }}</dt><dd>{{ resetLastSaved }}</dd></div>
    </dl>
    <p class="reset-help">{{ t('officeEditor.access.resetHelp') }}</p>
    <p class="reset-warning">{{ t('officeEditor.access.resetWarning') }}</p>
    <label class="reset-acknowledgement"><input v-model="resetAcknowledged" type="checkbox" data-testid="office-clear-acknowledgement" :disabled="busy" /> <span>{{ t('officeEditor.access.resetAcknowledge') }}</span></label>
    <label v-if="resetRequiresOnlineConfirmation" class="reset-acknowledgement"><input v-model="resetInterruptAcknowledged" type="checkbox" data-testid="office-clear-interrupt-acknowledgement" :disabled="busy" /> <span>{{ t('officeEditor.access.resetInterruptAcknowledge') }}</span></label>
    <template #footer><button type="button" class="access-button" :disabled="busy" @click="cancelReset">{{ t('common.cancel') }}</button><button type="button" class="access-button danger" data-testid="office-clear-confirm" :disabled="busy || !canConfirmReset" @click="clearOccupancy">{{ t('officeEditor.access.resetConfirm') }}</button></template>
  </el-dialog>
</template>

<style scoped>
.office-access-panel { display: flex; flex-wrap: wrap; align-items: center; gap: 10px 18px; padding: 12px 16px; border-bottom: 1px solid var(--el-border-color-lighter); background: var(--el-fill-color-extra-light); font-size: 12px; }
.access-copy { display: flex; flex: 1 1 240px; flex-direction: column; gap: 4px; min-width: 0; color: var(--el-text-color-secondary); line-height: 1.6; }.access-copy strong { font-weight: 600; color: var(--el-text-color-primary); }.access-copy small { font-size: 11px; }
.access-actions { display: flex; flex-wrap: wrap; gap: 6px; }.access-button { border: 1px solid var(--el-border-color); border-radius: 7px; background: var(--el-bg-color); color: var(--el-text-color-primary); font: inherit; padding: 6px 10px; cursor: pointer; }.access-button.primary { color: var(--el-color-primary); border-color: var(--el-color-primary-light-5); }.access-button:hover:not(:disabled) { background: var(--el-fill-color); }.access-button:disabled, .copy-download:disabled { opacity: .45; cursor: not-allowed; }.access-button:focus-visible, .copy-download:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 2px; }
.access-error { flex-basis: 100%; margin: 0; color: var(--el-color-danger); overflow-wrap: anywhere; }.recovery-copies { flex-basis: 100%; color: var(--el-text-color-secondary); }.recovery-copies summary { cursor: pointer; }.recovery-copies p { margin: 8px 0; }.copy-download { display: block; max-width: 100%; overflow-wrap: anywhere; border: 0; padding: 5px 0; background: none; color: var(--el-color-primary); text-align: left; cursor: pointer; font: inherit; }
.access-button.caution { color: var(--el-color-danger); border-color: var(--el-color-danger-light-5); }.access-button.danger { margin-left: 8px; color: var(--el-color-white, #fff); background: var(--el-color-danger); border-color: var(--el-color-danger); }.access-button.danger:hover:not(:disabled) { background: var(--el-color-danger-dark-2); }
.reset-document { display: grid; gap: 8px; margin: 0 0 18px; padding: 12px 14px; border-radius: 8px; background: var(--el-fill-color-light); font-size: 12px; }.reset-document div { display: grid; grid-template-columns: 112px minmax(0, 1fr); gap: 12px; }.reset-document dt { color: var(--el-text-color-secondary); }.reset-document dd { margin: 0; color: var(--el-text-color-primary); overflow-wrap: anywhere; }.reset-help, .reset-warning { font-size: 13px; line-height: 1.75; margin: 12px 0; }.reset-warning { color: var(--el-color-danger); }.reset-acknowledgement { display: flex; align-items: flex-start; gap: 8px; margin-top: 14px; color: var(--el-text-color-primary); font-size: 13px; line-height: 1.7; cursor: pointer; }.reset-acknowledgement input { flex: 0 0 auto; margin: 5px 0 0; accent-color: var(--el-color-danger); }.reset-acknowledgement input:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 2px; }
</style>
