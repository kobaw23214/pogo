import { inject, provide, type InjectionKey, type Ref } from "vue";

export interface ActivityReadingContext {
  following: Readonly<Ref<boolean>>;
  pause(): void;
}
export const activityReadingKey: InjectionKey<ActivityReadingContext> = Symbol("activity-reading");
export function provideActivityReading(context: ActivityReadingContext): void { provide(activityReadingKey, context); }
export function useActivityReading(): ActivityReadingContext | undefined { return inject(activityReadingKey, undefined); }

/** A layout update is not permission to take the reader back to the bottom. */
export function canAutoFoldActivity(input: {
  closed: boolean; following: boolean; active: boolean; full: boolean;
  userChoice?: boolean; openDescendant: boolean; focused: boolean; selected: boolean;
}): boolean {
  return input.closed && input.following && !input.active && !input.full &&
    input.userChoice === undefined && !input.openDescendant && !input.focused && !input.selected;
}

export function activityContainsSelection(element: HTMLElement | null): boolean {
  if (!element || typeof window === "undefined" || typeof window.getSelection !== "function") return false;
  const selection = window.getSelection();
  return !!selection && !selection.isCollapsed && !!selection.toString() &&
    (!!selection.anchorNode && element.contains(selection.anchorNode) || !!selection.focusNode && element.contains(selection.focusNode));
}
