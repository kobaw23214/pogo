import { setLocale } from "@/i18n";
import { beforeEach, describe, expect, it } from 'vitest';
import { skillChoices, skillIsReady } from './skillSelection';
import type { PlatformCapabilities } from './platformTypes';

const id = 'rs_' + 'a'.repeat(64);
const runtimeSkill = { id: 'local.test', version: '1.0.0', manifestDigest: 'm', packageDigest: 'p' };
const contract = { id: 'file.scan', version: '1.0.0', digest: 'c' };
const catalog: PlatformCapabilities = {
  skills: [{ id: 'ordinary', name: 'Ordinary', description: '', hidden: false, tool_count: 0 }, { id, name: 'Scan', description: '', hidden: false, tool_count: 1, source: 'bundle', package: runtimeSkill, state: 'needs_configuration', required_capabilities: [contract] }],
  mcp_servers: [], runtime_tools: [{ id: 'tool', provider_id: 'scan', package: runtimeSkill, package_name: 'Test', contract, description: '', revoked: false, resource_slots: [{ id: 'index', kind: 'files', description: 'Index' }] }],
};
describe('Skill choices', () => {
  it('counts ready and legacy knowledge skills without counting setup or unavailable states', () => {
    const states = [undefined, 'available', 'needs_configuration', 'unavailable', 'revoked', 'future_state'];
    expect(states.filter(state => skillIsReady({ state }))).toEqual([undefined, 'available']);
  });
  it('hides disabled Skills from choices while keeping a removable notice for old bindings', () => {
    const revoked = { ...catalog.skills[1]!, id: 'old', name: 'Old scan', state: 'revoked' as const };
    const choices = skillChoices([...catalog.skills, revoked], ['old', 'missing', id]);
    expect(choices.available.map(s => s.id)).toEqual(['ordinary', id]);
    expect(choices.unavailable).toEqual([{ id: 'old', name: 'Old scan', reason: '已停用' }, { id: 'missing', name: 'missing', reason: '不可用' }]);
    expect(skillChoices([...catalog.skills, revoked], []).unavailable).toEqual([]);
  });
});

// These fixtures assert Chinese labels/order; the interface now follows an explicit locale.
beforeEach(() => setLocale("zh-CN"));
