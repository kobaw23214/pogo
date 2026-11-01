<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Close, Refresh, Search } from "@element-plus/icons-vue";
import FileTree from "./FileTree.vue";
import FilePreview from "./FilePreview.vue";
import ChangesView from "./ChangesView.vue";
import QuickOpen from "./QuickOpen.vue";
import WorkspaceFileMenu from "./WorkspaceFileMenu.vue";
import { beginWorkspaceFileMutation, observeWorkspaceFileMutation, publishWorkspaceFileMutation, workspacePathContains, type WorkspaceFileTarget } from "./workspaceFileActions";
import { clearTreeCache, indexEpoch, invalidateIndex, invalidateTree, treeEpoch } from "./fileTreeStore";
import { collectChanges, changedPathCount } from "@/agent/changes";
import { api } from "@/api/client";
import type { TimelineItem } from "@/agent/timeline";
import type { OfficeReferenceSnapshot } from "@/api/types";
import type { OfficeViewContext } from "@/agent/officeImmersion";
import { isEditableOfficeFile } from "@/agent/officeFiles";

// The workspace file panel: two tabs — the file tree and this session's
// changes. Sizing, dragging and the dock edge all live in DockArea; this
// component fills whatever slot it is given and keeps the tabs and tools.
// The preview takes the whole panel rather than splitting it, so a narrow
// panel still gives file content full width.
const props = defineProps<{
  items: TimelineItem[];
  workspace?: string;
  sessionId?: string | null;
  officeSessions?: Record<string, string>;
  immersive?: boolean;
  revision?: number;
}>();
const emit = defineEmits<{ close: []; navigateFile: [path: string]; openOffice: [path: string]; focusOffice: [path: string]; officeClosed: [id: string]; officeEditing: [context: OfficeViewContext | null]; officeOpened: [context: OfficeViewContext]; officeReference: [snapshot: OfficeReferenceSnapshot]; exitImmersive: []; openCurrentBrowser: [request: { url: string; id: string; sessionId: string; path: string }]; openBrowser: [request: { url: string; id: string; sessionId: string }] }>();

const { t } = useI18n();

type View = "tree" | "file" | "changes";
const view = ref<View>("tree");
// backTo remembers which tab the preview was opened from.
const backTo = ref<"tree" | "changes">("tree");
const selected = ref("");
const officeView = ref<OfficeViewContext | null>(null);
function officeEditing(context: OfficeViewContext | null) {
  officeView.value = context;
  emit("officeEditing", context);
}
const preview = ref<{ prepareClose(): Promise<boolean>; refreshPreview(): Promise<boolean>; activeOfficePath(): string | null } | null>(null);

// Switching sessions can switch the workspace the panel is scoped to; a
// preview left over from the previous one would show a file this tree does
// not even list.
watch(
  () => props.workspace,
  async () => {
    if (view.value === "file" && preview.value && !await preview.value.prepareClose()) return;
    if (view.value === "file") view.value = "tree";
    selected.value = "";
  },
);
// epoch forces a tree/preview remount after a refresh: every still-expanded
// directory refetches, which is exactly what the refresh button promises.
const epoch = ref(0);

const menuTarget = ref<WorkspaceFileTarget | null>(null);
const menuX = ref(0), menuY = ref(0);
const action = ref<"rename" | "delete" | null>(null);
const actionTarget = ref<WorkspaceFileTarget | null>(null);
const actionName = ref("");
const renameInput = ref<{ input?: HTMLInputElement } | null>(null);
const actionError = ref("");
const actionBusy = ref(false);
let actionScope = "";
const currentScope = () => JSON.stringify([props.workspace || "",props.sessionId || ""]);
function showMenu(target: WorkspaceFileTarget, x: number, y: number) { if (actionBusy.value) return; menuTarget.value = target; menuX.value = x; menuY.value = y; }
function showAction(kind: "rename" | "delete", target: WorkspaceFileTarget) {
  menuTarget.value = null; actionTarget.value = { ...target }; actionName.value = target.name;
  actionError.value = ""; actionScope = currentScope(); action.value = kind;
}
function cancelAction() { if (!actionBusy.value) { action.value = null; actionTarget.value = null; actionError.value = ""; } }
function actionVisibility(value: boolean) { if (!value) cancelAction(); }
function focusAction() {
  if (action.value !== "rename") return;
  const input = renameInput.value?.input; if (!input) return;
  input.focus(); const dot = actionName.value.lastIndexOf(".");
  input.setSelectionRange(0, !actionTarget.value?.dir && dot > 0 ? dot : actionName.value.length);
}
const actionValid = computed(() => action.value === "delete" || (!!actionName.value.trim() && actionName.value !== actionTarget.value?.name && !/[\/\\\u0000\r\n]/.test(actionName.value) && actionName.value !== "." && actionName.value !== ".."));
async function submitAction() {
  const target = actionTarget.value, kind = action.value;
  if (actionBusy.value || !target || !kind || !actionValid.value) return;
  if (actionScope !== currentScope()) { cancelAction(); return; }
  const release = beginWorkspaceFileMutation(target.path);
  if (!release) { actionError.value = t("filesPanel.editingBlocked"); return; }
  actionBusy.value = true; actionError.value = "";
  try {
    const result = kind === "rename" ? await api.renameFile(target.path,actionName.value,target.mtime_ms,target.size) : await api.deleteFile(target.path,target.dir,target.mtime_ms,target.size);
    publishWorkspaceFileMutation(result);
    action.value = null; actionTarget.value = null;
  } catch (error) {
    // A lost response or a partially removed directory is not a promise that
    // nothing changed. Refresh listings without automatically replaying it.
    invalidateTree(); invalidateIndex();
    actionError.value = error instanceof Error ? error.message : String(error);
  }
  finally { release(); actionBusy.value = false; }
}
watch(() => [props.workspace,props.sessionId], () => { menuTarget.value = null; if (!actionBusy.value) cancelAction(); });
const removeMutationObserver = observeWorkspaceFileMutation(change => {
  if (selected.value && workspacePathContains(change.path,selected.value)) { selected.value = ""; view.value = "tree"; }
  indexPaths.value = null;
});
onBeforeUnmount(removeMutationObserver);

const changeCount = computed(() => changedPathCount(collectChanges(props.items)));

async function openFile(path: string) {
  emit("navigateFile", path);
  // Office owns one persistent hub sheet, shared with attachments and agent
  // opens. Navigating the file tree never tears down an unsaved editor.
  if (isEditableOfficeFile(path)) { emit("openOffice", path); return; }
  if (view.value === "file" && path !== selected.value && preview.value && !await preview.value.prepareClose()) return;
  if (view.value !== "file") backTo.value = view.value === "changes" ? "changes" : "tree";
  selected.value = path;
  view.value = "file";
}

async function refresh() {
  clearTreeCache();
  indexPaths.value = null;
  if (view.value === "file" && preview.value && !await preview.value.refreshPreview()) return;
  epoch.value++;
}
async function switchView(target: "tree" | "changes") {
  if (view.value === "file" && preview.value && !await preview.value.prepareClose()) return;
  view.value = target;
}

// --- quick open (⌘P / Ctrl+P) ----------------------------------------------
const searching = ref(false);
const indexPaths = ref<string[] | null>(null);

// The stream busts the index when a run ends (the agent's writes may have
// added files); dropping the cached paths means the next ⌘P rewalks lazily.
watch(indexEpoch, () => {
  indexPaths.value = null;
});

// Quick-open follows the same scoping as the tree: with a session workspace
// set, only paths under it are offered.
const scopedIndex = computed(() => {
  const all = indexPaths.value ?? [];
  const ws = props.workspace ?? "";
  return ws ? all.filter((p) => p.startsWith(ws + "/")) : all;
});

async function openSearch() {
  searching.value = true;
  if (indexPaths.value === null) {
    try {
      indexPaths.value = (await api.fileIndex()).paths;
    } catch {
      indexPaths.value = [];
    }
  }
}

async function openFromSearch(path: string) {
  await openFile(path);
  searching.value = false;
}

// ⌘P is the browser's print shortcut: intercept it only while the panel is
// open, which is exactly when the user means "open a file".
function onKeydown(e: KeyboardEvent) {
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "p") {
    e.preventDefault();
    if (searching.value) searching.value = false;
    else openSearch();
  }
}
onMounted(() => window.addEventListener("keydown", onKeydown));
onBeforeUnmount(() => window.removeEventListener("keydown", onKeydown));
defineExpose({ previewPath: () => view.value === "file" ? selected.value : null, activeOfficePath: () => preview.value?.activeOfficePath() || null });
</script>

<template>
  <section class="panel">
    <header v-if="immersive && officeView" class="head office-head">
      <strong class="office-title" :title="officeView.path">{{ officeView.path.split('/').pop() }}</strong>
      <button class="ghost office-exit" :title="t('immersiveBrowser.exitTip')" :aria-label="t('immersiveBrowser.exit')" @click="emit('exitImmersive')">
        <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true"><rect x="1.5" y="2.5" width="13" height="11" rx="2" stroke="currentColor"/><rect x="8" y="7" width="5" height="5" rx="1" fill="currentColor" opacity=".8"/></svg>
        <span>{{ t('immersiveBrowser.exit') }}</span>
      </button>
    </header>
    <header v-else class="head">
      <div class="tabs">
        <button class="tab" :class="{ on: view !== 'changes' }" @click="switchView('tree')">{{ t("filesPanel.tabFiles") }}</button>
        <button class="tab" :class="{ on: view === 'changes' }" @click="switchView('changes')">
          {{ t("filesPanel.tabChanges") }}<span v-if="changeCount" class="count">{{ changeCount }}</span>
        </button>
      </div>
      <!-- With a session workspace set the whole panel is scoped to it; say
           so, or the tree reads as "the root, mysteriously emptier". -->
      <span v-if="workspace" class="ws-root" :title="t('filesPanel.workspaceTitle', { ws: workspace })">/{{ workspace }}</span>
      <button class="ghost" :title="t('filesPanel.quickOpen')" @click="openSearch">
        <el-icon><Search /></el-icon>
      </button>
      <button class="ghost" :title="t('filesPanel.refresh')" @click="refresh">
        <el-icon><Refresh /></el-icon>
      </button>
      <button class="ghost" :title="t('common.close')" @click="emit('close')">
        <el-icon><Close /></el-icon>
      </button>
    </header>
    <div class="content">
      <QuickOpen v-if="searching" :paths="scopedIndex" @open="openFromSearch" @close="searching = false" />
      <div v-if="!searching && view === 'tree'" class="scroll">
        <!-- The tree roots at the session's workspace and re-roots itself
             smoothly on a switch (hold-dim-swap inside FileTree), so the
             key deliberately does NOT include the workspace. Emitted paths
             stay root-relative, so preview and save need no mapping. -->
        <FileTree
          :key="`${epoch}-${treeEpoch}`"
          :path="workspace ?? ''"
          :depth="0"
          :selected="selected"
          @open="openFile"
          @context="showMenu"
        />
      </div>
      <FilePreview v-if="view === 'file'" v-show="!searching" ref="preview" :key="`${selected}#${epoch}`" :path="selected" :session-id="sessionId" :revision="revision" :office-session-id="officeSessions?.[selected]" :immersive="immersive" @back="view = backTo" @focus-office="emit('focusOffice', $event)" @open-copy="emit('openOffice', $event)" @office-closed="emit('officeClosed', $event)" @office-editing="officeEditing" @office-opened="emit('officeOpened', $event)" @office-reference="emit('officeReference', $event)" @open-browser="emit('openBrowser', $event)" @open-current-browser="emit('openCurrentBrowser', $event)" />
      <div v-if="!searching && view === 'changes'" class="scroll" :key="`changes-${epoch}`">
        <ChangesView
          :items="items"
          :workspace="workspace ?? ''"
          @preview="openFile"
        />
      </div>
    </div>
    <WorkspaceFileMenu :target="menuTarget" :x="menuX" :y="menuY" @close="menuTarget = null" @rename="showAction('rename', $event)" @delete="showAction('delete', $event)" />
    <el-dialog :model-value="!!action" :title="t(action === 'rename' ? 'filesPanel.renameTitle' : 'filesPanel.deleteTitle')" width="min(460px, calc(100vw - 32px))" append-to-body :close-on-click-modal="!actionBusy" :close-on-press-escape="!actionBusy" :show-close="!actionBusy" @update:model-value="actionVisibility" @opened="focusAction">
      <div v-if="actionTarget" class="file-action-content">
        <strong class="file-action-name">{{ actionTarget.name }}</strong>
        <span class="file-action-path">{{ actionTarget.path }}</span>
        <template v-if="action === 'rename'">
          <label for="workspace-file-name">{{ t('filesPanel.newName') }}</label>
          <el-input id="workspace-file-name" ref="renameInput" v-model="actionName" :disabled="actionBusy" maxlength="255" @keydown.enter.prevent="submitAction" />
        </template>
        <p v-else class="delete-note">{{ t(actionTarget.dir ? 'filesPanel.deleteFolderBody' : 'filesPanel.deleteFileBody') }}</p>
        <p v-if="actionError" class="file-action-error" role="alert">{{ actionError }}</p>
      </div>
      <template #footer>
        <el-button :disabled="actionBusy" @click="cancelAction">{{ t('common.cancel') }}</el-button>
        <el-button :type="action === 'delete' ? 'danger' : 'primary'" :disabled="!actionValid" :loading="actionBusy" @click="submitAction">{{ t(action === 'rename' ? 'filesPanel.rename' : 'filesPanel.deleteConfirm') }}</el-button>
      </template>
    </el-dialog>
  </section>
</template>

<style scoped lang="scss">
.panel {
  height: 100%;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  background: var(--el-bg-color);
}

.head {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 8px 10px;
  border-bottom: 1px solid var(--el-border-color-lighter);
}

.office-head { min-height: 36px; box-sizing: border-box; padding: 4px 10px; }
.office-title { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; }
.office-exit { display: inline-flex; align-items: center; gap: 6px; height: 28px; padding: 0 9px; color: var(--el-color-primary); background: var(--el-color-primary-light-9); border: 1px solid var(--el-border-color-lighter); border-radius: 7px; font-size: 11px; }

.ws-root {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 11px;
  font-family: ui-monospace, monospace;
  color: var(--el-text-color-secondary);
}

.tabs {
  flex: 1;
  display: flex;
  gap: 2px;
  padding-left: 6px;
}

.tab {
  border: 0;
  background: transparent;
  padding: 3px 8px;
  border-radius: 4px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  gap: 4px;

  &.on {
    background: var(--el-fill-color);
    color: var(--el-text-color-primary);
    font-weight: 600;
  }

  .count {
    font-size: 10px;
    padding: 0 5px;
    border-radius: 8px;
    background: var(--el-fill-color-darker);
    color: var(--el-text-color-primary);
    font-variant-numeric: tabular-nums;
  }
}

.ghost {
  display: inline-flex;
  border: 0;
  background: transparent;
  padding: 4px;
  border-radius: 4px;
  font-size: 14px;
  color: var(--el-text-color-secondary);
  cursor: pointer;

  &:hover {
    background: var(--el-fill-color);
  }
}

.content {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
}

.scroll {
  flex: 1;
  overflow: auto;
  padding: 6px 4px 12px;
}
.file-action-content { display: flex; flex-direction: column; gap: 10px; }
.file-action-name { color: var(--el-text-color-primary); overflow-wrap: anywhere; }
.file-action-path { color: var(--el-text-color-secondary); font-size: 12px; overflow-wrap: anywhere; }
.file-action-content label { margin-top: 7px; color: var(--el-text-color-regular); font-size: 13px; }
.delete-note { margin: 8px 0 0; line-height: 1.7; }
.file-action-error { margin: 0; color: var(--el-color-danger); line-height: 1.6; }
</style>
