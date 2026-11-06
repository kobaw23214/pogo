import { afterEach, describe, expect, it } from "vitest";
import { buildCurlPreview, sq } from "./mcpCurl";
import { setLocale } from "@/i18n";
afterEach(() => setLocale("zh-CN"));

describe("sq", () => {
  it("wraps in single quotes and escapes embedded ones POSIX-style", () => {
    expect(sq("plain")).toBe("'plain'");
    expect(sq("it's")).toBe("'it'\\''s'");
    expect(sq("a'b'c")).toBe("'a'\\''b'\\''c'");
  });
});

describe("buildCurlPreview — streamable http", () => {
  const out = buildCurlPreview({
    url: "https://example.com/mcp",
    bearer_token_env: "MY_TOKEN",
    headers: { "X-Team": "sec ops" },
  });

  it("renders the handshake and the tools/list as two calls", () => {
    expect(out).toContain("curl -i -N -X POST 'https://example.com/mcp'");
    expect(out).toContain('"method":"initialize"');
    expect(out).toContain('"method":"tools/list"');
    expect(out).toContain("Mcp-Session-Id");
  });

  it("carries the bearer by variable name and the custom header verbatim", () => {
    expect(out).toContain("-H 'Authorization: Bearer $MY_TOKEN'");
    expect(out).toContain("-H 'X-Team: sec ops'");
  });

  it("never knows a token value — only the $NAME reference exists", () => {
    // The config type has no field for a token value; the board can therefore
    // only ever render the variable reference.
    expect(out).not.toMatch(/Bearer (?!\$)\S/);
    expect(out).toContain("Bearer $MY_TOKEN");
  });

  it("masks a literal bearer token as *** and never prints it", () => {
    const masked = buildCurlPreview({ url: "https://example.com/mcp", bearer_token: "secret-value-123" });
    expect(masked).toContain("-H 'Authorization: Bearer ***'");
    expect(masked).not.toContain("secret-value-123");
    // A literal wins over a variable when both are set (they are mutually
    // exclusive in the file; the board declines to pick the leakable one).
    const both = buildCurlPreview({
      url: "https://example.com/mcp",
      bearer_token: "secret-value-123",
      bearer_token_env: "MY_TOKEN",
    });
    expect(both).toContain("Bearer ***");
    expect(both).not.toContain("secret-value-123");
  });
});

describe("buildCurlPreview — legacy sse", () => {
  const out = buildCurlPreview({
    url: "https://example.com/sse",
    transport: "sse",
    bearer_token_env: "SSE_TOKEN",
  });

  it("is the two-endpoint dance: GET stream first, POSTs to the handed-back path", () => {
    expect(out).toContain("curl -N 'https://example.com/sse'");
    expect(out).toContain("-H 'Accept: text/event-stream'");
    expect(out).toContain("endpoint");
    expect(out).toContain("curl -N -X POST 'https://example.com<post-path>'");
    expect(out).toContain('"method":"initialize"');
    expect(out).toContain('"method":"tools/list"');
  });
});

describe("MCP curl preview language", () => {
  it("updates explanatory comments without changing the executable request", () => {
    const config = { url: "https://example.com/mcp", bearer_token_env: "MY_TOKEN" };
    setLocale("zh-CN"); const chinese = buildCurlPreview(config);
    setLocale("en"); const english = buildCurlPreview(config);
    expect(chinese).toContain("列出工具");
    expect(english).toContain("List tools");
    expect(english).not.toMatch(/\p{Script=Han}/u);
    const commands = (value: string) => value.split("\n").filter(line => !line.trimStart().startsWith("#"));
    expect(commands(english)).toEqual(commands(chinese));
    expect(english).toContain("Mcp-Session-Id: <MCP_SESSION_ID>");
  });
});

describe("buildCurlPreview — stdio", () => {
  const out = buildCurlPreview({
    command: "npx",
    args: ["-y", "@modelcontextprotocol/server-filesystem", "/data dir"],
    env: { API_KEY: "$MY_API_KEY" },
    cwd: "/srv/work",
  });

  it("shows the spawned command line with env prefixes and a cwd note", () => {
    expect(out).toContain("cd '/srv/work'");
    expect(out).toContain("API_KEY='$MY_API_KEY' npx -y @modelcontextprotocol/server-filesystem '/data dir'");
  });

  it("has no curl in it", () => {
    expect(out).not.toContain("curl -");
  });

  it("quotes only the args that need it", () => {
    const plain = buildCurlPreview({ command: "uvx", args: ["srv"] });
    expect(plain).toContain("uvx srv");
    expect(plain).not.toContain("'uvx'");
  });
});
