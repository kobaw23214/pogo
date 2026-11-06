import { describe, expect, it, vi } from "vitest";
import { createSSRApp, h } from "vue";
import { renderToString } from "vue/server-renderer";
import IncomingArgs from "./IncomingArgs.vue";
import type { IncomingArgs as IncomingCall } from "@/api/types";

vi.mock("vue-i18n", async original => ({ ...await original<typeof import("vue-i18n")>(), useI18n: () => ({ t: (key: string) => key }) }));
async function render(incoming: IncomingCall, extra: Record<string, unknown> = {}) {
  return renderToString(createSSRApp({ render: () => h(IncomingArgs, { incoming, ...extra }) }));
}

describe("Incoming file tool card", () => {
  it("shows generation progress without implying that the file was written", async () => {
    const html = await render({ call_id: "c1", name: "write", head: '{"path":"artifacts/demo.html","content":"', tail: "<html>\n<body>\nhello", bytes: 2048, lines: 42 });
    expect(html).toContain("incoming file");
    expect(html).toContain("artifacts/demo.html");
    expect(html).toContain("incomingArgs.generating");
    expect(html).toContain("incomingArgs.notWritten");
    expect(html).toContain("hello");
    expect(html).toContain(">+42</span>");
  });
  it("shows the approximate edit diff while arguments are still generating", async () => {
    const html = await render({ call_id: "c3", name: "edit", head: '{"path":"artifacts/demo.html","edits":[]}', preview_added: 7, preview_removed: 3, bytes: 2048 });
    expect(html).toContain(">+7</span>");
    expect(html).toContain(">-3</span>");
    expect(html).toContain("incomingArgs.notWritten");
  });
  it("keeps the generic argument preview for non-file tools", async () => {
    const html = await render({ call_id: "c2", name: "bash", tail: "go test ./...", bytes: 12 });
    expect(html).toContain("incomingArgs.received");
    expect(html).not.toContain("incomingArgs.notWritten");
    expect(html).not.toContain("diffstats");
  });

  it("preserves the original live file preview and counts in compact summary mode", async () => {
    const incoming = { call_id: "write", name: "write", head: '{"path":"report.md","content":"', tail: "private draft fragment", bytes: 64, lines: 4 };
    const compact = await render(incoming, { compact: true });
    expect(compact).toContain("incomingArgs.generating");
    expect(compact).toContain("incomingArgs.notWritten");
    expect(compact).toContain("report.md");
    expect(compact).toContain("private draft fragment");
    expect(compact).toContain(">+4</span>");
    expect(compact).not.toContain("aria-expanded");
  });

  it("keeps both edit diff counts visible without opening details in compact mode", async () => {
    const html = await render({ call_id: "edit", name: "edit", head: '{"path":"report.md","edits":[', tail: "replacement code", preview_added: 7, preview_removed: 3, bytes: 64 }, { compact: true });
    expect(html).toContain(">+7</span>");
    expect(html).toContain(">-3</span>");
    expect(html).toContain("replacement code");
    expect(html).toContain("incomingArgs.notWritten");
    expect(html).not.toContain("display:none");
  });

  it("does not expose streamed non-file arguments or invent a target in compact mode", async () => {
    const html = await render({ call_id: "query", name: "mcp_unknown", tail: '{"query":"unfinished', bytes: 10 }, { compact: true });
    expect(html).toContain("toolDisclosure.objectPending");
    expect(html).toContain("toolDisclosure.notExecuted");
    expect(html).not.toContain("unfinished");
    expect(html).not.toContain("aria-expanded");
  });
});
