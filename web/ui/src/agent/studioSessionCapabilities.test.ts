import { describe, expect, it } from "vitest";
import type { ModelInfo } from "@/api/types";
import { canSelectStudioModel, studioSessionModels, type StudioSessionCapabilities } from "./studioSessionCapabilities";

const catalog = [
  { id: "k3", configured: true },
  { id: "glm-5.3", configured: true },
  { id: "other", configured: true },
] as ModelInfo[];
const bound: StudioSessionCapabilities = { model: "k3", models: ["k3", "glm-5.3"], skills: [], mcp_servers: [] };

describe("conversation-bound Studio models", () => {
  it("shows only models in the pinned selection, even when the global catalog grows", () => {
    expect(studioSessionModels(catalog, bound).map((model) => model.id)).toEqual(["k3", "glm-5.3"]);
    expect(canSelectStudioModel(bound, "other")).toBe(false);
    expect(canSelectStudioModel(bound, "glm-5.3")).toBe(true);
  });

  it("does not substitute global permissions before the session binding arrives", () => {
    expect(studioSessionModels(catalog, null)).toEqual([]);
    expect(canSelectStudioModel(null, "k3")).toBe(false);
  });

  it("keeps an old single-model conversation restricted independently of newer Agent selections", () => {
    const old = { ...bound, models: ["k3"] };
    expect(studioSessionModels(catalog, old).map((model) => model.id)).toEqual(["k3"]);
    expect(canSelectStudioModel(old, "glm-5.3")).toBe(false);
  });

  it("does not enable an unrelated model when a selected model disappears", () => {
    expect(studioSessionModels(catalog.slice(2), bound)).toEqual([]);
    expect(canSelectStudioModel({ ...bound, models: [] }, "k3")).toBe(false);
  });
});
