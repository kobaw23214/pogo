import { computed, nextTick, onScopeDispose, ref, watch } from "vue";
import { api } from "@/api/client";
import { gt } from "@/i18n";
import { randomUUID } from "@/utils/uuid";
import { MAX_WEBSITE_SECONDS, websiteError, isWebServiceRecord, newerWebService, reusableWebServiceSpec, validWebServiceId, webServiceRecoveryIssue, type DeletedWebService, type WebServiceChange, type WebServiceLog, type WebServiceRecord, type WebServiceSpec, type WebsiteQuota } from "@/api/webServices";

type Action = "stop" | "restart" | "renew" | "start" | "rename";
interface Context { sessionId: string | null; enabled: boolean; connected?: boolean; change?: WebServiceChange | null }
interface Operation { action: Action; id: string; uncertain: boolean; startedAt: number; spec?: WebServiceSpec; leaseSeconds?: number; displayName?: string }

export function useWebServices(context: () => Context, onDeleted?: (request: DeletedWebService) => void) {
  const records = ref<WebServiceRecord[]>([]), error = ref(""), loading = ref(false), stale = ref(false), expanded = ref(false);
  const quota = ref<WebsiteQuota>(), defaultLeaseSeconds = ref(7200);
  const selected = ref<string | null>(null), log = ref<WebServiceLog | null>(null), logError = ref(""), logLoading = ref(false);
  const busy = ref<Record<string, Action | "delete">>({}), operations = ref<Record<string, Operation>>({}), actionErrors = ref<Record<string, string>>({});
  const deleting = ref<Record<string, boolean>>({}), deleteErrors = ref<Record<string, string>>({});
  const unavailable = ref(false), now = ref(Date.now());
  const revealedEntry = ref(false), settledContext = ref({ sessionId: null as string | null, enabled: false });
  let epoch = 0, listedEpoch = -1, disposed = false, listSerial = 0, logSerial = 0, changeSerial = 0;
  let listAbort: AbortController | undefined, logAbort: AbortController | undefined;
  let refreshWork: Promise<void> | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const changeVersions = new Map<string, number>();
  // Service IDs are never reused. A confirmed removal fences older lists,
  // SSE updates and action replies for the rest of this conversation view.
  const deletedIds = new Set<string>();
  const current = (sid: string, version = epoch) => !disposed && context().enabled && context().sessionId === sid && version === epoch;
  const readyFor = (sid: string) => !disposed && context().enabled && context().sessionId === sid && settledContext.value.enabled && settledContext.value.sessionId === sid;
  function accept(record: unknown) {
    if (!isWebServiceRecord(record) || deletedIds.has(record.service_id)) return;
    if (record.phase === "deleted") {
      const sessionId = context().sessionId;
      if (sessionId) forget(record.service_id, sessionId);
      return;
    }
    const old = records.value.find(r => r.service_id === record.service_id);
    if (old && !newerWebService(record, old)) return;
    if (old && old.generation !== record.generation && selected.value === record.service_id) {
      logSerial++; logAbort?.abort(); log.value = null; logError.value = ""; logLoading.value = false;
    }
    changeVersions.set(record.service_id, ++changeSerial);
    records.value = old ? records.value.map(r => r.service_id === record.service_id ? record : r) : [...records.value, record];
  }
  function forget(id: string, sessionId: string) {
    if (deletedIds.has(id)) return;
    deletedIds.add(id); changeVersions.set(id, ++changeSerial);
    records.value = records.value.filter(record => record.service_id !== id);
    delete operations.value[id]; delete actionErrors.value[id]; delete deleteErrors.value[id]; delete deleting.value[id];
    if (selected.value === id) {
      logSerial++; logAbort?.abort(); selected.value = null; log.value = null; logError.value = ""; logLoading.value = false;
    }
    onDeleted?.({ sessionId, serviceId: id });
  }
  function schedule() {
    clearTimeout(timer);
    if (!disposed && context().enabled && !unavailable.value) timer = setTimeout(() => {
      if (typeof document === "undefined" || !document.hidden) void refresh();
      else schedule();
    }, records.value.length ? 3000 : 12000);
  }
  function refresh(): Promise<void> {
    if (refreshWork) return refreshWork;
    const work = loadRecords();
    refreshWork = work;
    void work.finally(() => { if (refreshWork === work) refreshWork = undefined; });
    return work;
  }
  async function loadRecords() {
    const { sessionId: sid, enabled } = context();
    if (!sid || !enabled || loading.value) return;
    const version = epoch, request = ++listSerial, changedBefore = changeSerial;
    listAbort?.abort(); const controller = new AbortController(); listAbort = controller;
    loading.value = true;
    try {
      const result = await api.webServices(sid, controller.signal);
      if (!current(sid, version) || request !== listSerial) return;
      if (!result || !Array.isArray(result.services) || !result.services.every(isWebServiceRecord)) throw new Error(gt("webServices.invalidResponse"));
      quota.value = result.quota;
      if (Number.isSafeInteger(result.default_lease_seconds) && result.default_lease_seconds! > 0) defaultLeaseSeconds.value = result.default_lease_seconds!;
      const ids = new Set(result.services.map(r => r.service_id));
      // Only a list started after the latest local change can confirm absence.
      // In particular, a pre-delete response cannot settle a lost DELETE reply.
      for (const record of [...records.value]) {
        if (!ids.has(record.service_id) && (changeVersions.get(record.service_id) ?? 0) <= changedBefore) forget(record.service_id, sid);
      }
      for (const record of result.services) accept(record);
      listedEpoch = version;
      unavailable.value = false; stale.value = false; error.value = "";
      for (const [id, operation] of Object.entries(operations.value)) {
        if (deletedIds.has(id)) continue;
        let status: WebServiceRecord;
        try { status = await api.webService(sid, id, controller.signal); }
        catch (cause) {
          if (!current(sid, version)) return;
          if (deletedIds.has(id)) continue;
          throw cause;
        }
        if (!current(sid, version)) return;
        if (deletedIds.has(id)) continue;
        accept(status);
        if (operations.value[id]?.id !== operation.id) continue;
        const receipt = status as WebServiceRecord & { operation_id?: string };
        if (receipt.operation_id === operation.id && ["completed", "failed", "cancelled"].includes(receipt.operation_status || "")) {
          if (receipt.operation_status !== "completed") actionErrors.value[id] = receipt.operation_error?.message || gt("webServices.actionFailed");
          delete operations.value[id];
        } else if (Date.now() - operation.startedAt > 60000) {
          operation.uncertain = true; actionErrors.value[id] = gt("webServices.actionFailed");
        }
      }
    } catch (cause) {
      if (!current(sid, version) || request !== listSerial || (cause as Error)?.name === "AbortError") return;
      const issue = cause as Error & { code?: string };
      unavailable.value = issue.code === "FEATURE_DISABLED" && !records.value.length;
      stale.value = !!records.value.length;
      error.value = websiteError(cause);
    } finally {
      if (current(sid, version) && request === listSerial) { loading.value = false; schedule(); }
    }
  }
  async function action(record: WebServiceRecord, action: Action, value?: number | string) {
    const sid = context().sessionId;
    if (!sid || !context().enabled || busy.value[record.service_id] || deleting.value[record.service_id] || record.phase === "deleting" || deletedIds.has(record.service_id)) return;
    const version = epoch, id = record.service_id;
    const prior = operations.value[id];
    let operation = prior?.action === action ? prior : undefined;
    if (!operation && action === "start") {
      const issue = webServiceRecoveryIssue(record);
      if (Date.parse(record.lease_expires_at) > Date.now() || issue) {
        actionErrors.value[id] = gt(issue || "webServices.previewUnavailable"); return;
      }
      operation = { action, id: randomUUID(), uncertain: false, startedAt: Date.now(), spec: reusableWebServiceSpec(record)! };
    }
    operation ||= { action, id: randomUUID(), uncertain: false, startedAt: Date.now() };
    if (action === "renew" && !operation.leaseSeconds) {
      if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 1 || value > MAX_WEBSITE_SECONDS) { actionErrors.value[id] = gt("websiteSettings.invalidDuration"); return; }
      operation.leaseSeconds = value;
    }
    if (action === "rename" && !operation.displayName) {
      const name = typeof value === "string" ? value.trim() : "";
      if (!name || [...name].length > 80 || /[\u0000-\u001f\u007f-\u009f\u2028\u2029]/u.test(name)) { actionErrors.value[id] = gt("websiteSettings.nameHint"); return; }
      operation.displayName = name;
    }
    operations.value[id] = operation; busy.value[id] = action; delete actionErrors.value[id];
    const controller = new AbortController(), timeout = setTimeout(() => controller.abort(), 15000);
    try {
      const result = action === "start" ? await api.startWebService(sid, operation.spec!, operation.id, controller.signal)
        : action === "rename" ? await api.renameWebService(sid, id, operation.displayName!, operation.id, controller.signal)
        : await api.webServiceAction(sid, id, action, operation.id, controller.signal, operation.leaseSeconds);
      if (!current(sid, version) || deletedIds.has(id)) return;
      if (!isWebServiceRecord(result) || result.service_id !== id) throw new Error(gt("webServices.invalidResponse"));
      accept(result);
      if (result.operation_status === "failed" || result.operation_error) {
        delete operations.value[id]; actionErrors.value[id] = result.operation_error?.message || gt("webServices.actionFailed");
      } else if (result.operation_status === "completed") delete operations.value[id];
      else operation.uncertain = false;
      void refresh();
    } catch (cause) {
      if (current(sid, version) && !deletedIds.has(id)) {
        const known = cause as Error & { status?: number };
        if (known.status && known.status >= 400 && known.status < 500) {
          delete operations.value[id]; actionErrors.value[id] = websiteError(cause);
        } else {
          operation.uncertain = true;
          actionErrors.value[id] = gt("webServices.actionUnknown", { message: known.message || gt("webServices.actionFailed") });
        }
      }
    } finally { clearTimeout(timeout); if (current(sid, version)) delete busy.value[id]; }
  }
  async function remove(record: WebServiceRecord): Promise<boolean> {
    const sid = context().sessionId, id = record.service_id, version = epoch;
    if (!sid || !context().enabled || busy.value[id] || deletedIds.has(id)) return false;
    deleting.value[id] = true; busy.value[id] = "delete"; delete deleteErrors.value[id];
    changeVersions.set(id, ++changeSerial);
    const controller = new AbortController(), timeout = setTimeout(() => controller.abort(), 30000);
    try {
      await api.deleteWebService(sid, id, controller.signal);
      if (!current(sid, version)) return false;
      forget(id, sid);
      // A list already in flight may predate deletion. Refresh once it settles
      // so quota usage is also taken from the authoritative post-delete list.
      const pending = refreshWork;
      if (pending) void pending.then(() => { if (current(sid, version)) void refresh(); });
      else void refresh();
      return true;
    } catch (cause) {
      if (!current(sid, version)) return false;
      if (deletedIds.has(id)) return true;
      const known = cause as Error & { status?: number; code?: string };
      const uncertain = !known.status || known.status >= 500;
      if (!uncertain) delete deleting.value[id];
      deleteErrors.value[id] = uncertain && known.code !== "SERVICE_DELETE_PENDING" ? gt("webServices.deleteUnknown", { message: websiteError(cause) }) : websiteError(cause);
      // A missing record in a fresh list confirms completion, including when
      // the DELETE response was lost. A generic 404 by itself never does.
      void refresh();
      return false;
    } finally { clearTimeout(timeout); if (current(sid, version)) delete busy.value[id]; }
  }
  async function readLog(record: WebServiceRecord, next = false) {
    const sid = context().sessionId;
    if (!sid || !context().enabled || deleting.value[record.service_id] || record.phase === "deleting" || deletedIds.has(record.service_id)) return;
    const version = epoch, ticket = ++logSerial, id = record.service_id, generation = record.generation;
    selected.value = id; logLoading.value = true; logError.value = "";
    const cursor = next && log.value?.generation === generation ? log.value.next_cursor : "";
    logAbort?.abort(); const controller = new AbortController(); logAbort = controller;
    try {
      const page = await api.webServiceLogs(sid, id, cursor, controller.signal);
      if (!current(sid, version) || ticket !== logSerial || records.value.find(r => r.service_id === id)?.generation !== generation) return;
      if (page.generation !== generation || typeof page.text !== "string" || typeof page.next_cursor !== "string") throw new Error(gt("webServices.invalidResponse"));
      log.value = { ...page, text: page.text.slice(0, 65536) };
    } catch (cause) { if (current(sid, version) && ticket === logSerial && (cause as Error)?.name !== "AbortError") logError.value = (cause as Error).message || gt("webServices.logFailed"); }
    finally { if (current(sid, version) && ticket === logSerial) logLoading.value = false; }
  }
  async function reveal(id: string, expectedSession = context().sessionId): Promise<WebServiceRecord | null> {
    await nextTick();
    if (!expectedSession || !readyFor(expectedSession)) return null;
    const version = epoch;
    if (!validWebServiceId(id)) {
      error.value = gt("webServices.notFound"); revealedEntry.value = true;
      await nextTick();
      if (current(expectedSession, version)) expanded.value = true;
      return null;
    }
    if (listedEpoch !== version || loading.value || stale.value) await refresh();
    if (!current(expectedSession, version)) return null;
    const record = records.value.find(r => r.service_id === id);
    if (!record && !error.value) error.value = gt("webServices.notFound");
    revealedEntry.value = true;
    // The entry/popover must mount with visible=false first. Opening before
    // the delayed owner list or child-context reset can lose this intent.
    await nextTick();
    if (!current(expectedSession, version)) return null;
    if (record) selected.value = id;
    expanded.value = true;
    return record ?? null;
  }
  watch([() => context().sessionId, () => context().enabled] as const, ([sessionId, enabled]) => {
    epoch++; listSerial++; logSerial++; clearTimeout(timer); listAbort?.abort(); logAbort?.abort(); refreshWork = undefined;
    settledContext.value = { sessionId, enabled }; revealedEntry.value = false;
    records.value = []; quota.value = undefined; defaultLeaseSeconds.value = 7200; error.value = ""; loading.value = false; stale.value = false; unavailable.value = false; expanded.value = false;
    selected.value = null; log.value = null; logError.value = ""; logLoading.value = false; busy.value = {}; operations.value = {}; actionErrors.value = {}; deleting.value = {}; deleteErrors.value = {}; changeVersions.clear(); deletedIds.clear();
    void refresh();
  }, { immediate: true });
  watch(() => context().change, change => { if (change && context().enabled && change.sessionId === context().sessionId) { unavailable.value = false; accept(change.record); schedule(); } });
  watch(() => context().connected, connected => { if (connected) void refresh(); });
  const clock = setInterval(() => { now.value = Date.now(); }, 1000);
  onScopeDispose(() => { disposed = true; epoch++; clearTimeout(timer); clearInterval(clock); listAbort?.abort(); logAbort?.abort(); });
  return { records, quota, defaultLeaseSeconds, error, loading, stale, expanded, selected, log, logError, logLoading, busy, operations, actionErrors, deleting, deleteErrors, unavailable, now, refresh, accept, action, remove, readLog, reveal, readyFor,
    visible: computed(() => records.value.length > 0 || revealedEntry.value && !!error.value) };
}
