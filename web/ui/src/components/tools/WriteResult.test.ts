import { describe, expect, it, vi } from "vitest";
import { createSSRApp, h } from "vue";
import { renderToString } from "vue/server-renderer";
import WriteResult from "./WriteResult.vue";
import type { ToolResult, WriteDetails } from "@/api/types";

vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string) => key }) }));
vi.mock("@/i18n", () => ({ gt: (key: string) => key }));
vi.mock("../DiffView.vue", () => ({ default: { render: () => null } }));
const result: ToolResult = { call_id: "c1", text: "ok" };

describe("Write result change statistics", () => {
  it("shows the settled added and removed line counts", async () => {
    const details: WriteDetails = { path: "artifacts/demo.html", bytes: 16512, created: true, added: 120, removed: 0 };
    const html = await renderToString(createSSRApp({ render: () => h(WriteResult, { result, details }) }));
    expect(html).toContain(">+120</span>");
    expect(html).toContain(">-0</span>");
    expect(html).toContain("16.13 KB");
  });
});
