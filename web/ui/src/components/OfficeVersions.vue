<script setup lang="ts">
import { computed, onBeforeUnmount, ref, shallowRef, watch } from "vue";
import { useI18n } from "vue-i18n";
import { officeActivityText } from "@/agent/officeActivityView";
import { officeVersionsApi, type OfficeFileVersion, type OfficeRestoreSide, type RestoreOfficeFileVersion } from "@/agent/officeVersions";
import { validOfficeVersion } from "@/agent/officeVersionSync";

const props = defineProps<{
  conversationId: string;
  path: string;
  fileRef: string;
  currentVersion?: string;
  canvasVersion?: string;
  canvasClean: boolean;
  canRestore: boolean;
  restoring: boolean;
  restoreVersion: RestoreOfficeFileVersion;
}>();
const { t, locale } = useI18n();
const records = shallowRef<OfficeFileVersion[]>([]);
const loading = ref(false);
const error = ref("");
const notice = ref("");
const nextCursor = ref("");
const retentionLimit = ref(50);
const selection = shallowRef<{ id: string; expectedVersion: string; fileRef: string; side: OfficeRestoreSide } | null>(null);
const submitting = ref(false);
let disposed = false;
let request: AbortController | undefined;
let requestSerial = 0;
const working = computed(() => submitting.value || props.restoring);
const dateFormat = computed(() => new Intl.DateTimeFormat(locale.value, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false }));
function time(record: OfficeFileVersion) {
  const value = Date.parse(record.committedAt || record.createdAt);
  return Number.isFinite(value) ? dateFormat.value.format(value) : "";
}
const text = officeActivityText;
function version(value: string) { return validOfficeVersion(value) ? value.slice(0, 8) : "—"; }
function status(record: OfficeFileVersion) {
  return record.afterVersion === props.canvasVersion ? props.canvasClean ? "canvas" : "canvasBaseline" : record.afterVersion === props.currentVersion ? "awaitingCanvas" : "historical";
}
function canSelect(record: OfficeFileVersion, side: OfficeRestoreSide = "after") {
  const target = side === "before" ? record.beforeVersion : record.afterVersion;
  return props.canRestore && !working.value && validOfficeVersion(props.currentVersion) && validOfficeVersion(target) && target !== props.currentVersion;
}
function choose(record: OfficeFileVersion, side: OfficeRestoreSide) {
  if (!canSelect(record, side)) return;
  selection.value = { id: record.id, expectedVersion: props.currentVersion!, fileRef: props.fileRef, side };
  error.value = ""; notice.value = "";
}
async function load(more = false) {
  if (disposed || !props.fileRef || (more && (!nextCursor.value || loading.value))) return;
  request?.abort();
  const controller = new AbortController(), serial = ++requestSerial;
  request = controller; loading.value = true; error.value = "";
  const scope = { conversationId: props.conversationId, path: props.path, fileRef: props.fileRef };
  const cursor = more ? nextCursor.value : undefined;
  try {
    const response = await officeVersionsApi.list(scope.conversationId, scope.path, cursor, controller.signal);
    if (disposed || serial !== requestSerial || scope.fileRef !== props.fileRef || scope.path !== props.path || scope.conversationId !== props.conversationId) return;
    if (response.ok !== true || response.fileRef !== scope.fileRef || response.path !== scope.path || !Array.isArray(response.items) || response.items.some(record => record.fileRef !== scope.fileRef || record.path !== scope.path || record.state !== "committed" || !record.diff || !Array.isArray(record.diff.changes) || !Array.isArray(record.diff.limitations))) throw new Error(t("officeVersions.identityChanged"));
    // Pages are bounded on the server. Keep a bounded list even if a future
    // server advertises a larger retention window; never poll this history.
    const merged = more ? [...records.value, ...response.items] : response.items;
    records.value = [...new Map(merged.map(record => [record.id, record])).values()].slice(0, 50);
    nextCursor.value = records.value.length < 50 ? response.nextCursor || "" : "";
    retentionLimit.value = Math.max(1, Math.min(50, response.retention?.maxVersions || 50));
  } catch (cause) {
    if (!disposed && serial === requestSerial && !controller.signal.aborted) error.value = t("officeVersions.failed", { message: cause instanceof Error ? cause.message : String(cause) });
  } finally {
    if (!disposed && serial === requestSerial) { loading.value = false; request = undefined; }
  }
}
async function restore() {
  const chosen = selection.value;
  if (!chosen || !props.canRestore || working.value || chosen.expectedVersion !== props.currentVersion || chosen.fileRef !== props.fileRef) return;
  submitting.value = true; error.value = ""; notice.value = "";
  try {
    // The editor owns the input/save barrier and the authenticated commit.
    // Closing this panel does not cancel a server-side mutation or its recovery.
    const response = await props.restoreVersion(chosen.id, chosen.expectedVersion, chosen.fileRef, chosen.side);
    if (disposed) return;
    if (response.ok !== true || response.state !== "committed") throw new Error(t("officeVersions.restoreUnknown"));
    selection.value = null;
    notice.value = t("officeVersions.restored");
    await load();
  } catch (cause) {
    if (disposed) return;
    const code = (cause as { code?: string }).code;
    error.value = ["FILE_CHANGED", "FILE_IDENTITY_CHANGED"].includes(code || "") ? t("officeVersions.restoreChanged") : cause instanceof Error ? cause.message : t("officeVersions.restoreUnknown");
    selection.value = null;
  } finally { if (!disposed) submitting.value = false; }
}
watch(() => [props.conversationId, props.path, props.fileRef] as const, () => {
  selection.value = null; records.value = []; nextCursor.value = ""; error.value = ""; notice.value = "";
  request?.abort(); ++requestSerial; loading.value = false;
  if (props.fileRef) void load();
}, { immediate: true });
watch(() => props.currentVersion, (value, previous) => {
  if (value === previous) return;
  selection.value = null;
  if (props.fileRef && !submitting.value) void load();
});
watch(() => props.canRestore, value => { if (!value && !working.value) selection.value = null; });
onBeforeUnmount(() => { disposed = true; ++requestSerial; request?.abort(); });
</script>

<template>
  <div class="office-versions" :aria-busy="loading || working">
    <div class="versions-intro"><p>{{ t('officeVersions.description') }}</p><button type="button" class="versions-refresh" :disabled="loading || working || !fileRef" :aria-label="t('officeVersions.refresh')" :title="t('officeVersions.refresh')" @click="load()"><svg viewBox="0 0 18 18" fill="none" aria-hidden="true"><path d="M14.5 7A5.7 5.7 0 1 0 14 13M14.5 3v4h-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" /></svg></button></div>
    <p v-if="error" class="versions-message error" role="alert">{{ error }}</p>
    <p v-if="notice" class="versions-message success" role="status">{{ notice }}</p>
    <p v-if="working" class="versions-message" role="status">{{ t('officeVersions.restoring') }}</p>
    <p v-else-if="records.length && !canRestore" class="versions-restore-help">{{ t('officeVersions.restoreBlocked') }}</p>
    <p v-if="!fileRef" class="versions-empty">{{ t('officeVersions.noIdentity') }}</p>
    <p v-else-if="loading && !records.length" class="versions-empty" role="status">{{ t('officeVersions.loading') }}</p>
    <p v-else-if="!records.length && !error" class="versions-empty">{{ t('officeVersions.empty') }}</p>
    <ol v-if="records.length" class="versions-list">
      <li v-for="record in records" :key="record.id" class="version-card" :class="{ current: status(record) === 'canvas', confirming: selection?.id === record.id }">
        <div class="version-heading"><span class="version-engine" :class="record.engine"><svg viewBox="0 0 18 18" fill="none" aria-hidden="true"><path v-if="record.engine === 'script'" d="m6 5-4 4 4 4m6-8 4 4-4 4m-2-10-2 12" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round" /><path v-else-if="record.engine === 'restore'" d="M3 8a6 6 0 1 1 1 5M3 3v5h5M9 5v4l3 2" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round" /><path v-else d="M5 2h6l3 3v11H4V3a1 1 0 0 1 1-1Zm5 0v4h4M6.5 9h5m-5 3h5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round" /></svg>{{ t(`officeVersions.engine.${record.engine}`) }}</span><span class="version-actor">{{ t(`officeVersions.${record.engine === 'native' ? 'savedBy' : 'actor'}.${record.actor}`) }}</span><time v-if="time(record)">{{ time(record) }}</time></div>
        <div class="version-state"><span class="saved-dot" /><span>{{ t('officeVersions.saved') }}</span><span class="canvas-state" :class="status(record)">{{ t(`officeVersions.${status(record)}`) }}</span></div>
        <p class="version-summary">{{ record.diff.coverage === 'unsupported' ? t('officeVersions.unsupported') : record.diff.changeCount === 0 ? t('officeVersions.noScopedChanges') : t('officeVersions.changes', { count: record.diff.changeCount }) }}</p>
        <div class="version-coverage"><strong>{{ t(`officeVersions.coverage.${record.diff.coverage}`) }}</strong><span>{{ t('officeVersions.textOnly') }}</span><span v-if="record.diff.limitations[0]">{{ text(record.diff.limitations[0], 240) }}</span><span v-if="record.diff.truncated" class="coverage-excerpt">{{ t('officeVersions.truncated') }}</span></div>
        <details class="version-diff">
          <summary>{{ t('officeVersions.compare') }}<svg viewBox="0 0 12 12" fill="none" aria-hidden="true"><path d="m4 2 4 4-4 4" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" /></svg></summary>
          <p class="diff-coverage">{{ t(`officeVersions.coverage.${record.diff.coverage}`) }}</p>
          <div v-for="(change, index) in record.diff.changes.slice(0, 6)" :key="index" class="version-change"><code v-if="change.sheet || change.address" class="change-location">{{ text(change.sheet, 100) }}{{ change.sheet && change.address ? ' · ' : '' }}{{ text(change.address, 100) }}</code><div v-if="change.before !== undefined" class="change-value before"><span aria-hidden="true">−</span><span class="sr-only">{{ t('officeVersions.before') }}: </span><span>{{ text(change.before, 500) || t('officeVersions.emptyValue') }}</span></div><div v-if="change.after !== undefined" class="change-value after"><span aria-hidden="true">+</span><span class="sr-only">{{ t('officeVersions.after') }}: </span><span>{{ text(change.after, 500) || t('officeVersions.emptyValue') }}</span></div></div>
          <p v-if="record.diff.truncated || record.diff.changes.length > 6" class="diff-limit">{{ t('officeVersions.excerptCount', { shown: Math.min(record.diff.changes.length, 6), count: record.diff.changeCount }) }}</p>
          <p class="diff-limit">{{ t('officeVersions.textOnly') }}</p>
          <ul v-if="record.diff.limitations.length" class="diff-limitations" :aria-label="t('officeVersions.limitations')"><li v-for="(limitation, index) in record.diff.limitations.slice(0, 6)" :key="index">{{ text(limitation, 240) }}</li></ul>
          <div class="version-metadata"><span>{{ t('officeVersions.versionPair') }}</span><code :title="`${record.beforeVersion} → ${record.afterVersion}`">{{ version(record.beforeVersion) }} → {{ version(record.afterVersion) }}</code><span>{{ t('officeVersions.operation') }}</span><code :title="record.operationId">{{ text(record.operationId, 56) }}</code><template v-if="record.executionId"><span>{{ t('officeVersions.execution') }}</span><code>{{ text(record.executionId, 56) }}</code></template><template v-if="record.scriptDigest"><span>{{ t('officeVersions.script') }}</span><code :title="record.scriptDigest">{{ text(record.scriptDigest, 16) }}</code></template><template v-if="record.restoredFrom"><span>{{ t('officeVersions.restoredFrom') }}</span><code>{{ text(record.restoredFrom, 56) }}</code></template></div>
          <p v-if="record.summary" class="version-intent"><span>{{ t('officeVersions.intent') }}：</span>{{ text(record.summary, 240) }}</p>
        </details>
        <div v-if="selection?.id === record.id" class="restore-confirm" role="group" :aria-label="t(selection.side === 'before' ? 'officeVersions.restoreBeforeTitle' : 'officeVersions.restoreTitle')"><strong>{{ t(selection.side === 'before' ? 'officeVersions.restoreBeforeTitle' : 'officeVersions.restoreTitle') }}</strong><p>{{ t('officeVersions.restoreDescription') }}</p><div class="restore-actions"><button type="button" class="version-button" :disabled="working" @click="selection = null">{{ t('officeVersions.cancel') }}</button><button type="button" class="version-button primary" :disabled="working || !canRestore || selection.expectedVersion !== currentVersion" @click="restore">{{ t(working ? 'officeVersions.restoring' : 'officeVersions.confirm') }}</button></div></div>
        <div v-else class="version-restore-options">
          <button v-if="validOfficeVersion(record.beforeVersion) && record.beforeVersion !== currentVersion" type="button" class="version-restore" :disabled="!canSelect(record, 'before')" :title="canSelect(record, 'before') ? t('officeVersions.restoreDescription') : t('officeVersions.restoreBlocked')" @click="choose(record, 'before')"><svg viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M3 7a5 5 0 1 1 .6 4M3 3v4h4" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round" /></svg>{{ t('officeVersions.restoreBefore') }}</button>
          <button v-if="validOfficeVersion(record.afterVersion) && record.afterVersion !== currentVersion" type="button" class="version-restore" :disabled="!canSelect(record)" :title="canSelect(record) ? t('officeVersions.restoreDescription') : t('officeVersions.restoreBlocked')" @click="choose(record, 'after')">{{ t('officeVersions.restore') }}</button>
        </div>
      </li>
    </ol>
    <button v-if="nextCursor" type="button" class="versions-more" :disabled="loading || working" @click="load(true)">{{ t(loading ? 'officeVersions.loading' : 'officeVersions.loadMore') }}</button>
    <footer class="versions-footer">{{ t('officeVersions.retention', { count: retentionLimit }) }}</footer>
  </div>
</template>

<style scoped>
.office-versions { display: flex; flex: 1 1 auto; flex-direction: column; min-height: 0; overflow-y: auto; overscroll-behavior: contain; scrollbar-width: thin; }
.versions-intro { display: flex; align-items: center; gap: 9px; padding: 0 14px 10px; }.versions-intro p { flex: 1; margin: 0; color: var(--el-text-color-secondary); font-size: 11px; line-height: 1.6; }.versions-refresh { flex: 0 0 25px; display: grid; place-items: center; width: 25px; height: 25px; padding: 5px; border: 0; border-radius: 6px; background: transparent; color: var(--el-text-color-secondary); cursor: pointer; }.versions-refresh svg { width: 16px; height: 16px; }.versions-refresh:hover:not(:disabled) { background: var(--el-fill-color); }
.versions-list { margin: 0; padding: 0 14px; list-style: none; }.version-card { min-width: 0; padding: 12px 0; }.version-card + .version-card { border-top: 1px solid var(--el-border-color-lighter); }.version-heading { display: flex; align-items: center; gap: 7px; font-size: 11px; }.version-engine { display: inline-flex; align-items: center; gap: 5px; color: var(--el-text-color-primary); font-weight: 600; }.version-engine svg { width: 15px; height: 15px; color: var(--el-color-primary); }.version-engine.script svg { color: var(--el-color-success); }.version-engine.restore svg { color: var(--el-color-warning); }.version-actor { color: var(--el-text-color-secondary); font-size: 10px; }.version-heading time { margin-left: auto; color: var(--el-text-color-placeholder); font-size: 10px; font-variant-numeric: tabular-nums; }
.version-state { display: flex; align-items: center; gap: 5px; margin-top: 7px; font-size: 10px; color: var(--el-text-color-secondary); }.saved-dot { width: 5px; height: 5px; flex: 0 0 5px; border-radius: 50%; background: var(--el-color-success); }.canvas-state { margin-left: auto; }.canvas-state.canvas { color: var(--el-color-success); }.canvas-state.awaitingCanvas { color: var(--el-color-warning); }.version-summary { margin: 7px 0 0; color: var(--el-text-color-regular); font-size: 11px; line-height: 1.6; }
.version-coverage { display: flex; flex-direction: column; gap: 4px; margin-top: 7px; padding: 8px 9px; border-radius: 7px; background: var(--el-fill-color-light); color: var(--el-text-color-secondary); font-size: 10px; line-height: 1.65; overflow-wrap: anywhere; }.version-coverage strong { font-weight: 600; color: var(--el-text-color-regular); }.version-coverage .coverage-excerpt { color: var(--el-color-warning); }.version-diff { margin-top: 6px; font-size: 10px; }.version-diff > summary { display: inline-flex; align-items: center; gap: 3px; list-style: none; color: var(--el-color-primary); cursor: pointer; }.version-diff > summary::-webkit-details-marker { display: none; }.version-diff > summary svg { width: 10px; height: 10px; transition: transform .15s ease; }.version-diff[open] > summary svg { transform: rotate(90deg); }.diff-coverage, .diff-limit, .diff-limitations, .version-intent { color: var(--el-text-color-secondary); font-size: 10px; line-height: 1.6; overflow-wrap: anywhere; }.diff-coverage { margin: 8px 0; }.diff-limit { margin: 7px 0; }.diff-limitations { padding-left: 15px; }.version-change { margin-top: 7px; }.change-location { display: block; margin-bottom: 4px; color: var(--el-text-color-secondary); overflow-wrap: anywhere; }.change-value { display: flex; align-items: baseline; gap: 7px; padding: 6px 8px; border-radius: 6px; margin-top: 3px; line-height: 1.6; white-space: pre-wrap; overflow-wrap: anywhere; }.change-value > span:first-child { flex: 0 0 8px; font-weight: 600; }.change-value > span:last-child { min-width: 0; flex: 1; }.before { background: color-mix(in srgb, var(--el-color-danger) 6%, var(--el-bg-color)); color: var(--el-text-color-secondary); }.before > span:first-child { color: var(--el-color-danger); }.after { background: color-mix(in srgb, var(--el-color-success) 9%, var(--el-bg-color)); color: var(--el-text-color-primary); }.after > span:first-child { color: var(--el-color-success); }.version-metadata { display: grid; grid-template-columns: auto minmax(0, 1fr); gap: 5px 12px; padding: 8px; border-radius: 7px; background: var(--el-fill-color-light); color: var(--el-text-color-secondary); font-size: 10px; }.version-metadata code { overflow-wrap: anywhere; }.version-intent { margin-bottom: 0; }
.version-restore-options { display: flex; gap: 14px; flex-wrap: wrap; }.version-restore { display: inline-flex; align-items: center; gap: 4px; padding: 4px 0; margin-top: 7px; border: 0; background: transparent; color: var(--el-color-primary); font: inherit; font-size: 10px; cursor: pointer; }.version-restore svg { width: 13px; height: 13px; }.restore-confirm { padding: 10px; margin-top: 9px; border: 1px solid color-mix(in srgb, var(--el-color-primary) 22%, var(--el-border-color)); border-radius: 9px; background: color-mix(in srgb, var(--el-color-primary) 4%, var(--el-bg-color)); }.restore-confirm strong { font-size: 11px; }.restore-confirm p { margin: 6px 0 10px; color: var(--el-text-color-secondary); font-size: 10px; line-height: 1.65; }.restore-actions { display: flex; justify-content: flex-end; gap: 6px; }.version-button { border: 1px solid var(--el-border-color); border-radius: 6px; padding: 5px 8px; background: var(--el-bg-color); color: var(--el-text-color-primary); font: inherit; font-size: 10px; cursor: pointer; }.version-button.primary { border-color: var(--el-color-primary); background: var(--el-color-primary); color: var(--el-color-white); }
.versions-more { margin: 3px 14px 11px; padding: 7px 10px; flex: 0 0 auto; border: 1px solid var(--el-border-color-lighter); border-radius: 7px; background: var(--el-fill-color-light); color: var(--el-text-color-secondary); font: inherit; font-size: 10px; cursor: pointer; }.versions-footer { margin-top: auto; padding: 10px 14px; flex: 0 0 auto; border-top: 1px solid var(--el-border-color-lighter); color: var(--el-text-color-secondary); font-size: 10px; line-height: 1.6; }.versions-restore-help { margin: 0 14px 10px; color: var(--el-text-color-secondary); font-size: 10px; line-height: 1.6; }.versions-empty { margin: 8px 14px 24px; color: var(--el-text-color-secondary); font-size: 11px; line-height: 1.7; }.versions-message { margin: 0 14px 10px; padding: 8px 10px; border-radius: 7px; background: var(--el-fill-color-light); color: var(--el-text-color-secondary); font-size: 11px; line-height: 1.6; overflow-wrap: anywhere; }.versions-message.error { background: color-mix(in srgb, var(--el-color-danger) 7%, var(--el-bg-color)); color: var(--el-color-danger); }.versions-message.success { background: color-mix(in srgb, var(--el-color-success) 8%, var(--el-bg-color)); color: var(--el-text-color-primary); }.sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
button:disabled { opacity: .45; cursor: not-allowed; }button:focus-visible, summary:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 2px; }@media (prefers-reduced-motion: reduce) { .version-diff > summary svg { transition: none; } }
</style>
