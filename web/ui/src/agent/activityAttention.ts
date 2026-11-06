import { getCurrentScope, onScopeDispose, watchEffect } from "vue";
import type { Composer } from "vue-i18n";
import { gt, i18n } from "@/i18n";

export interface ActivityAttentionInput {
  sessionId?: string;
  enabled: boolean;
  approvalIds: readonly string[];
  answerIds: readonly string[];
}

export interface ActivityAttentionTitleTarget {
  title: string;
  head?: Node | null;
  documentElement?: Node | null;
}

/** The categories have separate request namespaces. Repeated instances of the
 * same approval/question in the transcript and sticky bar count only once. */
export function formatActivityAttention(title: string, input: ActivityAttentionInput): string {
  if (!input.enabled || !input.sessionId?.trim()) return title;
  const unique = (ids: readonly string[]) => new Set(ids.filter(id => typeof id === "string" && id.trim().length > 0)).size;
  const approvals = unique(input.approvalIds), answers = unique(input.answerIds);
  if (!approvals && !answers) return title;
  const kind = approvals && answers ? "mixed" : approvals ? "approval" : "answer";
  return gt(`activityRun.attention.${kind}`, { n: approvals + answers, title });
}

/** Owns only the prefix it rendered. A route or another title writer may
 * replace/append to document.title while the pending state remains active. */
export function createActivityAttention(target: ActivityAttentionTitleTarget) {
  let current: ActivityAttentionInput = { enabled: false, approvalIds: [], answerIds: [] };
  let base = target.title;
  let rendered: string | undefined;
  let prefix = "";
  let disposed = false;
  function unprefix(value: string) {
    if (rendered !== undefined && value === rendered) return base;
    return prefix && value.startsWith(prefix) ? value.slice(prefix.length) : value;
  }
  function refresh() {
    if (disposed) return;
    base = unprefix(target.title);
    const next = formatActivityAttention(base, current);
    rendered = next === base ? undefined : next;
    // Current translations put the supplied title last. Restrict ownership to
    // the known exact prefix; never strip arbitrary parenthesized user titles.
    prefix = rendered !== undefined && next.endsWith(base) ? next.slice(0, next.length - base.length) : "";
    if (target.title !== next) target.title = next;
  }
  function update(input: ActivityAttentionInput) { current = input; refresh(); }
  function dispose() {
    if (disposed) return;
    const clean = unprefix(target.title);
    if (target.title !== clean) target.title = clean;
    disposed = true;
  }
  return { update, refresh, dispose };
}

/** Reactive title-only attention. No browser notification or permission request
 * is involved, and a fresh mount recomputes from current pending facts. */
export function useActivityAttention(
  input: () => ActivityAttentionInput,
  target: ActivityAttentionTitleTarget | undefined = typeof document === "undefined" ? undefined : document,
) {
  if (!target) return { refresh: () => {}, dispose: () => {} };
  const controller = createActivityAttention(target);
  const stop = watchEffect(() => {
    // Explicitly track locale even if a translator implementation caches t().
    void (i18n.global as unknown as Composer).locale.value;
    controller.update(input());
  });
  const node = target.head ?? target.documentElement;
  const observer = typeof MutationObserver !== "undefined" && node
    ? new MutationObserver(() => controller.refresh()) : undefined;
  observer?.observe(node!, { childList: true, subtree: true, characterData: true });
  let disposed = false;
  function dispose() {
    if (disposed) return;
    disposed = true;
    stop(); observer?.disconnect(); controller.dispose();
  }
  if (getCurrentScope()) onScopeDispose(dispose);
  return { refresh: controller.refresh, dispose };
}
