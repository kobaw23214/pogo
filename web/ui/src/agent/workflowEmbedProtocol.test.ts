import { describe, expect, it, vi } from "vitest";
import { workflowEmbedExample, workflowEmbedId, workflowEmbedLocation, workflowLaunchMessage, workflowParentOrigin } from "./workflowEmbedProtocol";
import { workbenchRedirect } from "./studioRoutes";

describe("workflow iframe protocol", () => {
  it("keeps the new route separate from the existing delegated mount", () => {
    expect(workflowEmbedId("/embed/workflows/pub_123")).toBe("pub_123");
    expect(workflowEmbedId("/embed/")).toBeNull();
    expect(workbenchRedirect("/embed/")).toBeNull();
    expect(workbenchRedirect("/embed/workflows/pub_123")).toBeNull();
    expect(workbenchRedirect("/embed/other")).toBe("/");
  });
  it("requires an exact web origin", () => {
    expect(workflowParentOrigin("https://host.example:443")).toBe("https://host.example");
    for (const value of [null, "*", "null", "https://host.example/path", "https://host.example?x=1", "https://user:pw@host.example", "javascript:alert(1)"]) expect(workflowParentOrigin(value)).toBeNull();
  });
  it("accepts launch only from the expected parent and active request", () => {
    const parent = {} as Window;
    const message = { type: "pogo.workflow.launch", protocol: 1, publication_id: "pub_123", request_id: "request-1", code: "single-use" };
    const event = { source: parent, origin: "https://host.example", data: message };
    expect(workflowLaunchMessage(event, parent, event.origin, "pub_123", "request-1")).toBe("single-use");
    expect(workflowLaunchMessage({ ...event, source: {} as Window }, parent, event.origin, "pub_123", "request-1")).toBeNull();
    expect(workflowLaunchMessage({ ...event, origin: "https://evil.example" }, parent, event.origin, "pub_123", "request-1")).toBeNull();
    for (const patch of [{ protocol: 2 }, { publication_id: "other" }, { request_id: "old" }, { code: 1 }, { code: "" }]) expect(workflowLaunchMessage({ ...event, data: { ...message, ...patch } }, parent, event.origin, "pub_123", "request-1")).toBeNull();
  });
  it("keeps the selected run without propagating account credentials", () => {
    const url = new URL(workflowEmbedLocation("https://pogo.example/embed/workflows/pub?parent_origin=https%3A%2F%2Fhost.example&token=account#secret", "run-new"));
    expect(url.searchParams.get("run")).toBe("run-new");
    expect(url.searchParams.get("parent_origin")).toBe("https://host.example");
    expect(url.searchParams.has("token")).toBe(false);
    expect(url.hash).toBe("");
  });
  it("generates a parent example with source checks and no backend secret", () => {
    const code = workflowEmbedExample("https://pogo.example", "pub_123", "https://host.example");
    expect(code).toContain('event.source !== frame.contentWindow');
    expect(code).toContain('event.origin !== "https://pogo.example"');
    expect(code).toContain('request_id: message.request_id');
    expect(code).toContain('/api/my-workflow-launch');
    expect(code).not.toContain('Authorization');
    expect(code).not.toContain('?token=');
  });
  it("restores the business Run after a host refresh without starting a new Run", async () => {
    const code = workflowEmbedExample("https://pogo.example", "pub_123", "https://host.example");
    const script = code.split("<script>")[1]!.split("</script>")[0]!;
    let receive!: (event: { origin: string; source: unknown; data: unknown }) => Promise<void>;
    const frame = { src: "", contentWindow: { postMessage: vi.fn() } };
    const win = { location: { href: "https://host.example/order/42?filter=mine&pogo_run=run_saved" }, history: { state: { app: "keep" }, replaceState: vi.fn() }, addEventListener: vi.fn((_type: string, listener: typeof receive) => { receive = listener; }) };
    const fetcher = vi.fn(async () => ({ ok: true, json: async () => ({ launch_code: "single-use" }) }));
    new Function("window", "document", "fetch", script)(win, { getElementById: () => frame }, fetcher);
    expect(new URL(frame.src).searchParams.get("run")).toBe("run_saved");
    await receive({ origin: "https://pogo.example", source: frame.contentWindow, data: { type: "pogo.workflow.ready", protocol: 1, publication_id: "pub_123", request_id: "request" } });
    expect(fetcher).toHaveBeenCalledTimes(1);
    const body = JSON.parse((fetcher.mock.calls as unknown as [string, RequestInit][])[0]![1].body as string);
    expect(body).toEqual({ publication_id: "pub_123", run_id: "run_saved" });
    await receive({ origin: "https://pogo.example", source: frame.contentWindow, data: { type: "pogo.workflow.run", protocol: 1, publication_id: "pub_123", run_id: "run_retry", status: "completed" } });
    const savedUrl = new URL(win.history.replaceState.mock.calls[0]![2]);
    expect(savedUrl.searchParams.get("pogo_run")).toBe("run_retry");
    expect(savedUrl.searchParams.get("filter")).toBe("mine");
    expect(win.history.replaceState.mock.calls[0]![0]).toEqual({ app: "keep" });
    expect(fetcher).toHaveBeenCalledTimes(1);
  });
});
