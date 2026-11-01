import type { BuiltinToolInfo, PlatformCapabilities, PlatformSkill } from './platformTypes';

type Catalog = Pick<PlatformCapabilities, 'skills' | 'builtin_tools'>;
export interface SkillToolSelection {
  skills: string[];
  tools: string[];
  skillToolBindings?: string[];
}

/** Only host-projected embedded metadata may establish a native grant group. */
export function boundNativeSkills(catalog: Catalog | null): PlatformSkill[] {
  return (catalog?.skills ?? []).filter(skill => skill.builtin === true && skill.source === 'embedded' && !!skill.native_tools?.length);
}

export function requiredBuiltinSkill(skill: PlatformSkill): boolean {
  return skill.builtin_required ?? skill.builtin ?? false;
}

export function nativeToolSkill(name: string, catalog: Catalog | null): string | undefined {
  const native = catalog?.builtin_tools?.find(tool => tool.name === name);
  if (native?.skill_binding) return native.skill_binding;
  return boundNativeSkills(catalog).find(skill => skill.native_tools?.includes(name) || skill.legacy_native_tools?.includes(name))?.name;
}

export function defaultNativeTools(catalog: Catalog | null, selectedSkills: readonly string[]): string[] {
  const selected = new Set(selectedSkills);
  const selectedGroups = new Set(boundNativeSkills(catalog).filter(skill => selected.has(skill.id)).map(skill => skill.name));
  return (catalog?.builtin_tools ?? []).filter(tool => {
    const owner = nativeToolSkill(tool.name, catalog);
    return !owner || selectedGroups.has(owner);
  }).map(tool => tool.name);
}

/** Old nil tool selections included compatibility verbs absent from today's picker. */
export function inheritedNativeTools(catalog: Catalog | null): string[] {
  return [...new Set([
    ...(catalog?.builtin_tools ?? []).map(tool => tool.name),
    ...boundNativeSkills(catalog).flatMap(skill => skill.legacy_native_tools ?? []),
  ])];
}

/** Reconcile managed groups only. Opening an old definition must not widen its grants. */
export function linkedNativeTools(selection: SkillToolSelection, catalog: Catalog | null): string[] {
  const result = new Set(selection.tools);
  const managed = new Set(selection.skillToolBindings ?? []);
  for (const skill of boundNativeSkills(catalog)) {
    if (!managed.has(skill.name)) continue;
    for (const name of [...(skill.native_tools ?? []), ...(skill.legacy_native_tools ?? [])]) result.delete(name);
    if (selection.skills.includes(skill.id)) for (const name of skill.native_tools ?? []) result.add(name);
  }
  return [...result];
}

export function boundSkillState(skill: PlatformSkill, selection: SkillToolSelection) {
  const names = skill.native_tools ?? [];
  const selected = selection.skills.includes(skill.id);
  const managed = selection.skillToolBindings?.includes(skill.name) === true;
  const granted = names.filter(name => selection.tools.includes(name));
  const legacy = (skill.legacy_native_tools ?? []).filter(name => selection.tools.includes(name));
  const complete = selected && names.length > 0 && granted.length === names.length && legacy.length === 0;
  return { selected, managed, granted, legacy, complete, mixed: !managed && (selected || granted.length > 0 || legacy.length > 0) && !complete };
}

/** A deliberate mixed-state click adopts the full group; an enabled click disables it. */
export function toggleSkillToolBinding(selection: SkillToolSelection, skill: PlatformSkill): SkillToolSelection {
  const state = boundSkillState(skill, selection);
  const enabled = state.mixed || !state.selected;
  return setSkillToolBinding(selection, skill, enabled);
}

export function setSkillToolBinding(selection: SkillToolSelection, skill: PlatformSkill, enabled: boolean): SkillToolSelection {
  const skills = selection.skills.filter(id => id !== skill.id);
  if (enabled) skills.push(skill.id);
  const group = new Set([...(skill.native_tools ?? []), ...(skill.legacy_native_tools ?? [])]);
  const tools = selection.tools.filter(name => !group.has(name));
  if (enabled) tools.push(...(skill.native_tools ?? []));
  return {
    skills: [...new Set(skills)], tools: [...new Set(tools)],
    skillToolBindings: [...new Set([...(selection.skillToolBindings ?? []), skill.name])],
  };
}

/** Ordinary tools retain their catalogue policy, including protected infrastructure. */
export function canToggleNativeTool(tool: BuiltinToolInfo, catalog: Catalog | null): boolean {
  return !tool.protected && !nativeToolSkill(tool.name, catalog);
}
