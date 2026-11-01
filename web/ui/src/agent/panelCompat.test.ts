import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import vm from "node:vm";

const script = readFileSync(new URL("../../../panel_compat_bootstrap.js", import.meta.url), "utf8");

function fixture() {
  const calls: Request[] = [];
  class Element { setAttribute(_name: string, _value: string) {} }
  class Document {}
  class XHR { open(_method: string, _url: string) {} }
  class Socket {
    static OPEN = 1;
    constructor(public url: string) {}
  }
  const location = new URL("http://pogo.test/panels/app/app/?entry=1#/home");
  const context: Record<string, any> = {
    URL, Request, Response, Uint8Array, console, Document, Element, XMLHttpRequest: XHR,
    location, document: { baseURI: location.href }, navigator: {},
    history: { pushState() {}, replaceState() {} },
    WebSocket: Socket, EventSource: Socket,
    fetch: async (input: string | URL | Request, init?: RequestInit) => {
      const req = input instanceof Request ? input : new Request(new URL(String(input), location.href), init);
      calls.push(req);
      return new Response(req.body ? await req.text() : "GET");
    },
  };
  for (const name of ["HTMLScriptElement", "HTMLLinkElement", "HTMLImageElement", "HTMLIFrameElement", "HTMLAnchorElement", "HTMLFormElement", "HTMLInputElement", "HTMLButtonElement", "HTMLMediaElement", "HTMLVideoElement", "HTMLSourceElement", "HTMLObjectElement"]) context[name] = class extends Element {};
  context.window = context;
  vm.runInNewContext(script.replace("__POGO_PANEL_CONFIG__", JSON.stringify({ prefix: "/panels/app", source: "http://upstream.test", target: "http://upstream.test" })), context);
  return { context, calls, Socket };
}

describe("panel compatibility bootstrap", () => {
  it("maps root, source-absolute, local-absolute and URL object requests, preserving unrelated origins", async () => {
    const {context, calls} = fixture();
    for (const value of ["/api/x", "http://upstream.test/api/x", new URL("http://pogo.test/api/x"), "/panels/app/api/x", "https://external.test/api/x"]) await context.fetch(value);
    expect(calls.map(c => c.url)).toEqual([
      ...Array(4).fill("http://pogo.test/panels/app/api/x"), "https://external.test/api/x",
    ]);
  });

  it("preserves Request POST bytes, headers, credentials and init overrides without streaming uploads", async () => {
    const {context, calls} = fixture();
    const request = () => new Request("http://pogo.test/api/post", {method: "POST", body: "original", credentials: "include", headers: {"X-App": "one"}});
    const first = request();
    expect(await (await context.fetch(first)).text()).toBe("original");
    expect(first.bodyUsed).toBe(true);
    expect(calls[0].headers.get("X-App")).toBe("one");
    expect(calls[0].credentials).toBe("include");
    expect(await (await context.fetch(request(), {body: "override", headers: {"X-App": "two"}})).text()).toBe("override");
    expect(calls[1].headers.get("X-App")).toBe("two");
    expect(calls[1].url).toBe("http://pogo.test/panels/app/api/post");
  });

  it("retains abort behavior and bounds Request-body buffering", async () => {
    const {context, calls} = fixture();
    const controller = new AbortController(); controller.abort();
    await expect(context.fetch(new Request("http://pogo.test/api/x", {method: "POST", body: "x", signal: controller.signal}))).rejects.toThrow();
    await expect(context.fetch(new Request("http://pogo.test/api/x", {method: "POST", body: new Uint8Array(16 * 1024 * 1024 + 1)}))).rejects.toThrow("exceeds 16 MiB");
    expect(calls).toHaveLength(0);
  });

  it("keeps WebSocket constructor constants/prototypes and selects the mounted URL", () => {
    const {context, Socket} = fixture();
    const ws = new context.WebSocket("ws://upstream.test/api/ws");
    expect(ws.url).toBe("ws://pogo.test/panels/app/api/ws");
    expect(ws).toBeInstanceOf(Socket);
    expect(context.WebSocket.OPEN).toBe(1);
  });
});
