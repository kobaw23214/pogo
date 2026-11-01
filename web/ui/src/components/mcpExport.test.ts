import { describe, expect, it } from "vitest";
import {
  buildServeExportEntry,
  exportMCPServers,
  isSensitiveHeader,
  scanTemplateRefs,
  suggestServeURL,
  varRef,
} from "./mcpExport";
import { parseMCPImport } from "./mcpImport";

describe("varRef / scanTemplateRefs", () => {
  it("renders each target's interpolation syntax", () => {
    expect(varRef("pogo", "TOK")).toBe("$TOK");
    expect(varRef("claude", "TOK")).toBe("${TOK}");
    expect(varRef("cursor", "TOK")).toBe("${env:TOK}");
    expect(varRef("vscode", "TOK")).toBe("${env:TOK}");
  });

  it("scans pogo templates and rejects unknown '$' shapes", () => {
    expect(scanTemplateRefs("$A and ${B}")).toEqual(["A", "B"]);
    expect(scanTemplateRefs("plain")).toEqual([]);
    expect(scanTemplateRefs("$1bad")).toBeNull();
    expect(scanTemplateRefs("${UNCLOSED")).toBeNull();
    expect(scanTemplateRefs("{env:VAR}")).toEqual([]); // no '$' → literal, not ours
  });
});

describe("exportMCPServers: four target formats", () => {
  const remote = { url: "https://api.example.com/mcp", bearer_token_env: "SVC_TOKEN" };

  it("pogo: native servers root, bearer_token_env kept", () => {
    const r = exportMCPServers({ svc: remote }, "pogo");
    const doc = JSON.parse(r.text);
    expect(doc.servers.svc).toEqual({ url: "https://api.example.com/mcp", bearer_token_env: "SVC_TOKEN" });
    expect(r.vars).toEqual([{ name: "SVC_TOKEN", server: "svc", why: "bearer", detail: undefined }]);
  });

  it("claude: mcpServers root, type:http, Authorization Bearer ${VAR}", () => {
    const r = exportMCPServers({ svc: remote }, "claude");
    const doc = JSON.parse(r.text);
    expect(doc.mcpServers.svc).toEqual({
      type: "http",
      url: "https://api.example.com/mcp",
      headers: { Authorization: "Bearer ${SVC_TOKEN}" },
    });
  });

  it("cursor: mcpServers root, ${env:VAR}, no type for streamable", () => {
    const r = exportMCPServers({ svc: remote }, "cursor");
    const doc = JSON.parse(r.text);
    expect(doc.mcpServers.svc).toEqual({
      url: "https://api.example.com/mcp",
      headers: { Authorization: "Bearer ${env:SVC_TOKEN}" },
    });
    expect(doc.mcpServers.svc.type).toBeUndefined();
  });

  it("vscode: servers root, type:http, ${env:VAR}", () => {
    const r = exportMCPServers({ svc: remote }, "vscode");
    const doc = JSON.parse(r.text);
    expect(doc.servers.svc).toEqual({
      type: "http",
      url: "https://api.example.com/mcp",
      headers: { Authorization: "Bearer ${env:SVC_TOKEN}" },
    });
  });

  it("keeps SSE explicit in every target", () => {
    const sse = { url: "https://api.example.com/sse", transport: "sse" as const };
    expect(JSON.parse(exportMCPServers({ s: sse }, "pogo").text).servers.s.transport).toBe("sse");
    expect(JSON.parse(exportMCPServers({ s: sse }, "claude").text).mcpServers.s.type).toBe("sse");
    expect(JSON.parse(exportMCPServers({ s: sse }, "cursor").text).mcpServers.s.type).toBe("sse");
    expect(JSON.parse(exportMCPServers({ s: sse }, "vscode").text).servers.s.type).toBe("sse");
  });

  it("stdio: command/args/env adapt per target", () => {
    const stdio = { command: "npx", args: ["-y", "srv"], env: { API_KEY: "$MY_KEY", MODE: "fast" } };
    const claude = JSON.parse(exportMCPServers({ s: stdio }, "claude").text).mcpServers.s;
    expect(claude).toEqual({
      type: "stdio",
      command: "npx",
      args: ["-y", "srv"],
      env: { API_KEY: "${MY_KEY}", MODE: "${MODE}" },
    });
    const cursor = JSON.parse(exportMCPServers({ s: stdio }, "cursor").text).mcpServers.s;
    expect(cursor.type).toBeUndefined();
    expect(cursor.env).toEqual({ API_KEY: "${env:MY_KEY}", MODE: "${env:MODE}" });
    const r = exportMCPServers({ s: stdio }, "cursor");
    expect(r.vars.map((v) => v.name)).toEqual(["MY_KEY", "MODE"]);
  });
});

describe("exportMCPServers: credentials never leak", () => {
  it("literal bearer_token becomes a derived variable, value nowhere in text or warnings", () => {
    const r = exportMCPServers({ svc: { url: "https://x.example/mcp", bearer_token: "super-secret-value" } }, "claude");
    expect(r.text).not.toContain("super-secret-value");
    expect(JSON.stringify(r.warnings)).not.toContain("super-secret-value");
    const doc = JSON.parse(r.text);
    expect(doc.mcpServers.svc.headers.Authorization).toBe("Bearer ${MCP_SVC_TOKEN}");
    expect(r.vars).toEqual([{ name: "MCP_SVC_TOKEN", server: "svc", why: "bearer", detail: undefined }]);
    // pogo target converts the literal to bearer_token_env, never both
    const p = exportMCPServers({ svc: { url: "https://x.example/mcp", bearer_token: "super-secret-value" } }, "pogo");
    expect(p.text).not.toContain("super-secret-value");
    const pdoc = JSON.parse(p.text);
    expect(pdoc.servers.svc.bearer_token_env).toBe("MCP_SVC_TOKEN");
    expect(pdoc.servers.svc.bearer_token).toBeUndefined();
  });

  it("sensitive header literals templatize, ordinary headers pass through", () => {
    expect(isSensitiveHeader("Authorization")).toBe(true);
    expect(isSensitiveHeader("X-Api-Key")).toBe(true);
    expect(isSensitiveHeader("X-Tenant")).toBe(false);
    const r = exportMCPServers(
      { svc: { url: "https://x.example/mcp", headers: { "X-Api-Key": "key-plaintext-123", "X-Tenant": "acme" } } },
      "cursor",
    );
    expect(r.text).not.toContain("key-plaintext-123");
    const doc = JSON.parse(r.text);
    expect(doc.mcpServers.svc.headers["X-Api-Key"]).toBe("${env:MCP_SVC_X_API_KEY}");
    expect(doc.mcpServers.svc.headers["X-Tenant"]).toBe("acme");
  });

  it("same-name collisions get suffixed variable names", () => {
    const r = exportMCPServers(
      {
        "a-b": { url: "https://x.example/mcp", bearer_token: "tok1" },
        a_b: { url: "https://y.example/mcp", bearer_token: "tok2" },
      },
      "claude",
    );
    expect(r.text).not.toContain("tok1");
    expect(r.text).not.toContain("tok2");
    const names = r.vars.map((v) => v.name);
    expect(names).toEqual(["MCP_A_B_TOKEN", "MCP_A_B_TOKEN_2"]);
  });

  it("URL userinfo blocks the entry; secret query templatizes for claude, blocks for pogo", () => {
    const ui = exportMCPServers({ svc: { url: "https://user:pw@x.example/mcp" } }, "claude");
    expect(ui.servers).toEqual([]);
    expect(ui.text).toBe("");
    expect(ui.warnings).toEqual([{ code: "urlUserinfo", params: { name: "svc" }, blocked: true }]);
    expect(JSON.stringify(ui)).not.toContain("user:pw");

    const q = exportMCPServers({ svc: { url: "https://x.example/mcp?api_key=sekrit&region=eu" } }, "claude");
    expect(q.text).not.toContain("sekrit");
    const doc = JSON.parse(q.text);
    expect(doc.mcpServers.svc.url).toBe("https://x.example/mcp?api_key=${MCP_SVC_API_KEY}&region=eu");
    expect(q.vars[0]).toMatchObject({ name: "MCP_SVC_API_KEY", why: "urlQuery" });

    const qp = exportMCPServers({ svc: { url: "https://x.example/mcp?api_key=sekrit" } }, "pogo");
    expect(qp.servers).toEqual([]);
    expect(qp.warnings[0].code).toBe("urlQueryPogo");
    expect(qp.text).not.toContain("sekrit");
  });

  it("credential-shaped args templatize for claude and block for pogo", () => {
    const stdio = { command: "srv", args: ["--api-key=abc123", "--token", "tok-value", "--verbose"] };
    const r = exportMCPServers({ s: stdio }, "claude");
    expect(r.text).not.toContain("abc123");
    expect(r.text).not.toContain("tok-value");
    const doc = JSON.parse(r.text);
    expect(doc.mcpServers.s.args).toEqual([
      "--api-key=${MCP_S_API_KEY}",
      "--token",
      "${MCP_S_TOKEN}",
      "--verbose",
    ]);
    const p = exportMCPServers({ s: stdio }, "pogo");
    expect(p.servers).toEqual([]);
    expect(p.warnings[0].code).toBe("argCredsPogo");
    expect(p.text).not.toContain("abc123");
  });

  it("args credential templates reuse the existing variable name", () => {
    const r = exportMCPServers({ s: { command: "srv", args: ["--token", "$EXISTING"] } }, "vscode");
    const doc = JSON.parse(r.text);
    expect(doc.servers.s.args).toEqual(["--token", "${env:EXISTING}"]);
    expect(r.vars.map((v) => v.name)).toEqual(["EXISTING"]);
  });

  it("unrecognized variable syntax blocks with an explicit warning, never empty", () => {
    const r = exportMCPServers({ s: { command: "x", env: { KEY: "${BROKEN" } } }, "pogo");
    expect(r.servers).toEqual([]);
    expect(r.warnings).toEqual([{ code: "varSyntax", params: { name: "s", field: "env.KEY" }, blocked: true }]);
    expect(r.text).toBe("");
  });
});

describe("exportMCPServers: fidelity and dropped settings", () => {
  it("foreign formats report dropped filters/timeouts/flags per field", () => {
    const sc = {
      url: "https://x.example/mcp",
      include_tools: ["read_*"],
      exclude_tools: ["drop"],
      tool_timeout_ms: 60000,
      trust: true,
      required: true,
    };
    const r = exportMCPServers({ svc: sc }, "claude");
    const doc = JSON.parse(r.text);
    expect(doc.mcpServers.svc.include_tools).toBeUndefined();
    expect(r.warnings).toEqual([
      {
        code: "fieldsDropped",
        params: { name: "svc", fields: "include_tools, exclude_tools, tool_timeout_ms, trust, required" },
      },
    ]);
  });

  it("disabled entries stay disabled for pogo, are omitted with warning elsewhere", () => {
    const servers = { off: { enabled: false, url: "https://x.example/mcp" } };
    const p = exportMCPServers(servers, "pogo");
    expect(JSON.parse(p.text).servers.off.enabled).toBe(false);
    for (const t of ["claude", "cursor", "vscode"] as const) {
      const r = exportMCPServers(servers, t);
      expect(r.servers).toEqual([]);
      expect(r.warnings).toEqual([{ code: "disabledOmitted", params: { name: "off" }, blocked: true }]);
    }
  });

  it("pogo export round-trips through parseMCPImport preserving fields", () => {
    const servers = {
      fs: {
        enabled: false,
        command: "npx",
        args: ["-y", "srv"],
        env: { API_KEY: "$MY_KEY" },
        cwd: "/work",
        startup_timeout_ms: 5000,
        include_tools: ["read_*"],
        trust: true,
      },
      rem: { url: "https://x.example/mcp", transport: "sse" as const, bearer_token_env: "TOK", exclude_tools: ["x"] },
    };
    const r = exportMCPServers(servers, "pogo");
    const back = parseMCPImport(r.text);
    expect(back.ok).toBe(true);
    if (!back.ok) return;
    expect(back.entries.every((e) => e.error === null)).toBe(true);
    expect(back.entries[0].config).toEqual(servers.fs);
    expect(back.entries[1].config).toEqual(servers.rem);
  });

  it("invalid remote URL blocks the entry", () => {
    const r = exportMCPServers({ svc: { url: "ftp://x" } }, "pogo");
    expect(r.servers).toEqual([]);
    expect(r.warnings[0].code).toBe("badUrl");
  });
});

describe("serve export helpers", () => {
  it("suggestServeURL joins reverse-proxy prefixes without doubling slashes", () => {
    expect(suggestServeURL(["/mcp", "/sse"], { origin: "https://host", pathname: "/" })).toBe("https://host/mcp");
    expect(suggestServeURL(["/mcp"], { origin: "https://host", pathname: "/pogo/" })).toBe("https://host/pogo/mcp");
    expect(suggestServeURL(["/sse"], { origin: "https://host/", pathname: "/pogo" })).toBe("https://host/pogo/sse");
    expect(suggestServeURL(undefined, { origin: "https://host", pathname: "/" })).toBe("https://host/mcp");
    expect(suggestServeURL(["/mcp"], { origin: "https://host", pathname: "/share/abc" })).toBe("https://host/mcp");
  });

  it("suggestServeURL strips the app's own routes instead of prefixing them", () => {
    // The export dialog opens on /manage/mcp; the URL must be origin + /mcp,
    // never /manage/mcp/mcp (an HTML page, which breaks the handshake with
    // "unsupported content type text/html").
    expect(suggestServeURL(["/mcp"], { origin: "http://192.168.225.101:19777", pathname: "/manage/mcp" })).toBe("http://192.168.225.101:19777/mcp");
    expect(suggestServeURL(["/mcp"], { origin: "https://host", pathname: "/manage" })).toBe("https://host/mcp");
    expect(suggestServeURL(["/mcp"], { origin: "https://host", pathname: "/a/ag_123" })).toBe("https://host/mcp");
    expect(suggestServeURL(["/mcp"], { origin: "https://host", pathname: "/embed/" })).toBe("https://host/mcp");
    // A reverse-proxy prefix survives ahead of a known route.
    expect(suggestServeURL(["/mcp"], { origin: "https://host", pathname: "/pogo/manage/mcp" })).toBe("https://host/pogo/mcp");
    expect(suggestServeURL(["/sse"], { origin: "https://host", pathname: "/pogo/a/ag_1" })).toBe("https://host/pogo/sse");
  });

  it("buildServeExportEntry exports only the connection definition, token as variable name", () => {
    const m = buildServeExportEntry({ name: "pogo-local", url: "http://127.0.0.1:7778/mcp", tokenRequired: true, tokenVar: "POGO_MCP_TOKEN" });
    expect(m).toEqual({ "pogo-local": { url: "http://127.0.0.1:7778/mcp", bearer_token_env: "POGO_MCP_TOKEN" } });
    // and it flows through the target conversion
    const r = exportMCPServers(m, "claude");
    expect(JSON.parse(r.text).mcpServers["pogo-local"].headers.Authorization).toBe("Bearer ${POGO_MCP_TOKEN}");
    const open = buildServeExportEntry({ name: "p", url: "http://h/mcp", tokenRequired: false, tokenVar: "IGNORED" });
    expect(open.p.bearer_token_env).toBeUndefined();
  });
});
