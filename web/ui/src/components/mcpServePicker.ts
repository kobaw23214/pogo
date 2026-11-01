// The serve sheet's grouped check tree (the UI lives in SettingsDialog.vue):
// the pure projections between include_tools patterns and the picker's check
// state, Vue-free so the semantics are testable — see mcpServePicker.test.ts.
// The catalog is GET /api/mcp's serve_catalog (api/types.ts's MCPServeCatalog).
//
// The semantics that matter, pinned here so both directions agree:
//   - an empty include means "expose every builtin + skill tool" server-side
//     (never forwards, never runtime packages), so it checks every
//     builtin/skill row and no forward/runtime row;
//   - a group whose wildcard-covered subset is fully checked collapses to
//     its wildcard (agent_*, mal.*, filesystem.*, skill_malware_*), the rest
//     to exact names — note agent_* covers only the agent_* rows: with a
//     runtime-enabled catalog the builtin group also carries the
//     skill_upload_file / skill_read_artifact helpers, which serialize as
//     exact names next to agent_*;
//   - an all-unchecked tree must NOT serialize to [] — that reads as "expose
//     everything" server-side — so it emits the sentinel "_" instead: a
//     legal pattern (MCP_TOOL_PATTERN_RE) that matches no real tool name.

import type { MCPServeCatalog } from "@/api/types";

export type ServePickerKind = "builtin" | "skill" | "forward" | "runtime";

export interface ServePickerTool {
  /** The exposed name the allowlist matches against. */
  name: string;
  title: string;
  /** Forward/runtime tools only; builtin/skill rows fall back to the name. */
  description: string;
  /** Consulted for forward (write badge) and runtime (read-only/write
      badges) tools. */
  readOnly: boolean;
  /** Runtime tools only: the original contract id (malware.sample.search). */
  contract?: string;
  /** Runtime tools only: needs files pre-uploaded via skill_upload_file. */
  fileInputs?: boolean;
  /** Runtime tools only: artifacts are fetched back via skill_read_artifact. */
  writesArtifacts?: boolean;
}

export interface ServePickerGroup {
  /** "builtin" / "skill:<prefix>" / "server:<name>" / "runtime:<rs_id>". */
  id: string;
  kind: ServePickerKind;
  /** Emitted when the subset of rows the wildcard covers is fully checked
      (anything it does not cover goes as exact names); null when the group
      has no prefix to wildcard over (a dotless skill, say). */
  wildcard: string | null;
  /** Forward groups: the parent server; runtime groups: the package. */
  serverName?: string;
  /** Forward groups: the pool state ("connected"); runtime groups: the
      package state ("available"). Anything else greys the group out. */
  serverState?: string;
  /** Runtime groups only: the package identity for the group head. */
  packageName?: string;
  version?: string;
  tools: ServePickerTool[];
}

// servePickerModel normalizes the catalog into the tree's groups, in display
// order: the builtin family, then skill families (grouped by the first dot
// segment — mal.search and mal.fetch share mal), then one group per server,
// then one group per runtime skill package. Unregistered skills are dropped:
// they are not real tools. A missing catalog or missing sections are fine —
// the model is simply smaller.
export function servePickerModel(catalog?: MCPServeCatalog | null): ServePickerGroup[] {
  const groups: ServePickerGroup[] = [];
  if (!catalog) return groups;
  if (catalog.builtin?.length) {
    groups.push({
      id: "builtin",
      kind: "builtin",
      // The contract: the builtin family is the agent_* tools — plus, on a
      // runtime-enabled catalog, the skill_upload_file / skill_read_artifact
      // helpers (which agent_* does NOT cover; serveIncludeFromChecks splits
      // them out as exact names).
      wildcard: "agent_*",
      tools: catalog.builtin.map((t) => ({ name: t.name, title: t.title, description: "", readOnly: true })),
    });
  }
  const skillGroups = new Map<string, ServePickerTool[]>();
  for (const s of catalog.skills ?? []) {
    if (!s.registered) continue;
    const dot = s.name.indexOf(".");
    const prefix = dot > 0 ? s.name.slice(0, dot) : s.name;
    const tools = skillGroups.get(prefix) ?? [];
    tools.push({ name: s.name, title: s.title, description: "", readOnly: true });
    skillGroups.set(prefix, tools);
  }
  for (const [prefix, tools] of skillGroups) {
    groups.push({
      id: `skill:${prefix}`,
      kind: "skill",
      // A family wildcards only when every member lives under the prefix —
      // a dotless sibling (solo next to solo.x) would escape "solo.*".
      wildcard: tools.every((t) => t.name.startsWith(`${prefix}.`)) ? `${prefix}.*` : null,
      tools,
    });
  }
  for (const srv of catalog.servers ?? []) {
    groups.push({
      id: `server:${srv.name}`,
      kind: "forward",
      wildcard: `${srv.name}.*`,
      serverName: srv.name,
      serverState: srv.state,
      tools: (srv.tools ?? []).map((t) => ({
        name: t.name,
        title: t.title,
        description: t.description ?? "",
        readOnly: !!t.read_only,
      })),
    });
  }
  for (const rt of catalog.runtime ?? []) {
    const tools: ServePickerTool[] = (rt.tools ?? []).map((t) => ({
      name: t.name,
      title: t.title ?? "",
      description: t.description ?? "",
      readOnly: !!t.read_only,
      contract: t.contract,
      fileInputs: !!t.file_inputs,
      writesArtifacts: !!t.writes_artifacts,
    }));
    groups.push({
      id: `runtime:${rt.id}`,
      kind: "runtime",
      wildcard: runtimeWildcard(tools.map((t) => t.name)),
      serverState: rt.state,
      packageName: rt.package_name,
      version: rt.version,
      tools,
    });
  }
  return groups;
}

// runtimeWildcard wildcards a package group over its tools' longest common
// prefix, but only when that prefix ends on a "_" word boundary
// (skill_malware_*): a mid-word LCP (skill_mal*) would also match other
// packages' tools, silently exposing rows the operator never checked.
function runtimeWildcard(names: string[]): string | null {
  if (!names.length) return null;
  let lcp = names[0];
  for (const n of names.slice(1)) {
    while (!n.startsWith(lcp)) lcp = lcp.slice(0, -1);
  }
  return lcp.endsWith("_") ? `${lcp}*` : null;
}

// toolPatternMatches applies one include_tools entry to an exposed name: an
// exact name, or a single trailing-* prefix (the only glob the server
// accepts).
export function toolPatternMatches(pattern: string, name: string): boolean {
  if (pattern.endsWith("*")) return name.startsWith(pattern.slice(0, -1));
  return pattern === name;
}

// serveChecksFromInclude derives the tree's check state from include_tools:
// a row is checked when an exact name or a covering wildcard hits it.
// Patterns that hit nothing the catalog lists (a removed server, say) simply
// check nothing — the picker can only speak for what it shows.
export function serveChecksFromInclude(include: string[], catalog?: MCPServeCatalog | null): Set<string> {
  const out = new Set<string>();
  // Empty include is the server's "expose everything": everything that
  // default covers (builtin + skills) reads as checked; forwards and
  // runtime packages are opt-in and only ever join by an explicit hit.
  const matchAll = include.length === 0;
  for (const g of servePickerModel(catalog)) {
    for (const tool of g.tools) {
      if (matchAll ? g.kind === "builtin" || g.kind === "skill" : include.some((p) => toolPatternMatches(p, tool.name))) {
        out.add(tool.name);
      }
    }
  }
  return out;
}

// serveIncludeFromChecks renders the check state back to include_tools:
// a group whose wildcard-covered subset is fully checked becomes its
// wildcard plus exact names for whatever the wildcard does not cover, a
// partial one its exact names, in the model's display order (builtin,
// skills, servers, runtime packages). Checked names the catalog no longer
// lists are dropped with it.
export function serveIncludeFromChecks(checked: Set<string>, catalog?: MCPServeCatalog | null): string[] {
  const out: string[] = [];
  for (const g of servePickerModel(catalog)) {
    const picked = g.tools.filter((t) => checked.has(t.name)).map((t) => t.name);
    if (!picked.length) continue;
    if (g.wildcard) {
      // The wildcard speaks only for the names it actually covers: with a
      // runtime-enabled catalog the builtin group also carries the
      // skill_upload_file / skill_read_artifact helpers, and agent_* must
      // not impersonate them. Emit the wildcard when its covered subset is
      // fully checked (a partial subset would silently expose the unchecked
      // remainder); uncovered picks go as exact names.
      const covers = (n: string) => toolPatternMatches(g.wildcard as string, n);
      const coverable = g.tools.filter((t) => covers(t.name)).length;
      if (coverable > 0 && picked.filter(covers).length === coverable) {
        out.push(g.wildcard, ...picked.filter((n) => !covers(n)));
      } else {
        out.push(...picked);
      }
      continue;
    }
    out.push(...picked);
  }
  // [] would read as "expose everything" server-side, so an all-unchecked
  // tree emits "_" instead — a legal pattern (MCP_TOOL_PATTERN_RE) matching
  // no real tool name, keeping the allowlist explicit: expose nothing.
  return out.length ? out : ["_"];
}
