import type { Block, InputLayout, MessageInputPart } from "@/api/types";
import { composerDisplayText } from "./composerParts";
import { skillMessageBlock } from "./composerTokens";
import { blocksComposerParts, composerInputParts, inputPartsBlocks } from "./orderedInput";
import { cloneInputParts, inputPartsText } from "./shortcutStructure";

export interface ConversationPlanDraft {
  key: string;
  title: string;
  prompt: string;
  parts: MessageInputPart[];
  input_layout: InputLayout;
  source_session_id: string;
  agent_id: string;
}

export interface ScheduleMessageSource {
  id: string;
  text: string;
  images?: string[];
  files?: { path: string; name: string }[];
  orderedContent?: Block[];
  inputLayout?: InputLayout;
}

/** A plan is a detached user request, without its past answer or tool results. */
export function conversationPlanDraft(parts: readonly MessageInputPart[], sessionId: string, agentId: string, layout: InputLayout, key: string): ConversationPlanDraft {
  const copied = cloneInputParts(parts);
  const display = composerDisplayText(blocksComposerParts(inputPartsBlocks(copied))).replace(/\s+/g, " ").trim();
  return {
    key,
    title: Array.from(display).slice(0, 48).join(""),
    prompt: inputPartsText(copied),
    parts: copied,
    input_layout: layout,
    source_session_id: sessionId,
    agent_id: agentId,
  };
}

export function messagePlanDraft(message: ScheduleMessageSource, sessionId: string, agentId: string, key: string): ConversationPlanDraft {
  let parts: MessageInputPart[];
  if (message.orderedContent) {
    parts = composerInputParts(blocksComposerParts(message.orderedContent), path => [path]);
  } else {
    const invocation = skillMessageBlock(message.text);
    const text = invocation ? invocation.trailing : message.text;
    const blocks: Block[] = [
      ...(message.images ?? []).map(path => ({ type: "image" as const, image_ref: path })),
      ...(message.files ?? []).map(file => ({ type: "file" as const, file_path: file.path, file_name: file.name })),
      ...(invocation ? [{ type: "text" as const, text: `/skill:${invocation.name} `, input_skill: invocation.name }] : []),
      { type: "text", text },
    ];
    parts = composerInputParts(blocksComposerParts(blocks), path => [path]);
  }
  return conversationPlanDraft(parts, sessionId, agentId, message.inputLayout ?? "top", key);
}
