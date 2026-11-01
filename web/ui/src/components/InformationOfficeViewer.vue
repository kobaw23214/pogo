<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId } from "vue";
import { useI18n } from "vue-i18n";
import { Loading, Lock } from "@element-plus/icons-vue";
import { randomUUID } from "@/utils/uuid";
import { openInformationOfficeView, validInformationOfficeView, informationOfficeViewSnapshot, type InformationOfficeView } from "@/api/informationOfficeView";
import { loadOfficeEngine, officeApi, officeBridge, officeBridgeBase, type OfficeBridgeMessage, type OfficeCapabilities, type OfficeCommand, type OfficeDocEditor, type OfficeSession } from "@/agent/officeTransport";
import { bindOfficeSelection, officeSelectionArgs, officeSelectionStillCurrent } from "@/agent/officeSelection";
import type { OfficeReferenceSnapshot } from "@/api/types";
import { OFFICE_STARTUP_TIMEOUT_MS } from "@/agent/officeStartup";
import { observeOfficeSelectionActivity } from "@/agent/officeSelectionActivity";

// The actual Office renderer and selection bridge are shared with conversation
// editing. This host only owns a server-enforced, immutable viewer capability.
// No autosave, document-state write, export, restore or editing code is loaded.
const props = withDefaults(defineProps<{ entryId: string; version: number; disabled?: boolean; mode?: "selection" | "preview" }>(), { mode: "selection" });
const emit = defineEmits<{ reference: [snapshot: OfficeReferenceSnapshot]; error: [message: string]; ready: [] }>();
const { t } = useI18n();
const mountId = `information-office-${useId().replace(/[^a-zA-Z0-9_-]/g, "")}`;
const root = ref<HTMLElement | null>(null);
const instanceId = randomUUID(), operationId = randomUUID(), lifecycle = 1;
const session = ref<OfficeSession | null>(null), loading = ref(true), failed = ref("");
const engineReady = ref(false), bridgeReady = ref(false), selectionRead = ref(false), capturing = ref(false), dispatching = ref(false), notice = ref("");
const readyCapabilities = ref<OfficeCapabilities | null>(null);
const ready = computed(() => engineReady.value && bridgeReady.value && !failed.value && !!session.value && !session.value.closed);
const canReference = computed(() => props.mode !== "preview" && ready.value && selectionRead.value && !props.disabled && !capturing.value && !dispatching.value);
let disposed = false, opened: InformationOfficeView | null = null, editor: OfficeDocEditor | null = null, source: Window | null = null;
let requestStarted = false, createPending: Promise<InformationOfficeView> | null = null, cleanupPending: Promise<void> | null = null;
let binding = false, polling = false, flight: { command: OfficeCommand; reporting: boolean } | null = null;
let selectionEpoch = 0, stopSelectionActivity: (() => void) | undefined;
let pollTimer: ReturnType<typeof setTimeout> | undefined, heartbeatTimer: ReturnType<typeof setInterval> | undefined, startupTimer: ReturnType<typeof setTimeout> | undefined;
const startup = new AbortController();
const current = () => !disposed && !!opened && session.value?.id === opened.session.id;
function error(value: unknown) { if (!disposed) { clearTimeout(startupTimer); failed.value = value instanceof Error ? value.message : t("informationOffice.unavailable"); loading.value = false; emit("error", failed.value); } }
function install(value: OfficeSession): boolean {
  if (!opened || value.id !== opened.session.id || value.documentId !== opened.session.documentId || value.generation !== opened.session.generation || value.path !== opened.source.path || value.canvasVersion !== opened.source.sha256 || value.dirty) throw new Error(t("officeEditor.unverified"));
  session.value = value; return true;
}
function schedule(delay = 150) { clearTimeout(pollTimer); if (current() && bridgeReady.value) pollTimer = setTimeout(() => void poll(), delay); }
async function refresh() { if (!current()) return; const value = await officeApi.status(opened!.session.id); if (current()) install(value.session); }
async function heartbeat() {
  if (!current() || !session.value?.leaseEpoch) return;
  try { const reply = await officeApi.heartbeat(opened!.session.id, instanceId, session.value.leaseEpoch); if (current()) { if (!reply.ok) throw new Error(t("officeEditor.unverified")); install(reply.session); } } catch (value) { if (current()) error(value); }
}
async function bind() {
  if (!current() || !engineReady.value || !source || !readyCapabilities.value || binding || bridgeReady.value) return;
  binding = true;
  try {
    // Forward the engine's entire verified capability receipt. The server
    // validates editor identity and extension/history guarantees; replacing
    // those with a host-generated subset would erase that evidence.
    const reply = await officeBridge<{ ok: boolean; session: OfficeSession }>(opened!, "ready", instanceId, { capabilities: readyCapabilities.value }, startup.signal);
    if (!current()) return;
    if (!reply.ok || !install(reply.session) || !reply.session.ready) throw new Error(t("officeEditor.selection.unavailable"));
    bridgeReady.value = true; loading.value = false; clearTimeout(startupTimer); emit("ready"); schedule(0);
  } catch (value) { if (current()) error(value); }
  finally { binding = false; }
}
async function poll() {
  if (!current() || !bridgeReady.value || polling) return;
  polling = true;
  try {
    const reply = await officeBridge<{ command: OfficeCommand | null; session?: OfficeSession; flight?: { operationId: string; finished: boolean; readOnly: boolean } }>(opened!, "next", instanceId, undefined, startup.signal, undefined, undefined, flight?.command.operationId);
    if (!current()) return;
    if (reply.session) install(reply.session);
    if (flight && reply.flight?.operationId === flight.command.operationId && reply.flight.finished && reply.flight.readOnly) { flight = null; dispatching.value = false; }
    if (reply.command && !flight) {
      // The server also enforces this allowlist. Never dispatch a mutation even
      // if a malformed or stale queue item reaches this read-only host.
      if (!["selection.read", "capabilities"].includes(reply.command.operation)) throw new Error(t("officeEditor.unverified"));
      flight = { command: reply.command, reporting: false };
      dispatching.value = true;
      source?.postMessage({ protocol: "pogo-office-lab/v1", type: "request", sessionId: opened!.session.id, documentId: opened!.session.documentId, token: opened!.bridgeToken, operationId: reply.command.operationId, operation: reply.command.operation, args: reply.command.args }, location.origin);
    }
  } catch (value) { if (current()) error(value); }
  finally { polling = false; }
  schedule();
}
async function result(message: OfficeBridgeMessage) {
  const active = flight;
  if (!current() || !active || active.reporting || message.operationId !== active.command.operationId) return;
  active.reporting = true;
  try {
    const reply = await officeBridge<{ ok: boolean; ignored?: boolean; session?: OfficeSession }>(opened!, "results", instanceId, { operationId: message.operationId, ok: message.ok === true, result: message.result, error: message.error }, startup.signal);
    if (!current() || flight !== active) return;
    if (!reply.ok || reply.ignored) throw new Error(t("officeEditor.unverified"));
    if (reply.session) install(reply.session);
    flight = null; dispatching.value = false; schedule(0);
  } catch (value) { if (current()) { active.reporting = false; error(value); } }
}
function message(event: MessageEvent) {
  const value = event.data as OfficeBridgeMessage;
  if (!current() || event.origin !== location.origin || !event.source || !value || value.protocol !== "pogo-office-lab/v1" || value.sessionId !== opened!.session.id || value.documentId !== opened!.session.documentId || value.token !== opened!.bridgeToken) return;
  if (value.type === "ready") {
    if (source && source !== event.source) return;
    if ((value.capabilities as OfficeCapabilities & { editor?: string } | undefined)?.editor !== opened!.session.editor) { error(new Error(t("officeEditor.unverified"))); return; }
    source = event.source as Window;
    source.postMessage({ protocol: "pogo-office-lab/v1", type: "ready-ack", sessionId: opened!.session.id, documentId: opened!.session.documentId, token: opened!.bridgeToken }, location.origin);
    if (!bridgeReady.value) { readyCapabilities.value = { ...value.capabilities }; selectionRead.value = value.capabilities?.selectionRead === true; }
    void bind();
  } else if (value.type === "response" && event.source === source) void result(value);
  else if (value.type === "bootstrap-error" && (!source || event.source === source)) error(new Error(t("officeEditor.selection.unavailable")));
}
async function referenceSelection() {
  if (!canReference.value || !opened) return;
  capturing.value = true; notice.value = "";
  const claim = opened;
  try {
    await refresh(); if (!current() || !session.value) return;
    const selected = bindOfficeSelection(session.value, instanceId, selectionEpoch, lifecycle, claim.session.conversationId);
    const reply = await officeApi.command(claim.session.id, { operationId: randomUUID(), operation: "selection.read", args: officeSelectionArgs(selected) });
    if (!current() || opened !== claim) return;
    if (!reply.ok) throw Object.assign(new Error(reply.error?.message), { code: reply.error?.code });
    await refresh();
    if (!current() || !officeSelectionStillCurrent(selected, session.value, instanceId, selectionEpoch, lifecycle, claim.session.conversationId)) throw Object.assign(new Error(), { code: "SELECTION_CHANGED" });
    emit("reference", informationOfficeViewSnapshot(claim, selected, session.value!, reply.result));
    notice.value = t("informationOffice.selectionReady");
  } catch (value) {
    if (!current()) return;
    const code = (value as { code?: string }).code;
    notice.value = t(code === "EMPTY_SELECTION" ? "officeEditor.selection.empty" : ["SELECTION_LIMIT", "DOCUMENT_LIMIT", "INVALID_ARGUMENT"].includes(code || "") ? "officeEditor.selection.limit" : code === "SELECTION_CHANGED" ? "officeEditor.selection.changed" : "officeEditor.selection.failed");
  } finally { if (!disposed) capturing.value = false; }
}
async function cleanup(): Promise<void> {
  if (cleanupPending) return cleanupPending;
  cleanupPending = (async () => {
    let claim = opened;
    if (!claim && requestStarted) {
      try { claim = await (createPending || openInformationOfficeView(props.entryId, props.version, instanceId, operationId)); }
      catch { try { claim = await openInformationOfficeView(props.entryId, props.version, instanceId, operationId); } catch { /* The server expires unresolved read-only viewers. */ } }
    }
    if (claim?.session?.id) {
      try { await officeApi.close(claim.session.id); }
      catch { try { await officeApi.close(claim.session.id); } catch { /* Never close another document or force editing state. */ } }
    }
  })();
  return cleanupPending;
}
onMounted(async () => {
  window.addEventListener("message", message);
  startupTimer = setTimeout(() => error(new Error(t("officeEditor.startup.timeout", { stage: t("officeEditor.startup.phases.document") }))), OFFICE_STARTUP_TIMEOUT_MS);
  try {
    requestStarted = true; createPending = openInformationOfficeView(props.entryId, props.version, instanceId, operationId);
    const claim = await createPending;
    opened = claim;
    if (disposed) { await cleanup(); return; }
    if (!validInformationOfficeView(claim, props.entryId, props.version)) throw new Error(t("officeEditor.unverified"));
    claim.bridgeURL = officeBridgeBase(claim.bridgeURL); session.value = claim.session;
    const api = await loadOfficeEngine(claim.browserURL, startup.signal); await nextTick();
    if (!current()) return;
    const config = { ...claim.config, width: "100%", height: "100%", events: {
      onDocumentReady: async () => { if (!current()) return; try { await officeBridge(claim, "mounted", instanceId, {}, startup.signal); if (current()) { engineReady.value = true; stopSelectionActivity?.(); if (root.value) stopSelectionActivity = observeOfficeSelectionActivity(root.value, () => { selectionEpoch++; }); void bind(); void heartbeat(); heartbeatTimer = setInterval(() => void heartbeat(), 10_000); } } catch (value) { if (current()) error(value); } },
      onError: (event: { data?: { errorDescription?: string } }) => { if (current()) error(new Error(event.data?.errorDescription || t("informationOffice.unavailable"))); },
    } };
    editor = new api.DocEditor(mountId, config);
  } catch (value) { if (!disposed) { error(value); void cleanup(); } }
});
onBeforeUnmount(() => {
  disposed = true; clearTimeout(pollTimer); clearTimeout(startupTimer); clearInterval(heartbeatTimer); stopSelectionActivity?.(); startup.abort(); window.removeEventListener("message", message);
  try { editor?.destroyEditor(); } catch { /* This renderer owns only its read-only viewer. */ }
  void cleanup();
});
defineExpose({ referenceSelection, cleanup });
</script>

<template>
  <section ref="root" class="information-office-viewer" data-testid="information-office-viewer" :data-office-ready="ready" :data-office-session="session?.id" data-office-readonly="true">
    <div class="information-office-toolbar"><span><el-icon><Lock /></el-icon>{{ t(mode === 'preview' ? 'informationOffice.previewReadonly' : 'informationOffice.readonly') }}</span><small>{{ t(mode === 'preview' ? 'informationOffice.previewHint' : session?.editor === 'cell' ? 'informationOffice.nativeExcelHint' : 'informationOffice.nativeWordHint') }}</small><button v-if="mode !== 'preview'" type="button" class="office-reference-selection" data-testid="office-capture-selection" :disabled="!canReference" :aria-busy="capturing" @pointerdown.prevent @click="referenceSelection"><el-icon v-if="capturing" class="is-loading"><Loading /></el-icon><svg v-else viewBox="0 0 20 20" fill="none" aria-hidden="true"><path d="M7 4H4v12h12v-3M9 4h7v7M16 4l-9 9" stroke="currentColor" stroke-width="1.55" stroke-linecap="round" stroke-linejoin="round" /></svg>{{ t('officeEditor.selection.action') }}</button></div>
    <div class="information-office-canvas"><div :id="mountId" class="information-office-mount"></div><div v-if="loading || failed" class="information-office-cover" :role="failed ? 'alert' : 'status'"><el-icon v-if="loading && !failed" class="is-loading"><Loading /></el-icon><strong>{{ failed || t('informationOffice.loading') }}</strong><span v-if="!failed">{{ t('informationOffice.engineLoading') }}</span><slot v-else name="error" /></div></div>
    <p v-if="notice" class="information-office-notice" role="status">{{ notice }}</p>
  </section>
</template>

<style scoped>
.information-office-viewer{display:flex;flex-direction:column;flex:1;min-width:0;min-height:0}.information-office-toolbar{display:flex;align-items:center;gap:14px;padding:8px 14px;min-height:38px;flex:none;border-bottom:1px solid var(--el-border-color-lighter);background:var(--el-bg-color)}.information-office-toolbar>span{display:flex;gap:5px;align-items:center;font-size:11px;white-space:nowrap;color:var(--el-text-color-secondary)}.information-office-toolbar>small{font-size:11px;color:var(--el-text-color-secondary);flex:1}.office-reference-selection{display:flex;align-items:center;gap:6px;flex:none;border:1px solid var(--el-border-color);border-radius:7px;padding:7px 11px;background:var(--el-bg-color);font:inherit;font-size:12px;color:var(--el-text-color-primary);cursor:pointer;transition:color .16s,border-color .16s,background .16s}.office-reference-selection>svg,.office-reference-selection>.el-icon{height:16px;width:16px}.office-reference-selection:hover:not(:disabled){color:var(--el-color-primary);border-color:var(--el-color-primary);background:var(--el-color-primary-light-9)}.office-reference-selection:disabled{opacity:.45;cursor:default}.information-office-canvas{flex:1;position:relative;min-height:0;background:var(--el-fill-color-light);isolation:isolate}.information-office-mount{height:100%;width:100%}.information-office-cover{position:absolute;inset:0;z-index:1;display:flex;align-items:center;justify-content:center;flex-direction:column;gap:14px;padding:24px;text-align:center;background:var(--el-fill-color-extra-light)}.information-office-cover>.el-icon{font-size:24px;color:var(--el-color-primary)}.information-office-cover strong{font-size:13px;font-weight:500;max-width:560px;overflow-wrap:anywhere}.information-office-cover span{font-size:11px;color:var(--el-text-color-secondary)}.information-office-cover[role=alert]{color:var(--el-color-danger)}.information-office-notice{flex:none;margin:0;padding:6px 14px;font-size:11px;line-height:1.5;color:var(--el-text-color-secondary);border-top:1px solid var(--el-border-color-lighter)}@media(max-width:650px){.information-office-toolbar>small{display:none}.office-reference-selection{margin-left:auto}.information-office-toolbar{gap:8px;padding:7px 9px}}
</style>
