// The "this machine as an MCP server" section of the MCP sheet (the UI lives
// in SettingsDialog.vue): the row↔config projection for mcp.json's "server"
// section, pure and Vue-free so the mutex and the validation rules are
// testable — see mcpServe.test.ts.
//
// Same stance as the client side's bearerMode: the token's three states (off
// / environment variable / literal) map onto two mutually exclusive fields in
// exactly one place — the projection — so a save can never ship both.

import type { MCPServeConfig, MCPServerState } from "@/api/types";
import { MCP_ENV_NAME_RE, MCP_TOOL_PATTERN_RE } from "./mcpImport";

export type ServeTokenMode = "off" | "env" | "direct";

// One row of the serve form: the section's fields, with the token pair folded
// into a mode plus one value each way. policy "" means the default
// ("standard"); the quota fields are undefined when left empty (the server's
// defaults: 8 / 60 / 32).
export interface MCPServeRow {
  enabled: boolean;
  tokenMode: ServeTokenMode;
  tokenEnv: string;
  token: string;
  policy: "" | "strict" | "standard" | "auto";
  /** The tool allowlist, comma-separated in the form (mal.*, agent_task_status);
      empty exposes every tool. */
  includeText: string;
  maxConcurrent: number | undefined;
  submitRpm: number | undefined;
  maxWaits: number | undefined;
}

// serveTokenMode infers the form's initial radio from a loaded section: a
// literal token wins over a variable name (it is the more explicit choice),
// neither means auth is off.
export function serveTokenMode(sc?: MCPServeConfig): ServeTokenMode {
  if (sc?.token) return "direct";
  if (sc?.token_env) return "env";
  return "off";
}

export function rowFromServeConfig(sc?: MCPServeConfig): MCPServeRow {
  return {
    enabled: sc?.enabled ?? false,
    tokenMode: serveTokenMode(sc),
    tokenEnv: sc?.token_env ?? "",
    token: sc?.token ?? "",
    policy: sc?.policy ?? "",
    includeText: (sc?.include_tools ?? []).join(", "),
    maxConcurrent: sc?.max_concurrent || undefined,
    submitRpm: sc?.submit_rpm || undefined,
    maxWaits: sc?.max_waits || undefined,
  };
}

// buildServeConfig projects a row back onto the section's shape. enabled is
// always written: the section defaults to off, and a saved "off" must not
// read as "unset". Only the mode's token field is emitted — never both.
export function buildServeConfig(r: MCPServeRow): MCPServeConfig {
  const sc: MCPServeConfig = { enabled: r.enabled };
  if (r.tokenMode === "env" && r.tokenEnv.trim()) sc.token_env = r.tokenEnv.trim();
  if (r.tokenMode === "direct" && r.token.trim()) sc.token = r.token.trim();
  if (r.policy) sc.policy = r.policy;
  const include = r.includeText.split(",").map((s) => s.trim()).filter(Boolean);
  if (include.length) sc.include_tools = include;
  if (r.maxConcurrent) sc.max_concurrent = r.maxConcurrent;
  if (r.submitRpm) sc.submit_rpm = r.submitRpm;
  if (r.maxWaits) sc.max_waits = r.maxWaits;
  return sc;
}

// validateServeRow: the same checks the client side applies — a variable
// must be a *name*, a literal must not be a $-reference nor blank, and the
// allowlist takes plain names or a single trailing-* prefix (the server
// rejects any other glob). Empty either way is legal (no auth, all tools).
export interface ServeRowError {
  code: "tokenEnv" | "token" | "includeTools";
  /** The offending allowlist entry, for includeTools. */
  param?: string;
}

export function validateServeRow(r: MCPServeRow): ServeRowError | null {
  if (r.tokenMode === "env") {
    if (r.tokenEnv.trim() && !MCP_ENV_NAME_RE.test(r.tokenEnv.trim())) return { code: "tokenEnv" };
  } else if (r.tokenMode === "direct") {
    const tok = r.token;
    if (tok && (tok.startsWith("$") || !tok.trim())) return { code: "token" };
  }
  for (const p of r.includeText.split(",").map((s) => s.trim()).filter(Boolean)) {
    if (!MCP_TOOL_PATTERN_RE.test(p)) return { code: "includeTools", param: p };
  }
  return null;
}

// ServeStatusParts is the status line's raw material, flattened and
// defaulted so the component only composes i18n text from it. policy falls
// back to "standard" (the backend's default), source to "default" when the
// backend does not say.
export interface ServeStatusParts {
  on: boolean;
  tokenRequired: boolean;
  policy: string;
  source: "env" | "file" | "default";
  restart: boolean;
  endpoints: string[];
}

export function serveStatusParts(st?: MCPServerState | null): ServeStatusParts {
  return {
    on: !!st?.enabled,
    tokenRequired: !!st?.token_required,
    policy: st?.policy || "standard",
    source: st?.source ?? "default",
    restart: !!st?.restart_required,
    endpoints: st?.endpoints ?? [],
  };
}
