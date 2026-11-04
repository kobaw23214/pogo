<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, useId } from "vue";
import { useI18n } from "vue-i18n";
import { ElMessageBox } from "element-plus";
import { ArrowDown, Document, Loading, DocumentChecked } from "@element-plus/icons-vue";
import { randomUUID } from "@/utils/uuid";
import { officeApi, officeBridge, officeBridgeBase, detachOfficeView, loadOfficeEngine, registerOfficeView, officePathMounted, officeViewMounted, observeOfficeSync, type OfficeOpen, type OfficeSession, type OfficeCapabilities, type OfficeCommand, type OfficeBridgeMessage, type OfficeDocEditor, type OfficeActivitySnapshot, type OfficeRecovery } from "@/agent/officeTransport";
import { newerOfficeActivity, officeActivityItems } from "@/agent/officeActivity";
import OfficeActivity from "./OfficeActivity.vue";
import OfficeVersions from "./OfficeVersions.vue";
import { officeVersionsApi, type OfficeRestoreSide, type OfficeVersionRestore } from "@/agent/officeVersions";
import { registerStudioNavigationGuard } from "@/agent/studioNavigation";
import { rememberOfficeFileIdentity, workspaceFilePath } from "@/agent/officeFiles";
import { officeHasNewVersion, officeReplacementMatches, officeSessionClean, officeStatusIsOlder, validOfficeVersion } from "@/agent/officeVersionSync";
import { bindOfficeSelection, officeSelectionArgs, officeSelectionSnapshot, officeSelectionStillCurrent } from "@/agent/officeSelection";
import { OFFICE_STARTUP_TIMEOUT_MS, officeStartupTimeout, retryOfficeStartup, type OfficeStartupPhase, type OfficeStartupProgress } from "@/agent/officeStartup";
import type { OfficeReferenceSnapshot } from "@/api/types";
import { observeOfficeInput } from "@/agent/officeInput";
import { createOfficeAutosave } from "@/agent/officeAutosave";
import { withOfficeEditorMode } from "@/agent/officeMode";

const props = defineProps<{ path: string; conversationId: string; currentConversationId?: string; officeSessionId?: string; immersive?: boolean; initialOpen?: OfficeOpen; initialInstanceId?: string; viewOnly?: boolean }>();
const emit = defineEmits<{ status: [session: OfficeSession]; closed: []; closedPrevious: [id: string]; saved: []; preview: []; reference: [snapshot: OfficeReferenceSnapshot]; openFailed: [message: string]; focusExisting: [path: string]; viewSaved: [] }>();
const { t, locale } = useI18n();
const mountBase = `office-${useId().replace(/[^a-zA-Z0-9_-]/g, "")}`;
const instanceId = props.initialInstanceId || randomUUID();
let suppliedClaim = props.initialOpen;
const root = ref<HTMLElement | null>(null);
const session = ref<OfficeSession | null>(null);
const activity = shallowRef<OfficeActivitySnapshot>({ version: 0, items: [] });
const activityItems = computed(() => {
  // Re-project only when receipts or locale change, not on every status poll.
  void locale.value;
  return officeActivityItems(activity.value, t);
});
const capabilities = ref<OfficeCapabilities>({});
const editorReady = ref(false);
const bridgeReady = ref(false);
const loading = ref(true);
const startupPhase = ref<OfficeStartupPhase>("opening");
const startupElapsed = ref(0);
const startupAttempt = ref(1);
const startupUnconfirmed = ref(false);
const bootstrapReleasePending = ref(false);
const startupReleasing = ref(false);
let startupController = new AbortController();
let startupStartedAt = Date.now();
let startupClock: ReturnType<typeof setInterval> | undefined;
const startupSteps: OfficeStartupPhase[] = ["opening", "engine", "document", "bridge"];
const startupStageLabel = computed(() => t(`officeEditor.startup.phases.${startupPhase.value}`));
const startupProgressLabel = computed(() => t(startupAttempt.value > 1 ? "officeEditor.startup.retrying" : "officeEditor.startup.progress", {
  stage: startupStageLabel.value, elapsed: startupElapsed.value, attempt: startupAttempt.value, max: 3,
}));
const busy = ref(false);
const capturingSelection = ref(false);
const selectionNotice = ref("");
const selectionError = ref(false);
const saveRequested = ref(false);
const dirty = ref(false);
const autoSaveEnabled = ref(!props.viewOnly);
const inputLocked = ref(false);
const settlingExport = ref(false);
const failed = ref("");
const closing = ref(false);
const recoverySession = ref<OfficeSession | null>(null);
const recovering = ref(false);
const connectionLost = ref(false);
const leasePaused = ref(false);
const leaseFenced = ref(false);
let heartbeatTimer: ReturnType<typeof setInterval> | undefined;
let heartbeatController: AbortController | null = null;
let lastHeartbeatAt = 0;
let stopInputObserver: (() => void) | undefined;
let composingInput = false;
let lastInputAt = 0;
let manualSavePending = false;
const mountVersion = ref(0);
const mountId = computed(() => `${mountBase}-${mountVersion.value}`);
const canvasPanes = ref([{ version: 0, id: `${mountBase}-0` }]);
const retainedCanvas = shallowRef<{ editor: OfficeDocEditor; session: OfficeSession; version: number } | null>(null);
const replacement = shallowRef<{ previous: OfficeSession; id: string } | null>(null);
const syncFrozen = ref(false);
const syncError = ref("");
const syncVerifiedVersion = ref("");
const reloadRequested = ref(false);
const cancellingBatch = ref(false);
const versionRestoring = ref(false);
const restoreOperations = new Map<string, string>();
const abortOperations = new Map<string, string>();
const discardConsent = shallowRef<{ sessionId: string; generation: number; canvasVersion: string; diskVersion: string; dirtyEpoch: number; localEpoch: number } | null>(null);
const reloadAttempts = new Set<string>();
const freezeAcknowledgements = new Set<string>();
const stateReports = new Set<Promise<unknown>>();
let freezeWork: Promise<void> | null = null;
let replacementWork: Promise<void> | null = null;
let verificationWork: Promise<void> | null = null;
let removeSyncObserver: (() => void) | undefined;
const newerVersion = computed(() => officeHasNewVersion(session.value));
const syncState = computed(() => syncError.value ? "error" : replacement.value ? "syncing" :
  session.value?.batch?.phase === "freeze_requested" ? "preparing" : session.value?.batch ? "batch" :
  reloadRequested.value ? "syncing" : newerVersion.value ? "conflict" :
  syncVerifiedVersion.value && syncVerifiedVersion.value === session.value?.canvasVersion ? "synced" : "idle");
const syncNotice = computed(() => syncError.value || (syncState.value === "idle" ? "" : t(`officeEditor.sync.${syncState.value === "preparing" && session.value?.batch?.purpose === "reload" ? "preparingReload" : syncState.value}`)));
const canLoadLatest = computed(() => !inFlight.value && newerVersion.value && session.value?.reloadAvailable === true && !syncFrozen.value && !reloadRequested.value && cleanLocalEditor());
const canRecover = computed(() => !!recoverySession.value && recoverySession.value.connectionState === "disconnected" && !recoverySession.value.closed && !recoverySession.value.dirty && recoverySession.value.pending === false && !recoverySession.value.blocked && !recovering.value);
let opened: OfficeOpen | null = null;
let bootstrapClaim: OfficeOpen | null = null;
let canvasMounted = false;
let bootstrapReleasing = false;
let opening: Promise<void> | null = null;
let savePromise: Promise<boolean> | null = null;
const restartAttempts = new Set<string>();
let editor: OfficeDocEditor | null = null;
let bridgeSource: Window | null = null;
let disposed = false;
let registering = false;
const pendingState = ref(0);
let stateUnconfirmed = false;
let localEpoch = 0;
let lifecycle = 1;
let polling = false;
let pollTimer: ReturnType<typeof setTimeout> | undefined;
let statusTimer: ReturnType<typeof setInterval> | undefined;
let bootstrapTimer: ReturnType<typeof setTimeout> | undefined;
let statusRead: Promise<boolean> | null = null;
let releaseView: (() => void) | undefined;
let removeNavigationGuard: (() => void) | undefined;
let deferred: OfficeCommand | null = null;
let commandFailure: { operationId?: string; code?: string } | null = null;
let transportNotice = "";
let readNotice = "";
let pollFailures = 0;
let recoveryFlight: { barrierId: string; revision: number; operationIds: string[]; exportIds: string[]; lifecycle: number; requestedAt: number; reporting: boolean } | null = null;
const settledExports = new Set<string>();
let recoveryRead: Promise<void> | null = null;
let recoveryReadAttempt: { revision: number; attemptedAt: number } | null = null;
let commandNotice: { sessionId: string; lifecycle: number; operationId?: string; code: string; text: string } | null = null;
const determinateCommandErrors = new Set(["PRECONDITION_FAILED", "INVALID_ARGUMENT", "TARGET_NOT_UNIQUE", "NO_HISTORY"]);
const mutationCommands = new Set(["word.replace", "sheet.apply", "history.undo", "history.redo"]);
type Flight = { command: OfficeCommand; lifecycle: number; reporting: boolean; confirmedByHost?: boolean; expired?: boolean; timer?: ReturnType<typeof setTimeout>; input?: HTMLElement; previousInert?: boolean; completion?: Promise<boolean>; settle?: (saved: boolean) => void };
let flight: Flight | null = null;
const inFlight = ref(false);
const ready = computed(() => editorReady.value && bridgeReady.value && !connectionLost.value && !leasePaused.value && !leaseFenced.value && !session.value?.closed);
const canQueue = computed(() => ready.value && !versionRestoring.value && !session.value?.blocked && session.value?.recovery?.state !== "checking" && !syncFrozen.value && !reloadRequested.value && !newerVersion.value && !busy.value && !closing.value && !settlingExport.value && (!inFlight.value || !flight?.expired) && !session.value?.savePhase && session.value?.saveState !== "saving");
const canReferenceSelection = computed(() => ready.value && capabilities.value.selectionRead === true && !capturingSelection.value && !inFlight.value &&
  !pendingState.value && !stateUnconfirmed && !busy.value && !saving.value && !closing.value && !syncFrozen.value && !replacement.value && !reloadRequested.value && !versionRestoring.value && !session.value?.batch && !session.value?.blocked);
const canRestoreVersion = computed(() => !props.viewOnly && canQueue.value && !inFlight.value && !changedScope.value && !replacement.value && !session.value?.batch &&
  !!session.value?.fileRef && validOfficeVersion(session.value.diskVersion) && session.value.diskVersion === session.value.canvasVersion && cleanLocalEditor());
const canvasAtSavedVersion = computed(() => ready.value && !inFlight.value && !pendingState.value && cleanLocalEditor());
const saving = computed(() => saveRequested.value || inputLocked.value || settlingExport.value || session.value?.saveState === "saving");
const queued = computed(() => session.value?.savePhase === "queued" || (saveRequested.value && !inputLocked.value && !["exporting", "committing"].includes(session.value?.savePhase || "")));
const changedScope = computed(() => !!session.value && session.value.conversationId !== (props.currentConversationId ?? props.conversationId));
const saveLabel = computed(() => session.value?.saveState === "saving" || saving.value ? t("officeEditor.saving") : dirty.value ? t("officeEditor.unsaved") : session.value?.savedAt ? t("officeEditor.saved", { version: session.value.revision }) : t("officeEditor.idle"));
const recoveringOperation = computed(() => session.value?.recovery?.state === "checking");
const stateLabel = computed(() => leaseFenced.value ? t("officeEditor.access.fencedShort") : leasePaused.value ? t("officeEditor.access.pausedShort") : recoveringOperation.value ? t("officeEditor.recoveringShort") : failed.value || session.value?.saveState === "error" ? t("officeEditor.failedShort") : loading.value ? t("officeEditor.connectingShort") : queued.value ? t("officeEditor.queuedShort") : session.value?.saveState === "saving" || saving.value ? t("officeEditor.savingShort") : busy.value ? t("officeEditor.processingShort") : dirty.value ? t("officeEditor.unsavedShort") : session.value?.savedAt ? t("officeEditor.savedShort") : t("officeEditor.idleShort"));
const current = (epoch: number) => !disposed && !bootstrapReleasing && lifecycle === epoch && !!opened && session.value?.id === opened.session.id;
const autosave = createOfficeAutosave({
  canSave: () => !props.viewOnly && (manualSavePending || autoSaveEnabled.value) && canQueue.value && (manualSavePending || !inFlight.value) && !pendingState.value && !stateUnconfirmed && !composingInput && (manualSavePending || Date.now() - lastInputAt >= 3000),
  save: async () => performSave(),
});

function fenceEditor() {
  leaseFenced.value = true; leasePaused.value = true;
  clearTimeout(pollTimer); clearInterval(heartbeatTimer);
  failed.value = t("officeEditor.access.fenced"); applyStageLock();
}
function ownershipError(error: unknown): boolean {
  return ["EDITOR_FENCED", "EDITOR_LEASE_EXPIRED", "EDITOR_LEASE_CHANGED", "EDITOR_INSTANCE_MISMATCH", "SESSION_CLOSED"].includes((error as { code?: string })?.code || "");
}
async function heartbeat(epoch: number) {
  const binding = opened;
  if (!current(epoch) || !binding || leaseFenced.value || closing.value || !binding.session.leaseEpoch) return;
  if (lastHeartbeatAt && Date.now() - lastHeartbeatAt >= 45_000) { leasePaused.value = true; applyStageLock(); }
  if (heartbeatController) return;
  const readEpoch = localEpoch;
  const controller = new AbortController(); heartbeatController = controller;
  const timeout = setTimeout(() => controller.abort(), 8000);
  try {
    const receipt = await officeApi.heartbeat(binding.session.id, instanceId, binding.session.leaseEpoch, controller.signal);
    if (!current(epoch)) return;
    if (receipt.ok !== true || !matchesBinding(receipt.session, epoch) || receipt.session.leaseEpoch !== binding.session.leaseEpoch || receipt.session.recoverySealed || receipt.session.closed) { fenceEditor(); return; }
    lastHeartbeatAt = Date.now(); leasePaused.value = false; installStatus(receipt.session, epoch, readEpoch); applyStageLock();
    if (failed.value === t("officeEditor.access.leaseLost")) failed.value = "";
  } catch (error) { if (current(epoch)) { if (ownershipError(error)) fenceEditor(); else if (Date.now() - lastHeartbeatAt >= 45_000) { leasePaused.value = true; failed.value = t("officeEditor.access.leaseLost"); applyStageLock(); } } }
  finally { clearTimeout(timeout); if (heartbeatController === controller) heartbeatController = null; }
}

function failure(error: unknown) {
  commandNotice = null;
  const value = error as { code?: string; phase?: OfficeStartupPhase };
  const message = value?.code === "OFFICE_STARTUP_TIMEOUT" || value?.code === "OFFICE_REQUEST_TIMEOUT" || value?.code === "OFFICE_OPEN_TIMEOUT"
    ? t("officeEditor.startup.timeout", { stage: t(`officeEditor.startup.phases.${value.phase || startupPhase.value}`) })
    : error instanceof Error ? error.message : String(error);
  failed.value = t("officeEditor.failed", { message });
}
function startupStage(phase: OfficeStartupPhase) {
  clearInterval(startupClock);
  startupPhase.value = phase; startupElapsed.value = 0; startupAttempt.value = 1; startupStartedAt = Date.now();
  startupClock = setInterval(() => { startupElapsed.value = Math.max(0, Math.floor((Date.now() - startupStartedAt) / 1000)); }, 1000);
}
function startupAttempted(progress: OfficeStartupProgress) { startupAttempt.value = progress.attempt; }
function startupFinished() { clearInterval(startupClock); clearTimeout(bootstrapTimer); }
function watchStartupStage(phase: "document" | "bridge", epoch: number) {
  startupStage(phase); clearTimeout(bootstrapTimer);
  bootstrapTimer = setTimeout(() => { if (current(epoch)) void failBootstrap(officeStartupTimeout(phase), epoch); }, OFFICE_STARTUP_TIMEOUT_MS);
}
function installActivity(value?: OfficeActivitySnapshot) {
  const next = newerOfficeActivity(activity.value, value);
  if (next) activity.value = next;
}
function matchesDocument(value: OfficeSession, epoch: number): boolean {
  return current(epoch) && value.id === opened!.session.id && value.documentId === opened!.session.documentId && value.conversationId === opened!.session.conversationId && value.path === opened!.session.path && value.fileRef === opened!.session.fileRef && value.generation === opened!.session.generation;
}
function matchesBinding(value: OfficeSession, epoch: number): boolean {
  return matchesDocument(value, epoch) && value.bootEpoch === opened!.session.bootEpoch;
}
function installStatus(value: OfficeSession, epoch: number, readEpoch = localEpoch) {
  if (!matchesBinding(value, epoch)) return false;
  if (value.recoverySealed || (value.leaseEpoch && opened!.session.leaseEpoch && value.leaseEpoch !== opened!.session.leaseEpoch)) { fenceEditor(); return false; }
  if (officeStatusIsOlder(session.value, value)) return false;
  session.value = value;
  installActivity(value.activity);
  if (!value.blocked && value.recovery?.state !== "checking" && recoveryFlight) recoveryFlight = null;
  if (value.saveState === "saved" && !value.dirty && value.pending === false && !value.blocked && !pendingState.value && !stateUnconfirmed && !flight && readEpoch === localEpoch) {
    dirty.value = false;
    autosave.saved(readEpoch);
    if (failed.value === t("officeEditor.saveToClose") || failed.value === t("officeEditor.unsavedLeave")) failed.value = "";
  }
  else if (value.dirty) { if (!dirty.value) autosave.changed(++localEpoch); dirty.value = true; }
  if (value.capabilities) capabilities.value = value.capabilities;
  emit("status", value);
  reconcileVersion(value, epoch);
  return true;
}
function readStatus(): Promise<boolean> {
  if (statusRead) return statusRead;
  const work = doReadStatus(); statusRead = work;
  void work.then(() => { if (statusRead === work) statusRead = null; }, () => { if (statusRead === work) statusRead = null; });
  return work;
}
async function doReadStatus(): Promise<boolean> {
  if (!opened || disposed) return false;
  const epoch = lifecycle, readEpoch = localEpoch;
  let response: { session: OfficeSession };
  try { response = await officeApi.status(opened.session.id); }
  catch (error) {
    if (current(epoch) && (error as { status?: number; code?: string }).status === 404 && (error as { code?: string }).code === "DOCUMENT_NOT_FOUND") return reconnectMissing(epoch);
    throw error;
  }
  if (matchesDocument(response.session, epoch) && response.session.bootEpoch && response.session.bootEpoch !== opened.session.bootEpoch) return reconnectMissing(epoch);
  if (!installStatus(response.session, epoch, readEpoch)) return false;
  if (current(epoch) && opened.session.leaseEpoch && response.session.closed && !closing.value && !response.session.replacedBy && !replacement.value) { fenceEditor(); return true; }
  if (current(epoch) && response.session.closed && !response.session.replacedBy && !replacement.value && !syncFrozen.value && !dirty.value && !pendingState.value && !flight) { stop(); emit("closed"); }
  return true;
}
async function refreshStatus() { await readStatus(); }

async function restoreFileVersion(versionId: string, expectedVersion: string, fileRef: string, restoreSide: OfficeRestoreSide): Promise<OfficeVersionRestore> {
  const binding = opened, epoch = lifecycle;
  if (!binding || !canRestoreVersion.value || binding.session.fileRef !== fileRef || session.value?.diskVersion !== expectedVersion) throw new Error(t("officeVersions.restoreBlocked"));
  versionRestoring.value = true; applyStageLock();
  try {
    // Stop new input before yielding, then flush already queued input events.
    // The server repeats the save/version barrier under its document lock.
    if (!await flushState(epoch) || !await readStatus() || !current(epoch) || !cleanLocalEditor() || session.value?.batch || session.value?.diskVersion !== expectedVersion || session.value?.canvasVersion !== expectedVersion) throw new Error(t("officeVersions.restoreChanged"));
    const key = `${fileRef}:${versionId}:${restoreSide}:${expectedVersion}`;
    let operationId = restoreOperations.get(key);
    if (!operationId) {
      operationId = randomUUID(); restoreOperations.set(key, operationId);
      if (restoreOperations.size > 50) restoreOperations.delete(restoreOperations.keys().next().value!);
    }
    const response = await officeVersionsApi.restore(binding.session.conversationId, binding.session.path, versionId, expectedVersion, operationId, restoreSide);
    if (response.ok !== true || response.state !== "committed" || (response.file_ref && response.file_ref !== fileRef)) throw new Error(t("officeVersions.restoreUnknown"));
    return response;
  } finally {
    // Never clear dirty or mount a replacement from this HTTP result. Status
    // drives the existing retained-canvas / reload-ready verification path,
    // including recovery after a lost restore acknowledgement.
    if (current(epoch)) { try { await readStatus(); } catch (error) { if (current(epoch)) interrupted(error); } }
    if (!disposed) { versionRestoring.value = false; applyStageLock(); }
  }
}

function applyStageLock() {
  const stage = root.value?.querySelector<HTMLElement>(".office-stage");
  if (stage) stage.inert = startupReleasing.value || versionRestoring.value || syncFrozen.value || leasePaused.value || leaseFenced.value || !editorReady.value || !!session.value?.closed || inputLocked.value;
}
function freezeInput(value: boolean) { syncFrozen.value = value; applyStageLock(); }
function cleanLocalEditor(allowClosed = false): boolean {
  const value = session.value;
  return !!value && officeSessionClean(allowClosed ? { ...value, closed: false } : value) && !dirty.value && !stateUnconfirmed &&
    !pendingState.value && !flight && !busy.value && !saveRequested.value && !closing.value;
}
function discardStillConfirmed(value = session.value): boolean {
  const consent = discardConsent.value;
  return !!value && !!consent && consent.sessionId === value.id && consent.generation === value.generation && consent.canvasVersion === value.canvasVersion &&
    consent.diskVersion === value.diskVersion && consent.dirtyEpoch === value.dirtyEpoch && consent.localEpoch === localEpoch &&
    !pendingState.value && !stateUnconfirmed && !flight && !busy.value && !value.pending && !value.blocked && !value.savePhase;
}
async function flushState(epoch: number): Promise<boolean> {
  // Inert is set synchronously before yielding. Let input events already in
  // the browser queue report their state before taking a save/clean barrier.
  await nextTick();
  await new Promise<void>(resolve => setTimeout(resolve, 0));
  for (let pass = 0; pass < 4 && current(epoch); pass++) {
    await Promise.allSettled([...stateReports]);
    if (stateUnconfirmed) await reportState(true, epoch);
    if (!stateReports.size && !pendingState.value) break;
  }
  return current(epoch) && !stateReports.size && !pendingState.value && !stateUnconfirmed;
}
function reconcileVersion(value: OfficeSession, epoch: number) {
  if (!current(epoch)) return;
  if (value.replacedBy) {
    freezeInput(true);
    if (!replacementWork && !replacement.value) void beginReplacement(value, value.replacedBy);
    return;
  }
  if (value.closed && (syncFrozen.value || reloadRequested.value || replacement.value)) {
    syncError.value = t("officeEditor.sync.failed"); freezeInput(true); return;
  }
  if (value.batch) {
    freezeInput(true);
    if (value.batch.phase === "freeze_requested" && !freezeWork && !flight && !busy.value && !value.pending && !value.blocked) {
      const key = `${epoch}:${value.batch.id}`;
      if (!freezeAcknowledgements.has(key)) {
        freezeAcknowledgements.add(key);
        const work = acknowledgeFreeze(value.batch.id, value.batch.purpose, epoch);
        freezeWork = work;
        void work.finally(() => { if (freezeWork === work) freezeWork = null; });
      }
    }
    return;
  }
  if (!replacement.value) freezeInput(false); // Only an authoritative newer status releases a batch lock.
  if (officeHasNewVersion(value) && value.reloadAvailable && !value.versionError && cleanLocalEditor() && ready.value && !replacement.value) void requestReload();
}
async function acknowledgeFreeze(batchId: string, purpose: "batch" | "reload", epoch: number) {
  const binding = opened;
  if (!binding) return;
  syncError.value = "";
  try {
    if (!await flushState(epoch)) throw new Error(t("officeEditor.sync.unconfirmed"));
    if (session.value?.batch?.id !== batchId) return;
    if (purpose === "batch") {
      // This is an internal save barrier while the independent batch lock is
      // held. The normal save button and native mutation controls stay disabled.
      if (flight || busy.value || session.value?.blocked || session.value?.pending) throw new Error(t("officeEditor.sync.unconfirmed"));
      if (!await performSave()) throw new Error(t("officeEditor.sync.saveFailed"));
    }
    if (!current(epoch) || session.value?.batch?.id !== batchId) return;
    if (!await flushState(epoch) || !await readStatus()) throw new Error(t("officeEditor.sync.currentChanges"));
    const value = session.value!;
    if (value.batch?.id !== batchId || value.batch.phase !== "freeze_requested") return;
    const discarding = purpose === "reload" && value.batch.discardChanges === true && discardStillConfirmed(value) &&
      value.batch.confirmedDirtyEpoch === discardConsent.value?.dirtyEpoch && value.batch.confirmedCanvasVersion === discardConsent.value?.canvasVersion;
    if (!cleanLocalEditor() && !discarding) throw new Error(t("officeEditor.sync.currentChanges"));
    if (!validOfficeVersion(value.canvasVersion)) throw new Error(t("officeEditor.sync.unconfirmed"));
    const receipt = await officeBridge<{ ok: boolean; session: OfficeSession }>(binding, "batch-freeze", instanceId, {
      batchId, canvasVersion: value.canvasVersion, dirtyEpoch: value.dirtyEpoch, persistedEpoch: value.persistedEpoch, frozen: true,
    });
    if (!current(epoch)) return;
    if (receipt.ok !== true || !installStatus(receipt.session, epoch)) throw new Error(t("officeEditor.sync.unconfirmed"));
  } catch (error) {
    if (!current(epoch)) return;
    syncError.value = error instanceof Error ? error.message : t("officeEditor.sync.unconfirmed");
    // A negative acknowledgement ends only this exact requested lease. Never
    // assume it succeeded or unlock on a network/unknown outcome.
    try {
      const receipt = await officeBridge<{ ok: boolean; session?: OfficeSession }>(binding, "batch-freeze", instanceId, { batchId, frozen: false, error: syncError.value });
      if (current(epoch) && receipt.session) installStatus(receipt.session, epoch);
    } catch { /* The lock remains until status confirms cancellation. */ }
    if (current(epoch)) { try { await readStatus(); } catch { /* Preserve the canvas and lock. */ } }
  }
}
async function requestReload(manual = false, discard = false) {
  const value = session.value, binding = opened, epoch = lifecycle;
  if (!value || !binding || reloadRequested.value || replacement.value || value.batch || !officeHasNewVersion(value) || (!discard && (!value.reloadAvailable || !cleanLocalEditor())) || (discard && !discardStillConfirmed(value))) return;
  const key = `${value.id}:${value.diskVersion}`;
  if (!manual && reloadAttempts.has(key)) return;
  reloadAttempts.add(key); reloadRequested.value = true; syncError.value = "";
  try {
    const consent = discard ? discardConsent.value : null;
    const receipt = await officeApi.reload(value.id, randomUUID(), value.diskVersion!, undefined, consent ? { discardChanges: true, confirmedDirtyEpoch: consent.dirtyEpoch, confirmedCanvasVersion: consent.canvasVersion, instanceId } : undefined);
    if (!current(epoch)) return;
    if (receipt.ok !== true || !receipt.session?.id) throw new Error(t("officeEditor.sync.unconfirmed"));
    // The request response and the status invalidation may race. Both lead to
    // the same deduplicated, identity-checked replacement.
    await beginReplacement({ ...value, diskVersion: value.diskVersion }, receipt.session.id);
  } catch (error) { if (current(epoch)) { syncError.value = error instanceof Error ? error.message : t("officeEditor.sync.failed"); try { await readStatus(); } catch { /* No inferred unlock. */ } } }
  finally { if (epoch === lifecycle || replacement.value) reloadRequested.value = false; }
}
function beginReplacement(previous: OfficeSession, id: string): Promise<void> {
  if (replacementWork) return replacementWork;
  if (replacement.value) return Promise.resolve();
  if ((!cleanLocalEditor(true) && !discardStillConfirmed(previous)) || !editor || !validOfficeVersion(previous.diskVersion)) {
    syncError.value = t("officeEditor.sync.currentChanges"); freezeInput(true); return Promise.resolve();
  }
  replacement.value = { previous, id }; syncError.value = ""; freezeInput(true);
  resetBinding(true);
  loading.value = true; connectionLost.value = false;
  const work = (async () => { await nextTick(); if (!disposed) await openEditor(false, previous.fileRef, id); })();
  replacementWork = work;
  void work.finally(() => { if (replacementWork === work) replacementWork = null; });
  return work;
}
function verifyReplacement(): Promise<void> {
  if (verificationWork) return verificationWork;
  const expected = replacement.value, binding = opened, epoch = lifecycle;
  if (!expected || !binding || !ready.value) return Promise.resolve();
  const work = (async () => {
    try {
      const command = async (operation: string, args: Record<string, unknown>) => {
        const operationId = randomUUID();
        const reply = await officeApi.command(binding.session.id, { operationId, operation, args });
        if (!current(epoch) || replacement.value !== expected || reply.ok !== true) throw new Error(t("officeEditor.sync.unconfirmed"));
        if (flight?.command.operationId === operationId && flight.completion && !await flight.completion) throw new Error(t("officeEditor.sync.unconfirmed"));
        return { operationId, result: reply.result as { kind?: string; sheets?: string[] } | undefined };
      };
      let read: { operationId: string; result?: { kind?: string } };
      if (binding.session.editor === "word") read = await command("word.read", {});
      else {
        const listed = await command("sheet.list", {}), sheet = listed.result?.sheets?.[0];
        if (listed.result?.kind !== "cell" || typeof sheet !== "string" || !sheet) throw new Error(t("officeEditor.sync.unconfirmed"));
        read = await command("sheet.read", { sheet, range: "A1" });
      }
      if (read.result?.kind !== binding.session.editor || !current(epoch) || replacement.value !== expected) throw new Error(t("officeEditor.sync.unconfirmed"));
      const receipt = await officeBridge<{ ok: boolean; session: OfficeSession }>(binding, "reload-ready", instanceId, { canvasVersion: binding.session.canvasVersion, readOperationId: read.operationId });
      if (!current(epoch) || replacement.value !== expected) return;
      if (receipt.ok !== true || receipt.session.reloadConfirmed !== true || receipt.session.replacementPending !== false || !installStatus(receipt.session, epoch) || !officeReplacementMatches(expected.previous, receipt.session, expected.id) || !cleanLocalEditor()) throw new Error(t("officeEditor.sync.unconfirmed"));
      try { retainedCanvas.value?.editor.destroyEditor(); } catch { /* The verified replacement owns the visible document. */ }
      retainedCanvas.value = null; canvasPanes.value = canvasPanes.value.filter(pane => pane.version === mountVersion.value);
      replacement.value = null; reloadRequested.value = false; syncVerifiedVersion.value = receipt.session.canvasVersion!; syncError.value = ""; failed.value = "";
      discardConsent.value = null;
      freezeInput(false); emit("closedPrevious", expected.previous.id); emit("status", receipt.session);
    } catch (error) {
      if (current(epoch)) { syncError.value = t("officeEditor.sync.failed"); failure(error); freezeInput(true); }
    }
  })();
  verificationWork = work;
  void work.finally(() => { if (verificationWork === work) verificationWork = null; });
  return work;
}
async function discardAndReload() {
  if (!opened || !newerVersion.value || reloadRequested.value || replacement.value || session.value?.batch || flight || busy.value || pendingState.value || stateUnconfirmed || session.value?.blocked) return;
  const epoch = lifecycle;
  if (!await readStatus() || !current(epoch) || !session.value || !validOfficeVersion(session.value.canvasVersion) || !validOfficeVersion(session.value.diskVersion)) return;
  const value = session.value;
  const consent = { sessionId: value.id, generation: value.generation, canvasVersion: value.canvasVersion!, diskVersion: value.diskVersion!, dirtyEpoch: value.dirtyEpoch, localEpoch };
  try {
    await ElMessageBox.confirm(t("officeEditor.sync.discardConfirm"), t("officeEditor.sync.discardTitle"), { type: "warning", confirmButtonText: t("officeEditor.sync.discardAction"), cancelButtonText: t("common.cancel"), closeOnClickModal: false });
  } catch { return; }
  if (!current(epoch)) return;
  discardConsent.value = consent;
  if (!discardStillConfirmed()) { discardConsent.value = null; syncError.value = t("officeEditor.sync.changedWhileConfirming"); return; }
  await requestReload(true, true);
}
async function cancelBatch() {
  const value = session.value, batch = value?.batch, epoch = lifecycle;
  if (!value || !batch || batch.purpose !== "batch" || cancellingBatch.value || replacement.value) return;
  const operationId = abortOperations.get(batch.id) || randomUUID();
  abortOperations.set(batch.id, operationId); cancellingBatch.value = true;
  try {
    const receipt = await officeApi.abortBatch(batch.id, value.conversationId, operationId);
    if (!current(epoch)) return;
    if (receipt.ok !== true || receipt.state !== "aborted" || receipt.batch_id !== batch.id) throw new Error(t("officeEditor.sync.cancelUnknown"));
    syncError.value = "";
  } catch (error) { if (current(epoch)) syncError.value = error instanceof Error ? error.message : t("officeEditor.sync.cancelUnknown"); }
  finally {
    if (current(epoch)) { try { await readStatus(); } catch { syncError.value = t("officeEditor.sync.cancelUnknown"); } }
    cancellingBatch.value = false;
  }
}
async function retrySynchronization() {
  if (!replacement.value) { await requestReload(true); return; }
  if (flight || pendingState.value || stateUnconfirmed || dirty.value || session.value?.blocked || session.value?.pending) return;
  syncError.value = "";
  if (ready.value) { await verifyReplacement(); return; }
  if (replacementWork || opening) return;
  const expected = replacement.value;
  resetBinding(); loading.value = true;
  await nextTick();
  await openEditor(false, expected.previous.fileRef, expected.id);
}

function lockInput(active: Flight) {
  // DocsAPI replaces the mount with an iframe named frameEditor, without
  // preserving its id. Restrict the lock to this component's actual frame.
  active.input = root.value?.querySelector<HTMLElement>(`.office-canvas[data-canvas-version="${mountVersion.value}"] > iframe`) || root.value?.querySelector<HTMLElement>(`[id="${mountId.value}"]`) || undefined;
  active.previousInert = active.input?.inert ?? false;
  if (active.input) active.input.inert = true;
  inputLocked.value = true;
}
function unlockInput(active: Flight | null) {
  if (active?.input?.isConnected) active.input.inert = active.previousInert ?? false;
  if (!active || active.lifecycle === lifecycle) inputLocked.value = false;
  applyStageLock();
}
function interrupted(error: unknown) {
  if (!opened || disposed) return;
  if (ownershipError(error)) { fenceEditor(); return; }
  const refused = error as { status?: number; code?: string };
  // Authorization and an executor change are authoritative boundaries. A
  // temporary queue/recovery conflict must not disconnect this live canvas.
  if ([401, 403].includes(refused.status ?? 0) || ["EDITOR_INSTANCE_CHANGED", "EDITOR_INSTANCE_MISMATCH"].includes(refused.code || "")) {
    bridgeReady.value = false; failure(error); return;
  }
  connectionLost.value = true;
  transportNotice = t("officeEditor.reconnecting");
  if (!commandNotice) failed.value = transportNotice;
  commandNotice = null;
  pollFailures = Math.min(pollFailures + 1, 5);
}
function connectedTransport() {
  connectionLost.value = false; pollFailures = 0;
  if (transportNotice && failed.value === transportNotice) failed.value = "";
  transportNotice = "";
}
function schedule(delay = 150) {
  clearTimeout(pollTimer);
  if (disposed || !opened || !bridgeReady.value || leaseFenced.value || closing.value || session.value?.closed) return;
  pollTimer = setTimeout(() => void poll(), Math.max(delay, pollFailures ? Math.min(3000, 150 * 2 ** pollFailures) : delay));
}
function requestReconciliation(value: OfficeRecovery, epoch: number) {
  if (!current(epoch) || !bridgeSource || !opened || value.state !== "checking" || !Number.isSafeInteger(value.revision) || value.revision < 1 || (!value.operationIds.length && !value.exportIds?.length)) return;
  const previous = recoveryFlight;
  if (previous && previous.lifecycle === epoch && previous.revision === value.revision && Date.now() - previous.requestedAt < 3000) return;
  if (previous?.reporting) return;
  const active = { barrierId: randomUUID(), revision: value.revision, operationIds: [...value.operationIds], exportIds: [...value.exportIds || []], lifecycle: epoch, requestedAt: Date.now(), reporting: false };
  recoveryFlight = active;
  bridgeSource.postMessage({ protocol: "pogo-office-lab/v1", type: "reconcile", sessionId: opened.session.id, documentId: opened.session.documentId, token: opened.bridgeToken, barrierId: active.barrierId, recoveryRevision: active.revision, operationIds: active.operationIds }, location.origin);
}
function requestRecoveryRead(value: OfficeRecovery, epoch: number) {
  if (!current(epoch) || !opened || !value.needsRead || recoveryRead || flight) return;
  if (recoveryReadAttempt?.revision === value.revision && Date.now() - recoveryReadAttempt.attemptedAt < 3000) return;
  const binding = opened;
  recoveryReadAttempt = { revision: value.revision, attemptedAt: Date.now() };
  const work = (async () => {
    try {
      let target = value.readTarget;
      if (binding.session.editor === "word") target = { operation: "word.read", args: {} };
      else if (!target || target.operation !== "sheet.read") {
        const listed = await officeApi.command(binding.session.id, { operationId: randomUUID(), operation: "sheet.list", args: {} });
        const sheet = (listed.result as { sheets?: unknown[] } | undefined)?.sheets?.[0];
        if (listed.ok !== true || typeof sheet !== "string" || !sheet || !current(epoch)) return;
        target = { operation: "sheet.read", args: { sheet, range: "A1" } };
      }
      if (!target || !["word.read", "sheet.read"].includes(target.operation) || !current(epoch)) return;
      await officeApi.command(binding.session.id, { operationId: randomUUID(), operation: target.operation, args: target.args });
      if (current(epoch)) { await readStatus(); schedule(0); }
    } catch (error) { if (current(epoch)) interrupted(error); }
  })();
  recoveryRead = work;
  void work.finally(() => { if (recoveryRead === work) recoveryRead = null; });
}
async function reconciliation(message: OfficeBridgeMessage) {
  const active = recoveryFlight, binding = opened;
  if (!active || !binding || active.reporting || !current(active.lifecycle) || message.barrierId !== active.barrierId || message.recoveryRevision !== active.revision || message.settled !== true || !Array.isArray(message.operations)) return;
  // A ledger barrier reports the original operation. It never repeats a
  // mutation and never treats an empty reply as evidence that it finished.
  const operations = message.operations.filter(value => active.operationIds.includes(value.operationId));
  const exports = active.exportIds.filter(id => settledExports.has(id)).map(operationId => ({ operationId, settled: true }));
  if (active.operationIds.some(id => !operations.some(value => value.operationId === id)) || active.exportIds.some(id => !settledExports.has(id))) return;
  active.reporting = true;
  try {
    const receipt = await officeBridge<{ ok: boolean; session: OfficeSession }>(binding, "reconcile", instanceId, { recoveryRevision: active.revision, barrierId: active.barrierId, settled: true, operations, exports });
    if (!current(active.lifecycle) || recoveryFlight !== active || receipt.ok !== true || !installStatus(receipt.session, active.lifecycle)) return;
    if (!receipt.session.blocked && receipt.session.recovery?.state !== "checking") {
      if (flight && [...active.operationIds, ...active.exportIds].includes(flight.command.operationId)) {
        clearTimeout(flight.timer); unlockInput(flight); flight.settle?.(false); flight = null; inFlight.value = false;
      }
      recoveryFlight = null; connectedTransport();
      if (!commandNotice) failed.value = "";
      if (stateUnconfirmed) await reportState(true, active.lifecycle);
      schedule(0);
    }
  } catch (error) { if (current(active.lifecycle)) interrupted(error); }
  finally { active.reporting = false; }
}
async function poll() {
  if (polling || disposed || !opened || !bridgeReady.value || leaseFenced.value || closing.value || session.value?.closed) return;
  polling = true;
  const epoch = lifecycle;
  try {
    const result = await officeBridge<{ command: OfficeCommand | null; activity?: OfficeActivitySnapshot; session?: OfficeSession; recovery?: OfficeRecovery; flight?: { operationId: string; finished: boolean; readOnly: boolean; reconciled?: boolean; known?: boolean; succeeded?: boolean } }>(opened, "next", instanceId, undefined, undefined, activity.value.version, session.value?.syncRevision, flight?.command.operationId);
    if (!current(epoch)) return;
    connectedTransport();
    if (result.session) installStatus(result.session, epoch);
    if (!current(epoch)) return;
    installActivity(result.activity);
    if (stateUnconfirmed && !pendingState.value) void reportState(true, epoch);
    const outstanding = result.flight;
    if (flight && outstanding?.operationId === flight.command.operationId && outstanding.finished && (outstanding.readOnly || outstanding.reconciled || outstanding.known)) {
      const completed = flight;
      clearTimeout(completed.timer); unlockInput(completed); flight = null; inFlight.value = false;
      completed.confirmedByHost = outstanding.known === true;
      if (completed.command.operation === "host.export" && outstanding.known && outstanding.succeeded) {
        // The native export and backend commit are complete even if their HTTP
        // acknowledgement is delayed. Re-read the current dirty epoch before
        // resolving a UI save barrier; newer manual input stays unsaved.
        settlingExport.value = false;
        let saved = false;
        try {
          const confirmed = await readStatus();
          saved = confirmed && current(epoch) && session.value?.saveState === "saved" && !dirty.value && !session.value?.pending && !session.value?.blocked && !pendingState.value && !stateUnconfirmed;
        } catch (error) { if (current(epoch)) interrupted(error); }
        completed.settle?.(saved);
        if (saved && current(epoch)) { if (failed.value === t("officeEditor.savingUnknown")) failed.value = ""; emit("saved"); }
      } else {
        completed.settle?.(outstanding.known === true && outstanding.succeeded === true);
        if (outstanding.readOnly && outstanding.succeeded && readNotice && failed.value === readNotice) { failed.value = ""; readNotice = ""; }
      }
    }
    const recovery = result.recovery || session.value?.recovery;
    if (recovery?.state === "checking") { requestReconciliation(recovery, epoch); requestRecoveryRead(recovery, epoch); }
    if (closing.value) { deferred = result.command; return; }
    if (result.command && !flight) dispatch(result.command, epoch);
  } catch (error) {
    if (current(epoch)) {
      if ((error as { status?: number }).status === 404 && (error as { code?: string }).code === "DOCUMENT_NOT_FOUND") { await reconnectMissing(epoch); return; }
      interrupted(error);
    }
  } finally { if (epoch === lifecycle) polling = false; }
  schedule();
}
function dispatch(command: OfficeCommand, epoch: number) {
  if (props.viewOnly && (command.operation === "host.export" || mutationCommands.has(command.operation))) return;
  if (!current(epoch) || flight || !bridgeReady.value || closing.value) return;
  const active: Flight = { command, lifecycle: epoch, reporting: false };
  active.completion = new Promise<boolean>(resolve => { active.settle = resolve; });
  flight = active; inFlight.value = true;
  if (command.operation === "host.export") {
    lockInput(active);
    active.timer = setTimeout(() => {
      if (!current(epoch) || flight !== active) return;
      active.expired = true; unlockInput(active); active.settle?.(false); failed.value = t("officeEditor.savingUnknown"); schedule(0);
    }, 35000);
    try {
      if (command.args.fileType !== session.value?.fileType || typeof editor?.downloadAs !== "function") throw new Error(t("officeEditor.unverified"));
      editor.downloadAs(command.args.fileType as string);
    } catch (error) { active.expired = true; settledExports.add(active.command.operationId); clearTimeout(active.timer); unlockInput(active); active.settle?.(false); failure(error); }
    schedule();
    return;
  }
  // Actor/activity metadata belongs to the host UI, never to editor arguments.
  bridgeSource?.postMessage({ protocol: "pogo-office-lab/v1", type: "request", sessionId: opened!.session.id, documentId: opened!.session.documentId, token: opened!.bridgeToken, operationId: command.operationId, operation: command.operation, args: command.args }, location.origin);
  schedule();
}
async function bindBridge(epoch: number) {
  if (!current(epoch) || !opened || !editorReady.value || !bridgeSource || registering || bridgeReady.value) return;
  registering = true;
  const binding = opened;
  try {
    const response = await retryOfficeStartup("bridge", signal => officeBridge<{ ok: boolean; session: OfficeSession }>(binding, "ready", instanceId, { capabilities: capabilities.value }, signal), {
      signal: startupController.signal, onAttempt: startupAttempted,
    });
    if (!current(epoch)) return;
    if (!installStatus(response.session, epoch)) { loading.value = false; startupFinished(); failed.value = t("officeEditor.unverified"); return; }
    bridgeReady.value = response.session.ready === true;
    if (!bridgeReady.value) { loading.value = false; startupFinished(); failed.value = response.session.error || t("officeEditor.historyUnavailable"); return; }
    if (dirty.value && stateUnconfirmed) await reportState(true, epoch);
    if (!current(epoch)) return;
    loading.value = false; failed.value = ""; startupUnconfirmed.value = false; bootstrapReleasePending.value = false; startupFinished(); schedule(0);
    await refreshStatus();
    if (current(epoch) && replacement.value) void verifyReplacement();
  } catch (error) { if (current(epoch)) { loading.value = false; startupFinished(); interrupted(error); failure(error); } }
  finally { if (epoch === lifecycle) registering = false; }
}
async function reportState(isDirty: boolean, epoch: number) {
  if (!current(epoch) || !opened || !isDirty) return;
  if (connectionLost.value) { stateUnconfirmed = true; return; }
  // Config is atomically claimed before the canvas exists. Record human input
  // with that executor nonce even before the plugin capability handshake.
  pendingState.value += 1;
  const request = officeBridge(opened, "state", instanceId, { dirty: true });
  stateReports.add(request);
  try { await request; if (current(epoch)) stateUnconfirmed = false; }
  catch (error) { if (current(epoch)) { stateUnconfirmed = true; interrupted(error); } }
  finally { stateReports.delete(request); if (current(epoch)) pendingState.value -= 1; }
}
async function result(message: OfficeBridgeMessage) {
  const active = flight;
  if (!active || active.command.operation === "host.export" || active.reporting || message.operationId !== active.command.operationId || !current(active.lifecycle) || !opened) return;
  active.reporting = true;
  if (message.ok !== true) commandFailure = { operationId: message.operationId, code: message.error?.code };
  else if (commandFailure?.operationId === message.operationId) commandFailure = null;
  const readOnly = active.command.operation.endsWith(".read") || active.command.operation === "sheet.list" || active.command.operation === "capabilities";
  if (!readOnly && (message.ok === true || ["OUTCOME_UNKNOWN", "EDITOR_OUTCOME_UNKNOWN"].includes(message.error?.code || ""))) { dirty.value = true; autosave.changed(++localEpoch); }
  try {
    const receipt = await officeBridge<{ ok: boolean; ignored?: boolean; session?: OfficeSession }>(opened, "results", instanceId, { operationId: message.operationId, ok: message.ok === true, result: message.result, error: message.error });
    if (!current(active.lifecycle) || flight !== active) return;
    if (receipt.ok !== true) throw new Error(t("officeEditor.unverified"));
    if (receipt.session) installStatus(receipt.session, active.lifecycle);
    flight = null; inFlight.value = false;
    if (message.ok !== true && receipt.ignored !== true && active.command.operation !== "selection.read") {
      failed.value = message.error?.code === "NO_HISTORY" ? t(active.command.operation === "history.redo" ? "officeEditor.noRedo" : "officeEditor.noUndo") : t("officeEditor.failed", { message: message.error?.message || t("officeEditor.unverified") });
      const code = message.error?.code || "";
      if (readOnly && ["READ_TIMEOUT", "READ_FAILED", "OPERATION_CANCELLED", "EDITOR_READ_TIMEOUT"].includes(code)) readNotice = failed.value;
      commandNotice = determinateCommandErrors.has(code) ? { sessionId: opened.session.id, lifecycle: active.lifecycle, operationId: message.operationId, code, text: failed.value } : null;
    }
    const statusConfirmed = await readStatus();
    active.settle?.(statusConfirmed && message.ok === true && receipt.ignored !== true);
    if (current(active.lifecycle) && statusConfirmed && readOnly && message.ok === true && receipt.ignored !== true && readNotice && failed.value === readNotice && !session.value?.blocked) { failed.value = ""; readNotice = ""; }
    // Clear only a still-visible, definite rejection from this same editor.
    // A read, ignored receipt, blocked outcome or newer error is not recovery.
    if (current(active.lifecycle) && statusConfirmed && message.ok === true && receipt.ignored !== true && mutationCommands.has(active.command.operation) && !session.value?.blocked && commandNotice?.sessionId === opened.session.id && commandNotice.lifecycle === active.lifecycle && failed.value === commandNotice.text) {
      failed.value = ""; commandNotice = null;
    }
    if (current(active.lifecycle)) schedule(0);
  } catch (error) { if (current(active.lifecycle) && !active.confirmedByHost) { active.settle?.(false); active.reporting = false; interrupted(error); schedule(0); } }
}
async function exported(data: { url?: string; fileType?: string }, epoch: number) {
  const active = flight;
  if (!current(epoch) || !opened || !active || active.lifecycle !== epoch || active.command.operation !== "host.export" || active.reporting) return;
  if (!data || typeof data.url !== "string" || data.fileType !== session.value?.fileType) { active.expired = true; settledExports.add(active.command.operationId); clearTimeout(active.timer); unlockInput(active); active.settle?.(false); failed.value = t("officeEditor.savingUnknown"); schedule(0); return; }
  // A late download callback is proof that this native export ended. For a
  // timed-out export it is used only for reconciliation, never replayed as save.
  if (active.expired) {
    settledExports.add(active.command.operationId); clearTimeout(active.timer); unlockInput(active);
    active.settle?.(false); await readStatus().catch(interrupted); schedule(0); return;
  }
  active.reporting = true;
  settlingExport.value = true;
  try {
    const receipt = await officeBridge<{ ok: boolean; session: OfficeSession }>(opened, "export", instanceId, { operationId: active.command.operationId, url: data.url, fileType: data.fileType });
    if (!current(epoch) || flight !== active || active.expired) return;
    if (receipt.ok !== true || !receipt.session || !matchesBinding(receipt.session, epoch)) throw new Error(t("officeEditor.savingUnknown"));
    installStatus(receipt.session, epoch);
    clearTimeout(active.timer); unlockInput(active); flight = null; inFlight.value = false;
    if (!await readStatus() || !current(epoch)) {
      if (current(epoch)) { flight = active; inFlight.value = true; }
      throw new Error(t("officeEditor.savingUnknown"));
    }
    if (session.value?.saveState !== "saved" || dirty.value || session.value?.pending || session.value?.blocked || pendingState.value || stateUnconfirmed) {
      active.settle?.(false); failed.value = t("officeEditor.newerChanges"); schedule(0); return;
    }
    active.settle?.(true);
    if (current(epoch)) { emit("saved"); schedule(0); }
  } catch (error) {
    if (current(epoch) && !active.confirmedByHost) {
      active.expired = true; clearTimeout(active.timer); unlockInput(active); active.settle?.(false);
      const refused = error as { status?: number; code?: string };
      if (refused.status === 409 && ["FILE_CHANGED", "FILE_IDENTITY_CHANGED"].includes(refused.code || "") && flight === active) {
        // A definite no-write response ends this flight, but never clears dirty
        // edits. Unknown/timeout outcomes retain their original protection.
        flight = null; inFlight.value = false; schedule(0);
      }
      failure(error);
    }
  }
  finally { if (!disposed && epoch === lifecycle) { settledExports.add(active.command.operationId); settlingExport.value = false; active.reporting = false; schedule(0); } }
}
function message(event: MessageEvent) {
  const value = event.data as OfficeBridgeMessage;
  if (disposed || event.origin !== location.origin || !event.source || !opened || !value || value.protocol !== "pogo-office-lab/v1" || value.sessionId !== opened.session.id || value.documentId !== opened.session.documentId || value.token !== opened.bridgeToken) return;
  if (value.type === "ready") {
    if (bridgeSource && event.source !== bridgeSource) return;
    bridgeSource = event.source as Window;
    bridgeSource.postMessage({ protocol: "pogo-office-lab/v1", type: "ready-ack", sessionId: opened.session.id, documentId: opened.session.documentId, token: opened.bridgeToken }, location.origin);
    // Repeated startup announcements only confirm delivery. Their cached
    // capabilities must not overwrite a live dirty/blocked operation state.
    if (bridgeReady.value) return;
    capabilities.value = value.capabilities || {}; void bindBridge(lifecycle);
  } else if (value.type === "bootstrap-error") {
    if (bridgeSource && event.source !== bridgeSource) return;
    if (bridgeReady.value) return;
    bridgeSource = event.source as Window;
    startupFinished(); loading.value = false;
    failure(officeStartupTimeout("bridge"));
  } else if (value.type === "response" && event.source === bridgeSource) void result(value);
  else if (value.type === "reconciled" && event.source === bridgeSource) void reconciliation(value);
}
async function referenceSelection() {
  if (!canReferenceSelection.value || !opened) return;
  const epoch = lifecycle, targetConversationId = props.currentConversationId || props.conversationId;
  capturingSelection.value = true; selectionNotice.value = ""; selectionError.value = false;
  try {
    // Flush already reported human edits, but never save, freeze, restore or
    // move the selection. Input during this asynchronous read rejects it.
    if (!await flushState(epoch) || !await readStatus() || !current(epoch) || !session.value) throw Object.assign(new Error(), { code: "SELECTION_CHANGED" });
    const binding = bindOfficeSelection(session.value, instanceId, localEpoch, epoch, targetConversationId);
    const reply = await officeApi.command(binding.sessionId, { operationId: randomUUID(), operation: "selection.read", args: officeSelectionArgs(binding) });
    if (reply.ok !== true) throw Object.assign(new Error(reply.error?.message), { code: reply.error?.code });
    if (!await readStatus() || !current(epoch) || !officeSelectionStillCurrent(binding, session.value, instanceId, localEpoch, lifecycle, props.currentConversationId || props.conversationId) || pendingState.value || stateUnconfirmed) throw Object.assign(new Error(), { code: targetConversationId !== (props.currentConversationId || props.conversationId) ? "CONVERSATION_CHANGED" : "SELECTION_CHANGED" });
    const snapshot = officeSelectionSnapshot(binding, session.value!, reply.result);
    emit("reference", snapshot);
    selectionNotice.value = t("officeEditor.selection.added");
  } catch (error) {
    if (!current(epoch)) return;
    const code = (error as { code?: string }).code || (error instanceof Error ? error.message : "");
    const key = code === "EMPTY_SELECTION" ? "empty" : code === "SELECTION_LIMIT" || code === "DOCUMENT_LIMIT" || code === "INVALID_ARGUMENT" ? "limit" :
      code === "SELECTION_SHAPE" ? "shape" : code === "CONVERSATION_CHANGED" ? "conversationChanged" : code === "SELECTION_CHANGED" ? "changed" :
      code === "UNSUPPORTED_CAPABILITY" || code === "ENGINE_EXTENSION_REQUIRED" ? "unavailable" : "failed";
    selectionNotice.value = t(`officeEditor.selection.${key}`); selectionError.value = true;
  } finally { if (!disposed) capturingSelection.value = false; }
}
async function run(operation: "history.undo" | "history.redo") {
  if (!canQueue.value || !opened) return;
  const capability = operation === "history.undo" ? capabilities.value.nativeUndo : capabilities.value.nativeRedo;
  if (capability !== true) { failed.value = t("officeEditor.historyUnavailable"); return; }
  busy.value = true;
  const epoch = lifecycle;
  const operationId = randomUUID();
  try {
    const reply = await officeApi.command(opened.session.id, { operationId, operation, args: {} });
    if (reply.ok === false) throw new Error(reply.error?.message || t("officeEditor.unverified"));
  }
  catch (error) { if (current(epoch)) { if (commandFailure?.operationId === operationId && commandFailure.code === "NO_HISTORY") failed.value = t(operation === "history.redo" ? "officeEditor.noRedo" : "officeEditor.noUndo"); else failure(error); } }
  finally { if (current(epoch)) busy.value = false; }
}
function save(): Promise<boolean> {
  if (props.viewOnly) return Promise.resolve(false);
  if (savePromise) return savePromise;
  if (!canQueue.value || !opened || pendingState.value || composingInput) return Promise.resolve(false);
  // The same scheduler joins manual saves, Ctrl+S and automatic write-back.
  // A manual save of a clean editor still obtains an actual file receipt.
  if (!autosave.state().pending) autosave.changed(++localEpoch);
  manualSavePending = true;
  const request = autosave.saveNow();
  savePromise = request;
  void request.finally(() => { if (savePromise === request) savePromise = null; manualSavePending = false; });
  return request;
}
async function performSave(): Promise<boolean> {
  if (props.viewOnly) return false;
  if (!opened || disposed || leaseFenced.value || leasePaused.value) return false;
  busy.value = true; saveRequested.value = true;
  const epoch = lifecycle;
  try {
    const receipt = await officeApi.save(opened!.session.id, randomUUID());
    if (!current(epoch)) return false;
    if (receipt.ok !== true || !receipt.session || !matchesBinding(receipt.session, epoch)) throw new Error(t("officeEditor.savingUnknown"));
    if (flight?.command.operation === "host.export" && flight.completion && !await flight.completion) throw new Error(t(dirty.value && !session.value?.blocked && session.value?.saveState !== "error" ? "officeEditor.newerChanges" : "officeEditor.savingUnknown"));
    if (!current(epoch)) return false;
    if (!await readStatus() || !current(epoch) || session.value?.saveState !== "saved" || dirty.value || session.value?.pending || session.value?.blocked || pendingState.value || stateUnconfirmed) throw new Error(t("officeEditor.savingUnknown"));
    return true;
  } catch (error) { if (current(epoch)) failure(error); return false; }
  finally { if (current(epoch)) { busy.value = false; saveRequested.value = false; } }
}
async function prepareClose(): Promise<boolean> {
  if (startupUnconfirmed.value || bootstrapReleasePending.value || startupReleasing.value) { failed.value = t("officeEditor.releaseUnknown"); return false; }
  if (versionRestoring.value || syncFrozen.value || reloadRequested.value || replacement.value || session.value?.batch) { failed.value = t("officeEditor.sync.waitToClose"); return false; }
  if (!opened) return !loading.value;
  if (busy.value || flight || pendingState.value || closing.value || session.value?.pending || session.value?.blocked) { failed.value = t("officeEditor.waitToClose"); return false; }
  if (dirty.value || stateUnconfirmed) { failed.value = t("officeEditor.saveToClose"); return false; }
  closing.value = true; clearTimeout(pollTimer);
  const epoch = lifecycle;
  try {
    await officeApi.close(opened.session.id);
    if (!current(epoch)) return false;
    if (session.value) session.value = { ...session.value, closed: true, ready: false };
    stop(); return true;
  } catch (error) {
    if (current(epoch)) {
      failure(error);
      try { await refreshStatus(); if (current(epoch) && !session.value?.closed && !session.value?.blocked) { closing.value = false; if (deferred) { const pending = deferred; deferred = null; dispatch(pending, epoch); } else schedule(0); } }
      catch { /* An unknown close remains paused without replacing the editor. */ }
    }
    return false;
  }
}
function beforeUnload(event: BeforeUnloadEvent) {
  if (startupUnconfirmed.value || bootstrapReleasePending.value || startupReleasing.value || versionRestoring.value || syncFrozen.value || replacement.value || reloadRequested.value || dirty.value || flight || busy.value || pendingState.value || stateUnconfirmed || session.value?.pending) { event.preventDefault(); event.returnValue = ""; return; }
  const binding = opened, epoch = lifecycle;
  // Another document may veto the same navigation after our handler runs.
  // Defer only the best-effort beacon until all unload handlers have voted.
  if (binding) queueMicrotask(() => {
    if (event.defaultPrevented || !current(epoch) || syncFrozen.value || replacement.value || dirty.value || flight || busy.value || pendingState.value || stateUnconfirmed || session.value?.pending || session.value?.closed || session.value?.blocked) return;
    try { detachOfficeView(binding, instanceId); } catch { /* The next page offers explicit clean-session recovery if cleanup is unconfirmed. */ }
  });
}
function stop() {
  if (disposed) return;
  unlockInput(flight); clearTimeout(flight?.timer); clearTimeout(pollTimer); clearInterval(statusTimer); clearTimeout(bootstrapTimer);
  clearInterval(heartbeatTimer); heartbeatController?.abort(); stopInputObserver?.(); autosave.dispose();
  startupFinished(); startupController.abort();
  flight?.settle?.(false);
  disposed = true; lifecycle += 1;
  window.removeEventListener("message", message); window.removeEventListener("beforeunload", beforeUnload);
  removeNavigationGuard?.(); releaseView?.();
  removeSyncObserver?.();
  try { editor?.destroyEditor(); } catch { /* Never report an unconfirmed save on teardown. */ }
  try { retainedCanvas.value?.editor.destroyEditor(); } catch { /* Both frames are owned by this component. */ }
  retainedCanvas.value = null;
  editor = null;
}
function cleanForReconnect(): boolean {
  return !versionRestoring.value && !syncFrozen.value && !replacement.value && !session.value?.batch && cleanLocalEditor();
}
function resetBinding(preserveCanvas = false) {
  selectionNotice.value = ""; selectionError.value = false;
  clearTimeout(pollTimer); clearInterval(statusTimer); clearTimeout(bootstrapTimer);
  clearInterval(heartbeatTimer); heartbeatController?.abort(); heartbeatController = null; lastHeartbeatAt = 0; leasePaused.value = false; leaseFenced.value = false;
  stopInputObserver?.(); stopInputObserver = undefined; composingInput = false;
  startupFinished(); startupController.abort(); startupController = new AbortController(); statusRead = null;
  unlockInput(flight); clearTimeout(flight?.timer); flight?.settle?.(false);
  lifecycle += 1; flight = null; inFlight.value = false; settlingExport.value = false; deferred = null;
  releaseView?.(); releaseView = undefined;
  if (preserveCanvas && editor && session.value && !retainedCanvas.value) retainedCanvas.value = { editor, session: session.value, version: mountVersion.value };
  else { try { editor?.destroyEditor(); } catch { /* No save is inferred from teardown. */ } }
  editor = null; bridgeSource = null; editorReady.value = false; bridgeReady.value = false;
  registering = false; polling = false; canvasMounted = false; bootstrapReleasing = false;
  startupReleasing.value = false; bootstrapReleasePending.value = false; startupUnconfirmed.value = false;
  opened = null; bootstrapClaim = null; session.value = null; pendingState.value = 0;
  stateUnconfirmed = false; commandFailure = null; commandNotice = null; readNotice = ""; recoveryFlight = null; recoveryRead = null; recoveryReadAttempt = null; transportNotice = ""; pollFailures = 0; settledExports.clear();
  capabilities.value = {}; activity.value = { version: 0, items: [] }; mountVersion.value += 1;
  const pane = { version: mountVersion.value, id: mountId.value };
  canvasPanes.value = retainedCanvas.value ? [...canvasPanes.value.filter(item => item.version === retainedCanvas.value!.version), pane] : [pane];
}
async function releaseBootstrap(claim: OfficeOpen): Promise<boolean> {
  const ownsCurrent = bootstrapClaim === claim || opened === claim, releaseEpoch = lifecycle;
  if (!ownsCurrent && (opened?.session.id === claim.session.id || officeViewMounted(claim.session.id))) return false;
  if (!claim.instanceOwned || !claim.bootstrapToken || claim.session.ready || claim.session.canvasMounted || claim.session.dirty || claim.session.pending || claim.session.blocked) return false;
  if (ownsCurrent && (canvasMounted || dirty.value || stateUnconfirmed || pendingState.value || flight)) return false;
  // Freeze local input but retain the frame until the host confirms this
  // exact unused claim can be retired. A failed release must keep the canvas.
  if (ownsCurrent) { startupReleasing.value = true; applyStageLock(); }
  try {
    if (ownsCurrent && editor && !disposed) {
      if (!await flushState(releaseEpoch) || releaseEpoch !== lifecycle || canvasMounted || dirty.value || stateUnconfirmed || pendingState.value || flight) return false;
    }
    if (ownsCurrent) bootstrapReleasing = true;
    const reply = await retryOfficeStartup("recovering", signal => officeApi.release(claim, instanceId, signal), { timeoutMs: 310_000 });
    if (reply.ok !== true) return false;
    // A late result belonging to an abandoned lifecycle may release only its
    // own host claim. It must never alter the current frame or local locks.
    if (!ownsCurrent || releaseEpoch !== lifecycle || (bootstrapClaim !== claim && opened !== claim)) return true;
    // The host's confirmed retirement must not turn into an editable zombie
    // frame if onDocumentReady arrived late. Preserve unexpected local input
    // without permitting further edits against the closed host session.
    if (dirty.value || stateUnconfirmed || pendingState.value || flight) {
      if (session.value?.id === claim.session.id) session.value = { ...session.value, closed: true, blocked: true };
      return false;
    }
    canvasMounted = false; editorReady.value = false;
    try { editor?.destroyEditor(); } catch { /* The host already retired only the unused claim. */ }
    editor = null;
    if (bootstrapClaim === claim) { releaseView?.(); releaseView = undefined; bootstrapClaim = null; opened = null; session.value = null; }
    bootstrapReleasePending.value = false;
    return true;
  } catch { return false; }
  finally { if (ownsCurrent && releaseEpoch === lifecycle) { bootstrapReleasing = false; startupReleasing.value = false; applyStageLock(); } }
}
async function failBootstrap(error: unknown, epoch: number) {
  if (disposed || epoch !== lifecycle) return;
  loading.value = false; failure(error); startupFinished();
  const claim = bootstrapClaim;
  if (replacement.value) {
    // A replacement is already the authoritative saved version. Keep its claim
    // and the previous read-only pane; releasing it would leave replacedBy
    // pointing at a closed session and could strand a subsequent retry.
    syncError.value = t("officeEditor.sync.failed");
    return;
  }
  if (canvasMounted || dirty.value || stateUnconfirmed || pendingState.value || flight) return;
  if (claim && !await releaseBootstrap(claim)) {
    if (!disposed && epoch === lifecycle) {
      bootstrapReleasePending.value = true;
      failed.value = t("officeEditor.releaseUnknown");
    }
    return;
  }
  // The POST may have claimed an instance even when its response was lost.
  // Retain this component and nonce, so Retry can recover that exact claim.
  if (!claim && startupUnconfirmed.value) return;
  if (!disposed && epoch === lifecycle) { releaseView?.(); releaseView = undefined; emit("openFailed", failed.value); }
}
async function retryStartup() {
  if (disposed || loading.value || opening || registering || startupReleasing.value) return;
  const epoch = lifecycle;
  if (opened && (canvasMounted || dirty.value || stateUnconfirmed || pendingState.value || flight || session.value?.ready || session.value?.canvasMounted)) {
    // Retry only the idempotent bridge handshake; keep the same native canvas,
    // pending operation ledger and any manual text.
    loading.value = true; failed.value = ""; watchStartupStage("bridge", epoch);
    bridgeSource?.postMessage({ protocol: "pogo-office-lab/v1", type: "retry-bootstrap", sessionId: opened.session.id, documentId: opened.session.documentId, token: opened.bridgeToken }, location.origin);
    if (bridgeSource && editorReady.value) await bindBridge(epoch);
    return;
  }
  if (bootstrapReleasePending.value && bootstrapClaim) {
    loading.value = true; startupStage("recovering");
    const released = await releaseBootstrap(bootstrapClaim);
    if (disposed || epoch !== lifecycle) return;
    loading.value = false; startupFinished();
    if (!released) { failed.value = t("officeEditor.releaseUnknown"); return; }
    failed.value = "";
    await openEditor(false); return;
  }
  failed.value = "";
  await openEditor(false);
}
async function reconnectMissing(epoch: number): Promise<boolean> {
  if (!current(epoch) || !opened) return false;
  const previous = opened.session, key = `${previous.bootEpoch}:${previous.id}`;
  connectionLost.value = true; bridgeReady.value = false; clearTimeout(pollTimer); clearInterval(statusTimer);
  if (!cleanForReconnect()) { loading.value = false; failed.value = t("officeEditor.connectionPreserved"); return false; }
  if (restartAttempts.has(key)) { loading.value = false; failed.value = t("officeEditor.reconnectFailed"); return false; }
  restartAttempts.add(key);
  resetBinding(); dirty.value = false; connectionLost.value = false; loading.value = true;
  emit("closedPrevious", previous.id);
  await nextTick();
  if (disposed) return false;
  await openEditor(false, previous.fileRef);
  return !!opened;
}
function openEditor(useExisting = true, expectedFileRef?: string, replacementId?: string): Promise<void> {
  if (opening) return opening;
  const work = doOpenEditor(useExisting, expectedFileRef, replacementId);
  opening = work;
  void work.finally(() => { if (opening === work) opening = null; });
  return work;
}
async function doOpenEditor(useExisting: boolean, expectedFileRef?: string, replacementId?: string) {
  const epoch = lifecycle, requestedPath = workspaceFilePath(props.path);
  loading.value = true; failed.value = ""; recoverySession.value = null;
  startupStage("opening");
  let result: OfficeOpen | null = null;
  try {
    if (officePathMounted(props.path, props.conversationId)) throw new Error(t("officeEditor.duplicate"));
    const requestClaim = async (signal: AbortSignal): Promise<OfficeOpen> => {
      const supplied = suppliedClaim; suppliedClaim = undefined;
      const claim = supplied || (replacementId ? await officeApi.config(replacementId, instanceId, signal, requestedPath)
        : useExisting && props.officeSessionId ? await officeApi.config(props.officeSessionId, instanceId, signal, requestedPath)
        : await officeApi.open(requestedPath, props.conversationId, instanceId, signal));
      if (disposed || epoch !== lifecycle) await releaseBootstrap(claim);
      return claim;
    };
    startupUnconfirmed.value = true;
    const claimOptions = { signal: startupController.signal, timeoutMs: 310_000, onAttempt: startupAttempted };
    if (replacementId) result = await retryOfficeStartup("opening", requestClaim, claimOptions);
    else if (useExisting && props.officeSessionId) {
      try { result = await retryOfficeStartup("opening", requestClaim, claimOptions); }
      catch (error) {
        if ((error as { status?: number }).status !== 404 || (error as { code?: string }).code !== "DOCUMENT_NOT_FOUND") throw error;
        if (disposed || epoch !== lifecycle) return;
        emit("closedPrevious", props.officeSessionId);
        result = await retryOfficeStartup("opening", async signal => {
          const claim = await officeApi.open(requestedPath, props.conversationId, instanceId, signal);
          if (disposed || epoch !== lifecycle) await releaseBootstrap(claim);
          return claim;
        }, claimOptions);
      }
    } else result = await retryOfficeStartup("opening", requestClaim, claimOptions);
    // Retain the scoped claim before validating identity or loading scripts,
    // so every bootstrap failure can release exactly its own unused instance.
    if (disposed || epoch !== lifecycle) { await releaseBootstrap(result); return; }
    bootstrapClaim = result;
    startupUnconfirmed.value = false;
    if (result.requestedPath !== requestedPath || result.session.conversationId !== props.conversationId || !result.session.fileRef || !result.session.bootEpoch || !["docx", "xlsx"].includes(result.session.fileType) || !result.bridgeToken || !result.bootstrapToken) throw new Error(t("officeEditor.unavailable"));
    if (expectedFileRef && result.session.fileRef !== expectedFileRef) throw new Error(t("officeEditor.identityChanged"));
    if (replacementId && (!replacement.value || !officeReplacementMatches(replacement.value.previous, result.session, replacementId))) throw new Error(t("officeEditor.identityChanged"));
    if (result.instanceOwned !== true) throw Object.assign(new Error(t("officeEditor.duplicate")), { code: "EDITOR_ALREADY_CONNECTED" });
    result.bridgeURL = officeBridgeBase(result.bridgeURL);
    opened = result; session.value = result.session; dirty.value = result.session.dirty;
    if (dirty.value) autosave.changed(++localEpoch);
    lastHeartbeatAt = Date.now();
    if (result.session.leaseEpoch) { void heartbeat(epoch); heartbeatTimer = setInterval(() => void heartbeat(epoch), 10_000); }
    rememberOfficeFileIdentity(requestedPath, result.session.path, result.session.fileRef);
    activity.value = { version: 0, items: [] }; installActivity(result.session.activity);
    releaseView = registerOfficeView(result.session, requestedPath);
    emit("status", result.session);
    startupStage("engine");
    const api = await retryOfficeStartup("engine", signal => loadOfficeEngine(result!.browserURL, signal), { signal: startupController.signal, onAttempt: startupAttempted });
    if (!current(epoch)) return;
    await nextTick();
    if (!current(epoch)) return;
    const config = withOfficeEditorMode({ ...result.config, width: "100%", height: "100%", events: {
      onDocumentReady: () => {
        if (disposed || epoch !== lifecycle || !opened || session.value?.id !== opened.session.id) return;
        canvasMounted = true; editorReady.value = true; clearTimeout(bootstrapTimer);
        if (root.value) stopInputObserver = observeOfficeInput(root.value, { activity: () => { lastInputAt = Date.now(); }, composition: active => { composingInput = active; }, save: () => { void save(); } });
        void officeBridge(opened!, "mounted", instanceId, {}).catch(error => { if (current(epoch)) failure(error); });
        if (bootstrapReleasing || bridgeReady.value) return;
        watchStartupStage("bridge", epoch);
        void bindBridge(epoch);
      },
      onDocumentStateChange: (event: { data: boolean }) => {
        if (props.viewOnly || disposed || epoch !== lifecycle || !opened || leaseFenced.value) return;
        // Engine "clean" means received by the editing service, not saved to
        // the workspace. Only the canonical export receipt clears dirty.
        if (event.data === false) { autosave.flush(); return; }
        dirty.value = true; autosave.changed(++localEpoch); void reportState(true, epoch);
      },
      onDownloadAs: (event: { data: { url?: string; fileType?: string } }) => { void exported(event.data, epoch); },
      onError: (event: { data?: { errorDescription?: string } }) => { if (current(epoch)) void failBootstrap(new Error(event.data?.errorDescription || t("officeEditor.unavailable")), epoch); },
    } }, props.viewOnly === true);
    watchStartupStage("document", epoch);
    editor = new api.DocEditor(mountId.value, config);
    statusTimer = setInterval(() => {
      if (!current(epoch) || closing.value) return;
      void readStatus().then(confirmed => {
        if (!confirmed || !current(epoch)) return;
        connectedTransport();
        if (!bridgeReady.value && editorReady.value && bridgeSource) void bindBridge(epoch);
      }).catch(error => { if (current(epoch)) interrupted(error); });
    }, 3000);
  } catch (error) {
    if (!disposed && epoch === lifecycle) {
      loading.value = false; failure(error);
      const code = (error as { code?: string }).code;
      const status = (error as { status?: number }).status;
      // An explicit refusal proves no new claim was issued. Transport failures
      // do not: keep the nonce for an idempotent recovery attempt.
      if (!result && status && ![408, 429, 502, 503, 504].includes(status)) startupUnconfirmed.value = false;
      if (!result && ["OFFICE_CONFIG_INVALID", "OFFICE_OPEN_TIMEOUT"].includes(code || "")) startupUnconfirmed.value = false;
      if (!opened && !result && !startupUnconfirmed.value && ["EDITOR_ALREADY_CONNECTED", "DOCUMENT_ALREADY_OPEN", "EDITOR_FENCED"].includes(code || "")) {
        startupFinished(); emit("viewSaved"); return;
      }
      if (!opened && !result && !officePathMounted(props.path, props.conversationId) && (code === "EDITOR_ALREADY_CONNECTED" || code === "DOCUMENT_ALREADY_OPEN" || (error as { status?: number }).status === 409)) {
        try {
          const listed = await officeApi.list(props.conversationId, undefined, requestedPath);
          if (!disposed && epoch === lifecycle) {
            recoverySession.value = listed.requestedPath === requestedPath && listed.fileRef ? listed.sessions.find(value => !value.closed && value.instanceBound && value.fileRef === listed.fileRef && value.conversationId === props.conversationId) || null : null;
            if (recoverySession.value) {
              rememberOfficeFileIdentity(requestedPath, recoverySession.value.path, listed.fileRef!);
              if (officeViewMounted(recoverySession.value.id)) {
                const existingPath = recoverySession.value.path; recoverySession.value = null;
                emit("focusExisting", existingPath); return;
              }
              failed.value = t("officeEditor.duplicate");
            }
          }
        } catch { /* Failed authorization does not produce a recovery authority. */ }
      }
      if (!recoverySession.value) await failBootstrap(error, epoch);
    }
  }
}
async function recoverCleanSession() {
  const old = recoverySession.value;
  if (!old || !canRecover.value || disposed) return false;
  recovering.value = true;
  const epoch = lifecycle;
  try {
    const { session: fresh } = await officeApi.status(old.id);
    if (disposed || epoch !== lifecycle) return false;
    if (fresh.id !== old.id || fresh.documentId !== old.documentId || fresh.path !== old.path || fresh.fileRef !== old.fileRef || fresh.bootEpoch !== old.bootEpoch || fresh.conversationId !== props.conversationId || fresh.generation !== old.generation) { recoverySession.value = null; failed.value = t("officeEditor.recoveryUnavailable"); return false; }
    if (fresh.dirty || fresh.pending !== false || fresh.blocked || fresh.closed) { recoverySession.value = fresh; failed.value = t("officeEditor.recoveryUnavailable"); return false; }
    const access = await officeApi.access(workspaceFilePath(props.path), props.conversationId);
    if (disposed || epoch !== lifecycle || access.writer?.id !== old.id || !access.access.canTakeover || access.writer.dirty) return false;
    suppliedClaim = await officeApi.takeover(workspaceFilePath(props.path), props.conversationId, instanceId, access);
    if (disposed || epoch !== lifecycle) { await officeApi.release(suppliedClaim, instanceId).catch(() => {}); suppliedClaim = undefined; return false; }
    if (disposed || epoch !== lifecycle) return false;
    emit("closedPrevious", old.id);
    await openEditor(false, old.fileRef);
    return !!opened;
  } catch (error) { if (!disposed) failure(error); return false; }
  finally { if (!disposed) recovering.value = false; }
}
onMounted(async () => {
  window.addEventListener("message", message); window.addEventListener("beforeunload", beforeUnload);
  removeSyncObserver = observeOfficeSync(value => {
    if (!opened || disposed || value.id !== opened.session.id || value.conversationId !== opened.session.conversationId || value.path !== opened.session.path || value.fileType !== opened.session.fileType) return;
    const epoch = lifecycle;
    void refreshStatus().catch(error => { if (current(epoch)) failure(error); });
  });
  removeNavigationGuard = registerStudioNavigationGuard(async () => {
    if (dirty.value || flight || pendingState.value || stateUnconfirmed) { failed.value = t("officeEditor.unsavedLeave"); return false; }
    const closed = await prepareClose();
    if (closed) emit("closed");
    return closed;
  });
  await openEditor();
});
onBeforeUnmount(() => {
  const abandoned = canvasMounted && opened && !versionRestoring.value && !syncFrozen.value && !replacement.value && !session.value?.batch && !session.value?.closed && !session.value?.blocked && !session.value?.pending && !dirty.value && !busy.value && !flight && !pendingState.value && !stateUnconfirmed ? opened.session.id : null;
  let detached = false;
  if (abandoned && opened) { try { detached = detachOfficeView(opened, instanceId); } catch { /* The account-scoped close remains the fallback. */ } }
  const unusedClaim = bootstrapClaim && !canvasMounted && !replacement.value ? bootstrapClaim : null;
  stop();
  if (unusedClaim) void releaseBootstrap(unusedClaim);
  if (!unusedClaim && abandoned && !detached) void officeApi.close(abandoned).catch(() => {});
});
defineExpose({ prepareClose, refreshStatus, save });
</script>

<template>
  <section ref="root" class="office-editor" :class="{ immersive }" :data-office-session="session?.id" :data-office-ready="ready" :data-office-dirty="dirty" :data-office-sync-state="syncState" :data-office-input-frozen="syncFrozen || leasePaused || leaseFenced" :data-office-autosave="autoSaveEnabled" :data-office-mode="viewOnly ? 'view' : 'edit'">
    <header class="office-toolbar">
      <el-popover placement="bottom-start" trigger="click" :width="360" popper-class="office-document-popover">
        <template #reference>
          <button type="button" class="office-action document-details-toggle" :title="t('officeEditor.details')" :aria-label="t('officeEditor.details')"><el-icon><Document /></el-icon><span>{{ t('officeEditor.document') }}</span><el-icon class="details-chevron"><ArrowDown /></el-icon></button>
        </template>
        <div class="office-document-details">
          <strong>{{ t('officeEditor.details') }}</strong>
          <slot name="document-details"><code class="document-path">{{ path }}</code></slot>
          <p>{{ saveLabel }}<span v-if="session"> · {{ t('officeEditor.version', { version: session.revision }) }}</span></p>
          <p>{{ ready ? t('officeEditor.sameDocument') : t('officeEditor.waiting') }}</p>
          <p class="history-note">{{ t('officeEditor.undoNote') }}</p>
          <label v-if="!viewOnly" class="autosave-toggle"><input v-model="autoSaveEnabled" type="checkbox" :disabled="leaseFenced" />{{ t('officeEditor.access.autosaveToggle') }}</label>
          <p class="history-note">{{ t('officeEditor.access.autosave') }}</p>
          <button type="button" class="office-action preview-action" @click="emit('preview')">{{ t('officeEditor.preview') }}</button>
        </div>
      </el-popover>
      <span class="document-state" :class="{ dirty, saving, waiting: loading || !ready, bad: !recoveringOperation && !connectionLost && (failed || session?.saveState === 'error') }" :title="failed || (saving ? t('officeEditor.saveHelp') : saveLabel)" role="status" aria-live="polite">{{ stateLabel }}</span>
      <span v-if="viewOnly" class="office-view-only">{{ t('officeEditor.viewOnly') }}</span>
      <div class="toolbar-context" :class="{ 'has-sync': !!syncNotice }">
        <el-popover v-if="syncNotice" placement="bottom-start" trigger="click" :width="360" popper-class="office-sync-popover">
          <template #reference>
            <button type="button" class="office-sync-notice" :class="{ bad: syncState === 'error' || syncState === 'conflict', synced: syncState === 'synced' }" :title="syncNotice" :aria-label="syncNotice">
              <el-icon v-if="['preparing', 'batch', 'syncing'].includes(syncState)" class="save-spinner"><Loading /></el-icon>
              <svg v-else class="sync-symbol" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path v-if="syncState === 'synced'" d="m3 8 3 3 7-7" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" /><template v-else><circle cx="8" cy="8" r="6" stroke="currentColor" stroke-width="1.3" /><path d="M8 4.5v4M8 11.5v.1" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></template></svg>
              <span class="sync-summary">{{ syncNotice }}</span>
              <el-icon class="sync-chevron"><ArrowDown /></el-icon>
            </button>
          </template>
          <div class="office-sync-details">
            <p>{{ syncNotice }}</p>
            <div class="sync-actions">
              <button v-if="session?.batch?.purpose === 'batch' && !replacement" type="button" class="office-action" :disabled="cancellingBatch" @click="cancelBatch">{{ t(cancellingBatch ? 'officeEditor.sync.cancelling' : 'officeEditor.sync.cancel') }}</button>
              <button v-if="syncState === 'error' && !flight && !pendingState && !session?.blocked" type="button" class="office-action" @click="retrySynchronization">{{ t('officeEditor.sync.retry') }}</button>
              <button v-if="newerVersion && !dirty && !syncFrozen && !reloadRequested" type="button" class="office-action" :disabled="!canLoadLatest" @click="requestReload(true)">{{ t('officeEditor.sync.loadLatest') }}</button>
              <button v-if="newerVersion && dirty && !syncFrozen && !reloadRequested && !flight && !pendingState && !session?.blocked" type="button" class="office-action discard-action" @click="discardAndReload">{{ t('officeEditor.sync.discardAction') }}</button>
            </div>
          </div>
        </el-popover>
        <span class="sync-announcement" role="status" aria-live="polite" aria-atomic="true">{{ syncNotice }}</span>
      </div>
      <OfficeActivity :items="activityItems" :has-versions="!!session?.fileRef">
        <template #versions><OfficeVersions v-if="session?.fileRef" :conversation-id="session.conversationId" :path="session.path" :file-ref="session.fileRef" :current-version="session.diskVersion" :canvas-version="session.canvasVersion" :canvas-clean="canvasAtSavedVersion" :can-restore="canRestoreVersion" :restoring="versionRestoring" :restore-version="restoreFileVersion" /></template>
      </OfficeActivity>
      <div v-if="!viewOnly" class="history-actions">
        <button type="button" class="office-action history-action" :disabled="!canQueue || capabilities.nativeUndo !== true" :title="`${t('officeEditor.undo')} · ${capabilities.nativeUndo ? t('officeEditor.undoNote') : t('officeEditor.historyUnavailable')}`" :aria-label="t('officeEditor.undo')" @click="run('history.undo')"><svg viewBox="0 0 20 20" fill="none" aria-hidden="true"><path d="M7 4 3 8l4 4M3 8h8a6 6 0 0 1 6 6v2" stroke="currentColor" stroke-width="1.65" stroke-linecap="round" stroke-linejoin="round" /></svg></button>
        <button type="button" class="office-action history-action" :disabled="!canQueue || capabilities.nativeRedo !== true" :title="`${t('officeEditor.redo')} · ${capabilities.nativeRedo ? t('officeEditor.undoNote') : t('officeEditor.historyUnavailable')}`" :aria-label="t('officeEditor.redo')" @click="run('history.redo')"><svg viewBox="0 0 20 20" fill="none" aria-hidden="true"><path d="m13 4 4 4-4 4M17 8H9a6 6 0 0 0-6 6v2" stroke="currentColor" stroke-width="1.65" stroke-linecap="round" stroke-linejoin="round" /></svg></button>
      </div>
      <button type="button" class="office-action reference-action" :disabled="!canReferenceSelection" :aria-busy="capturingSelection" :title="t(capabilities.selectionRead === true ? 'officeEditor.selection.help' : 'officeEditor.selection.unavailable')" @pointerdown.prevent @click="referenceSelection"><el-icon v-if="capturingSelection" class="save-spinner"><Loading /></el-icon><svg v-else viewBox="0 0 20 20" fill="none" aria-hidden="true"><path d="M7 4H4v12h12v-3M9 4h7v7M16 4l-9 9" stroke="currentColor" stroke-width="1.55" stroke-linecap="round" stroke-linejoin="round" /></svg>{{ t('officeEditor.selection.action') }}</button>
      <button v-if="!viewOnly" type="button" class="office-action save-action" :class="{ saving }" :disabled="!canQueue || pendingState > 0" :aria-busy="saving" :title="queued ? t('officeEditor.queueHelp') : saving ? t('officeEditor.saveHelp') : t('officeEditor.save')" @click="save"><el-icon :class="{ 'save-spinner': saving }"><Loading v-if="saving" /><DocumentChecked v-else /></el-icon>{{ queued ? t('officeEditor.queuedAction') : t('officeEditor.save') }}</button>
    </header>
    <p v-if="changedScope" class="office-notice">{{ t('officeEditor.changedScope') }}</p>
    <p v-if="selectionNotice" class="office-notice selection-notice" :class="{ bad: selectionError }" role="status" aria-live="polite">{{ selectionNotice }}</p>
    <p v-if="session?.versionError" class="office-notice" role="status">{{ t('officeEditor.sync.checkFailed') }} {{ session.versionError }}</p>
    <p v-if="session?.blocked || recoveringOperation" class="office-notice recovery-notice" role="status" aria-live="polite"><el-icon class="save-spinner"><Loading /></el-icon>{{ t('officeEditor.blocked') }}</p>
    <p v-if="session?.error && session.error !== failed" class="office-notice bad" role="status">{{ session.error }}</p>
    <p v-if="failed" class="office-notice bad" role="alert">
      {{ failed }}
      <span v-if="editorReady || dirty || pendingState || inFlight">{{ t('officeEditor.startup.preserved') }}</span>
      <button v-if="startupUnconfirmed || bootstrapReleasePending || (editorReady && !bridgeReady)" type="button" class="office-action startup-retry" :disabled="loading || startupReleasing" @click="retryStartup">{{ t(bootstrapReleasePending && !editorReady ? 'officeEditor.startup.releaseRetry' : 'officeEditor.startup.retryAction') }}</button>
    </p>
    <div v-if="recoverySession" class="office-recovery">
      <p>{{ t('officeEditor.recoveryHelp') }}</p>
      <button type="button" class="office-action" :disabled="!canRecover" @click="recoverCleanSession">{{ t('officeEditor.recover') }}</button>
      <span v-if="recoverySession.dirty || recoverySession.pending || recoverySession.blocked">{{ t('officeEditor.recoveryUnavailable') }}</span>
    </div>
    <div class="office-stage" :inert="startupReleasing || versionRestoring || !editorReady || session?.closed || inputLocked || syncFrozen || leasePaused || leaseFenced">
      <div v-for="pane in canvasPanes" :key="pane.version" class="office-canvas" :data-canvas-version="pane.version" :class="{ pending: !!retainedCanvas && pane.version === mountVersion }" :inert="!!retainedCanvas && pane.version === retainedCanvas.version">
        <div :id="pane.id" class="editor-mount" />
      </div>
      <div v-if="loading && !retainedCanvas" class="office-cover startup-cover" :aria-busy="loading">
        <div class="startup-emblem" aria-hidden="true"><svg viewBox="0 0 32 32" fill="none"><path d="M10 4h9l6 6v16a2 2 0 0 1-2 2H9a2 2 0 0 1-2-2V7a3 3 0 0 1 3-3Z" stroke="currentColor" stroke-width="1.5"/><path d="M19 4v7h6M12 17h8m-8 5h5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg></div>
        <strong role="status" aria-live="polite">{{ startupStageLabel }}</strong>
        <span class="startup-progress" aria-hidden="true">{{ startupProgressLabel }}</span>
        <ol class="startup-steps" aria-hidden="true"><li v-for="(step, index) in startupSteps" :key="step" :class="{ active: step === startupPhase, complete: index < startupSteps.indexOf(startupPhase) }"><span class="startup-step-dot" />{{ t(`officeEditor.startup.phases.${step}`) }}</li></ol>
        <p v-if="startupElapsed >= 15" class="startup-slow">{{ t('officeEditor.startup.slow', { stage: startupStageLabel, elapsed: startupElapsed }) }}</p>
      </div>
      <div v-else-if="editorReady && !bridgeReady && !connectionLost && !retainedCanvas" class="office-cover"><span>{{ t('officeEditor.unavailable') }}</span></div>
    </div>
  </section>
</template>

<style scoped>
.office-editor { display: flex; flex-direction: column; flex: 1; min-height: 0; height: 100%; background: var(--el-bg-color); }
.office-view-only { flex: 0 0 auto; padding: 1px 6px; border-radius: 4px; background: var(--el-fill-color-light); color: var(--el-text-color-secondary); font-size: 10px; }
.autosave-toggle { display: inline-flex; align-items: center; gap: 7px; font-size: 12px; cursor: pointer; }.autosave-toggle input { accent-color: var(--el-color-primary); }
.office-toolbar { display: flex; align-items: center; flex: 0 0 36px; box-sizing: border-box; min-width: 0; gap: 9px; padding: 3px 8px; border-bottom: 1px solid var(--el-border-color-lighter); }
.document-state { display: inline-flex; align-items: center; gap: 6px; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 11px; color: var(--el-text-color-secondary); }.document-state::before { content: ''; flex: 0 0 6px; width: 6px; height: 6px; border-radius: 50%; background: var(--el-color-success); }.document-state.waiting::before { background: var(--el-text-color-placeholder); }.document-state.dirty::before { background: var(--el-color-warning); }.document-state.bad { color: var(--el-color-danger); }.document-state.bad::before { background: var(--el-color-danger); }
.toolbar-context { display: flex; align-items: center; flex: 1 1 0; min-width: 0; }.toolbar-context.has-sync { min-width: 50px; }.office-action { display: inline-flex; align-items: center; justify-content: center; flex: 0 0 auto; gap: 5px; min-height: 28px; box-sizing: border-box; padding: 4px 9px; border: 1px solid var(--el-border-color); border-radius: 7px; background: var(--el-bg-color); color: var(--el-text-color-primary); font-size: 11px; cursor: pointer; }.office-action:disabled { opacity: .4; cursor: not-allowed; }.office-action:hover:not(:disabled) { background: var(--el-fill-color-light); }.office-action:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 2px; }.save-action { background: var(--el-color-primary); color: var(--el-color-white); border-color: var(--el-color-primary); }.save-action:hover:not(:disabled) { background: var(--el-color-primary-dark-2); }
.document-details-toggle { border-color: transparent; background: transparent; padding: 4px 6px; }.details-chevron { font-size: 10px; color: var(--el-text-color-secondary); }.history-actions { display: flex; flex: 0 0 auto; gap: 2px; padding-right: 7px; border-right: 1px solid var(--el-border-color-lighter); }.history-action { width: 28px; border-color: transparent; background: transparent; padding: 4px; font-size: 14px; }
.history-action svg { width: 18px; height: 18px; }
.reference-action svg { width: 15px; height: 15px; }.selection-notice:not(.bad) { background: var(--el-color-primary-light-9); color: var(--el-color-primary); }
.office-document-details { display: flex; flex-direction: column; gap: 9px; min-width: 0; color: var(--el-text-color-regular); font-size: 12px; }.office-document-details > strong { color: var(--el-text-color-primary); }.office-document-details p { margin: 0; line-height: 1.6; }.document-path { display: block; overflow-wrap: anywhere; font-size: 11px; }.history-note { color: var(--el-text-color-secondary); }.preview-action { align-self: flex-start; margin-top: 3px; }
.office-stage { flex: 1; min-height: 0; position: relative; overflow: hidden; background: var(--el-fill-color-light); }.editor-mount { width: 100%; height: 100%; }.office-cover { position: absolute; inset: 0; display: grid; place-items: center; padding: 24px; background: var(--el-bg-color); color: var(--el-text-color-secondary); font-size: 13px; }
.startup-cover { display: flex; flex-direction: column; justify-content: center; gap: 12px; text-align: center; }.startup-cover strong { color: var(--el-text-color-primary); font-size: 15px; font-weight: 600; }.startup-emblem { display: grid; place-items: center; width: 56px; height: 56px; margin-bottom: 4px; border: 1px solid color-mix(in srgb, var(--el-color-primary) 20%, var(--el-border-color-lighter)); border-radius: 16px; background: color-mix(in srgb, var(--el-color-primary) 7%, var(--el-bg-color)); color: var(--el-color-primary); }.startup-emblem svg { width: 32px; height: 32px; }.startup-progress { color: var(--el-text-color-secondary); font-size: 12px; font-variant-numeric: tabular-nums; }.startup-steps { display: flex; justify-content: center; gap: 16px; flex-wrap: wrap; margin: 12px 0 0; padding: 0; list-style: none; }.startup-steps li { display: inline-flex; align-items: center; gap: 6px; color: var(--el-text-color-placeholder); font-size: 11px; }.startup-step-dot { width: 6px; height: 6px; border-radius: 50%; background: var(--el-border-color); }.startup-steps .active { color: var(--el-color-primary); }.startup-steps .active .startup-step-dot { background: var(--el-color-primary); box-shadow: 0 0 0 4px color-mix(in srgb, var(--el-color-primary) 10%, transparent); }.startup-steps .complete { color: var(--el-text-color-secondary); }.startup-steps .complete .startup-step-dot { background: var(--el-color-success); }.startup-slow { max-width: 380px; margin: 8px 0 0; color: var(--el-text-color-secondary); font-size: 11px; line-height: 1.7; }.startup-retry { margin-left: 8px; padding: 3px 8px; min-height: 24px; }
.office-canvas { position: absolute; inset: 0; width: 100%; height: 100%; }.office-canvas.pending { visibility: hidden; pointer-events: none; }
.office-sync-notice { display: inline-flex; align-items: center; gap: 6px; min-width: 0; max-width: 100%; height: 28px; box-sizing: border-box; padding: 3px 6px; overflow: hidden; border: 0; border-radius: 6px; font: inherit; font-size: 11px; color: var(--el-text-color-secondary); background: transparent; cursor: pointer; }.office-sync-notice:hover { background: var(--el-fill-color-light); }.office-sync-notice:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 1px; }.sync-summary { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }.office-sync-notice > .el-icon, .sync-symbol { flex: 0 0 14px; width: 14px; height: 14px; }.office-sync-notice .sync-chevron { flex-basis: 10px; width: 10px; font-size: 10px; opacity: .65; }.office-sync-notice.bad { color: var(--el-color-warning); }.office-sync-notice.synced { color: var(--el-color-success); }
.office-sync-details { color: var(--el-text-color-regular); font-size: 12px; line-height: 1.6; }.office-sync-details p { margin: 0; overflow-wrap: anywhere; }.sync-actions { display: flex; flex-wrap: wrap; gap: 7px; }.sync-actions:not(:empty) { margin-top: 10px; }.sync-actions .discard-action { color: var(--el-color-danger); }.sync-announcement { position: absolute; width: 1px; height: 1px; padding: 0; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
.save-action.saving:disabled { opacity: 1; cursor: wait; }.save-spinner { animation: office-save-spin 1s linear infinite; }.document-state.saving::before { background: var(--el-color-primary); }
@keyframes office-save-spin { to { transform: rotate(360deg); } }
@media (prefers-reduced-motion: reduce) { .save-spinner { animation: none; } }
.office-notice { margin: 0; flex-shrink: 0; padding: 6px 10px; background: var(--el-color-warning-light-9); color: var(--el-text-color-secondary); font-size: 11px; line-height: 1.6; overflow-wrap: anywhere; }.office-notice.bad { color: var(--el-color-danger); background: var(--el-color-danger-light-9); }
.immersive .office-toolbar { padding-inline: 6px; }
@media (max-width: 420px) { .office-toolbar { gap: 5px; padding-inline: 5px; }.document-details-toggle > span { display: none; }.history-actions { padding-right: 3px; }.save-action { padding-inline: 7px; } }
.recovery-notice { display: flex; gap: 6px; align-items: center; color: var(--el-color-primary); }.office-recovery { padding: 12px; font-size: 11px; line-height: 1.6; color: var(--el-text-color-secondary); border-bottom: 1px solid var(--el-border-color); }.office-recovery p { margin: 0 0 9px; }.office-recovery span { display: block; margin-top: 8px; color: var(--el-color-warning); }
</style>
