import type { PlatformAgent } from "./platformTypes";
import type { SessionInfo, StarterCard, Starters } from "@/api/types";

// Agent Studio context for the full Pogo workbench (AgentView's studioAgentId
// prop). Pure and DOM-free so the boot/persistence policy is testable without
// mounting the workbench.
//
// Shared-state policy, stated honestly: an Agent Studio session is an ordinary
// Pogo session owned by the signed-in user, so the WORKSPACE it runs in —
// files, dock panels and terminal — is the same account-wide one the classic
// "/" workbench uses. Model/Skill/MCP selection comes from the backend's pinned
// session binding; it does not create a separate operating-system sandbox.
// What is namespaced per Agent in this module is the browser's memory OF the
// Studio: which session was open, which dock sheet/tabs were up, which project
// groups were folded. One Agent's remembered session/dock must never reopen
// inside another Agent's Studio, so those keys carry the Agent id.

/** Per-Agent localStorage key: pogo:studio:{agentId}:{base minus its pogo: prefix}. */
export function studioKey(agentId: string, base: string): string {
  const bare = base.startsWith("pogo:") ? base.slice("pogo:".length) : base;
  return `pogo:studio:${agentId}:${bare}`;
}

/** Base key for the remembered open session (pair with studioKey). */
export const STUDIO_LAST_SESSION = "pogo:last-session";

/** The deep link's session id: /a/{agentId}?c={sessionId}. */
export function requestedSessionId(search: string): string | null {
  const sid = new URLSearchParams(search).get("c")?.trim();
  return sid || null;
}

/** Preserve the entry surface when opening, creating or restoring a conversation. */
export function studioPath(
  agentId: string,
  sessionId?: string | null,
  surface: { managedChat?: boolean; embedded?: boolean } = {},
): string {
  const base = surface.managedChat
    ? `/manage/agents/${encodeURIComponent(agentId)}/chat`
    : `/a/${encodeURIComponent(agentId)}`;
  const query = new URLSearchParams();
  if (surface.embedded && !surface.managedChat) query.set("embed", "1");
  if (sessionId) query.set("c", sessionId);
  return query.size ? `${base}?${query}` : base;
}

/**
 * Which session to open at boot: the deep link wins, then this Agent's own
 * remembered session, then the list's first row. A candidate absent from THIS
 * Agent's list is ignored outright — never another Agent's memory, never the
 * account-wide session pool. null means the caller creates a fresh session.
 */
export function chooseBootSession(
  sessions: readonly SessionInfo[],
  requested?: string | null,
  remembered?: string | null,
): string | null {
  const listed = (sid?: string | null): sid is string => !!sid && sessions.some((s) => s.id === sid);
  if (listed(requested)) return requested;
  if (listed(remembered)) return remembered;
  return sessions[0]?.id ?? null;
}

/**
 * The welcome screen from the Agent definition itself: description as the
 * heading, starter prompts as the ordinary workbench's starter cards. Prompt
 * cards only, and send stays false — a click fills the composer, it never
 * spends a model call on the user's behalf.
 */
export function studioStarters(agent: PlatformAgent): Starters {
  // Card entries keep their title, corner label and icon; plain strings stay
  // the simple full-text card. Pagination belongs to StarterCards; this
  // adapter must preserve the entire saved set and ordered content.
  const cards: StarterCard[] = agent.starters
    .map<StarterCard>((entry) => {
      if (typeof entry === "string") {
        const prompt = entry.trim();
        return { title: prompt, prompt };
      }
      const prompt = entry.prompt.trim();
      return {
        icon: entry.icon,
        title: entry.title?.trim() || prompt,
        label: entry.label?.trim() || undefined,
        prompt,
        ...(entry.parts ? { parts: entry.parts.map(part => ({ ...part })) } : {}),
        ...(entry.input_layout ? { input_layout: entry.input_layout } : {}),
      };
    })
    .filter((c) => c.prompt || c.parts?.length);
  return { heading: agent.description || agent.name, send: false, cards };
}
