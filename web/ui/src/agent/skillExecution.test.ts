import { describe, expect, it } from 'vitest';
import type { SkillExecutionStatus } from '@/api/skillTypes';
import type { SkillUploadResult } from '@/api/types';
import { canConfigureUploadedSkill, skillExecutionMessages, skillExecutionPlatforms, skillUploadState } from './skillExecution';

const execution = (overrides: Partial<SkillExecutionStatus> = {}): SkillExecutionStatus => ({
  configured: true,
  host_platform: 'linux/arm64',
  supported_platforms: ['linux/amd64', 'linux/arm64'],
  ...overrides,
});
const installed = (overrides: Partial<SkillUploadResult> = {}): SkillUploadResult => ({
  status: 'installed', name: 'Analysis', skill_ids: ['rs_abc'], ...overrides,
});

describe('Skill execution presentation', () => {
  it('reports a deployment target mismatch without treating native ARM as unsupported', () => {
    expect(skillExecutionPlatforms(execution({ expected_platform: 'linux/amd64' }))).toEqual({
      host: 'linux/arm64', expected: 'linux/amd64', mismatch: true,
      supported: ['linux/amd64', 'linux/arm64'],
    });
    expect(skillExecutionPlatforms(execution({ expected_platform: 'linux/arm64' })).mismatch).toBe(false);
    expect(skillExecutionPlatforms(execution()).mismatch).toBe(false);
  });

  it('does not fabricate platform information for an older or missing response', () => {
    expect(skillExecutionPlatforms()).toEqual({ host: '', expected: '', mismatch: false, supported: [] });
    expect(skillExecutionPlatforms({ configured: false } as SkillExecutionStatus)).toEqual({ host: '', expected: '', mismatch: false, supported: [] });
  });

  it('preserves package and executor reasons and shows duplicated messages once', () => {
    expect(skillExecutionMessages(execution({ issue: 'Executor is unavailable' }), [
      { code: 'executor_unavailable', message: ' Executor is unavailable ' },
      { code: 'platform_mismatch', message: 'Package requires linux/amd64' },
    ])).toEqual(['Executor is unavailable', 'Package requires linux/amd64']);
  });

  it('does not lose an executor issue when the selection has no issues', () => {
    expect(skillExecutionMessages(execution({ issue: 'Cannot start the executor' }))).toEqual(['Cannot start the executor']);
  });
});

describe('Skill installation and readiness', () => {
  it('uses server readiness rather than configured or warning presence', () => {
    expect(skillUploadState(installed({ ready: false, execution: execution(), warnings: [] }))).toBe('pending');
    expect(skillUploadState(installed({ ready: true, warnings: ['Optional metadata was ignored'] }))).toBe('ready');
  });

  it('does not infer executability for knowledge skills or old upload responses', () => {
    expect(skillUploadState({ name: 'Knowledge' })).toBe('installed');
    expect(skillUploadState(installed({ execution: execution() }))).toBe('installed');
  });

  it('allows configuring drafts with pending runtimes or installation warnings', () => {
    expect(canConfigureUploadedSkill(installed({ ready: false, warnings: ['Runtime cannot execute here'] }))).toBe(true);
    expect(canConfigureUploadedSkill(installed({ ready: true }))).toBe(true);
    expect(canConfigureUploadedSkill({ name: 'Knowledge' })).toBe(false);
    expect(canConfigureUploadedSkill(null)).toBe(false);
  });

  it('never offers the incoming package before replacement or after keeping the old version', () => {
    for (const status of ['confirm_replace', 'kept_old'] as const) {
      const result = installed({ status, ready: true });
      expect(skillUploadState(result)).toBe(status);
      expect(canConfigureUploadedSkill(result)).toBe(false);
    }
  });
});
