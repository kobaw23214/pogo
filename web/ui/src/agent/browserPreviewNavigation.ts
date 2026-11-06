import type { DockTab } from "@/components/dockSheets";

export interface WebsiteBrowserRequest { url: string; title: string; sessionId: string; serviceId: string }

/** Only an existing website sheet for this conversation may send its already
 * validated grant to a new native Page. This does not renew or reopen a site. */
export function websiteBrowserRequestCurrent(request: WebsiteBrowserRequest, sessionId: string | null, tabs: readonly DockTab[]): boolean {
  if (!sessionId || request.sessionId !== sessionId || !request.serviceId || !tabs.some(tab => tab.kind === "website" && tab.parentSession === sessionId && tab.webServiceId === request.serviceId)) return false;
  try {
    const url = new URL(request.url);
    if (!["http:", "https:"].includes(url.protocol) || url.username || url.password || url.search || url.hash || /[\r\n]/.test(request.url)) return false;
    return /^\/_pogo\/web\/[a-f0-9]{48}\/$/.test(url.pathname) || url.pathname === "/" && /^[a-f0-9]{48}\.[A-Za-z0-9.-]+$/.test(url.hostname);
  } catch { return false; }
}
