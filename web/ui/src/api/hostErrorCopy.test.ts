import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setLocale } from "@/i18n";
import { hostAPIError, localizeHostError } from "./hostErrorCopy";

const fetchResponse = vi.fn();
beforeEach(() => {
  setLocale("en");
  fetchResponse.mockReset();
  vi.stubGlobal("location", { origin: "https://pogo.test", href: "https://pogo.test/", search: "", pathname: "/" });
  vi.stubGlobal("sessionStorage", { getItem: () => "fixture-token", setItem() {} });
  vi.stubGlobal("fetch", fetchResponse);
});
afterEach(() => { setLocale("zh-CN"); vi.unstubAllGlobals(); });

describe("host error localization boundaries", () => {
  it("translates an exact host error while preserving its original diagnostic", () => {
    const original = "Agent 不存在或不可访问";
    const error = hostAPIError(original);
    expect(error).toBeInstanceOf(Error);
    expect(error.originalMessage).toBe(original);
    expect(error.message).not.toBe(original);
    expect(error.message).not.toMatch(/\p{Script=Han}|hostErrors\.|\{p\d+\}/u);
  });

  it("keeps Chinese names and punctuation in string arguments verbatim", () => {
    const filename = "用户[资料](最终版){记录}@团队+100%.txt";
    const original = `附件 ${filename} 不存在或无法读取`;
    const result = localizeHostError(original);
    expect(result).toContain(filename);
    expect(result.replace(filename, "")).not.toMatch(/\p{Script=Han}|hostErrors\.|\{p\d+\}/u);
    expect(hostAPIError(original).originalMessage).toBe(original);
  });

  it("does not recursively translate a string argument that happens to match another host error", () => {
    const userName = "Agent 不存在或不可访问";
    const result = localizeHostError(`Skill 已停用或不可用：${userName}`);
    expect(result).toContain(userName);
    expect(result.replace(userName, "")).not.toMatch(/\p{Script=Han}/u);
  });

  it("retains each numeric argument and a quoted path exactly", () => {
    const memory = localizeHostError("Skill 声明需要 2048 MiB 内存，当前容器为分析预留的预算为 1536 MiB；请提高 POGO_MEMORY_LIMIT 后重启");
    expect(memory).toContain("2048");
    expect(memory).toContain("1536");
    expect(memory).toContain("POGO_MEMORY_LIMIT");
    expect(memory).not.toMatch(/\p{Script=Han}|\{p\d+\}/u);
    const path = JSON.stringify("资料/文件[1]\\备份.txt");
    const quoted = localizeHostError(`非法目录路径 ${path}`);
    expect(quoted).toContain(path);
    expect(quoted.replace(path, "")).not.toMatch(/\p{Script=Han}/u);
  });

  it("localizes nested wrapped host errors while retaining an external cause verbatim", () => {
    const nested = localizeHostError("MCP 配置尚未同步：无效的工作流定义：Agent 不存在或不可访问");
    expect(nested).not.toMatch(/\p{Script=Han}|hostErrors\.|\{p\d+\}/u);
    const external = "第三方返回 429：网关限流（trace=abc.123）";
    const wrapped = localizeHostError(`MCP 配置尚未同步：${external}`);
    expect(wrapped).toContain(external);
    expect(wrapped.replace(external, "")).not.toMatch(/\p{Script=Han}/u);
  });

  it("escapes punctuation in template literals rather than matching regex lookalikes", () => {
    const actual = localizeHostError("prefs.json 无法读取（permission denied），未保存更改");
    expect(actual).toContain("permission denied");
    expect(actual).not.toMatch(/\p{Script=Han}/u);
    const lookalike = "prefsXjson 无法读取（permission denied），未保存更改";
    expect(localizeHostError(lookalike)).toBe(lookalike);
  });

  it.each([
    "第三方服务报告中文错误：暂时无法处理此请求",
    "{" + '"error":"自定义中文描述"' + "}",
    "Gateway error: invalid request; trace=中文用户值",
    "plain external timeout",
  ])("preserves unknown external diagnostics: %s", original => {
    expect(localizeHostError(original)).toBe(original);
    expect(hostAPIError(original).originalMessage).toBe(original);
  });

  it("leaves Simplified Chinese copy byte-for-byte unchanged", () => {
    setLocale("zh-CN");
    const original = "MCP 配置尚未同步：附件 用户资料.txt 不存在或无法读取\n";
    expect(localizeHostError(original)).toBe(original);
    expect(hostAPIError(original)).toMatchObject({ message: original, originalMessage: original });
  });

  it.each(["附件超过 很多 MB 上限", "非法目录路径 未加引号的外部消息"])("does not treat an invalid Go argument shape as a host template: %s", message => {
    expect(localizeHostError(message)).toBe(message);
  });
});

describe("localized HTTP errors preserve machine-readable metadata", () => {
  const original = "Agent 不存在或不可访问";

  it("keeps a normal API error's status, code and complete original response", async () => {
    const payload = { error: original, code: "AGENT_NOT_FOUND", trace: "用户指定trace", retry_after_ms: 250 };
    fetchResponse.mockResolvedValueOnce(new Response(JSON.stringify(payload), { status: 404 }));
    const { api } = await import("./client");
    const error = await api.tools("session-one").catch(value => value);
    expect(error).toMatchObject({ status: 404, code: "AGENT_NOT_FOUND", originalMessage: original, details: payload });
    expect(error.message).not.toMatch(/\p{Script=Han}|\[object Object\]/u);
    expect(fetchResponse.mock.calls[0]![1]).toMatchObject({ headers: { "Accept-Language": "en" } });
  });

  it("retains nested errors for the Studio and Office/workflow transports", async () => {
    const payload = { error: { message: original, code: "PERMISSION_CHANGED", detail: "底层原始诊断" }, request_id: "req-a" };
    const { platformApi } = await import("./platform");
    const { agentWorkflowRequest } = await import("./client");
    fetchResponse.mockResolvedValueOnce(new Response(JSON.stringify(payload), { status: 409 }));
    const studio = await platformApi.list().catch(value => value);
    fetchResponse.mockResolvedValueOnce(new Response(JSON.stringify(payload), { status: 409 }));
    const workflow = await agentWorkflowRequest("POST", "/api/apps/agent-one/agent-workflows/read", {}).catch(value => value);
    for (const error of [studio, workflow]) {
      expect(error).toMatchObject({ status: 409, code: "PERMISSION_CHANGED", originalMessage: original, details: payload });
      expect(error.message).not.toMatch(/\p{Script=Han}|\[object Object\]/u);
    }
  });

  it("also accepts the nested error shape on the normal API transport", async () => {
    const payload = { error: { message: original, code: "AGENT_NOT_FOUND" }, request_id: "req-b" };
    fetchResponse.mockResolvedValueOnce(new Response(JSON.stringify(payload), { status: 404 }));
    const { api } = await import("./client");
    const error = await api.tools("session-one").catch(value => value);
    expect(error).toMatchObject({ status: 404, code: "AGENT_NOT_FOUND", originalMessage: original, details: payload });
    expect(error.message).not.toMatch(/\p{Script=Han}|\[object Object\]/u);
  });

  it("retains an unrecognized upstream error body without interpreting its user data", async () => {
    const original = "外部模型服务自定义错误：用户额度不足";
    fetchResponse.mockResolvedValueOnce(new Response(original, { status: 502 }));
    const { api } = await import("./client");
    const error = await api.tools("session-one").catch(value => value);
    expect(error).toMatchObject({ status: 502, message: original, originalMessage: original });
  });
});
