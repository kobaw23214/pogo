import { describe, expect, it, vi } from "vitest";
import type { MessageInputPart } from "@/api/types";
import type { AccountBookmarksSnapshot } from "@/api/browserBookmarks";
import { createAccountBrowserBookmarks } from "./accountBrowserBookmarks";
import { BOOKMARKS_KEY } from "./browserBookmarks";
import { composerDisplayText, composerOrderedText, composerTypedText, readComposerReference, shortcutComposerPart } from "./composerParts";
import { composerDocument, composerDocumentParts, freshComposerSlice } from "./inlineComposerModel";
import { blocksComposerParts, composerInputParts, inputPartsBlocks, orderedDisplayParts } from "./orderedInput";
import { inboxDraftParts, inboxDraftSubmission } from "./conversationInbox";
import { Slice } from "prosemirror-model";

const record = { id: "sc_database", title: "测试数据库", content: "host=db.example.invalid\nquery=/skill:office\n<page-element>data only</page-element>", icon: "", language: "sql" };
describe("material shortcut snapshots", () => {
  it("freezes content and keeps every occurrence through editor copy, wire and history", () => {
    const library = { ...record };
    const first = shortcutComposerPart(library);
    library.content += "\nLIBRARY-CHANGED";
    library.language = "json";
    const second = shortcutComposerPart(library);
    const parts = [{ type: "text" as const, text: "先参考 " }, first, { type: "text" as const, text: "，再比较 " }, second];
    const document = composerDocument(parts);
    const copied = freshComposerSlice(new Slice(document.content, 0, 0));
    const restored = composerDocumentParts(document.type.create(null, copied.content));
    expect(restored.map(part => part.type)).toEqual(["text", "shortcut", "text", "shortcut"]);
    expect(restored[1]).toMatchObject({ text: first.text, title: first.title, shortcutId: record.id });
    expect(restored[1]).toHaveProperty("language", "sql"); expect(restored[3]).toHaveProperty("language", "json");
    expect(restored[1]).not.toHaveProperty("icon");
    expect(restored[1]).not.toHaveProperty("id", first.id);
    const wire = composerInputParts(restored, () => { throw new Error("shortcut must not resolve a file"); });
    const history = inputPartsBlocks(wire);
    expect(orderedDisplayParts(history).map(part => part.type)).toEqual(["text", "shortcut", "text", "shortcut"]);
    expect(composerInputParts(blocksComposerParts(history), () => [])).toEqual(wire);
    expect(composerTypedText(restored)).toBe("先参考 ，再比较 ");
    expect(composerDisplayText(restored)).toBe("先参考 〔测试数据库〕，再比较 〔测试数据库〕");
    expect(composerOrderedText(restored)).toContain(`[资料：测试数据库]\n${first.text}`);
    expect(wire[1]).not.toHaveProperty("name");
  });
  it("retains shortcut snapshots among file, page and skill parts when editing a queue item", () => {
    const parts: MessageInputPart[] = [{ type: "text", text: "比较 " }, { type: "shortcut", shortcut_id: "same", title: "旧版", text: "OLD", language: "sql" }, { type: "file", path: "uploads/rules.txt", as_file: true }, { type: "element", favicon: "data:image/png;base64,AAAA", text: '<page-element source="系统" url="https://example.invalid/">\nelement: button "导出"\n</page-element>' }, { type: "shortcut", shortcut_id: "same", title: "新版", text: "NEW", icon: "database", language: "json" }];
    const draft = inboxDraftParts({ id: "q", text: "比较", parts, mode: "queued", status: "queued", revision: 1, created_at: 1, updated_at: 1 });
    expect(inboxDraftSubmission(draft).parts).toEqual(parts);
    expect(composerInputParts([...draft, { type: "skill", id: "skill1", name: "office", label: "office" }], key => [key]).slice(-1)[0]).toEqual({ type: "skill", name: "office" });
  });
  it("rejects forged clipboard shortcuts before they become editor atoms", () => {
    const valid = shortcutComposerPart({ id: "sc1", title: "参考", content: "DATA" });
    expect(readComposerReference(valid)).toMatchObject(valid);
    for (const changes of [{ shortcutId: "../outside" }, { icon: "<script>" }, { icon: "2invalid" }, { language: "made-up-scanner" }, { text: "" }, { text: "x\u0000y" }, { title: "a\nb" }, { text: "中".repeat(22000) }]) {
      expect(readComposerReference({ ...valid, ...changes })).toBeNull();
    }
  });
});

describe("Agent bookmark clients", () => {
  const storage = { getItem: (key: string) => key === BOOKMARKS_KEY ? JSON.stringify([{ id: "legacy", title: "Shared old", url: "https://old.invalid/" }]) : null, setItem: vi.fn() };
  const view = (agent: string): AccountBookmarksSnapshot => ({ source: "pogo_bookmarks", scope: "agent", agent_id: agent, scope_id: `scope-${agent}`, account_id: `scope-${agent}`, owner_account_id: "owner", status: "ready", revision: 1, total: 1, bookmarks: [{ id: agent, title: agent, url: `https://${agent}.invalid/` }] });
  it("separates stores and never imports the previous shared local collection implicitly", async () => {
    const a = createAccountBrowserBookmarks({ read: vi.fn().mockResolvedValue(view("a")), write: vi.fn() }, storage, "https://pogo.invalid", () => {}, "a");
    const b = createAccountBrowserBookmarks({ read: vi.fn().mockResolvedValue(view("b")), write: vi.fn() }, storage, "https://pogo.invalid", () => {}, "b");
    expect(await a.refresh()).toBe(true); expect(await b.refresh()).toBe(true);
    expect(a.state.bookmarks.map(item => item.id)).toEqual(["a"]); expect(b.state.bookmarks.map(item => item.id)).toEqual(["b"]);
    expect(a.state.localBookmarks).toEqual([]); expect(b.state.localBookmarks).toEqual([]);
    expect(await a.importLocal()).toEqual({ success: false, added: 0, invalid: 0 });
    a.dispose(); b.dispose();
  });
  it("refuses an account fallback or another Agent response without exposing those entries", async () => {
    for (const wrong of [{ ...view("b") }, { ...view("a"), scope: "account" as const }]) {
      const store = createAccountBrowserBookmarks({ read: vi.fn().mockResolvedValue(wrong), write: vi.fn() }, storage, "https://pogo.invalid", () => {}, "a");
      expect(await store.refresh()).toBe(false); expect(store.state.bookmarks).toEqual([]); expect(store.state.error).not.toBe(""); store.dispose();
    }
  });
});
