import { describe, expect, it } from 'vitest';
import { safeWorkflowFilePath, workflowInputDefaults, workflowInputOptions, workflowInputDisplay, workflowSelectedFiles } from './agentWorkflowInputs';
describe('typed workflow form values', () => {
  it('preserves string choice values and legacy empty text while applying declared defaults', () => {
    const fields = [{ key: 'topic' }, { key: 'review', type: 'boolean' as const, default: 'yes' }, { key: 'audience', type: 'select' as const, default: 'team', options: [{ value: 'team', label: '同事' }] }];
    expect(workflowInputDefaults(fields)).toEqual({ topic: '', review: 'yes', audience: 'team' });
    expect(workflowInputOptions(fields[1]!, '需要', '不需要')).toEqual([{ value: 'yes', label: '需要' }, { value: 'no', label: '不需要' }]);
    expect(workflowInputOptions(fields[2]!)[0]?.value).toBe('team');
    expect(workflowInputDisplay(fields, 'audience', 'team')).toEqual({ label:'audience', value:'同事' });
    expect(workflowInputDisplay(fields, 'review', 'yes', '需要', '不需要').value).toBe('需要');
    expect(workflowInputDisplay([{key:'legacy'}], 'legacy', 'yes').value).toBe('yes');
  });
  it('deduplicates selected relative paths and rejects paths outside the source workspace', () => {
    expect(workflowSelectedFiles('资料/a b.md\r\n资料/a b.md\nreport.txt\n')).toEqual(['资料/a b.md', 'report.txt']);
    expect(safeWorkflowFilePath('资料/a b.md')).toBe(true);
    for (const path of ['/tmp/private', '../other', 'dir/../../other', 'dir\\other', 'a\nb', 'C:/secret', './file']) expect(safeWorkflowFilePath(path)).toBe(false);
  });
});
