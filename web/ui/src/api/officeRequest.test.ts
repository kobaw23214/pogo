import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// Exercise the real authenticated HTTP response parser. No live Office engine
// or Go service is substituted by these transport tests.
const bridge = vi.hoisted(() => ({ enabled: false, request: vi.fn() }));
vi.mock("./delegated", () => ({ delegatedRequest: bridge.request, get isDelegatedEmbed() { return bridge.enabled; } }));
let client: typeof import("./client");
const fetchResponse = vi.fn();
beforeEach(async () => {
  vi.resetModules(); fetchResponse.mockReset(); bridge.enabled = false; bridge.request.mockReset();
  vi.stubGlobal("location", { origin: "https://pogo.test", href: "https://pogo.test/", search: "", pathname: "/" });
  vi.stubGlobal("sessionStorage", { getItem: () => "synthetic-account-token", setItem() {} });
  vi.stubGlobal("fetch", fetchResponse);
  const { setLocale } = await import("@/i18n");
  setLocale("zh-CN");
  client = await import("./client");
});
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); });

describe("Office scoped HTTP error responses", () => {
  it("extracts a structured backend message, code and HTTP status", async () => {
    fetchResponse.mockResolvedValue(new Response(JSON.stringify({ ok: false, error: { code: "EDITOR_ALREADY_CONNECTED", message: "当前文档已绑定其他编辑器实例。" } }), { status: 409 }));
    await expect(client.officeRequest("GET", "/api/office/sessions/a/config?instanceId=current"))
      .rejects.toMatchObject({ message: "当前文档已绑定其他编辑器实例。", code: "EDITOR_ALREADY_CONNECTED", status: 409 });
    expect(fetchResponse).toHaveBeenCalledTimes(1);
    expect(fetchResponse.mock.calls[0]![1].headers).toEqual({ Authorization: "Bearer synthetic-account-token", "Accept-Language": "zh-CN", "X-Pogo-Office-Origin": "https://pogo.test" });
  });
  it("retains plain authentication and legacy string errors", async () => {
    fetchResponse.mockResolvedValueOnce(new Response("Unauthorized", { status: 401 }));
    await expect(client.officeRequest("GET", "/api/office/config")).rejects.toMatchObject({ message: "Unauthorized", status: 401 });
    fetchResponse.mockResolvedValueOnce(new Response(JSON.stringify({ error: "操作被拒绝。", code: "DENIED" }), { status: 403 }));
    await expect(client.officeRequest("POST", "/office/bridge/a/c/ready", {})).rejects.toMatchObject({ message: "操作被拒绝。", code: "DENIED", status: 403 });
  });
  it("uses a readable HTTP fallback for a malformed structured message", async () => {
    fetchResponse.mockResolvedValue(new Response(JSON.stringify({ error: { code: "OUTCOME_UNKNOWN", message: { unexpected: true } } }), { status: 409 }));
    await expect(client.officeRequest("GET", "/api/office/config")).rejects.toMatchObject({ message: "HTTP 409", code: "OUTCOME_UNKNOWN", status: 409 });
  });
  it("keeps successful payloads and existing request body authentication unchanged", async () => {
    const reply = { session: { id: "document-a", ready: false }, instanceOwned: true };
    fetchResponse.mockResolvedValue(new Response(JSON.stringify(reply), { status: 200 }));
    await expect(client.officeRequest("POST", "/api/office/sessions", { path: "test.docx", conversation_id: "conversation-a", instanceId: "instance-a" })).resolves.toEqual(reply);
    const [path, options] = fetchResponse.mock.calls[0]!;
    expect(path).toBe("/api/office/sessions");
    expect(options.headers).toEqual({ Authorization: "Bearer synthetic-account-token", "Accept-Language": "zh-CN", "X-Pogo-Office-Origin": "https://pogo.test", "Content-Type": "application/json" });
    expect(JSON.parse(options.body)).toEqual({ path: "test.docx", conversation_id: "conversation-a", instanceId: "instance-a" });
  });
  it("preserves non-Office API string error handling", async () => {
    fetchResponse.mockResolvedValue(new Response(JSON.stringify({ error: "Model unavailable", code: "MODEL_UNAVAILABLE" }), { status: 503 }));
    await expect(client.api.models()).rejects.toMatchObject({ message: "Model unavailable", code: "MODEL_UNAVAILABLE", status: 503 });
    expect(fetchResponse.mock.calls[0]![1].headers).not.toHaveProperty("X-Pogo-Office-Origin");
  });
  it.each(["http://localhost:8080", "https://pogo.example:8443"])("carries the current access origin for a GET bootstrap at %s", async origin => {
    vi.stubGlobal("location", { origin, href: `${origin}/`, search: "", pathname: "/" });
    fetchResponse.mockResolvedValue(new Response(JSON.stringify({ browserURL: "/office/editor" }), { status: 200 }));
    await client.officeRequest("GET", "/api/office/sessions/a/config?instanceId=current");
    expect(fetchResponse.mock.calls[0]![0]).toBe("/api/office/sessions/a/config?instanceId=current");
    expect(fetchResponse.mock.calls[0]![1].headers).toEqual({ Authorization: "Bearer synthetic-account-token", "Accept-Language": "zh-CN", "X-Pogo-Office-Origin": origin });
  });
  it("rejects another API path without fetching it", () => {
    expect(() => client.officeRequest("GET", "/api/models")).toThrow("Invalid Office request path");
    expect(fetchResponse).not.toHaveBeenCalled();
  });
  it("opens the pinned library Office viewer through the real authenticated fetch and Office deadline", async () => {
    vi.useFakeTimers();
    const information = await import("./informationOfficeView");
    const entry = "inf_" + "a".repeat(32), reply = { session: { id: "viewer-a", readOnly: true } };
    fetchResponse.mockResolvedValue(new Response(JSON.stringify(reply), { status: 200 }));
    await expect(information.openInformationOfficeView(entry, 3, "instance-a", "operation-a")).resolves.toEqual(reply);
    const [path, options] = fetchResponse.mock.calls[0]!;
    expect(path).toBe(`/api/information/entries/${entry}/office-view`);
    expect(options.method).toBe("POST");
    expect(options.headers).toEqual({ Authorization: "Bearer synthetic-account-token", "Accept-Language": "zh-CN", "X-Pogo-Office-Origin": "https://pogo.test", "Content-Type": "application/json" });
    expect(JSON.parse(options.body)).toEqual({ version: 3, instanceId: "instance-a", operationId: "operation-a" });
    expect(options.signal).toBeInstanceOf(AbortSignal);
    expect(vi.getTimerCount()).toBe(0);
  });
  it("keeps the 310 second Office timeout for creating a library viewer without retrying it", async () => {
    vi.useFakeTimers();
    fetchResponse.mockImplementation(() => new Promise(() => {}));
    const request = client.officeRequest("POST", `/api/information/entries/inf_${"a".repeat(32)}/office-view`, { operationId: "original-operation" });
    const rejected = expect(request).rejects.toMatchObject({ code: "OFFICE_REQUEST_TIMEOUT", timeoutMs: 310_000 });
    await vi.advanceTimersByTimeAsync(309_999); expect((fetchResponse.mock.calls[0]![1].signal as AbortSignal).aborted).toBe(false);
    await vi.advanceTimersByTimeAsync(1); await rejected;
    expect(fetchResponse).toHaveBeenCalledTimes(1); expect(vi.getTimerCount()).toBe(0);
  });
  it.each([
    ["GET", `/api/information/entries/inf_${"a".repeat(32)}/office-view`],
    ["DELETE", `/api/information/entries/inf_${"a".repeat(32)}/office-view`],
    ["POST", `/api/information/entries/inf_${"a".repeat(32)}/office-view?version=1`],
    ["POST", `/api/information/entries/inf_${"A".repeat(32)}/office-view`],
    ["POST", `/api/information/entries/inf_${"a".repeat(31)}/office-view`],
    ["POST", `/api/information/entries/inf_${"a".repeat(32)}/office-view/extra`],
    ["POST", "/api/information/office-views"],
    ["POST", "/api/information/files"],
  ])("refuses non-viewer information requests (%s %s) before fetch", (method, path) => {
    expect(() => client.officeRequest(method, path, {})).toThrow("Invalid Office request path"); expect(fetchResponse).not.toHaveBeenCalled();
  });
  it("gives a stalled Office request 310 seconds and never replays a timed-out write", async () => {
    vi.useFakeTimers();
    fetchResponse.mockImplementation(() => new Promise(() => {}));
    const request = client.officeRequest("POST", "/api/office/sessions/a/commands", { operationId: "same-operation", operation: "sheet.apply", args: {} });
    const rejected = expect(request).rejects.toMatchObject({ name: "TimeoutError", code: "OFFICE_REQUEST_TIMEOUT", status: 504, timeoutMs: 310_000 });
    const signal = fetchResponse.mock.calls[0]![1].signal as AbortSignal;
    await vi.advanceTimersByTimeAsync(309_999);
    expect(signal.aborted).toBe(false);
    await vi.advanceTimersByTimeAsync(1); await rejected;
    expect(signal.aborted).toBe(true);
    expect(fetchResponse).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
  });
  it("keeps the deadline active while a successful response body is being consumed", async () => {
    vi.useFakeTimers();
    let finishBody!: (value: string) => void;
    fetchResponse.mockResolvedValue({ ok: true, status: 200, text: () => new Promise<string>(resolve => { finishBody = resolve; }) });
    const request = client.officeRequest("GET", "/api/office/config");
    await vi.advanceTimersByTimeAsync(40_000);
    expect(vi.getTimerCount()).toBe(1);
    finishBody(JSON.stringify({ enabled: true }));
    await expect(request).resolves.toEqual({ enabled: true });
    expect(vi.getTimerCount()).toBe(0);
  });
  it("times out a stalled response body as well as stalled headers", async () => {
    vi.useFakeTimers();
    fetchResponse.mockResolvedValue({ ok: false, status: 503, text: () => new Promise(() => {}) });
    const request = client.officeRequest("GET", "/api/office/config");
    const rejected = expect(request).rejects.toMatchObject({ code: "OFFICE_REQUEST_TIMEOUT" });
    await vi.advanceTimersByTimeAsync(310_000); await rejected;
    expect(fetchResponse).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
  });
  it("cancels promptly with the caller's reason without retrying or clearing a stage timeout identity", async () => {
    vi.useFakeTimers();
    fetchResponse.mockImplementation(() => new Promise(() => {}));
    const controller = new AbortController();
    const request = client.officeRequest("POST", "/api/office/sessions", { instanceId: "original-executor" }, controller.signal);
    const reason = Object.assign(new Error("stage expired"), { code: "OFFICE_STARTUP_TIMEOUT", phase: "opening" });
    const rejected = expect(request).rejects.toBe(reason);
    controller.abort(reason); await rejected;
    expect((fetchResponse.mock.calls[0]![1].signal as AbortSignal).aborted).toBe(true);
    expect(fetchResponse).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
  });
  it("does not fetch for an already cancelled caller", async () => {
    vi.useFakeTimers();
    const controller = new AbortController(); controller.abort();
    await expect(client.officeRequest("GET", "/api/office/config", undefined, controller.signal)).rejects.toMatchObject({ name: "AbortError" });
    expect(fetchResponse).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);
  });
  it("cleans deadline timers on both successful parsing and backend rejection", async () => {
    vi.useFakeTimers();
    fetchResponse.mockResolvedValueOnce(new Response(JSON.stringify({ enabled: true }), { status: 200 }));
    await client.officeRequest("GET", "/api/office/config");
    expect(vi.getTimerCount()).toBe(0);
    fetchResponse.mockResolvedValueOnce(new Response(JSON.stringify({ error: { code: "FILE_CHANGED", message: "File changed" } }), { status: 409 }));
    await expect(client.officeRequest("POST", "/api/office/versions/restore", {})).rejects.toMatchObject({ code: "FILE_CHANGED", status: 409 });
    expect(fetchResponse).toHaveBeenCalledTimes(2);
    expect(vi.getTimerCount()).toBe(0);
  });
  it("does not change the timeout behavior of unrelated API requests", async () => {
    vi.useFakeTimers();
    fetchResponse.mockResolvedValue(new Response(JSON.stringify({ models: [] }), { status: 200 }));
    await client.api.models();
    expect(fetchResponse.mock.calls[0]![1].signal).toBeUndefined();
    expect(vi.getTimerCount()).toBe(0);
  });
  it("keeps Office's existing delegated authorization boundary and passes cancellation without fetching directly", async () => {
    vi.useFakeTimers(); bridge.enabled = true;
    bridge.request.mockRejectedValue(Object.assign(new Error("Office is not supported by the parent bridge"), { status: 403 }));
    await expect(client.officeRequest("GET", "/api/office/config")).rejects.toMatchObject({ status: 403 });
    expect(bridge.request).toHaveBeenCalledWith("GET", "/api/office/config", undefined, expect.any(AbortSignal));
    expect(fetchResponse).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);
  });
});
