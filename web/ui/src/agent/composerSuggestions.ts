export interface ComposerSuggestion {
  id: string;
  kind: "command" | "skill-entry" | "skill" | "mcp-entry" | "mcp";
  label: string;
  description: string;
  value: string;
  origin?: "builtin" | "external";
  shortcut?: string;
  mcp?: { server: string; name: string };
}

export interface ComposerMcpMethod { server: string; name: string; description: string }

interface SuggestionCommand { name: string; label: string; description: string }
interface SuggestionSkill { name: string; aliases?: string[]; description: string; source: string }
interface SkillEntry { label: string; description: string }

/** Suggestions only fill the composer. Execution remains an explicit send. */
export function composerSuggestions(line: string, commands: SuggestionCommand[], skills: SuggestionSkill[], entry: SkillEntry, mcps: readonly ComposerMcpMethod[] = [], mcpEntry?: SkillEntry): ComposerSuggestion[] {
  if (!/^\/[^\s]*$/.test(line)) return [];
  const query = line.toLowerCase();
  if (query.startsWith("/mcp:")) return mcpSuggestions(line.slice(5), mcps);
  const skillSyntax = query.startsWith("/skill:") || query === "/skill";
  const prefix = skillSyntax ? query.slice(query.startsWith("/skill:") ? 7 : 6) : query.slice(1);
  const seen = new Set<string>();
  const matchingSkills: ComposerSuggestion[] = skills.filter(skill => {
    if (seen.has(skill.name) || ![skill.name, ...(skill.aliases ?? [])].some(name => name.toLowerCase().startsWith(prefix))) return false;
    seen.add(skill.name);
    return true;
  }).map(skill => ({ id: `skill-${encodeURIComponent(skill.name)}`, kind: "skill", label: skill.name, description: skill.description,
    value: `/skill:${skill.name} `, origin: skill.source === "embedded" ? "builtin" : "external" }));
  if (skillSyntax) return matchingSkills;
  const result: ComposerSuggestion[] = commands.filter(command => command.name.toLowerCase().startsWith(query)).map(command => ({
    id: `command-${command.name.slice(1)}`, kind: "command", label: command.label, description: command.description,
    value: `${command.name} `, shortcut: command.name,
  }));
  if ("/skill:".startsWith(query)) result.push({ id: "skill-entry", kind: "skill-entry", label: entry.label, description: entry.description, value: "/skill:", shortcut: "/skill:" });
  const mcpEntries: ComposerSuggestion[] = mcpEntry && "/mcp:".startsWith(query)
    ? [{ id: "mcp-entry", kind: "mcp-entry", label: mcpEntry.label, description: mcpEntry.description, value: "/mcp:", shortcut: "/mcp:" }] : [];
  return [...result, ...(query === "/mcp" ? [] : matchingSkills), ...mcpEntries];
}

function mcpSuggestions(query: string, methods: readonly ComposerMcpMethod[]): ComposerSuggestion[] {
  // Decode only the search text. A partially typed escape remains searchable,
  // while the selected reference always retains the catalog's exact identity.
  const lower = (value: string) => {
    try { return decodeURIComponent(value).toLowerCase(); }
    catch { return value.toLowerCase(); }
  };
  const slash = query.indexOf("/");
  const search = lower(query);
  const serverSearch = slash < 0 ? "" : lower(query.slice(0, slash));
  const methodSearch = slash < 0 ? "" : lower(query.slice(slash + 1));
  const seen = new Set<string>();
  return methods.flatMap((method): ComposerSuggestion[] => {
    if (!method.server || !method.name) return [];
    const identity = JSON.stringify([method.server, method.name]);
    if (seen.has(identity)) return [];
    seen.add(identity);
    const encoded = `${encodeURIComponent(method.server)}/${encodeURIComponent(method.name)}`;
    const matches = slash < 0
      ? [method.server, method.name, method.description, encoded].some(value => value.toLowerCase().includes(search))
      : method.server.toLowerCase().includes(serverSearch) && method.name.toLowerCase().includes(methodSearch);
    if (!matches) return [];
    return [{ id: `mcp-${encoded}`, kind: "mcp", label: `${method.server}/${method.name}`, description: method.description,
      value: `/mcp:${encoded} `, mcp: { server: method.server, name: method.name } }];
  });
}

export function moveComposerSuggestion(items: ComposerSuggestion[], currentId: string | null, direction: 1 | -1): string | null {
  if (!items.length) return null;
  const index = items.findIndex(item => item.id === currentId);
  const start = index < 0 ? (direction === 1 ? -1 : 0) : index;
  return items[(start + direction + items.length) % items.length].id;
}

interface SuggestionKey { key: string; isComposing?: boolean; keyCode?: number; repeat?: boolean; shiftKey?: boolean; altKey?: boolean; ctrlKey?: boolean; metaKey?: boolean }
export function composerSuggestionAction(event: SuggestionKey, menuOpen: boolean): "next" | "previous" | "complete" | "dismiss" | "ignore" | null {
  if (event.isComposing || event.keyCode === 229) return null;
  if (event.key === "Enter" && event.repeat && !event.shiftKey) return "ignore";
  if (!menuOpen || event.shiftKey || event.altKey || event.ctrlKey || event.metaKey) return null;
  if (event.key === "ArrowDown") return "next";
  if (event.key === "ArrowUp") return "previous";
  if (event.key === "Tab" || event.key === "Enter") return "complete";
  if (event.key === "Escape") return "dismiss";
  return null;
}
