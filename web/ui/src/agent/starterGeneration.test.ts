import { describe, expect, it } from 'vitest';
import { appendGeneratedStarters, occupiedStarter, starterGenerationInput } from './starterGeneration';
import { buildAgentInput, emptyStarters, initialCreateSkills, type AgentFormState } from './studioPresentation';

function form(): AgentFormState {
  return {
    name: '研究助手', description: '分析资料', instructions: '', model: 'default-model', models: ['default-model', 'other-model'],
    skills: ['browser-use'], mcpServers: ['research'], tools: ['read', 'browser'],
    skillsInherited: false, mcpInherited: false, toolsInherited: false,
    color: '#5965e8', icon: 'spark', category: 'research', starters: emptyStarters(),
  };
}

describe('opening card generation draft handling', () => {
  it('carries the locked system skills into both generation and saving when the catalog was already loaded', () => {
    const draft = form();
    const catalog = { skills: [
      { id: 'browser-use', builtin: true, name: 'Browser', description: '', hidden: false, tool_count: 0 },
      { id: 'docs', builtin: true, name: 'Docs', description: '', hidden: false, tool_count: 0 },
      { id: 'optional', name: 'Optional', description: '', hidden: false, tool_count: 0 },
    ] };
    draft.skills = initialCreateSkills(catalog, ['browser-use', 'preset']);
    expect(starterGenerationInput(draft, 3).skills).toEqual(['browser-use', 'docs', 'preset']);
    expect(buildAgentInput(draft, 'draft').skills).toEqual(['browser-use', 'docs', 'preset']);
    expect(initialCreateSkills(catalog)).toEqual(['browser-use', 'docs']);
    expect(initialCreateSkills(null, ['preset'])).toEqual(['preset']);
  });

  it('uses the selected default and explicit capability choices without requiring a saved Agent', () => {
    const draft = form();
    const input = starterGenerationInput(draft, 3);
    expect(input).toMatchObject({ model: 'default-model', models: ['default-model', 'other-model'], skills: ['browser-use'], mcp_servers: ['research'], tools: ['read', 'browser'], count: 3 });
    expect(input.existing_starters).toEqual([]);
    draft.models.push('late-model');
    expect(input.models).toEqual(['default-model', 'other-model']);
  });

  it('preserves partially filled manual cards when appending generated entries', () => {
    const draft = form();
    draft.starters[0] = { title: '我的标题', label: '', icon: 'book', prompt: '' };
    expect(occupiedStarter(draft.starters[0]!)).toBe(true);
    const input = starterGenerationInput(draft, 2);
    expect(input.existing_starters).toEqual([{ title: '我的标题', label: '', icon: 'book', prompt: '' }]);
    const result = appendGeneratedStarters(draft.starters, [{ title: '梳理资料', prompt: '请整理这些资料', icon: 'file' }, '对比两个方案']);
    expect(result.added).toBe(2);
    expect(result.starters[0]).toEqual(draft.starters[0]);
    expect(result.starters.map(entry => entry.prompt)).toEqual(['', '请整理这些资料', '对比两个方案']);
    expect(draft.starters[1]?.prompt).toBe('');
  });

  it('ignores duplicate prompts and appends without a total cap', () => {
    const current = Array.from({ length: 7 }, (_, i) => ({ title: '', label: '', icon: '', prompt: `问题 ${i}` }));
    const result = appendGeneratedStarters(current, [' 问题 0 ', '新问题', '另一个问题']);
    expect(result.added).toBe(2);
    expect(result.starters).toHaveLength(9);
    expect(result.starters[result.starters.length - 1]?.prompt).toBe('另一个问题');
  });
});
