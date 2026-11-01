import { describe, expect, it } from "vitest";
import { looksLikeSse, parseSseFrames, summarizeFrame } from "./sseFrames";

describe("parseSseFrames", () => {
  it("parses named frames with JSON data and multi-line data joining", () => {
    const text = [
      'event: message',
      'data: {"jsonrpc":"2.0","id":1,"result":{"ok":true}}',
      '',
      'event: message',
      'data: {"jsonrpc":"2.0","method":"notifications/progress",',
      'data: "params":{"p":1}}',
      '',
      '',
    ].join("\n");
    const frames = parseSseFrames(text);
    expect(frames).toHaveLength(2);
    expect(frames[0].event).toBe("message");
    expect((frames[0].json as { result: { ok: boolean } }).result.ok).toBe(true);
    expect((frames[1].json as { method: string }).method).toBe("notifications/progress");
    expect(frames.every((f) => !f.partial)).toBe(true);
  });

  it("flags a tail frame cut by the capture cap as partial", () => {
    const cut = 'event: message\ndata: {"jsonrpc":"2.0","id":2,"result":{"protocolVer';
    const frames = parseSseFrames(cut);
    expect(frames).toHaveLength(1);
    expect(frames[0].partial).toBe(true);
    expect(frames[0].json).toBeUndefined();
  });

  it("defaults the event name to message and skips keepalive comments", () => {
    const text = ': keepalive\n\ndata: {"a":1}\n\n';
    const frames = parseSseFrames(text);
    expect(frames).toHaveLength(1);
    expect(frames[0].event).toBe("message");
  });

  it("detects SSE shape", () => {
    expect(looksLikeSse("data: {}\n\n")).toBe(true);
    expect(looksLikeSse('{"plain":"json"}')).toBe(false);
  });
});

describe("summarizeFrame", () => {
  it("classifies JSON-RPC shapes", () => {
    const mk = (data: string) => parseSseFrames(`data: ${data}\n\n`)[0];
    expect(summarizeFrame(mk('{"jsonrpc":"2.0","id":2,"result":{}}')).kind).toBe("response");
    expect(summarizeFrame(mk('{"jsonrpc":"2.0","id":2,"error":{"message":"boom"}}'))).toMatchObject({ kind: "error", message: "boom" });
    expect(summarizeFrame(mk('{"jsonrpc":"2.0","method":"notifications/progress","params":{}}'))).toMatchObject({ kind: "notification", method: "notifications/progress" });
    expect(summarizeFrame(mk('{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{}}'))).toMatchObject({ kind: "request", method: "tools/call" });
    expect(summarizeFrame(parseSseFrames("data: not json\n\n")[0]).kind).toBe("text");
  });
});
