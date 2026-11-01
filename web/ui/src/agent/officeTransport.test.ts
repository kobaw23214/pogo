import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
const request = vi.hoisted(() => vi.fn());
vi.mock("@/api/client", () => ({ officeRequest: request }));
import { officeApi, officeBridge, officeBridgeBase, officeBridgeURL, detachOfficeView, officePathMounted, officeViewMounted, registerOfficeView, type OfficeOpen, type OfficeSession } from "./officeTransport";

const session: OfficeSession = { id: "office-session-a", documentId: "document-a", conversationId: "conversation-a", path: "uploads/test.docx", title: "Test.docx", fileType: "docx", editor: "word", generation: 1, ready: false, closed: false, dirty: false, dirtyEpoch: 0, persistedEpoch: 0, saveState: "saved", revision: 0, blocked: false };
const opened: OfficeOpen = { session, config: {}, browserURL: "/office/editor", bridgeURL: "/office/bridge/office-session-a/short-cap/", bridgeToken: "synthetic-capability", instanceOwned: true };
beforeEach(() => { vi.stubGlobal("location", { origin: "https://pogo.test", href: "https://pogo.test/" }); request.mockReset(); request.mockResolvedValue({}); });
afterEach(() => vi.unstubAllGlobals());

describe("Office authenticated transport boundaries", () => {
  it("rejects a different origin, credentials and non-bridge paths before issuing a request", async () => {
    for (const bridgeURL of ["https://other.test/office/bridge/a/c/", "https://user:password@pogo.test/office/bridge/a/c/", "/api/files/content", "/office/bridge/../outside/", "/office/bridge/a/c/?token=secret", "/office/bridge/a/c/#fragment", "/office/bridge/a/c/next", "/office/bridge/a/%2F/", "/office/bridge/a/"]) {
      expect(() => officeBridgeURL(bridgeURL, "ready")).toThrow();
      expect(() => officeBridge({ ...opened, bridgeURL }, "ready", "instance-a", {})).toThrow();
    }
    expect(request).not.toHaveBeenCalled();
  });
  it("keeps the capability on the current SSH forwarded origin without permitting the configured public address", async () => {
    vi.stubGlobal("location", { origin: "http://localhost:8080", href: "http://localhost:8080/" });
    expect(officeBridgeBase(opened.bridgeURL)).toBe(opened.bridgeURL);
    expect(officeBridgeBase(`http://localhost:8080${opened.bridgeURL}`)).toBe(opened.bridgeURL);
    await officeBridge(opened, "ready", "forwarded-instance", {});
    expect(request).toHaveBeenLastCalledWith("POST", "/office/bridge/office-session-a/short-cap/ready", { instanceId: "forwarded-instance" }, undefined);
    expect(() => officeBridgeBase(`http://192.0.2.99:19770${opened.bridgeURL}`)).toThrow("Office bridge origin is not authorized");
    expect(() => officeBridgeBase(`http://localhost:19770${opened.bridgeURL}`)).toThrow("Office bridge origin is not authorized");
    expect(request).toHaveBeenCalledTimes(1);
  });
  it("retains one canonical Office API and binds opening to the selected conversation", async () => {
    await officeApi.open("uploads/中文.docx", "conversation-a", "instance-a");
    expect(request).toHaveBeenLastCalledWith("POST", "/api/office/sessions", { path: "uploads/中文.docx", conversation_id: "conversation-a", instanceId: "instance-a" }, undefined);
    await officeApi.config("office/session", "instance/a");
    expect(request).toHaveBeenLastCalledWith("GET", "/api/office/sessions/office%2Fsession/config?instanceId=instance%2Fa", undefined, undefined);
  });
  it("binds the same executor nonce to queue reads and write receipts without sending another session token", async () => {
    await officeBridge(opened, "next", "instance-a");
    expect(request).toHaveBeenLastCalledWith("GET", "/office/bridge/office-session-a/short-cap/next?instanceId=instance-a", undefined, undefined);
    await officeBridge(opened, "results", "instance-a", { instanceId: "wrong", operationId: "operation-a", ok: true });
    expect(request).toHaveBeenLastCalledWith("POST", "/office/bridge/office-session-a/short-cap/results", { instanceId: "instance-a", operationId: "operation-a", ok: true }, undefined);
  });
  it("uses an activity version on the existing queue request without creating another polling path", async () => {
    await officeBridge(opened, "next", "instance-a", undefined, undefined, 8);
    expect(request).toHaveBeenLastCalledWith("GET", "/office/bridge/office-session-a/short-cap/next?instanceId=instance-a&activityVersion=8", undefined, undefined);
    expect(() => officeBridgeURL(opened.bridgeURL, "next", "instance-a", -1)).toThrow();
    expect(() => officeBridgeURL(opened.bridgeURL, "next", "instance-a", NaN)).toThrow();
    expect(request).toHaveBeenCalledTimes(1);
  });
  it("requests only newer sync metadata and binds freeze/readback acknowledgements to the same instance", async () => {
    await officeBridge(opened, "next", "instance-a", undefined, undefined, 8, 12);
    expect(request).toHaveBeenLastCalledWith("GET", "/office/bridge/office-session-a/short-cap/next?instanceId=instance-a&activityVersion=8&syncRevision=12", undefined, undefined);
    expect(() => officeBridgeURL(opened.bridgeURL, "next", "instance-a", undefined, -1)).toThrow();
    await officeBridge(opened, "batch-freeze", "instance-a", { instanceId: "foreign", batchId: "owned-batch", frozen: true });
    expect(request).toHaveBeenLastCalledWith("POST", "/office/bridge/office-session-a/short-cap/batch-freeze", { instanceId: "instance-a", batchId: "owned-batch", frozen: true }, undefined);
    await officeBridge(opened, "reload-ready", "instance-a", { canvasVersion: "a".repeat(64), readOperationId: "read-a" });
    expect(request).toHaveBeenLastCalledWith("POST", "/office/bridge/office-session-a/short-cap/reload-ready", { instanceId: "instance-a", canvasVersion: "a".repeat(64), readOperationId: "read-a" }, undefined);
  });
  it("reports the original in-flight identity and sends only scoped reconciliation", async () => {
    await officeBridge(opened, "next", "instance-a", undefined, undefined, 3, 9, "old/write");
    expect(request).toHaveBeenLastCalledWith("GET", "/office/bridge/office-session-a/short-cap/next?instanceId=instance-a&activityVersion=3&syncRevision=9&inFlightId=old%2Fwrite", undefined, undefined);
    await officeBridge(opened, "reconcile", "instance-a", { recoveryRevision: 9, barrierId: "barrier-a", settled: true, operations: [{ operationId: "old/write", operation: "word.replace", ok: true }] });
    expect(request).toHaveBeenLastCalledWith("POST", "/office/bridge/office-session-a/short-cap/reconcile", { instanceId: "instance-a", recoveryRevision: 9, barrierId: "barrier-a", settled: true, operations: [{ operationId: "old/write", operation: "word.replace", ok: true }] }, undefined);
  });
  it("keeps clean reload, explicit user discard and batch cancellation scoped and distinct", async () => {
    await officeApi.reload("doc/a", "op-a", "b".repeat(64));
    expect(request).toHaveBeenLastCalledWith("POST", "/api/office/sessions/doc%2Fa/reload", { operationId: "op-a", expectedVersion: "b".repeat(64) }, undefined);
    await officeApi.reload("doc/a", "op-b", "b".repeat(64), undefined, { discardChanges: true, confirmedDirtyEpoch: 8, confirmedCanvasVersion: "a".repeat(64), instanceId: "owned" });
    expect(request).toHaveBeenLastCalledWith("POST", "/api/office/sessions/doc%2Fa/reload", { operationId: "op-b", expectedVersion: "b".repeat(64), discardChanges: true, confirmedDirtyEpoch: 8, confirmedCanvasVersion: "a".repeat(64), instanceId: "owned" }, undefined);
    await officeApi.abortBatch("batch/a", "chat", "abort-a");
    expect(request).toHaveBeenLastCalledWith("POST", "/api/office/batches/batch%2Fa/abort", { conversationId: "chat", operationId: "abort-a" }, undefined);
  });
  it("reuses the original visible instance and keeps another conversation separate", () => {
    const release = registerOfficeView(session);
    try {
      expect(officeViewMounted(session.id)).toBe(true);
      expect(officePathMounted(session.path, session.conversationId)).toBe(true);
      expect(officePathMounted(session.path, "conversation-b")).toBe(false);
      expect(() => registerOfficeView(session)).toThrow(/already open/);
    } finally { release(); }
    expect(officeViewMounted(session.id)).toBe(false);
    expect(officePathMounted(session.path, session.conversationId)).toBe(false);
  });
  it("uses only the scoped same-origin capability for unload cleanup", async () => {
    const sendBeacon = vi.fn(() => true);
    vi.stubGlobal("navigator", { sendBeacon });
    expect(detachOfficeView(opened, "instance-a")).toBe(true);
    const [url, payload] = sendBeacon.mock.calls[0] as unknown as [string, Blob];
    expect(url).toBe("/office/bridge/office-session-a/short-cap/detach");
    expect(JSON.parse(await payload.text())).toEqual({ instanceId: "instance-a" });
    expect(request).not.toHaveBeenCalled();
    expect(() => detachOfficeView({ ...opened, bridgeURL: "https://other.test/office/bridge/a/c/" }, "instance-a")).toThrow();
    expect(sendBeacon).toHaveBeenCalledTimes(1);
  });
  it("does not claim cleanup succeeded when beacon is absent or refused", () => {
    vi.stubGlobal("navigator", undefined);
    expect(detachOfficeView(opened, "instance-a")).toBe(false);
    vi.stubGlobal("navigator", { sendBeacon: () => false });
    expect(detachOfficeView(opened, "instance-a")).toBe(false);
  });
});
