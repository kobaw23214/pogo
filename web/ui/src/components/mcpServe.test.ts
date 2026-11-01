import { describe, expect, it } from "vitest";
import { buildServeConfig, rowFromServeConfig, serveStatusParts, serveTokenMode, validateServeRow } from "./mcpServe";

describe("serveTokenMode / rowFromServeConfig", () => {
  it("infers the radio from whichever field is set", () => {
    expect(serveTokenMode(undefined)).toBe("off");
    expect(serveTokenMode({})).toBe("off");
    expect(serveTokenMode({ token_env: "MCP_TOKEN" })).toBe("env");
    expect(serveTokenMode({ token: "abc" })).toBe("direct");
    // Both set (a hand-edited file): the literal is the more explicit choice.
    expect(serveTokenMode({ token: "abc", token_env: "MCP_TOKEN" })).toBe("direct");
  });

  it("round-trips through buildServeConfig", () => {
    const row = rowFromServeConfig({
      enabled: true,
      token_env: "MCP_TOKEN",
      policy: "strict",
      max_concurrent: 4,
      submit_rpm: 30,
      max_waits: 16,
    });
    expect(buildServeConfig(row)).toEqual({
      enabled: true,
      token_env: "MCP_TOKEN",
      policy: "strict",
      max_concurrent: 4,
      submit_rpm: 30,
      max_waits: 16,
    });
  });
});

describe("buildServeConfig", () => {
  const base = {
    enabled: true,
    tokenEnv: " MCP_TOKEN ",
    token: " tok ",
    policy: "" as const,
    includeText: "",
    maxConcurrent: undefined,
    submitRpm: undefined,
    maxWaits: undefined,
  };

  it("emits exactly the mode's token field, never both", () => {
    expect(buildServeConfig({ ...base, tokenMode: "off" })).toEqual({ enabled: true });
    expect(buildServeConfig({ ...base, tokenMode: "env" })).toEqual({ enabled: true, token_env: "MCP_TOKEN" });
    expect(buildServeConfig({ ...base, tokenMode: "direct" })).toEqual({ enabled: true, token: "tok" });
  });

  it("always writes enabled — a saved off must not read as unset", () => {
    expect(buildServeConfig({ ...base, enabled: false, tokenMode: "off" })).toEqual({ enabled: false });
  });

  it("omits empty policy and quota fields so defaults apply", () => {
    const out = buildServeConfig({ ...base, tokenMode: "off", policy: "auto", maxConcurrent: 0, submitRpm: 60 });
    expect(out).toEqual({ enabled: true, policy: "auto", submit_rpm: 60 });
  });
});

describe("include_tools", () => {
  const base = {
    enabled: false,
    tokenMode: "off" as const,
    tokenEnv: "",
    token: "",
    policy: "" as const,
    maxConcurrent: undefined,
    submitRpm: undefined,
    maxWaits: undefined,
  };

  it("fills the form from the array (回填)", () => {
    const row = rowFromServeConfig({ include_tools: ["mal.*", "agent_task_status"] });
    expect(row.includeText).toBe("mal.*, agent_task_status");
  });

  it("saves the comma string back to an array, trimming and dropping empties", () => {
    const out = buildServeConfig({ ...base, includeText: " mal.*, , agent_* ," });
    expect(out.include_tools).toEqual(["mal.*", "agent_*"]);
  });

  it("omits the field entirely when the box is left empty (exposes all)", () => {
    const out = buildServeConfig({ ...base, includeText: "  , ," });
    expect(out).not.toHaveProperty("include_tools");
  });

  it("rejects glob shapes the server would refuse", () => {
    expect(validateServeRow({ ...base, includeText: "mal.*" })).toBeNull();
    expect(validateServeRow({ ...base, includeText: "a*b" })).toEqual({ code: "includeTools", param: "a*b" });
  });
});

describe("validateServeRow", () => {
  const base = { enabled: false, tokenEnv: "", token: "", policy: "" as const, includeText: "" } as const;

  it("accepts empty either way (no auth is legal)", () => {
    expect(validateServeRow({ ...base, tokenMode: "off", maxConcurrent: undefined, submitRpm: undefined, maxWaits: undefined })).toBeNull();
    expect(validateServeRow({ ...base, tokenMode: "env", maxConcurrent: undefined, submitRpm: undefined, maxWaits: undefined })).toBeNull();
    expect(validateServeRow({ ...base, tokenMode: "direct", maxConcurrent: undefined, submitRpm: undefined, maxWaits: undefined })).toBeNull();
  });

  it("rejects a variable value that is not a name", () => {
    const row = { ...base, tokenMode: "env" as const, tokenEnv: "sk-live", maxConcurrent: undefined, submitRpm: undefined, maxWaits: undefined };
    expect(validateServeRow(row)).toEqual({ code: "tokenEnv" });
  });

  it("rejects a $-prefixed or blank literal", () => {
    const common = { ...base, tokenMode: "direct" as const, maxConcurrent: undefined, submitRpm: undefined, maxWaits: undefined };
    expect(validateServeRow({ ...common, token: "$MCP_TOKEN" })).toEqual({ code: "token" });
    expect(validateServeRow({ ...common, token: "   " })).toEqual({ code: "token" });
    expect(validateServeRow({ ...common, token: "tok-1" })).toBeNull();
  });
});

describe("serveStatusParts", () => {
  it("defaults an absent state to off/standard/default", () => {
    expect(serveStatusParts(undefined)).toEqual({
      on: false,
      tokenRequired: false,
      policy: "standard",
      source: "default",
      restart: false,
      endpoints: [],
    });
  });

  it("passes a full state through", () => {
    expect(
      serveStatusParts({
        enabled: true,
        token_required: true,
        policy: "strict",
        source: "env",
        restart_required: true,
        endpoints: ["/mcp", "/sse"],
      }),
    ).toEqual({ on: true, tokenRequired: true, policy: "strict", source: "env", restart: true, endpoints: ["/mcp", "/sse"] });
  });
});
