import { hostAPIError } from "./hostErrorCopy";
import { gt, getLocale } from "@/i18n";
import { authHeaders, isDelegatedEmbed } from "./client";
import type { BrowserBookmark } from "@/agent/browserBookmarks";

export interface AccountBookmarksSnapshot {
  source: "pogo_bookmarks";
  scope: "account" | "agent";
  agent_id?: string;
  scope_id?: string;
  owner_account_id?: string;
  status: "ready" | "not_initialized";
  account_id: string;
  revision: number;
  bookmarks: BrowserBookmark[];
  total: number;
}
export interface AccountBookmarksWrite {
  account_id: string;
  revision: number;
  bookmarks: BrowserBookmark[];
}
export interface AccountBookmarksAPI {
  read(signal?: AbortSignal): Promise<AccountBookmarksSnapshot>;
  write(input: AccountBookmarksWrite, signal?: AbortSignal): Promise<AccountBookmarksSnapshot>;
}

async function request(method: string, body?: AccountBookmarksWrite, signal?: AbortSignal, agentId?: string): Promise<AccountBookmarksSnapshot> {
  // This account API is not part of the delegated embed protocol. Never send
  // an embed visitor's browser-local data to the host's ordinary account API.
  if (isDelegatedEmbed) throw new Error(gt("uiCoverage.connection.bookmarksNotAvailable"));
  const response = await fetch("/api/browser/bookmarks" + (agentId ? `?agent_id=${encodeURIComponent(agentId)}` : ""), {
    method, credentials: "same-origin", cache: "no-store", signal,
    headers: { "Accept-Language": getLocale(), ...authHeaders(), ...(body ? { "Content-Type": "application/json" } : {}) },
    body: body ? JSON.stringify(body) : undefined,
  });
  const raw = await response.text();
  let result: unknown;
  try { result = JSON.parse(raw); } catch { result = undefined; }
  if (!response.ok) {
    throw Object.assign(hostAPIError((result as { error?: string } | undefined)?.error || raw || `HTTP ${response.status}`), { status: response.status });
  }
  return result as AccountBookmarksSnapshot;
}

export const accountBookmarksAPI: AccountBookmarksAPI = {
  read: signal => request("GET", undefined, signal),
  write: (input, signal) => request("PUT", input, signal),
};

export function agentBookmarksAPI(agentId: string): AccountBookmarksAPI {
  if (!agentId.trim()) throw new Error(gt("uiCoverage.connection.bookmarksAgentRequired"));
  return {
    read: signal => request("GET", undefined, signal, agentId),
    write: (input, signal) => request("PUT", input, signal, agentId),
  };
}
