import type { AgentWorkflowCodeSpec, AgentWorkflowValueSchema, AgentWorkflowValueType } from '@/api/agentWorkflows';

export const workflowValueTypes: AgentWorkflowValueType[] = ['string', 'number', 'integer', 'boolean', 'object', 'array'];
export function defaultWorkflowCode(language: AgentWorkflowCodeSpec['language'] = 'python'): AgentWorkflowCodeSpec {
  return { language, source: language === 'python' ? 'def main(inputs):\n    return {"result": inputs["value"]}\n' : 'async function main(inputs) {\n  return { result: inputs.value };\n}\n', inputs: { value: { type: 'string', value: '' } }, outputs: { result: { type: 'string' } }, timeout_seconds: 10 };
}
export function workflowTypeDefault(type: AgentWorkflowValueType): unknown {
  return type === 'string' ? '' : type === 'boolean' ? false : type === 'object' ? {} : type === 'array' ? [] : 0;
}
export function workflowValueMatches(type: AgentWorkflowValueType, value: unknown): boolean {
  return type === 'string' ? typeof value === 'string' : type === 'boolean' ? typeof value === 'boolean' : type === 'number' ? typeof value === 'number' && Number.isFinite(value) : type === 'integer' ? typeof value === 'number' && Number.isSafeInteger(value) : type === 'array' ? Array.isArray(value) : type === 'object' ? !!value && typeof value === 'object' && !Array.isArray(value) : false;
}
export function workflowSchemaValid(schema: AgentWorkflowValueSchema, depth = 0): boolean {
  if (!schema || depth > 8 || !workflowValueTypes.includes(schema.type)) return false;
  if (schema.type === 'array' && (!schema.items || !workflowSchemaValid(schema.items, depth + 1))) return false;
  if (schema.type === 'object' && Object.values(schema.properties || {}).some(value => !workflowSchemaValid(value, depth + 1))) return false;
  return true;
}
export function workflowNodeTestDefaults(spec: AgentWorkflowCodeSpec): Record<string, unknown> {
  return Object.fromEntries(Object.entries(spec.inputs || {}).map(([name, binding]) => [name, Object.prototype.hasOwnProperty.call(binding, 'value') ? binding.value : workflowTypeDefault(binding.type)]));
}
export function workflowNodeTestInputs(spec: AgentWorkflowCodeSpec, source: string): Record<string,unknown> | null {
  try {
    const value: unknown = JSON.parse(source);
    if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
    const inputs = value as Record<string,unknown>, names = Object.keys(spec.inputs || {});
    if (Object.keys(inputs).length !== names.length || names.some(name => !Object.prototype.hasOwnProperty.call(inputs, name) || !workflowValueMatches(spec.inputs[name]!.type, inputs[name]))) return null;
    return inputs;
  } catch { return null; }
}
