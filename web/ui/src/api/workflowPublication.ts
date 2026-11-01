import { authHeaders } from "./client";

export interface WorkflowPublicationInputField { key: string; label: string; required: boolean }
export interface WorkflowPublicationMapping { subject: string; username: string }
export type WorkflowHistoryScope = "business" | "workflow";
export interface WorkflowPublicationCallback { enabled: boolean; url: string }
export interface WorkflowPublication {
  id: string;
  workflow_id: string;
  version_id: string;
  name: string;
  enabled: boolean;
  revision: number;
  allowed_origins: string[];
  input_fields: WorkflowPublicationInputField[];
  mappings: WorkflowPublicationMapping[];
  history_scope?: WorkflowHistoryScope;
  callback?: WorkflowPublicationCallback;
}
export type WorkflowPublicationInput = Omit<WorkflowPublication, "id" | "workflow_id" | "revision">;
export interface WorkflowPublicationList {
  publications: WorkflowPublication[];
  subjects: { username: string; display_name?: string }[];
}

export interface WorkflowCallbackDelivery {
  id: string;
  event_id?: string;
  run_id: string;
  business_id?: string;
  event_type?: string;
  status: string;
  attempts: number;
  next_attempt_at?: string | number;
  last_error?: string;
  created_at?: string | number;
  delivered_at?: string | number;
  state_revision?: number;
  sequence?: number;
  last_status?: number;
  target_url?: string;
  blocked_reason?: string;
}
export interface WorkflowPublicationSecrets {
  publication: WorkflowPublication;
  client_secret?: string;
  callback_secret?: string;
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const response = await fetch(path, { method, cache: "no-store", credentials: "same-origin", headers: { ...authHeaders(), ...(body === undefined ? {} : { "Content-Type": "application/json" }) }, body: body === undefined ? undefined : JSON.stringify(body) });
  const raw = await response.text();
  let data: unknown;
  try { data = JSON.parse(raw); } catch { data = undefined; }
  if (!response.ok) throw Object.assign(new Error((data as { error?: string } | undefined)?.error || raw || "Request failed"), { status: response.status });
  return data as T;
}
const base = (wid: string) => `/api/manage/workflows/${encodeURIComponent(wid)}/publications`;
export const workflowPublications = {
  list: (wid: string) => request<WorkflowPublicationList>("GET", base(wid)),
  create: (wid: string, body: WorkflowPublicationInput) => request<WorkflowPublicationSecrets>("POST", base(wid), body),
  update: (wid: string, pid: string, body: WorkflowPublicationInput & { revision: number }) => request<WorkflowPublication & { callback_secret?: string }>("PUT", `${base(wid)}/${encodeURIComponent(pid)}`, body),
  rotate: (wid: string, pid: string) => request<{ publication: WorkflowPublication; client_secret: string }>("POST", `${base(wid)}/${encodeURIComponent(pid)}/rotate-secret`, {}),
  rotateCallback: (wid: string, pid: string) => request<WorkflowPublicationSecrets>("POST", `${base(wid)}/${encodeURIComponent(pid)}/rotate-callback-secret`, {}),
  deliveries: (wid: string, pid: string) => request<{ deliveries: WorkflowCallbackDelivery[] }>("GET", `${base(wid)}/${encodeURIComponent(pid)}/deliveries`),
  retryDelivery: (wid: string, pid: string, id: string) => request<unknown>("POST", `${base(wid)}/${encodeURIComponent(pid)}/deliveries/${encodeURIComponent(id)}/retry`, { use_current_target: true }),
};
