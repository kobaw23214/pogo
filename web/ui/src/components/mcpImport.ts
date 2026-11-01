// Import mapping for the MCP sheet's "import JSON" dialog (the UI lives in
// SettingsDialog.vue). Pure functions with no Vue imports, so the mapping
// rules are unit-testable — see mcpImport.test.ts.
//
// The importer is the door for foreign data, not the file format itself, so
// it is deliberately tolerant where the on-disk decoder is strict: two root
// keys (pogo's own "servers" and the ecosystem's "mcpServers" — Claude,
// Cursor, Gemini, Codex), aliases for the common field names, and
// unrecognized fields collected and reported instead of failing the entry.
// What it never tolerates is an entry that would lie: the name, endpoint and
// tool-filter rules are exactly the ones the form and the server apply.

import type { MCPServerConfig } from "@/api/types";

// The same rules mcpclient/config.go applies; the settings form imports these
// too, so form and importer cannot drift apart.
export const MCP_NAME_RE = /^[A-Za-z0-9_-]{1,32}$/;
export const MCP_ENV_NAME_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;
export const MCP_TOOL_PATTERN_RE = /^[A-Za-z0-9_.-]+\*?$/;

// A per-entry verdict. code is an i18n suffix — the dialog renders it as
// settingsDialog.mcpImportErr_<code> with params as the interpolation values.
export interface MCPImportError {
  code: "name" | "endpoint" | "url" | "bearer" | "bearerToken" | "bearerBoth" | "toolFilter" | "crossField" | "notObject";
  params?: Record<string, string>;
}

export interface MCPImportEntry {
  name: string;
  /** The mapped config; null when error is set. */
  config: MCPServerConfig | null;
  error: MCPImportError | null;
  /** Source fields dropped on the way in (unrecognized, or unusably typed). */
  ignored: string[];
}

export type MCPImportResult =
  | { ok: true; entries: MCPImportEntry[] }
  | { ok: false; reason: "syntax" | "noServers"; detail?: string };

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return !!v && typeof v === "object" && !Array.isArray(v);
}

export function parseMCPImport(text: string): MCPImportResult {
  let raw: unknown;
  try {
    raw = JSON.parse(text);
  } catch (err) {
    return { ok: false, reason: "syntax", detail: err instanceof Error ? err.message : String(err) };
  }
  if (!isPlainObject(raw)) return { ok: false, reason: "noServers" };
  // pogo's own root wins when both exist — it is the file's real shape.
  const servers = isPlainObject(raw.servers)
    ? raw.servers
    : isPlainObject(raw.mcpServers)
      ? raw.mcpServers
      : null;
  if (!servers) return { ok: false, reason: "noServers" };
  return { ok: true, entries: Object.entries(servers).map(([name, sc]) => mapImportEntry(name, sc)) };
}

function mapImportEntry(name: string, raw: unknown): MCPImportEntry {
  if (!isPlainObject(raw)) {
    return { name, config: null, error: { code: "notObject" }, ignored: [] };
  }
  const { config, ignored } = mapImportServer(raw);
  const error = validateImportServer(name, config);
  return { name, config: error ? null : config, error, ignored };
}

// mapImportServer folds one foreign entry onto MCPServerConfig. Alias
// resolution is order-independent: the native field always beats its aliases
// (url over httpUrl, tool_timeout_ms over timeout, include_tools over
// includeTools/enabled_tools, bearer_token_env over bearer_token_env_var),
// and an explicit transport beats the type:"sse" shorthand. A recognized
// field with an unusable value (an args that is not an array, a timeout that
// is not a number) joins the ignored list rather than being guessed at.
export function mapImportServer(raw: Record<string, unknown>): { config: MCPServerConfig; ignored: string[] } {
  const config: MCPServerConfig = {};
  const ignored: string[] = [];

  const asStr = (v: unknown): string | null => (typeof v === "string" ? v : null);
  const asNum = (v: unknown): number | null =>
    typeof v === "number" && Number.isFinite(v) ? Math.trunc(v) : null;
  const asList = (v: unknown): string[] | null =>
    Array.isArray(v) ? v.filter((x) => typeof x === "string") : null;
  const asMap = (v: unknown): Record<string, string> | null => {
    if (!isPlainObject(v)) return null;
    const out: Record<string, string> = {};
    for (const [k, val] of Object.entries(v)) out[k] = String(val);
    return out;
  };

  for (const [k, v] of Object.entries(raw)) {
    switch (k) {
      case "command":
      case "cwd":
      case "url": {
        const s = asStr(v);
        if (s !== null) config[k] = s;
        else ignored.push(k);
        break;
      }
      case "httpUrl": {
        // Gemini's remote key: streamable HTTP, so no transport lands.
        const s = asStr(v);
        if (s !== null) {
          if (config.url === undefined) config.url = s;
        } else ignored.push(k);
        break;
      }
      case "args": {
        const l = asList(v);
        if (l) config.args = l;
        else ignored.push(k);
        break;
      }
      case "env":
      case "headers": {
        const m = asMap(v);
        if (m) config[k] = m;
        else ignored.push(k);
        break;
      }
      case "type":
        // The ecosystem's transport shorthand; only sse says anything the
        // default does not. Unrecognized values are ignored, not guessed.
        if (v === "sse") config.transport = "sse";
        else if (v === "stdio" || v === "http" || v === "streamable") break;
        else ignored.push(k);
        break;
      case "transport":
        if (v === "sse") config.transport = "sse";
        else if (v === "streamable") delete config.transport; // explicit beats type:"sse"
        else ignored.push(k);
        break;
      case "trust":
      case "required":
        if (typeof v === "boolean") {
          if (v) config[k] = true; // false is the default; omit it
        } else ignored.push(k);
        break;
      case "enabled":
        if (typeof v === "boolean") {
          if (!v) config.enabled = false; // true is the default; omit it
        } else ignored.push(k);
        break;
      case "disabled":
        if (typeof v === "boolean") {
          if (v) config.enabled = false;
        } else ignored.push(k);
        break;
      case "timeout": {
        // Gemini's per-call budget, in ms — our tool_timeout_ms.
        const n = asNum(v);
        if (n !== null) {
          if (config.tool_timeout_ms === undefined) config.tool_timeout_ms = n;
        } else ignored.push(k);
        break;
      }
      case "tool_timeout_ms":
      case "startup_timeout_ms": {
        const n = asNum(v);
        if (n !== null) config[k] = n;
        else ignored.push(k);
        break;
      }
      case "include_tools":
      case "includeTools":
      case "enabled_tools": {
        const l = asList(v);
        if (l) {
          // The native field always wins; between aliases, the first wins.
          if (k === "include_tools" || config.include_tools === undefined) config.include_tools = l;
        } else ignored.push(k);
        break;
      }
      case "exclude_tools":
      case "excludeTools":
      case "disabled_tools": {
        const l = asList(v);
        if (l) {
          if (k === "exclude_tools" || config.exclude_tools === undefined) config.exclude_tools = l;
        } else ignored.push(k);
        break;
      }
      case "bearer_token_env":
      case "bearer_token_env_var": {
        const s = asStr(v);
        if (s !== null) {
          if (k === "bearer_token_env" || config.bearer_token_env === undefined) config.bearer_token_env = s;
        } else ignored.push(k);
        break;
      }
      case "bearer_token": {
        // The literal-token form: passes through verbatim; the $-prefix and
        // blank-value rules are the validator's, not the mapper's.
        const s = asStr(v);
        if (s !== null) config.bearer_token = s;
        else ignored.push(k);
        break;
      }
      default:
        ignored.push(k);
    }
  }
  return { config, ignored };
}

// validateImportServer applies the same rules as the settings form (and the
// server's own Validate): a legal name, exactly one endpoint, no fields from
// the other endpoint's side, http(s) URLs only, a variable *name* in
// bearer_token_env, and plain-or-prefix-wildcard tool filters.
export function validateImportServer(name: string, sc: MCPServerConfig): MCPImportError | null {
  if (!MCP_NAME_RE.test(name)) return { code: "name", params: { name } };
  const hasCmd = !!sc.command?.trim();
  const hasURL = !!sc.url?.trim();
  if (hasCmd === hasURL) return { code: "endpoint" };
  if (hasCmd) {
    const bad: string[] = [];
    if (sc.transport) bad.push("transport");
    if (sc.headers && Object.keys(sc.headers).length) bad.push("headers");
    if (sc.bearer_token_env) bad.push("bearer_token_env");
    if (sc.bearer_token) bad.push("bearer_token");
    if (bad.length) return { code: "crossField", params: { fields: bad.join(", ") } };
  } else {
    const bad: string[] = [];
    if (sc.args?.length) bad.push("args");
    if (sc.env && Object.keys(sc.env).length) bad.push("env");
    if (sc.cwd?.trim()) bad.push("cwd");
    if (bad.length) return { code: "crossField", params: { fields: bad.join(", ") } };
    if (!/^https?:\/\//.test(sc.url!.trim())) return { code: "url" };
    if (sc.bearer_token_env && !MCP_ENV_NAME_RE.test(sc.bearer_token_env)) return { code: "bearer" };
    // The literal form: mutually exclusive with the variable form, never a
    // $-reference (that is what the variable form is for), never blank.
    if (sc.bearer_token !== undefined) {
      if (sc.bearer_token_env) return { code: "bearerBoth" };
      if (sc.bearer_token.startsWith("$") || !sc.bearer_token.trim()) return { code: "bearerToken" };
    }
  }
  for (const [field, ps] of [
    ["include_tools", sc.include_tools ?? []],
    ["exclude_tools", sc.exclude_tools ?? []],
  ] as const) {
    for (const p of ps) {
      if (!MCP_TOOL_PATTERN_RE.test(p)) return { code: "toolFilter", params: { field, pattern: p } };
    }
  }
  return null;
}
