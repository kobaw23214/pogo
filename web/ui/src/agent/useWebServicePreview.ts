import { onScopeDispose, ref, watch } from "vue";
import { api } from "@/api/client";
import { gt } from "@/i18n";
import { randomUUID } from "@/utils/uuid";
import { isWebServiceRecord, newerWebService, pendingWebServiceOpen, validWebServiceGrant, validWebServiceId, websiteError, type WebServiceGrant, type WebServiceRecord } from "@/api/webServices";

interface Context { sessionId: string; serviceId: string; active: boolean; openRequest?: number }
export function useWebServicePreview(context: () => Context) {
  const record = ref<WebServiceRecord | null>(null), grant = ref<WebServiceGrant | null>(null);
  const viewVersion = ref(0);
  const issue = ref(""), closeIssue = ref(""), loading = ref(false), closing = ref(false), stale = ref(false), now = ref(Date.now());
  let epoch = 0, disposed = false, pageHidden = false, missing = false, statusTicket = 0, openingTicket = 0, openingId: string | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const isCurrent = (captured: Context, version = epoch) => !disposed && !pageHidden && version === epoch && captured.sessionId === context().sessionId && captured.serviceId === context().serviceId;
  function schedule() {
    clearTimeout(timer);
    if (!disposed && !pageHidden && !missing && (context().active || grant.value)) timer = setTimeout(() => { void refresh(); }, grant.value ? 3000 : 12000);
  }
  async function refresh(): Promise<WebServiceRecord | null> {
    const captured = { ...context() }, version = epoch, ticket = ++statusTicket;
    if (!validWebServiceId(captured.sessionId) || !validWebServiceId(captured.serviceId)) { issue.value = gt("webServices.notFound"); return null; }
    try {
      const next = await api.webService(captured.sessionId, captured.serviceId);
      if (!isCurrent(captured, version) || ticket !== statusTicket) return null;
      if (!isWebServiceRecord(next) || next.service_id !== captured.serviceId) throw new Error(gt("webServices.invalidResponse"));
      if (record.value && !newerWebService(next, record.value)) return record.value;
      const previous = record.value;
      record.value = next; stale.value = false; missing = false;
      if (next.phase === "deleting") {
        // Deletion may be observed in another window while its open request
        // is still in flight. Retire that view before accepting any receipt.
        openingTicket++; openingId = undefined; loading.value = false; grant.value = null;
        issue.value = gt("webServices.deleting");
        return next;
      }
      if (grant.value) {
        const newLease = previous?.lease_id && next.lease_id && previous.lease_id !== next.lease_id;
        const expired = Date.parse(next.lease_expires_at) <= Date.now();
        if (newLease || ["stopped", "expired", "failed", "interrupted"].includes(next.state) || expired) {
          grant.value = null; issue.value = gt(next.state === "expired" || expired ? "webServices.previewExpired" : "webServices.previewUnavailable");
        } else {
          // A website lease owns one address. Renewal updates its deadline;
          // restart reloads that same address when the new generation is ready.
          grant.value = { ...grant.value, generation: next.generation, grant_expires_at: next.lease_expires_at, lease_expires_at: next.lease_expires_at };
          issue.value = "";
        }
      }
      return next;
    } catch (cause) {
      if (isCurrent(captured, version) && ticket === statusTicket) {
        const failure = cause as { status?: number; code?: string };
        if (failure.status === 404 || failure.code === "SERVICE_NOT_FOUND" || failure.code === "NOT_FOUND") {
          missing = true; openingTicket++; openingId = undefined; loading.value = false;
          record.value = null; grant.value = null; stale.value = false; issue.value = gt("webServices.notFound");
        } else { stale.value = true; issue.value = websiteError(cause); }
      }
      return null;
    } finally { if (isCurrent(captured, version) && ticket === statusTicket) schedule(); }
  }
  async function open() {
    if (loading.value || disposed || pageHidden) return;
    const captured = { ...context() }, version = epoch, ticket = ++openingTicket;
    loading.value = true; issue.value = "";
    try {
      const latest = await refresh();
      if (!isCurrent(captured, version) || ticket !== openingTicket) return;
      if (!latest || latest.state !== "ready" || Date.parse(latest.lease_expires_at) <= Date.now()) { issue.value ||= gt("webServices.previewUnavailable"); return; }
      openingId ||= randomUUID();
      const operationId = openingId;
      const next = await api.openWebService(captured.sessionId, captured.serviceId, operationId);
      if (!isCurrent(captured, version) || ticket !== openingTicket) return;
      if (pendingWebServiceOpen(next, latest, operationId)) { issue.value = gt("webServices.previewPending"); return; }
      let expected = record.value || latest;
      const receipt = next as Partial<WebServiceGrant>;
      if (receipt.service_id === expected.service_id && typeof receipt.preview_url === "string" &&
        Number.isSafeInteger(receipt.generation) && receipt.generation !== expected.generation) {
        // A restart can finish between the status read and open response.
        // Confirm its new generation once, without labelling that transition
        // as malformed data or ever installing a stale-generation address.
        if (receipt.generation! > expected.generation) {
          expected = await refresh() || expected;
          if (!isCurrent(captured, version) || ticket !== openingTicket) return;
        }
        if (receipt.generation !== expected.generation || expected.state !== "ready") {
          openingId = undefined; issue.value = gt("webServices.previewChanged"); return;
        }
      }
      if (!validWebServiceGrant(next, expected)) { openingId = undefined; throw new Error(gt("webServices.invalidResponse")); }
      if (record.value?.generation !== next.generation || record.value.state !== "ready") { openingId = undefined; issue.value = gt("webServices.previewChanged"); return; }
      grant.value = { ...next, grant_expires_at: record.value.lease_expires_at, lease_expires_at: record.value.lease_expires_at }; viewVersion.value++; openingId = undefined; schedule();
    } catch (cause) {
      if (isCurrent(captured, version) && ticket === openingTicket) {
        if ((cause as { code?: string })?.code === "GRANT_EXPIRED") openingId = undefined;
        issue.value = websiteError(cause);
      }
    } finally { if (isCurrent(captured, version) && ticket === openingTicket) loading.value = false; }
  }
  async function prepareClose() {
    openingTicket++; grant.value = null; openingId = undefined; loading.value = false;
    // Closing a view never revokes an address shared for the website's lease.
    return true;
  }
  watch(() => [context().sessionId, context().serviceId] as const, () => {
    epoch++; openingTicket++; statusTicket++; openingId = undefined; missing = false; clearTimeout(timer); grant.value = null;
    record.value = null; issue.value = ""; loading.value = false; stale.value = false; void refresh();
  }, { immediate: true });
  watch(() => context().openRequest, request => { if (request) void open(); }, { immediate: true });
  watch(() => context().active, active => { if (active) void refresh(); else schedule(); });
  const clock = setInterval(() => {
    now.value = Date.now();
    if (grant.value && Date.parse(record.value?.lease_expires_at || grant.value.lease_expires_at) <= now.value) { issue.value = gt("webServices.previewExpired"); grant.value = null; }
  }, 1000);
  const onPageHide = () => { pageHidden = true; epoch++; openingTicket++; statusTicket++; openingId = undefined; clearTimeout(timer); grant.value = null; loading.value = false; };
  const onPageShow = () => { if (!disposed) { pageHidden = false; void refresh(); } };
  if (typeof window !== "undefined") { window.addEventListener("pagehide", onPageHide); window.addEventListener("pageshow", onPageShow); }
  onScopeDispose(() => {
    disposed = true; epoch++; openingTicket++; statusTicket++; clearTimeout(timer); clearInterval(clock); grant.value = null;
    if (typeof window !== "undefined") { window.removeEventListener("pagehide", onPageHide); window.removeEventListener("pageshow", onPageShow); }
  });
  return { record, grant, viewVersion, issue, closeIssue, loading, closing, stale, now, refresh, open, prepareClose };
}
