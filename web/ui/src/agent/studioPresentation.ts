import { gt as tr } from "@/i18n";
import type {
  AgentTemplate,
  PlatformAgent,
  PlatformAgentInput,
  PlatformCapabilities,
  PlatformModel,
  StarterEntry,
} from "./platformTypes";
import type { StudioSection } from "./studioRoutes";
import type { InputLayout, MessageInputPart } from "@/api/types";
import { inheritedNativeTools, requiredBuiltinSkill } from './skillToolBindings';

export type { StudioSection } from "./studioRoutes";

// Agent Studio presentation rules, pure and DOM-free so the whole surface is
// testable without mounting a component. Anything here that needs a runtime
// value (the origin for a published link) takes it as an argument rather than
// reading window, so the tests pin the exact string the dialog will show.

/** The SettingsDialog's own grouping: Studio modules map onto its sheets. */
export type StudioSettingsSection = "models" | "mcp" | "account" | "general" | "memory";

export interface StudioNavItem {
  section: StudioSection;
  /** Canonical management location; navigation goes through navigateStudio. */
  path: string;
  label: string;
}

/** Top-level Studio modules, in sidebar order. Templates are not a module —
    they are choices inside the creation dialog. */
export const STUDIO_NAV: readonly StudioNavItem[] = [
  { section: "agents", path: "/", get label() { return tr('studioManagement.text618'); } },
  { section: "information", path: "/manage/information", get label() { return tr('studioManagement.text600'); } },
  { section: "models", path: "/manage/models", get label() { return tr('studioManagement.text620'); } },
  { section: "mcp", path: "/manage/mcp", get label() { return tr('studioManagement.text621'); } },
  { section: "skills", path: "/manage/skills", get label() { return tr('studioManagement.text601'); } },
  { section: "memory", path: "/manage/memory", get label() { return tr('memoryPanel.preferencesTitle'); } },
  { section: "account", path: "/manage/account", get label() { return tr('studioManagement.text622'); } },
  { section: "settings", path: "/manage/settings", get label() { return tr('studioManagement.text623'); } },
];

/** Modules that only exist as account surfaces. A deployment without the
 *  users.json account system (token-only, e.g. a test instance) has no
 *  accounts to manage and no per-account preferences to edit, so both stay
 *  out of the sidebar — the rest of the Studio is unaffected. */
const ACCOUNT_SECTIONS: readonly StudioSection[] = ["account", "settings"];

/** The sidebar's modules for one deployment: everything when the account
 *  system is on, everything except the account surfaces when it is not. */
export function visibleStudioNav(passwordEnabled: boolean): readonly StudioNavItem[] {
  if (passwordEnabled) return STUDIO_NAV;
  return STUDIO_NAV.filter((n) => !ACCOUNT_SECTIONS.includes(n.section));
}

/** Route-level fallback: opening /manage/account directly on a token-only
 *  deployment lands on the Agents page rather than an orphaned module. */
export function visibleSection(section: StudioSection, passwordEnabled: boolean): StudioSection {
  if (passwordEnabled || !ACCOUNT_SECTIONS.includes(section)) return section;
  return "agents";
}

export const STUDIO_SECTION_LABELS: Record<StudioSection, string> = {
  get agents() { return tr('studioManagement.text618'); },
  get information() { return tr('studioManagement.text600'); },
  get models() { return tr('studioManagement.text620'); },
  get mcp() { return tr('studioManagement.text621'); },
  get skills() { return tr('studioManagement.text601'); },
  get memory() { return tr('memoryPanel.preferencesTitle'); },
  get account() { return tr('studioManagement.text622'); },
  get settings() { return tr('studioManagement.text623'); },
};

/** One honest line under each module title: what this page actually edits. */
export const STUDIO_SECTION_DESCS: Record<StudioSection, string> = {
  get agents() { return tr('studioManagement.text624'); },
  get information() { return tr('studioManagement.text625'); },
  get models() { return tr('studioManagement.text627'); },
  get mcp() { return tr('studioManagement.text628'); },
  get skills() { return tr('studioManagement.text629'); },
  get memory() { return tr('memoryPanel.description'); },
  get account() { return tr('studioManagement.text630'); },
  get settings() { return tr('studioManagement.text631'); },
};

/** Studio module → the SettingsDialog section that renders it. "settings" is
    the dialog's general sheet; the other modules share their names. */
export function settingsSectionFor(section: StudioSection): StudioSettingsSection {
  switch (section) {
    case "models":
      return "models";
    case "mcp":
      return "mcp";
    case "account":
      return "account";
    case "memory":
      return "memory";
    // Skills render their own management pages, never the
    // SettingsDialog; "general" is only the total-function fallback.
    default:
      return "general";
  }
}

/** The Studio route for a module — sidebar clicks and the embedded settings'
    cross-module links both answer with these. */
export function studioPathForSection(section: StudioSection): string {
  return STUDIO_NAV.find((n) => n.section === section)?.path ?? "/";
}

/** The dialog asks to switch modules by its own section name; map it back to
    the Studio module that owns that section. */
export function sectionForSettingsSection(section: StudioSettingsSection): StudioSection {
  switch (section) {
    case "models":
      return "models";
    case "mcp":
      return "mcp";
    case "account":
      return "account";
    case "memory":
      return "memory";
    default:
      return "settings";
  }
}

/* ---------------------------------- links ---------------------------------- */

/** The canonical published page. Visitors open it and sign in with the
    existing workspace authorization; no credential ever rides in the URL. */
export function publishedAgentUrl(origin: string, agentId: string): string {
  return `${origin}/a/${encodeURIComponent(agentId)}`;
}

/** The embeddable variant of the published page: chrome reduced for iframes. */
export function embedAgentUrl(origin: string, agentId: string): string {
  return `${publishedAgentUrl(origin, agentId)}?embed=1`;
}

/** The copyable embed block. Sized to a sensible default the host page can
    restyle; the title is fixed text so no Agent value needs HTML escaping. */
export function embedSnippet(origin: string, agentId: string): string {
  return `<iframe src="${embedAgentUrl(origin, agentId)}" width="100%" height="640" style="border:0;border-radius:12px" title="Pogo Agent" allow="clipboard-write" referrerpolicy="no-referrer"></iframe>`;
}

/** The admin's own conversation preview: inside the Studio, with a way back. */
export function managedChatPath(agentId: string): string {
  return `/manage/agents/${encodeURIComponent(agentId)}/chat`;
}

/* ---------------------------------- catalog ---------------------------------- */

export const CATEGORY_ORDER = ["general", "writing", "research", "code", "planning"] as const;

const CATEGORY_LABELS: Record<string, string> = {
  get general() { return tr('studioManagement.text632'); },
  get writing() { return tr('studioManagement.text358'); },
  get research() { return tr('studioManagement.text429'); },
  get code() { return tr('studioManagement.text322'); },
  get planning() { return tr('studioManagement.text633'); },
};

export function categoryLabel(category: string): string {
  return CATEGORY_LABELS[category] ?? category;
}

/** Category filter chips: only categories actually present, canonical order
    first, unknown ones alphabetically after. */
export function categoryChips(agents: readonly PlatformAgent[]): { value: string; label: string }[] {
  const present = new Set(agents.map((a) => a.category));
  const ordered = CATEGORY_ORDER.filter((c) => present.has(c));
  const extra = [...present].filter((c) => !(CATEGORY_ORDER as readonly string[]).includes(c)).sort();
  return [{ value: "all", label: tr('studioManagement.text213') }, ...[...ordered, ...extra].map((c) => ({ value: c, label: categoryLabel(c) }))];
}

export type AgentStatusFilter = "all" | "ready" | "draft";

export function filterAgents(
  agents: readonly PlatformAgent[],
  query: string,
  status: AgentStatusFilter,
  category = "all",
): PlatformAgent[] {
  const q = query.trim().toLowerCase();
  return agents.filter((a) => {
    if (category !== "all" && a.category !== category) return false;
    if (status !== "all" && a.status !== status) return false;
    if (q && !`${a.name} ${a.description} ${a.model}`.toLowerCase().includes(q)) return false;
    return true;
  });
}

/** The counts the agents page header reports. */
export function agentStats(agents: readonly PlatformAgent[], models: readonly PlatformModel[]) {
  const ready = agents.filter((a) => a.status === "ready").length;
  return {
    total: agents.length,
    ready,
    draft: agents.length - ready,
    availableModels: models.filter((m) => m.available).length,
  };
}

/** Today's cards name the day and clock time; every other day gets a full date. */
export function formatAgentTime(iso: string, now: Date = new Date(), todayLabel: string = tr('studioSentences.today')): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  if (d.toDateString() === now.toDateString()) {
    return `${todayLabel} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
  }
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

export function modelLabel(models: readonly PlatformModel[], id: string): string {
  if (!id) return tr('studioManagement.text634');
  const m = models.find((x) => x.id === id);
  if (!m) return id;
  return m.available ? `${m.provider} · ${m.id}` : tr('studioManagement.text635', { value0: m.provider, value1: m.id });
}

/** The preselected model for a new Agent: k3 first, glm-5.3 next, then the
    first available one. (Reasoning effort is backend policy, not a form field.) */
export function defaultModelId(models: readonly PlatformModel[]): string {
  const available = models.filter((m) => m.available);
  return (
    available.find((m) => /^(k3|kimi-k3)$/i.test(m.id))?.id ??
    available.find((m) => /^glm-5\.3$/i.test(m.id))?.id ??
    available[0]?.id ??
    ""
  );
}

/* ---------------------------- capability selection ---------------------------- */

/** The editor's whole capability picture: catalogs plus current selection.
    Pure data — the dialog mutates through the helpers below so every
    transition (legacy inheritance, stale ids, default swaps) is testable. */
export interface CapabilitySelection {
  /** Selected model ids, in catalog order. */
  models: string[];
  /** Which selected model is the default. Always one of `models`. */
  defaultModel: string;
  /** Selected Skill ids (opaque). */
  skills: string[];
  /** Selected MCP server ids (mcp.json keys). */
  mcpServers: string[];
  /** Selected native tool names; protected ones are always members. */
  tools: string[];
  skillToolBindings?: string[];
  /** True while an untouched legacy Agent still inherits every Skill. */
  skillsInherited: boolean;
  /** True while an untouched legacy Agent still inherits every MCP server. */
  mcpInherited: boolean;
  /** True while an untouched legacy Agent still inherits the full tool set. */
  toolsInherited: boolean;
}

const dedupe = (ids: readonly string[]): string[] => [...new Set(ids)];

/** The locked system-skill checks must also be present in a new draft's payload. */
export function initialCreateSkills(catalog: Pick<PlatformCapabilities, 'skills'> | null, preset: readonly string[] = []): string[] {
  return dedupe([...(catalog?.skills ?? []).filter(requiredBuiltinSkill).map(skill => skill.id), ...preset]);
}

/** Resolve the editor's opening selection from a saved Agent.
 *
 * - omitted `models` → legacy single-model list `[model]`;
 * - omitted `skills` / `mcp_servers` / `tools` → the agent inherits ALL
 *   catalog resources, so the editor opens with the full legacy selection
 *   checked (never silently dropped by opening the editor);
 * - explicit arrays (including []) are honored as-is, even stale ids —
 *   those stay visible in the dialog as unavailable retained selections.
 *
 * Tools get one extra rule: the protected core (the server's `protected`
 * flag) is unioned into every explicit selection, because the registry keeps
 * those tools whatever the saved array says — the editor shows reality, not
 * the raw bytes.
 */
export function initialCapabilities(
  agent: Pick<PlatformAgent, "model" | "models" | "skills" | "mcp_servers" | "tools" | "skill_tool_bindings">,
  catalog: Pick<PlatformCapabilities, "skills" | "mcp_servers" | "builtin_tools">,
): CapabilitySelection {
  const models = dedupe(agent.models ?? (agent.model ? [agent.model] : []));
  const defaultModel = models.includes(agent.model) ? agent.model : models[0] ?? "";
  // Absent arrays mean the legacy "inherit everything" definition; explicit
  // arrays (including empty) are the pinned selection, honored as-is.
  const skillsInherited = agent.skills == null;
  const mcpInherited = agent.mcp_servers == null;
  const toolsInherited = agent.tools == null;
  const builtinNames = inheritedNativeTools(catalog);
  const protectedNames = (catalog.builtin_tools ?? []).filter((t) => t.protected).map((t) => t.name);
  return {
    models,
    defaultModel,
    skills: skillsInherited ? catalog.skills.map((s) => s.id) : dedupe(agent.skills ?? []),
    mcpServers: mcpInherited ? catalog.mcp_servers.filter((m) => m.enabled).map((m) => m.id) : dedupe(agent.mcp_servers ?? []),
    tools: toolsInherited ? builtinNames : dedupe([...protectedNames, ...(agent.tools ?? [])]),
    ...(agent.skill_tool_bindings === undefined ? {} : { skillToolBindings: dedupe(agent.skill_tool_bindings) }),
    skillsInherited,
    mcpInherited,
    toolsInherited,
  };
}

/** Check/uncheck one model. Removing the default hands the role to another
 *  selected model; checking into an empty list makes it the default. */
export function toggleModelSelection(sel: CapabilitySelection, id: string): CapabilitySelection {
  const has = sel.models.includes(id);
  const models = has ? sel.models.filter((m) => m !== id) : [...sel.models, id];
  let defaultModel = sel.defaultModel;
  if (!has && !defaultModel) defaultModel = id;
  if (has && !models.length) defaultModel = "";
  if (has && models.length && !models.includes(defaultModel)) defaultModel = models[0] ?? "";
  return { ...sel, models, defaultModel };
}

/** Pick the default among already-selected models; a no-op otherwise. */
export function setDefaultModel(sel: CapabilitySelection, id: string): CapabilitySelection {
  return sel.models.includes(id) ? { ...sel, defaultModel: id } : sel;
}

/** Toggle one id in a plain selection list (Skills / MCP servers). */
export function toggleId(ids: readonly string[], id: string): string[] {
  return ids.includes(id) ? ids.filter((x) => x !== id) : dedupe([...ids, id]);
}

/** Selected ids the catalog no longer knows — retained, but flagged. */
export function unavailableSelected(selected: readonly string[], catalogIds: readonly string[]): string[] {
  const known = new Set(catalogIds);
  return selected.filter((id) => !known.has(id));
}

/** One honest chip for the Agent card: counts, or the legacy inheritance. */
export function agentCapabilitySummary(
  agent: Pick<PlatformAgent, "model" | "models" | "skills" | "mcp_servers">,
): string {
  const models = agent.models ?? (agent.model ? [agent.model] : []);
  const parts: string[] = [];
  parts.push(models.length === 1 ? tr('studioManagement.text636') : tr('studioManagement.text637', { value0: models.length }));
  if (agent.skills === undefined && agent.mcp_servers === undefined) {
    parts.push(tr('studioManagement.text638'));
  } else {
    if (agent.skills !== undefined) parts.push(`${agent.skills.length} Skill`);
    if (agent.mcp_servers !== undefined) parts.push(`${agent.mcp_servers.length} MCP`);
  }
  return parts.join(" · ");
}

/** The card's model chip: the default model, plus a count when more follow. */
export function agentModelsLabel(
  models: readonly PlatformModel[],
  agent: Pick<PlatformAgent, "model" | "models">,
): string {
  const selected = agent.models ?? (agent.model ? [agent.model] : []);
  const label = modelLabel(models, agent.model);
  return selected.length > 1 ? tr('studioManagement.text639', { value0: label, value1: selected.length }) : label;
}

/* ---------------------------------- form ---------------------------------- */

export interface AgentFormState {
  name: string;
  description: string;
  instructions: string;
  /** Default model id; always a member of `models` once validated. */
  model: string;
  /** Selected model ids (multi-select, one default). */
  models: string[];
  /** Selected Skill ids; the editor always sends an explicit array. */
  skills: string[];
  /** Selected MCP server ids; the editor always sends an explicit array. */
  mcpServers: string[];
  /** Selected native tool names; the editor always sends an explicit array,
   *  protected core included. */
  tools: string[];
  /** Undefined retains old independent grants until a Skill group is deliberately changed. */
  skillToolBindings?: string[];
  /** Whether the skills selection still mirrors full legacy inheritance. */
  skillsInherited: boolean;
  /** Whether the MCP selection still mirrors full legacy inheritance. */
  mcpInherited: boolean;
  /** Whether the tool selection still mirrors the full legacy default. */
  toolsInherited: boolean;
  color: string;
  icon: string;
  category: string;
  starters: StarterFormEntry[];
}

/** The editable shape of one opening question. Card fields are optional: an
 *  entry with only a prompt saves back as a plain string, exactly the legacy
 *  wire shape, so simple Agents never grow object noise. */
export interface StarterFormEntry {
  title: string;
  label: string;
  icon: string;
  prompt: string;
  parts?: MessageInputPart[];
  input_layout?: InputLayout;
}

export const AGENT_FORM_LIMITS = { name: 60, description: 240, instructions: 12000, starter: 200, starterTitle: 60, starterLabel: 40, starterGenerationBatch: 8 } as const;

/** Normalize any wire entry (string or card) into the editable shape. */
export function normalizeStarter(entry: StarterEntry): StarterFormEntry {
  if (typeof entry === "string") return { title: "", label: "", icon: "", prompt: entry };
  return { title: entry.title ?? "", label: entry.label ?? "", icon: entry.icon ?? "", prompt: entry.prompt,
    ...(entry.parts ? { parts: entry.parts.map((part) => ({ ...part })) } : {}),
    ...(entry.input_layout ? { input_layout: entry.input_layout } : {}),
  };
}

/** The wire shape for one entry: compact string when it carries no card
 *  fields, the card object when it does. */
export function starterToWire(entry: StarterFormEntry): StarterEntry {
  const prompt = entry.prompt.trim();
  const title = entry.title.trim();
  const label = entry.label.trim();
  const icon = entry.icon.trim();
  if (!title && !label && !icon && !entry.parts && !entry.input_layout) return prompt;
  return { title, label, icon, prompt,
    ...(entry.parts ? { parts: entry.parts.map((part) => ({ ...part })) } : {}),
    ...(entry.input_layout ? { input_layout: entry.input_layout } : {}),
  };
}

/** Validation errors keyed by field; empty means submittable. */
export function validateAgentForm(form: AgentFormState): Record<string, string> {
  const errors: Record<string, string> = {};
  const name = form.name.trim();
  if (!name) errors.name = tr('studioManagement.text640');
  else if (name.length > AGENT_FORM_LIMITS.name) errors.name = tr('studioManagement.text641', { value0: AGENT_FORM_LIMITS.name });
  if (form.description.trim().length > AGENT_FORM_LIMITS.description)
    errors.description = tr('studioManagement.text642', { value0: AGENT_FORM_LIMITS.description });
  if (form.instructions.trim().length > AGENT_FORM_LIMITS.instructions)
    errors.instructions = tr('studioManagement.text643', { value0: AGENT_FORM_LIMITS.instructions });
  if (!form.models.length || !form.model) errors.model = tr('studioManagement.text644');
  else if (!form.models.includes(form.model)) errors.model = tr('studioManagement.text645');
  return errors;
}

/** Apply a creation template to a draft: prefills identity and starters but
 *  never touches the capability choices already made — switching templates is
 *  a what-it-looks-like decision, not a what-it-can-use reset. */
export function withTemplate(form: AgentFormState, tpl: AgentTemplate | null): AgentFormState {
  const base: AgentFormState = {
    ...form,
    name: "",
    description: "",
    instructions: "",
    color: "#5965e8",
    icon: "spark",
    category: "general",
    starters: emptyStarters(),
  };
  if (!tpl) return base;
  return {
    ...base,
    name: tpl.name,
    description: tpl.description,
    instructions: tpl.instructions,
    icon: tpl.icon,
    color: tpl.color,
    category: tpl.category,
    starters: padStarters(tpl.starters.map(normalizeStarter)),
  };
}

export function emptyStarters(): StarterFormEntry[] {
  return Array.from({ length: 3 }, () => ({ title: "", label: "", icon: "", prompt: "" }));
}

/** At least three rows in the editor; the list itself has no count cap. */
export function padStarters(list: StarterFormEntry[]): StarterFormEntry[] {
  const starters = [...list];
  while (starters.length < 3) starters.push({ title: "", label: "", icon: "", prompt: "" });
  return starters;
}

/** The form as the platform API expects it: trimmed, starters compacted, and
 *  always an explicit capability list — the server never has to guess whether
 *  an edit meant "inherit all" or "really none". */
export function buildAgentInput(form: AgentFormState, status: "ready" | "draft"): PlatformAgentInput {
  return {
    name: form.name.trim(),
    description: form.description.trim(),
    instructions: form.instructions.trim(),
    model: form.model,
    models: dedupe(form.models),
    skills: dedupe(form.skills),
    mcp_servers: dedupe(form.mcpServers),
    tools: dedupe(form.tools),
    ...(form.skillToolBindings === undefined ? {} : { skill_tool_bindings: dedupe(form.skillToolBindings) }),
    color: form.color,
    icon: form.icon,
    category: form.category,
    starters: form.starters
      .filter((s) => s.prompt.trim() || s.parts?.length)
      .map(starterToWire),
    status,
  };
}

/* ---------------------------- native tools ---------------------------- */

/** A non-blocking hint about selected capabilities, not a runtime availability
 *  or approval check. Optional actions, including background-task management,
 *  lift the hint; protected coordination tools alone do not. Read-and-discuss
 *  configurations remain valid and saving stays allowed. */
export function toolsetWarning(
  form: Pick<AgentFormState, "tools" | "skills" | "mcpServers">,
  catalog: Pick<PlatformCapabilities, "skills" | "builtin_tools">,
): string {
  const canAct = form.tools.some((name) => ["write", "edit", "bash", "browser", "shortcut_manage", "shortcut_create", "shortcut_update", "shortcut_folder_create", "shortcut_folder_rename", "shortcut_move", "shortcut_delete", "shortcut_merge", "skill_save", "watch", "background_task", "watch_test", "agent_workflow_edit", "agent_workflow_enable", "agent_workflow_run"].includes(name));
  if (canAct) return "";
  const selected = new Set(form.skills);
  const declaredSkill = catalog.skills.some((s) => selected.has(s.id) && (s.tool_count > 0 || (s.runtime_count ?? 0) > 0));
  if (declaredSkill || form.mcpServers.length > 0) return "";
  return tr('studioManagement.text646');
}
