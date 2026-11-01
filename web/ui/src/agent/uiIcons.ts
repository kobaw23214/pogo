import { readonly, shallowRef } from 'vue';
import { isUIIconID, validUIIconPath, uiIconsApi, UIIconsAPIError, type UIIcon, type UIIconModule, type UIIconsView } from '@/api/uiIcons';

const library = shallowRef<UIIconsView | null>(null);
const paths = shallowRef<Record<string, string>>({});
const loading = new Map<string, Promise<void>>();
const missing = new Set<string>();
let generation = 0;
let pending: Promise<UIIconsView | null> | undefined;
export const uiIconLibrary = readonly(library);
export const uiIconPaths = readonly(paths);
export function acceptUIIcons(view: UIIconsView) {
  library.value = structuredClone(view);
  const next = {...paths.value};
  for (const icon of view.icons) if (validUIIconPath(icon.path)) next[icon.id] = icon.path;
  paths.value = next;
}
/** Clear private names, drafts and requests when leaving an authenticated surface. */
export function clearUIIcons() { generation++; library.value = null; paths.value = {}; loading.clear(); missing.clear(); pending = undefined; }
export function refreshUIIcons(): Promise<UIIconsView | null> {
  if (pending) return pending;
  const current = generation;
  const request = uiIconsApi.list().then(view => { if (current === generation) acceptUIIcons(view); return current === generation ? view : null; }).catch(error => {
    if (current === generation && error instanceof UIIconsAPIError && (error.status === 401 || error.status === 403)) clearUIIcons();
    return null;
  }).finally(() => { if (pending === request) pending = undefined; });
  pending = request;
  return request;
}
/** Public shares resolve only IDs that occur in a saved card; they never enumerate the library. */
export function ensureUIIcon(id: string): void {
  if (!isUIIconID(id) || paths.value[id] || loading.has(id) || missing.has(id) || typeof fetch === 'undefined') return;
  const current = generation;
  const request = fetch(`/icon-assets/${id}.json`, {credentials: 'omit', cache: 'force-cache'}).then(async response => {
    if (!response.ok) { if (current === generation) missing.add(id); return; }
    const value = await response.json() as {id?: string; path?: unknown; width?: number; height?: number};
    if (current === generation && value.id === id && value.width === 16 && value.height === 16 && validUIIconPath(value.path)) paths.value = {...paths.value, [id]: value.path};
  }).catch(() => {}).finally(() => { if (loading.get(id) === request) loading.delete(id); });
  loading.set(id, request);
}
export function customUIIcons(module: UIIconModule): readonly UIIcon[] { return library.value?.icons.filter(i => !i.archived && i.modules.includes(module)) ?? []; }
export function hiddenUIBuiltins(module: UIIconModule): readonly string[] { return library.value?.hidden_builtin[module] ?? []; }
export function uiIconName(id: string): string { return library.value?.icons.find(i => i.id === id)?.name ?? ''; }
