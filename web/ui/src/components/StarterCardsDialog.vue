<script setup lang="ts">
import { useI18n } from "vue-i18n";
import { nextTick, ref, watch } from "vue";
import type { PanelInfo, StarterCard, Starters } from "@/api/types";
import StarterCards from "@/components/StarterCards.vue";
import { cloneStarterDraft, type PrepareStarterDraft, type StarterDraft } from "@/components/starterCardDraft";

const open = defineModel<boolean>({ required: true });
const props = withDefaults(defineProps<{
  cards: StarterCard[];
  set?: Starters | null;
  panels?: PanelInfo[];
  readOnly?: boolean;
  onSave?: (cards: StarterCard[]) => Promise<void>;
  prepareDraft?: PrepareStarterDraft;
  maxCards?: number;
  agentId?: string;
  fullscreen?: boolean;
}>(), { readOnly: true });
const emit = defineEmits<{ pick: [StarterCard]; saved: []; closed: [] }>();
const { t } = useI18n();
const editor = ref<InstanceType<typeof StarterCards>>();
let pendingAdd: { draft?: StarterDraft } | undefined;
function applyPendingAdd() {
  if (!editor.value || !pendingAdd) return;
  const draft = pendingAdd.draft;
  pendingAdd = undefined;
  editor.value.openAdd(draft);
}
async function openAdd(draft?: StarterDraft) {
  if (props.readOnly) return;
  pendingAdd = { draft: draft ? cloneStarterDraft(draft) : undefined };
  open.value = true;
  await nextTick();
  applyPendingAdd();
}
watch(editor, applyPendingAdd);
defineExpose({ openAdd });
</script>

<template>
  <el-dialog
    v-model="open"
    :title="t('starterCards.shortcuts')"
    width="min(960px, calc(100vw - 32px))"
    :fullscreen="fullscreen"
    class="starter-dialog"
    data-testid="starter-dialog"
    append-to-body
    destroy-on-close
    @closed="emit('closed')"
  >
    <div class="shortcut-content">
      <StarterCards
        ref="editor"
        :cards="cards"
        :set="set"
        :panels="panels"
        :read-only="readOnly"
        :on-save="onSave"
        :prepare-draft="prepareDraft"
        :max-cards="maxCards"
        :agent-id="agentId"
        :fallback="t(cards.length ? 'starterCards.pickHint' : 'starterCards.emptyShortcuts')"
        show-downloads
        @pick="emit('pick', $event)"
        @saved="emit('saved')"
      />
    </div>
    <template v-if="cards.length" #footer>
      <p class="shortcut-hint">{{ t("starterCards.draftHint") }}</p>
    </template>
  </el-dialog>
</template>

<style scoped lang="scss">
.shortcut-content {
  max-height: 68vh;
  max-height: 68dvh;
  overflow-y: auto;
  padding: 8px;

  :deep(.starters) { padding: 0 0 4px; gap: 18px; }
  :deep(.heading) {
    align-self: flex-start;
    font-size: 13px;
    font-weight: 400;
    line-height: 1.6;
    text-align: left;
    color: var(--el-text-color-secondary);
  }
  :deep(.cards) { max-width: none; }
  :deep(.toolbar) {
    order: -2;
    width: 100%;
    justify-content: flex-start;
    gap: 8px;
  }
  :deep(.tool) { min-height: 34px; padding: 6px 12px; }
  :deep([data-testid="starter-add"]) {
    border-style: solid;
    border-color: var(--pg-accent-line);
    background: var(--el-color-primary-light-9);
    color: var(--el-color-primary);
  }
  :deep(.pager) {
    order: -1;
    position: sticky;
    top: 0;
    z-index: 2;
    margin-top: -8px;
    box-shadow: 0 2px 5px rgb(0 0 0 / 4%);
  }
  :deep(.card-title), :deep(.card-label) { overflow-wrap: anywhere; }
  :deep(.card:focus-visible) {
    outline: 2px solid var(--el-color-primary);
    outline-offset: -2px;
  }
}

.shortcut-hint {
  margin: 0;
  font-size: 12px;
  line-height: 1.6;
  text-align: left;
  color: var(--el-text-color-secondary);
}

@media (max-width: 560px) {
  .shortcut-content {
    :deep(.cards) { grid-template-columns: 1fr; }
  }
}
</style>

<style lang="scss">
.starter-dialog {
  display: flex;
  flex-direction: column;
  margin-top: 4vh;
  margin-top: 4dvh;
  max-height: 92vh;
  max-height: 92dvh;
  border-radius: 16px;

  .el-dialog__body { min-height: 0; overflow: hidden; }
  .el-dialog__footer { flex: 0 0 auto; }
}
.starter-dialog.is-fullscreen { margin:0; max-height:100dvh; border-radius:0; .el-dialog__body { flex:1; } .shortcut-content { max-height:none; height:100%; max-width:1320px; margin:auto; padding:20px 24px; box-sizing:border-box; } }
@media (max-width: 560px) {
  .starter-dialog { margin-top: 12px; max-height: calc(100dvh - 24px); }
}
</style>
