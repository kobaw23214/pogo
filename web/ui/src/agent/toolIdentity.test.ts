import { afterEach, describe, expect, it } from "vitest";
import type { ToolCatalogEntry } from "@/api/types";
import { setLocale } from "@/i18n";
import { buildToolIdentityCatalog, createToolIdentityCatalogSession, pureSleepSeconds, resolveToolIdentity, type ToolCatalogResponse } from "./toolIdentity";

const tool = (wire: string, title: string, extra: Partial<ToolCatalogEntry> = {}): ToolCatalogEntry => ({ name: "lookup", wire_name: wire, title, description: "", input_schema: { type: "object" }, ...extra });
const response = (server: string, title = server): ToolCatalogResponse => ({ tools: [], mcp: [{ name: server, state: "connected", tools: [tool("mcp_hash", title)] }] });
setLocale("zh-CN");
afterEach(() => setLocale("zh-CN"));

describe("tool identity catalog", () => {
  it("uses explicit external mappings before a result exists, with result identity taking precedence", () => {
    const catalog = buildToolIdentityCatalog(response("server-a", "查询告警"));
    expect(resolveToolIdentity("mcp_hash", {}, undefined, catalog)).toMatchObject({ name: "查询告警", secondary: "server-a", server: "server-a", glyph: "mcp", protocolName: "mcp_hash" });
    expect(resolveToolIdentity("mcp_hash", {}, { server: "recorded-server", tool: "lookup", title: "历史标题" }, catalog)).toMatchObject({ name: "历史标题", secondary: "recorded-server · lookup", server: "recorded-server" });
    expect(resolveToolIdentity("mcp_unknown_hash", {}, undefined, catalog)).toMatchObject({ name: "mcp_unknown_hash", server: undefined });
  });

  it("does not confuse same-name tools on different servers or display-only declarations without a wire key", () => {
    const catalog = buildToolIdentityCatalog({ tools: [tool("", "missing identity")], mcp: [
      { name: "alpha", state: "connected", tools: [tool("mcp_alpha", "Alpha lookup")] },
      { name: "beta", state: "disconnected", tools: [tool("mcp_beta", "Beta lookup")] },
    ] });
    expect(resolveToolIdentity("mcp_alpha", {}, undefined, catalog).secondary).toBe("alpha");
    expect(resolveToolIdentity("mcp_beta", {}, undefined, catalog)).toMatchObject({ name: "Beta lookup", secondary: "beta" });
    expect(catalog.has("")).toBe(false);
  });

  it("clears the old session immediately and ignores late responses, including A to B to A", async () => {
    const pending: Array<{ session: string; resolve: (value: ToolCatalogResponse) => void }> = [];
    const loader = createToolIdentityCatalogSession(session => new Promise(resolve => pending.push({ session, resolve })));
    const firstA = loader.setSession("a");
    const firstB = loader.setSession("b");
    const secondA = loader.setSession("a");
    pending[2].resolve(response("a", "Current A")); await secondA;
    pending[0].resolve(response("a", "Stale A")); await firstA;
    pending[1].resolve(response("b", "B")); await firstB;
    expect(loader.catalog.value.get("mcp_hash")?.title).toBe("Current A");
    const bAgain = loader.setSession("b");
    expect(loader.catalog.value.size).toBe(0);
    pending[3].resolve(response("b", "Current B")); await bAgain;
    expect(loader.catalog.value.get("mcp_hash")?.title).toBe("Current B");
    loader.dispose();
  });

  it("uses only the newest refresh and keeps failure fallback scoped to the current session", async () => {
    let resolveOld!: (value: ToolCatalogResponse) => void;
    let attempt = 0;
    const loader = createToolIdentityCatalogSession(async () => {
      attempt++;
      if (attempt === 1) return response("a", "Initial");
      if (attempt === 2) return new Promise(resolve => { resolveOld = resolve; });
      if (attempt === 3) return response("a", "Newer");
      throw new Error("offline");
    });
    await loader.setSession("a");
    const old = loader.refresh(); await loader.refresh();
    resolveOld(response("a", "Old refresh")); await old;
    expect(loader.catalog.value.get("mcp_hash")?.title).toBe("Newer");
    await loader.refresh(); expect(loader.catalog.value.get("mcp_hash")?.title).toBe("Newer");
    await loader.setSession("b"); expect(loader.catalog.value.size).toBe(0);
    loader.dispose();
  });

  it("does not publish metadata after disposal", async () => {
    let resolve!: (value: ToolCatalogResponse) => void;
    const loader = createToolIdentityCatalogSession(() => new Promise(done => { resolve = done; }));
    const loading = loader.setSession("a"); loader.dispose();
    resolve(response("a")); await loading;
    expect(loader.catalog.value.size).toBe(0);
  });

  it("resolves synthetic tool declarations without inventing protocol support", () => {
    const catalog = buildToolIdentityCatalog({ tools: [tool("docs_pdf-info", "读取 PDF 信息", { skill: "documents", renderer: "table" })], mcp: [] });
    expect(resolveToolIdentity("docs_pdf-info", { path: "invoice.pdf" }, undefined, catalog)).toMatchObject({ name: "读取 PDF 信息", glyph: "skill", target: "invoice.pdf", secondary: "documents", renderer: "table" });
  });
});

describe("declared tool targets", () => {
  it("shows browser actions and their own targets without using typed secrets as object labels", () => {
    expect(resolveToolIdentity("browser", { action: "click", target: { text: "查询" }, tab_id: "t1" })).toMatchObject({ name: "点击", target: "查询" });
    expect(resolveToolIdentity("browser", { action: "click", target: "#search-button" })).toMatchObject({ name: "点击", target: "#search-button" });
    expect(resolveToolIdentity("browser", { action: "tab", tab_action: "switch", target: "tab-3" }).target).toBe("tab-3");
    expect(resolveToolIdentity("browser", { action: "navigate", url: "https://example.com/alerts" })).toMatchObject({ name: "打开页面", target: "https://example.com/alerts" });
    expect(resolveToolIdentity("browser", { action: "fill", text: "secret", target: { label: "Account" } }).target).toBe("Account");
    expect(resolveToolIdentity("browser", { action: "snapshot" })).toMatchObject({ name: "读取页面", target: undefined });
    expect(resolveToolIdentity("browser", { action: "snapshot" }, { action: "snapshot", ok: true, title: "Recorded page", active_tab: "t1" }).target).toBe("Recorded page");
  });

  it("recognizes only a literal standalone sleep command", () => {
    expect(pureSleepSeconds({ command: "sleep 1.5" })).toBe(1.5);
    expect(resolveToolIdentity("bash", { command: "sleep 45" })).toMatchObject({ name: "等待", target: "45 秒" });
    for (const command of ["sleep $SECONDS", "sleep 5; rm x", "cd /tmp && sleep 3", "sleep 1 && echo done", "sleep 1h", "sleep Infinity", "sleep -1"]) expect(pureSleepSeconds({ command })).toBeUndefined();
  });
});
