import { shallowRef } from "vue";
import type { SessionFileLinkResolution } from "@/api/types";
import { resolvedWorkspaceFileTarget, type WorkspaceFileTarget } from "./workspaceFileLinks";
import type { WorkspaceFileNavigation, WorkspaceFileScope } from "./workspaceFileNavigation";

export interface AnswerFileChoice {
  name: string;
  workspace: string;
  candidates: { path: string; name: string }[];
  incomplete: boolean;
  error?: string;
}

interface Options {
  scope(): WorkspaceFileScope | null;
  resolve(sid: string, path: string, lookupByName: boolean, signal: AbortSignal): Promise<SessionFileLinkResolution>;
  openFile(target: WorkspaceFileTarget): void;
  failureMessage(): string;
}

function scopeKey(scope: WorkspaceFileScope | null): string {
  return scope ? JSON.stringify([scope.conversationId, scope.cwd, scope.workspace, scope.root]) : "";
}

/** Answer-only navigation. Attachments and explicit paths keep their existing
 * exact destination; the server searches only the originating conversation's
 * directory for a bare name. A partial search never earns an automatic open.
 */
export function createAnswerFileNavigation(options: Options) {
  const choice = shallowRef<AnswerFileChoice | null>(null);
  let version = 0;
  let pending: AbortController | null = null;
  let selection: { key: string; target: WorkspaceFileTarget; scope: WorkspaceFileScope } | null = null;

  function cancel() {
    ++version;
    pending?.abort();
    pending = null;
    selection = null;
    choice.value = null;
  }

  function reveal(target: WorkspaceFileTarget, scope: WorkspaceFileScope, request: WorkspaceFileTarget) {
    // relative_path has been validated against this conversation. Join once,
    // before the shared text/Office opener de-duplicates workspace prefixes.
    const path = scope.workspace ? `${scope.workspace}/${target.path}` : target.path;
    options.openFile({ ...target, path, lineStart: request.lineStart, lineEnd: request.lineEnd });
  }

  const open: WorkspaceFileNavigation["open"] = (request, origin) => {
    const scope = options.scope();
    if (!scope || origin.conversationId !== scope.conversationId || origin.cwd !== scope.cwd) return;
    const target = resolvedWorkspaceFileTarget(request.path, scope);
    if (!target) return;
    cancel();
    if (request.lookupByName !== true || target.path.includes("/")) {
      reveal(target, scope, request);
      return;
    }

    const controller = new AbortController();
    const mine = version;
    const key = scopeKey(scope);
    pending = controller;
    const current = () => mine === version && !controller.signal.aborted && scopeKey(options.scope()) === key;
    return (async () => {
      try {
        const result = await options.resolve(scope.conversationId, target.path, true, controller.signal);
        if (!current()) return;
        if (!result || !Array.isArray(result.candidates) || result.candidates.length > 50 ||
            !["exact", "unique", "ambiguous", "not_found", "incomplete"].includes(result.status)) throw new Error("Invalid file lookup response");
        const candidates = new Map<string, WorkspaceFileTarget>();
        for (const item of result.candidates) {
          if (!item || typeof item.relative_path !== "string") throw new Error("Invalid file lookup candidate");
          const local = resolvedWorkspaceFileTarget(item.relative_path, scope);
          if (!local || local.name !== target.name) throw new Error("File lookup escaped the requested scope");
          candidates.set(local.path, local);
        }
        const found = [...candidates.values()];
        if (result.complete === true && found.length === 1 &&
            (result.status === "unique" || result.status === "exact" && found[0]!.path === target.path)) {
          reveal(found[0]!, scope, request);
          return;
        }
        selection = { key, target: { ...target, lineStart: request.lineStart, lineEnd: request.lineEnd }, scope };
        choice.value = { name: target.name, workspace: scope.cwd, candidates: found, incomplete: result.complete !== true || result.status === "incomplete" };
      } catch {
        if (current()) choice.value = { name: target.name, workspace: scope.cwd, candidates: [], incomplete: false, error: options.failureMessage() };
      } finally {
        if (mine === version) pending = null;
      }
    })();
  };

  function choose(path: string) {
    if (!selection || selection.key !== scopeKey(options.scope())) { cancel(); return; }
    const item = choice.value?.candidates.find(candidate => candidate.path === path);
    if (!item) return;
    const { scope, target } = selection;
    cancel();
    reveal(item, scope, target);
  }

  return { choice, open, choose, cancel };
}
