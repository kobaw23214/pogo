import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { uploadFile } from "./upload";

class FakeXHR {
  static latest: FakeXHR;
  upload = { onprogress: null as ((e: { lengthComputable: boolean; loaded: number; total: number }) => void) | null, onload: null as (() => void) | null };
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onabort: (() => void) | null = null;
  ontimeout: (() => void) | null = null;
  headers: Record<string, string> = {};
  body: unknown;
  path = "";
  status = 201;
  responseText = '{"name":"installed"}';
  constructor() { FakeXHR.latest = this; }
  open(method: string, path: string) { expect(method).toBe("POST"); this.path = path; }
  setRequestHeader(name: string, value: string) { this.headers[name.toLowerCase()] = value; }
  send(body: unknown) { this.body = body; }
  abort() { this.onabort?.(); }
}

beforeEach(() => vi.stubGlobal("XMLHttpRequest", FakeXHR));
afterEach(() => vi.unstubAllGlobals());

describe("shared streamed upload", () => {
  it("sends the original File and distinguishes transfer from installation", async () => {
    const file = new File(["ZIP"], "runtimeSkill.zip");
    const progress = vi.fn();
    const uploaded = vi.fn();
    let completed = false;
    const promise = uploadFile("/api/skills/upload", file, { raw: true, headers: { Authorization: "test" }, onProgress: progress, onUploaded: uploaded }).then((data) => { completed = true; return data; });
    const xhr = FakeXHR.latest;
    expect(xhr.body).toBe(file);
    expect(xhr.headers).toMatchObject({ authorization: "test", "content-type": "application/zip" });
    xhr.upload.onprogress?.({ lengthComputable: true, loaded: 2, total: 3 });
    expect(progress).toHaveBeenCalledWith(2, 3);
    xhr.upload.onload?.();
    expect(uploaded).toHaveBeenCalledOnce();
    await Promise.resolve();
    expect(completed).toBe(false);
    xhr.onload?.();
    await expect(promise).resolves.toEqual({ name: "installed" });
  });

  it("preserves the Skill multipart file field without setting a boundary", async () => {
    const file = new File(["skill"], "skill.zip");
    const promise = uploadFile("/api/skills/upload", file);
    const xhr = FakeXHR.latest;
    expect(xhr.body).toBeInstanceOf(FormData);
    const posted = (xhr.body as FormData).get("file") as File;
    expect(posted.name).toBe(file.name);
    expect(await posted.text()).toBe("skill");
    expect(xhr.headers["content-type"]).toBeUndefined();
    xhr.onload?.();
    await promise;
  });

  it("propagates server conflict status and message", async () => {
    const promise = uploadFile("/upload", new File(["x"], "x.zip"));
    FakeXHR.latest.status = 409;
    FakeXHR.latest.responseText = '{"error":"version conflict"}';
    FakeXHR.latest.onload?.();
    await expect(promise).rejects.toMatchObject({ status: 409, message: "version conflict" });
  });

  it("aborts transfer and removes its signal listener", async () => {
    const control = new AbortController();
    const remove = vi.spyOn(control.signal, "removeEventListener");
    const promise = uploadFile("/upload", new File(["x"], "x.zip"), { signal: control.signal });
    control.abort();
    await expect(promise).rejects.toMatchObject({ name: "AbortError" });
    expect(remove).toHaveBeenCalledWith("abort", expect.any(Function));
    expect(FakeXHR.latest.upload.onload).toBeNull();
  });

  it("does not send an already cancelled request", async () => {
    const control = new AbortController();
    control.abort();
    const send = vi.spyOn(FakeXHR.prototype, "send");
    await expect(uploadFile("/upload", new File(["x"], "x.zip"), { signal: control.signal })).rejects.toMatchObject({ name: "AbortError" });
    expect(send).not.toHaveBeenCalled();
    send.mockRestore();
  });

  it.each(["network", "invalid-response"])("rejects %s without claiming installation succeeded", async (failure) => {
    const promise = uploadFile("/upload", new File(["x"], "x.zip"));
    if (failure === "network") FakeXHR.latest.onerror?.();
    else { FakeXHR.latest.responseText = "upstream returned HTML"; FakeXHR.latest.onload?.(); }
    await expect(promise).rejects.toBeInstanceOf(Error);
  });
});
