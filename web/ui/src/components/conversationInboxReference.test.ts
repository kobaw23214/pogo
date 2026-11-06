import { afterEach, describe, expect, it, vi } from "vitest";
import { effectScope, reactive, type Ref } from "vue";
import type { ComposerPart } from "@/agent/composerParts";
import type { InboxItem } from "@/api/types";
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), useSSRContext: () => ({ modules: new Set() }) }));
vi.mock("vue-i18n", async original => { const { ref } = await import("vue"); return { ...await original<typeof import("vue-i18n")>(), useI18n: () => ({ t: (key: string) => key, locale: ref("zh-CN") }) }; });
vi.mock("./InlineComposer.vue", () => ({ default: {} }));
vi.mock("./OrderedMessageContent.vue", () => ({ default: {} }));
vi.mock("./ShortcutReferenceDialog.vue", () => ({ default: {} }));
import ConversationInbox from "./ConversationInbox.vue";
interface Controller {
  beginEdit(item: InboxItem): void;
  activateReference(part: ComposerPart): void;
  draftParts: Ref<ComposerPart[]>;
  shortcutPreview: Ref<{ id: string; title: string; content: string; icon?: string } | null>;
}
const scopes: ReturnType<typeof effectScope>[] = [];
afterEach(() => { for (const scope of scopes.splice(0)) scope.stop(); });
describe("Queue reference details", () => {
  it("opens saved shortcut details during an edit without sending, changing, or reloading the draft", () => {
    const item: InboxItem = { id: "queued", text: "", parts: [{ type: "shortcut", shortcut_id: "saved", title: "资料", text: "PRIVATE_SNAPSHOT", icon: "database" }], mode: "queued", status: "queued", revision: 1, created_at: 1, updated_at: 1 };
    const emit = vi.fn(), scope = effectScope(); scopes.push(scope);
    const controller = scope.run(() => (ConversationInbox as unknown as { setup(props: unknown, ctx: unknown): Controller }).setup(reactive({ items: [item], working: true }), { emit, expose() {} }))!;
    controller.beginEdit(item); const before = JSON.stringify(controller.draftParts.value);
    controller.activateReference(controller.draftParts.value[0]);
    expect(controller.shortcutPreview.value).toEqual({ id: "saved", title: "资料", content: "PRIVATE_SNAPSHOT", icon: "database" });
    expect(JSON.stringify(controller.draftParts.value)).toBe(before); expect(emit).not.toHaveBeenCalled();
  });
});
