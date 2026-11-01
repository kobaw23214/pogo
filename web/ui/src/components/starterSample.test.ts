import { describe, expect, it } from "vitest";
import { starterSample } from "@/components/starterSample";
import { starterIconNames } from "@/components/starterIcons";

// A copy of the Go side's skills.starterIcons. The list lives there because the
// server is what enforces it; this copy exists so the frontend table and the
// backend set cannot drift apart silently — a name added in one place and
// forgotten in the other shows up here, not as a card that quietly vanishes on
// its next save.
const serverIcons = [
  "search", "filter", "eye", "target", "scan",
  "fingerprint", "compare", "graph", "trend", "clock",
  "list", "table", "tag", "flag", "code",
  "terminal", "bug", "shield", "lock", "key",
  "mask", "file", "folder", "archive", "box",
  "book", "layers", "database", "hard-drive", "cpu",
  "robot", "network", "share", "link", "globe",
  "cloud", "mail", "message", "bell", "user",
  "users", "puzzle", "wand", "spark", "bolt",
  "fire", "warn", "refresh", "history", "download",
];

describe("the closed icon set", () => {
  it("renders the same fifty names the server validates", () => {
    expect([...starterIconNames].sort()).toEqual([...serverIcons].sort());
  });
});

describe("the downloadable starter sample", () => {
  const cards = starterSample.cards;

  it("carries twenty cards — ten medium, then ten complex", () => {
    expect(cards).toHaveLength(20);
  });

  it("passes every server-side validation rule, so import warns about nothing", () => {
    cards.forEach((c, i) => {
      const title = (c.title ?? "").trim();
      expect(title.length, `card ${i + 1}: title required`).toBeGreaterThan(0);
      expect([...title].length, `card ${i + 1}: title ≤ 40`).toBeLessThanOrEqual(40);
      expect([...(c.label ?? "").trim()].length, `card ${i + 1}: label ≤ 30`).toBeLessThanOrEqual(30);
      if (c.icon) expect(serverIcons, `card ${i + 1}: known icon`).toContain(c.icon);
      const hasPrompt = Boolean((c.prompt ?? "").trim());
      const hasPanel = Boolean((c.panel ?? "").trim());
      expect(hasPrompt !== hasPanel, `card ${i + 1}: exactly one action`).toBe(true);
      if (hasPrompt) {
        expect([...c.prompt!.trim()].length, `card ${i + 1}: prompt ≤ 2000`).toBeLessThanOrEqual(2000);
      }
    });
  });

  // A starter card is a first message: it lands on an empty conversation, so it
  // may not lean on a previous turn. Cards that continue one belong in
  // followups, where the `when` match guarantees the turn happened.
  it("is self-contained — no card references an earlier turn", () => {
    const deixis = ["上面那个", "该样本", "刚才", "这个样本"];
    cards.forEach((c, i) => {
      const prompt = c.prompt ?? "";
      for (const marker of deixis) {
        expect(prompt, `card ${i + 1} references a previous turn via "${marker}"`).not.toContain(marker);
      }
    });
  });

  it("drives the browser on exactly three cards, all in the medium half", () => {
    const browser = cards.filter((c) => (c.prompt ?? "").startsWith("/skill:browser-use"));
    expect(browser).toHaveLength(3);
    for (const c of browser) {
      expect(cards.indexOf(c), "browser-use cards sit with the medium openings").toBeLessThan(10);
    }
  });
});
