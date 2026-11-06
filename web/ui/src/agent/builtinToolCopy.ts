import { gt } from "@/i18n";

// The host catalog uses Chinese descriptions. Localize only known native
// capabilities, never tool prose supplied by an MCP server or a user Skill.
const NATIVE_TOOL_NAMES = new Set([
  "read", "ls", "find", "grep", "tool_result", "shortcut_query", "shortcut_manage", "shortcut_delete", "shortcut_merge",
  "skill_search", "skill_read", "skill_save", "agent_workflow_read", "agent_workflow_edit", "agent_workflow_enable", "agent_workflow_run",
  "background_task", "write", "edit", "bash", "browser", "todo", "subagent", "analyze_image", "shell_tasks", "shell_output", "shell_stop",
  "watch", "watch_test", "watch_decide",
]);

/** Call from a component render/computed so the current locale is tracked. */
export function builtinToolDescription(name: string, fallback: string): string {
  return NATIVE_TOOL_NAMES.has(name) ? gt(`settingsCoverage.builtinDescriptions.${name}`) : fallback;
}

const BUILTIN_SKILL_NAMES = new Set(["office", "browser-use", "background-tasks", "skill-creator", "agent-workflows", "web-drawing"]);

/** Display summaries only: installed/user Skill instructions stay verbatim. */
export function builtinSkillDescription(name: string, fallback: string, builtin: boolean): string {
  return builtin && BUILTIN_SKILL_NAMES.has(name) ? gt(`settingsCoverage.builtinSkillDescriptions.${name}`) : fallback;
}
