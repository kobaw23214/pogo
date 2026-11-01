import { describe, expect, it } from "vitest";
import { isSavedShortcutDetails } from "./shortcutReceipt";

const receipt = () => ({ type: "shortcut", action: "create", saved: true, agent_id: "ag_test", request_id: "request-one", revision: 1, shortcut: { id: "sh_root", title: "流程", content: "摘要", parts: [{ type: "text" as const, text: "开始" }, { type: "shortcut" as const, shortcut_id: "sh_child", title: "子卡", text: "子内容" }] } });
describe("saved shortcut host receipts", () => {
  it("renders a valid ordered nested receipt and an idempotent reuse", () => {
    expect(isSavedShortcutDetails(receipt())).toBe(true);
    expect(isSavedShortcutDetails({ ...receipt(), reused: true })).toBe(true);
  });
  it.each([{ saved: false }, { type: "text" }, { action: "read" }, { shortcut: null }, { agent_id: "" }, { revision: 0 }, { request_id: "" }])("rejects non-persisted or malformed metadata %j", patch => {
    expect(isSavedShortcutDetails({ ...receipt(), ...patch })).toBe(false);
  });
  it("rejects forged paths and recursive invalid content before opening a preview", () => {
    const value = receipt();
    Object.assign(value.shortcut, { parts: [{ type: "file", path: "../other-owner.txt" }] });
    expect(isSavedShortcutDetails(value)).toBe(false);
  });
});

import { isShortcutMutationDetails, isShortcutMergePreview } from "./shortcutReceipt";
import type { ShortcutChange, ShortcutDetails, ShortcutMutationAction } from "@/api/types";
const change = (action: ShortcutChange["action"] = "move", kind: ShortcutChange["kind"] = "shortcut", id = "sh_one"): ShortcutChange => ({ action, kind, id, title: "合成资料", from_parent_id: "folder_old", to_parent_id: "folder_new" });
const mutation = (action: ShortcutMutationAction = "move"): ShortcutDetails => ({
  type: "shortcut", action, saved: true, changed: true, agent_id: "ag_test", request_id: "mutation_1", revision: 8, applied_revision: 8,
  changes: [change(action === "merge" ? "delete" : action, action.startsWith("folder_") ? "folder" : "shortcut")],
  ...(action === "update" ? { shortcut: { ...receipt().shortcut, id: "sh_one" } } : {}),
  ...(action.startsWith("folder_") ? { folder: { id: "sh_one", name: "合成资料", parent_id: "folder_new" } } : {}),
});
const preview = (): ShortcutDetails => ({ type: "shortcut", action: "merge", saved: false, changed: false, agent_id: "ag_test", revision: 7, changes: [], plan: { id: "sm_plan", revision: 7, groups: [{ keep_id: "sh_one", duplicate_ids: ["sh_two", "sh_three"], title: "合成资料", parent_id: "folder_old" }], removals: 2 } });

describe("shortcut organization host receipts", () => {
  it.each(["folder_create", "update", "folder_rename", "move", "delete", "merge"] as const)("accepts the exact %s host action, never a different tool", action => {
    expect(isShortcutMutationDetails(mutation(action), `shortcut_${action}`, "ag_test")).toBe(true);
    expect(isShortcutMutationDetails(mutation(action), "shortcut_create", "ag_test")).toBe(false);
    expect(isShortcutMutationDetails(mutation(action), undefined, "ag_other")).toBe(false);
  });
  it("accepts saved no-ops and idempotent receipts without claiming another mutation", () => {
    expect(isShortcutMutationDetails({ ...mutation(), changed: false, changes: [] })).toBe(true);
    expect(isShortcutMutationDetails({ ...mutation(), revision: 12, applied_revision: 8, changed: false, reused: true, changes: [] })).toBe(true);
    expect(isShortcutMutationDetails({ ...mutation(), revision: 12, applied_revision: 8, reused: true })).toBe(false);
  });
  it("acknowledges a replay whose old target was later deleted without fabricating a snapshot", () => {
    for (const action of ["update", "folder_create", "folder_rename"] as const) {
      const value = { ...mutation(action), reused: true, changed: false, changes: [], shortcut: undefined, folder: undefined };
      expect(isShortcutMutationDetails(value)).toBe(true); expect(isSavedShortcutDetails(value)).toBe(false);
    }
  });
  it("accepts recursive folder deletion and mixed folder/card moves as metadata only", () => {
    for (const action of ["delete", "move"] as const) expect(isShortcutMutationDetails({ ...mutation(action), changes: [change(action), change(action, "folder", "folder_one")] })).toBe(true);
    expect(isSavedShortcutDetails(mutation("delete"))).toBe(false);
  });
  it("opens only a complete validated update snapshot and preserves nested order", () => {
    const value = mutation("update"), before = JSON.stringify(value);
    expect(isSavedShortcutDetails(value)).toBe(true); expect(JSON.stringify(value)).toBe(before);
    expect(isSavedShortcutDetails({ ...value, shortcut: { ...value.shortcut, parts: [{ type: "file", path: "../foreign.txt" }] } })).toBe(false);
    expect(isSavedShortcutDetails({ ...value, shortcut: { ...value.shortcut, id: "other" } })).toBe(false);
    expect(isSavedShortcutDetails({ ...value, saved: false })).toBe(false);
  });
  it.each([
    { agent_id: "../foreign" }, { revision: -1 }, { revision: Number.MAX_SAFE_INTEGER + 1 }, { applied_revision: 0 }, { applied_revision: 9 },
    { request_id: "" }, { request_id: "../../foreign" }, { reused: "yes" }, { changed: undefined }, { changed: "true" }, { saved: false },
    { changes: null }, { changes: [] }, { changes: [{ ...change(), action: "delete" }] }, { changes: [{ ...change(), id: "@information/file" }] },
    { changes: [{ ...change(), title: "" }] }, { changes: [{ ...change(), to_parent_id: "@quick-cards" }] }, { changes: [{ ...change(), from_parent_id: "../foreign" }] },
    { changes: [{ ...change(), kind: "file" }] }, { changes: [change(), change()] }, { shortcut: receipt().shortcut }, { folder: { id: "f_one", name: "unexpected" } },
  ])("rejects malformed or misleading mutation metadata %j", patch => expect(isShortcutMutationDetails({ ...mutation(), ...patch })).toBe(false));
  it("rejects preset folders and mismatched folder metadata", () => {
    expect(isShortcutMutationDetails({ ...mutation("folder_create"), folder: { id: "@information", name: "不可变" } })).toBe(false);
    expect(isShortcutMutationDetails({ ...mutation("folder_rename"), folder: { id: "sh_one", name: "不可变", read_only: true } })).toBe(false);
    expect(isShortcutMutationDetails({ ...mutation("folder_create"), changes: [change("folder_create", "shortcut")] })).toBe(false);
  });
});
describe("merge preview is not a saved operation", () => {
  it("accepts a host preview including an empty library but never shows it as saved", () => {
    expect(isShortcutMergePreview(preview(), "ag_test")).toBe(true); expect(isShortcutMutationDetails(preview(), "shortcut_merge")).toBe(true);
    expect(isSavedShortcutDetails(preview())).toBe(false);
    expect(isShortcutMergePreview({ ...preview(), revision: 0, plan: { id: "sm_empty", revision: 0, groups: [], removals: 0 } })).toBe(true);
    expect(isShortcutMergePreview(preview(), "ag_other")).toBe(false);
  });
  it.each([{ saved: true }, { changed: true }, { changes: [change("delete")] }, { reused: true }, { applied_revision: 1 }, { action: "move" }, { revision: 8 }, { plan: null }, { request_id: "not-a-preview-request" }])("does not confuse preview and apply %j", patch => expect(isShortcutMutationDetails({ ...preview(), ...patch })).toBe(false));
  it.each([
    { id: "../../plan" }, { removals: 1 }, { revision: 8 }, { groups: [{ keep_id: "sh_one", duplicate_ids: ["sh_one"], title: "重复" }] },
    { groups: [{ keep_id: "sh_one", duplicate_ids: [], title: "重复" }] }, { groups: [{ keep_id: "sh_one", duplicate_ids: ["sh_two"], title: "重复", parent_id: "@information" }] },
    { groups: [{ keep_id: "sh_one", duplicate_ids: ["sh_two"], title: "重复" }, { keep_id: "sh_three", duplicate_ids: ["sh_two"], title: "重复" }] },
  ])("rejects an inconsistent or unsafe merge plan %j", patch => expect(isShortcutMergePreview({ ...preview(), plan: { ...preview().plan, ...patch } })).toBe(false));
  it("allows an apply plan only at the exact prior library revision", () => {
    expect(isShortcutMutationDetails({ ...mutation("merge"), plan: preview().plan })).toBe(true);
    expect(isShortcutMutationDetails({ ...mutation("merge"), plan: { ...preview().plan, revision: 8 } })).toBe(false);
  });
});
