import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createSSRApp, effectScope, h, nextTick, reactive, type ComputedRef, type Ref } from "vue";
import { renderToString } from "vue/server-renderer";
import type { ConfigMetadataProbeRequest, ConfigProbeResult } from "@/api/types";
import type { ModelMetadataPreset, ModelMetadataRow } from "./modelMetadata";
import messages from "@/i18n/messages/modelMetadata";

const host = vi.hoisted(() => ({ query: vi.fn(), save: vi.fn(), generate: vi.fn(), vision: vi.fn(), success: vi.fn(), failure: vi.fn(), cleanups: [] as (() => void)[] }));
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), useSSRContext: () => ({ modules: new Set() }), onBeforeUnmount: (cleanup: () => void) => host.cleanups.push(cleanup) }));
vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string) => {
  const path = key.replace(/^modelMetadata\./, "").split(".");
  let value: unknown = messages.messages["zh-CN"];
  for (const part of path) value = value && typeof value === "object" ? (value as Record<string, unknown>)[part] : undefined;
  return typeof value === "string" ? value : key;
} }) }));
vi.mock("element-plus", () => ({ ElMessage: { success: host.success, error: host.failure } }));
vi.mock("@/api/client", () => ({ api: { probeConfigMetadata: host.query, saveConfig: host.save, probeConfig: host.generate, visionProbe: host.vision } }));

import ModelMetadataProbe from "./ModelMetadataProbe.vue";

interface Provider { name: string; baseUrl: string; keyEnv: string; keyValue: string; resolvedKey?: string; headers: { k: string; v: string }[] }
interface Props { provider: Provider; model: string }
interface Controller {
  preset: Ref<ModelMetadataPreset>; queryModel: Ref<string>; busy: Ref<boolean>; result: Ref<ConfigProbeResult | null>; error: Ref<string>;
  query: ComputedRef<{ request: unknown; error: string }>; canSend: ComputedRef<boolean>; curl: ComputedRef<string>;
  detected: ComputedRef<{ rows: ModelMetadataRow[]; validJSON: boolean }>; prettyBody: ComputedRef<string>; showUnknown: ComputedRef<boolean>;
  send(): Promise<void>; copy(): Promise<void>;
}
const scopes: ReturnType<typeof effectScope>[] = [];
const provider = (): Provider => ({ name: "intranet", baseUrl: "http://example.invalid/gateway/team/compatible/v1/", keyEnv: "PRIVATE_MODEL_KEY", keyValue: "typed-fixture", resolvedKey: "saved-fixture", headers: [{ k: " X-Tenant ", v: "synthetic-tenant" }] });
const response = (extra: Partial<ConfigProbeResult> = {}): ConfigProbeResult => ({ ok: true, status: 200, status_text: "200 OK", body: JSON.stringify({ data: [{ id: "private-model", max_model_len: 32768, max_output_tokens: 8192 }] }), ...extra });
function deferred<T>() { let resolve!: (value: T) => void, reject!: (reason: unknown) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; }
function mount(extra: Partial<Props> = {}) {
  const props = reactive<Props>({ provider: provider(), model: "private-model", ...extra }), scope = effectScope(); scopes.push(scope);
  const c = scope.run(() => (ModelMetadataProbe as unknown as { setup(props: unknown, context: unknown): Controller }).setup(props, { expose() {} }))!;
  return { c, props };
}
async function render(c: Controller, props: Props) {
  const component = { ...ModelMetadataProbe, setup: () => c };
  return renderToString(createSSRApp({ render: () => h(component as Parameters<typeof h>[0], props) }));
}
beforeEach(() => { for (const fn of [host.query, host.save, host.generate, host.vision, host.success, host.failure]) fn.mockReset(); host.query.mockResolvedValue(response()); });
afterEach(() => { for (const cleanup of host.cleanups.splice(0)) cleanup(); for (const scope of scopes.splice(0)) scope.stop(); vi.unstubAllGlobals(); });

describe("Model metadata inspection remains separate from model configuration", () => {
  it("does not send on open and previews a fixed metadata GET preserving the gateway prefix", () => {
    const { c } = mount();
    expect(host.query).not.toHaveBeenCalled(); expect(c.preset.value).toBe("vllm");
    expect(c.curl.value).toContain("curl -X GET 'http://example.invalid/gateway/team/compatible/v1/models'");
    expect(c.curl.value).not.toContain("chat/completions"); expect(c.curl.value).not.toContain("ping");
    expect(c.curl.value).not.toContain("-d "); expect(c.canSend.value).toBe(true);
  });

  it("displays returned values without mutating frozen provider/model settings or calling a save or generation API", async () => {
    const original = provider(); Object.freeze(original.headers[0]); Object.freeze(original.headers); Object.freeze(original);
    const { c, props } = mount({ provider: original }), before = JSON.stringify(props);
    await c.send();
    expect(host.query).toHaveBeenCalledExactlyOnceWith({ name: "intranet", base_url: "http://example.invalid/gateway/team/compatible/v1/", key_env: "PRIVATE_MODEL_KEY", key: "typed-fixture", model: "private-model", headers: { "X-Tenant": "synthetic-tenant" }, preset: "vllm" }, expect.any(AbortSignal));
    expect(c.detected.value.rows).toContainEqual(expect.objectContaining({ model: "private-model", contextWindow: 32768, maxOutputTokens: 8192, kind: "runtime" }));
    expect(JSON.stringify(props)).toBe(before); expect(host.save).not.toHaveBeenCalled(); expect(host.generate).not.toHaveBeenCalled(); expect(host.vision).not.toHaveBeenCalled();
    const html = await render(c, props); expect(html).toContain("32,768"); expect(html).toContain("8,192");
    expect(html).toContain("仅查询展示"); expect(html).toContain("不修改或回填任何配置");
    expect(html).not.toContain("<button"); expect(html).not.toContain("应用配置"); expect(html).not.toContain("保存配置");
  });

  it("uses a local Ollama query model without changing the selected configured model", async () => {
    host.query.mockResolvedValueOnce(response({ body: JSON.stringify({ model_info: { "general.architecture": "qwen2", "qwen2.context_length": 131072 }, parameters: "num_ctx 8192" }) }));
    const { c, props } = mount(); const before = JSON.stringify(props);
    c.preset.value = "ollama-show"; c.queryModel.value = "analysis:local"; await c.send();
    expect(host.query).toHaveBeenCalledWith(expect.objectContaining({ model: "analysis:local", preset: "ollama-show" }), expect.any(AbortSignal));
    expect(c.curl.value).toContain("/gateway/team/compatible/api/show"); expect(c.curl.value).toContain("-d '{\"model\":\"analysis:local\"}'");
    expect(JSON.stringify(props)).toBe(before); expect(props.model).toBe("private-model"); expect(host.save).not.toHaveBeenCalled();
    const html = await render(c, props); expect(html).toMatch(/<td[^>]*>analysis:local<\/td>/); expect(html).not.toMatch(/<td[^>]*>private-model<\/td>/);
  });

  it("refreshes its local query and clears detection when the parent selects another model, without writing provider settings", async () => {
    const { c, props } = mount(); await c.send(); expect(c.result.value).not.toBeNull();
    const beforeProvider = JSON.stringify(props.provider); props.model = "second-configured-model"; await nextTick();
    expect(c.queryModel.value).toBe("second-configured-model"); expect(c.result.value).toBeNull();
    expect(JSON.stringify(props.provider)).toBe(beforeProvider); expect(host.save).not.toHaveBeenCalled();
  });

  it("cancels and rejects an old response when the service preset changes", async () => {
    const pending = deferred<ConfigProbeResult>(); host.query.mockReturnValueOnce(pending.promise);
    const { c } = mount(); const sending = c.send(), signal = host.query.mock.calls[0]![1] as AbortSignal;
    expect(c.busy.value).toBe(true); await c.send(); expect(host.query).toHaveBeenCalledOnce();
    c.preset.value = "llamacpp"; expect(signal.aborted).toBe(true); expect(c.busy.value).toBe(false);
    pending.resolve(response()); await sending; expect(c.result.value).toBeNull(); expect(c.detected.value.rows).toEqual([]);
  });

  it("clears prior results and ignores stale errors if the provider address changes while a request is pending", async () => {
    const { c, props } = mount(); await c.send(); expect(c.result.value).not.toBeNull();
    const pending = deferred<ConfigProbeResult>(); host.query.mockReturnValueOnce(pending.promise); const sending = c.send(), signal = host.query.mock.calls[1]![1] as AbortSignal;
    props.provider.baseUrl = "http://second.invalid/v1"; expect(signal.aborted).toBe(true); expect(c.result.value).toBeNull();
    pending.reject(new Error("late old connection error")); await sending; expect(c.error.value).toBe(""); expect(c.result.value).toBeNull();
    expect(c.curl.value).toContain("http://second.invalid/v1/models");
  });

  it("aborts on dialog-component unmount and does not expose a late result", async () => {
    const pending = deferred<ConfigProbeResult>(); host.query.mockReturnValueOnce(pending.promise);
    const { c } = mount(); const sending = c.send(), signal = host.query.mock.calls[0]![1] as AbortSignal;
    host.cleanups.pop()!(); expect(signal.aborted).toBe(true); pending.resolve(response()); await sending;
    expect(c.result.value).toBeNull(); expect(c.error.value).toBe(""); expect(host.save).not.toHaveBeenCalled();
  });

  it.each([{ ok: false, status: 403, status_text: "403 Forbidden" }, { ok: true, truncated: true }])("never treats a failed or incomplete metadata reply as configuration: %j", async extra => {
    host.query.mockResolvedValueOnce(response(extra)); const { c, props } = mount(); await c.send();
    expect(c.detected.value.rows).toEqual([]); const html = await render(c, props);
    expect(html).not.toContain("<table"); expect(html).toContain("查看原始响应");
    if (extra.truncated) expect(html).toContain("不从不完整内容识别配置");
  });

  it("shows an unknown result for a valid model list without explicit limits rather than guessing from the model name", async () => {
    host.query.mockResolvedValueOnce(response({ body: JSON.stringify({ data: [{ id: "model-128k" }] }) }));
    const { c, props } = mount(); await c.send(); expect(c.showUnknown.value).toBe(true);
    const html = await render(c, props); expect(html).toContain("不推测数值"); expect(html).not.toContain("<table"); expect(host.save).not.toHaveBeenCalled();
  });

  it("keeps a non-JSON error response inert in the rendered raw response", async () => {
    host.query.mockResolvedValueOnce(response({ ok: false, body: "<script>alert('fixture')</script> gateway failed" }));
    const { c, props } = mount(); await c.send(); const html = await render(c, props);
    expect(html).toContain("&lt;script&gt;"); expect(html).not.toContain("<script>"); expect(c.detected.value.rows).toEqual([]);
  });

  it("blocks invalid addresses and an empty Ollama model without submitting a request", async () => {
    const { c, props } = mount(); props.provider.baseUrl = "file:///tmp/model"; expect(c.canSend.value).toBe(false); await c.send();
    props.provider.baseUrl = "http://example.invalid/v1"; c.preset.value = "ollama-show"; c.queryModel.value = " ";
    expect(c.query.value.error).toContain("请填写本次要查询"); expect(c.canSend.value).toBe(false); await c.send(); expect(host.query).not.toHaveBeenCalled();
  });

  it("shell-quotes query data and custom headers literally, and never interprets configured text as shell code", async () => {
    const { c, props } = mount(); props.provider.keyValue = "fixture'$(do-not-run)`literal`";
    props.provider.headers = [{ k: "X-Literal", v: "owner's $(never-run)" }]; c.preset.value = "ollama-show"; c.queryModel.value = "owner's-model";
    expect(c.curl.value).toContain("'Authorization: Bearer fixture'\\''$(do-not-run)`literal`'");
    expect(c.curl.value).toContain("'X-Literal: owner's".replace("owner's", "owner'\\''s"));
    expect(c.curl.value).toContain("owner'\\''s-model"); expect(c.curl.value).not.toContain("\n+  ");
    await c.send(); const sent = host.query.mock.calls[0]![0] as ConfigMetadataProbeRequest;
    expect(sent.key).toBe(props.provider.keyValue); expect(sent.model).toBe("owner's-model");
  });

  it("copies only the displayed metadata request and does not submit or persist anything", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined); vi.stubGlobal("navigator", { clipboard: { writeText } });
    const { c } = mount(); await c.copy(); expect(writeText).toHaveBeenCalledExactlyOnceWith(c.curl.value);
    expect(host.query).not.toHaveBeenCalled(); expect(host.save).not.toHaveBeenCalled(); expect(host.generate).not.toHaveBeenCalled();
  });
});
