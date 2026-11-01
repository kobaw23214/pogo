import type { Block, InboxItem, MessageInputPart } from "@/api/types";
import { randomUUID } from "@/utils/uuid";
import { blocksComposerParts, inputPartsBlocks, composerInputParts } from "./orderedInput";
import { composerOrderedText, type ComposerPart } from "./composerParts";

export function inboxMessageBlocks(item: InboxItem): Block[] {
  return item.parts ? inputPartsBlocks(item.parts) : [{ type: "text", text: item.text }];
}

/** Queue edits must retain both file placement and the sender's image mode. */
export function inboxDraftParts(item: InboxItem): ComposerPart[] {
  const parts = blocksComposerParts(inboxMessageBlocks(item));
  const files = item.parts?.filter((part) => part.type === "file") ?? [];
  let fileIndex = 0;
  return parts.map((part) => {
    if (part.type !== "file") return part;
    const file = files[fileIndex++];
    return { ...part, asFile: file?.as_file };
  });
}

export function inboxDraftSubmission(parts: readonly ComposerPart[]): { text: string; parts: MessageInputPart[] } {
  return {
    text: composerOrderedText(parts, () => ""),
    parts: composerInputParts(parts, key => {
      const part = parts.find(value => value.type === "file" && value.key === key);
      if (!part || part.type !== "file" || !part.path || part.status === "uploading" || part.status === "error") throw new Error("File is not ready");
      return [part.path];
    }),
  };
}


/** HTTP acknowledgements and SSE updates can arrive in either order. */
export function mergeInboxItem(items: readonly InboxItem[], incoming: InboxItem): InboxItem[] {
  const existing = items.find(item => item.id === incoming.id);
  if (existing && existing.revision >= incoming.revision) return [...items];
  return [...items.filter(item => item.id !== incoming.id), incoming]
    .sort((a, b) => a.created_at - b.created_at || a.id.localeCompare(b.id));
}

export function pendingInboxItems(items: readonly InboxItem[]): InboxItem[] {
  return items.filter(item => item.status === "queued" || item.status === "steering");
}

export function inboxId(): string {
  return `inbox-${randomUUID()}`;
}

/** Local transport receipts survive chat switches, without mixing their drafts. */
export function createInboxDraftLedger(nextId: () => string = inboxId) {
  const sends = new Map<string, string>();
  const withdrawals = new Map<string, InboxItem>();
  const restoredMessages = (session: string, items: readonly InboxItem[]): InboxItem[] => {
    const restored: InboxItem[] = [];
    for (const item of items) {
      const k = key(session, item.id), pending = withdrawals.get(k);
      if (!pending) continue;
      if (item.status === "withdrawn") { restored.push(pending); withdrawals.delete(k); }
      else if (item.status === "delivered") withdrawals.delete(k);
    }
    return restored;
  };
  const key = (session: string, value: string) => JSON.stringify([session, value]);
  return {
    submission(session: string, signature: string): string {
      const k = key(session, signature);
      let id = sends.get(k);
      if (!id) { id = nextId(); sends.set(k, id); }
      return id;
    },
    acknowledge(session: string, signature: string, id: string): void {
      const k = key(session, signature);
      if (sends.get(k) === id) sends.delete(k);
    },
    withdrawing(session: string, item: InboxItem): void {
      withdrawals.set(key(session, item.id), { ...item, ...(item.parts ? { parts: JSON.parse(JSON.stringify(item.parts)) } : {}) });
    },
    restoredMessages,
    restoredDrafts(session: string, items: readonly InboxItem[]): string[] {
      return restoredMessages(session, items).map((item) => item.text);
    },
  };
}
