export const workflowToolActions = ["discover", "read", "validate", "edit", "test", "run", "publish"] as const;
export type WorkflowToolAction = typeof workflowToolActions[number];
export interface WorkflowAuthoringDetails { type: "workflow_authoring"; action: WorkflowToolAction; result: Record<string, unknown> }
export function isWorkflowAuthoringDetails(value: unknown): value is WorkflowAuthoringDetails {
  if (!value || typeof value !== "object") return false;
  const d = value as WorkflowAuthoringDetails;
  return d.type === "workflow_authoring" && workflowToolActions.includes(d.action)
    && !!d.result && typeof d.result === "object" && !Array.isArray(d.result);
}
const record = (v: unknown): Record<string, unknown> => v && typeof v === "object" && !Array.isArray(v) ? v as Record<string, unknown> : {};
const identity = /^[A-Za-z0-9_-]{1,160}$/;
export function workflowReceiptLocation(details: WorkflowAuthoringDetails): string | null {
  const r = details.result, w = record(r.workflow), run = record(r.run);
  const id = r.workflow_id ?? w.id ?? run.workflow_id ?? (typeof r.id === "string" && r.id.startsWith("wf_") ? r.id : undefined);
  if (typeof id !== "string" || !identity.test(id)) return null;
  const rid = r.run_id ?? run.id ?? (typeof r.id === "string" && r.id.startsWith("run_") ? r.id : undefined);
  return `/manage/workflows/${encodeURIComponent(id)}` + (typeof rid === "string" && identity.test(rid) ? `?run=${encodeURIComponent(rid)}` : "");
}
export function workflowInputKeys(nodes: readonly { task?: string; prompt?: string }[]): string[] {
  const keys = new Set<string>();
  for (const node of nodes) for (const text of [node.task, node.prompt]) {
    for (const match of (text ?? "").matchAll(/\{\{\s*inputs\.([A-Za-z_][A-Za-z0-9_]{0,63})\s*\}\}/g)) keys.add(match[1]!);
  }
  return [...keys].sort();
}
