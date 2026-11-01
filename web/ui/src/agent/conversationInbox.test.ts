import { describe, expect, it } from "vitest";
import { createInboxDraftLedger, inboxDraftParts, inboxDraftSubmission, inboxMessageBlocks, mergeInboxItem, pendingInboxItems } from "./conversationInbox";
import type { InboxItem, MessageInputPart } from "@/api/types";

const item = (overrides: Partial<InboxItem> = {}): InboxItem => ({ id: "inbox-a", text: "补充", mode: "queued", status: "queued", revision: 1, created_at: 100, updated_at: 100, ...overrides });
describe("durable conversation inbox projection", () => {
  it("reuses an unacknowledged submission across chat switches without reusing confirmed sends", () => {
    let next = 0; const ledger = createInboxDraftLedger(() => `id-${++next}`);
    const a = ledger.submission("A", "request");
    expect(ledger.submission("B", "request")).not.toBe(a);
    ledger.submission("A", "another request");
    expect(ledger.submission("A", "request")).toBe(a);
    ledger.acknowledge("A", "request", a);
    expect(ledger.submission("A", "request")).not.toBe(a);
  });
  it("restores a withdrawn draft once from SSE or recovery even when its HTTP ack is lost", () => {
    const ledger = createInboxDraftLedger(); ledger.withdrawing("A", item());
    const receipt = item({ status: "withdrawn", text: "", revision: 2 });
    expect(ledger.restoredDrafts("B", [receipt])).toEqual([]);
    expect(ledger.restoredDrafts("A", [receipt])).toEqual(["补充"]);
    expect(ledger.restoredDrafts("A", [receipt])).toEqual([]);
  });
  it("does not restore a draft if delivery won the withdrawal race", () => {
    const ledger = createInboxDraftLedger(); ledger.withdrawing("A", item());
    expect(ledger.restoredDrafts("A", [item({ status: "delivered", revision: 3 })])).toEqual([]);
  });
  it("does not let a delayed HTTP receipt overwrite a delivered SSE update", () => {
    const delivered = item({ revision: 3, status: "delivered", message_id: "u2" });
    expect(mergeInboxItem([delivered], item())).toEqual([delivered]);
    expect(pendingInboxItems(mergeInboxItem([delivered], item()))).toEqual([]);
  });
  it("merges edits once without changing submission order", () => {
    const first = item(); const second = item({ id: "inbox-b", created_at: 200 });
    const edited = item({ text: "改后的要求", revision: 2 });
    expect(mergeInboxItem([first, second], edited)).toEqual([edited, second]);
    expect(mergeInboxItem([edited, second], edited)).toEqual([edited, second]);
  });
  it("retains waiting messages and removes withdrawn entries from pending UI", () => {
    const queued = item(); const steering = item({ id: "b", status: "steering" });
    expect(pendingInboxItems([queued, steering, item({ id: "c", status: "withdrawn" })])).toEqual([queued, steering]);
  });
  it("restores the original ordered snapshot once, including file mode and layout", () => {
    const ledger = createInboxDraftLedger();
    const parts: MessageInputPart[] = [{ type: "text", text: "先看" }, { type: "file", path: "uploads/清单.png", as_file: true }, { type: "text", text: "再操作" }];
    const original = item({ parts, input_layout: "inline" });
    ledger.withdrawing("A", original);
    // HTTP/SSE receipts may omit the withdrawn payload. Mutation of an old
    // reactive object must not mutate the snapshot waiting for its receipt.
    parts[0] = { type: "text", text: "被其他视图修改" };
    const restored = ledger.restoredMessages("A", [item({ status: "withdrawn", text: "", revision: 2 })]);
    expect(restored).toHaveLength(1);
    expect(restored[0]).toMatchObject({ input_layout: "inline", parts: [{ type: "text", text: "先看" }, { type: "file", path: "uploads/清单.png", as_file: true }, { type: "text", text: "再操作" }] });
    expect(ledger.restoredMessages("A", [item({ status: "withdrawn" })])).toEqual([]);
    expect(ledger.restoredDrafts("A", [item({ status: "withdrawn" })])).toEqual([]);
  });
  it("accepts a full ordered-content signature instead of conflating equal prose", () => {
    let next = 0;
    const ledger = createInboxDraftLedger(() => `id-${++next}`);
    const first = JSON.stringify({ text: "按文件修改", parts: [{ type: "file", path: "a.xlsx" }] });
    const second = JSON.stringify({ text: "按文件修改", parts: [{ type: "file", path: "b.xlsx" }] });
    const id = ledger.submission("A", first);
    expect(ledger.submission("A", second)).not.toBe(id);
    expect(ledger.submission("A", first)).toBe(id);
    ledger.acknowledge("A", first, id);
    expect(ledger.submission("A", first)).not.toBe(id);
  });
  it("edits ordered queue messages without extracting all references to the end", () => {
    const element = '<page-element source="系统" url="https://example.com">\nelement: button "导出"\n</page-element>';
    const parts: MessageInputPart[] = [
      { type: "text", text: "  点击" }, { type: "element", text: element },
      { type: "text", text: "，按" }, { type: "file", path: "uploads/要求.png", as_file: true },
      { type: "text", text: "处理，再点击" }, { type: "element", text: element }, { type: "text", text: "。\n" },
    ];
    const queued = item({ text: "legacy fallback", parts, input_layout: "inline" });
    const draft = inboxDraftParts(queued);
    expect(draft.map((part) => part.type)).toEqual(["text", "element", "text", "file", "text", "element", "text"]);
    const payload = inboxDraftSubmission(draft);
    expect(payload.parts).toEqual(parts);
    expect(payload.text).toBe(`  点击${element}，按处理，再点击${element}。\n`);
    const elementIDs = draft.filter((part) => part.type === "element").map((part) => part.id);
    expect(new Set(elementIDs).size).toBe(2);
    expect(inboxMessageBlocks(queued)[3]).toMatchObject({ type: "file", file_path: "uploads/要求.png" });
  });
  it("keeps a file-only queued message editable and rejects unresolved files", () => {
    const queued = item({ text: "", parts: [{ type: "file", path: "uploads/a.xlsx" }], input_layout: "top" });
    expect(inboxDraftSubmission(inboxDraftParts(queued))).toEqual({ text: "", parts: queued.parts });
    expect(() => inboxDraftSubmission([{ type: "file", id: "f", key: "pending", label: "未完成.xlsx", status: "uploading" }])).toThrow("File is not ready");
  });

  it("preserves automatic, forced-file and explicit-false image modes independently", () => {
    const parts: MessageInputPart[] = [
      { type: "file", path: "uploads/image.png" }, { type: "text", text: " then " },
      { type: "file", path: "uploads/image.png", as_file: true }, { type: "text", text: " then " },
      { type: "file", path: "uploads/image.png", as_file: false },
    ];
    const draft = inboxDraftParts(item({ parts, input_layout: "inline" }));
    expect(draft.filter((part) => part.type === "file").map((part) => part.asFile)).toEqual([undefined, true, false]);
    expect(inboxDraftSubmission(draft).parts).toEqual(parts);
  });

});
