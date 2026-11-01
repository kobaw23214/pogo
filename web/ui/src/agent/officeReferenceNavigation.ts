import type { OfficeReferenceSnapshot } from "@/api/types";
import type { WorkspaceFileScope } from "./workspaceFileNavigation";
import { workspaceFileTarget } from "./workspaceFileLinks";

/** Authenticated Office responses may use a service-relative canonical path.
 * Normalize it in the declared workspace before the common preview joins it. */
export function officeReferenceTarget(office: OfficeReferenceSnapshot, scope: WorkspaceFileScope) {
  if (office.information || office.path.startsWith("information-assets/")) return null;
  if (office.source_session && office.source_session !== scope.conversationId) return null;
  return workspaceFileTarget(office.path, scope);
}
