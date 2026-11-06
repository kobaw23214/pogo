import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { getLocale, setLocale } from "@/i18n";
import { effectScope, nextTick, reactive, type Ref } from "vue";
import type { BuiltinToolEntry, SkillContent, SkillInfo, ToolCatalogEntry } from "@/api/types";
const host = vi.hoisted(() => ({ skills: vi.fn(), tools: vi.fn(), content: vi.fn(), authored: vi.fn(), cleanup: [] as (() => void)[] }));
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), onBeforeUnmount: (fn: () => void) => host.cleanup.push(fn), useSSRContext: () => ({ modules: new Set() }) }));
vi.mock("vue-i18n", async original => ({ ...await original<typeof import("vue-i18n")>(), useI18n: () => ({ t: (key: string) => key, te: () => true }) }));
vi.mock("@/api/client", () => ({ api: { skills: host.skills, tools: host.tools, skillContent: host.content, authoredSkills: host.authored }, skillSampleURL: () => "/fixture" }));
vi.mock("./McpMonitorPanel.vue", () => ({ default: {} })); vi.mock("./MarkdownView.vue", () => ({ default: {} })); vi.mock("./tools/ToolRow.vue", () => ({ default: {} })); vi.mock("./SkillExecutionNotice.vue", () => ({ default: {} }));
import SkillsPanel from "./SkillsPanel.vue";
const skill = (name = "office"): SkillInfo => ({ name, description: "处理文档", path: "/fixture/" + name + "/SKILL.md", dir: "/fixture/" + name, source: "embedded" });
const content = (name = "office", text = "技能正文"): SkillContent => ({ name, content: text, path: "/fixture/" + name, source: "embedded", truncated: false });
interface Controller { builtinTools: Ref<BuiltinToolEntry[]>; filteredBuiltinTools: Ref<BuiltinToolEntry[]>; filter: Ref<string>; skills: Ref<SkillInfo[] | null>; tools: Ref<ToolCatalogEntry[] | null>; selected: Ref<SkillInfo | null>; detail: Ref<SkillContent | null>; detailError: Ref<string>; detailLoading: Ref<boolean>; view: Ref<"skill" | "tool" | "calls">; authored: Ref<import("@/api/types").AuthoredSkillSummary[]>; authoredSelected: Ref<import("@/api/types").AuthoredSkillSummary | null>; authoredOpen: Ref<boolean>; filteredSkills: Ref<SkillInfo[]>; loadAuthored(): Promise<void>; openAuthored(skill: import("@/api/types").AuthoredSkillSummary): void; authoredChanged(event: Event): void; load(): Promise<void>; openSkill(skill: SkillInfo): Promise<void>; reload(): void; back(): void; }
const scopes: ReturnType<typeof effectScope>[] = [];
const pending = <T>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done; }); return { promise, resolve }; };
const flush = async () => { for (let i = 0; i < 8; i++) await Promise.resolve(); await nextTick(); };
function mount(extra: Record<string, unknown> = {}) { const props = reactive({ sessionId: "session-a", agentId: "agent-a", active: true, readOnly: true, embedded: false, toolRevision: 0, capabilityRevision: 0, toolsPending: false, ...extra }), scope = effectScope(); scopes.push(scope); const c = scope.run(() => (SkillsPanel as unknown as { setup(props: unknown, context: unknown): Controller }).setup(props, { emit: vi.fn(), expose() {} }))!; return { c, props }; }
beforeEach(() => { host.skills.mockReset().mockResolvedValue({ skills: [skill()] }); host.tools.mockReset().mockResolvedValue({ tools: [], mcp: [] }); host.content.mockReset().mockResolvedValue(content()); host.authored.mockReset().mockResolvedValue({ skills: [] }); });
afterEach(() => { for (const cleanup of host.cleanup.splice(0)) cleanup(); for (const scope of scopes.splice(0)) scope.stop(); });
describe("Skills introduction and MCP history lifetime", () => {
  it("reorders skill names on a language switch without refetching capabilities", async () => {
    const previous = getLocale();
    try {
      setLocale("zh-CN");
      host.skills.mockResolvedValueOnce({ skills: [skill("東京"), skill("大阪"), skill("京都")] });
      const { c } = mount(); await flush();
      expect(c.filteredSkills.value.map(value => value.name)).toEqual(["大阪", "東京", "京都"]);
      setLocale("ja"); await nextTick();
      expect(c.filteredSkills.value.map(value => value.name)).toEqual(["京都", "大阪", "東京"]);
      expect(host.skills).toHaveBeenCalledTimes(1);
      expect(host.tools).toHaveBeenCalledTimes(1);
    } finally { setLocale(previous); }
  });

  it("loads the Agent session catalog but never substitutes a global catalog without a session", async () => { const { c } = mount({ sessionId: "" }); await flush(); await c.load(); await c.openSkill(skill()); expect(host.skills).not.toHaveBeenCalled(); expect(host.content).not.toHaveBeenCalled(); expect(c.detail.value).toBeNull(); });
  it("does not fetch while hidden, then loads the exact visible session", async () => { const { props } = mount({ active: false }); await flush(); expect(host.skills).not.toHaveBeenCalled(); props.active = true; await flush(); expect(host.skills).toHaveBeenCalledWith("session-a"); expect(host.tools).toHaveBeenCalledWith("session-a"); });
  it("drops late catalogs when the Agent changes", async () => { const result = pending<{ skills: SkillInfo[] }>(); host.skills.mockReturnValueOnce(result.promise); const { c, props } = mount(); host.skills.mockResolvedValueOnce({ skills: [skill("browser-use")] }); props.agentId = "agent-b"; props.sessionId = "session-b"; await flush(); result.resolve({ skills: [skill("old-private")] }); await flush(); expect(c.skills.value?.map(row => row.name)).toEqual(["office"]); expect(host.skills).toHaveBeenLastCalledWith("session-b"); });
  it("cannot cache or display an old Agent skill content after the scope changes", async () => { const result = pending<SkillContent>(); host.content.mockReturnValueOnce(result.promise); const { c, props } = mount(); await flush(); const request = c.openSkill(skill()); props.agentId = "agent-b"; props.sessionId = "session-b"; await flush(); result.resolve(content("office", "old-private-body")); await request; expect(c.selected.value).toBeNull(); expect(c.detail.value).toBeNull(); host.content.mockResolvedValueOnce(content("office", "new-body")); await c.openSkill(skill()); expect(c.detail.value?.content).toBe("new-body"); expect(host.content).toHaveBeenLastCalledWith("office", "session-b"); });
  it("retains cached skill descriptions within the same scope and resets the viewer on reload", async () => { const { c } = mount(); await flush(); await c.openSkill(skill()); c.back(); await c.openSkill(skill()); expect(host.content).toHaveBeenCalledTimes(1); c.reload(); await flush(); expect(c.selected.value).toBeNull(); expect(c.detailLoading.value).toBe(false); await c.openSkill(skill()); expect(host.content).toHaveBeenCalledTimes(2); });
  it("retains the calls tab across scope updates while clearing catalog and detail data", async () => { const { c, props } = mount(); await flush(); c.view.value = "calls"; props.active = false; await flush(); expect(c.view.value).toBe("calls"); expect(c.skills.value).toBeNull(); expect(c.detail.value).toBeNull(); });
  it("does not display late content on unmount", async () => { const result = pending<SkillContent>(); host.content.mockReturnValueOnce(result.promise); const { c } = mount(); await flush(); const request = c.openSkill(skill()); for (const cleanup of host.cleanup.splice(0)) cleanup(); result.resolve(content("office", "late")); await request; expect(c.detail.value).toBeNull(); });
});

describe("committed Skill and MCP changes", () => {
  it("removes revoked Skill details and cached content without changing the user's lens or filter", async () => {
    const { c, props } = mount(); await flush(); await c.openSkill(skill()); c.view.value = "tool"; c.filter.value = "office";
    host.skills.mockResolvedValueOnce({ skills: [] }); props.capabilityRevision++; await flush();
    expect(c.skills.value).toEqual([]); expect(c.selected.value).toBeNull(); expect(c.detail.value).toBeNull();
    expect(c.view.value).toBe("tool"); expect(c.filter.value).toBe("office");
    host.skills.mockResolvedValueOnce({ skills: [skill()] }); host.content.mockResolvedValueOnce(content("office", "new authorized version"));
    props.capabilityRevision++; await flush(); await c.openSkill(skill());
    expect(host.content).toHaveBeenCalledTimes(2); expect(c.detail.value?.content).toBe("new authorized version");
  });

  it("drops an in-flight revoked Skill read after a committed capability change", async () => {
    const doc = pending<SkillContent>(), { c, props } = mount(); await flush(); host.content.mockReturnValueOnce(doc.promise);
    const reading = c.openSkill(skill()); host.skills.mockResolvedValueOnce({ skills: [] }); props.capabilityRevision++; await flush();
    doc.resolve(content("office", "late revoked content")); await reading;
    expect(c.selected.value).toBeNull(); expect(c.detail.value).toBeNull(); expect(c.detailLoading.value).toBe(false);
  });

  it("reloads both scoped catalogs and retains only an authorized selection on MCP-only changes", async () => {
    const { c, props } = mount(); await flush(); await c.openSkill(skill());
    host.content.mockResolvedValueOnce(content("office", "fresh content")); props.capabilityRevision++; await flush();
    expect(host.skills).toHaveBeenCalledTimes(2); expect(host.tools).toHaveBeenCalledTimes(2);
    expect(host.skills).toHaveBeenLastCalledWith("session-a"); expect(host.tools).toHaveBeenLastCalledWith("session-a");
    expect(c.selected.value?.name).toBe("office"); expect(c.detail.value?.content).toBe("fresh content");
  });

  it("cannot restore a previous conversation's delayed refreshed catalog", async () => {
    const { c, props } = mount(); await flush();
    const old = pending<{ skills: SkillInfo[] }>(); host.skills.mockReturnValueOnce(old.promise); props.capabilityRevision++; await flush();
    host.skills.mockResolvedValueOnce({ skills: [skill("new-session")] }); props.sessionId = "session-b"; await flush();
    old.resolve({ skills: [skill("old-session")] }); await flush();
    expect(c.skills.value?.map(item => item.name)).toEqual(["new-session"]); expect(c.selected.value).toBeNull();
  });
});


describe("effective native tools", () => {
  it("shows only the exact scoped builtin response, including shortcut_create", async () => {
    host.tools.mockResolvedValue({ tools: [], mcp: [], builtin: [{ name: "shortcut_create", description: "保存快捷方式" }] });
    const { c } = mount(); await flush(); expect(c.builtinTools.value.map(x => x.name)).toEqual(["shortcut_create"]); expect(host.tools).toHaveBeenCalledWith("session-a");
    c.filter.value = "shortcut"; expect(c.filteredBuiltinTools.value).toHaveLength(1); c.filter.value = "bash"; expect(c.filteredBuiltinTools.value).toEqual([]);
  });
  it("pending-only state does not refetch or prematurely add a tool", async () => {
    host.tools.mockResolvedValue({ tools: [], mcp: [], builtin: [{ name: "read", description: "Read" }] }); const { c, props } = mount(); await flush();
    props.toolsPending = true; await flush(); expect(host.tools).toHaveBeenCalledTimes(1); expect(c.builtinTools.value.map(x => x.name)).toEqual(["read"]);
  });
  it("effective revision refreshes in place while retaining a selected description and filter", async () => {
    host.tools.mockResolvedValueOnce({ tools: [], mcp: [], builtin: [{ name: "read", description: "Read" }] }); const { c, props } = mount(); await flush(); await c.openSkill(skill()); c.filter.value = "shortcut";
    host.tools.mockResolvedValueOnce({ tools: [], mcp: [], builtin: [{ name: "read", description: "Read" }, { name: "shortcut_create", description: "Create" }] }); props.toolRevision = 2; await flush();
    expect(c.builtinTools.value.map(x => x.name)).toEqual(["read", "shortcut_create"]); expect(c.selected.value?.name).toBe("office"); expect(c.filter.value).toBe("shortcut");
  });
  it("an effective tool refresh does not cancel an in-flight Skill document or leave its loading state stuck", async () => {
    const doc = pending<SkillContent>(); host.content.mockReturnValueOnce(doc.promise); const { c, props } = mount(); await flush();
    const reading = c.openSkill(skill()); expect(c.detailLoading.value).toBe(true);
    host.tools.mockResolvedValueOnce({ tools: [], mcp: [], builtin: [{ name: "shortcut_create", description: "Create" }] }); props.toolRevision = 4; await flush();
    expect(host.skills).toHaveBeenCalledTimes(1); expect(c.selected.value?.name).toBe("office");
    doc.resolve(content("office", "completed while tools updated")); await reading; expect(c.detail.value?.content).toBe("completed while tools updated"); expect(c.detailLoading.value).toBe(false);
  });
  it("an older in-flight catalog cannot undo the newer effective revision", async () => {
    const old = pending<{ tools: []; mcp: []; builtin: BuiltinToolEntry[] }>(); host.tools.mockReturnValueOnce(old.promise); const { c, props } = mount();
    host.tools.mockResolvedValueOnce({ tools: [], mcp: [], builtin: [{ name: "shortcut_create", description: "Create" }] }); props.toolRevision = 3; await flush(); old.resolve({ tools: [], mcp: [], builtin: [{ name: "read", description: "Read" }] }); await flush(); expect(c.builtinTools.value.map(x => x.name)).toEqual(["shortcut_create"]);
  });
});

const privateSkill = () => ({ id: "as_one", name: "sql-dates", description: "日期调整流程", revision: "a".repeat(64), version: 1, editable: true, enabled: false, source: "authored" });
describe("current Agent authored Skills", () => {
  it("lists enabled entries and drafts with an editable detail entry, without duplicating session Skills", async () => {
    host.authored.mockResolvedValueOnce({ skills: [privateSkill()] }); host.skills.mockResolvedValueOnce({ skills: [skill(), { ...skill("sql-dates"), source: "authored" }] });
    const { c } = mount(); await flush(); expect(host.authored).toHaveBeenCalledWith("agent-a"); expect(c.authored.value).toEqual([privateSkill()]); expect(c.filteredSkills.value.map(s => s.name)).toEqual(["office"]);
    c.openAuthored(c.authored.value[0]!); expect(c.authoredOpen.value).toBe(true); expect(c.authoredSelected.value?.id).toBe("as_one");
  });
  it("cannot display the old private list when another Agent becomes current", async () => {
    const old = pending<{ skills: ReturnType<typeof privateSkill>[] }>(); host.authored.mockReturnValueOnce(old.promise); const { c, props } = mount();
    host.authored.mockResolvedValueOnce({ skills: [] }); props.agentId = "agent-b"; props.sessionId = "session-b"; await flush(); old.resolve({ skills: [privateSkill()] }); await flush();
    expect(c.authored.value).toEqual([]); expect(c.authoredSelected.value).toBeNull(); expect(host.authored).toHaveBeenLastCalledWith("agent-b");
  });
  it("ignores another Agent's refresh event and never substitutes an unscoped lookup", async () => {
    const { c, props } = mount(); await flush(); const count = host.authored.mock.calls.length;
    c.authoredChanged({ detail: { agentId: "other-agent" } } as unknown as Event); await flush(); expect(host.authored).toHaveBeenCalledTimes(count);
    c.authoredChanged({ detail: { agentId: "agent-a" } } as unknown as Event); await flush(); expect(host.authored).toHaveBeenCalledTimes(count + 1);
    props.sessionId = ""; await flush(); await c.loadAuthored(); expect(host.authored).toHaveBeenCalledTimes(count + 1);
  });
});
