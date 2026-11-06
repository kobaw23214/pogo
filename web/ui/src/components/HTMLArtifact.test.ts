import { describe, expect, it, vi } from "vitest";
import { createSSRApp, defineComponent, h } from "vue";
import { renderToString } from "vue/server-renderer";
import type { WorkspaceFileScope } from "@/agent/workspaceFileNavigation";

vi.mock("vue-i18n", async original => { const { ref } = await import("vue"); return { ...await original<typeof import("vue-i18n")>(), useI18n: () => ({ t: (key: string) => key, locale: ref("zh-CN") }) }; });
vi.mock("./HTMLBrowserPane.vue", () => ({ default: defineComponent({ setup: () => () => h("div", { "data-html-browser": true }) }) }));
import HTMLArtifact from "./HTMLArtifact.vue";

const scope: WorkspaceFileScope = { conversationId: "chat-a", cwd: "/home/pogo/users/alice/project", root: "/home/pogo", workspace: "users/alice/project" };
async function render(source: string, current: WorkspaceFileScope | null = scope) {
  return renderToString(createSSRApp({ render: () => h(HTMLArtifact, { source, scope: current }) }));
}

describe("HTML Artifact card", () => {
  it("renders a lazy sandbox card with source, download and dock actions", async () => {
    const html = await render('{"path":"artifacts/app.html","title":"Demo"}');
    expect(html).toContain("Demo");
    expect(html).toContain("htmlArtifact.sandbox");
    expect(html).toContain("htmlArtifact.load");
    expect(html).toContain("htmlArtifact.source");
    expect(html).toContain("htmlArtifact.openDock");
    expect(html).toContain("users%2Falice%2Fproject%2Fartifacts%2Fapp.html");
    expect(html).toContain("download=1");
    expect(html).not.toContain("data-html-browser");
  });
  it("fails closed for invalid metadata or an unavailable workspace", async () => {
    expect(await render("not json")).toContain("htmlArtifact.invalid");
    expect(await render('{"path":"artifacts/app.html"}', null)).toContain("htmlArtifact.unavailable");
    expect(await render('{"path":"../app.html"}')).toContain("htmlArtifact.unavailable");
  });
});
