import type { AgentFormState, StarterFormEntry } from './studioPresentation';
import { normalizeStarter, padStarters, starterToWire } from './studioPresentation';
import type { StarterEntry, StarterGenerationInput } from './platformTypes';

/** A partly filled manual card counts as entered too; do not lose its metadata. */
export function occupiedStarter(entry: StarterFormEntry): boolean {
  return [entry.title, entry.label, entry.icon, entry.prompt].some(value => value.trim().length > 0);
}

export function starterGenerationInput(form: AgentFormState, count: number, agentId?: string | null): StarterGenerationInput {
  return {
    ...(agentId ? { agent_id: agentId } : {}),
    name: form.name.trim(), description: form.description.trim(), instructions: form.instructions.trim(),
    model: form.model, models: [...form.models], skills: [...form.skills],
    mcp_servers: [...form.mcpServers], tools: [...form.tools], count,
    ...(form.skillToolBindings === undefined ? {} : { skill_tool_bindings: [...form.skillToolBindings] }),
    existing_starters: form.starters.filter(occupiedStarter).map(starterToWire),
  };
}

/** Append generated cards after every manually entered card, replacing only empty placeholders. */
export function appendGeneratedStarters(current: readonly StarterFormEntry[], generated: readonly StarterEntry[]) {
  const existing = current.filter(occupiedStarter).map(entry => ({ ...entry }));
  const seen = new Set(existing.map(entry => entry.prompt.trim()).filter(Boolean));
  const added: StarterFormEntry[] = [];
  for (const raw of generated) {
    const entry = normalizeStarter(raw);
    const prompt = entry.prompt.trim();
    if (!prompt || seen.has(prompt)) continue;
    seen.add(prompt);
    added.push({ ...entry, prompt });
  }
  return { starters: padStarters([...existing, ...added]), added: added.length };
}
