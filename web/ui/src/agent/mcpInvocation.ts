/** A user-selected method identity. Descriptions and arguments come from the
 * server's live catalog, never from clipboard or saved draft metadata. */
export interface MCPReference { server: string; name: string }

export function validMcpReference(value: unknown): value is MCPReference {
  if (!value || typeof value !== "object") return false;
  const reference = value as Record<string, unknown>;
  const valid = typeof reference.server === "string" && /^[A-Za-z0-9_-]{1,32}$/.test(reference.server)
    && typeof reference.name === "string" && reference.name.length > 0 && new TextEncoder().encode(reference.name).length <= 512
    && !/[\s\p{Cc}\p{Cf}"'`<>\\]/u.test(reference.name);
  if (!valid) return false;
  try { encodeURIComponent(reference.name as string); return true; } catch { return false; }
}

export function mcpInvocationText(server: string, name: string): string {
  return `/mcp:${encodeURIComponent(server)}/${encodeURIComponent(name)}`;
}

export interface MCPInvocation extends MCPReference { body: string; completed: boolean }

/** Exact leading invocation only; filenames, embedded prose and frozen material
 * contents must never become executable references through substring matching. */
export function parseMcpInvocation(text: string): MCPInvocation | null {
  const match = /^\/mcp:([^\s/]+)\/([^\s/]+)(?:(\r\n|[ \t\n\r\f\v])([\s\S]*))?$/.exec(text);
  if (!match) return null;
  try {
    const reference = { server: decodeURIComponent(match[1]), name: decodeURIComponent(match[2]) };
    return validMcpReference(reference) ? { ...reference, body: match[4] ?? "", completed: !!match[3] } : null;
  } catch { return null; }
}
