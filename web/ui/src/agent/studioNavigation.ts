export type StudioNavigationGuard = () => boolean | Promise<boolean>;
const guards = new Set<StudioNavigationGuard>();

/** Active editors own their leave guard and remove it when unmounted. */
export function registerStudioNavigationGuard(guard: StudioNavigationGuard): () => void {
  guards.add(guard);
  return () => { guards.delete(guard); };
}

export async function confirmStudioNavigation(): Promise<boolean> {
  for (const guard of [...guards]) {
    if (!(await guard())) return false;
  }
  return true;
}
