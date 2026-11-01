import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { OFFICE_STARTUP_TIMEOUT_MS } from "./officeStartup";

vi.mock("@/api/client", () => ({ officeRequest: vi.fn() }));
type Script = { src: string; onload: (() => void) | null; onerror: (() => void) | null; remove(): void };
let scripts: Script[];
let transport: typeof import("./officeTransport");
const api = { DocEditor: class { destroyEditor() {} downloadAs() {} } };
beforeEach(async () => {
  vi.useFakeTimers(); vi.resetModules(); scripts = [];
  vi.stubGlobal("location", { origin: "https://pogo.test", href: "https://pogo.test/" });
  vi.stubGlobal("window", {});
  vi.stubGlobal("document", { createElement: () => ({ src: "", onload: null, onerror: null, remove: vi.fn() }), head: { append: (script: Script) => scripts.push(script) } });
  transport = await import("./officeTransport");
});
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); });

describe("Office engine script lifetime", () => {
  it("loads the engine through the current forwarded Web port", async () => {
    vi.stubGlobal("location", { origin: "http://localhost:8080", href: "http://localhost:8080/" });
    const load = transport.loadOfficeEngine("/office/editor");
    expect(scripts[0]!.src).toBe("http://localhost:8080/office/editor/web-apps/apps/api/documents/api.js");
    window.DocsAPI = api; scripts[0]!.onload!(); expect(await load).toBe(api);
    await expect(transport.loadOfficeEngine("http://192.0.2.99:19770/office/editor")).rejects.toThrow("Pogo Web gateway");
    expect(scripts).toHaveLength(1);
  });
  it("cancels a failed loader timer so a successful retry retains its verified service origin", async () => {
    const first = transport.loadOfficeEngine("/office/editor");
    const firstError = expect(first).rejects.toThrow("could not be loaded");
    const staleLoad = scripts[0]!.onload;
    scripts[0]!.onerror!(); await firstError;
    const second = transport.loadOfficeEngine("/office/editor/");
    window.DocsAPI = api; scripts[1]!.onload!(); expect(await second).toBe(api);
    staleLoad?.(); await vi.advanceTimersByTimeAsync(21000);
    expect(await transport.loadOfficeEngine("https://pogo.test/office/editor")).toBe(api);
    expect(scripts).toHaveLength(2); expect(vi.getTimerCount()).toBe(0);
    await expect(transport.loadOfficeEngine("https://other-engine.test/office/editor")).rejects.toThrow("Pogo Web gateway");
  });
  it("shares a pending script load across simultaneous document opens", async () => {
    const a = transport.loadOfficeEngine("/office/editor"), b = transport.loadOfficeEngine("https://pogo.test/office/editor/");
    expect(scripts).toHaveLength(1);
    window.DocsAPI = api; scripts[0]!.onload!();
    expect(await a).toBe(api); expect(await b).toBe(api);
  });
  it("uses only the fixed same-origin gateway and never puts credentials into a script URL", async () => {
    const load = transport.loadOfficeEngine("/office/editor");
    expect(scripts[0]!.src).toBe("https://pogo.test/office/editor/web-apps/apps/api/documents/api.js");
    window.DocsAPI = api; scripts[0]!.onload!(); await load;
    for (const url of ["http://127.0.0.1:19778", "https://other.test/office/editor", "https://user:secret@pogo.test/office/editor", "/office/editor?token=secret", "/office/editor#secret", "/office/editor/not-a-base", "/", "javascript:alert(1)"]) {
      await expect(transport.loadOfficeEngine(url)).rejects.toThrow("Pogo Web gateway");
    }
    expect(scripts).toHaveLength(1);
  });
  it("gives editor resources five minutes instead of rejecting a slow first load after twenty seconds", async () => {
    const load = transport.loadOfficeEngine("/office/editor");
    await vi.advanceTimersByTimeAsync(OFFICE_STARTUP_TIMEOUT_MS - 1);
    expect(scripts[0]!.remove).not.toHaveBeenCalled();
    window.DocsAPI = api; scripts[0]!.onload!();
    expect(await load).toBe(api);
    expect(vi.getTimerCount()).toBe(0);
  });
  it("accepts an authorized API that arrives after a timeout without changing the gateway identity", async () => {
    const load = transport.loadOfficeEngine("/office/editor");
    const rejected = expect(load).rejects.toMatchObject({ code: "OFFICE_STARTUP_TIMEOUT", phase: "engine", timeoutMs: 300_000 });
    const staleLoad = scripts[0]!.onload;
    await vi.advanceTimersByTimeAsync(OFFICE_STARTUP_TIMEOUT_MS); await rejected;
    expect(scripts[0]!.remove).toHaveBeenCalledOnce();
    window.DocsAPI = api; staleLoad?.();
    expect(await transport.loadOfficeEngine("https://pogo.test/office/editor")).toBe(api);
    expect(scripts).toHaveLength(1);
    await expect(transport.loadOfficeEngine("https://other.test/office/editor")).rejects.toThrow("Pogo Web gateway");
  });
  it("ignores stale script callbacks and keeps the successful API if an old script later overwrites the global", async () => {
    const first = transport.loadOfficeEngine("/office/editor");
    const rejected = expect(first).rejects.toThrow("timed out");
    const staleLoad = scripts[0]!.onload;
    await vi.advanceTimersByTimeAsync(OFFICE_STARTUP_TIMEOUT_MS); await rejected;
    const second = transport.loadOfficeEngine("/office/editor");
    const oldAPI = { DocEditor: class { destroyEditor() {} downloadAs() {} } };
    window.DocsAPI = oldAPI; staleLoad?.();
    expect(vi.getTimerCount()).toBe(1);
    window.DocsAPI = api; scripts[1]!.onload!(); expect(await second).toBe(api);
    window.DocsAPI = oldAPI; staleLoad?.();
    expect(await transport.loadOfficeEngine("/office/editor")).toBe(api);
    expect(scripts).toHaveLength(2); expect(vi.getTimerCount()).toBe(0);
  });
  it("cancels only one SDK waiter when another document is still opening", async () => {
    const controller = new AbortController();
    const first = transport.loadOfficeEngine("/office/editor", controller.signal), second = transport.loadOfficeEngine("/office/editor");
    const rejected = expect(first).rejects.toMatchObject({ name: "AbortError" });
    controller.abort(); await rejected;
    expect(scripts).toHaveLength(1); expect(scripts[0]!.remove).not.toHaveBeenCalled();
    window.DocsAPI = api; scripts[0]!.onload!(); expect(await second).toBe(api);
    expect(await transport.loadOfficeEngine("/office/editor")).toBe(api);
    expect(vi.getTimerCount()).toBe(0);
  });
  it("does not create an SDK script for an already cancelled caller or trust a preexisting unknown API", async () => {
    const controller = new AbortController(); controller.abort();
    await expect(transport.loadOfficeEngine("/office/editor", controller.signal)).rejects.toMatchObject({ name: "AbortError" });
    expect(scripts).toHaveLength(0);
    window.DocsAPI = api;
    await expect(transport.loadOfficeEngine("/office/editor")).rejects.toThrow("identity changed");
    expect(scripts).toHaveLength(0);
  });
});
