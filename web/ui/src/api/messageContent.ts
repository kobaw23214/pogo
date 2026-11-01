import type { Block, Message } from "./types";

const record = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === "object" && !Array.isArray(value);

function normalizeBlock(value: unknown): Block | null {
  if (!record(value)) return null;
  switch (value.type) {
    case "text":
    case "thinking":
      if (typeof value.text === "string") return value as Block;
      return value.text == null ? { ...value, text: "" } as Block : null;
    case "tool_use":
      // Arguments are opaque here: malformed JSON is shown by the tool card,
      // never reparsed or turned into a different executable call.
      return typeof value.id === "string" && typeof value.name === "string" ? value as Block : null;
    case "tool_result":
      return typeof value.tool_use_id === "string" && (value.text == null || typeof value.text === "string") ? value as Block : null;
    case "image":
      return typeof value.image_ref === "string" ? value as Block : null;
    case "file":
      return typeof value.file_path === "string" ? value as Block : null;
    default:
      return null;
  }
}

/** Keep the UI's array contract at every wire/history boundary. Older servers
 * can send null for an empty/aborted message; imported history may contain a
 * string or a single block. None of those should break the whole conversation.
 * This only adapts the display copy, never the stored transcript. */
export function normalizeMessageContent(value: unknown, fallbackText?: unknown): Block[] {
  if (typeof value === "string") return [{ type: "text", text: value }];
  if (!Array.isArray(value)) {
    const block = normalizeBlock(value);
    if (block) return [block];
    return typeof fallbackText === "string" ? [{ type: "text", text: fallbackText }] : [];
  }
  let blocks: Block[] | undefined;
  for (let i = 0; i < value.length; i++) {
    const entry = value[i];
    const block = normalizeBlock(entry);
    if (block !== entry || block === null) blocks ??= value.slice(0, i) as Block[];
    if (blocks && block) blocks.push(block);
  }
  // Keep settled objects stable for the context estimator's identity cache.
  return blocks ?? value as Block[];
}

export function normalizeMessages(value: unknown): Message[] {
  if (!Array.isArray(value)) return [];
  let messages: Message[] | undefined;
  for (let i = 0; i < value.length; i++) {
    const entry = value[i];
    if (!record(entry) || typeof entry.id !== "string" ||
        entry.role != null && entry.role !== "" && entry.role !== "user" && entry.role !== "assistant") {
      messages ??= value.slice(0, i) as Message[];
      continue;
    }
    const content = normalizeMessageContent(entry.content, entry.text);
    const role = entry.role || "assistant";
    if (content !== entry.content || role !== entry.role) {
      messages ??= value.slice(0, i) as Message[];
      messages.push({ ...entry, role, content } as Message);
    } else if (messages) messages.push(entry as unknown as Message);
  }
  return messages ?? value as Message[];
}
