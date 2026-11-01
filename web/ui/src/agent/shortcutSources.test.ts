import { afterEach, describe, expect, it, vi } from "vitest";
import { notifyStarterShortcutsChanged, observeStarterShortcutsChanged } from "./shortcutSources";
afterEach(() => vi.unstubAllGlobals());
describe("homepage shortcut source notifications", () => {
  it("refreshes only the current Agent and removes listeners on disposal", () => {
    const surface = new EventTarget() as EventTarget & { localStorage: { setItem: ReturnType<typeof vi.fn> } };
    surface.localStorage = { setItem: vi.fn() };
    vi.stubGlobal("window", surface);
    let agent = "agent-a";
    const refresh = vi.fn();
    const stop = observeStarterShortcutsChanged(() => agent, refresh);
    notifyStarterShortcutsChanged("agent-b"); expect(refresh).not.toHaveBeenCalled();
    notifyStarterShortcutsChanged("agent-a"); expect(refresh).toHaveBeenCalledTimes(1);
    agent = "agent-b";
    notifyStarterShortcutsChanged("agent-a"); expect(refresh).toHaveBeenCalledTimes(1);
    notifyStarterShortcutsChanged("agent-b"); expect(refresh).toHaveBeenCalledTimes(2);
    stop(); notifyStarterShortcutsChanged("agent-b"); expect(refresh).toHaveBeenCalledTimes(2);
  });
  it("uses cross-window storage notifications without storing shortcut contents", () => {
    const surface = new EventTarget() as EventTarget & { localStorage: { setItem: ReturnType<typeof vi.fn> } };
    surface.localStorage = { setItem: vi.fn() }; vi.stubGlobal("window", surface);
    const refresh = vi.fn(), stop = observeStarterShortcutsChanged(() => "agent-a", refresh);
    const event = new Event("storage"); Object.assign(event, { key: "pogo:starter-shortcuts-change:agent-a" });
    surface.dispatchEvent(event); expect(refresh).toHaveBeenCalledTimes(1);
    notifyStarterShortcutsChanged("agent-a");
    expect(surface.localStorage.setItem).toHaveBeenCalledWith("pogo:starter-shortcuts-change:agent-a", expect.stringMatching(/^\d+:[\d.]+$/));
    stop();
  });
});
