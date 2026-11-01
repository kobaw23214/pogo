import { describe, expect, it } from 'vitest';
import type { PlatformCapabilities, PlatformSkill } from './platformTypes';
import { boundNativeSkills, boundSkillState, canToggleNativeTool, defaultNativeTools, linkedNativeTools, setSkillToolBinding, toggleSkillToolBinding } from './skillToolBindings';
import { buildAgentInput, emptyStarters, initialCapabilities, initialCreateSkills, type AgentFormState } from './studioPresentation';
import { starterGenerationInput } from './starterGeneration';

const workflow: PlatformSkill = { id: 'embedded-flow', name: 'agent-workflows', source: 'embedded', builtin: true, builtin_required: false, description: '流程', hidden: false, tool_count: 0, native_tools: ['agent_workflow_read', 'agent_workflow_edit', 'agent_workflow_run', 'agent_workflow_enable'] };
const background: PlatformSkill = { id: 'embedded-background', name: 'background-tasks', source: 'embedded', builtin: true, builtin_required: false, description: '后台', hidden: false, tool_count: 0, native_tools: ['background_task'], legacy_native_tools: ['watch', 'watch_test'] };
const office: PlatformSkill = { id: 'embedded-office', name: 'office', source: 'embedded', builtin: true, builtin_required: true, description: '文档', hidden: false, tool_count: 1 };
const catalog: PlatformCapabilities = { skills: [workflow, background, office], mcp_servers: [], builtin_tools: [
  { name: 'read', description: '读取', protected: true }, { name: 'write', description: '写入' }, { name: 'shortcut_delete', description: '删除' },
  ...workflow.native_tools!.map(name => ({ name, description: '内部流程', skill_binding: workflow.name })),
  { name: 'background_task', description: '后台任务', skill_binding: background.name },
] };

describe('trusted Skill and native tool selection', () => {
  it('new drafts select required guides but never grant unselected linked capabilities', () => {
    const skills = initialCreateSkills(catalog);
    expect(skills).toEqual([office.id]);
    expect(defaultNativeTools(catalog, skills)).toEqual(['read', 'write', 'shortcut_delete']);
    expect(defaultNativeTools(catalog, [...skills, workflow.id])).toEqual(['read', 'write', 'shortcut_delete', ...workflow.native_tools!]);
  });

  it('does not let a custom same-name Skill establish native bindings', () => {
    const custom = { ...workflow, id: 'custom-flow', source: 'user', builtin: false };
    expect(boundNativeSkills({ ...catalog, skills: [custom] })).toEqual([]);
    expect(defaultNativeTools({ ...catalog, skills: [custom] }, [custom.id])).not.toContain('agent_workflow_run');
  });

  it('preserves a saved partial grant when opening and reconciling an unrelated edit', () => {
    const existing = { model: 'model', skills: [workflow.id], tools: ['read', 'agent_workflow_read'], mcp_servers: [] };
    const opened = initialCapabilities(existing, catalog);
    expect(opened.skillToolBindings).toBeUndefined();
    expect(opened.tools).toEqual(existing.tools);
    expect(linkedNativeTools(opened, catalog)).toEqual(existing.tools);
    expect(boundSkillState(workflow, opened)).toMatchObject({ mixed: true, managed: false, granted: ['agent_workflow_read'] });
    expect(existing.tools).toEqual(['read', 'agent_workflow_read']);
  });

  it('requires a deliberate mixed-state click to adopt the complete group and a second click to revoke it', () => {
    const old = { skills: [workflow.id], tools: ['read', 'write', 'agent_workflow_read'] };
    const enabled = toggleSkillToolBinding(old, workflow);
    expect(enabled.skillToolBindings).toEqual([workflow.name]);
    expect(enabled.tools).toEqual(['read', 'write', ...workflow.native_tools!]);
    const disabled = toggleSkillToolBinding(enabled, workflow);
    expect(disabled.skills).toEqual([]);
    expect(disabled.tools).toEqual(['read', 'write']);
    expect(disabled.skillToolBindings).toEqual([workflow.name]);
    expect(old.tools).toEqual(['read', 'write', 'agent_workflow_read']);
  });

  it('keeps legacy watch-only permission until explicit adoption and never treats it as full background permission', () => {
    const old = { skills: [background.id], tools: ['read', 'watch_test', 'shortcut_delete'] };
    expect(linkedNativeTools(old, catalog)).toEqual(old.tools);
    expect(boundSkillState(background, old)).toMatchObject({ mixed: true, granted: [], legacy: ['watch_test'] });
    expect(toggleSkillToolBinding(old, background).tools).toEqual(['read', 'shortcut_delete', 'background_task']);
    expect(setSkillToolBinding(old, background, false).tools).toEqual(['read', 'shortcut_delete']);
  });

  it('materializes hidden compatibility grants for old nil inheritance before ordinary edits', () => {
    const opened = initialCapabilities({ model: 'model' }, catalog);
    expect(opened.toolsInherited).toBe(true);
    expect(opened.tools).toContain('background_task');
    expect(opened.tools).toContain('watch');
    expect(opened.tools).toContain('watch_test');
    const withoutWrite = { ...opened, tools: opened.tools.filter(name => name !== 'write'), toolsInherited: false };
    expect(linkedNativeTools(withoutWrite, catalog)).toEqual(withoutWrite.tools);
    expect(withoutWrite.tools).toEqual(expect.arrayContaining(['watch', 'watch_test', 'background_task']));
    const adopted = setSkillToolBinding(withoutWrite, background, true);
    expect(adopted.tools).toContain('background_task');
    expect(adopted.tools).not.toContain('watch');
    expect(adopted.tools).not.toContain('watch_test');
  });

  it('normalizes only managed groups while preserving other independent and legacy rights', () => {
    const selection = { skills: [workflow.id], tools: ['read', 'write', 'background_task'], skillToolBindings: [workflow.name] };
    expect(linkedNativeTools(selection, catalog)).toEqual(['read', 'write', 'background_task', ...workflow.native_tools!]);
    expect(linkedNativeTools({ ...selection, skills: [], skillToolBindings: [workflow.name, background.name] }, catalog)).toEqual(['read', 'write']);
  });

  it('locks linked and core tools but leaves general tools independently configurable', () => {
    expect(canToggleNativeTool(catalog.builtin_tools![0]!, catalog)).toBe(false);
    expect(canToggleNativeTool(catalog.builtin_tools![1]!, catalog)).toBe(true);
    expect(canToggleNativeTool(catalog.builtin_tools![3]!, catalog)).toBe(false);
  });

  it('preserves absence of old policy markers and carries explicit adoption into save and generation', () => {
    const form: AgentFormState = { name: '报告', description: '', instructions: '', model: 'model', models: ['model'], skills: [workflow.id], tools: ['read', 'agent_workflow_read'], mcpServers: [], skillsInherited: false, toolsInherited: false, mcpInherited: false, color: '', icon: '', category: '', starters: emptyStarters() };
    expect(buildAgentInput(form, 'ready')).not.toHaveProperty('skill_tool_bindings');
    expect(starterGenerationInput(form, 2, 'ag_old')).toMatchObject({ agent_id: 'ag_old' });
    const explicit = { ...form, ...toggleSkillToolBinding(form, workflow) };
    expect(buildAgentInput(explicit, 'ready').skill_tool_bindings).toEqual([workflow.name]);
    expect(starterGenerationInput(explicit, 2, 'ag_old').skill_tool_bindings).toEqual([workflow.name]);
  });
});
