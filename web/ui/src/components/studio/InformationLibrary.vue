<script setup lang="ts">
import { computed, onBeforeUnmount, provide, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Icon } from "@iconify/vue";
import { fileIcon, folderIcon, folderOpenIcon } from "@/components/fileIcons";
import { ElMessage } from "element-plus";
import { Folder, Collection, Search, Plus, ArrowRight, ArrowDown, Refresh, Grid, List, Upload, MoreFilled, Close, View, Expand, Fold } from "@element-plus/icons-vue";
import { informationApi, InformationAPIError, type InformationEntry, type InformationTree } from "@/api/information";
import { shortcutsApi, shortcutEntries, type ShortcutSnapshot } from "@/api/shortcuts";
import { platformApi } from "@/api/platform";
import { api } from "@/api/client";
import type { PlatformAgent } from "@/agent/platformTypes";
import type { MessageInputPart, SkillInfo } from "@/api/types";
import { informationDescendants, informationPath, informationVisible, restoreInformationView } from "@/agent/informationLibrary";
import { fileComposerPart, shortcutComposerPart } from "@/agent/composerParts";
import { useInformationFilePicker } from "@/agent/useInformationFilePicker";
import { shortcutSearchSegments } from "@/components/shortcutPresentation";
import { validateInputParts } from "@/agent/shortcutStructure";
import { registerStudioNavigationGuard } from "@/agent/studioNavigation";
import { randomUUID } from "@/utils/uuid";
import ShortcutEditorDialog from "@/components/ShortcutEditorDialog.vue";
import ShortcutDetails from "@/components/ShortcutDetails.vue";
import InformationEntryDialog from "@/components/InformationEntryDialog.vue";
import InformationMoveTargetTree from "./InformationMoveTargetTree.vue";
import { informationCanMoveTo } from "./informationMoveTargets";
import { workspaceFileNavigationKey } from "@/agent/workspaceFileNavigation";
import ConfirmDialog from "@/components/ConfirmDialog.vue";

const props = withDefaults(defineProps<{ picker?: boolean; filesOnly?: boolean; disabled?: boolean; active?: boolean; agents?: PlatformAgent[] }>(), { picker: false, filesOnly: false, disabled: false, active: true });
const emit = defineEmits<{ select: [entry: InformationEntry] }>();
const { t, locale } = useI18n();
provide(workspaceFileNavigationKey, { scope: () => null, open: () => {} });
const tree = ref<InformationTree | null>(null), loading = ref(false), error = ref(""), busy = ref(false);
const folder = ref(""), selected = ref(""), mode = ref<"list" | "grid">("list"), flat = ref(false), query = ref(""), expanded = ref<string[]>([]);
const full = ref<InformationEntry | null>(null), detailLoading = ref(false), detailError = ref("");
const previewOpen = ref(false), previewTarget = ref<InformationEntry | null>(null);
const fileInput = ref<HTMLInputElement>(), dragOver = ref(false), uploadLabel = ref("");
const nameDialog = ref(false), nameAction = ref<"folder" | "rename">("folder"), nameDraft = ref(""), actionTarget = ref<InformationEntry | null>(null), actionScope = ref(""), actionError = ref("");
const moveDialog = ref(false), moveParent = ref(""), deleteDialog = ref(false);
const editOpen = ref(false), editExisting = ref(false), editItem = ref<ShortcutSnapshot>({ id: "", title: "", content: "", icon: "" }), editEntry = ref<InformationEntry | null>(null), editParent = ref(""), editScope = ref("");
const skills = ref<SkillInfo[]>([]);
const referenceChoices = computed(() => entries.value.filter(entry => entry.kind === "shortcut").map(entry => ({ id: entry.id, title: entry.name })));
const importOpen = ref(false), sourceAgent = ref(""), sourceItems = ref<ShortcutSnapshot[]>([]), sourceID = ref(""), sourceLoading = ref(false), sourceError = ref(""), sourceAgents = ref<PlatformAgent[]>([]);
const confirm = ref<InstanceType<typeof ConfirmDialog> | null>(null);
let disposed = false, loadSequence = 0, detailSequence = 0, sourceSequence = 0, moveSequence = 0;
let controller: AbortController | undefined, detailController: AbortController | undefined;
const entries = computed(() => tree.value?.entries || []);
const browsableEntries = computed(() => props.filesOnly ? entries.value.filter(entry => entry.kind !== "shortcut") : entries.value);
const writable = computed(() => !props.picker && !!tree.value?.can_write && !props.disabled);
const visible = computed(() => informationVisible(browsableEntries.value, folder.value, query.value, flat.value));
const crumbs = computed(() => informationPath(entries.value, folder.value));
const selectedMeta = computed(() => browsableEntries.value.find(entry => entry.id === selected.value));
const treeRows = computed(() => {
  const rows: { entry: InformationEntry; depth: number }[] = [], seen = new Set<string>();
  const walk = (parent: string, depth: number) => { for (const entry of informationVisible(browsableEntries.value, parent, "", false)) { if (seen.has(entry.id)) continue; seen.add(entry.id); rows.push({ entry, depth }); if (entry.kind === "folder" && expanded.value.includes(entry.id)) walk(entry.id, depth + 1); } };
  walk("", 0); return rows;
});
const currentMoveEntry = computed(() => entries.value.find(entry => entry.id === actionTarget.value?.id));
const currentMoveParent = computed(() => currentMoveEntry.value?.parent_id ?? actionTarget.value?.parent_id ?? "");
const moveSelectionDirty = computed(() => moveDialog.value && moveParent.value !== currentMoveParent.value);
const moveCanSubmit = computed(() => moveDialog.value && !!tree.value && !!currentMoveEntry.value && writable.value && editableScopesCurrent.value && !busy.value && !loading.value && moveParent.value !== currentMoveParent.value && informationCanMoveTo(entries.value, currentMoveEntry.value.id, moveParent.value));
const descendants = computed(() => { const ids = actionTarget.value ? informationDescendants(entries.value, actionTarget.value.id) : new Set<string>(); return entries.value.filter(entry => ids.has(entry.id) && entry.id !== actionTarget.value?.id); });
const deleteCounts = computed(() => ({ folders: descendants.value.filter(entry => entry.kind === "folder").length, files: descendants.value.filter(entry => entry.kind === "file").length, shortcuts: descendants.value.filter(entry => entry.kind === "shortcut").length }));
const selectedSource = computed(() => sourceItems.value.find(item => item.id === sourceID.value));
const editableScopesCurrent = computed(() => actionScope.value === tree.value?.scope_id);
function pathLabel(id: string) { return [t("information.root"), ...informationPath(entries.value, id).map(entry => entry.name)].join(" / "); }
function iconFor(entry: InformationEntry, open = false) { return entry.kind === "folder" ? (open ? folderOpenIcon : folderIcon) : fileIcon(entry.file?.name || entry.name); }
function preview(item: InformationEntry) { if (props.disabled) return; if (item.kind === "folder") { openFolder(item); return; } previewTarget.value = item; previewOpen.value = true; }
function bytes(value: number) { return value < 1024 ? `${value} B` : value < 1024 ** 2 ? `${(value / 1024).toFixed(1)} KiB` : `${(value / 1024 ** 2).toFixed(1)} MiB`; }
function date(value: string) { const d = new Date(value); return Number.isNaN(d.getTime()) ? value : d.toLocaleString(locale.value, { dateStyle: "short", timeStyle: "short" }); }
function errorText(value: unknown) { return value instanceof Error ? value.message : String(value); }
function persist() { if (!tree.value) return; try { localStorage.setItem(`pogo:information:view:${tree.value.scope_id}`, JSON.stringify({ folder: folder.value, selected: selected.value, mode: mode.value, flat: flat.value, expanded: expanded.value })); } catch { /* Browsing remains available when preferences cannot be saved. */ } }
watch([folder, selected, mode, flat, expanded], persist, { deep: true });
function adopt(next: InformationTree) {
  const priorScope = tree.value?.scope_id, changed = priorScope !== next.scope_id; tree.value = next;
  if (changed) { let raw: string | null = null; try { raw = localStorage.getItem(`pogo:information:view:${next.scope_id}`); } catch { /* no local preferences */ } const state = restoreInformationView(raw, next.entries); folder.value = state.folder; selected.value = state.selected; mode.value = state.mode; flat.value = state.flat; expanded.value = state.expanded; if (priorScope) query.value = ""; }
  else { if (folder.value && !next.entries.some(entry => entry.id === folder.value && entry.kind === "folder")) folder.value = ""; if (selected.value && !next.entries.some(entry => entry.id === selected.value)) selected.value = ""; expanded.value = expanded.value.filter(id => next.entries.some(entry => entry.id === id && entry.kind === "folder")); }
}
async function load() {
  if (busy.value) return;
  const ticket = ++loadSequence; controller?.abort(); const current = new AbortController(); controller = current; loading.value = true; error.value = "";
  try { const result = await informationApi.tree(current.signal); if (!disposed && ticket === loadSequence) adopt(result); }
  catch (value) { if (!disposed && ticket === loadSequence && !current.signal.aborted) error.value = t("information.loadFailed", { message: errorText(value) }); }
  finally { if (!disposed && ticket === loadSequence) loading.value = false; }
}
watch(() => props.active, active => { if (active && !tree.value && !loading.value) void load(); }, { immediate: true });
async function loadDetail() {
  const item = selectedMeta.value, scope = tree.value?.scope_id, ticket = ++detailSequence; detailController?.abort(); full.value = null; detailError.value = ""; detailLoading.value = false;
  if (!props.picker || !item || item.kind === "folder") return;
  const current = new AbortController(); detailController = current; detailLoading.value = true;
  try {
    const result = await informationApi.entry(item.id, item.version, current.signal);
    if (!disposed && ticket === detailSequence && tree.value?.scope_id === scope) full.value = result;
  } catch (value) { if (!disposed && ticket === detailSequence && !current.signal.aborted) detailError.value = t("information.previewError", { message: errorText(value) }); }
  finally { if (!disposed && ticket === detailSequence) detailLoading.value = false; }
}
watch(() => [selected.value, selectedMeta.value?.version, tree.value?.scope_id], () => { void loadDetail(); });
watch(() => tree.value?.scope_id, () => { previewOpen.value = false; previewTarget.value = null; }, { flush: "sync" });
watch(() => [tree.value?.scope_id, tree.value?.can_write, props.disabled, props.picker], () => { moveSequence++; }, { flush: "sync" });
function selectItem(item: InformationEntry) {
  if (props.disabled) return;
  if (item.kind === "folder") { openFolder(item); return; }
  selected.value = item.id;
  if (!props.picker) preview(item);
}
function openFolder(item?: InformationEntry) { folder.value = item?.id || ""; selected.value = ""; query.value = ""; flat.value = false; if (item) expanded.value = [...new Set([...expanded.value, ...informationPath(entries.value, item.id).map(entry => entry.id)])]; }
function toggle(item: InformationEntry) { expanded.value = expanded.value.includes(item.id) ? expanded.value.filter(id => id !== item.id) : [...expanded.value, item.id]; }
function onTree(item: InformationEntry) { if (item.kind === "folder") openFolder(item); else selectItem(item); }
function mutationError(value: unknown) {
  if (value instanceof InformationAPIError && value.current && value.current.scope_id === tree.value?.scope_id) adopt(value.current);
  return value instanceof InformationAPIError && value.code === "INFORMATION_CONFLICT" ? t("information.conflict") : value instanceof InformationAPIError && value.code === "INFORMATION_NAME_CONFLICT" ? t("information.nameConflict") : errorText(value);
}
function beginAction(item: InformationEntry | null) { actionTarget.value = item; actionScope.value = tree.value?.scope_id || ""; actionError.value = ""; }
function newFolder() { if (!writable.value || busy.value) return; beginAction(null); nameAction.value = "folder"; nameDraft.value = ""; nameDialog.value = true; }
function rename(item: InformationEntry) { if (!writable.value || busy.value) return; beginAction(item); nameAction.value = "rename"; nameDraft.value = item.name; nameDialog.value = true; }
async function saveName() {
  if (!tree.value || busy.value || !writable.value || !nameDraft.value.trim()) return;
  if (!editableScopesCurrent.value) { actionError.value = t("information.scopeChanged"); return; }
  busy.value = true; actionError.value = "";
  try { const result = nameAction.value === "folder" ? await informationApi.create({ revision: tree.value.revision, parent_id: folder.value, kind: "folder", name: nameDraft.value.trim() }) : await informationApi.update(actionTarget.value!.id, { revision: tree.value.revision, name: nameDraft.value.trim() }); if (disposed) return; adopt(result); nameDialog.value = false; if (result.entry) selected.value = result.entry.id; ElMessage.success(t("information.saved")); }
  catch (value) { if (!disposed) actionError.value = mutationError(value); }
  finally { if (!disposed) busy.value = false; }
}
function askMove(item: InformationEntry) { if (!writable.value || busy.value || loading.value) return; const current = entries.value.find(entry => entry.id === item.id); if (!current) return; moveSequence++; beginAction(current); moveParent.value = current.parent_id; moveDialog.value = true; }
async function move() {
  if (!moveCanSubmit.value || !tree.value || !currentMoveEntry.value) return;
  const scope = tree.value.scope_id, id = currentMoveEntry.value.id, ticket = moveSequence;
  const current = () => !disposed && ticket === moveSequence && scope === tree.value?.scope_id && actionScope.value === scope && actionTarget.value?.id === id && moveDialog.value && writable.value;
  busy.value = true; actionError.value = "";
  try { const result = await informationApi.update(id, { revision: tree.value.revision, parent_id: moveParent.value }); if (!current()) return; if (result.scope_id !== scope) { actionError.value = t("information.scopeChanged"); return; } adopt(result); moveDialog.value = false; ElMessage.success(t("information.saved")); }
  catch (value) { if (current()) actionError.value = mutationError(value); }
  finally { if (!disposed) busy.value = false; }
}
async function closeMove() { const ticket = moveSequence; if (await confirmLeave() && !disposed && !busy.value && ticket === moveSequence) { moveDialog.value = false; moveSequence++; } }
function askDelete(item: InformationEntry) { if (!writable.value || busy.value) return; beginAction(item); deleteDialog.value = true; }
async function remove() {
  if (!tree.value || !actionTarget.value || busy.value || !writable.value || !editableScopesCurrent.value) return;
  busy.value = true; actionError.value = "";
  try { const result = await informationApi.remove(actionTarget.value.id, tree.value.revision, actionTarget.value.kind === "folder"); if (disposed) return; adopt(result); deleteDialog.value = false; ElMessage.success(t("information.removed")); }
  catch (value) { if (!disposed) actionError.value = mutationError(value); }
  finally { if (!disposed) busy.value = false; }
}
async function upload(files: File[]) {
  if (!tree.value || !writable.value || busy.value || !files.length) return;
  const scope = tree.value.scope_id, parent = folder.value; busy.value = true; error.value = "";
  try { for (let index = 0; index < files.length; index++) { const file = files[index]!; uploadLabel.value = t("information.uploadProgress", { name: file.name, index: index + 1, total: files.length }); const result = await informationApi.upload(file, parent, tree.value!.revision); if (disposed || tree.value?.scope_id !== scope) return; adopt(result); if (result.entry) selected.value = result.entry.id; } if (!disposed) ElMessage.success(t("information.uploaded")); }
  catch (value) { if (!disposed) error.value = mutationError(value); }
  finally { if (!disposed) { busy.value = false; uploadLabel.value = ""; } }
}
function picked(event: Event) { const input = event.target as HTMLInputElement; void upload(Array.from(input.files || [])); input.value = ""; }
function dropped(event: DragEvent) { dragOver.value = false; if (!event.dataTransfer?.files.length) return; event.preventDefault(); void upload(Array.from(event.dataTransfer.files)); }
async function openEditor(item?: InformationEntry) {
  if (!tree.value || busy.value || !writable.value) return;
  const scope = tree.value.scope_id; busy.value = true; error.value = "";
  try {
    const fullEntry = item ? await informationApi.entry(item.id, item.version) : null;
    const catalog = await api.skills().catch(() => null);
    if (disposed || tree.value?.scope_id !== scope) return;
    skills.value = catalog?.skills || [];
    editItem.value = fullEntry?.shortcut ? JSON.parse(JSON.stringify(fullEntry.shortcut)) : { id: randomUUID(), title: "", content: "", icon: "" };
    editEntry.value = fullEntry; editExisting.value = !!fullEntry; editParent.value = item?.parent_id || folder.value; editScope.value = scope; editOpen.value = true;
  } catch (value) { if (!disposed) error.value = mutationError(value); }
  finally { if (!disposed) busy.value = false; }
}
async function resolveReference(id: string) {
  const metadata = entries.value.find(entry => entry.id === id && entry.kind === "shortcut");
  if (!metadata) throw new Error(t("information.scopeChanged"));
  const result = await informationApi.entry(metadata.id, metadata.version);
  if (!result.shortcut) throw new Error(t("information.scopeChanged"));
  return result.shortcut;
}
async function prepareParts(parts: MessageInputPart[]) {
  const check = (list: MessageInputPart[]): boolean => list.every(part => part.type === "file" ? /^information-assets\/[a-f0-9]{64}\/[^/\\\u0000]+$/.test(part.path) : part.type === "shortcut" && part.parts ? check(part.parts) : true);
  if (!check(parts)) throw new Error(t("information.externalAsset"));
  if (validateInputParts(parts)) throw new Error(t("nestedShortcuts.invalid"));
  return parts;
}
async function saveShortcut(item: ShortcutSnapshot) {
  if (!tree.value || tree.value.scope_id !== editScope.value) throw new Error(t("information.scopeChanged"));
  const scope = tree.value.scope_id;
  try { const result = editEntry.value ? await informationApi.update(editEntry.value.id, { revision: tree.value.revision, name: item.title, shortcut: item }) : await informationApi.create({ revision: tree.value.revision, parent_id: editParent.value, kind: "shortcut", name: item.title, shortcut: item }); if (disposed || tree.value?.scope_id !== scope) throw new Error(t("information.scopeChanged")); adopt(result); if (result.entry) selected.value = result.entry.id; ElMessage.success(t("information.saved")); }
  catch (value) { throw new Error(mutationError(value)); }
}
async function uploadAsset(file: File) { const result = await informationApi.uploadAsset(file); return result.attachment; }
const informationPicker = useInformationFilePicker({
  allowedKinds: ["file", "shortcut"],
  scope: () => JSON.stringify([tree.value?.scope_id, editOpen.value, editItem.value.id, props.active]),
  resolve: async entry => {
    if (!tree.value || tree.value.scope_id !== editScope.value) throw new Error(t("information.scopeChanged"));
    const fullEntry = await informationApi.entry(entry.id, entry.version);
    if (fullEntry.kind === "shortcut" && fullEntry.shortcut) return [shortcutComposerPart({ ...fullEntry.shortcut, id: `${fullEntry.id}_v${fullEntry.version}` })];
    if (fullEntry.kind !== "file" || !fullEntry.file) throw new Error(t("nestedShortcuts.invalid"));
    const file = fullEntry.file;
    return [fileComposerPart({ key: file.asset, path: file.asset, label: file.name, mime: file.mime, size: file.size, status: "ready", asFile: true })];
  },
  errorText: value => t("nestedShortcuts.insertFailed", { message: errorText(value) }),
});
const { open: informationPickerOpen, busy: informationPickerBusy, error: informationPickerError } = informationPicker;
async function showImport() {
  if (!writable.value || busy.value) return;
  beginAction(null); sourceID.value = ""; sourceItems.value = []; sourceError.value = ""; sourceAgent.value = ""; importOpen.value = true;
  try { sourceAgents.value = props.agents || (await platformApi.list()).agents; } catch (value) { sourceError.value = t("information.importSourceError", { message: errorText(value) }); }
  await loadSource();
}
async function loadSource() {
  const agent = sourceAgent.value, ticket = ++sourceSequence; sourceLoading.value = true; sourceItems.value = []; sourceID.value = ""; sourceError.value = "";
  try { const result = await shortcutsApi.list(agent); if (!disposed && ticket === sourceSequence && importOpen.value && sourceAgent.value === agent) sourceItems.value = shortcutEntries(result); }
  catch (value) { if (!disposed && ticket === sourceSequence) sourceError.value = t("information.importSourceError", { message: errorText(value) }); }
  finally { if (!disposed && ticket === sourceSequence) sourceLoading.value = false; }
}
async function importSelected() {
  if (!tree.value || busy.value || !sourceID.value || !editableScopesCurrent.value) return;
  busy.value = true; actionError.value = "";
  try { const result = await informationApi.importShortcut({ revision: tree.value.revision, parent_id: folder.value, source_agent_id: sourceAgent.value, shortcut_id: sourceID.value }); if (disposed) return; adopt(result); importOpen.value = false; if (result.entry) selected.value = result.entry.id; ElMessage.success(t("information.imported")); }
  catch (value) { if (!disposed) actionError.value = mutationError(value); }
  finally { if (!disposed) busy.value = false; }
}
function reference() { if (full.value && full.value.id === selectedMeta.value?.id && full.value.version === selectedMeta.value?.version && full.value.kind !== "folder" && (!props.filesOnly || full.value.kind === "file") && !props.disabled && !busy.value) emit("select", JSON.parse(JSON.stringify(full.value))); }
async function closeName() { if (await confirmLeave()) nameDialog.value = false; }
async function confirmLeave() {
  if (busy.value) return false;
  const dirtyName = nameDialog.value && nameDraft.value.trim() !== (nameAction.value === "rename" ? actionTarget.value?.name : "");
  if (!dirtyName && !moveSelectionDirty.value) return true;
  return await confirm.value?.ask({ title: t("information.discardTitle"), body: t("information.discardBody"), confirmText: t("information.discard"), cancelText: t("information.cancel"), danger: true }) ?? false;
}
const unregister = registerStudioNavigationGuard(confirmLeave);
function beforeUnload(event: BeforeUnloadEvent) { if (busy.value || nameDialog.value && nameDraft.value.trim() || moveSelectionDirty.value) { event.preventDefault(); event.returnValue = ""; } }
window.addEventListener("beforeunload", beforeUnload);
onBeforeUnmount(() => { disposed = true; controller?.abort(); detailController?.abort(); unregister(); window.removeEventListener("beforeunload", beforeUnload); });
defineExpose({ refresh: load, confirmLeave });
</script>

<template>
  <section class="information-library" :class="{ picker, 'drag-over': dragOver }" data-testid="information-library" :aria-label="t('information.title')" @dragover="writable && $event.dataTransfer?.types.includes('Files') ? ($event.preventDefault(), dragOver = true) : undefined" @dragleave.self="dragOver = false" @drop="dropped">
    <aside class="information-tree">
      <div class="tree-heading"><strong>{{ t('information.title') }}</strong><div class="tree-actions"><button type="button" class="tree-action" :title="t('information.expandAll')" :aria-label="t('information.expandAll')" @click="expanded = entries.filter(entry => entry.kind === 'folder').map(entry => entry.id)"><el-icon><Expand /></el-icon><span>{{ t('information.expand') }}</span></button><button type="button" class="tree-action" :title="t('information.collapseAll')" :aria-label="t('information.collapseAll')" @click="expanded = []"><el-icon><Fold /></el-icon><span>{{ t('information.collapse') }}</span></button></div></div>
      <button type="button" class="tree-root" :class="{ active: !folder && !selected }" @click="openFolder()"><el-icon><Collection /></el-icon><span>{{ t('information.root') }}</span><small>{{ browsableEntries.length }}</small></button>
      <div class="tree-content" role="tree" :aria-label="t('information.title')">
        <div v-for="row in treeRows" :key="row.entry.id" class="tree-row" :class="{ active: selected === row.entry.id || !selected && folder === row.entry.id }" :style="{ paddingLeft: `${10 + row.depth * 16}px` }" role="treeitem" :aria-level="row.depth + 1" :aria-expanded="row.entry.kind === 'folder' ? expanded.includes(row.entry.id) : undefined" :aria-selected="selected === row.entry.id">
          <button v-if="row.entry.kind === 'folder'" type="button" class="tree-toggle" :aria-label="`${t(expanded.includes(row.entry.id) ? 'information.collapse' : 'information.expand')} ${row.entry.name}`" @click="toggle(row.entry)"><el-icon><ArrowDown v-if="expanded.includes(row.entry.id)" /><ArrowRight v-else /></el-icon></button><span v-else class="tree-spacer" />
          <button type="button" class="tree-name" :title="row.entry.name" @click="onTree(row.entry)"><el-icon v-if="row.entry.kind === 'shortcut'"><Collection /></el-icon><Icon v-else class="tree-file-icon" :icon="iconFor(row.entry, expanded.includes(row.entry.id))" aria-hidden="true" /><span>{{ row.entry.name }}</span></button>
        </div>
      </div>
      <div class="tree-foot"><span class="shared-dot" />{{ t('information.shared') }}</div>
    </aside>
    <div class="information-main">
      <header class="information-toolbar">
        <slot name="navigation" />
        <div class="information-search"><el-icon><Search /></el-icon><input v-model="query" type="search" :placeholder="t('information.search')" :aria-label="t('information.search')" data-testid="information-search" /><button v-if="query" type="button" class="icon-button" :title="t('information.clearSearch')" :aria-label="t('information.clearSearch')" @click="query = ''"><el-icon><Close /></el-icon></button></div>
        <button type="button" class="icon-button" :disabled="busy || loading" :title="t('information.refresh')" :aria-label="t('information.refresh')" @click="load"><el-icon><Refresh /></el-icon></button>
        <el-dropdown v-if="!picker" trigger="click" :disabled="!writable || busy || loading"><el-button type="primary" :disabled="!writable || busy || loading" data-testid="information-new"><el-icon><Plus /></el-icon>{{ t('information.new') }}<el-icon><ArrowDown /></el-icon></el-button><template #dropdown><el-dropdown-menu><el-dropdown-item data-testid="information-new-folder" @click="newFolder"><el-icon><Folder /></el-icon>{{ t('information.folder') }}</el-dropdown-item><el-dropdown-item @click="fileInput?.click()"><el-icon><Upload /></el-icon>{{ t('information.upload') }}</el-dropdown-item><el-dropdown-item @click="openEditor()"><el-icon><Collection /></el-icon>{{ t('information.createShortcut') }}</el-dropdown-item><el-dropdown-item divided @click="showImport">{{ t('information.importShortcut') }}</el-dropdown-item></el-dropdown-menu></template></el-dropdown>
        <input ref="fileInput" type="file" multiple hidden @change="picked" />
      </header>
      <div class="information-viewbar"><nav class="information-breadcrumb" :aria-label="t('information.path')"><button type="button" @click="openFolder()">{{ t('information.root') }}</button><template v-for="entry in crumbs" :key="entry.id"><el-icon><ArrowRight /></el-icon><button type="button" @click="openFolder(entry)">{{ entry.name }}</button></template></nav><div class="view-controls"><label class="flat-toggle"><input v-model="flat" type="checkbox" :disabled="!!query" />{{ t('information.flat') }}</label><div class="view-modes"><button type="button" :class="{ active: mode === 'list' }" :aria-label="t('information.list')" :aria-pressed="mode === 'list'" @click="mode = 'list'"><el-icon><List /></el-icon></button><button type="button" :class="{ active: mode === 'grid' }" :aria-label="t('information.grid')" :aria-pressed="mode === 'grid'" @click="mode = 'grid'"><el-icon><Grid /></el-icon></button></div></div></div>
      <p v-if="query" class="search-note">{{ t('information.showingSearch') }} · {{ t('information.items', { n: visible.length }) }}</p>
      <p v-if="uploadLabel" class="information-notice" role="status">{{ uploadLabel }}</p><p v-if="error" class="information-error" role="alert">{{ error }} <button type="button" @click="load">{{ t('information.retry') }}</button></p>
      <div class="information-columns">
        <div class="information-results" :class="mode" data-testid="information-results">
          <div v-if="loading && !tree" class="information-empty" role="status">{{ t('information.loading') }}</div>
          <div v-else-if="!visible.length" class="information-empty"><el-icon><Collection /></el-icon><strong>{{ t(query ? 'information.noResults' : 'information.empty') }}</strong><p>{{ t(query ? 'information.noResultsHint' : 'information.emptyHint') }}</p><button v-if="query" type="button" class="text-button" @click="query = ''">{{ t('information.clearSearch') }}</button><span v-else-if="writable">{{ t('information.uploadHint') }}</span></div>
          <article v-for="entry in visible" :key="entry.id" class="information-item" :class="{ selected: selected === entry.id }" data-testid="information-item" @dblclick="entry.kind === 'folder' ? openFolder(entry) : undefined">
            <button type="button" class="information-item-content" @click="selectItem(entry)"><span class="item-icon" :class="entry.kind"><el-icon v-if="entry.kind === 'shortcut'"><Collection /></el-icon><Icon v-else :icon="iconFor(entry)" aria-hidden="true" /></span><span class="item-copy"><strong :title="entry.name"><template v-for="(segment, index) in shortcutSearchSegments(entry.name, query)" :key="index"><mark v-if="segment.match">{{ segment.text }}</mark><template v-else>{{ segment.text }}</template></template></strong><span class="item-meta"><small>{{ t(`information.${entry.kind}`) }}<template v-if="entry.file"> · {{ bytes(entry.file.size) }}</template><template v-if="entry.kind !== 'folder'"> · {{ t('information.version', { n: entry.version }) }}</template></small><small v-if="query || flat" class="item-path" :title="pathLabel(entry.parent_id)">{{ pathLabel(entry.parent_id) }}</small></span></span><time>{{ date(entry.updated_at) }}</time></button>
            <button v-if="picker && entry.kind !== 'folder'" type="button" class="icon-button item-preview" :aria-label="`${t('information.preview')} ${entry.name}`" :title="t('information.preview')" :disabled="disabled" @click.stop="preview(entry)"><el-icon><View /></el-icon></button>
            <el-dropdown v-if="writable" trigger="click" :disabled="busy"><button type="button" class="icon-button item-menu" :disabled="busy" :aria-label="`${entry.name} ${t('information.edit')}`"><el-icon><MoreFilled /></el-icon></button><template #dropdown><el-dropdown-menu><el-dropdown-item v-if="entry.kind === 'shortcut'" @click="openEditor(entry)">{{ t('information.edit') }}</el-dropdown-item><el-dropdown-item @click="rename(entry)">{{ t('information.rename') }}</el-dropdown-item><el-dropdown-item @click="askMove(entry)">{{ t('information.move') }}</el-dropdown-item><el-dropdown-item divided @click="askDelete(entry)"><span class="danger-text">{{ t('information.remove') }}</span></el-dropdown-item></el-dropdown-menu></template></el-dropdown>
          </article>
        </div>

      </div>
      <div v-if="picker" class="information-selection" data-testid="information-selection"><div class="selection-copy"><strong>{{ selectedMeta?.name || t('information.selectHint') }}</strong><small v-if="selectedMeta">{{ pathLabel(selectedMeta.parent_id) }}</small><small v-if="detailError" class="information-error" role="alert">{{ detailError }} <button type="button" @click="loadDetail">{{ t('information.retry') }}</button></small></div><el-button v-if="selectedMeta && selectedMeta.kind !== 'folder'" :disabled="disabled" @click="preview(selectedMeta)"><el-icon><View /></el-icon>{{ t('information.preview') }}</el-button><el-button type="primary" :disabled="disabled || detailLoading || !full || busy" :loading="disabled || detailLoading" data-testid="information-reference" @click="reference">{{ t('information.select') }}</el-button></div>
    </div>
    <div v-if="dragOver && writable" class="information-drop-overlay"><el-icon><Upload /></el-icon>{{ t('information.dropFiles') }}</div>
  </section>
  <InformationEntryDialog v-if="previewTarget" :key="`${tree?.scope_id}:${previewTarget.id}:${previewTarget.version}`" v-model="previewOpen" :entry-id="previewTarget.id" :version="previewTarget.version" />
  <el-dialog v-model="nameDialog" :title="t(nameAction === 'folder' ? 'information.newFolder' : 'information.rename')" width="min(560px, calc(100vw - 32px))" append-to-body :close-on-click-modal="false" :show-close="!busy" :close-on-press-escape="!busy" :before-close="closeName"><form @submit.prevent="saveName"><label class="information-form-field"><span>{{ t('information.name') }}</span><el-input v-model="nameDraft" :disabled="busy" :placeholder="t('information.namePlaceholder')" :maxlength="nameAction === 'rename' && actionTarget?.kind === 'shortcut' ? 120 : 160" data-testid="information-name-input" /></label><p class="information-form-hint">{{ pathLabel(nameAction === 'folder' ? folder : actionTarget?.parent_id || '') }}</p></form><template #footer><p v-if="actionError" class="information-error" role="alert">{{ actionError }}</p><el-button :disabled="busy" @click="closeName">{{ t('information.cancel') }}</el-button><el-button type="primary" :loading="busy" :disabled="!nameDraft.trim() || !editableScopesCurrent" data-testid="information-name-save" @click="saveName">{{ t('information.save') }}</el-button></template></el-dialog>
  <el-dialog v-model="moveDialog" :title="t('information.moveTitle')" width="min(680px, calc(100vw - 24px))" class="information-move-dialog" data-testid="information-move-dialog" append-to-body :close-on-click-modal="false" :show-close="!busy" :close-on-press-escape="!busy" :before-close="closeMove"><div class="information-move-source"><span class="item-icon" :class="actionTarget?.kind"><el-icon v-if="actionTarget?.kind === 'shortcut'"><Collection /></el-icon><Icon v-else-if="actionTarget" :icon="iconFor(actionTarget)" aria-hidden="true" /></span><div><small>{{ t('information.movingItem') }}</small><strong>{{ actionTarget?.name }}</strong></div></div><InformationMoveTargetTree v-if="moveDialog && actionTarget" :key="`${actionScope}:${actionTarget.id}`" v-model="moveParent" :entries="entries" :moving-id="actionTarget.id" :current-parent="currentMoveParent" :disabled="busy || loading || !writable || !editableScopesCurrent" /><p v-if="!editableScopesCurrent" class="information-error" role="alert">{{ t('information.scopeChanged') }}</p><template #footer><p v-if="actionError" class="information-error" role="alert">{{ actionError }}</p><el-button :disabled="busy" @click="closeMove">{{ t('information.cancel') }}</el-button><el-button type="primary" :loading="busy" :disabled="!moveCanSubmit" data-testid="information-move-save" @click="move">{{ t('information.move') }}</el-button></template></el-dialog>
  <el-dialog v-model="deleteDialog" :title="t('information.deleteTitle')" width="min(560px, calc(100vw - 32px))" append-to-body :close-on-click-modal="false" :show-close="!busy" :close-on-press-escape="!busy"><p>{{ t('information.deleteBody', { name: actionTarget?.name }) }}</p><p v-if="descendants.length" class="information-delete-summary">{{ t('information.recursiveBody', deleteCounts) }}</p><template #footer><p v-if="actionError" class="information-error" role="alert">{{ actionError }}</p><el-button :disabled="busy" @click="deleteDialog = false">{{ t('information.cancel') }}</el-button><el-button type="danger" :loading="busy" :disabled="!editableScopesCurrent" @click="remove">{{ t('information.deleteConfirm') }}</el-button></template></el-dialog>
  <ShortcutEditorDialog v-model="editOpen" :item="editItem" :existing="editExisting" :scope-key="tree?.scope_id || ''" :reference-choices="referenceChoices" :resolve-reference="resolveReference" :skills="skills" :save="saveShortcut" :prepare-parts="prepareParts" :upload-file="uploadAsset" :pick-information="informationPicker.choose" />
  <el-dialog :model-value="informationPickerOpen" :title="t('information.pickerTitle')" width="min(1220px, calc(100vw - 28px))" class="information-picker-dialog" append-to-body destroy-on-close :close-on-click-modal="!informationPickerBusy" :close-on-press-escape="!informationPickerBusy" :show-close="!informationPickerBusy" @update:model-value="!$event && informationPicker.cancel()">
    <p class="information-picker-hint">{{ t('information.pickerHint') }}</p><p v-if="informationPickerError" class="information-error" role="alert">{{ informationPickerError }}</p>
    <InformationLibrary v-if="informationPickerOpen" picker :disabled="informationPickerBusy" @select="informationPicker.select" />
    <template #footer><el-button :disabled="informationPickerBusy" @click="informationPicker.cancel()">{{ t('information.cancel') }}</el-button></template>
  </el-dialog>
  <el-dialog v-model="importOpen" :title="t('information.importTitle')" width="min(820px, calc(100vw - 32px))" class="information-import-dialog" append-to-body :close-on-click-modal="false" :show-close="!busy" :close-on-press-escape="!busy"><p class="information-form-hint">{{ t('information.importHint') }}</p><label class="information-form-field"><span>{{ t('information.sourceAgent') }}</span><el-select v-model="sourceAgent" filterable :disabled="busy" @change="loadSource"><el-option value="" :label="t('information.accountShortcuts')" /><el-option v-for="agent in sourceAgents" :key="agent.id" :value="agent.id" :label="agent.name" /></el-select></label><p v-if="sourceLoading" role="status">{{ t('information.loadingSources') }}</p><p v-if="sourceError" class="information-error" role="alert">{{ sourceError }}</p><div v-else-if="!sourceLoading" class="information-import-content"><div class="information-source-list"><button v-for="item in sourceItems" :key="item.id" type="button" :class="{ selected: sourceID === item.id }" @click="sourceID = item.id"><strong>{{ item.title }}</strong><small>{{ item.content }}</small></button><p v-if="!sourceItems.length">{{ t('information.noShortcuts') }}</p></div><div class="information-source-preview"><ShortcutDetails v-if="selectedSource" :title="selectedSource.title" :content="selectedSource.content" :parts="selectedSource.parts" :language="selectedSource.language" :icon="selectedSource.icon" /><p v-else>{{ t('information.chooseShortcut') }}</p></div></div><template #footer><p v-if="actionError" class="information-error" role="alert">{{ actionError }}</p><el-button :disabled="busy" @click="importOpen = false">{{ t('information.cancel') }}</el-button><el-button type="primary" :loading="busy" :disabled="!sourceID || sourceLoading || !editableScopesCurrent" @click="importSelected">{{ t('information.import') }}</el-button></template></el-dialog>
  <ConfirmDialog ref="confirm" />
</template>

<style scoped>
.information-move-source{display:flex;align-items:center;gap:12px;padding:0 0 18px;margin-bottom:18px;border-bottom:1px solid var(--el-border-color-lighter)}.information-move-source>div{display:flex;flex-direction:column;gap:6px;min-width:0}.information-move-source small{font-size:11px;color:var(--el-text-color-secondary)}.information-move-source strong{font-size:14px;font-weight:550;overflow-wrap:anywhere}
.information-library{display:flex;position:relative;min-height:420px;height:calc(100dvh - 24px);overflow:hidden;border:1px solid var(--el-border-color-light);border-radius:14px;background:var(--el-bg-color);color:var(--el-text-color-primary)}.information-tree{display:flex;flex-direction:column;flex:0 0 244px;min-width:0;background:var(--el-fill-color-lighter);border-right:1px solid var(--el-border-color-lighter)}.tree-heading{display:flex;align-items:center;justify-content:space-between;padding:16px 12px 12px;font-size:13px}.tree-heading>div{display:flex;gap:2px}.tree-action{display:inline-flex;align-items:center;justify-content:center;gap:3px;height:28px;padding:0 6px;border:0;border-radius:6px;background:transparent;color:var(--el-text-color-secondary);font:inherit;font-size:11px;white-space:nowrap;cursor:pointer}.tree-action:hover{background:var(--el-fill-color);color:var(--el-color-primary)}.tree-action>.el-icon{font-size:14px}.icon-button{display:inline-grid;place-items:center;width:29px;height:29px;border:0;border-radius:7px;background:transparent;color:var(--el-text-color-secondary);cursor:pointer;font-size:16px}.icon-button:hover:not(:disabled){background:var(--el-fill-color);color:var(--el-color-primary)}button:disabled{opacity:.5;cursor:not-allowed}button:focus-visible,input:focus-visible{outline:2px solid var(--el-color-primary);outline-offset:2px}.tree-root{display:flex;gap:8px;align-items:center;margin:0 8px 7px;padding:9px 10px;border:0;border-radius:7px;background:transparent;color:var(--el-text-color-regular);font:inherit;font-size:12px;text-align:left;cursor:pointer}.tree-root small{margin-left:auto;color:var(--el-text-color-placeholder)}.tree-root.active,.tree-row.active{background:var(--el-color-primary-light-9);color:var(--el-color-primary)}.tree-content{flex:1;overflow:auto;min-height:0;padding:0 7px 14px}.tree-row{display:flex;align-items:center;min-height:34px;border-radius:6px;padding-right:6px}.tree-row:hover{background:var(--el-fill-color)}.tree-toggle,.tree-spacer{display:inline-grid;place-items:center;width:17px;flex:0 0 17px;padding:0;border:0;background:transparent;color:inherit;font-size:10px;cursor:pointer}.tree-name{display:flex;gap:6px;align-items:center;min-width:0;flex:1;padding:7px 0;border:0;background:transparent;color:inherit;font:inherit;font-size:12px;text-align:left;cursor:pointer}.tree-name>.el-icon,.tree-file-icon{flex:0 0 17px;width:17px;height:17px;font-size:16px}.tree-name>span{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.tree-foot{display:flex;align-items:center;gap:6px;padding:13px 15px;border-top:1px solid var(--el-border-color-lighter);font-size:11px;color:var(--el-text-color-secondary)}.shared-dot{width:6px;height:6px;border-radius:50%;background:var(--el-color-success)}.information-main{display:flex;flex-direction:column;flex:1;min-width:0}.information-toolbar{display:flex;align-items:center;gap:10px;padding:17px 20px 12px}.information-search{display:flex;align-items:center;gap:8px;flex:1;min-width:100px;padding:9px 11px;border:1px solid var(--el-border-color-light);border-radius:9px;color:var(--el-text-color-placeholder);background:var(--el-fill-color-lighter)}.information-search:focus-within{border-color:var(--el-color-primary)}.information-search>input{flex:1;min-width:0;border:0;background:transparent;outline:0;color:var(--el-text-color-primary);font:inherit;font-size:13px}.information-search>.icon-button{width:20px;height:20px}.information-toolbar .el-button{gap:6px}.information-viewbar{display:flex;align-items:center;gap:14px;justify-content:space-between;padding:0 20px 13px;border-bottom:1px solid var(--el-border-color-lighter)}.information-breadcrumb{display:flex;gap:5px;align-items:center;min-width:0;overflow:auto;white-space:nowrap}.information-breadcrumb button{padding:2px 0;border:0;background:transparent;color:var(--el-text-color-secondary);font:inherit;font-size:12px;cursor:pointer;max-width:180px;overflow:hidden;text-overflow:ellipsis}.information-breadcrumb button:last-child{color:var(--el-text-color-primary)}.information-breadcrumb>.el-icon{font-size:10px;flex:0 0 10px;color:var(--el-text-color-placeholder)}.view-controls{display:flex;align-items:center;gap:12px;flex:0 0 auto}.flat-toggle{display:flex;align-items:center;gap:5px;font-size:11px;color:var(--el-text-color-secondary);white-space:nowrap;cursor:pointer}.flat-toggle input{accent-color:var(--el-color-primary)}.view-modes{display:flex;padding:2px;border:1px solid var(--el-border-color-light);border-radius:7px}.view-modes button{display:grid;place-items:center;width:27px;height:25px;border:0;border-radius:4px;background:transparent;color:var(--el-text-color-secondary);cursor:pointer}.view-modes button.active{background:var(--el-fill-color);color:var(--el-color-primary)}.information-columns{display:flex;flex:1;min-height:0}.information-results{flex:1;min-width:0;overflow:auto;padding:10px 12px;align-content:start}.information-results.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(200px,1fr));grid-auto-rows:168px;gap:12px}.information-item{display:flex;align-items:center;position:relative;border:1px solid transparent;border-radius:9px;min-width:0}.information-item:hover{background:var(--el-fill-color-lighter)}.information-item.selected{border-color:var(--el-color-primary-light-5);background:var(--el-color-primary-light-9)}.information-item-content{display:flex;align-items:center;gap:11px;width:100%;min-width:0;padding:12px 37px 12px 10px;border:0;background:transparent;color:inherit;font:inherit;text-align:left;cursor:pointer}.item-icon{display:inline-grid;place-items:center;flex:0 0 34px;width:34px;height:36px;border-radius:8px;font-size:21px;background:var(--el-fill-color-light);color:var(--el-text-color-secondary)}.item-icon.folder{background:color-mix(in srgb,#ffca28 10%,transparent)}.item-icon>.iconify{width:25px;height:25px}.item-icon.shortcut{color:var(--el-color-primary);background:var(--el-color-primary-light-9)}.item-copy{display:flex;flex:1;flex-direction:column;gap:5px;min-width:0}.item-meta{display:flex;align-items:center;gap:8px;min-width:0}.item-meta>small{flex:0 1 auto;min-width:0}.item-meta>.item-path{flex:1;text-align:right}.item-copy strong{font-size:13px;font-weight:550;overflow:hidden;white-space:nowrap;text-overflow:ellipsis}.item-copy small{font-size:10px;color:var(--el-text-color-secondary);overflow:hidden;white-space:nowrap;text-overflow:ellipsis}.item-copy .item-path{color:var(--el-text-color-placeholder)}.information-item time{font-size:10px;color:var(--el-text-color-placeholder);white-space:nowrap}.item-menu{position:absolute;right:5px;top:14px;opacity:0}.information-item:hover .item-menu,.information-item:focus-within .item-menu{opacity:1}.information-results.grid .information-item{display:block;height:168px;box-sizing:border-box;overflow:hidden;border-color:var(--el-border-color-lighter)}.grid .information-item-content{flex-direction:column;align-items:flex-start;gap:12px;padding:16px;height:100%;box-sizing:border-box;overflow:hidden}.grid .item-copy{width:100%;flex:1;justify-content:flex-end;overflow:hidden}.grid .information-item time{display:none}.grid .item-copy strong{line-height:20px;height:40px;flex:0 0 40px;overflow-wrap:anywhere;white-space:normal;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical}.grid .item-meta{height:15px;flex:0 0 15px}.grid .item-meta>small{font-size:10px;line-height:15px}.grid .item-menu{top:7px}.information-empty{display:flex;align-items:center;justify-content:center;flex-direction:column;gap:10px;padding:50px 22px;text-align:center;color:var(--el-text-color-secondary);font-size:13px;line-height:1.7;grid-column:1/-1}.information-empty>.el-icon{font-size:35px;color:var(--el-text-color-placeholder);margin-bottom:6px}.information-empty>strong{color:var(--el-text-color-primary);font-weight:550}.information-empty p{margin:0;max-width:290px;font-size:12px}.information-empty>span{font-size:11px;color:var(--el-text-color-placeholder);max-width:280px}.information-notice,.information-error,.search-note{margin:8px 20px;font-size:12px;line-height:1.7;overflow-wrap:anywhere}.information-notice{color:var(--el-color-primary)}.information-error{color:var(--el-color-danger)}.information-error>button,.text-button{border:0;background:transparent;color:var(--el-color-primary);font:inherit;cursor:pointer}.search-note{color:var(--el-text-color-placeholder);font-size:11px}.information-drop-overlay{display:flex;position:absolute;inset:0;z-index:30;align-items:center;justify-content:center;flex-direction:column;gap:15px;border:2px dashed var(--el-color-primary);border-radius:14px;background:color-mix(in srgb,var(--el-bg-color) 92%,transparent);color:var(--el-color-primary);font-size:15px;pointer-events:none}.information-drop-overlay>.el-icon{font-size:35px}.danger-text{color:var(--el-color-danger)}mark{background:color-mix(in srgb,var(--el-color-primary) 20%,transparent);color:inherit;padding:0 1px;border-radius:2px}.information-form-field{display:flex;flex-direction:column;gap:10px;font-size:13px}.information-form-hint{color:var(--el-text-color-secondary);font-size:12px;line-height:1.8;overflow-wrap:anywhere}.information-delete-summary{padding:12px 14px;background:var(--el-color-danger-light-9);border-radius:8px;font-size:12px;line-height:1.8;color:var(--el-color-danger)}.information-import-content{display:grid;grid-template-columns:250px minmax(0,1fr);gap:18px;min-height:300px;max-height:50dvh;margin-top:18px}.information-source-list{overflow:auto;border-right:1px solid var(--el-border-color-lighter);padding-right:12px}.information-source-list button{display:flex;flex-direction:column;gap:7px;text-align:left;width:100%;padding:12px;border:1px solid transparent;border-radius:8px;background:transparent;color:var(--el-text-color-primary);cursor:pointer}.information-source-list button.selected{background:var(--el-color-primary-light-9);border-color:var(--el-color-primary-light-5)}.information-source-list strong{font-size:13px}.information-source-list small{font-size:11px;color:var(--el-text-color-secondary);display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden;word-break:break-all;white-space:pre-wrap}.information-source-list p,.information-source-preview>p{font-size:12px;color:var(--el-text-color-secondary)}.information-source-preview{overflow:auto;min-width:0}.picker{height:65dvh;max-height:800px;min-height:420px}.picker .information-tree{flex-basis:244px}.picker .information-toolbar{padding-top:12px}
.information-selection{display:flex;align-items:center;gap:10px;flex:0 0 auto;min-height:62px;padding:12px 18px;box-sizing:border-box;border-top:1px solid var(--el-border-color-lighter);background:var(--el-bg-color)}.selection-copy{display:flex;flex:1;flex-direction:column;gap:4px;min-width:0}.selection-copy strong{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:12px;font-weight:550}.selection-copy small{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--el-text-color-placeholder);font-size:10px}.selection-copy .information-error{margin:0;color:var(--el-color-danger)}.information-selection>.el-button{margin:0;flex:none;font-size:12px;gap:4px}.item-preview{position:absolute;right:5px;top:8px;opacity:0}.information-item:hover .item-preview,.information-item:focus-within .item-preview{opacity:1}.picker .information-item-content{padding-right:42px}.picker .grid .information-item-content{padding-right:16px}
@media(max-width:1200px){.information-item time{display:none}}@media(max-width:850px){.information-tree{flex-basis:224px}.tree-heading{padding-inline:10px}.tree-action{padding-inline:4px}.information-toolbar{padding:12px}.information-viewbar{padding:0 12px 10px}.flat-toggle{font-size:10px}.information-results.grid{grid-template-columns:repeat(auto-fill,minmax(180px,1fr))}.picker .information-tree{flex-basis:224px}}@media(max-width:650px){.information-library{min-height:420px}.information-tree{display:none}.information-toolbar{gap:5px}.information-viewbar{flex-wrap:wrap;gap:9px}.view-controls{margin-left:auto}.information-results.grid{grid-template-columns:repeat(2,minmax(0,1fr));grid-auto-rows:168px;gap:8px}.grid .information-item-content{padding:12px}.information-import-content{grid-template-columns:1fr;max-height:65dvh}.information-source-list{max-height:160px;border-right:0;border-bottom:1px solid var(--el-border-color-lighter);padding:0 0 10px}.information-source-preview{max-height:230px}.picker{height:65dvh;min-height:360px}.item-menu,.item-preview{opacity:1}.information-selection{gap:7px;padding:10px}.information-selection .el-button{padding-inline:10px}.information-selection .el-button>.el-icon{display:none}.selection-copy small{display:none}}
/* Anchor dropdowns to the item, not the zero-width inline trigger wrapper. */
.information-item > :deep(.el-dropdown){position:absolute;right:6px;top:8px;z-index:1}
.information-item .item-menu{position:static}
</style>
<style>
.information-move-dialog{margin-top:5dvh;max-height:90dvh;display:flex;flex-direction:column}.information-move-dialog .el-dialog__header,.information-move-dialog .el-dialog__footer{flex:none}.information-move-dialog .el-dialog__body{min-height:0;overflow:auto}.information-move-dialog .el-dialog__footer{padding-top:12px}
</style>
