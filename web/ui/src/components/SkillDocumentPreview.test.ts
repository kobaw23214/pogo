import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, reactive, type Ref } from "vue";
import type { SkillContent } from "@/api/types";
import { api } from "@/api/client";
import SkillDocumentPreview from "./SkillDocumentPreview.vue";

vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), onBeforeUnmount() {}, useSSRContext: () => ({ modules: new Set() }) }));
vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string) => key }) }));
vi.mock("@/api/client", () => ({ api: { skillContent: vi.fn() } }));
vi.mock("./SkillDocumentViewer.vue", () => ({ default: {} }));

interface Controller {
  root: Ref<SkillContent | null>;
  loading: Ref<boolean>;
  error: Ref<string>;
  loadRoot(): Promise<void>;
  loadDocument(path: string): Promise<SkillContent>;
}
const document: SkillContent = { name: "browser-use", path: "/cache/browser-use/SKILL.md", source: "embedded", relative_path: "SKILL.md", content: "# Browser", truncated: false };
const scopes: ReturnType<typeof effectScope>[] = [];
function mount(path = "SKILL.md") {
  const props = reactive({ skill: "browser-use", sessionId: "session_1", path });
  const scope = effectScope(); scopes.push(scope);
  const controller = scope.run(() => (SkillDocumentPreview as unknown as { setup(props: unknown, context: unknown): Controller }).setup(props, { emit() {}, expose() {} }))!;
  return { controller, props };
}
beforeEach(() => { vi.mocked(api.skillContent).mockReset().mockResolvedValue(document); });
afterEach(() => { for (const scope of scopes.splice(0)) scope.stop(); });

describe("Skill document preview", () => {
  it("loads through the selected session and keeps package paths relative", async () => {
    const { controller } = mount("references/download.md");
    await vi.waitFor(() => expect(controller.root.value).toEqual(document));
    expect(api.skillContent).toHaveBeenCalledExactlyOnceWith("browser-use", "session_1");
    await controller.loadDocument("references/download.md");
    expect(api.skillContent).toHaveBeenLastCalledWith("browser-use", "session_1", "references/download.md");
    expect(api.skillContent).toHaveBeenCalledTimes(2);
  });
  it("shows a load failure and retries through the same read-only endpoint", async () => {
    vi.mocked(api.skillContent).mockRejectedValueOnce(new Error("not authorized"));
    const { controller } = mount();
    await vi.waitFor(() => expect(controller.error.value).toBe("not authorized"));
    expect(controller.root.value).toBeNull();
    await controller.loadRoot();
    expect(controller.error.value).toBe("");
    expect(controller.root.value).toEqual(document);
    expect(api.skillContent).toHaveBeenLastCalledWith("browser-use", "session_1");
  });
  it("does not let a late response cross a changed session or Skill", async () => {
    let resolveFirst!: (value: SkillContent) => void;
    vi.mocked(api.skillContent).mockImplementationOnce(() => new Promise(resolve => { resolveFirst = resolve; }))
      .mockResolvedValueOnce({ ...document, name: "other-skill" });
    const { controller, props } = mount();
    props.sessionId = "session_2"; props.skill = "other-skill";
    await vi.waitFor(() => expect(controller.root.value?.name).toBe("other-skill"));
    resolveFirst({ ...document, name: "browser-use" });
    await Promise.resolve();
    expect(controller.root.value?.name).toBe("other-skill");
    expect(api.skillContent).toHaveBeenNthCalledWith(2, "other-skill", "session_2");
  });
});
