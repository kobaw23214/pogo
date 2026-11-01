import { afterEach, describe, expect, it, vi } from "vitest";
import { isAuthoredSkillSummary, isSavedSkillDetails, notifyLiveAuthoredSkillSaved } from "./skillAuthoringReceipt";
export const authored = () => ({ id: "as_abc123", name: "sql-dates", description: "Adjust SQL dates", revision: "a".repeat(64), version: 1, editable: true, enabled: true, source: "authored" });
const receipt = () => ({ type: "skill_authoring", action: "save", saved: true, agent_id: "ag_owner", skill: authored(), request_id: "save-once", reused: false, enabled: true, pending: false, behavior_tested: false });
afterEach(() => vi.unstubAllGlobals());
describe("authored Skill receipts", () => {
  it("accepts persisted host metadata, draft receipts and pending activation", () => {
    expect(isAuthoredSkillSummary(authored())).toBe(true);
    expect(isSavedSkillDetails(receipt())).toBe(true);
    expect(isSavedSkillDetails({ ...receipt(), pending: true })).toBe(true);
    expect(isSavedSkillDetails({ ...receipt(), enabled: false, skill: { ...authored(), enabled: false } })).toBe(true);
    expect(isSavedSkillDetails({ ...receipt(), pending: true, enabled: false, skill: { ...authored(), enabled: false }, request_id: "save:sql.2026-10-22" })).toBe(true);
  });
  it.each([{ saved: false }, { action: "read" }, { agent_id: "../other" }, { request_id: "" }, { behavior_tested: true }, { skill: null }, { enabled: false }, { skill: { ...authored(), source: "bundle" } }, { skill: { ...authored(), version: 0 } }, { skill: { ...authored(), revision: "not-a-revision" } }])("does not show a success card for malformed or mismatched metadata %j", patch => {
    expect(isSavedSkillDetails({ ...receipt(), ...patch })).toBe(false);
  });
  it("refreshes the exact Agent once per live save, and never treats text as a receipt", () => {
    const dispatchEvent = vi.fn(); vi.stubGlobal("window", { dispatchEvent }); vi.stubGlobal("CustomEvent", class { constructor(readonly type: string, readonly init: unknown) {} });
    const saved = { ...receipt(), request_id: "notify-test" };
    expect(isSavedSkillDetails(saved)).toBe(true);
    notifyLiveAuthoredSkillSaved(saved as Parameters<typeof notifyLiveAuthoredSkillSaved>[0]);
    notifyLiveAuthoredSkillSaved(saved as Parameters<typeof notifyLiveAuthoredSkillSaved>[0]);
    expect(dispatchEvent).toHaveBeenCalledTimes(1);
    expect(dispatchEvent).toHaveBeenCalledWith(expect.objectContaining({ type: "pogo-skills-changed", init: { detail: { agentId: "ag_owner", skillId: "as_abc123", revision: "a".repeat(64) } } }));
  });
});
