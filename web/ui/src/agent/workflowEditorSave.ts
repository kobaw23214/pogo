import type { Workflow, WorkflowInput, WorkflowNode } from "./workflowTypes";
import { toWorkflowInput } from "./workflowPresentation";

/** Capture the acknowledged request, so edits made during the request stay dirty. */
export async function saveWorkflowDraft(
  workflow: Pick<Workflow, "id" | "revision">,
  draft: { name: string; description: string; nodes: readonly WorkflowNode[] },
  update: (id: string, input: WorkflowInput, revision: number) => Promise<Workflow>,
): Promise<{ workflow: Workflow; savedInput: WorkflowInput }> {
  const savedInput = toWorkflowInput(draft);
  const updated = await update(workflow.id, savedInput, workflow.revision);
  return { workflow: updated, savedInput };
}

/** Saving may make the publish pointer stale; decide only after the save settles. */
export async function prepareWorkflowRun(flushSave: () => Promise<boolean>, needsPublish: () => boolean): Promise<"blocked" | "publish" | "start"> {
  if (!(await flushSave())) return "blocked";
  return needsPublish() ? "publish" : "start";
}
