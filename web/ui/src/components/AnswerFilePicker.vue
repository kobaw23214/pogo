<script setup lang="ts">
import { ref } from "vue";
import { Icon } from "@iconify/vue";
import { useI18n } from "vue-i18n";
import { fileIcon } from "./fileIcons";

interface FileChoice {
  name: string;
  workspace: string;
  candidates: Array<{ path: string; name: string }>;
  incomplete: boolean;
  error?: string;
}

defineProps<{ choice: FileChoice | null }>();
const emit = defineEmits<{ choose: [path: string]; close: [] }>();
const { t } = useI18n();
const list = ref<HTMLElement | null>(null);
const cancel = ref<HTMLButtonElement | null>(null);

function parentPath(path: string): string {
  const separator = path.lastIndexOf("/");
  return separator < 0 ? t("workspaceFiles.pickerRoot") : path.slice(0, separator);
}

function visibility(visible: boolean) {
  if (!visible) emit("close");
}

function focusChoice() {
  const first = list.value?.querySelector<HTMLButtonElement>("button");
  (first ?? cancel.value)?.focus();
}
</script>

<template>
  <el-dialog
    :model-value="choice !== null"
    :title="t('workspaceFiles.pickerTitle')"
    width="min(640px, calc(100vw - 32px))"
    class="answer-file-picker"
    append-to-body
    destroy-on-close
    @update:model-value="visibility"
    @opened="focusChoice"
  >
    <div v-if="choice" class="afp-content">
      <div class="afp-file">
        <Icon class="afp-file-icon" :icon="fileIcon(choice.name)" aria-hidden="true" />
        <span :title="choice.name">{{ choice.name }}</span>
      </div>

      <div class="afp-workspace">
        <span>{{ t('workspaceFiles.pickerWorkspace') }}</span>
        <code :title="choice.workspace">{{ choice.workspace }}</code>
      </div>

      <p v-if="choice.incomplete" class="afp-notice" role="status">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true"><path d="m12 3 10 18H2L12 3Z" stroke-linejoin="round" /><path d="M12 9v4m0 3h.01" stroke-linecap="round" /></svg>
        <span>{{ t('workspaceFiles.pickerIncomplete') }}</span>
      </p>

      <template v-if="choice.candidates.length">
        <div class="afp-list-heading">
          <span>{{ t('workspaceFiles.pickerHint') }}</span>
          <span class="afp-count">{{ t('workspaceFiles.pickerCount', { count: choice.candidates.length }) }}</span>
        </div>
        <p v-if="choice.error" class="afp-error" role="alert">{{ choice.error }}</p>
        <div ref="list" class="afp-list" :aria-label="t('workspaceFiles.pickerMatches')">
          <button
            v-for="candidate in choice.candidates"
            :key="candidate.path"
            type="button"
            class="afp-row"
            :title="candidate.path"
            :aria-label="t('workspaceFiles.pickerOpen', { name: candidate.name, path: candidate.path })"
            @click="emit('choose', candidate.path)"
          >
            <Icon class="afp-row-icon" :icon="fileIcon(candidate.name)" aria-hidden="true" />
            <span class="afp-row-text">
              <strong>{{ candidate.name }}</strong>
              <span class="afp-parent">{{ parentPath(candidate.path) }}</span>
            </span>
            <svg class="afp-arrow" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true"><path d="m9 5 7 7-7 7" stroke-linecap="round" stroke-linejoin="round" /></svg>
          </button>
        </div>
      </template>

      <div v-else class="afp-empty" :role="choice.error ? 'alert' : 'status'">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true"><path d="M14 3H6a1 1 0 0 0-1 1v16a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8z" /><path d="M14 3v5h5M9 12h6m-6 4h4" stroke-linecap="round" stroke-linejoin="round" /></svg>
        <strong>{{ t(choice.error ? 'workspaceFiles.pickerFailedTitle' : choice.incomplete ? 'workspaceFiles.pickerIncompleteTitle' : 'workspaceFiles.pickerEmptyTitle') }}</strong>
        <p>{{ choice.error || t(choice.incomplete ? 'workspaceFiles.pickerIncompleteEmpty' : 'workspaceFiles.pickerEmpty') }}</p>
      </div>
    </div>
    <template #footer>
      <button ref="cancel" type="button" class="afp-cancel" @click="emit('close')">{{ t('workspaceFiles.pickerCancel') }}</button>
    </template>
  </el-dialog>
</template>

<style scoped>
.afp-content { min-width: 0; }
.afp-file { display: flex; align-items: center; gap: 10px; min-width: 0; margin-bottom: 14px; font-size: 15px; font-weight: 600; color: var(--el-text-color-primary); }
.afp-file > span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.afp-file-icon { width: 25px; height: 25px; flex: 0 0 auto; }
.afp-workspace { display: flex; flex-direction: column; gap: 4px; margin-bottom: 18px; color: var(--el-text-color-secondary); font-size: 12px; }
.afp-workspace code { color: var(--el-text-color-regular); line-height: 1.6; overflow-wrap: anywhere; font-size: 11px; }
.afp-notice { display: flex; align-items: flex-start; gap: 8px; margin: 0 0 16px; padding: 10px 12px; border-radius: 8px; background: var(--el-color-warning-light-9); color: var(--el-color-warning-dark-2); font-size: 12px; line-height: 1.65; }
.afp-notice svg { flex: 0 0 auto; width: 16px; height: 16px; margin-top: 2px; }
.afp-list-heading { display: flex; flex-wrap: wrap; align-items: baseline; justify-content: space-between; gap: 6px 12px; margin-bottom: 9px; color: var(--el-text-color-secondary); font-size: 12px; line-height: 1.6; }
.afp-count { flex: 0 0 auto; font-variant-numeric: tabular-nums; }
.afp-list { display: flex; flex-direction: column; gap: 7px; max-height: min(360px, 48dvh); padding: 2px; margin: -2px; overflow-y: auto; overscroll-behavior: contain; }
.afp-row { display: flex; align-items: center; gap: 11px; width: 100%; min-width: 0; padding: 12px; border: 1px solid var(--el-border-color-lighter); border-radius: 9px; background: var(--el-bg-color); color: var(--el-text-color-primary); font: inherit; text-align: left; cursor: pointer; transition: border-color 140ms ease, background 140ms ease; }
.afp-row:hover { background: var(--el-fill-color-light); border-color: var(--el-border-color); }
.afp-row:focus-visible, .afp-cancel:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 2px; }
.afp-row-icon { flex: 0 0 auto; width: 24px; height: 24px; }
.afp-row-text { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 4px; }
.afp-row-text strong { font-size: 13px; line-height: 1.5; font-weight: 550; overflow-wrap: anywhere; }
.afp-parent { color: var(--el-text-color-secondary); font: 11px/1.6 ui-monospace, SFMono-Regular, Menlo, monospace; overflow-wrap: anywhere; }
.afp-arrow { flex: 0 0 auto; width: 15px; height: 15px; color: var(--el-text-color-placeholder); }
.afp-empty { display: flex; flex-direction: column; align-items: center; gap: 8px; padding: 20px 12px 16px; text-align: center; color: var(--el-text-color-secondary); }
.afp-empty > svg { width: 31px; height: 31px; margin-bottom: 3px; color: var(--el-text-color-placeholder); }
.afp-empty > strong { font-size: 13px; font-weight: 550; color: var(--el-text-color-regular); }
.afp-empty > p { margin: 0; max-width: 420px; font-size: 12px; line-height: 1.75; overflow-wrap: anywhere; }
.afp-error { margin: 0 0 10px; color: var(--el-color-danger); font-size: 12px; line-height: 1.6; overflow-wrap: anywhere; }
.afp-cancel { padding: 7px 15px; border: 1px solid var(--el-border-color); border-radius: 8px; background: var(--el-bg-color); color: var(--el-text-color-regular); font: inherit; font-size: 13px; cursor: pointer; }
.afp-cancel:hover { background: var(--el-fill-color-light); }
@media (prefers-reduced-motion: reduce) { .afp-row { transition: none; } }
</style>

<style>
.answer-file-picker { max-height: calc(100dvh - 40px); margin-top: 10dvh; overflow: auto; }
.answer-file-picker .el-dialog__body { padding-top: 6px; }
.answer-file-picker .el-dialog__footer { padding-top: 16px; }
@media (max-height: 600px) { .answer-file-picker { margin-top: 20px; } }
</style>
