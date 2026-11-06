import { setLocale } from "@/i18n";
import { beforeEach, describe, expect, it } from "vitest";
import type { AgentTemplate, PlatformAgent, PlatformCapabilities, PlatformModel } from "./platformTypes";
import {
  AGENT_FORM_LIMITS,
  STUDIO_NAV,
  agentCapabilitySummary,
  agentModelsLabel,
  buildAgentInput,
  categoryChips,
  defaultModelId,
  embedAgentUrl,
  embedSnippet,
  filterAgents,
  formatAgentTime,
  initialCapabilities,
  managedChatPath,
  modelLabel,
  publishedAgentUrl,
  sectionForSettingsSection,
  setDefaultModel,
  settingsSectionFor,
  toggleId,
  toggleModelSelection,
  toolsetWarning,
  unavailableSelected,
  validateAgentForm,
  normalizeStarter,
  withTemplate,
  studioPathForSection,
  visibleSection,
  visibleStudioNav,
  type CapabilitySelection,
  type AgentFormState,
} from "./studioPresentation";

const agent = (over: Partial<PlatformAgent>): PlatformAgent => ({
  id: "ag_1",
  name: "研究助手",
  description: "拆解复杂问题",
  instructions: "…",
  model: "k3",
  color: "#4a90c2",
  icon: "search",
  category: "research",
  starters: [],
  status: "ready",
  revision: 3,
  created_at: "2026-09-01T08:00:00Z",
  updated_at: "2026-09-20T10:00:00Z",
  conversation_count: 12,
  ...over,
});

const models: PlatformModel[] = [
  { id: "glm-5.3", provider: "zhipu", available: true },
  { id: "k3", provider: "moonshot", available: true },
  { id: "retired", provider: "old", available: false },
];

const capabilities: PlatformCapabilities = {
  skills: [
    { id: "skill-pdf", name: "PDF 处理", description: "读取和拆分 PDF", hidden: false, tool_count: 3 },
    { id: "skill-web", name: "网页检索", description: "打开并摘录网页", hidden: true, tool_count: 2 },
  ],
  mcp_servers: [
    { id: "github", name: "GitHub", transport: "stdio", enabled: true, status: "运行中", tool_count: 8 },
    { id: "browser", name: "浏览器", transport: "sse", enabled: false, status: "已停用", tool_count: 4 },
  ],
  builtin_tools: [
    { name: "read", description: "读取文件", read_only: true, protected: true },
    { name: "write", description: "写文件" },
    { name: "bash", description: "执行命令" },
    { name: "browser", description: "驱动浏览器" },
  ],
};

const blank: AgentFormState = {
  name: "",
  description: "",
  instructions: "",
  model: "",
  models: [],
  skills: [],
  mcpServers: [],
  tools: [],
  skillsInherited: false,
  mcpInherited: false,
  toolsInherited: false,
  color: "#5965e8",
  icon: "spark",
  category: "general",
  starters: [{ title: "", label: "", icon: "", prompt: "" }, { title: "", label: "", icon: "", prompt: "" }, { title: "", label: "", icon: "", prompt: "" }],
};

describe("Studio navigation modules", () => {
  it("declares the management modules including memory, agents first", () => {    expect(STUDIO_NAV.map((n) => n.section)).toEqual(["agents", "information", "models", "mcp", "skills", "memory", "account", "settings"]);
    expect(STUDIO_NAV.map((n) => n.path)).toEqual([
      "/",
      "/manage/information",
      "/manage/models",
      "/manage/mcp",
      "/manage/skills",
      "/manage/memory",
      "/manage/account",
      "/manage/settings",
    ]);
    expect(STUDIO_NAV.map((n) => n.label)).toEqual([
      "我的 Agents",
      "信息库",
      "模型配置",
      "MCP 管理",
      "Skill 管理",
      "记忆与偏好",
      "账号与安全",
      "设置",
    ]);
    // 模板灵感 is deliberately absent: templates live inside the creation dialog.
    expect(STUDIO_NAV.some((n) => n.label.includes("模板"))).toBe(false);
  });

  it("maps modules onto SettingsDialog sections, settings being the general sheet", () => {
    expect(settingsSectionFor("models")).toBe("models");
    expect(settingsSectionFor("mcp")).toBe("mcp");
    expect(settingsSectionFor("account")).toBe("account");
    expect(settingsSectionFor("settings")).toBe("general");
    expect(settingsSectionFor("memory")).toBe("memory");
    expect(settingsSectionFor("agents")).toBe("general");
    expect(settingsSectionFor("skills")).toBe("general");
  });

  it("maps settings sections back to their Studio module and route", () => {
    expect(sectionForSettingsSection("models")).toBe("models");
    expect(sectionForSettingsSection("mcp")).toBe("mcp");
    expect(sectionForSettingsSection("account")).toBe("account");
    expect(sectionForSettingsSection("general")).toBe("settings");
    expect(sectionForSettingsSection("memory")).toBe("memory");
    expect(studioPathForSection("memory")).toBe("/manage/memory");
    expect(studioPathForSection("mcp")).toBe("/manage/mcp");
    expect(studioPathForSection("skills")).toBe("/manage/skills");
    // The home address is the agents surface; /manage stays a valid alias.
    expect(studioPathForSection("agents")).toBe("/");
  });
});

describe("published and managed links", () => {
  it("builds the canonical published URL from the origin, encoding the id", () => {
    expect(publishedAgentUrl("https://pogo.example", "ag_1")).toBe("https://pogo.example/a/ag_1");
    expect(publishedAgentUrl("https://pogo.example", "a/b c")).toBe("https://pogo.example/a/a%2Fb%20c");
  });

  it("builds the embed URL and a self-contained iframe snippet", () => {
    expect(embedAgentUrl("http://localhost:5173", "ag_9")).toBe("http://localhost:5173/a/ag_9?embed=1");
    const snippet = embedSnippet("http://localhost:5173", "ag_9");
    expect(snippet).toContain('src="http://localhost:5173/a/ag_9?embed=1"');
    expect(snippet.startsWith("<iframe ")).toBe(true);
  });

  it("keeps the admin preview on the management route, never the published one", () => {
    expect(managedChatPath("ag_1")).toBe("/manage/agents/ag_1/chat");
  });

  it("never carries credentials or session values in any link", () => {
    for (const url of [
      publishedAgentUrl("https://pogo.example", "ag_1"),
      embedAgentUrl("https://pogo.example", "ag_1"),
      embedSnippet("https://pogo.example", "ag_1"),
      managedChatPath("ag_1"),
    ]) {
      expect(url).not.toMatch(/token|key|secret|password|session/i);
    }
  });
});

describe("agent catalog filtering", () => {
  const list = [
    agent({ id: "a1", name: "写作伙伴", category: "writing", status: "ready" }),
    agent({ id: "a2", name: "代码讲解", category: "code", status: "draft", model: "glm-5.3" }),
    agent({ id: "a3", name: "研究助手", category: "research", status: "ready" }),
  ];

  it("filters by query across name, description and model", () => {
    expect(filterAgents(list, "写作", "all").map((a) => a.id)).toEqual(["a1"]);
    expect(filterAgents(list, "glm", "all").map((a) => a.id)).toEqual(["a2"]);
    expect(filterAgents(list, "  ", "all")).toHaveLength(3);
  });

  it("filters by status and category together", () => {
    expect(filterAgents(list, "", "ready").map((a) => a.id)).toEqual(["a1", "a3"]);
    expect(filterAgents(list, "", "all", "code").map((a) => a.id)).toEqual(["a2"]);
    expect(filterAgents(list, "", "draft", "code").map((a) => a.id)).toEqual(["a2"]);
    expect(filterAgents(list, "", "ready", "code")).toEqual([]);
  });

  it("lists category chips in canonical order, only for categories in use", () => {
    expect(categoryChips(list).map((c) => c.value)).toEqual(["all", "writing", "research", "code"]);
    expect(categoryChips([]).map((c) => c.value)).toEqual(["all"]);
  });
});

describe("model presentation", () => {
  it("prefers k3, then glm-5.3, then the first available model", () => {
    expect(defaultModelId(models)).toBe("k3");
    expect(defaultModelId(models.filter((m) => m.id !== "k3"))).toBe("glm-5.3");
    expect(defaultModelId([{ id: "other", provider: "p", available: true }])).toBe("other");
    expect(defaultModelId([{ id: "off", provider: "p", available: false }])).toBe("");
  });

  it("labels models with their provider and marks unavailable ones", () => {
    expect(modelLabel(models, "k3")).toBe("moonshot · k3");
    expect(modelLabel(models, "retired")).toBe("old · retired（不可用）");
    expect(modelLabel(models, "ghost")).toBe("ghost");
    expect(modelLabel(models, "")).toBe("未选择模型");
  });
});

describe("agent form", () => {
  it("requires a name and a model, within the documented limits", () => {
    expect(validateAgentForm(blank)).toEqual({ name: "请填写名称", model: "请至少选择一个模型" });
    const over = { ...blank, name: "x".repeat(AGENT_FORM_LIMITS.name + 1), models: ["k3"], model: "k3" };
    expect(validateAgentForm(over).name).toContain("60");
    expect(validateAgentForm({ ...blank, name: "好", models: ["k3"], model: "k3" })).toEqual({});
    expect(validateAgentForm({ ...blank, name: "好", models: ["k3", "glm-5.3"], model: "glm-5.3" })).toEqual({});
    expect(validateAgentForm({ ...blank, name: "好", models: ["k3", "glm-5.3"], model: "retired" }).model).toBe(
      "默认模型必须在已选模型中",
    );
  });

  it("builds the API input trimmed, starters compacted; card fields ride along", () => {
    const input = buildAgentInput(
      {
        ...blank,
        name: "  助手  ",
        models: ["k3", "k3", "glm-5.3"],
        model: "k3",
        skills: ["skill-pdf", "skill-pdf"],
        mcpServers: ["github", "github"],
        tools: ["read", "bash", "bash"],
        starters: [
          { title: "", label: "", icon: "", prompt: " 一 " },
          { title: "", label: "", icon: "", prompt: "" },
          { title: "t", label: "l", icon: "shield", prompt: "二" },
          { title: "", label: "", icon: "", prompt: "三" },
        ],
      },
      "ready",
    );
    expect(input.name).toBe("助手");
    expect(input.starters).toEqual(["一", { title: "t", label: "l", icon: "shield", prompt: "二" }, "三"]);
    expect(input.status).toBe("ready");
    expect(input.models).toEqual(["k3", "glm-5.3"]);
    expect(input.skills).toEqual(["skill-pdf"]);
    expect(input.mcp_servers).toEqual(["github"]);
    expect(input.tools).toEqual(["read", "bash"]);
  });
});

describe("builtin tool selection", () => {
  it("shows a read-and-discuss hint unless an optional action, tool-bearing Skill or MCP is selected", () => {
    const readonly = { ...blank, tools: ["read"], skills: [], mcpServers: [] };
    expect(toolsetWarning(readonly, capabilities)).toContain("仅包含读取和讨论能力");
    // Each capability family independently lifts this informational hint.
    expect(toolsetWarning({ ...readonly, tools: ["read", "bash"] }, capabilities)).toBe("");
    expect(toolsetWarning({ ...readonly, skills: ["skill-pdf"] }, capabilities)).toBe("");
    expect(toolsetWarning({ ...readonly, mcpServers: ["github"] }, capabilities)).toBe("");
    // A knowledge-only skill (no declared tools) does not count as acting.
    expect(toolsetWarning({ ...readonly, skills: ["skill-none"] }, { ...capabilities, skills: [...capabilities.skills, { id: "skill-none", name: "纯知识", description: "", hidden: false, tool_count: 0 }] })).toContain("仅包含读取和讨论能力");
  });

  it.each([
    "write", "edit", "bash", "browser", "shortcut_manage", "shortcut_create", "shortcut_update",
    "shortcut_folder_create", "shortcut_folder_rename", "shortcut_move", "shortcut_delete", "shortcut_merge",
    "skill_save", "watch", "background_task", "watch_test",
  ])("recognizes the optional host action %s without requiring unrelated tools", name => {
    expect(toolsetWarning({ tools: ["read", name], skills: [], mcpServers: [] }, capabilities)).toBe("");
  });

  it.each([
    "read", "ls", "find", "grep", "tool_result", "shortcut_query", "shortcut_search", "shortcut_read",
    "shortcut_list", "skill_search", "skill_read", "todo",
  ])("keeps the hint for read/discussion selection %s", name => {
    expect(toolsetWarning({ tools: [name], skills: [], mcpServers: [] }, capabilities)).not.toBe("");
  });

  it("does not treat protected coordination as an optional action grant", () => {
    const catalog: PlatformCapabilities = { ...capabilities, builtin_tools: [
      { name: "read", description: "Read", read_only: true, protected: true },
      { name: "todo", description: "Coordinate", read_only: true, protected: true },
      { name: "subagent", description: "Delegate with inherited permissions", read_only: false, protected: true },
      { name: "watch_decide", description: "Decide an existing event batch", read_only: true, protected: true },
    ] };
    expect(toolsetWarning({ tools: ["read", "todo", "subagent", "watch_decide"], skills: [], mcpServers: [] }, catalog)).not.toBe("");
  });

  it("recognizes selected runtime Skill tools even without manifest tools", () => {
    const catalog: PlatformCapabilities = { ...capabilities, skills: [
      ...capabilities.skills, { id: "runtime", name: "Runtime capability", description: "", hidden: false, tool_count: 0, runtime_count: 1 },
    ] };
    expect(toolsetWarning({ tools: ["read"], skills: ["runtime"], mcpServers: [] }, catalog)).toBe("");
  });

  it("leaves a read-and-discuss Agent valid to save", () => {
    const form: AgentFormState = { ...blank, name: "只读助手", model: "k3", models: ["k3"], tools: ["read"] };
    expect(toolsetWarning(form, capabilities)).not.toBe("");
    expect(validateAgentForm(form)).toEqual({});
    expect(buildAgentInput(form, "ready").tools).toEqual(["read"]);
  });
});

describe("agent capability selection", () => {
  const selection = (over: Partial<CapabilitySelection> = {}): CapabilitySelection => ({
    models: ["k3", "glm-5.3"],
    defaultModel: "k3",
    skills: ["skill-pdf"],
    mcpServers: ["github"],
    tools: ["read", "write", "bash", "browser"],
    skillsInherited: false,
    mcpInherited: false,
    toolsInherited: false,
    ...over,
  });

  it("opens a legacy Agent with its single model and effective inherited catalog, excluding disabled MCP", () => {
    const opened = initialCapabilities(agent({}), capabilities);
    expect(opened).toEqual({
      models: ["k3"],
      defaultModel: "k3",
      skills: ["skill-pdf", "skill-web"],
      mcpServers: ["github"],
      tools: ["read", "write", "bash", "browser"],
      skillsInherited: true,
      mcpInherited: true,
      toolsInherited: true,
    });
  });

  it("retains an explicitly selected disabled MCP so it can be reviewed and removed", () => {
    const opened = initialCapabilities(agent({ mcp_servers: ["browser"] }), capabilities);
    expect(opened.mcpServers).toEqual(["browser"]);
    expect(opened.mcpInherited).toBe(false);
  });

  it("distinguishes explicit none from legacy inheritance", () => {
    const opened = initialCapabilities(
      agent({ models: ["glm-5.3"], model: "glm-5.3", skills: [], mcp_servers: [], tools: [] }),
      capabilities,
    );
    expect(opened.models).toEqual(["glm-5.3"]);
    expect(opened.defaultModel).toBe("glm-5.3");
    expect(opened.skills).toEqual([]);
    expect(opened.mcpServers).toEqual([]);
    expect(opened.tools).toEqual(["read"]);
    expect(opened.skillsInherited).toBe(false);
    expect(opened.mcpInherited).toBe(false);
    expect(opened.toolsInherited).toBe(false);
  });

  it("unions the protected core into an explicit tool selection, honoring the rest", () => {
    const opened = initialCapabilities(agent({ tools: ["bash", "bash"] }), capabilities);
    expect(opened.tools).toEqual(["read", "bash"]);
    expect(opened.toolsInherited).toBe(false);
  });

  it("keeps stale explicit ids instead of silently deleting them", () => {
    const opened = initialCapabilities(
      agent({ models: ["k3", "retired"], skills: ["gone-skill"], mcp_servers: ["gone-server"] }),
      capabilities,
    );
    expect(opened.models).toEqual(["k3", "retired"]);
    expect(opened.skills).toEqual(["gone-skill"]);
    expect(opened.mcpServers).toEqual(["gone-server"]);
    expect(unavailableSelected(opened.skills, capabilities.skills.map((s) => s.id))).toEqual(["gone-skill"]);
    expect(unavailableSelected(opened.mcpServers, capabilities.mcp_servers.map((m) => m.id))).toEqual(["gone-server"]);
  });

  it("keeps the default model valid across every multi-select transition", () => {
    const withoutDefault = toggleModelSelection(selection(), "k3");
    expect(withoutDefault.models).toEqual(["glm-5.3"]);
    expect(withoutDefault.defaultModel).toBe("glm-5.3");

    const empty = toggleModelSelection(selection({ models: ["k3"], defaultModel: "k3" }), "k3");
    expect(empty).toEqual(selection({ models: [], defaultModel: "" }));

    const fromEmpty = toggleModelSelection(selection({ models: [], defaultModel: "" }), "glm-5.3");
    expect(fromEmpty.models).toEqual(["glm-5.3"]);
    expect(fromEmpty.defaultModel).toBe("glm-5.3");

    const moved = setDefaultModel(selection(), "glm-5.3");
    expect(moved.defaultModel).toBe("glm-5.3");
    expect(setDefaultModel(moved, "retired").defaultModel).toBe("glm-5.3");
  });

  it("toggles Skill and MCP ids without losing the other selection", () => {
    expect(toggleId(["skill-pdf", "skill-web"], "skill-pdf")).toEqual(["skill-web"]);
    expect(toggleId(["skill-pdf"], "skill-web")).toEqual(["skill-pdf", "skill-web"]);
    expect(toggleId(["github"], "github")).toEqual([]);
  });

  it("summarizes models and either explicit grants or legacy inheritance for cards", () => {
    expect(agentCapabilitySummary(agent({}))).toBe("1 个模型 · Skill/MCP 继承工作空间");
    expect(agentCapabilitySummary(agent({ models: ["k3", "glm-5.3"], skills: [], mcp_servers: [] }))).toBe(
      "2 个模型 · 0 Skill · 0 MCP",
    );
    expect(agentModelsLabel(models, agent({ models: ["k3", "glm-5.3"] }))).toBe("moonshot · k3 等 2 个");
    expect(agentModelsLabel(models, agent({}))).toBe("moonshot · k3");
    expect(agentCapabilitySummary(agent({ models: [], model: "", skills: [], mcp_servers: [] }))).toBe(
      "0 个模型 · 0 Skill · 0 MCP",
    );
  });
});

describe("timestamps", () => {
  const now = new Date("2026-09-23T15:00:00");

  it("names today and uses a full date for every other day", () => {
    expect(formatAgentTime("2026-09-23T09:05:00", now, "今日")).toBe("今日 09:05");
    expect(formatAgentTime("2026-03-02T09:05:00", now)).toBe("2026-03-02");
    expect(formatAgentTime("2025-12-31T23:59:00", now)).toBe("2025-12-31");
    expect(formatAgentTime("not-a-date", now)).toBe("");
  });
});

describe("templates", () => {
  const tpl: AgentTemplate = {
    id: "tpl_fixture",
    name: "研究助手",
    description: "拆解复杂问题，区分事实与假设。",
    instructions: "你是一位严谨的研究助手。面对复杂问题，先澄清目标与边界，再把它拆解成可以逐一验证的小问题；清楚区分事实、假设与推测。",
    icon: "search",
    color: "#4a90c2",
    category: "research",
    starters: ["帮我梳理这个问题的研究思路", "把这份资料提炼成要点", "对比这两个方案"],
  };

  it("applies a well-formed template to a blank draft without validation errors", () => {
    expect(
      validateAgentForm({
        ...blank,
        name: tpl.name,
        description: tpl.description,
        instructions: tpl.instructions,
        model: "k3",
        models: ["k3"],
        color: tpl.color,
        icon: tpl.icon,
        category: tpl.category,
        starters: tpl.starters.map(normalizeStarter),
      }),
    ).toEqual({});
  });

  it("refills identity but never erases checked capabilities", () => {
    const before: AgentFormState = {
      ...blank,
      name: "旧名字",
      model: "glm-5.3",
      models: ["k3", "glm-5.3"],
      skills: ["skill-pdf", "gone-skill"],
      mcpServers: ["github"],
      skillsInherited: false,
      mcpInherited: false,
    };
    const after = withTemplate(before, tpl);
    expect(after.name).toBe(tpl.name);
    expect(after.models).toEqual(["k3", "glm-5.3"]);
    expect(after.model).toBe("glm-5.3");
    expect(after.skills).toEqual(["skill-pdf", "gone-skill"]);
    expect(after.mcpServers).toEqual(["github"]);
  });
});

describe("Account surfaces without the account system", () => {
  it("keeps every module when password login is enabled", () => {
    expect(visibleStudioNav(true).map((n) => n.section)).toEqual([
      "agents", "information", "models", "mcp", "skills", "memory", "account", "settings",
    ]);
  });

  it("drops the account and settings modules on a token-only deployment", () => {
    expect(visibleStudioNav(false).map((n) => n.section)).toEqual([
      "agents", "information", "models", "mcp", "skills", "memory",
    ]);
  });

  it("falls an account deep link back to the Agents page, leaves the rest alone", () => {
    expect(visibleSection("account", false)).toBe("agents");
    expect(visibleSection("settings", false)).toBe("agents");
    expect(visibleSection("account", true)).toBe("account");
    expect(visibleSection("skills", false)).toBe("skills");
    expect(visibleSection("memory", false)).toBe("memory");
  });
});

// These fixtures assert Chinese labels/order; the interface now follows an explicit locale.
beforeEach(() => setLocale("zh-CN"));
