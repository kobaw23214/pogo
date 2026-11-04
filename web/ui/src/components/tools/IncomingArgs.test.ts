import { describe, expect, it, vi } from "vitest";
import { createSSRApp, h } from "vue";
import { renderToString } from "vue/server-renderer";
import IncomingArgs from "./IncomingArgs.vue";
import type { IncomingArgs as IncomingCall } from "@/api/types";

vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string) => key }) }));
async function render(incoming: IncomingCall) {
  return renderToString(createSSRApp({ render: () => h(IncomingArgs, { incoming }) }));
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
});
