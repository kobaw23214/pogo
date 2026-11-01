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
    "# 1) initialize 握手；-i 打印响应头，从里面拿 Mcp-Session-Id",
    `curl -i -N -X POST ${sq(url)} \\`,
    ...cont(base),
    `  -d ${sq(INITIALIZE_BODY)}`,
    "",
    "# 2) 列出工具（把上一步响应头里的 Mcp-Session-Id 填进来）",
    `curl -N -X POST ${sq(url)} \\`,
    ...cont([...base, `-H ${sq("Mcp-Session-Id: <填上一步的响应头值>")}`]),
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
  const post = `${origin}<回发路径>`;
  const postHead = [`-H ${sq("Content-Type: application/json")}`, ...auth];
  return [
    "# 1) 旧版 SSE 传输：先挂流；流里的 endpoint 事件会给出回发路径（形如 /messages/?session_id=xxx）",
    `curl -N ${sq(url)} \\`,
    ...cont([`-H ${sq("Accept: text/event-stream")}`, ...auth]),
    `  # ↑ 这条一直挂着；真正的 POST 在下面`,
    "",
    "# 2) 往回发路径 POST initialize（地址 = 源站 + 上一步给的路径）",
    `curl -N -X POST ${sq(post)} \\`,
    ...cont(postHead),
    `  -d ${sq(INITIALIZE_BODY)}`,
    "",
    "# 3) 同一个回发地址上列出工具",
    `curl -N -X POST ${sq(post)} \\`,
    ...cont(postHead),
    `  -d ${sq(TOOLS_LIST_BODY)}`,
  ].join("\n");
}

// stdio: no curl at all — the equivalent is the child process pogo spawns:
// cwd first when set (as a comment, it is pogo's chdir not a shell cd), then
// env prefixes and the command line. Env values are verbatim, $VAR included.
function stdioCurl(sc: MCPServerConfig): string {
  const lines = ["# stdio 是本地子进程，没有 curl；pogo 实际执行的是："];
  if (sc.cwd?.trim()) {
    lines.push(`# （指定了 cwd：pogo 先 cd ${sq(sc.cwd.trim())} 再起进程）`);
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
