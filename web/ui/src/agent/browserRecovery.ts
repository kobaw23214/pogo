import { normalizeUrl } from "./browserUrl";

export type BrowserRecoveryReason = "context_changed" | "tab_closed" | "session_expired";
const codes: Record<string, { reason: BrowserRecoveryReason; statuses: number[] }> = {
  BROWSER_CONTEXT_CHANGED: { reason: "context_changed", statuses: [409] },
  BROWSER_TAB_CLOSED: { reason: "tab_closed", statuses: [410] },
  BROWSER_SESSION_EXPIRED: { reason: "session_expired", statuses: [404, 410] },
};
const legacy = new Map<string, BrowserRecoveryReason>([
  ["409:browser context changed; this saved tab binding is no longer current", "context_changed"],
  ["409:browser context changed; inspect current browser state", "context_changed"],
  ["410:bound browser tab is closed", "tab_closed"],
  ["410:browser tab closed during takeover", "tab_closed"],
  ["404:browser session not found", "session_expired"],
  ["410:browser session was closed; create a new session", "session_expired"],
]);

/** Only retired identities authorize discovery. Busy actions, debugger pauses,
 * uncertain input and ordinary connection failures never reset a target. */
export function browserRecoveryReason(error: unknown): BrowserRecoveryReason | undefined {
  if (!error || typeof error !== "object") return;
  const issue = error as { code?: unknown; status?: unknown; message?: unknown };
  if (typeof issue.code === "string" && issue.code) {
    const code = codes[issue.code];
    return code && (issue.status === undefined || typeof issue.status === "number" && code.statuses.includes(issue.status)) ? code.reason : undefined;
  }
  if (typeof issue.status !== "number" || typeof issue.message !== "string") return;
  const prefix = `native browser service returned HTTP ${issue.status}: `;
  const message = issue.message.startsWith(prefix) ? issue.message.slice(prefix.length) : issue.message;
  return legacy.get(`${issue.status}:${message}`);
}

/** Reopening is a new explicit navigation, never reuse of an old binding. */
export function browserReopenURL(value: string | undefined): string | undefined {
  if (!value || !/^https?:\/\//i.test(value.trim()) || /[\u0000-\u001f\u007f]/.test(value)) return;
  const normalized = normalizeUrl(value, "http://localhost/");
  if (!normalized) return;
  const url = new URL(normalized);
  if (url.username || url.password) return;
  return url.href;
}

export function browserErrorDetails(error: unknown): string {
  return (error instanceof Error ? error.message : typeof error === "string" ? error : "").slice(0, 4096);
}
