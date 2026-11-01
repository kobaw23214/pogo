import type { AgentWorkflowUpdate } from "./agentWorkflowUpdates";

/** Immersion is explicit and temporary. Selecting a workflow leaves its canvas
 * beside the existing conversation until the user asks for more space. */
export function isAgentWorkflowWorkspace(active: string | null, selected: boolean, agentId?: string, browserImmersive = false, immersiveRequested = false): boolean {
  return active === "workflows" && selected && !!agentId && !browserImmersive && immersiveRequested;
}

/** Successful creation can reveal the canvas next to the authoring chat. An
 * open sheet, another Agent, or a historical session keeps its current view. */
export function shouldRevealCreatedAgentWorkflow(update: AgentWorkflowUpdate | null, active: string | null, agentId?: string, sessionId?: string | null): boolean {
  return active === null && !!agentId && !!sessionId && update?.action === "create" && update.agentId === agentId && update.sessionId === sessionId;
}

export function agentWorkflowCanvasSize(availableWidth: number, availableHeight: number): { width: string; height: string } {
  return {
    width: Number.isFinite(availableWidth) && availableWidth > 0 ? `${availableWidth}px` : "100%",
    height: Number.isFinite(availableHeight) && availableHeight > 0 ? `${availableHeight}px` : "100%",
  };
}

