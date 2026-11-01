// Export mapping for the MCP sheet's "export config" dialog (the UI lives in
// McpExportDialog.vue). Pure functions with no Vue imports — same stance as
// mcpImport.ts: every mapping rule is unit-testable, see mcpExport.test.ts.
//
// Recognized credential fields are not emitted literally in the preview
// JSON. Literal secrets (bearer_token, sensitive header/env values,
// URL userinfo or secret-looking query, credential-shaped args) are replaced
// by the target format's environment-variable reference and listed as
// variables the receiver must set — or the entry is refused outright, with
// an explicit warning, when the target cannot express that safely. What the
// export keeps verbatim is the routing: endpoint, transport, filters,
// timeouts and flags the target understands.

import type { MCPServerConfig } from "@/api/types";
import { MCP_ENV_NAME_RE } from "./mcpImport";

export type MCPExportTarget = "pogo" | "claude" | "cursor" | "vscode";

export const MCP_EXPORT_TARGETS: MCPExportTarget[] = ["pogo", "claude", "cursor", "vscode"];

/** A variable the receiver must define in the target environment. */
export interface MCPExportVar {
  name: string;
  /** The server entry that needs it. */
  server: string;
  /** i18n suffix: rendered as settingsDialog.mcpExpVarWhy_<why>. */
  why: "bearer" | "env" | "header" | "urlQuery" | "arg";
  /** The env key / header name / query key / arg flag the value came from. */
  detail?: string;
}

export interface MCPExportWarning {
  /** i18n suffix: settingsDialog.mcpExpWarn_<code>. */
  code:
    | "disabledOmitted"
    | "fieldsDropped"
    | "urlUserinfo"
    | "urlQueryPogo"
    | "argCredsPogo"
    | "varSyntax"
    | "badUrl"
    | "bearerEnvInvalid"
    | "authOverride";
  params?: Record<string, string>;
  /** True when the warning means the entry was left out of the JSON. */
  blocked?: boolean;
}

export interface MCPExportResult {
  /** Pretty-printed JSON; "" when nothing was exportable. */
  text: string;
  /** Names that made it into the JSON, in input order. */
  servers: string[];
  vars: MCPExportVar[];
  warnings: MCPExportWarning[];
}

// ── Variable references ─────────────────────────────────────────────────────

// varRef renders one variable name in the target's interpolation syntax:
// pogo expands $VAR (os.Expand) in env/header values; Claude Code ${VAR};
// Cursor and VS Code ${env:VAR}. Pogo has no {env:...} form, so the mapping
// is one direction per target and never mixed.
export function varRef(target: MCPExportTarget, name: string): string {
  switch (target) {
    case "pogo":
      return `$${name}`;
    case "claude":
      return `\${${name}}`;
    default:
      return `\${env:${name}}`;
  }
}

// scanTemplateRefs returns the variable names a pogo template references
// (os.Expand semantics: $VAR or ${VAR}, anywhere in the string), or null
// when a '$' appears in neither shape — the exporter refuses to guess at a
// syntax it cannot translate rather than silently emitting a value that
// would expand to nothing or stay literal.
export function scanTemplateRefs(value: string): string[] | null {
  const names: string[] = [];
  for (let i = 0; i < value.length; i++) {
    if (value[i] !== "$") continue;
    const rest = value.slice(i + 1);
    let m = /^\{([A-Za-z_][A-Za-z0-9_]*)\}/.exec(rest);
    if (m) {
      names.push(m[1]);
      i += m[0].length;
      continue;
    }
    m = /^([A-Za-z_][A-Za-z0-9_]*)/.exec(rest);
    if (m) {
      names.push(m[1]);
      i += m[0].length;
      continue;
    }
    return null;
  }
  return names;
}

// rewriteTemplate re-expresses a validated pogo template in the target's
// syntax (pogo normalizes ${VAR} to $VAR; both expand the same way).
function rewriteTemplate(value: string, target: MCPExportTarget): string {
  return value.replace(
    /\$(?:\{([A-Za-z_][A-Za-z0-9_]*)\}|([A-Za-z_][A-Za-z0-9_]*))/g,
    (_all, braced: string | undefined, plain: string | undefined) => varRef(target, braced ?? plain ?? ""),
  );
}

// deriveVarName builds a legal, collision-free variable name from a hint
// (server name, env key, header name). Suffixes _2, _3… on collisions.
function deriveVarName(hint: string, used: Set<string>): string {
  let base = hint
    .toUpperCase()
    .replace(/[^A-Z0-9_]/g, "_")
    .replace(/^_+|_+$/g, "")
    .replace(/_{2,}/g, "_");
  if (!/^[A-Z_]/.test(base)) base = `MCP_${base}`;
  if (!base) base = "MCP_VAR";
  let name = base;
  for (let i = 2; used.has(name); i++) name = `${base}_${i}`;
  used.add(name);
  return name;
}

// ── Credential shape detection ──────────────────────────────────────────────

// A header whose literal value is a credential: never exported verbatim.
const SENSITIVE_HEADER_RE = /^(authorization|proxy-authorization|x-api-key|cookie|set-cookie)$/i;
const SENSITIVE_HEADER_WORD_RE = /(token|secret|password|passwd|credential|api[-_]?key|auth[-_]?code)/i;

export function isSensitiveHeader(name: string): boolean {
  return SENSITIVE_HEADER_RE.test(name) || SENSITIVE_HEADER_WORD_RE.test(name);
}

const SENSITIVE_QUERY_RE = /^(token|api[-_]?key|apikey|key|secret|access[-_]?token|refresh[-_]?token|password|passwd|sig|signature|auth|credential|private[-_]?key|client[-_]?secret)$/i;

// The sensitive-looking flag name of an args entry ("--api-key", "-token"),
// split into flag and inline value when joined by '='. prefix keeps the
// dashes exactly as written ("-token" stays single-hyphen in the output).
const ARG_CRED_RE = /^(-{1,2})([A-Za-z][\w-]*?)(?:=(.*))?$/;
const ARG_CRED_NAME_RE = /(token|secret|password|passwd|api[-_]?key|apikey|credential|bearer|access[-_]?key|auth[-_]?token)$/i;

export function argCredFlag(arg: string): { flag: string; prefix: string; inline: string | undefined } | null {
  const m = ARG_CRED_RE.exec(arg);
  if (!m || !ARG_CRED_NAME_RE.test(m[2])) return null;
  return { flag: m[2], prefix: m[1] + m[2], inline: m[3] };
}

// An args *value* that carries a credential header verbatim, e.g.
// "--header", "Authorization: Bearer <secret>" — the flag name is innocent,
// so the shape of the value is what gives it away.
function embeddedArgCredential(arg: string): { value: string; prefix: string; detail: string } | null {
  const option = /^(-{1,2}[A-Za-z][\w-]*=)([\s\S]*)$/.exec(arg);
  const value = option ? option[2] : arg;
  const prefix = option?.[1] ?? "";
  const header = /^([A-Za-z0-9_-]+)\s*:/.exec(value);
  if (header && isSensitiveHeader(header[1])) return { value, prefix, detail: header[1] };
  if (/^https?:\/\//i.test(value)) {
    try {
      const url = new URL(value);
      if (url.username || url.password || [...url.searchParams.keys()].some((key) => SENSITIVE_QUERY_RE.test(key))) {
        return { value, prefix, detail: "URL" };
      }
    } catch { /* ordinary invalid argument; not interpreted as a URL */ }
  }
  const assignment = /^([A-Za-z_][A-Za-z0-9_]*)=([\s\S]*)$/.exec(arg);
  if (assignment && ARG_CRED_NAME_RE.test(assignment[1])) {
    return { value: assignment[2], prefix: `${assignment[1]}=`, detail: assignment[1] };
  }
  return null;
}

/** Safe display label: upstream URL credentials and query values stay out of the selection list. */
export function exportConnectionLabel(sc: MCPServerConfig): string {
  if (sc.command) return sc.command;
  try {
    const url = new URL(sc.url ?? "");
    return `${url.protocol}//${url.host}${url.pathname}`;
  } catch {
    return "HTTP";
  }
}

// ── The per-target conversion ───────────────────────────────────────────────

interface Ctx {
  target: MCPExportTarget;
  /** Committed variables (from entries that made it into the JSON). */
  vars: MCPExportVar[];
  warnings: MCPExportWarning[];
  /** Every name ever referenced or derived — even from a blocked entry —
      stays reserved, so a later derivation can never collide with a name
      that already means something else. */
  usedVars: Set<string>;
}

// addVar records into the entry-local list (transactional: a blocked entry's
// variables are dropped with it) and reserves the name globally.
function addVar(ctx: Ctx, local: MCPExportVar[], name: string, server: string, why: MCPExportVar["why"], detail?: string): void {
  ctx.usedVars.add(name);
  if (local.some((v) => v.name === name) || ctx.vars.some((v) => v.name === name)) return;
  local.push({ name, server, why, detail });
}

// reuseOrDerive prefers the natural name (an env key keeps its own name)
// when it is legal and still free, else derives a collision-free one.
function reuseOrDerive(hint: string, ctx: Ctx): string {
  if (MCP_ENV_NAME_RE.test(hint) && !ctx.usedVars.has(hint)) {
    ctx.usedVars.add(hint);
    return hint;
  }
  return deriveVarName(hint, ctx.usedVars);
}

const PURE_TEMPLATE_RE = /^(?:\$[A-Za-z_][A-Za-z0-9_]*|\$\{[A-Za-z_][A-Za-z0-9_]*\})$/;
const BEARER_TEMPLATE_RE = /^Bearer\s+(\$[A-Za-z_][A-Za-z0-9_]*|\$\{[A-Za-z_][A-Za-z0-9_]*\})$/i;

// mapValue converts one env/header-style value. Pure templates are
// re-expressed in the target's syntax with their names registered; literal
// values are either templatized (sensitive headers, and every env value) or
// kept verbatim (ordinary headers). A mixed template in a sensitive position
// — literal text spliced with a reference — is reduced to a single derived
// variable, because the literal fragment may be half a credential. Returns
// null when the value holds an unrecognized '$' shape: the caller blocks.
function mapValue(
  ctx: Ctx,
  local: MCPExportVar[],
  server: string,
  value: string,
  why: MCPExportVar["why"],
  templatizeLiteral: (() => string) | null,
  detail?: string,
): string | null {
  const refs = scanTemplateRefs(value);
  if (refs === null) return null;
  if (refs.length) {
    if (templatizeLiteral) {
      // "Bearer $TOKEN" keeps its scheme and reuses the token's name.
      const bearer = BEARER_TEMPLATE_RE.exec(value.trim());
      if (bearer) {
        const name = scanTemplateRefs(bearer[1])![0];
        addVar(ctx, local, name, server, why, detail);
        return `Bearer ${varRef(ctx.target, name)}`;
      }
      if (!PURE_TEMPLATE_RE.test(value.trim())) {
        const name = templatizeLiteral();
        addVar(ctx, local, name, server, why, detail);
        return varRef(ctx.target, name);
      }
    }
    for (const r of refs) addVar(ctx, local, r, server, why, detail);
    // pogo keeps its native template verbatim: normalizing ${VAR} to $VAR is
    // only safe at a word boundary, and a trailing identifier would silently
    // rename the variable that gets read ("$REGION_suffix" ≠ "${REGION}_suffix").
    return ctx.target === "pogo" ? value : rewriteTemplate(value, ctx.target);
  }
  if (templatizeLiteral) {
    const name = templatizeLiteral();
    addVar(ctx, local, name, server, why, detail);
    return varRef(ctx.target, name);
  }
  return value;
}

// mapBearer resolves the bearer pair into either pogo's bearer_token_env or a
// foreign Authorization header. The literal token is never emitted: it
// becomes a derived variable the receiver must set. Caller must have checked
// that no explicit Authorization header exists (that header wins at runtime,
// so it wins here too).
function mapBearer(
  ctx: Ctx,
  local: MCPExportVar[],
  name: string,
  sc: MCPServerConfig,
  headers: Record<string, string>,
): { bearer_env?: string } | null {
  const envName = sc.bearer_token_env?.trim();
  if (envName) {
    if (!MCP_ENV_NAME_RE.test(envName)) {
      ctx.warnings.push({ code: "bearerEnvInvalid", params: { name }, blocked: true });
      return null;
    }
    addVar(ctx, local, envName, name, "bearer");
    if (ctx.target === "pogo") return { bearer_env: envName };
    headers["Authorization"] = `Bearer ${varRef(ctx.target, envName)}`;
    return {};
  }
  if (sc.bearer_token !== undefined) {
    const varName = deriveVarName(`MCP_${name}_TOKEN`, ctx.usedVars);
    addVar(ctx, local, varName, name, "bearer");
    if (ctx.target === "pogo") return { bearer_env: varName };
    headers["Authorization"] = `Bearer ${varRef(ctx.target, varName)}`;
    return {};
  }
  return {};
}

// reserveReferences pre-scans every entry's existing variable names
// (bearer_token_env, $VAR templates in env/headers) into ctx.usedVars before
// any conversion runs, so a derived name can never collide with a reference
// another entry — processed earlier OR later — already relies on.
function reserveReferences(ctx: Ctx, servers: Record<string, MCPServerConfig>): void {
  for (const sc of Object.values(servers)) {
    const envName = sc.bearer_token_env?.trim();
    if (envName && MCP_ENV_NAME_RE.test(envName)) ctx.usedVars.add(envName);
    for (const group of [sc.env ?? {}, sc.headers ?? {}]) {
      for (const v of Object.values(group)) {
        for (const r of scanTemplateRefs(v) ?? []) ctx.usedVars.add(r);
      }
    }
    for (const arg of sc.args ?? []) {
      for (const name of scanTemplateRefs(arg) ?? []) ctx.usedVars.add(name);
    }
  }
}

// convertServer maps one saved config onto the target's entry shape, or
// returns null (with a blocked warning already pushed) when the entry cannot
// be expressed without leaking or silently breaking something. Variables are
// collected entry-locally and committed only on success.
function convertServer(ctx: Ctx, name: string, sc: MCPServerConfig): { entry: Record<string, unknown>; vars: MCPExportVar[] } | null {
  const t = ctx.target;
  const local: MCPExportVar[] = [];
  const isStdio = !!sc.command?.trim();

  // Disabled entries: only pogo's native format carries the flag. Foreign
  // formats would connect the entry immediately, so it is left out with an
  // explicit warning rather than silently enabled.
  if (sc.enabled === false && t !== "pogo") {
    ctx.warnings.push({ code: "disabledOmitted", params: { name }, blocked: true });
    return null;
  }

  // URL checks come first: userinfo blocks everywhere (there is no safe
  // variable form for it in any target), secret query keys templatize only
  // where the URL field expands variables.
  let url = sc.url?.trim();
  if (!isStdio) {
    if (!url || !/^https?:\/\//.test(url)) {
      ctx.warnings.push({ code: "badUrl", params: { name }, blocked: true });
      return null;
    }
    let u: URL;
    try {
      u = new URL(url);
    } catch {
      ctx.warnings.push({ code: "badUrl", params: { name }, blocked: true });
      return null;
    }
    if (u.username || u.password) {
      ctx.warnings.push({ code: "urlUserinfo", params: { name }, blocked: true });
      return null;
    }
    const secretKeys = [...u.searchParams.keys()].filter((k) => SENSITIVE_QUERY_RE.test(k));
    if (secretKeys.length) {
      if (t === "pogo") {
        // pogo expands env/header values only; a variable in the URL would
        // stay literal, so the entry is refused instead of half-exported.
        ctx.warnings.push({ code: "urlQueryPogo", params: { name, keys: secretKeys.join(", ") }, blocked: true });
        return null;
      }
      // Rewrite at the string level: URL.searchParams.set would percent-encode
      // the ${VAR} reference into something the target cannot expand.
      const [beforeHash, ...hashRest] = url.split("#");
      const qMark = beforeHash.indexOf("?");
      const base = qMark >= 0 ? beforeHash.slice(0, qMark) : beforeHash;
      const pairs = (qMark >= 0 ? beforeHash.slice(qMark + 1) : "").split("&").filter((p) => p !== "");
      const mapped = pairs.map((p) => {
        const eq = p.indexOf("=");
        const k = eq >= 0 ? p.slice(0, eq) : p;
        // Match on the decoded key ("%74oken" is still "token"); each
        // occurrence gets its own variable — two repeated keys may carry two
        // distinct secrets, and merging them would corrupt one.
        let decoded = k;
        try {
          decoded = decodeURIComponent(k);
        } catch {
          /* keep the raw key */
        }
        if (!SENSITIVE_QUERY_RE.test(decoded)) return p;
        const varName = deriveVarName(`MCP_${name}_${decoded}`, ctx.usedVars);
        addVar(ctx, local, varName, name, "urlQuery", decoded);
        return `${k}=${varRef(t, varName)}`;
      });
      url = `${base}?${mapped.join("&")}${hashRest.length ? `#${hashRest.join("#")}` : ""}`;
    }
  }

  // An explicit Authorization header overrides the bearer field at runtime
  // (pool.go stamps headers after the token), so it wins here as well: the
  // bearer pair is not exported, and the fact is reported.
  const headerEntries = Object.entries(sc.headers ?? {});
  const hasExplicitAuth = headerEntries.some(([k]) => k.toLowerCase() === "authorization");

  const headers: Record<string, string> = Object.create(null);
  let bearer: { bearer_env?: string } | null = {};
  if (hasExplicitAuth && (sc.bearer_token_env || sc.bearer_token !== undefined)) {
    ctx.warnings.push({ code: "authOverride", params: { name } });
  } else {
    bearer = mapBearer(ctx, local, name, sc, headers);
    if (bearer === null) return null;
  }
  for (const [hk, hv] of headerEntries) {
    const mapped = mapValue(ctx, local, name, hv, "header", isSensitiveHeader(hk) ? () => deriveVarName(`MCP_${name}_${hk}`, ctx.usedVars) : null, hk);
    if (mapped === null) {
      ctx.warnings.push({ code: "varSyntax", params: { name, field: `headers.${hk}` }, blocked: true });
      return null;
    }
    headers[hk] = mapped;
  }

  // env (stdio side): literal values become variable references — the file
  // format documents env as the secret channel, so plaintext stays out.
  let env: Record<string, string> | undefined;
  if (sc.env && Object.keys(sc.env).length) {
    env = Object.create(null) as Record<string, string>;
    for (const [ek, ev] of Object.entries(sc.env)) {
      const mapped = mapValue(ctx, local, name, ev, "env", () => reuseOrDerive(ek, ctx), ek);
      if (mapped === null) {
        ctx.warnings.push({ code: "varSyntax", params: { name, field: `env.${ek}` }, blocked: true });
        return null;
      }
      env[ek] = mapped;
    }
  }

  // args (stdio side): credential-shaped flags and credential-header values
  // templatize where args expand variables (Claude/Cursor/VS Code); pogo
  // never expands args, so there the entry is refused instead of shipping a
  // literal credential. An existing template value keeps its variable name.
  let args = sc.args;
  if (isStdio && args?.length) {
    const out: string[] = [];
    for (let i = 0; i < args.length; i++) {
      const a = args[i];
      const cred = argCredFlag(a);
      const embedded = cred ? null : embeddedArgCredential(a);
      if (!cred && !embedded) {
        out.push(a);
        continue;
      }
      // Where the credential value sits: inline (=value), the next arg, or —
      // for a credential-header-shaped value — inside this arg itself.
      const inline = cred?.inline;
      const ownValue = embedded ? embedded.value : inline;
      const value = ownValue !== undefined ? ownValue : i + 1 < args.length ? args[i + 1] : undefined;
      if (value === undefined) {
        out.push(a); // a bare flag carries no credential
        continue;
      }
      const detail = cred ? cred.prefix : embedded!.detail;
      const hint = `MCP_${name}_${detail}`;
      const refs = scanTemplateRefs(value);
      if (refs === null) {
        ctx.warnings.push({ code: "varSyntax", params: { name, field: `args[${detail}]` }, blocked: true });
        return null;
      }
      if (t === "pogo") {
        ctx.warnings.push({ code: "argCredsPogo", params: { name, flags: detail }, blocked: true });
        return null;
      }
      const pure = refs.length === 1 && PURE_TEMPLATE_RE.test(value.trim());
      const varName = pure ? refs[0] : deriveVarName(hint, ctx.usedVars);
      addVar(ctx, local, varName, name, "arg", detail);
      if (embedded || !cred) out.push(`${embedded?.prefix ?? ""}${varRef(t, varName)}`);
      else if (inline !== undefined) out.push(`${cred.prefix}=${varRef(t, varName)}`);
      else {
        out.push(a, varRef(t, varName));
        i++; // the value arg is consumed
      }
    }
    args = out;
  }

  // Field-level fidelity: pogo keeps the native shape verbatim (only the
  // credential channels above are rewritten); foreign targets take endpoint
  // + transport + auth and report what they cannot carry.
  let entry: Record<string, unknown>;
  if (t === "pogo") {
    const e: Record<string, unknown> = Object.create(null);
    if (sc.enabled === false) e.enabled = false;
    if (isStdio) {
      e.command = sc.command;
      if (args?.length) e.args = args;
      if (env) e.env = env;
      if (sc.cwd?.trim()) e.cwd = sc.cwd;
    } else {
      e.url = url;
      if (sc.transport === "sse") e.transport = "sse";
      if (Object.keys(headers).length) e.headers = headers;
      if (bearer?.bearer_env) e.bearer_token_env = bearer.bearer_env;
    }
    if (sc.startup_timeout_ms !== undefined) e.startup_timeout_ms = sc.startup_timeout_ms;
    if (sc.tool_timeout_ms !== undefined) e.tool_timeout_ms = sc.tool_timeout_ms;
    if (sc.include_tools?.length) e.include_tools = sc.include_tools;
    if (sc.exclude_tools?.length) e.exclude_tools = sc.exclude_tools;
    if (sc.trust) e.trust = true;
    if (sc.required) e.required = true;
    entry = e;
  } else {
    const dropped: string[] = [];
    if (sc.include_tools?.length) dropped.push("include_tools");
    if (sc.exclude_tools?.length) dropped.push("exclude_tools");
    if (sc.startup_timeout_ms !== undefined) dropped.push("startup_timeout_ms");
    if (sc.tool_timeout_ms !== undefined) dropped.push("tool_timeout_ms");
    if (sc.trust) dropped.push("trust");
    if (sc.required) dropped.push("required");
    if (dropped.length) ctx.warnings.push({ code: "fieldsDropped", params: { name, fields: dropped.join(", ") } });

    if (isStdio) {
      const e: Record<string, unknown> = Object.create(null);
      if (t !== "cursor") e.type = "stdio";
      e.command = sc.command;
      if (args?.length) e.args = args;
      if (env) e.env = env;
      if (sc.cwd?.trim()) ctx.warnings.push({ code: "fieldsDropped", params: { name, fields: "cwd" } });
      entry = e;
    } else {
      const e: Record<string, unknown> = Object.create(null);
      const sse = sc.transport === "sse";
      if (t === "claude" || t === "vscode") e.type = sse ? "sse" : "http";
      else if (sse) e.type = "sse"; // cursor: streamable is the url default
      e.url = url;
      if (Object.keys(headers).length) e.headers = headers;
      entry = e;
    }
  }
  return { entry, vars: local };
}

// exportMCPServers is the dialog's single entry point: the saved servers map
// (already filtered to the user's selection) plus the target, in, and the
// validated preview JSON plus its variable list and warnings, out. Copy and
// download both serialize from this text, so they can never diverge.
export function exportMCPServers(servers: Record<string, MCPServerConfig>, target: MCPExportTarget): MCPExportResult {
  const ctx: Ctx = { target, vars: [], warnings: [], usedVars: new Set() };
  reserveReferences(ctx, servers);
  // Null prototype: server names are user data, and "__proto__" must survive
  // as an own key through JSON round-trips.
  const out: Record<string, unknown> = Object.create(null);
  const included: string[] = [];
  for (const [name, sc] of Object.entries(servers)) {
    const r = convertServer(ctx, name, sc);
    if (r === null) continue;
    Object.defineProperty(out, name, { value: r.entry, enumerable: true, writable: true, configurable: true });
    included.push(name);
    for (const v of r.vars) if (!ctx.vars.some((x) => x.name === v.name)) ctx.vars.push(v);
  }
  const root: Record<string, unknown> = Object.create(null);
  root[target === "pogo" || target === "vscode" ? "servers" : "mcpServers"] = out;
  return {
    text: included.length ? JSON.stringify(root, null, 2) : "",
    servers: included,
    vars: ctx.vars,
    warnings: ctx.warnings,
  };
}

// ── This-machine-as-server export ───────────────────────────────────────────

// suggestServeURL composes the default connection URL for the serving half:
// the current origin, any reverse-proxy path prefix the UI itself is served
// under, and the first endpoint path the backend reports (/mcp preferred).
// The dialog still lets the user replace it with any reachable http(s) URL.
export function suggestServeURL(endpoints: string[] | undefined, loc?: { origin: string; pathname: string }): string {
  const ep = endpoints?.find((e) => e.startsWith("/")) ?? "/mcp";
  const l = loc ?? (typeof window !== "undefined" ? window.location : { origin: "http://127.0.0.1:7778", pathname: "/" });
  return `${l.origin.replace(/\/+$/, "")}${servePrefix(l.pathname)}${ep}`;
}

// The pathname names one of the app's own pages (/manage/mcp, /a/x, /share/y,
// /embed) in the common root deployment; those routes never belong in a
// connection URL. A route segment is stripped together with everything after
// it — only a path ahead of a known route is a reverse-proxy prefix (/pogo in
// https://host/pogo/manage/mcp). A bare unknown path (proxy root, /pogo/) is
// kept whole as before.
function servePrefix(pathname: string): string {
  const path = pathname.replace(/\/+$/, "");
  const route = /^(.*?)\/(?:manage|a|share|embed)(?:\/|$)/.exec(path);
  return (route?.[1] ?? path).replace(/\/+$/, "");
}

export interface ServeExportInput {
  /** Connection name for the target config (validated by the dialog). */
  name: string;
  /** Full http(s) URL the receiver can reach. */
  url: string;
  /** From server_state.token_required: when true the receiver must supply a
      Bearer token — exported only as a variable *name*, never a value. */
  tokenRequired: boolean;
  /** The variable name the receiver should set (default POGO_MCP_TOKEN). */
  tokenVar: string;
}

// buildServeExportEntry reduces the serving half to one ordinary remote
// client entry, so the same per-target conversion (and its credential rules)
// applies. Only the connection definition is exported — tool schemas are for
// the receiver to discover, and mcp.json's "server" section stays behind.
export function buildServeExportEntry(i: ServeExportInput): Record<string, MCPServerConfig> {
  const sc: MCPServerConfig = { url: i.url.trim() };
  if (i.tokenRequired && i.tokenVar.trim()) sc.bearer_token_env = i.tokenVar.trim();
  return { [i.name.trim()]: sc };
}
