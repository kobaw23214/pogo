<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { TopRight, EditPen, Close } from "@element-plus/icons-vue";
import type { InboxItem, InputLayout, MessageInputPart } from "@/api/types";
import { inboxDraftParts, inboxDraftSubmission, inboxMessageBlocks, pendingInboxItems } from "@/agent/conversationInbox";
import type { ComposerPart } from "@/agent/composerParts";
import InlineComposer from "./InlineComposer.vue";
import OrderedMessageContent from "./OrderedMessageContent.vue";
import type { OfficeReferenceSnapshot } from "@/api/types";
import OfficeReferenceDialog from "./OfficeReferenceDialog.vue";
import ShortcutReferenceDialog from "./ShortcutReferenceDialog.vue";
import type { ShortcutSnapshot } from "@/api/shortcuts";

const props = defineProps<{ items: InboxItem[]; working: boolean; disabled?: boolean; changing?: string[]; errors?: Record<string, string> }>();
const emit = defineEmits<{
  steer: [item: InboxItem];
  edit: [item: InboxItem, text: string, parts?: MessageInputPart[], inputLayout?: InputLayout];
  withdraw: [item: InboxItem];
  openFile: [file: { path: string; name: string }];
}>();
const { t } = useI18n();
const pending = computed(() => pendingInboxItems(props.items));
const editing = ref<string | null>(null);
const draft = ref("");
const editingRevision = ref(0);
const draftParts = ref<ComposerPart[]>([]);
const draftLayout = ref<InputLayout>("top");
const orderedDraft = ref(false);
const shortcutPreview = ref<ShortcutSnapshot | null>(null);
const officePreview = ref<OfficeReferenceSnapshot | null>(null);
const draftHasContent = computed(() => orderedDraft.value
  ? draftParts.value.some((part) => part.type !== "text" || part.text.trim()) && !draftParts.value.some((part) => part.type === "file" && (!part.path || part.status === "uploading" || part.status === "error"))
  : !!draft.value.trim());
function beginEdit(item: InboxItem) {
  editing.value = item.id;
  editingRevision.value = item.revision;
  draft.value = item.text;
  orderedDraft.value = !!item.parts || item.input_layout === "inline";
  draftParts.value = inboxDraftParts(item);
  draftLayout.value = item.input_layout ?? "top";
}
function blocked(item: InboxItem) { return props.disabled || item.status !== "queued" || props.changing?.includes(item.id); }
function save(item: InboxItem) {
  if (!draftHasContent.value || blocked(item)) return;
  if (orderedDraft.value) {
    const submission = inboxDraftSubmission(draftParts.value);
    emit("edit", { ...item, revision: editingRevision.value }, submission.text, submission.parts, draftLayout.value);
  } else emit("edit", { ...item, revision: editingRevision.value }, draft.value.trim());
}
function editorKeydown(event: KeyboardEvent) {
  if (event.isComposing || event.keyCode === 229) return;
  if (event.key === "Escape") { event.preventDefault(); editing.value = null; }
}
function activateReference(part: ComposerPart) {
  if (part.type === "office") officePreview.value = JSON.parse(JSON.stringify(part.office));
  if (part.type === "file" && part.path) emit("openFile", { path: part.path, name: part.label });
  if (part.type === "shortcut") shortcutPreview.value = { id: part.shortcutId, title: part.title, content: part.text, ...(part.icon ? { icon: part.icon } : {}), ...(part.language ? { language: part.language } : {}), ...(part.parts ? { parts: JSON.parse(JSON.stringify(part.parts)) } : {}) };
}
watch(() => props.items, items => {
  const current = items.find(item => item.id === editing.value);
  if (!current || current.status !== "queued" || current.revision !== editingRevision.value) editing.value = null;
});
</script>

<template>
  <section v-if="pending.length" class="conversation-inbox" :aria-label="t('conversationInbox.heading')" data-testid="conversation-inbox">
    <div class="inbox-heading">{{ t('conversationInbox.heading') }} <span>{{ pending.length }}</span></div>
    <div class="inbox-list">
    <article v-for="item in pending" :key="item.id" class="inbox-item" :data-inbox-id="item.id">
      <div class="inbox-state" role="status">
        <span class="state-dot" :class="{ active: item.status === 'steering' }" aria-hidden="true" />
        <span>{{ t(item.status === 'steering' ? 'conversationInbox.steering' : 'conversationInbox.queued') }}</span>
        <span v-if="item.status === 'queued'" class="inbox-timing">{{ t(working && !item.note ? 'conversationInbox.later' : 'conversationInbox.idle') }}</span>
      </div>
      <template v-if="editing === item.id">
        <div v-if="orderedDraft" class="inbox-editor ordered-editor" data-testid="inbox-ordered-editor"><InlineComposer v-model="draftParts" :aria-label="t('conversationInbox.editing')" :disabled="blocked(item)" @keydown="editorKeydown" @submit="save(item)" @activate-reference="activateReference" /></div>
        <textarea v-else v-model="draft" class="inbox-editor" :aria-label="t('conversationInbox.editing')" :disabled="blocked(item)" @keydown.esc.prevent="editing = null" />
        <div class="inbox-actions"><button type="button" class="inbox-guide" :disabled="blocked(item) || !draftHasContent" @click="save(item)">{{ t('conversationInbox.save') }}</button><button type="button" class="inbox-action" :disabled="!!changing?.includes(item.id)" @click="editing = null">{{ t('conversationInbox.cancel') }}</button></div>
      </template>
      <template v-else>
        <div class="inbox-text"><OrderedMessageContent v-if="item.parts || item.input_layout" :blocks="inboxMessageBlocks(item)" @open-file="emit('openFile', $event)" /><template v-else>{{ item.text }}</template></div>
        <div class="inbox-actions">
          <button type="button" class="inbox-guide" :disabled="blocked(item)" :title="t('conversationInbox.hint')" @click="emit('steer', item)"><el-icon><TopRight /></el-icon>{{ t(working ? 'conversationInbox.guide' : 'conversationInbox.send') }}</button>
          <button type="button" class="inbox-action" :disabled="blocked(item)" @click="beginEdit(item)"><el-icon><EditPen /></el-icon>{{ t('conversationInbox.edit') }}</button>
          <button type="button" class="inbox-action" :disabled="blocked(item)" @click="emit('withdraw', item)"><el-icon><Close /></el-icon>{{ t('conversationInbox.withdraw') }}</button>
          <span v-if="changing?.includes(item.id)" class="inbox-changing">{{ t('conversationInbox.changing') }}</span>
        </div>
      </template>
      <p v-if="item.note" class="inbox-note">{{ item.note }}</p>
      <p v-if="errors?.[item.id]" class="inbox-error" role="alert">{{ errors[item.id] }}</p>
    </article>
    </div>
    <OfficeReferenceDialog v-model="officePreview" /><ShortcutReferenceDialog v-model="shortcutPreview" />
  </section>
</template>

<style scoped>
.conversation-inbox { display:flex; flex-direction:column; min-width:0; min-height:104px; max-height:min(28dvh,240px); box-sizing:border-box; margin:0; padding:10px 12px 4px; border:1px solid var(--el-border-color-light); border-radius:12px; background:var(--el-bg-color); overflow:hidden; }
.inbox-list { min-height:0; overflow-y:auto; overscroll-behavior:contain; scrollbar-gutter:stable; padding:0 3px 3px; scroll-padding:7px; }
.inbox-heading { display:flex; flex:0 0 auto; padding-bottom:3px; gap:7px; align-items:center; color:var(--el-text-color-secondary); font-size:12px; }
.inbox-heading span { font-variant-numeric:tabular-nums; }
.inbox-item { padding:10px 0; }
.inbox-item + .inbox-item { border-top:1px solid var(--el-border-color-lighter); }
.inbox-state { display:flex; flex-wrap:wrap; align-items:center; gap:7px; color:var(--el-text-color-secondary); font-size:12px; }
.state-dot { width:5px; height:5px; border-radius:50%; background:var(--el-text-color-placeholder); }
.state-dot.active { background:var(--el-color-primary); }
.inbox-timing { margin-left:auto; }
.inbox-text { margin:7px 0; white-space:pre-wrap; overflow-wrap:anywhere; max-height:84px; overflow:auto; font-size:13px; line-height:1.65; color:var(--el-text-color-primary); }
.inbox-actions { display:flex; gap:12px; align-items:center; flex-wrap:wrap; }
.inbox-actions button { display:inline-flex; gap:5px; align-items:center; border:0; border-radius:6px; padding:6px 0; background:transparent; color:var(--el-text-color-secondary); font:inherit; font-size:12px; cursor:pointer; }
.inbox-actions .inbox-guide { padding:6px 10px; color:var(--el-color-primary); background:var(--pg-accent-wash); }
.inbox-actions button:hover:not(:disabled) { color:var(--el-color-primary); }
.inbox-actions button:disabled { opacity:.55; cursor:default; }
.inbox-actions button:focus-visible,.inbox-editor:focus-visible { outline:2px solid var(--el-color-primary); outline-offset:3px; }
.inbox-editor { display:block; width:100%; box-sizing:border-box; min-height:76px; max-height:144px; margin:9px 0; padding:9px; resize:vertical; border:1px solid var(--el-border-color); border-radius:7px; background:var(--el-bg-color); color:var(--el-text-color-primary); font:inherit; font-size:13px; }
.ordered-editor { resize:none; max-height:180px; overflow-y:auto; }
.ordered-editor :deep(.inline-composer-document) { min-height:58px; max-height:140px; }
.inbox-note,.inbox-error,.inbox-changing { margin:8px 0 0; color:var(--el-text-color-secondary); font-size:12px; overflow-wrap:anywhere; }
.inbox-error { color:var(--el-color-danger); }
@media(pointer:coarse) { .inbox-actions button { min-height:44px; } .inbox-editor { font-size:16px; } }
</style>
