import type { InputLayout, MessageInputPart } from "@/api/types";

/** Agent Studio's product contract. Runtime credentials never enter this shape. */
export interface PlatformAgent {
  id: string;
  name: string;
  description: string;
  instructions: string;
  /** Default model; the conversation can switch among `models`. */
  model: string;
  /** Selected models (default first when the server sends an order). */
  models?: string[];
  /** Explicit Skill IDs; omitted means a legacy Agent inherits all. */
  skills?: string[];
  /** Explicit MCP server IDs (config keys); omitted means inherit all. */
  mcp_servers?: string[];
  /** Explicit native tool names; omitted means the full built-in set.
   *  The protected core (read/ls/find/grep/todo/subagent) stays regardless. */
  tools?: string[];
  /** Trusted embedded Skill groups whose native tools follow the Skill choice. */
  skill_tool_bindings?: string[];
  color: string;
  icon: string;
  category: string;
  starters: StarterEntry[];
  status: "ready" | "draft";
  revision: number;
  created_at: string;
  updated_at: string;
  conversation_count: number;
}

export interface PlatformAgentInput {
  name: string;
  description: string;
  instructions: string;
  model: string;
  models?: string[];
  skills?: string[];
  mcp_servers?: string[];
  tools?: string[];
  skill_tool_bindings?: string[];
  color: string;
  icon: string;
  category: string;
  starters: StarterEntry[];
  status: "ready" | "draft";
}

export interface PlatformModel {
  id: string;
  provider: string;
  available: boolean;
}

/** Generate opening cards from an unsaved editor draft. This never saves an Agent. */
export interface StarterGenerationInput {
  /** Existing Agent identity when previewing an edited definition. */
  agent_id?: string;
  name: string;
  description: string;
  instructions: string;
  model: string;
  models: string[];
  skills: string[];
  mcp_servers: string[];
  tools: string[];
  skill_tool_bindings?: string[];
  count: number;
  existing_starters?: StarterEntry[];
}

export interface StarterGenerationResult {
  starters: StarterEntry[];
  model: string;
}

/** One admin-visible Skill. `id` is the opaque Skill ID, not the file path. */
/** One opening question, in either wire shape: a plain string (the original
 *  contract) or a card object — title, corner label, full prompt and an icon
 *  name — rendered as a chat-homepage card by StarterCards. */
export type StarterEntry = string | {
  title?: string;
  label?: string;
  prompt: string;
  icon?: string;
  parts?: MessageInputPart[];
  input_layout?: InputLayout;
};

export interface PlatformSkill {
  id: string;
  name: string;
  description: string;
  hidden: boolean;
  tool_count: number;
  /** browser-task skill (browser-use): invoking it arms the browser gate.
   *  Drives the "增强内置" badge — it grants no tools of its own, it wakes
   *  the built-in browser one. */
  browser_task?: boolean;
  /** Embedded source identity. Whether its checkbox is required is separate. */
  builtin?: boolean;
  /** Required system guide. False makes an embedded capability selectable. */
  builtin_required?: boolean;
  /** Host-issued native grants controlled by this embedded Skill. */
  native_tools?: string[];
  /** Retained old grants; selecting this Skill replaces, never grants, these names. */
  legacy_native_tools?: string[];
  source?: string;
  package?: PackageRef;
  package_name?: string;
  skill_id?: string;
  removable?: boolean;
  runtime_count?: number;
  asset_count?: number;
  package_size_bytes?: number;
  installed_at?: string;
  state?: 'available' | 'needs_configuration' | 'revoked' | 'unavailable';
  issues?: SkillIssue[];
  required_capabilities?: SkillContractRef[];
}

/** One admin-visible MCP server. `id` is the mcp.json config key. */
export interface PlatformMcpServer {
  id: string;
  name: string;
  transport: string;
  enabled: boolean;
  status: string;
  tool_count: number;
}

/** GET /api/manage/capabilities — catalog metadata only, never credentials
    or server config bodies. */
export interface PlatformCapabilities {
  skills: PlatformSkill[];
  mcp_servers: PlatformMcpServer[];
  /** The native tool catalog (from /api/tools' builtin section): the Agent
   *  editor's built-in column. Optional only because older servers lack it. */
  builtin_tools?: BuiltinToolInfo[];
  runtime_tools?: RuntimeTool[];
  skill_execution_configured?: boolean;
}

/** One native built-in tool, as /api/tools' builtin section serves it. */
export interface BuiltinToolInfo {
  name: string;
  description: string;
  read_only?: boolean;
  protected?: boolean;
  /** Canonical trusted embedded Skill name controlling this native tool. */
  skill_binding?: string;
}

export interface PackageRef { id: string; version: string; manifestDigest: string; packageDigest: string }
export interface SkillContractRef { id: string; version: string; digest: string }
export type SkillIssue = import('@/api/skillTypes').SkillExecutionIssue;
export interface SkillResource { packageId: string; slotId: string; resourceRef: string }
export interface SkillSelection { skill_ids: string[] }
export interface RuntimeTool { id: string; package: PackageRef; package_name: string; provider_id: string; contract: SkillContractRef; description: string; revoked: boolean; resource_slots: { id: string; kind: string; description: string }[]; asset_ids?: string[] }
export interface SkillSelectionPlan {
  execution?: import('@/api/skillTypes').SkillExecutionStatus;
  selection: SkillSelection;
  ready: boolean;
  issues: SkillIssue[];
  tools: RuntimeTool[];
  resources: (SkillResource & { kind: string; description: string })[];
}

export interface SkillLibraryCatalog {
  skills: PlatformSkill[];
  tools: RuntimeTool[];
  execution: import('@/api/skillTypes').SkillExecutionStatus | null;
  max_upload_bytes: number;
}

export interface AgentTemplate {
  id: string;
  name: string;
  description: string;
  instructions: string;
  icon: string;
  color: string;
  category: string;
  starters: StarterEntry[];
}
