/** Management locations and published conversation links are distinct surfaces. */
export type StudioSection = "agents" | "information" | "models" | "mcp" | "skills" | "account" | "settings" | "memory";

export type StudioRoute =
  | { kind: "manage"; section: StudioSection }
  | { kind: "chat"; agentId: string; managedChat: boolean; embedded: boolean }
  | { kind: "invalid"; management: boolean };

export function parseStudioRoute(pathname: string, search = ""): StudioRoute | null {
  // "/" is the home address: the same manage surface as /manage, section
  // "agents". The exact "/manage" spelling stays valid as the canonical
  // address of that section.
  if (pathname === "/" || pathname === "") return { kind: "manage", section: "agents" };
  if (/^\/manage\/?$/.test(pathname)) return { kind: "manage", section: "agents" };
  const section = /^\/manage\/(information|models|mcp|skills|account|settings|memory)\/?$/.exec(pathname);
  if (section) return { kind: "manage", section: section[1] as StudioSection };
  const preview = /^\/manage\/agents\/([a-zA-Z0-9_-]+)\/chat\/?$/.exec(pathname);
  if (preview) return { kind: "chat", agentId: preview[1]!, managedChat: true, embedded: false };
  const published = /^\/a\/([a-zA-Z0-9_-]+)\/?$/.exec(pathname);
  if (published) {
    return {
      kind: "chat", agentId: published[1]!, managedChat: false,
      embedded: new URLSearchParams(search).get("embed") === "1",
    };
  }
  if (pathname.startsWith("/manage/")) return { kind: "invalid", management: true };
  if (pathname === "/a" || pathname.startsWith("/a/")) return { kind: "invalid", management: false };
  return null;
}

export function isStudioPath(path: string): boolean {
  return parseStudioRoute(path) !== null;
}

export function studioAgentId(path: string): string | undefined {
  const route = parseStudioRoute(path);
  return route?.kind === "chat" ? route.agentId : undefined;
}

/** Only known local routes may be pushed; never navigate to external or malformed URLs. */
export function isStudioDestination(path: string): boolean {
  if (!/^\/(?!\/)/.test(path) || /[\\\r\n\t#]/.test(path)) return false;
  const pathname = path.split("?", 1)[0]!;
  const route = parseStudioRoute(pathname);
  return route !== null && route.kind !== "invalid";
}

/**
 * The agent-less workbench no longer has an address: every conversation lives
 * behind a created Agent's link. Any path that would still mount the plain
 * workbench — a stray /foo, an old bookmark, /embed without its slash — lands
 * on the home route (/) instead. Two surfaces keep the app shell: the public
 * /share/ pages and the exact /embed/ delegated mount (the business-backend
 * iframe, whose conversations are its own product surface, not studio Agents').
 */
export function workbenchRedirect(pathname: string): string | null {
  if (parseStudioRoute(pathname) !== null) return null;
  if (pathname.startsWith("/share/")) return null;
  if (pathname === "/embed/") return null;
  return "/";
}
