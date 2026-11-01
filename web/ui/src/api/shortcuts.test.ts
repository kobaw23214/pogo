import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.mock("./client", () => ({ authHeaders: () => ({ Authorization: "Bearer isolated-test" }) }));
import { shortcutsApi, shortcutEntries, resolveStarterShortcut, saveHomepageStarters, ShortcutsAPIError, type ShortcutsView } from "./shortcuts";
const fetchMock = vi.fn<typeof fetch>();
const view = (agent_id = "agent-a"): ShortcutsView => ({ scope_id: "scope-a", agent_id, revision: 4, shortcuts: [{ id: "one", title: "背景", content: "资料正文", icon: "" }] });
beforeEach(() => { vi.stubGlobal("fetch", fetchMock); fetchMock.mockReset(); });
afterEach(() => vi.unstubAllGlobals());
describe("Agent reference shortcut API", () => {
  it("projects homepage prompts separately without writing duplicates or treating panel actions as text", async () => {
    const projected = { ...view(), starter_shortcuts: [{ id: "starter_a", title: "首页流程", content: "先分析", parts: [{ type: "text" as const, text: "先分析" }, { type: "skill" as const, name: "office" }] }], starter_actions: [{ title: "系统", panel: "system", at: "#/list" }], can_manage_starters: true };
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify(projected), { status: 200 }));
    const result = await shortcutsApi.list("agent-a");
    expect(shortcutEntries(result)).toEqual([view().shortcuts[0], { ...projected.starter_shortcuts[0], source: "starter" }]);
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify(projected), { status: 200 }));
    await shortcutsApi.save("agent-a", result);
    expect(JSON.parse(fetchMock.mock.calls[1][1]!.body as string).shortcuts).toEqual(view().shortcuts);
    expect(JSON.parse(fetchMock.mock.calls[1][1]!.body as string)).not.toHaveProperty("starter_shortcuts");
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ ...projected, starter_actions: [{ title: "unsafe", panel: "system", prompt: "execute" }] }), { status: 200 }));
    await expect(shortcutsApi.list("agent-a")).rejects.toThrow("Invalid shortcut response");
  });
  it("materializes only a content-addressed current source and freezes resolved parts for private nesting", async () => {
    const item = { id: "starter_hash", title: "流程", content: "原内容", source: "starter" as const, parts: [{ type: "file" as const, path: "starter-assets/source/data.csv", as_file: true }] };
    const parts = [{ type: "file" as const, path: "uploads/resolved/data.csv", as_file: true }];
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ parts, attachments: [] }), { status: 200 }));
    const result = await resolveStarterShortcut("session / one", item);
    expect(fetchMock.mock.calls[0][0]).toBe("/api/sessions/session%20%2F%20one/shortcuts/starters/starter_hash/resolve");
    expect(fetchMock.mock.calls[0][1]).not.toHaveProperty("body");
    expect(result).toMatchObject({ id: item.id, title: item.title, content: item.content, parts });
    expect(result).not.toHaveProperty("source");
    expect(item.parts[0].path).toBe("starter-assets/source/data.csv");
  });
  it("sends the effective homepage version when managing a global source", async () => {
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ ok: true, cards: 0 }), { status: 200 }));
    await saveHomepageStarters({ cards: [] }, "effective-source-hash");
    expect(JSON.parse(fetchMock.mock.calls[0][1]!.body as string)).toEqual({ cards: [], expected_revision: "effective-source-hash" });
  });
  it("scopes reads to the exact encoded Agent and supports account scope", async () => {
    const signal = new AbortController().signal;
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify(view("agent & one")), { status: 200 }));
    await shortcutsApi.list("agent & one", signal);
    expect(fetchMock).toHaveBeenCalledWith("/api/shortcuts?agent_id=agent+%26+one", expect.objectContaining({ method: "GET", credentials: "same-origin", cache: "no-store", signal }));
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify(view("")), { status: 200 })); await shortcutsApi.list();
    expect(fetchMock.mock.calls[1][0]).toBe("/api/shortcuts");
  });
  it("writes scope and revision with the entire snapshot, without Agent identity in the body", async () => {
    const value = view(); fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ ...value, revision: 5 }), { status: 200 }));
    expect((await shortcutsApi.save("agent-a", value)).revision).toBe(5);
    expect(JSON.parse(fetchMock.mock.calls[0][1]!.body as string)).toEqual({ scope_id: "scope-a", revision: 4, shortcuts: value.shortcuts });
    expect(fetchMock.mock.calls[0][1]!.headers).toMatchObject({ Authorization: "Bearer isolated-test", "Content-Type": "application/json" });
  });
  it("saves folders and item parents in the same private CAS snapshot without system projections", async () => {
    const value: ShortcutsView = { ...view(), folders: [{ id: "parent", name: "私有目录" }, { id: "child", name: "子目录", parent_id: "parent" }], shortcuts: [{ ...view().shortcuts[0], parent_id: "child" }], starter_shortcuts: [{ id: "starter_source", title: "只读来源", content: "source" }] };
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify(value), { status: 200 }));
    await expect(shortcutsApi.list("agent-a")).resolves.toEqual(value);
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ ...value, revision: 5 }), { status: 200 }));
    await shortcutsApi.save("agent-a", value);
    expect(JSON.parse(fetchMock.mock.calls[1][1]!.body as string)).toEqual({ scope_id: value.scope_id, revision: 4, folders: value.folders, shortcuts: value.shortcuts });
  });
  it("omits folders for legacy saves but sends an explicit empty array when clearing a valid tree", async () => {
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify(view()), { status: 200 }));
    await shortcutsApi.save("agent-a", view());
    expect(JSON.parse(fetchMock.mock.calls[0][1]!.body as string)).not.toHaveProperty("folders");
    const empty = { ...view(), folders: [] };
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify(empty), { status: 200 }));
    await shortcutsApi.save("agent-a", empty);
    expect(JSON.parse(fetchMock.mock.calls[1][1]!.body as string).folders).toEqual([]);
  });
  it("retains the latest complete private tree in a CAS conflict for explicit retry", async () => {
    const current = { ...view(), folders: [{ id: "private", name: "Current tree" }], shortcuts: [{ ...view().shortcuts[0], parent_id: "private" }] };
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ error: "Changed", code: "SHORTCUT_REVISION_CONFLICT", current }), { status: 409 }));
    const error = await shortcutsApi.save("agent-a", view()).catch(error => error);
    expect(error).toMatchObject({ code: "SHORTCUT_REVISION_CONFLICT", current });
  });
  it("rejects malformed private hierarchies without treating system directories as private parents", async () => {
    const malformed = [
      { ...view(), folders: null },
      { ...view(), folders: [{ id: "@information", name: "System" }] },
      { ...view(), folders: [{ id: "private", name: "Private", parent_id: "@quick-cards" }] },
      { ...view(), folders: [{ id: "one", name: "Item ID collision" }] },
      { ...view(), folders: [{ id: "a", name: "A" }, { id: "a", name: "Duplicate" }] },
      { ...view(), folders: [{ id: "a", name: "A", parent_id: "b" }, { id: "b", name: "B", parent_id: "a" }] },
      { ...view(), folders: [{ id: "a", name: "A", parent_id: "missing" }] },
      { ...view(), folders: [], shortcuts: [{ ...view().shortcuts[0], parent_id: "missing" }] },
      { ...view(), shortcuts: [{ ...view().shortcuts[0], parent_id: "private" }] },
    ];
    for (const value of malformed) {
      fetchMock.mockResolvedValueOnce(new Response(JSON.stringify(value), { status: 200 }));
      await expect(shortcutsApi.list("agent-a")).rejects.toThrow("Invalid shortcut response");
    }
  });
  it("preserves structured conflict data for an explicit user retry", async () => {
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ error: "Changed", code: "SHORTCUT_REVISION_CONFLICT", current: view() }), { status: 409 }));
    const error = await shortcutsApi.save("agent-a", view()).catch(value => value);
    expect(error).toBeInstanceOf(ShortcutsAPIError); expect(error).toMatchObject({ status: 409, code: "SHORTCUT_REVISION_CONFLICT", current: view() });
  });
  it("rejects mismatched scopes, invalid response shapes and malformed errors", async () => {
    for (const value of [view("different-agent"), { ...view(), revision: -1 }, { ...view(), shortcuts: [{ title: "无正文" }] }, { ...view(), shortcuts: [{ id: "unsafe", title: "Title", content: "DATA", language: "<script>" }] }]) {
      fetchMock.mockResolvedValueOnce(new Response(JSON.stringify(value), { status: 200 }));
      await expect(shortcutsApi.list("agent-a")).rejects.toThrow("Invalid shortcut response");
    }
    fetchMock.mockResolvedValueOnce(new Response("unstructured", { status: 503, statusText: "Unavailable" }));
    await expect(shortcutsApi.list("agent-a")).rejects.toMatchObject({ status: 503, message: "Unavailable", current: undefined });
  });
});
