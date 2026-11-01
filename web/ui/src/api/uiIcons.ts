import { authHeaders, isDelegatedEmbed } from './client';

export const UI_ICON_MODULES = ['shortcuts', 'starters', 'agents'] as const;
export type UIIconModule = (typeof UI_ICON_MODULES)[number];
export interface UIIcon { id: string; name: string; path: string; modules: UIIconModule[]; archived?: boolean; created_at?: string }
export interface UIIconsView { revision: number; icons: UIIcon[]; hidden_builtin: Record<UIIconModule, string[]>; can_manage: boolean }
export const isUIIconID = (id: unknown): id is string => typeof id === 'string' && /^ui_[a-f0-9]{32}$/.test(id);
// Only path commands and finite numeric coordinates can cross into an SVG body.
// No markup, attributes, URLs, CSS, or external resources are accepted.
export function validUIIconPath(value: unknown): value is string {
  if (typeof value !== 'string' || !value || value.length > 4096 || value.trim() !== value || !/^[MmLlHhVvCcSsQqTtAaZz\d\s.,+-]+$/.test(value)) return false;
  const arities: Record<string, number> = {M:2,L:2,H:1,V:1,C:6,S:4,Q:4,T:2,A:7,Z:0};
  let position = 0, commands = 0, segments = 0;
  const whitespace = () => { while (position < value.length && /[ \t\r\n]/.test(value[position]!)) position++; };
  while (position < value.length) {
    whitespace();
    if (position === value.length) break;
    const command = value[position++]!.toUpperCase();
    const arity = arities[command];
    if (arity === undefined || (!commands && command !== 'M') || ++commands > 256) return false;
    if (!arity) { if (++segments > 256) return false; continue; }
    const numbers: number[] = [];
    let afterComma = false;
    while (position < value.length) {
      whitespace();
      if (position === value.length) break;
      if (value[position] === ',') {
        if (!numbers.length || afterComma) return false;
        afterComma = true; position++; continue;
      }
      if (/[A-Za-z]/.test(value[position]!)) break;
      const token = value.slice(position).match(/^[-+]?(?:\d*\.\d+|\d+\.?\d*)/);
      if (!token) return false;
      const number = Number(token[0]);
      if (!Number.isFinite(number) || Math.abs(number) > 512) return false;
      numbers.push(number); position += token[0].length; afterComma = false;
      if (numbers.length > 1536) return false;
    }
    if (afterComma || !numbers.length || numbers.length % arity) return false;
    segments += numbers.length / arity;
    if (segments > 256) return false;
    if (command === 'A') for (let i = 0; i < numbers.length; i += 7) {
      if (numbers[i]! < 0 || numbers[i + 1]! < 0 || Math.abs(numbers[i + 2]!) > 360 || ![0,1].includes(numbers[i + 3]!) || ![0,1].includes(numbers[i + 4]!)) return false;
    }
  }
  return commands > 0 && segments > 0;
}

export function validUIIcon(value: unknown): value is UIIcon {
  if (!value || typeof value !== 'object') return false;
  const i = value as UIIcon;
  return isUIIconID(i.id) && typeof i.name === 'string' && i.name.trim().length > 0 && [...i.name].length <= 80 && !/[\u0000-\u001f\u007f]/.test(i.name) && validUIIconPath(i.path) && Array.isArray(i.modules) && i.modules.every(m => UI_ICON_MODULES.includes(m)) && (i.archived === undefined || typeof i.archived === 'boolean');
}
export function validUIIconsView(value: unknown): value is UIIconsView {
  if (!value || typeof value !== 'object') return false;
  const view = value as UIIconsView;
  return Number.isInteger(view.revision) && view.revision >= 0 && typeof view.can_manage === 'boolean' && Array.isArray(view.icons) && view.icons.length <= 256 && view.icons.every(validUIIcon) && !!view.hidden_builtin && UI_ICON_MODULES.every(m => Array.isArray(view.hidden_builtin[m]) && view.hidden_builtin[m].every(v => typeof v === 'string'));
}
export class UIIconsAPIError extends Error {
  constructor(message: string, public status: number, public current?: UIIconsView) { super(message); }
}
async function request(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<unknown> {
  if (isDelegatedEmbed) throw new UIIconsAPIError('Icon management is unavailable in an embedded session', 403);
  const response = await fetch(path, { method, signal, credentials: 'same-origin', cache: 'no-store', headers: { ...authHeaders(), ...(body ? { 'Content-Type': 'application/json' } : {}) }, body: body ? JSON.stringify(body) : undefined });
  const value: unknown = await response.json().catch(() => undefined);
  if (!response.ok) {
    const error = value as {error?: string; current?: unknown} | undefined;
    throw new UIIconsAPIError(error?.error || `HTTP ${response.status}`, response.status, validUIIconsView(error?.current) ? error.current : undefined);
  }
  return value;
}
async function view(method: string, body?: unknown, signal?: AbortSignal) {
  const value = await request(method, '/api/ui-icons', body, signal);
  if (!validUIIconsView(value)) throw new UIIconsAPIError('Invalid icon library response', 502);
  return value;
}
export const uiIconsApi = {
  list: (signal?: AbortSignal) => view('GET', undefined, signal),
  save: (value: UIIconsView, signal?: AbortSignal) => view('PUT', {revision: value.revision, icons: value.icons, hidden_builtin: value.hidden_builtin}, signal),
  generate: async (prompt: string, model: string, count: number, signal?: AbortSignal) => {
    const value = await request('POST', '/api/ui-icons/generate', {prompt, model, count}, signal) as {icons?: unknown[]; model?: string};
    if (!Array.isArray(value.icons) || !value.icons.length || value.icons.length > 4 || !value.icons.every(validUIIcon)) throw new UIIconsAPIError('Invalid generated icons', 502);
    return {icons: value.icons as UIIcon[], model: value.model};
  },
};
