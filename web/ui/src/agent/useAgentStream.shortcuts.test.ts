import { afterEach, describe, expect, it, vi } from "vitest";
import { useAgentStream } from "./useAgentStream";
import { observeStarterShortcutsChanged } from "./shortcutSources";

afterEach(() => vi.unstubAllGlobals());
describe("Agent shortcut creation notifications", () => {
  it("refreshes only the matching Agent on live saves and ignores replay", () => {
    const surface = new EventTarget() as EventTarget & { localStorage: { setItem: ReturnType<typeof vi.fn> } };
    surface.localStorage = { setItem: vi.fn() };
    vi.stubGlobal("window", surface);
    const refresh = vi.fn(), other = vi.fn();
    const stop = observeStarterShortcutsChanged(() => "ag_one", refresh);
    const stopOther = observeStarterShortcutsChanged(() => "ag_two", other);
    const stream = useAgentStream({ attachments: false });
    const event = { type: "shortcuts_changed" as const, seq: 1, ts: 1, shortcut_agent_id: "ag_one", shortcut_revision: 2, shortcut_id: "sh_saved" };
    stream.apply(event, { sessionId: "session", replay: true });
    expect(refresh).not.toHaveBeenCalled();
    stream.apply({ ...event, seq: 2 }, { sessionId: "session", replay: false });
    expect(refresh).toHaveBeenCalledTimes(1); expect(other).not.toHaveBeenCalled();
    expect(surface.localStorage.setItem).toHaveBeenCalledTimes(1);
    stop(); stopOther(); stream.disconnect();
  });
});
