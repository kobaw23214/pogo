import type { SkillExecutionIssue, SkillExecutionStatus } from '@/api/skillTypes';
import type { SkillUploadResult } from '@/api/types';
import { gt } from '@/i18n';

const HOST_ISSUE_CODES = new Set([
  'INVALID_PLATFORM', 'PLATFORM_MISMATCH', 'UNSUPPORTED_HOST_PLATFORM', 'UNSUPPORTED_PLATFORM', 'INVALID_MANIFEST',
  'SANDBOX_UNAVAILABLE', 'RUNNER_UNAVAILABLE', 'RUNTIME_DEPENDENCY_UNAVAILABLE', 'EXECUTOR_REQUIRED',
  'EXECUTOR_UNAVAILABLE', 'INTEGRITY_FAILED', 'SELECTION_INVALID',
]);

function issueText(code: string | undefined, message: string): string {
  return code && HOST_ISSUE_CODES.has(code) ? gt(`settingsCoverage.skillIssues.${code}`) : message;
}

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
  const messages = [...issues.map(issue => issueText(issue.code, issue.message)), issueText(execution?.issue_code, execution?.issue ?? '')];
  return [...new Set(messages.map(message => message.trim()).filter(Boolean))];
}

/** Keep the original host diagnostic available for troubleshooting, separately
 * from translated summaries. Arbitrary external messages are never rewritten. */
export function skillExecutionDiagnosticDetails(execution?: SkillExecutionStatus | null, issues: readonly SkillExecutionIssue[] = []): string[] {
  const values = [...issues, ...(execution?.issue ? [{ code: execution.issue_code ?? '', message: execution.issue }] : [])];
  return [...new Set(values.filter(issue => HOST_ISSUE_CODES.has(issue.code) && issue.message.trim() && issueText(issue.code, issue.message) !== issue.message)
    .map(issue => `${issue.code}: ${issue.message.trim()}`))];
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
