import { describe, expect, it } from "vitest";
import { exportConnectionLabel, exportMCPServers, type MCPExportTarget } from "./mcpExport";

const endpoint = "https://example.invalid/mcp";

describe("MCP export preserves effective credentials", () => {
  it("keeps the explicit Authorization header that overrides the bearer field at runtime", () => {
    const result = exportMCPServers({
      service: { url: endpoint, bearer_token_env: "FALLBACK_TOKEN", headers: { authorization: "Bearer ${ACTUAL_TOKEN}" } },
    }, "claude");
    const entry = JSON.parse(result.text).mcpServers.service;
    const auth = Object.entries(entry.headers).find(([name]) => name.toLowerCase() === "authorization")?.[1];
    expect(auth).toBe("Bearer ${ACTUAL_TOKEN}");
    expect(result.vars.map((v) => v.name)).toContain("ACTUAL_TOKEN");
    expect(result.vars.map((v) => v.name)).not.toContain("FALLBACK_TOKEN");
  });

  it.each([false, true])("keeps generated credentials distinct from existing references in either entry order (%s)", (reverse) => {
    const entries = [
      ["existing", { url: endpoint, bearer_token_env: "MCP_SECOND_TOKEN" }],
      ["second", { url: endpoint, bearer_token: "synthetic-literal" }],
    ] as const;
    const result = exportMCPServers(Object.fromEntries(reverse ? [...entries].reverse() : entries), "claude");
    const servers = JSON.parse(result.text).mcpServers;
    expect(servers.existing.headers.Authorization).not.toBe(servers.second.headers.Authorization);
    expect(result.vars).toHaveLength(2);
  });

  it("does not embed literal credential fragments from a mixed template", () => {
    const result = exportMCPServers({
      service: { url: endpoint, headers: { "X-Api-Key": "synthetic-static-secret-${SUFFIX}" } },
    }, "claude");
    expect(JSON.stringify(result)).not.toContain("synthetic-static-secret");
  });

  it.each(["pogo", "claude", "cursor", "vscode"] as MCPExportTarget[])("does not copy credential header arguments into %s", (target) => {
    const result = exportMCPServers({
      service: { command: "demo", args: ["--header", "Authorization: Bearer synthetic-arg-secret"] },
    }, target);
    expect(JSON.stringify(result)).not.toContain("synthetic-arg-secret");
    if (result.text) expect(result.vars.length).toBeGreaterThan(0);
    else expect(result.warnings.some((w) => w.blocked)).toBe(true);
  });

  it("never emits a percent-encoded, nonfunctional environment reference for a URL credential", () => {
    const result = exportMCPServers({ service: { url: endpoint + "?token=synthetic-query-secret" } }, "claude");
    expect(JSON.stringify(result)).not.toContain("synthetic-query-secret");
    if (result.text) {
      const url = JSON.parse(result.text).mcpServers.service.url;
      expect(url).not.toMatch(/%24%7B/i);
      expect(url).toContain("${");
    } else {
      expect(result.warnings.some((w) => w.blocked)).toBe(true);
    }
  });

  it("only asks for variables belonging to included entries", () => {
    const result = exportMCPServers({
      excluded: { url: endpoint, bearer_token: "synthetic-literal", headers: { "X-Region": "${env:UNSUPPORTED}" } },
      included: { url: endpoint },
    }, "pogo");
    expect(result.servers).toEqual(["included"]);
    expect(result.vars).toEqual([]);
  });

  it("redacts a credential query key even when the key is URL-encoded", () => {
    const result = exportMCPServers({ service: { url: endpoint + "?%74oken=synthetic-encoded-secret" } }, "claude");
    expect(JSON.stringify(result)).not.toContain("synthetic-encoded-secret");
    if (result.text) expect(result.vars).toHaveLength(1);
  });

  it("does not merge two distinct repeated credential query values into one variable", () => {
    const result = exportMCPServers({ service: { url: endpoint + "?token=first-value&token=second-value" } }, "claude");
    if (result.text) {
      const values = new URL(JSON.parse(result.text).mcpServers.service.url).searchParams.getAll("token");
      expect(values[0]).not.toBe(values[1]);
    } else {
      expect(result.warnings.some((w) => w.blocked)).toBe(true);
    }
  });

  it("preserves a single-hyphen credential flag when substituting its value", () => {
    const result = exportMCPServers({ service: { command: "demo", args: ["-token=synthetic-inline-secret"] } }, "claude");
    if (result.text) expect(JSON.parse(result.text).mcpServers.service.args[0]).toMatch(/^-token=\$\{/);
    else expect(result.warnings.some((w) => w.blocked)).toBe(true);
  });

  it.each([
    "--header=Authorization: Bearer synthetic-secret",
    "Cookie: session=synthetic-secret",
    "https://user:synthetic-secret@example.invalid/mcp",
    "--endpoint=https://example.invalid/mcp?api_key=synthetic-secret",
    "API_KEY=synthetic-secret",
  ])("does not expose credentials embedded in an argument: %s", (arg) => {
    for (const target of ["pogo", "claude", "cursor", "vscode"] as MCPExportTarget[]) {
      const result = exportMCPServers({ service: { command: "demo", args: [arg] } }, target);
      expect(JSON.stringify(result)).not.toContain("synthetic-secret");
      if (target === "pogo") expect(result.warnings.some((w) => w.blocked)).toBe(true);
      else expect(result.vars).toHaveLength(1);
    }
  });

  it("reserves references used by argument credentials before generating names", () => {
    const result = exportMCPServers({
      second: { url: endpoint, bearer_token: "synthetic-literal" },
      existing: { command: "demo", args: ["--token", "${MCP_SECOND_TOKEN}"] },
    }, "claude");
    const servers = JSON.parse(result.text).mcpServers;
    expect(servers.second.headers.Authorization).not.toContain("${MCP_SECOND_TOKEN}");
    expect(servers.existing.args[1]).toBe("${MCP_SECOND_TOKEN}");
    expect(result.vars).toHaveLength(2);
  });

  it("does not show URL credentials in the connection selection label", () => {
    const label = exportConnectionLabel({ url: "https://user:synthetic-secret@example.invalid/mcp?token=synthetic-query#synthetic-fragment" });
    expect(label).toBe("https://example.invalid/mcp");
  });
});

describe("MCP export preserves identifiers and template boundaries", () => {
  it("preserves valid names that are special JavaScript object keys", () => {
    const servers = JSON.parse('{"__proto__":{"url":"https://example.invalid/mcp"},"constructor":{"url":"https://example.invalid/mcp"}}');
    const result = exportMCPServers(servers, "pogo");
    const actual = JSON.parse(result.text).servers;
    expect(Object.keys(actual)).toEqual(["__proto__", "constructor"]);
    expect(actual.__proto__.url).toBe(endpoint);
  });

  it("preserves a native variable boundary followed by an identifier suffix", () => {
    const result = exportMCPServers({ service: { url: endpoint, headers: { "X-Region": "${REGION}_suffix" } } }, "pogo");
    expect(JSON.parse(result.text).servers.service.headers["X-Region"]).toBe("${REGION}_suffix");
  });
});
