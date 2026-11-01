import type { SkillExecutionIssue, SkillExecutionStatus } from '@/api/skillTypes';
import type { SkillUploadResult } from '@/api/types';

/** The host is the server's runtime platform, never the browser's platform. */
export function skillExecutionPlatforms(execution?: SkillExecutionStatus | null) {
  const host = execution?.host_platform ?? '';
  const expected = execution?.expected_platform ?? '';
  return {
    host,
    expected,
    mismatch: !!host && !!expected && host !== expected,
    supported: execution?.supported_platforms ?? [],
  };
}

/** Selection and executor diagnostics can report the same cause. Show it once. */
export function skillExecutionMessages(
  execution?: SkillExecutionStatus | null,
  issues: readonly SkillExecutionIssue[] = [],
): string[] {
  const messages = [...issues.map(issue => issue.message), execution?.issue ?? ''];
  return [...new Set(messages.map(message => message.trim()).filter(Boolean))];
}

export type SkillUploadState = 'installed' | 'ready' | 'pending' | 'confirm_replace' | 'kept_old';

/** Installation and executability are separate server facts. Missing ready is unknown. */
export function skillUploadState(result: SkillUploadResult): SkillUploadState {
  if (result.status === 'confirm_replace' || result.status === 'kept_old') return result.status;
  if (result.ready === false) return 'pending';
  if (result.ready === true) return 'ready';
  return 'installed';
}

/** A pending runtime may configure an Agent draft; publication still needs plan.ready. */
export function canConfigureUploadedSkill(result: SkillUploadResult | null): boolean {
  return !!result?.skill_ids?.length && result.status !== 'kept_old' && result.status !== 'confirm_replace';
}
