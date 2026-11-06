// The equivalent-curl board for the MCP edit form (the UI lives in
// SettingsDialog.vue). Pure, no Vue imports — the templates are unit-tested
// in mcpCurl.test.ts.
//
// The board shows what the "test connection" button does, as commands a
// person can run by hand: the initialize handshake plus tools/list for the
// HTTP transports, the spawned command line for stdio. Shell style is POSIX
// single quotes (the user's environment is Git Bash). No secret ever appears:
// a bearer token is referenced by the *name* of its environment variable,
// which is all the file — and therefore this board — ever knows.

import type { MCPServerConfig } from "@/api/types";
import { gt } from "@/i18n";

// sq single-quotes POSIX-style: everything literal except a single quote
// itself, which closes, escapes and reopens ('\'').
export function sq(s: string): string {
  return "'" + s.replace(/'/g, "'\\''") + "'";
}

// shArg leaves obviously safe words bare and quotes the rest — a readable
// command line that stays paste-safe.
const SHELL_SAFE_RE = /^[A-Za-z0-9_@%+=:,./-]+$/;
function shArg(s: string): string {
  return SHELL_SAFE_RE.test(s) ? s : sq(s);
}

// The bodies the probe's handshake speaks: initialize first (protocol version
// is the one the client offers), tools/list second.
const INITIALIZE_BODY = JSON.stringify({
  jsonrpc: "2.0",
  id: 1,
  method: "initialize",
  params: {
    protocolVersion: "2025-11-25",
    capabilities: {},
    clientInfo: { name: "pogo-mcp-client", version: "1" },
  },
});
const TOOLS_LIST_BODY = JSON.stringify({ jsonrpc: "2.0", id: 2, method: "tools/list", params: {} });

// The headers both HTTP transports carry on every call: the bearer reference
// first, then the custom headers in file order, values verbatim — a $VAR
// stays a $VAR, it expands in the user's shell just as it does at connect
// time. The two bearer forms: a variable renders as its $NAME (never a
// value), a literal token is masked to *** — the board is for reading and
// pasting in a shared terminal, and the probe is the only thing that should
// ever send the real one.
function authHeaderLines(sc: MCPServerConfig): string[] {
  const lines: string[] = [];
  if (sc.bearer_token?.trim()) {
    lines.push(`-H ${sq("Authorization: Bearer ***")}`);
  } else if (sc.bearer_token_env?.trim()) {
    lines.push(`-H ${sq(`Authorization: Bearer $${sc.bearer_token_env.trim()}`)}`);
  }
  for (const [k, v] of Object.entries(sc.headers ?? {})) {
    lines.push(`-H ${sq(`${k}: ${v}`)}`);
  }
  return lines;
}

// cont indents continuation lines and closes each with the backslash; the
// final -d line is appended unterminated by the caller.
function cont(lines: string[]): string[] {
  return lines.map((l) => `  ${l} \\`);
}

// streamable: one endpoint, JSON-RPC POSTs; the session id comes back on the
// handshake's response header and rides every later call.
function streamableCurl(sc: MCPServerConfig): string {
  const url = sc.url?.trim() || "<url>";
  const base = [`-H ${sq("Content-Type: application/json")}`, `-H ${sq("Accept: application/json, text/event-stream")}`, ...authHeaderLines(sc)];
  return [
    gt("settingsCoverage.curl.initialize"),
    `curl -i -N -X POST ${sq(url)} \\`,
    ...cont(base),
    `  -d ${sq(INITIALIZE_BODY)}`,
    "",
    gt("settingsCoverage.curl.listTools"),
    `curl -N -X POST ${sq(url)} \\`,
    ...cont([...base, `-H ${sq("Mcp-Session-Id: <MCP_SESSION_ID>")}`]),
    `  -d ${sq(TOOLS_LIST_BODY)}`,
  ].join("\n");
}

// sse (the legacy 2024-11-05 transport): two endpoints — a GET stream that
// hands back a per-session POST path in its endpoint event, then JSON-RPC
// POSTs to that path.
function sseCurl(sc: MCPServerConfig): string {
  const url = sc.url?.trim() || "<url>";
  const auth = authHeaderLines(sc);
  // The POST target is the origin plus whatever path the endpoint event
  // named; parse the origin out when the URL parses, else keep the raw text.
  let origin = url;
  try {
    origin = new URL(url).origin;
  } catch {
    // an unparseable URL keeps the raw text as the placeholder it already is
  }
  // Keep replacement markers stable across languages, while translating only
  // the explanatory comments around these literal shell commands.
  const post = `${origin}<post-path>`;
  const postHead = [`-H ${sq("Content-Type: application/json")}`, ...auth];
  return [
    gt("settingsCoverage.curl.sseOpen"),
    `curl -N ${sq(url)} \\`,
    ...cont([`-H ${sq("Accept: text/event-stream")}`, ...auth]),
    `  ${gt("settingsCoverage.curl.sseKeepOpen")}`,
    "",
    gt("settingsCoverage.curl.sseInitialize"),
    `curl -N -X POST ${sq(post)} \\`,
    ...cont(postHead),
    `  -d ${sq(INITIALIZE_BODY)}`,
    "",
    gt("settingsCoverage.curl.sseListTools"),
    `curl -N -X POST ${sq(post)} \\`,
    ...cont(postHead),
    `  -d ${sq(TOOLS_LIST_BODY)}`,
  ].join("\n");
}

// stdio: no curl at all — the equivalent is the child process pogo spawns:
// cwd first when set (as a comment, it is pogo's chdir not a shell cd), then
// env prefixes and the command line. Env values are verbatim, $VAR included.
function stdioCurl(sc: MCPServerConfig): string {
  const lines = [gt("settingsCoverage.curl.stdio")];
  if (sc.cwd?.trim()) {
    lines.push(gt("settingsCoverage.curl.stdioCwd", { directory: sq(sc.cwd.trim()) }));
  }
  const env = Object.entries(sc.env ?? {}).map(([k, v]) => `${k}=${sq(v)}`);
  const cmd = [shArg(sc.command?.trim() || "<command>"), ...(sc.args ?? []).map(shArg)];
  lines.push([...env, ...cmd].join(" "));
  return lines.join("\n");
}

// buildCurlPreview renders the board for one entry. The form always sets
// exactly one side (buildMCPServer projects by the kind switch), so a url
// key's presence picks the HTTP boards; everything else is stdio.
export function buildCurlPreview(sc: MCPServerConfig): string {
  if (sc.url !== undefined) {
    return sc.transport === "sse" ? sseCurl(sc) : streamableCurl(sc);
  }
  return stdioCurl(sc);
}
