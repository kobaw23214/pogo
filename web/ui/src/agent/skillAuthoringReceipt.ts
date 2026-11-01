import type { AuthoredSkillSummary, SkillAuthoringDetails } from "@/api/types";

const identity = /^[A-Za-z0-9_-]{1,160}$/;
const revision = /^[a-f0-9]{64}$/;
export function isAuthoredSkillSummary(value: unknown): value is AuthoredSkillSummary {
  if (!value || typeof value !== "object") return false;
  const s = value as AuthoredSkillSummary;
  return typeof s.id === "string" && identity.test(s.id)
    && typeof s.name === "string" && /^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(s.name) && s.name.length <= 64
    && typeof s.description === "string" && !!s.description.trim()
    && typeof s.revision === "string" && revision.test(s.revision)
    && Number.isSafeInteger(s.version) && s.version > 0
    && typeof s.editable === "boolean" && typeof s.enabled === "boolean"
    && s.source === "authored";
}
export function isSavedSkillDetails(value: unknown): value is SkillAuthoringDetails {
  if (!value || typeof value !== "object") return false;
  const d = value as SkillAuthoringDetails;
  return d.type === "skill_authoring" && d.action === "save" && d.saved === true
    && typeof d.agent_id === "string" && identity.test(d.agent_id)
    && typeof d.request_id === "string" && /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$/.test(d.request_id)
    && typeof d.reused === "boolean" && typeof d.pending === "boolean"
    && typeof d.enabled === "boolean" && d.behavior_tested === false
    && isAuthoredSkillSummary(d.skill) && d.enabled === d.skill.enabled;
}

// A live receipt refreshes completion once. Historical transcript cards remain
// usable without repeatedly sending catalog refresh events during replay.
const notified = new Set<string>();
export function notifyAuthoredSkillChanged(agentId: string, id: string, rev: string) {
  if (typeof window !== "undefined") window.dispatchEvent(new CustomEvent("pogo-skills-changed", { detail: { agentId, skillId: id, revision: rev } }));
}
export function notifyLiveAuthoredSkillSaved(details: SkillAuthoringDetails) {
  if (!isSavedSkillDetails(details)) return;
  const key = `${details.agent_id}:${details.request_id}:${details.skill.revision}`;
  if (notified.has(key)) return;
  notified.add(key);
  if (notified.size > 256) notified.delete(notified.values().next().value!);
  notifyAuthoredSkillChanged(details.agent_id, details.skill.id, details.skill.revision);
}
