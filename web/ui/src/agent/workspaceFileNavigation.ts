import type { InjectionKey } from "vue";
import type { WorkspaceFileContext, WorkspaceFileTarget } from "./workspaceFileLinks";

export interface WorkspaceFileScope extends WorkspaceFileContext {
  conversationId: string;
}

/** A containing conversation supplies the existing, workspace-scoped preview. */
export interface WorkspaceFileNavigation {
  scope(): WorkspaceFileScope | null;
  open(target: WorkspaceFileTarget, origin: Pick<WorkspaceFileScope, "conversationId" | "cwd">): void | Promise<void>;
  openHTML?(target: WorkspaceFileTarget, origin: Pick<WorkspaceFileScope, "conversationId" | "cwd">): void | Promise<void>;
}

export const workspaceFileNavigationKey: InjectionKey<WorkspaceFileNavigation> = Symbol("workspaceFileNavigation");
