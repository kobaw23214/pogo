import { describe, expect, it } from "vitest";
import { mapImportServer, parseMCPImport, validateImportServer } from "./mcpImport";

describe("parseMCPImport", () => {
  it("accepts pogo's native servers root", () => {
    const r = parseMCPImport(JSON.stringify({ servers: { fs: { command: "npx", args: ["-y", "srv"] } } }));
    expect(r.ok).toBe(true);
    if (!r.ok) return;
    expect(r.entries).toHaveLength(1);
    expect(r.entries[0].error).toBeNull();
    expect(r.entries[0].config).toEqual({ command: "npx", args: ["-y", "srv"] });
  });

  it("accepts the ecosystem mcpServers root (Claude/Cursor style)", () => {
    const r = parseMCPImport(
      JSON.stringify({
        mcpServers: {
          remote: { url: "https://example.com/mcp", headers: { "X-Key": "1" } },
          local: { command: "uvx", env: { API_KEY: "$MY_KEY" } },
        },
      }),
    );
    expect(r.ok).toBe(true);
    if (!r.ok) return;
    expect(r.entries.map((e) => e.name)).toEqual(["remote", "local"]);
    expect(r.entries.every((e) => e.error === null)).toBe(true);
  });

  it("prefers servers over mcpServers when both exist", () => {
    const r = parseMCPImport(JSON.stringify({ servers: { a: { command: "x" } }, mcpServers: { b: { command: "y" } } }));
    expect(r.ok).toBe(true);
    if (!r.ok) return;
    expect(r.entries.map((e) => e.name)).toEqual(["a"]);
  });

  it("reports syntax errors and a missing servers object", () => {
    expect(parseMCPImport("{nope")).toMatchObject({ ok: false, reason: "syntax" });
    expect(parseMCPImport("{}")).toMatchObject({ ok: false, reason: "noServers" });
    expect(parseMCPImport("[]")).toMatchObject({ ok: false, reason: "noServers" });
  });
});

describe("mapImportServer", () => {
  it("maps Gemini aliases: httpUrl, timeout, includeTools/excludeTools", () => {
    const { config, ignored } = mapImportServer({
      httpUrl: "https://example.com/mcp",
      timeout: 60000,
      includeTools: ["read_*"],
      excludeTools: ["drop"],
    });
    expect(config).toEqual({
      url: "https://example.com/mcp",
      tool_timeout_ms: 60000,
      include_tools: ["read_*"],
      exclude_tools: ["drop"],
    });
    expect(ignored).toEqual([]);
  });

  it("maps Codex's bearer_token_env_var and the type:sse shorthand", () => {
    const { config } = mapImportServer({ url: "https://example.com/sse", type: "sse", bearer_token_env_var: "MCP_TOKEN" });
    expect(config).toEqual({ url: "https://example.com/sse", transport: "sse", bearer_token_env: "MCP_TOKEN" });
  });

  it("lets native fields beat their aliases regardless of order", () => {
    const { config } = mapImportServer({
      httpUrl: "https://alias.example/mcp",
      url: "https://native.example/mcp",
      timeout: 1000,
      tool_timeout_ms: 2000,
      enabled_tools: ["a"],
      include_tools: ["b"],
      bearer_token_env_var: "ALIAS",
      bearer_token_env: "NATIVE",
    });
    expect(config).toEqual({
      url: "https://native.example/mcp",
      tool_timeout_ms: 2000,
      include_tools: ["b"],
      bearer_token_env: "NATIVE",
    });
  });

  it("maps enabled:false and disabled:true onto the enabled tri-state", () => {
    expect(mapImportServer({ command: "x", enabled: false }).config.enabled).toBe(false);
    expect(mapImportServer({ command: "x", disabled: true }).config.enabled).toBe(false);
    // true is the default and stays unwritten, keeping the file clean.
    expect(mapImportServer({ command: "x", enabled: true }).config.enabled).toBeUndefined();
    expect(mapImportServer({ command: "x", disabled: false }).config.enabled).toBeUndefined();
  });

  it("collects unrecognized and mistyped fields as ignored", () => {
    const { config, ignored } = mapImportServer({
      command: "x",
      description: "a note",
      args: "not-an-array",
      alwaysAllow: ["y"],
    });
    expect(config).toEqual({ command: "x" });
    expect(ignored).toEqual(["description", "args", "alwaysAllow"]);
  });
});

describe("validateImportServer", () => {
  it("rejects a bad name and a missing/double endpoint", () => {
    expect(validateImportServer("bad name!", { command: "x" })?.code).toBe("name");
    expect(validateImportServer("ok", {})?.code).toBe("endpoint");
    expect(validateImportServer("ok", { command: "x", url: "https://e.com" })?.code).toBe("endpoint");
  });

  it("rejects cross-field pollution on both kinds", () => {
    expect(validateImportServer("ok", { command: "x", headers: { A: "b" } })).toMatchObject({
      code: "crossField",
      params: { fields: "headers" },
    });
    expect(validateImportServer("ok", { url: "https://e.com", env: { A: "b" } })?.code).toBe("crossField");
  });

  it("rejects bad url schemes, bearer names and tool patterns", () => {
    expect(validateImportServer("ok", { url: "ftp://e.com" })?.code).toBe("url");
    expect(validateImportServer("ok", { url: "https://e.com", bearer_token_env: "sk-live-token" })?.code).toBe("bearer");
    expect(validateImportServer("ok", { command: "x", include_tools: ["a*b"] })?.code).toBe("toolFilter");
    expect(validateImportServer("ok", { command: "x", include_tools: ["a.*"] })).toBeNull();
  });

  it("passes a literal bearer_token through, and rejects its misuse", () => {
    // Passthrough: a literal token survives the mapping untouched.
    const { config } = mapImportServer({ url: "https://e.com/mcp", bearer_token: "tok-abc-123" });
    expect(config.bearer_token).toBe("tok-abc-123");
    expect(validateImportServer("ok", config)).toBeNull();
    // A $-prefixed literal is a variable reference in the wrong field; both
    // forms at once violate the mutex; blank is no token at all.
    expect(validateImportServer("ok", { url: "https://e.com", bearer_token: "$MY_TOKEN" })?.code).toBe("bearerToken");
    expect(validateImportServer("ok", { url: "https://e.com", bearer_token: "   " })?.code).toBe("bearerToken");
    expect(
      validateImportServer("ok", { url: "https://e.com", bearer_token: "x", bearer_token_env: "MY_TOKEN" })?.code,
    ).toBe("bearerBoth");
    // And it is an http-side field, so a stdio entry carrying it is crossField.
    expect(validateImportServer("ok", { command: "x", bearer_token: "t" })).toMatchObject({
      code: "crossField",
      params: { fields: "bearer_token" },
    });
  });

  it("lets a non-object entry surface as notObject via parseMCPImport", () => {
    const r = parseMCPImport(JSON.stringify({ servers: { bad: "nope" } }));
    expect(r.ok).toBe(true);
    if (!r.ok) return;
    expect(r.entries[0]).toMatchObject({ name: "bad", config: null, error: { code: "notObject" } });
  });
});
