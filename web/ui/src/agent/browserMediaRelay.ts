import type { NativeBrowserTarget } from "./nativeBrowser";

export type BrowserMediaRelayBinding = NativeBrowserTarget & { generation: number; media_epoch: string };
export interface BrowserMediaRelayTicket extends BrowserMediaRelayBinding {
  ice_servers: [{ urls: string; username: string; credential: string }];
  ice_transport_policy: "relay";
  expires_in: number;
}
const record = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);
const exact = (value: Record<string, unknown>, keys: string[]) => Object.keys(value).every(key => keys.includes(key));

/** TURN shares the Web authority, but is a separate protocol on that port.
 * Neither the worker nor a page can select an arbitrary ICE service. */
export function browserMediaRelayURL(origin: string): string | null {
  let web: URL;
  try { web = new URL(origin); } catch { return null; }
  if (!["http:", "https:"].includes(web.protocol) || web.origin !== origin || web.username || web.password) return null;
  return `${web.protocol === "https:" ? "turns" : "turn"}:${web.hostname}:${web.port || (web.protocol === "https:" ? "443" : "80")}?transport=tcp`;
}

/** This credential only lives in the receiver configuration. It never grants
 * page input ownership, goes into a worker message, or enters diagnostics. */
export function readBrowserMediaRelayTicket(value: unknown, binding: BrowserMediaRelayBinding, origin: string, requestedAt: number, now = Date.now()): RTCConfiguration | null {
  const endpoint = browserMediaRelayURL(origin);
  if (!endpoint || !record(value) || !exact(value, ["context_ref", "tab_id", "generation", "media_epoch", "ice_servers", "ice_transport_policy", "expires_in"]) ||
      value.context_ref !== binding.context_ref || value.tab_id !== binding.tab_id || value.generation !== binding.generation || value.media_epoch !== binding.media_epoch ||
      value.ice_transport_policy !== "relay" || !Number.isSafeInteger(value.expires_in) || Number(value.expires_in) < 1 || Number(value.expires_in) > 300 ||
      !Number.isFinite(requestedAt) || !Number.isFinite(now) || now < requestedAt || now - requestedAt >= Number(value.expires_in) * 1000 ||
      !Array.isArray(value.ice_servers) || value.ice_servers.length !== 1) return null;
  const server = value.ice_servers[0];
  if (!record(server) || !exact(server, ["urls", "username", "credential"]) || server.urls !== endpoint ||
      [server.username, server.credential].some(field => typeof field !== "string" || !field.length || field.length > 1024 || /[\s\x00-\x1f\x7f]/.test(field))) return null;
  return { iceServers: [{ urls: endpoint, username: server.username as string, credential: server.credential as string }], iceTransportPolicy: "relay" };
}
