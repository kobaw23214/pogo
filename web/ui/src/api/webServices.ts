import { gt } from "@/i18n";

export const webServiceStates = ["preparing", "starting", "ready", "degraded", "reconciling", "stopping", "stopped", "expired", "failed", "interrupted"] as const;
export type WebServiceState = typeof webServiceStates[number];
export interface WebServiceSpec {
  name: string; display_name?: string; project_root: string; profile: "python-web" | "node-web"; start_argv: string[]; health_path?: string;
}
export const MAX_WEBSITE_SECONDS = 9223372036;
export interface WebsitePolicy {
  default_lease_seconds: number; max_per_session: number; max_per_user: number; max_total: number;
  internal_port: number; cpus: number; memory_mib: number;
}
export interface WebsiteRestart { service_id: string; name: string; state: string; operation_id?: string; error?: { code: string; message: string } }
export interface WebsiteSettingsState {
  settings: WebsitePolicy; profiles: string[]; usage: { total: number; users: Record<string, number>; sessions: Record<string, number> }; restarting: WebsiteRestart[];
}
export interface WebsiteSettingsResponse { state: WebsiteSettingsState; available_profiles: string[] }
export interface WebsiteSettingsUpdate { settings: WebsitePolicy; effects: { immediate: string[]; restart_required: string[] }; restarted: WebsiteRestart[] }
export interface WebsiteQuota { session: { current: number; limit: number }; user: { current: number; limit: number }; total: { current: number; limit: number } }
export interface WebServiceRecord {
  service_id: string; generation: number; revision: number; state: WebServiceState;
  name: string; profile: string; project_root: string; lease_expires_at: string;
  slot_name?: string; lease_id?: string;
  phase?: string; last_error?: { code: string; message: string }; updated_at?: string;
  storage_warning?: { code: string; message: string; retryable?: boolean };
  operation_status?: string; operation_error?: { code: string; message: string };
  /** Optional for old records; validate before offering a fresh lease. */
  spec?: unknown;
}
export interface WebServiceGrant {
  service_id: string; generation: number; grant_id: string; preview_url: string;
  grant_expires_at: string; lease_expires_at: string; service_url?: string;
  browser_url: string | null; limitations: string[];
  canonical_origin?: string;
  allowed_parent_origins?: string[];
}
export interface WebServiceOpenPending extends WebServiceRecord {
  operation_id: string; operation_status: "accepted" | "running"; accepted: true;
}
export type WebServiceOpenResponse = WebServiceGrant | WebServiceOpenPending;
export function pendingWebServiceOpen(value: unknown, record: WebServiceRecord, operationId: string): value is WebServiceOpenPending {
  if (!isWebServiceRecord(value)) return false;
  const receipt = value as WebServiceOpenPending;
  return receipt.service_id === record.service_id && receipt.generation === record.generation && receipt.operation_id === operationId && receipt.accepted === true &&
    ["accepted", "running"].includes(receipt.operation_status) && !("grant_id" in receipt) && !("preview_url" in receipt);
}
export interface WebServiceLog {
  generation: number; from_cursor: string; next_cursor: string; text: string; truncated: boolean; eof: boolean;
}
export interface WebServiceChange { sessionId: string; record: WebServiceRecord }
export interface DeletedWebService { sessionId: string; serviceId: string }
export interface OpenWebServiceRequest { sessionId: string; serviceId: string; label: string; open: boolean }
export const validWebServiceId = (value: unknown): value is string => typeof value === "string" && /^[A-Za-z0-9_-]{1,128}$/.test(value);
export function isWebServiceRecord(value: unknown): value is WebServiceRecord {
  if (!value || typeof value !== "object") return false;
  const r = value as WebServiceRecord;
  return validWebServiceId(r.service_id) && Number.isSafeInteger(r.generation) && r.generation > 0 && Number.isSafeInteger(r.revision) && r.revision >= 0 &&
    webServiceStates.includes(r.state) && typeof r.name === "string" && typeof r.profile === "string" && typeof r.project_root === "string" && Number.isFinite(Date.parse(r.lease_expires_at));
}
export function newerWebService(a: WebServiceRecord, b: WebServiceRecord): boolean {
  return a.generation > b.generation || a.generation === b.generation && a.revision >= b.revision;
}
export const webServiceRunning = (record: WebServiceRecord) => !["stopped", "expired", "failed", "interrupted"].includes(record.state);
export function webServiceStorageWarningKey(record: WebServiceRecord | null | undefined): "webServices.storageCheckRetrying" | "webServices.storageCheckUnavailable" | "" {
  if (!record?.storage_warning) return "";
  return ["ready", "degraded"].includes(record.state) ? "webServices.storageCheckRetrying" : "webServices.storageCheckUnavailable";
}
export function reusableWebServiceSpec(record: WebServiceRecord): WebServiceSpec | null {
  if (!record.spec || typeof record.spec !== "object" || Array.isArray(record.spec)) return null;
  const spec = record.spec as Record<string, unknown>;
  if (typeof spec.name !== "string" || !/^[a-z0-9][a-z0-9-]{0,47}$/.test(spec.name) || spec.name !== (record.slot_name || record.name) ||
    !["python-web", "node-web"].includes(spec.profile as string) || spec.profile !== record.profile || typeof spec.project_root !== "string" || !spec.project_root || spec.project_root !== record.project_root ||
    spec.project_root.startsWith("/") || /[\\\x00]/.test(spec.project_root) || spec.project_root.split("/").includes("..") ||
    !Array.isArray(spec.start_argv) || spec.start_argv.length < 1 || spec.start_argv.length > 64 ||
    !spec.start_argv.every(v => typeof v === "string" && v.length > 0 && v.length <= 8192 && !v.includes("\x00")) ||
    spec.build_argv !== undefined && (!Array.isArray(spec.build_argv) || spec.build_argv.length > 0) ||
    spec.health_path !== undefined && (typeof spec.health_path !== "string" || !spec.health_path.startsWith("/") || spec.health_path.startsWith("//") || /[\r\n]/.test(spec.health_path))) return null;
  return { name: spec.name, display_name: record.name, project_root: spec.project_root, profile: spec.profile as WebServiceSpec["profile"], start_argv: [...spec.start_argv] as string[],
    ...(typeof spec.health_path === "string" ? { health_path: spec.health_path } : {}) };
}

export function websiteError(cause: unknown): string {
  const issue = cause as Error & { code?: string; details?: { details?: { scope?: string; current?: number; limit?: number } } };
  if (issue?.code === "SERVICE_DELETE_PENDING") return gt("webServices.deletePending");
  const quota = issue?.details?.details;
  if (issue?.code === "QUOTA_EXCEEDED" && quota && ["session", "user", "total"].includes(quota.scope || "")) {
    return gt(`websiteSettings.quota_${quota.scope}`, { current: quota.current, limit: quota.limit });
  }
  return issue?.message || gt("webServices.actionFailed");
}
export function webServiceRecoveryIssue(record: WebServiceRecord): "webServices.waitForStop" | "webServices.missingSpec" | "" {
  if (webServiceRunning(record)) return "webServices.waitForStop";
  return reusableWebServiceSpec(record) ? "" : "webServices.missingSpec";
}
export function validWebServiceGrant(value: unknown, record: WebServiceRecord): value is WebServiceGrant {
  if (!value || typeof value !== "object") return false;
  const g = value as WebServiceGrant;
  if (g.service_id !== record.service_id || g.generation !== record.generation || !validWebServiceId(g.grant_id) ||
    typeof g.preview_url !== "string" || !Number.isFinite(Date.parse(g.grant_expires_at)) || !Number.isFinite(Date.parse(g.lease_expires_at))) return false;
  try {
    const u = new URL(g.preview_url);
    if (!["http:", "https:"].includes(u.protocol) || u.username || u.password || u.search || u.hash ||
      Date.parse(g.grant_expires_at) <= Date.now() || Date.parse(g.grant_expires_at) > Date.parse(g.lease_expires_at)) return false;
    const domainPreview = u.pathname === "/" && /^[a-f0-9]{48}\.[A-Za-z0-9.-]+$/.test(u.hostname);
    // A path website keeps its canonical address even when the workbench is
    // opened at an administrator-configured alias. Cross-origin embedding is
    // accepted only with the gateway's explicit, authenticated parent list.
    // Legacy receipts remain same-origin only; never infer trust from a URL.
    const pathPreview = allowedWebsiteParent(g, u.origin) &&
      /^\/_pogo\/web\/[a-f0-9]{48}\/$/.test(u.pathname) && g.preview_url === `${u.origin}${u.pathname}`;
    return domainPreview || pathPreview;
  } catch { return false; }
}

function allowedWebsiteParent(grant: WebServiceGrant, websiteOrigin: string): boolean {
  if (typeof location === "undefined") return false;
  if (websiteOrigin === location.origin) return true;
  const parents = grant.allowed_parent_origins;
  if (grant.canonical_origin !== websiteOrigin || !Array.isArray(parents) || !parents.length || parents.length > 256 ||
    !parents.includes(websiteOrigin) || !parents.includes(location.origin)) return false;
  return parents.every(value => {
    if (typeof value !== "string") return false;
    try {
      const parsed = new URL(value);
      return ["http:", "https:"].includes(parsed.protocol) && parsed.origin === value &&
        !parsed.username && !parsed.password && !parsed.search && !parsed.hash && parsed.pathname === "/";
    } catch { return false; }
  });
}
