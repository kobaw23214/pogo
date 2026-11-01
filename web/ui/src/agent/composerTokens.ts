import { parseSkillBlock } from "./timeline";
import type { SkillBlock } from "./timeline";
import { normalizeComposerParts, skillComposerPart, type ComposerPart } from "./composerParts";

export interface ComposerToken {
  kind: "skill" | "command";
  /** A skill name, or the slash-prefixed command name. */
  name: string;
  /** Everything after the completed invocation's first separator. */
  body: string;
  /** The canonical invocation and one ASCII space, ready for serialization. */
  prefix: string;
}

interface AvailableSkill { name: string; aliases?: string[] }
function availableSkill(skills: readonly AvailableSkill[], name: string) { return skills.find(item => item.name === name) ?? skills.find(item => item.aliases?.includes(name)); }

/** Capture the selected top token before appending a mixed shortcut changes the layout. */
export function composerDraftWithToken(parts: readonly ComposerPart[], token: ComposerToken | null): ComposerPart[] {
  return normalizeComposerParts([...(token?.kind === "skill" ? [skillComposerPart(token.name)] : []), ...parts]);
}

// One separator completes a token. Further whitespace belongs to the request,
// including intentional blank lines. A CRLF pair is one newline separator.
const SKILL_INVOCATION = /^\/skill:([^\s"'`<>]+)(?:(\r\n|[ \t\n\r\f\v])([\s\S]*))?$/;
const SKILL_NAME = /^[^\s"'`<>]+$/;
const COMPLETED_COMMAND = /^(\/[^\s]+)(\r\n|[ \t\n\r\f\v])([\s\S]*)$/;

/**
 * Drafts use the currently available skills. Passing an empty list while that
 * scope loads keeps typed text intact; a bare name stays a menu query until a
 * completion or an explicit separator selects it.
 */
export function readComposerToken(
  line: string,
  skills: readonly AvailableSkill[],
  commandNames: readonly string[] = ["/compact"],
): ComposerToken | null {
  const skill = SKILL_INVOCATION.exec(line);
  const selected = skill && availableSkill(skills, skill[1]);
  if (skill?.[2] && selected) {
    return { kind: "skill", name: selected.name, body: skill[3] ?? "", prefix: `/skill:${selected.name} ` };
  }
  const command = COMPLETED_COMMAND.exec(line);
  // Commands in the composer have no task arguments. Keeping a command with
  // ordinary text un-tokenized preserves the existing command validation.
  if (command && commandNames.includes(command[1]) && !command[3].trim()) {
    return { kind: "command", name: command[1], body: command[3], prefix: `${command[1]} ` };
  }
  return null;
}

/** Only a completed, exact known skill may gain the server's ASCII separator. */
export function normalizeComposerTokenInput(line: string, skills: readonly AvailableSkill[]): string {
  const invocation = SKILL_INVOCATION.exec(line);
  const selected = invocation && availableSkill(skills, invocation[1]);
  if (!invocation?.[2] || !selected) return line;
  return `/skill:${selected.name} ${invocation[3] ?? ""}`;
}

/** Native textarea editing changes only the body; the canonical draft is text. */
export function replaceComposerToken(token: ComposerToken, body: string): string {
  return token.prefix + body;
}

/**
 * parseSkillBlock establishes the server envelope. Read its trailing request
 * again without trim(), so copy/edit/rewind retain picks and whitespace rather
 * than restoring hundreds of lines of model-facing instructions.
 */
function expandedSkillMessage(text: string): SkillBlock | null {
  const block = parseSkillBlock(text);
  if (!block || !SKILL_NAME.test(block.name)) return null;
  const envelope = /^<skill name="[^"]*" location="[^"]*">\n[\s\S]*?\n<\/skill>(?:\n\n([\s\S]*))?$/.exec(text.trimStart());
  if (envelope) return { ...block, trailing: envelope[1] ?? "" };
  // A legacy block can have trailing formatting whitespace without the double
  // newline delimiter. It is still recognized by the existing timeline parser.
  return block;
}

/** Restore a persisted skill expansion to the invocation the user can edit. */
export function canonicalUserSkillText(text: string): string {
  const block = expandedSkillMessage(text);
  if (!block) return text;
  return `/skill:${block.name} ${block.trailing}`;
}

/**
 * History is independent of today's skill scope: a saved invocation remains a
 * skill message after switching Agents. Raw optimistic messages have no skill
 * document yet, so only their name and user-written trailing request display.
 */
export function skillMessageBlock(text: string): SkillBlock | null {
  const expanded = expandedSkillMessage(text);
  if (expanded) return expanded;
  const raw = SKILL_INVOCATION.exec(text);
  if (!raw) return null;
  return { name: raw[1], location: "", body: "", trailing: raw[3] ?? "" };
}
