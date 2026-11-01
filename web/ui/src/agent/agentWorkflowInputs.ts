import type { AgentWorkflowInput, AgentWorkflowInputOption } from '@/api/agentWorkflows';

export function workflowInputDefaults(fields: AgentWorkflowInput[]): Record<string, string> {
  return Object.fromEntries(fields.map(field => [field.key, field.default || '']));
}
export function workflowInputOptions(field: AgentWorkflowInput, yes = 'Yes', no = 'No'): AgentWorkflowInputOption[] {
  return field.type === 'boolean' && !field.options?.length ? [{ value: 'yes', label: yes }, { value: 'no', label: no }] : field.options || [];
}
export function workflowInputDisplay(fields: AgentWorkflowInput[], key: string, value: string, yes = 'Yes', no = 'No'): { label: string; value: string } {
  const field = fields.find(input => input.key === key);
  const selected = field && field.type && field.type !== 'text' ? workflowInputOptions(field, yes, no).find(option => option.value === value) : undefined;
  return { label: field?.label || key, value: selected?.label || selected?.value || value };
}
export function workflowSelectedFiles(value: string): string[] {
  return [...new Set(value.split(/\r?\n/).map(path => path.trim()).filter(Boolean))];
}
export function safeWorkflowFilePath(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0 && value.length <= 1024 && !value.startsWith('/') && !/[\\\u0000-\u001f\u007f]/.test(value) && value.split('/').every(part => part !== '' && part !== '.' && part !== '..') && !/^[A-Za-z]:/.test(value);
}
