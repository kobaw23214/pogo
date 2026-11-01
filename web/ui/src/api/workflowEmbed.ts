import type { WorkflowRun } from "@/agent/workflowTypes";
import type { WorkflowPublication } from "./workflowPublication";

export interface WorkflowEmbedSession {
  session_token?: string;
  expires_at: string;
  publication: Pick<WorkflowPublication, "id" | "workflow_id" | "version_id" | "name" | "input_fields" | "enabled" | "history_scope">;
  subject: string;
  run_id?: string;
  business_id?: string;
  inputs?: Record<string, string>;
  can_invoke?: boolean;
  parent_origin: string;
}

/** A separate bearer-only transport. Account cookies, account tokens and
 * browser storage are never used by an embedded workflow. */
export function createWorkflowEmbedApi(publicationId: string) {
  const base = `/api/workflow-embed/publications/${encodeURIComponent(publicationId)}`;
  let sessionToken = "";
  let exchangeEpoch = 0;
  async function request<T>(method: string, path: string, body?: unknown, authenticated = true, extraHeaders: Record<string, string> = {}): Promise<T> {
    if (authenticated && !sessionToken) throw Object.assign(new Error("Embedded session required"), { status: 401 });
    const response = await fetch(base + path, { method, credentials: "omit", cache: "no-store", headers: { ...(authenticated ? { Authorization: `Bearer ${sessionToken}` } : {}), ...extraHeaders, ...(body === undefined ? {} : { "Content-Type": "application/json" }) }, body: body === undefined ? undefined : JSON.stringify(body) });
    const raw = await response.text();
    let data: unknown;
    try { data = raw ? JSON.parse(raw) : undefined; } catch { data = undefined; }
    if (!response.ok) throw Object.assign(new Error((data as { error?: string } | undefined)?.error || raw || "Request failed"), { status: response.status });
    return data as T;
  }
  const runPath = (rid: string) => `/runs/${encodeURIComponent(rid)}`;
  return {
    async exchange(code: string, parentOrigin: string) {
      const epoch = ++exchangeEpoch;
      const session = await request<WorkflowEmbedSession>("POST", "/session", { code, parent_origin: parentOrigin }, false);
      if (epoch !== exchangeEpoch) throw new Error("Superseded embedded session");
      if (!session.session_token?.startsWith("wes_")) throw Object.assign(new Error("Invalid embedded session"), { status: 401 });
      sessionToken = session.session_token;
      return session;
    },
    clear() { ++exchangeEpoch; sessionToken = ""; },
    session: () => request<WorkflowEmbedSession>("GET", "/session"),
    list: () => request<{ runs: WorkflowRun[] }>("GET", "/runs"),
    get: (rid: string) => request<WorkflowRun>("GET", runPath(rid)),
    start: (inputs: Record<string, string>, requestKey: string) => request<{ id: string }>("POST", "/runs", { inputs, request_key: requestKey }),
    cancel: (rid: string) => request<{ cancelled: boolean }>("POST", runPath(rid) + "/cancel", {}),
    retry: (rid: string, requestKey: string) => request<{ id: string }>("POST", runPath(rid) + "/retry", {}, true, { "Idempotency-Key": requestKey }),
    resume: (rid: string, nid: string, action: string, fields: Record<string, string>) => request<WorkflowRun>("POST", runPath(rid) + `/nodes/${encodeURIComponent(nid)}/resume`, { action, fields }),
    async artifact(rid: string, nid: string): Promise<string> {
      if (!sessionToken) throw Object.assign(new Error("Embedded session required"), { status: 401 });
      const response = await fetch(base + runPath(rid) + `/nodes/${encodeURIComponent(nid)}/artifact`, { credentials: "omit", cache: "no-store", headers: { Authorization: `Bearer ${sessionToken}` } });
      const text = await response.text();
      if (!response.ok) throw Object.assign(new Error(text || "Request failed"), { status: response.status });
      return text;
    },
  };
}
