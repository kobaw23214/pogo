import { describe, expect, it } from "vitest";
import { pickFollowups } from "./followupPick";
import type { FollowupGroup } from "./followups";
import type { TimelineItem } from "./timeline";

const user = (text: string): TimelineItem => ({ kind: "user", id: "u", text });

const turn = (id: string, text: string, calls: { name: string; args?: unknown }[] = []): TimelineItem => ({
  kind: "turn",
  id,
  index: 1,
  thinking: "",
  text,
  streaming: false,
  // The cast keeps the fixture readable: only id/text/calls are read.
  calls: calls.map((c) => ({ callId: "c", name: c.name, args: c.args, running: false, orphaned: false })),
} as TimelineItem);

const generated = { title: "继续深入", prompt: "go deeper" };
const groups: FollowupGroup[] = [
  { when: ["mal-decompile"], chips: [{ title: "生成 Yara", prompt: "yara" }] },
];
const noChips = { messageId: null, chips: [] };

describe("pickFollowups", () => {
  it("prefers the server's chips when they name the last turn", () => {
    const items = [user("hi"), turn("m12", "ran mal-decompile")];
    const pick = pickFollowups({ messageId: "m12", chips: [generated] }, items, groups);
    expect(pick).toEqual({ chips: [generated], source: "generated", messageId: "m12" });
  });

  // The generated row can trail the run's end while the next question is
  // already up: arrival order must never put one turn's chips under another.
  it("falls back to the rules when the generated chips name an older turn", () => {
    const items = [turn("m11", "first"), user("next"), turn("m12", "ran mal-decompile")];
    const pick = pickFollowups({ messageId: "m11", chips: [generated] }, items, groups);
    expect(pick?.source).toBe("rules");
    expect(pick?.chips[0].title).toBe("生成 Yara");
    expect(pick?.messageId).toBe("m12");
  });

  it("uses the rules when nothing was generated", () => {
    const items = [turn("m12", "ran mal-decompile")];
    const pick = pickFollowups(noChips, items, groups);
    expect(pick?.source).toBe("rules");
    expect(pick?.chips[0].title).toBe("生成 Yara");
  });

  it("treats an empty generated set as absent", () => {
    const items = [turn("m12", "ran mal-decompile")];
    const pick = pickFollowups({ messageId: "m12", chips: [] }, items, groups);
    expect(pick?.source).toBe("rules");
  });

  it("shows nothing when neither source has anything relevant", () => {
    const items = [turn("m12", "just chatting")];
    expect(pickFollowups(noChips, items, groups)).toBeNull();
    // A stale generated id plus no rule match is still silence, not stale chips.
    expect(pickFollowups({ messageId: "m11", chips: [generated] }, items, groups)).toBeNull();
  });

  it("shows nothing before the first turn", () => {
    expect(pickFollowups({ messageId: "m12", chips: [generated] }, [user("hi")], groups)).toBeNull();
    expect(pickFollowups(noChips, [], groups)).toBeNull();
  });

  it("does not need configured groups to show generated chips", () => {
    const items = [turn("m12", "anything")];
    const pick = pickFollowups({ messageId: "m12", chips: [generated] }, items, []);
    expect(pick?.source).toBe("generated");
  });
});
