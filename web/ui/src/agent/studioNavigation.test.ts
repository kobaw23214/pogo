import { describe, expect, it } from "vitest";
import { confirmStudioNavigation, registerStudioNavigationGuard } from "./studioNavigation";

describe("unsaved Studio editors", () => {
  it("stays on the current module when an editor cancels navigation", async () => {
    const remove = registerStudioNavigationGuard(async () => false);
    try { expect(await confirmStudioNavigation()).toBe(false); }
    finally { remove(); }
    expect(await confirmStudioNavigation()).toBe(true);
  });

  it("awaits the user's decision before leaving and releases unmounted editors", async () => {
    let answer!: (allowed: boolean) => void;
    const remove = registerStudioNavigationGuard(() => new Promise<boolean>((resolve) => { answer = resolve; }));
    const pending = confirmStudioNavigation();
    answer(true);
    expect(await pending).toBe(true);
    remove();
    expect(await confirmStudioNavigation()).toBe(true);
  });
});
