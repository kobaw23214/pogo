import type { NativeBrowserState, NativeBrowserTarget } from "./nativeBrowser";

export interface BrowserHumanAdoption { adopt_if_unowned: true; generation: number; control_epoch: 0 }
interface AdoptionView {
  sessionId: string; target: NativeBrowserTarget; state: NativeBrowserState;
  visible: boolean; connected: boolean; busy: boolean; lease: string; knownEpoch?: number;
}

/** This is lease initialization for a human Page whose owner is unknown, not
 * a takeover. The server alone proves that the Page has no existing lease. */
export class BrowserHumanAdoptions {
  private attempted = new Set<string>();
  request(view: AdoptionView): BrowserHumanAdoption | null {
    const { state, target } = view;
    if (!view.visible || !view.connected || view.busy || view.lease || (view.knownEpoch ?? 0) > 0
      || !view.sessionId || state.control !== "human" || state.control_epoch !== 0 || state.busy
      || state.dialog || state.debugger_paused || state.devtools_blocked || state.reset
      || state.context_ref !== target.context_ref || state.viewed_tab !== target.tab_id
      || !/^ctx_[a-f0-9]{24}$/.test(target.context_ref) || !/^t[1-9]\d*$/.test(target.tab_id)
      || !Number.isSafeInteger(state.generation) || state.generation! < 1) return null;
    const stamp = JSON.stringify([view.sessionId, target.context_ref, target.tab_id, state.generation, 0]);
    if (this.attempted.has(stamp)) return null;
    this.attempted.add(stamp);
    // Only nonsecret identities are retained, and an old observation can never
    // replay an uncertain request while its stamp remains in this window.
    return { adopt_if_unowned: true, generation: state.generation!, control_epoch: 0 };
  }
}

// Multiple observing Sheets in one window cannot initialize the same Page
// twice. Nothing is persisted across reloads and no token is stored here.
export const browserHumanAdoptions = new BrowserHumanAdoptions();
